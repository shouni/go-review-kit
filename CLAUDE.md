# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Go Review Kit is a library (not a standalone binary) that provides the engine for AI review of git
diffs: open a repo, extract a diff between two refs, generate a structured review via Gemini
(ResponseSchema-constrained; not limited to code — see `gemini/schema.go`), publish the result
(JSON→HTML via `go-prompt-kit`'s `jsonconverter`) to storage, and notify. It is consumed by the
downstream app `git-gemini-web` (Web), which supplies the concrete adapters and owns the process
entrypoint.

This module is a complete redesign of `gemini-reviewer-core`, not a port of it. That repo still
exists at its own module path and is **not** kept in sync — do not copy code or API shapes back and
forth. `README.md` documents every intentional divergence in its 設計上の変更点 table; read it before
making non-trivial changes.

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
  every port. Ports are deliberately 1–2 methods each: `Reviewer`, `DiffSource`,
  `DiffSourceFactory`, `PromptGenerator`, `Publisher`, `Notifier`. Changing a signature here ripples
  into every adapter and into `git-gemini-web` — check that repo before doing so.
- **`pipeline`** — `Pipeline.Run(ctx, Request) (Result, error)` is the single orchestration
  entrypoint; there is no second layer under it. Dependencies arrive as a `Deps` struct (not
  positional args, so a mis-ordered interface can't compile clean). Only a genuine successful review
  is published (`Publisher.Publish`); empty-diff and error outcomes are notified only. Empty diff is
  **not** a failure: `Run` returns `StatusSkipped` with a nil error.
- **`git`** — two interchangeable `DiffSource` implementations, selected by the caller (not by this
  package): `GoGit` (pure Go, `go-git`, deletes its workdir on `Close`, good for serverless) and
  `CLI` (shells out to the local `git` binary, restores the base ref and cleans instead of deleting,
  good for local dev/CI where a reusable checkout matters). `Factory` picks the workdir under a root
  via `RepoDirName`. Note the package imports go-git aliased as `gogit` — the package names collide.
- **`gemini`** — `Reviewer`, the only `review.Reviewer` implementation, wraps `go-gemini-client/gemini`
  (imported aliased as `geminiclient`, same collision). It decodes the model output into a
  `review.Report` exactly once; no other layer sees the raw JSON.
- **`publish`** — JSON→HTML conversion (via `go-prompt-kit/md/jsonconverter`, templated in
  `publish/assets/report.html` + `report.css`) and storage writes behind `review.Publisher`.

## Working conventions

- Comments, doc comments, error messages, and README are Japanese. Commit messages are concise
  English.
- Comments should explain *why*, not restate the code. Several comments in this repo record a
  decision and the failure it prevents (e.g. why refs resolve remote-branch-first, why publish and
  cleanup detach from the caller's deadline) — keep that style and don't strip those explanations.
- This repo has no `main` package; nothing here talks to a real repo URL or Gemini API at runtime
  outside of tests. Prefer table-driven tests with the existing `fake*` types in each package's
  `*_test.go` files over adding new mocking machinery. `git`'s tests build real repositories in
  `t.TempDir()`; the `CLI` ones skip when the `git` binary is absent (`requireGitBinary`).
- Enum values live in `review` only (`Severities()` / `Decisions()`). `gemini/schema.go` builds the
  ResponseSchema from them — never hard-code the strings a second time.
- Wrap errors with `%w`, and attach the step with `review.WrapStep` at the pipeline boundary so
  `errors.Is` and `review.StepOf` both keep working.
- Keep `README.md`'s package table and project tree in sync with `review`/`pipeline` when adding or
  renaming packages — it's the authoritative architecture reference.
- This module and `go-gemini-client` are versioned independently (semver tags, no `replace`
  directive). A breaking change here (e.g. removing/renaming an exported `review` type or a
  constructor like `git.NewCLIFactory`) requires: commit → tag a new version → bump `go.mod` in
  `git-gemini-web` → fix its call sites. Don't assume a local edit is visible to that repo until
  that whole sequence has happened.
