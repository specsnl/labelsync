package palette

import (
	"regexp"
	"slices"
	"testing"

	"github.com/lucasb-eyer/go-colorful"
)

// hexPattern is GitHub's label colour form: six lower-case hex digits, no
// leading #.
var hexPattern = regexp.MustCompile(`^[0-9a-f]{6}$`)

// quantisation is the largest lightness shift a round trip through 24-bit hex
// can introduce. Each channel moves by at most 1/255, and HSL lightness is the
// midpoint of the largest and smallest channel, so the midpoint moves by at most
// 1/255 as well. The bound is deliberately loose — it is here to absorb rounding,
// not to leave room for a wider grid.
const quantisation = 1.0 / 255.0

// TestCandidates_Deterministic is the guarantee the whole package rests on: two
// calls agree element for element, so re-running labelsync cannot churn colours.
func TestCandidates_Deterministic(t *testing.T) {
	first, second := candidates(), candidates()

	if len(first) != len(second) {
		t.Fatalf("len(candidates()) = %d then %d, want the same set both times", len(first), len(second))
	}

	for i := range first {
		if first[i] != second[i] {
			t.Errorf("candidates()[%d] = %+v then %+v, want identical", i, first[i], second[i])
		}
	}
}

// TestCandidates_CallerCannotPerturbOrdering guards the copy in candidates().
// Handing out the shared grid would let one caller reorder every later
// allocation, which is exactly the churn determinism is supposed to rule out.
func TestCandidates_CallerCannotPerturbOrdering(t *testing.T) {
	before := candidates()

	scrambled := candidates()
	slices.Reverse(scrambled)

	after := candidates()

	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("candidates()[%d] = %+v before a caller mutated its slice, %+v after", i, before[i], after[i])
		}
	}
}

func TestCandidates_SortedAscendingByHex(t *testing.T) {
	got := candidates()

	for i := 1; i < len(got); i++ {
		if got[i-1].Hex >= got[i].Hex {
			t.Errorf("candidates()[%d..%d] = %q, %q, want strictly ascending", i-1, i, got[i-1].Hex, got[i].Hex)
		}
	}
}

func TestCandidates_NoDuplicates(t *testing.T) {
	seen := make(map[string]int, len(candidates()))

	for i, c := range candidates() {
		if first, dup := seen[c.Hex]; dup {
			t.Errorf("hex %q appears at both index %d and %d", c.Hex, first, i)

			continue
		}

		seen[c.Hex] = i
	}
}

// TestCandidates_LightnessWithinBounds is the legibility guarantee: GitHub
// derives label text colour from background luminance, so a candidate outside
// these bounds would render at poor contrast whatever text colour it got.
func TestCandidates_LightnessWithinBounds(t *testing.T) {
	for _, c := range candidates() {
		_, _, l := c.Color.Hsl()

		if l < minLightness-quantisation || l > maxLightness+quantisation {
			t.Errorf("candidate %q has lightness %.4f, want within [%.2f, %.2f] ±%.4f",
				c.Hex, l, minLightness, maxLightness, quantisation)
		}
	}
}

// TestCandidates_SaturationWithinBounds pairs with the lightness test: the grid
// deliberately contains no greys, so no label is ever recoloured to something
// that reads as "unset".
func TestCandidates_SaturationWithinBounds(t *testing.T) {
	lowest := slices.Min(saturations)

	for _, c := range candidates() {
		_, s, _ := c.Color.Hsl()

		if s < lowest-quantisation {
			t.Errorf("candidate %q has saturation %.4f, want at least %.2f ±%.4f",
				c.Hex, s, lowest, quantisation)
		}
	}
}

func TestCandidates_HexFormIsWhatGitHubWants(t *testing.T) {
	for _, c := range candidates() {
		if !hexPattern.MatchString(c.Hex) {
			t.Errorf("candidate hex %q does not match %s", c.Hex, hexPattern)
		}
	}
}

// TestCandidates_ColorRoundTripsFromHex is what makes perceptual distance
// honest: Color has to be the colour GitHub will store, not the unrounded HSL
// point that generated it.
func TestCandidates_ColorRoundTripsFromHex(t *testing.T) {
	for _, c := range candidates() {
		if got := hexOf(c.Color); got != c.Hex {
			t.Errorf("hexOf(candidate{%q}.Color) = %q, want %q", c.Hex, got, c.Hex)
		}
	}
}

// TestCandidates_Size checks the grid is the size the design describes: 324
// points, minus whatever collapsed to a shared hex. A set that suddenly halved
// would still pass every property above.
func TestCandidates_Size(t *testing.T) {
	const points = hueCount * 3 * 3 // 36 hues × 3 saturations × 3 lightnesses

	if got := len(saturations) * len(lightnesses) * hueCount; got != points {
		t.Fatalf("the grid axes describe %d points, want %d", got, points)
	}

	got := len(candidates())

	// All 324 points survive deduplication today. The assertion is a floor rather
	// than that exact number because whether two grid points round to the same
	// hex is a property of colorful's HSL conversion, and pinning it would turn a
	// dependency upgrade into a failing test for no reason.
	if got < points*9/10 || got > points {
		t.Errorf("len(candidates()) = %d, want between %d and %d", got, points*9/10, points)
	}
}

// TestCandidates_CoversEveryHue guards the loop bounds: an off-by-one in the hue
// range would quietly drop a slice of the colour wheel.
func TestCandidates_CoversEveryHue(t *testing.T) {
	seen := make(map[int]bool, hueCount)

	for _, c := range candidates() {
		h, _, _ := c.Color.Hsl()

		// Round to the nearest grid step; the round trip through hex nudges hue by
		// a fraction of a degree.
		step := int((h + hueStep/2) / hueStep) % hueCount
		seen[step] = true
	}

	for h := range hueCount {
		if !seen[h] {
			t.Errorf("no candidate at hue %d°", h*int(hueStep))
		}
	}
}

// TestCandidates_ContainsGeneratedPoint pins the generation rule itself: one
// known HSL point has to be present, in the hex form GitHub stores.
func TestCandidates_ContainsGeneratedPoint(t *testing.T) {
	want := hexOf(colorful.Hsl(0, 0.65, 0.50))

	found := slices.ContainsFunc(candidates(), func(c candidate) bool {
		return c.Hex == want
	})

	if !found {
		t.Errorf("candidates() does not contain hsl(0, 0.65, 0.50) = %q", want)
	}
}
