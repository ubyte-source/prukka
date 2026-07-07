// Dashboard e2e: a real daemon, the embedded SPA, three browser engines.
// Covers the load path, the registry-fed wizard, and the full session
// lifecycle (create → live table via SSE → remove) using the control token
// exactly as `prukka up` hands it off — via the URL fragment.

import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

function controlToken(): string {
  return readFileSync(
    join(process.env.TMPDIR ?? tmpdir(), "prukka-e2e-state", "control.token"),
    "utf8",
  ).trim();
}

test("loads with live daemon stats", async ({ page }) => {
  await page.goto("/ui/");

  await expect(page).toHaveTitle("Prukka — dashboard");
  await expect(page.getByRole("heading", { name: "Prukka" })).toBeVisible();

  // The stats strip fills from /api/v1/stats: a real version, not the dash.
  await expect(page.locator("dl dd").nth(3)).not.toHaveText("–");
});

test("wizard dropdowns come from the language registry", async ({ page }) => {
  await page.goto("/ui/");

  // No hardcoded languages: the source dropdown and the target
  // chips are fed by /api/v1/languages.
  const source = page.getByLabel("Source language");
  await expect(source.locator("option", { hasText: "Italiano — it" })).toHaveCount(1);

  await expect(
    page.getByRole("group", { name: "Target languages" })
      .getByRole("button", { name: "English — en" }),
  ).toBeVisible();
});

test("session lifecycle: create, appears live, remove", async ({ page }) => {
  // Token hand-off exactly as `prukka up` does it: URL fragment.
  await page.goto(`/ui/#token=${controlToken()}`);

  await page.getByLabel("Name").fill("e2e-demo");
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/e2e-demo");
  await page
    .getByRole("group", { name: "Target languages" })
    .getByRole("button", { name: "English — en" })
    .click();
  await page.getByRole("button", { name: "Create session" }).click();

  // The SSE stream refreshes the table without a reload.
  const row = page.getByRole("row").filter({ hasText: "e2e-demo" });
  await expect(row).toBeVisible();
  await expect(row.getByRole("link", { name: "en", exact: true })).toBeVisible();

  await row.getByRole("button", { name: "remove session e2e-demo" }).click();
  await expect(row).toHaveCount(0);
});

test("writes without a token are rejected with an honest error", async ({
  page,
}) => {
  await page.goto("/ui/");
  await page.evaluate(() => sessionStorage.clear());

  await page.getByLabel("Name").fill("no-token");
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/x");
  await page
    .getByRole("group", { name: "Target languages" })
    .getByRole("button", { name: "English — en" })
    .click();
  await page.getByRole("button", { name: "Create session" }).click();

  await expect(page.getByRole("alert")).toBeVisible();
});

test("locale switch translates the UI and persists", async ({ page }) => {
  await page.goto("/ui/");

  await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();

  await page.getByRole("navigation", { name: "Language" }).getByRole("button", { name: "it" }).click();
  await expect(page.getByRole("heading", { name: "Nuova sessione" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "it");

  // The choice survives a reload.
  await page.reload();
  await expect(page.getByRole("heading", { name: "Nuova sessione" })).toBeVisible();

  await page.getByRole("navigation", { name: "Language" }).getByRole("button", { name: "en" }).click();
  await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();
});

test("settings edit persists through the daemon", async ({ page }) => {
  await page.goto(`/ui/#token=${controlToken()}`);

  const settings = page.locator("section", {
    has: page.getByRole("heading", { name: "Settings" }),
  });

  // The form loads the live configuration: the default backend is selected.
  await expect(
    settings.getByRole("radio", { name: "OpenRouter (hosted)" }),
  ).toBeChecked();

  // Switch to the local backend (progressive disclosure shows its fields)
  // and raise the per-session budget.
  await settings.getByText("Local (OpenAI-compatible)").click();
  await expect(
    settings.getByLabel("Transcription model", { exact: true }),
  ).toBeVisible();

  await settings.getByLabel("Per-session budget (€/h)").fill("6");
  await settings.getByRole("button", { name: "Save settings" }).click();

  await expect(settings.getByText("Settings saved and applied.")).toBeVisible();

  // The edit survives a full reload — it came back from the daemon's file,
  // not from browser state.
  await page.reload();
  await expect(
    settings.getByRole("radio", { name: "Local (OpenAI-compatible)" }),
  ).toBeChecked();
  await expect(settings.getByLabel("Per-session budget (€/h)")).toHaveValue("6");

  // Leave the suite's daemon on the default backend for later tests.
  await settings.getByText("OpenRouter (hosted)").click();
  await settings.getByRole("button", { name: "Save settings" }).click();
  await expect(settings.getByText("Settings saved and applied.")).toBeVisible();
});

test("settings save without a token is rejected honestly", async ({ page }) => {
  await page.goto("/ui/");
  await page.evaluate(() => sessionStorage.clear());

  const settings = page.locator("section", {
    has: page.getByRole("heading", { name: "Settings" }),
  });

  await settings.getByRole("button", { name: "Save settings" }).click();
  await expect(settings.getByText(/token|401|unauthor/i)).toBeVisible();
});
