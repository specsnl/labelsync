package output

import (
	"io"

	"github.com/charmbracelet/x/term"
)

// IsTTY reports whether w is an interactive terminal.
//
// Styling does not need this — [NewPrettyWriter] wraps each stream in a
// colorprofile.Writer, which makes the same determination itself and strips the
// escape sequences when the answer is no. This is for the decisions that are not
// about colour:
//
//   - the rate-limit countdown, which rewrites one line with \r on a terminal and
//     logs at a fixed interval into a pipe, because control characters in a CI
//     log are unreadable;
//   - the prune prompt, which must never be shown to a pipe — a CI job blocked on
//     an invisible prompt hangs until it is cancelled.
//
// A writer that is not an *os.File — a bytes.Buffer in a test, say — is not a
// terminal.
func IsTTY(w io.Writer) bool {
	f, ok := w.(term.File)
	if !ok {
		return false
	}

	return term.IsTerminal(f.Fd())
}
