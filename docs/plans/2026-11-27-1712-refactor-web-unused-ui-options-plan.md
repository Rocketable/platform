---
title: Web Unused UI Options and Leftovers - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Web Unused UI Options and Leftovers - Plan

## Goal Capsule

- **Objective:** The RocketClaw web UI looks and behaves exactly as before, and its counted TypeScript source is smaller, so the web line budget has more room for product work.
- **Means:** Delete style options in `internal/rocketclaw/web/src/components/ui` that no screen uses, and remove a handful of unused parameters, defaults, return values and repeated work in the web source (KTD1-KTD6).
- **Authority:** Owner-approved decision carried by the caller. The repository `AGENTS.md` comes next, with react-doctor winning over its inlining rules for TypeScript.
- **Stop conditions:** Skip an item, and report it, when react-doctor (`make -C internal/rocketclaw/web lint`) needs the current shape. Stop and report when a change would alter something a person can see.
- **Execution profile:** One pull request, Lightweight.
- **Finish and ship:** The implementing agent verifies, regenerates the embedded build, and opens the PR. It does not merge.

## Product Contract

### Summary

Remove dead style options from the copied shadcn components and five small leftovers in the web source, with no visible change and fewer counted TypeScript lines.

### Problem Frame

The web source counts against a TypeScript line budget. The copied shadcn components ship variants, sizes and aligns that no screen in RocketClaw uses. A few helpers also keep parameters, defaults and return values that no caller needs, and the palette is written to the page three times on load when the page's inline script already did it before paint. Each line of this costs budget and makes a reader wonder where it is used.

### Requirements

**Unused style options**

- R1. Each variant, size or align option in `src/components/ui` that no code outside `components/ui` reaches, whether through JSX props, `buttonVariants(...)`-style calls, props spread from objects, or defaults, is deleted along with its class strings.
- R2. Options that are still reached, including defaults that callers rely on by omitting the prop, stay.

**Leftovers**

- R3. `useLayout` returns only the layout, because its one caller never uses the setter.
- R4. `listSessions` takes its URL as a required parameter, because every caller passes one.
- R5. `readSessionEnumeration` takes the batch stream directly, not a promise of it, and has no default for `onRemote`. Its production caller stops wrapping its input in `Promise.resolve`.
- R6. The palette is no longer re-applied on load by `useTheme` or by `PaletteChooser`'s mount effect, because `index.html` applies the same stored palette before React mounts. The picker still shows the stored palette and still changes it.
- R7. `ParkedRow.shared` is removed. `CardHeading`'s `icon` prop and the `cardClass` helper, which each have one caller, are inlined. `CardHeading` stays its own component.

**Constraints**

- R8. No visible or behavioral change.
- R9. Counted non-test TypeScript lines go down (`make -C internal/rocketclaw/web cloc`, source line).

### Key Decisions

- **Delete unused options instead of keeping the full shadcn copy.** (session-settled: user-approved — chosen over keeping the upstream shadcn option sets for future use: options no screen uses cost line budget and are easy to add back from upstream when a screen needs them.) Governs R1, R2.
- **Fix the leftovers in the same PR, with no visible change.** (session-settled: user-approved — chosen over separate PRs per item: each item is a few lines and they share one verification pass.) Governs R3-R8.
- **Keep `CardHeading` as its own component while inlining its single-caller options.** (session-settled: user-directed — chosen over inlining `CardHeading` into `SpaceCard`: the component split keeps `SpaceCard` readable and under react-doctor's complexity limit.) Governs R7.

### Scope Boundaries

- Primitive positioning props (`side`, `align` on Tooltip, Select and DropdownMenu content, `orientation` on Tabs) pass through to Base UI and affect behavior. They are not style options and stay.
- `SheetContent`'s `side` is only ever `right`, but it is a positioning prop, not a variant, size or align, so it stays. It is reported as a possible follow-up.
- `CodeBlock` has a single `default` variant and dead `group-data-[variant=destructive]` classes. It has no unused option to delete, so it stays as is.
- `useTheme` also re-applies the light or dark mode on mount, which `index.html` already does. The decision covers the palette only, so mode handling stays.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Unused options found by search, deleted with their selectors.** Searching every non-`components/ui` source file, plus component-to-component uses inside `components/ui`, finds these unused options:
  - `button.tsx` variant `quiet`.
  - `badge.tsx` variants `ghost` and `link` (used: `default`, `secondary`, `destructive`, `outline`).
  - `tabs.tsx` `TabsList` variant `line` (`default` is the only look used). `tabsListVariants` collapses to a plain class string and its export goes, because nothing imports it. `TabsTrigger`'s line-only selectors and invisible underline go too, and its default-only shadow becomes a plain `data-active:shadow-sm`, so `TabsList` no longer needs a `data-variant` attribute.
  - `attachment.tsx` size `xs`, plus the `group-data-[size=xs]/attachment` selectors in `attachmentMediaVariants`.
  - `input-group.tsx` aligns `inline-start` and `inline-end`. The two callers pass `block-start` and `block-end`. The `inline-start` default and the `data-align=inline-*` selectors in `InputGroup` go too. `InputGroupButton` loses its unused `xs` and `icon-xs` sizes, because every caller passes `sm` or `icon-sm`.
  - `bubble.tsx` variant `default`. The single caller always passes `secondary` or `ghost`.
  - `field.tsx` orientations `horizontal` and `responsive`. No caller passes `orientation`, so the cva collapses to the `vertical` classes. `data-orientation="vertical"` stays only if a selector reads it, and none does today.
  (session-settled: user-approved — chosen over keeping the upstream shadcn option sets: unused options cost budget.)
- KTD2. **`listSessions(url, signal?)`.** Make the URL the first required parameter instead of typing `signal` as `AbortSignal | undefined` ahead of it. That lets callers that have no signal drop the `undefined` argument. Callers in `ui.tsx` and the transport and unit tests are updated.
- KTD3. **`readSessionEnumeration(owner, batches, baseline, onProgress, signal, onRemote)` with `batches: AsyncIterable<SessionBatch>`, `signal: AbortSignal` and `onRemote` required.** A required `onRemote` cannot follow an optional `signal`. The one production caller always passes `ac.signal`, so `signal` becomes required too, and the `signal?.` optional chains become plain accesses. Tests pass a signal and a no-op `onRemote` explicitly. (session-settled: user-approved — chosen over keeping the Promise-accepting signature: its only production caller wraps its input in `Promise.resolve` just to match.)
- KTD4. **Palette: drop both mount-time `applyPalette` calls and keep the change-time one.** `index.html` reads the same `palette` storage key, checks it against the same id list (already asserted by `src/palette.test.ts`) and falls back to `neutral`, as `parsePalette` does, before the module script loads. No content security policy blocks that inline script: the served CSP sets only `frame-ancestors`. An effect that only copies storage into state fails react-doctor (`rendering-hydration-no-flicker`), so `PaletteChooser` reads the stored palette through `useSyncExternalStore`, as `ThemeModeChooser` already does for the mode. A change writes storage, applies the palette, and fires the same `themechange` event. Static rendering in tests still gets `neutral` from the server snapshot.
- KTD5. **`useLayout()` returns `StripLayout`.** `saveLayout` stays a module function used directly by its other callers.
- KTD6. **`ParkedRow` type removed.** `parkedRows` returns `Tab`-shaped rows and the shared branch pushes the shared tab unchanged. `CardHeading` picks `FolderOpen` or `Folder` itself. `SpaceCard` writes the card class inline with `cn(...)`.

### Assumptions

- The embedded build `internal/rocketclaw/internal/web/dist/index.html.gz` is committed alongside web source changes. Recent web PRs on `main` all regenerate it in the same commit.
- `SpaceCard` stays under react-doctor's complexity limit after `cardClass`'s ternary moves into it. If not, the `cardClass` inlining is skipped and reported.

---

## Implementation Units

### U1. Delete unused style options in components/ui

- **Goal:** Remove every unused option listed in KTD1.
- **Requirements:** R1, R2, R8, R9. KTD1.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/web/src/components/ui/button.tsx`, `badge.tsx`, `tabs.tsx`, `attachment.tsx`, `input-group.tsx`, `bubble.tsx`, `field.tsx` (all under `internal/rocketclaw/web/src/components/ui/`).
- **Approach:**
  1. Re-run the usage search before each deletion (`variant=`, `size=`, `align=`, `orientation=`, spread objects such as `transcript-text.tsx`'s `actionProps`, `buttonVariants({...})` in `ui.tsx` and `questionnaire.tsx`, and component-to-component uses such as `AttachmentAction`, `InputGroupButton`, `MessageScrollerButton`, `Dialog` and `Sheet` close buttons).
  2. Delete the option, its class string, and any selector that only matches it.
  3. Where the only remaining option is the default and no `data-*` selector reads the value, collapse the cva to a class string. Keep the `data-*` attributes that selectors still read.
- **Patterns to follow:** Existing cva usage in the same files.
- **Test scenarios:** Test expectation: none -- style-only deletion of unreachable options. The type check (`bunx tsc --noEmit` in lint) proves no caller passes a deleted value, and the browser tests that render tabs, attachments, the composer and messages cover the kept looks.
- **Verification:** `make lint` passes, and a final search finds no use of a deleted option.

### U2. Small leftovers in api, session-list, ui and theme

- **Goal:** Apply R3-R7.
- **Requirements:** R3, R4, R5, R6, R7, R8, R9. KTD2-KTD6.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/web/src/api.ts`, `src/session-list.ts`, `src/ui.tsx`, `src/components/theme.tsx`. Tests: `src/api.test.ts`, `src/session-list.test.ts`, `src/entry-transport.test.ts`, `src/shadows-transport.test.ts`, `src/sidebar-transport.test.ts`.
- **Approach:**
  1. `listSessions` per KTD2, updating every caller.
  2. `readSessionEnumeration` per KTD3. The `ui.tsx` caller passes `listSessions(...)` directly.
  3. `useLayout` per KTD5, and its caller's destructuring becomes a plain assignment.
  4. Theme per KTD4.
  5. `ParkedRow`, `CardHeading` and `cardClass` per KTD6.
- **Test scenarios:**
  - `session-list.test.ts`: the existing enumeration tests still pass with a direct async iterable, an explicit signal and an explicit no-op `onRemote`. The remote-metadata test still records the server metadata through `onRemote`.
  - `api.test.ts`: streaming, oversized batch and authentication-error cases still pass with `listSessions(url, signal?)`.
  - `palette.test.ts`: the picker still renders `Neutral` with no stored palette, and `index.html` still applies every palette id before paint.
  - The sidebar and tab browser tests still show parked rows and space cards with the same headings, icons and current-card tint.
- **Verification:** The touched test files pass, and `make lint` passes.

### U3. Regenerate the embedded build

- **Goal:** Commit the regenerated `internal/rocketclaw/internal/web/dist/index.html.gz` with the source changes.
- **Requirements:** R8.
- **Dependencies:** U1, U2.
- **Files:** `internal/rocketclaw/internal/web/dist/index.html.gz` (generated, never edited by hand).
- **Approach:** Run the web generator after the source changes are final.
- **Test scenarios:** Test expectation: none -- generated artifact.
- **Verification:** The generator runs cleanly, and its output is in the commit.

---

## Verification Contract

| Gate | Command | Proves |
|---|---|---|
| Lint, types, react-doctor | `make -C internal/rocketclaw/web lint` | No caller uses a deleted option, and react-doctor accepts the new shapes |
| TS line budget | `make -C internal/rocketclaw/web check-cloc-budget` and `make -C internal/rocketclaw/web cloc` before and after | R9 |
| Web tests | `make -C internal/rocketclaw/web test-files TESTS="src/session-list.test.ts src/api.test.ts src/palette.test.ts src/tabs.browser.test.ts src/session-list.browser.test.ts src/composer.browser.test.ts src/github-pr.browser.test.ts"` after `bun install` | Touched behavior is unchanged |
| Transport tests | The `*-transport.test.ts` files need a server URL that the Go browser tests in `internal/rocketclaw/frontend/rpc` provide. Lint type-checks their new `listSessions` calls. Run the matching Go tests locally if Postgres is available, otherwise CI runs them | The call-shape change reaches real streaming |
| Embedded build | `go -C internal/rocketclaw generate ./internal/web` | The dist matches the source |
| Go lint | `make -C internal/rocketclaw lint` | Only when a Go file changes, which this plan does not expect |

## Definition of Done

- Every item in R1-R7 is either done or reported as skipped with the react-doctor reason.
- The source line in `cloc` is lower than the baseline of 11913.
- All gates above pass.
- No experimental or abandoned code remains in the diff, and no comments were added.
- The PR text names no internal deployments, people or hosts.
