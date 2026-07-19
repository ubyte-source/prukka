# Managed FFmpeg runtime

Prukka invokes FFmpeg as a separate executable process. It does not link
FFmpeg libraries into the Prukka binary. On a supported platform,
`prukka setup` always installs the platform pin declared in
[`internal/media/ffmpeg/builds.go`](../internal/media/ffmpeg/builds.go).
Runtime resolution prefers that verified managed install and falls back to an
external `PATH` executable only when no complete managed install exists.

Every managed pin records:

- the FFmpeg version and full source commit;
- the vendor, exact archive URL and SHA-256;
- the matching FFmpeg source URL and SHA-256;
- the immutable build-recipe URL and revision;
- the exact build command and vendor build-information URL; and
- the resulting executable SHA-256.

Setup installs `ffmpeg-build.json`, `FFMPEG-NOTICE.txt` and the official
GPL-3.0 text beside the executable. The directory is content-addressed by the
archive checksum. Setup prepares and syncs the complete directory before an
atomic rename; a failed download, verification, extraction or publication
leaves the previous install intact. Other content-addressed pins are retained
because another installed Prukka version or running process may still use
them. The platform uninstaller's purge mode removes the containing Prukka
state directory, including every retained pin.

The former flat `state/bin/ffmpeg` layout has no manifest or executable hash
and is therefore not trusted as a managed install. `Resolve` fails closed with
a `prukka setup` migration instruction. Demo fixtures expose a known external
FFmpeg through `PATH`; they never manufacture a managed install with a symlink.
Automation uses `prukka setup --print-path`, which suppresses progress and
prints only the verified executable path.

## Pin provenance

Linux and Windows use the BtbN `gpl 8.1` build matrix at one immutable release
and recipe revision. macOS uses the Martin Riedl 8.1.2 release build for both
architectures at one immutable recipe revision. Both recipes enable GPL and
version-3 components. The previous Evermeet macOS pin is not used because no
matching immutable build recipe was established for that archive.

The build map and installed manifest are the source of truth for exact URLs,
hashes and commands. The vendor build-information link captures the reported
FFmpeg configuration and dependency versions for each artifact.

## Distribution and mirror gate

The manifest records distribution mode
`upstream-direct-download/not-distributed-in-prukka-release`: Prukka release
archives do not contain FFmpeg, and explicit setup downloads the selected
binary directly from its upstream vendor. The separate mirror status is
`blocked-pending-complete-corresponding-source`.

Before Prukka mirrors or republishes any pinned FFmpeg binary, the release
owner must archive and publish a verified source bundle matching that binary:
FFmpeg, every statically linked dependency, the complete build scripts and the
exact build configuration. That bundle must remain associated with the mirrored
binary and its manifest. Until this gate is satisfied, download remains direct
from upstream; operators can also use a separately installed FFmpeg.

This is a project release-control requirement, not legal advice. A distributor
is responsible for its own compliance review. FFmpeg's upstream licensing and
compliance notes are at <https://ffmpeg.org/legal.html>.
