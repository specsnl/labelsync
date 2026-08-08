package cmd

import (
	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// Version is the binary version, injected at build time:
//
//	-ldflags "-X github.com/specsnl/labelsync/internal/cmd.Version=1.2.3"
//
// Both .goreleaser.yml and the Dockerfile name this variable by that exact
// path, so it has to stay in this package under this name — rename it and every
// build silently ships as "dev".
var Version = "dev"

const flagDontPrettify = "dont-prettify"

// versionRow is the machine rendering of the version: `{"version":"1.2.3"}`.
// The json tag is a public contract in the same way error_kind is — added to,
// never renamed.
type versionRow struct {
	Version string `json:"version"`
}

func newVersionCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the labelsync version",
		Long: `Print the labelsync version.

The version is a result, not narration, so it goes to stdout and can be
captured:

  labelsync version --dont-prettify   # 1.2.3
  labelsync version --output=json     # {"version":"1.2.3"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bare, _ := cmd.Flags().GetBool(flagDontPrettify)

			// The bare form exists for $(labelsync version --dont-prettify): a
			// shell substitution wants the value, not a sentence around it. JSON
			// output ignores both phrasings and emits the record.
			format := labelsync.AppName + " version %s"
			if bare {
				format = "%s"
			}

			app.Out.WriteResult(versionRow{Version: Version}, format, Version)

			return nil
		},
	}

	cmd.Flags().Bool(flagDontPrettify, false, "Print the bare version string, with nothing around it")

	return cmd
}
