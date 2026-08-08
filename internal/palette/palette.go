package palette

import (
	"math"

	"github.com/lucasb-eyer/go-colorful"
)

// exhaustionFloor is the minimum perceptual distance an allocation has to reach
// before the grid counts as exhausted.
//
// The unit is go-colorful's: DistanceCIEDE2000 scales the standard ΔE2000 by
// 0.01, so 0.05 here is ΔE2000 ≈ 5. One ΔE2000 is a just-noticeable difference
// under ideal viewing conditions; a label swatch is small and sits in a list of
// other swatches, so the floor is set several times that — below it, two labels
// read as "the same colour, roughly" to someone scanning the list, which is the
// thing recolouring a squatter was supposed to fix.
//
// Falling below the floor does not fail the allocation. It only marks the result
// [Allocation.Exhausted], so the caller can carry a warning in the action's
// reason: a suboptimal colour beats an aborted sync.
const exhaustionFloor = 0.05

// Allocation is one colour picked from the candidate grid, with what the caller
// needs to report the choice.
type Allocation struct {
	// Hex is the colour as GitHub stores it: six lower-case hex digits, no
	// leading #. This is the value to send to the label API.
	Hex string

	// Color is Hex as a colour, ready to be passed straight back into a later
	// Allocate call's used set.
	Color colorful.Color

	// Distance is the allocation's score: the perceptual distance from the
	// nearest colour in used or reserved, in go-colorful's CIEDE2000 unit
	// (ΔE2000 × 0.01). It is +Inf when both sets are empty, since there is then
	// nothing to be distant from.
	Distance float64

	// Exhausted reports that Distance fell below the exhaustion floor: every
	// candidate is perceptually close to a colour already present, and the
	// allocation below is merely the least bad one. The colour is still valid
	// and still safe to apply — this is a warning, not a failure.
	Exhausted bool
}

// Allocate picks the candidate colour with the maximum minimum perceptual
// distance (CIEDE2000 in CIELAB space) from every colour in used and reserved —
// the colour most different from everything else present.
//
// used and reserved are treated identically; the split is the caller's
// bookkeeping. used is what is on the labels today plus whatever this run has
// already allocated, reserved the colours configured labels are about to claim.
//
// # Contract
//
// Allocate is stateless, so it cannot know about a colour it handed out a moment
// ago: **the caller must add each allocated colour to used before the next
// call.** Without that, two squatters allocated against the same used set are
// handed the same colour — the grid is fixed and the rule is deterministic, so
// identical input necessarily produces identical output.
//
// Ties break first-wins, via a strict >, and the grid is sorted ascending by
// hex, so equally distant candidates resolve to the lowest hex value. That is
// what makes a second run a no-op rather than a reshuffle.
//
// Allocate never fails. When every candidate sits within [exhaustionFloor] of an
// existing colour it returns the best available anyway, with
// [Allocation.Exhausted] set.
func Allocate(used, reserved []colorful.Color) Allocation {
	var best candidate

	// Below every real score, so the first candidate always wins and the grid's
	// order decides. A score is a distance and cannot be negative.
	bestScore := -1.0

	for _, c := range candidates() {
		score := nearestDistance(c.Color, used, reserved)

		// Strict, so the first candidate at a given score keeps it: the grid is
		// sorted ascending, so ties go to the lowest hex value.
		if score > bestScore {
			best, bestScore = c, score
		}
	}

	return Allocation{
		Hex:       best.Hex,
		Color:     best.Color,
		Distance:  bestScore,
		Exhausted: bestScore < exhaustionFloor,
	}
}

// nearestDistance returns the perceptual distance from c to the closest colour
// in used or reserved, or +Inf when both are empty.
//
// The two sets are walked in place rather than concatenated: Allocate calls this
// once per candidate, and building a joined slice 300-odd times per label would
// allocate for nothing.
func nearestDistance(c colorful.Color, used, reserved []colorful.Color) float64 {
	nearest := math.Inf(1)

	for _, set := range [][]colorful.Color{used, reserved} {
		for _, other := range set {
			if d := c.DistanceCIEDE2000(other); d < nearest {
				nearest = d
			}
		}
	}

	return nearest
}
