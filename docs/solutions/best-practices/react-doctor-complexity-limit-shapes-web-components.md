---
title: The react-doctor Complexity Limit Shapes RocketClaw Web Components
date: 2026-10-20
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: frontend
severity: medium
applies_when:
  - Adding a branch, ternary, or conditional spread to a large React component in internal/rocketclaw/web/src/ui.tsx
  - Reviewing or simplifying a small hook or module function next to SessionApp or CommandPalette
  - make lint in internal/rocketclaw/web fails with react-doctor/no-high-complexity-react-function
tags:
  - react-doctor
  - lint
  - complexity
  - make-lint
  - web
---

# The react-doctor Complexity Limit Shapes RocketClaw Web Components

## Context

`make lint` in `internal/rocketclaw/web` runs `bunx react-doctor@latest --no-telemetry --blocking warning` over every `.tsx` file. With `--blocking warning`, every warning fails lint. One of those warnings is `react-doctor/no-high-complexity-react-function`, which counts the branches (`if`, `&&`, `||`, ternaries, `??`, conditional spreads) inside a single React function.

While building title-only Cmd/Ctrl+P and palette shortcuts (`docs/plans/2026-10-20-2028-feat-palette-title-search-and-shortcuts-plan.md`), the limit was hit twice:

- `CommandPalette` reached cyclomatic complexity 16 and was flagged.
- `SessionApp` reached 17 after adding a `vertical ? [...] : []` command list and a `route.shortcuts ? <Page /> : null`, and was flagged. On `main` it sat at 15 and passed.

So in practice 15 passes and 16 fails. `SessionApp` and `CommandPalette` both sit at the limit, so a single new branch in either one fails lint.

## Guidance

Move a new branch out of the big component instead of adding it inline:

- Put state-plus-branch logic in a small hook. `usePaletteSessions` in `ui.tsx` holds the "plain text or filter" routing and the title filter that would otherwise sit in `CommandPalette`.
- Put a conditional list in a plain module function. `keyCommands(step, toggle, medium)` builds the palette's tab commands, including the `medium` condition, outside `SessionApp`.
- Let a page decide its own visibility. `ShortcutsPage({ show })` returns `null` when hidden, like `PageIndex`, so `SessionApp` renders it without a ternary.
- Move behavior that belongs to a hook into that hook. Tab stepping became `useTabs().step`, which removed two branches from `SessionApp`'s key handler.

To read the exact numbers, run `bunx react-doctor@latest --no-telemetry --blocking warning --no-color --verbose src/ui.tsx` and open the `diagnostics.json` it names. The message gives cyclomatic and cognitive complexity per function.

## Why This Matters

These helpers look like the single-use abstractions that `AGENTS.md` says to inline, but for TypeScript and React code `AGENTS.md` lets react-doctor win. In the review of this work, two reviewers proposed inlining `keyCommands` and `shownTitle`. The validator rejected the `keyCommands` proposal because inlining pushes `SessionApp` back over the limit and fails `make lint`. Without this note, the next simplification pass is likely to inline them, break lint, and rediscover the limit. The Go side has the same pattern under `gocyclo`'s limit of 30.

## When to Apply

- Before adding a branch to `SessionApp` or `CommandPalette`, expect lint to fail and plan where the branch will live.
- Before inlining a small hook or function in `ui.tsx`, check whether it exists to keep a component under the limit.
- Do not raise the limit, add a react-doctor config, or suppress the rule without the human partner's approval (`AGENTS.md`, linter suppression rule).
