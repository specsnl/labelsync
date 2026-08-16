---
title: Architecture
weight: 10
---

Internal design documents covering the architecture, key decisions, and internals of `labelsync`.
For how to *drive* the tool, see [Usage]({{< ref "../usage" >}}).

These pages describe **what has been built**. The forward-looking plan — goals, prior art, the
reconciliation algorithm, milestones, and open questions — lives in
[design.md](https://github.com/specsnl/labelsync/blob/main/docs/design.md). As each subsystem
lands, its behaviour moves from the design plan into this section.

| Page                                                      | Covers                                                                            |
|-----------------------------------------------------------|-----------------------------------------------------------------------------------|
| [Overview]({{< ref "./overview.md" >}})                   | Package structure, CLI command tree, the reconciliation data flow                 |
| [Error Handling]({{< ref "./error-handling.md" >}})       | Sentinel errors, the `%w` wrapping rule, and the `error_kind` JSON contract       |
| [Output & Exit Codes]({{< ref "./output.md" >}})          | `output.Writer`, pretty vs NDJSON, TTY detection, the `slog` boundary, exit codes |
| [Versioning]({{< ref "./versioning.md" >}})               | The linker-injected `Version`, and what each build produces                       |
| [Colour Palette]({{< ref "./palette.md" >}})              | The deterministic HSL candidate grid, its legibility bounds, and determinism      |
| [Configuration]({{< ref "./configuration.md" >}})         | Config file resolution, YAML parsing, and the normalisation rules                 |
| [Planner]({{< ref "./plan.md" >}})                        | The `Action` / `Plan` vocabulary and its JSON contract                            |
| [Authentication]({{< ref "./authentication.md" >}})       | The four-step token resolution chain, and why tokens are redacted at the type     |
| [GitHub Client]({{< ref "./github-client.md" >}})         | The go-github wrapper, the per-repository error taxonomy, and the `5xx` retry     |
| [Rate Limiting]({{< ref "./rate-limiting.md" >}})         | The write bucket, header tracking, backoff, and the `--max-wait` ceiling          |
| [Apply]({{< ref "./apply.md" >}})                         | Executing a plan in append mode, partial runs, and the startup budget check       |
| [Distribution]({{< ref "./distribution.md" >}})           | The release pipeline, the three install channels, and the Homebrew cask           |
| [Library Decisions]({{< ref "./library-decisions.md" >}}) | Every direct dependency, what it is for, and what it was chosen over              |
| [Demo Recording]({{< ref "./demo.md" >}})                 | The VHS tape behind the GIF, and how to re-record it                              |

Pages are added here as the subsystems they document are implemented.
