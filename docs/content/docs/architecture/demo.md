---
title: Demo Recording
weight: 12
---

The GIF at the top of the README and of [Getting started]({{< ref "../usage/getting-started.md" >}})
is a [VHS](https://github.com/charmbracelet/vhs) recording, produced by a tape in the repository
rather than by hand. Everything it needs — the terminal size, the font, the shell, the `labelsync`
build, and the config being reconciled — is pinned in the repository, so re-recording it is one
command and not a set-up.

```sh
task demo:record
```

| Path                             | What it is                                               |
|----------------------------------|----------------------------------------------------------|
| `docs/demo/labelsync.tape`       | The script: terminal settings, the commands, the timings |
| `docs/demo/labels.yml`           | The config the recorded commands reconcile               |
| `docs/static/demo/labelsync.gif` | The output, committed, served at `/demo/labelsync.gif`   |
| `taskfiles/Taskfile.demo.yml`    | `demo:record`, and the Linux build it needs first        |

## What it records

The command surface, then two commands against three **public** repositories that carry GitHub's
stock labels — `specsnl/php83`, `specsnl/php84`, and `specsnl/php85`:

```sh
labelsync --help            # what the tool offers, cleared from the frame before the run
labelsync groups            # the three repositories the config selects, and where from
labelsync sync --dry-run    # the plan: two renames each, one drifted description, the rest in sync
```

The `clear` between the help and the run is the reason the closing frame is the plan and not a
screen scrolled halfway through it. `Set Width 1240` is the other half of that: 118 columns is the
longest line `--help` prints, and anything narrower wraps it mid-word.

They were chosen because they are almost, but not quite, identical: all three still carry `bug` and
`enhancement`, which the demo config renames, and `php85`'s `docker` label describes itself as
"update docker code" where the other two say "Docker". That is one real, unstaged instance of every
row kind the planner can emit — a rename, an update, and a repository already in sync — on live
data, with nothing set up for the camera.

### Both commands are read-only

This is the constraint the tape is written to, and it is not incidental: the recording runs against
repositories that other people use. `groups` enumerates and `sync --dry-run` reads and computes;
neither writes. **A tape that runs a bare `sync`, or a `--mode=prune` without `--dry-run`, would
mutate public repositories to produce a GIF.** If a future demo has to end on an applied change, it
ends on a throwaway repository, not on these.

### Why the config names repositories instead of selecting the org

`docs/demo/labels.yml` lists the three repositories explicitly rather than using
`org: specsnl` with an `include: ["php8*"]` glob, which would be the more natural way to write it.
The reason is [`labelsync groups`]({{< ref "../usage/commands.md" >}}) itself: enumerating an
organisation makes it report one "filtered out" line per rejected repository, *by name*, including
every private one. That is exactly the right behaviour for a human debugging a selector, and
exactly the wrong thing to publish as a GIF. Naming the three keeps the frame short and everything
in it public.

## What re-recording needs

- **Docker and [Task](https://taskfile.dev)** — nothing else. `vhs` is not installed on the host:
  `task demo:record` runs the pinned `ghcr.io/charmbracelet/vhs` image, which carries `ttyd`,
  `ffmpeg`, and the JetBrains Mono the tape asks for. A locally installed `vhs` would record with
  whatever font and terminal the machine happens to have, which is the thing this avoids.
- **A GitHub token** — `GH_TOKEN`, or a logged-in `gh` on the host, which is where `demo:record`
  reads it from when `GH_TOKEN` is unset. Reading the labels of three public repositories needs
  only `public_repo`. A token is required even though the run only reads, because labelsync has no
  anonymous path: see [Authentication]({{< ref "./authentication.md" >}}). It reaches the container
  as an environment variable, and the tape never types it, so it cannot land in a frame.

`demo:record` builds a **Linux** `labelsync` into `dev/` first, with the same
`docker buildx bake` target `task build` uses. The tape runs inside the image, so the macOS binary
`task build` leaves in the working directory is not the one that can be recorded; `dev/` is
gitignored and deliberately separate from `./labelsync`.

## Re-recording is not reproducible, and that is fine

The same tape produces a different GIF every time. The frames carry the live state of three
repositories that dependabot and their maintainers keep changing, and the encoder's timing jitters
between runs, so the bytes never match and the *content* only matches until someone adds a label.
Two consequences worth knowing:

- **The GIF is a snapshot, not a test.** Nothing in CI records or compares it. If the plan it shows
  stops matching what `labelsync sync --dry-run` prints today, the recording is stale — it is not a
  failure of anything.
- **Re-record deliberately.** Running `task demo:record` always dirties the working tree, so a
  regenerated GIF that shows nothing new is 100+ KB of churn in the diff. Commit it when what it
  shows has actually changed: new output, a renamed command, a different plan.
