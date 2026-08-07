package output_test

import (
	"strings"
	"testing"

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
	assertGolden(t, "table_groups", output.RenderTable(sampleHeaders, sampleRows))
}

func TestRenderTable_NoRows(t *testing.T) {
	got := output.RenderTable(sampleHeaders, nil)
	if !strings.Contains(got, "Group") {
		t.Errorf("header row missing from an empty table:\n%s", got)
	}
}

func TestJSONKey(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   string
	}{
		{"Repo", "repo"},
		{"New colour", "new_colour"},
		{"# of labels", "of_labels"},
		{"error_kind", "error_kind"},
		{"Last synced (UTC)", "last_synced_utc"},
		{"", ""},
		{"---", ""},
	} {
		t.Run(tc.header, func(t *testing.T) {
			if got := output.JSONKey(tc.header); got != tc.want {
				t.Errorf("JSONKey(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
