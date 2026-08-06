import assert from "node:assert/strict";
import test from "node:test";

import { apiBase } from "../src/lib/api/origin.ts";

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
