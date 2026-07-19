#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(import.meta.url);
const root = resolve(dirname(script), "../..");
const legalName =
  /^(?:(?:licen[cs]e|copying|notice|patents)|third[-_.]?party[-_.]?notices?(?:text)?)(?:[._-].*)?$/i;
const maxLicenseBytes = 2 << 20;

export const releaseTargets = Object.freeze([
  { goos: "darwin", goarch: "amd64", cgo: "1", tags: ["bundleddrivers"] },
  { goos: "darwin", goarch: "arm64", cgo: "1", tags: ["bundleddrivers"] },
  { goos: "linux", goarch: "amd64", cgo: "0", tags: ["bundleddrivers"] },
  { goos: "linux", goarch: "arm64", cgo: "0", tags: ["bundleddrivers"] },
  { goos: "windows", goarch: "amd64", cgo: "0", tags: ["bundleddrivers"] },
]);

const embeddedPayloads = [
  "internal/devices/assets/darwin/microphone.tar.gz",
  "internal/devices/assets/darwin/speaker.tar.gz",
  "internal/devices/assets/darwin/webcam.tar.gz",
  "internal/devices/assets/linux/src.tar.gz",
  "internal/devices/assets/windows/webcam.tar.gz",
];

export function compareBytes(a, b) {
  return Buffer.compare(Buffer.from(a), Buffer.from(b));
}

export function isLegalDocument(name) {
  return legalName.test(name);
}

function licenseFiles(dir) {
  return readdirSync(dir)
    .filter(isLegalDocument)
    .filter((name) => statSync(resolve(dir, name)).isFile())
    .sort(compareBytes);
}

function readLicense(dir, name) {
  const path = resolve(dir, name);
  const size = statSync(path).size;
  if (size > maxLicenseBytes) {
    throw new Error(`${path} exceeds ${maxLicenseBytes} bytes`);
  }

  const text = readFileSync(path, "utf8").replaceAll("\r\n", "\n").trimEnd();
  if (text.includes("\0")) {
    throw new Error(`${path} is not text`);
  }

  return text;
}

function addPackage(documents, label, source, dir, declaredLicense) {
  const files = licenseFiles(dir);
  if (files.length === 0) {
    if (!declaredLicense) {
      throw new Error(`${label} has no license file or declared license`);
    }
    documents.push({
      label,
      source,
      file: "package metadata",
      text: `Declared license: ${declaredLicense}\nNo standalone license file was included in the package archive.`,
    });
    return;
  }

  for (const file of files) {
    documents.push({
      label,
      source: typeof source === "function" ? source(file) : source,
      file,
      text: readLicense(dir, file),
    });
  }
}

function cleanGoEnv(extra = {}) {
  return {
    ...process.env,
    GOENV: "off",
    GOFLAGS: "",
    GOWORK: "off",
    ...extra,
  };
}

function goRuntime(documents) {
  const [goroot, version] = execFileSync("go", ["env", "GOROOT", "GOVERSION"], {
    cwd: root,
    encoding: "utf8",
    env: cleanGoEnv(),
  })
    .trim()
    .split("\n");
  if (!goroot || !version) {
    throw new Error("incomplete Go runtime metadata");
  }
  addPackage(
    documents,
    `Go runtime/stdlib: ${version}`,
    (file) => `https://go.dev/${file}`,
    goroot,
    "",
  );
}

function goModules(documents) {
  const temp = mkdtempSync(join(tmpdir(), "prukka-notices-"));
  try {
    const payload = resolve(temp, "payload");
    const overlay = resolve(temp, "overlay.json");
    writeFileSync(payload, "notices\n", "utf8");
    writeFileSync(
      overlay,
      JSON.stringify({
        Replace: Object.fromEntries(
          embeddedPayloads.map((path) => [resolve(root, path), payload]),
        ),
      }),
      "utf8",
    );

    const moduleLines = new Set();
    for (const target of releaseTargets) {
      const modules = execFileSync(
        "go",
        [
          "list",
          "-mod=readonly",
          `-overlay=${overlay}`,
          `-tags=${target.tags.join(",")}`,
          "-deps",
          "-f",
          "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}",
          "./cmd/prukka",
        ],
        {
          cwd: root,
          encoding: "utf8",
          env: cleanGoEnv({
            GOOS: target.goos,
            GOARCH: target.goarch,
            CGO_ENABLED: target.cgo,
          }),
        },
      );
      for (const line of modules.split("\n").filter(Boolean)) {
        moduleLines.add(line);
      }
    }

    for (const line of [...moduleLines].sort(compareBytes)) {
      const [name, version, dir] = line.split("\t");
      if (!name || !version || !dir) {
        throw new Error(`incomplete Go module metadata: ${line}`);
      }
      addPackage(
        documents,
        `Go: ${name}@${version}`,
        `https://pkg.go.dev/${name}@${version}`,
        dir,
        "",
      );
    }
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
}

function npmPackages(documents) {
  const web = resolve(root, "web");
  const lock = JSON.parse(readFileSync(resolve(web, "package-lock.json"), "utf8"));
  const packages = Object.entries(lock.packages).sort(([a], [b]) =>
    compareBytes(a, b),
  );
  for (const [path, metadata] of packages) {
    if (!path || metadata.optional) {
      continue;
    }

    const dir = resolve(web, path);
    if (!existsSync(dir) || relative(web, dir).startsWith("..")) {
      throw new Error(`${path} is missing; run npm ci in web/ first`);
    }

    const manifest = JSON.parse(readFileSync(resolve(dir, "package.json"), "utf8"));
    addPackage(
      documents,
      `npm: ${manifest.name}@${manifest.version}`,
      metadata.resolved ?? manifest.homepage ?? "npm package archive",
      dir,
      metadata.license ?? manifest.license,
    );
  }
}

function render(documents) {
  const grouped = new Map();
  for (const document of documents) {
    const hash = createHash("sha256").update(document.text).digest("hex");
    const group = grouped.get(hash) ?? { text: document.text, entries: [] };
    group.entries.push(document);
    grouped.set(hash, group);
  }

  const groups = [...grouped.values()].sort((a, b) =>
    compareBytes(a.entries[0].label, b.entries[0].label),
  );
  const chunks = [
    "THIRD-PARTY NOTICES",
    "",
    "Generated by hack/ci/third-party-notices.mjs from the locked Go and dashboard dependencies.",
    "Prukka's own licensing terms are in LICENSE and drivers/linux/LICENSE.",
  ];

  for (const group of groups) {
    chunks.push("", "=".repeat(80));
    for (const entry of group.entries.sort((a, b) => compareBytes(a.label, b.label))) {
      chunks.push(`${entry.label} — ${entry.file}`, `Source: ${entry.source}`);
    }
    chunks.push("-".repeat(80), group.text);
  }

  return `${chunks.join("\n")}\n`;
}

export function generateNotices(output) {
  const documents = [];
  goRuntime(documents);
  goModules(documents);
  npmPackages(documents);

  const temp = `${output}.tmp-${process.pid}`;
  try {
    writeFileSync(temp, render(documents), "utf8");
    renameSync(temp, output);
  } finally {
    rmSync(temp, { force: true });
  }
  return documents.length;
}

if (process.argv[1] && resolve(process.argv[1]) === script) {
  const output = resolve(root, process.argv[2] ?? "NOTICE.txt");
  const count = generateNotices(output);
  console.log(`wrote ${relative(root, output)} (${count} dependency documents)`);
}
