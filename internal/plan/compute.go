// compute.go holds the reconciler itself: the pure function that turns one
// repository's configured labels and its current labels into the ordered list
// of actions that converges the second onto the first.
//
// Nothing here does I/O. The remote half of the input is [Label], a plain struct
// this package declares itself rather than the client's label type, which is
// what keeps internal/plan free of internal/github — the interesting logic is
// testable with two slices and no HTTP mock.
package plan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lucasb-eyer/go-colorful"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/palette"
)

// Mode selects how far reconciliation goes. Append is additive and never
// deletes; prune makes the repository match the config exactly.
type Mode string

// The two reconciliation modes.
const (
	ModeAppend Mode = "append"
	ModePrune  Mode = "prune"
)

// Label is a label as it exists in a repository today.
//
// It is deliberately not the GitHub client's type: the planner takes plain
// structs and returns plain structs, so translating an API response into this
// is the caller's job and this package imports no client.
type Label struct {
	// Name is the label's name exactly as the repository stores it, casing
	// included. Matching against the config is case-insensitive, but the
	// stored casing is what an update has to correct.
	Name string

	// Color is six-digit hex. A leading # and upper case are tolerated —
	// Compute normalises before comparing — but GitHub stores neither.
	Color string

	// Description is the label's description, empty when it has none. GitHub
	// does not distinguish an absent description from an empty one, and
	// neither does this.
	Description string
}

// exhaustionNote annotates a recolour the palette could only satisfy with a
// colour close to one already present. It is reporting text: the colour is
// valid and still applied, so this is a warning and never a failure.
const exhaustionNote = "palette exhausted: no clearly distinct colour remains"

// Compute reconciles one repository. It is pure — no network, no clock, no
// randomness — so the same input always produces byte-identical output.
//
// repo is the owner/repo the actions belong to; it fills [Action.Repo] and
// [RepoPlan.Repo] and is otherwise untouched. desired is the label set resolved
// for this repository, current is what the repository holds today.
//
// # Order
//
// Actions come back in the order they have to be applied:
//
//  1. Renames, so later matching sees the new names. Not yet implemented; the
//     parameter is here so that adding the pass is a change to this function
//     and not to every call site.
//  2. Squatter recolours, before the configured label claims the colour, so
//     that a run aborting mid-repository leaves no configured label sharing a
//     colour with a label that was supposed to have moved off it. GitHub
//     permits duplicate colours, so this is crash-consistency, not validity.
//  3. Creates, for configured labels the repository does not have.
//  4. Existing configured labels in ascending name order: an update when
//     colour, description, or casing differs, a no-op when nothing does.
//  5. Deletes, prune mode only. Not yet implemented.
//
// # Colour ownership
//
// Every configured colour is reserved. An unconfigured label sitting on a
// reserved colour is a squatter and is recoloured, in ascending name order,
// each allocation being fed back into the used set so no two squatters are
// handed the same colour.
//
// # Descriptions
//
// Descriptions are authoritative. A configured label with no description means
// the description is "", and converging on that clears whatever the repository
// has.
func Compute(repo string, desired []config.Label, current []Label, mode Mode, renames []config.Rename) RepoPlan {
	// Renames (#27) and prune (#28) are not implemented yet: mode and renames
	// are accepted so the signature is final, and are ignored until they are.
	_, _ = mode, renames

	sorted := slices.Clone(desired)
	slices.SortFunc(sorted, func(a, b config.Label) int { return strings.Compare(a.Name, b.Name) })

	claimants := claimedColors(sorted)
	matched, unconfigured := partition(sorted, current)

	var actions []Action

	actions = append(actions, recolourSquatters(repo, sorted, unconfigured, claimants)...)
	creates, converged := converge(repo, sorted, matched)
	actions = append(actions, creates...)
	actions = append(actions, converged...)

	return RepoPlan{Repo: repo, Actions: actions}
}

// claimedColors maps each reserved colour to the configured label that owns it.
//
// Two configured labels may legitimately share a colour, in which case the
// first in ascending name order is the one reported as having displaced a
// squatter — the recolour is the same either way, and picking the first keeps
// the reason deterministic.
func claimedColors(desired []config.Label) map[string]string {
	claimants := make(map[string]string, len(desired))

	for _, d := range desired {
		color := normalizeHex(d.Color)

		if _, taken := claimants[color]; !taken {
			claimants[color] = d.Name
		}
	}

	return claimants
}

// partition splits the repository's labels into the ones the config knows
// about, keyed by lower-cased name for lookup, and the ones it does not, in
// ascending name order. Names are compared case-insensitively: GitHub rejects
// two labels whose names differ only in case, so "Bug" and "bug" are the same
// label wearing different casing, not two labels.
func partition(desired []config.Label, current []Label) (matched map[string]Label, unconfigured []Label) {
	configured := make(map[string]struct{}, len(desired))

	for _, d := range desired {
		configured[strings.ToLower(d.Name)] = struct{}{}
	}

	matched = make(map[string]Label, len(current))

	for _, c := range current {
		key := strings.ToLower(c.Name)

		if _, ok := configured[key]; ok {
			matched[key] = c

			continue
		}

		unconfigured = append(unconfigured, c)
	}

	slices.SortFunc(unconfigured, func(a, b Label) int { return strings.Compare(a.Name, b.Name) })

	return matched, unconfigured
}

// recolourSquatters moves every unconfigured label off a reserved colour.
//
// The used set starts as the colours of the unconfigured labels that are
// staying put, and grows by each allocation: palette.Allocate is stateless, so
// feeding the result back is the caller's half of its contract and the only
// thing stopping two squatters from being handed the same colour. Matched
// labels contribute nothing to used — they are about to hold their configured
// colour, which is already reserved.
func recolourSquatters(repo string, desired []config.Label, unconfigured []Label, claimants map[string]string) []Action {
	var (
		squatters []Label
		used      []colorful.Color
	)

	for _, u := range unconfigured {
		if _, reserved := claimants[normalizeHex(u.Color)]; reserved {
			squatters = append(squatters, u)

			continue
		}

		if c, err := colorful.Hex("#" + normalizeHex(u.Color)); err == nil {
			used = append(used, c)
		}
	}

	if len(squatters) == 0 {
		return nil
	}

	reserved := make([]colorful.Color, 0, len(desired))

	for hex := range claimants {
		if c, err := colorful.Hex("#" + hex); err == nil {
			reserved = append(reserved, c)
		}
	}

	// claimants is a map, so its iteration order is random. Allocate weighs
	// every colour in reserved against every candidate and the result does not
	// depend on the order, but sorting costs nothing and makes the input to the
	// one deterministic thing in this package deterministic too.
	slices.SortFunc(reserved, func(a, b colorful.Color) int { return strings.Compare(a.Hex(), b.Hex()) })

	actions := make([]Action, 0, len(squatters))

	for _, s := range squatters {
		allocation := palette.Allocate(used, reserved)
		used = append(used, allocation.Color)

		reason := fmt.Sprintf("displaced by %q", claimants[normalizeHex(s.Color)])

		if allocation.Exhausted {
			reason += "; " + exhaustionNote
		}

		actions = append(actions, Action{
			Kind:   KindUpdate,
			Repo:   repo,
			Name:   s.Name,
			Color:  new(allocation.Hex),
			Reason: reason,
		})
	}

	return actions
}

// converge walks the configured labels in ascending name order and returns the
// creates separately from the actions for labels that already exist, because
// the two go into different positions in the repository's action order.
//
// The second slice holds updates and no-ops interleaved in name order: both are
// outcomes for a label that exists, a no-op is never sent to the API, and
// keeping them in one run means a report reads down the configured labels in
// the order they were written.
func converge(repo string, desired []config.Label, matched map[string]Label) (creates, existing []Action) {
	for _, d := range desired {
		current, ok := matched[strings.ToLower(d.Name)]

		if !ok {
			creates = append(creates, Action{
				Kind:        KindCreate,
				Repo:        repo,
				Name:        d.Name,
				Color:       new(normalizeHex(d.Color)),
				Description: new(d.Description),
			})

			continue
		}

		existing = append(existing, update(repo, d, current))
	}

	return creates, existing
}

// update diffs one configured label against the label the repository holds, and
// returns either an update carrying only the fields that differ or a no-op.
//
// The action's Name is the *current* name, since that is the key the label is
// found under; a casing correction travels in NewName, exactly as a rename
// would, because GitHub has no other way to restyle a name.
func update(repo string, desired config.Label, current Label) Action {
	action := Action{Kind: KindUpdate, Repo: repo, Name: current.Name}

	if desired.Name != current.Name {
		action.NewName = new(desired.Name)
	}

	if color := normalizeHex(desired.Color); color != normalizeHex(current.Color) {
		action.Color = new(color)
	}

	// Authoritative: a configured label with no description means "", and an
	// existing description then has to be cleared rather than left alone.
	if desired.Description != current.Description {
		action.Description = new(desired.Description)
	}

	if action.NewName == nil && action.Color == nil && action.Description == nil {
		return Action{Kind: KindNoOp, Repo: repo, Name: current.Name}
	}

	return action
}

// normalizeHex strips one leading # and lower-cases the rest, so that a colour
// read back from the API compares equal to the same colour written in the
// config. Whether what is left is valid hex is config validation's question;
// here an unparseable colour simply never matches and never enters a colour
// set.
func normalizeHex(raw string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
}
