// Package palette owns colour allocation: the fixed grid of candidate colours a
// displaced label may be moved to, and the rule that picks one of them.
//
// It takes colours and returns colours. It imports neither internal/config nor
// internal/github, which is what keeps the interesting part — determinism —
// testable without a config file or an HTTP mock.
//
// # Determinism
//
// Re-running labelsync must not churn colours, and that guarantee starts here:
// the candidate grid is generated in a fixed order, deduplicated, and sorted
// ascending by hex, so the same input always produces the same allocation. A
// caller that breaks ties on first-wins therefore always lands on the lowest hex
// value.
package palette

import (
	"slices"
	"strings"
	"sync"

	"github.com/lucasb-eyer/go-colorful"
)

// The HSL grid. 36 hues × 3 saturations × 3 lightnesses = 324 points, before
// deduplication collapses the ones that round to the same 24-bit hex.
const (
	hueStep  = 10.0
	hueCount = 36 // 0, 10, … 350

	// minLightness and maxLightness bound the grid away from white and black.
	//
	// GitHub does not store a label's text colour: it derives it from the
	// background luminance, black on a light background and white on a dark one.
	// The choice is a step function, so backgrounds close to either extreme land
	// on the near-side text colour and render as low-contrast — near-white
	// backgrounds get black text that is legible but visually washed out, and
	// near-black ones get white text on a swatch indistinguishable from its
	// neighbours. Neither is something a user asked for, and both are avoidable
	// by simply never generating those colours.
	//
	// Configured labels are unaffected: a colour written in labels.yml is used
	// exactly as written. These bounds constrain only the colours labelsync picks
	// on a user's behalf.
	minLightness = 0.35
	maxLightness = 0.65
)

// saturations and lightnesses are the grid's other two axes. Saturation stays
// clear of 0 so no candidate is a shade of grey, and clear of 1 so the set does
// not read as a row of neon.
var (
	saturations = []float64{0.45, 0.65, 0.85}
	lightnesses = []float64{minLightness, 0.50, maxLightness}
)

// candidate is one colour in the grid, in both of the forms callers need.
type candidate struct {
	// Hex is the colour as GitHub stores it: six lower-case hex digits, no
	// leading #. It is also the sort key and the deduplication key.
	Hex string

	// Color is Hex parsed back into a colour, not the HSL point that generated
	// it. Hex is 8 bits per channel and the grid is not, so the two differ
	// slightly — and it is Hex that GitHub will store, so it is Hex that
	// perceptual distance has to be measured from. Measuring from the unrounded
	// HSL point would score a colour the tool never actually applies.
	Color colorful.Color
}

// candidates returns the grid: deduplicated, sorted ascending by hex.
//
// The result is generated once and returned as a copy, so a caller cannot
// perturb the ordering that every allocation depends on.
func candidates() []candidate {
	return slices.Clone(grid())
}

// grid builds the candidate set. Wrapped in sync.OnceValue because it is a
// constant that happens to be computed: the result cannot change between calls,
// and Allocate walks the whole set for every label it places.
var grid = sync.OnceValue(func() []candidate {
	found := make([]candidate, 0, hueCount*len(saturations)*len(lightnesses))
	seen := make(map[string]bool, cap(found))

	for h := range hueCount {
		for _, s := range saturations {
			for _, l := range lightnesses {
				hex := hexOf(colorful.Hsl(float64(h)*hueStep, s, l))

				if seen[hex] {
					continue
				}

				seen[hex] = true

				// Parsed rather than reused: see candidate.Color. Hsl produces
				// in-gamut colours for this grid, so the hex above is always
				// valid and the error cannot fire.
				parsed, err := colorful.Hex("#" + hex)
				if err != nil {
					panic("palette: generated an unparseable hex value " + hex)
				}

				found = append(found, candidate{Hex: hex, Color: parsed})
			}
		}
	}

	// Fixed-width lower-case hex, so byte order is numeric order.
	slices.SortFunc(found, func(a, b candidate) int {
		return strings.Compare(a.Hex, b.Hex)
	})

	return found
})

// hexOf renders a colour the way GitHub's API wants it: six lower-case hex
// digits with no leading #. colorful.Hex includes the #, which no field in the
// label API accepts.
func hexOf(c colorful.Color) string {
	return c.Clamped().Hex()[1:]
}
