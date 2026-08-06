# Brand notes

**Prukka** — Cimbrian (Sette Comuni dialect of the Asiago plateau) for
*bridge*: the bridge between languages and between streams. Pronounced
*PROOK-ka*. One binary, `prukka` — CLI and daemon alike. Only the local IPC
endpoint carries the daemon spelling: `prukkad.sock` under the state
directory, and the `prukkad-` named pipe on Windows.

Tagline: **"Every stream, every language — one bridge."**

## Logo

A square teal winged-helmet icon with white wings, thin outlines and a
transparent background. The brand SVG lives in
[`assets/brand/`](../assets/brand/) (`prukka.svg`); the tray rendering is
`internal/tray/icon.png` (22 px PNG).

## Dashboard design system

The dashboard's visual language lives in one place — the design tokens in
[`web/src/app.css`](../web/src/app.css) — and the contrast pairs below are
executable: [`web/unit/tokens.test.mjs`](../web/unit/tokens.test.mjs)
recomputes them from the CSS itself and reads the boundary tokens back out of
the components that draw them, so no palette edit and no border-token swap on
a control can break one of them without failing `npm run check`. It composites
a border's alpha over the surface, but never a control's own tinted fill under
its text: that pair belongs to the axe scan
([`web/tests/a11y.spec.ts`](../web/tests/a11y.spec.ts)).

### Color

One accent, the brand teal, in both schemes; never a gradient on text
(gradient text fails contrast checkers silently).

| Token | Role | Dark | Light |
|---|---|---|---|
| `brand` | accent text, focus ring, selected states | `#5eead4` | `#0d6a63` |
| `brand-dim` | filled controls, white text (≥ 3:1 as a fill) | `#0f7d75` | `#0f766e` |
| `surface` / `panel` / `panel-2` | page, card, inset | near-black stack | Apple-gray stack |
| `hairline` | structural rules: section edges, dividers, notices | low-contrast | low-contrast |
| `line` | control boundaries (≥ 3:1) | mid-gray | mid-gray |
| `ink` / `ink-dim` | primary / secondary text (≥ 4.5:1) | | |
| `ok` / `warn` / `danger` | status text (≥ 4.5:1) | | |

Invariants the unit gate enforces on every surface, in both schemes: text
tokens ≥ 4.5:1; `line`, `brand-dim` and the focus ring ≥ 3:1; white on
`brand-dim` ≥ 4.5:1; and every resting border a `<button>`, `<input>`,
`<select>` or `<textarea>` draws ≥ 3:1, alpha composited over the surface.

### Shape, depth, type, motion

- Radius: one scale — 16 px (`rounded-2xl`), 12 px (`rounded-xl`), 10 px,
  8 px (`rounded-lg`), 4 px (`rounded`) — plus pills (`rounded-full`).
  Sections take the largest; text fields, the select trigger and the segmented
  track take 10 px; language chips are pills.
- Depth: `shadow-card` for resting sections, `shadow-pop` for overlays;
  hairline borders carry the structure, shadows only lift it.
- Type: the system SF stack (`-apple-system` first); headings
  `font-semibold tracking-tight`; data in the mono stack.
- Motion: one entrance (`.step-enter`, 240 ms ease-out) plus 150–200 ms
  color/transform transitions; everything collapses under
  `prefers-reduced-motion`.

### Interaction rules

- Every control is a real ARIA pattern: `role="switch"` toggles, segmented
  `radiogroup`s, APG select-only comboboxes; focus is always visible
  (2 px brand ring, 2 px offset).
- Usable choices lead a chip list; blocked ones stay visible with the reason
  in the accessible name, and only a glyph in the visual row.
- Disabled-by-hardware options stay rendered (dimmed), never removed: the UI
  does not hide state.

The e2e suite adds two more design gates: an axe-core scan
(`web/tests/a11y.spec.ts`) and screenshot regression on macOS
(`web/tests/visual.spec.ts`).
