# Releasing

Releases are requested from `main`; pushing a tag never executes a privileged
workflow. The trusted `Release` workflow is loaded from the default branch,
revalidates the requested tag, runs every acceptance gate, builds without
publish credentials, attests the archives, and only then stages and publishes
the release.

## Required repository policy

Configure these controls before the first release:

1. Protect `main` with required reviews and status checks; prohibit force pushes
   and deletion.
2. Add a repository ruleset for tags matching the release SemVer pattern. Limit
   tag creation to release maintainers and prohibit tag updates and deletion.
3. Create a `release` environment. Restrict it to `main`, require a maintainer
   approval, and prevent administrator bypass where the repository policy
   permits. The build needs no provider or cloud secrets.
4. Restrict Actions to approved, SHA-pinned actions. Keep workflow approval and
   token defaults read-only.

The workflow verifies that the tag is canonical SemVer, that its commit is an
ancestor of `origin/main`, and that both the tag object and peeled commit remain
unchanged before build and publication. Repository tag protection is still the
authority that makes the reference immutable between workflow runs.

## Procedure

Before tagging, attach the manual accessibility evidence required by
[`ACCESSIBILITY.md`](ACCESSIBILITY.md) to the release review. The protected
`release` environment approver verifies that evidence and any accepted
residuals.

Create the release tag from the reviewed commit, then invoke the request
workflow explicitly from `main`:

```bash
git tag 1.2.3
git push origin refs/tags/1.2.3
gh workflow run release-request.yml --ref main -f tag=1.2.3
```

The release remains private until all five archives exist and their provenance
attestations have been recorded. A failed upload leaves a workflow-managed
draft; retrying the request safely replaces only that managed draft. A published
release is never replaced.

Verify a downloaded archive independently:

```bash
gh attestation verify prukka_<os>_<arch>.<ext> -R ubyte-source/prukka
```

## Engine artifacts

The native inference tools (whisper.cpp / whisper-server, the CTranslate2
Opus-MT `mt` runtime, and Piper) and the model packs ship as assets of the
same prukka SemVer release as the daemon — the release whose tag equals the
version — so `prukka setup` and the dashboard resolve them against the
running daemon's own version. After the daemon release is cut, the
`build-engine` workflow — dispatched with that release tag — builds the macOS
runtime archives on both architectures, converts and packages the model packs,
generates `prukka-engine-catalog.json` (SHA-256 and size for every asset)
plus `ENGINE-NOTICE.txt`, and attaches them to that same release, verifying
each asset against the catalog. The
speech-engine orchestrator itself is not a separate binary: it is the
`internal/engine` package, run as hidden `stt`/`mt`/`tts` subcommands that the
single `prukka` binary self-executes. Native tools are built for macOS (amd64
and arm64) only today; Linux and Windows native builds remain future work, so
out-of-box speech is macOS-only. `prukka setup` and the dashboard download
exclusively through the catalog and verify every artifact before staging it.
