package cmd_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/plan"
	"github.com/specsnl/labelsync/internal/util/progress/progresstest"
)

// TestSync_ReportsEveryPhase runs a prune end to end with a recorder in place of
// the status line, and checks the line is down at the two moments something
// else needs the terminal: when the plan is rendered and the prompt drawn, and
// when the applied record is written.
func TestSync_ReportsEveryPhase(t *testing.T) {
	recorder := &progresstest.Recorder{}

	app, flags := fakeGitHub(t, newStore(squatted()))
	app.Progress = recorder

	var atPrompt string

	app.Prompt = func(_ context.Context, candidates []plan.Candidate) ([]plan.Candidate, error) {
		atPrompt = recorder.Last()

		for _, candidate := range candidates {
			if candidate.Name == "invalid" {
				return []plan.Candidate{candidate}, nil
			}
		}

		return nil, nil
	}

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune")...); err != nil {
		t.Fatalf("sync --mode prune: %v", err)
	}

	if atPrompt != "done" {
		t.Errorf("progress at the prompt = %q, want the read line taken down", atPrompt)
	}

	want := []string{
		"enumerating 0/1 0",
		"enumerating 1/1 1",
		"done",
		"reading 0/1",
		"reading 1/1",
		"done",
		"applying 1/1 0/1 specsnl/example-website",
		"applying 1/1 1/1 specsnl/example-website",
		"done",
	}

	if got := recorder.Calls(); !slices.Equal(got, want) {
		t.Errorf("progress =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestSync_ProgressLeavesStdoutAlone is the contract that lets progress exist at
// all: the plan and the result are the same bytes whether or not anything was
// drawn on stderr.
func TestSync_ProgressLeavesStdoutAlone(t *testing.T) {
	run := func(withProgress bool) string {
		app, flags := fakeGitHub(t, newStore(squatted()))
		if withProgress {
			app.Progress = &progresstest.Recorder{}
		}

		_, stdout, _, err := runApp(t, app, nil, args(writeConfig(t, oneRepo), flags, "sync", "--mode", "prune", "--yes")...)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}

		return stdout
	}

	if without, with := run(false), run(true); without != with {
		t.Errorf("stdout changed with progress on:\n--- without\n%s\n--- with\n%s", without, with)
	}
}

// TestGroups_ReportsEnumeration is the read-only command that shares the
// enumeration line with sync, and takes it down before the table is printed.
func TestGroups_ReportsEnumeration(t *testing.T) {
	recorder := &progresstest.Recorder{}

	app, flags := fakeGitHub(t, orgListing())
	app.Progress = recorder

	if _, _, _, err := runApp(t, app, nil, args(writeConfig(t, groupsConfig), flags, "groups")...); err != nil {
		t.Fatalf("groups: %v", err)
	}

	calls := recorder.Calls()

	if len(calls) == 0 || !strings.HasPrefix(calls[0], "enumerating 0/") {
		t.Errorf("first progress call = %v, want the enumeration line opened at zero", calls)
	}

	if got := recorder.Last(); got != "done" {
		t.Errorf("last progress call = %q, want done", got)
	}
}
