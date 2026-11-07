package output_test

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/specsnl/labelsync/internal/util/output"
)

// diffRows is the design plan's pretty diff sample, as the planner will hand it
// over: a gutter cell, an action, a name, a colour transition, and an optional
// trailing note. Rows are deliberately ragged — a no-op has nothing to say about
// colour.
var diffRows = [][]string{
	{"+", "create", "type: bug", "#d73a4a", `"Something isn't working"`},
	{"~", "update", "type: feature", "#1d76db → #0e8a16"},
	{"~", "recolour", "wontfix", "#d73a4a → #16a3c4", `(displaced by "type: bug")`},
	{"=", "ok", "priority: high"},
	{"-", "delete", "old-label", "", "(unconfigured)"},
}

func TestRenderColumns_Golden(t *testing.T) {
	assertGolden(t, "columns_diff", output.RenderColumns(diffRows))
}

func TestRenderColumns_NoTrailingWhitespace(t *testing.T) {
	for i, line := range strings.Split(output.RenderColumns(diffRows), "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line %d has trailing whitespace: %q", i+1, line)
		}
	}
}

// Every cell in a column starts at the same offset, or the diff is unreadable.
func TestRenderColumns_AlignsColumns(t *testing.T) {
	lines := strings.Split(output.RenderColumns(diffRows), "\n")

	// Column 2 is the action verb, present in every sample row. "recolour" is
	// the widest at 8 characters, and column 0 is one character plus the gap.
	const wantOffset = 1 + 2

	for i, line := range lines {
		verb := strings.Fields(line)[1]
		if got := strings.Index(line, verb); got != wantOffset {
			t.Errorf("line %d: action column starts at %d, want %d: %q", i+1, got, wantOffset, line)
		}
	}
}

func TestRenderColumns_Empty(t *testing.T) {
	if got := output.RenderColumns(nil); got != "" {
		t.Errorf("RenderColumns(nil) = %q, want empty", got)
	}
}

// Width is measured in display columns, not bytes: a label name may contain an
// emoji, and len() would over-pad the column by several characters.
func TestRenderColumns_WideRunes(t *testing.T) {
	// "🐛 bug" is six display columns but ten bytes. Measured by len() the first
	// column would be sized at ten and every other row pushed four spaces too far
	// right.
	got := output.RenderColumns([][]string{
		{"🐛 bug", "#d73a4a"},
		{"wide", "#000000"},
	})

	want := "🐛 bug  #d73a4a\nwide    #000000"
	if got != want {
		t.Errorf("RenderColumns measured bytes rather than display width:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTable_Golden(t *testing.T) {
	data := sampleTable()

	assertGolden(t, "table_groups", output.RenderTable(data.Headers, data.Cells, 0))
}

func TestRenderTable_NoRows(t *testing.T) {
	got := output.RenderTable(sampleTable().Headers, nil, 0)
	if !strings.Contains(got, "Group") {
		t.Errorf("header row missing from an empty table:\n%s", got)
	}
}

// orgGroup is the issue's example: one org group whose repository list alone is
// far wider than a normal terminal.
var orgGroup = []string{
	"specsops", "org: specsnl", "6",
	"specsnl/specsops, specsnl/specsops-ansible, specsnl/specsops-ansible-collection, " +
		"specsnl/specsops-golden-images, specsnl/specsops-opentofu, specsnl/specsops-sops",
}

var groupHeaders = []string{"Group", "Source", "Count", "Repositories"}

// plainLines strips the styling from a rendered table and splits it into lines.
func plainLines(rendered string) []string {
	return strings.Split(ansi.Strip(rendered), "\n")
}

// assertBoxed checks the invariants every table holds whatever its width: each
// line is exactly as wide as the top border and no wider than maxWidth, and
// every line between the top and bottom borders starts and ends with a side
// border.
func assertBoxed(t *testing.T, rendered string, maxWidth int) {
	t.Helper()

	lines := plainLines(rendered)
	want := lipgloss.Width(lines[0])

	if want > maxWidth {
		t.Errorf("table is %d columns wide, want at most %d:\n%s", want, maxWidth, ansi.Strip(rendered))
	}

	for i, line := range lines {
		if got := lipgloss.Width(line); got != want {
			t.Errorf("line %d is %d columns wide, want %d: %q", i+1, got, want, line)
		}

		if i == 0 || i == len(lines)-1 {
			continue
		}

		if !strings.HasPrefix(line, "│ ") || !strings.HasSuffix(line, " │") {
			t.Errorf("line %d has a broken side border: %q", i+1, line)
		}
	}
}

// When the natural layout fits, the width changes nothing.
func TestRenderTable_FitsAsIs(t *testing.T) {
	data := sampleTable()
	natural := output.RenderTable(data.Headers, data.Cells, 0)

	for _, width := range []int{lipgloss.Width(plainLines(natural)[0]), 200} {
		if got := output.RenderTable(data.Headers, data.Cells, width); got != natural {
			t.Errorf("width %d changed a table that fits:\ngot:\n%s\nwant:\n%s", width, got, natural)
		}
	}
}

func TestRenderTable_WrapsWideColumn_Golden(t *testing.T) {
	assertGolden(t, "table_wrapped", output.RenderTable(groupHeaders, [][]string{orgGroup}, 96))
}

func TestRenderTable_WrapsWideColumn(t *testing.T) {
	const width = 96

	got := output.RenderTable(groupHeaders, [][]string{orgGroup}, width)
	assertBoxed(t, got, width)

	lines := plainLines(got)
	header := lines[1]
	reposAt := strings.Index(header, "Repositories")

	// The narrow columns keep their natural width, so Repositories starts where
	// it would in an unbounded table.
	if want := strings.Index(plainLines(output.RenderTable(groupHeaders, [][]string{orgGroup}, 0))[1], "Repositories"); reposAt != want {
		t.Errorf("Repositories column starts at %d, want %d", reposAt, want)
	}

	body := lines[3 : len(lines)-1]
	if len(body) < 2 {
		t.Fatalf("repository list did not wrap:\n%s", strings.Join(lines, "\n"))
	}

	var repos []string

	for i, line := range body {
		cell := strings.TrimRight(strings.TrimSuffix(line, "│"), " ")

		// Continuation lines leave the narrow columns blank and resume the
		// repository list at the same offset as the first line.
		if i > 0 && strings.TrimSpace(cell[:reposAt]) != "│" {
			t.Errorf("continuation line %d has content outside the wrapped column: %q", i+1, line)
		}

		repos = append(repos, cell[reposAt:])
	}

	if got, want := strings.Join(repos, " "), orgGroup[3]; got != want {
		t.Errorf("wrapping lost or reordered content:\ngot:  %q\nwant: %q", got, want)
	}

	// Breaks fall between repositories, never inside one.
	for _, r := range repos {
		for name := range strings.SplitSeq(strings.TrimSuffix(r, ","), ", ") {
			if !strings.HasPrefix(name, "specsnl/") {
				t.Errorf("repository name split across lines: %q", name)
			}
		}
	}
}

// A token longer than its column is hard-broken rather than left to overflow.
func TestRenderTable_LongToken(t *testing.T) {
	const width = 30

	token := strings.Repeat("x", 60)
	got := output.RenderTable([]string{"Name", "Value"}, [][]string{{"key", token}}, width)
	assertBoxed(t, got, width)

	plain := ansi.Strip(got)
	if n := strings.Count(plain, "x"); n != len(token) {
		t.Errorf("hard break lost characters: %d of %d remain:\n%s", n, len(token), plain)
	}

	if rows := len(plainLines(got)) - 4; rows < 3 {
		t.Errorf("long token spans %d lines, want at least 3:\n%s", rows, plain)
	}
}

// Wrapped rows are separated by a blank line, so a continuation line cannot be
// read as the start of the next row.
func TestRenderTable_MultiRowWrap(t *testing.T) {
	const width = 60

	other := []string{"websites", "org: specsnl", "2", "specsnl/website-one, specsnl/website-two-with-a-long-name"}
	got := output.RenderTable(groupHeaders, [][]string{orgGroup, other}, width)
	assertBoxed(t, got, width)

	lines := plainLines(got)

	var starts, blanks int

	for _, line := range lines[3 : len(lines)-1] {
		inner := strings.TrimSpace(strings.Trim(line, "│"))

		switch {
		case inner == "":
			blanks++
		case strings.HasPrefix(inner, "specsops") || strings.HasPrefix(inner, "websites"):
			starts++
		}
	}

	if starts != 2 || blanks != 1 {
		t.Errorf("got %d row starts and %d separators, want 2 and 1:\n%s", starts, blanks, ansi.Strip(got))
	}
}

// Wrapping measures display width: an emoji is two columns, not four bytes.
func TestRenderTable_WrapsWideRunes(t *testing.T) {
	const width = 24

	got := output.RenderTable(
		[]string{"Label", "Note"},
		[][]string{{"🐛 bug", "🐛 🐛 🐛 🐛 🐛 🐛 🐛 🐛 🐛 🐛"}},
		width,
	)
	assertBoxed(t, got, width)
}

func TestTerminalWidth_NotATerminal(t *testing.T) {
	if got := output.TerminalWidth(&bytes.Buffer{}); got != 0 {
		t.Errorf("TerminalWidth(buffer) = %d, want 0", got)
	}
}
