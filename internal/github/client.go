// client.go wraps go-github and classifies its errors once, so that no caller
// anywhere else in the tree sniffs a status code.
//
// # Why go-github
//
// The deciding reason is typed *github.RateLimitError and
// *github.AbuseRateLimitError. Secondary rate limits are this tool's most likely
// failure mode — hundreds of sequential writes against a roughly 80/minute
// ceiling — and a distinguishable error type is what makes the backoff logic
// clean and testable rather than a status-code guess. resp.NextPage for
// enumeration is a bonus.
//
// # The taxonomy in one place
//
// [Classify] is the only function in labelsync that looks at an HTTP status, and
// [Client.Do] is the only thing that calls it. Everything downstream reasons
// about [labelsync.ErrRepoInaccessible] and [IsAlreadyExists] instead.
package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/specsnl/labelsync/internal/github/ratelimit"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
	"github.com/specsnl/labelsync/internal/util/output"
)

// Retry defaults. A 5xx from GitHub is nearly always transient, and a label
// write is idempotent enough that repeating one is always safe: creating a label
// that now exists returns the 422 [IsAlreadyExists] recognises, and a PATCH
// applying the same values twice is the same label.
const (
	// DefaultRetries is how many times a 5xx is retried, *in addition* to the
	// first attempt — so four requests at most.
	DefaultRetries = 3

	// DefaultBackoff is the wait before the first retry. It doubles each time,
	// so the three waits are 500ms, 1s, and 2s.
	DefaultBackoff = 500 * time.Millisecond
)

// Client is labelsync's handle on the GitHub API.
//
// It owns the per-repository failures the run collected, because "keep going and
// report at the end" is a property of the whole run rather than of any one call
// site — see [Failures].
type Client struct {
	rest     *gogithub.Client
	failures *Failures
	cache    *cache
	limiter  *ratelimit.Limiter

	// The authenticated login, resolved at most once — see [Client.Login].
	loginOnce sync.Once
	login     string
	loginErr  error
}

// options are the knobs [New] accepts. Zero values mean the production defaults.
type options struct {
	cacheDir   string
	limiter    *ratelimit.Limiter
	baseURL    string
	httpClient *http.Client
	retries    int
	backoff    time.Duration
	sleep      func(ctx context.Context, d time.Duration) error
}

// Option configures a [Client].
type Option func(*options)

// WithBaseURL points the client at another API root. This is what lets the tests
// run against net/http/httptest rather than github.com; it is not a GitHub
// Enterprise Server switch, which is a non-goal.
//
// A trailing slash is added if absent, because go-github resolves every request
// path relative to this URL and silently loses the last segment without one.
func WithBaseURL(raw string) Option {
	return func(o *options) { o.baseURL = raw }
}

// WithCacheDir points the ETag cache at a directory, and is what turns caching
// on: an empty directory — the default — is a client that never reads or writes
// one. That is how --no-cache arrives, and it is deliberately the absence of a
// destination rather than a flag threaded through the read path.
//
// Production passes labelsync.CacheDir(); a test passes t.TempDir(), which is
// also what stops a test run from touching the developer's real cache.
func WithCacheDir(dir string) Option {
	return func(o *options) { o.cacheDir = dir }
}

// WithLimiter installs the rate limiter. Without one the client issues requests
// as fast as it is asked to, which is the right behaviour for a test and the
// wrong one for a few hundred label writes.
func WithLimiter(l *ratelimit.Limiter) Option {
	return func(o *options) { o.limiter = l }
}

// WithHTTPClient supplies the transport the retry wrapper is layered over.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// WithRetries sets how many times a 5xx is retried after the first attempt.
// Zero disables retrying.
func WithRetries(n int) Option {
	return func(o *options) { o.retries = n }
}

// WithBackoff sets the wait before the first retry; it doubles thereafter.
func WithBackoff(d time.Duration) Option {
	return func(o *options) { o.backoff = d }
}

// WithSleep replaces the backoff's sleep. The tests inject one that records the
// waits and returns immediately, which is what keeps a retry suite fast and
// makes the doubling assertable rather than merely plausible.
func WithSleep(sleep func(ctx context.Context, d time.Duration) error) Option {
	return func(o *options) { o.sleep = sleep }
}

// New builds a client that authenticates with token.
//
// The retry wrapper sits *under* go-github's auth transport, so a retried
// request carries the Authorization header the first one did.
func New(token Token, opts ...Option) (*Client, error) {
	// Resolve guarantees a non-empty token, so reaching here with an empty one
	// means a caller built a Token itself and skipped the chain. Failing now
	// beats a 401 on every repository in the set.
	if strings.TrimSpace(token.Value) == "" {
		return nil, fmt.Errorf("%w: %s", labelsync.ErrNoToken, noTokenHelp)
	}

	o := options{retries: DefaultRetries, backoff: DefaultBackoff, sleep: sleepContext}
	for _, opt := range opts {
		opt(&o)
	}

	base := http.DefaultTransport
	timeout := time.Duration(0)

	if o.httpClient != nil {
		timeout = o.httpClient.Timeout

		if o.httpClient.Transport != nil {
			base = o.httpClient.Transport
		}
	}

	// The limiter sits closest to the network, under the retry wrapper, so a
	// retried attempt is paced and observed like any other request rather than
	// slipping past the bucket because the first attempt already paid for it.
	if o.limiter != nil {
		base = &ratelimit.Transport{Next: base, Limiter: o.limiter}
	}

	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &retryTransport{
			next:    base,
			retries: o.retries,
			backoff: o.backoff,
			sleep:   o.sleep,
		},
	}

	rest := gogithub.NewClient(httpClient).WithAuthToken(token.Value)

	// go-github keeps its own memory of a limit it has seen and refuses later
	// requests **without issuing them**, against the real clock. That is a
	// sensible default and the wrong one here: this tool waits limits out
	// itself, through an injected clock, and a second opinion that cannot be
	// told that time has passed turns a retry into a refusal — and, under test,
	// into a run that spends its whole --max-wait without making a request.
	// Waiting is ratelimit.Limiter's job, and it has to be the only thing doing
	// it.
	rest.DisableRateLimitCheck = true

	if o.baseURL != "" {
		parsed, err := url.Parse(strings.TrimSuffix(o.baseURL, "/") + "/")
		if err != nil {
			return nil, fmt.Errorf("invalid base URL %q: %w", o.baseURL, err)
		}

		rest.BaseURL = parsed
	}

	slog.Debug("github client built", "token", token, "base_url", rest.BaseURL.String(), "retries", o.retries)

	return &Client{rest: rest, failures: &Failures{}, cache: newCache(o.cacheDir), limiter: o.limiter}, nil
}

// REST exposes the underlying go-github client, for the request-issuing code in
// repos.go and labels.go. Call it through [Client.Do] so the result is
// classified.
func (c *Client) REST() *gogithub.Client { return c.rest }

// Failures returns the per-repository failures collected so far.
func (c *Client) Failures() *Failures { return c.failures }

// Do runs one repository-scoped call and classifies whatever it returns.
//
// op is a short present-tense description used in messages — "list labels",
// "create label" — and repo is owner/repo.
//
// A per-repository failure is **recorded before it is returned**, so a caller
// that means to continue can do so without remembering to collect anything:
//
//	if err := c.Do(ctx, repo, "list labels", call); err != nil {
//	    if errors.Is(err, labelsync.ErrRepoInaccessible) {
//	        continue // already collected; the summary will name it
//	    }
//	    return err // a real failure: the run stops
//	}
//
// Recording inside Do rather than at the call site is deliberate. A skipped
// repository that never reaches the summary is indistinguishable from one that
// synced cleanly, and that is the one mistake here a user cannot detect.
// # Rate limits are waited out, not returned
//
// A call that comes back rate-limited is retried once the limiter has slept it
// off, because a rate limit is not an outcome — it is the API asking for the
// same request later. The loop ends when the call stops being rate-limited, or
// when the wait would take the run past --max-wait, which comes back as
// [labelsync.ErrMaxWaitExceeded].
func (c *Client) Do(ctx context.Context, repo, op string, call func(context.Context) (*gogithub.Response, error)) error {
	for {
		resp, err := call(ctx)

		classified := Classify(repo, op, resp, err)
		if classified == nil {
			return nil
		}

		if c.limiter != nil {
			retry, waitErr := c.limiter.Recover(ctx, classified)
			if waitErr != nil {
				return waitErr
			}

			if retry {
				slog.Debug("retrying after a rate limit", "repo", repo, "op", op)

				continue
			}
		}

		c.failures.Record(classified)

		return classified
	}
}

// RateLimit reads the current budget from GET /rate_limit.
//
// The endpoint is **free**: it does not itself count against the limit, which is
// what makes it worth calling at startup. The reading seeds the limiter, so the
// first request of a run is issued as informed as the last, and --debug reports
// what is left before anything is spent.
//
// A failure here is not fatal to the caller's decision-making — the run can
// proceed uninformed, which is what it did before the call existed — but it is
// returned rather than swallowed so a caller can say so.
func (c *Client) RateLimit(ctx context.Context) (*gogithub.Rate, error) {
	limits, _, err := c.rest.RateLimit.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the rate limit: %w", err)
	}

	core := limits.GetCore()
	if core == nil {
		return nil, fmt.Errorf("reading the rate limit: %w", errNoCoreBudget)
	}

	if c.limiter != nil {
		c.limiter.Prime(core.Remaining, core.Reset.Time)
	}

	slog.Debug("rate limit at startup",
		"remaining", core.Remaining,
		"limit", core.Limit,
		"resets_at", core.Reset.Format(time.RFC3339),
	)

	return core, nil
}

// Login returns the login of the user the token belongs to, from GET /user.
//
// It is asked for one reason: config.Resolve needs it to decide which of the two
// user endpoints a `user:` selector has to call, and whether asking for that
// user's private repositories is going to come back empty. A config with no
// `user:` group never needs it, so callers check before spending the request.
//
// The answer is cached for the life of the client. It cannot change during a run,
// and a second request for it would be a request spent on a question already
// answered.
//
// A failure is the caller's to shrug off: a token that cannot read /user — a
// GitHub App installation token, most likely — still lists organisations
// perfectly well. Resolve treats an empty login as "somebody else", which is the
// conservative reading.
func (c *Client) Login(ctx context.Context) (string, error) {
	c.loginOnce.Do(func() {
		user, _, err := c.rest.Users.Get(ctx, "")
		if err != nil {
			c.loginErr = fmt.Errorf("reading the authenticated user: %w", err)

			return
		}

		c.login = user.GetLogin()

		slog.Debug("authenticated user resolved", "login", c.login)
	})

	return c.login, c.loginErr
}

// errNoCoreBudget is a /rate_limit response with no core resource in it. It is
// not a sentinel in internal/labelsync because it is not a way a run fails: the
// caller reports it and carries on uninformed.
var errNoCoreBudget = errors.New("the response carried no core budget")

// RepoError is a failure that belongs to one repository rather than to the run.
//
// It wraps [labelsync.ErrRepoInaccessible], so errors.Is matches it and KindOf
// renders repo_inaccessible, while the fields carry what a summary line needs.
type RepoError struct {
	// Repo is owner/repo.
	Repo string

	// Op is what was being attempted, in the present tense: "list labels".
	Op string

	// Status is the HTTP status that produced the classification.
	Status int

	// Reason says what the status means for this repository, in the terms a user
	// can act on rather than as a number.
	Reason string

	err error
}

// Error implements error.
func (e *RepoError) Error() string {
	return fmt.Sprintf("%s: cannot %s: %s (HTTP %d)", e.Repo, e.Op, e.Reason, e.Status)
}

// Unwrap exposes the wrapped sentinel, keeping errors.Is and
// [labelsync.KindOf] working through the struct.
func (e *RepoError) Unwrap() error { return e.err }

// Classify turns what a go-github call returned into one of three things: nil,
// a [RepoError] the run should skip past, or an error the run should fail on.
//
// The three per-repository statuses come from the design:
//
//	403  archived, or the token lacks permission
//	404  renamed or deleted between enumeration and sync, or invisible to the token
//	410  gone
//
// A rate-limit error is checked *first*, because it also arrives as a 403 and is
// emphatically not a repository that cannot be reached — it is the whole run
// needing to wait. Mistaking one for the other would skip every remaining
// repository and report success.
func Classify(repo, op string, resp *gogithub.Response, err error) error {
	if err == nil {
		return nil
	}

	// A cancelled context is the run ending, not a repository failing. Skipping
	// every remaining repository on Ctrl-C would report a "completed" run.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %s: %w", repo, op, err)
	}

	if isRateLimited(resp, err) {
		return fmt.Errorf("%s: %s: %w", repo, op, err)
	}

	reason, ok := repoFailureReason(statusOf(resp, err))
	if !ok {
		return fmt.Errorf("%s: %s: %w", repo, op, err)
	}

	return &RepoError{
		Repo:   repo,
		Op:     op,
		Status: statusOf(resp, err),
		Reason: reason,
		err:    fmt.Errorf("%w: %s: %s: %w", labelsync.ErrRepoInaccessible, repo, reason, err),
	}
}

// isRateLimited reports whether a failure is the run needing to wait rather than
// a repository that cannot be reached.
//
// The typed errors are the reason go-github was chosen and are checked first.
// The header checks behind them are not redundant: go-github only produces an
// AbuseRateLimitError when the response body's documentation_url carries the
// right suffix, and GitHub's error bodies are famously inconsistent. A 403 or
// 429 carrying Retry-After, or a 403 with the remaining quota at zero, is a rate
// limit whatever the body says — and misreading one as an inaccessible
// repository would skip every repository left in the set and report success.
func isRateLimited(resp *gogithub.Response, err error) bool {
	var (
		rateErr  *gogithub.RateLimitError
		abuseErr *gogithub.AbuseRateLimitError
	)

	if errors.As(err, &rateErr) || errors.As(err, &abuseErr) {
		return true
	}

	status := statusOf(resp, err)
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}

	return headerOf(resp, err, "Retry-After") != "" || headerOf(resp, err, "X-RateLimit-Remaining") == "0"
}

// headerOf reads a response header from whichever of the two carriers has one.
func headerOf(resp *gogithub.Response, err error, key string) string {
	var errResp *gogithub.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		if value := errResp.Response.Header.Get(key); value != "" {
			return value
		}
	}

	if resp != nil && resp.Response != nil {
		return resp.Header.Get(key)
	}

	return ""
}

// repoFailureReason maps the three per-repository statuses to what they mean.
// Any other status is the run's problem, not one repository's.
func repoFailureReason(status int) (string, bool) {
	switch status {
	case http.StatusForbidden:
		return "archived, or the token lacks permission", true
	case http.StatusNotFound:
		return "renamed, deleted, or invisible to this token", true
	case http.StatusGone:
		return "gone", true
	default:
		return "", false
	}
}

// statusOf digs the status out of whichever of the two places carries it. A
// go-github error carries its own response, and it is the more reliable of the
// two: resp is nil on a transport failure.
func statusOf(resp *gogithub.Response, err error) int {
	var errResp *gogithub.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		return errResp.Response.StatusCode
	}

	if resp != nil && resp.Response != nil {
		return resp.StatusCode
	}

	return 0
}

// IsAlreadyExists reports whether err is GitHub refusing to create a label that
// is already there: a 422 carrying an "already_exists" code.
//
// This is **not** a failure, and the caller turns it into an update. Two
// perfectly ordinary situations produce it — a plan computed against state that
// has since changed, and two runs overlapping — and in both the desired end
// state is a label with the configured values, which an update reaches.
//
// It is also how case-only drift surfaces: a repository holding `bug` rejects
// the creation of `Bug`, because label names are case-insensitively unique.
func IsAlreadyExists(err error) bool {
	var errResp *gogithub.ErrorResponse
	if !errors.As(err, &errResp) {
		return false
	}

	if errResp.Response == nil || errResp.Response.StatusCode != http.StatusUnprocessableEntity {
		return false
	}

	return slices.ContainsFunc(errResp.Errors, func(e gogithub.Error) bool {
		return e.Code == "already_exists"
	})
}

// Failures collects the repositories a run could not reach.
//
// Reads run in parallel, so it is safe for concurrent use.
type Failures struct {
	mu    sync.Mutex
	items []*RepoError
}

// Record files err if it is a per-repository failure, and reports whether it
// did. A false means the error is the run's, and the caller must not continue.
func (f *Failures) Record(err error) bool {
	var repoErr *RepoError
	if !errors.As(err, &repoErr) {
		return false
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.items = append(f.items, repoErr)

	return true
}

// Len is how many repositories were skipped.
func (f *Failures) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.items)
}

// All returns the collected failures, sorted by repository and then by
// operation. Sorted rather than in arrival order because arrival order is
// whatever the parallel reads happened to finish in, and a summary that
// reshuffles between two identical runs is not a summary anyone can diff.
func (f *Failures) All() []*RepoError {
	f.mu.Lock()
	defer f.mu.Unlock()

	sorted := slices.Clone(f.items)
	slices.SortFunc(sorted, func(a, b *RepoError) int {
		if a.Repo != b.Repo {
			return strings.Compare(a.Repo, b.Repo)
		}

		return strings.Compare(a.Op, b.Op)
	})

	return sorted
}

// ExitCode is [exit.Skipped] when any repository was skipped, and [exit.OK]
// otherwise. It is an outcome bit: the caller ORs it with whatever else the run
// concluded, so a dry run that both drifted and skipped exits 6.
func (f *Failures) ExitCode() exit.Code {
	if f.Len() == 0 {
		return exit.OK
	}

	return exit.Skipped
}

// Report writes the end-of-run summary of skipped repositories.
//
// On stderr, through Warn: a skipped repository is a recoverable problem and the
// story of the run, not its product. `labelsync groups --output=json | jq` has to
// keep working when three repositories turn out to be archived.
func (f *Failures) Report(w output.Writer) {
	items := f.All()
	if len(items) == 0 {
		return
	}

	w.Warn("%d repositor%s skipped:", len(items), plural(len(items), "y", "ies"))

	for _, item := range items {
		w.Warn("  %s — cannot %s: %s (HTTP %d)", item.Repo, item.Op, item.Reason, item.Status)
	}
}

// plural picks a suffix. Inline enough not to earn a home in util.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}

// retryTransport retries 5xx responses with exponential backoff.
//
// It is a RoundTripper rather than a loop at each call site so that every
// request through the client gets it, including the ones go-github issues on its
// own for pagination.
type retryTransport struct {
	next    http.RoundTripper
	retries int
	backoff time.Duration
	sleep   func(ctx context.Context, d time.Duration) error
}

// RoundTrip implements http.RoundTripper.
//
// A transport must not modify the request it is given, so each attempt goes out
// on a clone with a freshly rewound body. A request whose body cannot be rewound
// is not retried: replaying half a body is worse than surfacing the 5xx.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	delay := t.backoff

	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; ; attempt++ {
		attemptReq := req.Clone(req.Context())

		if req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr != nil {
				return nil, fmt.Errorf("rewinding the request body for a retry: %w", bodyErr)
			}

			attemptReq.Body = body
		}

		resp, err = t.next.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}

		if attempt >= t.retries || resp.StatusCode < http.StatusInternalServerError {
			return resp, nil
		}

		// Only a rewindable body can be sent again. A nil Body is trivially
		// rewindable, which covers every GET and DELETE.
		if req.Body != nil && req.GetBody == nil {
			return resp, nil
		}

		slog.Debug("retrying after a server error",
			"status", resp.StatusCode,
			"attempt", attempt+1,
			"of", t.retries,
			"wait", delay.String(),
		)

		// The body has to be drained and closed or the connection is not reused,
		// and a retry storm then opens a new one every time.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if sleepErr := t.sleep(req.Context(), delay); sleepErr != nil {
			return nil, sleepErr
		}

		delay *= 2
	}
}

// sleepContext waits for d, or until the context is done. A cancelled run must
// not sit out a backoff it will never use the result of.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
