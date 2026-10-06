---
title: RocketClaw start-new-thread creates Web sessions - Plan
type: feat
date: 2026-10-06
artifact_contract: ce-unified-plan/v1
execution: code
---

# RocketClaw start-new-thread creates Web sessions - Plan

## Goal Capsule

- **Objective:** `rocketclaw_start_new_thread` creates a new Web session for every caller (human Slack, human Web, cron) and returns a full URL the agent can relay.
- **Means:** Reuse the Web "new session" path (random conversation ID, managed thread bridge, first turn); delete the Slack-root path; build the Web URL from this machine's Tailscale IPv4 and the `web.listen_address` port.
- **Authority:** Decisions below were settled with the human partner on 2026-10-06. Repository instructions govern implementation.
- **Execution:** Implement and verify locally once this plan is approved. Shipping and deployment need separate authority.
- **Stop:** Report budget conflicts, failing checks, or any need to touch packages beyond those listed instead of widening scope.

## Product Contract

### Summary

RocketClaw is de-emphasizing Slack. Model-created conversations move to Web only. The tool stays default-deny and still needs an explicit per-agent `allow`.

### Problem Frame

- The tool only works on human Slack turns and only creates Slack threads. Web turns never see it, and the backend rejects `web` sources.
- Cron lost the tool when cron moved from `harnessbridge/raw_run.go` to `cmd/rocketclaw/cron.go` + `backend.RunTurn`. Cron turns are non-human `system` turns, so the bridge gate (`startNewThreadNativeTurn`) hides the tool. `CHEATSHEET.md` still says cron has it.
- The tool description claimed the new conversation inherits the caller's context. It does not. (Fixed already in this change: description and `CHEATSHEET.md` now say it starts empty.)

### Key Decisions

- **Web only, for every caller.** (session-settled: user-approved — chosen over same-surface and Web+Slack mirror.)
- **The agent's permission alone decides exposure.** An explicit per-agent `allow` offers the tool on every turn: Slack, Web, cron, external MCP, goal continuations, scheduled messages. (session-settled: user-directed — chosen over gating by turn type; this replaced the earlier human-Slack/human-Web/cron caller list.)
- **Starts empty; prompt carries context.** (session-settled: user-approved — chosen over copying caller history via `ForkConversation`.)
- **Result must include a full, relayable URL.** (session-settled: user-approved.)
- **Tailscale is mandatory; links always use the Tailscale IPv4.** No `web.public_url` setting and no use of the listen address host. Existing configs keep working. (session-settled: user-directed — chosen over an optional `web.public_url` override, which suggested proxy setups that break Web's IP-based identity.)
- **No special Tailscale fallback.** Web login already depends on the `tailscale` CLI, so a failed lookup is an ordinary tool error. (session-settled: user-approved.)

### Requirements

- R1. The tool is offered whenever the agent explicitly allows it, on every kind of turn. No other check decides exposure.
- R2. Which agent a new session may use stays limited: Slack turns to the channel's allowed agents, cron runs to the job's own agent.
- R3. Each call creates a new managed conversation with a fresh random ID (`rand.Text()`, same as Web `createSession`). No Slack message is posted.
- R4. `title` becomes the session name shown in the Web sidebar.
- R5. `prompt` is submitted verbatim (whitespace preserved) as the first turn, origin `System`, with no Slack reply target.
- R6. Agent: `agent` argument, else the calling agent. It must be configured. Slack callers stay limited to the channel's allowed agents (existing metadata). Cron runs are locked to the job's agent.
- R7. Result: `{"conversation_id": "<id>", "url": "<base>/s/<base64url(id)>"}`. The URL encoding matches `web/src/session-id.ts`.
- R8. `created_by` records the triggering human principal, or `cron` for cron runs.
- R9. `<base>` is `http://<tailscale-ipv4>:<port>`: the host is always this machine's Tailscale IPv4 from `tailscale ip -4`, and the port comes from `web.listen_address`. A failed lookup fails the tool call before any session is created.

### Scope Boundaries

No history copy, no Slack mirroring of the new session, no parent/child link in the UI, no Web UI changes.

## Planning Contract

### Key Technical Decisions

- KTD1. **Exposure in `bridge.go` is the permission check alone.** Delete `startNewThreadNativeTurn` and the Slack-only opt-out metadata. For cron, the tool sets `AllowedAgents` to the current agent (matches the old raw-run lock) instead of reading channel metadata.
- KTD2. **`threadBridgeManager.StartNewThread` drops the `createRoot` callback.** It validates the agent as today, mints the ID, calls `ensureStartedThread` (`requireCreated: true`), sets the name via `SessionService.UpdateConversationDetails`, and submits the first turn. The source switch goes away; gating lives only in the bridge (R1).
- KTD3. **Shrink `protocol.StartNewThreadRequest`.** Remove `Source` and `SlackReply`; add the creator principal. Delete `StartNewThreadRootResult`.
- KTD4. **Delete the Slack-root path:** `Connector.StartNewThreadRoot`, the `SlackFrontend.StartNewThreadRoot` interface method, `startThreadRoot` wiring in `app.go`/`runtime.go`, and the regenerated mocks (`backend/slackfrontend_mocks_test.go`, `cmd/rocketclaw/slackfrontend_mocks_test.go`) plus their tests.
- KTD5. **URL building** lives in `threadBridgeManager.StartNewThread`, using `base64.RawURLEncoding`. Resolve the Tailscale IPv4 on each call (the tool is rare; no startup state or cache). Shell out to `tailscale` the same way `frontend/rpc/server.go` `tailscaleUsername` does; no Tailscale library dependency.

## Implementation Units

### U1. Removed

An optional `web.public_url` override was built and then dropped: Tailscale is mandatory and links always use the Tailscale IPv4.

### U2. Web-only creation

- **Files:** `internal/rocketclaw/protocol/types.go`, `backend/thread_bridges.go`, `backend/app.go`, `backend/runtime.go`, `frontend/slack/connector.go`, mocks, and their tests.
- **Tests:** rewrite `TestThreadBridgeManagerStartNewThreadUsesFreshThreadLocalConversation` for a Web ID, name, creator, verbatim prompt, and no Slack target. Keep the agent-lock rejection case. Delete `TestThreadBridgeManagerStartNewThreadAcceptsSystemSourceWithChannel`, the source-rejection cases, and `TestStartNewThreadRoot*` in `connector_test.go`. Update `app_test.go:133`.

### U3. Tool gating, result URL, cron restore

- **Files:** `internal/rocketclaw/backend/bridge.go`, `bridge_test.go`.
- **Tests:** `agentExplicitlyAllowsRocketClawTool` already covers the permission check. Cron locks the agent. `TestStartNewThreadToolPreservesLiteralPrompt` asserts the exact URL.

### U4. Docs

- **Files:** `cmd/rocketclaw/CHEATSHEET.md` (where it runs, what it creates, URL), `README.md` (how links are built), `internal/rocketclaw/skel/agents/main.md` only if it describes this tool.

## Verification Contract

`gofmt` on touched files, `go test ./...`, `make lint`, `make test` (including the CLOC budget; the Slack-root deletion should offset the additions).

## Rollout Note

No config change is needed to upgrade. The machine must be on Tailscale for the tool to return links.
