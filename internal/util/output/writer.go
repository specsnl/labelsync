// Package output is the single channel for user-facing output.
//
// Everything a user is meant to read goes through a [Writer] — never
// fmt.Println. Two implementations back the --output flag: [PrettyWriter]
// renders lipgloss-styled text for a human, [JSONWriter] renders NDJSON for a
// machine. Both are constructed with explicit streams so a test can capture
// them.
//
// # slog is not this
//
// log/slog is a debug-only diagnostic channel on stderr, silent on a normal run
// and never used for reporting. See [SetupLogger]. The boundary is settled here
// so that no later package has to think about it: if a user is supposed to see
// it, it is a Writer call; if it only helps someone debugging, it is slog.
//
// # Writes are best-effort
//
// Writer has no error channel, and reporting a failed write would itself need a
// working stream. Output calls therefore discard their error, which is why
// .golangci.yml excludes fmt.Fprintln and lipgloss.Fprintln from errcheck.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// Format names an --output value. The strings are the flag values themselves.
type Format string

const (
	// FormatPretty is lipgloss-styled text for a human reader. The default.
	FormatPretty Format = "pretty"

	// FormatJSON is newline-delimited JSON: one object per line, so a consumer
	// can parse the stream as it arrives rather than waiting for the run to end.
	FormatJSON Format = "json"
)

var (
	styleInfo  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(12)) // bright blue
	styleWarn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(11)) // bright yellow
	styleError = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(9))  // bright red
)

// Writer is the interface every user-facing message passes through.
//
// [Writer.WriteTable] is the only method on stdout, and that asymmetry is the point:
// stdout is the product, and everything else — progress, warnings, failures —
// is the story of making it. It is what keeps `labelsync groups --output=json |
// jq` working when a repository turns out to be inaccessible halfway through,
// and it means a JSON run puts nothing on stdout that a consumer cannot type.
type Writer interface {
	// Info reports normal progress on stderr. Progress is narration, not the
	// result the command exists to produce: it must not land in the file when a
	// user redirects stdout. If a command needs a result line that is not a
	// table, that is a new method, not this one.
	Info(format string, args ...any)

	// Warn reports a recoverable problem on stderr — a skipped repository, say.
	Warn(format string, args ...any)

	// Error reports a failure on stderr.
	Error(format string, args ...any)

	// WriteErr renders err as an error-level message on stderr. JSON output
	// carries an "error_kind" field when err wraps a known labelsync sentinel.
	WriteErr(err error)

	// WriteTable renders a prepared table. Pretty output aligns the cells into a
	// bordered table; JSON output marshals one source record per line.
	//
	// Call [Table] rather than this: the generic constructor is what keeps the
	// cells aligned with the headers and pairs each rendered row with the record
	// it came from.
	WriteTable(t TableData)
}

// Both implementations satisfy Writer.
var (
	_ Writer = (*PrettyWriter)(nil)
	_ Writer = (*JSONWriter)(nil)
)

// PrettyWriter writes lipgloss-styled output for a human reader.
//
// Colour support is detected once, at construction, from the streams and the
// environment — see [NewPrettyWriter]. A run piped to a file or a CI log gets
// the same text with the escape sequences stripped, not a wall of \x1b[.
type PrettyWriter struct {
	stdout io.Writer
	stderr io.Writer
}

// NewPrettyWriter creates a PrettyWriter over the given streams.
//
// Each stream is wrapped in a colorprofile.Writer, which downsamples or strips
// colour based on what that stream can actually render: truecolor, 256-colour,
// 16-colour, or none at all when it is not a terminal. NO_COLOR, CLICOLOR, and
// CLICOLOR_FORCE are honoured.
//
// Pass nil for environ to use the process environment. Tests pass an explicit
// slice — []string{} for a plain, golden-comparable rendering — so the output
// does not depend on whoever is running them.
func NewPrettyWriter(stdout, stderr io.Writer, environ []string) *PrettyWriter {
	return &PrettyWriter{
		stdout: colorprofile.NewWriter(stdout, environ),
		stderr: colorprofile.NewWriter(stderr, environ),
	}
}

// NewDefaultPrettyWriter creates a PrettyWriter over os.Stdout and os.Stderr,
// detecting colour support from the process environment.
func NewDefaultPrettyWriter() *PrettyWriter {
	return NewPrettyWriter(os.Stdout, os.Stderr, nil)
}

// Info reports normal progress on stderr.
func (w *PrettyWriter) Info(format string, args ...any) {
	fmt.Fprintln(w.stderr, styleInfo.Render("info")+" "+fmt.Sprintf(format, args...))
}

// Warn reports a recoverable problem on stderr.
func (w *PrettyWriter) Warn(format string, args ...any) {
	fmt.Fprintln(w.stderr, styleWarn.Render("warn")+" "+fmt.Sprintf(format, args...))
}

// Error reports a failure on stderr.
func (w *PrettyWriter) Error(format string, args ...any) {
	fmt.Fprintln(w.stderr, styleError.Render("error")+" "+fmt.Sprintf(format, args...))
}

// WriteErr renders err as an error-level message. The sentinel kind is not
// shown: it exists for machines, and the message already reads well.
func (w *PrettyWriter) WriteErr(err error) {
	w.Error("%v", err)
}

// WriteTable renders a bordered, column-aligned table on stdout. The records
// are ignored: a human reads the cells.
func (w *PrettyWriter) WriteTable(t TableData) {
	fmt.Fprintln(w.stdout, RenderTable(t.Headers, t.Cells))
}

// JSONWriter writes NDJSON: exactly one self-contained JSON object per line, so
// a consumer can parse the stream mid-run instead of waiting for a closing
// bracket that a killed process would never write.
//
// Objects carry a "level" field. Tables go to stdout; progress, warnings, and
// errors go to stderr — so every line on stdout is a data record with the same
// keys, and `jq -r .group` never trips over a narration object that has no
// group.
type JSONWriter struct {
	stdout io.Writer
	stderr io.Writer
}

// NewJSONWriter creates a JSONWriter over the given streams. There is no colour
// to detect, so no environment is consulted.
func NewJSONWriter(stdout, stderr io.Writer) *JSONWriter {
	return &JSONWriter{stdout: stdout, stderr: stderr}
}

// NewDefaultJSONWriter creates a JSONWriter over os.Stdout and os.Stderr.
func NewDefaultJSONWriter() *JSONWriter {
	return NewJSONWriter(os.Stdout, os.Stderr)
}

// Info emits an info-level object on stderr.
func (w *JSONWriter) Info(format string, args ...any) {
	writeJSONLine(w.stderr, map[string]string{
		"level":   "info",
		"message": fmt.Sprintf(format, args...),
	})
}

// Warn emits a warn-level object on stderr.
func (w *JSONWriter) Warn(format string, args ...any) {
	writeJSONLine(w.stderr, map[string]string{
		"level":   "warn",
		"message": fmt.Sprintf(format, args...),
	})
}

// Error emits an error-level object on stderr.
func (w *JSONWriter) Error(format string, args ...any) {
	writeJSONLine(w.stderr, map[string]string{
		"level":   "error",
		"message": fmt.Sprintf(format, args...),
	})
}

// WriteErr emits an error-level object on stderr, with an "error_kind" field
// when err wraps a known sentinel. The kind strings are the stable half of the
// contract — the message is free to be reworded, error_kind is not.
func (w *JSONWriter) WriteErr(err error) {
	payload := map[string]string{
		"level":   "error",
		"message": err.Error(),
	}

	if kind := labelsync.KindOf(err); kind != "" {
		payload["error_kind"] = kind
	}

	writeJSONLine(w.stderr, payload)
}

// WriteTable emits one object per source record on stdout — not a single array.
// An array could only be written once the last row was known, which is exactly
// the mid-run parseability NDJSON exists to preserve.
//
// The records are marshalled as they are, so the keys and the types are the
// row struct's own: a count stays a number, and a heading reworded for the
// pretty table cannot disturb a consumer's filter. The headers and cells are
// ignored here — they are the human's rendering of the same rows.
func (w *JSONWriter) WriteTable(t TableData) {
	for _, record := range t.Records {
		writeJSONLine(w.stdout, record)
	}
}

// writeJSONLine writes v as exactly one line of JSON. json.Encoder.Encode
// terminates every value with a newline, which is what makes the stream NDJSON
// by construction rather than by convention.
//
// HTML escaping is off: label names legitimately contain characters like & and
// <, and rendering them as & helps nobody reading a log.
//
// The error is discarded for the reason given in the package doc — Writer has no
// error channel. Encoding cannot fail for the map[string]string payloads used
// here, so a non-nil error can only mean the stream is already gone.
func writeJSONLine(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	_ = enc.Encode(v)
}
