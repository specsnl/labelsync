package cmd_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/cmd"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
	"github.com/specsnl/labelsync/internal/util/output"
)

// run builds the tree the way main does, points both streams at buffers, and
// executes. Every assertion below reads those buffers: if a test cannot see the
// output, the writers were built from os.Stdout/os.Stderr and the wiring is
// wrong, which is exactly what this helper is here to catch.
func run(t *testing.T, extra []*cobra.Command, args ...string) (*cmd.App, string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	app := cmd.NewApp()

	root := cmd.NewRootCmd(app)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	for _, c := range extra {
		root.AddCommand(c)
	}

	err := root.Execute()

	return app, stdout.String(), stderr.String(), err
}

// noopCmd is a leaf that does nothing, so a test can exercise the persistent
// flags without depending on a real command's behaviour.
func noopCmd() *cobra.Command {
	return &cobra.Command{Use: "noop", RunE: func(*cobra.Command, []string) error { return nil }}
}

// failCmd is a leaf that fails with err, for the exit-code and error_kind paths.
func failCmd(err error) *cobra.Command {
	return &cobra.Command{Use: "boom", RunE: func(*cobra.Command, []string) error { return err }}
}

func TestRoot_FlagDefaults(t *testing.T) {
	app, _, _, err := run(t, []*cobra.Command{noopCmd()}, "noop")
	if err != nil {
		t.Fatalf("noop: %v", err)
	}

	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"--config", app.ConfigPath, ""},
		{"--debug", app.Debug, false},
		{"--output", app.Format, output.FormatPretty},
		{"--no-cache", app.NoCache, false},
		{"--concurrency", app.Concurrency, 8},
		{"--write-rate", app.WriteRate, 70},
		{"--max-wait", app.MaxWait, 15 * time.Minute},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestRoot_FlagParsing(t *testing.T) {
	app, _, _, err := run(t, []*cobra.Command{noopCmd()},
		"--config", "/tmp/labels.yml",
		"--debug",
		"--output", "json",
		"--no-cache",
		"--concurrency", "3",
		"--write-rate", "20",
		"--max-wait", "90s",
		"noop",
	)
	if err != nil {
		t.Fatalf("noop: %v", err)
	}

	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"--config", app.ConfigPath, "/tmp/labels.yml"},
		{"--debug", app.Debug, true},
		{"--output", app.Format, output.FormatJSON},
		{"--no-cache", app.NoCache, true},
		{"--concurrency", app.Concurrency, 3},
		{"--write-rate", app.WriteRate, 20},
		{"--max-wait", app.MaxWait, 90 * time.Second},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// --output selects the writer, and the only honest way to assert that is to
// look at what a command actually produced.
func TestRoot_OutputFlagSelectsWriter(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		wantJSON  bool
		wantPlain string
	}{
		{args: []string{"version"}, wantPlain: "labelsync version"},
		{args: []string{"--output", "json", "version"}, wantJSON: true},
		{args: []string{"-o", "json", "version"}, wantJSON: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, stdout, _, err := run(t, nil, tc.args...)
			if err != nil {
				t.Fatalf("version: %v", err)
			}

			var obj map[string]any

			gotJSON := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &obj) == nil
			if gotJSON != tc.wantJSON {
				t.Fatalf("stdout %q: JSON = %v, want %v", stdout, gotJSON, tc.wantJSON)
			}

			if !tc.wantJSON && !strings.Contains(stdout, tc.wantPlain) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, tc.wantPlain)
			}
		})
	}
}

func TestRoot_RejectsBadFlagValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown output format", []string{"--output", "yaml", "noop"}, `invalid --output "yaml"`},
		{"zero concurrency", []string{"--concurrency", "0", "noop"}, "invalid --concurrency 0"},
		{"negative write rate", []string{"--write-rate", "-1", "noop"}, "invalid --write-rate -1"},
		{"negative max wait", []string{"--max-wait", "-1s", "noop"}, "invalid --max-wait -1s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := run(t, []*cobra.Command{noopCmd()}, tc.args...)
			if err == nil {
				t.Fatalf("want an error, got none")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}

			if code := exit.Of(err); code != exit.Error {
				t.Errorf("exit code = %s, want %s", code, exit.Error)
			}
		})
	}
}

// A rejected --output still has a writer to report itself through, or the
// message would have nowhere to go.
func TestRoot_BadOutputFlagStillHasAWriter(t *testing.T) {
	app, _, _, err := run(t, []*cobra.Command{noopCmd()}, "--output", "yaml", "noop")
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if app.Out == nil {
		t.Fatal("app.Out is nil after a rejected --output")
	}
}

// An error wrapping a sentinel keeps its identity all the way out of the tree,
// which is what puts the stable error_kind on a JSON run's final line.
func TestRoot_ErrorCarriesKindAndCode(t *testing.T) {
	failure := fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible)

	_, _, _, err := run(t, []*cobra.Command{failCmd(failure)}, "--output", "json", "boom")
	if !errors.Is(err, labelsync.ErrRepoInaccessible) {
		t.Fatalf("error lost its sentinel: %v", err)
	}

	if code := exit.Of(err); code != exit.Error {
		t.Errorf("exit code = %s, want %s", code, exit.Error)
	}

	if kind := labelsync.KindOf(err); kind != "repo_inaccessible" {
		t.Errorf("error_kind = %q, want %q", kind, "repo_inaccessible")
	}
}

// An outcome is not a failure: the code travels, the error does not.
func TestRoot_OutcomeCodeTravelsOnASilentCarrier(t *testing.T) {
	_, _, _, err := run(t, []*cobra.Command{failCmd(&exit.Err{Code: exit.Drift})}, "boom")
	if err == nil {
		t.Fatal("want the carrier back, got nil")
	}

	if code := exit.Of(err); code != exit.Drift {
		t.Errorf("exit code = %s, want %s", code, exit.Drift)
	}

	var carrier *exit.Err
	if !errors.As(err, &carrier) || carrier.Err != nil {
		t.Errorf("carrier is not silent: %#v", carrier)
	}
}

// Without SilenceUsage every runtime failure drags the usage block along;
// without SilenceErrors Cobra prints its own copy, which carries no error_kind.
func TestRoot_SilencesUsageAndErrors(t *testing.T) {
	root := cmd.NewRootCmd(cmd.NewApp())
	if !root.SilenceUsage || !root.SilenceErrors {
		t.Fatalf("SilenceUsage = %v, SilenceErrors = %v, want both true", root.SilenceUsage, root.SilenceErrors)
	}

	_, stdout, stderr, err := run(t, []*cobra.Command{failCmd(errors.New("it broke"))}, "boom")
	if err == nil {
		t.Fatal("want an error, got none")
	}

	for name, got := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if strings.Contains(got, "Usage:") {
			t.Errorf("%s carries a usage block after a runtime failure: %q", name, got)
		}

		if strings.Contains(got, "it broke") {
			t.Errorf("%s carries Cobra's own error line; main owns the single print: %q", name, got)
		}
	}
}

// slog is silent without --debug, and lands on the command's stderr with it —
// never on stdout, and never on os.Stderr where a test could not see it.
func TestRoot_DebugFlagGatesTheLogger(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantDebug bool
	}{
		{"silent by default", []string{"noop"}, false},
		{"debug on stderr", []string{"--debug", "noop"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logging := &cobra.Command{Use: "noop", RunE: func(*cobra.Command, []string) error {
				slog.Debug("hello from the command")

				return nil
			}}

			_, stdout, stderr, err := run(t, []*cobra.Command{logging}, tc.args...)
			if err != nil {
				t.Fatalf("noop: %v", err)
			}

			if got := strings.Contains(stderr, "hello from the command"); got != tc.wantDebug {
				t.Errorf("debug record on stderr = %v, want %v (stderr: %q)", got, tc.wantDebug, stderr)
			}

			if strings.Contains(stdout, "hello from the command") {
				t.Errorf("a debug record reached stdout: %q", stdout)
			}
		})
	}
}
