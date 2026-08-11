---
title: Apply
weight: 9
---

`internal/apply` executes a [`plan.Plan`](./plan.md). It is the only code in `labelsync` that writes
to GitHub, and it is deliberately the smallest package that can be: the planner already decided what
to do and in what order, so all that is left is to do it, in that order, and to be honest about what
happened.

## Append mode only

`Apply` creates missing configured labels, updates existing ones, and recolours displaced squatters.
**It never deletes.** Prune is a later step with its own prompt in front of it, and until that lands
`sync --mode=prune` refuses to apply rather than listing removal candidates and removing none of
them.

The guard is in this package and not only in the planner, because this is the package holding the
destructive call. A plan carrying a `delete` — read back from a file, or produced by a prune path
that has not grown its prompt yet — is refused **before the first write of the run**, so a refused
apply has changed nothing. Refusing when the delete is *reached* would mean a run that created six
labels on its way to declining the job.

## The order is a crash-consistency guarantee

Actions go out in exactly the order the planner emitted them: renames, then squatter recolours, then
creates, then updates. That order is what makes every intermediate state coherent — see
[Planner § Ordering](./plan.md) — so a run killed halfway through a repository leaves it consistent
rather than with a configured label sharing a colour with a squatter that was supposed to have moved
off it.

Nothing here reorders for throughput, and nothing writes to two repositories at once. That is not a
sacrifice: the [write bucket](./rate-limiting.md#proactive) paces writes at roughly one a second, so
parallelism would buy no wall-clock time and would cost the guarantee.

Within a repository, the **first** failure stops that repository. The steps after a failed rename
assume it landed, so carrying on would apply them against a name that is not there.

## `LabelPatch`, and why an update is not a whole label

An `Action` carries the change and not the state it replaces, so an update's three optional fields
are pointers and a nil one means *unchanged*. `github.LabelPatch` mirrors them field for field, and
`PatchLabel` marshals them with `omitempty` on pointers — a nil field disappears from the body and
GitHub leaves it alone; a pointer to `""` marshals as an explicit empty string and clears the
description.

Both halves matter, and each is a bug the other would cause:

- A recoloured squatter's action carries a **colour and nothing else**. Filling the rest in from the
  desired label would rename a label nobody configured and clear a description the config has no
  opinion about.
- A configured label with no description means the description is `""`, and clearing it is a thing
  the config authoritatively asks for. Plain strings with `omitempty` would silently turn "clear it"
  into "leave it".

`Client.UpdateLabel` is the whole-label form, for the one caller that has a complete desired label:
the `422 already_exists` path, where a create is
[reclassified as an update](./github-client.md) and there is no observed label to diff against.

## A failed repository is not a failed run

A repository that becomes unreachable partway through is abandoned, and the run continues with the
next one. The failure is already recorded in `github.Failures` by the time `Apply` sees it — that
happens inside `Client.Do` — so the end-of-run summary names it and the exit code carries
`exit.Skipped`, which is `4`.

Every other error ends the run: a cancelled context, or a rate-limit wait that would exceed
`--max-wait`. Neither is about one repository, and treating one as though it were would skip every
repository left in the set and report a run that mostly succeeded.

A partially applied run therefore has two things to say, and says both: the plan on stdout is what
it set out to do, and the `applied` record is what it managed.

## What was applied is not what was planned

`Report` is a typed record on stdout carrying `"kind": "applied"`, alongside the planner's
`"summary"`. It is a separate record because it answers a separate question — the two differ exactly
when a repository failed partway — and because a consumer discriminating on `kind` should not have
to guess which of two summaries it is holding.

```json
{"kind":"applied","repositories":2,"created":4,"updated":0,"unchanged":0}
```

```text
applied: 4 created · 0 updated · 0 unchanged
```

`Abandoned` names the repositories a failure cut short. Being in that list does not say whether
anything was written before the failure, which is the honest answer: the write that failed is the
only one whose outcome is known.

## The startup budget check

`sync` refuses to begin an apply that obviously cannot finish. `GET /rate_limit` is free, so the run
reads the budget before its first write and compares it with `apply.Writes(p)` — every action but the
no-ops, which never reach the API.

The refusal is `budget_exhausted`, and the message quotes both numbers and the reset time, because
"not enough budget" is not actionable and "30 writes to make, 25 requests left until 14:22Z" is.

The decision lives in `internal/cmd` rather than in the limiter or in this package.
`Limiter.Affordable` deliberately only *answers*: whether a half-finished apply is worse than none at
all is a policy question, and for this command the answer is yes.

Note the two thresholds are different mechanisms. This one refuses to start; the limiter's own
[threshold pause](./rate-limiting.md#proactive) stops mid-run when the budget drops to its last 20
requests, and waits for the window to turn over.

## Nothing here needs an HTTP mock to test

`Apply` takes a `Writer` — the two methods of `*github.Client` it uses — so the semantics that matter
here are testable against a fake that records calls in order: the emitted order, the abandonment of a
failed repository, the refusal to delete, the no-op that is never sent. The end-to-end suite in
`internal/cmd` drives the real client against a **stateful** `httptest` fixture, which is what makes
the convergence assertion possible: a server that answered every listing with the same fixed body
could not tell an apply that worked from one that sent its requests into a void.

Convergence is asserted rather than assumed. Applying, then immediately planning again, must produce
a plan of nothing but no-ops — a create that quietly failed and a create that landed look identical
from one run.
