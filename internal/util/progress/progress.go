// Package progress draws what a long run is doing while it does it.
//
// A run against a real organisation spends most of its time in three phases that
// print nothing of their own: enumerating the groups, reading every repository's
// labels, and applying the writes one by one. Without a status line, each of
// them looks like a hang.
//
// # One rendering, or none
//
// Progress draws on **stderr**, so stdout — the plan, the table, the result —
// stays byte-for-byte what it would have been. It is drawn only when there is
// somebody to watch it:
//
//	stderr is a TTY + --output=pretty   one line, rewritten in place
//	stderr is not a TTY                 nothing
//	--output=json                       nothing
//	--debug                             nothing
//
// A log or a JSON stream gets nothing rather than a periodic line, because the
// end of every phase is already in the output — the plan, the table, the applied
// record — and a CI log gains nothing from a count it cannot watch move. The
// rate-limit countdown is the exception, and keeps reporting there, because a
// wait is the one silence that can last for minutes. --debug turns progress off
// because the debug log writes whole lines to the same stream, and every one of
// them would land on top of the status line.
package progress

import (
	"fmt"
	"io"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"

	"github.com/specsnl/labelsync/internal/util/output"
)

// Reporter hears about a run as it advances. Every method is safe for
// concurrent use, because the reads it reports on run in parallel.
type Reporter interface {
	// Enumerating reports that groupsDone of groupsTotal selectors have been
	// listed, and that they have turned up repos repositories between them.
	Enumerating(groupsDone, groupsTotal, repos int)

	// Reading reports that done of total repositories have had their labels read.
	Reading(done, total int)

	// Applying reports where the apply phase has got to.
	Applying(a Apply)

	// Done ends the current phase and removes whatever it drew, so the next thing
	// written to the terminal starts on a clean line.
	Done()

	// Pause takes the line down until the matching Resume, for something else that
	// has to draw on the same row: the rate-limit countdown. Pauses nest.
	Pause()

	// Resume undoes one Pause, and redraws the line once the last one is undone.
	Resume()
}

// Apply is where the apply phase has got to.
type Apply struct {
	// Repo is the repository being written to, as owner/repo.
	Repo string

	// Repos is the 1-based position of Repo in the plan, of ReposTotal.
	Repos      int
	ReposTotal int

	// Writes is how many requests have been sent so far, of WritesTotal.
	Writes      int
	WritesTotal int
}

// New picks the rendering from what --output asked for, whether --debug is on,
// and what stderr turns out to be.
func New(stderr io.Writer, format output.Format, debug bool) Reporter {
	if format != output.FormatPretty || debug || !output.IsTTY(stderr) {
		return Nop()
	}

	return NewLive(stderr, terminalWidth(stderr))
}

// Nop is the rendering that draws nothing.
func Nop() Reporter { return nop{} }

type nop struct{}

func (nop) Enumerating(int, int, int) {}
func (nop) Reading(int, int)          {}
func (nop) Applying(Apply)            {}
func (nop) Done()                     {}
func (nop) Pause()                    {}
func (nop) Resume()                   {}

// eraseLine is "erase from the cursor to the end of the line". Paired with a
// carriage return it blanks the row however long the previous line was.
const eraseLine = "\x1b[K"

// NewLive is the in-place rendering, over a stream it assumes is a terminal.
// [New] is what a command calls; this is exported so a test can drive the
// rendering without needing a terminal to hand it one.
//
// width reports the terminal's width in columns, and is asked before every
// redraw so a resized window is honoured. A line longer than the terminal wraps,
// and a carriage return only goes back to the start of the last row it wrapped
// onto, so every line is cut to fit.
func NewLive(stderr io.Writer, width func() int) *Live {
	return &Live{out: stderr, width: width}
}

// Live rewrites one line in place.
type Live struct {
	mu    sync.Mutex
	out   io.Writer
	width func() int

	// text is what the line says, or "" between phases.
	text string

	// paused counts the Pause calls not yet resumed.
	paused int

	// drawn says the row currently holds a line that has to be erased.
	drawn bool
}

// Enumerating implements [Reporter].
func (l *Live) Enumerating(groupsDone, groupsTotal, repos int) {
	l.set(fmt.Sprintf("enumerating… %d/%s · %s",
		groupsDone, count(groupsTotal, "group", "groups"), count(repos, "repository", "repositories")))
}

// Reading implements [Reporter].
func (l *Live) Reading(done, total int) {
	l.set(fmt.Sprintf("reading labels… %d/%s", done, count(total, "repository", "repositories")))
}

// Applying implements [Reporter].
func (l *Live) Applying(a Apply) {
	l.set(fmt.Sprintf("applying… %d/%s · %d/%s · %s",
		a.Repos, count(a.ReposTotal, "repository", "repositories"),
		a.Writes, count(a.WritesTotal, "write", "writes"),
		a.Repo))
}

// Done implements [Reporter].
func (l *Live) Done() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.text = ""
	l.erase()
}

// Pause implements [Reporter].
func (l *Live) Pause() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.paused++
	l.erase()
}

// Resume implements [Reporter].
func (l *Live) Resume() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.paused > 0 {
		l.paused--
	}

	if l.paused == 0 && l.text != "" {
		l.draw()
	}
}

func (l *Live) set(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.text = text

	if l.paused == 0 {
		l.draw()
	}
}

func (l *Live) draw() {
	fmt.Fprint(l.out, "\r"+eraseLine+fit(l.text, l.width()))

	l.drawn = true
}

func (l *Live) erase() {
	if !l.drawn {
		return
	}

	fmt.Fprint(l.out, "\r"+eraseLine)

	l.drawn = false
}

// fit cuts text to one column short of width, so the cursor never sits past the
// last column, where some terminals wrap it onto a row of its own. Counting
// runes is exact here: every character the lines are built from is one column
// wide.
func fit(text string, width int) string {
	limit := width - 1
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}

	runes := []rune(text)

	return string(runes[:limit-1]) + "…"
}

// fallbackWidth is assumed when the terminal will not say how wide it is.
const fallbackWidth = 80

// terminalWidth asks the terminal behind stream how wide it is, every time it is
// called.
func terminalWidth(stream any) func() int {
	return func() int {
		f, ok := stream.(interface{ Fd() uintptr })
		if !ok {
			return fallbackWidth
		}

		width, _, err := term.GetSize(f.Fd())
		if err != nil || width <= 0 {
			return fallbackWidth
		}

		return width
	}
}

// count is a number with its noun, which agrees with it: "4 groups", "1 group".
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}

	return fmt.Sprintf("%d %s", n, plural)
}

var _ Reporter = (*Live)(nil)
