package config_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/specsnl/labelsync/internal/config"
)

var update = flag.Bool("update", false, "rewrite the .golden files from the current output")

// TestNormalize_Golden pins the whole of normalisation: a config file in,
// the normalised struct marshalled back out. Anything that changes how a
// colour, a name, a default, or an inherited group is handled shows up as a
// diff in the golden file rather than as a subtly different plan later.
//
// Rewrite the goldens with:
//
//	task test:update
func TestNormalize_Golden(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.yml"))
	if err != nil {
		t.Fatalf("globbing the fixtures: %v", err)
	}

	if len(fixtures) == 0 {
		t.Fatal("no fixtures found under testdata")
	}

	for _, fixture := range fixtures {
		name := filepath.Base(fixture)

		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatalf("reading %s: %v", fixture, err)
			}

			cfg, err := config.Parse(data)
			if err != nil {
				t.Fatalf("Parse(%s) returned an unexpected error: %v", fixture, err)
			}

			got, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshalling the normalised config: %v", err)
			}

			assertGolden(t, fixture, string(got))
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "a leading # is stripped and the colour is lowercased",
			yaml: "labels:\n  - name: a\n    color: \"#D73A4A\"\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Labels[0].Color; got != "d73a4a" {
					t.Errorf("color = %q, want %q", got, "d73a4a")
				}
			},
		},
		{
			name: "only one leading # is stripped",
			yaml: "labels:\n  - name: a\n    color: \"##ABC\"\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				// What is left is invalid, which is validate.go's answer to
				// give — normalisation must not quietly repair it.
				if got := cfg.Labels[0].Color; got != "#abc" {
					t.Errorf("color = %q, want %q", got, "#abc")
				}
			},
		},
		{
			name: "label names are trimmed",
			yaml: "labels:\n  - name: \"  type: bug \\t\"\n    color: d73a4a\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Labels[0].Name; got != "type: bug" {
					t.Errorf("name = %q, want %q", got, "type: bug")
				}
			},
		},
		{
			name: "renames are trimmed too, because they are matched against names",
			yaml: "renames:\n  - from: \" bug \"\n    to: \" type: bug \"\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Renames[0]; got.From != "bug" || got.To != "type: bug" {
					t.Errorf("rename = %+v, want {bug type: bug}", got)
				}
			},
		},
		{
			name: "defaults.groups fills in for a label that declares none",
			yaml: "defaults:\n  groups: [a, b]\nlabels:\n  - name: x\n    color: aabbcc\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Labels[0].Groups; len(got) != 2 || got[0] != "a" || got[1] != "b" {
					t.Errorf("groups = %v, want [a b]", got)
				}
			},
		},
		{
			name: "a label's own groups are left alone",
			yaml: "defaults:\n  groups: [a]\nlabels:\n  - name: x\n    color: aabbcc\n    groups: [b]\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Labels[0].Groups; len(got) != 1 || got[0] != "b" {
					t.Errorf("groups = %v, want [b]", got)
				}
			},
		},
		{
			name: "inherited groups are cloned per label, not shared",
			yaml: "defaults:\n  groups: [a]\nlabels:\n  - name: x\n    color: aabbcc\n  - name: y\n    color: ddeeff\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				cfg.Labels[0].Groups[0] = "mutated"

				if got := cfg.Labels[1].Groups[0]; got != "a" {
					t.Errorf("second label's groups = %q after mutating the first, want %q", got, "a")
				}

				if got := cfg.Defaults.Groups[0]; got != "a" {
					t.Errorf("defaults.groups = %q after mutating a label, want %q", got, "a")
				}
			},
		},
		{
			name: "no defaults leaves a label's groups empty",
			yaml: "labels:\n  - name: x\n    color: aabbcc\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Labels[0].Groups; len(got) != 0 {
					t.Errorf("groups = %v, want none", got)
				}
			},
		},
		{
			name: "an omitted description is an empty one",
			yaml: "labels:\n  - name: x\n    color: aabbcc\n",
			want: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				if got := cfg.Labels[0].Description; got != "" {
					t.Errorf("description = %q, want \"\"", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Parse returned an unexpected error: %v", err)
			}

			tt.want(t, cfg)
		})
	}
}

func TestGroupDefaults(t *testing.T) {
	tests := []struct {
		name             string
		yaml             string
		wantSkipArchived bool
		wantSkipForks    bool
		wantVisibility   config.Visibility
	}{
		{
			name:             "omitted keys take the documented defaults",
			yaml:             "groups:\n  g:\n    org: specsnl\n",
			wantSkipArchived: true,
			wantSkipForks:    true,
			wantVisibility:   config.VisibilityAll,
		},
		{
			name:             "a group with no body at all still takes them",
			yaml:             "groups:\n  g:\n",
			wantSkipArchived: true,
			wantSkipForks:    true,
			wantVisibility:   config.VisibilityAll,
		},
		{
			name:             "an explicit false is not overwritten by the default",
			yaml:             "groups:\n  g:\n    org: specsnl\n    skip_archived: false\n    skip_forks: false\n    visibility: private\n",
			wantSkipArchived: false,
			wantSkipForks:    false,
			wantVisibility:   config.VisibilityPrivate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Parse returned an unexpected error: %v", err)
			}

			got := cfg.Groups["g"]

			if got.SkipArchived != tt.wantSkipArchived {
				t.Errorf("skip_archived = %v, want %v", got.SkipArchived, tt.wantSkipArchived)
			}

			if got.SkipForks != tt.wantSkipForks {
				t.Errorf("skip_forks = %v, want %v", got.SkipForks, tt.wantSkipForks)
			}

			if got.Visibility != tt.wantVisibility {
				t.Errorf("visibility = %q, want %q", got.Visibility, tt.wantVisibility)
			}
		})
	}
}

// assertGolden compares got against the fixture's .golden sibling, rewriting
// the file instead when -update is passed.
func assertGolden(t *testing.T, fixture, got string) {
	t.Helper()

	path := fixture[:len(fixture)-len(filepath.Ext(fixture))] + ".golden"

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run the tests with -update to create it)", path, err)
	}

	if got != string(want) {
		t.Errorf("normalised config does not match %s\n--- got ---\n%s\n--- want ---\n%s", path, got, string(want))
	}
}
