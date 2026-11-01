---
title: External MCP caller metadata shared the map RocketClaw reads for routing
date: 2026-10-31
category: docs/solutions/security-issues/
module: cmd/rocketclaw
problem_type: security_issue
component: assistant
symptoms:
  - "An External MCP caller that put rocketclaw_agent in its metadata ran its turn as an agent the server does not expose"
  - "Caller keys such as rocketclaw_sender_session showed a fake agent sender in the Web queue and in the agent's prompt"
  - "Web turns in an External MCP chat got ROCKETCLAW_METADATA_WEB_MESSAGE_ID and ROCKETCLAW_METADATA_ROCKETCLAW_RAW_TEXT, made from RocketClaw's own keys"
root_cause: missing_validation
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/protocol
  - internal/rocketclaw/backend
tags: [external-mcp, metadata, trust-boundary, deny-list, inbound-message, routing, migration, rollback]
---

# External MCP caller metadata shared the map RocketClaw reads for routing

## Problem

An External MCP caller sends a free-form `metadata` map with each prompt. `submitExternalMCPInput` (`cmd/rocketclaw/mcp.go:194`) copied that map into `InboundMessage.Metadata`, the same map RocketClaw uses for its own routing and provenance keys. It then deleted six keys. Every other key went through, so an authenticated caller could steer RocketClaw's internals. The worst case was choosing which agent ran the turn, which skipped the "only exposed agents" rule (`mcp.go:118` and `:129`).

The fix is on this branch, in the change "internal/rocketclaw: keep External MCP caller metadata out of routing". Its PR was not open yet when this was written.

## Symptoms

What a caller could do by setting a key that RocketClaw reads from `Metadata`:

| Caller key | Read at | Effect |
|---|---|---|
| `rocketclaw_agent` | `internal/rocketclaw/backend/bridge.go:1068` | Turn runs as any agent, exposed or not |
| `rocketclaw_allowed_agents` | `internal/rocketclaw/backend/session_tools.go:849` | Widens which agents the session may start new sessions with |
| `rocketclaw_sender_session`, `rocketclaw_sender_action` | `internal/rocketclaw/backend/bridge.go:3442`, `internal/rocketclaw/frontend/rpc/server.go:1629` | Fake "message from session X" sender in the prompt and the Web queue |
| `web_message_id`, `background_job_ids` | `internal/rocketclaw/backend/bridge.go:2423` and the queue code | Choose internal turn input and queue item IDs |
| `rocketclaw_raw_text` | `internal/rocketclaw/frontend/rpc/server.go:1637` | Text the Web queue offers when someone edits the message |

The leak also ran the other way. `ROCKETCLAW_METADATA_*` variables were built from `Metadata` minus a skip list. So a Web turn in an External MCP chat turned RocketClaw's own `web_message_id` and `rocketclaw_raw_text` into variables.

An end-to-end test proved the main case. `TestExternalMCPMetadataReachesWebChatTurns` (`cmd/rocketclaw/external_mcp_env_test.go`) sends `"rocketclaw_agent": "intruder"` to an exposed `planner`. On the old code it failed with `the turn did not run as planner: Intruder prompt`.

## What Didn't Work

- **Extending the delete list.** This was rejected before coding. A deny list is only as good as its author's memory of every internal key, and nobody kept that list. Several keys the code reads are not `Inbound*MetadataKey` constants in `protocol`: `rocketclaw_agent` is `inboundAgentMetadataKey` in `internal/rocketclaw/backend/thread_bridges.go:29`, `background_job_ids` is in `internal/rocketclaw/backend/background.go:67`, and `web_message_id` and `web_goal_prompt` are bare strings. Each new internal key would reopen the hole unless someone remembered to add it.
- **Dropping every `rocketclaw_*` key.** This is smaller, but it is still a rule list. `web_message_id`, `web_goal_prompt` and `background_job_ids` have no prefix, so it needs exceptions on day one, and every future internal key has to follow the prefix rule.
- **Accepting that queued work loses its variables once.** The first plan added the new field and let items queued before the upgrade lose their `ROCKETCLAW_METADATA_*` values. The user asked for a safe upgrade, which led to the migration. That also cleans planted keys out of input that was already queued.
- **Writing the migration with `json` functions because rows "may hold NUL".** The plan claimed that stored `inbound_json` could contain `\u0000`, which a `::jsonb` cast rejects. A plan review showed this is false. Every writer of these columns strips it with `removeSessionEntryNUL` (`internal/rocketclaw/backend/store.go:1739`, called from `startTurnDB` in `internal/rocketclaw/backend/store_dao.go`, `putThreadQueueItem`, `internal/rocketclaw/backend/conversations.go:390` and `internal/rocketclaw/backend/shadows.go`). The migration uses `jsonb`. Check the writers before designing around a data shape you only assume.
- **The first migration draft** kept a caller-planted `rocketclaw_raw_text` in `Metadata`, and left `Metadata` as `null` when every key moved out. Plan reviews caught both.

## Solution

Give caller input its own field, so a caller key can never collide with an internal one:

```go
// internal/rocketclaw/protocol/types.go
// ExternalMCPMetadata is an External MCP caller's free-form metadata, which its turns see as
// ROCKETCLAW_METADATA_* variables. RocketClaw never reads it for routing.
ExternalMCPMetadata map[string]string
```

```go
// cmd/rocketclaw/mcp.go, submitExternalMCPInput
inbound.ExternalMCPMetadata = map[string]string{}
maps.Copy(inbound.ExternalMCPMetadata, metadata)
inbound.ExternalMCPMetadata["external_conversation_id"] = externalConversationID

if principal != "" {
	inbound.Metadata[protocol.InboundPrincipalMetadataKey] = principal
}
```

`turnShellEnv` builds the variables from `origin.ExternalMCPMetadata` (`internal/rocketclaw/backend/bridge.go:2655` and `:2670`). The six-key delete list and the skip list in `externalMCPMetadataEnv` are gone. `Metadata` on an External MCP inbound now holds only what RocketClaw sets: the raw text from `NewInboundMessageFromContent` (`internal/rocketclaw/protocol/types.go:267`) and the authenticated principal.

Migration `internal/rocketclaw/backend/migrations/080_external_mcp_metadata.sql` rewrites stored External MCP inbounds wherever an inbound is stored: `turns.inbound_json`, `shadow_turns.inbound_json`, `conversations.producer_inbound_json`, and each element of the journaled steer arrays in `steps` under `<turn>/steers`. The steers were the place that was easy to miss. For each one:

- `Metadata` keeps only the keys the old variable rule skipped: origin, media, principal, principal name and email, and `source`.
- Every other key moves to `ExternalMCPMetadata`, so the turn's variables stay the same after the upgrade.
- `rocketclaw_raw_text` is deleted, not moved. The old code replaced `Metadata` with the caller's map, so in these rows that key came from the caller or from a Web queue edit, and it drives the queue's edit text. That variable is the only one that disappears.
- Rows with nothing to move stay as they are. `Metadata` becomes `{}`, not `null`, when everything moves.

The down migration merges `ExternalMCPMetadata` back into `Metadata`, where the older binary reads it. It skips the six keys the older binary deleted from caller input (origin, media, principal, principal name, principal email, sender agent), so a rollback never plants a principal or sender agent that binary would have refused. If a key is in both maps, the `Metadata` value wins.

Tests: `TestSubmitExternalMCPInputPreservesPublicConversationMetadata` (`cmd/rocketclaw/mcp_test.go`) asserts the whole `Metadata` map equals `{principal, raw text}`. `TestExternalMCPMetadataMigration` (`internal/rocketclaw/backend/store_schema_test.go`) seeds all four storage places, migrates up and down, and compares variables before and after.

## Why This Works

The old guard asked "which keys are dangerous?", and that list could never be complete because the code that reads `Metadata` keeps growing. With a separate field, nothing that reads `Metadata` can see caller input, whatever keys exist today or later. The variables come from one field with one naming rule, so nothing needs to be filtered in either direction. Migrating the stored rows matters because queued turns, steers and producer inbounds outlive the deploy. Without it, a key planted before the upgrade would still route a turn after it.

## Prevention

- Never merge a caller-controlled map into a map that also carries trusted keys. Give untrusted input its own field or type, and convert it in one place. If you catch yourself writing `delete(m, someKey)` after copying input into a shared map, the design is wrong.
- The comment on `InboundMessage.Metadata` (`internal/rocketclaw/protocol/types.go:158`) still says it "carries source-supplied context". Read that as context RocketClaw's own connectors set. Do not use it to carry outside keys. Today the connectors each set named keys: Web (`internal/rocketclaw/frontend/rpc/server.go:1527`), voice (`internal/rocketclaw/frontend/rpc/voice.go:455`) and Slack (`internal/rocketclaw/frontend/slack/connector.go:1113`). Only External MCP ever bulk-copied a map from outside. Keep it that way for any new connector, page or API.
- To find who could abuse a shared map, list its readers, not its writers. Grep for `Metadata[` across the backend and frontends. The internal keys are spread across several packages and not all of them are `protocol` constants.
- Test the whole map with `Equal`, not `NotContains` for a few chosen keys. The old unit test asserted that three keys were absent, and stayed green while every other key leaked.
- Prove the exploit end to end before the fix. Seed a forbidden agent with a different prompt and assert the model request carries the allowed agent's prompt.
- When a fix changes where stored input lives, migrate every place an inbound is stored. Today that is `turns`, `shadow_turns`, `conversations.producer_inbound_json` and the `<turn>/steers` journal in `steps`. Grep for `inbound_json`, `json.Marshal(inbound` and `journaledSteer` to confirm.
- A down migration restores the old binary's behavior, including its holes. Here a rollback brings back `rocketclaw_agent` and the sender keys in `Metadata` for migrated rows, and the older binary acts on them again. That was accepted on purpose. Say so in the plan, and skip at least the keys the older binary itself refused, so a rollback is never worse than the old code.

## Related Issues

- `docs/solutions/logic-errors/external-mcp-thread-lost-metadata-after-copy-free-hidden-runs.md`: how the stored External MCP metadata reaches turns in the Web chat and in subagents. It uses the same `turnShellEnv` path, which now reads `ExternalMCPMetadata`.
- `docs/solutions/security-issues/easy-agent-folder-checks-must-compare-real-paths.md`: another security fix from the easy-mode work (PR #822), where a check compared values from two different spaces (real paths and names).
- Plan: `docs/plans/2026-10-31-1317-fix-easy-mode-follow-ups-plan.md` (KTD3 covers the migration). Decisions: `docs/specs/2026-10-31-easy-mode-follow-ups-design.md`.
- `CONCEPTS.md`: External MCP Metadata, External MCP Session.
