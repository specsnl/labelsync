package apply_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/apply"
	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/plan"
)

// fake is a [apply.Writer] that records what it was asked to do, in the order it
// was asked. The order is half of what this package promises, so a fake that
// only counted calls would not be testing it.
type fake struct {
	calls []string

	// fail is consulted before every call; a non-nil result is returned instead
	// of recording one. It is how a mid-run failure is placed exactly where a
	// test wants it.
	fail func(op, repo, name string) error
}

func (f *fake) CreateLabel(_ context.Context, owner, repo string, label github.Label) error {
	return f.record("create", owner+"/"+repo, label.Name,
		fmt.Sprintf("create %s/%s %s #%s %q", owner, repo, label.Name, label.Color, label.Description))
}

func (f *fake) PatchLabel(_ context.Context, owner, repo, current string, patch github.LabelPatch) error {
	return f.record("patch", owner+"/"+repo, current,
		fmt.Sprintf("patch %s/%s %s new_name=%s color=%s description=%s",
			owner, repo, current, show(patch.NewName), show(patch.Color), show(patch.Description)))
}

func (f *fake) record(op, repo, name, line string) error {
	if f.fail != nil {
		if err := f.fail(op, repo, name); err != nil {
			return err
		}
	}

	f.calls = append(f.calls, line)

	return nil
}

// show renders an optional field so that "left alone" and "set to empty" are
// distinguishable in a failure message, which is the distinction the pointers
// exist for.
func show(s *string) string {
	if s == nil {
		return "<nil>"
	}

	return `"` + *s + `"`
}

// inaccessible is what github.Client.Do hands back for a repository that cannot
// be reached: the sentinel, already recorded, wrapped in context.
func inaccessible(repo string) error {
	return fmt.Errorf("%s: create label: %w: forbidden", repo, labelsync.ErrRepoInaccessible)
}

// fullPlan is one repository's worth of every kind append mode executes, in the
// order the planner emits them: a rename, a squatter recolour, a create, an
// update, and a no-op.
func fullPlan() plan.Plan {
	return plan.Plan{Repos: []plan.RepoPlan{{
		Repo: "specsnl/example-website",
		Actions: []plan.Action{
			{Kind: plan.KindUpdate, Repo: "specsnl/example-website", Name: "bug", NewName: new("type: bug")},
			{
				Kind: plan.KindUpdate, Repo: "specsnl/example-website", Name: "wontfix",
				Color: new("16a3c4"), Reason: `displaced by "type: bug"`,
			},
			{
				Kind: plan.KindCreate, Repo: "specsnl/example-website", Name: "type: feature",
				Color: new("a2eeef"), Description: new("New functionality"),
			},
			{
				Kind: plan.KindUpdate, Repo: "specsnl/example-website", Name: "type: bug",
				Color: new("d73a4a"), Description: new(""),
			},
			{Kind: plan.KindNoOp, Repo: "specsnl/example-website", Name: "docs"},
		},
	}}}
}

// TestApplyExecutesInTheEmittedOrder is the crash-consistency guarantee. Every
// intermediate state is coherent only while the order holds, so reordering for
// throughput would be trading a correctness property for wall-clock time the
// token bucket takes back anyway.
func TestApplyExecutesInTheEmittedOrder(t *testing.T) {
	client := &fake{}

	report, err := apply.Apply(t.Context(), client, fullPlan())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	want := []string{
		`patch specsnl/example-website bug new_name="type: bug" color=<nil> description=<nil>`,
		`patch specsnl/example-website wontfix new_name=<nil> color="16a3c4" description=<nil>`,
		`create specsnl/example-website type: feature #a2eeef "New functionality"`,
		`patch specsnl/example-website type: bug new_name=<nil> color="d73a4a" description=""`,
	}

	if !slices.Equal(client.calls, want) {
		t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(client.calls, "\n  "), strings.Join(want, "\n  "))
	}

	if report.Created != 1 || report.Updated != 3 || report.Unchanged != 1 || report.Repositories != 1 {
		t.Errorf("report = %+v, want 1 repository, 1 created, 3 updated, 1 unchanged", report)
	}

	if report.Kind != apply.ReportKind {
		t.Errorf("report.Kind = %q, want %q", report.Kind, apply.ReportKind)
	}
}

// TestApplyNeverSendsANoOp pins the one action kind that exists purely for
// reporting. A no-op is a label that already matches, and spending a request to
// tell GitHub so would make a converged run as expensive as a first one.
func TestApplyNeverSendsANoOp(t *testing.T) {
	client := &fake{}

	p := plan.Plan{Repos: []plan.RepoPlan{{
		Repo: "specsnl/example-website",
		Actions: []plan.Action{
			{Kind: plan.KindNoOp, Repo: "specsnl/example-website", Name: "docs"},
			{Kind: plan.KindNoOp, Repo: "specsnl/example-website", Name: "bug"},
		},
	}}}

	report, err := apply.Apply(t.Context(), client, p)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	if len(client.calls) != 0 {
		t.Errorf("calls = %v, want none", client.calls)
	}

	if report.Unchanged != 2 {
		t.Errorf("report.Unchanged = %d, want 2", report.Unchanged)
	}

	// The same question, asked of the budget check the command makes before it
	// starts: a converged plan costs nothing.
	if got := apply.Writes(p); got != 0 {
		t.Errorf("Writes() = %d, want 0", got)
	}
}

// TestApplyRefusesADeleteBeforeWritingAnything is what "append mode never
// deletes" means in the package that holds the destructive call. Refusing when
// the delete is *reached* would mean a run that created six labels on its way to
// declining the job.
func TestApplyRefusesADeleteBeforeWritingAnything(t *testing.T) {
	client := &fake{}

	p := fullPlan()
	p.Repos[0].Actions = append(p.Repos[0].Actions, plan.Action{
		Kind: plan.KindDelete, Repo: "specsnl/example-website", Name: "old-label", Reason: "unconfigured",
	})

	report, err := apply.Apply(t.Context(), client, p)
	if err == nil {
		t.Fatal("Apply() error = nil, want a refusal")
	}

	if !strings.Contains(err.Error(), "old-label") {
		t.Errorf("error = %q, want it to name the candidate", err)
	}

	if len(client.calls) != 0 {
		t.Errorf("a refused apply wrote %v, want nothing at all", client.calls)
	}

	if report.Created != 0 || report.Updated != 0 {
		t.Errorf("report = %+v, want an empty one", report)
	}
}

// TestApplyContinuesPastAnInaccessibleRepository is the per-repository failure
// rule: one archived repository in a set of fifty must not end the run, and the
// repositories after it are the ones this proves are still applied.
func TestApplyContinuesPastAnInaccessibleRepository(t *testing.T) {
	client := &fake{fail: func(_, repo, _ string) error {
		if repo == "specsnl/example-platform" {
			return inaccessible(repo)
		}

		return nil
	}}

	p := plan.Plan{Repos: []plan.RepoPlan{
		{Repo: "specsnl/example-website", Actions: []plan.Action{create("specsnl/example-website", "type: bug")}},
		{Repo: "specsnl/example-platform", Actions: []plan.Action{create("specsnl/example-platform", "type: bug")}},
		{Repo: "specsnl/example-api", Actions: []plan.Action{create("specsnl/example-api", "type: bug")}},
	}}

	report, err := apply.Apply(t.Context(), client, p)
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil: a skipped repository is not a failed run", err)
	}

	if len(client.calls) != 2 {
		t.Errorf("calls = %v, want the two reachable repositories", client.calls)
	}

	if !slices.Equal(report.Abandoned, []string{"specsnl/example-platform"}) {
		t.Errorf("report.Abandoned = %v, want the one that failed", report.Abandoned)
	}

	if report.Created != 2 {
		t.Errorf("report.Created = %d, want 2", report.Created)
	}
}

// TestApplyStopsAtTheFirstActionARepositoryRejects is the other half of the
// ordering guarantee: the steps after a failed rename assume it landed, so
// carrying on within the repository would apply them against a name that is not
// there.
func TestApplyStopsAtTheFirstActionARepositoryRejects(t *testing.T) {
	client := &fake{fail: func(op, repo, _ string) error {
		if op == "patch" {
			return inaccessible(repo)
		}

		return nil
	}}

	report, err := apply.Apply(t.Context(), client, fullPlan())
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	if len(client.calls) != 0 {
		t.Errorf("calls = %v, want none: the first action failed", client.calls)
	}

	if len(report.Abandoned) != 1 {
		t.Errorf("report.Abandoned = %v, want the repository named", report.Abandoned)
	}
}

// TestApplyStopsTheRunOnAFailureThatIsNotARepositorys covers the other
// classification. A rate-limit wait past --max-wait, or a cancelled context, is
// not one repository's problem, and treating it as one would skip every
// repository left in the set and report a run that mostly succeeded.
func TestApplyStopsTheRunOnAFailureThatIsNotARepositorys(t *testing.T) {
	client := &fake{fail: func(_, repo, _ string) error {
		if repo == "specsnl/example-platform" {
			return fmt.Errorf("waiting: %w", labelsync.ErrMaxWaitExceeded)
		}

		return nil
	}}

	p := plan.Plan{Repos: []plan.RepoPlan{
		{Repo: "specsnl/example-website", Actions: []plan.Action{create("specsnl/example-website", "type: bug")}},
		{Repo: "specsnl/example-platform", Actions: []plan.Action{create("specsnl/example-platform", "type: bug")}},
		{Repo: "specsnl/example-api", Actions: []plan.Action{create("specsnl/example-api", "type: bug")}},
	}}

	report, err := apply.Apply(t.Context(), client, p)
	if !errors.Is(err, labelsync.ErrMaxWaitExceeded) {
		t.Fatalf("error = %v, want one wrapping ErrMaxWaitExceeded", err)
	}

	if len(client.calls) != 1 {
		t.Errorf("calls = %v, want only the repository before the failure", client.calls)
	}

	if report.Created != 1 {
		t.Errorf("report.Created = %d, want the one that landed: a partial apply has to say so", report.Created)
	}
}

// TestApplyRejectsAMalformedRepository covers a plan that was read back rather
// than computed. Splitting "labelsync" into an owner and a name would address
// some other resource entirely.
func TestApplyRejectsAMalformedRepository(t *testing.T) {
	for _, repo := range []string{"labelsync", "specsnl/", "/labelsync", "a/b/c"} {
		t.Run(repo, func(t *testing.T) {
			client := &fake{}

			p := plan.Plan{Repos: []plan.RepoPlan{{Repo: repo, Actions: []plan.Action{create(repo, "type: bug")}}}}

			if _, err := apply.Apply(t.Context(), client, p); !errors.Is(err, labelsync.ErrInvalidRepoRef) {
				t.Fatalf("error = %v, want one wrapping ErrInvalidRepoRef", err)
			}

			if len(client.calls) != 0 {
				t.Errorf("calls = %v, want none", client.calls)
			}
		})
	}
}

// TestWrites counts what an apply will send, which is what the startup budget
// check is made against and what the countdown counts down.
func TestWrites(t *testing.T) {
	if got, want := apply.Writes(fullPlan()), 4; got != want {
		t.Errorf("Writes() = %d, want %d: every action but the no-op", got, want)
	}

	if got := apply.Writes(plan.Plan{}); got != 0 {
		t.Errorf("Writes(empty) = %d, want 0", got)
	}
}

// create is a minimal create action, for the tests that care about which
// repositories were reached rather than about what was sent.
func create(repo, name string) plan.Action {
	return plan.Action{
		Kind:        plan.KindCreate,
		Repo:        repo,
		Name:        name,
		Color:       new("d73a4a"),
		Description: new("Something isn't working"),
	}
}
