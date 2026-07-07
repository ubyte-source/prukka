# Data protection (GDPR) notes

Honest engineering applies to data flows too: this page states exactly what
leaves the machine, what is stored, and who controls it. Nothing here is legal
advice.

## What leaves the machine

Only while a session is live, and only what the configured backend needs:

- **`providers.backend: openrouter`** — audio segments (VAD-endpointed
  utterances) are sent to OpenRouter and routed to the configured STT
  provider; transcripts go to the routed MT provider and translated text to
  the routed TTS provider.
- **`providers.backend: local`** — nothing leaves the machine: transcription,
  translation and voice run on the operator's own OpenAI-compatible servers.
- **`providers.clone: cartesia`** — a short **reference clip of each
  speaker's own voice** is sent to Cartesia once per session to clone their
  timbre, and translated text is synthesized there. A voice sample can
  qualify as biometric data in several jurisdictions: obtain the speaker's
  consent before enabling cloning (Cartesia's terms require it too), and the
  cloned voices persist in the Cartesia account (named `prukka-*`) until
  deleted there.
- There is no telemetry and no update phoning home (the updater is always
  explicit).

With no session live, no audio path is active and nothing leaves the machine.

## What is stored locally

| Data | Default | Control |
|---|---|---|
| Audio | **never stored** | `privacy.store_audio` (off by default) |
| Transcripts | ring buffer, 24 h TTL | `privacy.store_transcripts` |
| Provider keys | OS keychain only | stored by `prukka key set`; `keychain://` references; doctor warns on plaintext |
| Control token | `$STATE/control.token`, mode 0600 | minted per install |

## Interpreting live calls

Several jurisdictions require the remote party's consent before a call is
processed by an AI interpreter. Prukka ships no automatic announcement:
obtaining that consent is the deployer's responsibility.

Deployers are the data controllers: where OpenRouter routes audio depends on
the models chosen in `providers.openrouter.*` — pin providers/regions there
if your jurisdiction requires it. The fully offline posture is
`providers.backend: local` with `providers.clone: off` or `pitch`.
