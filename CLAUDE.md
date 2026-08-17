# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Go Review Kit is a library (not a standalone binary) that provides the engine for AI review of git
diffs: open a repo, extract a diff between two refs, run a caller-supplied reviewer
(`review.Reviewer` or the workspace-inspecting `review.WorkspaceReviewer`), hand the typed
`review.Report` to a caller-supplied `review.Publisher`, and notify. Since v1.3.0 the kit ships
**no AI adapter** — reviewer implementations (and the AI SDK choice) live in the consuming app.
It is consumed by the downstream app `adk-review` (Web/Worker), which supplies the concrete
adapters and owns the process entrypoint.

`README.md` has a 動作の約束 section listing the guarantees callers are allowed to rely on (empty
diff is not a failure, Publisher only runs on success, Notifier always runs exactly once, publish
and cleanup detach from the caller's deadline). Those are contract, not incidental behaviour — read
it before changing `pipeline`.

An older library, `gemini-reviewer-core`, covered the same ground and still exists at its own module
path. It is **not** kept in sync with this one; do not copy code or API shapes between them.

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
  `Result`/`Status`), the sentinel errors (`ErrEmptyDiff`, `ErrRefNotFound`, `ErrInvalidReport`, …),
  `StepError` (which carries the failing step name — there is no separate step field anywhere), and
  every port. Ports are deliberately 1–2 methods each: `Reviewer`, `WorkspaceReviewer` (agent-style
  reviewer that inspects the checked-out worktree; exactly one of the two goes into
  `pipeline.Deps`), `DiffSource` (+ the optional `WorkspaceProvider` capability — `CheckoutHead` —
  which the pipeline demands via type assertion only when `WorkspaceReviewer` is configured),
  `DiffSourceFactory`, `PromptGenerator`, `Publisher`, `Notifier`. Changing a signature here ripples
  into every adapter and into `adk-review` — check that repo before doing so.
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
There is deliberately **no reviewer or publisher implementation here**. The reviewer decides the
AI SDK (Gemini via go-gemini-client, ADK, or anything else) and the publisher decides how a report
is represented and stored — both are the consuming app's choices, so shipping either would force a
dependency onto every consumer. The former `gemini` package moved to `adk-review`'s
`internal/adapters` in v1.3.0. Direct dependency is `go-git` only — keep it that way.

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
- Enum values live in `review` only (`Severities()` / `Decisions()`). Consumers build their
  AI output schemas from them (see adk-review's `internal/adapters/gemini_schema.go`) — never
  hard-code the strings a second time.
- Wrap errors with `%w`, and attach the step with `review.WrapStep` at the pipeline boundary so
  `errors.Is` and `review.StepOf` both keep working.
- Keep `README.md`'s package table and project tree in sync with `review`/`pipeline` when adding or
  renaming packages — it's the authoritative architecture reference.
- This module is consumed via semver tags (no `replace` directive). A breaking change here
  (e.g. removing/renaming an exported `review` type or a constructor like `git.NewCLIFactory`)
  requires: commit → tag a new version → bump `go.mod` in `adk-review` → fix its call sites.
  Don't assume a local edit is visible to that repo until that whole sequence has happened.
  With adk-review as the sole consumer, pragmatic breaking changes within v1 are accepted
  (v1.3.0 removed the `gemini` package this way).
