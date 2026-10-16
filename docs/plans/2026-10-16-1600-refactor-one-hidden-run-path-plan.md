---
title: One Hidden-Run Path With Live Progress - Plan
type: refactor
date: 2026-10-16
execution: code
---

# One Hidden-Run Path With Live Progress - Plan

## Goal

When someone runs a cron job from `/cron`, the chat that opens shows the job working, live, instead of "<job> is running" until it finishes. The same applies to every hidden run that reports into a chat: scheduled cron wakes, one-off cron, and External MCP. All of them use one code path, and the change leaves less code than before.

## What the code does today (verified)

- Cron, one-off cron, and External MCP all run as a **Private Producer Conversation** whose turns carry `SyncDestination`. `syncConversation` (`internal/rocketclaw/backend/conversations.go`) copies the finished history into the destination. External MCP sets it in `cmd/rocketclaw/mcp.go:279`; one-off cron in `frontend/cron/manager.go:239`; background wakes in `backend/bridge.go:1104`.
- A second, older mechanism also exists: a bridge `Config.ManagedConversationID` makes the session store write each saved entry into both conversations (`appendExternalMCPEntry`, `appendPairEntryDB`) and makes turns wait on a pair lock (`lockTurnPair`). **Production never sets `Config.ManagedConversationID` or `Config.ExternalConversationID`.** The only bridge factory (`thread_bridges.go:852`) sets `ConversationID` and `Agent`. This path is dead code that only tests keep alive.
- While a hidden run is working, its in-progress turn (`active_turns` row with a live `output_trace_json`) belongs to the producer. The destination's history query (`observeTranscriptDB`) only reads the destination's own active turns. Change notifications for `active_turns` go only to the producer ID. So the Web chat shows nothing until Sync.
- The Web works around this with a client-only placeholder (`notePendingCron`/`usePendingCron` in `web/src/ui.tsx`) that prints "<stem> is running".

## Requirements

- R1. A chat shows the in-progress turn of any hidden run whose `SyncDestination` is that chat, live, with the usual Working… state.
- R2. When the run finishes and syncs, the synced copy replaces the live turn with no flash: both use the key `turn:<producer>:<turn>`.
- R3. Live producer turns sort after the chat's existing messages and never appear on older pages.
- R4. Only one hidden-run mechanism remains: `SyncDestination` plus Sync.
- R5. Net source lines go down (Go and web CLOC), with no lint suppressions or budget changes.

## Decisions

- KTD1. **Delete the dead pair path** rather than port anything to it: `Config.ManagedConversationID`, `Config.ExternalConversationID`, `waitingTurnCancel`, the pair-lock wait in `handle`, `settlePairWait`, `completeRequestTurnPairReservation`, `lockTurnPair`, gate `token`/`refs`/`releaseTurnGateReference`, `sessionStore.managedConversationID`/`managedReplayPrefix`, `appendExternalMCPEntry`, `appendPairEntryDB`, the `finishTurn` pair release, and `externalMCPManagedEntry`'s always-nil prefix. Keep turn-pair **reservations**, which Sync and cancel still use.
- KTD2. **Read live producer turns in the one transcript query.** In `observeTranscriptDB`, `readable_turns` also takes active turns whose `inbound_json->>'SyncDestination'` is the chat, when the chat has no staged revert. Only running rows (`terminal = ''`) count, so old failed hidden turns never show up in existing chats. Such a row reports `source_conversation_id` = producer and `synced` = true, so its key and attribution match the later synced copy. It sorts after all of the chat's history and never appears on settled older pages. `saved_turns` is keyed by (source, turn ID), so a synced copy hides the live row while a producer's reused turn ID still never hides the chat's own turn (an existing test invariant).
- KTD3. **Notify the destination.** A migration adds an `active_turns` trigger that sends the existing transcript notification to the row's `SyncDestination` on insert, delete, and trace/terminal/phase updates.
- KTD4. **Delete the Web placeholder.** The live turn replaces "<stem> is running".

## Units

1. Delete the dead pair path (KTD1) and its dedicated tests; update tests that set the dead fields only incidentally. Verify: `go build ./...`, backend tests.
2. Live producer turns in history (KTD2, KTD3). Add one backend contract test: a running producer turn shows in its destination's transcript under the producer key, sorted last, and is replaced by the synced copy. Verify: test plus `frontend/rpc` history tests.
3. Delete the Web placeholder (KTD4). Update `web/README.md` and `CONCEPTS.md` wording. Verify: web build and tests.
4. Full verification: `gofmt`, `go test ./...`, `make lint`, `make test`, CLOC before/after.

## Risks

- A finished producer row is deleted a few milliseconds before Sync commits the copy. A history read that lands in that gap briefly shows the turn missing until the Sync notification arrives.
- The chat may show the empty-state text for the moment between opening and the producer turn starting.
- `sessionHistorySQL` still contains subqueries. It is shared and untouched here, so rewriting it into CTEs is out of scope.
