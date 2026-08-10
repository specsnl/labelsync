package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

const flagForce = "force"

// initRow is the machine rendering of a scaffolded config: the path that was
// written. The json tag is a public contract — added to, never renamed.
type initRow struct {
	Path string `json:"path"`
}

func newInitCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a labels.yml",
		Long: `Write a starter labels.yml into the working directory.

The scaffolded file is a worked example: a group per source kind, the defaults
that decide which labels a group gets, a rename, and four labels. Edit it, then
check what it would do before it does anything:

  labelsync groups              # which repositories each group resolves to
  labelsync sync --dry-run      # what would change, without writing anything

--config chooses where the file goes: a path writes there, a directory writes
labels.yml inside it. An existing file is never overwritten without --force.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			force, _ := cmd.Flags().GetBool(flagForce)

			return runInit(app, force)
		},
	}

	cmd.Flags().Bool(flagForce, false, "Overwrite an existing config file")

	return cmd
}

// runInit writes the scaffold, unless something is already there.
func runInit(app *App, force bool) error {
	path, err := scaffoldPath(app.ConfigPath)
	if err != nil {
		return err
	}

	if err := checkDestination(path, force); err != nil {
		return err
	}

	// 0o644 rather than 0o600: a config file is committed to a repository and
	// read by CI, and there is nothing secret in it — the token never lives
	// here.
	if err := os.WriteFile(path, config.Scaffold(), 0o644); err != nil { //nolint:gosec // A label catalogue is not a secret.
		return fmt.Errorf("writing %s: %w", path, err)
	}

	app.Out.WriteResult(initRow{Path: path}, "wrote %s", path)
	app.Out.Info("edit it, then run: labelsync sync --dry-run")

	return nil
}

// scaffoldPath resolves where the file goes. An empty --config means the
// working directory; a --config naming a directory means labels.yml inside it,
// which is the same reading Find gives the flag.
func scaffoldPath(explicit string) (string, error) {
	if explicit == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolving the working directory: %w", err)
		}

		return filepath.Join(wd, labelsync.ConfigYMLFile), nil
	}

	info, err := os.Stat(explicit)
	if err == nil && info.IsDir() {
		return filepath.Join(explicit, labelsync.ConfigYMLFile), nil
	}

	return explicit, nil
}

// checkDestination refuses the two ways a scaffold would do damage.
//
// Overwriting a hand-edited catalogue is the obvious one, and --force is the
// answer to it. The other one is not: writing labels.yml into a directory that
// already holds labels.yaml leaves a directory that every later run rejects as
// ambiguous, and it does so at *load* time, a step removed from the command
// that caused it. --force does not cover that, because there is no version of
// "I know what I am doing" that makes the result loadable.
func checkDestination(path string, force bool) error {
	if other := otherSpelling(path); other != "" {
		if _, err := os.Stat(other); err == nil {
			return fmt.Errorf("%w: %s already exists, and holding both spellings is an error — remove it first",
				labelsync.ErrAmbiguousConfigFile, other)
		}
	}

	if force {
		return nil
	}

	_, err := os.Stat(path)
	if err == nil {
		return fmt.Errorf("%w: %s — pass --force to overwrite it", labelsync.ErrConfigExists, path)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	return nil
}

// otherSpelling returns the accepted config file name that path is not, or ""
// when path is neither of them — a --config pointing at labels.local.yml is a
// deliberate name and nothing is ambiguous about it.
func otherSpelling(path string) string {
	dir, name := filepath.Split(path)

	switch name {
	case labelsync.ConfigYMLFile:
		return filepath.Join(dir, labelsync.ConfigYAMLFile)
	case labelsync.ConfigYAMLFile:
		return filepath.Join(dir, labelsync.ConfigYMLFile)
	default:
		return ""
	}
}
