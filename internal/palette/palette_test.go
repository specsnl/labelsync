package palette

import (
	"math"
	"slices"
	"testing"

	"github.com/lucasb-eyer/go-colorful"
)

// identical is the distance below which two colours are the same colour. The
// CIEDE2000 formula routes a colour through CIELAB and back through a handful of
// square roots and trigonometric calls, so a colour compared against itself
// scores a few multiples of the float64 epsilon rather than a clean zero.
const identical = 1e-12

// githubDefaults is the label set a fresh GitHub repository ships with, black
// and white added. It stands in for "colours already present" in the tests that
// want a realistic used set rather than a constructed one.
var githubDefaults = []string{
	"d73a4a", "0075ca", "cfd3d7", "a2eeef", "7057ff",
	"008672", "e4e669", "d876e3", "ffffff", "000000",
}

// colors parses hex values in GitHub's form — no leading # — into colours.
func colors(t *testing.T, hexes ...string) []colorful.Color {
	t.Helper()

	out := make([]colorful.Color, 0, len(hexes))

	for _, hex := range hexes {
		c, err := colorful.Hex("#" + hex)
		if err != nil {
			t.Fatalf("colorful.Hex(%q) = %v, want a colour", "#"+hex, err)
		}

		out = append(out, c)
	}

	return out
}

// allCandidateColors is the whole grid as a used set: the input that leaves
// Allocate nothing to pick that is not already taken.
func allCandidateColors() []colorful.Color {
	all := candidates()
	out := make([]colorful.Color, 0, len(all))

	for _, c := range all {
		out = append(out, c.Color)
	}

	return out
}

// TestAllocate_Deterministic is the guarantee the package exists to provide:
// re-running labelsync must not churn colours, so the same input has to produce
// the same allocation every time.
func TestAllocate_Deterministic(t *testing.T) {
	used := colors(t, githubDefaults...)
	reserved := colors(t, "1f77b4", "ff7f0e")

	first := Allocate(used, reserved)

	for i := range 5 {
		if got := Allocate(used, reserved); got != first {
			t.Fatalf("Allocate() call %d = %+v, want %+v", i+2, got, first)
		}
	}
}

// TestAllocate_DoesNotMutateItsInput guards the caller's slices: a plan holds a
// used set across many allocations, and an allocator that appended into it would
// corrupt every later call.
func TestAllocate_DoesNotMutateItsInput(t *testing.T) {
	used := colors(t, githubDefaults...)
	reserved := colors(t, "1f77b4", "ff7f0e")

	usedBefore := append([]colorful.Color(nil), used...)
	reservedBefore := append([]colorful.Color(nil), reserved...)

	Allocate(used, reserved)

	for i := range usedBefore {
		if used[i] != usedBefore[i] {
			t.Errorf("used[%d] = %v after Allocate, was %v", i, used[i], usedBefore[i])
		}
	}

	for i := range reservedBefore {
		if reserved[i] != reservedBefore[i] {
			t.Errorf("reserved[%d] = %v after Allocate, was %v", i, reserved[i], reservedBefore[i])
		}
	}
}

// TestAllocate_MaximisesTheMinimumDistance checks the rule itself: no candidate
// is further from everything in use than the one Allocate returned.
func TestAllocate_MaximisesTheMinimumDistance(t *testing.T) {
	used := colors(t, githubDefaults...)
	reserved := colors(t, "1f77b4")

	got := Allocate(used, reserved)

	for _, c := range candidates() {
		score := nearestDistance(c.Color, used, reserved)

		if score > got.Distance {
			t.Errorf("candidate %q scores %.4f, beating the allocated %q at %.4f",
				c.Hex, score, got.Hex, got.Distance)
		}
	}
}

// TestAllocate_ReportsTheScoreItPicked pins Distance to the returned colour, so
// a caller reporting the score is reporting the truth about that colour.
func TestAllocate_ReportsTheScoreItPicked(t *testing.T) {
	used := colors(t, githubDefaults...)

	got := Allocate(used, nil)

	if want := nearestDistance(got.Color, used, nil); got.Distance != want {
		t.Errorf("Allocate().Distance = %.6f, want %.6f — the distance from %q to the nearest used colour",
			got.Distance, want, got.Hex)
	}
}

// TestAllocate_TiesBreakOnLowestHex is determinism's second leg. With nothing in
// use every candidate is equally (infinitely) distant, so the strict > has to
// leave the first candidate standing — and the grid is sorted ascending.
func TestAllocate_TiesBreakOnLowestHex(t *testing.T) {
	got := Allocate(nil, nil)

	if want := candidates()[0]; got.Hex != want.Hex {
		t.Errorf("Allocate(nil, nil).Hex = %q, want %q — the lowest hex in the grid", got.Hex, want.Hex)
	}

	if !math.IsInf(got.Distance, 1) {
		t.Errorf("Allocate(nil, nil).Distance = %v, want +Inf — nothing is present to be distant from", got.Distance)
	}

	if got.Exhausted {
		t.Error("Allocate(nil, nil).Exhausted = true, want false — the whole grid is available")
	}
}

// TestAllocate_UsedAndReservedAreEquivalent documents that the split is the
// caller's bookkeeping and nothing else: moving a colour between the two sets
// cannot change the outcome.
func TestAllocate_UsedAndReservedAreEquivalent(t *testing.T) {
	all := colors(t, githubDefaults...)

	tests := map[string]struct {
		used, reserved []colorful.Color
	}{
		"all used":      {used: all},
		"all reserved":  {reserved: all},
		"split in half": {used: all[:len(all)/2], reserved: all[len(all)/2:]},
		"swapped halves": {
			used:     all[len(all)/2:],
			reserved: all[:len(all)/2],
		},
	}

	want := Allocate(all, nil)

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Allocate(tc.used, tc.reserved); got != want {
				t.Errorf("Allocate() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestAllocate_SquattersNeverCollide walks the contract the caller has to keep:
// add each allocation to used before the next call, and no two labels can be
// handed the same colour. The grid holds 300-odd colours; 64 is well inside it.
func TestAllocate_SquattersNeverCollide(t *testing.T) {
	const squatters = 64

	used := colors(t, githubDefaults...)
	seen := make(map[string]int, squatters)

	for i := range squatters {
		got := Allocate(used, nil)

		if first, dup := seen[got.Hex]; dup {
			t.Fatalf("squatter %d was allocated %q, already given to squatter %d", i, got.Hex, first)
		}

		seen[got.Hex] = i
		used = append(used, got.Color)
	}
}

// TestAllocate_SquattersWithoutTheContractCollide is the other half: the
// duplicate the contract prevents is real, not theoretical. Allocate is
// stateless, so a caller that forgets to grow used gets the same colour twice.
func TestAllocate_SquattersWithoutTheContractCollide(t *testing.T) {
	used := colors(t, githubDefaults...)

	if first, second := Allocate(used, nil), Allocate(used, nil); first != second {
		t.Errorf("two allocations against an unchanged used set = %+v and %+v, want identical", first, second)
	}
}

// TestAllocate_AvoidsColoursAlreadyPresent is the point of the whole exercise: a
// squatter is recoloured to get out of the way, so the colour it is given must
// not be one that is already there.
func TestAllocate_AvoidsColoursAlreadyPresent(t *testing.T) {
	// Every candidate but one is taken, so there is exactly one right answer and
	// only the rule can find it.
	all := candidates()
	want := all[len(all)/3]

	used := make([]colorful.Color, 0, len(all)-1)

	for _, c := range all {
		if c.Hex != want.Hex {
			used = append(used, c.Color)
		}
	}

	got := Allocate(used, nil)

	if got.Hex != want.Hex {
		t.Errorf("Allocate() = %q, want %q — the only candidate not already in use", got.Hex, want.Hex)
	}

	// Exhausted is deliberately not asserted here: the one free candidate has grid
	// neighbours in use, and neighbouring grid points sit below the floor by
	// construction. That is the exhaustion warning doing its job, not a failure —
	// the colour is still the only free one, and it is still returned.
	if got.Distance <= identical {
		t.Errorf("Allocate().Distance = %.6g, want a real distance — %q is the one colour not in use", got.Distance, got.Hex)
	}
}

// TestAllocate_ExhaustionReturnsAColourAnyway is the never-fail guarantee. With
// the entire grid in use every candidate scores 0, and Allocate still has to
// return a usable colour — a suboptimal colour beats an aborted sync.
func TestAllocate_ExhaustionReturnsAColourAnyway(t *testing.T) {
	got := Allocate(allCandidateColors(), nil)

	if !got.Exhausted {
		t.Error("Allocate().Exhausted = false, want true — every candidate is already in use")
	}

	if got.Distance > identical {
		t.Errorf("Allocate().Distance = %.6g, want ~0 — every candidate is an exact match for a used colour", got.Distance)
	}

	if !hexPattern.MatchString(got.Hex) {
		t.Errorf("Allocate().Hex = %q, want a usable colour despite exhaustion", got.Hex)
	}

	if !slices.ContainsFunc(candidates(), func(c candidate) bool { return c.Hex == got.Hex }) {
		t.Errorf("Allocate().Hex = %q, want a colour from the grid", got.Hex)
	}
}

// TestAllocate_ExhaustedTracksTheFloor pins Exhausted to exhaustionFloor rather
// than to "distance happens to be 0", and pins the boundary: at exactly the
// floor the allocation is still good enough.
func TestAllocate_ExhaustedTracksTheFloor(t *testing.T) {
	tests := map[string]struct {
		used []colorful.Color
		want bool
	}{
		"empty grid in use": {used: nil, want: false},
		"a realistic repo":  {used: colors(t, githubDefaults...), want: false},
		"whole grid in use": {used: allCandidateColors(), want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Allocate(tc.used, nil)

			if got.Exhausted != tc.want {
				t.Errorf("Allocate().Exhausted = %t at distance %.4f (floor %.2f), want %t",
					got.Exhausted, got.Distance, exhaustionFloor, tc.want)
			}

			if want := got.Distance < exhaustionFloor; got.Exhausted != want {
				t.Errorf("Allocate().Exhausted = %t, want %t — Exhausted must mean Distance < exhaustionFloor",
					got.Exhausted, want)
			}
		})
	}
}

// TestAllocate_RespectsLegibilityBounds is the guarantee the candidate grid
// carries, restated at the only place it reaches a label: whatever Allocate
// hands out is a colour GitHub can put legible text on.
func TestAllocate_RespectsLegibilityBounds(t *testing.T) {
	used := colors(t, githubDefaults...)

	// Grown as it goes, so the bound is checked at every density from an almost
	// empty repository to a crowded one.
	for i := range 64 {
		got := Allocate(used, nil)

		_, s, l := got.Color.Hsl()

		if l < minLightness-quantisation || l > maxLightness+quantisation {
			t.Errorf("allocation %d (%q) has lightness %.4f, want within [%.2f, %.2f] ±%.4f",
				i, got.Hex, l, minLightness, maxLightness, quantisation)
		}

		if lowest := saturations[0]; s < lowest-quantisation {
			t.Errorf("allocation %d (%q) has saturation %.4f, want at least %.2f ±%.4f",
				i, got.Hex, s, lowest, quantisation)
		}

		used = append(used, got.Color)
	}
}

// TestAllocate_ColorAndHexAgree keeps the two forms in step: a caller that feeds
// Color back into used on the next call must be feeding back the colour whose
// hex it just applied.
func TestAllocate_ColorAndHexAgree(t *testing.T) {
	got := Allocate(colors(t, githubDefaults...), nil)

	if hex := hexOf(got.Color); hex != got.Hex {
		t.Errorf("hexOf(Allocate().Color) = %q, want %q", hex, got.Hex)
	}
}

// TestNearestDistance covers the helper's own edges, which the Allocate tests
// only reach indirectly.
func TestNearestDistance(t *testing.T) {
	red := colors(t, "ff0000")[0]

	tests := map[string]struct {
		used, reserved []colorful.Color
		want           float64
	}{
		"nothing present":         {want: math.Inf(1)},
		"exact match in used":     {used: colors(t, "ff0000"), want: 0},
		"exact match in reserved": {reserved: colors(t, "ff0000"), want: 0},
		"nearest wins across the sets": {
			used:     colors(t, "00ff00"),
			reserved: colors(t, "ff0000", "0000ff"),
			want:     0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := nearestDistance(red, tc.used, tc.reserved)

			if math.Abs(got-tc.want) > identical {
				t.Errorf("nearestDistance() = %v, want %v", got, tc.want)
			}
		})
	}
}
