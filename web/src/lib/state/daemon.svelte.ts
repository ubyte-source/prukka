// Live daemon state: sessions, stats and the event feed, kept current by
// the SSE stream with a periodic refresh as a safety net. This is the one
// place that owns shared bootstrap reads; components consume its projection.

import { getConfig, listDevices, listLanguages, listSessions, stats } from "../api/client";
import { subscribe } from "../api/sse";
import { isControlToken } from "./token.svelte";
import type {
  DaemonConfig,
  Device,
  Language,
  Session,
  SessionEvent,
  Stats,
} from "../api/types";

export type Status = "idle" | "live" | "degraded";

export interface LogEntry {
  id: number;
  at: Date;
  text: string;
}

const LOG_LIMIT = 20;
const REFRESH_MS = 30_000;

function runtimeHealth(sessions: Session[]): Status {
  if (sessions.some((session) => session.status === "failed")) return "degraded";
  if (sessions.some((session) =>
    session.status === "starting" || session.status === "running" || session.status === undefined
  )) return "live";
  return "idle";
}

function bySlug(a: Session, b: Session): number {
  return a.slug < b.slug ? -1 : Number(a.slug > b.slug);
}

function applySessionEvent(sessions: Session[], event: SessionEvent): Session[] {
  if (event.type === "deleted") {
    return sessions.filter((session) => session.slug !== event.session.slug);
  }
  if (event.type !== "created" && event.type !== "updated" && event.type !== "status") {
    return sessions;
  }

  const next = sessions.filter((session) => session.slug !== event.session.slug);
  next.push(event.session);
  next.sort(bySlug);

  return next;
}

class DaemonState {
  sessions = $state<Session[]>([]);
  stats = $state<Stats>({});
  status = $state<Status>("idle");
  events = $state<LogEntry[]>([]);
  // languages mirrors the daemon registry and feeds every dropdown — no
  // client hardcodes a language.
  languages = $state<Language[]>([]);
  languagesLoaded = $state(false);
  languagesError = $state(false);
  // devices is the machine's picker list; empty means manual entry.
  devices = $state<Device[]>([]);
  devicesLoaded = $state(false);
  devicesError = $state(false);
  config = $state<DaemonConfig>({});
  configLoaded = $state(false);
  configError = $state(false);
  private sessionsRevision = 0;
  private setupRevision = 0;
  private configAbort: AbortController | null = null;
  private devicesRefreshing = false;
  private nextLogID = 0;

  setConfig(config: DaemonConfig) {
    // Deep-copy the wire so the Settings form's live edits stay isolated from
    // the shared config the wizard reads.
    this.config = structuredClone(config);
    this.configLoaded = true;
  }

  private setSessions(sessions: Session[]) {
    this.sessions = [...sessions].sort(bySlug);
    this.sessionsRevision += 1;
    this.stats = { ...this.stats, sessionsActive: this.sessions.length };
    this.status = runtimeHealth(this.sessions);
  }

  async refresh() {
    const revision = this.sessionsRevision;
    try {
      const [sessions, s] = await Promise.all([listSessions(), stats()]);
      this.stats = s;
      if (this.sessionsRevision === revision) {
        this.setSessions(sessions);
      } else {
        this.stats = { ...this.stats, sessionsActive: this.sessions.length };
      }
    } catch {
      this.status = "degraded";
    }
  }

  log(text: string) {
    this.events = [{ id: ++this.nextLogID, at: new Date(), text }, ...this.events].slice(
      0,
      LOG_LIMIT,
    );
  }

  /** start opens the SSE stream and the refresh timer; returns a stop. */
  start(controlToken: string): () => void {
    this.reloadSetup(controlToken);

    const close = this.startStream();

    return () => {
      close();
      this.setupRevision += 1;
      this.configAbort?.abort();
      this.configAbort = null;
    };
  }

  /** reloadSetup refreshes the registries needed by the session wizard. */
  reloadSetup(controlToken: string) {
    const revision = ++this.setupRevision;
    this.configAbort?.abort();
    this.configAbort = null;
    void this.refresh();
    this.languagesLoaded = false;
    this.languagesError = false;
    this.devices = [];
    this.devicesLoaded = false;
    this.devicesError = false;
    this.devicesRefreshing = false;
    this.configLoaded = false;
    this.configError = false;
    // Configuration contains operator-selected paths. Drop the previous
    // projection before an unauthenticated or different token can reuse it.
    this.config = {};

    if (isControlToken(controlToken)) {
      const controller = new AbortController();
      this.configAbort = controller;
      getConfig(controlToken, controller.signal)
        .then((reply) => {
          if (revision === this.setupRevision && !controller.signal.aborted) this.setConfig(reply);
        })
        .catch(() => {
          if (revision !== this.setupRevision || controller.signal.aborted) return;
          this.configError = true;
          this.status = "degraded";
        })
        .finally(() => {
          if (revision !== this.setupRevision || controller.signal.aborted) return;
          if (this.configAbort === controller) this.configAbort = null;
          this.configLoaded = true;
        });

      // Hardware labels and endpoint IDs are local-machine inventory, so the
      // daemon exposes them only to an authenticated dashboard.
      listDevices(controlToken)
        .then((reply) => {
          if (revision === this.setupRevision) this.devices = reply;
        })
        .catch(() => {
          if (revision !== this.setupRevision) return;
          this.devicesError = true;
          this.status = "degraded";
        })
        .finally(() => {
          if (revision === this.setupRevision) this.devicesLoaded = true;
        });
    } else {
      this.devicesLoaded = true;
    }

    listLanguages()
      .then((reply) => {
        if (revision === this.setupRevision) this.languages = reply;
      })
      .catch(() => {
        if (revision !== this.setupRevision) return;
        this.languagesError = true;
        this.status = "degraded";
      })
      .finally(() => {
        if (revision === this.setupRevision) this.languagesLoaded = true;
      });
  }

  /** refreshDevices re-enumerates hardware: OS capture consent granted
   *  after load, or hotplug, can grow the list without a reload. */
  refreshDevices(controlToken: string) {
    if (this.devicesRefreshing || !isControlToken(controlToken)) return;
    const revision = this.setupRevision;
    this.devicesRefreshing = true;
    listDevices(controlToken)
      .then((reply) => {
        if (revision === this.setupRevision) {
          this.devices = reply;
          this.devicesError = false;
        }
      })
      .catch(() => {
        if (revision === this.setupRevision) this.devicesError = true;
      })
      .finally(() => {
        if (revision === this.setupRevision) this.devicesRefreshing = false;
      });
  }

  private startStream(): () => void {
    const close = subscribe({
      onSnapshot: (sessions) => {
        this.setSessions(sessions);
      },
      onSession: (event) => {
        this.log(`session ${event.session.slug} ${event.type}`);
        this.setSessions(applySessionEvent(this.sessions, event));
      },
      onUp: () => void this.refresh(),
      onDown: () => {
        this.status = "degraded";
      },
    });

    const timer = setInterval(() => void this.refresh(), REFRESH_MS);

    return () => {
      close();
      clearInterval(timer);
    };
  }
}

export const daemon = new DaemonState();
