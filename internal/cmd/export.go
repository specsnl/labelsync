package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// flagOut is --out, and it is not the design sketch's `-o`. The root already
// defines `-o` as the shorthand for --output, and a second flag claiming the same
// letter is a pflag panic rather than a preference.
const flagOut = "out"

// exportRow is the machine rendering of an export that went to a file: where it
// went, and how many labels it carries.
//
// An export to stdout emits no row at all — the YAML *is* the product there, and
// a JSON object spliced into it would corrupt the file it is being redirected
// into.
type exportRow struct {
	Path   string `json:"path"`
	Labels int    `json:"labels"`
}

func newExportCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <owner/repo>",
		Short: "Dump a repository's labels as config YAML",
		Long: `Write a repository's current labels as a labelsync config file.

  labelsync export specsnl/labelsync                 # to stdout
  labelsync export specsnl/labelsync --out labels.yml

Run this before the first sync against repositories that already have labels.
Descriptions are authoritative: a label whose description your config does not
carry has its description cleared. An export is a faithful starting point, so
that never happens by accident.

The output is sorted by name and normalised the same way the loader normalises a
config, so export, edit, export is a diff of your edits and nothing else.

It validates clean, with one exception it flags rather than hides: a repository
holding two labels of the same colour is exported as it is, with a comment on
both. Colours have to be unique across a config file, so that is an edit only a
human can make — being told which two to look at is the whole point.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, _ := cmd.Flags().GetString(flagOut)

			return runExport(cmd.Context(), app, args[0], out)
		},
	}

	cmd.Flags().String(flagOut, "", "Write to this file instead of stdout (a directory means labels.yml inside it)")

	return cmd
}

// runExport reads one repository's labels and renders them as config YAML.
func runExport(ctx context.Context, app *App, ref, out string) error {
	repo, err := config.ParseRepoRef(ref)
	if err != nil {
		return err
	}

	client, err := app.client(ctx)
	if err != nil {
		return err
	}

	labels, err := client.ListLabels(ctx, repo.Owner, repo.Name)
	if err != nil {
		return err
	}

	rendered, err := config.Export(repo.String(), exportedLabels(labels))
	if err != nil {
		return err
	}

	warnDuplicateColors(app, labels)

	if out == "" {
		// Straight to the stream, not through the writer. The YAML is a file
		// rather than a record, and every product-level Writer method would
		// either wrap it in JSON or interleave prose with it. This is the one
		// command whose stdout is not one typed object per line, and it is
		// deliberate: `labelsync export x > labels.yml` has to produce a config
		// file, whatever --output says.
		if _, err := app.Stdout.Write(rendered); err != nil {
			return fmt.Errorf("writing the export: %w", err)
		}

		return nil
	}

	path := exportPath(out)

	if err := os.WriteFile(path, rendered, 0o644); err != nil { //nolint:gosec // A label catalogue is not a secret.
		return fmt.Errorf("writing %s: %w", path, err)
	}

	app.Out.WriteResult(exportRow{Path: path, Labels: len(labels)}, "wrote %d labels to %s", len(labels), path)

	return nil
}

// exportedLabels converts the client's labels into the config's.
//
// A mapping function rather than a conversion, unlike plan.Label: config.Label
// carries Groups, which a repository's labels have nothing to say about — the
// exported file puts every label in one group, which is the file's business
// rather than the API response's.
func exportedLabels(labels []github.Label) []config.Label {
	out := make([]config.Label, len(labels))
	for i, label := range labels {
		out[i] = config.Label{
			Name:        label.Name,
			Color:       label.Color,
			Description: label.Description,
		}
	}

	return out
}

// warnDuplicateColors says on stderr what the file says in a comment.
//
// Both, because they reach different people: an export redirected into a file is
// one nobody reads until the next run rejects it, and an export to a terminal
// scrolls past. The file is the record; this is the notice.
func warnDuplicateColors(app *App, labels []github.Label) {
	duplicates := config.DuplicateColors(exportedLabels(labels))
	if len(duplicates) == 0 {
		return
	}

	for _, color := range slices.Sorted(maps.Keys(duplicates)) {
		app.Out.Warn("%s is used by %s — colours have to be unique in a config file, so change one before syncing",
			color, strings.Join(quoteAll(duplicates[color]), " and "))
	}
}

// quoteAll quotes names for a message, so a label called `bug` and one called
// `bug ` are visibly different things.
func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = fmt.Sprintf("%q", name)
	}

	return out
}

// exportPath resolves --out: a directory means labels.yml inside it, which is
// the same reading --config gets everywhere else.
func exportPath(out string) string {
	if info, err := os.Stat(out); err == nil && info.IsDir() {
		return filepath.Join(out, labelsync.ConfigYMLFile)
	}

	return out
}
