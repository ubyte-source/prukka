# Deployment

Service definitions are **generated, not copied** — one source of truth: the
daemon renders exactly what it installs.

```bash
# See what would be installed on this platform:
prukka service install --print

# Install and start. The daemon runs as you and uses a per-user control
# socket, so no sudo is needed on any OS:
prukka service install --now

# Inspect / remove:
prukka service status
prukka service remove

# Restart after a manual binary swap (prukka update does this itself):
prukka service restart
```

The service starts at login and restarts the daemon on failure: a launchd
agent on macOS, a systemd user unit on Linux, a scheduled logon task on
Windows.

The virtual devices are the one privileged step. Never run `prukka` from
`~/.local/bin`, `%LOCALAPPDATA%` or another user-writable directory with
`sudo`/Administrator rights. The release installers cross that boundary with
a verified private stage: `install.sh` uses a root-created temporary directory;
`install.ps1` uses an Administrator/SYSTEM-only `%ProgramData%` directory and
UAC. Rerun the installer to retry device setup safely.
Custom archive URLs are accepted only with an explicit
`PRUKKA_INSTALL_SHA256` pin; a mirror-supplied checksum is not a trust anchor,
so custom archive code is never used for privileged device setup. Install an
official release when virtual-device installation is required.

```bash
prukka devices status
prukka-uninstall  # fixed platform cleanup; prompts for privilege when needed
```

The Prukka control daemon/CLI is one binary and runs as a per-user service;
there is no container or orchestration layer. Functional speech also needs the
separately supplied helper, native inference tools and models, while
`prukka setup` downloads FFmpeg. The one-command installers
([`install.sh`](install.sh), [`install.ps1`](install.ps1)) wrap the verified
daemon download, `prukka setup`, service install and staged device install; OS packages
(deb/rpm/pkg/msi) build on top of them. The hosted dashboard, if you run one,
is a static bundle — see [`web/`](web/).

Published Windows packages are x64-only until every bundled driver has a
native ARM64 build. The PowerShell installer rejects Windows ARM64 before it
downloads an incompatible archive.

## Uninstall

The uninstallers stop the daemon, remove the service, unregister every
Prukka-managed virtual device, remove the executable and undo the Windows
user-PATH entry. Run them as the regular user who installed Prukka. On Windows,
the `%LOCALAPPDATA%` script remains non-elevated and requests UAC for the
verified, Administrator-owned copy installed in `%ProgramData%`; that trusted
copy never launches the user-writable `prukka.exe` and uses only fixed Windows
PnP and registry cleanup. Service, process and profile cleanup remains in the
non-elevated user process. After successful device cleanup, its protected
ProgramData copy becomes a completion record so an interrupted user cleanup is
safe to retry; the next verified install removes that record before reinstalling
devices.

```bash
prukka-uninstall             # keep configuration, state and logs
prukka-uninstall --purge     # remove all Prukka-owned user data too
```

```powershell
& "$env:LOCALAPPDATA\Prukka\bin\prukka-uninstall.ps1"
& "$env:LOCALAPPDATA\Prukka\bin\prukka-uninstall.ps1" -Purge
```

Release installers place these scripts beside the executable; from a source
checkout the equivalent entry points are `deploy/uninstall.sh` and
`deploy/uninstall.ps1`.

If an interrupted update left the executable missing or unusable, each script
falls back to the platform service and driver tools. User cleanup still runs;
any privileged artifact that could not be removed is named exactly and the
uninstaller exits nonzero.

`purge` validates every path component before changing the machine, rejects
symlinks, junctions and other reparse points, and deletes only directories
whose final component is `Prukka`. Custom config files are removed only when
their basename is `config.yaml` and their physical parent remains inside the
installing user's profile. Unix fixture-only system-root overrides require the
exact `PRUKKA_DEPLOY_TEST_MODE=prukka-deploy-fixtures-v1` sentinel and are
confined below `PRUKKA_TEST_ROOT`; that mode never invokes `sudo`, and
production runs reject the overrides. Kernel signing
keys and MOK enrollments remain user-managed. Test-signed Windows audio
packages are removed by exact Prukka hardware ID, provider and original INF
name.
