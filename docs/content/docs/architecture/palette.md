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

| Test                             | Guards                                                       |
|----------------------------------|--------------------------------------------------------------|
| repeated calls agree             | The determinism guarantee itself                             |
| a caller mutating its slice      | That `candidates()` copies                                   |
| strictly ascending, no duplicate | Sort order and deduplication                                 |
| lightness and saturation bounds  | Legibility — the documented `0.35 … 0.65` window             |
| hex matches `^[0-9a-f]{6}$`      | The wire form GitHub accepts                                 |
| `Color` round-trips to `Hex`     | That distance is measured from the colour GitHub stores      |
| every hue present, size floor    | Loop bounds — an off-by-one would silently drop a hue range  |

A tolerance of `1/255` is allowed on the lightness and saturation assertions: the round trip through
24-bit hex moves each channel by up to one step, and HSL lightness is the midpoint of the largest
and smallest channel. The tolerance absorbs rounding; it is not headroom for a wider grid.

## Still to come

`Allocate()` — the rule that picks from the grid — is the other half of this package. A displaced
label is reassigned the candidate with the **maximum minimum perceptual distance** (CIEDE2000 in
CIELAB space) from every colour currently in use: the colour most different from everything else
present. If every candidate is within a minimum-distance floor of an existing colour, it returns the
best available anyway and the action carries a warning, rather than failing the run — a suboptimal
colour beats an aborted sync.

The rule and its exhaustion behaviour are specified in
[design.md § Colour allocation](https://github.com/specsnl/labelsync/blob/main/docs/design.md#colour-allocation)
and move here as they land.
