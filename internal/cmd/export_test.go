package cmd_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// exportedLabels is what the fake repository holds: an out-of-order list with a
// `#`-prefixed upper-case colour, an empty description, and a name YAML would
// read as something other than a string.
const exportedLabels = `[
  {"name":"wontfix","color":"#FFFFFF","description":""},
  {"name":"type: bug","color":"D73A4A","description":"Something isn't working"},
  {"name":"no","color":"0e8a16","description":"A name YAML would take for a bool"}
]`

// TestExport_RoundTrips is the claim the command rests on: what it writes loads
// back as the labels it read. An export that did not round-trip would be a
// starting point that clears the descriptions it was written to preserve.
func TestExport_RoundTrips(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(exportedLabels))

	path := filepath.Join(t.TempDir(), "labels.yml")

	_, _, _, err := runApp(t, app, nil, append(flags, "export", "specsnl/labelsync", "--out", path)...)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// LoadFile validates, so a file that broke a rule fails here rather than on
	// the user's next run.
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("loading the export: %v", err)
	}

	want := []config.Label{
		{Name: "no", Color: "0e8a16", Description: "A name YAML would take for a bool"},
		{Name: "type: bug", Color: "d73a4a", Description: "Something isn't working"},
		{Name: "wontfix", Color: "ffffff", Description: ""},
	}

	if len(cfg.Labels) != len(want) {
		t.Fatalf("loaded %d labels, want %d: %+v", len(cfg.Labels), len(want), cfg.Labels)
	}

	for i, got := range cfg.Labels {
		// Groups come from the exported file's own defaults, not from the API,
		// so they are compared separately.
		if got.Name != want[i].Name || got.Color != want[i].Color || got.Description != want[i].Description {
			t.Errorf("label %d = %+v, want %+v", i, got, want[i])
		}

		if len(got.Groups) == 0 {
			t.Errorf("label %q belongs to no group, so the exported config selects nothing for it", got.Name)
		}
	}

	// The group is the repository it came from, so the file is usable as it
	// lands rather than after an edit nothing told the user to make.
	group, ok := cfg.Groups[config.ExportGroup]
	if !ok {
		t.Fatalf("groups = %v, want a %q group", cfg.Groups, config.ExportGroup)
	}

	if len(group.Repos) != 1 || group.Repos[0] != "specsnl/labelsync" {
		t.Errorf("groups.%s.repos = %v, want [specsnl/labelsync]", config.ExportGroup, group.Repos)
	}
}

// Colours are normalised the way the loader normalises them, and the list is
// sorted, so two exports of one repository are byte-identical however the API
// happened to order them.
func TestExport_IsNormalisedAndSorted(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(exportedLabels))

	_, first, _, err := runApp(t, app, nil, append(flags, "export", "specsnl/labelsync")...)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if strings.Contains(first, "#FFFFFF") || strings.Contains(first, "D73A4A") {
		t.Errorf("the export carries an unnormalised colour:\n%s", first)
	}

	names := []string{`name: "no"`, `name: "type: bug"`, `name: "wontfix"`}

	at := -1

	for _, name := range names {
		i := strings.Index(first, name)
		if i < 0 {
			t.Fatalf("the export does not carry %s:\n%s", name, first)
		}

		if i < at {
			t.Errorf("the export is not sorted by name:\n%s", first)
		}

		at = i
	}

	app2, flags2 := fakeGitHub(t, labelServer(exportedLabels))

	_, second, _, err := runApp(t, app2, nil, append(flags2, "export", "specsnl/labelsync")...)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if first != second {
		t.Errorf("two exports of the same repository differ:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// stdout is the file. `labelsync export x > labels.yml` has to produce a config
// file whatever --output says, so the YAML is not wrapped in a record.
func TestExport_StdoutIsTheFile(t *testing.T) {
	for _, format := range []string{"pretty", "json"} {
		t.Run(format, func(t *testing.T) {
			app, flags := fakeGitHub(t, labelServer(exportedLabels))

			_, stdout, _, err := runApp(t, app, nil,
				append(flags, "--output", format, "export", "specsnl/labelsync")...)
			if err != nil {
				t.Fatalf("export: %v", err)
			}

			cfg, err := config.Parse([]byte(stdout))
			if err != nil {
				t.Fatalf("stdout is not a config file: %v\n%s", err, stdout)
			}

			if err := cfg.Validate(); err != nil {
				t.Errorf("stdout does not validate: %v\n%s", err, stdout)
			}
		})
	}
}

// A repository genuinely holding two labels of one colour is exported as it is,
// and flagged twice over: in the file, where the edit has to be made, and on
// stderr, because a redirected export is a file nobody reads until the next run
// rejects it.
func TestExport_FlagsDuplicateColours(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(`[
	  {"name":"bug","color":"d73a4a","description":"One"},
	  {"name":"defect","color":"d73a4a","description":"The other"}
	]`))

	_, stdout, stderr, err := runApp(t, app, nil, append(flags, "export", "specsnl/labelsync")...)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if !strings.Contains(stdout, "colours must be unique") {
		t.Errorf("the file does not flag the duplicate colour:\n%s", stdout)
	}

	if !strings.Contains(stderr, "d73a4a") || !strings.Contains(stderr, `"bug"`) {
		t.Errorf("stderr = %q, want it to name the colour and the labels sharing it", stderr)
	}

	// Faithful rather than repaired: the repository really does hold both, and
	// inventing a colour would export a file that no longer describes it.
	if strings.Count(stdout, "d73a4a") < 2 {
		t.Errorf("the duplicate colour was not exported as it is:\n%s", stdout)
	}
}

// --out writes the file and reports where it went, and the path is the product
// on stdout rather than the YAML.
func TestExport_OutFile(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(exportedLabels))

	dir := t.TempDir()
	path := filepath.Join(dir, "captured.yml")

	_, stdout, _, err := runApp(t, app, nil, append(flags, "export", "specsnl/labelsync", "--out", path)...)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}

	if !strings.Contains(stdout, path) {
		t.Errorf("stdout = %q, want it to name the file", stdout)
	}

	if strings.Contains(stdout, "version: 1") {
		t.Errorf("stdout carries the YAML as well as the path: %q", stdout)
	}

	// A directory means labels.yml inside it, the same reading --config gets.
	if _, _, _, err := runApp(t, app, nil, append(flags, "export", "specsnl/labelsync", "--out", dir)...); err != nil {
		t.Fatalf("export --out <dir>: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "labels.yml")); err != nil {
		t.Errorf("--out <dir> did not write labels.yml inside it: %v", err)
	}
}

// A repository with no labels would export a config declaring none, which is the
// rule the loader rejects it by — so it fails now, wrapped in that sentinel,
// rather than as a puzzle on the next run.
func TestExport_EmptyRepository(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(`[]`))

	_, _, _, err := runApp(t, app, nil, append(flags, "export", "specsnl/labelsync")...)
	if !errors.Is(err, labelsync.ErrEmptyConfig) {
		t.Fatalf("error = %v, want one wrapping ErrEmptyConfig", err)
	}
}

// A bad reference is the same rule a repos: entry is held to, and it fails
// before a request is sent.
func TestExport_RejectsABadReference(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(exportedLabels))

	_, _, _, err := runApp(t, app, nil, append(flags, "export", "labelsync")...)
	if !errors.Is(err, labelsync.ErrInvalidRepoRef) {
		t.Fatalf("error = %v, want one wrapping ErrInvalidRepoRef", err)
	}
}

// An inaccessible repository is a failure here rather than a skip: the command
// is about one repository, so there is nothing left to report on.
func TestExport_InaccessibleRepository(t *testing.T) {
	app, flags := fakeGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, `{"message":"Not Found"}`)
	}))

	_, _, _, err := runApp(t, app, nil, append(flags, "export", "specsnl/nope")...)
	if !errors.Is(err, labelsync.ErrRepoInaccessible) {
		t.Fatalf("error = %v, want one wrapping ErrRepoInaccessible", err)
	}
}

// The sync help is where a user first meets the risk export exists to remove, so
// it has to say so.
func TestSync_HelpMentionsExport(t *testing.T) {
	_, stdout, _, err := run(t, nil, "sync", "--help")
	if err != nil {
		t.Fatalf("sync --help: %v", err)
	}

	if !strings.Contains(stdout, "export") {
		t.Errorf("sync --help does not mention export:\n%s", stdout)
	}
}
