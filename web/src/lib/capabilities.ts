import type { TranslationPair } from "./api/types";

export function baseLanguage(tag: string): string {
  return tag.trim().toLowerCase().split("-", 1)[0] ?? "";
}

export function sameBaseLanguage(left: string, right: string): boolean {
  const leftBase = baseLanguage(left);
  return leftBase !== "" && leftBase === baseLanguage(right);
}

/** translationSupported mirrors the daemon: same-base targets need no MT model. */
export function translationSupported(
  pairs: readonly TranslationPair[],
  source: string,
  target: string,
): boolean {
  if (sameBaseLanguage(source, target)) return true;

  const sourceBase = baseLanguage(source);
  const targetBase = baseLanguage(target);
  return pairs.some((pair) =>
    baseLanguage(pair.from) === sourceBase && baseLanguage(pair.to) === targetBase
  );
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
