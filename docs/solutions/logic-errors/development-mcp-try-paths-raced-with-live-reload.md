---
title: Development MCP Try Paths Raced With Live Reload
date: 2026-08-23
category: docs/solutions/logic-errors/
module: internal/rocketclaw/app
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

- A Development MCP read, lint, or try-turn could observe a live overlay tree while reload was wiping it. `ReplaceRuntimeAssetsAfterValidation` rebuilds into a stage, then calls `resetRuntimeDirectory(live, logger, false)` before committing the stage (`internal/rocketclaw/skel/skel.go:251-280`). With overlays not preserved, clones are removed (`internal/rocketclaw/skel/skel.go:874-886`).
- `ReadOverlayContext` walks the live clone and treats a missing root as empty rather than an error (`internal/rocketclaw/skel/skel.go:632-679`). If reload removes the clone before those Stat calls, a known spec can succeed with an empty file list. A deletion during `WalkDir` returns an error, not a list.
- `LintTry` and `RunTryTurn` both call `StageLiveRuntime`, which copies from each overlay clone (`internal/rocketclaw/developmentmcp/lint.go:14-18`, `internal/rocketclaw/developmentmcp/turn.go:15-18`, `internal/rocketclaw/skel/skel.go:185-196`).
- Before the door lock, two `run_turn` calls with the same conversation ID could share one `DevelopmentChat` while the chat map mutex only covered lookup or insert (`internal/rocketclaw/app/app.go:796-799`).

## What Didn't Work

A fail-closed check in `ReadOverlayContext` when the clone directory had vanished was considered and not landed. `TestReadOverlayContextMissingClone` expects a known spec with no clone on disk to succeed with an empty file list (`internal/rocketclaw/skel/skel_test.go:732-739`). Changing that contract without coverage would have been a second, untested behavior change. Reload already deletes live overlays on purpose, so the try path must wait, not reinterpret a missing clone as a new error class.

## Solution

Share the existing reload mutex with Development MCP try paths, and serialize same-id chat the same way External MCP already does. As of this writing the lock is on the Development MCP bookmark, not on trunk.

`startDevelopmentMCP` takes `overlayMu *sync.Mutex`. `Run` passes `&reloadMu` (`internal/rocketclaw/app/app.go:540`). Each try door locks that mutex around the live-tree call:

- read: lock, then `skel.ReadOverlayContext` (`internal/rocketclaw/app/app.go:779-783`)
- lint: lock, then `developmentmcp.LintTry` (`internal/rocketclaw/app/app.go:785-788`)
- turn: lock, then `locks.lock(conversationID)`, then `RunTryTurn` (`internal/rocketclaw/app/app.go:789-794`)

`requestReload` already locks `reloadMu` around `ReplaceRuntimeAssetsAfterValidation` (`internal/rocketclaw/app/app.go:206-208`). Because try paths use the same mutex, a read, lint, or try-turn waits until reload finishes, and reload waits until that door callback returns (the full read, lint, or try-turn, not only the clone walk).

Same-id chat uses `keyedConversationLocks`. Development MCP holds `locks.lock(conversationID)` across `RunTryTurn` (`internal/rocketclaw/app/app.go:776`, `internal/rocketclaw/app/app.go:793-794`).

`TestStartDevelopmentMCPReadWaitsForOverlayLock` holds the overlay mutex, starts `rocketclaw_development_read_context_from_overlay`, and fails if the tool returns before unlock (`internal/rocketclaw/app/app_test.go:457-508`). `TestStartDevelopmentMCPEnabledStarts` calls lint and `run_turn` through that door (`internal/rocketclaw/app/app_test.go:429-445`).

## Why This Works

Live overlay clones are a single mutable tree. Reload's commit path is "delete live, including overlays, then move the staged tree in." Try paths are "read or copy that live tree." Those two operations cannot overlap. Passing `&reloadMu` into `startDevelopmentMCP` reuses the mutex that already owns the reload critical section.

The conversation lock is a different resource. Overlay serialization stops the filesystem race. Per-conversation serialization stops two turns from mutating one `DevelopmentChat`. `chatsMu` only protects the map; the keyed lock is what matches External MCP's one-turn-per-conversation-ID contract.

Fail-closed-on-vanished-clone is not required once the lock is held for the whole walk or stage. A missing clone after the lock is acquired is the same idle empty-tree case `TestReadOverlayContextMissingClone` already documents, not a mid-reload tear.

## Prevention

- Any new Development MCP door that reads or copies live overlay clones must take the overlay mutex for the whole call (`internal/rocketclaw/app/app.go:779-794`).
- Keep Development MCP and live reload on one mutex. Do not introduce a second overlay lock unless reload's critical section moves with it.
- Keep `run_turn` on `keyedConversationLocks.lock(conversationID)` across the turn (`internal/rocketclaw/app/app.go:793-794`).
- Keep a wait test that holds the overlay mutex and asserts the read tool does not complete until unlock (`internal/rocketclaw/app/app_test.go:457-508`).
- Keep the enabled-start test calling lint and `run_turn` through `startDevelopmentMCP` (`internal/rocketclaw/app/app_test.go:441-445`).
- Do not change `ReadOverlayContext` missing-clone success-with-empty-files unless that contract is intentionally redesigned and tested.

## Related Issues

- [Slack root app-mention redelivery](slack-root-app-mention-redelivery-cleared-buffered-follow-ups.md) — another mutex-across-lifecycle lesson, different subsystem.
- Feature plan: `internal/rocketclaw/docs/plans/2026-08-22-1207-feat-rocketclaw-development-mcp-plan.md` (assumed a stable live clone during try; did not specify this lock).
