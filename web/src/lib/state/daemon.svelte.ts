// Live daemon state: sessions, stats and the event feed, kept current by
// the SSE stream with a periodic refresh as a safety net. This is the one
// place that talks to the API for reads; components only render it.

import { listLanguages, listSessions, stats } from "../api/client";
import { subscribe } from "../api/sse";
import type { Language, Session, Stats } from "../api/types";

export type Status = "idle" | "live" | "degraded";

export interface LogEntry {
  at: Date;
  text: string;
}

const LOG_LIMIT = 20;
const REFRESH_MS = 30_000;

class DaemonState {
  sessions = $state<Session[]>([]);
  stats = $state<Stats>({});
  status = $state<Status>("idle");
  events = $state<LogEntry[]>([]);
  // languages is the immutable registry: loaded once, feeds
  // every dropdown — no client hardcodes a language.
  languages = $state<Language[]>([]);

  async refresh() {
    try {
      const [sessions, s] = await Promise.all([listSessions(), stats()]);
      this.sessions = sessions;
      this.stats = s;
      this.status = sessions.length > 0 ? "live" : "idle";
    } catch {
      this.status = "degraded";
    }
  }

  log(text: string) {
    this.events = [{ at: new Date(), text }, ...this.events].slice(
      0,
      LOG_LIMIT,
    );
  }

  /** start opens the SSE stream and the refresh timer; returns a stop. */
  start(): () => void {
    listLanguages()
      .then((reply) => (this.languages = reply))
      .catch(() => {
        this.status = "degraded";
      });

    const close = subscribe({
      onSnapshot: (sessions) => {
        this.sessions = sessions;
        this.status = sessions.length > 0 ? "live" : "idle";
      },
      onSession: (event) => {
        this.log(`session ${event.session.slug} ${event.type}`);
        void this.refresh();
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
