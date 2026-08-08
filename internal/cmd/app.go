// Package cmd is the labelsync command tree.
//
// One file per command, all of them leaves on the root built in [Execute]. The
// wiring that every command inherits — the output writer, the debug logger, and
// the persistent flags — is resolved once in the root's PersistentPreRunE and
// reaches the leaves through the [App] they were built with.
//
// # No os.Exit here
//
// Commands return errors; main turns them into exit codes with exit.Of. os.Exit
// skips deferred cleanup, so calling it inside a command would leak temp files,
// unreleased locks, and unflushed writers. A non-zero code that is not a failure
// travels on an *exit.Err with a nil Err field, which main prints nothing for.
package cmd

import (
	"log/slog"
	"time"

	"github.com/specsnl/labelsync/internal/util/output"
)

// Default values for the persistent flags. Named because the tests assert on
// them and the help text renders them.
const (
	// DefaultConcurrency bounds the parallel per-repository reads. Eight is
	// enough to hide the round-trip latency of a mid-sized repository set without
	// tripping GitHub's abuse detection on the read side.
	DefaultConcurrency = 8

	// DefaultWriteRate is the ceiling on label writes per minute. GitHub's
	// secondary rate limits are undocumented and content-based; 70/min is the
	// rate that has proven not to trip them.
	DefaultWriteRate = 70

	// DefaultMaxWait is how long a run may sleep for a rate-limit reset before it
	// gives up with ErrMaxWaitExceeded. A CI job should fail with a clear reason
	// rather than sit on a runner for an hour.
	DefaultMaxWait = 15 * time.Minute
)

// App holds everything a command needs that is not its own flags: the output
// writer, the handle on the debug log level, and the resolved values of the
// persistent flags.
//
// Every command is built with the App and closes over it, so a test constructs
// one App, points it at buffers, and drives the tree. The zero-argument
// [NewApp] is what main uses.
type App struct {
	// Out is the single channel for user-facing output. It is replaced in the
	// root's PersistentPreRunE with a writer over the command's own streams, once
	// --output has been parsed.
	Out output.Writer

	// LogLevel gates the debug logger. Cobra parses flags after the tree is
	// built, so the level is held here and raised once --debug is known.
	LogLevel *slog.LevelVar

	// ConfigPath is --config: an explicit config file path, or "" to search the
	// working directory and then the XDG config directory.
	ConfigPath string

	// Format is --output, already validated against the known formats.
	Format output.Format

	// Debug is --debug: raises the slog level to Debug on stderr.
	Debug bool

	// NoCache is --no-cache: ignore the ETag cache for this run, both on read
	// and on write.
	NoCache bool

	// Concurrency is --concurrency: the bound on parallel per-repository reads.
	Concurrency int

	// WriteRate is --write-rate: the ceiling on label writes per minute.
	WriteRate int

	// MaxWait is --max-wait: the longest a rate-limit backoff may sleep before
	// the run fails with ErrMaxWaitExceeded.
	MaxWait time.Duration
}

// NewApp creates an App with the flag defaults and a silent logger.
//
// Out is a pretty writer over os.Stdout and os.Stderr, and LogLevel is silent.
// Both are placeholders for the window before flags are parsed — a failure
// during parsing still has somewhere to go — and both are replaced in
// PersistentPreRunE by equivalents over the command's own streams. A command
// that reads app.Out is therefore always reading the test's buffers under test.
func NewApp() *App {
	return &App{
		Out:         output.NewDefaultPrettyWriter(),
		LogLevel:    output.SetupDefaultLogger(output.FormatPretty, false),
		Format:      output.FormatPretty,
		Concurrency: DefaultConcurrency,
		WriteRate:   DefaultWriteRate,
		MaxWait:     DefaultMaxWait,
	}
}
