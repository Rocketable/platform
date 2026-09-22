---
title: "Web Color Themes - Plan"
type: feat
date: 2026-09-22
topic: web-themes
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Web Color Themes - Plan

## Goal Capsule

- **Objective:** A person using RocketClaw in the browser can pick one of a few color themes on Config, see it immediately, and still have it after a reload.
- **Means:** Four CSS palettes, stored in the browser, chosen on the Config screen. (KTD1)
- **Product authority:** Product Contract below is source of WHAT. Planning Contract is HOW.
- **Product Contract preservation:** n/a (ce-plan-bootstrap)
- **Open blockers:** None.
- **Stop conditions:** Palette parse, apply, and pre-paint script tests pass. `bun run lint`, `bunx tsc --noEmit`, and `bun test` pass in `internal/rocketclaw/web`.

## Product Contract

### Summary

Config offers Neutral, Harbor, Grove, and Ember. The choice is kept in the browser and painted before the app loads. Light, dark, and system stay on the existing corner toggle.

### Problem Frame

The web UI only switches light, dark, and system. That choice is already stored in the browser. There is no color theme, and Config only shows server settings.

### Requirements

- R1. Config shows a theme chooser with Neutral, Harbor, Grove, and Ember.
- R2. The chosen theme is stored in the browser and restored on the next visit, including before the app script paints.
- R3. An unknown or missing stored value uses Neutral.
- R4. Light, dark, and system continue to work and stay in their existing browser key.
- R5. The chooser is on Config even when the server config query is still loading or has failed.

### Acceptance Examples

- AE1. Choosing Harbor on Config sets the page to Harbor and a reload is still Harbor. Covers R1, R2.
- AE2. A stored value that is not one of the four themes shows Neutral. Covers R3.
- AE3. Switching the corner toggle among light, dark, and system does not clear the color theme, and choosing a color theme does not clear light, dark, or system. Covers R4.
- AE4. The chooser is visible on Config while server config is loading. Covers R5.

### Scope Boundaries

- Do not store the theme on the server, in a cookie, or in the State Store.
- Do not replace the corner light/dark/system toggle.
- Do not swap shadcn component style presets.
- Do not add a theme editor or more themes than the four named here.

### Key Decisions

- Color themes are separate from light, dark, and system. Governs R4.
- The chooser lives on Config, not in the nav. Governs R1, R5.

## Planning Contract

### Assumptions

- Browser `localStorage` is the storage the user allowed. A cookie was the other allowed option and is not used, because nothing on the server reads the choice and the existing appearance key is already `localStorage`.
- Neutral is the current grayscale look. Harbor, Grove, and Ember are blue, green, and warm palettes, each with light and dark tokens.
- No institutional learning covers this surface. Do not copy Slack or Go config persistence.

### Key Technical Decisions

- KTD1. Store the palette in `localStorage` key `palette`, and set `data-palette` on the document root. The existing key `theme` remains light, dark, or system. The pre-paint script in `internal/rocketclaw/web/index.html` applies both before React loads. Unknown values fall back to Neutral and are not written until the person chooses.
- KTD2. The chooser is a `ToggleGroup` on `ConfigPage`, outside `ConfigLoaded`, so it does not wait on `ListConfig`.
- KTD3. Palette colors are CSS variable overrides for `:root[data-palette]` and `.dark[data-palette]`. Component `className` is layout only.

### Sequencing

1. Palette parse/apply and pre-paint script.
2. Config chooser.
3. Light and dark token blocks for Harbor, Grove, and Ember.

## Implementation Units

### U1. Palette persistence and Config chooser

- **Goal:** R1–R5 are true.
- **Requirements:** R1, R2, R3, R4, R5
- **Files:** `internal/rocketclaw/web/src/components/theme.tsx`, `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/index.html`, `internal/rocketclaw/web/app/globals.css`, `internal/rocketclaw/web/src/components/ui/toggle.tsx`, `internal/rocketclaw/web/src/components/ui/toggle-group.tsx`, `internal/rocketclaw/web/src/palette.test.ts`, `internal/rocketclaw/web/README.md`
- **Approach:** Follow KTD1–KTD3. Keep the existing `ThemeToggle` behavior. Add `PaletteChooser` beside it.
- **Test scenarios:**
  - Known ids parse to themselves; anything else parses to Neutral.
  - `applyPalette` writes the id onto an element dataset, including Neutral.
  - `index.html` reads `palette`, lists the same four ids, and still reads `theme` for light, dark, and system.
- **Verification:** `bun test src/palette.test.ts` from `internal/rocketclaw/web`.

## Verification Contract

From `internal/rocketclaw/web`:

- `bun test src/palette.test.ts`
- `bun run lint`
- `bunx tsc --noEmit`
- `bun test`

Web source CLOC must stay under `TS_CLOC_BUDGET` (4500). Do not raise that budget.

## Definition of Done

- R1–R5 hold.
- U1 tests pass.
- Lint and typecheck pass.
- Abandoned experiments are not in the diff.
- README mentions the Config chooser and browser storage.
