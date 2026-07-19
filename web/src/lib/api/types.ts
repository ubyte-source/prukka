// Wire types mirroring prukka.v1 (grpc-gateway JSON). Field names follow
// the gateway's lowerCamelCase JSON mapping of the proto.

export interface Session {
  slug: string;
  profile: string;
  /** Sanitized display identity returned by current daemons. */
  sourceLabel?: string;
  /** Write-only in current daemons; retained for older read responses. */
  sourceUrl?: string;
  langs: string[];
  flags?: Record<string, string>;
  delaySeconds?: number;
  /** Observed lane state; absent only when connected to an older daemon. */
  status?: "starting" | "running" | "finished" | "failed";
  /** Bounded, sanitized failure detail. */
  error?: string;
  /** Languages with an effective, ready-capable synthesized-audio track. */
  effectiveDubbedLangs?: string[];
}

export interface Stats {
  sessionsActive?: string | number;
  uptimeSeconds?: number;
  version?: string;
}

export interface Language {
  tag: string;
  label: string;
}

export interface Device {
  /** url is the ready-to-use device:// source or target. */
  url: string;
  label: string;
  /** kind is one of audio-in, video-in, audio-out or video-out. */
  kind: string;
  /** virtual marks Prukka's own loopback devices. */
  virtual?: boolean;
}

export interface DoctorCheck {
  name: string;
  // status is one of ok, warn or fail.
  status: string;
  detail: string;
}

export interface SessionEvent {
  type: "created" | "updated" | "deleted" | "status";
  session: Session;
}

export interface NewSession {
  slug: string;
  /** profile is broadcast or call. */
  profile: string;
  sourceUrl: string;
  langs: string[];
  /** dub_langs is an optional comma-separated subset of langs. */
  flags: Record<string, string>;
  /** Omitted values are seeded from the daemon's live configuration. */
  delaySeconds?: number;
}

export interface PushArgs {
  slug: string;
  lang: string;
  targetUrl: string;
  subs: string;
}

export interface LocalConfig {
  sttModel?: string;
  /** dubbedLangs are the languages the local voices can synthesize. */
  dubbedLangs?: string[];
  mt?: TranslationConfig;
}

export interface TranslationConfig {
  pairs?: TranslationPair[];
}

export interface TranslationPair {
  from: string;
  to: string;
}

export interface ProvidersConfig {
  /** local enables the configured TTS voice; off makes every target captions-only. */
  voices?: "local" | "off";
  local?: LocalConfig;
}

export interface DefaultsConfig {
  langs?: string[];
  subs?: string;
  bed?: string;
  delaySeconds?: number;
}

export interface DaemonConfig {
  providers?: ProvidersConfig;
  defaults?: DefaultsConfig;
}

export interface UpdateConfigReply {
  config?: DaemonConfig;
  /** restartRequired names structural fields that only apply at restart. */
  restartRequired?: string[];
}

/** EnginePack is one downloadable unit in the managed speech-engine catalog. */
export interface EnginePack {
  id: string;
  /** kind is one of stt, mt or voice. */
  kind: string;
  installed?: boolean;
  /** sizeBytes is an int64, rendered by the gateway as a JSON string. */
  sizeBytes?: string;
  license?: string;
  /** from and to name the endpoints of an mt route. */
  from?: string;
  to?: string;
  /** lang names the language a voice pack speaks. */
  lang?: string;
}

export type EnginePhase = "download" | "verify" | "install" | "done" | "error";

/** EngineOperation is the daemon's single in-flight engine task (REST shape:
 *  byte counters are int64 strings). */
export interface EngineOperation {
  kind: string;
  packId?: string;
  phase: EnginePhase;
  doneBytes?: string;
  totalBytes?: string;
  error?: string;
}

export interface EngineStatus {
  /** installed reports the managed runtime itself, not any pack. */
  installed?: boolean;
  protocol?: number;
  /** catalogError is non-empty when the pack catalog could not be fetched. */
  catalogError?: string;
  packs?: EnginePack[];
  /** operation is absent while the engine manager is idle. */
  operation?: EngineOperation;
}

/** EngineEvent is the SSE progress frame: byte counters are JSON numbers,
 *  unlike the string-typed REST operation. */
export interface EngineEvent {
  kind: string;
  packId?: string;
  phase: EnginePhase;
  doneBytes?: number;
  totalBytes?: number;
  error?: string;
}
