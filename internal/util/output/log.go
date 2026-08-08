package output

import (
	"io"
	"log/slog"
	"os"
)

// LevelSilent is above every level slog defines, so a logger set to it emits
// nothing at all.
//
// It is the default, and that is the whole point of the boundary this package
// draws: slog is a diagnostic channel for someone debugging labelsync, not a
// reporting channel for someone using it. A normal run produces no slog records,
// and anything a user is meant to read went through a [Writer] instead. Without
// a silent default, warn- and error-level diagnostics would leak onto stderr and
// interleave with the real output.
const LevelSilent = slog.LevelError + 1

// SetupLogger installs the process-wide default slog logger on stderr and
// returns the [slog.LevelVar] gating it.
//
// debug selects [slog.LevelDebug]; otherwise the level is [LevelSilent] and
// nothing is written. Records are formatted to match the output format, so
// `--debug --output=json` yields a stderr stream that is JSON all the way down.
//
// Cobra parses persistent flags after the root command is built, so the level is
// returned rather than baked in: call SetupLogger once during construction, then
// level.Set(slog.LevelDebug) from PersistentPreRunE once --debug is known.
//
// Stderr, never stdout: `labelsync groups --output=json | jq` must not have debug
// lines spliced into the object stream.
func SetupLogger(stderr io.Writer, format Format, debug bool) *slog.LevelVar {
	level := new(slog.LevelVar)
	level.Set(LevelSilent)

	if debug {
		level.Set(slog.LevelDebug)
	}

	slog.SetDefault(slog.New(newHandler(stderr, format, level)))

	return level
}

// SetupDefaultLogger installs the default slog logger on os.Stderr.
func SetupDefaultLogger(format Format, debug bool) *slog.LevelVar {
	return SetupLogger(os.Stderr, format, debug)
}

func newHandler(w io.Writer, format Format, level *slog.LevelVar) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}

	if format == FormatJSON {
		return slog.NewJSONHandler(w, opts)
	}

	return slog.NewTextHandler(w, opts)
}
