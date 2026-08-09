package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
	"github.com/specsnl/labelsync/internal/util/output"
)

// testToken is what every client in this file authenticates with.
var testToken = Token{Value: "gho_test", Source: TokenSourceFlag}

// newTestClient points a client at a test server, with the backoff replaced by a
// recorder so that a retry suite costs no wall-clock time and the waits can be
// asserted rather than assumed.
func newTestClient(t *testing.T, handler http.Handler, opts ...Option) (*Client, *[]time.Duration) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	var (
		mu    sync.Mutex
		waits []time.Duration
	)

	base := []Option{
		WithBaseURL(server.URL),
		WithSleep(func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()

			waits = append(waits, d)

			return nil
		}),
	}

	client, err := New(testToken, append(base, opts...)...)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	return client, &waits
}

// write is fmt.Fprint with the error dropped. A test server's response writer
// has nowhere useful to report a failed write to, and the assertions are all on
// what the client made of the response anyway.
func write(w io.Writer, body string) {
	_, _ = fmt.Fprint(w, body)
}

// errorBody renders the JSON shape GitHub returns for a failure.
func errorBody(message string, errs ...string) string {
	var details []string
	for _, code := range errs {
		details = append(details, fmt.Sprintf(`{"resource":"Label","field":"name","code":%q}`, code))
	}

	return fmt.Sprintf(`{"message":%q,"errors":[%s]}`, message, strings.Join(details, ","))
}

// secondaryLimitBody is the shape go-github needs in order to return a typed
// *AbuseRateLimitError: it keys off the documentation_url suffix, not off the
// status or the Retry-After header.
const secondaryLimitBody = `{"message":"You have exceeded a secondary rate limit",` +
	`"documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#secondary-rate-limits"}`

// TestNewRejectsEmptyToken covers the guard on construction. Resolve never
// returns an empty token, so an empty one here means a caller built a Token
// itself — and failing now beats a 401 against every repository in the set.
func TestNewRejectsEmptyToken(t *testing.T) {
	for _, value := range []string{"", "   "} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			_, err := New(Token{Value: value})
			if !errors.Is(err, labelsync.ErrNoToken) {
				t.Fatalf("New() error = %v, want one wrapping ErrNoToken", err)
			}

			if labelsync.KindOf(err) != "no_token" {
				t.Errorf("KindOf(err) = %q, want %q", labelsync.KindOf(err), "no_token")
			}
		})
	}
}

// TestClientAuthenticates covers the two things construction has to get right:
// requests carry the token, and they go to the base URL the test set rather than
// to github.com.
func TestClientAuthenticates(t *testing.T) {
	var got string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")

		write(w, `[]`)
	})

	client, _ := newTestClient(t, handler)

	_, _, err := client.REST().Issues.ListLabels(t.Context(), "specsnl", "labelsync", nil)
	if err != nil {
		t.Fatalf("ListLabels() error = %v, want nil", err)
	}

	if want := "Bearer " + testToken.Value; got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
}

// TestBaseURLTrailingSlash pins the one way WithBaseURL is easy to get wrong.
// go-github resolves every request path relative to BaseURL, and a URL without a
// trailing slash silently loses its last segment — which against a bare
// httptest URL is invisible, and against a real one is not.
func TestBaseURLTrailingSlash(t *testing.T) {
	for _, suffix := range []string{"", "/"} {
		t.Run(fmt.Sprintf("suffix %q", suffix), func(t *testing.T) {
			var path string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path

				write(w, `[]`)
			}))
			t.Cleanup(server.Close)

			client, err := New(testToken, WithBaseURL(server.URL+suffix))
			if err != nil {
				t.Fatalf("New() error = %v, want nil", err)
			}

			if _, _, err := client.REST().Issues.ListLabels(t.Context(), "specsnl", "labelsync", nil); err != nil {
				t.Fatalf("ListLabels() error = %v, want nil", err)
			}

			if want := "/repos/specsnl/labelsync/labels"; path != want {
				t.Errorf("request path = %q, want %q", path, want)
			}
		})
	}
}

// TestClassifyPerRepository is the taxonomy itself: the three statuses that mean
// "this repository, not this run", each wrapping the sentinel so that errors.Is
// matches and KindOf renders repo_inaccessible.
func TestClassifyPerRepository(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantReason string
	}{
		{
			name:       "403 is archived or unpermitted",
			status:     http.StatusForbidden,
			body:       errorBody("Resource not accessible by personal access token"),
			wantReason: "archived, or the token lacks permission",
		},
		{
			name:       "404 is renamed or deleted",
			status:     http.StatusNotFound,
			body:       errorBody("Not Found"),
			wantReason: "renamed, deleted, or invisible to this token",
		},
		{
			name:       "410 is gone",
			status:     http.StatusGone,
			body:       errorBody("Gone"),
			wantReason: "gone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				write(w, tt.body)
			})

			client, _ := newTestClient(t, handler)

			err := client.Do(t.Context(), "specsnl/labelsync", "list labels", func(ctx context.Context) (*gogithub.Response, error) {
				_, resp, err := client.REST().Issues.ListLabels(ctx, "specsnl", "labelsync", nil)

				return resp, err
			})

			if !errors.Is(err, labelsync.ErrRepoInaccessible) {
				t.Fatalf("Do() error = %v, want one wrapping ErrRepoInaccessible", err)
			}

			if labelsync.KindOf(err) != "repo_inaccessible" {
				t.Errorf("KindOf(err) = %q, want %q", labelsync.KindOf(err), "repo_inaccessible")
			}

			var repoErr *RepoError
			if !errors.As(err, &repoErr) {
				t.Fatalf("Do() error = %v, want a *RepoError", err)
			}

			if repoErr.Status != tt.status {
				t.Errorf("status = %d, want %d", repoErr.Status, tt.status)
			}

			if repoErr.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", repoErr.Reason, tt.wantReason)
			}

			if repoErr.Repo != "specsnl/labelsync" || repoErr.Op != "list labels" {
				t.Errorf("repo/op = %q/%q, want %q/%q", repoErr.Repo, repoErr.Op, "specsnl/labelsync", "list labels")
			}

			// Recorded on the way out, not by the caller: a skipped repository
			// that never reaches the summary reads as one that synced cleanly.
			if client.Failures().Len() != 1 {
				t.Errorf("Failures().Len() = %d, want 1", client.Failures().Len())
			}
		})
	}
}

// TestClassifyRunFailures covers the other side of the taxonomy: statuses that
// are the run's problem, not one repository's. Treating any of these as a
// skipped repository would walk the rest of the set, collect the same failure
// every time, and report a run that "completed".
func TestClassifyRunFailures(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
	}{
		{
			name:   "401 is a bad token",
			status: http.StatusUnauthorized,
			body:   errorBody("Bad credentials"),
		},
		{
			name:   "422 is a rejected write",
			status: http.StatusUnprocessableEntity,
			body:   errorBody("Validation Failed", "custom"),
		},
		{
			name:    "403 with the rate limit exhausted is not an inaccessible repository",
			status:  http.StatusForbidden,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Reset": "1750000000"},
			body:    errorBody("API rate limit exceeded"),
		},
		{
			name:    "403 for a secondary limit is not an inaccessible repository",
			status:  http.StatusForbidden,
			headers: map[string]string{"Retry-After": "60"},
			body:    secondaryLimitBody,
		},
		{
			// go-github only types this when the body's documentation_url says
			// so, and GitHub's error bodies are inconsistent. A bare Retry-After
			// on a 403 still has to be read as a limit rather than as a
			// repository nobody can reach.
			name:    "403 with only a Retry-After header is still not an inaccessible repository",
			status:  http.StatusForbidden,
			headers: map[string]string{"Retry-After": "60"},
			body:    errorBody("You have exceeded a secondary rate limit"),
		},
		{
			name:   "429 is not an inaccessible repository",
			status: http.StatusTooManyRequests,
			headers: map[string]string{
				"Retry-After": "60",
			},
			body: errorBody("Too Many Requests"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range tt.headers {
					w.Header().Set(key, value)
				}

				w.WriteHeader(tt.status)
				write(w, tt.body)
			})

			client, _ := newTestClient(t, handler)

			err := client.Do(t.Context(), "specsnl/labelsync", "list labels", func(ctx context.Context) (*gogithub.Response, error) {
				_, resp, err := client.REST().Issues.ListLabels(ctx, "specsnl", "labelsync", nil)

				return resp, err
			})
			if err == nil {
				t.Fatal("Do() error = nil, want a failure")
			}

			if errors.Is(err, labelsync.ErrRepoInaccessible) {
				t.Errorf("Do() error = %v, which was classified as a skippable repository", err)
			}

			if client.Failures().Len() != 0 {
				t.Errorf("Failures().Len() = %d, want 0 — a run failure is not a skipped repository", client.Failures().Len())
			}
		})
	}
}

// TestClassifyRateLimitTypes pins the reason go-github was chosen over a
// hand-rolled client: both rate-limit shapes arrive as typed errors rather than
// as a 403 to sniff, which is what the backoff in ratelimit/ will branch on.
func TestClassifyRateLimitTypes(t *testing.T) {
	t.Run("primary", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Reset", "1750000000")
			w.WriteHeader(http.StatusForbidden)
			write(w, errorBody("API rate limit exceeded"))
		})

		client, _ := newTestClient(t, handler)

		err := client.Do(t.Context(), "specsnl/labelsync", "list labels", func(ctx context.Context) (*gogithub.Response, error) {
			_, resp, err := client.REST().Issues.ListLabels(ctx, "specsnl", "labelsync", nil)

			return resp, err
		})

		var rateErr *gogithub.RateLimitError
		if !errors.As(err, &rateErr) {
			t.Fatalf("Do() error = %v (%T), want a *github.RateLimitError", err, err)
		}
	})

	t.Run("secondary", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusForbidden)
			write(w, secondaryLimitBody)
		})

		client, _ := newTestClient(t, handler)

		err := client.Do(t.Context(), "specsnl/labelsync", "list labels", func(ctx context.Context) (*gogithub.Response, error) {
			_, resp, err := client.REST().Issues.ListLabels(ctx, "specsnl", "labelsync", nil)

			return resp, err
		})

		var abuseErr *gogithub.AbuseRateLimitError
		if !errors.As(err, &abuseErr) {
			t.Fatalf("Do() error = %v (%T), want a *github.AbuseRateLimitError", err, err)
		}
	})
}

// TestClassifySuccessAndCancellation covers the two edges: nothing to classify,
// and a run the user stopped. A cancelled context must not be mistaken for a
// repository problem, or Ctrl-C would skip the remaining set and report a
// completed run.
func TestClassifySuccessAndCancellation(t *testing.T) {
	t.Run("nil error classifies to nil", func(t *testing.T) {
		if err := Classify("specsnl/labelsync", "list labels", nil, nil); err != nil {
			t.Errorf("Classify() = %v, want nil", err)
		}
	})

	t.Run("cancellation is the run, not the repository", func(t *testing.T) {
		err := Classify("specsnl/labelsync", "list labels", nil, context.Canceled)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("Classify() = %v, want it to wrap context.Canceled", err)
		}

		if errors.Is(err, labelsync.ErrRepoInaccessible) {
			t.Error("Classify() treated a cancelled context as a skippable repository")
		}
	})
}

// TestIsAlreadyExists covers the reclassification: a create that collides is not
// a failure, it is an update. Two ordinary situations produce it — a plan
// computed against state that has since changed, and case-only drift, since
// GitHub holds label names case-insensitively unique.
func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "422 already_exists",
			status: http.StatusUnprocessableEntity,
			body:   errorBody("Validation Failed", "already_exists"),
			want:   true,
		},
		{
			name:   "422 for another reason",
			status: http.StatusUnprocessableEntity,
			body:   errorBody("Validation Failed", "custom"),
			want:   false,
		},
		{
			name:   "422 with no error details",
			status: http.StatusUnprocessableEntity,
			body:   errorBody("Validation Failed"),
			want:   false,
		},
		{
			name:   "404 is not a collision",
			status: http.StatusNotFound,
			body:   errorBody("Not Found"),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				write(w, tt.body)
			})

			client, _ := newTestClient(t, handler)

			_, _, err := client.REST().Issues.CreateLabel(t.Context(), "specsnl", "labelsync", &gogithub.Label{
				Name:  new("type: bug"),
				Color: new("d73a4a"),
			})

			if got := IsAlreadyExists(err); got != tt.want {
				t.Errorf("IsAlreadyExists(%v) = %t, want %t", err, got, tt.want)
			}
		})
	}

	t.Run("an unrelated error is not a collision", func(t *testing.T) {
		if IsAlreadyExists(errors.New("boom")) {
			t.Error("IsAlreadyExists(plain error) = true, want false")
		}

		if IsAlreadyExists(nil) {
			t.Error("IsAlreadyExists(nil) = true, want false")
		}
	})
}

// TestFailingRepositoryDoesNotAbortTheRun is the property the whole taxonomy
// exists for: one unreachable repository costs that repository and nothing else.
// The reachable ones are still synced, the failure is in the summary, and the
// exit code says the set was not covered in full.
func TestFailingRepositoryDoesNotAbortTheRun(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/archived"):
			w.WriteHeader(http.StatusForbidden)
			write(w, errorBody("Repository was archived so is read-only"))
		case strings.Contains(r.URL.Path, "/deleted"):
			w.WriteHeader(http.StatusNotFound)
			write(w, errorBody("Not Found"))
		default:
			write(w, `[{"name":"bug","color":"d73a4a"}]`)
		}
	})

	client, _ := newTestClient(t, handler)

	repos := []string{"specsnl/deleted", "specsnl/website", "specsnl/archived", "specsnl/labelsync"}

	var synced []string

	for _, repo := range repos {
		owner, name, _ := strings.Cut(repo, "/")

		err := client.Do(t.Context(), repo, "list labels", func(ctx context.Context) (*gogithub.Response, error) {
			_, resp, err := client.REST().Issues.ListLabels(ctx, owner, name, nil)

			return resp, err
		})
		if err != nil {
			if !errors.Is(err, labelsync.ErrRepoInaccessible) {
				t.Fatalf("Do(%s) error = %v, want nil or a skippable repository", repo, err)
			}

			continue
		}

		synced = append(synced, repo)
	}

	if want := []string{"specsnl/website", "specsnl/labelsync"}; !slicesEqual(synced, want) {
		t.Errorf("synced = %v, want %v", synced, want)
	}

	failures := client.Failures()

	if failures.Len() != 2 {
		t.Fatalf("Failures().Len() = %d, want 2", failures.Len())
	}

	// Sorted, not in arrival order: two identical runs must produce a summary
	// that diffs to nothing.
	all := failures.All()
	if all[0].Repo != "specsnl/archived" || all[1].Repo != "specsnl/deleted" {
		t.Errorf("failures = %q, %q, want them sorted by repository", all[0].Repo, all[1].Repo)
	}

	if got := failures.ExitCode(); got != exit.Skipped {
		t.Errorf("ExitCode() = %d, want %d", got, exit.Skipped)
	}

	var stdout, stderr bytes.Buffer

	failures.Report(output.NewPrettyWriter(&stdout, &stderr, nil))

	summary := stderr.String()
	for _, want := range []string{"2 repositories skipped", "specsnl/archived", "specsnl/deleted", "list labels"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not contain %q", summary, want)
		}
	}

	// stdout is the product. A skipped repository is the story of the run, and
	// putting it on stdout would break `... --output=json | jq`.
	if stdout.Len() != 0 {
		t.Errorf("summary wrote %q to stdout, want nothing", stdout.String())
	}
}

// TestFailuresEmpty covers the clean run: no skips, no summary, no outcome bit.
func TestFailuresEmpty(t *testing.T) {
	var failures Failures

	if got := failures.ExitCode(); got != exit.OK {
		t.Errorf("ExitCode() = %d, want %d", got, exit.OK)
	}

	var stdout, stderr bytes.Buffer

	failures.Report(output.NewPrettyWriter(&stdout, &stderr, nil))

	if stderr.Len() != 0 {
		t.Errorf("Report() wrote %q, want nothing on a clean run", stderr.String())
	}
}

// TestFailuresRecordRejectsRunFailures pins that the collector only collects
// what it is for. A run failure filed as a skipped repository would let a run
// continue past something it cannot continue past.
func TestFailuresRecordRejectsRunFailures(t *testing.T) {
	var failures Failures

	if failures.Record(errors.New("boom")) {
		t.Error("Record(plain error) = true, want false")
	}

	if failures.Record(fmt.Errorf("%w: mid-run", labelsync.ErrMaxWaitExceeded)) {
		t.Error("Record(ErrMaxWaitExceeded) = true, want false")
	}

	if failures.Len() != 0 {
		t.Errorf("Failures().Len() = %d, want 0", failures.Len())
	}
}

// TestRetryOnServerError covers the backoff: a 5xx is retried up to the
// configured number of times, the waits double, and the eventual success is what
// the caller sees.
func TestRetryOnServerError(t *testing.T) {
	var requests int

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		if requests <= 3 {
			w.WriteHeader(http.StatusBadGateway)
			write(w, errorBody("Bad gateway"))

			return
		}

		write(w, `[{"name":"bug","color":"d73a4a"}]`)
	})

	client, waits := newTestClient(t, handler, WithBackoff(10*time.Millisecond))

	labels, _, err := client.REST().Issues.ListLabels(t.Context(), "specsnl", "labelsync", nil)
	if err != nil {
		t.Fatalf("ListLabels() error = %v, want nil after the retries succeed", err)
	}

	if len(labels) != 1 {
		t.Errorf("labels = %d, want 1", len(labels))
	}

	if requests != 4 {
		t.Errorf("requests = %d, want 4 — the first attempt and %d retries", requests, DefaultRetries)
	}

	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if !slicesEqual(*waits, want) {
		t.Errorf("waits = %v, want %v — the backoff should double", *waits, want)
	}
}

// TestRetryExhausted covers the 5xx that never clears: the retries run out and
// the last response is surfaced rather than swallowed.
func TestRetryExhausted(t *testing.T) {
	var requests int

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.WriteHeader(http.StatusInternalServerError)
		write(w, errorBody("Server Error"))
	})

	client, _ := newTestClient(t, handler, WithBackoff(time.Millisecond))

	err := client.Do(t.Context(), "specsnl/labelsync", "list labels", func(ctx context.Context) (*gogithub.Response, error) {
		_, resp, err := client.REST().Issues.ListLabels(ctx, "specsnl", "labelsync", nil)

		return resp, err
	})
	if err == nil {
		t.Fatal("Do() error = nil, want the exhausted 500")
	}

	// A persistent 5xx is the API being down, not this repository being
	// unreachable. Skipping past it would walk the whole set collecting the
	// same failure and then report a run that mostly worked.
	if errors.Is(err, labelsync.ErrRepoInaccessible) {
		t.Errorf("Do() error = %v, which was classified as a skippable repository", err)
	}

	if requests != DefaultRetries+1 {
		t.Errorf("requests = %d, want %d", requests, DefaultRetries+1)
	}
}

// TestNoRetryOnClientError pins that the retry is for 5xx only. Repeating a 404
// three times is three requests of quota spent to be told the same thing.
func TestNoRetryOnClientError(t *testing.T) {
	var requests int

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.WriteHeader(http.StatusNotFound)
		write(w, errorBody("Not Found"))
	})

	client, waits := newTestClient(t, handler)

	_, _, _ = client.REST().Issues.ListLabels(t.Context(), "specsnl", "labelsync", nil)

	if requests != 1 {
		t.Errorf("requests = %d, want 1 — a 404 is not retried", requests)
	}

	if len(*waits) != 0 {
		t.Errorf("waits = %v, want none", *waits)
	}
}

// TestRetryReplaysTheRequestBody covers the write path. A retried POST that
// arrives with an empty body would create a label with no name, so the body has
// to be rewound for each attempt.
func TestRetryReplaysTheRequestBody(t *testing.T) {
	var bodies []string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))

		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			write(w, errorBody("Service Unavailable"))

			return
		}

		w.WriteHeader(http.StatusCreated)
		write(w, `{"name":"type: bug","color":"d73a4a"}`)
	})

	client, _ := newTestClient(t, handler, WithBackoff(time.Millisecond))

	label, _, err := client.REST().Issues.CreateLabel(t.Context(), "specsnl", "labelsync", &gogithub.Label{
		Name:  new("type: bug"),
		Color: new("d73a4a"),
	})
	if err != nil {
		t.Fatalf("CreateLabel() error = %v, want nil after the retry succeeds", err)
	}

	if label.GetName() != "type: bug" {
		t.Errorf("label name = %q, want %q", label.GetName(), "type: bug")
	}

	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}

	if bodies[0] != bodies[1] {
		t.Errorf("retried body = %q, want the first body %q", bodies[1], bodies[0])
	}

	if !strings.Contains(bodies[1], "type: bug") {
		t.Errorf("retried body = %q, which lost the label name", bodies[1])
	}
}

// TestRetryStopsOnCancellation covers the run the user stopped mid-backoff. A
// cancelled context must not sit out a wait whose result nothing will use.
func TestRetryStopsOnCancellation(t *testing.T) {
	var requests int

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++

		w.WriteHeader(http.StatusBadGateway)
		write(w, errorBody("Bad gateway"))
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := New(testToken,
		WithBaseURL(server.URL),
		WithSleep(func(context.Context, time.Duration) error { return context.Canceled }),
	)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	_, _, err = client.REST().Issues.ListLabels(t.Context(), "specsnl", "labelsync", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListLabels() error = %v, want one wrapping context.Canceled", err)
	}

	if requests != 1 {
		t.Errorf("requests = %d, want 1 — the backoff should not have been waited out", requests)
	}
}

// slicesEqual compares two slices of comparable values.
func slicesEqual[T comparable](got, want []T) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
