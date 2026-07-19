// Live daemon state: sessions, stats and the event feed, kept current by
// the SSE stream with a periodic refresh as a safety net. This is the one
// place that owns shared bootstrap reads; components consume its projection.

import {
  ApiError,
  getConfig,
  getEngine,
  listDevices,
  listLanguages,
  listSessions,
  stats,
} from "../api/client";
import { subscribe } from "../api/sse";
import { operationBusy } from "../enginePacks";
import { isControlToken } from "./token.svelte";
import type {
  DaemonConfig,
  Device,
  EngineEvent,
  EngineStatus,
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
// While a pack downloads, a bounded poll backs up the SSE progress stream.
const ENGINE_POLL_MS = 2_000;
// Local (no network) cadence for waiters observing the engine state.
const ENGINE_WAIT_MS = 250;

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
  // engine mirrors the daemon's pack manager; null until the first read.
  engine = $state<EngineStatus | null>(null);
  engineLoaded = $state(false);
  engineError = $state(false);
  /** engineSupported turns false when the daemon predates /api/v1/engine. */
  engineSupported = $state(true);
  /** engineEvent is the freshest SSE progress frame for the live operation. */
  engineEvent = $state<EngineEvent | null>(null);
  private sessionsRevision = 0;
  private statsSeq = 0;
  private statsApplied = 0;
  private setupRevision = 0;
  private configAbort: AbortController | null = null;
  private engineAbort: AbortController | null = null;
  private engineTimer: ReturnType<typeof setInterval> | null = null;
  private engineRefreshing = false;
  private controlToken = "";
  private devicesRefreshing = false;
  private nextLogID = 0;

  setConfig(config: DaemonConfig) {
    // Deep-copy the wire so the Settings form's live edits stay isolated from
    // the shared config the wizard reads.
    this.config = structuredClone(config);
    this.configLoaded = true;
  }

  /** setEngine adopts a fresh engine snapshot and manages the progress poll. */
  setEngine(engine: EngineStatus) {
    const wasBusy = operationBusy(this.engine?.operation);
    this.engine = engine;
    this.engineLoaded = true;
    this.engineError = false;

    // Drop an SSE frame the snapshot has overtaken: a frame for another pack,
    // or a terminal frame while the wire shows a live (retried) operation.
    const event = this.engineEvent;
    if (event !== null) {
      const operation = engine.operation;
      const samePack = operation !== undefined
        && (event.packId ?? "") === (operation.packId ?? "");
      const staleTerminal = (event.phase === "done" || event.phase === "error")
        && operationBusy(operation);
      if (operation === undefined || !samePack || staleTerminal) this.engineEvent = null;
    }

    const busy = operationBusy(engine.operation);
    if (busy && this.engineTimer === null) {
      // SSE can drop mid-download; the poll keeps progress and the terminal
      // state flowing until the operation leaves the wire.
      this.engineTimer = setInterval(() => void this.refreshEngine(), ENGINE_POLL_MS);
    }
    if (!busy && this.engineTimer !== null) {
      clearInterval(this.engineTimer);
      this.engineTimer = null;
    }
    if (wasBusy && !busy) {
      // A finished operation can change what the daemon dubs or translates.
      void this.refreshConfig();
    }
  }

  /** refreshEngine re-reads the engine snapshot (progress poll and retries). */
  async refreshEngine(): Promise<void> {
    const tokenSnapshot = this.controlToken;
    if (this.engineRefreshing || !this.engineSupported || !isControlToken(tokenSnapshot)) return;
    const revision = this.setupRevision;
    this.engineRefreshing = true;
    try {
      const engine = await getEngine(tokenSnapshot);
      if (revision === this.setupRevision) this.setEngine(engine);
    } catch {
      // Keep the last snapshot; the poll or an operator retry recovers.
    } finally {
      this.engineRefreshing = false;
    }
  }

  /** retryEngine re-attempts the engine read after a load failure. */
  retryEngine() {
    if (!isControlToken(this.controlToken)) return;
    this.engineError = false;
    this.engineLoaded = false;
    this.loadEngine(this.controlToken, this.setupRevision);
  }

  /** refreshConfig quietly re-reads the daemon configuration; pack changes
   *  move dubbed languages and MT pairs without a full setup reload. */
  async refreshConfig(): Promise<void> {
    const tokenSnapshot = this.controlToken;
    if (!isControlToken(tokenSnapshot)) return;
    const revision = this.setupRevision;
    try {
      const config = await getConfig(tokenSnapshot);
      if (revision === this.setupRevision) this.setConfig(config);
    } catch {
      // The next full reload will surface a real configuration failure.
    }
  }

  /** waitForEngineIdle resolves once the daemon's single engine operation
   *  leaves the wire; a terminal error rejects so callers stop their plan. */
  async waitForEngineIdle(signal: AbortSignal): Promise<void> {
    for (;;) {
      if (signal.aborted) throw new DOMException("engine wait aborted", "AbortError");
      const operation = this.engine?.operation;
      if (operation?.phase === "error") {
        throw new Error(operation.error === undefined || operation.error === ""
          ? "engine operation failed"
          : operation.error);
      }
      const event = this.engineEvent;
      if (event?.phase === "error") {
        throw new Error(event.error === undefined || event.error === ""
          ? "engine operation failed"
          : event.error);
      }
      if (!operationBusy(operation)) return;
      await new Promise((resolve) => setTimeout(resolve, ENGINE_WAIT_MS));
    }
  }

  private loadEngine(tokenSnapshot: string, revision: number) {
    this.engineAbort?.abort();
    const controller = new AbortController();
    this.engineAbort = controller;
    getEngine(tokenSnapshot, controller.signal)
      .then((engine) => {
        if (revision === this.setupRevision && !controller.signal.aborted) this.setEngine(engine);
      })
      .catch((e: unknown) => {
        if (revision !== this.setupRevision || controller.signal.aborted) return;
        if (e instanceof ApiError && e.status === 404) {
          // Older daemons predate the pack manager; the section stays hidden.
          this.engineSupported = false;
        } else {
          this.engineError = true;
        }
        this.engineLoaded = true;
      })
      .finally(() => {
        if (revision !== this.setupRevision || controller.signal.aborted) return;
        if (this.engineAbort === controller) this.engineAbort = null;
      });
  }

  private applyEngineEvent(event: EngineEvent) {
    if (!this.engineSupported || !isControlToken(this.controlToken)) return;
    this.engineEvent = event;
    if (event.phase === "done") {
      // Terminal: re-read the catalog and the capabilities it unlocked.
      void this.refreshEngine();
      void this.refreshConfig();
    } else if (event.phase === "error") {
      void this.refreshEngine();
    }
  }

  private resetEngine() {
    this.engineAbort?.abort();
    this.engineAbort = null;
    if (this.engineTimer !== null) {
      clearInterval(this.engineTimer);
      this.engineTimer = null;
    }
    this.engine = null;
    this.engineLoaded = false;
    this.engineError = false;
    this.engineSupported = true;
    this.engineEvent = null;
    this.engineRefreshing = false;
  }

  private setSessions(sessions: Session[]) {
    this.sessions = [...sessions].sort(bySlug);
    this.sessionsRevision += 1;
    this.stats = { ...this.stats, sessionsActive: this.sessions.length };
    this.status = runtimeHealth(this.sessions);
  }

  async refresh() {
    const revision = this.sessionsRevision;
    const seq = ++this.statsSeq;
    try {
      const [sessions, s] = await Promise.all([listSessions(), stats()]);
      // Stats freshness is tracked independently of sessionsRevision (which
      // SSE session updates also bump): apply these stats unless a
      // later-started refresh already applied its own. An older reply can
      // never clobber fresher stats, yet a live SSE session update never
      // discards the stats this refresh legitimately fetched.
      if (seq > this.statsApplied) {
        this.statsApplied = seq;
        this.stats = s;
      }
      // The fetched session list is authoritative only if no SSE update landed
      // while awaiting; otherwise keep the live table and reconcile the count.
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
      this.engineAbort?.abort();
      this.engineAbort = null;
      if (this.engineTimer !== null) {
        clearInterval(this.engineTimer);
        this.engineTimer = null;
      }
    };
  }

  /** reloadSetup refreshes the registries needed by the session wizard. */
  reloadSetup(controlToken: string) {
    const revision = ++this.setupRevision;
    this.controlToken = controlToken;
    this.configAbort?.abort();
    this.configAbort = null;
    this.resetEngine();
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

      // Pack management is a sensitive read: catalog state describes what
      // the machine can process locally.
      this.loadEngine(controlToken, revision);

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
      this.engineLoaded = true;
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
    const close = subscribe(
      {
        onSnapshot: (sessions) => {
          this.setSessions(sessions);
        },
        onSession: (event) => {
          this.log(`session ${event.session.slug} ${event.type}`);
          this.setSessions(applySessionEvent(this.sessions, event));
        },
        onEngine: (event) => {
          this.applyEngineEvent(event);
        },
        onUp: () => void this.refresh(),
        onDown: () => {
          this.status = "degraded";
        },
      },
      this.controlToken,
    );

    const timer = setInterval(() => void this.refresh(), REFRESH_MS);

    return () => {
      close();
      clearInterval(timer);
    };
  }
}

export const daemon = new DaemonState();
