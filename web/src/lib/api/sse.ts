// Reconnecting wrapper around the daemon's SSE stream (/api/v1/events).
// EventSource retries transient drops by itself; a hard error (daemon gone)
// is surfaced so the UI can show a degraded state, and the source keeps
// retrying with its native backoff.

import { api } from "./client";
import type { EngineEvent, Session, SessionEvent } from "./types";

export interface EventHandlers {
  onSnapshot: (sessions: Session[]) => void;
  onSession: (event: SessionEvent) => void;
  /** onEngine receives pack install progress; byte counters are numbers. */
  onEngine?: (event: EngineEvent) => void;
  onUp: () => void;
  onDown: () => void;
}

/**
 * subscribe opens the event stream and returns a close function. The engine
 * sub-stream carries the same install/provider detail the token-gated engine
 * read guards, so the token is passed as a query parameter (EventSource cannot
 * set an Authorization header); without it the daemon streams session events
 * only. App.svelte re-runs start() on every token change, so the source — and
 * thus this URL — is rebuilt whenever the token does.
 */
export function subscribe(handlers: EventHandlers, token = ""): () => void {
  const url = token
    ? `${api("/api/v1/events")}?token=${encodeURIComponent(token)}`
    : api("/api/v1/events");
  const source = new EventSource(url);

  source.addEventListener("snapshot", (e: MessageEvent<string>) => {
    handlers.onSnapshot(JSON.parse(e.data) as Session[]);
  });

  source.addEventListener("session", (e: MessageEvent<string>) => {
    handlers.onSession(JSON.parse(e.data) as SessionEvent);
  });

  const onEngine = handlers.onEngine;
  if (onEngine) {
    source.addEventListener("engine", (e: MessageEvent<string>) => {
      onEngine(JSON.parse(e.data) as EngineEvent);
    });
  }

  source.onopen = handlers.onUp;
  source.onerror = handlers.onDown;

  return () => source.close();
}
