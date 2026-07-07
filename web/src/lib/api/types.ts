// Wire types mirroring prukka.v1 (grpc-gateway JSON). Field names follow
// the gateway's lowerCamelCase JSON mapping of the proto.

export interface Session {
  slug: string;
  profile: string;
  sourceUrl: string;
  langs: string[];
  voiceMap?: Record<string, string>;
  flags?: Record<string, string>;
  budgetEurPerHour?: number;
  delaySeconds?: number;
}

export interface Stats {
  sessionsActive?: string | number;
  uptimeSeconds?: number;
  version?: string;
  costEurPerHour?: number;
}

export interface Language {
  tag: string;
  label: string;
}

export interface DoctorCheck {
  name: string;
  // status is one of ok, warn or fail.
  status: string;
  detail: string;
}

export interface SessionEvent {
  // type is one of created, updated or deleted.
  type: string;
  session: Session;
}

export interface NewSession {
  slug: string;
  sourceUrl: string;
  langs: string[];
  flags: Record<string, string>;
}

export interface PushArgs {
  slug: string;
  lang: string;
  targetUrl: string;
  subs: string;
}

export interface OpenRouterConfig {
  baseUrl?: string;
  sttModel?: string;
  mtModel?: string;
  mtTemperature?: number;
  ttsModel?: string;
  eurPerUsd?: number;
  timeoutSeconds?: number;
  /** keySet is read-only: whether an API key is configured. */
  keySet?: boolean;
}

export interface LocalConfig {
  baseUrl?: string;
  sttBaseUrl?: string;
  sttModel?: string;
  mtBaseUrl?: string;
  mtModel?: string;
  mtTemperature?: number;
  ttsBaseUrl?: string;
  ttsModel?: string;
  ttsVoice?: string;
  timeoutSeconds?: number;
}

export interface CartesiaConfig {
  baseUrl?: string;
  model?: string;
  timeoutSeconds?: number;
  /** keySet is read-only: whether an API key is configured. */
  keySet?: boolean;
}

export interface ProvidersConfig {
  backend?: string;
  clone?: string;
  openrouter?: OpenRouterConfig;
  local?: LocalConfig;
  cartesia?: CartesiaConfig;
}

export interface DefaultsConfig {
  langs?: string[];
  subs?: string;
  bed?: string;
  delaySeconds?: number;
}

export interface BudgetsConfig {
  perSessionEurH?: number;
  hardStop?: boolean;
}

export interface PrivacyConfig {
  storeTranscriptsHours?: number;
  storeAudio?: boolean;
}

export interface DaemonConfig {
  providers?: ProvidersConfig;
  defaults?: DefaultsConfig;
  budgets?: BudgetsConfig;
  privacy?: PrivacyConfig;
}

export interface UpdateConfigReply {
  config?: DaemonConfig;
  /** restartRequired names structural fields that only apply at restart. */
  restartRequired?: string[];
}
