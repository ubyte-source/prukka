# Security policy

## Supported versions

Prukka is under active development. Security fixes target the latest tagged
version and `main`; older pre-release builds may not receive backports. Verify
the issue on a current revision before reporting when it is safe to do so.

## Report a vulnerability

Do not open a public issue or attach secrets, stream URLs, tokens, personal data
or exploit details to a public discussion.

Use [GitHub private vulnerability reporting](https://github.com/ubyte-source/prukka/security/advisories/new)
and include:

- affected version/commit and operating system;
- impact and required attacker position;
- minimal reproduction steps or a proof of concept;
- whether a control token, privileged driver operation or untrusted media input
  is involved; and
- a proposed mitigation, if known.

The maintainers will acknowledge the report, reproduce it and coordinate a fix
and disclosure timeline. Do not test against systems or data you do not own or
have permission to assess.

## Security boundaries

The default deployment assumes one trusted local user and an HTTP listener on
`127.0.0.1`. It is not designed to be exposed directly to an untrusted network.

- gRPC uses a UNIX-domain socket or Windows named pipe and a per-install
  control token. The token file is created with mode `0600` on platforms that
  support POSIX permissions.
- Every REST mutation under `/api/v1/` requires the same token, and so do the
  four sensitive reads that can carry local paths or provider configuration:
  `/api/v1/config`, `/api/v1/devices`, `/api/v1/engine` and the diagnostic
  `/api/v1/doctor`. The remaining `/api/v1/` reads — the session list, daemon
  stats and the language registry — are deliberately unauthenticated and rely on
  the loopback boundary; their payloads carry a sanitised source label instead
  of source credentials. The events stream likewise serves session lifecycle
  events without the token and adds engine progress only for a client that
  proves it. Media endpoints and other non-API HTTP reads are intentionally
  unauthenticated for the same reason. In particular, a network-reachable
  listener can disclose the session inventory, daemon stats, the language
  registry, HLS, direct audio and WebVTT output without the control token.
- A host-header guard protects loopback binds against foreign hosts. CORS is a
  browser control, not authentication and not a network access-control layer.
- The dashboard can adopt a 64-hex-character token from the URL fragment into
  browser `sessionStorage`, then removes the fragment. Browser extensions,
  copied URLs, screenshots and a compromised hosted origin remain relevant.
- Source and output URLs can contain credentials. API/session event responses
  use a sanitised source label, but operators must still protect configuration,
  process arguments, logs and support bundles.
- RTMP/SRT/device/file inputs and output destinations cross trust boundaries.
  Their authentication, encryption and availability properties depend on the
  selected endpoint and operator configuration.
- `providers.local.bin` executes an operator-supplied native program. Its
  executable, dependent libraries and models are part of the trusted computing
  base and must be obtained, verified and patched separately.
- `prukka setup` (and the dashboard) downloads the pinned FFmpeg runtime and,
  on every platform with a published runtime, installs and SHA-256-verifies the
  managed native speech tools and model packs. Those assets come from prukka's
  own GitHub release, the one whose tag
  matches the daemon version, checked against the `prukka-engine-catalog.json`
  asset of that same release. On platforms without a native build the speech
  tools are not installed and must be operator-supplied via
  `providers.local.bin`.
- Virtual-device installation crosses an administrative/kernel boundary.
  Driver packaging, signing, permissions and uninstall behaviour are in scope.
- `prukka update` is explicit. Its release metadata, checksums, archive
  extraction and atomic replacement path are security-sensitive.

Prukka has no current provider API-key or OS-keychain feature. The sensitive
values it does handle include the control token, source/output URLs and the
personal data carried by live media and transcripts.

Rolling HLS files live under the media state directory. Removing a session
or stopping the daemon gracefully deletes its tree. An abrupt termination can
leave media behind until the next successful daemon start or manual purge.

## High-value report areas

- authentication bypass, token disclosure or DNS rebinding;
- path traversal, unsafe archive extraction or arbitrary file access;
- command/argument injection through media, device or configuration inputs;
- credential disclosure in API replies, events, logs or errors;
- unsafe parser behaviour for untrusted audio/video/network streams;
- unbounded allocation, goroutine/process leaks or remotely triggerable denial
  of service;
- race conditions that cross session, configuration or process-lifetime
  boundaries;
- update, dependency and release supply-chain verification; and
- privilege escalation or persistence in service/device installation.

## Operator baseline

Keep the listener on loopback, restrict state-directory permissions, rotate a
token after suspected disclosure, use authenticated/encrypted media transports
where available, minimise service privileges, patch the application and native
dependencies, and test deletion and incident-response procedures. Treat the
dashboard's configured CORS origin as privileged code.

Release artifacts may include GitHub attestations. When an attestation exists,
verify it in addition to the published checksum:

```bash
gh attestation verify prukka_<os>_<arch>.<ext> -R ubyte-source/prukka
gh attestation verify prukka-engine-runtime_<os>_<arch>.tar.gz -R ubyte-source/prukka
gh attestation verify prukka-engine-catalog.json -R ubyte-source/prukka
```

The engine assets are attested from the same release tag as the daemon, and the
catalog is what every native tool and model pack is resolved through, so
verifying the catalog verifies the root of that chain. `prukka setup` itself
checks each download against the catalog digests over TLS; verifying the
attestation is an operator step, not yet a client one.

The absence of a matching attestation is a failed verification, not permission
to skip the check.
