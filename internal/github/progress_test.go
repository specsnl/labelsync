package github

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/util/progress/progresstest"
)

// TestSelectReportsProgress covers the enumeration line: it opens at zero before
// the first request, counts every selector — including one whose owner could not
// be listed, which still finished — and is taken down when the walk is over, so
// the table that follows starts on a clean row.
func TestSelectReportsProgress(t *testing.T) {
	recorder := &progresstest.Recorder{}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.EscapedPath(), "typo") {
			w.WriteHeader(http.StatusNotFound)
			write(w, errorBody("Not Found"))

			return
		}

		write(w, listing(
			repoEntry{Owner: "specsnl", Name: "labelsync", HasIssues: true},
			repoEntry{Owner: "specsnl", Name: "specs-cli", HasIssues: true},
		))
	}), WithProgress(recorder))

	selectors := []config.Selector{orgSelector("core", "specsnl"), orgSelector("broken", "typo")}

	// One at a time, so the order of the two completions is the selectors' order.
	if _, err := client.Select(t.Context(), selectors, 1); err != nil {
		t.Fatalf("Select() error = %v, want nil", err)
	}

	want := []string{"enumerating 0/2 0", "enumerating 1/2 2", "enumerating 2/2 2", "done"}

	if got := recorder.Calls(); !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
}

// TestReadLabelsReportsProgress is the read line: one step per repository,
// reached or skipped, ending with the line taken down before the plan is
// rendered over it.
func TestReadLabelsReportsProgress(t *testing.T) {
	recorder := &progresstest.Recorder{}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "forbidden") {
			w.WriteHeader(http.StatusForbidden)
			write(w, errorBody("Forbidden"))

			return
		}

		write(w, `[]`)
	}), WithProgress(recorder))

	repos := []config.Repo{
		{Owner: "specsnl", Name: "one"},
		{Owner: "specsnl", Name: "forbidden"},
		{Owner: "specsnl", Name: "three"},
	}

	if _, err := client.ReadLabels(t.Context(), repos, 8); err != nil {
		t.Fatalf("ReadLabels() error = %v, want nil", err)
	}

	want := []string{"reading 0/3", "reading 1/3", "reading 2/3", "reading 3/3", "done"}

	if got := recorder.Calls(); !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
}

// TestReadLabelsTakesTheLineDownOnFailure is the run that ends early: the error
// is printed next, and it must not be printed on top of a half-drawn count.
func TestReadLabelsTakesTheLineDownOnFailure(t *testing.T) {
	recorder := &progresstest.Recorder{}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		write(w, errorBody("Bad credentials"))
	}), WithProgress(recorder))

	if _, err := client.ReadLabels(t.Context(), []config.Repo{{Owner: "specsnl", Name: "one"}}, 1); err == nil {
		t.Fatal("ReadLabels() error = nil, want the 401")
	}

	if got := recorder.Last(); got != "done" {
		t.Errorf("last progress call = %q, want done", got)
	}
}
