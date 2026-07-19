// Single-flight controller for adding a language from anywhere in the UI: it
// installs the managed runtime and STT core on demand, then the language's
// packs, exposing one aggregate progress the wizard and Sessions both render.
// Correct as a singleton because the daemon runs exactly one engine op at a
// time; a second start() while busy is refused.

import { installEnginePack, installEngineRuntime } from "../api/client";
import type { EnginePhase, EngineStatus } from "../api/types";
import { packsToAdd, totalSizeBytes, type LanguagePlan } from "../enginePacks";
import { daemon } from "./daemon.svelte";

export type InstallPhase = "idle" | "preparing" | "running" | "done" | "error";

/** coreInstalled reports the managed runtime plus the STT core pack — the
 *  prerequisite every language shares. */
function coreInstalled(engine: EngineStatus | null): boolean {
  const core = engine?.packs?.find((pack) => pack.id === "stt-core");
  return (engine?.installed ?? false) && (core?.installed ?? false);
}

class EngineInstall {
  /** tag is the registry language being added; "" when idle. */
  tag = $state("");
  phase = $state<InstallPhase>("idle");
  /** stepCount / stepIndex track which pack of the bundle is running. */
  stepCount = $state(0);
  stepIndex = $state(0);
  /** grandTotal is the summed size of the packs with a known size. */
  grandTotal = $state(0);
  private doneBytes = $state(0);
  private ctrl: AbortController | null = null;

  /** busy reports an install currently in flight (its prerequisites or packs). */
  get busy(): boolean {
    return this.phase === "preparing" || this.phase === "running";
  }

  /** liveBytes is this op's freshest byte progress: the SSE frame (numbers)
   *  wins over the 2 s REST poll (int64 strings), mirroring Languages.svelte. */
  private liveBytes(): { done: number; total: number; phase: EnginePhase | null } {
    const event = daemon.engineEvent;
    if (event !== null && event.phase !== "done" && event.phase !== "error") {
      return { done: event.doneBytes ?? 0, total: event.totalBytes ?? 0, phase: event.phase };
    }
    const operation = daemon.engine?.operation;
    if (operation !== undefined && operation.phase !== "done" && operation.phase !== "error") {
      return {
        done: Number(operation.doneBytes ?? "0"),
        total: Number(operation.totalBytes ?? "0"),
        phase: operation.phase,
      };
    }
    return { done: 0, total: 0, phase: null };
  }

  /** livePhase is the current fine-grained phase for a plain-language label. */
  livePhase(): EnginePhase | null {
    return this.liveBytes().phase;
  }

  /** overallPct is the aggregate percent across the whole bundle, or null when
   *  the total is unknown (indeterminate pulse — never a fake number). */
  overallPct(): number | null {
    if (this.grandTotal <= 0) return null;
    const done = this.doneBytes + this.liveBytes().done;
    return Math.min(100, Math.max(0, Math.round((done / this.grandTotal) * 100)));
  }

  /** doneMb / totalMb feed the "12 MB of 45 MB" line. */
  doneBytesNow(): number {
    return this.doneBytes + this.liveBytes().done;
  }

  totalBytesNow(): number {
    return this.grandTotal;
  }

  /** start adds `tag`, installing prerequisites first, then its packs. onAdded
   *  fires once on success, after the daemon config has been re-read, so the
   *  new capability is already visible to the caller when it selects the
   *  language. onError receives the failure so the caller can word a toast. */
  async start(
    tag: string,
    plan: LanguagePlan,
    needVoice: boolean,
    token: string,
    onAdded: () => void,
    onError: (err: unknown) => void,
  ): Promise<void> {
    if (this.busy) return;
    const ctrl = new AbortController();
    const signal = ctrl.signal;
    this.ctrl = ctrl;
    this.tag = tag;
    this.stepIndex = 0;
    this.stepCount = 0;
    this.grandTotal = 0;
    this.doneBytes = 0;
    const packs = packsToAdd(plan, needVoice);

    try {
      if (!coreInstalled(daemon.engine)) {
        this.phase = "preparing";
        if (!(daemon.engine?.installed ?? false)) {
          daemon.setEngine(await installEngineRuntime(token));
          await daemon.waitForEngineIdle(signal);
        }
        const core = daemon.engine?.packs?.find((pack) => pack.id === "stt-core");
        if (core !== undefined && !(core.installed ?? false)) {
          daemon.setEngine(await installEnginePack("stt-core", token));
          await daemon.waitForEngineIdle(signal);
        }
      }

      this.phase = "running";
      this.stepCount = packs.length;
      this.grandTotal = totalSizeBytes(packs);
      this.doneBytes = 0;
      for (let index = 0; index < packs.length; index += 1) {
        if (signal.aborted) return;
        this.stepIndex = index + 1;
        daemon.setEngine(await installEnginePack(packs[index].id, token));
        await daemon.waitForEngineIdle(signal);
        this.doneBytes += Number(packs[index].sizeBytes ?? "0");
      }
      if (signal.aborted) return;

      // The packs changed what the daemon can dub and translate. Await the
      // config re-read so onAdded's contract — the new capability is already
      // selectable — is enforced rather than raced against the fire-and-forget
      // refresh the busy->idle edge triggers. refreshConfig never throws.
      await daemon.refreshConfig();
      if (signal.aborted) return;

      this.phase = "done";
      onAdded();
      this.settle(ctrl);
    } catch (err) {
      if (this.ctrl !== ctrl) return;
      this.ctrl = null;
      if (!signal.aborted) {
        this.phase = "error";
        onError(err);
      }
    }
  }

  /** cancel aborts the in-flight add and returns to idle. */
  cancel(): void {
    this.ctrl?.abort();
    this.settle(this.ctrl);
  }

  private settle(ctrl: AbortController | null): void {
    if (ctrl !== null && this.ctrl !== ctrl && this.ctrl !== null) return;
    this.ctrl = null;
    this.tag = "";
    this.phase = "idle";
    this.stepIndex = 0;
    this.stepCount = 0;
    this.grandTotal = 0;
    this.doneBytes = 0;
  }
}

export const engineInstall = new EngineInstall();
