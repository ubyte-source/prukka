# Deployment

Service definitions are **generated, not copied** — one source of truth: the
daemon renders exactly what it installs.

```bash
# See what would be installed on this platform:
prukka service install --print

# Install and start (systemd on Linux, launchd on macOS, SCM on Windows):
sudo prukka service install --now

# Inspect / remove:
prukka service status
sudo prukka service remove
```

The unit survives reboots (`WantedBy=multi-user.target`, `RunAtLoad`,
`StartType: automatic` respectively) and restarts the daemon on failure.

Prukka is a single binary and an OS service — there is no container or
orchestration layer. The one-command installers ([`install.sh`](install.sh),
[`install.ps1`](install.ps1)) wrap the download, `prukka setup` and the service
install; OS packages (deb/rpm/pkg/msi) build on top of them. The hosted
dashboard, if you run one, is a static bundle — see [`web/`](web/).
