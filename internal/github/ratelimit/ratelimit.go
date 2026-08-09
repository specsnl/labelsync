// Package ratelimit keeps a run inside GitHub's limits, proactively and
// reactively.
//
// Reads are already cheap — roughly 51 requests for 50 repositories against a
// 5,000/hour budget. **Writes are what need managing**: a few hundred label
// operations against a content-creation ceiling of roughly 80 a minute is this
// tool's most likely failure mode, and it is an undocumented, body-shaped
// secondary limit rather than a header-shaped primary one.
//
// # Proactive, then reactive
//
//   - A token bucket paces writes at [DefaultWriteRate] a minute, under the
//     ceiling, so the limit is usually never reached at all.
//   - Every response's x-ratelimit-remaining and x-ratelimit-reset are read, and
//     a budget running out means sleeping until the reset **before** issuing the
//     next request rather than racing into a 403.
//   - When a limit is hit anyway, [Limiter.Recover] waits it out: until
//     Rate.Reset for a primary limit, and Retry-After — or a jittered backoff
//     from a minute — for a secondary one.
//
// # Nothing here sleeps for real under test
//
// Every wait goes through an injected [Clock]. A rate-limit suite that waits out
// its own backoffs is a suite nobody runs, and one that asserts on wall-clock
// time is one that fails on a loaded machine.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// Defaults. The command's flags carry the same values; these are what a caller
// that passes nothing gets.
const (
	// DefaultWriteRate is the ceiling on label writes per minute. GitHub's
	// content-creation limit is roughly 80/minute and undocumented, so the
	// default sits under it with room to spare.
	DefaultWriteRate = 70

	// DefaultMaxWait caps the total time a run may spend sleeping for limits. A
	// CI job should fail with a clear reason rather than idle for an hour
	// burning minutes.
	DefaultMaxWait = 15 * time.Minute

	// DefaultThreshold is how much primary budget has to be left before the
	// limiter stops and waits for the reset. It is not zero: a run that spends
	// its last request discovering it has none left has already lost, and the
	// margin absorbs the requests in flight when the reading was taken.
	DefaultThreshold = 20

	// secondaryBackoff is the first wait for a secondary limit that arrives
	// without Retry-After. GitHub's own guidance is to wait at least a minute,
	// and it doubles from here.
	secondaryBackoff = time.Minute

	// maxSecondaryBackoff caps the doubling. Past this the wait is long enough
	// that --max-wait is the thing that should end the run, not another double.
	maxSecondaryBackoff = 15 * time.Minute
)

// Clock is the limiter's view of time. Production uses [SystemClock]; tests
// inject one that records what it was asked to wait and returns immediately.
type Clock interface {
	// Now is the current time.
	Now() time.Time

	// Sleep waits for d, or until ctx is done — whichever comes first. A
	// cancelled run must not sit out a backoff whose result nothing will use.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock is the real one.
type SystemClock struct{}

// Now implements [Clock].
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep implements [Clock].
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Limiter paces requests and waits out limits. The zero value is not usable;
// build one with [New].
//
// It is safe for concurrent use: reads run in parallel, and every one of them
// reports what it saw into the same budget.
type Limiter struct {
	clock     Clock
	writeRate int
	maxWait   time.Duration
	threshold int

	// jitter spreads the secondary backoff so that several processes that hit
	// the limit together do not come back together. Injected, because a
	// randomised wait is not something a test can assert on.
	jitter func(time.Duration) time.Duration

	mu sync.Mutex

	// tokens is the write bucket, in requests, refilled at writeRate a minute
	// and capped at a minute's worth.
	tokens float64
	filled time.Time

	// remaining and reset are the last thing GitHub said about the primary
	// budget. valid is false until a response has been seen: zero remaining and
	// a zero reset are indistinguishable from "nothing has been read yet", and
	// acting on that would stall the first request of every run.
	remaining int
	reset     time.Time
	valid     bool

	// waited is the run's total, against maxWait.
	waited time.Duration

	// backoff is the next secondary wait, doubling per consecutive hit.
	backoff time.Duration
}

// Option configures a [Limiter].
type Option func(*Limiter)

// WithClock replaces the clock. This is what keeps the suite instant.
func WithClock(c Clock) Option {
	return func(l *Limiter) { l.clock = c }
}

// WithWriteRate sets the writes-per-minute ceiling — the --write-rate flag.
// Non-positive disables pacing, which is a thing to do on a single-repository
// run and a thing to regret on fifty.
func WithWriteRate(perMinute int) Option {
	return func(l *Limiter) { l.writeRate = perMinute }
}

// WithMaxWait caps the total time spent waiting — the --max-wait flag.
func WithMaxWait(d time.Duration) Option {
	return func(l *Limiter) { l.maxWait = d }
}

// WithThreshold sets how much primary budget must remain before the limiter
// waits for the reset.
func WithThreshold(n int) Option {
	return func(l *Limiter) { l.threshold = n }
}

// WithJitter replaces the randomisation applied to a secondary backoff. Tests
// inject the identity, which is what makes the doubling assertable.
func WithJitter(f func(time.Duration) time.Duration) Option {
	return func(l *Limiter) { l.jitter = f }
}

// New builds a limiter. The bucket starts full: a run's first writes should go
// out immediately, and pacing is about the sustained rate rather than the first
// one.
func New(opts ...Option) *Limiter {
	l := &Limiter{
		clock:     SystemClock{},
		writeRate: DefaultWriteRate,
		maxWait:   DefaultMaxWait,
		threshold: DefaultThreshold,
		jitter:    defaultJitter,
		backoff:   secondaryBackoff,
	}

	for _, opt := range opts {
		opt(l)
	}

	l.tokens = float64(l.writeRate)
	l.filled = l.clock.Now()

	return l
}

// defaultJitter spreads a wait over ±20% of itself, so that several runs that
// hit the same secondary limit do not resume in lockstep and hit it again.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}

	spread := float64(d) * 0.2

	return time.Duration(float64(d) - spread + rand.Float64()*2*spread) //nolint:gosec // Spreading a backoff is not a security decision.
}

// Await blocks until the next request may go out.
//
// write says whether the request creates content, which is the only kind the
// bucket paces. The primary-budget check applies to both: a read is cheap, but a
// read issued with nothing left in the budget is still a 403.
func (l *Limiter) Await(ctx context.Context, write bool) error {
	pace, budget := l.plan(write)

	// The two waits are taken outside the lock, so a paced write does not hold
	// every parallel read behind it.
	// Pacing is not booked against --max-wait. That ceiling is about a run
	// sitting out somebody else's limit; this is the tool spacing its own
	// requests, it is sub-second, and counting it would make --max-wait a cap on
	// how much work a run may do.
	if pace > 0 {
		slog.Debug("pacing a write", "wait", pace.String(), "rate_per_minute", l.writeRate)

		if err := l.clock.Sleep(ctx, pace); err != nil {
			return err
		}
	}

	if budget > 0 {
		return l.waitOut(ctx, budget, "primary budget nearly spent")
	}

	return nil
}

// plan decides both waits under one lock, and spends the write token.
func (l *Limiter) plan(write bool) (pace, budget time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()

	if write && l.writeRate > 0 {
		l.refill(now)

		// Below one token the wait is however long the next one takes to
		// arrive. The token is spent either way: by the time the sleep is over,
		// the bucket will have refilled it.
		if l.tokens < 1 {
			pace = time.Duration(float64(time.Minute) * (1 - l.tokens) / float64(l.writeRate))
		}

		l.tokens--
	}

	// Sleeping until the reset is only worth doing while there is a reset to
	// wait for; a stale one from a window that has already passed says nothing.
	if l.valid && l.remaining <= l.threshold && now.Before(l.reset) {
		budget = l.reset.Sub(now)
	}

	return pace, budget
}

// refill tops the bucket up for the time that has passed, capped at a minute's
// worth so that an idle run does not bank an unlimited burst.
func (l *Limiter) refill(now time.Time) {
	elapsed := now.Sub(l.filled)
	if elapsed <= 0 {
		return
	}

	l.filled = now
	l.tokens = min(l.tokens+elapsed.Minutes()*float64(l.writeRate), float64(l.writeRate))
}

// Observe records what a response said about the primary budget.
//
// It is called for every response, including the ones that failed: a 403 that is
// a rate limit carries the same headers, and those are the ones worth having.
func (l *Limiter) Observe(header http.Header) {
	remaining, okRemaining := intHeader(header, "X-RateLimit-Remaining")
	reset, okReset := intHeader(header, "X-RateLimit-Reset")

	if !okRemaining || !okReset {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.remaining = remaining
	l.reset = time.Unix(int64(reset), 0)
	l.valid = true
}

// Remaining is the last primary budget reading, and whether there has been one.
// It is what --debug reports and what an apply consults before starting.
func (l *Limiter) Remaining() (int, time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.remaining, l.reset, l.valid
}

// Prime seeds the budget from the free GET /rate_limit at startup, so the first
// request of a run is issued as informed as the last.
func (l *Limiter) Prime(remaining int, reset time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.remaining = remaining
	l.reset = reset
	l.valid = true
}

// Affordable reports whether writes requests fit in what is known to be left.
//
// It is deliberately a question and not a refusal: this package knows the
// budget, and the command knows whether a half-finished apply is worse than none
// at all. An unknown budget is affordable — refusing on no information would
// stop a run that would have succeeded.
func (l *Limiter) Affordable(writes int) bool {
	remaining, _, valid := l.Remaining()

	return !valid || writes <= remaining
}

// Recover waits out a rate limit, and reports whether the request should be
// retried.
//
// An error that is not a rate limit returns (false, nil): it is the caller's,
// and this package has nothing to say about it.
//
// A wait that would take the run past --max-wait is refused with
// [labelsync.ErrMaxWaitExceeded] rather than taken, and the message says how
// long was asked for and how much of the budget is left — a CI job that idles
// for an hour and then fails has wasted both the hour and the reason.
func (l *Limiter) Recover(ctx context.Context, err error) (bool, error) {
	wait, kind, ok := l.penalty(err)
	if !ok {
		return false, nil
	}

	if waitErr := l.waitOut(ctx, wait, kind); waitErr != nil {
		return false, waitErr
	}

	return true, nil
}

// penalty turns a rate-limit error into how long to wait for it.
//
// The typed errors are why go-github was chosen. Behind them sits the header
// check, which is not redundant: go-github only produces an AbuseRateLimitError
// when the body's documentation_url carries the right suffix, and GitHub's error
// bodies are famously inconsistent.
func (l *Limiter) penalty(err error) (time.Duration, string, bool) {
	now := l.clock.Now()

	var primary *gogithub.RateLimitError
	if errors.As(err, &primary) {
		l.resetBackoff()

		return primary.Rate.Reset.Sub(now), "primary rate limit", true
	}

	var secondary *gogithub.AbuseRateLimitError
	if errors.As(err, &secondary) {
		if secondary.RetryAfter != nil && *secondary.RetryAfter > 0 {
			l.resetBackoff()

			return *secondary.RetryAfter, "secondary rate limit", true
		}

		// No Retry-After: back off from a minute, doubling per consecutive hit,
		// jittered so that several runs do not resume together and trip it
		// again.
		return l.nextBackoff(), "secondary rate limit", true
	}

	return 0, "", false
}

// nextBackoff returns the current secondary wait and doubles it for the next.
func (l *Limiter) nextBackoff() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	wait := l.backoff
	l.backoff = min(l.backoff*2, maxSecondaryBackoff)

	return l.jitter(wait)
}

// resetBackoff puts the secondary backoff back to its starting value. A limit
// that told us exactly how long to wait is not the escalating case.
func (l *Limiter) resetBackoff() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.backoff = secondaryBackoff
}

// waitOut sleeps for d, against the run's budget.
func (l *Limiter) waitOut(ctx context.Context, d time.Duration, reason string) error {
	if d <= 0 {
		return nil
	}

	if err := l.spend(d); err != nil {
		return err
	}

	slog.Debug("waiting out a rate limit", "reason", reason, "wait", d.String())

	return l.clock.Sleep(ctx, d)
}

// spend books d against --max-wait, or refuses it.
func (l *Limiter) spend(d time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.maxWait <= 0 {
		return nil
	}

	if l.waited+d > l.maxWait {
		return fmt.Errorf("%w: waiting %s would exceed the %s ceiling, %s of it already spent",
			labelsync.ErrMaxWaitExceeded, d.Round(time.Second), l.maxWait, l.waited.Round(time.Second))
	}

	l.waited += d

	return nil
}

// Waited is how long the run has spent asleep for limits so far.
func (l *Limiter) Waited() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.waited
}

// intHeader reads a header as an integer, reporting whether it was there and
// was one.
func intHeader(header http.Header, key string) (int, bool) {
	raw := header.Get(key)
	if raw == "" {
		return 0, false
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}

	return value, true
}
