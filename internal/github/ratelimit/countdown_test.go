package ratelimit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/specsnl/labelsync/internal/github/ratelimit"
	"github.com/specsnl/labelsync/internal/util/output"
)

var update = flag.Bool("update", false, "rewrite the .golden files from the current output")

// epoch is where every fake clock here starts. A fixed instant, so a failure
// reads the same on every machine and at every hour — and so that "resume_at"
// is a value a golden file can hold.
var epoch = time.Date(2026, time.July, 31, 14, 17, 38, 0, time.UTC)

// advancingClock advances instead of waiting, which is what lets a four-minute
// countdown render two hundred and seventy-two ticks in no time at all.
type advancingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *advancingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *advancingClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)

	return nil
}

// buffer is the stream every rendering draws into. A bytes.Buffer is not a
// terminal, which is exactly why the TTY rendering is constructed directly here
// rather than through NewReporter: what the constructor *chooses* is asserted
// separately, in TestNewReporterPicksTheRendering.
type buffer struct{ bytes.Buffer }

// unknownWrites is what a dry run's limiter knows about the job: nothing.
// ExpectWrites is simply never called.
const unknownWrites = -1

// waitFor runs a limiter through one secondary-limit wait of d, with the
// reporter installed. It returns nothing: the reporter's own stream is what a
// test asserts on.
func waitFor(t *testing.T, reporter ratelimit.Reporter, d time.Duration, writes int) {
	t.Helper()

	clock := &advancingClock{now: epoch}

	l := ratelimit.New(
		ratelimit.WithClock(clock),
		ratelimit.WithMaxWait(time.Hour),
		ratelimit.WithJitter(func(x time.Duration) time.Duration { return x }),
		ratelimit.WithReporter(reporter),
	)

	if writes != unknownWrites {
		l.ExpectWrites(writes)
	}

	retryAfter := d

	if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{RetryAfter: &retryAfter}); err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}
}

// TestCountdownRenderings is the whole issue in one table: three renderings, one
// per context, each pinned to a golden file. Getting this wrong is how CLIs
// become unusable in CI, so the goldens are the contract rather than a
// convenience.
func TestCountdownRenderings(t *testing.T) {
	const wait = 272 * time.Second // 04:32, the duration the design sketches

	tests := map[string]struct {
		golden string
		writes int
		wait   time.Duration
		build  func(*buffer) ratelimit.Reporter
	}{
		// A terminal redraws every second, so this one is deliberately short: a
		// golden file holding two hundred and seventy-two redraws would pin the
		// arithmetic and hide the rendering.
		"tty pretty": {
			golden: "countdown_tty",
			writes: 143,
			wait:   5 * time.Second,
			build:  func(b *buffer) ratelimit.Reporter { return ratelimit.NewLiveReporter(b) },
		},
		"non-tty pretty": {
			golden: "countdown_log",
			writes: 143,
			wait:   wait,
			build: func(b *buffer) ratelimit.Reporter {
				return ratelimit.NewLoggedReporter(output.NewPrettyWriter(nil, b, []string{}))
			},
		},
		"json": {
			golden: "countdown_json",
			writes: 143,
			wait:   wait,
			build: func(b *buffer) ratelimit.Reporter {
				return ratelimit.NewLoggedReporter(output.NewJSONWriter(nil, b))
			},
		},
		"a run with no write count says nothing about one": {
			golden: "countdown_log_unknown_writes",
			writes: unknownWrites,
			wait:   wait,
			build: func(b *buffer) ratelimit.Reporter {
				return ratelimit.NewLoggedReporter(output.NewPrettyWriter(nil, b, []string{}))
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b buffer

			waitFor(t, tc.build(&b), tc.wait, tc.writes)

			assertGolden(t, tc.golden, b.String())
		})
	}
}

// TestTheLoggedRenderingsCarryNoControlCharacters is the one that keeps a CI log
// readable. A carriage return in a log file turns fifteen minutes of countdown
// into one unreadable line, and in a JSON stream it is worse than unreadable.
func TestTheLoggedRenderingsCarryNoControlCharacters(t *testing.T) {
	writers := map[string]func(*buffer) output.Writer{
		"pretty": func(b *buffer) output.Writer { return output.NewPrettyWriter(nil, b, []string{}) },
		"json":   func(b *buffer) output.Writer { return output.NewJSONWriter(nil, b) },
	}

	for name, build := range writers {
		t.Run(name, func(t *testing.T) {
			var b buffer

			waitFor(t, ratelimit.NewLoggedReporter(build(&b)), 20*time.Minute, 143)

			for _, r := range b.String() {
				// A newline is the line separator both renderings are built on.
				// Everything else below space is a control character.
				if r != '\n' && r < 0x20 {
					t.Fatalf("the %s rendering emitted control character %q:\n%q", name, r, b.String())
				}
			}
		})
	}
}

// TestTheJSONEventIsTyped covers the half of the contract a golden file cannot:
// a consumer filters on fields, and "seconds" has to be a number it can compare
// rather than prose it would have to parse.
func TestTheJSONEventIsTyped(t *testing.T) {
	var b buffer

	waitFor(t, ratelimit.NewLoggedReporter(output.NewJSONWriter(nil, &b)), 272*time.Second, 143)

	var first map[string]any

	line, _, _ := strings.Cut(strings.TrimSpace(b.String()), "\n")
	if err := json.Unmarshal([]byte(line), &first); err != nil {
		t.Fatalf("line %q is not a JSON object: %v", line, err)
	}

	for field, want := range map[string]any{
		"level":            "warn",
		"event":            "rate_limit_wait",
		"kind":             "secondary",
		"seconds":          272.0,
		"resume_at":        "2026-07-31T14:22:10Z",
		"writes_remaining": 143.0,
	} {
		if got := first[field]; got != want {
			t.Errorf("%s = %v (%T), want %v (%T)", field, got, got, want, want)
		}
	}
}

// TestTheLiveRenderingClearsItsLine is what stops the countdown from leaving a
// stale "resuming in 00:01" on the terminal for the rest of the run, and what
// stops a shortening line from leaving its last character behind.
func TestTheLiveRenderingClearsItsLine(t *testing.T) {
	var b buffer

	waitFor(t, ratelimit.NewLiveReporter(&b), 3*time.Second, 143)

	out := b.String()

	if !strings.HasSuffix(out, "\r") {
		t.Errorf("the live rendering does not end by returning to column zero:\n%q", out)
	}

	last := out[strings.LastIndex(out[:len(out)-1], "\r"):]
	if strings.TrimSpace(last) != "" {
		t.Errorf("the final draw is %q, want blanks: the line has to be cleared", last)
	}
}

// TestNewReporterPicksTheRendering covers the selection itself, against real
// streams. A buffer is not a terminal, which is the case that matters: it is
// what a pipe and a CI log both look like, and getting it wrong is what puts
// carriage returns in a log file.
func TestNewReporterPicksTheRendering(t *testing.T) {
	var b buffer

	tests := map[string]struct {
		format output.Format
		stderr io.Writer
		live   bool
	}{
		"pretty into a pipe": {output.FormatPretty, &b, false},
		"json into a pipe":   {output.FormatJSON, &b, false},

		// os.Stderr under `go test` is redirected and so is not a terminal either.
		// That leaves the half that can be asserted without one, which is the half
		// that goes wrong in CI: nothing animates unless the stream says it is a
		// terminal.
		"pretty at whatever os.Stderr is": {output.FormatPretty, os.Stderr, output.IsTTY(os.Stderr)},
		"json at whatever os.Stderr is":   {output.FormatJSON, os.Stderr, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			reporter := ratelimit.NewReporter(output.NewJSONWriter(nil, &b), tc.stderr, tc.format)

			_, live := reporter.(*ratelimit.LiveReporter)
			if live != tc.live {
				t.Errorf("live rendering = %t, want %t", live, tc.live)
			}
		})
	}
}

// TestASilentLimiterTakesTheWaitInOneSleep pins the reason the countdown loop is
// conditional. Slicing a wait into interval-sized pieces is what the animation
// needs; a run nobody is watching should not wake up sixty times to say nothing,
// and every existing caller is entitled to assume a wait is a wait.
func TestASilentLimiterTakesTheWaitInOneSleep(t *testing.T) {
	clock := &countingClock{now: epoch}

	l := ratelimit.New(
		ratelimit.WithClock(clock),
		ratelimit.WithMaxWait(time.Hour),
		ratelimit.WithJitter(func(d time.Duration) time.Duration { return d }),
	)

	retryAfter := 5 * time.Minute
	if _, err := l.Recover(t.Context(), &gogithub.AbuseRateLimitError{RetryAfter: &retryAfter}); err != nil {
		t.Fatalf("Recover() error = %v, want nil", err)
	}

	if clock.sleeps != 1 {
		t.Errorf("a silent wait took %d sleeps, want 1", clock.sleeps)
	}
}

// TestPendingWritesCountDown covers the number the countdown reports. A wait is
// a different proposition when three writes are left and when three hundred are.
func TestPendingWritesCountDown(t *testing.T) {
	l := ratelimit.New(ratelimit.WithClock(&advancingClock{now: epoch}), ratelimit.WithWriteRate(1000))

	if _, known := l.PendingWrites(); known {
		t.Error("PendingWrites() is known before anything set one, want unknown: a dry run has nothing to say")
	}

	l.ExpectWrites(3)

	for range 2 {
		if err := l.Await(t.Context(), true); err != nil {
			t.Fatalf("Await() error = %v, want nil", err)
		}
	}

	pending, known := l.PendingWrites()
	if !known || pending != 1 {
		t.Errorf("PendingWrites() = %d, %t, want 1, true", pending, known)
	}

	// Reads do not consume the plan.
	if err := l.Await(t.Context(), false); err != nil {
		t.Fatalf("Await() error = %v, want nil", err)
	}

	if pending, _ := l.PendingWrites(); pending != 1 {
		t.Errorf("PendingWrites() = %d after a read, want 1", pending)
	}

	// And the count never goes below zero, whatever the retries did.
	for range 5 {
		if err := l.Await(t.Context(), true); err != nil {
			t.Fatalf("Await() error = %v, want nil", err)
		}
	}

	if pending, _ := l.PendingWrites(); pending != 0 {
		t.Errorf("PendingWrites() = %d, want 0", pending)
	}
}

// countingClock counts sleeps rather than recording them, for the one assertion
// that is about how many there were.
type countingClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps int
}

func (c *countingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *countingClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sleeps++
	c.now = c.now.Add(d)

	return nil
}

// assertGolden compares got against testdata/<name>.golden, rewriting the file
// instead when -update is passed:
//
//	task test:update
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}

		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run the tests with -update to create it)", path, err)
	}

	if got != string(want) {
		t.Errorf("output does not match %s\n--- got ---\n%q\n--- want ---\n%q", path, got, string(want))
	}
}
