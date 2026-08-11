package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/cmd"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/plan"
	"github.com/specsnl/labelsync/internal/util/exit"
)

// squatted is one repository holding the configured labels plus three the config
// does not mention. Every prune test below is about which of those three go.
func squatted() map[string][]storedLabel {
	return map[string][]storedLabel{"specsnl/example-website": {
		{Name: "type: bug", Color: "d73a4a", Description: "Something isn't working"},
		{Name: "type: feature", Color: "a2eeef", Description: "New functionality"},
		{Name: "duplicate", Color: "cccccc", Description: "Unconfigured"},
		{Name: "invalid", Color: "eeeeee", Description: "Unconfigured"},
		{Name: "wontfix", Color: "111111", Description: "Unconfigured"},
	}}
}

// oneRepo is syncConfig's label set over the single repository squatted holds.
const oneRepo = `
version: 1

groups:
  all:
    repos: [specsnl/example-website]

defaults:
  groups: [all]

labels:
  - name: "type: bug"
    color: "d73a4a"
    description: "Something isn't working"
  - name: "type: feature"
    color: "a2eeef"
    description: "New functionality"
`

// names is what a repository holds, in name order, for comparing against the set
// a prune was supposed to leave behind.
func names(labels []storedLabel) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		out = append(out, label.Name)
	}

	return out
}

// TestSync_PruneWithoutATerminalRefusesImmediately is the guard, and the reason it
// exists: a huh prompt shown to a pipe blocks a CI job until somebody cancels it,
// which is the most common way an interactive CLI breaks in a pipeline.
//
// "Immediately" is half the assertion. The refusal has to come before the first
// request, not after the enumeration and the label reads have been spent on a run
// that was never able to finish.
func TestSync_PruneWithoutATerminalRefusesImmediately(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)

	_, _, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...)

	if !errors.Is(err, labelsync.ErrInteractiveRequired) {
		t.Fatalf("error = %v, want one wrapping ErrInteractiveRequired", err)
	}

	if kind := labelsync.KindOf(err); kind != "interactive_required" {
		t.Errorf("error_kind = %q, want interactive_required", kind)
	}

	if code := exit.Of(err); code != exit.Error {
		t.Errorf("exit code = %s, want %s", code, exit.Error)
	}

	if got := store.requests(); len(got) != 0 {
		t.Errorf("the refused run issued %v, want nothing at all: the guard is before the first request", got)
	}

	// The message has to name both ways out, because the run that hit this is
	// almost always a pipeline that wanted one of them.
	for _, want := range []string{"--prune=all", "--dry-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestSync_PruneDryRunNeedsNoTerminal is the exemption. A dry run lists the
// candidates and removes none of them, so it never prompts — and firing the guard
// there would take away the one prune a pull-request check can run.
func TestSync_PruneDryRunNeedsNoTerminal(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)

	_, stdout, _, err := runApp(t, app, nil,
		args(writeConfig(t, oneRepo), flags, "sync", "--dry-run", "--mode", "prune")...)
	if code := exit.Of(err); code != exit.Drift {
		t.Fatalf("exit code = %s, want %s (error: %v)", code, exit.Drift, err)
	}

	if !strings.Contains(stdout, "3 deleted") {
		t.Errorf("stdout = %q, want the three candidates counted", stdout)
	}

	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("a dry run issued %v, want nothing", writes)
	}
}

// TestSync_PruneAllRemovesExactlyTheCandidates is what --prune=all buys, and what
// it must not overreach into. Every unconfigured label goes; every configured one
// stays, with its colour and description.
func TestSync_PruneAllRemovesExactlyTheCandidates(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)
	config := writeConfig(t, oneRepo)

	_, stdout, stderr, err := runApp(t, app, nil,
		args(config, flags, "sync", "--mode", "prune", "--prune", "all")...)
	if err != nil {
		t.Fatalf("sync --mode prune --prune all: %v", err)
	}

	want := []string{"type: bug", "type: feature"}
	if got := names(store.snapshot("specsnl/example-website")); !slices.Equal(got, want) {
		t.Errorf("repository holds %v, want %v", got, want)
	}

	if !strings.Contains(stdout, "applied: 0 created · 0 updated · 3 deleted") {
		t.Errorf("stdout = %q, want the three deletions reported", stdout)
	}

	// Loud on stderr, because nobody was asked, and the consequence is not the one
	// "delete a label" suggests.
	if !strings.Contains(stderr, "3 unconfigured labels") {
		t.Errorf("stderr = %q, want a warning naming what is going", stderr)
	}

	// Convergence: the same command again has nothing left to remove.
	_, stdout, _, err = runApp(t, app, nil, args(config, flags, "sync", "--mode", "prune", "--prune", "all")...)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if !strings.Contains(stdout, "0 created · 0 updated · 0 deleted · 2 unchanged") {
		t.Errorf("the second run is not converged:\n%s", stdout)
	}
}

// TestSync_AppendNeverDeletesEvenWithCandidates is the safety property under the
// condition that would break it. The same repository, the same config, the same
// unconfigured labels — and without --mode=prune not one of them is touched.
func TestSync_AppendNeverDeletesEvenWithCandidates(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)

	// A prompt that would answer "delete everything" if it were ever reached, so
	// that append mode ignoring the selection entirely is what is being asserted
	// rather than there being nothing to select.
	app.Prompt = takeEverything(t)

	_, stdout, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync")...)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	want := []string{"duplicate", "invalid", "type: bug", "type: feature", "wontfix"}
	if got := names(store.snapshot("specsnl/example-website")); !slices.Equal(got, want) {
		t.Errorf("repository holds %v, want %v: append mode never deletes", got, want)
	}

	if !strings.Contains(stdout, "0 deleted") {
		t.Errorf("stdout = %q, want a plan with nothing to delete", stdout)
	}

	for _, write := range store.writes() {
		if strings.HasPrefix(write, "DELETE ") {
			t.Errorf("append mode issued %q", write)
		}
	}
}

// TestSync_PruneRemovesOnlyWhatWasSelected is the interactive path: the selection
// narrows the plan, and the labels that were left unticked survive.
func TestSync_PruneRemovesOnlyWhatWasSelected(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)

	var offered []plan.Candidate

	app.Prompt = func(_ context.Context, candidates []plan.Candidate) ([]plan.Candidate, error) {
		offered = candidates

		// One of the three, chosen by name so that a filter keyed on anything else
		// takes the wrong label and this fails.
		for _, candidate := range candidates {
			if candidate.Name == "invalid" {
				return []plan.Candidate{candidate}, nil
			}
		}

		return nil, nil
	}

	_, stdout, stderr, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...)
	if err != nil {
		t.Fatalf("sync --mode prune: %v", err)
	}

	// Every candidate is offered, in the plan's ascending name order, whatever the
	// repository happened to list them in.
	wantOffered := []plan.Candidate{
		{Repo: "specsnl/example-website", Name: "duplicate"},
		{Repo: "specsnl/example-website", Name: "invalid"},
		{Repo: "specsnl/example-website", Name: "wontfix"},
	}

	if !slices.Equal(offered, wantOffered) {
		t.Errorf("offered %+v, want %+v", offered, wantOffered)
	}

	want := []string{"duplicate", "type: bug", "type: feature", "wontfix"}
	if got := names(store.snapshot("specsnl/example-website")); !slices.Equal(got, want) {
		t.Errorf("repository holds %v, want %v: only the selected candidate goes", got, want)
	}

	if !strings.Contains(stdout, "applied: 0 created · 0 updated · 1 deleted") {
		t.Errorf("stdout = %q, want the one deletion reported", stdout)
	}

	if !strings.Contains(stderr, "removing 1 label") {
		t.Errorf("stderr = %q, want the selection reported", stderr)
	}
}

// TestSync_PruneWithNothingSelectedDeletesNothing is the answer "none of them",
// which is a legitimate one. The appends still land — the user declined the
// removals, not the sync.
func TestSync_PruneWithNothingSelectedDeletesNothing(t *testing.T) {
	store := newStore(map[string][]storedLabel{"specsnl/example-website": {
		{Name: "wontfix", Color: "111111", Description: "Unconfigured"},
	}})

	app, flags := fakeGitHub(t, store)
	app.Prompt = func(context.Context, []plan.Candidate) ([]plan.Candidate, error) { return nil, nil }

	_, stdout, stderr, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...)
	if err != nil {
		t.Fatalf("sync --mode prune: %v", err)
	}

	want := []string{"type: bug", "type: feature", "wontfix"}
	if got := names(store.snapshot("specsnl/example-website")); !slices.Equal(got, want) {
		t.Errorf("repository holds %v, want %v", got, want)
	}

	if !strings.Contains(stdout, "applied: 2 created · 0 updated · 0 deleted") {
		t.Errorf("stdout = %q, want the creates applied and nothing deleted", stdout)
	}

	if !strings.Contains(stderr, "nothing selected") {
		t.Errorf("stderr = %q, want it said out loud that nothing will be removed", stderr)
	}
}

// TestSync_PruneCancelledWritesNothing covers Ctrl-C on the prompt. Backing out of
// a destructive question ends the run: a user who declined the prune did not
// thereby agree to the rest of the plan landing while they were not looking.
func TestSync_PruneCancelledWritesNothing(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)
	app.Prompt = func(context.Context, []plan.Candidate) ([]plan.Candidate, error) {
		return nil, errors.New("user aborted")
	}

	_, _, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...)
	if err == nil {
		t.Fatal("want an error for a cancelled prompt, got none")
	}

	if code := exit.Of(err); code != exit.Error {
		t.Errorf("exit code = %s, want %s", code, exit.Error)
	}

	if writes := store.writes(); len(writes) != 0 {
		t.Errorf("a cancelled prune issued %v, want nothing", writes)
	}
}

// TestSync_PruneWithNothingToRemoveNeverAsks is the converged tree, which is most
// runs. A prompt whose every answer is the same is one that should not be shown.
func TestSync_PruneWithNothingToRemoveNeverAsks(t *testing.T) {
	store := newStore(map[string][]storedLabel{"specsnl/example-website": {
		{Name: "type: bug", Color: "d73a4a", Description: "Something isn't working"},
		{Name: "type: feature", Color: "a2eeef", Description: "New functionality"},
	}})

	app, flags := fakeGitHub(t, store)
	app.Prompt = func(context.Context, []plan.Candidate) ([]plan.Candidate, error) {
		t.Error("the prompt was shown for a repository with no removal candidates")

		return nil, nil
	}

	_, stdout, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...)
	if err != nil {
		t.Fatalf("sync --mode prune: %v", err)
	}

	if !strings.Contains(stdout, "applied: 0 created · 0 updated · 0 deleted · 2 unchanged") {
		t.Errorf("stdout = %q, want a converged run", stdout)
	}
}

// TestSync_PruneFlag pins what --prune accepts, and what it refuses. Both
// refusals are before the first request: a command line that reads as destructive
// and would delete nothing is worth failing on rather than interpreting.
func TestSync_PruneFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{
			name: "all with prune mode is accepted",
			argv: []string{"sync", "--dry-run", "--mode", "prune", "--prune", "all"},
		},
		{
			name:    "any other value is refused",
			argv:    []string{"sync", "--dry-run", "--mode", "prune", "--prune", "some"},
			wantErr: `invalid --prune "some"`,
		},
		{
			name:    "all without prune mode is refused",
			argv:    []string{"sync", "--dry-run", "--prune", "all"},
			wantErr: "--prune=all needs --mode=prune",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(squatted())

			app, flags := fakeGitHub(t, store)

			_, _, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, tc.argv...)...)

			if tc.wantErr == "" {
				if err != nil && exit.Of(err) == exit.Error {
					t.Fatalf("sync: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}

			if got := store.requests(); len(got) != 0 {
				t.Errorf("the refused run issued %v, want nothing at all", got)
			}
		})
	}
}

// TestSync_PruneReportsTheCandidatesBeforeAsking is the report-first rule. The
// prompt is a list of names with no colour, no description, and no reason; the
// diff on stdout is where a user reads what they are about to lose, so it has to
// be there before the question is asked.
func TestSync_PruneReportsTheCandidatesBeforeAsking(t *testing.T) {
	store := newStore(squatted())

	app, flags := fakeGitHub(t, store)

	var reported string

	app.Prompt = func(context.Context, []plan.Candidate) ([]plan.Candidate, error) {
		// Whatever is on stdout at the moment of asking. The plan is written
		// before the prompt or it is not a report. App.Stdout is the buffer
		// runApp pointed the command at, which is the only way to read the
		// stream part-way through a run.
		stdout, ok := app.Stdout.(*bytes.Buffer)
		if !ok {
			t.Fatalf("App.Stdout is %T, want the harness buffer", app.Stdout)
		}

		reported = stdout.String()

		return nil, nil
	}

	if _, _, _, err := runApp(t, app, nil,
		args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...); err != nil {
		t.Fatalf("sync --mode prune: %v", err)
	}

	for _, want := range []string{"specsnl/example-website", "delete", "duplicate", "invalid", "wontfix", "unconfigured"} {
		if !strings.Contains(reported, want) {
			t.Errorf("stdout at the moment of asking does not contain %q:\n%s", want, reported)
		}
	}
}

// takeEverything is a selector that accepts every candidate offered, for the tests
// that assert the prompt is never reached.
func takeEverything(t *testing.T) cmd.Selector {
	t.Helper()

	return func(_ context.Context, candidates []plan.Candidate) ([]plan.Candidate, error) {
		return candidates, nil
	}
}
