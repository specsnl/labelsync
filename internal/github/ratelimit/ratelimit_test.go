package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// epoch is where every fake clock here starts. A fixed instant rather than
// time.Now, so a failure reads the same on every machine and at every hour.
var epoch = time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)

// fakeClock records what it was asked to wait for and advances instead of
// waiting. Advancing matters: a token bucket refills over time, and a clock that
// stood still would make the second write wait exactly as long as the first.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newClock() *fakeClock { return &fakeClock{now: epoch} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)

	return nil
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.waits...)
}

// noJitter is the identity, which is what makes the doubling assertable rather
// than merely plausible.
func noJitter(d time.Duration) time.Duration { return d }

// headers renders the two rate-limit headers a response carries.
func headers(remaining int, reset time.Time) http.Header {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))

	return h
}

// TestBucketPacesWrites is the proactive half: a run that never trips the
// secondary limit is one that never has to wait a minute out.
func TestBucketPacesWrites(t *testing.T) {
	clock := newClock()

	// Two a minute, so the bucket starts with two and the third write has to
	// wait half a minute for the next token.
	l := New(WithClock(clock), WithWriteRate(2), WithJitter(noJitter))

	for range 3 {
		if err := l.Await(t.Context(), true); err != nil {
			t.Fatalf("Await() error = %v, want nil", err)
		}
	}

	waits := clock.slept()
	if len(waits) != 1 {
		t.Fatalf("waits = %v, want exactly one: the bucket starts full", waits)
	}

	if waits[0] != 30*time.Second {
		t.Errorf("wait = %s, want 30s at two writes a minute", waits[0])
	}
}

// TestBucketDoesNotPaceReads pins what the bucket is for. Reads are cheap —
// roughly 51 requests for 50 repositories against 5,000 an hour — and pacing
// them would slow every dry run for nothing.
func TestBucketDoesNotPaceReads(t *testing.T) {
	clock := newClock()
	l := New(WithClock(clock), WithWriteRate(1), WithJitter(noJitter))

	for range 10 {
		if err := l.Await(t.Context(), false); err != nil {
			t.Fatalf("Await() error = %v, want nil", err)
		}
	}

	if waits := clock.slept(); len(waits) != 0 {
		t.Errorf("reads waited %v, want no pacing at all", waits)
	}
}

// TestWaitsForTheResetBeforeSpendingTheLastOfTheBudget is the header-tracking
// half: stopping before the budget runs out beats racing into a 403 and finding
// out afterwards.
func TestWaitsForTheResetBeforeSpendingTheLastOfTheBudget(t *testing.T) {
	clock := newClock()
	l := New(WithClock(clock), WithThreshold(10), WithMaxWait(time.Hour), WithJitter(noJitter))

	reset := epoch.Add(5 * time.Minute)

	// Above the threshold: nothing to do.
	l.Observe(headers(11, reset))

	if err := l.Await(t.Context(), false); err != nil {
		t.Fatalf("Await() error = %v, want nil", err)
	}

	if waits := clock.slept(); len(waits) != 0 {
		t.Fatalf("waited %v with budget to spare, want no wait", waits)
	}

	// At the threshold: wait for the window to turn over.
	l.Observe(headers(10, reset))

	if err := l.Await(t.Context(), false); err != nil {
		t.Fatalf("Await() error = %v, want nil", err)
	}

	waits := clock.slept()
	if len(waits) != 1 || waits[0] != 5*time.Minute {
		t.Errorf("waits = %v, want one wait of 5m — until the reset", waits)
	}
}

// TestAnUnreadBudgetNeverWaits covers the first request of every run: nothing
// has been observed yet, and a zero remaining is indistinguishable from no
// reading at all. Stalling there would stall every run.
func TestAnUnreadBudgetNeverWaits(t *testing.T) {
	clock := newClock()
	l := New(WithClock(clock), WithThreshold(10), WithJitter(noJitter))

	if err := l.Await(t.Context(), false); err != nil {
		t.Fatalf("Await() error = %v, want nil", err)
	}

	if waits := clock.slept(); len(waits) != 0 {
		t.Errorf("waited %v before any response had been seen, want none", waits)
	}

	// A malformed header is the same situation: not a reading.
	bad := http.Header{}
	bad.Set("X-RateLimit-Remaining", "soon")
	bad.Set("X-RateLimit-Reset", "later")

	l.Observe(bad)

	if _, _, valid := l.Remaining(); valid {
		t.Error("a malformed header was taken as a reading")
	}
}

// TestRecover covers the reactive half, which is the whole reason for choosing a
// client library with typed rate-limit errors.
func TestRecover(t *testing.T) {
	retryAfter := 90 * time.Second

	tests := map[string]struct {
		err       error
		wantRetry bool
		wantWait  time.Duration
	}{
		"a primary limit sleeps until the reset": {
			err: &gogithub.RateLimitError{
				Rate: gogithub.Rate{Reset: gogithub.Timestamp{Time: epoch.Add(4 * time.Minute)}},
			},
			wantRetry: true,
			wantWait:  4 * time.Minute,
		},
		"a secondary limit honours Retry-After": {
			err:       &gogithub.AbuseRateLimitError{RetryAfter: &retryAfter},
			wantRetry: true,
			wantWait:  retryAfter,
		},
		"a secondary limit without one backs off from a minute": {
			err:       &gogithub.AbuseRateLimitError{},
			wantRetry: true,
			wantWait:  time.Minute,
		},
		"anything else is not ours": {
			err:       errors.New("the network went away"),
			wantRetry: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			l := New(WithClock(clock), WithMaxWait(time.Hour), WithJitter(noJitter))

			retry, err := l.Recover(t.Context(), tc.err)
			if err != nil {
				t.Fatalf("Recover() error = %v, want nil", err)
			}

			if retry != tc.wantRetry {
				t.Errorf("retry = %t, want %t", retry, tc.wantRetry)
			}

			waits := clock.slept()

			if tc.wantWait == 0 {
				if len(waits) != 0 {
					t.Errorf("waited %v for an error that is not a rate limit", waits)
				}

				return
			}

			if len(waits) != 1 || waits[0] != tc.wantWait {
				t.Errorf("waits = %v, want [%s]", waits, tc.wantWait)
			}
		})
	}
}

// TestSecondaryBackoffDoubles covers the escalation. A secondary limit that
// arrives without Retry-After says nothing about how long to wait, so waiting
// the same minute repeatedly is how a run hammers a limit it has already hit.
func TestSecondaryBackoffDoubles(t *testing.T) {
	clock := newClock()
	l := New(WithClock(clock), WithMaxWait(time.Hour), WithJitter(noJitter))

	for range 3 {
		if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{}); err != nil {
			t.Fatalf("Recover() error = %v, want nil", err)
		}
	}

	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}
	if got := clock.slept(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("waits = %v, want %v", got, want)
	}

	// A limit that says how long to wait is not the escalating case, and the
	// next one without a Retry-After starts from a minute again.
	retryAfter := 10 * time.Second
	if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{RetryAfter: &retryAfter}); err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}

	if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{}); err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}

	got := clock.slept()
	if last := got[len(got)-1]; last != time.Minute {
		t.Errorf("backoff after an explicit Retry-After = %s, want it reset to 1m", last)
	}
}

// TestMaxWaitIsRefusedRatherThanTaken is what stops a CI job idling for an hour.
// The failure has to arrive *instead of* the wait: idling and then failing wastes
// both the time and the reason.
func TestMaxWaitIsRefusedRatherThanTaken(t *testing.T) {
	clock := newClock()
	l := New(WithClock(clock), WithMaxWait(90*time.Second), WithJitter(noJitter))

	// A minute fits.
	if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{}); err != nil {
		t.Fatalf("first Recover() error = %v, want nil", err)
	}

	// Two more do not: the doubled backoff would take the run past the ceiling.
	retry, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{})
	if !errors.Is(err, labelsync.ErrMaxWaitExceeded) {
		t.Fatalf("error = %v, want one wrapping ErrMaxWaitExceeded", err)
	}

	if retry {
		t.Error("retry = true, want false: the run is over")
	}

	if labelsync.KindOf(err) != "max_wait_exceeded" {
		t.Errorf("KindOf(err) = %q, want max_wait_exceeded", labelsync.KindOf(err))
	}

	if got := clock.slept(); len(got) != 1 {
		t.Errorf("waits = %v, want only the one that fitted: the refused wait must not be taken", got)
	}

	// The message says what was asked for and how much had gone, because the
	// answer to "raise --max-wait to what?" has to be in it.
	for _, want := range []string{"2m0s", "1m30s", "1m0s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestAffordable covers the startup question an apply asks: is there enough
// budget left to be worth starting?
func TestAffordable(t *testing.T) {
	l := New(WithClock(newClock()), WithJitter(noJitter))

	// Nothing has been read yet. Refusing on no information would stop a run
	// that would have succeeded.
	if !l.Affordable(1_000_000) {
		t.Error("Affordable() = false on an unknown budget, want true")
	}

	l.Prime(120, epoch.Add(time.Hour))

	if !l.Affordable(120) {
		t.Error("Affordable(120) = false with 120 remaining, want true")
	}

	if l.Affordable(121) {
		t.Error("Affordable(121) = true with 120 remaining, want false")
	}

	remaining, reset, valid := l.Remaining()
	if !valid || remaining != 120 || !reset.Equal(epoch.Add(time.Hour)) {
		t.Errorf("Remaining() = %d, %s, %t, want 120 and the primed reset", remaining, reset, valid)
	}
}

// TestWaitedAccumulates covers the running total the ceiling is measured
// against, and which a summary reports.
func TestWaitedAccumulates(t *testing.T) {
	clock := newClock()
	l := New(WithClock(clock), WithMaxWait(time.Hour), WithJitter(noJitter))

	retryAfter := 30 * time.Second
	for range 2 {
		if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{RetryAfter: &retryAfter}); err != nil {
			t.Fatalf("Recover() error = %v, want nil", err)
		}
	}

	if got := l.Waited(); got != time.Minute {
		t.Errorf("Waited() = %s, want 1m", got)
	}
}
