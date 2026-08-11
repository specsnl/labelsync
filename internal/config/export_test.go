package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// exportSample is deliberately awkward: out of order, an upper-case colour with
// a `#`, a name with surrounding whitespace, an empty description, a name YAML
// would read as a bool, and a colour two labels share.
func exportSample() []config.Label {
	return []config.Label{
		{Name: "wontfix", Color: "#FFFFFF", Description: ""},
		{Name: "  type: bug  ", Color: "D73A4A", Description: "Something isn't working"},
		{Name: "no", Color: "0e8a16", Description: "A name YAML would take for a bool"},
		{Name: "defect", Color: "d73a4a", Description: "The duplicate colour"},
	}
}

// The emitted file is pinned by a golden, because every property it has is one a
// reader would have to notice by eye otherwise: the header, the section
// comments, the quoting, the sort order, and the annotation on a shared colour.
func TestExport_Golden(t *testing.T) {
	rendered, err := config.Export("specsnl/labelsync", exportSample())
	if err != nil {
		t.Fatalf("Export() error = %v, want nil", err)
	}

	// The fixture path is the .golden's sibling, which is the convention
	// assertGolden and the rest of this package's goldens share. There is no
	// .yml here: the input is the sample above rather than a file.
	assertGolden(t, filepath.Join("testdata", "export.yml"), string(rendered))
}

// Export normalises exactly the way the loader does, so exporting what was just
// loaded is a fixed point rather than a diff. Anything else would make
// export → edit → export a churn of incidental spelling changes.
func TestExport_RoundTripsThroughTheLoader(t *testing.T) {
	first, err := config.Export("specsnl/labelsync", exportSample())
	if err != nil {
		t.Fatalf("Export() error = %v, want nil", err)
	}

	cfg, err := config.Parse(first)
	if err != nil {
		t.Fatalf("Parse(export) = %v, want it to parse", err)
	}

	second, err := config.Export("specsnl/labelsync", cfg.Labels)
	if err != nil {
		t.Fatalf("Export() error = %v, want nil", err)
	}

	if string(first) != string(second) {
		t.Errorf("export is not a fixed point:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// The ordinary case validates clean. The whole value of the command is that what
// it writes is a config file, not something shaped like one.
func TestExport_ValidatesClean(t *testing.T) {
	rendered, err := config.Export("specsnl/labelsync", []config.Label{
		{Name: "type: bug", Color: "d73a4a", Description: "Something isn't working"},
		{Name: "type: feature", Color: "a2eeef", Description: "New functionality"},
	})
	if err != nil {
		t.Fatalf("Export() error = %v, want nil", err)
	}

	path := filepath.Join(t.TempDir(), "labels.yml")

	if err := os.WriteFile(path, rendered, 0o600); err != nil {
		t.Fatalf("writing the export: %v", err)
	}

	if _, err := config.LoadFile(path); err != nil {
		t.Fatalf("LoadFile(export) = %v, want it to validate clean", err)
	}
}

// A duplicate colour is exported as it is and annotated, rather than repaired.
// The repository really does hold both, and inventing a colour would export a
// file that no longer describes it — so the file is rejected by the loader until
// a human picks, which is the one thing this cannot do for them.
func TestExport_DuplicateColourIsFlaggedNotRepaired(t *testing.T) {
	rendered, err := config.Export("specsnl/labelsync", exportSample())
	if err != nil {
		t.Fatalf("Export() error = %v, want nil", err)
	}

	if want := 2; strings.Count(string(rendered), `color: "d73a4a"`) != want {
		t.Errorf("the shared colour was not exported as it is:\n%s", rendered)
	}

	if count := strings.Count(string(rendered), "colours must be unique"); count != 2 {
		t.Errorf("the shared colour is annotated %d times, want 2 (once per label):\n%s", count, rendered)
	}

	cfg, err := config.Parse(rendered)
	if err != nil {
		t.Fatalf("Parse(export) = %v, want it to parse", err)
	}

	// It parses, and the loader rejects it — which is what the annotation is
	// there to explain.
	if err := cfg.Validate(); !errors.Is(err, labelsync.ErrDuplicateLabelColor) {
		t.Errorf("Validate() = %v, want ErrDuplicateLabelColor", err)
	}
}

// DuplicateColors is what the command warns with, so it reports every colour
// more than one label uses and nothing else.
func TestDuplicateColors(t *testing.T) {
	got := config.DuplicateColors([]config.Label{
		{Name: "b", Color: "#D73A4A"},
		{Name: "a", Color: "d73a4a"},
		{Name: "c", Color: "0e8a16"},
	})

	if len(got) != 1 {
		t.Fatalf("DuplicateColors() = %v, want one entry", got)
	}

	// Normalised, so `#D73A4A` and `d73a4a` are one colour — the same reading
	// the loader gives them.
	names, ok := got["d73a4a"]
	if !ok {
		t.Fatalf("DuplicateColors() = %v, want it keyed by the normalised colour", got)
	}

	if strings.Join(names, ",") != "a,b" {
		t.Errorf("names = %v, want [a b] sorted", names)
	}
}

// A repository with no labels would export a config declaring none, which is the
// rule the loader would reject it by.
func TestExport_NothingToExport(t *testing.T) {
	_, err := config.Export("specsnl/labelsync", nil)
	if !errors.Is(err, labelsync.ErrEmptyConfig) {
		t.Fatalf("Export() error = %v, want one wrapping ErrEmptyConfig", err)
	}
}
