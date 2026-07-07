// Typed client for the daemon's REST gateway. Reads are open on loopback;
// writes carry the control token as a Bearer header.
//
// Hosted mode: when the page is served from an operator's web
// origin instead of the daemon, every call still targets the LOCAL daemon —
// media and configuration never touch the hosting server. Loopback is
// exempt from browsers' mixed-content blocking.

import type {
  DaemonConfig,
  DoctorCheck,
  Language,
  NewSession,
  PushArgs,
  Session,
  Stats,
  UpdateConfigReply,
} from "./types";

const LOCAL = ["127.0.0.1", "localhost"].includes(location.hostname);
const BASE = LOCAL ? "" : "http://127.0.0.1:8080";

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

  const reply = await fetch(api(path), { ...init, headers });
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

export async function doctor(): Promise<DoctorCheck[]> {
  const reply = await request<{ checks?: DoctorCheck[] }>("/api/v1/doctor");
  return reply.checks ?? [];
}

export async function createSession(
  s: NewSession,
  token: string,
): Promise<void> {
  await request(
    "/api/v1/sessions",
    {
      method: "POST",
      body: JSON.stringify({ ...s, profile: "broadcast" }),
    },
    token,
  );
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

export async function getConfig(): Promise<DaemonConfig> {
  const reply = await request<{ config?: DaemonConfig }>("/api/v1/config");
  return reply.config ?? {};
}

export async function updateConfig(
  config: DaemonConfig,
  token: string,
): Promise<UpdateConfigReply> {
  return request<UpdateConfigReply>(
    "/api/v1/config",
    { method: "PUT", body: JSON.stringify(config) },
    token,
  );
}

export async function setKey(
  provider: string,
  key: string,
  token: string,
): Promise<void> {
  await request(
    "/api/v1/keys",
    { method: "POST", body: JSON.stringify({ provider, key }) },
    token,
  );
}

export async function push(args: PushArgs, token: string): Promise<void> {
  await request(
    `/api/v1/sessions/${encodeURIComponent(args.slug)}/push`,
    {
      method: "POST",
      body: JSON.stringify({
        lang: args.lang,
        targetUrl: args.targetUrl,
        subs: args.subs,
      }),
    },
    token,
  );
}
