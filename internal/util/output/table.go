package output

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	tableHeaderStyle = lipgloss.NewStyle().Bold(true)
	tableBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(240))
)

// columnGap separates adjacent columns. Two spaces read as a column break
// without needing a rule between them.
const columnGap = "  "

// RenderColumns aligns rows into columns and returns them as lines, with no
// header, no border, and no trailing whitespace.
//
// This is the renderer behind the pretty diff. The diff is a list, not a table —
// there is nothing to put in a header row and a box around it would fight the
// per-repository grouping — but the action, name, and colour columns still have
// to line up down the page or the changes are unreadable:
//
//	+ create    type: bug       #d73a4a  "Something isn't working"
//	~ recolour  wontfix         #d73a4a → #16a3c4
//	= ok        priority: high
//
// Callers supply the gutter (the +/~/=) as the first cell. Short rows are fine:
// a row with fewer cells than the widest simply ends early.
func RenderColumns(rows [][]string) string {
	widths := columnWidths(rows)
	lines := make([]string, len(rows))

	for i, row := range rows {
		lines[i] = strings.TrimRight(joinCells(row, widths), " ")
	}

	return strings.Join(lines, "\n")
}

// tableChrome is the width the box adds around the content: one border
// column and one column of padding on each side.
const tableChrome = 4

// RenderTable renders headers and rows as a bordered, column-aligned table.
// Column widths are computed from the content, headers included.
//
// maxWidth is the widest the table may be, border included. When the natural
// layout is wider, the widest columns give way first and their cells wrap over
// several lines — at spaces where they can, hard-broken where a single token is
// longer than the column. Narrow columns keep their natural width. A wrapped
// table separates its rows with a blank line, so a continuation line cannot be
// mistaken for the next row. Zero or less means unbounded: the natural layout,
// whatever its width. A terminal too narrow for even one character per column
// still gets that one character; the table is then wider than maxWidth.
//
// This is what [PrettyWriter.WriteTable] uses — list output such as `groups` or
// `cache info`, where a header row is meaningful. For the diff, see
// [RenderColumns].
func RenderTable(headers []string, rows [][]string, maxWidth int) string {
	widths := columnWidths(append([][]string{headers}, rows...))

	wrapped := maxWidth > 0 && totalWidth(widths)+tableChrome > maxWidth
	if wrapped {
		shrinkColumns(widths, maxWidth-tableChrome)
	}

	var sb strings.Builder

	for i, line := range wrapRow(headers, widths) {
		if i > 0 {
			sb.WriteString("\n")
		}

		sb.WriteString(tableHeaderStyle.Render(line))
	}

	sb.WriteString("\n")
	sb.WriteString(tableBorderStyle.Render(strings.Repeat("─", totalWidth(widths))))

	for i, row := range rows {
		if wrapped && i > 0 {
			sb.WriteString("\n")
		}

		for _, line := range wrapRow(row, widths) {
			sb.WriteString("\n")
			sb.WriteString(line)
		}
	}

	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.ANSIColor(240)).
		Padding(0, 1).
		Render(sb.String())
}

// shrinkColumns narrows widths in place until the row fits in avail, one
// column at a time and always the widest, so the columns that cause the
// overflow absorb it and the narrow ones are only touched once everything is
// equally narrow. No column goes below one.
func shrinkColumns(widths []int, avail int) {
	for totalWidth(widths) > avail {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}

		if widths[widest] <= 1 {
			return
		}

		widths[widest]--
	}
}

// wrapRow wraps each cell to its column width and lays the result out as one
// or more lines, padding every cell — blank continuation cells included — so
// each column starts at the same offset on every line. Lines are right-trimmed.
func wrapRow(row []string, widths []int) []string {
	cells := make([][]string, len(row))
	height := 1

	for i, cell := range row {
		cells[i] = wrapCell(cell, widths[i])
		height = max(height, len(cells[i]))
	}

	lines := make([]string, height)
	line := make([]string, len(row))

	for l := range lines {
		for i := range row {
			line[i] = ""
			if l < len(cells[i]) {
				line[i] = cells[i][l]
			}
		}

		lines[l] = strings.TrimRight(joinCells(line, widths), " ")
	}

	return lines
}

// wrapCell breaks s into lines no wider than width. It fills each line with
// whole space-separated words, and hard-breaks only a word that is longer than
// width on its own.
func wrapCell(s string, width int) []string {
	if lipgloss.Width(s) <= width {
		return []string{s}
	}

	var (
		lines []string
		cur   string
	)

	for word := range strings.FieldsSeq(s) {
		switch {
		case cur == "":
		case lipgloss.Width(cur)+1+lipgloss.Width(word) <= width:
			cur += " " + word

			continue
		default:
			lines = append(lines, cur)
		}

		pieces := strings.Split(ansi.Hardwrap(word, width, false), "\n")
		lines = append(lines, pieces[:len(pieces)-1]...)
		cur = pieces[len(pieces)-1]
	}

	return append(lines, cur)
}

// columnWidths returns the display width of the widest cell in each column.
// Width, not byte length: a label name is free to contain an emoji, and len()
// would over-pad the column by several characters.
func columnWidths(rows [][]string) []int {
	var widths []int

	for _, row := range rows {
		for i, cell := range row {
			for len(widths) <= i {
				widths = append(widths, 0)
			}

			if w := lipgloss.Width(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	return widths
}

// joinCells pads each cell to its column width and joins them with columnGap.
// The final cell is padded too; callers trim the line.
func joinCells(row []string, widths []int) string {
	var sb strings.Builder

	for i, cell := range row {
		if i > 0 {
			sb.WriteString(columnGap)
		}

		sb.WriteString(cell)
		sb.WriteString(strings.Repeat(" ", max(0, widths[i]-lipgloss.Width(cell))))
	}

	return sb.String()
}

// totalWidth is the rendered width of a full row: every column plus the gaps
// between them.
func totalWidth(widths []int) int {
	total := 0
	for _, w := range widths {
		total += w
	}

	if len(widths) > 1 {
		total += (len(widths) - 1) * len(columnGap)
	}

	return total
}
