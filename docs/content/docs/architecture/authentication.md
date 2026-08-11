---
title: Authentication
weight: 8
---

`internal/github/auth.go` resolves the GitHub credential every request in the package
authenticates with. It is the only thing in `labelsync` that `github.com/cli/go-gh` is a dependency
for — everything else goes through go-github.

## The chain

Four sources, in a fixed order, **first non-empty wins**:

| # | Source                          | Reported as             | Where it comes from                       |
|---|---------------------------------|-------------------------|-------------------------------------------|
| 1 | `--token`                       | `--token`               | The flag, discouraged — see below         |
| 2 | `GH_TOKEN`, then `GITHUB_TOKEN` | the variable's own name | The environment                           |
| 3 | The `gh` config file            | `gh config`             | go-gh's `auth.TokenForHost("github.com")` |
| 4 | `gh auth token`                 | `gh auth token`         | Shelling out to the `gh` binary           |

`GH_TOKEN` and `GITHUB_TOKEN` are two sources rather than one because they are two variables.
Telling a user with both exported that "an environment variable won" leaves them no better off than
before.

A source that is *present but carries nothing* is walked past rather than resolved to: a variable
exported as the empty string, a variable set to whitespace, and `gh auth token` printing only its
trailing newline are all the same absence. The winner is trimmed, so a stray newline never reaches
an `Authorization` header.

When every source is empty, the run fails with `ErrNoToken` — `error_kind: no_token` — and a
message naming all four, because a user who has just been told "no GitHub token found" needs to be
told what would count as one.

## Why step 4 is not redundant

It looks like dead code sitting behind step 3, and it is not.

Modern `gh` stores its token in the **system keychain** by default on macOS, and a keychain token is
not written to `hosts.yml`. go-gh's config reader therefore returns nothing for a user who is
perfectly well logged in. `gh` itself knows how to read its own keychain entry, and asking it is the
only way to reach that token.

Neither step subsumes the other:

- **Step 3 without step 4** misses the developer laptop, where the token is in the keychain.
- **Step 4 without step 3** misses the CI image with a `hosts.yml` baked in and no `gh` on `PATH`.

The reasoning is repeated as a comment on `ghAuthToken` for the same reason it is here: without it,
the function reads as something to delete.

## A broken source is not a broken run

A step that fails does not end the walk. `gh` not being installed is the ordinary case on a CI
runner, not something worth reporting to someone who set `GITHUB_TOKEN` anyway. Failures are logged
at debug level and the chain continues, so the only two outcomes a caller ever sees are a token or
`ErrNoToken`.

## Tokens are redacted at the type

`Token` carries the credential and the source that produced it, and it implements both
`fmt.Stringer` and `slog.LogValuer` to render as `token from <source>`:

```go
type Token struct {
    Value  string
    Source TokenSource
}
```

Those are the two routes a struct normally takes to an output stream, and both are closed. The
guarantee lives on the type rather than in the discipline of every call site, because a call site
that gets it right today says nothing about the one added tomorrow — and a credential written to a
CI log is not recoverable by editing the log.

The same rule is why `--token` is absent from the root command's `flags resolved` debug line, which
logs `token_set` instead. `--debug` is exactly the situation where someone is about to paste output
into an issue.

## Why `--token` is discouraged

The flag exists because a script sometimes has nowhere else to put a credential, and its help text
says what it costs: a token on the command line is in the shell history and in every process list on
the machine. `GH_TOKEN` is the same convenience without either.

## What CI resolves to, and what comes after it

The token GitHub Actions injects into a workflow is scoped to **the repository the workflow runs
in**, so it can write labels to exactly one repository — which is the one thing this tool is not
for. Step 2 of the chain is what CI uses instead: a personal access token in a secret, exported as
`GH_TOKEN`. `GH_TOKEN` and not `GITHUB_TOKEN`, because it is read first, so a workflow that has both
cannot silently resolve to the useless one.

This repository's own [`.github/workflows/labels.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/labels.yml)
does exactly that, with the PAT in `secrets.LABELSYNC_TOKEN`.

**The cost is rotation, and it is a real one.** A fine-grained PAT expires — a year at most, and
GitHub's default is far less — and when it does, every scheduled run fails with `no_token` until
somebody notices and mints a new one. Nothing in the chain warns as the date approaches, because
nothing in the chain can see it: a token is an opaque string until a request is made with it. A
calendar reminder is the whole mitigation.

A **GitHub App installation token** is the answer to that: minted per run, so there is no expiry to
diarise, with a higher rate limit and an install scoped to selected repositories rather than to
everything the human who owns the PAT can reach. It is deliberately not built yet — the PAT works
today with no code at all — and the reason it can wait is structural. An App token would be a fifth
step in the same `Resolver`, or a value handed to step 1's field, and either way it resolves to the
same `Token` and every call site keeps asking the same question. The chain is the seam that makes
this a later decision instead of a migration.

## Testing

The `Resolver`'s three seams — `LookupEnv`, `ConfigToken`, `CLIToken` — are function fields, nil in
production and stubbed in tests. Precedence cases populate *every source below* the expected winner,
so a passing case proves the ordering rather than merely proving the one populated source was found.

`ghAuthToken` itself is covered against a fake `gh` shell script on a `PATH` built for the test, so
the suite says the same thing on a laptop with a `gh` login as on a runner without one.

---

The design record for the resolution chain, and the CI options it leaves open, is
[design.md § Authentication](https://github.com/specsnl/labelsync/blob/main/docs/design.md#authentication).
