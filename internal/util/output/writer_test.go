package output_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/output"
)

// groupRow is the `labelsync groups` shape: the row type both writers project,
// one into cells and the other into JSON. Repositories is an int on purpose —
// the count a consumer would want to filter on numerically.
type groupRow struct {
	Group        string `json:"group"`
	Repositories int    `json:"repositories"`
	Source       string `json:"source"`
}

var (
	sampleRows = []groupRow{
		{Group: "websites", Repositories: 12, Source: "org: specsnl"},
		{Group: "platform", Repositories: 3, Source: "repos:"},
		{Group: "archive", Repositories: 0, Source: "org: specsnl (excluded)"},
	}

	sampleColumns = []output.Column[groupRow]{
		output.Col("Group", func(g groupRow) string { return g.Group }),
		output.Col("Repositories", func(g groupRow) string { return strconv.Itoa(g.Repositories) }),
		output.Col("Source", func(g groupRow) string { return g.Source }),
	}
)

// sampleTable is what the writers receive, prepared the way a command would
// prepare it.
func sampleTable() output.TableData {
	var captured output.TableData

	output.Table(captureWriter{onTable: func(t output.TableData) { captured = t }}, sampleRows, sampleColumns...)

	return captured
}

// captureWriter records the prepared table and discards everything else, so a
// test can assert on what output.Table built rather than on how it rendered.
type captureWriter struct {
	onTable func(output.TableData)
}

func (captureWriter) Info(string, ...any)             {}
func (captureWriter) WriteDiff(output.DiffData)       {}
func (captureWriter) Warn(string, ...any)             {}
func (captureWriter) Error(string, ...any)            {}
func (captureWriter) WriteErr(error)                  {}
func (captureWriter) WriteEvent(any, string, ...any)  {}
func (captureWriter) WriteResult(any, string, ...any) {}

func (w captureWriter) WriteTable(t output.TableData) { w.onTable(t) }

// sampleEvent is a structured stderr diagnostic, shaped the way the rate-limit
// countdown shapes one: fields a consumer can compare, and its own level.
type sampleEvent struct {
	Level   string `json:"level"`
	Event   string `json:"event"`
	Seconds int    `json:"seconds"`
}

// writeSampleRun exercises every Writer method once, in the order a real run
// would: progress, a table, a recoverable skip, a structured diagnostic, then a
// failure.
func writeSampleRun(w output.Writer) {
	w.Info("resolving %d groups", 3)
	output.Table(w, sampleRows, sampleColumns...)
	w.Warn("skipping %s: archived", "specsnl/old-thing")
	w.WriteEvent(
		sampleEvent{Level: "warn", Event: "rate_limit_wait", Seconds: 272},
		"waiting %s for the rate limit", "04:32",
	)
	w.WriteErr(fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible))
}

func TestPrettyWriter_Golden_NoColor(t *testing.T) {
	var stdout, stderr bytes.Buffer

	writeSampleRun(output.NewPrettyWriter(&stdout, &stderr, goldenPlain))

	assertGolden(t, "pretty_stdout_nocolor", stdout.String())
	assertGolden(t, "pretty_stderr_nocolor", stderr.String())
}

// The colour golden is the other half of the same claim: the styling is really
// there, and the plain golden above is degradation rather than an absence of
// styles.
func TestPrettyWriter_Golden_Color(t *testing.T) {
	var stdout, stderr bytes.Buffer

	writeSampleRun(output.NewPrettyWriter(&stdout, &stderr, goldenColor))

	assertGolden(t, "pretty_stdout_color", stdout.String())
	assertGolden(t, "pretty_stderr_color", stderr.String())
}

// A buffer is not a terminal, so nothing styled may survive to it.
func TestPrettyWriter_StripsEscapesOffTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer

	writeSampleRun(output.NewPrettyWriter(&stdout, &stderr, goldenPlain))

	for name, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if strings.Contains(got, "\x1b[") {
			t.Errorf("%s contains an ANSI escape sequence off a terminal: %q", name, got)
		}
	}
}

// WriteTable, WriteResult, and WriteDiff are the only methods on stdout.
// Everything else is narration, and narration in a redirected file is the defect
// this split exists to prevent.
func TestWriters_OnlyTableReachesStdout(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(stdout, stderr *bytes.Buffer) output.Writer
	}{
		{"pretty", func(stdout, stderr *bytes.Buffer) output.Writer {
			return output.NewPrettyWriter(stdout, stderr, goldenPlain)
		}},
		{"json", func(stdout, stderr *bytes.Buffer) output.Writer {
			return output.NewJSONWriter(stdout, stderr)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			w := tc.make(&stdout, &stderr)
			w.Info("resolving %d groups", 3)
			w.Warn("careful")
			w.Error("broken")
			w.WriteErr(errors.New("fatal"))

			if stdout.Len() != 0 {
				t.Errorf("non-table output reached stdout: %q", stdout.String())
			}

			for _, want := range []string{"resolving 3 groups", "careful", "broken", "fatal"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr missing %q, got: %q", want, stderr.String())
				}
			}
		})
	}
}

// WriteResult is the product channel for a command whose whole answer is one
// value. The two audiences get different projections of it: the human gets the
// sentence, the machine gets the record — and neither leaks into the other.
func TestWriters_WriteResult(t *testing.T) {
	type versionRow struct {
		Version string `json:"version"`
	}

	record := versionRow{Version: "1.2.3"}

	t.Run("pretty renders the text", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		output.NewPrettyWriter(&stdout, &stderr, goldenPlain).
			WriteResult(record, "labelsync version %s", record.Version)

		if got, want := stdout.String(), "labelsync version 1.2.3\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}

		if stderr.Len() != 0 {
			t.Errorf("a result reached stderr: %q", stderr.String())
		}
	})

	t.Run("json marshals the record and drops the prose", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		output.NewJSONWriter(&stdout, &stderr).
			WriteResult(record, "labelsync version %s", record.Version)

		if got, want := stdout.String(), "{\"version\":\"1.2.3\"}\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}

		if strings.Contains(stdout.String(), "labelsync version") {
			t.Errorf("the human phrasing was spliced into the data stream: %q", stdout.String())
		}
	})
}

// WriteDiff is the third product-level method, and it splits the two audiences
// the same way WriteResult does: the human gets the assembled text, the machine
// gets the records, and neither leaks into the other.
func TestWriters_WriteDiff(t *testing.T) {
	type actionRecord struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}

	data := output.DiffData{
		Text: "specsnl/labelsync\n  + create  type: bug",
		Records: []any{
			actionRecord{Kind: "create", Name: "type: bug"},
			actionRecord{Kind: "summary", Name: ""},
		},
	}

	t.Run("pretty writes the text", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		output.NewPrettyWriter(&stdout, &stderr, goldenPlain).WriteDiff(data)

		if got, want := stdout.String(), data.Text+"\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}

		if stderr.Len() != 0 {
			t.Errorf("a diff reached stderr: %q", stderr.String())
		}
	})

	t.Run("json emits one object per record and drops the text", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		output.NewJSONWriter(&stdout, &stderr).WriteDiff(data)

		lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		if len(lines) != len(data.Records) {
			t.Fatalf("got %d lines, want %d: %q", len(lines), len(data.Records), stdout.String())
		}

		for i, line := range lines {
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Errorf("line %d is not a JSON object: %v (%q)", i+1, err, line)
			}
		}

		if strings.Contains(stdout.String(), "specsnl/labelsync\n  +") {
			t.Errorf("the human rendering was spliced into the data stream: %q", stdout.String())
		}
	})
}

// The reason Info is on stderr rather than a style preference: every line on
// stdout has to be a data record with the same keys, or a consumer's filter
// silently yields null for the narration.
func TestJSONWriter_StdoutIsUniformlyTyped(t *testing.T) {
	var stdout, stderr bytes.Buffer

	writeSampleRun(output.NewJSONWriter(&stdout, &stderr))

	for i, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("stdout line %d is not a JSON object: %v", i+1, err)
		}

		if _, ok := obj["group"]; !ok {
			t.Errorf("stdout line %d is not a data record: %q", i+1, line)
		}

		if _, ok := obj["level"]; ok {
			t.Errorf("stdout line %d carries a level, so it is narration: %q", i+1, line)
		}
	}
}

// TestWriteEvent_IsStructuredOnStderr covers the method the rate-limit countdown
// exists for: the same call has to reach a human as a sentence and a machine as
// fields, and neither may touch stdout — a countdown spliced into the NDJSON
// product is a line `jq` cannot type.
func TestWriteEvent_IsStructuredOnStderr(t *testing.T) {
	event := sampleEvent{Level: "warn", Event: "rate_limit_wait", Seconds: 272}

	var prettyOut, prettyErr, jsonOut, jsonErr bytes.Buffer

	output.NewPrettyWriter(&prettyOut, &prettyErr, goldenPlain).
		WriteEvent(event, "waiting %s", "04:32")
	output.NewJSONWriter(&jsonOut, &jsonErr).
		WriteEvent(event, "waiting %s", "04:32")

	for name, stdout := range map[string]string{"pretty": prettyOut.String(), "json": jsonOut.String()} {
		if stdout != "" {
			t.Errorf("the %s writer put %q on stdout, want nothing: an event is narration", name, stdout)
		}
	}

	if want := "waiting 04:32"; !strings.Contains(prettyErr.String(), want) {
		t.Errorf("pretty stderr = %q, want it to contain %q", prettyErr.String(), want)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonErr.String())), &got); err != nil {
		t.Fatalf("json stderr %q is not one object: %v", jsonErr.String(), err)
	}

	// The fields, not the sentence: a consumer comparing 272 against a threshold
	// is the whole reason this is not Warn.
	for field, want := range map[string]any{"level": "warn", "event": "rate_limit_wait", "seconds": 272.0} {
		if got[field] != want {
			t.Errorf("stderr[%q] = %v, want %v", field, got[field], want)
		}
	}

	if _, spliced := got["message"]; spliced {
		t.Errorf("the JSON event carries the human phrasing too: %v", got)
	}
}

func TestJSONWriter_Golden(t *testing.T) {
	var stdout, stderr bytes.Buffer

	writeSampleRun(output.NewJSONWriter(&stdout, &stderr))

	assertGolden(t, "json_stdout", stdout.String())
	assertGolden(t, "json_stderr", stderr.String())
}

// The NDJSON contract: every line is a complete object on its own, so a consumer
// can parse the stream as it arrives and a run killed halfway still leaves valid
// lines behind. A single JSON array would satisfy neither.
func TestJSONWriter_OneObjectPerLine(t *testing.T) {
	var stdout, stderr bytes.Buffer

	writeSampleRun(output.NewJSONWriter(&stdout, &stderr))

	for stream, out := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if !strings.HasSuffix(out, "\n") {
			t.Errorf("%s does not end with a newline: %q", stream, out)
		}

		for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Errorf("%s line %d is not a JSON object: %v (%q)", stream, i+1, err, line)
			}
		}
	}
}

// A table is one object per row — three lines for the three sample rows — and
// never a single array.
func TestJSONWriter_TableEmitsOneObjectPerRow(t *testing.T) {
	var stdout bytes.Buffer

	output.NewJSONWriter(&stdout, &bytes.Buffer{}).WriteTable(sampleTable())

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != len(sampleRows) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(sampleRows), stdout.String())
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first row is not a JSON object: %v", err)
	}

	// The keys are the row struct's own json tags, not a normalisation of the
	// column headings, so rewording a heading cannot break a consumer's filter.
	want := map[string]any{"group": "websites", "repositories": float64(12), "source": "org: specsnl"}
	for key, value := range want {
		if first[key] != value {
			t.Errorf("row[%q] = %#v, want %#v (got %v)", key, first[key], value, first)
		}
	}
}

// The reason the JSON side marshals records rather than cells: a count stays a
// number, so `jq 'select(.repositories > 5)'` works. Rendering it through the
// table would have made it the string "12".
func TestJSONWriter_TableKeepsValueTypes(t *testing.T) {
	var stdout bytes.Buffer

	output.NewJSONWriter(&stdout, &bytes.Buffer{}).WriteTable(sampleTable())

	first := strings.SplitN(stdout.String(), "\n", 2)[0]
	if !strings.Contains(first, `"repositories":12`) {
		t.Errorf("count was not emitted as a number: %q", first)
	}
}

// A row can no longer disagree with its headers: the cells are built from the
// columns, for every row, by the same loop. This is the defect the [][]string
// signature could not rule out.
func TestTable_CellsAlignWithHeaders(t *testing.T) {
	data := sampleTable()

	if len(data.Headers) != len(sampleColumns) {
		t.Fatalf("got %d headers, want %d", len(data.Headers), len(sampleColumns))
	}

	if len(data.Records) != len(data.Cells) {
		t.Errorf("got %d records for %d rendered rows", len(data.Records), len(data.Cells))
	}

	for i, cells := range data.Cells {
		if len(cells) != len(data.Headers) {
			t.Errorf("row %d has %d cells for %d headers: %q", i, len(cells), len(data.Headers), cells)
		}
	}
}

// A column need not correspond to a field: the cell function is free to compute,
// reformat, or ignore the row. That is what lets the human rendering differ from
// the record without the record giving up its types.
func TestTable_ComputedColumn(t *testing.T) {
	var captured output.TableData

	output.Table(
		captureWriter{onTable: func(t output.TableData) { captured = t }},
		sampleRows,
		output.Col("Group", func(g groupRow) string { return g.Group }),
		output.Col("Empty", func(g groupRow) string {
			if g.Repositories == 0 {
				return "yes"
			}

			return ""
		}),
	)

	if got := captured.Cells[2][1]; got != "yes" {
		t.Errorf("computed cell = %q, want %q", got, "yes")
	}

	// The record is untouched by the rendering: it has no such field.
	if got, ok := captured.Records[2].(groupRow); !ok || got.Repositories != 0 {
		t.Errorf("record was not passed through intact: %#v", captured.Records[2])
	}
}

func TestJSONWriter_WriteErr_KnownSentinel(t *testing.T) {
	var stderr bytes.Buffer

	err := fmt.Errorf("reading labels: %w", fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible))
	output.NewJSONWriter(&bytes.Buffer{}, &stderr).WriteErr(err)

	var obj map[string]string
	if uerr := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &obj); uerr != nil {
		t.Fatalf("not a JSON object: %v", uerr)
	}

	if obj["error_kind"] != "repo_inaccessible" {
		t.Errorf("error_kind = %q, want %q", obj["error_kind"], "repo_inaccessible")
	}

	if obj["level"] != "error" {
		t.Errorf("level = %q, want %q", obj["level"], "error")
	}

	if obj["message"] != err.Error() {
		t.Errorf("message = %q, want %q", obj["message"], err.Error())
	}
}

// An error wrapping no sentinel gets no error_kind at all, rather than an empty
// one — an empty string would look like a kind that failed to resolve.
func TestJSONWriter_WriteErr_UnknownError(t *testing.T) {
	var stderr bytes.Buffer

	output.NewJSONWriter(&bytes.Buffer{}, &stderr).WriteErr(errors.New("something unexpected"))

	if strings.Contains(stderr.String(), "error_kind") {
		t.Errorf("unknown error carried an error_kind: %q", stderr.String())
	}
}

// Label names and descriptions legitimately contain & and <; escaping them to
// & makes a log harder to read and a `jq` result harder to eyeball.
func TestJSONWriter_DoesNotEscapeHTML(t *testing.T) {
	var stderr bytes.Buffer

	output.NewJSONWriter(&bytes.Buffer{}, &stderr).Info("status: %s", "blocked & <waiting>")

	if !strings.Contains(stderr.String(), "blocked & <waiting>") {
		t.Errorf("HTML-escaped output: %q", stderr.String())
	}
}

func TestPrettyWriter_WriteErr_OmitsKind(t *testing.T) {
	var stderr bytes.Buffer

	output.NewPrettyWriter(&bytes.Buffer{}, &stderr, goldenPlain).
		WriteErr(fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible))

	got := stderr.String()
	if !strings.Contains(got, labelsync.ErrRepoInaccessible.Error()) {
		t.Errorf("stderr missing the error message: %q", got)
	}

	if strings.Contains(got, "repo_inaccessible") {
		t.Errorf("pretty output leaked the machine kind string: %q", got)
	}
}

// fdReader is stdin's shape: readable, carrying a descriptor, and with no Write
// method at all. It would not compile against an io.Writer parameter, which is
// the whole reason IsTTY takes any.
type fdReader struct{ f *os.File }

func (r fdReader) Read(p []byte) (int, error) { return r.f.Read(p) }
func (r fdReader) Fd() uintptr                { return r.f.Fd() }

func TestIsTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}

	t.Cleanup(func() { _ = f.Close() })

	for _, tc := range []struct {
		name   string
		stream any
	}{
		{"a buffer has no descriptor", &bytes.Buffer{}},
		{"a regular file is not a terminal", f},
		{"a read-only stream is answerable", fdReader{f: f}},
		{"a plain string is not a stream at all", "stdin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if output.IsTTY(tc.stream) {
				t.Errorf("IsTTY(%T) = true, want false", tc.stream)
			}
		})
	}
}
