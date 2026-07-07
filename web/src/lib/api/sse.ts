// Reconnecting wrapper around the daemon's SSE stream (/api/v1/events).
// EventSource retries transient drops by itself; a hard error (daemon gone)
// is surfaced so the UI can show a degraded state, and the source keeps
// retrying with its native backoff.

import { api } from "./client";
import type { Session, SessionEvent } from "./types";

export interface EventHandlers {
  onSnapshot: (sessions: Session[]) => void;
  onSession: (event: SessionEvent) => void;
  onUp: () => void;
  onDown: () => void;
}

/** subscribe opens the event stream and returns a close function. */
export function subscribe(handlers: EventHandlers): () => void {
  const source = new EventSource(api("/api/v1/events"));

  source.addEventListener("snapshot", (e: MessageEvent<string>) => {
    handlers.onSnapshot(JSON.parse(e.data) as Session[]);
  });

  source.addEventListener("session", (e: MessageEvent<string>) => {
    handlers.onSession(JSON.parse(e.data) as SessionEvent);
  });

  source.onopen = handlers.onUp;
  source.onerror = handlers.onDown;

  return () => source.close();
}
