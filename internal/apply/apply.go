// Package apply executes a [plan.Plan]. It is the only code in labelsync that
// writes to GitHub.
//
// # Append mode only
//
// [Apply] creates missing configured labels, updates existing ones, and
// recolours displaced squatters. **It never deletes.** A delete action is
// refused rather than executed — see [Apply] — and the prune path that will
// eventually produce one has its own prompt in front of it.
//
// # The order is a crash-consistency guarantee
//
// Actions go out in exactly the order the planner emitted them: renames, then
// squatter recolours, then creates, then updates. That order is what makes every
// intermediate state coherent, so a run killed halfway through a repository
// leaves it consistent rather than with a configured label sharing a colour with
// a squatter that was supposed to have moved off it. Nothing here reorders for
// throughput, and nothing here writes to two repositories at once: the token
// bucket paces writes at roughly one a second anyway, so parallelism would buy
// no wall-clock time and would cost the guarantee.
//
// # A failed repository is not a failed run
//
// A repository that becomes unreachable partway through is abandoned and the
// run continues with the next one. The failure is already recorded in
// github.Failures by the time it is returned, so the end-of-run summary names it
// and the exit code carries [exit.Skipped]. Every other error — a cancelled
// context, a rate-limit wait past --max-wait — ends the run, because it is not
// about one repository.
package apply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/plan"
)

// ReportKind is the "kind" of the record [Report] marshals to.
//
// Like the planner's summary and repository kinds it is not an action kind and
// is never sent to the API. It shares the "kind" key so a consumer reading the
// NDJSON stream has one discriminator: `jq 'select(.kind == "applied")'` picks
// out what the run actually did, which on a partially failed run is not the same
// thing as what it planned. The string is a wire contract — added to, never
// renamed.
const ReportKind = "applied"

// Writer is the half of *github.Client that applying a plan uses.
//
// It is an interface so the semantics here — the order, the abandonment of a
// failed repository, the refusal to delete — are testable against a fake that
// records calls, without an HTTP mock deciding whether the test passes. The
// end-to-end suite drives the real client against httptest; this narrows what
// has to be simulated to answer "did it do the right things, in the right
// order".
type Writer interface {
	CreateLabel(ctx context.Context, owner, repo string, label github.Label) error
	PatchLabel(ctx context.Context, owner, repo, current string, patch github.LabelPatch) error
}

// Report is what a run of [Apply] did, as opposed to what its plan proposed. The
// two differ exactly when a repository failed partway.
type Report struct {
	Kind string `json:"kind"` // always ReportKind

	// Repositories is how many repositories were written to or confirmed clean —
	// every one the run reached, including the ones that needed nothing.
	Repositories int `json:"repositories"`

	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`

	// Abandoned names the repositories a failure cut short, in the order they
	// were reached. Being here does not say whether anything was written before
	// the failure, which is the honest answer: the write that failed is the only
	// one whose outcome is known.
	Abandoned []string `json:"abandoned,omitempty"`
}

// Apply executes p and reports what it did.
//
// # Deletes are refused
//
// Append mode never deletes, and the guard is here rather than only in the
// planner because this is the package holding the destructive call. A plan
// carrying a delete — read back from a file, or produced by a prune path that
// has not grown its prompt yet — is refused before the **first** write of the
// run rather than partway through, so a refused apply has changed nothing.
func Apply(ctx context.Context, client Writer, p plan.Plan) (Report, error) {
	report := Report{Kind: ReportKind}

	if err := refuseDeletes(p); err != nil {
		return report, err
	}

	for _, repoPlan := range p.Repos {
		owner, name, ok := split(repoPlan.Repo)
		if !ok {
			return report, fmt.Errorf("%w: %s", labelsync.ErrInvalidRepoRef, repoPlan.Repo)
		}

		report.Repositories++

		if err := applyRepo(ctx, client, owner, name, repoPlan, &report); err != nil {
			// Already recorded by github.Client.Do, and the end-of-run summary
			// will name it. Every other error is the run's.
			if errors.Is(err, labelsync.ErrRepoInaccessible) {
				report.Abandoned = append(report.Abandoned, repoPlan.Repo)

				slog.Debug("abandoning a repository partway through an apply", "repo", repoPlan.Repo, "error", err)

				continue
			}

			return report, err
		}
	}

	slog.Debug("apply complete",
		"repositories", report.Repositories,
		"created", report.Created,
		"updated", report.Updated,
		"unchanged", report.Unchanged,
		"abandoned", len(report.Abandoned),
	)

	return report, nil
}

// applyRepo executes one repository's actions in the order they were emitted,
// stopping at the first failure. Stopping is the point: the ordering is a
// crash-consistency guarantee, and carrying on past a failed rename would apply
// the steps that assumed it landed.
func applyRepo(
	ctx context.Context,
	client Writer,
	owner, name string,
	repoPlan plan.RepoPlan,
	report *Report,
) error {
	for _, action := range repoPlan.Actions {
		switch action.Kind {
		case plan.KindNoOp:
			// Never sent to the API. It exists so reporting can show a label as
			// checked rather than silently omitting it, which is the difference
			// between "I looked" and saying nothing at all.
			report.Unchanged++

		case plan.KindCreate:
			if err := client.CreateLabel(ctx, owner, name, github.Label{
				Name:        action.Name,
				Color:       deref(action.Color),
				Description: deref(action.Description),
			}); err != nil {
				return err
			}

			report.Created++

		case plan.KindUpdate:
			// The action's pointers go straight through: an update carries the
			// fields it changes and nothing else, and a nil one has to stay nil
			// all the way to the request body. Filling them in from the desired
			// label would clear the description of every recoloured squatter.
			if err := client.PatchLabel(ctx, owner, name, action.Name, github.LabelPatch{
				NewName:     action.NewName,
				Color:       action.Color,
				Description: action.Description,
			}); err != nil {
				return err
			}

			report.Updated++

		case plan.KindDelete:
			// Unreachable: refuseDeletes ran before the first write. The case is
			// here so that adding a kind is a compiler error rather than a silent
			// no-op, and so that a delete reaching this far fails loudly.
			return fmt.Errorf("%w: %s: delete reached the executor", errNeverDeletes, action.Name)

		default:
			return fmt.Errorf("%w: %q in %s", errUnknownKind, action.Kind, repoPlan.Repo)
		}
	}

	return nil
}

// refuseDeletes rejects a plan carrying anything destructive, before the first
// write.
//
// Refusing up front rather than when the delete is reached is what makes the
// refusal safe: a run that created six labels and then refused would have
// changed a repository on its way to declining to do the job.
func refuseDeletes(p plan.Plan) error {
	for _, repoPlan := range p.Repos {
		for _, action := range repoPlan.Actions {
			if action.Kind == plan.KindDelete {
				return fmt.Errorf("%w: %s holds a removal candidate for %q",
					errNeverDeletes, repoPlan.Repo, action.Name)
			}
		}
	}

	return nil
}

// The two ways a plan can be one this package will not execute. Neither is a
// sentinel in internal/labelsync, because neither is a way a *run* fails: both
// mean the plan handed over was not one append mode can execute, which is a
// caller bug rather than a condition a user can produce from a config file.
var (
	errNeverDeletes = errors.New("append mode never deletes")
	errUnknownKind  = errors.New("unknown action kind")
)

// Writes is how many requests executing p will send: every action except the
// no-ops, which never reach the API.
//
// It is the number the startup budget check is made against, and the number the
// rate-limit countdown counts down.
func Writes(p plan.Plan) int {
	n := 0

	for _, repoPlan := range p.Repos {
		for _, action := range repoPlan.Actions {
			if action.Kind != plan.KindNoOp {
				n++
			}
		}
	}

	return n
}

// split takes owner/repo apart. A plan that is computed rather than read back
// always carries a well-formed one, so the failure path exists for the plan that
// was not.
func split(slug string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(slug, "/")

	return owner, name, ok && owner != "" && name != "" && !strings.Contains(name, "/")
}

// deref reads an optional field, treating absent as empty. On a create every
// field is a value rather than a change — the label does not exist yet, so there
// is nothing to leave alone — and a plan missing one asks for the empty string,
// which is what GitHub stores for a label with no description.
func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
