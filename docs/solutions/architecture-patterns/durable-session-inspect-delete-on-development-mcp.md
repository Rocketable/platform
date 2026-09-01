---
title: Durable Session Inspect and Delete on Development MCP
date: 2026-09-01
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw
problem_type: architecture_pattern
component: development_workflow
severity: high
applies_when:
  - Operators or coding agents list, snapshot, or delete durable session_entries turns
  - A CLI would open DatabaseURL without constructing the running server
  - Development MCP is the operator door for inspect and delete
  - Protocol ListSessions, ObserveSession, and DeleteSession are wired through the live SessionService
related_components:
  - internal/rocketclaw/protocol
  - internal/rocketclaw/frontend/developmentmcp
  - internal/rocketclaw/backend
  - cmd/rocketclaw
tags:
  - development-mcp
  - protocol
  - session-entries
  - list-session
  - observe-session
  - delete-session
  - rocketclaw-fc
  - session-service
---

# Durable session inspect and delete belong on protocol and Development MCP, not a second binary

## Context

`rocketclaw fc` was a second process that opened config `DatabaseURL` and called store helpers (`ListSessionsInOptions`, `ObserveEntries` / `ObserveSessionEntries`, `DeleteSessionIn`) without starting the server. An operator could run a CLI whose schema and delete semantics did not match the deployed daemon and still mutate `session_entries`.

That CLI is gone in this workspace: `cmd/rocketclaw/fc.go` does not exist, and `cmd/rocketclaw/main.go` has no `fc` subcommand (`run`, `exec`, `setup`, `doctor`, `lint`, `agent-graph`, `oai`, `help` only; `cmd/rocketclaw/main.go:28-70`). Help text no longer teaches `fc` (`cmd/rocketclaw/main.go:28`).

The replacement is protocol messages plus Development MCP tools, wired through the live `SessionService` the daemon already holds. As of this writing that work is pending in PR #32 (open, not yet on the default bookmark). This workspace already contains the tree described below.

DSN one-shot helpers still exist for tests and capture (`ListSessionsInOptions` at `internal/rocketclaw/backend/store.go:1500`, `DeleteSessionIn` at `internal/rocketclaw/backend/store.go:1389`, `ObserveSessionEntries` at `internal/rocketclaw/backend/store.go:1304`). They are not an operator door. Do not revive a CLI that calls them against production `DatabaseURL`.

## Guidance

Put list, one-shot observe, and turns-only delete on the frontend-backend protocol, then expose them only on Development MCP. Do not keep a leftover CLI that talks to the store beside the running server. Do not add a CLI fallback when that door is off.

**Protocol.** `internal/rocketclaw/protocol/sessions.go` defines ordinary request/result DTOs:

- `ListSessionsRequest` / `ListSessionsResult` with `Since`, `Until`, `Limit`, `OmitPreview`, and `SessionSummary` rows (`internal/rocketclaw/protocol/sessions.go:8-25`).
- `ObserveSessionRequest` / `ObserveSessionResult`: conversation id in, a one-shot `[]json.RawMessage` snapshot out (`internal/rocketclaw/protocol/sessions.go:27-35`). The request has no follow cursor.
- `DeleteSessionRequest` / `DeleteSessionResult`: conversation id in, deleted turn count out (`internal/rocketclaw/protocol/sessions.go:37-45`).

**Development MCP tools.** When the door is on, `internal/rocketclaw/frontend/developmentmcp/server.go` registers:

- `rocketclaw_development_list_session` (`internal/rocketclaw/frontend/developmentmcp/server.go:30`, `:193`)
- `rocketclaw_development_observe_session` (`internal/rocketclaw/frontend/developmentmcp/server.go:31`, `:226`)
- `rocketclaw_development_delete_session` (`internal/rocketclaw/frontend/developmentmcp/server.go:32`, `:250`)

Those tools map MCP JSON onto the protocol DTOs. They reject overlay `context` (`internal/rocketclaw/frontend/developmentmcp/server.go:194-195`, `:227-228`, `:251-252`). Slack and External MCP production packages do not register these calls.

**Assembler uses the live store, not a second DSN.** `startDevelopmentMCP` injects callbacks over the process `*backend.SessionService` (`cmd/rocketclaw/mcp.go:249-285`). List calls `sessions.ListSessions` (`cmd/rocketclaw/mcp.go:250`). Observe calls `sessions.ObserveEntries(..., 0)` (`cmd/rocketclaw/mcp.go:262`) — `lastID` is always 0, so the snapshot is the full stored transcript once. Delete calls `sessions.DeleteSession` (`cmd/rocketclaw/mcp.go:279`). Session tools do not take the overlay mutex; overlay read/lint/try-turn still do (`cmd/rocketclaw/mcp.go:217-248` vs `:249-285`). `TestStartDevelopmentMCPSessionToolsDoNotWaitForOverlayLock` locks the overlay mutex and still completes `list_session` (`cmd/rocketclaw/mcp_test.go:464-490`).

**Observe is one-shot.** Tool copy: "Snapshot only; does not follow new turns" (`internal/rocketclaw/frontend/developmentmcp/server.go:226`). Protocol result comment: "one-shot snapshot of stored entry JSON" (`internal/rocketclaw/protocol/sessions.go:32`). An empty conversation id returns `[]` without hitting the store (`internal/rocketclaw/frontend/developmentmcp/server.go:232-233`). Missing or try-turn ids also return an empty array after a store query (`internal/rocketclaw/frontend/developmentmcp/server.go:226`).

**Delete removes `session_entries` turns only.** `SessionService.DeleteSession` runs `DELETE FROM session_entries WHERE conversation_id = $1` and returns `RowsAffected` (`internal/rocketclaw/backend/store.go:976-993`). Tool copy: stored turns only; no thread, goal, or routing rows; no confirmation; missing or try-turn ids return `deleted` 0 (`internal/rocketclaw/frontend/developmentmcp/server.go:250`, `:255-257`).

**Try-turns stay in-memory.** Development MCP chat is `DevelopmentChat` wrapping `memoryStore` (`internal/rocketclaw/backend/development_run.go:24-26`; `memoryStore` at `internal/rocketclaw/backend/store.go:1779`). `startDevelopmentMCP` keeps chats in a process-local map (`cmd/rocketclaw/mcp.go:211-246`). List/observe/delete therefore cannot see try-turn conversation ids, because those ids never land in `session_entries`.

**Door off = no inspect/delete.** If `cfg.MCPDevelopment.Enabled` is false, `startDevelopmentMCP` returns `nil, nil` and does not listen (`cmd/rocketclaw/mcp.go:197-200`). `TestStartDevelopmentMCPDisabledDoesNotListen` asserts that (`cmd/rocketclaw/mcp_test.go:404-408`). There is no CLI fallback. CONCEPTS.md states the inspect/delete path is a second job of Development MCP, not External MCP, and does not include in-memory try-turn chats (`CONCEPTS.md:85-93`).

**Operator text.** README, CHEATSHEET, and exec help name the Development MCP tools and that the door must be on (`README.md:34`, `:42`; `cmd/rocketclaw/CHEATSHEET.md:61`; `cmd/rocketclaw/exec.go:107-110`).

## Why This Matters

A second binary that opens the same DSN as the daemon is a version-skew weapon. List is mostly harmless. Observe of a mismatched schema can mislead. Delete of a mismatched schema can erase or miss rows the live process still believes exist. Moving the calls onto protocol and the already-gated Development MCP door means:

1. The code that mutates `session_entries` is the deployed server's code, not whatever `rocketclaw` happens to be on `PATH`.
2. Auth is Development MCP Basic Auth (`rocketclaw.development.users.json`), not "anyone who can run a binary against the DSN".
3. Frontends stay on protocol. A later RPC can expose `ListSessions` / `ObserveSession` / `DeleteSession` without inventing a new call kind (`internal/rocketclaw/protocol/sessions.go:8-45`).
4. The accepted gap is explicit: when Development MCP is off, inspect and delete do not exist (`cmd/rocketclaw/mcp.go:197-200`). Prefer that gap over a CLI that can be the wrong version.

Ignoring this guidance recreates the original failure: a convenient store screwdriver that bypasses the process that owns the store.

## When to Apply

Apply this pattern when:

- An operator or coding agent must list, snapshot, or delete durable Slack, exec, or External MCP stored turns.
- Those turns live in the daemon's `session_entries` store, not in a Development MCP try-turn chat.
- Development MCP is enabled and the caller authenticates with `rocketclaw.development.users.json`.

Do not apply it when:

- Development MCP is off. There is no inspect/delete path by design (`cmd/rocketclaw/mcp.go:197-200`; `README.md:42`).
- The conversation is a Development MCP try-turn. Those chats are in-memory only (`internal/rocketclaw/backend/development_run.go:24-26`; `cmd/rocketclaw/mcp.go:211-246`). Observe returns `[]`; delete returns `deleted: 0`.
- You need live follow of new turns. Observe is a snapshot (`internal/rocketclaw/frontend/developmentmcp/server.go:226`; assembler `lastID` is 0 at `cmd/rocketclaw/mcp.go:262`).
- You need to wipe thread, goal, or routing rows. Delete is turns only (`internal/rocketclaw/backend/store.go:983`).
- You are tempted to expose the same calls on Slack or External MCP. This tree does not; keep that a separate product decision.
- You are writing a test or capture helper that already uses `ListSessionsInOptions` / `ObserveSessionEntries` / `DeleteSessionIn` against an isolated test DSN. Those helpers remain for tests (`internal/rocketclaw/backend/store.go:1304`, `:1389`, `:1500`). Do not promote them back into an operator CLI.

## Evidence

**Before (do not restore):** a `rocketclaw fc list|observe|delete` process read `DatabaseURL` and called DSN helpers without constructing the server. Wrong binary, same database. `cmd/rocketclaw/fc.go` is deleted in this workspace; the DSN helpers it used remain only as test/capture entry points.

**After — door on:** assembler starts Development MCP and injects live-store callbacks (`cmd/rocketclaw/mcp.go:249-285`). A coding agent calls:

- `rocketclaw_development_list_session` with optional `since` (Go duration or RFC3339), `until` (RFC3339), `limit`, `include_message_preview` (omitted means true) (`internal/rocketclaw/frontend/developmentmcp/server.go:193`; mapping at `:322-349`).
- `rocketclaw_development_observe_session` with `conversation_id`; gets one JSON array of stored entries, then the call ends (`internal/rocketclaw/frontend/developmentmcp/server.go:226-248`).
- `rocketclaw_development_delete_session` with `conversation_id`; gets `{ "deleted": N }` for `session_entries` rows only (`internal/rocketclaw/frontend/developmentmcp/server.go:250-265`; SQL at `internal/rocketclaw/backend/store.go:983`).

Exec replay points at the observe tool, not a CLI: "replayed later with rocketclaw_development_observe_session when Development MCP is enabled" (`cmd/rocketclaw/exec.go:107-110`).

**After — door off:** `startDevelopmentMCP` returns a nil server (`cmd/rocketclaw/mcp.go:198-199`). The three tools are not registered anywhere. `rocketclaw help` has no `fc` command (`cmd/rocketclaw/main.go:28`). CONCEPTS.md: inspect/delete is a second job of Development MCP and does not include try-turn chats (`CONCEPTS.md:93`).

**Try-turn vs durable store:** `run_turn` stores history on `DevelopmentChat.memory` in a process map (`cmd/rocketclaw/mcp.go:239-246`). A later `observe_session` / `delete_session` of that `devmcp-...` id hits `session_entries`, finds nothing, and returns empty / `deleted` 0 (`internal/rocketclaw/frontend/developmentmcp/server.go:226`, `:250`). Durable Slack, exec, and External MCP ids are the ones list/observe/delete can see.

Pending: PR #32 (open as of this writing). Confirm the default bookmark before treating this as shipped trunk behavior.

## Related

- Overlay read/lint/try-turn still take `overlayMu`; session list/observe/delete must not. See `docs/solutions/logic-errors/development-mcp-try-paths-raced-with-live-reload.md` (keep the overlay-clone qualifier on that prevention rule).
- PR #32: https://github.com/Rocketable/platform/pull/32
