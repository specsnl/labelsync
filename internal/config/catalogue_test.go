package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/specsnl/labelsync/internal/config"
)

// cataloguePath is this repository's own labels.yml, relative to the package
// directory a `go test` binary runs in.
var cataloguePath = filepath.Join("..", "..", "labels.yml")

// Two files claim to be valid labelsync configs, and both are read by people
// rather than only by the tool: the repository's own catalogue, which is the
// worked example the documentation points at, and the scaffold `labelsync init`
// writes, which is the first config most users will ever see.
//
// They are held to one set of assertions on purpose. The example and the real
// file cannot drift apart into demonstrating different things, and neither can
// drift away from what the validator accepts — a rule that changed under either
// of them fails here rather than in somebody's first run.
func catalogues(t *testing.T) map[string]*config.Config {
	t.Helper()

	return map[string]*config.Config{
		"labels.yml":    loadFile(t, cataloguePath),
		"init scaffold": parseScaffold(t),
	}
}

// The properties below are re-asserted directly rather than left to Validate.
// The point is not to test the validator twice: it is that a later change to a
// rule, or to either file, cannot quietly cost it a property the documentation
// claims it has.
func TestCatalogues(t *testing.T) {
	for name, cfg := range catalogues(t) {
		t.Run(name, func(t *testing.T) {
			t.Run("colours are globally unique", func(t *testing.T) {
				seen := make(map[string]string, len(cfg.Labels))

				for _, label := range cfg.Labels {
					if first, ok := seen[label.Color]; ok {
						t.Errorf("colour %s is used by both %q and %q", label.Color, first, label.Name)
					}

					seen[label.Color] = label.Name
				}
			})

			t.Run("names and descriptions are within GitHub's bounds", func(t *testing.T) {
				for _, label := range cfg.Labels {
					if n := utf8.RuneCountInString(label.Name); n == 0 || n > config.MaxNameRunes {
						t.Errorf("label %q is %d code points, want 1..%d", label.Name, n, config.MaxNameRunes)
					}

					if n := utf8.RuneCountInString(label.Description); n > config.MaxDescriptionRunes {
						t.Errorf("label %q has a %d code point description, the maximum is %d",
							label.Name, n, config.MaxDescriptionRunes)
					}
				}
			})

			t.Run("every referenced group is defined", func(t *testing.T) {
				for _, group := range cfg.Defaults.Groups {
					if _, ok := cfg.Groups[group]; !ok {
						t.Errorf("defaults.groups names %q, which no group defines", group)
					}
				}

				for _, label := range cfg.Labels {
					// Normalisation has already copied defaults.groups onto every
					// label that named none, so a label with no groups at all
					// would mean the file selects no repositories for it.
					if len(label.Groups) == 0 {
						t.Errorf("label %q belongs to no group", label.Name)
					}

					for _, group := range label.Groups {
						if _, ok := cfg.Groups[group]; !ok {
							t.Errorf("label %q is in %q, which no group defines", label.Name, group)
						}
					}
				}

				for name, group := range cfg.Groups {
					for _, included := range group.IncludeGroups {
						if _, ok := cfg.Groups[included]; !ok {
							t.Errorf("group %q includes %q, which no group defines", name, included)
						}
					}
				}
			})

			t.Run("every label carries a description", func(t *testing.T) {
				// Descriptions are authoritative: an omitted one clears the
				// label's description on GitHub. Neither file demonstrates that
				// by accident.
				for _, label := range cfg.Labels {
					if strings.TrimSpace(label.Description) == "" {
						t.Errorf("label %q has no description", label.Name)
					}
				}
			})
		})
	}
}

// The catalogue describes this repository, so the labels it declares are the
// labels the issues in this repository are filed under. Pinning the group keeps
// a self-referential example self-referential.
func TestCatalogue_TargetsThisRepository(t *testing.T) {
	cfg := loadFile(t, cataloguePath)

	group, ok := cfg.Groups["self"]
	if !ok {
		t.Fatalf("groups = %v, want a %q group", cfg.Groups, "self")
	}

	if want := []string{"specsnl/labelsync"}; len(group.Repos) != 1 || group.Repos[0] != want[0] {
		t.Errorf("groups.self.repos = %v, want %v", group.Repos, want)
	}
}

// GitHub creates `bug` and `enhancement` in every new repository, and this
// catalogue's own `type:` labels are where they belong. Both are declared as
// renames rather than left as prune candidates, because a rename is a PATCH: the
// label keeps its identity and stays on every issue and pull request already
// carrying it, where a delete and a create would silently unlabel all of them.
//
// On this repository the two renames are inert — `type: bug` and `type: feature`
// already exist, so the planner skips a rename whose target is present — and that
// is the point: the entries are what makes the *next* repository the catalogue
// covers migrate instead of losing its history.
func TestCatalogue_MigratesGitHubsStockLabels(t *testing.T) {
	cfg := loadFile(t, cataloguePath)

	want := map[string]string{"bug": "type: bug", "enhancement": "type: feature"}

	got := make(map[string]string, len(cfg.Renames))
	for _, rename := range cfg.Renames {
		got[rename.From] = rename.To
	}

	for from, to := range want {
		if got[from] != to {
			t.Errorf("renames[%q] = %q, want %q", from, got[from], to)
		}
	}
}

// The scaffold is what a user meets first, so it demonstrates the sections a
// config has rather than the smallest thing that validates. Each of these is a
// section the documentation tells them to look at in the file they were just
// given.
func TestScaffold_DemonstratesEverySection(t *testing.T) {
	cfg := parseScaffold(t)

	sources := make(map[string]bool, len(cfg.Groups))
	for _, group := range cfg.Groups {
		sources["org"] = sources["org"] || group.Org != ""
		sources["repos"] = sources["repos"] || len(group.Repos) > 0
	}

	for _, source := range []string{"org", "repos"} {
		if !sources[source] {
			t.Errorf("no group in the scaffold uses the %q source", source)
		}
	}

	if len(cfg.Defaults.Groups) == 0 {
		t.Error("the scaffold declares no defaults.groups")
	}

	if len(cfg.Renames) == 0 {
		t.Error("the scaffold declares no renames")
	}

	// A label naming its own groups is the thing defaults.groups is contrasted
	// with, and the scaffold's comments say so.
	if !hasLabelWithOwnGroups(cfg) {
		t.Error("no label in the scaffold names groups of its own")
	}
}

// The scaffold is written to disk as bytes and read back by the same loader any
// other config file goes through, comments and all. Parsing the embedded copy
// would skip the one step that could go wrong.
func TestScaffold_LoadsFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.yml")

	if err := os.WriteFile(path, config.Scaffold(), 0o600); err != nil {
		t.Fatalf("writing the scaffold: %v", err)
	}

	loadFile(t, path)
}

// Scaffold hands out a copy. A caller that sliced into the embedded bytes could
// otherwise change what every later call returns, for the life of the process.
func TestScaffold_IsCopied(t *testing.T) {
	first := config.Scaffold()
	first[0] = 'x'

	if second := config.Scaffold(); second[0] == 'x' {
		t.Error("Scaffold() handed out the embedded bytes; a caller can corrupt every later call")
	}
}

func hasLabelWithOwnGroups(cfg *config.Config) bool {
	for _, label := range cfg.Labels {
		if len(label.Groups) != len(cfg.Defaults.Groups) {
			return true
		}

		for i, group := range label.Groups {
			if group != cfg.Defaults.Groups[i] {
				return true
			}
		}
	}

	return false
}

// parseScaffold runs the embedded starter config through the whole load path —
// parse, normalise, validate — which is the claim `init` makes about it: the
// tool can never emit a config it rejects.
func parseScaffold(t *testing.T) *config.Config {
	t.Helper()

	cfg, err := config.Parse(config.Scaffold())
	if err != nil {
		t.Fatalf("Parse(Scaffold()) = %v, want the scaffold to parse", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Scaffold().Validate() = %v, want the scaffold to validate clean", err)
	}

	return cfg
}

// loadFile loads a config through LoadFile, which is the whole point: LoadFile
// validates, so a file that broke a rule fails here rather than at apply time.
func loadFile(t *testing.T, path string) *config.Config {
	t.Helper()

	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%q) = %v, want it to validate clean", path, err)
	}

	return cfg
}
