package exit_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
)

// The codes are a public contract: CI pipelines branch on the numbers, so a
// renumbering is a breaking change and has to fail here rather than in a
// pipeline. Each entry is spelled out literally on purpose — asserting
// exit.OK == exit.OK would test nothing.
func TestCodes_AreStable(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  exit.Code
		want exit.Code
	}{
		{"OK", exit.OK, 0},
		{"Error", exit.Error, 1},
		{"Drift", exit.Drift, 2},
		{"Skipped", exit.Skipped, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("exit.%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

// The outcome codes have to be single, non-overlapping bits or they cannot be
// OR'd: two outcomes sharing a bit would make 6 ambiguous, and an outcome with
// two bits set would collide with a combination of others.
func TestOutcomeCodes_AreDisjointSingleBits(t *testing.T) {
	var union exit.Code

	for _, tc := range []struct {
		name string
		code exit.Code
	}{
		{"Drift", exit.Drift},
		{"Skipped", exit.Skipped},
	} {
		if tc.code&(tc.code-1) != 0 {
			t.Errorf("exit.%s = %d has more than one bit set", tc.name, tc.code)
		}

		if union&tc.code != 0 {
			t.Errorf("exit.%s = %d overlaps an earlier outcome code", tc.name, tc.code)
		}

		union |= tc.code
	}

	// Error is not in the bit space: it must not be reachable by combining
	// outcomes, or a pipeline masking for failure would see one.
	if union&exit.Error != 0 {
		t.Errorf("outcome codes %d overlap exit.Error", union)
	}
}

// The combination the scheme exists for: a dry run that finds drift and also
// cannot reach a repository reports both, rather than the more alarming one.
func TestCodes_Combine(t *testing.T) {
	code := exit.OK
	code |= exit.Drift
	code |= exit.Skipped

	if code != 6 {
		t.Errorf("Drift|Skipped = %d, want 6", code)
	}

	if code&exit.Drift == 0 || code&exit.Skipped == 0 {
		t.Errorf("%d does not carry both outcomes", code)
	}
}

func TestOf(t *testing.T) {
	sentinel := fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible)

	for _, tc := range []struct {
		name string
		err  error
		want exit.Code
	}{
		{"nil is success", nil, exit.OK},
		{"a plain error is a failure", errors.New("boom"), exit.Error},
		{"a wrapped sentinel is a failure", sentinel, exit.Error},
		{"a carrier reports its code", &exit.Err{Code: exit.Drift}, exit.Drift},
		{"a carrier reports a combination", &exit.Err{Code: exit.Drift | exit.Skipped}, 6},
		{"a carrier with a failure", &exit.Err{Code: exit.Error, Err: sentinel}, exit.Error},
		// Left unset by mistake: a failed run must not exit zero, because that is
		// the one wrong answer a pipeline cannot detect.
		{"a carrier with no code but a failure", &exit.Err{Err: sentinel}, exit.Error},
		{"a wrapped carrier", fmt.Errorf("syncing: %w", &exit.Err{Code: exit.Skipped}), exit.Skipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exit.Of(tc.err); got != tc.want {
				t.Errorf("Of(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// A carrier must not hide the sentinel it wraps, or the error_kind field in JSON
// output would go missing for exactly the failures that carry a code.
func TestErr_UnwrapsToTheSentinel(t *testing.T) {
	err := error(&exit.Err{
		Code: exit.Error,
		Err:  fmt.Errorf("%w: specsnl/old-thing", labelsync.ErrRepoInaccessible),
	})

	if !errors.Is(err, labelsync.ErrRepoInaccessible) {
		t.Error("errors.Is could not see through the carrier")
	}

	if kind := labelsync.KindOf(err); kind != "repo_inaccessible" {
		t.Errorf("KindOf = %q, want %q", kind, "repo_inaccessible")
	}
}

// A silent carrier still has to satisfy error, because it travels as one. The
// message is a fallback for a caller that formats it directly; main prints
// nothing for this case.
func TestErr_Error(t *testing.T) {
	if got := (&exit.Err{Code: exit.Drift}).Error(); got != "exit code 2" {
		t.Errorf("Error() = %q, want %q", got, "exit code 2")
	}

	wrapped := &exit.Err{Code: exit.Error, Err: errors.New("boom")}
	if got := wrapped.Error(); got != "boom" {
		t.Errorf("Error() = %q, want %q", got, "boom")
	}
}
