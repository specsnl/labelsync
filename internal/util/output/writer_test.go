package output_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/output"
)

// sampleHeaders and sampleRows are the `labelsync groups` shape: the table
// rendering both writers have to agree on the content of and disagree on the
// form of.
var (
	sampleHeaders = []string{"Group", "Repositories", "Source"}
	sampleRows    = [][]string{
		{"websites", "12", "org: specsnl"},
		{"platform", "3", "repos:"},
		{"archive", "0", "org: specsnl (excluded)"},
	}
)

// writeSampleRun exercises every Writer method once, in the order a real run
// would: progress, a table, a recoverable skip, then a failure.
func writeSampleRun(w output.Writer) {
	w.Info("resolving %d groups", 3)
	w.Table(sampleHeaders, sampleRows)
	w.Warn("skipping %s: archived", "specsnl/old-thing")
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

// Table is the only method on stdout. Everything else is narration, and
// narration in a redirected file is the defect this split exists to prevent.
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

	output.NewJSONWriter(&stdout, &bytes.Buffer{}).Table(sampleHeaders, sampleRows)

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != len(sampleRows) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(sampleRows), stdout.String())
	}

	var first map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first row is not a JSON object: %v", err)
	}

	// Headers are normalised to stable keys, so rewording a heading does not
	// break a consumer's filter.
	want := map[string]string{"group": "websites", "repositories": "12", "source": "org: specsnl"}
	for key, value := range want {
		if first[key] != value {
			t.Errorf("row[%q] = %q, want %q (got %v)", key, first[key], value, first)
		}
	}
}

// A row shorter than the header set simply omits those keys rather than padding
// them with empty strings, which would be indistinguishable from a real "".
func TestJSONWriter_TableShortRow(t *testing.T) {
	var stdout bytes.Buffer

	output.NewJSONWriter(&stdout, &bytes.Buffer{}).
		Table([]string{"Group", "Repositories"}, [][]string{{"websites"}})

	var obj map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &obj); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}

	if _, ok := obj["repositories"]; ok {
		t.Errorf("missing cell produced a key anyway: %v", obj)
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
