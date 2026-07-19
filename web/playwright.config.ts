import { existsSync } from "node:fs";

import { defineConfig, devices } from "@playwright/test";
import { webkit } from "playwright-core";

// Cross-browser gate for the dashboard: the suite drives a REAL daemon
// (hack/e2e/daemon.sh) through the embedded SPA. WebKit cannot even be
// installed on every dev host (Playwright drops old macOS), so its project
// exists only where its browser does — while CI must fail loudly if an
// engine is missing rather than let the 3-engine gate shrink silently.
const PORT = process.env.PRUKKA_E2E_PORT ?? "18093";

function hasWebkit(): boolean {
  try {
    return existsSync(webkit.executablePath());
  } catch {
    return false;
  }
}

const webkitPresent = hasWebkit();

if (process.env.CI && !webkitPresent) {
  throw new Error(
    "webkit is not installed in CI: run `npx --no-install playwright install --with-deps`",
  );
}

export default defineConfig({
  testDir: "./tests",
  timeout: 60_000,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  // "list" everywhere: the github reporter stamps a summary annotation on
  // every run — even a fully green one — and this repo keeps its checks
  // annotation-free. Failures still print in full in the job log.
  reporter: "list",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "on-first-retry",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "firefox", use: { ...devices["Desktop Firefox"] } },
    ...(webkitPresent
      ? [{ name: "webkit", use: { ...devices["Desktop Safari"] } }]
      : []),
  ],
  webServer: {
    command: "../hack/e2e/daemon.sh",
    url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
