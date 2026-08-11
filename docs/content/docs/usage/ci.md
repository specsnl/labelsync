---
title: Running in CI
weight: 4
---

`labelsync` is built to run unattended: the config file is committed, a pull-request check reports
drift, and a job on merge — or on a schedule — reconciles it. There is no plan artifact to pass
between jobs, because a plan is cheap to recompute and a stale one is worse than none.

Two things make this work, and both need setting up deliberately: an [exit code](#exit-codes) a job
can branch on, and a [token that can actually write](#the-token).

## Exit codes

```sh
labelsync sync --dry-run; rc=$?
(( rc == 1 ))  && exit 1                                  # the run itself failed
(( rc & 2 ))   && echo "labels have drifted"
(( rc & 4 ))   && echo "some repositories were unreachable"
```

| Code | Meaning                                                              |
|------|----------------------------------------------------------------------|
| `0`  | In sync — nothing to do, or applied successfully with no drift left  |
| `1`  | The run failed: invalid config, no token, an unrecoverable API error |
| `2`  | Drift — a `--dry-run` found pending actions                          |
| `4`  | One or more repositories could not be reached                        |

`2` and `4` are **bits and combine** — a dry run that finds drift *and* cannot reach a repository
exits `6`. Test bits, not equality. `1` stays exclusive: a failed run has no live state to report
on. Exit `2` prints no error line at all, because the drift *was* the successful result and the
diff is already on stdout.

`2` is a `--dry-run` code. An apply that succeeds exits `0` however much it changed; there is no
drift left to report once it has been reconciled.

The shape is `terraform plan -detailed-exitcode`'s, for the same reason: a check job wants "did
anything differ" as a status rather than as text to grep. The full contract, with the reasoning, is
in [Output & Exit Codes](../architecture/output.md#exit-codes).

`4` is the one to decide about. Whether an unreachable repository should fail your pipeline depends
on whether the set is stable — a `repos:` list, where a `4` means something is wrong — or an `org:`
enumeration that a private repository can leave at any moment. `(( rc & 4 ))` lets you warn instead
of failing.

## Output

Pass `--output=json` in CI. Every line is one self-contained JSON object, failures carry a stable
`error_kind`, and nothing is coloured or redrawn — a rate-limit countdown becomes a structured
event every 30 seconds rather than a line rewritten in place, which is unreadable in a log file.

**stdout is the product; stderr is the story of making it.** The plan and the summary go to stdout;
progress, warnings, and diagnostics go to stderr. So a step can capture the answer and nothing
else:

```sh
labelsync sync --dry-run --output=json > plan.ndjson
jq -r 'select(.kind != "summary" and .kind != "noop") | "\(.repo) \(.kind) \(.name)"' plan.ndjson
```

Both renderings are described in full in [Output & Exit Codes](../architecture/output.md).

## The token

The `GITHUB_TOKEN` an Actions workflow is handed automatically is scoped to **the repository the
workflow runs in**. It cannot write labels to any other repository — which is the entire point of
this tool, so it is not a token to start from. A run using it fails on every repository but one.

| Option                       | Trade-off                                                                                                                                 |
|------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| **PAT in a secret**          | Works today with no extra code — the resolver already reads `GH_TOKEN` / `GITHUB_TOKEN` from the environment. Expires, and needs rotating |
| **GitHub App install token** | No expiry to manage, higher rate limits, scoped to selected repositories. Needs an App and a token-minting step                           |

Start with a PAT in a secret, exported as `GH_TOKEN` — a fine-grained token with **Read and write**
on *Issues* for the repositories you sync, or `repo` on a classic one. A GitHub App can be swapped
in later behind the same resolver without touching anything else; see
[Authentication](../architecture/authentication.md) for the four-step chain both go through.

Set it as `GH_TOKEN` rather than `GITHUB_TOKEN`: `GH_TOKEN` is read first, so an environment where
Actions has already set `GITHUB_TOKEN` cannot silently win. Never pass `--token` in a workflow — it
lands in the command line, and therefore in the log.

### The rotation burden

A PAT expires, and a scheduled job is the worst place to find that out. Once the token is dead every
run fails with `no_token` (exit `1`) until somebody mints a replacement, and nothing warns as the
date approaches — a token is an opaque string until a request is made with it, so there is no expiry
for the tool to read.

Two things make that survivable, and both are calendar work rather than code:

- **Set the expiry deliberately and write the date down** where whoever owns the repository will see
  it, rather than leaving it at whatever the token form defaulted to.
- **Expect the weekly run to be how you find out.** A failed scheduled workflow emails the actor who
  last ran it; that notification is the alarm, so do not filter it away.

A **GitHub App** removes the chore entirely — an installation token is minted per run, so there is no
expiry to diarise, the rate limit is higher, and the install is scoped to selected repositories
rather than to everything the PAT's owner can reach. It costs an App and a token-minting step, which
is why the PAT comes first; it can be swapped in later behind the same resolver without touching a
single call site. See [Authentication](../architecture/authentication.md#what-ci-resolves-to-and-what-comes-after-it).

## A GitHub Actions workflow

This is the workflow labelsync runs against **its own labels**, trimmed to the parts worth copying.
The full file is
[`.github/workflows/labels.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/labels.yml).

```yaml
name: Labels

on:
  pull_request:
    paths: ["labels.yml"]
  push:
    branches: [main]
    paths: ["labels.yml"]
  schedule:
    - cron: "0 6 * * 1"          # weekly drift correction
  workflow_dispatch:

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: false

permissions:
  contents: read

jobs:
  labels:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    env:
      GH_TOKEN: ${{ secrets.LABELSYNC_TOKEN }}
    steps:
      - uses: actions/checkout@v4

      - name: Install labelsync
        run: go install github.com/specsnl/labelsync@latest

      - name: Check for drift
        if: github.event_name == 'pull_request'
        run: labelsync sync --dry-run --output=json

      - name: Apply
        if: github.event_name != 'pull_request'
        run: labelsync sync --output=json
```

- **The pull-request step is a check, not a preview.** It exits `2` when the committed config and
  the live labels disagree, which fails the job — so a config change cannot merge while claiming to
  be already applied, and a label edited by hand in the UI shows up as a failing check.
- **`paths:` keeps it to config changes**, and the schedule covers everything the filter misses:
  drift comes from people editing labels in the web UI, which no push triggers.
- **The scheduled run is safe to repeat.** `sync` is convergent — a second run with nothing to fix
  does nothing at all and exits `0`.
- **The token is job-level, not step-level**, so there is one place to change it and no step that
  quietly runs without it.
- **`go install` is the line to copy.** The real file builds the binary from its own checkout
  instead, so a pull request is checked by the labelsync in the commit under test — which only makes
  sense in this repository.
- **`cancel-in-progress: false`**, because an apply halfway through its writes has to finish. The
  check writes nothing and could be cancelled, but splitting the policy by event takes a second
  workflow to say.
- **`permissions: contents: read`** is all the job needs from the automatic token; every write goes
  through the PAT.

### Not a required status check

Two things keep this job off the branch protection list: `paths:` means it does not run on most pull
requests at all, so requiring it would deadlock every unrelated change, and a pull request **from a
fork cannot see the secret** — Actions withholds secrets from fork runs, so the job has nothing to
authenticate with. The workflow this repository ships therefore checks for the token first and
annotates a warning instead of failing when it is absent. That is a deliberate trade: a red X on
every pull request until the secret exists is worse, and the warning is loud enough not to read as a
pass.

## Pruning in CI

`--mode=prune` **refuses to run without a terminal on stdin** rather than prompting into a pipe, so
a plain prune in a workflow fails immediately with `interactive_required` (exit `1`) — before the
config is read and before the first request. That is deliberate: a prompt nobody can answer blocks
the job until it times out.

The two ways through are the two the message names:

```sh
labelsync sync --dry-run --mode prune     # report removal candidates; never prompts
labelsync sync --mode prune --prune all   # remove every candidate, unattended
```

`--dry-run --mode prune` is the one to put in a pull-request check: it lists every unconfigured
label as a removal candidate, exits `2` if there is anything to report, and writes nothing.
`--prune=all` deletes without asking, and deleting a label removes it from every issue and pull
request that carries it — so put it behind a manual `workflow_dispatch` rather than a schedule, and
read the dry run first. The full semantics are in
[Commands § Removing labels](./commands.md#prune).

## Rate limits and long runs

A first apply across many repositories is slow on purpose: writes are paced at `--write-rate` a
minute (default 70) to stay under GitHub's content-creation limit. Two consequences for a job:

- **Give the job a generous timeout.** Several hundred labels is minutes, not seconds.
- **A run that cannot finish inside the rate-limit budget is refused before its first write**
  (`budget_exhausted`, exit `1`) rather than stopping halfway. Rerunning after the budget resets
  picks up where it left off, because the reconciler only ever writes what is still missing.

`--max-wait` (default `15m`) caps the total time a run may spend asleep waiting for a limit to
clear; exceeding it fails with `max_wait_exceeded` instead of taking the wait. In CI that is a
guard against a job billing an hour of runner time to a countdown — see
[Rate Limiting](../architecture/rate-limiting.md).

## Other CI systems

Nothing here is Actions-specific. Any runner works the same way: install the binary, put a token in
`GH_TOKEN`, run `labelsync sync --dry-run --output=json` for a check and `labelsync sync` for an
apply, and branch on the exit code. The only Actions-specific note on this page is the one about
its injected `GITHUB_TOKEN`, which no other system has.
