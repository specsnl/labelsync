package output_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the .golden files from the current output")

// goldenPlain is the environment a PrettyWriter is given when the test wants
// unstyled output: empty, so colorprofile finds nothing to enable, and the
// stream is a buffer rather than a terminal. This is the CI-log rendering.
var goldenPlain = []string{}

// goldenColor forces colour on regardless of where the test runs.
// CLICOLOR_FORCE overrides the not-a-terminal answer; TERM picks the depth.
var goldenColor = []string{"CLICOLOR_FORCE=1", "TERM=xterm-256color"}

// assertGolden compares got against testdata/<name>.golden, rewriting the file
// instead when -update is passed:
//
//	task test:update
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}

		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run the tests with -update to create it)", path, err)
	}

	if got != string(want) {
		t.Errorf("output does not match %s\n--- got ---\n%q\n--- want ---\n%q", path, got, string(want))
	}
}
