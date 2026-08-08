package cmd_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/cmd"
)

// The version is the product of the command, so it belongs on stdout: a user
// running `labelsync version --dont-prettify > v.txt` must find it in the file.
func TestVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"prettified by default", []string{"version"}, "labelsync version " + cmd.Version + "\n"},
		{"bare with --dont-prettify", []string{"version", "--dont-prettify"}, cmd.Version + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stdout, stderr, err := run(t, nil, tc.args...)
			if err != nil {
				t.Fatalf("version: %v", err)
			}

			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}

			if stderr != "" {
				t.Errorf("version narrated on stderr: %q", stderr)
			}
		})
	}
}

// Under --output=json the record is the answer, and --dont-prettify has nothing
// left to strip: JSON has no prose in it to begin with.
func TestVersion_JSON(t *testing.T) {
	for _, args := range [][]string{
		{"--output", "json", "version"},
		{"--output", "json", "version", "--dont-prettify"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stdout, _, err := run(t, nil, args...)
			if err != nil {
				t.Fatalf("version: %v", err)
			}

			var obj map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &obj); err != nil {
				t.Fatalf("stdout is not one JSON object: %q (%v)", stdout, err)
			}

			if got := obj["version"]; got != cmd.Version {
				t.Errorf("version = %v, want %q", got, cmd.Version)
			}

			if len(obj) != 1 {
				t.Errorf("the record carries more than the version: %v", obj)
			}
		})
	}
}

// --version is defined as `version --dont-prettify`, so the test is that the two
// produce the same bytes rather than that each produces something plausible.
func TestVersion_RootFlagMatchesDontPrettify(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"pretty", nil},
		{"json", []string{"--output", "json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, viaFlag, flagStderr, err := run(t, nil, append(tc.flags, "--version")...)
			if err != nil {
				t.Fatalf("--version: %v", err)
			}

			_, viaCmd, _, err := run(t, nil, append(tc.flags, "version", "--dont-prettify")...)
			if err != nil {
				t.Fatalf("version --dont-prettify: %v", err)
			}

			if viaFlag != viaCmd {
				t.Errorf("--version = %q, version --dont-prettify = %q; they must agree", viaFlag, viaCmd)
			}

			if flagStderr != "" {
				t.Errorf("--version narrated on stderr: %q", flagStderr)
			}
		})
	}
}

// The flag has to be handled after the writers are built, or --output would not
// have been read yet and JSON would get a bare line in its object stream. This
// is what Cobra's built-in version flag does, and why it is not used.
func TestVersion_RootFlagHonoursOutputFormat(t *testing.T) {
	_, stdout, _, err := run(t, nil, "--output", "json", "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &obj); err != nil {
		t.Fatalf("--output=json --version is not JSON: %q (%v)", stdout, err)
	}
}

// Without the flag the root still prints its help, the way a bare `labelsync`
// did before the flag gave the root a RunE at all.
func TestRoot_BareInvocationPrintsHelp(t *testing.T) {
	_, stdout, _, err := run(t, nil)
	if err != nil {
		t.Fatalf("bare invocation: %v", err)
	}

	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("stdout = %q, want the help", stdout)
	}
}

// A RunE on the root must not turn a typo into a silent help screen.
func TestRoot_UnknownCommandStillFails(t *testing.T) {
	_, _, _, err := run(t, nil, "bogus")
	if err == nil {
		t.Fatal("want an error for an unknown command, got none")
	}
}

// Nothing injects Version under `go test`, so the fallback is what a build
// without -ldflags ships as.
func TestVersion_DefaultsToDev(t *testing.T) {
	if cmd.Version != "dev" {
		t.Errorf("Version = %q, want %q", cmd.Version, "dev")
	}
}

// The build files name the variable by its full path, in a string the compiler
// never checks. Renaming or moving Version would leave both of them injecting
// into nothing, and every release would silently ship as "dev" — so the paths
// are asserted against the source rather than trusted.
func TestVersion_BuildFilesInjectThisVariable(t *testing.T) {
	const want = "github.com/specsnl/labelsync/internal/cmd.Version"

	for _, file := range []string{"../../.goreleaser.yml", "../../Dockerfile"} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}

		// The Dockerfile builds the path from ${GO_MODULE}, so match the suffix
		// the module variable is prepended to rather than the literal.
		if !strings.Contains(string(content), want) &&
			!strings.Contains(string(content), strings.TrimPrefix(want, "github.com/specsnl/labelsync")) {
			t.Errorf("%s does not inject %s; a release built from it would ship as %q", file, want, "dev")
		}
	}
}

func TestVersion_RejectsArguments(t *testing.T) {
	_, _, _, err := run(t, nil, "version", "extra")
	if err == nil {
		t.Fatal("want an error for an unexpected argument, got none")
	}
}
