// Managed-engine catalog projections: which packs make up a language, what
// state a language row is in, and which packs an install or removal must
// touch. All of the dashboard's pack math lives (and is tested) here.

import type { EngineOperation, EnginePack, EngineStatus } from "./api/types";

export type LanguageState = "installed" | "partial" | "available";

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

/** operationBusy reports a live, non-terminal engine operation. */
export function operationBusy(operation: EngineOperation | undefined): boolean {
  return operation !== undefined
    && operation.phase !== "done"
    && operation.phase !== "error";
}
