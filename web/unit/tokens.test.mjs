// The design tokens' accessibility contract, executable: the contrast pairs
// docs/BRAND.md documents are computed here from app.css itself, so a palette
// edit that drops one below its minimum fails the unit gate instead of
// shipping. The 3:1 boundary minimum (SC 1.4.11) is measured on the border
// tokens the components actually draw, read back out of the sources, so a
// control reaching for the decorative rule fails here too. Text over a
// control's own tinted fill is not composited here; the axe scan in
// tests/a11y.spec.ts measures that in the browser.

import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import test from "node:test";

const src = new URL("../src/", import.meta.url);
const css = readFileSync(new URL("app.css", src), "utf8");

function palette(block) {
  const colors = {};
  for (const match of block.matchAll(/--color-([\w-]+):\s*(#[0-9a-fA-F]{6})/g)) {
    colors[match[1]] = match[2];
  }
  return colors;
}

const lightStart = css.indexOf("prefers-color-scheme: light");
assert.ok(lightStart > 0, "app.css declares a light palette");
const dark = palette(css.slice(0, lightStart));
const light = { ...dark, ...palette(css.slice(lightStart)) };

// A canary for the reader: `palette` matches 6-digit hex and nothing else, so
// a color written any other way would be skipped — and the light scheme
// measured on the dark value it means to override.
test("every color token is a hex the palette reader can parse", () => {
  const blocks = [["dark", css.slice(0, lightStart)], ["light", css.slice(lightStart)]];
  for (const [scheme, block] of blocks) {
    for (const [, name, value] of block.matchAll(/--color-([\w-]+):\s*([^;}\n]+)/g)) {
      assert.match(value.trim(), /^#[0-9a-fA-F]{6}$/, `${scheme} --color-${name}`);
    }
  }
});

function channels(hex) {
  const value = parseInt(hex.slice(1), 16);
  return [value >> 16, (value >> 8) & 255, value & 255];
}

function luminance(rgb) {
  const linear = (channel) => {
    const c = channel / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * linear(rgb[0]) + 0.7152 * linear(rgb[1]) + 0.0722 * linear(rgb[2]);
}

function ratio(a, b) {
  const [hi, lo] = [Math.max(luminance(a), luminance(b)), Math.min(luminance(a), luminance(b))];
  return (hi + 0.05) / (lo + 0.05);
}

function over(hex, alpha, backdrop) {
  const [fg, bg] = [channels(hex), channels(backdrop)];
  return fg.map((channel, i) => channel * alpha + bg[i] * (1 - alpha));
}

const surfaces = ["surface", "panel", "panel-2"];

function sources(dir) {
  const files = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const child = new URL(entry.name + (entry.isDirectory() ? "/" : ""), dir);
    if (entry.isDirectory()) files.push(...sources(child));
    else if (/\.(svelte|ts)$/.test(entry.name)) files.push(child);
  }
  return files;
}

// The attribute text of every <button>, <input>, <select> and <textarea>:
// the elements SC 1.4.11 calls user interface components. The tag ends at
// the first `>` outside a quote and outside an expression, so the `=>` in an
// event handler does not cut a tag short.
function openTags(markup) {
  const tags = [];
  const opener = /<(?:button|input|select|textarea)\b/g;
  for (let match = opener.exec(markup); match !== null; match = opener.exec(markup)) {
    let depth = 0;
    let quote = "";
    let end = match.index + match[0].length;
    for (; end < markup.length; end++) {
      const char = markup[end];
      if (quote !== "") {
        if (char === quote) quote = "";
      } else if (char === '"' || char === "'" || char === "`") quote = char;
      else if (char === "{") depth++;
      else if (char === "}") depth--;
      else if (char === ">" && depth === 0) break;
    }
    tags.push(markup.slice(match.index, end));
  }
  return tags;
}

// A resting border carries no `hover:`/`focus:`/`disabled:` variant, and both
// arms of a class ternary are resting states. An alpha modifier is kept and
// composited before measuring: a tinted boundary is still the boundary.
const restingBorder = /(?<![\w:/-])border-([a-z][a-z0-9-]*)(?:\/(\d+))?(?![\w-])/g;

const boundaries = [];
for (const file of sources(src)) {
  const where = file.pathname.slice(src.pathname.length);
  const text = readFileSync(file, "utf8");
  // classes.ts is nothing but the shared control skins, so all of it counts.
  const scanned = where === "lib/components/classes.ts" ? [text] : openTags(text);
  for (const chunk of scanned) {
    for (const match of chunk.matchAll(restingBorder)) {
      if (Object.hasOwn(dark, match[1])) {
        boundaries.push([where, match[1], Number(match[2] ?? 100) / 100]);
      }
    }
  }
}

// A canary for the scan: a Svelte syntax the tag reader cannot follow would
// empty `boundaries` and turn the check below into a no-op.
test("the gate can see the boundaries the components draw", () => {
  assert.ok(boundaries.length >= 20, `found ${boundaries.length} control boundaries`);
});

for (const [theme, colors] of [["dark", dark], ["light", light]]) {
  test(`${theme}: text tokens hold AA (4.5:1) on every surface`, () => {
    for (const text of ["ink", "ink-dim", "brand", "ok", "warn", "danger"]) {
      for (const surface of surfaces) {
        const r = ratio(channels(colors[text]), channels(colors[surface]));
        assert.ok(
          r >= 4.5,
          `${theme} ${text} on ${surface}: ${r.toFixed(2)}:1 (needs 4.5:1)`,
        );
      }
    }
  });

  test(`${theme}: filled controls and the declared boundary hold their minimums`, () => {
    const filled = ratio(channels("#ffffff"), channels(colors["brand-dim"]));
    assert.ok(filled >= 4.5, `${theme} white on brand-dim: ${filled.toFixed(2)}:1`);

    // `line` is the token the design system names as the control boundary and
    // `brand-dim` is the fill that stands in for one on a filled control; the
    // focus ring is drawn by app.css, not by a class.
    for (const token of ["line", "brand-dim"]) {
      for (const surface of surfaces) {
        const r = ratio(channels(colors[token]), channels(colors[surface]));
        assert.ok(r >= 3, `${theme} ${token} on ${surface}: ${r.toFixed(2)}:1 (needs 3:1)`);
      }
    }
    const focus = ratio(channels(colors.brand), channels(colors.surface));
    assert.ok(focus >= 3, `${theme} focus ring on surface: ${focus.toFixed(2)}:1`);
  });

  test(`${theme}: every boundary the controls draw holds 3:1 (WCAG 1.4.11)`, () => {
    for (const [where, token, alpha] of boundaries) {
      for (const surface of surfaces) {
        const drawn = over(colors[token], alpha, colors[surface]);
        const r = ratio(drawn, channels(colors[surface]));
        const name = alpha === 1 ? token : `${token}/${alpha * 100}`;
        assert.ok(
          r >= 3,
          `${theme} ${where}: border-${name} on ${surface}: ${r.toFixed(2)}:1 (needs 3:1)`,
        );
      }
    }
  });
}
