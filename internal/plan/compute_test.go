package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lucasb-eyer/go-colorful"

	"github.com/specsnl/labelsync/internal/config"
)

// repo is the repository every test computes against. Compute does nothing with
// it but copy it onto the actions, so one value is enough.
const repo = "specsnl/labelsync"

// TestCompute is the core suite: (desired, current) in, ordered actions out.
//
// The recolour colours are the real values palette.Allocate hands out, written
// here literally rather than recomputed in the test. Recomputing them would
// assert that the planner calls the allocator, which is not the interesting
// property; pinning them asserts that a second run over an unchanged repository
// does not churn a squatter's colour, which is.
func TestCompute(t *testing.T) {
	tests := []struct {
		name    string
		desired []config.Label
		current []Label
		renames []config.Rename
		want    []Action
	}{
		{
			name: "nothing configured and nothing present",
		},
		{
			name: "missing labels are created in name order",
			desired: []config.Label{
				{Name: "type: feature", Color: "a2eeef", Description: "New functionality"},
				{Name: "type: bug", Color: "d73a4a", Description: "Something is broken"},
			},
			want: []Action{
				{Kind: KindCreate, Repo: repo, Name: "type: bug", Color: new("d73a4a"), Description: new("Something is broken")},
				{Kind: KindCreate, Repo: repo, Name: "type: feature", Color: new("a2eeef"), Description: new("New functionality")},
			},
		},
		{
			name:    "a label with no description is created with an empty one",
			desired: []config.Label{{Name: "bug", Color: "d73a4a"}},
			want: []Action{
				{Kind: KindCreate, Repo: repo, Name: "bug", Color: new("d73a4a"), Description: new("")},
			},
		},
		{
			name:    "an identical label is a no-op",
			desired: []config.Label{{Name: "bug", Color: "d73a4a", Description: "Something is broken"}},
			current: []Label{{Name: "bug", Color: "d73a4a", Description: "Something is broken"}},
			want: []Action{
				{Kind: KindNoOp, Repo: repo, Name: "bug"},
			},
		},
		{
			name:    "a colour change carries only the colour",
			desired: []config.Label{{Name: "bug", Color: "d73a4a", Description: "Something is broken"}},
			current: []Label{{Name: "bug", Color: "1d76db", Description: "Something is broken"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", Color: new("d73a4a")},
			},
		},
		{
			name:    "a description change carries only the description",
			desired: []config.Label{{Name: "bug", Color: "d73a4a", Description: "A defect"}},
			current: []Label{{Name: "bug", Color: "d73a4a", Description: "Something is broken"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", Description: new("A defect")},
			},
		},
		{
			name:    "an omitted description clears the remote one",
			desired: []config.Label{{Name: "bug", Color: "d73a4a"}},
			current: []Label{{Name: "bug", Color: "d73a4a", Description: "Something is broken"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", Description: new("")},
			},
		},
		{
			name:    "casing drift renames the label to the configured casing",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{{Name: "Type: Bug", Color: "d73a4a"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "Type: Bug", NewName: new("type: bug")},
			},
		},
		{
			name:    "casing, colour and description drift travel in one update",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}},
			current: []Label{{Name: "TYPE: BUG", Color: "1d76db", Description: "Something is broken"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "TYPE: BUG", NewName: new("type: bug"), Color: new("d73a4a"), Description: new("A defect")},
			},
		},
		{
			name:    "colours compare normalised, so # and upper case are not drift",
			desired: []config.Label{{Name: "bug", Color: "d73a4a"}},
			current: []Label{{Name: "bug", Color: "#D73A4A"}},
			want: []Action{
				{Kind: KindNoOp, Repo: repo, Name: "bug"},
			},
		},
		{
			name:    "an unconfigured label off a reserved colour is left alone in append mode",
			desired: []config.Label{{Name: "bug", Color: "d73a4a"}},
			current: []Label{
				{Name: "bug", Color: "d73a4a"},
				{Name: "wontfix", Color: "ffffff"},
			},
			want: []Action{
				{Kind: KindNoOp, Repo: repo, Name: "bug"},
			},
		},
		{
			name:    "a squatter is recoloured before the configured label claims the colour",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{{Name: "wontfix", Color: "d73a4a"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "wontfix", Color: new("13ec13"), Reason: `displaced by "type: bug"`},
				{Kind: KindCreate, Repo: repo, Name: "type: bug", Color: new("d73a4a"), Description: new("")},
			},
		},
		{
			name:    "a squatter is detected through colour normalisation",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{{Name: "wontfix", Color: "#D73A4A"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "wontfix", Color: new("13ec13"), Reason: `displaced by "type: bug"`},
				{Kind: KindCreate, Repo: repo, Name: "type: bug", Color: new("d73a4a"), Description: new("")},
			},
		},
		{
			name: "two squatters are recoloured in name order and never share a colour",
			desired: []config.Label{
				{Name: "type: bug", Color: "d73a4a"},
				{Name: "type: chore", Color: "1d76db"},
			},
			current: []Label{
				{Name: "wontfix", Color: "1d76db"},
				{Name: "duplicate", Color: "d73a4a"},
			},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "duplicate", Color: new("a4ec13"), Reason: `displaced by "type: bug"`},
				{Kind: KindUpdate, Repo: repo, Name: "wontfix", Color: new("318167"), Reason: `displaced by "type: chore"`},
				{Kind: KindCreate, Repo: repo, Name: "type: bug", Color: new("d73a4a"), Description: new("")},
				{Kind: KindCreate, Repo: repo, Name: "type: chore", Color: new("1d76db"), Description: new("")},
			},
		},
		{
			name: "two configured labels sharing a colour credit the first by name",
			desired: []config.Label{
				{Name: "z: last", Color: "d73a4a"},
				{Name: "a: first", Color: "d73a4a"},
			},
			current: []Label{{Name: "wontfix", Color: "d73a4a"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "wontfix", Color: new("13ec13"), Reason: `displaced by "a: first"`},
				{Kind: KindCreate, Repo: repo, Name: "a: first", Color: new("d73a4a"), Description: new("")},
				{Kind: KindCreate, Repo: repo, Name: "z: last", Color: new("d73a4a"), Description: new("")},
			},
		},
		{
			name: "recolours come first, then creates, then existing labels in name order",
			desired: []config.Label{
				{Name: "b: updated", Color: "1d76db"},
				{Name: "a: created", Color: "d73a4a"},
				{Name: "c: unchanged", Color: "0e8a16"},
			},
			current: []Label{
				{Name: "c: unchanged", Color: "0e8a16"},
				{Name: "b: updated", Color: "ffffff"},
				{Name: "squatter", Color: "d73a4a"},
			},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "squatter", Color: new("eca413"), Reason: `displaced by "a: created"`},
				{Kind: KindCreate, Repo: repo, Name: "a: created", Color: new("d73a4a"), Description: new("")},
				{Kind: KindUpdate, Repo: repo, Name: "b: updated", Color: new("1d76db")},
				{Kind: KindNoOp, Repo: repo, Name: "c: unchanged"},
			},
		},
		{
			// Without the rename, "bug" is unconfigured and sitting on a
			// reserved colour, so it would be recoloured as a squatter and
			// "type: bug" created alongside it.
			name:    "a rename is emitted before matching, so the label is matched and not squatting",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}},
			current: []Label{{Name: "bug", Color: "d73a4a", Description: "A defect"}},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", NewName: new("type: bug")},
				{Kind: KindNoOp, Repo: repo, Name: "type: bug"},
			},
		},
		{
			name:    "a rename matches the remote name case-insensitively",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{{Name: "BUG", Color: "d73a4a"}},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "BUG", NewName: new("type: bug")},
				{Kind: KindNoOp, Repo: repo, Name: "type: bug"},
			},
		},
		{
			name:    "a rename and a recolour of one label are two coherent steps",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}},
			current: []Label{{Name: "bug", Color: "1d76db", Description: "Something is broken"}},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", NewName: new("type: bug")},
				{Kind: KindUpdate, Repo: repo, Name: "type: bug", Color: new("d73a4a"), Description: new("A defect")},
			},
		},
		{
			name:    "a rename whose source is absent is skipped silently",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{{Name: "type: bug", Color: "d73a4a"}},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindNoOp, Repo: repo, Name: "type: bug"},
			},
		},
		{
			name:    "a rename whose target already exists is skipped, and the source is left alone",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{
				{Name: "bug", Color: "ffffff"},
				{Name: "type: bug", Color: "d73a4a"},
			},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindNoOp, Repo: repo, Name: "type: bug"},
			},
		},
		{
			name:    "a rename whose target exists in another casing is skipped too",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{
				{Name: "bug", Color: "ffffff"},
				{Name: "TYPE: BUG", Color: "d73a4a"},
			},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "TYPE: BUG", NewName: new("type: bug")},
			},
		},
		{
			name: "renames are emitted before every other action",
			desired: []config.Label{
				{Name: "type: bug", Color: "d73a4a"},
				{Name: "type: chore", Color: "1d76db"},
			},
			current: []Label{
				{Name: "bug", Color: "ffffff"},
				{Name: "wontfix", Color: "1d76db"},
			},
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", NewName: new("type: bug")},
				{Kind: KindUpdate, Repo: repo, Name: "wontfix", Color: new("a4ec13"), Reason: `displaced by "type: chore"`},
				{Kind: KindCreate, Repo: repo, Name: "type: chore", Color: new("1d76db"), Description: new("")},
				{Kind: KindUpdate, Repo: repo, Name: "type: bug", Color: new("d73a4a")},
			},
		},
		{
			name:    "a half-empty rename is ignored",
			desired: []config.Label{{Name: "type: bug", Color: "d73a4a"}},
			current: []Label{{Name: "bug", Color: "d73a4a"}},
			renames: []config.Rename{{From: "bug"}, {To: "type: bug"}},
			want: []Action{
				{Kind: KindUpdate, Repo: repo, Name: "bug", Color: new("13ec13"), Reason: `displaced by "type: bug"`},
				{Kind: KindCreate, Repo: repo, Name: "type: bug", Color: new("d73a4a"), Description: new("")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compute(repo, tt.desired, tt.current, ModeAppend, tt.renames)

			if got.Repo != repo {
				t.Errorf("repo = %q, want %q", got.Repo, repo)
			}

			if !reflect.DeepEqual(got.Actions, tt.want) {
				t.Errorf("actions =\n%s\nwant\n%s", format(got.Actions), format(tt.want))
			}
		})
	}
}

// TestComputeDoesNotMutateInput guards the sort: Compute orders the configured
// labels by name, and doing that in place would reorder the caller's slice —
// which is the config's label list, shared across every repository in a run.
func TestComputeDoesNotMutateInput(t *testing.T) {
	desired := []config.Label{
		{Name: "type: feature", Color: "a2eeef"},
		{Name: "type: bug", Color: "d73a4a"},
	}

	Compute(repo, desired, nil, ModeAppend, nil)

	if desired[0].Name != "type: feature" {
		t.Errorf("desired was reordered: %v", desired)
	}
}

// TestComputeRenameIsConvergent is what makes re-running the tool safe: once a
// rename has been applied, the same config against the renamed repository has
// nothing left to do. Applying the plan is simulated rather than mocked — the
// planner is pure, so the second run only needs the labels the first one would
// have produced.
func TestComputeRenameIsConvergent(t *testing.T) {
	desired := []config.Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}}
	renames := []config.Rename{{From: "bug", To: "type: bug"}}
	current := []Label{{Name: "bug", Color: "1d76db", Description: "Something is broken"}}

	first := Compute(repo, desired, current, ModeAppend, renames).Actions
	if len(first) == 0 {
		t.Fatal("the first run should have had work to do")
	}

	applied := []Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}}

	want := []Action{{Kind: KindNoOp, Repo: repo, Name: "type: bug"}}
	if got := Compute(repo, desired, applied, ModeAppend, renames).Actions; !reflect.DeepEqual(got, want) {
		t.Errorf("re-running produced\n%s\nwant\n%s", format(got), format(want))
	}
}

// TestComputeChainedRenames pins what the planner does with a chain a→b→c.
// config validation rejects one, but the planner must not quietly depend on
// that: renaming against the rewritten view means the second entry sees the name
// the first one produced, so the chain resolves to c instead of two actions
// fighting over b.
func TestComputeChainedRenames(t *testing.T) {
	desired := []config.Label{{Name: "type: bug", Color: "d73a4a"}}
	renames := []config.Rename{{From: "defect", To: "bug"}, {From: "bug", To: "type: bug"}}
	current := []Label{{Name: "defect", Color: "d73a4a"}}

	want := []Action{
		{Kind: KindUpdate, Repo: repo, Name: "defect", NewName: new("bug")},
		{Kind: KindUpdate, Repo: repo, Name: "bug", NewName: new("type: bug")},
		{Kind: KindNoOp, Repo: repo, Name: "type: bug"},
	}

	got := Compute(repo, desired, current, ModeAppend, renames).Actions
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actions =\n%s\nwant\n%s", format(got), format(want))
	}

	// And the chain converges: nothing is left to do against the result.
	applied := []Label{{Name: "type: bug", Color: "d73a4a"}}

	settled := []Action{{Kind: KindNoOp, Repo: repo, Name: "type: bug"}}
	if again := Compute(repo, desired, applied, ModeAppend, renames).Actions; !reflect.DeepEqual(again, settled) {
		t.Errorf("re-running the chain produced\n%s\nwant\n%s", format(again), format(settled))
	}
}

// TestComputeDoesNotMutateCurrent guards the rename pass's clone: rewriting the
// local view in place would rename the caller's labels, which are the ones read
// back from the API and may well be reused for reporting.
func TestComputeDoesNotMutateCurrent(t *testing.T) {
	current := []Label{{Name: "bug", Color: "d73a4a"}}

	Compute(repo, []config.Label{{Name: "type: bug", Color: "d73a4a"}}, current,
		ModeAppend, []config.Rename{{From: "bug", To: "type: bug"}})

	if current[0].Name != "bug" {
		t.Errorf("current was rewritten: %v", current)
	}
}

// TestComputeIsDeterministic is the property re-running the tool depends on: the
// same input has to produce the same actions, colours included, or every run
// would churn the squatters it recoloured on the last one.
func TestComputeIsDeterministic(t *testing.T) {
	desired := []config.Label{
		{Name: "type: bug", Color: "d73a4a"},
		{Name: "type: chore", Color: "1d76db"},
	}
	current := []Label{
		{Name: "wontfix", Color: "1d76db"},
		{Name: "duplicate", Color: "d73a4a"},
		{Name: "question", Color: "cc317c"},
	}

	first := Compute(repo, desired, current, ModeAppend, nil)

	for i := range 5 {
		if again := Compute(repo, desired, current, ModeAppend, nil); !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%s\nwant\n%s", i+2, format(again.Actions), format(first.Actions))
		}
	}
}

// TestComputeReportsPaletteExhaustion is the planner's half of the palette's
// exhaustion contract: when every candidate colour is already spoken for, the
// squatter is still recoloured and the action carries the warning.
//
// Reserving the whole grid is how exhaustion is forced. The grid is regenerated
// here from the axes the palette documents rather than read out of it, since
// nothing about it is exported — if those axes ever change, the reserved set
// stops covering the grid exactly and this test says so.
func TestComputeReportsPaletteExhaustion(t *testing.T) {
	var desired []config.Label

	for hue := 0; hue < 360; hue += 10 {
		for _, saturation := range []float64{0.45, 0.65, 0.85} {
			for _, lightness := range []float64{0.35, 0.50, 0.65} {
				hex := colorful.Hsl(float64(hue), saturation, lightness).Clamped().Hex()
				desired = append(desired, config.Label{Name: "reserves " + hex, Color: strings.TrimPrefix(hex, "#")})
			}
		}
	}

	current := []Label{{Name: "wontfix", Color: strings.TrimPrefix(colorful.Hsl(0, 0.45, 0.35).Clamped().Hex(), "#")}}

	got := Compute(repo, desired, current, ModeAppend, nil).Actions

	if len(got) == 0 || got[0].Name != "wontfix" {
		t.Fatalf("expected a recolour of wontfix first, got\n%s", format(got))
	}

	if !strings.Contains(got[0].Reason, exhaustionNote) {
		t.Errorf("reason = %q, want it to carry %q", got[0].Reason, exhaustionNote)
	}

	if got[0].Color == nil || *got[0].Color == "" {
		t.Error("an exhausted allocation still has to produce a colour")
	}
}

// TestComputeRecolourAvoidsEveryPresentColour checks the allocator is fed both
// halves of its input: a recoloured squatter must not land on a colour a
// configured label has reserved, nor on one another label already holds.
func TestComputeRecolourAvoidsEveryPresentColour(t *testing.T) {
	desired := []config.Label{
		{Name: "type: bug", Color: "d73a4a"},
		{Name: "type: chore", Color: "1d76db"},
	}
	current := []Label{
		{Name: "duplicate", Color: "d73a4a"},
		{Name: "wontfix", Color: "d73a4a"},
		{Name: "question", Color: "cc317c"},
	}

	taken := map[string]struct{}{"d73a4a": {}, "1d76db": {}, "cc317c": {}}

	for _, action := range Compute(repo, desired, current, ModeAppend, nil).Actions {
		if action.Kind != KindUpdate || action.Reason == "" {
			continue
		}

		if _, clash := taken[*action.Color]; clash {
			t.Errorf("%q was recoloured to %s, which is already taken", action.Name, *action.Color)
		}

		taken[*action.Color] = struct{}{}
	}
}

// format renders actions one per line, so a failing table row shows what
// differed rather than a wall of pointer addresses.
func format(actions []Action) string {
	if len(actions) == 0 {
		return "  (none)"
	}

	var b strings.Builder

	for _, a := range actions {
		b.WriteString("  " + string(a.Kind) + " " + a.Name)
		b.WriteString(field(" new_name=", a.NewName))
		b.WriteString(field(" color=", a.Color))
		b.WriteString(field(" description=", a.Description))

		if a.Reason != "" {
			b.WriteString(" reason=" + a.Reason)
		}

		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// field renders one optional field, distinguishing nil from a pointer to the
// empty string — the distinction the pointers exist to carry.
func field(label string, value *string) string {
	if value == nil {
		return ""
	}

	return label + `"` + *value + `"`
}
