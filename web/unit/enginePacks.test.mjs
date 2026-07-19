import assert from "node:assert/strict";
import test from "node:test";

import {
  installedLanguages,
  languagePlans,
  mib,
  operationBusy,
  totalSizeBytes,
  voiceLanguage,
} from "../src/lib/enginePacks.ts";

// A hub catalog: every MT pack is en<->X. Cross-language pairs (e.g. de<->it)
// never exist — pivoting reaches them through en.
const catalog = [
  { id: "stt-core", kind: "stt", installed: true, sizeBytes: "189000000" },
  { id: "voice-en", kind: "voice", lang: "en", installed: true, sizeBytes: "52428800" },
  { id: "voice-it", kind: "voice", lang: "it", installed: true, sizeBytes: "52428800" },
  { id: "voice-de", kind: "voice", lang: "de", sizeBytes: "104857600" },
  { id: "mt-it-en", kind: "mt", from: "it", to: "en", installed: true, sizeBytes: "31457280" },
  { id: "mt-en-it", kind: "mt", from: "en", to: "it", sizeBytes: "31457280" },
  { id: "mt-de-en", kind: "mt", from: "de", to: "en", sizeBytes: "31457280" },
  { id: "mt-en-de", kind: "mt", from: "en", to: "de", sizeBytes: "31457280" },
];

test("installed languages come from installed voice packs", () => {
  assert.deepEqual(installedLanguages(catalog), ["en", "it"]);
  assert.equal(voiceLanguage({ id: "voice-pt", kind: "voice" }), "pt");
  assert.equal(voiceLanguage({ id: "custom", kind: "voice", lang: "fr" }), "fr");
});

test("a language needs its voice and both hub routes; the hub needs only its voice", () => {
  const plans = languagePlans({ packs: catalog });
  const byTag = new Map(plans.map((plan) => [plan.tag, plan]));

  // en is the hub: it owns no routes, so its installed voice is enough.
  assert.equal(byTag.get("en").state, "installed");
  assert.deepEqual(byTag.get("en").required.map((pack) => pack.id), ["voice-en"]);

  // it has its voice and it->en but misses en->it, so it is partial.
  assert.equal(byTag.get("it").state, "partial");
  assert.deepEqual(byTag.get("it").missing.map((pack) => pack.id), ["mt-en-it"]);

  // de has nothing installed: available, voice first, then its two hub routes.
  const de = byTag.get("de");
  assert.equal(de.state, "available");
  assert.deepEqual(
    de.missing.map((pack) => pack.id),
    ["voice-de", "mt-de-en", "mt-en-de"],
  );

  // Completing en->it makes it fully installed; en was already installed.
  const complete = catalog.map((pack) =>
    pack.id === "mt-en-it" ? { ...pack, installed: true } : pack
  );
  const completed = new Map(languagePlans({ packs: complete }).map((plan) => [plan.tag, plan]));
  assert.equal(completed.get("it").state, "installed");
  assert.equal(completed.get("en").state, "installed");
});

test("removal takes the voice plus the language's own installed hub routes", () => {
  const complete = catalog.map((pack) =>
    pack.id === "mt-en-it" ? { ...pack, installed: true } : pack
  );
  const it = languagePlans({ packs: complete }).find((plan) => plan.tag === "it");
  assert.deepEqual(
    it.removable.map((pack) => pack.id),
    ["voice-it", "mt-it-en", "mt-en-it"],
  );

  // The hub owns no routes, so removing en never drags other languages' spokes.
  const en = languagePlans({ packs: complete }).find((plan) => plan.tag === "en");
  assert.deepEqual(en.removable.map((pack) => pack.id), ["voice-en"]);
});

test("sizes sum gateway int64 strings and render as whole MiB", () => {
  assert.equal(totalSizeBytes([{ id: "a", kind: "mt", sizeBytes: "31457280" }, { id: "b", kind: "mt" }]), 31457280);
  assert.equal(mib(31457280), "30");
  assert.equal(mib(0), "0");
});

test("only download, verify and install count as a busy operation", () => {
  for (const phase of ["download", "verify", "install"]) {
    assert.equal(operationBusy({ kind: "install-pack", phase }), true);
  }
  assert.equal(operationBusy({ kind: "install-pack", phase: "done" }), false);
  assert.equal(operationBusy({ kind: "install-pack", phase: "error" }), false);
  assert.equal(operationBusy(undefined), false);
});
