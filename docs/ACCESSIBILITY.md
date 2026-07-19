# Dashboard accessibility

Review snapshot: **13 July 2026**. The engineering target is WCAG 2.2 level AA.
This is a target, not a certification, legal opinion or accessibility
declaration. Conformance requires testing the built product, its content and
the way each operator deploys it.

## Legal scope

No single rule makes every private software dashboard automatically subject to
the same obligations.

- Directive (EU) 2016/2102 and its Italian implementation cover public-sector
  websites and mobile applications. Where they apply, the currently cited
  harmonised standard is EN 301 549 V3.2.1 (2021-03), which incorporates WCAG
  2.1 requirements. A WCAG 2.2 target is useful but does not replace an
  assessment against every applicable EN 301 549 requirement.
- Italy extends parts of Law 4/2004 to private providers offering services to
  the public through websites or apps whose average turnover exceeded EUR 500
  million in the previous three years.
- The European Accessibility Act and Italian Legislative Decree 82/2022 have
  applied to the listed consumer products and services since 28 June 2025.
  These include electronic communications, access to audiovisual media,
  specified passenger transport elements, consumer banking, e-books and
  e-commerce. A Prukka deployment can be in scope when it forms part of such a
  service; the dashboard is not in scope merely because it is a web UI.
  Microenterprises providing services have the decree's specific exemption;
  product obligations and other laws must still be assessed separately.
- Procurement terms, employment duties and sector rules can require
  accessibility even where those statutory scopes do not apply.

An in-scope operator remains responsible for the required assessment,
accessible support and information, feedback mechanism, accessibility
statement or service information, and any disproportionate-burden analysis.
This repository document is not a substitute for an AgID declaration.

## Implemented baseline

The dashboard currently provides:

- semantic landmarks, a keyboard-visible skip link and a single main heading;
- persistent visible focus, reduced-motion support and text/status labels that
  do not rely on colour alone;
- labelled forms, grouped toggle states, table row/column headers and an
  announced event log;
- focus transfer between wizard steps, keyboard type-ahead in the custom
  select, and wizard/settings/push controls disabled while their mutations are
  in flight;
- persistent dismissible error notifications, inline load failures and retry
  controls;
- explicit notice before creating a session and before transmitting media to a
  configured destination;
- English and Italian interface messages; and
- no tracking script, cookie banner or remote font dependency in the embedded
  UI.

## Verification required before release

Automated checks catch regressions but cannot establish conformance. The Make
targets install and enforce the repository-pinned Node toolchain:

```bash
make web-audit
make web-e2e
```

Then manually verify at least:

1. Complete keyboard operation, logical focus order and focus visibility,
   including every custom select and dialog.
2. VoiceOver with Safari and NVDA with Firefox or Chrome, including status,
   error and live-event announcements.
3. Reflow and lossless operation at 200% and 400% zoom, narrow viewports,
   forced-colour/high-contrast modes and text spacing overrides.
4. Contrast for every state, target size, labels/instructions and error
   recovery with realistic translated content.
5. Reduced motion, timeout behaviour and operation without pointer gestures.
6. Captions and dubbed audio with representative users. Recognition or
   translation accuracy is a separate quality measure and is not demonstrated
   by WCAG checks.

Record the tested commit, browsers, assistive technologies, results and
accepted residuals in the release review. The automated workflow cannot perform
this manual matrix; approval of the protected `release` environment confirms
that its evidence has been reviewed.

## Known assurance limits

- No independent accessibility audit or assistive-technology compatibility
  matrix is recorded for the current build.
- The custom select has automated keyboard coverage but still needs manual
  screen-reader and mobile/touch validation.
- Operational events and backend diagnostics may remain in English even when
  the dashboard locale is Italian.
- Generated live captions can contain errors; the dashboard does not claim a
  regulated captioning accuracy level.

## Official sources

- [WCAG 2.2, W3C Recommendation](https://www.w3.org/TR/WCAG22/)
- [Directive (EU) 2016/2102](https://eur-lex.europa.eu/eli/dir/2016/2102/oj)
- [EU decision citing EN 301 549 V3.2.1](https://eur-lex.europa.eu/eli/dec_impl/2018/2048/2021-08-12/eng)
- [European Accessibility Act, Directive (EU) 2019/882](https://eur-lex.europa.eu/eli/dir/2019/882/oj)
- [Italian Legislative Decree 82/2022](https://www.normattiva.it/atto/caricaDettaglioAtto?atto.codiceRedazionale=22G00089&atto.dataPubblicazioneGazzetta=2022-07-01&tipoDettaglio=vigente)
- [AgID service-accessibility guidelines, version 1.0 of 4 March 2026](https://www.agid.gov.it/sites/agid/files/2026-03/Linee_Guida_accessibilit%C3%A0_dei_servizi_%28EAA%29.pdf)
- [AgID guidance for private providers under Law 4/2004](https://www.agid.gov.it/index.php/it/design-servizi/accessibilita/linee-guida-accessibilita-privati)
