# A VHS tape (https://github.com/charmbracelet/vhs), recorded by `task demo:record:init`.
#
# Offline, unlike labelsync.tape: `init` writes a scaffold and talks to nothing,
# so this one needs no token and no network.
#
# Background: docs/content/docs/architecture/demo.md.

Output ../static/demo/init.gif

Require labelsync

Set Shell bash
Set FontFamily "JetBrains Mono"
Set FontSize 16
Set Width 1100
# Tall enough to hold the whole scaffold plus the two commands above it, so the
# closing frame shows what produced the file.
Set Height 1110
Set Padding 20
Set TypingSpeed 45ms
Set WindowBar Colorful

# An empty directory, because init refuses to overwrite and the working
# directory of the recording already holds a labels.yml. Named rather than
# mktemp'd, so the path init reports is the same in every recording.
Hide
Type "rm -rf /tmp/demo && mkdir /tmp/demo && cd /tmp/demo"
Enter
Type "clear"
Enter
Sleep 1s
Show

Type "labelsync init"
Enter
Sleep 3s

# Without the header comments: they are two thirds of the file, and the shape of
# the config is the point here.
Type "grep -v '^#' labels.yml"
Enter
Sleep 8s
