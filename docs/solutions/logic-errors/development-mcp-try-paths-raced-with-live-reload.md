---
title: Development MCP Try Paths Raced With Live Reload
date: 2026-08-23
last_updated: 2026-09-01
category: docs/solutions/logic-errors/
module: cmd/rocketclaw
problem_type: logic_error
component: development_workflow
symptoms:
  - Live reload could delete overlay clones while a Development MCP try copied them.
  - read, lint, and run_turn raced with reload because the reload mutex did not cover try staging.
  - Same-id overlapping run_turn calls shared unsynchronized in-memory chat.
root_cause: concurrency
resolution_type: code_fix
severity: high
tags:
  - development-mcp
  - live-reload
  - overlay
  - concurrency
  - mutex
  - run-turn
---

# Development MCP Try Paths Raced With Live Reload

## Problem

Development MCP try tools walked and copied live overlay clones while live reload could delete those same clones, and `run_turn` could run two turns on the same conversation ID at once.

## Symptoms

- A Development MCP read, lint, or try-turn could observe a live overlay tree while reload was wiping it. `ReplaceRuntimeAssetsAfterValidation` rebuilds into a stage, then calls `resetRuntimeDirectory(live, logger, false)` before committing the stage (`internal/rocketclaw/skel/skel.go:251-280`). With overlays not preserved, clones are removed (`internal/rocketclaw/skel/skel.go:874-882`).
- `ReadOverlayContext` walks the live clone and treats a missing root as empty rather than an error (`internal/rocketclaw/skel/skel.go:632-679`). If reload removes the clone before those Stat calls, a known spec can succeed with an empty file list. A deletion during `WalkDir` returns an error, not a list.
- `LintTry` and `RunTryTurn` both call `StageLiveRuntime`, which copies from each overlay clone (`internal/rocketclaw/backend/development_try.go:45-46`, `internal/rocketclaw/backend/development_try.go:104-105`, `internal/rocketclaw/skel/skel.go:185-196`).
- Before the door lock, two `run_turn` calls with the same conversation ID could share one `DevelopmentChat` while the chat map mutex only covered lookup or insert.

## What Didn't Work

A fail-closed check in `ReadOverlayContext` when the clone directory had vanished was considered and not landed. `TestReadOverlayContextMissingClone` expects a known spec with no clone on disk to succeed with an empty file list (`internal/rocketclaw/skel/skel_test.go:715-722`). Changing that contract without coverage would have been a second, untested behavior change. Reload already deletes live overlays on purpose, so the try path must wait, not reinterpret a missing clone as a new error class.

## Solution

Share the existing reload mutex with Development MCP try paths, and serialize same-id chat the same way External MCP already does.

`startDevelopmentMCP` takes `overlayMu *sync.Mutex` (`cmd/rocketclaw/mcp.go:197`). Assemble passes `rt.OverlayMu`, which is `&reloadMu` from the backend runtime (`cmd/rocketclaw/assemble.go:128`; `internal/rocketclaw/backend/app.go:167`, `:336`). `requestReload` already locks `reloadMu` around `ReplaceRuntimeAssetsAfterValidation` (`internal/rocketclaw/backend/app.go:172-174`). Each overlay-touching door locks that mutex around the live-tree call:

- read: lock, then `skel.ReadOverlayContext` (`cmd/rocketclaw/mcp.go:217-221`)
- lint: lock, then `backend.LintTry` (`cmd/rocketclaw/mcp.go:227-231`)
- turn: lock, then `locks.Lock(conversationID)`, then `RunTryTurn` (`cmd/rocketclaw/mcp.go:232-248`)

Because try paths use the same mutex, a read, lint, or try-turn waits until reload finishes, and reload waits until that door callback returns (the full read, lint, or try-turn, not only the clone walk).

Same-id chat uses `backend.NewKeyedConversationLocks`. Development MCP holds `locks.Lock(conversationID)` across `RunTryTurn` (`cmd/rocketclaw/mcp.go:214`, `:236-237`; `internal/rocketclaw/backend/development_try.go:76-77`).

Store-backed session tools (list, observe, delete) do not take `overlayMu`. They talk to `SessionService`, not live overlay clones (`cmd/rocketclaw/mcp.go:249-285`). `TestStartDevelopmentMCPSessionToolsDoNotWaitForOverlayLock` holds the overlay mutex and still completes `list_session` (`cmd/rocketclaw/mcp_test.go:464-490`).

`TestStartDevelopmentMCPReadWaitsForOverlayLock` holds the overlay mutex, starts `rocketclaw_development_read_context_from_overlay`, and fails if the tool returns before unlock (`cmd/rocketclaw/mcp_test.go:411-461`). `TestStartDevelopmentMCPEnabledStarts` calls lint and `run_turn` through that door (`cmd/rocketclaw/mcp_test.go:377-393`).

## Why This Works

Live overlay clones are a single mutable tree. Reload's commit path is "delete live, including overlays, then move the staged tree in." Try paths are "read or copy that live tree." Those two operations cannot overlap. Passing `&reloadMu` into `startDevelopmentMCP` reuses the mutex that already owns the reload critical section.

The conversation lock is a different resource. Overlay serialization stops the filesystem race. Per-conversation serialization stops two turns from mutating one `DevelopmentChat`. `chatsMu` only protects the map; the keyed lock is what matches External MCP's one-turn-per-conversation-ID contract.

Fail-closed-on-vanished-clone is not required once the lock is held for the whole walk or stage. A missing clone after the lock is acquired is the same idle empty-tree case `TestReadOverlayContextMissingClone` already documents, not a mid-reload tear.

Session list/observe/delete are a different door class. They do not read or copy clones, so waiting on `overlayMu` would stall inspect/delete behind overlay work for no benefit.

## Prevention

- Any new Development MCP door that reads or copies live overlay clones must take the overlay mutex for the whole call (`cmd/rocketclaw/mcp.go:217-248`).
- Doors that only talk to `SessionService` must not take that mutex (`cmd/rocketclaw/mcp.go:249-285`; `cmd/rocketclaw/mcp_test.go:464-490`).
- Keep Development MCP overlay try paths and live reload on one mutex. Do not introduce a second overlay lock unless reload's critical section moves with it.
- Keep `run_turn` on `locks.Lock(conversationID)` across the turn (`cmd/rocketclaw/mcp.go:236-237`).
- Keep a wait test that holds the overlay mutex and asserts the read tool does not complete until unlock (`cmd/rocketclaw/mcp_test.go:411-461`).
- Keep the enabled-start test calling lint and `run_turn` through `startDevelopmentMCP` (`cmd/rocketclaw/mcp_test.go:390-393`).
- Do not change `ReadOverlayContext` missing-clone success-with-empty-files unless that contract is intentionally redesigned and tested.

## Related Issues

- [Slack root app-mention redelivery](slack-root-app-mention-redelivery-cleared-buffered-follow-ups.md) — another mutex-across-lifecycle lesson, different subsystem.
- [Durable session inspect and delete on Development MCP](../architecture-patterns/durable-session-inspect-delete-on-development-mcp.md) — store-backed session tools on the same door must not take `overlayMu`.
- Feature plan: `internal/rocketclaw/docs/plans/2026-08-22-1207-feat-rocketclaw-development-mcp-plan.md` (assumed a stable live clone during try; did not specify this lock).
