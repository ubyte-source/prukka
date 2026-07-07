# Data protection and AI transparency

Legal snapshot: **13 July 2026**. This document records the product's current
data flows and gives operators an implementation checklist. It is not legal
advice and is not a declaration of compliance: the controller must assess the
actual deployment, purposes, people, destinations and national or sector rules.

## Current product status and data flow

The standard setup currently installs FFmpeg only. The native speech helper,
its native tools, and STT/MT/TTS models are not distributed by `prukka setup`;
speech lanes are unavailable until an operator supplies a compatible helper
and configures `providers.local.bin` and the model paths. The statements below
describe the runtime once that helper is configured.

- STT, translation and supported-language TTS are designed to run in a local
  subprocess. Translation is limited to declared directed MT pairs; same-base
  output needs no MT model and an unsupported detected direction fails the
  lane. A target with MT support but incompatible with the one configured
  voice language is caption-only. Prukka has no hosted inference provider,
  provider account or API-key flow.
- Prukka sends no telemetry. Update and dependency downloads occur only after
  an explicit operator command and do not contain session media.
- Media crosses the host boundary through configured network inputs and output
  routes. The daemon also serves HLS, direct audio and WebVTT reads without a
  token: they rely on the listener being loopback-only. A listener reachable by
  other machines is therefore an additional disclosure route. RTMP and SRT
  endpoints can belong to another controller, processor or country; the URL
  scheme alone does not establish confidentiality or a lawful international
  transfer.
- The dashboard is served by the daemon or can be loaded from the configured
  CORS origin. The hosted static files do not proxy media or configuration.

The current storage behaviour is:

| Data | Location and lifetime | Operator control |
|---|---|---|
| Source PCM | Bounded memory buffers; not archived by Prukka | delete the session or stop the daemon |
| Direct WebVTT | Memory; up to 200 recent cues for each session/language | delete the session or stop the daemon |
| HLS output | Private rolling files under the state media directory; a finished session's final window remains while the definition is registered, and an abrupt stop can leave files behind | delete the session or stop the daemon gracefully; remove crash debris manually or let the next successful daemon start clear it |
| Daemon and service logs | Runtime-controlled destination: stderr, journald/launchd, or the configured Windows log. Prukka does not deliberately log transcript or translation content, but logs can contain session metadata, errors, URLs and captured stderr from the operator-supplied helper | restrict access and configure retention/rotation in the OS or service manager; validate helper logging and sanitise secret-bearing URLs before sharing logs |
| Runtime dependencies | FFmpeg under the state directory; helper, native tool, and model paths are operator supplied | remove the files or purge the state directory |
| Control token | Per-user state directory; file mode `0600` where POSIX permissions are supported | rotate or remove the installation state; verify Windows profile/state ACLs |
| Dashboard locale | Browser `localStorage` | browser/site-data controls |
| Dashboard control token | Browser `sessionStorage`, after adoption from the URL fragment | closing the tab/session or browser/site-data controls |

The control token grants local control-plane access. Do not publish URLs that
contain it, paste them into tickets, or expose the daemon to an untrusted
network. A URL fragment is not sent in the HTTP request, and the dashboard
removes a valid token fragment after adopting it, but browser extensions,
screenshots and copied URLs remain part of the operator's threat model.

## GDPR classification

Local processing is still processing under the GDPR. A voice recording or
transcript is personal data whenever a person is identified or identifiable;
the transcript can also reveal special-category data. Voice data is biometric
data under Article 4(14) only when it results from specific technical processing
of characteristics that allow or confirm unique identification. Prukka does
not currently identify speakers by identity or implement voice cloning, but
that does not make ordinary voice and transcript data anonymous.

The operator must determine whether it is controller, joint controller or
processor for each deployment. Consent is not universally required and must
not be presented as the only possible lawful basis. The controller needs a
valid Article 6 basis for each purpose and, if Article 9 data is processed, a
separate applicable Article 9 condition. Employment, communications,
copyright, confidentiality and recording rules can add requirements beyond
the GDPR.

## Deployment checklist

Before enabling a session, the controller should document and implement at
least the following:

1. Define purposes, categories of people and data, lawful bases, roles and
   retention periods. Keep the Article 30 record where required.
2. Inform speakers and affected bystanders before processing, in a concise and
   accessible notice covering purposes, legal basis, recipients/output routes,
   retention, rights, transfers and use of AI. Prukka makes no automatic
   announcement and does not collect consent.
3. Minimise target languages, subtitle modes, destinations and retention. Give
   authorised staff a deletion procedure for sessions, state files and browser
   storage.
4. Assess every recipient, processor and sub-processor. Put Article 28 terms in
   place where applicable and implement a Chapter V transfer mechanism before
   routing personal data outside the EEA.
5. Apply Article 25 and Article 32 measures appropriate to risk: access
   control, token protection and rotation, host hardening, secure routes,
   logging policy, patching, backups and tested incident response. Determine
   whether Articles 33 and 34 breach notifications are required after an
   incident.
6. Complete a DPIA before processing that is likely to create high risk. This
   is especially relevant when criteria combine, such as systematic monitoring,
   large scale, sensitive data, vulnerable people, workplace use or innovative
   technology. The Italian Garante's list is non-exhaustive.
7. Provide a process for access, erasure, restriction, objection and other
   applicable data-subject rights. Do not rely on the dashboard as that process.

## EU AI Act readiness

Article 50 transparency duties become applicable on **2 August 2026**. The
provider/deployer role and the exact duties depend on how Prukka and the chosen
models are placed on the market and used. Before that date, assess and test:

- notice when an in-scope system directly interacts with a person, unless this
  is obvious from the circumstances;
- machine-readable, detectable marking of synthetic audio where Article 50(2)
  applies and to the extent required by its technical-feasibility rules; and
- a clear disclosure when generated or manipulated audio qualifies as a
  deepfake under Article 50(4).

A preset synthetic voice is not automatically a deepfake: that classification
depends on whether the output resembles an existing person or other subject and
would falsely appear authentic. The dashboard's operational warning is useful
notice, but is not by itself evidence that all Article 50 duties are met.

The EU adopted a Digital Omnibus amendment in June 2026, but publication and
consolidation must be checked before relying on any transition rule. The
Commission's transparency code is voluntary; signing it does not provide
conclusive evidence of compliance.

## Official sources

- [GDPR, Regulation (EU) 2016/679](https://eur-lex.europa.eu/eli/reg/2016/679/oj)
- [Italian Garante: AI-generated voice, personal and biometric data (18 December 2025)](https://www.garanteprivacy.it/web/guest/home/docweb/-/docweb-display/docweb/10207132)
- [Italian Garante: processing subject to a DPIA](https://www.garanteprivacy.it/home/docweb/-/docweb-display/docweb/9058979)
- [EDPB Opinion 28/2024 on personal data and AI models](https://www.edpb.europa.eu/our-work-tools/our-documents/opinion-board-art-64/opinion-282024-certain-data-protection-aspects_en)
- [EU AI Act, Regulation (EU) 2024/1689](https://eur-lex.europa.eu/eli/reg/2024/1689/oj)
- [European Commission: Article 50 transparency code and application date](https://digital-strategy.ec.europa.eu/en/policies/code-practice-ai-generated-content)
- [Digital Omnibus on AI, adopted legislative text](https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=consil%3APE_30_2026_INIT)
