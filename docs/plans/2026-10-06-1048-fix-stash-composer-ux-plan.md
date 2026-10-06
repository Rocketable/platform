---
title: "fix: Quiet the Stash button and drop queue row prefixes"
type: fix
date: 2026-10-06
execution: code
---

# fix: Quiet the Stash button and drop queue row prefixes

## Summary

The Stash button in the Web composer stops drawing more attention than Steer: it looks faded under exactly the same condition Steer is faded, and lights up only when Steer lights up. Queued and stashed rows show just the message text, because the action button on the right of each row already says what will happen (Steer/Send for queued, Pop for stashed).

Observed on the deployed page: with text in the composer and no reply running, Stash is enabled and bright white while Steer is disabled and faded. Both prefixes (`Stashed · `, `Queued · `) were introduced by the stash feature commit (`feat(web): stash messages and pop them into the queue`); before it, queue rows showed only the text.

## Decisions

- Stash stays clickable while nothing is running; it only looks faded there. The human asked that Stash not call attention more than Steer, not that it be disabled. Idle stashing is existing behavior and is covered by tests.
- Remove both `Stashed · ` and `Queued · ` prefixes.

## Implementation Units

### U1. Fade Stash whenever Steer is faded

- **File:** `internal/rocketclaw/web/src/ui.tsx` (composer action buttons, around lines 2817–2819)
- Hoist Steer's existing off condition, `!busy || sending || empty || isStopCommand(text)`, into one local and use it for both buttons.
- Steer keeps using it as `disabled`, as today.
- Stash keeps `disabled={sending || empty}` and adds `opacity-50` to its class whenever that shared condition holds. This is the same fade `components/ui/button.tsx` applies through `disabled:opacity-50`.
- **Test:** `internal/rocketclaw/web/src/session-list.browser.test.ts`, inside the existing stash flow:
  - idle with text in the composer (around line 2647): Stash is enabled and faded;
  - reply running with text in the composer (around line 2694): Stash is not faded.

### U2. Remove the row prefixes

- **File:** `internal/rocketclaw/web/src/ui.tsx:267`. Render `{item.text}` only.
- **Tests:** change the existing prefix assertions to match the plain message text inside the row:
  - `internal/rocketclaw/web/src/session-list.browser.test.ts` lines 2656, 2688, 2697;
  - `internal/rocketclaw/web/src/session-commands.browser.test.ts` line 428.

## Verification

- Rebuild the embedded web assets with `internal/rocketclaw/web/build.ts`, because the browser tests and the Go server serve `internal/rocketclaw/internal/web/dist`.
- Run the web unit and browser tests, `make lint`, and `make test`.
- Visually confirm on a session: when idle with text typed, Stash and Steer look equally faded.

## Out of Scope

- The white Pop button on stashed rows.
- Adding a keyboard-hint tooltip to Stash.

README impact: considered; no update needed.
