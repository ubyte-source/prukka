// Typed client for the daemon's REST gateway. Mutations and sensitive reads
// carry the control token as a Bearer header.
//
// Hosted mode: the current bundle sends API calls directly to the local
// daemon. The hosting origin still controls executable JavaScript and is a
// privileged trust boundary. Loopback is exempt from mixed-content blocking.

import type {
  DaemonConfig,
  Device,
  DoctorCheck,
  EngineStatus,
  Language,
  NewSession,
  PushArgs,
  Session,
  Stats,
  UpdateConfigReply,
} from "./types";
import { apiBase } from "./origin";

const configuredBase = document
  .querySelector<HTMLMetaElement>('meta[name="prukka-api-base"]')
  ?.getAttribute("content") ?? null;
const BASE = apiBase(configuredBase);

/** api resolves a daemon path in both local and hosted mode. */
export function api(path: string): string {
  return BASE + path;
}

/** ApiError carries the gateway's message and HTTP status for the UI. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(
  path: string,
  init: RequestInit = {},
  token = "",
): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  if (token) headers.set("Authorization", `Bearer ${token}`);

  let reply: Response;
  try {
    reply = await fetch(api(path), { ...init, headers });
  } catch (cause) {
    if (BASE) {
      throw new ApiError(
        0,
        "Cannot reach the local daemon. Start it and allow Local Network Access for this site.",
      );
    }

    throw cause;
  }
  if (!reply.ok) {
    const detail = (await reply.json().catch(() => ({}))) as {
      message?: string;
    };
    throw new ApiError(reply.status, detail.message ?? `http ${reply.status}`);
  }

  return (await reply.json()) as T;
}

export async function listSessions(): Promise<Session[]> {
  const reply = await request<{ sessions?: Session[] }>("/api/v1/sessions");
  return reply.sessions ?? [];
}

export async function stats(): Promise<Stats> {
  return request<Stats>("/api/v1/stats");
}

export async function listLanguages(): Promise<Language[]> {
  const reply = await request<{ languages?: Language[] }>("/api/v1/languages");
  return reply.languages ?? [];
}

export async function listDevices(token: string): Promise<Device[]> {
  const reply = await request<{ devices?: Device[] }>("/api/v1/devices", {}, token);
  return reply.devices ?? [];
}

export async function doctor(token: string, signal?: AbortSignal): Promise<DoctorCheck[]> {
  const reply = await request<{ checks?: DoctorCheck[] }>(
    "/api/v1/doctor",
    { signal },
    token,
  );
  return reply.checks ?? [];
}

export async function createSession(
  s: NewSession,
  token: string,
): Promise<Session> {
  const reply = await request<{ session?: Session }>(
    "/api/v1/sessions",
    {
      method: "POST",
      body: JSON.stringify(s),
    },
    token,
  );
  if (!reply.session) throw new ApiError(502, "create response did not include the session");

  return reply.session;
}

export async function deleteSession(
  slug: string,
  token: string,
): Promise<void> {
  await request(
    `/api/v1/sessions/${encodeURIComponent(slug)}`,
    { method: "DELETE" },
    token,
  );
}

export async function updateLangs(
  slug: string,
  addLangs: string[],
  removeLangs: string[],
  token: string,
): Promise<void> {
  await request(
    `/api/v1/sessions/${encodeURIComponent(slug)}`,
    {
      method: "PATCH",
      body: JSON.stringify({ addLangs, removeLangs }),
    },
    token,
  );
}

export async function getConfig(token: string, signal?: AbortSignal): Promise<DaemonConfig> {
  const reply = await request<{ config?: DaemonConfig }>(
    "/api/v1/config",
    { signal },
    token,
  );
  return reply.config ?? {};
}

export async function updateConfig(
  config: DaemonConfig,
  token: string,
  signal?: AbortSignal,
): Promise<UpdateConfigReply> {
  return request<UpdateConfigReply>(
    "/api/v1/config",
    { method: "PUT", body: JSON.stringify(config), signal },
    token,
  );
}

export async function getEngine(token: string, signal?: AbortSignal): Promise<EngineStatus> {
  const reply = await request<{ engine?: EngineStatus }>(
    "/api/v1/engine",
    { signal },
    token,
  );
  return reply.engine ?? {};
}

/** installEngineRuntime starts the async managed-runtime install (409 when busy). */
export async function installEngineRuntime(token: string): Promise<EngineStatus> {
  const reply = await request<{ engine?: EngineStatus }>(
    "/api/v1/engine/runtime",
    { method: "POST", body: "{}" },
    token,
  );
  return reply.engine ?? {};
}

/** installEnginePack starts an async pack download (409 busy, 400 unknown id). */
export async function installEnginePack(id: string, token: string): Promise<EngineStatus> {
  const reply = await request<{ engine?: EngineStatus }>(
    "/api/v1/engine/packs",
    { method: "POST", body: JSON.stringify({ id }) },
    token,
  );
  return reply.engine ?? {};
}

/** removeEnginePack removes an installed pack synchronously. */
export async function removeEnginePack(id: string, token: string): Promise<EngineStatus> {
  const reply = await request<{ engine?: EngineStatus }>(
    `/api/v1/engine/packs/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    token,
  );
  return reply.engine ?? {};
}

export async function push(
  args: PushArgs,
  token: string,
  signal?: AbortSignal,
): Promise<void> {
  await request(
    `/api/v1/sessions/${encodeURIComponent(args.slug)}/push`,
    {
      method: "POST",
      signal,
      body: JSON.stringify({
        lang: args.lang,
        targetUrl: args.targetUrl,
        subs: args.subs,
      }),
    },
    token,
  );
}
