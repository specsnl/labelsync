package cmd

// prune.go is the half of prune mode that needs a person: turning the removal
// candidates a plan carries into the ones that will actually be deleted.
//
// The planner records every unconfigured label as a candidate and decides nothing
// (see internal/plan). The executor deletes what its plan holds and decides
// nothing either (see internal/apply). The decision lives here, between the two,
// because it is the only part of prune that involves a terminal — and keeping it
// out of both of those packages is what lets prune semantics and the destructive
// write be tested without one.

import (
	"context"
	"errors"
	"fmt"

	"charm.land/huh/v2"

	"github.com/specsnl/labelsync/internal/plan"
	"github.com/specsnl/labelsync/internal/util/output"
)

// errPruneDeclined is what an aborted prompt returns. Ctrl-C on a destructive
// question is an answer — "not this" — and it ends the run rather than falling
// through to the appends, because a user who backed out of a prune did not
// necessarily consent to the rest of the plan landing without them watching.
//
// It is not a sentinel in internal/labelsync: it is not a way a run *fails* so
// much as a way a user stops one, and giving it a stable error_kind would invite
// a pipeline to branch on something only a human can produce.
var errPruneDeclined = errors.New("prune cancelled: nothing was deleted")

// pruneTitle is the prompt's heading, and pruneDescription is the sentence under
// it. The consequence is spelled out at the moment of asking rather than only in
// the docs, because "delete a label" reads far more reversible than it is.
const (
	pruneTitle       = "Remove these labels?"
	pruneDescription = "Space selects · enter confirms · a deleted label is removed from every issue and " +
		"pull request that carries it, and nothing restores that."
)

// Selector answers "which of these candidates should be removed?".
//
// It is the seam the interactive prompt is replaced through. A test that supplies
// one is also declaring that a selection can be answered at all — see
// [App.canPrompt] — because a terminal is not a thing a test can portably fake,
// and everything above the prompt is what there is to test.
type Selector func(ctx context.Context, candidates []plan.Candidate) ([]plan.Candidate, error)

// canPrompt reports whether this run can ask a question.
//
// In production that is [output.IsTTY] on stdin, and stdin specifically: the
// failure being avoided is a read with nobody to answer it, so a job with a
// terminal on stderr and its stdin closed must still count as unable. Under test
// it is the presence of an [App.Prompt], which stands in for both the terminal and
// the answer.
func (a *App) canPrompt() bool {
	return a.Prompt != nil || output.IsTTY(a.Stdin)
}

// selectRemovals returns the plan that will be applied: p under append mode, and
// under prune mode p narrowed to the candidates that were chosen.
//
// Nothing here writes to GitHub, and nothing here adds an action. A candidate can
// only be dropped between the report on stdout and the writes, never introduced,
// which is what makes "the plan you were shown is the plan that ran, minus what
// you declined" a property rather than a promise.
func selectRemovals(ctx context.Context, app *App, opts syncOpts, p plan.Plan) (plan.Plan, error) {
	if opts.mode != plan.ModePrune {
		return p, nil
	}

	candidates := plan.Candidates(p)

	// Prune over a repository set that holds nothing unconfigured. There is
	// nothing to ask about, so asking would be a prompt whose every answer is the
	// same — and on a converged tree that is every run.
	if len(candidates) == 0 {
		return p, nil
	}

	if opts.pruneAll {
		// Loud, on stderr, because nobody was asked. The plan on stdout already
		// lists every one of them; this is the line that says they are all going.
		app.Out.Warn("--%s=%s: removing %s from every issue and pull request that carries them",
			flagPrune, pruneAll, plural(len(candidates), "unconfigured label"))

		return p, nil
	}

	selected, err := app.prompt()(ctx, candidates)
	if err != nil {
		return plan.Plan{}, err
	}

	if len(selected) == 0 {
		app.Out.Info("nothing selected: no labels will be removed")
	} else {
		app.Out.Info("removing %s", plural(len(selected), "label"))
	}

	return plan.RetainDeletes(p, selected), nil
}

// prompt is the selector this run should use: the injected one, or the real
// terminal prompt.
func (a *App) prompt() Selector {
	if a.Prompt != nil {
		return a.Prompt
	}

	return a.multiSelect
}

// multiSelect is the real prompt: a huh.MultiSelect over the candidates, in plan
// order, with nothing pre-selected.
//
// Nothing is pre-selected on purpose. A prompt that arrives with every box ticked
// turns an accidental enter into a full prune, and the whole reason prune is
// report-first is that the accident is not undoable.
//
// It draws on **stderr** rather than stdout, like every other thing that narrates
// a run: stdout is the product, and a `--output=json | jq` pipeline must not
// receive a redrawn form in the middle of its stream. It reads stdin, which
// [App.canPrompt] has already established is a terminal.
func (a *App) multiSelect(ctx context.Context, candidates []plan.Candidate) ([]plan.Candidate, error) {
	options := make([]huh.Option[plan.Candidate], 0, len(candidates))
	for _, candidate := range candidates {
		options = append(options, huh.NewOption(candidate.Repo+"  "+candidate.Name, candidate))
	}

	var selected []plan.Candidate

	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[plan.Candidate]().
			Title(pruneTitle).
			Description(pruneDescription).
			Options(options...).
			Value(&selected),
	)).WithInput(a.Stdin).WithOutput(a.Stderr)

	// RunWithContext rather than Run, so ^C at the shell and a cancelled run stop
	// the same way: the context the whole command tree is executed with is the one
	// the form watches.
	if err := form.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, errPruneDeclined
		}

		return nil, fmt.Errorf("asking which labels to remove: %w", err)
	}

	return selected, nil
}
