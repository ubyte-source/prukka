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

async function reachDevices(
  page: Page,
  profile: "broadcast" | "call",
  slug: string,
  source: string,
) {
  const choice = profile === "call" ? /^Call/ : /^Broadcast/;
  await page.getByRole("button", { name: choice }).click();
  await page.getByLabel("Name").fill(slug);
  const sourceLabel = profile === "call" ? "Call audio source" : "Source URL";
  const sourceInput = page.locator(`input[aria-label="${sourceLabel}"]`);
  if (await sourceInput.isVisible()) await sourceInput.fill(source);
}

async function reachLanguages(
  page: Page,
  profile: "broadcast" | "call",
  slug: string,
  source: string,
) {
  await reachDevices(page, profile, slug, source);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Choose languages and delivery" })).toBeVisible();
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
  await expect(page.getByRole("heading", { name: "Prukka" })).toBeVisible();

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
  await page.route("**/api/v1/events", (route) => route.abort());
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
  await reachLanguages(page, "broadcast", "reflow", "rtmp://127.0.0.1/in/reflow");
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
    "Enter the control token in the session wizard to run environment checks.",
  )).toBeVisible();
  await expect(environment.getByRole("alert")).toHaveCount(0);
  expect(requests).toBe(0);
  expect(configRequests).toBe(0);
  await expect(page.getByText(
    "Enter a valid control token to load the daemon configuration.",
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
    await reachLanguages(page, "broadcast", "doctor-token", "rtmp://0.0.0.0:1935/in/doctor");
    await page.getByLabel("Control token").fill(newToken);

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

  await page.goto(`/ui/#token=${controlToken()}`);

  await expect(page.getByText("Languages or configuration could not be loaded.")).toBeVisible();
  await expect(page.getByText("could not load the configuration", { exact: true })).toBeVisible();
  await expect(page.getByText("The daemon health check is unavailable.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(3);
});

test("device discovery failure keeps manual setup available", async ({ page }) => {
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );

  await page.goto(authenticatedDashboard());
  await page.getByRole("button", { name: /^Broadcast/ }).click();

  await expect(page.getByText("Device discovery is unavailable.", { exact: false })).toBeVisible();
  await expect(page.getByLabel("Source URL")).toBeVisible();
});

test("loads sessions through REST when the event stream is unavailable", async ({ page }) => {
  await page.route("**/api/v1/events", (route) => route.abort());
  await page.route("**/api/v1/sessions", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        sessions: [{
          slug: "rest-fallback",
          profile: "broadcast",
          sourceUrl: "rtmp://127.0.0.1/live",
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
  await page.route("**/api/v1/events", (route) => route.abort());
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
  await page.route("**/api/v1/events", (route) => route.abort());
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "local",
            local: {
              ttsLanguage: "en",
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
  await reachLanguages(page, "broadcast", "registry", "rtmp://0.0.0.0:1935/in/registry");

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

test("wizard seeds languages and subtitles from daemon config", async ({ page }) => {
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "local",
            local: {
              ttsLanguage: "de",
              mt: { pairs: [{ from: "it", to: "de" }] },
            },
          },
          defaults: { langs: ["de"], subs: "off", bed: "-12dB", delaySeconds: 4 },
        },
      }),
    }),
  );

  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "configured", "rtmp://0.0.0.0:1935/in/configured");

  const group = page.getByRole("group", { name: "Dubbed languages" });
  await expect(group.getByRole("button", { name: "Deutsch — de" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(group.getByRole("button", { name: "English — en · translation unavailable" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  const wizard = page.locator("section", { has: page.getByRole("heading", { name: "New session" }) });
  await expect(wizard.getByRole("combobox", { name: "Subtitles" })).toContainText("Off");
});

test("wizard marks languages unsupported by the configured voice as captions only", async ({
  page,
}) => {
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "capability", "rtmp://0.0.0.0:1935/in/capability");

  const dubbed = page.getByRole("group", { name: "Dubbed languages" });
  await expect(dubbed.getByRole("button", { name: "English — en" })).toBeEnabled();
  await expect(
    dubbed.getByRole("button", { name: "Italiano — it · captions only" }),
  ).toBeDisabled();
  await expect(page.getByText("The configured TTS voice supports English — en.")).toBeVisible();
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
  await page.goto(authenticatedDashboard());
  await reachLanguages(page, "broadcast", "directed", "rtmp://0.0.0.0:1935/in/directed");

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

test("wizard does not advertise dubbing when the voice stage is off", async ({ page }) => {
  await page.route("**/api/v1/config", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        config: {
          providers: {
            voices: "off",
            local: {
              ttsLanguage: "en",
              mt: { pairs: [{ from: "it", to: "en" }] },
            },
          },
          defaults: { langs: ["en"], subs: "vtt" },
        },
      }),
    }),
  );

  await page.goto(authenticatedDashboard());
  // The call defaults must not silently re-enable a stage disabled by config.
  await reachLanguages(page, "call", "voice-off", "device://audio/voice-off");

  await expect(page.getByRole("checkbox", { name: "Dubbing" })).toBeDisabled();
  await expect(page.getByRole("checkbox", { name: "Dubbing" })).not.toBeChecked();
  await expect(page.getByText("Dubbing is disabled in the daemon configuration.")).toBeVisible();
  await expect(page.getByRole("group", { name: "Dubbed languages" })).toHaveCount(0);
  const wizard = page.getByRole("region", { name: "New session" });
  await expect(
    wizard.getByRole("group", { name: "Target languages" })
      .getByRole("button", { name: "English — en" }),
  ).toHaveAttribute("aria-pressed", "true");
});

test("session lifecycle: create, appears live, remove", async ({ page }) => {
  // Token hand-off exactly as `prukka up` does it: URL fragment.
  await page.goto(`/ui/#token=${controlToken()}`);

  await reachLanguages(page, "broadcast", "e2e-demo", "rtmp://0.0.0.0:1935/in/e2e-demo");
  await setDubbedLanguages(page, ["English — en"]);
  await page.getByRole("button", { name: "Create session" }).click();

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

  const eventsStarted = page.waitForRequest("**/api/v1/events");
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

  await reachLanguages(page, "broadcast", "no-token", "rtmp://0.0.0.0:1935/in/x");
  await setDubbedLanguages(page, ["English — en"]);
  await page.getByLabel("Control token").fill("");
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

  // Legacy engine fields are not offered: the runtime does not apply them.
  await expect(settings.getByLabel("Transcription model", { exact: true })).toHaveCount(0);
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
  await page.getByLabel("Control token").fill(value);
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

  await page.getByLabel("Control token").fill("");
  await expect(settings.getByText(
    "Enter a valid control token to view and edit settings.",
  )).toBeVisible();
  releaseSave();
  await expect(page.getByRole("button", { name: /^Broadcast/ })).toBeDisabled();
  await expect(settings.getByText("Settings saved and applied.")).toHaveCount(0);
});

test("wizard creates a call-profile session on a device source", async ({ page }) => {
  // Keep this creation/lifecycle case independent of host audio hardware;
  // output-routing readiness has its own deterministic test below.
  await page.route("**/api/v1/devices", (route) =>
    route.fulfill({ contentType: "application/json", body: '{"devices":[]}' }),
  );
  await page.goto(`/ui/#token=${controlToken()}`);

  await reachLanguages(page, "call", "e2e-call", "device://audio/9");
  await setDubbedLanguages(page, ["English — en"]);
  await page.getByRole("button", { name: "Create session" }).click();

  const row = page.getByRole("row").filter({ hasText: "e2e-call" });
  await expect(row).toBeVisible();
  await expect(row).toContainText("call");

  await row.getByRole("button", { name: "remove session e2e-call" }).click();
  await expect(row).toHaveCount(0);
});

test("call routing waits for the dubbed mixer", async ({ page }) => {
  let pushes = 0;

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
  await reachDevices(page, "call", "ready-race", "device://audio/9");
  await page.getByRole("combobox", { name: "I listen on" }).click();
  await page.getByRole("option", { name: "Built-in Output" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await setDubbedLanguages(page, ["English — en"]);
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
  await reachDevices(page, "call", "rejected-voice", "device://audio/9");
  await page.getByRole("combobox", { name: "I listen on" }).click();
  await page.getByRole("option", { name: "Built-in Output" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await setDubbedLanguages(page, ["English — en"]);
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
  await page.getByRole("button", { name: /^Broadcast/ }).click();
  await page.getByLabel("Name").fill("video-route");
  await page.getByLabel("Source URL").fill("rtmp://0.0.0.0:1935/in/video-route");
  await page.getByRole("combobox", { name: "Send video to" }).click();
  await page.getByRole("option", { name: "Prukka Camera" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await setDubbedLanguages(page, ["English — en"]);
  await page.getByRole("button", { name: "Create session" }).click();

  await expect.poll(() => target).toBe("device://video/prukka");
});

test("caption-only broadcast routes video with its caption language", async ({ page }) => {
  let pushed: Record<string, unknown> = {};

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
  await reachDevices(page, "broadcast", "caption-video", "rtmp://0.0.0.0:1935/in/caption-video");
  await page.getByRole("combobox", { name: "Send video to" }).click();
  await page.getByRole("option", { name: "Prukka Camera" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("combobox", { name: "Source language" }).click();
  await page.getByRole("option", { name: "Italiano — it" }).click();
  await setDubbedLanguages(page, []);
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
  const call = page.getByRole("button", { name: /^Call/ });
  await expect(call).toBeDisabled();
  await expect(
    page.getByRole("status").filter({ hasText: "Loading languages and local devices" }),
  ).toBeVisible();

  releaseDevices();
  await expect(call).toBeEnabled();
  await call.click();
  await expect(page.getByText("Two-way translated calls", { exact: false })).toBeVisible();
  await expect(page.getByRole("checkbox", { name: "Translate both sides" })).toHaveCount(0);
  const sourcePicker = page.getByRole("combobox", { name: "Source device" });
  await expect(sourcePicker).toContainText("Prukka Speaker");
  await sourcePicker.click();
  await page.getByRole("option", { name: "Custom URL…" }).click();
  await expect(page.getByRole("textbox", { name: "Call audio source" })).toBeVisible();
});

test("caption-only languages do not advertise dubbed audio", async ({ page }) => {
  await page.goto(`/ui/#token=${controlToken()}`);
  await reachLanguages(page, "broadcast", "captions-only", "rtmp://0.0.0.0:1935/in/captions-only");
  await page.getByRole("combobox", { name: "Source language" }).click();
  await page.getByRole("option", { name: "Italiano — it" }).click();
  await page.getByRole("button", { name: "Create session" }).click();

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
  await page.getByRole("button", { name: /^Broadcast/ }).click();
  const picker = page.getByRole("combobox", { name: "Source device" });

  if (captures.length > 0) {
    // Real devices feed the dropdown, and manual entry stays reachable.
    await picker.click();
    await expect(page.getByRole("option", { name: "Custom URL…" })).toHaveCount(1);
    await page.keyboard.press("Escape");
    await expect(page.getByLabel("Source URL")).toBeVisible();
  } else {
    // No devices: the wizard degrades to the manual URL field alone.
    await expect(picker).toHaveCount(0);
    await expect(page.getByLabel("Source URL")).toBeVisible();
  }
});
