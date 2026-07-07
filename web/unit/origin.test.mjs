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
