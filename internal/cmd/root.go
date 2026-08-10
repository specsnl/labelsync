package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/util/output"
)

// Flag names, in one place because both the definition and the PersistentPreRunE
// lookup have to agree on the string.
const (
	flagConfig      = "config"
	flagToken       = "token"
	flagDebug       = "debug"
	flagOutput      = "output"
	flagNoCache     = "no-cache"
	flagConcurrency = "concurrency"
	flagWriteRate   = "write-rate"
	flagMaxWait     = "max-wait"
	flagVersion     = "version"
)

// Execute builds the command tree and runs it with a background context.
func Execute(app *App) error {
	return ExecuteContext(context.Background(), app)
}

// ExecuteContext builds the command tree and runs it with the given context.
// The context reaches the handlers as cmd.Context(), which is how a cancelled
// run stops mid-flight instead of finishing its writes.
func ExecuteContext(ctx context.Context, app *App) error {
	return NewRootCmd(app).ExecuteContext(ctx)
}

// NewRootCmd builds the labelsync root command, with every subcommand attached
// and every persistent flag defined.
//
// Exported because a test drives the tree the way main does — build it, point
// cmd.SetOut/SetErr at buffers, execute — and that is the only way to assert
// that the output really is captured.
func NewRootCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "labelsync",
		Short: "Synchronise GitHub labels across repositories from a YAML file",
		Long: `labelsync synchronises GitHub issue/PR labels across a configured set of
repositories, using a local YAML file as the source of truth.

Use "labelsync <command> --help" for more information about a command.`,

		// Without SilenceUsage every runtime failure appends the full usage block
		// after the message. Usage helps with a bad flag, not with "repository is
		// inaccessible"; on a runtime failure it buries the one line that matters.
		SilenceUsage: true,

		// Without SilenceErrors Cobra prints its own bare "Error: ..." line in
		// addition to main's, and its copy carries no error_kind. main owns the
		// single print.
		SilenceErrors: true,

		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return app.resolveFlags(cmd)
		},

		// The root has a RunE only so that --version has somewhere to be handled.
		// Without the flag it does what a bare `labelsync` did before: print the
		// help. Args stays nil so Cobra's legacy validator keeps rejecting an
		// unknown subcommand rather than silently showing help for it.
		RunE: func(cmd *cobra.Command, _ []string) error {
			if version, _ := cmd.Flags().GetBool(flagVersion); version {
				writeVersion(app, true)

				return nil
			}

			return cmd.Help()
		},
	}

	flags := cmd.PersistentFlags()
	flags.StringP(flagConfig, "c", "", "Path to the config file (default: ./labels.yml, then the XDG config directory)")
	flags.String(flagToken, "", "GitHub token (discouraged: visible in shell history and process lists — prefer GH_TOKEN)")
	flags.Bool(flagDebug, false, "Write debug diagnostics to stderr")
	flags.StringP(flagOutput, "o", string(output.FormatPretty), `Output format: "pretty" or "json"`)
	flags.Bool(flagNoCache, false, "Ignore the ETag cache for this run")
	flags.Int(flagConcurrency, DefaultConcurrency, "Maximum repositories read in parallel")
	flags.Int(flagWriteRate, DefaultWriteRate, "Maximum label writes per minute")
	flags.Duration(flagMaxWait, DefaultMaxWait, "Longest a rate-limit backoff may sleep before the run fails")

	// Local, not persistent: --version answers for the binary, and `labelsync
	// sync --version` would be asking a question the subcommand does not have.
	//
	// Reimplemented rather than enabled with cmd.Version + SetVersionTemplate,
	// because Cobra handles its built-in version flag inside execute() *before*
	// PersistentPreRunE runs. At that point --output has not been read and
	// app.Out is still the fallback writer, so `--output=json --version` would
	// print a bare line into what is supposed to be a stream of typed JSON
	// objects. Going through RunE means the flag gets the writer the user asked
	// for.
	cmd.Flags().Bool(flagVersion, false, "Print the version and exit (same as: version --dont-prettify)")

	cmd.AddCommand(
		newExportCmd(app),
		newGroupsCmd(app),
		newInitCmd(app),
		newSyncCmd(app),
		newVersionCmd(app),
	)

	return cmd
}

// resolveFlags copies the parsed persistent flags onto the App and rebuilds the
// output writer and the logger over the command's own streams.
//
// The writers come from cmd.OutOrStdout / cmd.ErrOrStderr rather than the
// NewDefault* constructors: those hardcode os.Stdout and os.Stderr, and are how
// output silently escapes a test's buffers. In production the accessors resolve
// to exactly the same files, so nothing is given up.
//
// The writer is installed before the flag values are validated, so a rejected
// --concurrency is still reported through the writer the user asked for.
func (a *App) resolveFlags(cmd *cobra.Command) error {
	flags := cmd.Flags()

	// Lookup errors are unreachable: every name here is defined a few lines above
	// as a flag of that type, so a mismatch is a compile-time-adjacent bug in this
	// file rather than anything a user can cause.
	format, _ := flags.GetString(flagOutput)
	a.ConfigPath, _ = flags.GetString(flagConfig)
	a.Token, _ = flags.GetString(flagToken)
	a.Debug, _ = flags.GetBool(flagDebug)
	a.NoCache, _ = flags.GetBool(flagNoCache)
	a.Concurrency, _ = flags.GetInt(flagConcurrency)
	a.WriteRate, _ = flags.GetInt(flagWriteRate)
	a.MaxWait, _ = flags.GetDuration(flagMaxWait)

	// An unknown format still has to report itself somehow, so anything that is
	// not json is wired as pretty and the rejection follows below.
	a.Format = output.FormatPretty
	if output.Format(format) == output.FormatJSON {
		a.Format = output.FormatJSON
	}

	if a.Format == output.FormatJSON {
		a.Out = output.NewJSONWriter(cmd.OutOrStdout(), cmd.ErrOrStderr())
	} else {
		a.Out = output.NewPrettyWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil)
	}

	// The raw stream, for `export`'s file — see App.Stdout. Taken from the
	// command for the same reason the writers are.
	a.Stdout = cmd.OutOrStdout()

	// Stderr, and the command's stderr specifically: --debug output a test cannot
	// capture is the one thing the accessors exist to prevent.
	a.LogLevel = output.SetupLogger(cmd.ErrOrStderr(), a.Format, a.Debug)

	if format != string(output.FormatPretty) && format != string(output.FormatJSON) {
		return fmt.Errorf("invalid --%s %q: want %q or %q", flagOutput, format, output.FormatPretty, output.FormatJSON)
	}

	if a.Concurrency < 1 {
		return fmt.Errorf("invalid --%s %d: want a positive number", flagConcurrency, a.Concurrency)
	}

	if a.WriteRate < 1 {
		return fmt.Errorf("invalid --%s %d: want a positive number", flagWriteRate, a.WriteRate)
	}

	if a.MaxWait < 0 {
		return fmt.Errorf("invalid --%s %s: want a non-negative duration", flagMaxWait, a.MaxWait)
	}

	// a.Token is deliberately absent from this line. It is the one flag whose
	// value must never reach a log, and --debug is exactly the situation where a
	// user is most likely to be pasting output into an issue. Which token source
	// won is reported by the resolver instead; see internal/github/auth.go.
	slog.Debug("flags resolved",
		"config", a.ConfigPath,
		"token_set", a.Token != "",
		"output", string(a.Format),
		"no_cache", a.NoCache,
		"concurrency", a.Concurrency,
		"write_rate", a.WriteRate,
		"max_wait", a.MaxWait,
	)

	return nil
}
