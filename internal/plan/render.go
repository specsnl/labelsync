package plan

// render.go turns a [Plan] into the two things a user can read: the pretty
// diff, grouped per repository and closed by a summary line, and the NDJSON
// stream, one action per line plus a final summary object.
//
// Both renderings come out of one pass and go through [output.Writer], so the
// --output flag picks between them rather than the caller doing so. The
// package still never touches the network: rendering is a projection of a plan
// that is already computed.

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/specsnl/labelsync/internal/util/output"
)

// SummaryKind is the "kind" of the final NDJSON object.
//
// It is not an action [Kind] and never appears on an [Action]. It shares the
// "kind" key so that a consumer reading the stream has one discriminator rather
// than two: `jq 'select(.kind == "summary")'` picks the totals out, and
// `select(.kind != "summary")` leaves a stream of actions. Like the action
// kinds, the string is a wire contract — it may be added to, never renamed.
const SummaryKind = "summary"

var (
	styleRepo   = lipgloss.NewStyle().Bold(true)
	styleCreate = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(10)) // bright green
	styleUpdate = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(11)) // bright yellow
	styleDelete = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(9))  // bright red
	styleNoOp   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))  // bright black
	styleReason = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))
)

// indent is the per-repository indent. Actions are two spaces in from the
// repository heading they belong to; the heading itself is flush left.
const indent = "  "

// Summary is the closing count of a rendered plan: the last line of the pretty
// diff, and the last object of the NDJSON stream.
//
// Unchanged counts [KindNoOp] actions — labels that were checked and already
// matched. They are reported precisely so a clean run says "I looked" rather
// than saying nothing at all.
type Summary struct {
	Kind         string `json:"kind"` // always SummaryKind
	Repositories int    `json:"repositories"`
	Created      int    `json:"created"`
	Updated      int    `json:"updated"`
	Deleted      int    `json:"deleted"`
	Unchanged    int    `json:"unchanged"`
}

// Summarise counts a plan. It is exported because the counts are also what a
// caller decides an exit code from — a dry run with anything but no-ops has
// found drift.
func Summarise(p Plan) Summary {
	s := Summary{Kind: SummaryKind, Repositories: len(p.Repos)}

	for _, repo := range p.Repos {
		for _, a := range repo.Actions {
			switch a.Kind {
			case KindCreate:
				s.Created++
			case KindUpdate:
				s.Updated++
			case KindDelete:
				s.Deleted++
			case KindNoOp:
				s.Unchanged++
			}
		}
	}

	return s
}

// Render writes p to w: the pretty diff for a human, the NDJSON stream for a
// machine. Which one lands is the writer's business, not the caller's.
func Render(w output.Writer, p Plan) {
	w.WriteDiff(Diff(p))
}

// Diff prepares both renderings of p without writing them. [Render] is the call
// site to reach for; this one exists so a test can assert on either projection,
// and so a future `plan -o file` can take the records without a writer.
func Diff(p Plan) output.DiffData {
	summary := Summarise(p)

	records := make([]any, 0, countActions(p)+1)

	for _, repo := range p.Repos {
		for _, a := range repo.Actions {
			records = append(records, a)
		}
	}

	records = append(records, summary)

	return output.DiffData{Text: renderText(p, summary), Records: records}
}

// countActions is the exact capacity for the record slice — one entry per
// action, and every action is emitted. Collapsing an unchanged repository is a
// choice the pretty rendering makes for a reader; the stream reports what the
// planner decided.
func countActions(p Plan) int {
	n := 0
	for _, repo := range p.Repos {
		n += len(repo.Actions)
	}

	return n
}

// renderText assembles the pretty diff: a block per repository, then the
// summary line, separated by blank lines and with no trailing newline — the
// writer adds that.
func renderText(p Plan, summary Summary) string {
	blocks := make([]string, 0, len(p.Repos)+1)

	for _, repo := range p.Repos {
		blocks = append(blocks, styleRepo.Render(repo.Repo)+"\n"+renderRepo(repo))
	}

	blocks = append(blocks, renderSummary(summary))

	return strings.Join(blocks, "\n\n")
}

// renderRepo renders one repository's actions, indented under its heading.
//
// A repository whose every action is a no-op collapses to a single line. On a
// converged run that is the difference between one screen and several hundred
// identical "= ok" lines, and the information lost — which labels were checked —
// is still in the NDJSON stream and still true of every one of them.
func renderRepo(repo RepoPlan) string {
	var rows [][]string

	if unchanged(repo) {
		rows = [][]string{{
			styleNoOp.Render("="),
			styleNoOp.Render("ok"),
			fmt.Sprintf("(%d labels, no changes)", len(repo.Actions)),
		}}
	} else {
		rows = make([][]string, len(repo.Actions))
		for i, a := range repo.Actions {
			rows[i] = row(a)
		}
	}

	lines := strings.Split(output.RenderColumns(rows), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}

	return strings.Join(lines, "\n")
}

// unchanged reports whether a repository has nothing to do. A repository with no
// actions at all counts: it was visited and needed nothing, which is the same
// thing a reader wants to know.
func unchanged(repo RepoPlan) bool {
	for _, a := range repo.Actions {
		if a.Kind != KindNoOp {
			return false
		}
	}

	return true
}

// row renders one action as the cells of a diff line: gutter, verb, name,
// colour, description, reason.
//
// Every row carries all six cells, empty ones included, so the reason column
// starts at the same offset whether or not the rows above it set a description.
// [output.RenderColumns] trims the trailing padding, so an empty tail costs
// nothing.
func row(a Action) []string {
	gutter, verb := verbOf(a)

	return []string{
		gutter,
		verb,
		name(a),
		colour(a),
		description(a),
		reason(a),
	}
}

// verbOf returns the gutter character and the styled verb for an action.
//
// "recolour" is an update that changes only the colour and says why: the
// displaced squatter of the reconciliation algorithm. It reads as a different
// operation from an ordinary update because it is one — nobody configured it,
// and the reason is what makes it make sense.
func verbOf(a Action) (string, string) {
	switch a.Kind {
	case KindCreate:
		return styleCreate.Render("+"), styleCreate.Render("create")
	case KindUpdate:
		if isRecolour(a) {
			return styleUpdate.Render("~"), styleUpdate.Render("recolour")
		}

		return styleUpdate.Render("~"), styleUpdate.Render("update")
	case KindDelete:
		return styleDelete.Render("-"), styleDelete.Render("delete")
	case KindNoOp:
		return styleNoOp.Render("="), styleNoOp.Render("ok")
	default:
		// Not reachable from a plan this package computes, but a plan read back
		// from a file is only as trustworthy as whoever wrote it, and a
		// rendering that silently dropped the verb would be worse than one that
		// shows the unknown string.
		return "?", string(a.Kind)
	}
}

// isRecolour reports whether a is a displaced squatter's recolour: a
// colour-only update carrying the reason that explains it.
func isRecolour(a Action) bool {
	return a.Kind == KindUpdate &&
		a.Color != nil &&
		a.NewName == nil &&
		a.Description == nil &&
		a.Reason != ""
}

// name renders the name cell, showing a rename as the transition it is.
func name(a Action) string {
	if a.NewName != nil {
		return a.Name + " → " + *a.NewName
	}

	return a.Name
}

// colour renders the colour cell in the colour itself, so a diff of six hex
// codes is scannable rather than a wall of digits. The style degrades with the
// stream: into a pipe it is the plain hex.
//
// Only the new colour is shown. An action carries the change, not the state it
// replaces — see the note in the architecture docs.
func colour(a Action) string {
	if a.Color == nil {
		return ""
	}

	hex := "#" + *a.Color

	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(hex)
}

// description renders the description cell, quoted so an empty one is visible.
// A pointer to "" is an update clearing the description, which renders as `""` —
// a blank cell would read as "unchanged", which is the one thing it is not.
func description(a Action) string {
	if a.Description == nil {
		return ""
	}

	return strconv.Quote(*a.Description)
}

// reason renders the reason cell in parentheses. It is the annotation the field
// exists for: a recolour that looks arbitrary becomes obvious next to
// `(displaced by "type: bug")`.
func reason(a Action) string {
	if a.Reason == "" {
		return ""
	}

	return styleReason.Render("(" + a.Reason + ")")
}

// renderSummary is the closing line: repositories, then one count per kind.
//
// Every count is shown even at zero. The line is read by eye across runs, and a
// column that appears only when non-zero moves the others, which is exactly
// what makes two runs hard to compare.
func renderSummary(s Summary) string {
	return strings.Join([]string{
		plural(s.Repositories, "repository", "repositories"),
		fmt.Sprintf("%d created", s.Created),
		fmt.Sprintf("%d updated", s.Updated),
		fmt.Sprintf("%d deleted", s.Deleted),
		fmt.Sprintf("%d unchanged", s.Unchanged),
	}, " · ")
}

// plural renders a count with the right noun. Only the repository count needs
// it: "1 created" is already correct English, and "1 repositories" is not.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return strconv.Itoa(n) + " " + many
}
