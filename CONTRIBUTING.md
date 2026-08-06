# Contributing Guide

Thank you for your interest in contributing to Prukka. This guide provides the
standards for effective collaboration. The short version: small PRs,
conventional commits, tests with every change, and the linter is law.

## Code of Conduct

This project maintains an open and welcoming environment. All contributors must:

- Use inclusive and professional language
- Respect differing viewpoints and experiences
- Accept constructive criticism gracefully
- Focus on community benefit
- Demonstrate empathy toward other members

Report concerns privately via the repository's
[security advisories](https://github.com/ubyte-source/prukka/security/advisories/new).

## Prerequisites

### Required Tools

- **Host**: macOS or Linux — or Windows through WSL2. The Make-driven workflow
  assumes a POSIX host (`SHELL := /bin/bash`, the `.tools/bin` symlink layout,
  `hack/ci/install-node.sh`), so it does not run under git-bash or PowerShell.
  Windows is a supported *target*: CI cross-builds it and runs the test suite
  on a Windows runner, and the release ships `prukka_windows_amd64.zip`.
- **Go**: 1.26 or later (pinned via the `go` directive in `go.mod`; `GOTOOLCHAIN=auto` fetches it transparently)
- **make**: for development automation
- **Node.js**: only to rebuild the dashboard; pinned and checksum-verified into `.tools/node` by `make tools-node` (never a system install)
- **golangci-lint** and **shellcheck**: pinned and installed by `make tools` — do not install your own; the versions are part of the linter contract

Everything the toolchain needs is pinned into `.tools/` by `make tools`; ffmpeg
installs itself at runtime via `prukka setup`.

### Setup

```bash
# Fork and clone
git clone https://github.com/your-username/prukka.git
cd prukka

# Add upstream remote
git remote add upstream https://github.com/ubyte-source/prukka.git

# Install the pinned toolchain
make tools

# Verify setup
make build
make test
```

## Development Workflow

### Branch Naming

Use descriptive branch names following these conventions:

- `feat/description` — new features or enhancements
- `fix/description` — bug fixes
- `docs/description` — documentation changes
- `perf/description` — performance improvements
- `refactor/description` — code refactoring

### Development Process

```bash
# Create feature branch
git checkout main
git pull upstream main
git checkout -b feat/your-feature

# Make changes and verify locally
make lint
make test

# Commit with clear messages, push to fork
git commit -m "feat: description"
git push origin feat/your-feature

# Create Pull Request
```

### The Entrypoints You Need

```bash
make dev     # daemon + dashboard on http://127.0.0.1:8080/ui/
make build   # stripped, trimmed, PGO binary into bin/prukka
make test    # tests with the race detector
make lint    # the maintainer's linter (zero nolint) + workflows + shell
make lint-all # the linter for Darwin, Linux and Windows
make verify  # the local blocking gate set (see the pre-submission checklist)
make web-audit # known vulnerabilities in the locked Node dependency graph
make help    # every target, including the demo scenarios
```

## Code Standards

### The Rules

- **Conventional commits** (`feat:`, `fix:`, `docs:`, `perf:`, `refactor:`, `chore:`)
- **Small PRs** — one logical unit, ≤ ~400 lines of diff
- Every PR: tests + a docs touch + `make lint` green locally (zero `nolint`)
- The interfaces in `internal/core/ports.go` change only with maintainer sign-off
- TODOs carry issue numbers or don't exist
- Blocked by a linter finding you believe is wrong? Write a minimal repro and ask the maintainer — never a suppression
- Blocked > 30 min on an external unknown? Document your findings in the issue or PR before choosing a path

---

## Engineering Constitution

These rules bind every line in the repository and outrank convenience and
speed. Where they touch style, the maintainer's linter (see "The linter
contract" below) is the final authority.

### DRY — never write the same logic twice

- A piece of *semantic* logic (validation, conversion, retry policy, protocol
  framing, the language registry…) exists in exactly one place; everything
  else calls it.
- Rule of three for *incidental* similarity: don't force an abstraction on
  the first coincidence; extract at the second real recurrence, mandatorily
  at the third.
- The wrong abstraction is worse than duplication: if an extracted helper
  needs mode flags that change behavior per caller, inline it back and
  redesign.
- Cross-platform code: the shared flow is written once; per-OS files (build
  tags) implement only the divergent syscall layer behind one interface.
- Declared single sources of truth: `core/ports.go` (interfaces), `core/lang`
  (language registry feeding GUI dropdowns, CLI and API validation), the
  config schema, the protobuf definitions. Duplicating any of them is a
  review-blocking bug.

### Abstraction & polymorphism, the Go way

- Polymorphism is achieved with **interfaces**, defined on the consumer side,
  kept small (1–3 methods), named for behavior (`Ingress`, `Meter`), never
  for implementation. Composition and embedding replace inheritance; no type
  hierarchies.
- Accept interfaces, return concrete types. An interface with one
  implementation *and* one consumer is indirection, not abstraction — remove
  it (deliberate exception: the AI/media ports, which exist for
  pluggability by design).
- Cross-cutting concerns (metrics, retries, budgets) wrap ports as
  decorators — e.g. `meteredSTT{next STT}` — never leak into business logic.
- Open/closed at the boundaries: adding a provider, transport or output
  format must not modify core — only add an adapter and its registration.
- Generics only when they remove real duplication across ≥2 concrete types
  without obscuring the code; `any` is not an escape hatch.

### Construction & state

- Dependencies are injected via constructors (`NewX(deps…)`). Zero
  package-level mutable state, no `init()` side effects, no singletons.
  Wiring lives only in `cmd/prukka`.
- Every goroutine has an owner and a lifecycle (errgroup/context
  cancellation); channels crossing package boundaries have documented
  ownership and close semantics.
- One-shot teardown has exactly one spelling: a close that returns *its own*
  error is `sync.OnceValue` over the teardown, stored in a `func() error`
  field the constructor installs. `sync.Once` is reserved for one-shot *side
  effects* that produce no result, including a close that only signals and then
  waits for a reader goroutine to publish the stream's terminal error. A
  `sync.Once` beside an error field the method returns is the shape this rule
  replaces.
- `context.Context` is the first parameter of anything that blocks, does I/O
  or can be cancelled.

### Errors & robustness

- Errors are wrapped with `%w` and context; typed/sentinel errors in core for
  programmatic handling; handled exactly once — log *or* return, never both.
- No `panic` outside `main` and programmer-error guards; library code returns
  errors.
- External input is validated at the boundary (language tags, URLs, config);
  internal code trusts validated types.
- An error the caller has decided not to act on is discarded through
  `besteffort.Ignore` — never `_ = f()`, never a `//nolint`. errcheck runs with
  `check-blank`, so the blank form is unavailable wherever the linter can see it.

### One answer per question

Where Go allows two spellings and the choice changes nothing a user can
observe, this repository picks one and says which. Five of the six decisions
are below; the sixth — how an ignored error is spelled — is an error rule and
lives in "Errors & robustness" above.

- A session is identified by a `slug` in every position — parameter, struct
  field, local, interface method, test helper — because `session.Session.Slug`,
  the proto's `string slug`, the `/api/v1/sessions/{slug}` route and
  `prukka session add <slug>` already spell it that way; the bare word
  `session` is left for the value.
- A composite identity is a comparable struct, never a concatenated string:
  `audio.pairID`, `audio.jobID`, `vtt.pairID`, `realtime.LanguagePair`,
  `config.TranslationPair`, `ffmpeg.buildTarget` and `speech.runtimeTarget` key
  their maps directly, and render through a `String()` method. This rule and
  the session-identifier one above have no gate; both are held in review.
- A composite literal fits on one line or gives every field its own line, and
  is never packed across two. `hack/ci/literal-gate.sh` owns the rule;
  `go run ./hack/cmd/literal-gate -w <files>` followed by `gofmt -w` repairs
  what it finds.
- A `return` is preceded by a blank line unless it is the only statement in its
  block; `nlreturn` (`block-size: 1`) owns that rule.
- An error that can reach `v1.Session.Error` names a source through
  `redact.URL` or does not name it at all; foreign text a producer did not
  author is declared with `redact.Untrusted` so the renderer drops an exact
  span instead of guessing which tokens look dangerous. This rule has no gate
  either; it is held in review.

### Comments — minimal, only where they add information

- Godoc on every exported identifier: one sentence, starting with the name.
  This is API surface, not noise, and the linter enforces it.
- Inside function bodies: comment the *why* — invariants, protocol quirks,
  non-obvious tradeoffs, links to the issue. Never the *what*.
- If a block needs a comment to explain what it does, refactor until it
  doesn't.
- Forbidden: commented-out code, journal comments, TODOs without an issue
  number, decorative separators and banners.
- A doc comment defines exactly one thing, and it is the declaration it sits
  on. Two docs merged onto one function, or a rename that left its doc on the
  neighbor, is the same bug.
- `hack/ci/comment-gate.sh` enforces the last two rules over every Go file,
  tests included.

### `hack/` is tooling, held to the same bar

`hack/cmd` holds the build and release tooling and is held to *every* gate the
shipped tree is, the comment doctrine included; there is no exemption. What
differs is only what a linter can reach: everything under `hack/` is
`package main`, which nothing can import, so `revive`'s rule about exported
identifiers seldom has one to bind to. `hack/ci/comment-gate.sh` requires a doc
comment on every *type* there in its place. The rest is judged the way
`internal/` is — and because the release tooling is spec-bound, what a reader
cannot derive is usually the SPDX or archive-format clause being obeyed.

`hack/ci/tooling-boundary-gate.sh` proves with `go list` that every package
under `hack/` is `package main` and that nothing in `cmd/` or `internal/`
reaches one, so `hack/` cannot quietly become a library.

### The linter contract (absolute)

- The maintainer provides `.golangci.yml` and the pinned linter version. They
  change only on explicit maintainer instruction and CODEOWNER review; ordinary
  feature work must never weaken, bypass or self-authorize an exclusion.
- `//nolint` directives: **zero tolerance**. If a finding looks genuinely
  unfixable, stop and hand the maintainer a minimal repro with your
  analysis — a human decides. The agent never self-authorizes a suppression.
- Code adapts to the linter, never vice versa. If linter and spec conflict:
  the linter wins on style, the spec wins on behavior — raise it, don't hack
  it.
- Enforcement:
  - `make lint` runs the pinned golangci-lint with the maintainer's config;
    blocking CI gate on every PR.
  - Config-integrity job: `.golangci.yml` must match the
    maintainer-committed `LINTER.sha256`; any mismatch fails CI.
  - Suppression gate: CI fails if a diff introduces non-allowlisted
    `nolint`/`#nosec` directives.
  - Comment gate, literal gate and tooling-boundary gate: `make comment-check`,
    `make literal-check` and `make tooling-boundary`, all blocking in
    `make verify` and in CI.
  - A blank line before `return`, `break` and `continue` is `nlreturn`'s job:
    `golangci-lint run --fix` applies it.
- Two linters were considered and rejected. Recorded here so the question is
  not reopened by measurement alone:
  - **`dupl` at a lower threshold** cannot find cross-package clones at any
    setting: golangci-lint runs it one package at a time. Lowering it buys only
    intra-package noise (571 findings at 25). It stays at 150.
  - **`godoclint`'s `start-with-name`** adds nothing `revive`'s `exported` rule
    does not already enforce (0 new findings), and its golangci-lint
    integration hardcodes `StartWithNameIncludeTests` to false, so it cannot
    see a test file at all. `hack/ci/comment-gate.sh` covers what it cannot.
  - Shell is held to the same contract: `make lint-shell` runs the pinned
    shellcheck (`tools/versions.mk`) over every tracked `*.sh`, and a
    `# shellcheck disable=` is allowed only on the single line it applies to,
    with the reason on it — never a file-wide or repository-wide exclusion,
    with `engine/pins.sh` the one exception a pure pin table earns.
  - `CODEOWNERS`: linter files, tools and their CI enforcement require
    maintainer review.
- Generated code (protobuf, gateway) is excluded only through the config the
  maintainer already ships; if it trips lint, the agent *requests* a config
  change — it never makes one.

### Tests are part of the code

- Table-driven and parallel where safe; assert behavior, not implementation;
  race detector always on in CI; the bench gates are blocking. Every
  bugfix lands with the regression test that would have caught it.

---

## Linting and Static Analysis

### Configuration

The repository uses golangci-lint configured in `.golangci.yml`, pinned and
byte-anchored by `LINTER.sha256`. It is maintainer-owned: code adapts to the
linter, not the reverse.

```bash
make lint             # the pinned linter for the host GOOS
make lint-all         # the pinned linter for Darwin, Linux and Windows
make lint-integrity  # verify .golangci.yml matches its anchor
make lint-shell      # the pinned shellcheck over every tracked *.sh
```

### NOLINT Usage

Zero tolerance. The only `//nolint` directives in the tree are maintainer
decisions on the suppression allowlist: `internal/media/wasapi` (Windows COM
interop), `internal/speechengine` (the native orchestrator execs bundle-resolved
tools and converts PCM widths), and the two files
`internal/update/replace.go` / `replace_windows.go` (the staged binary must be
owner-executable; Windows retirement passes a Win32 struct pointer to
`SetFileInformationByHandle`). `hack/ci/suppression-gate.sh` is authoritative —
that list is derived from it. `#nosec` is never allowed. If you believe a
finding is wrong, open an issue with a minimal repro — do not add a
suppression.

## Testing

### Test Requirements

- **Unit tests**: cover every new function and method
- **Race Detection**: the race detector is always on in CI (`make test`)
- **Performance Gates**: the hot-path zero-allocation gate is blocking in CI
  (`make bench`); `make load` is the real-engine acceptance gate for release
  environments that provide the pinned helper, models and FFmpeg
- **Table-Driven Tests**: use for multiple input/output combinations
- **Regression First**: every bugfix lands with the test that would have caught it
- **Real Dependencies**: prefer real ffmpeg (pinned) and the live provider over mocks where they are the system under test

### Running Tests

```bash
make test            # all tests with the race detector
make bench           # hot-path benchmarks + zero-alloc gate
make load            # it→en load: per-lane captions + decoded voice-only HLS
make web-e2e         # dashboard cross-browser e2e (Playwright)
make cover           # coverage for the core packages
```

## Pull Request Process

### Pre-submission Checklist

`make verify` is the local blocking set: module graph, lint (Go, workflows and
shell), tests, benches, coverage, generated-code sync, and the proto and
bundled-driver gates. It is a subset of CI, which *also* blocks on `buf lint`,
`govulncheck` across three build contexts, the six-target cross-build matrix,
the three acceptance demos and the full web set (`make web`, `web-audit`,
`licenses-check`, `web-e2e`) — those stay out of `verify` because they need a
network tool install or repeat a check it already runs. Run what your change
touches before pushing.

- [ ] All tests pass (`make test`)
- [ ] Code passes linting with zero suppressions (`make lint`; CI runs
      `make lint-all` across Darwin, Linux and Windows)
- [ ] Shell changes: `make lint-shell` (shellcheck, pinned like every other
      linter; workflow `run:` blocks are actionlint's job)
- [ ] Performance and coverage gates green (`make bench`, `make cover-gate`)
- [ ] Dependency changes: `make mod-check`, and `govulncheck ./...` if you can
- [ ] Proto changes: `make gen-check` leaves `internal/gen/` unchanged and
      `make proto-breaking` passes
- [ ] Dashboard changes: `make web` rebuilds `internal/webui/dist`
      byte-identically, and `make web-audit`, `make licenses-check` and
      `make web-e2e` pass
- [ ] Documentation updated (README / architecture / relevant `.md`)
- [ ] Regression test added for any bugfix
- [ ] Rebased on latest `main` with a clean commit history

```bash
git fetch upstream
git rebase upstream/main
```

### Pull Request Template

**Title**: clear, conventional summary (e.g. `feat: hedge STT past observed p95`)

```markdown
## Changes
- Technical implementation details
- Modified components

## Motivation
- Problem being solved
- Relevant issue links

## Impact
- Breaking changes (if any)
- Performance implications

## Testing
- Test approach and coverage additions
- Benchmark results (for performance changes)
```

### Review Process

1. **Initial Review**: maintainer reviews for adherence to the Constitution
2. **Feedback**: address comments and push updates
3. **Approval**: maintainer approval required for merge
4. **Merge**: merged to `main`; feature branch deleted

## Issue Reporting

### Bug Reports

**Environment**: Go version (`go version`), OS and version, install method,
`prukka doctor` output.

**Reproduction**: steps, expected behavior, actual behavior, relevant daemon
log lines and configuration.

### Feature Requests

Describe the use case and motivation, the proposed solution, alternatives
considered, and the affected components.

### Security Issues

**DO NOT** create public issues for security vulnerabilities. Report them
privately via [SECURITY.md](SECURITY.md).

## Git Commit Messages

- Use conventional-commit prefixes and imperative mood ("Add feature" not "Added feature")
- First line: a concise summary (≤ ~72 characters)
- Separate subject from body with a blank line
- Body: explain *what* and *why*, not *how*
- Reference issues and PRs

Example:

```
fix(dispatch): close the Submit/Close acceptance race

A Close could pass inflight.Wait between a submitter's open check and its
Add, stranding an accepted job. The accept edge is now a mutex-guarded
critical section; the lock-free ring data plane is unchanged.

Fixes #123
```

## Contribution License

By contributing, you agree that your contributions will be licensed under the
license governing the path you modify: GPL-2.0-only under `drivers/linux/`,
and GPL-3.0-or-later everywhere else.

## Additional Resources

- [Project README](README.md)
- [Architecture Documentation](docs/ARCHITECTURE.md)
- [Release Procedure](docs/RELEASING.md)
- [Go Documentation](https://go.dev/doc/)
- [golangci-lint](https://golangci-lint.run/)
