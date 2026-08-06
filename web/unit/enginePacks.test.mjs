import assert from "node:assert/strict";
import test from "node:test";

import {
  installedLanguages,
  languagePlans,
  mb,
  mib,
  normalizeEngineStatus,
  operationBusy,
  packsToAdd,
  planForLanguage,
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

test("mb renders whole decimal megabytes for the user-facing size", () => {
  assert.equal(mb(45_000_000), "45");
  assert.equal(mb(31_457_280), "31");
  assert.equal(mb(0), "0");
});

test("planForLanguage resolves an exact tag, then falls back to the base language", () => {
  const plans = languagePlans({ packs: catalog });
  assert.equal(planForLanguage(plans, "it")?.tag, "it");
  // pt is not in the catalog at all — no plan to bridge to.
  assert.equal(planForLanguage(plans, "pt-BR"), undefined);

  // A regional variant resolves to its base pack when that base is present.
  const withPt = [
    ...catalog,
    { id: "voice-pt", kind: "voice", lang: "pt", sizeBytes: "52428800" },
    { id: "mt-pt-en", kind: "mt", from: "pt", to: "en", sizeBytes: "31457280" },
    { id: "mt-en-pt", kind: "mt", from: "en", to: "pt", sizeBytes: "31457280" },
  ];
  const ptPlans = languagePlans({ packs: withPt });
  assert.equal(planForLanguage(ptPlans, "pt-BR")?.tag, "pt");
});

test("packsToAdd installs the whole bundle for a voice, only MT routes for captions", () => {
  const de = languagePlans({ packs: catalog }).find((plan) => plan.tag === "de");
  assert.deepEqual(packsToAdd(de, true).map((pack) => pack.id), ["voice-de", "mt-de-en", "mt-en-de"]);
  // captions-only excludes the voice pack entirely.
  assert.deepEqual(packsToAdd(de, false).map((pack) => pack.id), ["mt-de-en", "mt-en-de"]);
});

test("only download, verify and install count as a busy operation", () => {
  for (const phase of ["download", "verify", "install"]) {
    assert.equal(operationBusy({ kind: "install-pack", phase }), true);
  }
  assert.equal(operationBusy({ kind: "install-pack", phase: "done" }), false);
  assert.equal(operationBusy({ kind: "install-pack", phase: "error" }), false);
  assert.equal(operationBusy(undefined), false);
  // The gateway (EmitUnpopulated) renders an idle engine as operation: null.
  assert.equal(operationBusy(null), false);
});

test("normalization scrubs the gateway's null operation before it enters the app", () => {
  const idle = normalizeEngineStatus({ installed: true, operation: null });
  assert.equal(idle.operation, undefined);
  assert.equal(idle.installed, true);

  const busy = {
    installed: true,
    operation: { kind: "install-pack", phase: "download" },
  };
  assert.deepEqual(normalizeEngineStatus(busy), busy);
});
