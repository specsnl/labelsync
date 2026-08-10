package plan_test

// determinism_test.go is the suite the design asks for by name: colour churn on
// re-run is the most likely subtle regression in the tool, and it is invisible
// to a test that checks one plan in isolation. Every plan here looks correct on
// its own; what is asserted is the relationship between two of them.
//
// Four properties, each a way the same input could stop producing the same
// output:
//
//   - planning twice renders byte-identical output,
//   - applying a plan and planning again produces nothing but no-ops,
//   - several squatters competing for colours are handed distinct ones, stably,
//   - shuffling the input order changes nothing, because the planner sorts.
//
// The suite runs against the exported API from outside the package, which is
// also what the command does: Compute per repository, then Render.

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/plan"
	"github.com/specsnl/labelsync/internal/util/output"
)

// repoState is one repository's existing labels, as the enumerator would have
// read them back.
type repoState struct {
	repo    string
	current []plan.Label

	// uncovered marks a repository no group resolved to: the planner is handed
	// an empty desired set for it, which is the input its safety guard is on.
	uncovered bool

	// issuesDisabled is the repository-level note. It is derived from input, so
	// it may not vary between two runs — which is exactly what this suite is
	// for, and why it is a field here rather than a case of its own.
	issuesDisabled bool
}

// target renders the state as the repository the planner is handed.
func (r repoState) target() config.Repo {
	owner, name, _ := strings.Cut(r.repo, "/")

	return config.Repo{Owner: owner, Name: name, HasIssues: new(!r.issuesDisabled)}
}

// fixture is a whole run: one configured label set and renames list — the config
// is global, the repositories are not — against several repositories in
// different states.
type fixture struct {
	name    string
	mode    plan.Mode
	desired []config.Label
	renames []config.Rename
	repos   []repoState
}

// compute runs the planner over every repository in the fixture, in order,
// exactly as the command does.
func (f fixture) compute() plan.Plan {
	p := plan.Plan{Repos: make([]plan.RepoPlan, 0, len(f.repos))}

	for _, r := range f.repos {
		desired := f.desired
		if r.uncovered {
			desired = nil
		}

		p.Repos = append(p.Repos, plan.Compute(r.target(), desired, r.current, f.mode, f.renames))
	}

	return p
}

// fixtures are deliberately weighted towards the cases where ordering bugs hide:
// several unconfigured labels sitting on reserved colours at once, renames
// feeding into recolours, and prune candidates on top of both.
func fixtures() []fixture {
	desired := []config.Label{
		{Name: "type: bug", Color: "d73a4a", Description: "Something is broken"},
		{Name: "type: feature", Color: "a2eeef", Description: "New functionality"},
		{Name: "type: chore", Color: "1d76db", Description: "Maintenance"},
		{Name: "priority: high", Color: "b60205"},
		{Name: "priority: low", Color: "0e8a16"},
	}

	return []fixture{
		{
			name:    "a fresh organisation",
			mode:    plan.ModeAppend,
			desired: desired,
			repos: []repoState{
				{repo: "specsnl/example-website"},
				{repo: "specsnl/example-platform", issuesDisabled: true},
			},
		},
		{
			name:    "five squatters competing for colours in one repository",
			mode:    plan.ModeAppend,
			desired: desired,
			repos: []repoState{
				{
					repo: "specsnl/example-website",
					// Every one of these sits on a colour the config reserves, so
					// all five are recoloured in the same pass and the allocator
					// has to hand out five distinct colours.
					current: []plan.Label{
						{Name: "bug", Color: "d73a4a"},
						{Name: "duplicate", Color: "a2eeef"},
						{Name: "invalid", Color: "1d76db"},
						{Name: "question", Color: "b60205"},
						{Name: "wontfix", Color: "0e8a16"},
					},
				},
			},
		},
		{
			name:    "squatters, renames and drift across three repositories",
			mode:    plan.ModeAppend,
			desired: desired,
			renames: []config.Rename{
				{From: "bug", To: "type: bug"},
				{From: "enhancement", To: "type: feature"},
			},
			repos: []repoState{
				{
					repo: "specsnl/example-website",
					current: []plan.Label{
						{Name: "bug", Color: "d73a4a", Description: "Something is broken"},
						{Name: "enhancement", Color: "84b6eb"},
						{Name: "duplicate", Color: "1d76db"},
						{Name: "wontfix", Color: "ffffff"},
					},
				},
				{
					repo: "specsnl/example-platform",
					current: []plan.Label{
						{Name: "TYPE: BUG", Color: "cccccc", Description: "stale"},
						{Name: "type: chore", Color: "1d76db", Description: "Maintenance"},
						{Name: "help wanted", Color: "b60205"},
						{Name: "good first issue", Color: "0e8a16"},
					},
				},
				{
					// Uncovered by any group: no desired labels, and therefore
					// never touched, whatever it holds.
					repo:      uncovered,
					uncovered: true,
					current: []plan.Label{
						{Name: "bug", Color: "d73a4a"},
						{Name: "wontfix", Color: "ffffff"},
					},
				},
			},
		},
		{
			name:    "prune, with candidates on top of recolours",
			mode:    plan.ModePrune,
			desired: desired,
			renames: []config.Rename{{From: "bug", To: "type: bug"}},
			repos: []repoState{
				{
					repo: "specsnl/example-website",
					current: []plan.Label{
						{Name: "bug", Color: "1d76db", Description: "Something is broken"},
						{Name: "duplicate", Color: "a2eeef"},
						{Name: "invalid", Color: "e4e669"},
						{Name: "question", Color: "b60205"},
					},
				},
			},
		},
	}
}

// uncovered is the repository no group resolves to.
const uncovered = "specsnl/example-legacy"

// fixtureNamed picks one fixture out of the table, so the tests that need a
// specific shape say which one rather than indexing into it.
func fixtureNamed(t *testing.T, name string) fixture {
	t.Helper()

	for _, f := range fixtures() {
		if f.name == name {
			return f
		}
	}

	t.Fatalf("no fixture named %q", name)

	return fixture{}
}

// TestDeterminism_RenderedOutputIsByteIdentical is the property the tool's
// whole re-run story rests on: two runs over the same input produce the same
// bytes, in both renderings.
//
// Rendered output rather than the plan struct, because that is what a human
// diffs between runs and what a machine consumer parses — and because a
// difference the structs hide, such as map iteration leaking into a reason
// string, still has to show up here.
func TestDeterminism_RenderedOutputIsByteIdentical(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			first := f.compute()

			// Several times, not twice: an ordering bug driven by map iteration
			// agrees with itself often enough to survive a single repeat.
			for run := range 8 {
				again := f.compute()

				if got, want := pretty(t, again), pretty(t, first); got != want {
					t.Fatalf("run %d rendered differently:\n--- got ---\n%s\n--- want ---\n%s", run+2, got, want)
				}

				if got, want := ndjson(t, again), ndjson(t, first); got != want {
					t.Fatalf("run %d streamed differently:\n--- got ---\n%s\n--- want ---\n%s", run+2, got, want)
				}

				if !reflect.DeepEqual(again, first) {
					t.Fatalf("run %d produced a different plan:\n%#v\nwant\n%#v", run+2, again, first)
				}
			}
		})
	}
}

// TestDeterminism_IsIndependentOfInputOrder shuffles both halves of the input.
// The planner sorts what it emits, so neither the order the config lists labels
// in nor the order the API happens to return them in may leak through — a run
// against a repository whose labels came back in a different order has to
// produce the identical diff, or every run would look like drift.
func TestDeterminism_IsIndependentOfInputOrder(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			want := pretty(t, f.compute())

			// A fixed seed: the shuffle has to vary between permutations, not
			// between test runs, or a failure would not reproduce.
			rng := rand.New(rand.NewPCG(30, 2026)) //nolint:gosec // deterministic permutations, not security

			for i := range 16 {
				shuffled := f
				shuffled.desired = shuffle(rng, f.desired)
				shuffled.repos = make([]repoState, len(f.repos))

				for j, r := range f.repos {
					shuffled.repos[j] = r
					shuffled.repos[j].current = shuffle(rng, r.current)
				}

				if got := pretty(t, shuffled.compute()); got != want {
					t.Fatalf("permutation %d rendered differently:\n--- got ---\n%s\n--- want ---\n%s", i+1, got, want)
				}
			}
		})
	}
}

// TestDeterminism_ConvergesAfterOneApply is convergence rather than
// determinism: the plan is applied to the repository it was computed against,
// and planning again has to find nothing left to do.
//
// Two runs that each churn the same label in the same way are perfectly
// deterministic and still wrong, which is why this is a separate property.
// Applying is simulated — the planner is pure, so the second run only needs the
// labels the first one would have produced.
func TestDeterminism_ConvergesAfterOneApply(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			first := f.compute()

			settled := f
			settled.repos = make([]repoState, len(f.repos))

			for i, r := range f.repos {
				settled.repos[i] = r
				settled.repos[i].current = apply(r.current, first.Repos[i].Actions)
			}

			second := settled.compute()

			for _, repo := range second.Repos {
				for _, a := range repo.Actions {
					if a.Kind != plan.KindNoOp {
						t.Errorf("%s still has work after applying the plan: %s %q", repo.Repo, a.Kind, a.Name)
					}
				}
			}

			// And the third run equals the second: convergence that oscillates
			// between two stable-looking states is not convergence.
			if got, want := pretty(t, settled.compute()), pretty(t, second); got != want {
				t.Errorf("a converged run is not stable:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// TestDeterminism_CompetingSquattersGetDistinctColours is the ordering bug this
// suite exists for, asserted directly: when several unconfigured labels are all
// displaced at once, each has to be handed a colour no configured label reserves
// and no other label already holds — and the same one on every run.
func TestDeterminism_CompetingSquattersGetDistinctColours(t *testing.T) {
	f := fixtureNamed(t, "five squatters competing for colours in one repository")

	reserved := make(map[string]string, len(f.desired))

	for _, d := range f.desired {
		reserved[d.Color] = d.Name
	}

	first := f.compute()

	recoloured := map[string]string{}

	for _, a := range first.Repos[0].Actions {
		if !isRecolour(a) {
			continue
		}

		if owner, clash := reserved[*a.Color]; clash {
			t.Errorf("%q was recoloured to %s, which %q reserves", a.Name, *a.Color, owner)
		}

		if other, taken := recoloured[*a.Color]; taken {
			t.Errorf("%q and %q were both recoloured to %s", other, a.Name, *a.Color)
		}

		recoloured[*a.Color] = a.Name
	}

	if len(recoloured) != len(f.repos[0].current) {
		t.Fatalf("recoloured %d labels, want all %d squatters", len(recoloured), len(f.repos[0].current))
	}

	// The colours are stable, not merely distinct: recomputing has to hand the
	// same label the same colour, or the second run would recolour every one of
	// them again.
	for _, a := range f.compute().Repos[0].Actions {
		if !isRecolour(a) {
			continue
		}

		if name := recoloured[*a.Color]; name != a.Name {
			t.Errorf("%q was recoloured to %s, which the first run gave %q", a.Name, *a.Color, name)
		}
	}
}

// isRecolour reports whether a is a displaced squatter's recolour rather than a
// configured label's colour: a colour-only update carrying the reason that
// explains it. This is the same shape the renderer keys its "recolour" verb off.
func isRecolour(a plan.Action) bool {
	return a.Kind == plan.KindUpdate &&
		a.Color != nil &&
		a.NewName == nil &&
		a.Description == nil &&
		a.Reason != ""
}

// TestDeterminism_UncoveredRepositoryStaysUntouchedAcrossRuns pins the safety
// property against the same repeat: a repository the config does not cover has
// no actions on any run, so nothing about it can churn.
func TestDeterminism_UncoveredRepositoryStaysUntouchedAcrossRuns(t *testing.T) {
	f := fixtureNamed(t, "squatters, renames and drift across three repositories")

	for run := range 4 {
		for _, repo := range f.compute().Repos {
			if repo.Repo == uncovered && len(repo.Actions) != 0 {
				t.Fatalf("run %d touched the uncovered repository: %#v", run+1, repo.Actions)
			}
		}
	}
}

// shuffle returns a permuted copy, leaving the fixture's slice alone so the
// permutations stay independent of one another.
func shuffle[T any](rng *rand.Rand, in []T) []T {
	out := slices.Clone(in)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })

	return out
}

// apply simulates the run: it folds a repository's actions into its labels and
// returns the state the repository would be in afterwards.
//
// A delete is applied, which means every prune candidate is taken — the harshest
// reading, and the one that has to converge. A no-op changes nothing, which is
// what makes it a no-op.
func apply(current []plan.Label, actions []plan.Action) []plan.Label {
	labels := slices.Clone(current)

	for _, a := range actions {
		i := slices.IndexFunc(labels, func(l plan.Label) bool {
			return strings.EqualFold(l.Name, a.Name)
		})

		switch a.Kind {
		case plan.KindCreate:
			labels = append(labels, plan.Label{
				Name:        a.Name,
				Color:       deref(a.Color),
				Description: deref(a.Description),
			})
		case plan.KindUpdate:
			if i < 0 {
				continue
			}

			if a.NewName != nil {
				labels[i].Name = *a.NewName
			}

			if a.Color != nil {
				labels[i].Color = *a.Color
			}

			if a.Description != nil {
				labels[i].Description = *a.Description
			}
		case plan.KindDelete:
			if i >= 0 {
				labels = slices.Delete(labels, i, i+1)
			}
		case plan.KindNoOp:
		}
	}

	return labels
}

// deref reads an optional field, treating nil as the zero value: a create always
// carries both, and a plan that somehow does not still has to apply.
func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// ndjson renders p through a JSONWriter and returns stdout, the machine-readable
// half of the byte-identical claim.
func ndjson(t *testing.T, p plan.Plan) string {
	t.Helper()

	var stdout, stderr bytes.Buffer

	plan.Render(output.NewJSONWriter(&stdout, &stderr), p)

	return stdout.String()
}
