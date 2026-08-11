package ratelimit

// countdown.go is what a wait looks like from the outside.
//
// Waiting is expected and fine. Waiting *silently* is not — a CLI that goes
// quiet for four minutes reads as one that has hung — and neither is waiting
// while spraying carriage returns into a log file, which is how a CI job's
// output becomes unreadable. So there are three renderings, chosen by where the
// countdown is drawing and what --output asked for:
//
//	stderr is a TTY + --output=pretty   one line, rewritten in place with \r
//	stderr is not a TTY                 a log line at a fixed interval
//	--output=json                       a structured event at the same interval
//
// All three draw on **stderr**. The countdown is progress, not the product: it
// must not land in the file when a user redirects stdout, and it has to stay
// visible on the terminal while they do. That is also why [output.IsTTY] is
// asked about stderr specifically — the stream being drawn to — rather than
// about stdout, which is somewhere else entirely by the time it matters.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/specsnl/labelsync/internal/util/output"
)

// Kind names which limit is being waited out. The strings are a wire contract —
// they are the "kind" field of the JSON event — so they may be added to and
// never renamed.
type Kind string

const (
	// KindPrimary is the hourly, header-shaped budget: 5,000 requests, and a 403
	// once it is gone.
	KindPrimary Kind = "primary"

	// KindSecondary is the undocumented content-creation limit of roughly 80
	// writes a minute. It is this tool's most likely wait.
	KindSecondary Kind = "secondary"

	// KindBudget is the proactive pause: the primary budget has dropped to its
	// last few requests and the run stops before spending them rather than racing
	// into a 403.
	KindBudget Kind = "budget"
)

// Label is the kind in prose, for the line a human reads.
func (k Kind) Label() string {
	switch k {
	case KindPrimary:
		return "Primary rate limit"
	case KindSecondary:
		return "Secondary rate limit"
	case KindBudget:
		return "Rate-limit budget nearly spent"
	default:
		return "Rate limit"
	}
}

// hourglass opens every rendering. It is not a control character and is
// therefore safe in a log file, which is the only property that matters here.
const hourglass = "⏳"

// tickInterval is how often each rendering redraws.
//
// A terminal gets a second, because a countdown that does not count is just a
// message. A log file gets thirty, because the point there is "still alive, here
// is how long is left" and one line every second for fifteen minutes is nine
// hundred lines nobody will read.
const (
	ttyTick = time.Second
	logTick = 30 * time.Second
)

var styleCountdown = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(11)) // bright yellow

// Wait is one wait, as the reporter is told about it.
type Wait struct {
	// Kind is which limit is being waited out.
	Kind Kind

	// Total is how long the wait is altogether.
	Total time.Duration

	// ResumeAt is when it ends, on the limiter's clock.
	ResumeAt time.Time

	// Writes is how many writes the run still has to make, and WritesKnown says
	// whether anything ever told the limiter. A dry run has not, and a countdown
	// that invented a zero would report a finished job.
	Writes      int
	WritesKnown bool
}

// Reporter draws a wait while the limiter sits it out.
//
// The limiter owns the sleeping, and calls [Reporter.Tick] every
// [Reporter.Interval] with what is left. That split is what keeps the whole
// thing testable: nothing here reads a clock, so a fake one drives the animation
// as fast as the test can run.
type Reporter interface {
	// Interval is how often Tick wants to be called.
	Interval() time.Duration

	// Start opens the wait. It is called once, before the first Tick.
	Start(w Wait)

	// Tick reports that left remains. It is called at least once, immediately,
	// because a wait shorter than one interval still has to say it is happening.
	Tick(w Wait, left time.Duration)

	// Done closes the wait, whether it finished or was cut short by a cancelled
	// context. A rendering that draws control characters has to undraw them here.
	Done(w Wait)
}

// NewReporter picks the rendering from what --output asked for and what stderr
// turns out to be.
//
// stderr is the stream being drawn to and the stream asked about, and it is
// passed raw as well as through the writer: the in-place rendering needs to
// write a carriage return with no newline after it, which is a thing no
// [output.Writer] method can do and should not learn to.
func NewReporter(w output.Writer, stderr io.Writer, format output.Format) Reporter {
	if format == output.FormatPretty && output.IsTTY(stderr) {
		return NewLiveReporter(stderr)
	}

	return NewLoggedReporter(w)
}

// NewLiveReporter is the in-place rendering, over a stream it assumes is a
// terminal. [NewReporter] is what a command calls; this is exported so a test
// can drive the rendering the constructor would have chosen without needing a
// terminal to be handed one.
func NewLiveReporter(stderr io.Writer) *LiveReporter { return &LiveReporter{out: stderr} }

// NewLoggedReporter is the periodic rendering, over a writer that decides
// whether it comes out as a log line or a structured event.
func NewLoggedReporter(w output.Writer) *LoggedReporter { return &LoggedReporter{out: w} }

// LiveReporter rewrites one line in place. It is the only rendering that emits
// a control character, and [NewReporter] reaches it only when stderr is a
// terminal.
type LiveReporter struct {
	out io.Writer

	// width is the longest line drawn so far, so the next one can be padded to
	// cover it. Without this, a countdown shortening from "10:00" to "9:59"
	// leaves the stale last character on screen.
	width int
}

// Interval implements [Reporter].
func (c *LiveReporter) Interval() time.Duration { return ttyTick }

// Start implements [Reporter]. Nothing is drawn: the first Tick follows
// immediately and would overwrite it.
func (c *LiveReporter) Start(Wait) {}

// Tick implements [Reporter], rewriting the line from column zero.
func (c *LiveReporter) Tick(w Wait, left time.Duration) {
	line := styleCountdown.Render(sentence(w, left))

	if pad := c.width - lipgloss.Width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}

	c.width = max(c.width, lipgloss.Width(line))

	fmt.Fprint(c.out, "\r"+line)
}

// Done implements [Reporter], clearing the line it drew.
//
// Clearing rather than leaving it: the wait is over, so a line saying how long
// is left of it is stale the moment it stops being redrawn, and the run's real
// output is about to continue on the same row.
func (c *LiveReporter) Done(Wait) {
	fmt.Fprint(c.out, "\r"+strings.Repeat(" ", c.width)+"\r")

	c.width = 0
}

// LoggedReporter reports at a fixed interval through the writer, which renders
// it as a warning line for a human and as a structured event under
// --output=json.
//
// One implementation covers two of the three renderings, because that is what
// [output.Writer] is for: the difference between a log line and a JSON event is
// the writer's business, not the countdown's. **No control characters**, in
// either — a `\r` in a CI log is unreadable, and a `\r` in a JSON stream is
// worse than unreadable.
type LoggedReporter struct {
	out output.Writer
}

// Interval implements [Reporter].
func (c *LoggedReporter) Interval() time.Duration { return logTick }

// Start implements [Reporter].
func (c *LoggedReporter) Start(Wait) {}

// Tick implements [Reporter].
func (c *LoggedReporter) Tick(w Wait, left time.Duration) {
	c.out.WriteEvent(event(w, left), "%s", sentence(w, left))
}

// Done implements [Reporter]. Nothing is emitted: the last tick already said
// what was left, and a line saying the wait is over adds no information a
// consumer cannot get from the next thing that happens.
func (c *LoggedReporter) Done(Wait) {}

// Event is the JSON object a countdown emits on stderr.
//
// The field names are a wire contract. Seconds is a number rather than "04:32"
// because a consumer that has to parse a duration back out of prose is a
// consumer the fields exist to spare.
type Event struct {
	Level           string `json:"level"` // always "warn"
	Event           string `json:"event"` // always EventName
	Kind            Kind   `json:"kind"`
	Seconds         int    `json:"seconds"`
	ResumeAt        string `json:"resume_at"` // RFC 3339, UTC
	WritesRemaining *int   `json:"writes_remaining,omitempty"`
}

// EventName is the "event" discriminator of a countdown object, so a consumer
// can `jq 'select(.event == "rate_limit_wait")'` the waits out of the stderr
// stream. Added to, never renamed.
const EventName = "rate_limit_wait"

// event builds the structured form of a wait.
func event(w Wait, left time.Duration) Event {
	e := Event{
		Level:    "warn",
		Event:    EventName,
		Kind:     w.Kind,
		Seconds:  int(left.Round(time.Second).Seconds()),
		ResumeAt: w.ResumeAt.UTC().Format(time.RFC3339),
	}

	if w.WritesKnown {
		e.WritesRemaining = &w.Writes
	}

	return e
}

// sentence is the human form, and the same words in all three renderings:
//
//	⏳ Secondary rate limit — resuming in 04:32 · 143 writes remaining
func sentence(w Wait, left time.Duration) string {
	line := fmt.Sprintf("%s %s — resuming in %s", hourglass, w.Kind.Label(), clock(left))

	if w.WritesKnown {
		line += fmt.Sprintf(" · %d write%s remaining", w.Writes, plural(w.Writes))
	}

	return line
}

// clock renders a duration as mm:ss, or hh:mm:ss once there is an hour of it.
//
// Not Duration.String(): "4m32s" is fine in a log and wrong in a countdown,
// where the eye wants a fixed number of digits in a fixed place rather than a
// field that changes width as it shrinks.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}

	total := int(d.Round(time.Second).Seconds())
	hours, minutes, seconds := total/3600, total/60%60, total%60

	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}

	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

// plural is the "s" on a count.
func plural(n int) string {
	if n == 1 {
		return ""
	}

	return "s"
}
