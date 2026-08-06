// Managed-engine catalog projections: which packs make up a language, what
// state a language row is in, and which packs an install or removal must
// touch. All of the dashboard's pack math lives (and is tested) here.

import type { EngineOperation, EnginePack, EngineStatus } from "./api/types";

export type LanguageState = "installed" | "partial" | "available";

/** sameBase compares two tags by their base language (mirrors
 *  capabilities.sameBaseLanguage; kept local so this catalog module stays free
 *  of runtime cross-imports and remains loadable by the node unit tests). */
function sameBase(left: string, right: string): boolean {
  const base = (tag: string): string => tag.trim().toLowerCase().split("-", 1)[0] ?? "";
  const leftBase = base(left);
  return leftBase !== "" && leftBase === base(right);
}

export interface LanguagePlan {
  tag: string;
  state: LanguageState;
  /** required is the voice pack plus the language's two routes to the English
   *  hub (X->en and en->X). Through those, pivoting reaches every other
   *  installed language, so no direct cross-language pairs are needed. The hub
   *  language itself owns no routes. */
  required: EnginePack[];
  /** missing lists the required packs still to install, voice first. */
  missing: EnginePack[];
  /** removable lists the installed packs only this language needs: its voice
   *  and its two hub routes, voice first. */
  removable: EnginePack[];
}

/** HUB is the pivot language every route connects through; see providers/pivot. */
const HUB = "en";

/** voiceLanguage names the language a voice pack speaks. */
export function voiceLanguage(pack: EnginePack): string {
  return pack.lang ?? pack.id.replace(/^voice-/, "");
}

function voices(packs: readonly EnginePack[]): EnginePack[] {
  return packs.filter((pack) => pack.kind === "voice");
}

function routes(packs: readonly EnginePack[]): EnginePack[] {
  return packs.filter((pack) => pack.kind === "mt");
}

/** installedLanguages lists the tags whose voice pack is installed. */
export function installedLanguages(packs: readonly EnginePack[]): string[] {
  return voices(packs)
    .filter((pack) => pack.installed ?? false)
    .map(voiceLanguage);
}

/** hubRoutes are the two MT packs that connect a language to the English hub:
 *  X->en and en->X. The hub language itself owns no routes — its spokes belong
 *  to the other endpoint. */
function hubRoutes(packs: readonly EnginePack[], tag: string): EnginePack[] {
  if (tag === HUB) return [];
  return routes(packs).filter(
    (pack) =>
      (pack.from === tag && pack.to === HUB) ||
      (pack.from === HUB && pack.to === tag),
  );
}

/** languagePlans derives one row per catalog voice pack. A language is
 *  installed when its voice and both hub routes are present; partial when only
 *  some are; available otherwise. The hub row needs only its voice. */
export function languagePlans(engine: EngineStatus): LanguagePlan[] {
  const packs = engine.packs ?? [];

  return voices(packs).map((voice) => {
    const tag = voiceLanguage(voice);
    const required = [voice, ...hubRoutes(packs, tag)];
    const missing = required.filter((pack) => !(pack.installed ?? false));
    const state: LanguageState = missing.length === 0
      ? "installed"
      : missing.length === required.length
        ? "available"
        : "partial";
    const removable = required.filter((pack) => pack.installed ?? false);

    return { tag, state, required, missing, removable };
  });
}

/** totalSizeBytes sums pack sizes (int64 strings on the wire). */
export function totalSizeBytes(packs: readonly EnginePack[]): number {
  return packs.reduce((sum, pack) => sum + Number(pack.sizeBytes ?? "0"), 0);
}

/** mib renders a byte count as whole binary megabytes. */
export function mib(bytes: number): string {
  return Math.max(0, Math.round(bytes / (1024 * 1024))).toString();
}

/** mb renders a byte count as whole decimal megabytes — the user-facing unit
 *  ("45 MB"), kept plain rather than the technical MiB the pack manager uses. */
export function mb(bytes: number): string {
  return Math.max(0, Math.round(bytes / 1_000_000)).toString();
}

/** planForLanguage bridges a registry tag to its catalog plan: an exact tag
 *  match first, then a same-base fallback so regional variants (pt-BR, pt-PT)
 *  resolve to the one 'pt' voice pack, matching the daemon's base-keyed
 *  capability. */
export function planForLanguage(
  plans: readonly LanguagePlan[],
  tag: string,
): LanguagePlan | undefined {
  return plans.find((plan) => plan.tag === tag)
    ?? plans.find((plan) => sameBase(plan.tag, tag));
}

/** packsToAdd returns exactly the packs to install for the wanted capability:
 *  the whole bundle when a voice is needed, only the two English-hub MT routes
 *  when captions-only, so a captions-only add never downloads a voice. */
export function packsToAdd(plan: LanguagePlan, needVoice: boolean): EnginePack[] {
  return needVoice ? plan.missing : plan.missing.filter((pack) => pack.kind === "mt");
}

/** coreInstalled reports the managed runtime plus the STT core pack — the
 *  prerequisite every language shares. */
export function coreInstalled(engine: EngineStatus | null): boolean {
  const core = engine?.packs?.find((pack) => pack.id === "stt-core");
  return (engine?.installed ?? false) && (core?.installed ?? false);
}

/** operationBusy reports a live, non-terminal engine operation. */
export function operationBusy(operation: EngineOperation | null | undefined): boolean {
  return operation != null
    && operation.phase !== "done"
    && operation.phase !== "error";
}

/** normalizeEngineStatus scrubs the gateway's EmitUnpopulated artifact: an
 *  idle engine arrives with `"operation": null`, which a presence check
 *  against undefined would treat as a live operation. */
export function normalizeEngineStatus(engine: EngineStatus): EngineStatus {
  return engine.operation == null ? { ...engine, operation: undefined } : engine;
}
