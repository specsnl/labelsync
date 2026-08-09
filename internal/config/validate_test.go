package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// TestValidate is the package's main suite: every rule in the design's
// validation table, with a valid case and an invalid one, asserting the
// specific sentinel through errors.Is.
//
// Each case is a whole config file rather than a hand-built struct, because
// Validate runs on a normalised Config and the normalisation — trimmed names,
// stripped "#", inherited groups — is part of what the rules assume.
func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr error
	}{
		// version
		{
			name: "a supported version",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
`,
		},
		{
			name: "a missing version",
			yaml: `
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrUnsupportedConfigVersion,
		},
		{
			name: "a version from the future",
			yaml: `
version: 2
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrUnsupportedConfigVersion,
		},

		// at least one label
		{
			name:    "no labels section at all",
			yaml:    "version: 1\n",
			wantErr: labelsync.ErrEmptyConfig,
		},
		{
			name: "an empty labels section",
			yaml: `
version: 1
labels: []
`,
			wantErr: labelsync.ErrEmptyConfig,
		},

		// unique names, case-insensitively
		{
			name: "names that differ by more than case",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: bugfix
    color: 0e8a16
`,
		},
		{
			name: "the same name twice",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: bug
    color: 0e8a16
`,
			wantErr: labelsync.ErrDuplicateLabelName,
		},
		{
			name: "names that differ only by case",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: Bug
    color: 0e8a16
`,
			wantErr: labelsync.ErrDuplicateLabelName,
		},
		{
			name: "names that differ only by surrounding whitespace, which is trimmed away",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: "  bug  "
    color: 0e8a16
`,
			wantErr: labelsync.ErrDuplicateLabelName,
		},

		// globally unique colours
		{
			name: "distinct colours",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: feature
    color: 0e8a16
`,
		},
		{
			name: "the same colour on two labels",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: feature
    color: d73a4a
`,
			wantErr: labelsync.ErrDuplicateLabelColor,
		},
		{
			name: "the same colour written two ways",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
  - name: feature
    color: "#D73A4A"
`,
			wantErr: labelsync.ErrDuplicateLabelColor,
		},
		{
			name: "the same colour in groups that never share a repository is still a duplicate",
			yaml: `
version: 1
groups:
  a:
    repos: [specsnl/a]
  b:
    repos: [specsnl/b]
labels:
  - name: bug
    color: d73a4a
    groups: [a]
  - name: feature
    color: d73a4a
    groups: [b]
`,
			wantErr: labelsync.ErrDuplicateLabelColor,
		},

		// colour format
		{
			name: "a colour with a leading # and upper case",
			yaml: `
version: 1
labels:
  - name: bug
    color: "#D73A4A"
`,
		},
		{
			name: "a three-digit colour",
			yaml: `
version: 1
labels:
  - name: bug
    color: abc
`,
			wantErr: labelsync.ErrInvalidColor,
		},
		{
			name: "a colour with a non-hex digit",
			yaml: `
version: 1
labels:
  - name: bug
    color: gggggg
`,
			wantErr: labelsync.ErrInvalidColor,
		},
		{
			name: "a colour with two leading #, only one of which is stripped",
			yaml: `
version: 1
labels:
  - name: bug
    color: "##d73a4a"
`,
			wantErr: labelsync.ErrInvalidColor,
		},
		{
			name: "a missing colour",
			yaml: `
version: 1
labels:
  - name: bug
`,
			wantErr: labelsync.ErrInvalidColor,
		},

		// description length, in code points
		{
			name: "a 100 code point ASCII description",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
    description: "` + strings.Repeat("a", 100) + `"
`,
		},
		{
			name: "a 101 code point ASCII description",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
    description: "` + strings.Repeat("a", 101) + `"
`,
			wantErr: labelsync.ErrDescriptionTooLong,
		},
		{
			// 400 bytes, 200 UTF-16 units, 100 code points — accepted by the API,
			// and the case a len()-based bound gets wrong while passing every
			// ASCII test above.
			name: "a 100 code point emoji description",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
    description: "` + strings.Repeat("🐛", 100) + `"
`,
		},
		{
			name: "a 101 code point emoji description",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
    description: "` + strings.Repeat("🐛", 101) + `"
`,
			wantErr: labelsync.ErrDescriptionTooLong,
		},
		{
			name: "no description at all",
			yaml: `
version: 1
labels:
  - name: bug
    color: d73a4a
`,
		},

		// label name length, in code points
		{
			name: "a 50 code point ASCII name",
			yaml: `
version: 1
labels:
  - name: "` + strings.Repeat("a", 50) + `"
    color: d73a4a
`,
		},
		{
			name: "a 51 code point ASCII name",
			yaml: `
version: 1
labels:
  - name: "` + strings.Repeat("a", 51) + `"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},
		{
			// 25 emoji and 25 letters: 50 code points, 125 bytes.
			name: "a 50 code point name mixing emoji and ASCII",
			yaml: `
version: 1
labels:
  - name: "` + strings.Repeat("🐛", 25) + strings.Repeat("a", 25) + `"
    color: d73a4a
`,
		},
		{
			name: "a 51 code point name mixing emoji and ASCII",
			yaml: `
version: 1
labels:
  - name: "` + strings.Repeat("🐛", 26) + strings.Repeat("a", 25) + `"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},
		{
			name: "a name that is only within bounds once trimmed",
			yaml: `
version: 1
labels:
  - name: "   ` + strings.Repeat("a", 50) + `   "
    color: d73a4a
`,
		},
		{
			name: "an empty name",
			yaml: `
version: 1
labels:
  - name: ""
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},
		{
			name: "a name that is empty once trimmed",
			yaml: `
version: 1
labels:
  - name: "   "
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},

		// emoji-only names
		{
			name: "a name with an emoji and a word",
			yaml: `
version: 1
labels:
  - name: "🐛 bug"
    color: d73a4a
`,
		},
		{
			name: "a colon-style name, which is not an emoji at all",
			yaml: `
version: 1
labels:
  - name: ":bug:"
    color: d73a4a
`,
		},
		{
			name: "a name of one emoji",
			yaml: `
version: 1
labels:
  - name: "🐛"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},
		{
			name: "a name of two emoji separated by a space",
			yaml: `
version: 1
labels:
  - name: "🐛 🐞"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},
		{
			name: "a name of one ZWJ sequence",
			yaml: `
version: 1
labels:
  - name: "👨‍👩‍👧"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidLabelName,
		},

		// group references
		{
			name: "a label in a group that exists",
			yaml: `
version: 1
groups:
  specs:
    org: specsnl
labels:
  - name: bug
    color: d73a4a
    groups: [specs]
`,
		},
		{
			name: "a label in a group that does not exist",
			yaml: `
version: 1
groups:
  specs:
    org: specsnl
labels:
  - name: bug
    color: d73a4a
    groups: [nope]
`,
			wantErr: labelsync.ErrUnknownGroup,
		},
		{
			name: "defaults.groups naming a group that does not exist",
			yaml: `
version: 1
groups:
  specs:
    org: specsnl
defaults:
  groups: [nope]
labels:
  - name: bug
    color: d73a4a
    groups: [specs]
`,
			wantErr: labelsync.ErrUnknownGroup,
		},
		{
			name: "include_groups naming a group that does not exist",
			yaml: `
version: 1
groups:
  everything:
    include_groups: [nope]
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrUnknownGroup,
		},

		// exactly one group source
		{
			name: "each of the four sources on its own",
			yaml: `
version: 1
groups:
  a:
    org: specsnl
  b:
    user: Ilyes512
  c:
    repos: [specsnl/labelsync]
  d:
    include_groups: [a, b, c]
labels:
  - name: bug
    color: d73a4a
    groups: [d]
`,
		},
		{
			name: "a group with two sources",
			yaml: `
version: 1
groups:
  a:
    org: specsnl
    user: Ilyes512
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a group with no source",
			yaml: `
version: 1
groups:
  a:
    exclude: ["*-archive"]
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a group written as a bare key, which has no source either",
			yaml: `
version: 1
groups:
  a:
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrAmbiguousGroupSource,
		},

		// include_groups cycles
		{
			name: "a diamond, which is not a cycle",
			yaml: `
version: 1
groups:
  base:
    org: specsnl
  left:
    include_groups: [base]
  right:
    include_groups: [base]
  top:
    include_groups: [left, right]
labels:
  - name: bug
    color: d73a4a
    groups: [top]
`,
		},
		{
			name: "a group that includes itself",
			yaml: `
version: 1
groups:
  a:
    include_groups: [a]
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrCyclicGroup,
		},
		{
			name: "a cycle through three groups",
			yaml: `
version: 1
groups:
  a:
    include_groups: [b]
  b:
    include_groups: [c]
  c:
    include_groups: [a]
labels:
  - name: bug
    color: d73a4a
`,
			wantErr: labelsync.ErrCyclicGroup,
		},

		// repos entries
		{
			name: "repos entries in owner/repo form",
			yaml: `
version: 1
groups:
  a:
    repos: [specsnl/labelsync, Ilyes512/dot-files]
labels:
  - name: bug
    color: d73a4a
    groups: [a]
`,
		},
		{
			name: "a repos entry with no owner",
			yaml: `
version: 1
groups:
  a:
    repos: [labelsync]
labels:
  - name: bug
    color: d73a4a
    groups: [a]
`,
			wantErr: labelsync.ErrInvalidRepoRef,
		},
		{
			name: "a repos entry that is a URL",
			yaml: `
version: 1
groups:
  a:
    repos: ["https://github.com/specsnl/labelsync"]
labels:
  - name: bug
    color: d73a4a
    groups: [a]
`,
			wantErr: labelsync.ErrInvalidRepoRef,
		},
		{
			name: "a repos entry with a space in it",
			yaml: `
version: 1
groups:
  a:
    repos: ["specsnl/label sync"]
labels:
  - name: bug
    color: d73a4a
    groups: [a]
`,
			wantErr: labelsync.ErrInvalidRepoRef,
		},

		// renames
		{
			name: "a rename onto a configured label",
			yaml: `
version: 1
renames:
  - from: bug
    to: "type: bug"
labels:
  - name: "type: bug"
    color: d73a4a
`,
		},
		{
			name: "a rename onto a configured label, differing in case",
			yaml: `
version: 1
renames:
  - from: bug
    to: "TYPE: BUG"
labels:
  - name: "type: bug"
    color: d73a4a
`,
		},
		{
			name: "a rename onto a name no label declares",
			yaml: `
version: 1
renames:
  - from: bug
    to: "type: defect"
labels:
  - name: "type: bug"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "a rename whose from is itself a configured label",
			yaml: `
version: 1
renames:
  - from: "type: feature"
    to: "type: bug"
labels:
  - name: "type: bug"
    color: d73a4a
  - name: "type: feature"
    color: 0e8a16
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "a case-only rename, which step 5 converges without one",
			yaml: `
version: 1
renames:
  - from: bug
    to: Bug
labels:
  - name: Bug
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "a chained rename",
			yaml: `
version: 1
renames:
  - from: bug
    to: defect
  - from: defect
    to: "type: bug"
labels:
  - name: defect
    color: d73a4a
  - name: "type: bug"
    color: 0e8a16
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "two renames onto the same label",
			yaml: `
version: 1
renames:
  - from: bug
    to: "type: bug"
  - from: defect
    to: "type: bug"
labels:
  - name: "type: bug"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "the same from renamed twice",
			yaml: `
version: 1
renames:
  - from: bug
    to: "type: bug"
  - from: BUG
    to: "type: feature"
labels:
  - name: "type: bug"
    color: d73a4a
  - name: "type: feature"
    color: 0e8a16
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "a rename with no to",
			yaml: `
version: 1
renames:
  - from: bug
    to: ""
labels:
  - name: "type: bug"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidRename,
		},
		{
			name: "a rename with no from",
			yaml: `
version: 1
renames:
  - from: ""
    to: "type: bug"
labels:
  - name: "type: bug"
    color: d73a4a
`,
			wantErr: labelsync.ErrInvalidRename,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Parse returned an unexpected error: %v", err)
			}

			err = cfg.Validate()

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() returned an unexpected error: %v", err)
				}

				return
			}

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}

			// The sentinel has to survive wrapping, because it is what the JSON
			// error_kind field is derived from.
			if kind := labelsync.KindOf(err); kind == "" {
				t.Errorf("KindOf(%v) = \"\", want a kind string", err)
			}
		})
	}
}

// TestValidate_DesignExample validates the design plan's full example, which is
// the fixture the normalisation goldens use. A rule that rejected it would be a
// rule that rejects the documentation.
func TestValidate_DesignExample(t *testing.T) {
	cfg, err := config.LoadFile("testdata/full.yml")
	if err != nil {
		t.Fatalf("LoadFile(testdata/full.yml) returned an unexpected error: %v", err)
	}

	if cfg.Version != config.SchemaVersion {
		t.Errorf("cfg.Version = %d, want %d", cfg.Version, config.SchemaVersion)
	}
}

// TestValidate_Bounds pins the constants against the unit they are counted in,
// so that a change to either one has to be deliberate.
func TestValidate_Bounds(t *testing.T) {
	if config.MaxNameRunes != 50 {
		t.Errorf("MaxNameRunes = %d, want 50", config.MaxNameRunes)
	}

	if config.MaxDescriptionRunes != 100 {
		t.Errorf("MaxDescriptionRunes = %d, want 100", config.MaxDescriptionRunes)
	}

	// The boundary fixtures above are only meaningful if an emoji really is one
	// code point and four bytes — otherwise a len()-based bound would pass them.
	emoji := strings.Repeat("🐛", config.MaxDescriptionRunes)

	if got := utf8.RuneCountInString(emoji); got != config.MaxDescriptionRunes {
		t.Errorf("RuneCountInString(100 emoji) = %d, want %d", got, config.MaxDescriptionRunes)
	}

	if got := len(emoji); got != 4*config.MaxDescriptionRunes {
		t.Errorf("len(100 emoji) = %d, want %d", got, 4*config.MaxDescriptionRunes)
	}
}

// TestLoadFile_Invalid pins that validation runs as part of loading a file:
// the point of the rules is that nothing downstream has to repeat them.
func TestLoadFile_Invalid(t *testing.T) {
	path := writeTemp(t, "version: 1\nlabels: []\n")

	_, err := config.LoadFile(path)
	if !errors.Is(err, labelsync.ErrEmptyConfig) {
		t.Fatalf("LoadFile(%q) error = %v, want %v", path, err, labelsync.ErrEmptyConfig)
	}

	if !strings.Contains(err.Error(), path) {
		t.Errorf("LoadFile error = %q, want it to name %q", err, path)
	}
}

// writeTemp drops content into a config file in a temporary directory and
// returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), labelsync.ConfigYMLFile)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	return path
}
