package cmd_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// TestInit_WritesTheScaffold covers the whole of the happy path: the file lands
// where --config points, it is byte-for-byte the scaffold the config package
// owns, and the path is the command's product on stdout.
func TestInit_WritesTheScaffold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.yml")

	_, stdout, stderr, err := run(t, nil, "--config", path, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	written, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading the scaffolded file: %v", readErr)
	}

	if string(written) != string(config.Scaffold()) {
		t.Errorf("the scaffolded file is not config.Scaffold():\n%s", written)
	}

	if !strings.Contains(stdout, path) {
		t.Errorf("stdout = %q, want it to name %q", stdout, path)
	}

	// The next step is narration, not the product: a user redirecting stdout
	// wants the path in the file and nothing else.
	if !strings.Contains(stderr, "sync --dry-run") {
		t.Errorf("stderr = %q, want it to suggest the next step", stderr)
	}
}

// The scaffolded file has to validate, or `init` hands a user a config the very
// next command rejects. config's own suite asserts the same of the embedded
// bytes; this asserts it of what actually reached the disk.
func TestInit_ScaffoldValidates(t *testing.T) {
	dir := t.TempDir()

	if _, _, _, err := run(t, nil, "--config", dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, err := config.Load(dir); err != nil {
		t.Fatalf("loading the scaffolded config: %v, want it to validate clean", err)
	}
}

// A --config naming a directory means labels.yml inside it, which is the same
// reading config.Find gives the flag.
func TestInit_ConfigDirectoryMeansLabelsYML(t *testing.T) {
	dir := t.TempDir()

	if _, _, _, err := run(t, nil, "--config", dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "labels.yml")); err != nil {
		t.Errorf("labels.yml was not written into the directory: %v", err)
	}
}

// Overwriting a hand-edited catalogue is not a thing to do by accident.
func TestInit_RefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.yml")

	const existing = "version: 1 # hand-edited\n"

	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seeding an existing config: %v", err)
	}

	_, _, _, err := run(t, nil, "--config", path, "init")
	if !errors.Is(err, labelsync.ErrConfigExists) {
		t.Fatalf("error = %v, want one wrapping ErrConfigExists", err)
	}

	if kind := labelsync.KindOf(err); kind != "config_exists" {
		t.Errorf("error_kind = %q, want %q", kind, "config_exists")
	}

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading the file back: %v", readErr)
	}

	if string(after) != existing {
		t.Errorf("the existing file was modified: %q", after)
	}
}

// --force is the answer to that refusal, and the only one.
func TestInit_ForceOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.yml")

	if err := os.WriteFile(path, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("seeding an existing config: %v", err)
	}

	if _, _, _, err := run(t, nil, "--config", path, "init", "--force"); err != nil {
		t.Fatalf("init --force: %v", err)
	}

	written, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading the file back: %v", readErr)
	}

	if string(written) != string(config.Scaffold()) {
		t.Errorf("--force did not overwrite the file:\n%s", written)
	}
}

// Writing labels.yml next to a labels.yaml leaves a directory every later run
// rejects as ambiguous — one step removed from the command that caused it. That
// is not something --force can make loadable, so it refuses either way.
func TestInit_RefusesToCreateAnAmbiguousDirectory(t *testing.T) {
	for _, force := range []string{"", "--force"} {
		t.Run("force="+force, func(t *testing.T) {
			dir := t.TempDir()

			if err := os.WriteFile(filepath.Join(dir, "labels.yaml"), []byte("version: 1\n"), 0o600); err != nil {
				t.Fatalf("seeding labels.yaml: %v", err)
			}

			args := []string{"--config", dir, "init"}
			if force != "" {
				args = append(args, force)
			}

			_, _, _, err := run(t, nil, args...)
			if !errors.Is(err, labelsync.ErrAmbiguousConfigFile) {
				t.Fatalf("error = %v, want one wrapping ErrAmbiguousConfigFile", err)
			}

			if _, statErr := os.Stat(filepath.Join(dir, "labels.yml")); statErr == nil {
				t.Error("labels.yml was written anyway, leaving the directory ambiguous")
			}
		})
	}
}

// stdout stays one typed object per line, so `labelsync init -o json | jq -r
// .path` is a path and not a sentence containing one.
func TestInit_JSONRendering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.yml")

	_, stdout, _, err := run(t, nil, "--output", "json", "--config", path, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	var row struct {
		Path string `json:"path"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &row); err != nil {
		t.Fatalf("stdout %q is not one JSON object: %v", stdout, err)
	}

	if row.Path != path {
		t.Errorf("path = %q, want %q", row.Path, path)
	}
}
