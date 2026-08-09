package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/specsnl/labelsync/internal/config"
)

// cataloguePath is this repository's own labels.yml, relative to the package
// directory a `go test` binary runs in.
var cataloguePath = filepath.Join("..", "..", "labels.yml")

// The repository's own catalogue is the worked example the documentation points
// at, and it was hand-written to satisfy the design's validation rules before a
// validator existed. It is loaded here through the real entry point — read,
// parse, normalise, validate — so the example and the rules cannot drift apart
// without a test going red.
//
// The properties below are re-asserted directly rather than left to Validate.
// The point is not to test the validator twice: it is that a later change to a
// rule, or to the file, cannot quietly cost the catalogue a property the
// documentation claims it has.
func TestCatalogue(t *testing.T) {
	cfg := loadCatalogue(t)

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
			// Normalisation has already copied defaults.groups onto every label
			// that named none, so a label with no groups at all would mean the
			// catalogue selects no repositories for it.
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
		// Descriptions are authoritative: an omitted one clears the label's
		// description on GitHub. The catalogue is the worked example, so it does
		// not demonstrate that by accident.
		for _, label := range cfg.Labels {
			if strings.TrimSpace(label.Description) == "" {
				t.Errorf("label %q has no description", label.Name)
			}
		}
	})
}

// The catalogue describes this repository, so the labels it declares are the
// labels the issues in this repository are filed under. Pinning the group keeps
// a self-referential example self-referential.
func TestCatalogue_TargetsThisRepository(t *testing.T) {
	cfg := loadCatalogue(t)

	group, ok := cfg.Groups["self"]
	if !ok {
		t.Fatalf("groups = %v, want a %q group", cfg.Groups, "self")
	}

	if want := []string{"specsnl/labelsync"}; len(group.Repos) != 1 || group.Repos[0] != want[0] {
		t.Errorf("groups.self.repos = %v, want %v", group.Repos, want)
	}
}

// loadCatalogue loads the repository's labels.yml through LoadFile, which is
// the whole point: LoadFile validates, so a catalogue that broke a rule fails
// here rather than at apply time.
func loadCatalogue(t *testing.T) *config.Config {
	t.Helper()

	cfg, err := config.LoadFile(cataloguePath)
	if err != nil {
		t.Fatalf("LoadFile(%q) = %v, want the repository's own catalogue to validate clean", cataloguePath, err)
	}

	return cfg
}
