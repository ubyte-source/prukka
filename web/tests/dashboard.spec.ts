// Dashboard e2e: a real daemon, the embedded SPA, three browser engines.
// Covers the load path, the registry-fed wizard, and the full session
// lifecycle (create → live table via SSE → remove) using the control token
// exactly as `prukka up` hands it off — via the URL fragment.

import { expect, test, type Page, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

function controlToken(): string {
  return readFileSync(
    join(process.env.TMPDIR ?? tmpdir(), "prukka-e2e-state", "control.token"),
    "utf8",
  ).trim();
}

function authenticatedDashboard(): string {
  return `/ui/#token=${controlToken()}`;
}

// The control token is held by the header's connect control. These drive it
// the way a user would: open it, paste the access code, unlock.
async function setToken(page: Page, value: string) {
  await page.getByRole("button", { name: /^(Connect Prukka|Connected)$/ }).click();
  await page.getByLabel("Access code").fill(value);
  await page.getByRole("button", { name: "Unlock" }).click();
}

async function disconnectToken(page: Page) {
  await page.getByRole("button", { name: /^(Connect Prukka|Connected)$/ }).click();
  await page.getByRole("button", { name: "Disconnect" }).click();
}

const callDevices = [
  { url: "device://audio/speaker-in", label: "Prukka Speaker", kind: "audio-in", virtual: true },
  { url: "device://audio/local-mic", label: "Built-in Microphone", kind: "audio-in" },
  { url: "device://audio/listen", label: "Built-in Output", kind: "audio-out" },
  { url: "device://audio/call-mic", label: "Prukka Microphone", kind: "audio-out", virtual: true },
];

async function fulfillCreatedSession(route: Route) {
  const session = route.request().postDataJSON() as {
    flags?: Record<string, string>;
    [key: string]: unknown;
  };
  const requested = (session.flags?.dub_langs ?? "").split(",").filter(Boolean);
  // The hermetic daemon uses its default English single-language voice.
  const effectiveDubbedLangs = requested.filter((tag) => tag.split("-")[0] === "en");
  await route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({ session: { ...session, effectiveDubbedLangs, status: "starting" } }),
  });
}

// The wizard asks for a source, not a profile: these helpers walk its four
// steps (source → languages → output → review) the way a user would.
async function pickSourceCard(page: Page, title: RegExp) {
  await page
    .getByRole("group", { name: "What do you want to translate?" })
    .getByRole("button", { name: title })
    .click();
}

async function fillSource(
  page: Page,
  profile: "broadcast" | "call",
  source: string,
) {
  if (profile === "call") {
    await pickSourceCard(page, /^A call I'm on/);
    const custom = page.locator('input[aria-label="Call audio source URL"]');
    if (await custom.isVisible()) await custom.fill(source);
  } else {
    await pickSourceCard(page, /^Stream or file/);
    await page.getByLabel("Source URL").fill(source);
  }
}

async function reachLanguages(
  page: Page,
  profile: "broadcast" | "call",
  source: string,
) {
  await fillSource(page, profile, source);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose languages" })).toBeVisible();
}

async function reachOutput(page: Page) {
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose where it goes" })).toBeVisible();
}

async function reachReview(page: Page, slug: string) {
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Review and start" })).toBeVisible();
  await page.getByLabel("Name").fill(slug);
}

/** createFromLanguages finishes the wizard from the languages step. */
async function createFromLanguages(page: Page, slug: string) {
  await reachOutput(page);
  await reachReview(page, slug);
  await page.getByRole("button", { name: "Create session" }).click();
}

async function setDubbedLanguages(page: Page, selected: string[]) {
  const buttons = await page
    .getByRole("group", { name: "Dubbed languages" })
    .getByRole("button")
    .all();
  for (const button of buttons) {
    const label = ((await button.textContent())?.trim() ?? "").replace(/^✓\s*/, "");
    const want = selected.includes(label);
    if ((await button.getAttribute("aria-pressed")) !== String(want)) await button.click();
  }
}

test("loads with live daemon stats", async ({ page }) => {
  await page.goto(authenticatedDashboard());

  await expect(page).toHaveTitle("Prukka — dashboard");
  await expect(page.getByRole("heading", { name: "Prukka", exact: true })).toBeVisible();

  // The stats strip fills from /api/v1/stats: a real version, not the dash.
  await expect(page.locator("dl dd").last()).not.toHaveText("–");
  await expect(page.locator("footer")).toContainText(
    "Configured routes and any network-reachable media listener can expose content",
  );
});

test("keyboard users can skip repeated navigation", async ({ page }) => {
  await page.goto(authenticatedDashboard());

  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to main content" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("main")).toBeFocused();
});

test("session actions have usable targets and the push dialog restores focus", async ({ page }) => {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/devices", (route) => route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({
      devices: [{
        url: "device://audio/focus-output",
        label: "Focus test output",
        kind: "audio-out",
      }],
    }),
  }));
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        sessions: [{
          slug: "focus-return",
          profile: "broadcast",
          sourceLabel: "file://[local]",
          langs: ["en"],
          effectiveDubbedLangs: ["en"],
          status: "running",
        }],
      }),
    });
  });

  await page.goto(authenticatedDashboard());
  const row = page.getByRole("row").filter({ hasText: "focus-return" });
  const trigger = row.getByRole("button", { name: "Push" });
  await trigger.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("combobox", { name: "Target destination" })).toBeVisible();
  await expect(dialog.getByRole("textbox", { name: "Custom target URL" })).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(trigger).toBeFocused();

  const undersizedTargets = await row.locator("a, button").evaluateAll((elements) =>
    elements.flatMap((element) => {
      const { width, height } = element.getBoundingClientRect();
      if (width >= 24 && height >= 24) return [];
      return [{
        name: element.getAttribute("aria-label") ?? element.textContent?.trim() ?? element.tagName,
        width: Math.round(width),
        height: Math.round(height),
      }];
    })
  );
  expect(undersizedTargets).toEqual([]);
});

test("the wizard reflows without page-level horizontal scrolling at 400% equivalent", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 720 });
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "rtmp://127.0.0.1/in/reflow");
  await page.getByRole("navigation", { name: "Language" })
    .getByRole("button", { name: "it" })
    .click();
  await page.evaluate(() => {
    for (const element of document.querySelectorAll<HTMLElement>("*")) {
      element.style.setProperty("letter-spacing", "0.12em", "important");
      element.style.setProperty("line-height", "1.5", "important");
      element.style.setProperty("word-spacing", "0.16em", "important");
    }
    for (const paragraph of document.querySelectorAll<HTMLElement>("p")) {
      paragraph.style.setProperty("margin-block-end", "2em", "important");
    }
  });

  const dimensions = await page.evaluate(() => ({
    client: document.documentElement.clientWidth,
    scroll: document.documentElement.scrollWidth,
  }));
  expect(dimensions.scroll, `page width ${dimensions.scroll}px exceeds ${dimensions.client}px`)
    .toBeLessThanOrEqual(dimensions.client);
});

test("control token fragment is validated, adopted and removed", async ({ page }) => {
  const value = controlToken();
  const doctorRequest = page.waitForRequest("**/api/v1/doctor");
  const configRequest = page.waitForRequest("**/api/v1/config");
  const devicesRequest = page.waitForRequest("**/api/v1/devices");
  await page.goto(`/ui/?source=test#token=${value.toUpperCase()}`);

  await expect(page).toHaveURL(/\/ui\/\?source=test$/);
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem("prukka-token"))).toBe(value);
  expect((await doctorRequest).headers().authorization).toBe(`Bearer ${value}`);
  expect((await configRequest).headers().authorization).toBe(`Bearer ${value}`);
  expect((await devicesRequest).headers().authorization).toBe(`Bearer ${value}`);
});

test("doctor waits for a control token without reporting a daemon failure", async ({ page }) => {
  let requests = 0;
  let configRequests = 0;
  await page.route("**/api/v1/doctor", async (route) => {
    requests += 1;
    await route.fulfill({ contentType: "application/json", body: '{"checks":[]}' });
  });
  await page.route("**/api/v1/config", async (route) => {
    configRequests += 1;
    await route.fulfill({ contentType: "application/json", body: '{"config":{}}' });
  });

  await page.goto("/ui/");

  const environment = page.locator("section", {
    has: page.getByRole("heading", { name: "Environment" }),
  });
  await expect(environment.getByText(
    "Connect Prukka with the button in the top bar to run environment checks.",
  )).toBeVisible();
  await expect(environment.getByRole("alert")).toHaveCount(0);
  expect(requests).toBe(0);
  expect(configRequests).toBe(0);
  await expect(page.getByText(
    "Connect Prukka with the button in the top bar to begin.",
  )).toBeVisible();
});

test("doctor refreshes immediately when the UI token changes and ignores stale replies", async ({
  page,
}) => {
  const oldToken = "a".repeat(64);
  const newToken = "b".repeat(64);
  let releaseOld = () => {};
  let markOldStarted = () => {};
  let markOldSettled = () => {};
  let markNewStarted = (_authorization: string | undefined) => {};
  const oldRelease = new Promise<void>((resolve) => {
    releaseOld = resolve;
  });
  const oldStarted = new Promise<void>((resolve) => {
    markOldStarted = resolve;
  });
  const oldSettled = new Promise<void>((resolve) => {
    markOldSettled = resolve;
  });
  const newStarted = new Promise<string | undefined>((resolve) => {
    markNewStarted = resolve;
  });

  // Keep the old fetch deliverable after AbortController.abort(), exercising
  // the revision guard as well as the production cancellation path.
  await page.addInitScript(() => {
    const nativeFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      if (typeof input !== "string" || !input.includes("/api/v1/doctor") || !init) {
        return nativeFetch(input, init);
      }

      const withoutSignal = { ...init };
      delete withoutSignal.signal;
      return nativeFetch(input, withoutSignal);
    };
  });

  await page.route("**/api/v1/config", (route) =>
    route.fulfill({ contentType: "application/json", body: '{"config":{}}' }),
  );

  await page.route("**/api/v1/doctor", async (route) => {
    const authorization = route.request().headers().authorization;
    if (authorization === `Bearer ${oldToken}`) {
      markOldStarted();
      await oldRelease;
      try {
        await route.fulfill({
          contentType: "application/json",
          body: JSON.stringify({
            checks: [{ name: "stale", status: "fail", detail: "stale doctor reply" }],
          }),
        });
      } finally {
        markOldSettled();
      }
      return;
    }

    markNewStarted(authorization);
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        checks: [{ name: "fresh", status: "ok", detail: "fresh doctor reply" }],
      }),
    });
  });

  await page.goto(`/ui/#token=${oldToken}`);
  await oldStarted;

  try {
    await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/doctor");
    await setToken(page, newToken);

    expect(await newStarted).toBe(`Bearer ${newToken}`);
    await expect(page.getByText("fresh doctor reply", { exact: true })).toBeVisible();

    releaseOld();
    await oldSettled;
    await expect(page.getByText("fresh doctor reply", { exact: true })).toBeVisible();
    await expect(page.getByText("stale doctor reply", { exact: true })).toHaveCount(0);
  } finally {
    releaseOld();
  }
});

test("bootstrap failures remain visible and retryable", async ({ page }) => {
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/api/v1/languages", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/api/v1/doctor", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/api/v1/engine", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );

  await page.goto(`/ui/#token=${controlToken()}`);

  await expect(page.getByText("Languages or configuration could not be loaded.")).toBeVisible();
  await expect(page.getByText("could not load the configuration", { exact: true })).toBeVisible();
  await expect(page.getByText("The daemon health check is unavailable.")).toBeVisible();
  await expect(page.getByText("could not load the language packs", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(4);
});

test("device discovery failure keeps manual setup available", async ({ page }) => {
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );

  await page.goto(authenticatedDashboard());

  // The failure is announced up front, hardware-backed cards are disabled,
  // and the URL-driven paths keep working.
  await expect(page.getByText("Device discovery is unavailable.", { exact: false })).toBeVisible();
  const cards = page.getByRole("group", { name: "What do you want to translate?" });
  await expect(cards.getByRole("button", { name: /^Microphone/ })).toBeDisabled();
  await pickSourceCard(page, /^Stream or file/);
  await expect(page.getByLabel("Source URL")).toBeVisible();
});

test("loads sessions through REST when the event stream is unavailable", async ({ page }) => {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/sessions", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        sessions: [{
          slug: "rest-fallback",
          profile: "broadcast",
          // What PublicSourceLabel makes of rtmp://127.0.0.1/live.
          sourceLabel: "rtmp://127.0.0.1",
          langs: ["it"],
          flags: { dub: "off" },
          status: "failed",
          error: "source unavailable",
        }],
      }),
    }),
  );
  await page.route("**/api/v1/stats", (route) =>
    route.fulfill({ contentType: "application/json", body: "{}" }),
  );

  await page.goto(authenticatedDashboard());

  const row = page.getByRole("row").filter({ hasText: "rest-fallback" });
  await expect(row).toBeVisible();
  await expect(row).toContainText("rtmp://127.0.0.1");
  await expect(row).not.toContainText("/live");
  await expect(row).toContainText("Failed");
  await expect(row).toContainText("source unavailable");
});

test("audio links reflect effective voice capability and lane readiness", async ({ page }) => {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/sessions", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        sessions: [
          {
            slug: "voice-ready",
            profile: "broadcast",
            sourceLabel: "file://[local]",
            langs: ["en", "it"],
            effectiveDubbedLangs: ["en"],
            status: "running",
          },
          {
            slug: "voice-starting",
            profile: "broadcast",
            sourceLabel: "file://[local]",
            langs: ["en"],
            effectiveDubbedLangs: ["en"],
            status: "starting",
          },
        ],
      }),
    }),
  );
  await page.route("**/api/v1/stats", (route) =>
    route.fulfill({ contentType: "application/json", body: "{}" }),
  );

  await page.goto(authenticatedDashboard());

  const ready = page.getByRole("row").filter({ hasText: "voice-ready" });
  await expect(ready.getByRole("link", { name: "dubbed audio (MPEG-TS) en" })).toBeVisible();
  await expect(ready.getByRole("link", { name: "dubbed audio (MPEG-TS) it" })).toHaveCount(0);

  const starting = page.getByRole("row").filter({ hasText: "voice-starting" });
  await expect(starting.getByRole("link", { name: "dubbed audio (MPEG-TS) en" })).toHaveCount(0);
});

test("session language edits expose only installed translation routes", async ({ page }) => {
  await page.route("**/api/v1/events**", (route) => route.abort());
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "local",
            local: {
              dubbedLangs: ["en"],
              mt: { pairs: [{ from: "it", to: "en" }] },
            },
          },
          defaults: { langs: ["en"], subs: "vtt" },
        },
      }),
    }),
  );
  await page.route("**/api/v1/sessions", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        sessions: [{
          slug: "route-filter",
          profile: "broadcast",
          sourceLabel: "file://[local]",
          langs: ["en"],
          flags: { source: "it" },
          effectiveDubbedLangs: ["en"],
          status: "running",
        }],
      }),
    }),
  );
  await page.route("**/api/v1/stats", (route) =>
    route.fulfill({ contentType: "application/json", body: "{}" }),
  );

  await page.goto(authenticatedDashboard());
  const row = page.getByRole("row").filter({ hasText: "route-filter" });
  await row.getByRole("combobox", { name: "add language to route-filter" }).click();
  await expect(page.getByRole("option", { name: "Italiano — it" })).toBeVisible();
  await expect(page.getByRole("option", { name: "Deutsch — de" })).toHaveCount(0);

  const defaults = page.getByRole("region", { name: "Settings" })
    .getByRole("group", { name: "Default target languages" });
  await expect(defaults.getByRole("button", { name: "Italiano — it" })).toBeVisible();
  await expect(defaults.getByRole("button", { name: "Deutsch — de" })).toHaveCount(0);
});

test("wizard dropdowns come from the language registry", async ({ page }) => {
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/registry");

  // No hardcoded languages: the source dropdown and the target
  // chips are fed by /api/v1/languages.
  await page.getByRole("combobox", { name: "Source language" }).click();
  await expect(page.getByRole("option", { name: "Italiano — it" })).toHaveCount(1);
  await page.keyboard.press("Escape");

  // Printable-key navigation is available without a pointer.
  await page.keyboard.type("de");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("combobox", { name: "Source language" })).toContainText("Deutsch — de");

  await expect(page.getByRole("group", { name: "Dubbed languages" })
    .getByRole("button", { name: "English — en · translation unavailable" })).toBeDisabled();
});

test("choosing a dropdown option with the mouse returns focus to its trigger", async ({ page }) => {
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/focus-return");

  const source = page.getByRole("combobox", { name: "Source language" });
  await source.click();
  await page.getByRole("option", { name: "Italiano — it" }).click();

  // The listbox unmounts on pick; focus must land back on the combobox
  // trigger (WCAG 2.4.3 focus order), not fall through to <body>.
  await expect(source).toBeFocused();
});

test("wizard seeds languages and subtitles from daemon config", async ({ page }) => {
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "local",
            local: {
              dubbedLangs: ["de"],
              mt: { pairs: [{ from: "it", to: "de" }] },
            },
          },
          defaults: { langs: ["de"], subs: "off", bed: "-12dB", delaySeconds: 4 },
        },
      }),
    }),
  );

  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/configured");

  const group = page.getByRole("group", { name: "Dubbed languages" });
  await expect(group.getByRole("button", { name: "Deutsch — de" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(group.getByRole("button", { name: "English — en · translation unavailable" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  // Subtitles live on the output step as a segmented control, seeded "Off".
  await reachOutput(page);
  await expect(
    page.getByRole("radiogroup", { name: "Subtitles" }).getByRole("radio", { name: "Off" }),
  ).toHaveAttribute("aria-checked", "true");
});

test("wizard marks languages unsupported by the configured voice as captions only", async ({
  page,
}) => {
  await page.route("**/api/v1/config", oneWayConfig);
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/capability");

  const dubbed = page.getByRole("group", { name: "Dubbed languages" });
  await expect(dubbed.getByRole("button", { name: "English — en" })).toBeEnabled();
  await expect(
    dubbed.getByRole("button", { name: "Italiano — it · captions only" }),
  ).toBeDisabled();
  await expect(page.getByText("The local voices dub English — en.")).toBeVisible();
  await expect(
    dubbed.getByRole("button", { name: "Deutsch — de · translation unavailable" }),
  ).toBeDisabled();
  await expect(page.getByText("Auto-detect shows the union of languages in installed MT pairs.")).toBeVisible();

  const captions = page.getByRole("group", { name: "Additional subtitle languages" });
  await expect(captions.getByRole("button", { name: "Italiano — it" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
});

test("wizard enforces directed MT pairs for a concrete source", async ({ page }) => {
  await page.route("**/api/v1/config", oneWayConfig);
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/directed");

  const source = page.getByRole("combobox", { name: "Source language" });
  const dubbed = page.getByRole("group", { name: "Dubbed languages" });

  await source.click();
  await page.getByRole("option", { name: "Italiano — it" }).click();
  await expect(dubbed.getByRole("button", { name: "English — en" })).toBeEnabled();
  await expect(
    dubbed.getByRole("button", { name: "Deutsch — de · translation unavailable" }),
  ).toBeDisabled();

  await source.click();
  await page.getByRole("option", { name: "English — en" }).click();
  await expect(
    dubbed.getByRole("button", { name: "Italiano — it · translation unavailable" }),
  ).toBeDisabled();
  await expect(dubbed.getByRole("button", { name: "English — en" })).toBeEnabled();
});

test("call flow cannot dub when the voice stage is off", async ({ page }) => {
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "off",
            local: {
              dubbedLangs: ["en"],
              mt: { pairs: [{ from: "it", to: "en" }] },
            },
          },
          defaults: { langs: ["en"], subs: "vtt" },
        },
      }),
    }),
  );

  await page.goto(authenticatedDashboard());
  // A call needs a voice; the disabled stage must be surfaced, not worked around.
  await reachLanguages(page, "call", "device://audio/voice-off");

  await expect(page.getByText("Dubbing is disabled in the daemon configuration.")).toBeVisible();
  const wizard = page.getByRole("region", { name: "New session" });
  await expect(wizard.getByRole("group", { name: "Dubbed languages" })).toHaveCount(0);
  await expect(wizard.getByRole("group", { name: "Target languages" })).toHaveCount(0);

  // The block holds all the way to the final step: the pre-filled name is
  // valid, yet the missing voice keeps the call uncreatable.
  await reachOutput(page);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Review and start" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Create session" })).toBeDisabled();
});

test("session lifecycle: create, appears live, remove", async ({ page }) => {
  // Token hand-off exactly as `prukka up` does it: URL fragment.
  await page.goto(`/ui/#token=${controlToken()}`);

  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/e2e-demo");
  await setDubbedLanguages(page, ["English — en"]);
  await createFromLanguages(page, "e2e-demo");

  // The SSE stream refreshes the table without a reload.
  const row = page.getByRole("row").filter({ hasText: "e2e-demo" });
  await expect(row).toBeVisible();
  await expect(row.getByRole("link", { name: "live subtitles (WebVTT) en" })).toBeVisible();

  await row.getByRole("button", { name: "Push" }).click();
  await expect(page.getByRole("dialog")).toContainText(
    "This action sends media to the destination you choose",
  );
  await page.getByRole("button", { name: "Cancel" }).click();

  await row.getByRole("button", { name: "remove session e2e-demo" }).click();
  await expect(row).toHaveCount(0);
});

test("ordered SSE events cannot be regressed by an older REST reply", async ({ page }) => {
  let releaseList = () => {};
  let markListStarted = () => {};
  const listStarted = new Promise<void>((resolve) => {
    markListStarted = resolve;
  });
  const held = new Promise<void>((resolve) => {
    releaseList = resolve;
  });
  let holdFirstList = true;

  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() !== "GET" || !holdFirstList) {
      await route.continue();
      return;
    }

    holdFirstList = false;
    markListStarted();
    await held;
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ sessions: [] }),
    });
  });

  const eventsStarted = page.waitForRequest("**/api/v1/events**");
  const token = controlToken();
  await page.goto(`/ui/#token=${token}`);
  await Promise.all([eventsStarted, listStarted]);

  try {
    const created = await page.evaluate(async (controlToken) => {
      const reply = await fetch("/api/v1/sessions", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${controlToken}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          slug: "ordered-sse",
          profile: "broadcast",
          sourceUrl: "file:///definitely/missing/private.wav",
          langs: ["it"],
        }),
      });

      return reply.json();
    }, token) as { session: { sourceLabel?: string; sourceUrl?: string } };

    expect(created.session.sourceUrl ?? "").toBe("");
    expect(created.session.sourceLabel).toBe("file://[local]");

    const row = page.getByRole("row").filter({ hasText: "ordered-sse" });
    await expect(row).toBeVisible();
    await expect(page.locator("header dl dd").first()).toHaveText("1");

    releaseList();
    await expect(row).toBeVisible();
    await expect(page.locator("header dl dd").first()).toHaveText("1");
    await expect(page.getByRole("status", { name: "Daemon status: Attention needed" })).toBeVisible();
  } finally {
    releaseList();
    await page.evaluate(async ({ controlToken }) => {
      await fetch("/api/v1/sessions/ordered-sse", {
        method: "DELETE",
        headers: { Authorization: `Bearer ${controlToken}` },
      });
    }, { controlToken: token });
  }
});

test("writes without a token are rejected with an honest error", async ({
  page,
}) => {
  await page.goto(authenticatedDashboard());

  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/x");
  await setDubbedLanguages(page, ["English — en"]);
  await reachOutput(page);
  await reachReview(page, "no-token");
  await disconnectToken(page);
  await page.getByRole("button", { name: "Create session" }).click();

  const notifications = page.getByRole("region", { name: "Error notifications" });
  await expect(notifications.getByRole("alert").getByText(
    "Control token missing or invalid",
    { exact: false },
  )).toBeVisible();
});

test("locale switch translates the UI and persists", async ({ page }) => {
  await page.goto(authenticatedDashboard());

  await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();

  await page.getByRole("navigation", { name: "Language" }).getByRole("button", { name: "it" }).click();
  await expect(page.getByRole("heading", { name: "Nuova sessione" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "it");

  // The choice survives a reload.
  await page.reload();
  await expect(page.getByRole("heading", { name: "Nuova sessione" })).toBeVisible();

  await page.getByRole("navigation", { name: "Lingua" }).getByRole("button", { name: "en" }).click();
  await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();
});

test("effective session defaults persist through the daemon", async ({ page }) => {
  await page.goto(`/ui/#token=${controlToken()}`);

  const settings = page.locator("section", {
    has: page.getByRole("heading", { name: "Settings" }),
  });

  await expect(settings.getByText("Default target languages")).toBeVisible();
  const delay = settings.getByLabel("Delay (seconds)", { exact: true });
  await expect(delay).toBeVisible();
  const originalDelay = await delay.inputValue();
  const updatedDelay = originalDelay === "7" ? "8" : "7";

  // Edit a default that is applied to newly created sessions.
  await delay.fill(updatedDelay);
  await settings.getByRole("button", { name: "Save settings" }).click();
  await expect(settings.getByText("Settings saved and applied.")).toBeVisible();

  // The edit survives a full reload — it came back from the daemon's file,
  // not from browser state.
  await page.reload();
  await expect(
    settings.getByLabel("Delay (seconds)", { exact: true }),
  ).toHaveValue(updatedDelay);

  // Restore the original value for later tests.
  await settings
    .getByLabel("Delay (seconds)", { exact: true })
    .fill(originalDelay);
  await settings.getByRole("button", { name: "Save settings" }).click();
  await expect(settings.getByText("Settings saved and applied.")).toBeVisible();
});

test("settings wait for an authenticated configuration read", async ({ page }) => {
  let requests = 0;
  await page.route("**/api/v1/config", async (route) => {
    requests += 1;
    await route.fulfill({ contentType: "application/json", body: '{"config":{"defaults":{}}}' });
  });
  await page.goto("/ui/");

  const settings = page.locator("section", {
    has: page.getByRole("heading", { name: "Settings" }),
  });
  await expect(settings.getByText(
    "Enter a valid control token to view and edit settings.",
  )).toBeVisible();
  expect(requests).toBe(0);

  const configRequest = page.waitForRequest("**/api/v1/config");
  const value = controlToken();
  await setToken(page, value);
  expect((await configRequest).headers().authorization).toBe(`Bearer ${value}`);
  await expect(settings.getByText("Session defaults")).toBeVisible();
});

test("settings ignore a save response after the control token changes", async ({ page }) => {
  let releaseSave = () => {};
  let markSaveStarted = () => {};
  const saveReleased = new Promise<void>((resolve) => {
    releaseSave = resolve;
  });
  const saveStarted = new Promise<void>((resolve) => {
    markSaveStarted = resolve;
  });
  await page.route("**/api/v1/config", async (route) => {
    if (route.request().method() !== "PUT") {
      await route.continue();
      return;
    }
    markSaveStarted();
    await saveReleased;
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: { defaults: { langs: ["en"], delaySeconds: 37 } },
      }),
    }).catch(() => {
      // Token invalidation aborts the request before this delayed response can
      // update dashboard state.
    });
  });

  await page.goto(authenticatedDashboard());
  const settings = page.locator("section", {
    has: page.getByRole("heading", { name: "Settings" }),
  });
  await expect(settings.getByText("Session defaults")).toBeVisible();
  await settings.getByLabel("Delay (seconds)", { exact: true }).fill("37");
  await settings.getByRole("button", { name: "Save settings" }).click();
  await saveStarted;

  await disconnectToken(page);
  await expect(settings.getByText(
    "Enter a valid control token to view and edit settings.",
  )).toBeVisible();
  releaseSave();
  await expect(
    page
      .getByRole("group", { name: "What do you want to translate?" })
      .getByRole("button", { name: /^Stream or file/ }),
  ).toBeDisabled();
  await expect(settings.getByText("Settings saved and applied.")).toHaveCount(0);
});

test("wizard creates a call-profile session on a device source", async ({ page }) => {
  // Keep this creation/lifecycle case independent of host audio hardware and
  // of the host's installed voices (one-way keeps a single session);
  // output-routing readiness has its own deterministic test below.
  await page.route("**/api/v1/config", oneWayConfig);
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({ contentType: "application/json", body: '{"devices":[]}' }),
  );
  await page.goto(`/ui/#token=${controlToken()}`);

  await reachLanguages(page, "call", "device://audio/9");
  await createFromLanguages(page, "e2e-call");

  const row = page.getByRole("row").filter({ hasText: "e2e-call" });
  await expect(row).toBeVisible();
  await expect(row).toContainText("call");

  await row.getByRole("button", { name: "remove session e2e-call" }).click();
  await expect(row).toHaveCount(0);
});

test("call routing waits for the dubbed mixer", async ({ page }) => {
  let pushes = 0;

  await page.route("**/api/v1/config", oneWayConfig);
  await page.route("**/api/v1/engine", engineIdle);
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        devices: [
          {
            url: "device://audio/2",
            label: "Built-in Output",
            kind: "audio-out",
            virtual: false,
          },
        ],
      }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() === "POST") {
      await fulfillCreatedSession(route);
      return;
    }
    await route.continue();
  });
  await page.route("**/api/v1/sessions/ready-race/push", async (route) => {
    pushes += 1;
    if (pushes < 5) {
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ message: "media output is starting" }),
      });
      return;
    }
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(`/ui/#token=${controlToken()}`);
  await reachLanguages(page, "call", "device://audio/9");
  await reachOutput(page);
  await page.getByRole("combobox", { name: "I listen on" }).click();
  await page.getByRole("option", { name: "Built-in Output" }).click();
  await reachReview(page, "ready-race");
  await page.getByRole("button", { name: "Create session" }).click();

  // Exponential retries span more than one second, covering real device
  // startup rather than only a fast in-process race.
  await expect.poll(() => pushes, { timeout: 5_000 }).toBe(5);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("wizard rolls back instead of routing when the daemon rejects voice capability", async ({
  page,
}) => {
  let deleted = false;
  let pushes = 0;

  await page.route("**/api/v1/config", oneWayConfig);
  await page.route("**/api/v1/engine", engineIdle);
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        devices: [{
          url: "device://audio/2",
          label: "Built-in Output",
          kind: "audio-out",
        }],
      }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() === "POST") {
      const session = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ session: { ...session, effectiveDubbedLangs: [] } }),
      });
      return;
    }
    await route.continue();
  });
  await page.route("**/api/v1/sessions/rejected-voice", async (route) => {
    deleted = route.request().method() === "DELETE";
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });
  await page.route("**/api/v1/sessions/rejected-voice/push", async (route) => {
    pushes += 1;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(`/ui/#token=${controlToken()}`);
  await reachLanguages(page, "call", "device://audio/9");
  await reachOutput(page);
  await page.getByRole("combobox", { name: "I listen on" }).click();
  await page.getByRole("option", { name: "Built-in Output" }).click();
  await reachReview(page, "rejected-voice");
  await page.getByRole("button", { name: "Create session" }).click();

  await expect(page.getByRole("alert")).toContainText("session was rolled back");
  await expect.poll(() => deleted).toBe(true);
  expect(pushes).toBe(0);
});

test("broadcast wizard offers discovered video outputs", async ({ page }) => {
  let target = "";

  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        devices: [
          {
            url: "device://video/prukka",
            label: "Prukka Camera",
            kind: "video-out",
            virtual: true,
          },
        ],
      }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() === "POST") {
      await fulfillCreatedSession(route);
      return;
    }
    await route.continue();
  });
  await page.route("**/api/v1/sessions/video-route/push", async (route) => {
    target = (route.request().postDataJSON() as { targetUrl: string }).targetUrl;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(`/ui/#token=${controlToken()}`);
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/video-route");
  await setDubbedLanguages(page, ["English — en"]);
  await reachOutput(page);
  await page.getByRole("combobox", { name: "Send video to" }).click();
  await page.getByRole("option", { name: "Prukka Camera" }).click();
  await reachReview(page, "video-route");
  await page.getByRole("button", { name: "Create session" }).click();

  await expect.poll(() => target).toBe("device://video/prukka");
});

test("caption-only broadcast routes video with its caption language", async ({ page }) => {
  let pushed: Record<string, unknown> = {};

  // "Captions only" needs a language the voices cannot dub: pin the capability.
  await page.route("**/api/v1/config", oneWayConfig);
  await page.route("**/api/v1/engine", engineIdle);
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        devices: [{
          url: "device://video/prukka",
          label: "Prukka Camera",
          kind: "video-out",
          virtual: true,
        }],
      }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() === "POST") {
      await fulfillCreatedSession(route);
      return;
    }
    await route.continue();
  });
  await page.route("**/api/v1/sessions/caption-video/push", async (route) => {
    pushed = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(`/ui/#token=${controlToken()}`);
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/caption-video");
  await page.getByRole("combobox", { name: "Source language" }).click();
  await page.getByRole("option", { name: "Italiano — it" }).click();
  await setDubbedLanguages(page, []);
  await reachOutput(page);
  await page.getByRole("combobox", { name: "Send video to" }).click();
  await page.getByRole("option", { name: "Prukka Camera" }).click();
  await reachReview(page, "caption-video");
  await page.getByRole("button", { name: "Create session" }).click();

  await expect.poll(() => pushed).toEqual(expect.objectContaining({
    lang: "it",
    targetUrl: "device://video/prukka",
  }));
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("wizard waits for device discovery before choosing a call path", async ({ page }) => {
  let releaseDevices: () => void = () => {};
  const devicesReady = new Promise<void>((resolve) => {
    releaseDevices = resolve;
  });

  await page.route("**/api/v1/devices", async (route) => {
    await devicesReady;
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ devices: callDevices }),
    });
  });

  await page.goto(authenticatedDashboard());
  const call = page
    .getByRole("group", { name: "What do you want to translate?" })
    .getByRole("button", { name: /^A call I'm on/ });
  await expect(call).toBeDisabled();
  await expect(
    page.getByRole("status").filter({ hasText: "Loading languages and local devices" }),
  ).toBeVisible();

  releaseDevices();
  await expect(call).toBeEnabled();
  await call.click();
  await expect(page.getByText("In the native call app", { exact: false })).toBeVisible();
  const sourcePicker = page.getByRole("combobox", { name: "Call audio source" });
  await expect(sourcePicker).toContainText("Prukka Speaker");
  await sourcePicker.click();
  await page.getByRole("option", { name: "Custom URL…" }).click();
  await expect(page.getByRole("textbox", { name: "Call audio source URL" })).toBeVisible();
});

test("caption-only languages do not advertise dubbed audio", async ({ page }) => {
  await page.goto(`/ui/#token=${controlToken()}`);
  await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/captions-only");
  await page.getByRole("combobox", { name: "Source language" }).click();
  await page.getByRole("option", { name: "Italiano — it" }).click();
  await createFromLanguages(page, "captions-only");

  const row = page.getByRole("row").filter({ hasText: "captions-only" });
  await expect(row).toBeVisible();
  await expect(row.getByRole("link", { name: "dubbed audio (MPEG-TS) it" })).toHaveCount(0);

  await row.getByRole("button", { name: "remove session captions-only" }).click();
  await expect(row).toHaveCount(0);
});

test("device picker mirrors /api/v1/devices", async ({ page, request }) => {
  // The endpoint is part of the dashboard contract on every OS; whether
  // it lists anything depends on the host's hardware.
  const reply = await request.get("/api/v1/devices", {
    headers: { Authorization: `Bearer ${controlToken()}` },
  });
  expect(reply.ok()).toBeTruthy();

  const { devices = [] } = (await reply.json()) as {
    devices?: { kind: string }[];
  };
  const captures = devices.filter((d) => d.kind === "audio-in");

  await page.goto(authenticatedDashboard());
  const cards = page.getByRole("group", { name: "What do you want to translate?" });

  if (captures.length > 0) {
    // Real capture devices enable the microphone card and feed its picker.
    await cards.getByRole("button", { name: /^Microphone/ }).click();
    await expect(page.getByRole("combobox", { name: "Microphone" })).toBeVisible();
  } else {
    // No devices: the hardware-backed card is disabled, never a dead end.
    await expect(cards.getByRole("button", { name: /^Microphone/ })).toBeDisabled();
  }

  // The URL path is always reachable regardless of hardware.
  await cards.getByRole("button", { name: /^Stream or file/ }).click();
  await expect(page.getByLabel("Source URL")).toBeVisible();
});

// Config for a single-voice machine: dubs English only, translates it→en.
// Pins capability-dependent expectations regardless of the models installed
// on the host the hermetic daemon runs on.
function oneWayConfig(route: Route) {
  return route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({
      config: {
        providers: {
          voices: "local",
          local: {
            dubbedLangs: ["en"],
            mt: { pairs: [{ from: "it", to: "en" }] },
          },
        },
        defaults: { langs: ["it", "en"], subs: "vtt" },
      },
    }),
  });
}

// Config for a machine whose voices and MT pairs cover a full IT↔EN call.
function twoWayConfig(route: Route) {
  return route.fulfill({
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
        defaults: { langs: ["it", "en"], subs: "vtt" },
      },
    }),
  });
}

// A benign engine snapshot for tests that assert on global alert counts:
// it keeps them independent of pack-catalog reachability on the host.
function engineIdle(route: Route) {
  return route.fulfill({
    contentType: "application/json",
    body: JSON.stringify({
      engine: {
        installed: true,
        protocol: 2,
        packs: [
          { id: "stt-core", kind: "stt", installed: true, sizeBytes: "189000000" },
          { id: "voice-en", kind: "voice", lang: "en", installed: true, sizeBytes: "52428800" },
        ],
      },
    }),
  });
}

async function pickCallLanguages(page: Page, you: string, them: string) {
  await page.getByRole("combobox", { name: "I speak" }).click();
  await page.getByRole("option", { name: you }).click();
  await page.getByRole("combobox", { name: "They speak" }).click();
  await page.getByRole("option", { name: them }).click();
}

test("a two-way call creates both lanes and routes each to its device", async ({ page }) => {
  const created: Array<Record<string, unknown>> = [];
  const pushed: Record<string, Record<string, unknown>> = {};

  await page.route("**/api/v1/config", twoWayConfig);
  await page.route("**/api/v1/engine", engineIdle);
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ devices: callDevices }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    const session = route.request().postDataJSON() as { flags?: Record<string, string> };
    created.push(session);
    const effectiveDubbedLangs = (session.flags?.dub_langs ?? "").split(",").filter(Boolean);
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ session: { ...session, effectiveDubbedLangs, status: "starting" } }),
    });
  });
  await page.route("**/api/v1/sessions/two-way-in/push", async (route) => {
    pushed.in = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });
  await page.route("**/api/v1/sessions/two-way-out/push", async (route) => {
    pushed.out = route.request().postDataJSON() as Record<string, unknown>;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(authenticatedDashboard());
  await pickSourceCard(page, /^A call I'm on/);
  // The remote side is auto-detected from the canonical virtual speaker.
  await expect(page.getByRole("combobox", { name: "Call audio source" })).toContainText(
    "Prukka Speaker",
  );
  await page.getByRole("button", { name: "Continue" }).click();

  await pickCallLanguages(page, "English — en", "Italiano — it");
  await expect(page.getByRole("switch", { name: "Also translate my voice" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await expect(page.getByText("Two-way call ready", { exact: false })).toBeVisible();
  await reachOutput(page);
  // The remaining device roles are auto-detected from the canonical labels.
  await expect(page.getByRole("combobox", { name: "I listen on" })).toContainText("Built-in Output");
  await expect(page.getByRole("combobox", { name: "My microphone" })).toContainText(
    "Built-in Microphone",
  );
  await expect(page.getByRole("combobox", { name: "Send my voice to" })).toContainText(
    "Prukka Microphone",
  );
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Review and start" })).toBeVisible();
  await page.getByLabel("Name").fill("two-way");
  // The two lane slugs gain "-in"/"-out": the name leaves room for them.
  await expect(page.getByLabel("Name")).toHaveAttribute("maxlength", "59");
  await page.getByRole("button", { name: "Create session" }).click();

  await expect.poll(() => created.length).toBe(2);
  expect(created[0]).toEqual(expect.objectContaining({
    slug: "two-way-in",
    profile: "call",
    sourceUrl: "device://audio/speaker-in",
    langs: ["en"],
    flags: { subs: "vtt", source: "it", dub_langs: "en", pair: "two-way-out" },
  }));
  expect(created[1]).toEqual(expect.objectContaining({
    slug: "two-way-out",
    profile: "call",
    sourceUrl: "device://audio/local-mic",
    langs: ["it"],
    flags: { subs: "vtt", source: "en", dub_langs: "it", pair: "two-way-in" },
  }));
  await expect.poll(() => pushed.in).toEqual(
    expect.objectContaining({ lang: "en", targetUrl: "device://audio/listen" }),
  );
  await expect.poll(() => pushed.out).toEqual(
    expect.objectContaining({ lang: "it", targetUrl: "device://audio/call-mic" }),
  );
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("a two-way call with an unresolved device is blocked before any session exists", async ({
  page,
}) => {
  let posts = 0;

  await page.route("**/api/v1/config", twoWayConfig);
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        devices: callDevices.filter((device) => device.label !== "Prukka Microphone"),
      }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    posts += 1;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(authenticatedDashboard());
  await pickSourceCard(page, /^A call I'm on/);
  await page.getByRole("button", { name: "Continue" }).click();
  await pickCallLanguages(page, "English — en", "Italiano — it");
  await expect(page.getByText("Two-way call ready", { exact: false })).toBeVisible();
  await reachOutput(page);
  await reachReview(page, "two-way-blocked");
  await page.getByRole("button", { name: "Create session" }).click();

  const notifications = page.getByRole("region", { name: "Error notifications" });
  await expect(notifications.getByRole("alert")).toContainText(
    "a two-way call needs all four devices",
  );
  expect(posts).toBe(0);
  // The form kept its state so the operator can fix the device and retry.
  await page.getByRole("button", { name: "Back" }).click();
  await page.getByRole("button", { name: "Back" }).click();
  await expect(page.getByRole("combobox", { name: "I speak" })).toContainText("English — en");
});

test("Enter in a source field advances the wizard instead of creating a session", async ({
  page,
}) => {
  let posts = 0;

  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ devices: callDevices }),
    }),
  );
  await page.route("**/api/v1/sessions", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    posts += 1;
    await route.fulfill({ contentType: "application/json", body: "{}" });
  });

  await page.goto(authenticatedDashboard());
  await pickSourceCard(page, /^Stream or file/);
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/enter-key");
  await page.getByLabel("Source URL").press("Enter");

  await expect(page.getByRole("heading", { name: "Choose languages" })).toBeVisible();
  expect(posts).toBe(0);
});

test("device refresh keeps a manually chosen call microphone", async ({ page }) => {
  let deviceFetches = 0;
  await page.route("**/api/v1/devices", (route) => {
    deviceFetches += 1;
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ devices: callDevices }),
    });
  });

  await page.goto(authenticatedDashboard());
  await pickSourceCard(page, /^A call I'm on/);
  await page.getByRole("button", { name: "Continue" }).click();
  await reachOutput(page);
  const mic = page.getByRole("combobox", { name: "My microphone" });
  await expect(mic).toContainText("Built-in Microphone");
  await mic.click();
  await page.getByRole("option", { name: "Prukka Speaker" }).click();

  // Contract, not mechanism: at least one device refresh must land AFTER the
  // manual selection and the choice must survive it. The generous timeout
  // covers the periodic refresh interval plus fetch latency under workers: 1.
  const before = deviceFetches;
  await expect.poll(() => deviceFetches, { timeout: 15_000 }).toBeGreaterThan(before);
  await expect(mic).toContainText("Prukka Speaker");
});

// The pack manager is driven entirely through page.route stubs: the dashboard's
// 2-second fallback poll makes progress deterministic without stubbing
// EventSource, and no test depends on the host reaching the pack catalog.
test.describe("language pack manager", () => {
  const allPacks = [
    { id: "stt-core", kind: "stt", sizeBytes: "189000000", license: "MIT" },
    { id: "voice-en", kind: "voice", lang: "en", sizeBytes: "52428800" },
    { id: "voice-it", kind: "voice", lang: "it", sizeBytes: "52428800" },
    { id: "voice-de", kind: "voice", lang: "de", sizeBytes: "104857600" },
    { id: "mt-it-en", kind: "mt", from: "it", to: "en", sizeBytes: "31457280" },
    { id: "mt-en-it", kind: "mt", from: "en", to: "it", sizeBytes: "31457280" },
    { id: "mt-de-en", kind: "mt", from: "de", to: "en", sizeBytes: "31457280" },
    { id: "mt-en-de", kind: "mt", from: "en", to: "de", sizeBytes: "31457280" },
  ];

  function packs(installed: readonly string[], catalog?: readonly string[]) {
    const listed = catalog === undefined
      ? allPacks
      : allPacks.filter((pack) => catalog.includes(pack.id));
    return listed.map((pack) => ({ ...pack, installed: installed.includes(pack.id) }));
  }

  function engineReply(engine: Record<string, unknown>) {
    return {
      contentType: "application/json",
      body: JSON.stringify({ engine }),
    };
  }

  function languagesSection(page: Page) {
    return page.getByRole("region", { name: "Languages" });
  }

  function row(page: Page, text: string) {
    return languagesSection(page).getByRole("listitem").filter({ hasText: text });
  }

  test("renders the engine core and per-language install state", async ({ page }) => {
    await page.route("**/api/v1/engine", (route) =>
      route.fulfill(engineReply({
        installed: true,
        protocol: 2,
        packs: packs(["stt-core", "voice-en", "voice-it", "mt-it-en", "mt-en-it"]),
      })),
    );

    await page.goto(authenticatedDashboard());

    const core = row(page, "Engine core");
    await expect(core).toContainText("Ready");
    await expect(core).toContainText("180 MiB");

    // Registry labels name the rows; the raw tag is only a fallback.
    const english = row(page, "English — en");
    await expect(english).toContainText("Installed");
    await expect(english.getByRole("button", { name: "Remove" })).toBeEnabled();

    // German needs its voice and both routes to the installed languages.
    const german = row(page, "Deutsch — de");
    await expect(german).toContainText("Available");
    await expect(german).toContainText("160 MiB");
    await expect(german.getByRole("button", { name: "Install" })).toBeEnabled();
  });

  test("language management waits for a control token", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/engine", (route) => {
      reads += 1;
      return route.fulfill(engineReply({ installed: true, protocol: 2, packs: packs([]) }));
    });

    await page.goto("/ui/");

    await expect(page.getByText(
      "Enter a valid control token to manage languages.",
    )).toBeVisible();
    expect(reads).toBe(0);
  });

  test("installing a language walks voice and route packs with visible progress", async ({
    page,
  }) => {
    const posted: string[] = [];
    const installed = new Set(["stt-core", "voice-en"]);
    let operation: Record<string, string> | null = null;

    const snapshot = () => ({
      installed: true,
      protocol: 2,
      packs: packs([...installed], [
        "stt-core", "voice-en", "voice-it", "mt-it-en", "mt-en-it",
      ]),
      ...(operation === null ? {} : { operation }),
    });

    await page.route("**/api/v1/engine", async (route) => {
      // Each fallback poll completes the in-flight download: the operation
      // leaves the wire and its pack flips to installed.
      if (operation !== null) {
        installed.add(operation.packId);
        operation = null;
      }
      await route.fulfill(engineReply(snapshot()));
    });
    await page.route("**/api/v1/engine/packs", async (route) => {
      const { id } = route.request().postDataJSON() as { id: string };
      posted.push(id);
      operation = {
        kind: "install-pack",
        packId: id,
        phase: "download",
        doneBytes: "5242880",
        totalBytes: "52428800",
        error: "",
      };
      await route.fulfill(engineReply(snapshot()));
    });

    await page.goto(authenticatedDashboard());
    const italian = row(page, "Italiano — it");
    await expect(italian).toContainText("Available");
    await italian.getByRole("button", { name: "Install" }).click();

    // The affected row carries an accessible progress bar while downloading,
    // with the phase and the size shown as their own lines (shared
    // DownloadProgress component).
    const bar = italian.getByRole("progressbar");
    await expect(bar).toBeVisible();
    await expect(bar).toHaveAttribute("aria-valuenow", "10");
    await expect(italian).toContainText("Downloading");
    await expect(italian).toContainText("5 / 50 MiB");

    // One pack at a time: the voice first, then the routes to the other
    // installed language, each waiting for its predecessor's terminal state.
    await expect.poll(() => posted.join(" "), { timeout: 30_000 })
      .toBe("voice-it mt-it-en mt-en-it");
    await expect(italian).toContainText("Installed", { timeout: 15_000 });
    await expect(italian.getByRole("progressbar")).toHaveCount(0);
  });

  test("removing a language deletes its voice and only its routes", async ({ page }) => {
    const removed: string[] = [];
    const installed = new Set(["stt-core", "voice-en", "voice-it", "mt-it-en", "mt-en-it"]);

    await page.route("**/api/v1/engine", (route) =>
      route.fulfill(engineReply({
        installed: true,
        protocol: 2,
        packs: packs([...installed]),
      })),
    );
    await page.route("**/api/v1/engine/packs/*", async (route) => {
      expect(route.request().method()).toBe("DELETE");
      const id = decodeURIComponent(
        new URL(route.request().url()).pathname.split("/").pop() ?? "",
      );
      removed.push(id);
      installed.delete(id);
      await route.fulfill(engineReply({
        installed: true,
        protocol: 2,
        packs: packs([...installed]),
      }));
    });

    await page.goto(authenticatedDashboard());
    const italian = row(page, "Italiano — it");
    await expect(italian).toContainText("Installed");
    await italian.getByRole("button", { name: "Remove" }).click();

    await expect.poll(() => removed.join(" ")).toBe("voice-it mt-it-en mt-en-it");
    await expect(italian).toContainText("Available");
    // The untouched language keeps working without the removed routes.
    await expect(row(page, "English — en")).toContainText("Installed");
  });

  test("an unreachable pack catalog is announced with a retry", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/engine", (route) => {
      reads += 1;
      return route.fulfill(engineReply({
        installed: true,
        protocol: 2,
        catalogError: "catalog fetch failed: offline",
        packs: packs(["stt-core", "voice-en"], ["stt-core", "voice-en"]),
      }));
    });

    await page.goto(authenticatedDashboard());

    const alert = languagesSection(page).getByRole("alert");
    await expect(alert).toContainText(
      "The pack catalog is unreachable — check your connection.",
    );
    const before = reads;
    await alert.getByRole("button", { name: "Retry" }).click();
    await expect.poll(() => reads).toBeGreaterThan(before);
  });

  test("the last installed language cannot be removed", async ({ page }) => {
    await page.route("**/api/v1/engine", (route) =>
      route.fulfill(engineReply({
        installed: true,
        protocol: 2,
        packs: packs(["stt-core", "voice-en"]),
      })),
    );

    await page.goto(authenticatedDashboard());

    const english = row(page, "English — en");
    await expect(english.getByRole("button", { name: "Remove" })).toBeDisabled();
    await expect(english.getByText(
      "The last installed language cannot be removed.",
    )).toBeVisible();
    // Other languages stay installable alongside the protected one.
    await expect(row(page, "Deutsch — de").getByRole("button", { name: "Install" }))
      .toBeEnabled();
  });

  test("a running operation disables every other action", async ({ page }) => {
    await page.route("**/api/v1/engine", (route) =>
      route.fulfill(engineReply({
        installed: true,
        protocol: 2,
        packs: packs(["stt-core", "voice-en", "voice-it", "mt-it-en", "mt-en-it"]),
        operation: {
          kind: "install-pack",
          packId: "voice-de",
          phase: "download",
          doneBytes: "26214400",
          totalBytes: "104857600",
          error: "",
        },
      })),
    );

    await page.goto(authenticatedDashboard());

    await expect(languagesSection(page)).toHaveAttribute("aria-busy", "true");

    // Progress lands on the affected row even when another client started it.
    await expect(row(page, "Deutsch — de").getByRole("progressbar"))
      .toHaveAttribute("aria-valuenow", "25");

    await expect(row(page, "English — en").getByRole("button", { name: "Remove" }))
      .toBeDisabled();
    await expect(row(page, "Italiano — it").getByRole("button", { name: "Remove" }))
      .toBeDisabled();
  });
});

// The guided one-click add (LanguageDownload + the engineInstall controller):
// the multi-step install a first-time operator hits from a session row or the
// wizard. Driven through stubs like the pack manager above; note the engine
// endpoints need three separate routes — Playwright's `*` never crosses a
// path separator, so a single "engine*" pattern would miss the subpaths.
test.describe("guided language add", () => {
  const guidedCatalog = [
    { id: "stt-core", kind: "stt", sizeBytes: "189000000" },
    { id: "voice-en", kind: "voice", lang: "en", sizeBytes: "52428800" },
    { id: "voice-it", kind: "voice", lang: "it", sizeBytes: "52428800" },
    { id: "voice-de", kind: "voice", lang: "de", sizeBytes: "104857600" },
    { id: "mt-it-en", kind: "mt", from: "it", to: "en", sizeBytes: "31457280" },
    { id: "mt-en-it", kind: "mt", from: "en", to: "it", sizeBytes: "31457280" },
    { id: "mt-de-en", kind: "mt", from: "de", to: "en", sizeBytes: "31457280" },
    { id: "mt-en-de", kind: "mt", from: "en", to: "de", sizeBytes: "31457280" },
  ];

  interface GuidedOptions {
    /** runtime / core: the managed runtime and stt-core start installed. */
    runtime?: boolean;
    core?: boolean;
    /** failFirstPack: the first pack POST returns a terminal error operation. */
    failFirstPack?: boolean;
    /** stuck: a pack id another client keeps downloading forever. */
    stuck?: string;
  }

  // Stateful engine + config stubs: en and it are installed, de is one
  // download away. The config the daemon would serve is derived from the
  // installed pack set, so a completed ladder really changes capability.
  async function routeGuidedEngine(page: Page, options: GuidedOptions = {}) {
    const { runtime = true, core = true, failFirstPack = false, stuck = "" } = options;
    const installed = new Set([
      "voice-en", "voice-it", "mt-it-en", "mt-en-it",
      ...(core ? ["stt-core"] : []),
    ]);
    let engineInstalled = runtime;
    let operation: Record<string, string> | null = stuck === ""
      ? null
      : {
        kind: "install-pack",
        packId: stuck,
        phase: "download",
        doneBytes: "26214400",
        totalBytes: "104857600",
        error: "",
      };
    let failNext = failFirstPack;
    const posted: string[] = [];

    const reply = () => ({
      contentType: "application/json",
      body: JSON.stringify({
        engine: {
          installed: engineInstalled,
          protocol: 2,
          packs: guidedCatalog.map((pack) => ({ ...pack, installed: installed.has(pack.id) })),
          ...(operation === null ? {} : { operation }),
        },
      }),
    });

    await page.route("**/api/v1/engine", async (route) => {
      // Each fallback poll completes the in-flight download: the operation
      // leaves the wire and the affected pack flips to installed. A stuck
      // operation (another client's) never completes.
      if (operation !== null && operation.phase === "download" && stuck === "") {
        if (operation.kind === "install-runtime") engineInstalled = true;
        else installed.add(operation.packId);
        operation = null;
      }
      await route.fulfill(reply());
    });
    await page.route("**/api/v1/engine/runtime", async (route) => {
      posted.push("runtime");
      operation = {
        kind: "install-runtime",
        packId: "",
        phase: "download",
        doneBytes: "1048576",
        totalBytes: "8388608",
        error: "",
      };
      await route.fulfill(reply());
    });
    await page.route("**/api/v1/engine/packs", async (route) => {
      const { id } = route.request().postDataJSON() as { id: string };
      posted.push(id);
      operation = failNext
        ? {
          kind: "install-pack",
          packId: id,
          phase: "error",
          doneBytes: "0",
          totalBytes: "0",
          error: "no space left on device",
        }
        : {
          kind: "install-pack",
          packId: id,
          phase: "download",
          doneBytes: "5242880",
          totalBytes: id === "voice-de" ? "104857600" : "31457280",
          error: "",
        };
      failNext = false;
      await route.fulfill(reply());
    });
    await page.route("**/api/v1/config", (route) => {
      const dubbedLangs = [...installed]
        .filter((id) => id.startsWith("voice-"))
        .map((id) => id.slice("voice-".length));
      const pairs = [...installed]
        .filter((id) => id.startsWith("mt-"))
        .map((id) => {
          const [, from, to] = id.split("-");
          return { from, to };
        });
      return route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          config: {
            providers: { voices: "local", local: { dubbedLangs, mt: { pairs } } },
            defaults: { langs: ["en"], subs: "vtt" },
          },
        }),
      });
    });

    return { posted };
  }

  // One running session that speaks Italian and serves English: German is the
  // language its picker can only offer through a download. PATCHes mutate the
  // stub, so the chip assertion checks the real round trip.
  async function routeGuidedSession(page: Page) {
    const session = {
      slug: "polyglot",
      profile: "broadcast",
      sourceLabel: "file://[local]",
      langs: ["en"],
      flags: { source: "it" },
      effectiveDubbedLangs: ["en"],
      status: "running",
    };
    let patched: { addLangs?: string[]; removeLangs?: string[] } | null = null;

    await page.route("**/api/v1/events**", (route) => route.abort());
    await page.route("**/api/v1/sessions", async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ sessions: [session] }),
      });
    });
    await page.route("**/api/v1/sessions/polyglot", async (route) => {
      expect(route.request().method()).toBe("PATCH");
      patched = route.request().postDataJSON() as { addLangs?: string[]; removeLangs?: string[] };
      session.langs = [...session.langs, ...(patched.addLangs ?? [])];
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ session }),
      });
    });

    return { getPatched: () => patched };
  }

  function sessionRow(page: Page) {
    return page.getByRole("row").filter({ hasText: "polyglot" });
  }

  async function beginSessionAdd(page: Page) {
    const row = sessionRow(page);
    await row
      .getByRole("combobox", { name: "Add a language you don't have yet to polyglot" })
      .click();
    await page.getByRole("option", { name: "Deutsch — de" }).click();
    return row;
  }

  function errorToasts(page: Page) {
    return page.getByRole("region", { name: "Error notifications" }).getByRole("alert");
  }

  test("a session's missing language installs runtime, core and packs, then lands as a selected chip", async ({
    page,
  }) => {
    // Nothing is installed yet: the add must climb the whole prerequisite
    // ladder (managed runtime, stt-core) before the language's own packs.
    const { posted } = await routeGuidedEngine(page, { runtime: false, core: false });
    const { getPatched } = await routeGuidedSession(page);

    await page.goto(authenticatedDashboard());
    const row = await beginSessionAdd(page);
    await row.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }).click();

    // Prerequisites run under a plain-language "preparing" label, then the
    // pack bundle reports aggregate step and size progress.
    await expect(row.getByText("Getting Prukka ready…")).toBeVisible();
    await expect(row.getByText(/Step [123] of 3/)).toBeVisible({ timeout: 20_000 });
    await expect(row.getByText(/of 168 MB/)).toBeVisible();

    await expect.poll(() => posted.join(" "), { timeout: 45_000 })
      .toBe("runtime stt-core voice-de mt-de-en mt-en-de");
    await expect.poll(() => getPatched()).toEqual({ addLangs: ["de"], removeLangs: [] });
    // The owner's confirmation: the language is now a live chip on the row.
    await expect(row.getByRole("link", { name: "live subtitles (WebVTT) de" })).toBeVisible();
    await expect(errorToasts(page)).toHaveCount(0);
  });

  test("cancelling an inline add stops the ladder and offers the button again", async ({
    page,
  }) => {
    const { posted } = await routeGuidedEngine(page);
    const { getPatched } = await routeGuidedSession(page);

    await page.goto(authenticatedDashboard());
    const row = await beginSessionAdd(page);
    await row.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }).click();
    await row.getByRole("button", { name: "Cancel" }).click();

    // The control returns to its idle offer once the engine drains; nothing
    // further was posted and the session was never patched.
    const after = posted.length;
    await expect(row.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }))
      .toBeVisible({ timeout: 15_000 });
    expect(posted.length).toBe(after);
    expect(getPatched()).toBeNull();
  });

  test("a daemon failure surfaces the reason and try-again completes the add", async ({
    page,
  }) => {
    const { posted } = await routeGuidedEngine(page, { failFirstPack: true });
    const { getPatched } = await routeGuidedSession(page);

    await page.goto(authenticatedDashboard());
    const row = await beginSessionAdd(page);
    await row.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }).click();

    // Inline: the plain-language recovery. Toast: the daemon's actual reason,
    // worded like every other failure surface.
    await expect(row.getByText("We couldn't add Deutsch — de.")).toBeVisible();
    await expect(errorToasts(page)).toContainText("no space left on device");

    await row.getByRole("button", { name: "Try again" }).click();
    await expect.poll(() => posted.join(" "), { timeout: 30_000 })
      .toBe("voice-de voice-de mt-de-en mt-en-de");
    await expect.poll(() => getPatched()).toEqual({ addLangs: ["de"], removeLangs: [] });
    await expect(row.getByRole("link", { name: "live subtitles (WebVTT) de" })).toBeVisible();
  });

  test("an operation started elsewhere parks the add button until the engine is idle", async ({
    page,
  }) => {
    // Another client is mid-download: the wire shows a live operation this
    // dashboard's controller does not own.
    const { posted } = await routeGuidedEngine(page, { stuck: "voice-it" });
    await routeGuidedSession(page);

    await page.goto(authenticatedDashboard());
    const row = await beginSessionAdd(page);

    await expect(
      row.getByRole("button", { name: "Please wait — finishing the current download." }),
    ).toBeDisabled();
    expect(posted).toEqual([]);
  });

  test("the broadcast picker selects the just-downloaded language", async ({ page }) => {
    await routeGuidedEngine(page);

    await page.goto(authenticatedDashboard());
    await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/bcast-add");

    const wizard = page.getByRole("region", { name: "New session" });
    await wizard.getByRole("combobox", { name: "Add a language you don't have yet" }).click();
    await page.getByRole("option", { name: "Deutsch — de" }).click();
    await wizard.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }).click();

    // The regression this gates: onAdded must observe the refreshed config,
    // or the toggle's capability guard drops the language it just fetched.
    const dubbed = wizard.getByRole("group", { name: "Dubbed languages" });
    await expect(dubbed.getByRole("button", { name: "Deutsch — de" }))
      .toHaveAttribute("aria-pressed", "true", { timeout: 30_000 });
    // Nothing is left to add, so the picker disappears entirely.
    await expect(
      wizard.getByRole("combobox", { name: "Add a language you don't have yet" }),
    ).toHaveCount(0);
    await expect(errorToasts(page)).toHaveCount(0);
  });

  test("cancelling a broadcast add hands the picker back", async ({ page }) => {
    await routeGuidedEngine(page);

    await page.goto(authenticatedDashboard());
    await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/bcast-cancel");

    const wizard = page.getByRole("region", { name: "New session" });
    const picker = wizard.getByRole("combobox", { name: "Add a language you don't have yet" });
    await picker.click();
    await page.getByRole("option", { name: "Deutsch — de" }).click();
    await wizard.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }).click();
    await wizard.getByRole("button", { name: "Cancel" }).click();

    // Abandoning the download returns the choice: the picker is the only way
    // to reach the other missing languages.
    await expect(picker).toBeVisible({ timeout: 15_000 });
    await expect(errorToasts(page)).toHaveCount(0);
  });

  test("a created session carries no answer into the next one", async ({ page }) => {
    await routeGuidedEngine(page);

    await page.goto(authenticatedDashboard());
    await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/bcast-reset");

    const wizard = page.getByRole("region", { name: "New session" });
    const picker = wizard.getByRole("combobox", { name: "Add a language you don't have yet" });
    const offer = wizard.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ });
    await picker.click();
    await page.getByRole("option", { name: "Deutsch — de" }).click();
    await expect(offer).toBeVisible();

    // A download opened but never started answered the finished session's
    // question; the next session starts from the picker again.
    await createFromLanguages(page, "bcast-reset");
    const row = page.getByRole("row").filter({ hasText: "bcast-reset" });
    await expect(row).toBeVisible();

    await reachLanguages(page, "broadcast", "rtmp://0.0.0.0:1935/in/bcast-reset-again");
    await expect(picker).toBeVisible();
    await expect(offer).toHaveCount(0);

    await row.getByRole("button", { name: "remove session bcast-reset" }).click();
    await expect(row).toHaveCount(0);
  });

  test("the call flow confirms an added language once its download panel disappears", async ({
    page,
  }) => {
    await routeGuidedEngine(page);
    await page.route("**/api/v1/devices", (route) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ devices: callDevices }),
      }),
    );

    await page.goto(authenticatedDashboard());
    await reachLanguages(page, "call", "device://audio/9");
    await page.getByRole("combobox", { name: "They speak" }).click();
    await page.getByRole("option", { name: "Deutsch — de" }).click();

    const wizard = page.getByRole("region", { name: "New session" });
    await expect(
      wizard.getByText("These languages need a quick download for this call:"),
    ).toBeVisible();
    await wizard.getByRole("button", { name: /Add Deutsch — de · \d+ MB/ }).click();

    // The panel unmounts the moment the packs land; the confirmation must
    // survive it in the wizard, and the call note flips to ready.
    await expect(wizard.getByText("Deutsch — de added — it's ready to use."))
      .toBeVisible({ timeout: 30_000 });
    await expect(wizard.getByText("Two-way call ready", { exact: false })).toBeVisible();
    await expect(errorToasts(page)).toHaveCount(0);
  });
});

// The first-run hero: a machine without the speech engine leads with the
// one-click core install.
test.describe("first run", () => {
  const freshCatalog = [
    { id: "stt-core", kind: "stt", sizeBytes: "189000000" },
    { id: "voice-en", kind: "voice", lang: "en", sizeBytes: "52428800" },
    { id: "mt-it-en", kind: "mt", from: "it", to: "en", sizeBytes: "31457280" },
    { id: "mt-en-it", kind: "mt", from: "en", to: "it", sizeBytes: "31457280" },
  ];

  // Stateful stub: each GET completes the in-flight operation, mirroring the
  // dashboard's 2 s fallback poll driving a real download to its end.
  async function routeFreshEngine(page: Page) {
    const installed = new Set<string>();
    let engineInstalled = false;
    let operation: Record<string, string> | null = null;
    const posted: string[] = [];

    const reply = () => ({
      contentType: "application/json",
      body: JSON.stringify({
        engine: {
          installed: engineInstalled,
          protocol: 2,
          packs: freshCatalog.map((pack) => ({ ...pack, installed: installed.has(pack.id) })),
          ...(operation === null ? {} : { operation }),
        },
      }),
    });

    await page.route("**/api/v1/engine", async (route) => {
      if (operation !== null) {
        if (operation.kind === "install-runtime") engineInstalled = true;
        else installed.add(operation.packId);
        operation = null;
      }
      await route.fulfill(reply());
    });
    await page.route("**/api/v1/engine/runtime", async (route) => {
      posted.push("runtime");
      operation = {
        kind: "install-runtime",
        packId: "",
        phase: "download",
        doneBytes: "1048576",
        totalBytes: "8388608",
        error: "",
      };
      await route.fulfill(reply());
    });
    await page.route("**/api/v1/engine/packs", async (route) => {
      const { id } = route.request().postDataJSON() as { id: string };
      posted.push(id);
      operation = {
        kind: "install-pack",
        packId: id,
        phase: "download",
        doneBytes: "5242880",
        totalBytes: "189000000",
        error: "",
      };
      await route.fulfill(reply());
    });

    return { posted };
  }

  function hero(page: Page) {
    return page.getByRole("region", { name: "Get Prukka ready" });
  }

  test("a fresh machine leads with the one-click engine install", async ({ page }) => {
    const { posted } = await routeFreshEngine(page);

    await page.goto(authenticatedDashboard());
    await expect(hero(page)).toBeVisible();
    await expect(hero(page)).toContainText("180 MiB");
    await hero(page).getByRole("button", { name: "Install the speech engine" }).click();

    // The ladder is the runtime first, then the STT core; the hero leaves for
    // good once both are in.
    await expect.poll(() => posted.join(" "), { timeout: 30_000 }).toBe("runtime stt-core");
    await expect(hero(page)).toHaveCount(0, { timeout: 15_000 });
  });

  test("the hero never shows once the core is installed", async ({ page }) => {
    await page.route("**/api/v1/engine", engineIdle);

    await page.goto(authenticatedDashboard());
    await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();
    await expect(hero(page)).toHaveCount(0);
  });

  // An unreachable catalog leaves the pack list empty, so the hero has no core
  // size to quote and must not quote one anyway.
  test("the hero quotes no size when the catalog offers no core pack", async ({ page }) => {
    await page.route("**/api/v1/engine", (route) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          engine: { installed: false, protocol: 2, catalogError: "catalog unreachable", packs: [] },
        }),
      }),
    );

    await page.goto(authenticatedDashboard());
    await expect(hero(page)).toBeVisible();
    await expect(hero(page)).not.toContainText("MiB");
  });

  test("without a control token the hero stays hidden", async ({ page }) => {
    await page.goto("/ui/");
    await expect(page.getByRole("heading", { name: "New session" })).toBeVisible();
    await expect(hero(page)).toHaveCount(0);
  });
});
