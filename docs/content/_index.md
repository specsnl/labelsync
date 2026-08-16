---
title: Labelsync
layout: hextra-home
---

<!-- markdownlint-disable MD033 -->

<div style="margin-top: 1.5rem; margin-bottom: 1.5rem">
{{< hextra/hero-headline >}}
  One YAML file, the same labels everywhere
{{< /hextra/hero-headline >}}
</div>

<div style="margin-bottom: 3rem">
{{< hextra/hero-subtitle >}}
  `labelsync` reconciles GitHub issue and pull request labels across every repository you select.
  Describe the labels you want once — **labelsync** works out the plan, shows it, and applies it.
{{< /hextra/hero-subtitle >}}
</div>

<div style="margin-bottom: 4rem">
{{< hextra/hero-button text="Get Started" link="docs/usage/getting-started" >}}
{{< hextra/hero-button text="GitHub" link="https://github.com/specsnl/labelsync" style="outline" >}}
</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="A Reconciler, Not a Script"
    subtitle="Reads the current labels, computes an ordered plan, applies it. Running it twice changes nothing the second time."
  >}}
  {{< hextra/feature-card
    title="See It Before You Do It"
    subtitle="`--dry-run` prints the full plan and writes nothing, exiting 2 when anything has drifted — a CI drift gate in one command."
  >}}
  {{< hextra/feature-card
    title="Renames, Not Delete-and-Recreate"
    subtitle="Renaming a label keeps it attached to every issue and pull request that already carries it."
  >}}
  {{< hextra/feature-card
    title="Nothing Is Deleted by Accident"
    subtitle="Extra labels are left alone unless you ask for `--mode=prune`, which reports first and then asks which to remove."
  >}}
  {{< hextra/feature-card
    title="Colours Chosen for You"
    subtitle="Leave a colour out and a deterministic HSL palette allocates a legible, stable one that never collides with its neighbours."
  >}}
  {{< hextra/feature-card
    title="Built for CI"
    subtitle="Stable exit codes, NDJSON output with a stable `error_kind`, an ETag cache, and rate-limit-aware writes."
  >}}
{{< /hextra/feature-grid >}}
