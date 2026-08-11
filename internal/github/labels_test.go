package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/plan"
)

// recorded is one request the fake API saw, reduced to what the assertions here
// care about: which endpoint was addressed, and what was sent to it.
type recorded struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

// recorder wraps a handler and files every request that reaches it. The path is
// taken escaped, because whether a label name containing a slash addressed one
// segment or two is exactly the thing under test.
func recorder(log *[]recorded, next func(w http.ResponseWriter, r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery}

		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&entry.Body)
		}

		*log = append(*log, entry)

		next(w, r)
	})
}

// TestListLabelsPaginates covers the one thing a truncated list would do
// silently: a repository whose labels stop at 100 would have the rest planned as
// creates, which then fail as already_exists on every single run.
func TestListLabelsPaginates(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			write(w, `[{"name":"docs","color":"0075ca","description":"Documentation"}]`)

			return
		}

		// The URL in a Link header is only read for its page parameter, so it
		// does not have to point at this server.
		w.Header().Set("Link", `<https://api.github.com/repositories/1/labels?page=2>; rel="next"`)
		write(w, `[{"name":"bug","color":"d73a4a","description":"Something is broken"}]`)
	})

	client, _ := newTestClient(t, handler)

	labels, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
	if err != nil {
		t.Fatalf("ListLabels() error = %v, want nil", err)
	}

	want := []Label{
		{Name: "bug", Color: "d73a4a", Description: "Something is broken"},
		{Name: "docs", Color: "0075ca", Description: "Documentation"},
	}

	if len(labels) != len(want) {
		t.Fatalf("ListLabels() returned %d labels, want %d: %+v", len(labels), len(want), labels)
	}

	for i, got := range labels {
		if got != want[i] {
			t.Errorf("label %d = %+v, want %+v", i, got, want[i])
		}
	}

	if len(log) != 2 {
		t.Fatalf("requests = %d, want 2: %+v", len(log), log)
	}

	if want := "100"; log[0].Query != "per_page="+want {
		t.Errorf("first request query = %q, want per_page=%s", log[0].Query, want)
	}
}

// TestLabelOperationsSkipInaccessibleRepositories covers the three per-repository
// statuses on every operation: each one is collected and returned as a skip, so a
// run over fifty repositories does not end because one of them is archived.
func TestLabelOperationsSkipInaccessibleRepositories(t *testing.T) {
	label := Label{Name: "bug", Color: "d73a4a"}

	operations := map[string]func(client *Client) error{
		"list": func(client *Client) error {
			_, err := client.ListLabels(t.Context(), "specsnl", "labelsync")

			return err
		},
		"create": func(client *Client) error {
			return client.CreateLabel(t.Context(), "specsnl", "labelsync", label)
		},
		"update": func(client *Client) error {
			return client.UpdateLabel(t.Context(), "specsnl", "labelsync", "bug", label)
		},
		"delete": func(client *Client) error {
			return client.DeleteLabel(t.Context(), "specsnl", "labelsync", "bug")
		},
	}

	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		for name, operation := range operations {
			t.Run(fmt.Sprintf("%s/%d", name, status), func(t *testing.T) {
				handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					write(w, errorBody("nope"))
				})

				client, _ := newTestClient(t, handler)

				err := operation(client)
				if !errors.Is(err, labelsync.ErrRepoInaccessible) {
					t.Fatalf("error = %v, want one wrapping ErrRepoInaccessible", err)
				}

				if got := client.Failures().Len(); got != 1 {
					t.Errorf("Failures().Len() = %d, want 1", got)
				}
			})
		}
	}
}

// TestCreateLabelSendsTheConfiguredValues checks the request shape, including an
// empty description: descriptions are authoritative, and a create that omitted
// the field would be a create the API is free to interpret.
func TestCreateLabelSendsTheConfiguredValues(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		write(w, `{"name":"bug","color":"d73a4a"}`)
	})

	client, _ := newTestClient(t, handler)

	err := client.CreateLabel(t.Context(), "specsnl", "labelsync", Label{Name: "bug", Color: "d73a4a"})
	if err != nil {
		t.Fatalf("CreateLabel() error = %v, want nil", err)
	}

	if len(log) != 1 {
		t.Fatalf("requests = %d, want 1: %+v", len(log), log)
	}

	if log[0].Method != http.MethodPost || log[0].Path != "/repos/specsnl/labelsync/labels" {
		t.Errorf("request = %s %s, want POST /repos/specsnl/labelsync/labels", log[0].Method, log[0].Path)
	}

	for field, want := range map[string]any{"name": "bug", "color": "d73a4a", "description": ""} {
		if got, ok := log[0].Body[field]; !ok || got != want {
			t.Errorf("body[%q] = %v (present %t), want %v", field, got, ok, want)
		}
	}
}

// TestCreateLabelReclassifies422AsUpdate is the case-only drift path: a
// repository holding `bug` refuses to create `Bug`, and the tool converges by
// patching the label it already has rather than failing the repository.
func TestCreateLabelReclassifies422AsUpdate(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusUnprocessableEntity)
			write(w, errorBody("Validation Failed", "already_exists"))

			return
		}

		write(w, `{"name":"Bug","color":"d73a4a"}`)
	})

	client, _ := newTestClient(t, handler)

	label := Label{Name: "Bug", Color: "d73a4a", Description: "Something is broken"}

	if err := client.CreateLabel(t.Context(), "specsnl", "labelsync", label); err != nil {
		t.Fatalf("CreateLabel() error = %v, want nil", err)
	}

	if len(log) != 2 {
		t.Fatalf("requests = %d, want 2 (the create and the update): %+v", len(log), log)
	}

	patch := log[1]
	if patch.Method != http.MethodPatch || patch.Path != "/repos/specsnl/labelsync/labels/Bug" {
		t.Errorf("second request = %s %s, want PATCH /repos/specsnl/labelsync/labels/Bug", patch.Method, patch.Path)
	}

	if got := patch.Body["new_name"]; got != label.Name {
		t.Errorf("body[new_name] = %v, want %q", got, label.Name)
	}

	// An already_exists is not a repository that could not be reached, and
	// recording it as one would put a healthy repository in the skipped summary.
	if got := client.Failures().Len(); got != 0 {
		t.Errorf("Failures().Len() = %d, want 0", got)
	}
}

// TestUpdateLabelRenamesWithNewName pins the field name. go-github's EditLabel
// sends `name`, which GitHub's update endpoint ignores — it would return 200
// having renamed nothing, and the drift would come back every run.
func TestUpdateLabelRenamesWithNewName(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, _ *http.Request) {
		write(w, `{"name":"defect"}`)
	})

	client, _ := newTestClient(t, handler)

	label := Label{Name: "defect", Color: "d73a4a"}

	if err := client.UpdateLabel(t.Context(), "specsnl", "labelsync", "bug", label); err != nil {
		t.Fatalf("UpdateLabel() error = %v, want nil", err)
	}

	if len(log) != 1 {
		t.Fatalf("requests = %d, want 1: %+v", len(log), log)
	}

	// The label is addressed by the name the repository was observed to hold, so
	// the request stays consistent with the state the plan was computed against.
	if got, want := log[0].Path, "/repos/specsnl/labelsync/labels/bug"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	if _, sent := log[0].Body["name"]; sent {
		t.Errorf("body carries name = %v, want new_name only", log[0].Body["name"])
	}

	for field, want := range map[string]any{"new_name": "defect", "color": "d73a4a", "description": ""} {
		if got, ok := log[0].Body[field]; !ok || got != want {
			t.Errorf("body[%q] = %v (present %t), want %v", field, got, ok, want)
		}
	}
}

// TestPatchLabelSendsOnlyTheFieldsItChanges is the recoloured squatter: a
// colour-only update that must not touch the name or the description of a label
// nobody configured. A body carrying the two zero values would rename it to ""
// and clear a description the config has no opinion about.
func TestPatchLabelSendsOnlyTheFieldsItChanges(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, _ *http.Request) {
		write(w, `{"name":"wontfix"}`)
	})

	client, _ := newTestClient(t, handler)

	color := "16a3c4"
	if err := client.PatchLabel(t.Context(), "specsnl", "labelsync", "wontfix", LabelPatch{Color: &color}); err != nil {
		t.Fatalf("PatchLabel() error = %v, want nil", err)
	}

	if len(log) != 1 {
		t.Fatalf("requests = %d, want 1: %+v", len(log), log)
	}

	if got, want := len(log[0].Body), 1; got != want {
		t.Fatalf("body carries %d fields, want %d: %+v", got, want, log[0].Body)
	}

	if got := log[0].Body["color"]; got != color {
		t.Errorf("body[color] = %v, want %q", got, color)
	}
}

// TestPatchLabelClearsADescription is the other half of the pointer: an empty
// string is a value the config legitimately asks for, and omitempty on a plain
// string would silently turn "clear it" into "leave it".
func TestPatchLabelClearsADescription(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, _ *http.Request) {
		write(w, `{"name":"bug"}`)
	})

	client, _ := newTestClient(t, handler)

	empty := ""
	if err := client.PatchLabel(t.Context(), "specsnl", "labelsync", "bug", LabelPatch{Description: &empty}); err != nil {
		t.Fatalf("PatchLabel() error = %v, want nil", err)
	}

	got, sent := log[0].Body["description"]
	if !sent || got != "" {
		t.Errorf("body[description] = %v (present %t), want an explicit empty string", got, sent)
	}
}

// TestLabelNamesAreEscapedInPaths covers `area/api`, which is an ordinary label
// name and two path segments if interpolated raw — addressing, and in the delete
// case destroying, something else entirely.
func TestLabelNamesAreEscapedInPaths(t *testing.T) {
	const escaped = "/repos/specsnl/labelsync/labels/area%2Fapi"

	tests := map[string]struct {
		call func(client *Client) error
		want string
	}{
		"update": {
			call: func(client *Client) error {
				return client.UpdateLabel(t.Context(), "specsnl", "labelsync", "area/api", Label{Name: "area/api"})
			},
			want: http.MethodPatch,
		},
		"delete": {
			call: func(client *Client) error {
				return client.DeleteLabel(t.Context(), "specsnl", "labelsync", "area/api")
			},
			want: http.MethodDelete,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var log []recorded

			handler := recorder(&log, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			client, _ := newTestClient(t, handler)

			if err := tc.call(client); err != nil {
				t.Fatalf("call error = %v, want nil", err)
			}

			if len(log) != 1 {
				t.Fatalf("requests = %d, want 1: %+v", len(log), log)
			}

			if log[0].Method != tc.want || log[0].Path != escaped {
				t.Errorf("request = %s %s, want %s %s", log[0].Method, log[0].Path, tc.want, escaped)
			}
		})
	}
}

// TestDeleteLabelIssuesADelete is the whole of the destructive operation: one
// DELETE, at the label's own path.
func TestDeleteLabelIssuesADelete(t *testing.T) {
	var log []recorded

	handler := recorder(&log, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	client, _ := newTestClient(t, handler)

	if err := client.DeleteLabel(t.Context(), "specsnl", "labelsync", "wontfix"); err != nil {
		t.Fatalf("DeleteLabel() error = %v, want nil", err)
	}

	if len(log) != 1 {
		t.Fatalf("requests = %d, want 1: %+v", len(log), log)
	}

	if log[0].Method != http.MethodDelete || log[0].Path != "/repos/specsnl/labelsync/labels/wontfix" {
		t.Errorf("request = %s %s, want DELETE /repos/specsnl/labelsync/labels/wontfix", log[0].Method, log[0].Path)
	}
}

// TestLabelConvertsToPlanLabel is the compile-time contract this package's Label
// exists to keep: the planner's input type and this one are the same shape, so
// bridging them is a conversion and not a mapping function. If either side gains,
// reorders, or renames a field, this stops compiling — which is the point, and is
// cheaper than discovering the mismatch as a silently empty description.
func TestLabelConvertsToPlanLabel(t *testing.T) {
	label := Label{Name: "bug", Color: "d73a4a", Description: "Something is broken"}

	converted := plan.Label(label)

	want := plan.Label{Name: "bug", Color: "d73a4a", Description: "Something is broken"}
	if converted != want {
		t.Errorf("plan.Label(label) = %+v, want %+v", converted, want)
	}
}

// TestReadLabelsKeepsOrderAndDropsSkippedRepositories covers the two things a
// caller of ReadLabels depends on. The order is the caller's, because it becomes
// a plan and a plan that reshuffled between two identical runs is not one anyone
// can diff. And a repository that could not be reached is *absent*, not present
// with an empty label set: the second reading is what a repository needing every
// label created looks like.
func TestReadLabelsKeepsOrderAndDropsSkippedRepositories(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "forbidden") {
			w.WriteHeader(http.StatusForbidden)
			write(w, errorBody("Forbidden"))

			return
		}

		write(w, `[{"name":"bug","color":"d73a4a","description":"Something is broken"}]`)
	})

	client, _ := newTestClient(t, handler)

	repos := []config.Repo{
		{Owner: "specsnl", Name: "zzz-last"},
		{Owner: "specsnl", Name: "forbidden"},
		{Owner: "specsnl", Name: "aaa-first"},
	}

	read, err := client.ReadLabels(t.Context(), repos, 8)
	if err != nil {
		t.Fatalf("ReadLabels() error = %v, want the run to continue", err)
	}

	want := []string{"specsnl/zzz-last", "specsnl/aaa-first"}

	got := make([]string, 0, len(read))
	for _, entry := range read {
		got = append(got, entry.Repo.String())
	}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ReadLabels() = %v, want %v", got, want)
	}

	for _, entry := range read {
		if len(entry.Labels) != 1 || entry.Labels[0].Name != "bug" {
			t.Errorf("%s labels = %+v, want the one label the API returned", entry.Repo, entry.Labels)
		}
	}

	if client.Failures().Len() != 1 {
		t.Errorf("Failures().Len() = %d, want 1", client.Failures().Len())
	}
}
