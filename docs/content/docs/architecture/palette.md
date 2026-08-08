---
title: Colour Palette
weight: 5
---

`internal/palette` answers one question: when labelsync has to give a label a colour nobody asked
for, which colour does it pick?

That happens in exactly one situation. Configured labels are the authoritative owners of their
colours, so when an unconfigured label is squatting on a colour a configured label wants, the
squatter is recoloured. A colour written in `labels.yml` is always used exactly as written — this
package never touches it.

The choice is made from a **fixed candidate grid**, and the grid is the reason a second run of
`labelsync sync` is a no-op rather than a reshuffle.

## The candidate grid

`candidates.go` generates a deterministic HSL grid — 36 hues × 3 saturations × 3 lightnesses:

| Axis       | Values                 | Count |
|------------|------------------------|-------|
| Hue        | `0`, `10`, … `350`     | 36    |
| Saturation | `0.45`, `0.65`, `0.85` | 3     |
| Lightness  | `0.35`, `0.50`, `0.65` | 3     |

324 points, deduplicated by hex, sorted ascending by hex. All 324 survive deduplication with the
current `go-colorful`, but two grid points rounding to the same 24-bit value is a property of the
HSL conversion rather than a guarantee, so the set is deduplicated and its size asserted as a floor.

Colours come from [`github.com/lucasb-eyer/go-colorful`](https://github.com/lucasb-eyer/go-colorful),
which provides `Hsl()`, `Hex()`, and `DistanceCIEDE2000()` directly. It was already an indirect
dependency via lipgloss, so promoting it to a direct one costs nothing in the dependency tree.

### Why lightness is bounded away from the extremes

GitHub does not store a label's text colour. It derives it from the background luminance: black text
on a light background, white text on a dark one. The choice is a step function, so a background
close to either extreme lands on the near-side text colour and renders at poor contrast — near-white
backgrounds get black text that is legible but washed out, and near-black ones get white text on a
swatch indistinguishable from its neighbours.

Neither outcome is one a user asked for, and both are avoidable by never generating those colours in
the first place. `0.35 … 0.65` is the bound; the tests assert every candidate sits inside it.

Saturation is bounded the same way and for the same kind of reason: away from `0`, so no candidate
reads as a grey "unset" label, and away from `1`, so the set does not read as a row of neon.

### Each candidate carries both forms

```go
type candidate struct {
    Hex   string          // "1f77b4" — six lower-case digits, no leading #
    Color colorful.Color  // Hex parsed back, not the HSL point that generated it
}
```

`Hex` is the form GitHub's label API accepts — `colorful.Hex()` includes a leading `#`, which no
field in that API takes, so it is stripped.

`Color` is deliberately the colour *parsed back from* `Hex`, not the HSL point that produced it. Hex
is 8 bits per channel and the grid is not, so the two differ slightly, and it is the hex value that
GitHub will store. Measuring perceptual distance from the unrounded HSL point would score a colour
the tool never actually applies.

## The allocation rule

`palette.go` picks from the grid. A displaced label is reassigned the candidate with the **maximum
minimum perceptual distance** (CIEDE2000 in CIELAB space) from every colour in play — the colour
most different from everything else present:

```go
func Allocate(used, reserved []colorful.Color) Allocation
```

`used` and `reserved` are treated identically; the split is the caller's bookkeeping. `used` is what
the labels carry today plus whatever this run has already allocated, `reserved` the colours
configured labels are about to claim. Moving a colour from one to the other cannot change the
outcome, and a test asserts that.

For every candidate, the score is the distance to the nearest colour in either set; the highest score
wins. Nothing in either set means every score is `+Inf` — a genuine tie across the whole grid, which
the tie-break resolves.

### The caller's contract

`Allocate` is stateless. It cannot know about a colour it handed out a moment ago, so **the caller
adds each allocated colour to `used` before the next call.** Skip that and two squatters allocated
against the same set get the same colour, necessarily: the grid is fixed and the rule is
deterministic, so identical input produces identical output. Both halves of this are tested — that
64 sequential allocations never collide when the contract is kept, and that two allocations against
an unchanged set are identical when it is not.

### What it returns

```go
type Allocation struct {
    Hex       string          // "1f77b4" — what to send to the label API
    Color     colorful.Color  // the same colour, ready to append to used
    Distance  float64         // the winning score; +Inf when nothing is in play
    Exhausted bool            // Distance fell below the exhaustion floor
}
```

`Distance` is in go-colorful's unit, which is **not** the textbook one: `DistanceCIEDE2000` scales
ΔE2000 by `0.01`, so `0.05` here is ΔE2000 ≈ 5.

### Exhaustion is a warning, never a failure

When every candidate sits within ΔE2000 ≈ 5 of a colour already present, the grid is exhausted:
whatever is left is close enough to something else that the two swatches read as the same colour at a
glance, which is the thing recolouring a squatter was supposed to fix.

`Allocate` returns the best available colour anyway and sets `Exhausted`, for the caller to carry as
a warning in the action's `Reason`. It has no error return — a suboptimal colour beats an aborted
sync.

The floor is ΔE2000 ≈ 5 rather than the ΔE2000 ≈ 1 just-noticeable difference because a label swatch
is small and sits in a list of other swatches. It is well clear of ordinary use: against a fresh
repository's ten default label colours, the grid still scores ΔE2000 ≈ 8.7 on the seventieth
consecutive allocation.

## Determinism

Re-running must not churn colours. Three things guarantee that, and the first two live here:

1. **Fixed generation order, then sorted by hex.** The grid is built by nested loops over constant
   axes and sorted ascending, so it is byte-identical on every call and in every process.
2. **Ties break first-wins.** The allocator compares scores with a strict `>`, so when two
   candidates are equally distant from everything in use, the lower hex value wins.
3. **Squatters are processed in ascending name order**, and each newly assigned colour joins the
   used set before the next allocation — so two squatters can never be handed the same colour.

Two implementation details protect the first point:

- The grid is computed once behind `sync.OnceValue`. It is a constant that happens to need
  computing, and the allocator walks the whole set for every label it places.
- `candidates()` returns a **copy**. Handing out the shared slice would let one caller reorder every
  later allocation, which is precisely the churn determinism exists to rule out.

## Testing

In-package tests, because the grid and its bounds are unexported. They assert properties rather than
a golden list, so the set can grow without a rewrite:

| Test                             | Guards                                                      |
|----------------------------------|-------------------------------------------------------------|
| repeated calls agree             | The determinism guarantee itself                            |
| a caller mutating its slice      | That `candidates()` copies                                  |
| strictly ascending, no duplicate | Sort order and deduplication                                |
| lightness and saturation bounds  | Legibility — the documented `0.35 … 0.65` window            |
| hex matches `^[0-9a-f]{6}$`      | The wire form GitHub accepts                                |
| `Color` round-trips to `Hex`     | That distance is measured from the colour GitHub stores     |
| every hue present, size floor    | Loop bounds — an off-by-one would silently drop a hue range |

And for the rule:

| Test                                | Guards                                                        |
|-------------------------------------|---------------------------------------------------------------|
| repeated `Allocate` calls agree     | Determinism at the level a caller sees it                     |
| no candidate scores higher          | The maximum-minimum rule itself                               |
| `Allocate(nil, nil)` is the first   | Tie-breaking, and `+Inf` when nothing is in play              |
| 64 sequential allocations, no reuse | The caller's contract, kept                                   |
| two allocations, unchanged `used`   | The collision the contract prevents is real                   |
| all but one candidate in use        | That the one free colour is the one found                     |
| the whole grid in use               | Exhaustion returns a usable colour and sets `Exhausted`       |
| `used` vs `reserved`, four splits   | That the two sets are equivalent                              |
| lightness and saturation, 64 deep   | Legibility, at every density from an empty repo to a busy one |
| input slices unchanged after a call | That a caller's `used` set survives being passed in           |

Two of those need a tolerance rather than an equality. A colour compared against itself scores a few
multiples of the float64 epsilon rather than a clean zero — CIEDE2000 routes through CIELAB and a
handful of square roots — so "already in use" is asserted as `≤ 1e-12`, not `== 0`. For the same
reason the exhaustion test does not assert *which* colour comes back when the whole grid ties at
zero: those ties are not bit-exact, so first-wins has nothing to resolve.

A tolerance of `1/255` is allowed on the lightness and saturation assertions: the round trip through
24-bit hex moves each channel by up to one step, and HSL lightness is the midpoint of the largest
and smallest channel. The tolerance absorbs rounding; it is not headroom for a wider grid.

## Still to come

Nothing in this package. What is missing is its caller: `internal/plan` decides *which* labels are
displaced, processes them in ascending name order, grows the `used` set as it goes, and turns an
`Exhausted` allocation into the warning that reaches the user. That is
[design.md § Reconciliation algorithm](https://github.com/specsnl/labelsync/blob/main/docs/design.md#reconciliation-algorithm).
