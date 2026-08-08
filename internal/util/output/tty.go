package output

import (
	"github.com/charmbracelet/x/term"
)

// fileDescriptor is the only thing a terminal check actually needs. Asserting
// this rather than an io.Writer is what lets [IsTTY] answer for stdin as
// naturally as for stdout, and what lets it accept the io.Reader that
// cmd.InOrStdin returns.
type fileDescriptor interface {
	Fd() uintptr
}

// IsTTY reports whether stream is an interactive terminal. It takes any stream —
// os.Stdin as readily as os.Stderr — because the two decisions below ask about
// different ones.
//
// Styling does not need this. [NewPrettyWriter] wraps each stream in a
// colorprofile.Writer, which makes the same determination itself and strips the
// escape sequences when the answer is no. This is for the decisions that are not
// about colour:
//
//   - the rate-limit countdown, which rewrites one line with \r on a terminal and
//     logs at a fixed interval into a pipe, because control characters in a CI
//     log are unreadable. It asks about stderr, where it draws;
//   - the prune prompt, which must never be shown to a pipe — a CI job blocked on
//     an invisible prompt hangs until it is cancelled. It asks about stdin,
//     because the hang it prevents is a read with nobody to answer it. A job with
//     a terminal on stderr and its stdin closed must still refuse to prompt.
//
// Anything without a file descriptor — a bytes.Buffer in a test, say — is not a
// terminal.
func IsTTY(stream any) bool {
	f, ok := stream.(fileDescriptor)
	if !ok {
		return false
	}

	return term.IsTerminal(f.Fd())
}
