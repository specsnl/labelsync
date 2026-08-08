package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// minimalConfig is enough YAML to parse; the resolution tests care about which
// file was picked, not what is in it.
const minimalConfig = `version: 1
labels:
  - name: "type: bug"
    color: "d73a4a"
`

// dirs holds the three directories a resolution case can place files in: the
// working directory, the XDG config directory, and an unrelated directory only
// --config can reach.
type dirs struct {
	wd    string
	cfg   string
	other string
}

func TestFind(t *testing.T) {
	tests := []struct {
		name     string
		inWD     []string
		inCfg    []string
		inOther  []string
		explicit func(d dirs) string
		want     func(d dirs) string
		wantErr  error
	}{
		{
			name:     "explicit path wins over the working directory",
			inWD:     []string{labelsync.ConfigYMLFile},
			inOther:  []string{"custom.yml"},
			explicit: func(d dirs) string { return filepath.Join(d.other, "custom.yml") },
			want:     func(d dirs) string { return filepath.Join(d.other, "custom.yml") },
		},
		{
			name:     "explicit path that does not exist",
			inWD:     []string{labelsync.ConfigYMLFile},
			explicit: func(d dirs) string { return filepath.Join(d.other, "nope.yml") },
			wantErr:  labelsync.ErrConfigNotFound,
		},
		{
			name:     "explicit directory is searched",
			inOther:  []string{labelsync.ConfigYAMLFile},
			explicit: func(d dirs) string { return d.other },
			want:     func(d dirs) string { return filepath.Join(d.other, labelsync.ConfigYAMLFile) },
		},
		{
			name:     "explicit directory holding neither spelling",
			explicit: func(d dirs) string { return d.other },
			wantErr:  labelsync.ErrConfigNotFound,
		},
		{
			name:     "explicit directory holding both spellings",
			inOther:  []string{labelsync.ConfigYMLFile, labelsync.ConfigYAMLFile},
			explicit: func(d dirs) string { return d.other },
			wantErr:  labelsync.ErrAmbiguousConfigFile,
		},
		{
			name:  "labels.yml in the working directory",
			inWD:  []string{labelsync.ConfigYMLFile},
			inCfg: []string{labelsync.ConfigYMLFile},
			want:  func(d dirs) string { return filepath.Join(d.wd, labelsync.ConfigYMLFile) },
		},
		{
			name: "labels.yaml in the working directory",
			inWD: []string{labelsync.ConfigYAMLFile},
			want: func(d dirs) string { return filepath.Join(d.wd, labelsync.ConfigYAMLFile) },
		},
		{
			name:    "both spellings in the working directory",
			inWD:    []string{labelsync.ConfigYMLFile, labelsync.ConfigYAMLFile},
			wantErr: labelsync.ErrAmbiguousConfigFile,
		},
		{
			name:  "falls through to the XDG config directory",
			inCfg: []string{labelsync.ConfigYMLFile},
			want:  func(d dirs) string { return filepath.Join(d.cfg, labelsync.ConfigYMLFile) },
		},
		{
			name:  "falls through to labels.yaml under XDG",
			inCfg: []string{labelsync.ConfigYAMLFile},
			want:  func(d dirs) string { return filepath.Join(d.cfg, labelsync.ConfigYAMLFile) },
		},
		{
			name:    "both spellings under XDG",
			inCfg:   []string{labelsync.ConfigYMLFile, labelsync.ConfigYAMLFile},
			wantErr: labelsync.ErrAmbiguousConfigFile,
		},
		{
			name:    "nothing anywhere",
			wantErr: labelsync.ErrConfigNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := setupDirs(t)

			writeConfigs(t, d.wd, tt.inWD)
			writeConfigs(t, d.cfg, tt.inCfg)
			writeConfigs(t, d.other, tt.inOther)

			var explicit string
			if tt.explicit != nil {
				explicit = tt.explicit(d)
			}

			got, err := config.Find(explicit)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Find(%q) error = %v, want %v", explicit, err, tt.wantErr)
				}

				// The sentinel has to survive wrapping, because it is what the
				// JSON error_kind field is derived from.
				if kind := labelsync.KindOf(err); kind == "" {
					t.Errorf("KindOf(%v) = %q, want a kind string", err, kind)
				}

				return
			}

			if err != nil {
				t.Fatalf("Find(%q) returned an unexpected error: %v", explicit, err)
			}

			if want := tt.want(d); got != want {
				t.Errorf("Find(%q) = %q, want %q", explicit, got, want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	d := setupDirs(t)
	writeConfigs(t, d.wd, []string{labelsync.ConfigYMLFile})

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load(\"\") returned an unexpected error: %v", err)
	}

	if want := filepath.Join(d.wd, labelsync.ConfigYMLFile); cfg.Path != want {
		t.Errorf("cfg.Path = %q, want %q", cfg.Path, want)
	}

	if len(cfg.Labels) != 1 || cfg.Labels[0].Name != "type: bug" {
		t.Errorf("cfg.Labels = %+v, want the single label from the fixture", cfg.Labels)
	}
}

func TestLoadFile_Missing(t *testing.T) {
	path := filepath.Join(t.TempDir(), labelsync.ConfigYMLFile)

	if _, err := config.LoadFile(path); !errors.Is(err, labelsync.ErrConfigNotFound) {
		t.Errorf("LoadFile(%q) error = %v, want %v", path, err, labelsync.ErrConfigNotFound)
	}
}

func TestLoadFile_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), labelsync.ConfigYMLFile)

	if err := os.WriteFile(path, []byte("labels: [\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	_, err := config.LoadFile(path)
	if err == nil {
		t.Fatal("LoadFile on malformed YAML returned no error")
	}

	// A YAML syntax error is not one of the run's failure modes, so it carries
	// no sentinel — but it does have to name the file it came from.
	if kind := labelsync.KindOf(err); kind != "" {
		t.Errorf("KindOf(%v) = %q, want \"\"", err, kind)
	}
}

// setupDirs points the working directory and XDG_CONFIG_HOME at fresh
// temporary directories, and returns those plus an unrelated third one.
func setupDirs(t *testing.T) dirs {
	t.Helper()

	wd := t.TempDir()
	xdgHome := t.TempDir()
	other := t.TempDir()

	t.Chdir(wd)
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	cfg := filepath.Join(xdgHome, labelsync.AppName)
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatalf("creating %s: %v", cfg, err)
	}

	// macOS resolves the temporary directory through a symlink, and os.Getwd
	// reports the resolved path — so the expected paths have to be resolved
	// too, or every working-directory case compares two spellings of the same
	// directory.
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("resolving %s: %v", wd, err)
	}

	return dirs{wd: resolved, cfg: cfg, other: other}
}

// writeConfigs drops a parseable config file at each of the given names.
func writeConfigs(t *testing.T, dir string, names []string) {
	t.Helper()

	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(minimalConfig), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}
