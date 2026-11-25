---
title: OpenCode MCP Bridge Tool Calls Must Run Under Their HTTP Request's Context
date: 2026-11-24
category: docs/solutions/integration-issues/
module: internal/rocketclaw/backend
problem_type: integration_issue
component: service_layer
symptoms:
  - "A waiting ask_secret call through the OpenCode MCP bridge was not withdrawn when the client closed its tools/call request on a pre-2026-07-28 MCP protocol."
  - "A pending ask keeps the Web's secret form up for a call nobody is waiting on any more."
root_cause: wrong_api
resolution_type: code_fix
severity: medium
retire_when: "go-sdk's StreamableHTTPOptions.PropagateRequestCancellation applies to every protocol version the handler accepts, or OpenCode can no longer speak a pre-2026-07-28 protocol; check that option's doc comment in mcp/streamable.go at the go-sdk version in go.mod, and negotiation() in packages/core/src/mcp/client.ts at the OpenCode tag in internal/rocketclaw/oc/manifest.json"
tags:
  - opencode
  - mcp-bridge
  - mcp
  - go-sdk
  - cancellation
  - turn-secrets
  - ask-secret
---

# OpenCode MCP Bridge Tool Calls Must Run Under Their HTTP Request's Context

## Problem

The OpenCode MCP bridge (`Runtime.OpenCodeMCP`, `internal/rocketclaw/backend/opencode_mcp.go`) serves `ask_secret`, a tool call that blocks until a person answers in the Web or the call's context ends (`askSecretTool`, `internal/rocketclaw/backend/web_secret.go`). When OpenCode gives up on such a call, the call must end and its pending ask must be withdrawn. On the older MCP protocol revisions, the context the go-sdk hands a tool handler does not end when the client closes the request, so the ask could outlive the call.

## Symptoms

- Per this work's conclusion, with the bridge's previous shape a `tools/call` that the client canceled on protocol `2025-11-25` left the `ask_secret` request pending instead of withdrawing it.
- A pending ask keeps the secret form on screen in the Web, asking a person for a value no running call will use.

## What Didn't Work

- **Trusting `PropagateRequestCancellation: true`.** The bridge already set it, and it reads as "cancel the handler when the HTTP request ends". The go-sdk v1.8.0 doc comment on `StreamableHTTPOptions.PropagateRequestCancellation` (the module's `mcp/streamable.go`) limits it: it "only applies to requests using the >= 2026-07-28 protocol", and "requests using older protocol versions ... are unaffected". The stateless handler computes `shouldPropagateCancellation: info.usesNewProtocol && (info.isSubscriptionsListen || h.opts.PropagateRequestCancellation)`, and `usesNewProtocol` is true only when the request's protocol version is at least `2026-07-28` (`ephemeralConnectOpts`).
- **One shared `*mcp.Server` per OpenCode server, built at startup, using the tool handler's `ctx`.** That was the bridge's shape before this change. A shared server's handler cannot see which HTTP request it is serving, so on an older protocol nothing tied the call to the request.
- **Assuming OpenCode always speaks the older protocol.** The planning research said OpenCode's default MCP protocol is `legacy`. OpenCode v2.4.1 (the tag in `internal/rocketclaw/oc/manifest.json`) defaults a remote server to `auto`: `connect` in OpenCode's `packages/core/src/mcp/client.ts` (`github.com/anomalyco/opencode`) sets `config.protocol ?? (config.type === "attached" ? "legacy" : "auto")`, and `auto` probes for `2026-07-28` and falls back to `legacy` when the server does not support it. `addOpenCodeBridge` sends no `protocol` field, so the bridge gets `auto`. The negotiated revision therefore depends on both sides' versions and on any `protocol` a person sets, which is why the fix must not depend on it.

## Solution

Build the MCP server per HTTP request and run each tool call under that request's context. The go-sdk calls the `getServer` function of `mcp.NewStreamableHTTPHandler` for every request in stateless mode (`serveStateless`), so the closure can capture the request:

```go
handler := mcp.NewStreamableHTTPHandler(func(call *http.Request) *mcp.Server {
	name := call.PathValue("name")
	mcpServer := mcp.NewServer(...)
	for _, tool := range r.openCodeTools(name, servers[name], "", "", nil) {
		mcpServer.AddTool(..., func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := r.callOpenCodeTool(call.Context(), name, servers[name], tool.Name, request)
			...
		})
	}
	return mcpServer
}, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: 16 << 20})
```

The handler's own `context.Context` is deliberately ignored (`_`). `call.Context()` ends when the client closes the request on any protocol revision, `askSecretTool`'s wait sees `ctx.Done()`, and the ask is withdrawn.

## Why This Works

In the stateless handler a `tools/call` lives exactly as long as its HTTP POST, on every protocol revision. The HTTP request context is the one signal that is correct for all of them, while the handler context is tied to the request only on `2026-07-28` and later. Building the server per request costs a server and a tool list per call, which is cheap next to a call that can wait minutes for a person.

## Prevention

- `TestOpenCodeMCPTurnSecrets` (`internal/rocketclaw/backend/opencode_mcp_test.go`) connects with `ProtocolVersion: "2025-11-25"`, starts an `ask_secret` call, cancels the call's context, and requires the pending ask count to drop to zero. The pinned version is what makes this test cover the older protocol: without it, `Client.Connect` asks for the latest revision, `2026-07-28`, `PropagateRequestCancellation` applies, and a return to the shared-server shape would most likely pass. Keep the pin, or add the same cancellation check for both revisions.
- When a tool handler on any go-sdk streamable server must stop on client disconnect, use the HTTP request's context, not the handler's, unless every client is known to negotiate `2026-07-28` or later.
- When upgrading go-sdk or OpenCode, re-read `PropagateRequestCancellation`'s doc comment and OpenCode's `negotiation()`; the `retire_when` above names the change that would make the per-request server unnecessary.

## Related Issues

- `docs/solutions/integration-issues/opencode-sessions-lacked-rocketclaw-tools-until-rocketclaw-added-the-bridge.md`: how RocketClaw registers this bridge with OpenCode, as a remote server with no `protocol` field.
- `docs/verification/turn-secrets/README.md`: the TLC model of turn secrets, including when an ask is withdrawn.
- `docs/plans/2026-11-24-1556-feat-turn-secrets-plan.md`: listed this cancellation as an open question for its MCP unit.
