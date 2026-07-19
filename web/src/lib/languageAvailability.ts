// Per-language readiness classification for the "add a language you don't
// have yet" pickers (Sessions today). It distinguishes three states a single
// "unavailable" answer would conflate: a language that is ready, one that
// just needs a quick download (addable), and one that is genuinely impossible
// on this setup.
//
// The wizard deliberately does NOT use this classifier: its call flow asks the
// narrower pack-level question "are packs still missing for this tag?"
// (Wizard.addablePack) with no route filter — a two-way call needs both hub
// routes plus a voice even where languageInfo would already answer "ready" —
// and its broadcast picker offers route-less languages as caption targets
// rather than downloads.

// Explicit .ts extensions keep this module (and its dependencies) loadable by
// the node unit tests, which run the sources under type stripping.
import type { TranslationPair } from "./api/types.ts";
import {
  autoTranslationTargetSupported,
  sameBaseLanguage,
  translationSupported,
} from "./capabilities.ts";
import {
  packsToAdd,
  planForLanguage,
  totalSizeBytes,
  type LanguagePlan,
} from "./enginePacks.ts";

export type LangStatus = "ready" | "addable" | "unavailable";

export interface LangInfo {
  status: LangStatus;
  /** addBytes is the download size when status is "addable", else 0. */
  addBytes: number;
  /** needVoice is the voice requirement this classification (and its pack set)
   *  was computed with: the caller wants dubbed audio and voices are enabled.
   *  Callers pass it straight to the download so the installed packs match. */
  needVoice: boolean;
  /** captionsOnly is true when a voice can never apply (voices disabled), so
   *  the language can only ever be captions, never dubbed. */
  captionsOnly: boolean;
}

/** LangCtx is everything languageInfo needs, all derived from live daemon
 *  state so the answer stays in lockstep with the backend. */
export interface LangCtx {
  pairs: readonly TranslationPair[];
  dubbedLangs: readonly string[];
  /** voicesOff mirrors config.providers.voices === "off". */
  voicesOff: boolean;
  plans: readonly LanguagePlan[];
  engineSupported: boolean;
  catalogAvailable: boolean;
  /** source is the chosen source language, or "auto" for auto-detect. */
  source: string;
}

/** languageInfo classifies a target language: ready to use, addable with a
 *  download, or unavailable. wantVoice asks whether the caller needs dubbed
 *  audio (vs captions only); a voices-off daemon can never satisfy it, so the
 *  language degrades to captions-only rather than offering a dead-end voice
 *  download. */
export function languageInfo(ctx: LangCtx, target: string, wantVoice: boolean): LangInfo {
  const routeOk = ctx.source === "auto"
    ? autoTranslationTargetSupported(ctx.pairs, target)
    : translationSupported(ctx.pairs, ctx.source, target);
  const voiceOk = !ctx.voicesOff && ctx.dubbedLangs.some((lang) => sameBaseLanguage(lang, target));
  const needVoice = wantVoice && !ctx.voicesOff;

  if (routeOk && (!needVoice || voiceOk)) {
    return { status: "ready", addBytes: 0, needVoice, captionsOnly: ctx.voicesOff };
  }

  const plan = planForLanguage(ctx.plans, target);
  const downloadable = ctx.engineSupported
    && ctx.catalogAvailable
    && plan !== undefined
    && plan.state !== "installed";
  if (downloadable) {
    const packs = packsToAdd(plan, needVoice);
    if (packs.length > 0) {
      return {
        status: "addable",
        addBytes: totalSizeBytes(packs),
        needVoice,
        captionsOnly: ctx.voicesOff,
      };
    }
  }

  return { status: "unavailable", addBytes: 0, needVoice, captionsOnly: ctx.voicesOff };
}
