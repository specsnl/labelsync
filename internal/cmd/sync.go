package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/plan"
	"github.com/specsnl/labelsync/internal/util/exit"
)

// The flags sync adds to the persistent set.
const (
	flagDryRun = "dry-run"
	flagMode   = "mode"
	flagRepo   = "repo"
)

// notImplementedHelp is what a `sync` without --dry-run says. Refusing is the
// honest answer while the write path is unlanded: the alternative is a command
// that reports a plan and quietly applies nothing, which is the one behaviour a
// user could not detect.
const notImplementedHelp = "applying is not implemented yet — run with --dry-run to see what would change"

func newSyncCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile labels against the config",
		Long: `Compute what it would take to make every selected repository match the config.

  labelsync sync --dry-run                      # every group
  labelsync sync --dry-run --group websites     # only these groups, repeatable
  labelsync sync --dry-run --repo specsnl/labelsync   # only these repositories

Exit codes follow terraform plan -detailed-exitcode, and the outcome codes are
bits that combine:

  0  in sync                       4  some repositories could not be reached
  1  the run failed                6  both of the above
  2  --dry-run found pending changes

Run "labelsync export <owner/repo>" first, before the first sync against
repositories that already have labels. Descriptions are authoritative: a label
whose description the config does not carry has its description cleared, and
export is what captures the ones you already have.

Applying is not implemented yet; --dry-run is required.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := syncOptions(cmd)
			if err != nil {
				return err
			}

			return runSync(cmd.Context(), app, opts)
		},
	}

	flags := cmd.Flags()
	flags.Bool(flagDryRun, false, "Compute and print the plan, writing nothing")
	flags.String(flagMode, string(plan.ModeAppend), `Reconciliation mode: "append" or "prune"`)
	flags.StringArray(flagGroup, nil, "Only sync this group (repeatable)")
	flags.StringArray(flagRepo, nil, "Only sync this owner/repo, bypassing group enumeration (repeatable)")

	return cmd
}

// syncOpts is what the command's own flags resolved to.
type syncOpts struct {
	dryRun bool
	mode   plan.Mode
	groups []string
	repos  []string
}

// syncOptions reads and validates the command's flags, before anything is
// loaded or any request is sent.
func syncOptions(cmd *cobra.Command) (syncOpts, error) {
	flags := cmd.Flags()

	opts := syncOpts{}
	opts.dryRun, _ = flags.GetBool(flagDryRun)
	opts.groups, _ = flags.GetStringArray(flagGroup)
	opts.repos, _ = flags.GetStringArray(flagRepo)

	mode, _ := flags.GetString(flagMode)

	switch plan.Mode(mode) {
	case plan.ModeAppend, plan.ModePrune:
		opts.mode = plan.Mode(mode)
	default:
		return opts, fmt.Errorf("invalid --%s %q: want %q or %q", flagMode, mode, plan.ModeAppend, plan.ModePrune)
	}

	if !opts.dryRun {
		return opts, fmt.Errorf("%s", notImplementedHelp)
	}

	return opts, nil
}

// runSync is the whole read pipeline: config, client, groups, enumeration,
// label reads, plan, render. It writes nothing.
func runSync(ctx context.Context, app *App, opts syncOpts) error {
	cfg, err := config.Load(app.ConfigPath)
	if err != nil {
		return err
	}

	client, err := app.client(ctx)
	if err != nil {
		return err
	}

	// Free — GET /rate_limit does not itself count against the limit — and only
	// worth the round trip when somebody is reading the diagnostics. It also
	// seeds the limiter, so the first request of the run is as informed as the
	// last.
	if app.Debug {
		if _, err := client.RateLimit(ctx); err != nil {
			slog.Debug("could not read the rate limit at startup", "error", err)
		}
	}

	resolution, err := app.resolve(ctx, client, cfg)
	if err != nil {
		return err
	}

	repos, err := targets(ctx, app, client, resolution, opts)
	if err != nil {
		return err
	}

	desired, repos := coveredRepos(app, resolution, repos, len(opts.repos) > 0)

	read, err := client.ReadLabels(ctx, repos, app.Concurrency)
	if err != nil {
		return err
	}

	p := plan.Plan{Repos: make([]plan.RepoPlan, 0, len(read))}

	for _, entry := range read {
		p.Repos = append(p.Repos, plan.Compute(
			entry.Repo,
			desired[entry.Repo.String()],
			currentLabels(entry.Labels),
			opts.mode,
			cfg.Renames,
		))
	}

	plan.Render(app.Out, p)

	client.Failures().Report(app.Out)

	return outcome(p, client.Failures(), opts.dryRun)
}

// targets resolves which repositories the run is about: the ones --repo named,
// or the ones the requested groups enumerate.
//
// --repo bypasses **enumeration**, not the config: a repository named on the
// command line still only gets the labels the groups that select it ask for, and
// gets nothing at all when no group does. Bypassing that too would make --repo
// the one way to touch a repository the config does not cover, which is the
// safety property the whole tool rests on.
func targets(
	ctx context.Context,
	app *App,
	client *github.Client,
	resolution *config.Resolution,
	opts syncOpts,
) ([]config.Repo, error) {
	if len(opts.repos) > 0 {
		out := make([]config.Repo, 0, len(opts.repos))

		for _, raw := range opts.repos {
			repo, err := config.ParseRepoRef(raw)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", flagRepo, err)
			}

			out = append(out, repo)
		}

		return out, nil
	}

	names, err := requestedGroups(resolution, opts.groups)
	if err != nil {
		return nil, err
	}

	var selectors []config.Selector

	seen := make(map[string]bool, len(names))

	for _, name := range names {
		for _, selector := range resolution.SelectorsFor(name) {
			if seen[selector.Group] {
				continue
			}

			seen[selector.Group] = true

			selectors = append(selectors, selector)
		}
	}

	return client.Enumerate(ctx, selectors, app.Concurrency)
}

// coveredRepos drops the repositories no configured label selects, and returns
// the desired label set of each one that survived, keyed by owner/repo.
//
// Dropping them is the resolution rule applied one step earlier than the
// planner's own guard: a repository the config does not cover is never touched,
// so reading its labels would be a request spent on a repository nothing will be
// done to. The guard in plan.Compute stays where it is — this is an
// optimisation, not the safety property.
//
// explicit says the repositories were named with --repo. That changes only how
// loudly an uncovered one is reported: naming a repository and being told
// nothing at all about it is the case worth a warning, whereas an enumerated
// group full of repositories no label targets is ordinary and belongs in --debug.
func coveredRepos(
	app *App,
	resolution *config.Resolution,
	repos []config.Repo,
	explicit bool,
) (map[string][]config.Label, []config.Repo) {
	desired := make(map[string][]config.Label, len(repos))
	covered := make([]config.Repo, 0, len(repos))

	for _, repo := range repos {
		labels := resolution.Desired(repo)
		if len(labels) == 0 {
			if explicit {
				app.Out.Warn("no configured label selects %s — it will not be touched", repo)
			} else {
				slog.Debug("no configured label selects this repository", "repo", repo.String())
			}

			continue
		}

		desired[repo.String()] = labels

		covered = append(covered, repo)
	}

	return desired, covered
}

// currentLabels converts the client's labels into the planner's.
//
// The two types are declared to be the same shape for exactly this, so bridging
// them is a conversion rather than a mapping function — and the compiler notices
// if either side drifts. See the note at the top of internal/github/labels.go.
func currentLabels(labels []github.Label) []plan.Label {
	out := make([]plan.Label, len(labels))
	for i, label := range labels {
		out[i] = plan.Label(label)
	}

	return out
}

// outcome turns what the run found into the code it exits with.
//
// The bits are OR-ed rather than ranked, so a dry run that both drifted and could
// not reach a repository exits 6. A non-zero code that is not a failure travels
// on a carrier with no wrapped error, so main prints nothing: the drift *was* the
// successful result, and the diff is already on stdout.
func outcome(p plan.Plan, failures *github.Failures, dryRun bool) error {
	code := exit.OK

	if dryRun && drifted(p) {
		code |= exit.Drift
	}

	code |= failures.ExitCode()

	if code == exit.OK {
		return nil
	}

	return &exit.Err{Code: code}
}

// drifted reports whether the plan holds anything that would change a
// repository. A no-op is a label that was checked and already matched, which is
// the opposite of drift.
func drifted(p plan.Plan) bool {
	summary := plan.Summarise(p)

	return summary.Created+summary.Updated+summary.Deleted > 0
}
