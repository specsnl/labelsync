package cmd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// renameConfig is one repository whose labels need one of everything a rename
// run can produce: the rename itself, a squatter to recolour, a label to create,
// and a colour and description to converge on the renamed label. Four writes is
// the smallest plan that can show the rename going out *first*.
const renameConfig = `
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
    description: "New functionality"
`

// renameRepo is the repository renameConfig targets.
const renameRepo = "specsnl/example-website"

// driftedForRename is what that repository holds before the run: the label the
// config renames, under its old name, colour and wording — and an unconfigured
// label sitting on a configured colour, so the plan has a recolour to order
// against.
func driftedForRename() map[string][]storedLabel {
	return map[string][]storedLabel{renameRepo: {
		{Name: "bug", Color: "111111", Description: "Old wording"},
		{Name: "wontfix", Color: "a2eeef", Description: "Sitting on a configured colour"},
	}}
}

// issueLabels wraps the stateful label fixture with the one thing a label store
// cannot show on its own: what an issue is labelled with.
//
// That is the whole point of renames being a `PATCH` carrying `new_name` rather
// than a delete plus a create, and it is the only property of the feature a unit
// test cannot reach — the planner and the applier both stop at the request. So
// the fixture models GitHub's side of it: a `PATCH` carries the association
// across to the new name, a `DELETE` takes it off the issue for good, and a
// created label starts out on nothing.
//
// It is a model and not a proof. What GitHub actually does with `new_name` is
// checked by hand against a live repository, and recorded in the pull request.
type issueLabels struct {
	*labelStore

	mu sync.Mutex

	// on is the set of labels the repository's one issue carries, keyed by
	// lower-cased name, because GitHub's label identity is case-insensitive.
	on map[string]bool
}

// labelledIssue puts names on the fixture's issue.
func labelledIssue(store *labelStore, names ...string) *issueLabels {
	tracked := &issueLabels{labelStore: store, on: make(map[string]bool, len(names))}

	for _, name := range names {
		tracked.on[strings.ToLower(name)] = true
	}

	return tracked
}

// carries returns the issue's labels in name order.
func (i *issueLabels) carries() []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	out := slices.Collect(maps.Keys(i.on))
	sort.Strings(out)

	return out
}

// ServeHTTP delegates to the label store and then moves the associations the way
// GitHub would have. The response is buffered so that only a request the store
// accepted changes them: a `PATCH` answered with a 404 renamed nothing, and an
// issue that lost its label to it would be the fixture inventing damage.
func (i *issueLabels) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any

	if r.Method == http.MethodPatch {
		// Read and put back: the store decodes the same body to decide what the
		// patch changes, so consuming it here would leave it applying nothing.
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))

		_ = json.Unmarshal(raw, &body)
	}

	recorder := httptest.NewRecorder()

	i.labelStore.ServeHTTP(recorder, r)

	if recorder.Code < http.StatusMultipleChoices {
		_, name, _ := parseLabelPath(r.URL.Path)

		switch r.Method {
		case http.MethodPatch:
			if to, ok := body["new_name"].(string); ok && to != "" {
				i.rename(name, to)
			}

		case http.MethodDelete:
			i.remove(name)
		}
	}

	maps.Copy(w.Header(), recorder.Header())
	w.WriteHeader(recorder.Code)

	_, _ = w.Write(recorder.Body.Bytes())
}

// rename carries the association across, which is what `new_name` does.
func (i *issueLabels) rename(from, to string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.on[strings.ToLower(from)] {
		delete(i.on, strings.ToLower(from))

		i.on[strings.ToLower(to)] = true
	}
}

// remove takes the label off the issue, which is what a delete does and nothing
// restores.
func (i *issueLabels) remove(name string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.on, strings.ToLower(name))
}

// TestSync_RenameKeepsTheIssueAssociation is the reason renames exist as a
// concept. The label the issue carries has to still be on it afterwards, under
// its new name — which is true of a `PATCH` and of nothing else, so the run must
// also never have deleted or re-created it.
func TestSync_RenameKeepsTheIssueAssociation(t *testing.T) {
	store := newStore(driftedForRename())
	tracked := labelledIssue(store, "bug")

	app, flags := fakeGitHub(t, tracked)

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, renameConfig), flags, "sync")...); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if got, want := tracked.carries(), []string{"type: bug"}; !slices.Equal(got, want) {
		t.Errorf("the issue carries %v, want %v: the rename dropped the association", got, want)
	}

	// The association survives because of *how* the rename was sent, so the
	// requests are the assertion as much as the outcome is. A delete plus a
	// create would leave the repository looking identical and the issue bare.
	for _, write := range store.writes() {
		if strings.HasPrefix(write, http.MethodDelete+" ") {
			t.Errorf("the run issued %q: a rename is a PATCH and never a delete plus a create", write)
		}
	}

	held := store.snapshot(renameRepo)

	want := storedLabel{Name: "type: bug", Color: "d73a4a", Description: "Something isn't working"}
	if !slices.Contains(held, want) {
		t.Errorf("the repository holds %+v, want the renamed label %+v", held, want)
	}
}

// TestSync_RenamesGoOutBeforeEveryOtherWrite pins the ordering guarantee at the
// only level that can observe it end to end: the requests the API saw. The
// planner emits renames first and the applier preserves the order, and a
// regression in either one is a repository where a create raced the rename to
// the same name and lost with a 422.
func TestSync_RenamesGoOutBeforeEveryOtherWrite(t *testing.T) {
	store := newStore(driftedForRename())

	app, flags := fakeGitHub(t, store)

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, renameConfig), flags, "sync")...); err != nil {
		t.Fatalf("sync: %v", err)
	}

	writes := store.writes()

	if len(writes) != 4 {
		t.Fatalf("the run issued %v, want four writes: the rename, the recolour, the create, the update", writes)
	}

	if got, want := writes[0], http.MethodPatch+" /repos/"+renameRepo+"/labels/bug"; got != want {
		t.Errorf("the first write is %q, want %q: renames go out before everything else", got, want)
	}

	// And nothing else is a rename, which is what makes the first-write assertion
	// mean "the rename pass ran first" rather than "one of the renames happened
	// to sort first".
	for _, write := range writes[1:] {
		if strings.HasSuffix(write, "/labels/bug") {
			t.Errorf("write %q touches the old name after the rename landed", write)
		}
	}
}

// TestSync_AnAppliedRenameIsANoOpTheSecondTime is convergence for the one action
// that cannot be re-applied: the second run finds no label under the old name,
// and a rename that were emitted anyway would fail with a 404 rather than do
// nothing.
func TestSync_AnAppliedRenameIsANoOpTheSecondTime(t *testing.T) {
	store := newStore(driftedForRename())
	tracked := labelledIssue(store, "bug")

	app, flags := fakeGitHub(t, tracked)
	config := writeConfig(t, renameConfig)

	if _, _, _, err := runApp(t, app, nil, args(config, flags, "sync")...); err != nil {
		t.Fatalf("sync: %v", err)
	}

	before := store.snapshot(renameRepo)
	applied := len(store.writes())

	_, stdout, _, err := runApp(t, app, nil, args(config, flags, "sync")...)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if got := store.writes(); len(got) != applied {
		t.Errorf("the second run issued %v, want nothing beyond the first run's %d writes", got[applied:], applied)
	}

	if !strings.Contains(stdout, "applied: 0 created · 0 updated · 0 deleted") {
		t.Errorf("the second run is not converged:\n%s", stdout)
	}

	if got := store.snapshot(renameRepo); !slices.Equal(got, before) {
		t.Errorf("the repository holds %+v, want it left exactly as %+v", got, before)
	}

	if got, want := tracked.carries(), []string{"type: bug"}; !slices.Equal(got, want) {
		t.Errorf("the issue carries %v, want %v after a second run", got, want)
	}
}

// TestSync_RenameOntoATakenNameIsSkippedNotForced is the one collision the API
// would answer with a 422. The repository already holds the target, so the
// rename is skipped silently and the label the config configured converges on
// its own — and the old name is left alone rather than deleted, because append
// mode never deletes.
func TestSync_RenameOntoATakenNameIsSkippedNotForced(t *testing.T) {
	store := newStore(map[string][]storedLabel{renameRepo: {
		{Name: "bug", Color: "111111", Description: "The one that is not renamed"},
		{Name: "Type: Bug", Color: "222222", Description: "Already there, in another casing"},
	}})

	tracked := labelledIssue(store, "bug", "Type: Bug")

	app, flags := fakeGitHub(t, tracked)

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, renameConfig), flags, "sync")...); err != nil {
		t.Fatalf("sync: %v", err)
	}

	names := make([]string, 0, 3)
	for _, label := range store.snapshot(renameRepo) {
		names = append(names, label.Name)
	}

	want := []string{"bug", "type: bug", "type: feature"}
	if !slices.Equal(names, want) {
		t.Errorf("the repository holds %v, want %v: the taken target converged and the source was left alone", names, want)
	}

	// Both associations survive: the target was renamed for casing, which is the
	// same PATCH, and the source was never touched at all.
	if got, want := tracked.carries(), []string{"bug", "type: bug"}; !slices.Equal(got, want) {
		t.Errorf("the issue carries %v, want %v", got, want)
	}
}
