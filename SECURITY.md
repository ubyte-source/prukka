# Security Policy

## Supported Versions

Prukka is under active development. Security fixes land on the latest release
and the `main` branch. Please run a current version before reporting.

## Reporting a Vulnerability

**Do not open a public issue for a security vulnerability.**

Report privately through GitHub's coordinated disclosure:

1. Go to the [Security Advisories](https://github.com/ubyte-source/prukka/security/advisories/new) page
2. Click **Report a vulnerability**
3. Include a description, reproduction steps, affected version and, if known, a suggested fix

You will receive an acknowledgement, and we will work with you on a fix and a
coordinated disclosure timeline before any public discussion.

## Scope & Threat Model

Prukka is designed to keep media and configuration local: the daemon
binds `127.0.0.1`, the control plane is token-authenticated over a UNIX socket
or named pipe, and provider keys live in the OS keychain, never on disk in
plaintext. Reports that concern any of the following are especially in scope:

- Control-plane authentication or token handling
- Path traversal or request data reaching the filesystem in the data plane
- Secret handling (`keychain://` resolution, key logging)
- The self-update path (checksum verification, archive extraction)
- The native drivers' privilege boundaries

Denial of service via an unbounded external source is expected to be handled by
the operator's network perimeter; the daemon is not intended to be exposed to
untrusted networks directly.

## Handling of Secrets

Provider API keys are stored only as `keychain://` references resolved from the
OS keychain at use. `prukka doctor` warns on any plaintext key in configuration.
Never paste a real key into an issue, a PR or a log excerpt.
