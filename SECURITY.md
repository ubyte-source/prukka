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
- REST mutations, configuration reads and the diagnostic `/api/v1/doctor`
  read require the same token. Some other HTTP reads and media endpoints are intentionally
  unauthenticated because loopback is the primary boundary. In particular, a
  network-reachable listener can disclose HLS, direct audio and WebVTT output
  without the control token.
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
- `prukka setup` downloads only the pinned FFmpeg runtime. The speech helper
  and models are not installed or verified by that command.
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
```

The absence of a matching attestation is a failed verification, not permission
to skip the check.
