import type { TranslationPair } from "./api/types";

export function baseLanguage(tag: string): string {
  return tag.trim().toLowerCase().split("-", 1)[0] ?? "";
}

export function sameBaseLanguage(left: string, right: string): boolean {
  const leftBase = baseLanguage(left);
  return leftBase !== "" && leftBase === baseLanguage(right);
}

/** HUB is the pivot language the daemon bridges every indirect route through. */
const HUB = "en";

/** translationSupported mirrors the daemon: a same-base target needs no model,
 *  a direct pair serves the route, and otherwise the English hub bridges it as
 *  source->en->target — exactly the pivot the daemon runs (internal/providers/
 *  pivot). Keeping this in lockstep means the picker never disables a route the
 *  daemon would accept, nor offers one it would reject. */
export function translationSupported(
  pairs: readonly TranslationPair[],
  source: string,
  target: string,
): boolean {
  if (sameBaseLanguage(source, target)) return true;

  const has = (from: string, to: string): boolean =>
    pairs.some((pair) => sameBaseLanguage(pair.from, from) && sameBaseLanguage(pair.to, to));

  if (has(source, target)) return true;
  if (sameBaseLanguage(source, HUB) || sameBaseLanguage(target, HUB)) return false;

  return has(source, HUB) && has(HUB, target);
}

/** autoTranslationTargetSupported returns the cautious union of configured pair endpoints. */
export function autoTranslationTargetSupported(
  pairs: readonly TranslationPair[],
  target: string,
): boolean {
  return pairs.some((pair) =>
    sameBaseLanguage(pair.from, target) || sameBaseLanguage(pair.to, target)
  );
}
