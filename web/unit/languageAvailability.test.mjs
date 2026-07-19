import assert from "node:assert/strict";
import test from "node:test";

import { languagePlans } from "../src/lib/enginePacks.ts";
import { languageInfo } from "../src/lib/languageAvailability.ts";

// A hub catalog mirroring the daemon: en and it fully installed, de only
// listed. The MT pairs the daemon reports match the installed route packs.
const catalog = [
  { id: "stt-core", kind: "stt", installed: true, sizeBytes: "189000000" },
  { id: "voice-en", kind: "voice", lang: "en", installed: true, sizeBytes: "52428800" },
  { id: "voice-it", kind: "voice", lang: "it", installed: true, sizeBytes: "52428800" },
  { id: "voice-de", kind: "voice", lang: "de", sizeBytes: "104857600" },
  { id: "mt-it-en", kind: "mt", from: "it", to: "en", installed: true, sizeBytes: "31457280" },
  { id: "mt-en-it", kind: "mt", from: "en", to: "it", installed: true, sizeBytes: "31457280" },
  { id: "mt-de-en", kind: "mt", from: "de", to: "en", sizeBytes: "31457280" },
  { id: "mt-en-de", kind: "mt", from: "en", to: "de", sizeBytes: "31457280" },
];

function ctx(overrides = {}) {
  return {
    pairs: [{ from: "it", to: "en" }, { from: "en", to: "it" }],
    dubbedLangs: ["en", "it"],
    voicesOff: false,
    plans: languagePlans({ packs: catalog }),
    engineSupported: true,
    catalogAvailable: true,
    source: "auto",
    ...overrides,
  };
}

test("an installed route and voice classify as ready with no download", () => {
  const dubbed = languageInfo(ctx(), "it", true);
  assert.equal(dubbed.status, "ready");
  assert.equal(dubbed.addBytes, 0);
  assert.equal(dubbed.needVoice, true);
  assert.equal(dubbed.captionsOnly, false);

  // Captions-only asks nothing of the voices.
  assert.equal(languageInfo(ctx(), "it", false).status, "ready");
});

test("a missing language is addable with the whole bundle for a voice, routes only for captions", () => {
  const dubbed = languageInfo(ctx(), "de", true);
  assert.equal(dubbed.status, "addable");
  // voice-de + mt-de-en + mt-en-de.
  assert.equal(dubbed.addBytes, 104857600 + 31457280 + 31457280);
  assert.equal(dubbed.needVoice, true);

  const captions = languageInfo(ctx(), "de", false);
  assert.equal(captions.status, "addable");
  // The two hub routes only — a captions add never downloads a voice.
  assert.equal(captions.addBytes, 31457280 + 31457280);
  assert.equal(captions.needVoice, false);
});

test("a concrete source classifies a partially installed language by its missing route", () => {
  // en->it is not installed: from source en the language needs one route pack.
  const partial = catalog.map((pack) =>
    pack.id === "mt-en-it" ? { ...pack, installed: false } : pack,
  );
  const info = languageInfo(
    ctx({ pairs: [{ from: "it", to: "en" }], plans: languagePlans({ packs: partial }), source: "en" }),
    "it",
    true,
  );
  assert.equal(info.status, "addable");
  assert.equal(info.addBytes, 31457280);
});

test("voices-off daemons degrade to captions-only and never ask for a voice pack", () => {
  const off = ctx({ voicesOff: true, dubbedLangs: [] });

  // The wanted voice can never apply, so the add ships only the MT routes.
  const add = languageInfo(off, "de", true);
  assert.equal(add.status, "addable");
  assert.equal(add.needVoice, false);
  assert.equal(add.captionsOnly, true);
  assert.equal(add.addBytes, 31457280 + 31457280);

  // An installed route stays ready even though dubbing is off.
  const ready = languageInfo(off, "it", true);
  assert.equal(ready.status, "ready");
  assert.equal(ready.needVoice, false);
  assert.equal(ready.captionsOnly, true);
});

test("no catalog plan means genuinely unavailable, not addable", () => {
  const info = languageInfo(ctx(), "fr", true);
  assert.equal(info.status, "unavailable");
  assert.equal(info.addBytes, 0);
});

test("an unreachable catalog or an unsupported daemon disables the download path", () => {
  assert.equal(languageInfo(ctx({ catalogAvailable: false }), "de", true).status, "unavailable");
  assert.equal(languageInfo(ctx({ engineSupported: false }), "de", true).status, "unavailable");
});
