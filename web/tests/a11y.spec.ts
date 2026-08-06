// Automated accessibility gate: axe-core over the dashboard's main surfaces,
// in both color schemes. This complements — never replaces — the behavioral
// a11y tests in dashboard.spec.ts (focus order, reflow, names); axe covers
// the rule classes it implements — roles, names, order and TEXT contrast
// (WCAG 1.4.3). It ships no rule for non-text contrast (1.4.11), so control
// boundaries are guarded by web/unit/tokens.test.mjs instead.
//
// Chromium only: axe analyzes the rendered DOM, which the three-engine
// behavioral gate already proves is equivalent; tripling the scan adds
// minutes, not coverage.

import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

test.skip(({ browserName }) => browserName !== "chromium", "axe runs once, on chromium");

function controlToken(): string {
  return readFileSync(
    join(process.env.TMPDIR ?? tmpdir(), "prukka-e2e-state", "control.token"),
    "utf8",
  ).trim();
}

async function expectNoViolations(page: Page) {
  // Contrast is a property of the resting UI: `.step-enter` fades a step in
  // over 240 ms, and a scan that lands inside the ramp measures both the text
  // and its backdrop part-way to transparent.
  await page.waitForFunction(() =>
    document.getAnimations().every((animation) => animation.playState !== "running")
  );
  const results = await new AxeBuilder({ page }).analyze();
  const summary = results.violations.map((violation) => ({
    id: violation.id,
    impact: violation.impact,
    nodes: violation.nodes.map((node) => node.target.join(" ")),
  }));
  expect(summary).toEqual([]);
}

for (const scheme of ["dark", "light"] as const) {
  test(`the ${scheme} home surface has no axe violations`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto(`/ui/#token=${controlToken()}`);
    await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();

    await expectNoViolations(page);
  });
}

test("every wizard step has no axe violations", async ({ page }) => {
  await page.goto(`/ui/#token=${controlToken()}`);

  await page
    .getByRole("group", { name: "What do you want to translate?" })
    .getByRole("button", { name: /^Stream or file/ })
    .click();
  await expectNoViolations(page);

  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/a11y");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose languages" })).toBeVisible();
  await expectNoViolations(page);

  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose where it goes" })).toBeVisible();
  await expectNoViolations(page);

  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Review and start" })).toBeVisible();
  await expectNoViolations(page);
});

test("an open dropdown and the push dialog have no axe violations", async ({ page }) => {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/sessions", (route) => {
    if (route.request().method() !== "GET") return route.continue();
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        sessions: [{
          slug: "a11y-session",
          profile: "broadcast",
          sourceLabel: "file://[local]",
          langs: ["en"],
          effectiveDubbedLangs: ["en"],
          status: "running",
        }],
      }),
    });
  });

  await page.goto(`/ui/#token=${controlToken()}`);

  await page
    .getByRole("group", { name: "What do you want to translate?" })
    .getByRole("button", { name: /^Stream or file/ })
    .click();
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/a11y");
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("combobox", { name: "Source language" }).click();
  await expect(page.getByRole("listbox", { name: "Source language" })).toBeVisible();
  await expectNoViolations(page);
  await page.keyboard.press("Escape");

  const row = page.getByRole("row").filter({ hasText: "a11y-session" });
  await row.getByRole("button", { name: "Push" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expectNoViolations(page);
});
