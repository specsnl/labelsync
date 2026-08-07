package exit_test

import (
	"testing"

	"github.com/specsnl/labelsync/internal/util/exit"
)

// The codes are a public contract: CI pipelines branch on the numbers, so a
// renumbering is a breaking change and has to fail here rather than in a
// pipeline. Each entry is spelled out literally on purpose — asserting
// exit.OK == exit.OK would test nothing.
func TestCodes_AreStable(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"OK", exit.OK, 0},
		{"Error", exit.Error, 1},
		{"Drift", exit.Drift, 2},
		{"Skipped", exit.Skipped, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("exit.%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestCodes_AreDistinct(t *testing.T) {
	seen := map[int]string{}

	for _, tc := range []struct {
		name string
		code int
	}{
		{"OK", exit.OK},
		{"Error", exit.Error},
		{"Drift", exit.Drift},
		{"Skipped", exit.Skipped},
	} {
		if other, dup := seen[tc.code]; dup {
			t.Errorf("exit.%s and exit.%s both equal %d", other, tc.name, tc.code)
		}

		seen[tc.code] = tc.name
	}
}
