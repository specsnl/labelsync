---
title: Architecture
weight: 10
---

Internal design documents covering the architecture, key decisions, and internals of `labelsync`.
For how to *drive* the tool, see [Usage](../usage/).

These pages describe **what has been built**. The forward-looking plan — goals, prior art, the
reconciliation algorithm, milestones, and open questions — lives in
[design.md](https://github.com/specsnl/labelsync/blob/main/docs/design.md). As each subsystem
lands, its behaviour moves from the design plan into this section.

| Page                                  | Covers                                                                            |
|---------------------------------------|-----------------------------------------------------------------------------------|
| [Overview](./overview.md)             | Package structure, CLI command tree, the reconciliation data flow                 |
| [Error Handling](./error-handling.md) | Sentinel errors, the `%w` wrapping rule, and the `error_kind` JSON contract       |
| [Output & Exit Codes](./output.md)    | `output.Writer`, pretty vs NDJSON, TTY detection, the `slog` boundary, exit codes |
| [Versioning](./versioning.md)         | The linker-injected `Version`, and what each build produces                       |
| [Colour Palette](./palette.md)        | The deterministic HSL candidate grid, its legibility bounds, and determinism      |

Pages are added here as the subsystems they document are implemented — the planner and the GitHub
client with its rate limiting each get their own page.
