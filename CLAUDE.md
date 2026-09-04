# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Go Review Kit is a library (not a standalone binary) that provides the engine for AI review of git
diffs: open a repo, extract a diff between two refs, check out the head, run a caller-supplied
`review.WorkspaceReviewer` over the worktree, hand the typed `review.Report` to a caller-supplied
`review.Publisher`, and notify. Since v1.3.0 the kit ships
**no AI adapter** — reviewer implementations (and the AI SDK choice) live in the consuming app.
It is consumed by the downstream app `adk-review` (Web/Worker), which supplies the concrete
adapters and owns the process entrypoint.

`README.md` has a 動作の約束 section listing the guarantees callers are allowed to rely on (empty
diff is not a failure, Publisher only runs on success, Notifier runs exactly once per `Run`, publish
and notify each detach from the caller's deadline with their own budget). Those are contract, not
incidental behaviour — read it before changing `pipeline`. It names no consuming repository and
pins no model version: both go stale, and the consumer list lives in `public-docs/libraries.md`.

## Commands

```bash
# Build
go build ./...

# Run all tests (race detector is used in CI)
go test -race ./...

# Run tests for one package
go test ./pipeline/... -v

# Vet / format
go vet ./...
gofmt -l .

# Lint (pinned version, matches CI)
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...

# Vulnerability check
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

There is no Makefile. CI (`.github/workflows/ci.yml`) runs Build & Test, Lint, and govulncheck as
separate jobs on every push/PR to `main`/`develop`.

## Architecture

Hexagonal (ports and adapters), strictly layered:

- **`review`** — the only package every other package may depend on, and it depends on nothing in
  this module. Holds the domain types (`Request`, `Report`/`Verdict`/`Finding`/`Severity`/`Decision`,
  `Result`/`Status`), the sentinel errors (`ErrEmptyDiff`, `ErrDiffTooLarge`, `ErrRefNotFound`,
  `ErrInvalidReport`, …), `StepError` (which carries the failing step name — there is no separate
  step field anywhere), and every port. `ParseReport` returns a `ParseInfo` alongside the report,
  saying whether the output had to be repaired (nothing lost) or was truncated and only partly
  recovered (**data dropped** — a caller ignoring `Truncated` publishes an incomplete review as a
  complete one). Ports are deliberately 1–2 methods each:
  `WorkspaceReviewer` (the only reviewer kind — it inspects the checked-out worktree and
  reports a `RunInfo` alongside the report),
  `DiffSource` (`Diff` + `CheckoutHead` + `Close`), `DiffSourceFactory`, `PromptGenerator`,
  `Publisher`, `Notifier`.
  Changing a signature here ripples into every adapter and into `adk-review` — check that repo
  before doing so.
- **`pipeline`** — `Pipeline.Run(ctx, Request) (Result, *Report, error)` is the single
  orchestration entrypoint; there is no second layer under it. The `*Report` exists so callers
  that need the review's contents (recording job state, say) can read them from the return value
  instead of implementing a `Notifier` to intercept them — `Notifier` is for outward notification,
  which is the only thing that needs the detached context. Dependencies arrive as a `Deps` struct (not
  positional args, so a mis-ordered interface can't compile clean). Only a genuine successful review
  is published (`Publisher.Publish`); empty-diff and error outcomes are notified only. Empty diff is
  **not** a failure: `Run` returns `StatusSkipped` with a nil error.
- **`git`** — two interchangeable `DiffSource` implementations, selected by the caller (not by this
  package): `GoGit` (pure Go, `go-git`, deletes its workdir on `Close`, good for serverless) and
  `CLI` (shells out to the local `git` binary, restores the base ref and cleans instead of deleting,
  good for local dev/CI where a reusable checkout matters). `Factory` picks the workdir under a root
  via `RepoDirName`. Note the package imports go-git aliased as `gogit` — the package names collide.
  **SSH URLs only** (scp form, `ssh://`, or a local path): auth is SSH-key only, so `Prepare` refuses
  `http(s)` rather than falling through to anonymous access that fails on anything private.
  `RepoDirName` appends a hash of the normalized URL — two repositories sharing a workdir means one
  repository's branch gets reviewed and published as the other's. The package takes **no lock**, so
  concurrent reviews of the *same* repository need `WithDirNamer` to keep them apart.
There is deliberately **no reviewer or publisher implementation here**. The reviewer decides the
AI SDK (ADK, a raw SDK client, or anything else) and the publisher decides how a report is
represented and stored — both are the consuming app's choices, so shipping either would force a
dependency onto every consumer. The former `gemini` package was removed in v1.3.0 and reviewers
have lived in the consuming app ever since. Direct dependency is `go-git` only — keep it that way.

## Working conventions

- Comments, doc comments, error messages, and README are Japanese. Commit messages are concise
  English.
- Comments should explain *why*, not restate the code. Several comments in this repo record a
  decision and the failure it prevents (e.g. why refs resolve remote-branch-first, why publishing and
  cleanup detach from the caller's deadline) — keep that style and don't strip those explanations.
- This repo has no `main` package; nothing here talks to a real repo URL or Gemini API at runtime
  outside of tests. Prefer table-driven tests with the existing `fake*` types in each package's
  `*_test.go` files over adding new mocking machinery. `git`'s tests build real repositories in
  `t.TempDir()`; the `CLI` ones skip when the `git` binary is absent (`requireGitBinary`).
- Enum values live in `review` only (`Severities()` / `Decisions()`, plus the `SeverityStrings()` /
  `DecisionStrings()` forms the AI SDKs want for a schema `Enum`). Consumers build their output
  schemas from those — never hard-code the strings a second time, and don't re-derive `[]string`
  in the consumer either.
- Timeouts have two knobs and they cover different ranges: `WithRunTimeout` bounds the review
  itself, `WithPublishTimeout` bounds publish/notify/cleanup, which run on detached contexts.
  A caller wrapping `Run` in its own deadline defeats the detach — that is what `WithRunTimeout`
  is for.
- Wrap errors with `%w`, and attach the step with `review.WrapStep` at the pipeline boundary so
  `errors.Is` and `review.StepOf` both keep working.
- Keep `README.md`'s package table and sequence diagram in sync with `review`/`pipeline` — they are
  the authoritative architecture reference. The diagram is easy to let drift: `DiffSource.Close`
  runs on `produce`'s defer, so it fires *before* publish, not after. The per-file project tree it
  used to carry is gone: it restated the package layout godoc already shows, and the README's job
  is the 動作の約束, not a directory listing.
- This module is consumed via semver tags (no `replace` directive). A breaking change here
  (e.g. removing/renaming an exported `review` type or a constructor like `git.NewCLIFactory`)
  requires: commit → tag a new version → bump `go.mod` in `adk-review` → fix its call sites.
  Don't assume a local edit is visible to that repo until that whole sequence has happened.
  With adk-review as the sole consumer, pragmatic breaking changes within v1 are accepted
  (v1.3.0 removed the `gemini` package this way).

## Conventions

- **Error text**: sentinel errors are English with a package prefix (`review: diff is empty`) so a deeply wrapped error still names its origin; the context added by `fmt.Errorf` wrapping is Japanese. Existing English wrap text is not being retrofitted — apply the rule to code you touch.
