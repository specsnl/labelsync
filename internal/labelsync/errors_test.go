package labelsync_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// allSentinels pairs every exported sentinel with its declared name and its
// documented kind string. Adding a sentinel without adding it here fails
// TestKindOf_CoversEverySentinel.
var allSentinels = []struct {
	name string
	err  error
	kind string
}{
	{"ErrConfigNotFound", labelsync.ErrConfigNotFound, "config_not_found"},
	{"ErrAmbiguousConfigFile", labelsync.ErrAmbiguousConfigFile, "ambiguous_config_file"},
	{"ErrConfigExists", labelsync.ErrConfigExists, "config_exists"},
	{"ErrUnsupportedConfigVersion", labelsync.ErrUnsupportedConfigVersion, "unsupported_config_version"},
	{"ErrEmptyConfig", labelsync.ErrEmptyConfig, "empty_config"},
	{"ErrDuplicateLabelName", labelsync.ErrDuplicateLabelName, "duplicate_label_name"},
	{"ErrDuplicateLabelColor", labelsync.ErrDuplicateLabelColor, "duplicate_label_color"},
	{"ErrInvalidColor", labelsync.ErrInvalidColor, "invalid_color"},
	{"ErrInvalidLabelName", labelsync.ErrInvalidLabelName, "invalid_label_name"},
	{"ErrDescriptionTooLong", labelsync.ErrDescriptionTooLong, "description_too_long"},
	{"ErrUnknownGroup", labelsync.ErrUnknownGroup, "unknown_group"},
	{"ErrAmbiguousGroupSource", labelsync.ErrAmbiguousGroupSource, "ambiguous_group_source"},
	{"ErrCyclicGroup", labelsync.ErrCyclicGroup, "cyclic_group"},
	{"ErrInvalidRepoRef", labelsync.ErrInvalidRepoRef, "invalid_repo_ref"},
	{"ErrInvalidRename", labelsync.ErrInvalidRename, "invalid_rename"},
	{"ErrNoToken", labelsync.ErrNoToken, "no_token"},
	{"ErrInteractiveRequired", labelsync.ErrInteractiveRequired, "interactive_required"},
	{"ErrRepoInaccessible", labelsync.ErrRepoInaccessible, "repo_inaccessible"},
	{"ErrUnsafeCacheDir", labelsync.ErrUnsafeCacheDir, "unsafe_cache_dir"},
	{"ErrMaxWaitExceeded", labelsync.ErrMaxWaitExceeded, "max_wait_exceeded"},
	{"ErrBudgetExhausted", labelsync.ErrBudgetExhausted, "budget_exhausted"},
}

func TestKindOf_KnownSentinels(t *testing.T) {
	for _, tc := range allSentinels {
		t.Run(tc.kind, func(t *testing.T) {
			if got := labelsync.KindOf(tc.err); got != tc.kind {
				t.Errorf("KindOf(%v) = %q, want %q", tc.err, got, tc.kind)
			}
		})
	}
}

func TestKindOf_WrappedSentinels(t *testing.T) {
	for _, tc := range allSentinels {
		t.Run(tc.kind, func(t *testing.T) {
			// Several layers deep, the way a real call stack wraps: a leaf call
			// site adding detail, then each caller adding its own context.
			wrapped := fmt.Errorf("%w: specsnl/example-website", tc.err)
			wrapped = fmt.Errorf("validating label %q: %w", "type: bug", wrapped)
			wrapped = fmt.Errorf("loading config: %w", wrapped)

			if got := labelsync.KindOf(wrapped); got != tc.kind {
				t.Errorf("KindOf(%v) = %q, want %q", wrapped, got, tc.kind)
			}

			if !errors.Is(wrapped, tc.err) {
				t.Errorf("errors.Is(%v, %v) = false, want true", wrapped, tc.err)
			}
		})
	}
}

func TestKindOf_UnknownError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"unrelated", errors.New("some unknown error")},
		{"wrapped unrelated", fmt.Errorf("loading config: %w", errors.New("boom"))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelsync.KindOf(tc.err); got != "" {
				t.Errorf("KindOf(%v) = %q, want %q", tc.err, got, "")
			}
		})
	}
}

// TestKindOf_KindsAreUnique guards the JSON contract: two sentinels sharing a
// kind string would make error_kind ambiguous for consumers.
func TestKindOf_KindsAreUnique(t *testing.T) {
	seen := make(map[string]error, len(allSentinels))

	for _, tc := range allSentinels {
		if first, dup := seen[tc.kind]; dup {
			t.Errorf("kind %q is used by both %v and %v", tc.kind, first, tc.err)

			continue
		}

		seen[tc.kind] = tc.err
	}
}

// TestKindOf_CoversEverySentinel keeps the table above in step with the package.
// The expected set comes from the package source itself rather than a hardcoded
// count, so adding or removing a sentinel fails here until the table follows.
func TestKindOf_CoversEverySentinel(t *testing.T) {
	covered := make(map[string]bool, len(allSentinels))

	for _, tc := range allSentinels {
		covered[tc.name] = true

		if tc.kind == "" {
			t.Errorf("sentinel %s has an empty kind string", tc.name)
		}
	}

	for _, name := range declaredSentinels(t) {
		if !covered[name] {
			t.Errorf("%s is declared in the package but missing from allSentinels", name)
		}

		delete(covered, name)
	}

	for name := range covered {
		t.Errorf("allSentinels lists %s, which the package no longer declares", name)
	}
}

// declaredSentinels parses the package's own (non-test) source and returns the
// name of every exported package-level Err* variable. The test binary runs with
// the package directory as its working directory, so "*.go" is that package.
func declaredSentinels(t *testing.T) []string {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package sources: %v", err)
	}

	fset := token.NewFileSet()

	var names []string

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		for _, decl := range parsed.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.VAR {
				continue
			}

			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for _, ident := range valueSpec.Names {
					if strings.HasPrefix(ident.Name, "Err") {
						names = append(names, ident.Name)
					}
				}
			}
		}
	}

	// A bad working directory or a failed glob would otherwise pass silently.
	if len(names) == 0 {
		t.Fatal("found no exported Err* variables in the package source")
	}

	return names
}
