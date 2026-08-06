// Screenshot regression over the dashboard's main surfaces: the design
// system's last line of defense — a refactor that shifts spacing, color or
// hierarchy fails here even when every behavioral assertion still passes.
//
// Baselines are recorded on macOS (system fonts differ per OS, so a shared
// baseline cannot exist); other platforms skip explicitly rather than
// pretending to cover this. Chromium only, hermetic API stubs throughout, so
// a pixel diff means the UI changed — not the daemon.

import { expect, test, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

test.skip(process.platform !== "darwin", "visual baselines are recorded on macOS");
test.skip(({ browserName }) => browserName !== "chromium", "one engine, stable pixels");

function controlToken(): string {
  return readFileSync(
    join(process.env.TMPDIR ?? tmpdir(), "prukka-e2e-state", "control.token"),
    "utf8",
  ).trim();
}

const packs = [
  { id: "stt-core", kind: "stt", installed: true, sizeBytes: "189000000" },
  { id: "voice-en", kind: "voice", lang: "en", installed: true, sizeBytes: "52428800" },
  { id: "voice-it", kind: "voice", lang: "it", installed: true, sizeBytes: "52428800" },
  { id: "voice-de", kind: "voice", lang: "de", installed: false, sizeBytes: "104857600" },
  { id: "mt-it-en", kind: "mt", from: "it", to: "en", installed: true, sizeBytes: "31457280" },
  { id: "mt-en-it", kind: "mt", from: "en", to: "it", installed: true, sizeBytes: "31457280" },
  { id: "mt-de-en", kind: "mt", from: "de", to: "en", installed: false, sizeBytes: "31457280" },
  { id: "mt-en-de", kind: "mt", from: "en", to: "de", installed: false, sizeBytes: "31457280" },
];

const devices = [
  { url: "device://audio/speaker-in", label: "Prukka Speaker", kind: "audio-in", virtual: true },
  { url: "device://audio/local-mic", label: "Built-in Microphone", kind: "audio-in" },
  { url: "device://audio/listen", label: "Built-in Output", kind: "audio-out" },
  { url: "device://audio/call-mic", label: "Prukka Microphone", kind: "audio-out", virtual: true },
  { url: "device://video/cam", label: "Built-in Camera", kind: "video-in" },
];

// Every dynamic surface is pinned so the only signal is the UI itself.
async function routeHermetic(page: Page, engineInstalled = true) {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/stats", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ sessionsActive: 0, uptimeSeconds: 3915, version: "1.0.0" }),
    }),
  );
  await page.route("**/api/v1/sessions", (route) =>
    route.fulfill({ contentType: "application/json", body: '{"sessions":[]}' }),
  );
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify({ devices }) }),
  );
  await page.route("**/api/v1/doctor", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        checks: [
          { name: "speech-engine", status: "ok", detail: "managed bundle installed" },
          { name: "ffmpeg", status: "ok", detail: "pinned build" },
        ],
      }),
    }),
  );
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "local",
            local: {
              dubbedLangs: ["en", "it"],
              mt: { pairs: [{ from: "it", to: "en" }, { from: "en", to: "it" }] },
            },
          },
          defaults: { langs: ["it", "en"], subs: "vtt", bed: "-15dB", delaySeconds: 8 },
        },
      }),
    }),
  );
  await page.route("**/api/v1/engine", (route: Route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        engine: {
          installed: engineInstalled,
          protocol: 2,
          packs: engineInstalled
            ? packs
            : packs.map((pack) => ({ ...pack, installed: false })),
        },
      }),
    }),
  );
}

function masks(page: Page) {
  // Uptime keeps counting even against stubs; nothing else on these pages moves.
  return [page.locator("header dl")];
}

const options = (page: Page) => ({
  animations: "disabled" as const,
  caret: "hide" as const,
  mask: masks(page),
  maxDiffPixelRatio: 0.002,
});

for (const scheme of ["dark", "light"] as const) {
  test(`home surface (${scheme})`, async ({ page }) => {
    await routeHermetic(page);
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto(`/ui/#token=${controlToken()}`);
    await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Languages" })).toBeVisible();

    await expect(page).toHaveScreenshot(`home-${scheme}.png`, options(page));
  });
}

test("wizard languages step (dark)", async ({ page }) => {
  await routeHermetic(page);
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto(`/ui/#token=${controlToken()}`);
  await page
    .getByRole("group", { name: "What do you want to translate?" })
    .getByRole("button", { name: /^Stream or file/ })
    .click();
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/visual");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose languages" })).toBeVisible();

  await expect(page).toHaveScreenshot("wizard-languages-dark.png", options(page));
});

test("wizard review step (dark)", async ({ page }) => {
  await routeHermetic(page);
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto(`/ui/#token=${controlToken()}`);
  await page
    .getByRole("group", { name: "What do you want to translate?" })
    .getByRole("button", { name: /^Stream or file/ })
    .click();
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/visual");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose languages" })).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose where it goes" })).toBeVisible();
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Review and start" })).toBeVisible();
  // The suggested name is random; pin it so the shot is stable.
  await page.getByLabel("Name").fill("visual-review");

  await expect(page).toHaveScreenshot("wizard-review-dark.png", options(page));
});

test("first-run hero (dark)", async ({ page }) => {
  await routeHermetic(page, false);
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto(`/ui/#token=${controlToken()}`);
  await expect(page.getByRole("heading", { name: "Get Prukka ready" })).toBeVisible();

  await expect(page).toHaveScreenshot("first-run-dark.png", options(page));
});
