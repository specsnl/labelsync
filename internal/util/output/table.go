package output

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
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

// RenderTable renders headers and rows as a bordered, column-aligned table.
// Column widths are computed from the content, headers included.
//
// This is what [PrettyWriter.Table] uses — list output such as `groups` or
// `cache info`, where a header row is meaningful. For the diff, see
// [RenderColumns].
func RenderTable(headers []string, rows [][]string) string {
	widths := columnWidths(append([][]string{headers}, rows...))

	var sb strings.Builder

	sb.WriteString(tableHeaderStyle.Render(strings.TrimRight(joinCells(headers, widths), " ")))
	sb.WriteString("\n")
	sb.WriteString(tableBorderStyle.Render(strings.Repeat("─", totalWidth(widths))))

	for _, row := range rows {
		sb.WriteString("\n")
		sb.WriteString(strings.TrimRight(joinCells(row, widths), " "))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.ANSIColor(240)).
		Padding(0, 1).
		Render(sb.String())
}

// JSONKey normalises a column header into a stable JSON object key: lowercased,
// with every run of non-alphanumeric characters collapsed to a single
// underscore.
//
//	"Repo"        → "repo"
//	"New colour"  → "new_colour"
//	"# of labels" → "of_labels"
//
// Pretty and JSON output share one set of headers, and the two have different
// audiences: a heading is prose that may be reworded, a JSON key is a contract
// consumers match on. Normalising here means a wording change does not silently
// break a `jq` filter — only an actual rename does.
func JSONKey(header string) string {
	var (
		sb        strings.Builder
		pendingUS bool
	)

	for _, r := range header {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if pendingUS && sb.Len() > 0 {
				sb.WriteByte('_')
			}

			pendingUS = false

			sb.WriteRune(unicode.ToLower(r))
		default:
			pendingUS = true
		}
	}

	return sb.String()
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
