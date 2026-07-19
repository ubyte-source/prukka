import assert from "node:assert/strict";
import test from "node:test";

import { apiBase } from "../src/lib/api/origin.ts";
import {
  autoTranslationTargetSupported,
  sameBaseLanguage,
  translationSupported,
} from "../src/lib/capabilities.ts";

test("the embedded marker keeps API calls on any dashboard origin", () => {
  assert.equal(apiBase("same-origin"), "");
});

test("hosted dashboards use only an explicit HTTP(S) API origin", () => {
  assert.equal(apiBase("http://127.0.0.1:8080"), "http://127.0.0.1:8080");
  assert.equal(apiBase("https://daemon.example:8443"), "https://daemon.example:8443");

  for (const configured of [null, "", "same-origin/", "javascript:alert(1)", "https://user@host/"]) {
    assert.throws(() => apiBase(configured), configured ?? "null");
  }
});

test("language capabilities compare base tags and directed MT pairs", () => {
  const pairs = [{ from: "it", to: "en" }];

  assert.equal(sameBaseLanguage("en-GB", "en-US"), true);
  assert.equal(translationSupported(pairs, "it-IT", "en-GB"), true);
  assert.equal(translationSupported(pairs, "it-IT", "it"), true);
  assert.equal(translationSupported(pairs, "en", "it"), false);
  assert.equal(autoTranslationTargetSupported(pairs, "it-IT"), true);
  assert.equal(autoTranslationTargetSupported(pairs, "en-GB"), true);
  assert.equal(autoTranslationTargetSupported(pairs, "de"), false);
  assert.equal(autoTranslationTargetSupported([], "it"), false);
});

test("translation capabilities bridge indirect routes through the English hub", () => {
  const spokes = [
    { from: "it", to: "en" }, { from: "en", to: "it" },
    { from: "en", to: "de" }, { from: "de", to: "en" },
  ];

  // Bridged both ways through en, though no direct it<->de pair is installed.
  assert.equal(translationSupported(spokes, "it", "de"), true);
  assert.equal(translationSupported(spokes, "de-CH", "it-IT"), true);
  // Direct spokes to and from the hub still resolve directly.
  assert.equal(translationSupported(spokes, "en", "de"), true);
  assert.equal(translationSupported(spokes, "it", "en"), true);
  // No hub leg to French: neither direct nor bridged.
  assert.equal(translationSupported(spokes, "it", "fr"), false);

  // A one-sided spoke set bridges only in the direction its legs allow.
  const oneSided = [{ from: "it", to: "en" }, { from: "en", to: "de" }];
  assert.equal(translationSupported(oneSided, "it", "de"), true);
  assert.equal(translationSupported(oneSided, "de", "it"), false);
});
