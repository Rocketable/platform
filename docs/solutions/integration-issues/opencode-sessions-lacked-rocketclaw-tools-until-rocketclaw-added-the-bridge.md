---
title: OpenCode Sessions Lacked RocketClaw's Tools Until RocketClaw Added the MCP Bridge Itself
date: 2026-11-17
category: docs/solutions/integration-issues/
module: internal/rocketclaw/backend
problem_type: integration_issue
component: service_layer
symptoms:
  - "Agents in Remote OpenCode Sessions opened through RocketClaw had no get_tags, toggle_tag, store, read, delete, list, kv_store or kv_read tools."
  - "opencode mcp list on the coding server showed no rocketclaw server, although the bridge answered tools/list and get_tags when called directly."
root_cause: missing_workflow_step
resolution_type: code_fix
severity: medium
related_components:
  - frontend
retire_when: "OpenCode removes or renames PUT /api/experimental/mcp/{server}, or adds an MCP field to session.create; check packages/protocol/src/groups/mcp.ts and packages/cli/src/acp/sessions.ts at the OpenCode tag named in internal/rocketclaw/oc/manifest.json"
tags:
  - opencode
  - remote-opencode-session
  - mcp-bridge
  - mcp
  - acp
  - hosted-pages
  - tags
---

# OpenCode Sessions Lacked RocketClaw's Tools Until RocketClaw Added the MCP Bridge Itself

## Problem

The MCP bridge at `/opencode/{name}/mcp` gives agents in Remote OpenCode Sessions RocketClaw's tag and Hosted Page tools. Nothing told OpenCode about it: the bridge existed for a session only if a person had added it to the coding server's own OpenCode config, so on a server set up without that step the tools never appeared.

## Symptoms

- An OpenCode session opened from the Web had none of the bridge's tools.
- `opencode mcp list` on the coding server listed only the servers in its own config.
- Calling the bridge directly with the server's Basic credentials worked: the MCP `tools/list` method returned all eight tools, and `get_tags` with `_meta["ai.opencode/sessionID"]` set to the session returned its tags. The server side was fine.

## What Didn't Work

Paths under `packages/` in this doc are in OpenCode's own repository (`github.com/anomalyco/opencode`) at tag v2.3.0, not in this repository.

- **Treating it as a setup gap.** The first diagnosis was "add the bridge to OpenCode's config by hand", as the original bridge plan's R10 required (`docs/plans/2026-10-24-0852-feat-coding-server-mcp-bridge-plan.md`). That plan had chosen startup config over OpenCode's runtime MCP API because runtime additions are per folder and lost when OpenCode restarts. Both facts are true, but they do not rule the API out: OpenCode's own ACP agent handles `session/new`'s `mcpServers` with exactly this API, adding each server to the attached session's folder and adding it again whenever a session is attached afresh (`packages/cli/src/acp/sessions.ts:104` at OpenCode v2.3.0).
- **Reading the folder from RocketClaw's stored copy.** `conversations.directory` lags OpenCode until the next sync, and a session can move to another folder. The relay asks OpenCode instead (`GET /api/session/<id>`, `location.directory`).
- **Reading the address from `SessionService.definitions` in the sync loop.** In production `Runtime.Cfg` is that same object (`internal/rocketclaw/backend/app.go:233`), but `TestWebRPCSyncsOpenCodeWithoutTailnetLogin` builds the two from different configs, and `SyncOpenCode`'s contract is the runtime config passed to it. The loop keeps taking `rt.Cfg`.

## Solution

RocketClaw adds the bridge itself through `PUT /api/experimental/mcp/rocketclaw?location[directory]=<folder>`, with a remote config whose URL is `<Web.Tsnet.Origin>/opencode/<name>/mcp`, `oauth: false`, and a Basic `Authorization` header built from the server's password (`addOpenCodeBridge`, `internal/rocketclaw/backend/opencode_mcp.go:116`). Two triggers call it:

- **Before each prompt from the Web.** The relay's prompt branch calls `AddOpenCodeSessionBridge` before forwarding `POST /api/session/<id>/prompt` (`internal/rocketclaw/frontend/rpc/relay.go:52`, `internal/rocketclaw/backend/opencode_mcp.go:134`). It looks the session's folder up in OpenCode and adds the bridge there. A failure is ignored, so the prompt is still forwarded.
- **On each sync.** After a server's sync succeeds, `addOpenCodeBridges` lists the folders OpenCode has open (`/api/debug/location`) and adds the bridge to each whose `GET /api/mcp` list lacks `rocketclaw` (`internal/rocketclaw/backend/opencode_mcp.go:148`, called from `internal/rocketclaw/backend/opencode.go:194`). This covers sessions driven from OpenCode's own terminal, turns OpenCode starts on its own, and folders reopened after an OpenCode restart. It is skipped while `Web.Tsnet.Origin` is still empty, because the tailnet sets it only once it serves (`cmd/rocketclaw/tailnet.go:173`).

## Why This Works

Facts verified against OpenCode v2.3.0's source and a live 2.3.0 server:

- **Scope is a folder, not a session.** `MCP.add` stores the server in an in-memory `overrides` map of the folder's MCP service (`packages/core/src/mcp/index.ts:923`), which lives as long as OpenCode keeps that folder open. `session.create` has no MCP field.
- **The folder scope still identifies the caller.** Every MCP tool call carries the calling session in `_meta["ai.opencode/sessionID"]` (`packages/core/src/tool/mcp.ts:75` → `packages/core/src/mcp/client.ts:718`), which is what the bridge already keys on.
- **Adding the same config again is free of reconnects.** Reconcile skips a server whose config is deep-equal (`packages/core/src/mcp/index.ts:851`). Live, the first add logged one `mcp connected server=rocketclaw tools=8`; three repeat adds logged nothing. Each add still runs OpenCode's MCP state reload, which is why the 5-second sync checks `GET /api/mcp` first while the per-prompt path just adds.
- **Adding before forwarding gives that same turn the tools.** A turn's context selection flushes MCP tools first (`packages/core/src/session/context.ts:142`), waiting up to 2 seconds for servers still connecting and refreshing the tool registry (`packages/core/src/tool/mcp.ts:143-150`).

## Prevention

- `TestOpenCodeProxy` (`internal/rocketclaw/frontend/rpc/relay_test.go`) asserts the call order for a Web prompt: session lookup, then the bridge `PUT` to that session's folder, then the prompt, with the exact bridge body.
- `TestAddOpenCodeBridgesReachesOpenFoldersWithoutIt` (`internal/rocketclaw/backend/opencode_test.go:256`) asserts the sync adds the bridge only to open folders that lack it. Dropping the "already listed" check makes it fail.
- When OpenCode moves past the tag in `internal/rocketclaw/oc/manifest.json`, re-check the four facts above in the new tag's source; the API is marked experimental.
- Nothing logs a folder that still lacks the bridge after a sync. If the tools go missing again, `GET /api/mcp?location[directory]=<folder>` on the coding server is the first thing to read.

## Related Issues

- `docs/solutions/architecture-patterns/web-acts-on-remote-opencode-sessions-through-the-api-proxy.md`: the same relay, and the same rule of acting on OpenCode through its own HTTP API.
- `docs/solutions/logic-errors/opencode-background-turns-never-ended-park-until-done.md`: the same sync loop, and why turns OpenCode starts on its own reach no relay.
- `docs/plans/2026-10-24-0852-feat-coding-server-mcp-bridge-plan.md`: the original bridge plan; its R10 is marked superseded by the fix in PR #1245.
