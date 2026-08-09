package github

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/specsnl/labelsync/internal/github/ratelimit"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// recordingClock is the limiter's clock under test: it records the waits and
// returns immediately, so a rate-limit suite costs no wall-clock time.
type recordingClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *recordingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *recordingClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)

	return nil
}

func (c *recordingClock) slept() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.waits)
}

// secondaryLimit answers a request with the shape go-github needs in order to
// produce a typed *AbuseRateLimitError.
func secondaryLimit(w http.ResponseWriter, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}

	w.WriteHeader(http.StatusForbidden)
	write(w, secondaryLimitBody)
}

// TestDoWaitsOutARateLimitAndRetries is the reactive half seen from the outside:
// a rate limit is not an outcome, it is the API asking for the same request
// later, so the caller never sees one.
func TestDoWaitsOutARateLimitAndRetries(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		attempt := requests
		mu.Unlock()

		if attempt == 1 {
			secondaryLimit(w, "30")

			return
		}

		write(w, `[{"name":"bug","color":"d73a4a"}]`)
	})

	clock := &recordingClock{now: time.Unix(1_770_000_000, 0)}
	limiter := ratelimit.New(
		ratelimit.WithClock(clock),
		ratelimit.WithMaxWait(time.Hour),
		ratelimit.WithJitter(func(d time.Duration) time.Duration { return d }),
	)

	client, _ := newTestClient(t, handler, WithLimiter(limiter))

	labels, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
	if err != nil {
		t.Fatalf("ListLabels() error = %v, want nil: a rate limit is waited out, not returned", err)
	}

	if len(labels) != 1 || labels[0].Name != "bug" {
		t.Errorf("labels = %+v, want the ones the retry read", labels)
	}

	if clock.slept() == 0 {
		t.Error("the limit was not waited out at all")
	}

	// A repository that was rate-limited is not a repository that could not be
	// reached, and listing it as skipped would be a lie about a repository that
	// synced.
	if got := client.Failures().Len(); got != 0 {
		t.Errorf("Failures().Len() = %d, want 0", got)
	}
}

// TestDoStopsAtMaxWait is what keeps a CI job from idling for an hour. The run
// ends with a reason a reader can act on — raise the ceiling, or run it when the
// window has turned over.
func TestDoStopsAtMaxWait(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondaryLimit(w, "")
	})

	clock := &recordingClock{now: time.Unix(1_770_000_000, 0)}
	limiter := ratelimit.New(
		ratelimit.WithClock(clock),
		ratelimit.WithMaxWait(30*time.Second), // Less than the minimum backoff.
		ratelimit.WithJitter(func(d time.Duration) time.Duration { return d }),
	)

	client, _ := newTestClient(t, handler, WithLimiter(limiter))

	_, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
	if !errors.Is(err, labelsync.ErrMaxWaitExceeded) {
		t.Fatalf("error = %v, want one wrapping ErrMaxWaitExceeded", err)
	}

	if labelsync.KindOf(err) != "max_wait_exceeded" {
		t.Errorf("KindOf(err) = %q, want max_wait_exceeded", labelsync.KindOf(err))
	}

	// Refused rather than taken: a run that idles and then fails has wasted both
	// the time and the reason.
	if got := clock.slept(); got != 0 {
		t.Errorf("waits = %d, want none taken", got)
	}
}

// TestWritesArePacedByTheirMethod covers where the write/read distinction is
// made. A call site can label itself wrong; a POST cannot, which is why the
// bucket lives in the transport.
func TestWritesArePacedByTheirMethod(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			write(w, `{"name":"bug"}`)

			return
		}

		write(w, `[]`)
	})

	clock := &recordingClock{now: time.Unix(1_770_000_000, 0)}
	limiter := ratelimit.New(
		ratelimit.WithClock(clock),
		// One a minute, so the bucket holds exactly one and the second write has
		// to wait for a token.
		ratelimit.WithWriteRate(1),
		ratelimit.WithJitter(func(d time.Duration) time.Duration { return d }),
	)

	client, _ := newTestClient(t, handler, WithLimiter(limiter))

	// Reads first, and as many as we like: they are not what the bucket is for.
	for range 3 {
		if _, err := client.ListLabels(t.Context(), "specsnl", "labelsync"); err != nil {
			t.Fatalf("ListLabels() error = %v, want nil", err)
		}
	}

	if got := clock.slept(); got != 0 {
		t.Fatalf("reads were paced %d times, want never", got)
	}

	label := Label{Name: "bug", Color: "d73a4a"}
	for range 2 {
		if err := client.CreateLabel(t.Context(), "specsnl", "labelsync", label); err != nil {
			t.Fatalf("CreateLabel() error = %v, want nil", err)
		}
	}

	if got := clock.slept(); got != 1 {
		t.Errorf("writes were paced %d times, want exactly one: the bucket starts full", got)
	}
}

// TestRateLimitPrimesTheLimiter covers the free startup call. GET /rate_limit
// does not count against the limit, which is what makes it worth making before
// anything is spent.
func TestRateLimitPrimesTheLimiter(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		write(w, `{"resources":{"core":{"limit":5000,"remaining":4321,"reset":1770000600}}}`)
	})

	limiter := ratelimit.New(ratelimit.WithClock(&recordingClock{now: time.Unix(1_770_000_000, 0)}))

	client, _ := newTestClient(t, handler, WithLimiter(limiter))

	core, err := client.RateLimit(t.Context())
	if err != nil {
		t.Fatalf("RateLimit() error = %v, want nil", err)
	}

	if core.Remaining != 4321 {
		t.Errorf("remaining = %d, want 4321", core.Remaining)
	}

	remaining, reset, valid := limiter.Remaining()
	if !valid || remaining != 4321 || reset.Unix() != 1_770_000_600 {
		t.Errorf("the limiter was primed with %d / %s (valid %t), want 4321 and the reported reset",
			remaining, reset, valid)
	}

	// And the budget question an apply asks is answered from it.
	if limiter.Affordable(4322) {
		t.Error("Affordable(4322) = true with 4321 remaining, want false")
	}
}
