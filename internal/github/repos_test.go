package github

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// perRepoGet matches GET /repos/{owner}/{repo} — the request this file exists to
// prove is never issued. Filtering is answered from the enumeration response, and
// a per-repository lookup would turn one request per hundred repositories into
// one per repository for information already in hand.
var perRepoGet = regexp.MustCompile(`^/repos/[^/]+/[^/]+$`)

// requestLog records what the fake API saw. Selectors are walked in parallel, so
// unlike the label tests this one has to be safe for concurrent use.
type requestLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *requestLog) record(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.paths = append(l.paths, path)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.paths...)
}

// assertNoPerRepoGet is the cost-model assertion, made in every test here rather
// than in one: a filter that starts issuing lookups would otherwise only show up
// as a slow run against a large org.
func (l *requestLog) assertNoPerRepoGet(t *testing.T) {
	t.Helper()

	for _, path := range l.all() {
		if perRepoGet.MatchString(path) {
			t.Errorf("a per-repository GET was issued: %s", path)
		}
	}
}

// repoEntry renders one repository as the listing endpoints return it, with the
// four attributes every filter is answered from.
type repoEntry struct {
	Owner     string
	Name      string
	Archived  bool
	Fork      bool
	Private   bool
	HasIssues bool
}

func (r repoEntry) json() string {
	return fmt.Sprintf(
		`{"name":%q,"owner":{"login":%q},"archived":%t,"fork":%t,"private":%t,"has_issues":%t}`,
		r.Name, r.Owner, r.Archived, r.Fork, r.Private, r.HasIssues,
	)
}

// listing renders a page of repositories.
func listing(entries ...repoEntry) string {
	rendered := make([]string, 0, len(entries))
	for _, entry := range entries {
		rendered = append(rendered, entry.json())
	}

	return "[" + strings.Join(rendered, ",") + "]"
}

// enumerator wires a client to a handler that logs every path it is asked for.
func enumerator(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *requestLog) {
	t.Helper()

	log := &requestLog{}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.EscapedPath())

		handler(w, r)
	}))

	return client, log
}

// names renders a result as owner/repo strings, which is what the assertions
// compare.
func names(repos []config.Repo) []string {
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repo.String())
	}

	return out
}

// orgSelector is a plain org selector with no filters set.
func orgSelector(group, org string) config.Selector {
	return config.Selector{
		Group:      group,
		Kind:       config.SourceOrg,
		Owner:      org,
		Visibility: config.VisibilityAll,
	}
}

// TestEnumerateOrgWalksEveryPage covers pagination and the attributes that ride
// along with it. A listing that stopped at the first page would silently narrow
// every group in the config.
func TestEnumerateOrgWalksEveryPage(t *testing.T) {
	client, log := enumerator(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			write(w, listing(repoEntry{Owner: "specsnl", Name: "specs-cli", HasIssues: true}))

			return
		}

		w.Header().Set("Link", `<https://api.github.com/organizations/1/repos?page=2>; rel="next"`)
		write(w, listing(repoEntry{Owner: "specsnl", Name: "labelsync", HasIssues: true}))
	})

	repos, err := client.Enumerate(t.Context(), []config.Selector{orgSelector("all", "specsnl")}, 8)
	if err != nil {
		t.Fatalf("Enumerate() error = %v, want nil", err)
	}

	want := []string{"specsnl/labelsync", "specsnl/specs-cli"}
	if got := names(repos); !slicesEqual(got, want) {
		t.Errorf("Enumerate() = %v, want %v", got, want)
	}

	paths := log.all()
	if len(paths) != 2 {
		t.Errorf("requests = %d, want 2 (one per page): %v", len(paths), paths)
	}

	for _, path := range paths {
		if path != "/orgs/specsnl/repos" {
			t.Errorf("request path = %q, want /orgs/specsnl/repos", path)
		}
	}

	log.assertNoPerRepoGet(t)
}

// TestEnumerateUserPicksTheEndpoint covers the one distinction that decides
// whether private repositories are visible at all. Only /user/repos returns
// them, and only for the token's own user.
func TestEnumerateUserPicksTheEndpoint(t *testing.T) {
	tests := map[string]struct {
		authenticated bool
		want          string
	}{
		"the token's own user": {authenticated: true, want: "/user/repos"},
		"somebody else":        {authenticated: false, want: "/users/ilyes512/repos"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var query string

			client, log := enumerator(t, func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery

				write(w, listing(repoEntry{Owner: "ilyes512", Name: "dotfiles", Private: true}))
			})

			selector := config.Selector{
				Group:             "mine",
				Kind:              config.SourceUser,
				Owner:             "ilyes512",
				Visibility:        config.VisibilityAll,
				AuthenticatedUser: tc.authenticated,
			}

			repos, err := client.Enumerate(t.Context(), []config.Selector{selector}, 8)
			if err != nil {
				t.Fatalf("Enumerate() error = %v, want nil", err)
			}

			if got := names(repos); !slicesEqual(got, []string{"ilyes512/dotfiles"}) {
				t.Errorf("Enumerate() = %v, want [ilyes512/dotfiles]", got)
			}

			if got := log.all(); len(got) != 1 || got[0] != tc.want {
				t.Errorf("requests = %v, want [%s]", got, tc.want)
			}

			// affiliation=owner is what narrows the listing to what the user
			// owns rather than everything they can see. GitHub rejects a request
			// carrying both affiliation and type, so it travels alone.
			if hasAffiliation := strings.Contains(query, "affiliation=owner"); hasAffiliation != tc.authenticated {
				t.Errorf("query = %q, affiliation=owner present = %t, want %t", query, hasAffiliation, tc.authenticated)
			}
		})
	}
}

// TestEnumerateFilters walks each filter in isolation and then combined, against
// one fixed listing. Every case is answered from that listing: none of them may
// cost a request.
func TestEnumerateFilters(t *testing.T) {
	catalogue := listing(
		repoEntry{Owner: "specsnl", Name: "labelsync", HasIssues: true},
		repoEntry{Owner: "specsnl", Name: "specs-cli", HasIssues: true},
		repoEntry{Owner: "specsnl", Name: "old-thing", Archived: true},
		repoEntry{Owner: "specsnl", Name: "forked-tool", Fork: true},
		repoEntry{Owner: "specsnl", Name: "secrets", Private: true},
	)

	tests := map[string]struct {
		selector config.Selector
		want     []string
	}{
		"no filters": {
			selector: orgSelector("all", "specsnl"),
			want: []string{
				"specsnl/forked-tool", "specsnl/labelsync", "specsnl/old-thing",
				"specsnl/secrets", "specsnl/specs-cli",
			},
		},
		"skip_archived": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.SkipArchived = true

				return s
			}(),
			want: []string{"specsnl/forked-tool", "specsnl/labelsync", "specsnl/secrets", "specsnl/specs-cli"},
		},
		"skip_forks": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.SkipForks = true

				return s
			}(),
			want: []string{"specsnl/labelsync", "specsnl/old-thing", "specsnl/secrets", "specsnl/specs-cli"},
		},
		"visibility public": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.Visibility = config.VisibilityPublic

				return s
			}(),
			want: []string{"specsnl/forked-tool", "specsnl/labelsync", "specsnl/old-thing", "specsnl/specs-cli"},
		},
		"visibility private": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.Visibility = config.VisibilityPrivate

				return s
			}(),
			want: []string{"specsnl/secrets"},
		},
		"include glob": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.Include = []string{"specs*"}

				return s
			}(),
			want: []string{"specsnl/specs-cli"},
		},
		"exclude glob": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.Exclude = []string{"*-tool", "secrets"}

				return s
			}(),
			want: []string{"specsnl/labelsync", "specsnl/old-thing", "specsnl/specs-cli"},
		},
		"combined": {
			selector: func() config.Selector {
				s := orgSelector("all", "specsnl")
				s.SkipArchived = true
				s.SkipForks = true
				s.Visibility = config.VisibilityPublic
				s.Exclude = []string{"specs-*"}

				return s
			}(),
			want: []string{"specsnl/labelsync"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			client, log := enumerator(t, func(w http.ResponseWriter, _ *http.Request) {
				write(w, catalogue)
			})

			repos, err := client.Enumerate(t.Context(), []config.Selector{tc.selector}, 8)
			if err != nil {
				t.Fatalf("Enumerate() error = %v, want nil", err)
			}

			if got := names(repos); !slicesEqual(got, tc.want) {
				t.Errorf("Enumerate() = %v, want %v", got, tc.want)
			}

			if got := len(log.all()); got != 1 {
				t.Errorf("requests = %d, want 1: filtering is free and must stay free", got)
			}

			log.assertNoPerRepoGet(t)
		})
	}
}

// TestEnumerateHasIssuesIsCarriedNeverFiltered pins both halves of the GH-17
// decision: the value reaches config.Repo, and it never removes a repository.
func TestEnumerateHasIssuesIsCarriedNeverFiltered(t *testing.T) {
	client, log := enumerator(t, func(w http.ResponseWriter, _ *http.Request) {
		write(w, listing(
			repoEntry{Owner: "specsnl", Name: "labelsync", HasIssues: true},
			repoEntry{Owner: "specsnl", Name: "prs-only", HasIssues: false},
		))
	})

	selector := orgSelector("all", "specsnl")
	selector.SkipArchived = true
	selector.SkipForks = true

	repos, err := client.Enumerate(t.Context(), []config.Selector{selector}, 8)
	if err != nil {
		t.Fatalf("Enumerate() error = %v, want nil", err)
	}

	want := map[string]bool{"specsnl/labelsync": true, "specsnl/prs-only": false}
	if len(repos) != len(want) {
		t.Fatalf("Enumerate() = %v, want both repositories: issues being disabled is a note, not a skip", names(repos))
	}

	for _, repo := range repos {
		if repo.HasIssues == nil {
			t.Errorf("%s HasIssues = nil, want it known: enumeration is where the value enters", repo)

			continue
		}

		if got := *repo.HasIssues; got != want[repo.String()] {
			t.Errorf("%s HasIssues = %t, want %t", repo, got, want[repo.String()])
		}
	}

	log.assertNoPerRepoGet(t)
}

// TestEnumerateExplicitReposIssueNoRequests covers the pass-through: the config
// named the repositories outright, so there is nothing to enumerate and nothing
// to filter.
func TestEnumerateExplicitReposIssueNoRequests(t *testing.T) {
	client, log := enumerator(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an explicit repos: entry issued a request")
		write(w, `[]`)
	})

	selector := config.Selector{
		Group: "explicit",
		Kind:  config.SourceRepos,
		Repos: []config.Repo{
			{Owner: "specsnl", Name: "labelsync"},
			{Owner: "other", Name: "thing"},
		},
		Visibility: config.VisibilityAll,

		// Filters a repos: selector carries are not applied to it. A filter that
		// silently dropped a repository the config asked for by name would be a
		// surprise rather than a safety net.
		SkipArchived: true,
		Exclude:      []string{"thing"},
	}

	repos, err := client.Enumerate(t.Context(), []config.Selector{selector}, 8)
	if err != nil {
		t.Fatalf("Enumerate() error = %v, want nil", err)
	}

	if got := names(repos); !slicesEqual(got, []string{"other/thing", "specsnl/labelsync"}) {
		t.Errorf("Enumerate() = %v, want both explicit repositories", got)
	}

	// Nothing enumerated these, so nothing knows whether they have issues
	// enabled. Leaving the flag nil is what stops the diff from noting a fact
	// nobody checked.
	for _, repo := range repos {
		if repo.HasIssues != nil {
			t.Errorf("%s HasIssues = %t, want nil: an explicit entry is never enumerated", repo, *repo.HasIssues)
		}
	}

	if got := log.all(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

// TestEnumerateUnionsAndDeduplicates covers a repository two groups both select.
// That is ordinary — it is how a repository ends up with the labels of both — and
// planning it twice would apply everything twice.
func TestEnumerateUnionsAndDeduplicates(t *testing.T) {
	client, _ := enumerator(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.EscapedPath(), "/orgs/specsnl/") {
			write(w, listing(repoEntry{Owner: "specsnl", Name: "labelsync"}))

			return
		}

		write(w, listing(repoEntry{Owner: "SpecsNL", Name: "LabelSync"}))
	})

	selectors := []config.Selector{
		orgSelector("core", "specsnl"),
		orgSelector("tools", "SpecsNL"),
	}

	repos, err := client.Enumerate(t.Context(), selectors, 8)
	if err != nil {
		t.Fatalf("Enumerate() error = %v, want nil", err)
	}

	if len(repos) != 1 {
		t.Errorf("Enumerate() = %v, want one repository: the two selectors name the same one", names(repos))
	}
}

// TestEnumerateSkipsUnreachableOwners covers one mistyped org in a config that
// names two: it is recorded for the end-of-run summary, and the other org still
// syncs.
func TestEnumerateSkipsUnreachableOwners(t *testing.T) {
	client, _ := enumerator(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.EscapedPath(), "typo") {
			w.WriteHeader(http.StatusNotFound)
			write(w, errorBody("Not Found"))

			return
		}

		write(w, listing(repoEntry{Owner: "specsnl", Name: "labelsync"}))
	})

	selectors := []config.Selector{
		orgSelector("core", "specsnl"),
		orgSelector("typo", "specsnl-typo"),
	}

	repos, err := client.Enumerate(t.Context(), selectors, 8)
	if err != nil {
		t.Fatalf("Enumerate() error = %v, want nil: an unreachable owner is a skip", err)
	}

	if got := names(repos); !slicesEqual(got, []string{"specsnl/labelsync"}) {
		t.Errorf("Enumerate() = %v, want [specsnl/labelsync]", got)
	}

	if got := client.Failures().Len(); got != 1 {
		t.Fatalf("Failures().Len() = %d, want 1", got)
	}

	if got := client.Failures().All()[0].Repo; got != "specsnl-typo" {
		t.Errorf("failure names %q, want specsnl-typo", got)
	}
}

// TestEnumerateBoundsConcurrency covers the politeness half of parallel reads.
// Reads are not subject to the content-creation secondary limit, so the bound is
// about not opening twenty connections to GitHub at once — which only shows up
// against a config with many groups, and never in a two-selector test.
func TestEnumerateBoundsConcurrency(t *testing.T) {
	const (
		selectors = 12
		limit     = 3
	)

	var (
		mu      sync.Mutex
		inFlight int
		peak    int
	)

	client, _ := enumerator(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()

		// Long enough that requests genuinely overlap: without a wait the first
		// walk can finish before the second starts and any limit would pass.
		time.Sleep(5 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()

		write(w, listing(repoEntry{Owner: "specsnl", Name: "labelsync"}))
	})

	work := make([]config.Selector, 0, selectors)
	for i := range selectors {
		work = append(work, orgSelector(fmt.Sprintf("group-%02d", i), fmt.Sprintf("owner-%02d", i)))
	}

	if _, err := client.Enumerate(t.Context(), work, limit); err != nil {
		t.Fatalf("Enumerate() error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if peak > limit {
		t.Errorf("peak concurrent requests = %d, want at most %d", peak, limit)
	}
}

// TestEnumerateFailsOnRunErrors is the other side of the skip: a 401 is not one
// owner's problem, and continuing would report a successful run that synced
// nothing.
func TestEnumerateFailsOnRunErrors(t *testing.T) {
	client, _ := enumerator(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		write(w, errorBody("Bad credentials"))
	})

	_, err := client.Enumerate(t.Context(), []config.Selector{orgSelector("all", "specsnl")}, 8)
	if err == nil {
		t.Fatal("Enumerate() error = nil, want the 401 to end the run")
	}

	if errors.Is(err, labelsync.ErrRepoInaccessible) {
		t.Errorf("error = %v, want one that is not a per-repository skip", err)
	}
}

// TestSelectKeepsWhatItFilteredOut covers the half of enumeration that only
// `labelsync groups` reads: a repository the listing returned and the filters
// removed is kept, with the reason, rather than silently dropped.
func TestSelectKeepsWhatItFilteredOut(t *testing.T) {
	client, _ := enumerator(t, func(w http.ResponseWriter, _ *http.Request) {
		write(w, listing(
			repoEntry{Owner: "specsnl", Name: "labelsync", HasIssues: true},
			repoEntry{Owner: "specsnl", Name: "retired", Archived: true},
			repoEntry{Owner: "specsnl", Name: "forked", Fork: true},
		))
	})

	filtered := orgSelector("all", "specsnl")
	filtered.SkipArchived = true
	filtered.SkipForks = true

	selectors := []config.Selector{
		filtered,
		{Group: "named", Kind: config.SourceRepos, Repos: []config.Repo{{Owner: "specsnl", Name: "specs-cli"}}},
	}

	selections, err := client.Select(t.Context(), selectors, 8)
	if err != nil {
		t.Fatalf("Select() error = %v, want nil", err)
	}

	if len(selections) != len(selectors) {
		t.Fatalf("Select() returned %d selections, want %d", len(selections), len(selectors))
	}

	// The order is the caller's, not whichever parallel walk finished first —
	// the group order a report reads down.
	for i, selection := range selections {
		if selection.Selector.Group != selectors[i].Group {
			t.Errorf("selection %d is for group %q, want %q", i, selection.Selector.Group, selectors[i].Group)
		}
	}

	if got := names(selections[0].Repos); !slicesEqual(got, []string{"specsnl/labelsync"}) {
		t.Errorf("selected = %v, want [specsnl/labelsync]", got)
	}

	rejected := make(map[string]string, len(selections[0].Rejected))
	for _, entry := range selections[0].Rejected {
		rejected[entry.Repo.String()] = entry.Reason
	}

	for repo, want := range map[string]string{
		"specsnl/retired": "skip_archived",
		"specsnl/forked":  "skip_forks",
	} {
		if reason, ok := rejected[repo]; !ok || !strings.Contains(reason, want) {
			t.Errorf("rejected[%s] = %q (present %t), want it to mention %q", repo, reason, ok, want)
		}
	}

	// Nothing is enumerated for a repos selector, so nothing can be filtered out
	// of one — a repository the config named outright is never dropped.
	if len(selections[1].Rejected) != 0 {
		t.Errorf("a repos selector rejected %v", selections[1].Rejected)
	}
}

// TestSelectSurvivesAnUnlistableOwner covers the reason Select does not simply
// return an error: one mistyped org must not take the rest of the config down
// with it, and the group it belongs to has to come back empty rather than
// missing.
func TestSelectSurvivesAnUnlistableOwner(t *testing.T) {
	client, _ := enumerator(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/orgs/nobody/") {
			w.WriteHeader(http.StatusNotFound)
			write(w, errorBody("Not Found"))

			return
		}

		write(w, listing(repoEntry{Owner: "specsnl", Name: "labelsync", HasIssues: true}))
	})

	selectors := []config.Selector{orgSelector("gone", "nobody"), orgSelector("all", "specsnl")}

	selections, err := client.Select(t.Context(), selectors, 8)
	if err != nil {
		t.Fatalf("Select() error = %v, want the run to continue", err)
	}

	if len(selections[0].Repos) != 0 {
		t.Errorf("the unlistable owner selected %v, want nothing", names(selections[0].Repos))
	}

	if got := names(selections[1].Repos); !slicesEqual(got, []string{"specsnl/labelsync"}) {
		t.Errorf("the other selector = %v, want [specsnl/labelsync]", got)
	}

	if client.Failures().Len() != 1 {
		t.Errorf("Failures().Len() = %d, want 1", client.Failures().Len())
	}
}

// TestUnionDeduplicatesAndSorts pins what Enumerate is on top of Select. Two
// groups naming one repository is ordinary — it is how a repository ends up with
// the labels of both — and enumerating it twice would plan it twice.
func TestUnionDeduplicatesAndSorts(t *testing.T) {
	selections := []Selection{
		{Repos: []config.Repo{{Owner: "specsnl", Name: "labelsync"}, {Owner: "acme", Name: "widget"}}},
		{Repos: []config.Repo{{Owner: "SpecsNL", Name: "Labelsync"}}},
	}

	want := []string{"acme/widget", "specsnl/labelsync"}
	if got := names(Union(selections)); !slicesEqual(got, want) {
		t.Errorf("Union() = %v, want %v", got, want)
	}
}
