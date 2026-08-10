package plan_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/plan"
	"github.com/specsnl/labelsync/internal/util/output"
)

var update = flag.Bool("update", false, "rewrite the .golden files from the current output")

// goldenPlain is the environment a PrettyWriter is given when the test wants
// unstyled output: empty, so colorprofile finds nothing to enable, and the
// stream is a buffer rather than a terminal. This is the CI-log rendering.
var goldenPlain = []string{}

// goldenColor forces colour on regardless of where the test runs, so the pair of
// goldens shows the plain one is degradation rather than an absence of styles.
var goldenColor = []string{"CLICOLOR_FORCE=1", "TERM=xterm-256color"}

// samplePlan is the fixture behind every golden here: one repository exercising
// all four kinds — including a rename, a displaced squatter, and a cleared
// description — one fully converged repository that has to collapse, and one
// with issues disabled, which is noted once and otherwise changes nothing.
func samplePlan() plan.Plan {
	return plan.Plan{Repos: []plan.RepoPlan{
		{
			Repo: "specsnl/example-website",
			Actions: []plan.Action{
				{
					Kind: plan.KindUpdate, Repo: "specsnl/example-website",
					Name: "bug", NewName: new("type: bug"),
				},
				{
					Kind: plan.KindUpdate, Repo: "specsnl/example-website",
					Name: "wontfix", Color: new("16a3c4"), Reason: `displaced by "type: bug"`,
				},
				{
					Kind: plan.KindCreate, Repo: "specsnl/example-website",
					Name: "type: bug", Color: new("d73a4a"), Description: new("Something isn't working"),
				},
				{
					Kind: plan.KindUpdate, Repo: "specsnl/example-website",
					Name: "type: feature", Color: new("0e8a16"),
				},
				{
					Kind: plan.KindUpdate, Repo: "specsnl/example-website",
					Name: "priority: low", Description: new(""),
				},
				{
					Kind: plan.KindNoOp, Repo: "specsnl/example-website",
					Name: "priority: high",
				},
				{
					Kind: plan.KindDelete, Repo: "specsnl/example-website",
					Name: "old-label", Reason: "unconfigured",
				},
			},
		},
		{
			Repo: "specsnl/example-platform",
			Actions: []plan.Action{
				{Kind: plan.KindNoOp, Repo: "specsnl/example-platform", Name: "type: bug"},
				{Kind: plan.KindNoOp, Repo: "specsnl/example-platform", Name: "type: feature"},
				{Kind: plan.KindNoOp, Repo: "specsnl/example-platform", Name: "priority: high"},
			},
		},
		{
			Repo:           "specsnl/example-prs-only",
			IssuesDisabled: true,
			Actions: []plan.Action{
				{
					Kind: plan.KindCreate, Repo: "specsnl/example-prs-only",
					Name: "type: bug", Color: new("d73a4a"), Description: new("Something isn't working"),
				},
				{Kind: plan.KindNoOp, Repo: "specsnl/example-prs-only", Name: "priority: high"},
			},
		},
	}}
}

func TestRender_PrettyGolden(t *testing.T) {
	var stdout, stderr bytes.Buffer

	plan.Render(output.NewPrettyWriter(&stdout, &stderr, goldenPlain), samplePlan())

	assertGolden(t, "diff_pretty_nocolor", stdout.String())

	if stderr.Len() != 0 {
		t.Errorf("the diff is the product and belongs on stdout, but stderr got: %q", stderr.String())
	}
}

func TestRender_PrettyGoldenColor(t *testing.T) {
	var stdout, stderr bytes.Buffer

	plan.Render(output.NewPrettyWriter(&stdout, &stderr, goldenColor), samplePlan())

	assertGolden(t, "diff_pretty_color", stdout.String())
}

func TestRender_NDJSONGolden(t *testing.T) {
	var stdout, stderr bytes.Buffer

	plan.Render(output.NewJSONWriter(&stdout, &stderr), samplePlan())

	assertGolden(t, "diff_ndjson", stdout.String())

	if stderr.Len() != 0 {
		t.Errorf("nothing narrates a rendered plan, but stderr got: %q", stderr.String())
	}
}

// The NDJSON contract: one complete object per line, so a consumer parses the
// stream as it arrives and a run killed halfway leaves valid lines behind.
func TestRender_NDJSONIsOneObjectPerLine(t *testing.T) {
	var stdout bytes.Buffer

	plan.Render(output.NewJSONWriter(&stdout, &bytes.Buffer{}), samplePlan())

	out := stdout.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout does not end with a newline: %q", out)
	}

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")

	// Twelve actions, one repository note, and the summary. The collapsed
	// repository is a choice the pretty rendering makes for a reader; the stream
	// reports every action the planner decided on.
	if want := 12 + 1 + 1; len(lines) != want {
		t.Fatalf("got %d lines, want %d: %q", len(lines), want, out)
	}

	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not a JSON object: %v (%q)", i+1, err, line)
		}

		// One discriminator for the whole stream: every line, the summary
		// included, says what it is.
		if _, ok := obj["kind"]; !ok {
			t.Errorf("line %d carries no kind: %q", i+1, line)
		}
	}
}

// The summary is last, and it is last because a consumer reading the stream
// incrementally has to be able to treat it as the end of the run.
func TestRender_SummaryIsTheFinalObject(t *testing.T) {
	var stdout bytes.Buffer

	plan.Render(output.NewJSONWriter(&stdout, &bytes.Buffer{}), samplePlan())

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")

	var got plan.Summary
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatalf("final line is not a summary: %v", err)
	}

	want := plan.Summary{
		Kind: plan.SummaryKind, Repositories: 3,
		Created: 2, Updated: 4, Deleted: 1, Unchanged: 5,
	}
	if got != want {
		t.Errorf("summary = %+v, want %+v", got, want)
	}
}

func TestSummarise(t *testing.T) {
	tests := []struct {
		name string
		plan plan.Plan
		want plan.Summary
	}{
		{
			name: "the empty plan",
			plan: plan.Plan{},
			want: plan.Summary{Kind: plan.SummaryKind},
		},
		{
			name: "a repository with no actions still counts as visited",
			plan: plan.Plan{Repos: []plan.RepoPlan{{Repo: "specsnl/labelsync"}}},
			want: plan.Summary{Kind: plan.SummaryKind, Repositories: 1},
		},
		{
			name: "every kind at once",
			plan: samplePlan(),
			want: plan.Summary{
				Kind: plan.SummaryKind, Repositories: 3,
				Created: 2, Updated: 4, Deleted: 1, Unchanged: 5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plan.Summarise(tt.plan); got != tt.want {
				t.Errorf("Summarise() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A converged repository collapses to one line rather than repeating "= ok" for
// every label it checked. The count is still there, so the line says what it
// looked at.
func TestRender_CollapsesAnUnchangedRepository(t *testing.T) {
	got := pretty(t, samplePlan())

	if !strings.Contains(got, "=  ok  (3 labels, no changes)") {
		t.Errorf("converged repository was not collapsed:\n%s", got)
	}

	// The collapse is per repository: the no-op inside the repository that does
	// have work is still listed by name.
	if !strings.Contains(got, "priority: high") {
		t.Errorf("a no-op alongside real work was dropped:\n%s", got)
	}
}

// A repository the planner visited and found nothing to do in — no actions at
// all — is the same statement as one whose actions are all no-ops.
func TestRender_CollapsesAnEmptyRepository(t *testing.T) {
	got := pretty(t, plan.Plan{Repos: []plan.RepoPlan{{Repo: "specsnl/labelsync"}}})

	if !strings.Contains(got, "=  ok  (0 labels, no changes)") {
		t.Errorf("empty repository did not collapse:\n%s", got)
	}
}

// Reason is the whole point of the field: a recolour nobody configured reads as
// arbitrary until it says what displaced it.
func TestRender_SurfacesReasons(t *testing.T) {
	got := pretty(t, samplePlan())

	for _, want := range []string{`(displaced by "type: bug")`, "(unconfigured)"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff is missing %s:\n%s", want, got)
		}
	}
}

// A colour-only update that says why is a displaced squatter, and reads as
// "recolour". An ordinary configured recolour is an "update" — nobody asked for
// the squatter's, which is what makes the distinction worth drawing.
func TestRender_Verbs(t *testing.T) {
	tests := []struct {
		name   string
		action plan.Action
		want   string
	}{
		{
			name:   "a create",
			action: plan.Action{Kind: plan.KindCreate, Name: "type: bug", Color: new("d73a4a")},
			want:   "+  create    type: bug",
		},
		{
			name:   "a displaced squatter",
			action: plan.Action{Kind: plan.KindUpdate, Name: "wontfix", Color: new("16a3c4"), Reason: "displaced"},
			want:   "~  recolour  wontfix",
		},
		{
			name:   "a configured recolour, which nothing displaced",
			action: plan.Action{Kind: plan.KindUpdate, Name: "type: feature", Color: new("0e8a16")},
			want:   "~  update    type: feature",
		},
		{
			name:   "a rename shows the transition",
			action: plan.Action{Kind: plan.KindUpdate, Name: "bug", NewName: new("type: bug")},
			want:   "~  update    bug → type: bug",
		},
		{
			name:   "a delete",
			action: plan.Action{Kind: plan.KindDelete, Name: "old-label"},
			want:   "-  delete    old-label",
		},
		{
			name:   "a no-op",
			action: plan.Action{Kind: plan.KindNoOp, Name: "priority: high"},
			want:   "=  ok        priority: high",
		},
		{
			name:   "a kind from a plan file this build does not know",
			action: plan.Action{Kind: "explode", Name: "mystery"},
			want:   "?  explode   mystery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A squatter recolour rides along in every case so the verb column
			// is always eight wide — the expectations above are written against
			// that width rather than against whatever the row under test pads to.
			p := plan.Plan{Repos: []plan.RepoPlan{{Repo: "specsnl/labelsync", Actions: []plan.Action{
				tt.action,
				{Kind: plan.KindUpdate, Name: "squatter", Color: new("000000"), Reason: "displaced"},
			}}}}

			if got := pretty(t, p); !strings.Contains(got, tt.want) {
				t.Errorf("diff does not contain %q:\n%s", tt.want, got)
			}
		})
	}
}

// A cleared description is an update setting it to empty, and it has to look
// different from an update that leaves it alone — a blank cell would read as
// "unchanged", which is the one thing it is not.
func TestRender_ClearedDescriptionIsVisible(t *testing.T) {
	cleared := pretty(t, plan.Plan{Repos: []plan.RepoPlan{{Repo: "o/r", Actions: []plan.Action{
		{Kind: plan.KindUpdate, Name: "label", Description: new("")},
	}}}})

	if !strings.Contains(cleared, `label    ""`) {
		t.Errorf("a cleared description was not rendered:\n%s", cleared)
	}

	untouched := pretty(t, plan.Plan{Repos: []plan.RepoPlan{{Repo: "o/r", Actions: []plan.Action{
		{Kind: plan.KindUpdate, Name: "label", Color: new("d73a4a")},
	}}}})

	if strings.Contains(untouched, `"`) {
		t.Errorf("an untouched description rendered as a value:\n%s", untouched)
	}
}

// An empty plan is a normal outcome — no repositories resolved — and it still
// has to say so rather than printing nothing at all.
func TestRender_EmptyPlan(t *testing.T) {
	got := pretty(t, plan.Plan{})

	if want := "0 repositories · 0 created · 0 updated · 0 deleted · 0 unchanged\n"; got != want {
		t.Errorf("empty plan rendered as %q, want %q", got, want)
	}
}

// The summary counts repositories in English. Everything else reads correctly at
// one already; "1 repositories" does not.
func TestRender_SummaryPluralisesRepositories(t *testing.T) {
	got := pretty(t, plan.Plan{Repos: []plan.RepoPlan{{Repo: "specsnl/labelsync"}}})

	if !strings.Contains(got, "1 repository · ") {
		t.Errorf("summary did not pluralise:\n%s", got)
	}
}

// A buffer is not a terminal, so nothing styled may survive to it. The colour
// golden is the other half of this claim.
func TestRender_StripsEscapesOffTerminal(t *testing.T) {
	if got := pretty(t, samplePlan()); strings.Contains(got, "\x1b[") {
		t.Errorf("the diff carries ANSI escapes off a terminal: %q", got)
	}
}

// Rendering the same plan twice produces the same bytes. The determinism suite
// asserts this end to end; here it guards the rendering half of it.
func TestRender_IsDeterministic(t *testing.T) {
	if first, second := pretty(t, samplePlan()), pretty(t, samplePlan()); first != second {
		t.Errorf("two renderings of one plan differ:\n%s\n---\n%s", first, second)
	}
}

// pretty renders p through a plain PrettyWriter and returns stdout.
func pretty(t *testing.T, p plan.Plan) string {
	t.Helper()

	var stdout, stderr bytes.Buffer

	plan.Render(output.NewPrettyWriter(&stdout, &stderr, goldenPlain), p)

	return stdout.String()
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

		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run the tests with -update to create it)", path, err)
	}

	if got != string(want) {
		t.Errorf("output does not match %s\n--- got ---\n%s\n--- want ---\n%s", path, got, string(want))
	}
}

// The note is a property of the repository, so it is rendered once under the
// heading — not on every action, which would repeat it, and not on one action,
// which would make a reader wonder what was special about that one.
func TestRender_IssuesDisabledNoteIsRenderedOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer

	plan.Render(output.NewPrettyWriter(&stdout, &stderr, goldenPlain), samplePlan())

	pretty := stdout.String()
	if got := strings.Count(pretty, "issues are disabled"); got != 1 {
		t.Errorf("the note appears %d times, want exactly 1:\n%s", got, pretty)
	}

	// A note is not a warning: it explains the diff, and the diff is the
	// product. Sending it to stderr would put it in a different stream from the
	// repository it is about.
	if stderr.Len() != 0 {
		t.Errorf("the note belongs with the diff on stdout, but stderr got: %q", stderr.String())
	}

	// Directly under the heading and above the actions, which is where a reader
	// meets it before having to make sense of what follows.
	lines := strings.Split(pretty, "\n")

	heading := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) == "specsnl/example-prs-only"
	})
	if heading == -1 {
		t.Fatalf("the noted repository has no heading:\n%s", pretty)
	}

	if !strings.Contains(lines[heading+1], "issues are disabled") {
		t.Errorf("the line under the heading is %q, want the note:\n%s", lines[heading+1], pretty)
	}
}

// In the stream the note is a record about the repository, never a synthetic
// action: it is not something to apply, and a consumer walking actions must not
// find it among them.
func TestRender_IssuesDisabledIsARepositoryRecord(t *testing.T) {
	var stdout bytes.Buffer

	plan.Render(output.NewJSONWriter(&stdout, &bytes.Buffer{}), samplePlan())

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")

	var (
		notes    int
		position int
	)

	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not a JSON object: %v", i+1, err)
		}

		if obj["kind"] != plan.RepositoryKind {
			continue
		}

		notes++
		position = i

		if obj["repo"] != "specsnl/example-prs-only" {
			t.Errorf("the note is on %v, want specsnl/example-prs-only", obj["repo"])
		}

		if obj["issues_disabled"] != true {
			t.Errorf("issues_disabled = %v, want true", obj["issues_disabled"])
		}

		// Nothing about it is applicable, so it carries none of an action's
		// fields.
		for _, field := range []string{"name", "new_name", "color", "description"} {
			if _, ok := obj[field]; ok {
				t.Errorf("the record carries %q, which belongs to an action", field)
			}
		}
	}

	if notes != 1 {
		t.Fatalf("got %d repository records, want 1", notes)
	}

	// Ahead of the actions it is about, so a consumer reading the stream in
	// order is told before it has to make sense of them.
	var next map[string]any
	if err := json.Unmarshal([]byte(lines[position+1]), &next); err != nil {
		t.Fatalf("no line follows the note: %v", err)
	}

	if next["repo"] != "specsnl/example-prs-only" {
		t.Errorf("the note is not followed by its own repository's actions: %v", next["repo"])
	}
}

// A repository with issues enabled renders exactly as it did before the note
// existed. Every other assertion here is about the noted repository; this one is
// about the ones that must be untouched.
func TestRender_NoNoteWithoutTheFlag(t *testing.T) {
	quiet := plan.Plan{Repos: []plan.RepoPlan{{
		Repo:    "specsnl/example-website",
		Actions: []plan.Action{{Kind: plan.KindNoOp, Repo: "specsnl/example-website", Name: "type: bug"}},
	}}}

	var stdout bytes.Buffer

	plan.Render(output.NewJSONWriter(&stdout, &bytes.Buffer{}), quiet)

	if strings.Contains(stdout.String(), plan.RepositoryKind) {
		t.Errorf("a repository record was emitted for a repository with nothing to say:\n%s", stdout.String())
	}

	if strings.Contains(stdout.String(), "issues_disabled") {
		t.Errorf("issues_disabled leaked into the stream:\n%s", stdout.String())
	}
}
