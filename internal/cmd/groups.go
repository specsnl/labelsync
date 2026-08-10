package cmd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
	"github.com/specsnl/labelsync/internal/util/output"
)

const flagGroup = "group"

// noRepositories is what an empty group's repository cell says. A blank cell
// would read as "nothing to report here", which is the opposite of the case it
// describes.
const noRepositories = "— none —"

// groupRow is one group's membership, as both audiences see it.
//
// Repositories is an int and not a rendered string, so a consumer can
// `select(.repositories == 0)` rather than matching prose. The json tags are a
// public contract: added to, never renamed.
type groupRow struct {
	Group string `json:"group"`

	// Source is where the group's repositories come from, as the config spells
	// it — "org: specsnl", "repos:", or the groups a composed group flattened
	// into. It is prose for a reader, and the one field here that is.
	Source string `json:"source"`

	Repositories int      `json:"repositories"`
	Repos        []string `json:"repos"`
}

func newGroupsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "Resolve and list group to repository membership",
		Long: `Print which repositories each group actually resolves to.

This is the command to run before a prune, and before the first sync against a
new selector: it answers "does this group mean what I think it means" without
writing anything.

Repositories a group's filters removed are reported on stderr with the reason —
archived, a fork, the wrong visibility, or a glob — because the absence of a
repository you expected is the exact thing this command exists to explain. A
group that resolves to nothing at all is called out the same way.

  labelsync groups                        # every group
  labelsync groups --group specs-all      # only this one, repeatable
  labelsync groups --output=json | jq 'select(.repositories == 0)'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			wanted, _ := cmd.Flags().GetStringArray(flagGroup)

			return runGroups(cmd, app, wanted)
		},
	}

	cmd.Flags().StringArray(flagGroup, nil, "Only resolve this group (repeatable)")

	return cmd
}

// runGroups resolves the requested groups and prints what they select.
func runGroups(cmd *cobra.Command, app *App, wanted []string) error {
	ctx := cmd.Context()

	cfg, err := config.Load(app.ConfigPath)
	if err != nil {
		return err
	}

	client, err := app.client(ctx)
	if err != nil {
		return err
	}

	resolution, err := app.resolve(ctx, client, cfg)
	if err != nil {
		return err
	}

	names, err := requestedGroups(resolution, wanted)
	if err != nil {
		return err
	}

	// One walk per distinct selector, however many groups point at it: a
	// composed group borrows the selectors of the groups it includes, and
	// enumerating an org once per group that reaches it would be the same
	// listing paid for twice.
	selections, err := client.Select(ctx, resolution.Selectors(), app.Concurrency)
	if err != nil {
		return err
	}

	byGroup := make(map[string]github.Selection, len(selections))
	for _, selection := range selections {
		byGroup[selection.Selector.Group] = selection
	}

	rows := make([]groupRow, 0, len(names))

	for _, name := range names {
		// Empty rather than nil, so an empty group marshals as "repos":[] and
		// `.repos | length` answers 0 instead of failing on a null.
		row := groupRow{Group: name, Source: sourceOf(cfg.Groups[name]), Repos: []string{}}

		for _, selector := range resolution.SelectorsFor(name) {
			selection := byGroup[selector.Group]

			for _, repo := range selection.Repos {
				row.Repos = append(row.Repos, repo.String())
			}

			reportRejected(app, name, selection)
		}

		// Deduplicated because two selectors of a composed group may both reach
		// the same repository, and a group does not select it twice.
		slices.Sort(row.Repos)

		row.Repos = slices.Compact(row.Repos)
		row.Repositories = len(row.Repos)

		if row.Repositories == 0 {
			app.Out.Warn("group %q resolves to no repositories", name)
		}

		rows = append(rows, row)
	}

	output.Table(app.Out, rows,
		output.Col("Group", func(r groupRow) string { return r.Group }),
		output.Col("Source", func(r groupRow) string { return r.Source }),
		output.Col("Count", func(r groupRow) string { return strconv.Itoa(r.Repositories) }),
		output.Col("Repositories", func(r groupRow) string {
			if len(r.Repos) == 0 {
				return noRepositories
			}

			return strings.Join(r.Repos, ", ")
		}),
	)

	client.Failures().Report(app.Out)

	if code := client.Failures().ExitCode(); code != exit.OK {
		return &exit.Err{Code: code}
	}

	return nil
}

// reportRejected says what a selector listed and then filtered out.
//
// On stderr, through Info: it explains the product rather than being it, and
// `labelsync groups --output=json | jq` has to keep working. It is one line per
// repository and deliberately not summarised — the reader is here because one
// specific repository is missing, and a count would not tell them which.
func reportRejected(app *App, group string, selection github.Selection) {
	for _, rejected := range selection.Rejected {
		app.Out.Info("group %q: %s filtered out — %s", group, rejected.Repo, rejected.Reason)
	}
}

// requestedGroups returns the groups to report on: the ones --group named, in
// the order they were given, or every group when it named none.
//
// A --group naming a group the config does not define is ErrUnknownGroup rather
// than an empty table. Reporting nothing for a typo is how a maintainer
// concludes a selector is broken when the selector is fine.
func requestedGroups(resolution *config.Resolution, wanted []string) ([]string, error) {
	if len(wanted) == 0 {
		return resolution.Names(), nil
	}

	defined := resolution.Names()

	for _, name := range wanted {
		if !slices.Contains(defined, name) {
			return nil, fmt.Errorf("%w: --group %q, and the config defines %s",
				labelsync.ErrUnknownGroup, name, strings.Join(defined, ", "))
		}
	}

	return wanted, nil
}

// sourceOf renders where a group's repositories come from, the way the config
// spells it.
//
// A composed group is shown as the groups it includes rather than as the
// selectors it flattened into: include_groups is what the file says, and a
// reader asking why a group selects something wants to be pointed at the line
// they wrote.
func sourceOf(group config.Group) string {
	switch {
	case group.Org != "":
		return "org: " + group.Org
	case group.User != "":
		return "user: " + group.User
	case len(group.Repos) > 0:
		return "repos: " + strconv.Itoa(len(group.Repos)) + " named"
	case len(group.IncludeGroups) > 0:
		return "include_groups: " + strings.Join(group.IncludeGroups, ", ")
	default:
		// Unreachable through Load, which rejects a sourceless group before it
		// gets here. A hand-built Config is only as trustworthy as whoever built
		// it, and an empty cell would read as a group with no repositories
		// rather than as one nothing can be said about.
		return "unknown"
	}
}
