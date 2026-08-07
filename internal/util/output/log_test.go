package output_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/util/output"
)

// SetupLogger installs a process-wide default logger, so every test here has to
// put it back. Restoring in TestMain as well keeps a failure inside one test
// from leaking a chatty logger into the rest of the package.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func withRestoredLogger(t *testing.T) {
	t.Helper()

	previous := slog.Default()

	t.Cleanup(func() { slog.SetDefault(previous) })
}

// The rule the whole package is built around: a normal run says nothing on the
// diagnostic channel. Anything a user should see went through a Writer.
func TestSetupLogger_SilentWithoutDebug(t *testing.T) {
	withRestoredLogger(t)

	var stderr bytes.Buffer

	output.SetupLogger(&stderr, output.FormatPretty, false)

	slog.Debug("resolving groups")
	slog.Info("read 12 repositories")
	slog.Warn("cache miss")
	slog.Error("write failed")

	if stderr.Len() != 0 {
		t.Errorf("logger emitted records without --debug: %q", stderr.String())
	}
}

func TestSetupLogger_DebugEnables(t *testing.T) {
	withRestoredLogger(t)

	var stderr bytes.Buffer

	output.SetupLogger(&stderr, output.FormatPretty, true)

	slog.Debug("resolving groups", "count", 3)

	got := stderr.String()
	if !strings.Contains(got, "resolving groups") {
		t.Errorf("--debug produced no record: %q", got)
	}

	if !strings.Contains(got, "count=3") {
		t.Errorf("text handler dropped the attribute: %q", got)
	}
}

// Cobra parses persistent flags after the command tree is built, so the level
// has to stay adjustable once the logger is installed.
func TestSetupLogger_LevelVarFlipsLater(t *testing.T) {
	withRestoredLogger(t)

	var stderr bytes.Buffer

	level := output.SetupLogger(&stderr, output.FormatPretty, false)

	slog.Debug("before")

	if stderr.Len() != 0 {
		t.Fatalf("logger was not silent before the flip: %q", stderr.String())
	}

	level.Set(slog.LevelDebug)
	slog.Debug("after")

	if !strings.Contains(stderr.String(), "after") {
		t.Errorf("level.Set(LevelDebug) did not enable the logger: %q", stderr.String())
	}
}

// --debug --output=json has to leave stderr parseable too, not text lines
// interleaved with a JSON stdout stream.
func TestSetupLogger_JSONFormat(t *testing.T) {
	withRestoredLogger(t)

	var stderr bytes.Buffer

	output.SetupLogger(&stderr, output.FormatJSON, true)

	slog.Debug("resolving groups", "count", 3)

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &record); err != nil {
		t.Fatalf("json format did not produce a JSON record: %v (%q)", err, stderr.String())
	}

	if record["msg"] != "resolving groups" {
		t.Errorf("msg = %v, want %q", record["msg"], "resolving groups")
	}
}

// The diagnostic channel never touches stdout: `labelsync groups --output=json |
// jq` must not have debug lines spliced into the object stream.
func TestSetupLogger_NeverWritesToStdout(t *testing.T) {
	withRestoredLogger(t)

	var stdout, stderr bytes.Buffer

	output.SetupLogger(&stderr, output.FormatPretty, true)
	output.NewPrettyWriter(&stdout, &stderr, goldenPlain).Info("resolving groups")

	before := stdout.Len()

	slog.Debug("this is a diagnostic")

	if stdout.Len() != before {
		t.Errorf("a slog record reached stdout: %q", stdout.String())
	}
}

func TestLevelSilent_IsAboveEveryLevel(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if output.LevelSilent <= level {
			t.Errorf("LevelSilent (%v) does not suppress %v", slog.Level(output.LevelSilent), level)
		}
	}
}
