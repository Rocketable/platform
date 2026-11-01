# Easy Mode Follow-Ups: Decisions

Date: 2026-10-31. Follows PR #822 (easy mode agents, merged). Every decision below was made by the user, one at a time, in the session that shipped #822. Code references point at `main` as of 834fae8e and may shift.

## Scope

Four loose ends, fixed in one PR. A fifth (making easy the default and moving existing advanced agents, including the bundled `main` agent, over) is **out of scope**: `main` manages `agents/`, `cron/`, `skills/` and `rocketclaw.json`, which easy agents are barred from, so it needs its own design.

Commit layout (the user's `prcleanup` rule): first the fixes for problems carried over from before #822 (D1, D2), then the follow-ups to #822 (D3, D4). Docs go with the commit whose behavior they describe.

## D1. `rocketclaw_store` has `source` only where a source can be read

**Problem.** `pageTools` (`internal/rocketclaw/backend/page_tools.go:49`, `:101`) always builds `rocketclaw_store` with a `source` field read through `outboundAttachment(root, shell, …)`. The OpenCode MCP bridge (`backend/opencode_mcp.go:36-40`, `:140`) serves the same tools with a nil root, deletes `source` from the advertised schema and rewrites the description. The MCP library does not validate arguments against the schema, so a call that still sends `source` reaches `root.ReadFile` on a nil `*os.Root` and panics.

**Decision (user-directed; rejected: refusing `source` at the bridge, giving the bridge a real workspace root, leaving it).** The user's principle: never expose a field that trips the caller.
- `pageTools` builds a base `rocketclaw_store` that accepts only `content` or `content_base64`. It takes no root and no shell.
- Native sessions (`backend/bridge.go`, where `pageTools` is called with the workspace root and the agent's `CustomShell`) wrap the base tool: add the `source` field and its sentence in the description, read the file through `outboundAttachment` (workspace for advanced agents, the easy file view for easy agents, exactly as today), then call the base tool with `content_base64`.
- The OpenCode bridge serves the base tool unchanged. Delete its schema trimming, description rewrite and nil root.

**Tests.** The bridge's `store` tool schema has no `source`. A native store from `source` still works, and an easy agent's `source` outside its folders still fails (keep `TestPageStoreReadsEasyFileView`).

## D2. External MCP caller metadata gets its own field, with a lossless migration

**Problem.** `submitExternalMCPInput` (`cmd/rocketclaw/mcp.go:197-211`) copies the caller's free-form `metadata` into `InboundMessage.Metadata`, the same map RocketClaw uses for internal keys, and deletes only six keys. A caller can therefore set, among others:
- `rocketclaw_agent`: picks the agent a turn runs as (`backend/bridge.go:1068`), bypassing the exposed-agents check (`mcp.go:118`, `:129`). Not yet proven by test; the fix's first test must prove it.
- `rocketclaw_allowed_agents` (`backend/session_tools.go:849`), `rocketclaw_sender_session` / `rocketclaw_sender_action` (fake sender in the Web queue and the prompt), `background_job_ids`, `web_message_id`, `rocketclaw_raw_text`.

**Decision (user-directed; rejected: dropping every `rocketclaw_*` key, extending the delete list).**
- A new field on `protocol.InboundMessage` holds External MCP caller metadata. Caller keys never enter `Metadata`.
- `ROCKETCLAW_METADATA_*` variables (`externalMCPMetadataEnv`, `backend/bridge.go:3645`) are built from the new field. Delete the six-key delete list in `mcp.go` and the skip list in `externalMCPMetadataEnv`.
- Variable names stay identical. `external_conversation_id`, which RocketClaw sets itself (`mcp.go:211`), keeps appearing as `ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID`.
- `rocketclaw_raw_text` no longer leaks into the variables as `ROCKETCLAW_METADATA_ROCKETCLAW_RAW_TEXT` (treated as a fix).

**Migration 079 (user asked for a safe upgrade: no stored message may lose its variables).** For every stored inbound whose `Source` is `external_mcp`, move into the new field exactly the keys today's rule turns into variables: all `Metadata` keys except `rocketclaw_origin`, `rocketclaw_media`, `rocketclaw_principal`, `rocketclaw_principal_name`, `rocketclaw_principal_email` and `source` (and except `rocketclaw_raw_text`, per the fix above). `Metadata` keeps the rest. Columns: `turns.inbound_json`, `shadow_turns.inbound_json`, `conversations.producer_inbound_json` (all `json`, Go default field names); confirm no other stored inbound (for example scheduled messages) during implementation. Caller keys planted in already-queued messages are moved out of `Metadata` too, which closes the hole for pre-upgrade rows. The down migration merges the maps back. SQL uses CTEs only. History entries that already record the variables (`externalMCPStoredMetadataEnv`) are unaffected.

**Tests.** A caller's `rocketclaw_agent` (and the sender keys) cannot change the turn's agent or sender (fails before the fix). Variables are identical for the same caller metadata. A migration test stores old-format External MCP rows, migrates, and checks the variables match.

## D3. The cron scheduler loads definitions only at startup and after a successful reload

**Problem.** `scanScheduled` (`frontend/cron/manager.go:375-378`) reloads all agent and cron definitions from the runtime copy every minute. Normal routes can't make that fail (reload validates a staged copy before replacing the live one, `skel.ReplaceRuntimeAssetsAfterValidation`), but a hand edit inside `.rocketclaw/` stops every cron job and routine, and an invalid shadow logs its warning every minute.

**Decision (user-directed; rejected: falling back to the last good set, leaving it).**
- The manager loads definitions at `Start` and keeps them in memory; the minute ticker fires due schedules from that set without reading files.
- A successful reload refreshes the manager's set, wired like the External MCP agent list (`backend/runtime.go` `Reload`, after `ReplaceRuntimeAssetsAfterValidation`). Startup and reload already validate.
- Arming one-off cron files (timestamp schedules) moves from the minute scan into the refresh, at most once per one-off as today; the timer still re-checks its definition when it fires.
- A hand edit inside `.rocketclaw/` now takes effect at the next reload.

**Tests.** A file change without a reload is not picked up; a reload is. A one-off cron file added by reload fires once. No definitions are read by the minute scan.

## D4. Routines take repeating schedules only

**Context.** Timestamp schedules (`"2026-11-15T09:00:00Z"`) predate this repo (`config/cron.go:180`) and were documented only in the bundled cron skill. A one-off cron file deletes itself after it fires. Routines reuse the cron-file parser, so they inherited timestamps, but a routine is a line in the agent file, which RocketClaw never edits: its only "already ran" mark is its run record, which retention deletes after 90 days (`backend/app.go:29`), after which it fires again.

**Decision (user-directed; rejected: a lasting "fired" table, skipping past-due one-offs, leaving it).**
- A timestamp schedule in `routines` fails loading (startup, reload, `rocketclaw lint`) with a message saying routines repeat and pointing at `rocketclaw_schedule_message` for a single future run.
- Delete the one-off routine code paths that become dead (for example the routine branch of one-off arming and `CronRunRecorded` if nothing else uses it).
- Timestamp cron files, and the Cron page's Run button, are unchanged.
- Docs: fix every place that promises routines' one-off times run once (README routines bullet, `cmd/rocketclaw/CHEATSHEET.md`, the bundled skills `main-create-or-update-agent` and `main-update-cron-or-heartbeat`).

**Tests.** A timestamp routine fails loading with that message; repeating routines are unchanged.

## Verification

`gofmt`, `go vet`, `go test ./internal/rocketclaw/... ./cmd/rocketclaw/...`, `make lint` and `make test` (coverage floor and CLOC budgets; never edit the budgets), run one at a time. Each fix gets a test that fails before it.
