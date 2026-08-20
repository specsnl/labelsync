---
title: Demo Recording
weight: 14
---

The GIFs in the README and the usage pages are [VHS](https://github.com/charmbracelet/vhs)
recordings, produced by tapes in the repository rather than by hand. Everything they need — the
terminal size, the font, the shell, the `labelsync` build, and the config being reconciled — is
pinned in the repository, so re-recording one is a command and not a set-up.

```sh
task demo:record:labelsync   # groups + sync --dry-run, needs a token
task demo:record:init        # the init scaffold, offline
```

One tape per invocation, deliberately. Every recording produces different bytes even when nothing
it shows has changed, so a "record everything" target would dirty GIFs nobody asked about.

| Path                          | What it is                                                       |
|-------------------------------|------------------------------------------------------------------|
| `docs/demo/labelsync.tape`    | The plan recording: terminal settings, the commands, the timings |
| `docs/demo/init.tape`         | The `init` recording — offline, no token                         |
| `docs/demo/labels.yml`        | The config `labelsync.tape` reconciles                           |
| `docs/static/demo/*.gif`      | The output, committed, served at `/demo/<name>.gif`              |
| `taskfiles/Taskfile.demo.yml` | `demo:record:*`, and the Linux build it needs first              |

## What `labelsync.tape` records

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

## What `init.tape` records

`labelsync init` into an empty directory, then the scaffold it wrote with the header comments
stripped — they are two thirds of the file, and the shape of the config is the point. It is the one
tape that touches nothing outside the container: no token, no network, and its output changes only
when the scaffold does. The recording runs in `/tmp/demo` rather than a `mktemp -d`, so the path
`init` reports is the same in every recording instead of appearing as a different random string in
each one.

## What re-recording needs

- **Docker and [Task](https://taskfile.dev)** — nothing else. `vhs` is not installed on the host:
  the Task target runs the pinned `ghcr.io/charmbracelet/vhs` image, which carries `ttyd`,
  `ffmpeg`, and the JetBrains Mono the tapes ask for. A locally installed `vhs` would record with
  whatever font and terminal the machine happens to have, which is the thing this avoids.
- **A GitHub token, for `labelsync.tape` only** — `GH_TOKEN`, or a logged-in `gh` on the host,
  which is where the target reads it from when `GH_TOKEN` is unset. Reading the labels of three
  public repositories needs only `public_repo`. A token is required even though the run only reads,
  because labelsync has no anonymous path: see
  [Authentication]({{< ref "./authentication.md" >}}). It reaches the container as an environment
  variable, and no tape ever types it, so it cannot land in a frame. `init.tape` needs none, and
  the precondition that demands one skips any tape that never calls the API.

Recording builds a **Linux** `labelsync` into `dev/` first, with the same
`docker buildx bake` target `task build` uses. The tape runs inside the image, so the macOS binary
`task build` leaves in the working directory is not the one that can be recorded; `dev/` is
gitignored and deliberately separate from `./labelsync`.

## Re-recording is not reproducible, and that is fine

The same tape produces a different GIF every time: the encoder's timing jitters between runs, so the
bytes never match. `labelsync.tape` has a second source of drift on top of that — its frames carry
the live state of three repositories that dependabot and their maintainers keep changing, so its
*content* only matches until someone adds a label. Two consequences worth knowing:

- **The GIFs are snapshots, not tests.** Nothing in CI records or compares them. If the plan one
  shows stops matching what `labelsync sync --dry-run` prints today, the recording is stale — it is
  not a failure of anything.
- **Re-record deliberately.** Recording always dirties the working tree, so a regenerated GIF that
  shows nothing new is 100+ KB of churn in the diff. Commit it when what it shows has actually
  changed: new output, a renamed command, a different plan.
