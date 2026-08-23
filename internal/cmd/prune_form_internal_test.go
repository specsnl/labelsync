package cmd

// This file is an internal test — the only one in the package — because what it
// checks is how the prune prompt is *rendered*, and a form is not something the
// App seam can hand back. Everything else about prune is behaviour, and is tested
// through the command in prune_test.go.

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/specsnl/labelsync/internal/plan"
)

// candidatesFor is n removal candidates over n repositories, all carrying the same
// label name — the shape the bug showed up in: one line per repository, and every
// line reading almost identically, so a viewport of one row looks like a prompt
// with a single entry rather than a list that has been cut off.
func candidatesFor(n int) []plan.Candidate {
	out := make([]plan.Candidate, 0, n)
	for i := range n {
		out = append(out, plan.Candidate{Repo: fmt.Sprintf("specsnl/php%02d", 83+i), Name: "dependencies"})
	}

	return out
}

// render draws the form once at a fixed terminal size. The window size is what
// decides whether the list has to scroll, so it is the input under test, not an
// incidental detail of the harness.
func render(t *testing.T, candidates []plan.Candidate, width, height int) string {
	t.Helper()

	var selected []plan.Candidate

	model, _ := pruneForm(candidates, &selected).Update(tea.WindowSizeMsg{Width: width, Height: height})

	return fmt.Sprint(model.View())
}

// TestPruneFormShowsEveryCandidate is the regression: with room on screen, every
// candidate is on screen. The prompt used to render a one-row viewport regardless
// of how much space there was, because the field subtracted its own title and
// description from a height derived from the option lines alone.
func TestPruneFormShowsEveryCandidate(t *testing.T) {
	t.Parallel()

	for _, count := range []int{1, 3, 20} {
		t.Run(fmt.Sprintf("%d candidates", count), func(t *testing.T) {
			t.Parallel()

			candidates := candidatesFor(count)
			view := render(t, candidates, 120, 60)

			for _, candidate := range candidates {
				if !strings.Contains(view, candidate.Repo) {
					t.Errorf("%s is missing from the prompt:\n%s", candidate.Repo, view)
				}
			}
		})
	}
}

// TestPruneFormFitsAShortTerminal is the other half of the same setting: the list
// is allowed to scroll when it genuinely cannot fit, and stays within a line of
// the window when it does.
//
// Within a line, and not exactly within the window, because huh sizes the group
// from the header and the help footer and does not count the blank line it puts
// between them — so a form that has to scroll renders one line taller than the
// height it was given. That is huh's arithmetic, not a height this package
// chooses, and one line of overdraw on a terminal too short for the prompt is not
// worth computing the window size here to work around.
func TestPruneFormFitsAShortTerminal(t *testing.T) {
	t.Parallel()

	const height = 12

	view := render(t, candidatesFor(40), 120, height)
	lines := len(strings.Split(strings.TrimRight(view, "\n"), "\n"))

	if lines > height+1 {
		t.Errorf("prompt is %d lines tall in a %d-line terminal:\n%s", lines, height, view)
	}

	if !strings.Contains(view, "specsnl/php83") {
		t.Errorf("the first candidate is not visible:\n%s", view)
	}
}

// TestPruneFormSelectsNothingByDefault guards the pre-selection decision from the
// build-up of options the form is configured with: enter on an untouched prompt
// deletes nothing.
func TestPruneFormSelectsNothingByDefault(t *testing.T) {
	t.Parallel()

	view := render(t, candidatesFor(3), 120, 60)

	if strings.Contains(view, "✓") {
		t.Errorf("a candidate arrives pre-selected:\n%s", view)
	}
}
