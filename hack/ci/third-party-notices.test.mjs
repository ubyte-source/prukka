import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  compareBytes,
  isLegalDocument,
  releaseTargets,
} from "./third-party-notices.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

function releaseConfigTargets() {
  const config = readFileSync(resolve(root, ".goreleaser.yaml"), "utf8");
  const start = config.indexOf("builds:\n");
  const end = config.indexOf("\narchives:");
  assert.ok(start >= 0 && end > start, "missing GoReleaser build matrix");
  const builds = config.slice(start, end);
  return builds
    .split(/\n  - id: /)
    .slice(1)
    .flatMap((block) => {
      const cgo = block.match(/\n      - CGO_ENABLED=(\d)/)?.[1];
      const goos = block.match(/\n    goos: \[([^\]]+)]/)?.[1].split(/,\s*/);
      const goarch = block.match(/\n    goarch: \[([^\]]+)]/)?.[1].split(/,\s*/);
      const tags = [...block.matchAll(/\n      - ([a-z][a-z0-9]*)/g)].map(
        (match) => match[1],
      );
      assert.ok(
        cgo && goos && goarch && tags.length > 0,
        "unrecognized GoReleaser build",
      );
      return goos.flatMap((os) =>
        goarch.map((arch) => ({ goos: os, goarch: arch, cgo, tags })),
      );
    });
}

function targetKey(target) {
  return `${target.goos}/${target.goarch}/${target.cgo}/${target.tags.join(",")}`;
}

test("release matrix matches GoReleaser", () => {
  assert.deepEqual(
    releaseTargets.map(targetKey).sort(compareBytes),
    releaseConfigTargets().map(targetKey).sort(compareBytes),
  );
});

test("legal document matcher covers grants and bundled notices", () => {
  for (const name of [
    "LICENSE",
    "LICENSE.md",
    "COPYING",
    "NOTICE.txt",
    "PATENTS",
    "ThirdPartyNotices.txt",
    "ThirdPartyNoticeText.txt",
  ]) {
    assert.equal(isLegalDocument(name), true, name);
  }
  assert.equal(isLegalDocument("README.md"), false);
});

test("byte ordering is locale independent", () => {
  assert.deepEqual(
    ["ä", "z", "a", "A", "@"].sort(compareBytes),
    ["@", "A", "a", "z", "ä"],
  );
});
