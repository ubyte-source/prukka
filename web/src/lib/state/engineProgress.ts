// The one authority on "how far along is the daemon's single engine
// operation": the SSE frame (number counters) wins over the string-typed REST
// operation the 2 s fallback poll refreshes. The pack manager, the guided add
// and the first-run hero all render from this selector.

import type { EnginePhase } from "../api/types";
import { daemon } from "./daemon.svelte";

export interface EngineProgress {
  packId: string;
  kind: string;
  phase: Exclude<EnginePhase, "done" | "error">;
  done: number;
  total: number;
}

/** liveEngineProgress returns the freshest non-terminal progress, or null
 *  while the engine manager is idle. */
export function liveEngineProgress(): EngineProgress | null {
  const event = daemon.engineEvent;
  if (event !== null && event.phase !== "done" && event.phase !== "error") {
    return {
      packId: event.packId ?? "",
      kind: event.kind,
      phase: event.phase,
      done: event.doneBytes ?? 0,
      total: event.totalBytes ?? 0,
    };
  }
  const operation = daemon.engine?.operation;
  if (operation != null && operation.phase !== "done" && operation.phase !== "error") {
    return {
      packId: operation.packId ?? "",
      kind: operation.kind,
      phase: operation.phase,
      done: Number(operation.doneBytes ?? "0"),
      total: Number(operation.totalBytes ?? "0"),
    };
  }
  return null;
}
