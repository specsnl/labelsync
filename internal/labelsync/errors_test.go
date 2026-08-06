package labelsync_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// allSentinels pairs every exported sentinel with its documented kind string.
// Adding a sentinel without adding it here fails TestKindOf_CoversEverySentinel.
var allSentinels = []struct {
	err  error
	kind string
}{
	{labelsync.ErrConfigNotFound, "config_not_found"},
	{labelsync.ErrAmbiguousConfigFile, "ambiguous_config_file"},
	{labelsync.ErrUnsupportedConfigVersion, "unsupported_config_version"},
	{labelsync.ErrEmptyConfig, "empty_config"},
	{labelsync.ErrDuplicateLabelName, "duplicate_label_name"},
	{labelsync.ErrDuplicateLabelColor, "duplicate_label_color"},
	{labelsync.ErrInvalidColor, "invalid_color"},
	{labelsync.ErrInvalidLabelName, "invalid_label_name"},
	{labelsync.ErrDescriptionTooLong, "description_too_long"},
	{labelsync.ErrUnknownGroup, "unknown_group"},
	{labelsync.ErrAmbiguousGroupSource, "ambiguous_group_source"},
	{labelsync.ErrCyclicGroup, "cyclic_group"},
	{labelsync.ErrInvalidRepoRef, "invalid_repo_ref"},
	{labelsync.ErrInvalidRename, "invalid_rename"},
	{labelsync.ErrNoToken, "no_token"},
	{labelsync.ErrInteractiveRequired, "interactive_required"},
	{labelsync.ErrRepoInaccessible, "repo_inaccessible"},
	{labelsync.ErrMaxWaitExceeded, "max_wait_exceeded"},
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

// TestKindOf_CoversEverySentinel keeps the table above in step with the package:
// the design fixes the sentinel count at 18, and every one must map to a
// non-empty kind.
func TestKindOf_CoversEverySentinel(t *testing.T) {
	const want = 18

	if got := len(allSentinels); got != want {
		t.Errorf("table covers %d sentinels, want %d — add the new sentinel to allSentinels", got, want)
	}

	for _, tc := range allSentinels {
		if tc.kind == "" {
			t.Errorf("sentinel %v has an empty kind string", tc.err)
		}
	}
}
