# A VHS tape (https://github.com/charmbracelet/vhs), recorded by `task demo:record`.
#
# It runs against three public repositories, so every command here must be
# read-only: `groups` enumerates, `sync --dry-run` computes without writing.
#
# Background: docs/content/docs/architecture/demo.md.

Output ../static/demo/labelsync.gif

Require labelsync

Set Shell bash
Set FontFamily "JetBrains Mono"
Set FontSize 16
# Wide enough for the longest line of `labelsync --help`, 118 columns, which
# otherwise wraps mid-word in the frame.
Set Width 1240
Set Height 900
Set Padding 20
Set TypingSpeed 45ms
Set WindowBar Colorful

# Opens on a clean prompt rather than on whatever the shell printed while vhs
# was starting up.
Hide
Type "clear"
Enter
Sleep 1s
Show

Type "labelsync --help"
Enter
Sleep 6s

Type "clear"
Enter
Sleep 500ms

Type "labelsync groups"
Enter
Sleep 4s

Type "labelsync sync --dry-run"
Enter
# Long enough to read the ~30-line plan, which is the frame the GIF ends on.
Sleep 8s
