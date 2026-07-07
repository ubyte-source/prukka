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

- **Go**: 1.26 or later (pinned via the `go` directive in `go.mod`; `GOTOOLCHAIN=auto` fetches it transparently)
- **make**: for development automation
- **Node.js**: only to rebuild the dashboard; pinned and checksum-verified into `.tools/node` by `make tools-node` (never a system install)
- **golangci-lint**: pinned and installed by `make tools` — do not install your own; the version is part of the linter contract

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
make lint    # the maintainer's linter (zero nolint)
make lint-all # the linter for Darwin, Linux and Windows
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

Everything below is the Engineering Constitution. It binds every line in
this repository.

---

## Engineering Constitution

These rules bind every line in the repository. They outrank convenience and
speed. Violations found in review are bugs. Where they touch style, the
maintainer's linter (see "The linter contract" below) is the final authority.

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
- `context.Context` is the first parameter of anything that blocks, does I/O
  or can be cancelled.

### Errors & robustness

- Errors are wrapped with `%w` and context; typed/sentinel errors in core for
  programmatic handling; handled exactly once — log *or* return, never both.
- No `panic` outside `main` and programmer-error guards; library code returns
  errors.
- External input is validated at the boundary (language tags, URLs, config);
  internal code trusts validated types.

### Comments — minimal, only where they add information

- Godoc on every exported identifier: one sentence, starting with the name.
  This is API surface, not noise, and the linter enforces it.
- Inside function bodies: comment the *why* — invariants, protocol quirks,
  non-obvious tradeoffs, links to the issue. Never the *what*.
- If a block needs a comment to explain what it does, refactor until it
  doesn't.
- Forbidden: commented-out code, journal comments, TODOs without an issue
  number, decorative separators.

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
byte-anchored by `LINTER.sha256`. It is maintainer-owned (see "The linter
contract" above): code
adapts to the linter, not the reverse.

```bash
make lint             # the pinned linter for the host GOOS
make lint-all         # the pinned linter for Darwin, Linux and Windows
make lint-integrity  # verify .golangci.yml matches its anchor
```

### NOLINT Usage

Zero tolerance. The only `//nolint` directives in the tree are in the
performance allowlist (`internal/ring`, `internal/media/wasapi`) and are
maintainer decisions. `#nosec` is never allowed. If you believe a finding is
wrong, open an issue with a minimal repro — do not add a suppression.

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

- [ ] All tests pass (`make test`)
- [ ] Code passes linting (`make lint`) with zero suppressions
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
and Apache-2.0 everywhere else.

## Additional Resources

- [Project README](README.md)
- [Architecture Documentation](docs/ARCHITECTURE.md)
- [Release Procedure](docs/RELEASING.md)
- [Go Documentation](https://go.dev/doc/)
- [golangci-lint](https://golangci-lint.run/)
