package cmd_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
)

// labelStore is a GitHub that remembers. Applying is the first code in the
// project that writes, so the fixture has to hold state: a server that answered
// every list with the same fixed body could not tell an apply that worked from
// one that sent its requests into a void, and could not answer the convergence
// question at all.
//
// It implements the four things an apply touches: listing, creating with the
// 422 GitHub returns for a name already present in any casing, patching the
// fields a body carries, and the free budget reading.
type labelStore struct {
	mu sync.Mutex

	// labels is owner/repo → name → label, with the name held as the repository
	// holds it. Lookup is case-insensitive, because GitHub's is.
	labels map[string]map[string]storedLabel

	// forbid names a repository that answers every request with a 403, from the
	// call at index reject onwards — which is how a failure is placed *partway*
	// through a repository rather than at its first request.
	forbid string
	reject int

	// budget is what GET /rate_limit reports, and remaining is what every other
	// response carries in its headers.
	budget int

	seen  int
	calls []string
}

type storedLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

func newStore(repos map[string][]storedLabel) *labelStore {
	store := &labelStore{labels: make(map[string]map[string]storedLabel, len(repos)), budget: 5000}

	for repo, labels := range repos {
		store.labels[repo] = make(map[string]storedLabel, len(labels))

		for _, label := range labels {
			store.labels[repo][strings.ToLower(label.Name)] = label
		}
	}

	return store
}

// snapshot returns one repository's labels in name order, which is what an
// assertion compares against.
func (s *labelStore) snapshot(repo string) []storedLabel {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]storedLabel, 0, len(s.labels[repo]))
	for _, label := range s.labels[repo] {
		out = append(out, label)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// writes returns the recorded non-GET requests, which is how a test asserts that
// a dry run wrote nothing and that a converged run wrote nothing either.
func (s *labelStore) writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []string

	for _, call := range s.calls {
		if !strings.HasPrefix(call, http.MethodGet+" ") {
			out = append(out, call)
		}
	}

	return out
}

func (s *labelStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/rate_limit" {
		s.mu.Lock()
		budget := s.budget
		s.mu.Unlock()

		writeJSON(w, fmt.Sprintf(
			`{"resources":{"core":{"limit":5000,"remaining":%d,"reset":4102444800}}}`, budget))

		return
	}

	repo, name, ok := parseLabelPath(r.URL.Path)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, `{"message":"Not Found"}`)

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, r.Method+" "+r.URL.Path)

	if repo == s.forbid {
		s.seen++

		if s.seen > s.reject {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, `{"message":"Forbidden"}`)

			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		s.list(w, repo)
	case http.MethodPost:
		s.create(w, r, repo)
	case http.MethodPatch:
		s.patch(w, r, repo, name)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *labelStore) list(w http.ResponseWriter, repo string) {
	held, ok := s.labels[repo]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, `{"message":"Not Found"}`)

		return
	}

	out := make([]storedLabel, 0, len(held))
	for _, label := range held {
		out = append(out, label)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	body, _ := json.Marshal(out)
	writeJSON(w, string(body))
}

// create is GitHub's own rule: label names are case-insensitively unique, so a
// create for a name already present is a 422 already_exists rather than a second
// label.
func (s *labelStore) create(w http.ResponseWriter, r *http.Request, repo string) {
	var body storedLabel

	_ = json.NewDecoder(r.Body).Decode(&body)

	if _, exists := s.labels[repo][strings.ToLower(body.Name)]; exists {
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(w, `{"message":"Validation Failed","errors":[{"resource":"Label","code":"already_exists","field":"name"}]}`)

		return
	}

	s.labels[repo][strings.ToLower(body.Name)] = body

	w.WriteHeader(http.StatusCreated)

	out, _ := json.Marshal(body)
	writeJSON(w, string(out))
}

// patch applies only the fields the body carried. An absent field is left alone,
// which is the behaviour the whole LabelPatch pointer dance exists to reach.
func (s *labelStore) patch(w http.ResponseWriter, r *http.Request, repo, name string) {
	var body map[string]any

	_ = json.NewDecoder(r.Body).Decode(&body)

	key := strings.ToLower(name)

	label, exists := s.labels[repo][key]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, `{"message":"Not Found"}`)

		return
	}

	if v, ok := body["color"].(string); ok {
		label.Color = v
	}

	if v, ok := body["description"].(string); ok {
		label.Description = v
	}

	if v, ok := body["new_name"].(string); ok && v != "" {
		delete(s.labels[repo], key)

		label.Name = v
		key = strings.ToLower(v)
	}

	s.labels[repo][key] = label

	out, _ := json.Marshal(label)
	writeJSON(w, string(out))
}

// parseLabelPath splits /repos/{owner}/{repo}/labels[/{name}].
func parseLabelPath(path string) (repo, name string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" || parts[3] != "labels" {
		return "", "", false
	}

	repo = parts[1] + "/" + parts[2]

	if len(parts) == 5 {
		name = parts[4]
	}

	return repo, name, len(parts) <= 5
}

// emptyRepos is two repositories holding nothing, so every configured label is a
// create.
func emptyRepos() map[string][]storedLabel {
	return map[string][]storedLabel{
		"specsnl/example-website":  {},
		"specsnl/example-platform": {},
	}
}

// TestSync_AppliesAPlanAndConverges is the whole point of the command. The
// second run is not decoration: convergence is asserted rather than assumed,
// because a create that quietly failed and a create that landed look identical
// from one run.
func TestSync_AppliesAPlanAndConverges(t *testing.T) {
	store := newStore(emptyRepos())

	app, flags := fakeGitHub(t, store)
	config := writeConfig(t, syncConfig)

	_, stdout, _, err := runApp(t, app, nil, args(config, flags, "sync")...)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	want := []storedLabel{
		{Name: "type: bug", Color: "d73a4a", Description: "Something isn't working"},
		{Name: "type: feature", Color: "a2eeef", Description: "New functionality"},
	}

	for repo := range emptyRepos() {
		if got := store.snapshot(repo); !slices.Equal(got, want) {
			t.Errorf("%s holds %+v, want %+v", repo, got, want)
		}
	}

	if !strings.Contains(stdout, "applied: 4 created") {
		t.Errorf("stdout = %q, want the applied summary", stdout)
	}

	// Immediately again, over the same repositories the first run left behind.
	_, stdout, _, err = runApp(t, app, nil, args(config, flags, "sync")...)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if !strings.Contains(stdout, "0 created · 0 updated · 0 deleted · 4 unchanged") {
		t.Errorf("the second run is not converged:\n%s", stdout)
	}

	if !strings.Contains(stdout, "applied: 0 created · 0 updated · 4 unchanged") {
		t.Errorf("stdout = %q, want an applied summary that wrote nothing", stdout)
	}
}

// TestSync_AppliesEveryKindOfAction walks a repository that needs one of
// everything append mode does: a rename, a squatter recoloured off a configured
// colour, a create, a colour correction, and a description cleared.
func TestSync_AppliesEveryKindOfAction(t *testing.T) {
	const config = `
version: 1

groups:
  all:
    repos: [specsnl/example-website]

defaults:
  groups: [all]

renames:
  - from: "bug"
    to: "type: bug"

labels:
  - name: "type: bug"
    color: "d73a4a"
    description: "Something isn't working"
  - name: "type: feature"
    color: "a2eeef"
`

	store := newStore(map[string][]storedLabel{"specsnl/example-website": {
		{Name: "bug", Color: "111111", Description: "Old wording"},
		{Name: "wontfix", Color: "a2eeef", Description: "Sitting on a configured colour"},
	}})

	app, flags := fakeGitHub(t, store)

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, config), flags, "sync")...); err != nil {
		t.Fatalf("sync: %v", err)
	}

	held := store.snapshot("specsnl/example-website")

	byName := make(map[string]storedLabel, len(held))
	for _, label := range held {
		byName[label.Name] = label
	}

	if len(held) != 3 {
		t.Fatalf("repository holds %+v, want three labels: nothing is ever deleted", held)
	}

	// The rename is a PATCH, so it kept the label rather than replacing it, and
	// the colour and description converged in the same pass.
	if got := byName["type: bug"]; got != (storedLabel{
		Name: "type: bug", Color: "d73a4a", Description: "Something isn't working",
	}) {
		t.Errorf(`"type: bug" = %+v, want the renamed, recoloured, redescribed label`, got)
	}

	// The configured label with no description gets one cleared, not left alone.
	if got := byName["type: feature"]; got.Color != "a2eeef" || got.Description != "" {
		t.Errorf(`"type: feature" = %+v, want a2eeef and an empty description`, got)
	}

	// The squatter moved off the configured colour, and kept everything else.
	squatter := byName["wontfix"]
	if squatter.Color == "a2eeef" {
		t.Errorf("wontfix was not recoloured off the configured colour: %+v", squatter)
	}

	if squatter.Description != "Sitting on a configured colour" {
		t.Errorf("wontfix description = %q, want it untouched: a recolour changes the colour and nothing else",
			squatter.Description)
	}
}

// TestSync_AppliesTheRestWhenOneRepositoryFails is the per-repository failure
// rule where it costs the most: one archived repository partway through a set
// must not undo or abandon the work the others already took.
func TestSync_AppliesTheRestWhenOneRepositoryFails(t *testing.T) {
	store := newStore(emptyRepos())
	store.forbid = "specsnl/example-platform"

	// The listing goes through; the first write does not, so the failure lands
	// partway rather than at the door.
	store.reject = 1

	app, flags := fakeGitHub(t, store)
	config := writeConfig(t, syncConfig)

	_, stdout, stderr, err := runApp(t, app, nil, args(config, flags, "sync")...)
	if code := exit.Of(err); code != exit.Skipped {
		t.Fatalf("exit code = %s, want %s (error: %v)", code, exit.Skipped, err)
	}

	if got, want := len(store.snapshot("specsnl/example-website")), 2; got != want {
		t.Errorf("the reachable repository holds %d labels, want %d", got, want)
	}

	if got := store.snapshot("specsnl/example-platform"); len(got) != 0 {
		t.Errorf("the failed repository holds %+v, want nothing", got)
	}

	if !strings.Contains(stderr, "specsnl/example-platform") {
		t.Errorf("stderr = %q, want the skipped repository named in the summary", stderr)
	}

	// The applied summary reports what the run *did*, which on a partial run is
	// not what its plan proposed.
	if !strings.Contains(stdout, "applied: 2 created") {
		t.Errorf("stdout = %q, want the two labels that landed", stdout)
	}
}

// TestSync_AlreadyExistsBecomesAnUpdate covers the plan computed against state
// that has since changed, and case-only drift, which arrive as the same 422.
func TestSync_AlreadyExistsBecomesAnUpdate(t *testing.T) {
	const config = `
version: 1

groups:
  all:
    repos: [specsnl/example-website]

defaults:
  groups: [all]

labels:
  - name: "Type: Bug"
    color: "d73a4a"
    description: "Something isn't working"
`

	store := newStore(map[string][]storedLabel{"specsnl/example-website": {}})

	// The listing is answered with nothing, and the label appears immediately
	// after: a plan computed against state that has since changed, which is one
	// of the two ordinary situations that produce a 422 already_exists. Its
	// casing differs from the config's, which is the third.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/labels") {
			writeJSON(w, `[]`)

			store.mu.Lock()
			store.labels["specsnl/example-website"]["type: bug"] = storedLabel{Name: "type: bug", Color: "111111"}
			store.mu.Unlock()

			return
		}

		store.ServeHTTP(w, r)
	})

	app, flags := fakeGitHub(t, handler)

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, config), flags, "sync")...); err != nil {
		t.Fatalf("sync: %v", err)
	}

	want := []storedLabel{{Name: "Type: Bug", Color: "d73a4a", Description: "Something isn't working"}}
	if got := store.snapshot("specsnl/example-website"); !slices.Equal(got, want) {
		t.Errorf("repository holds %+v, want %+v: the 422 became an update", got, want)
	}
}

// TestSync_DryRunAgainstAStatefulServerWritesNothing is the promise --dry-run
// makes, checked against a server that would have recorded a write.
func TestSync_DryRunAgainstAStatefulServerWritesNothing(t *testing.T) {
	store := newStore(emptyRepos())

	app, flags := fakeGitHub(t, store)
	config := writeConfig(t, syncConfig)

	_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run")...)
	if code := exit.Of(err); code != exit.Drift {
		t.Fatalf("exit code = %s, want %s", code, exit.Drift)
	}

	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("a dry run issued %v, want nothing", writes)
	}
}

// TestSync_RefusesAnApplyThatCannotFinish is the free reading turned into a
// decision. A run that spends half its plan and then stalls until the budget
// resets has left every repository it touched in a state nobody asked for.
func TestSync_RefusesAnApplyThatCannotFinish(t *testing.T) {
	store := newStore(map[string][]storedLabel{"specsnl/example-website": {}})

	// Thirty labels to create, and a budget above the limiter's own threshold —
	// which would otherwise pause the *reads* first, and this is a test about the
	// refusal rather than about the pause.
	const labels = 30

	store.budget = 25

	app, flags := fakeGitHub(t, store)

	_, _, _, err := runApp(t, app, nil, args(writeConfig(t, manyLabels(labels)), flags, "sync")...)
	if kind := labelsync.KindOf(err); kind != "budget_exhausted" {
		t.Fatalf("error_kind = %q, want budget_exhausted (error: %v)", kind, err)
	}

	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("a refused apply issued %v, want nothing at all", writes)
	}

	// The answer to "raise it how?" has to be in the message.
	for _, want := range []string{"30 writes", "25 requests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// manyLabels builds a config declaring n labels over one repository, for the
// tests that need a plan bigger than a rate-limit budget. Names and colours are
// derived from the index, because both have to be unique across a file.
func manyLabels(n int) string {
	var b strings.Builder

	b.WriteString("version: 1\n\ngroups:\n  all:\n    repos: [specsnl/example-website]\n\n" +
		"defaults:\n  groups: [all]\n\nlabels:\n")

	for i := range n {
		fmt.Fprintf(&b, "  - name: %q\n    color: %q\n", fmt.Sprintf("label-%02d", i), fmt.Sprintf("%06x", 0x101010+i*0x030507))
	}

	return b.String()
}

// TestSync_RefusesToApplyAPrune is the same honesty the whole-command refusal
// used to carry, narrowed to the mode whose write path is still unlanded: a
// command that lists removal candidates and removes none of them is the one
// outcome a user could not detect.
func TestSync_RefusesToApplyAPrune(t *testing.T) {
	store := newStore(emptyRepos())

	app, flags := fakeGitHub(t, store)
	config := writeConfig(t, syncConfig)

	_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--mode", "prune")...)
	if err == nil {
		t.Fatal("want an error for an unlanded prune apply, got none")
	}

	if !strings.Contains(err.Error(), "--dry-run") {
		t.Errorf("error = %q, want it to point at --dry-run", err)
	}

	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("a refused prune issued %v, want nothing", writes)
	}
}

// TestSync_AppliedSummaryIsATypedRecord keeps the NDJSON stream one typed object
// per line. A consumer discriminates on "kind", and what a run did is a
// different question from what it planned.
func TestSync_AppliedSummaryIsATypedRecord(t *testing.T) {
	store := newStore(emptyRepos())

	app, flags := fakeGitHub(t, store)
	config := writeConfig(t, syncConfig)

	_, stdout, _, err := runApp(t, app, nil, args(config, flags, "--output", "json", "sync")...)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	var applied map[string]any

	for _, line := range jsonLines(t, stdout) {
		if line["kind"] == "applied" {
			applied = line
		}
	}

	if applied == nil {
		t.Fatalf("stdout carries no applied record:\n%s", stdout)
	}

	for field, want := range map[string]any{"created": 4.0, "updated": 0.0, "unchanged": 0.0, "repositories": 2.0} {
		if got := applied[field]; got != want {
			t.Errorf("applied[%q] = %v, want %v", field, got, want)
		}
	}
}
