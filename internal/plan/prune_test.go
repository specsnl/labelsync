package plan

import (
	"reflect"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
)

// candidatePlan is two repositories with something to keep and something to
// remove in each, which is the shape that catches a filter keyed on the label
// name alone: both repositories hold a candidate called "wontfix".
func candidatePlan() Plan {
	return Plan{Repos: []RepoPlan{
		{
			Repo: "specsnl/example-website",
			Actions: []Action{
				{Kind: KindCreate, Repo: "specsnl/example-website", Name: "type: bug", Color: new("d73a4a")},
				{Kind: KindDelete, Repo: "specsnl/example-website", Name: "duplicate", Reason: "unconfigured"},
				{Kind: KindDelete, Repo: "specsnl/example-website", Name: "wontfix", Reason: "unconfigured"},
			},
		},
		{
			Repo:           "specsnl/example-platform",
			IssuesDisabled: true,
			Actions: []Action{
				{Kind: KindNoOp, Repo: "specsnl/example-platform", Name: "type: bug"},
				{Kind: KindDelete, Repo: "specsnl/example-platform", Name: "wontfix", Reason: "unconfigured"},
			},
		},
	}}
}

// TestCandidates is the order the prompt is built from. A destructive question
// whose rows move between two runs over the same input is one a user cannot
// answer twice the same way.
func TestCandidates(t *testing.T) {
	want := []Candidate{
		{Repo: "specsnl/example-website", Name: "duplicate"},
		{Repo: "specsnl/example-website", Name: "wontfix"},
		{Repo: "specsnl/example-platform", Name: "wontfix"},
	}

	if got := Candidates(candidatePlan()); !reflect.DeepEqual(got, want) {
		t.Errorf("Candidates() =\n  %+v\nwant\n  %+v", got, want)
	}

	if got := Candidates(Plan{}); got != nil {
		t.Errorf("Candidates(empty) = %+v, want nil", got)
	}
}

// TestCandidatesIgnoresEveryOtherKind keeps the candidate list to the actions
// that are actually destructive. A recoloured squatter is a candidate because the
// planner emitted a delete for it as well, never because of the recolour.
func TestCandidatesIgnoresEveryOtherKind(t *testing.T) {
	p := Plan{Repos: []RepoPlan{{
		Repo: "specsnl/example-website",
		Actions: []Action{
			{Kind: KindUpdate, Repo: "specsnl/example-website", Name: "wontfix", Color: new("13ec13")},
			{Kind: KindCreate, Repo: "specsnl/example-website", Name: "type: bug", Color: new("d73a4a")},
			{Kind: KindNoOp, Repo: "specsnl/example-website", Name: "docs"},
		},
	}}}

	if got := Candidates(p); got != nil {
		t.Errorf("Candidates() = %+v, want nil: nothing here is a delete", got)
	}
}

func TestRetainDeletes(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep []Candidate
		want Plan
	}{
		{
			// What --prune=all asks for: every candidate back, and therefore the
			// plan the user was shown, unchanged.
			name: "every candidate leaves the plan as it was",
			keep: Candidates(candidatePlan()),
			want: candidatePlan(),
		},
		{
			// The repositories survive with their other actions: a declined
			// candidate is not a repository that goes untouched.
			name: "no candidate leaves every other action",
			keep: nil,
			want: Plan{Repos: []RepoPlan{
				{
					Repo: "specsnl/example-website",
					Actions: []Action{
						{Kind: KindCreate, Repo: "specsnl/example-website", Name: "type: bug", Color: new("d73a4a")},
					},
				},
				{
					Repo:           "specsnl/example-platform",
					IssuesDisabled: true,
					Actions: []Action{
						{Kind: KindNoOp, Repo: "specsnl/example-platform", Name: "type: bug"},
					},
				},
			}},
		},
		{
			// The name is not the key. Two repositories holding a "wontfix" is the
			// ordinary case, and selecting one must not take the other with it.
			name: "the same name in two repositories is two candidates",
			keep: []Candidate{{Repo: "specsnl/example-platform", Name: "wontfix"}},
			want: Plan{Repos: []RepoPlan{
				{
					Repo: "specsnl/example-website",
					Actions: []Action{
						{Kind: KindCreate, Repo: "specsnl/example-website", Name: "type: bug", Color: new("d73a4a")},
					},
				},
				{
					Repo:           "specsnl/example-platform",
					IssuesDisabled: true,
					Actions: []Action{
						{Kind: KindNoOp, Repo: "specsnl/example-platform", Name: "type: bug"},
						{Kind: KindDelete, Repo: "specsnl/example-platform", Name: "wontfix", Reason: "unconfigured"},
					},
				},
			}},
		},
		{
			// A selection can only ever narrow. Nothing a candidate list did not
			// carry can be introduced by handing it back.
			name: "a candidate the plan does not carry adds nothing",
			keep: []Candidate{{Repo: "specsnl/example-website", Name: "never-offered"}},
			want: Plan{Repos: []RepoPlan{
				{
					Repo: "specsnl/example-website",
					Actions: []Action{
						{Kind: KindCreate, Repo: "specsnl/example-website", Name: "type: bug", Color: new("d73a4a")},
					},
				},
				{
					Repo:           "specsnl/example-platform",
					IssuesDisabled: true,
					Actions: []Action{
						{Kind: KindNoOp, Repo: "specsnl/example-platform", Name: "type: bug"},
					},
				},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetainDeletes(candidatePlan(), tc.keep); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RetainDeletes() =\n  %+v\nwant\n  %+v", got.Repos, tc.want.Repos)
			}
		})
	}
}

// TestRetainDeletesLeavesTheInputAlone is the plan-is-a-value rule. The caller
// still has the full plan afterwards, because that is the one that was reported
// and the one the exit code is derived from.
func TestRetainDeletesLeavesTheInputAlone(t *testing.T) {
	p := candidatePlan()

	RetainDeletes(p, nil)

	if !reflect.DeepEqual(p, candidatePlan()) {
		t.Errorf("the input plan was modified:\n  %+v", p.Repos)
	}
}

// TestRetainDeletesEmptiesToNil keeps a filtered repository indistinguishable
// from one Compute found nothing to do for. The two mean the same thing, and a
// stray empty slice is the kind of difference that only shows up in somebody
// else's test.
func TestRetainDeletesEmptiesToNil(t *testing.T) {
	p := Plan{Repos: []RepoPlan{{
		Repo:    "specsnl/example-website",
		Actions: []Action{{Kind: KindDelete, Repo: "specsnl/example-website", Name: "wontfix"}},
	}}}

	got := RetainDeletes(p, nil)

	if got.Repos[0].Actions != nil {
		t.Errorf("Actions = %+v, want nil", got.Repos[0].Actions)
	}

	if len(got.Repos) != 1 {
		t.Errorf("Repos = %+v, want the repository kept: it was still visited", got.Repos)
	}
}

// TestRetainDeletesRoundTripsWhatComputeEmitted is the two halves of prune mode
// meeting: every candidate Compute recorded, offered and taken, is the plan
// Compute produced.
func TestRetainDeletesRoundTripsWhatComputeEmitted(t *testing.T) {
	p := Plan{Repos: []RepoPlan{Compute(
		repo,
		[]config.Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}},
		[]Label{{Name: "type: bug", Color: "d73a4a", Description: "A defect"}, {Name: "wontfix", Color: "ffffff"}},
		ModePrune,
		nil,
	)}}

	candidates := Candidates(p)
	if len(candidates) != 1 {
		t.Fatalf("Candidates() = %+v, want the one unconfigured label", candidates)
	}

	if got := RetainDeletes(p, candidates); !reflect.DeepEqual(got, p) {
		t.Errorf("RetainDeletes(p, Candidates(p)) =\n  %+v\nwant\n  %+v", got.Repos, p.Repos)
	}
}
