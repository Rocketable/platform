---
title: Outside MCP Servers See Only Their Own Turn Because Each Execute Call Starts Them
date: 2026-11-20
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/mcpclient
problem_type: architecture_pattern
component: execute
severity: high
applies_when:
  - "Making stdio MCP servers outlive one execute call, pooling them, or sharing them between sessions"
  - "Changing how mcpclient.New merges config env and headers with the turn's ROCKETCLAW_* values"
  - "Adding a new per-turn value that outside MCP servers must see"
  - "Writing an MCP server that scopes writes, such as replies to one support case, from ROCKETCLAW_* values"
related_components:
  - internal/rocketclaw/harness
  - internal/rocketclaw/backend
tags: [mcp-servers, mcpclient, stdio, session-env, case-scoping, external-mcp, headers, isolation]
---

# Outside MCP Servers See Only Their Own Turn Because Each Execute Call Starts Them

## Context

Customer-support agents are moving from bash scripts (run as agent commands) to stdio MCP servers. The scripts' main safety rule is case scoping: a write, such as an Intercom reply, may only go to the current case, read from `ROCKETCLAW_METADATA_INTERCOM_CONVERSATION_ID`. The scripts could trust that value because agent commands run on the host with an environment RocketClaw builds. A shim's environment is `h.env` (`internal/rocketclaw/harness/emulated.go:101`), not anything the agent exports inside the sandbox.

MCP servers used to get only a fixed allowlist plus their config `env`, so they could not scope anything. Now `mcpclient.New` (`internal/rocketclaw/mcpclient/registry.go:43`) takes the turn's shell environment, the same `harness.Config.ShellEnv` map the shell gets (`internal/rocketclaw/harness/rocketcode.go:302`):

- a `command` (stdio) server gets each key as an environment variable;
- a `url` server gets each key as a header, `ROCKETCLAW_METADATA_TICKET` becoming `X-Rocketclaw-Metadata-Ticket`.

## Guidance

**A stdio server's environment is fixed when its process starts.** These values are only correct because one server process never serves more than one `execute` call:

- the backend builds a new runtime config, and so a new registry, for every turn (`internal/rocketclaw/backend/bridge.go:2337`);
- `callExecute` opens a registry session for the call and closes it when the call ends (`internal/rocketclaw/harness/mcp_tools.go:313-315`). Closing the session stops the stdio processes.

Do not pool or reuse stdio servers across `execute` calls, turns or sessions. Starting every visible server on every call costs time, and that makes sharing look like an easy speedup. It would quietly hand one conversation's case ID to another, with no error. `TestStdioServerSeesOnlyItsOwnSessionEnv` (`internal/rocketclaw/mcpclient/registry_test.go`) fails if a server is shared inside `mcpclient`. It cannot see a registry kept alive across turns one layer up in the harness or backend.

If long-lived servers are ever needed, move the values into the request instead: MCP's per-call `_meta` field (`CallToolParams.Meta` in the Go SDK). The agent cannot set it, because Code Mode only controls tool arguments. Every server then has to read the case from each request and never keep it, which is a new duty for every server author. That is why this change did not take that route.

**Config cannot set these values.** `New` drops every `ROCKETCLAW_*` key from a server's `env` and every `X-Rocketclaw-*` header from its `headers` (`registry.go:48`, `registry.go:55`), then writes the turn's values. For `env`, writing them last already makes them win. For `headers` it does not: a config key can differ in case from the turn's (`x-rocketclaw-…` against `X-ROCKETCLAW-…`), the map keeps both, and the request sets them in random order, so the drop is what makes the turn's value win. The drop also stops config from adding a value the turn does not have, such as a fake case ID in a plain Web session.

This looks like the pattern `docs/solutions/security-issues/external-mcp-caller-metadata-shared-the-routing-map.md` warns about: "If you catch yourself writing `delete(m, someKey)` after copying input into a shared map, the design is wrong." It is not, for two reasons. First, the prefix is the whole namespace of session values, not a list of chosen keys. Every key the backend puts in the shell environment starts with `ROCKETCLAW_` (`bridge.go:65-66`, `externalMCPMetadataEnv` at `bridge.go:3647-3665`, the stored-metadata filter at `bridge.go:3687`, `bridge.go:2720-2723`, `bridge.go:2738`). A new session value must keep that prefix, or config could override it. Second, a process environment and a header set are each one flat namespace, so the earlier doc's fix of giving untrusted input its own field is not available here.

**Metadata values are untrusted.** A value holding a line break cannot go in a header. Go's HTTP client refuses it, so calls to that `url` server fail instead of carrying an injected header (the `session value with a newline` case in `TestSessionErrors`, `registry_test.go:156`). Stdio servers get the value unchanged.

## Why This Matters

The variable tells the server which case it is on; the server's own code still has to refuse writes to other cases. If the variable can come from the wrong conversation, that check passes for the wrong case. Nothing errors, and the approver, a second check, is all that is left.

## When to Apply

- Before changing server lifetime in `mcpclient`, `callExecute` or how the backend builds the per-turn config.
- When a backend change adds a value to the shell environment: it reaches MCP servers too, and must start with `ROCKETCLAW_`.
- When writing a server that scopes by these values: read them per process, which today means per `execute` call.

## Examples

What a stdio server sees in an External MCP session whose metadata is `{"intercom_conversation_id": "123"}`, when its config sets a fake value:

```json
"mcp_servers": {
  "support": {
    "command": "support-mcp",
    "env": {"SUPPORT_TOKEN": "...", "ROCKETCLAW_METADATA_INTERCOM_CONVERSATION_ID": "999"}
  }
}
```

Besides the allowlisted variables such as `PATH` and `HOME`, the process gets `SUPPORT_TOKEN`, `ROCKETCLAW_CONVERSATION_ID` and `ROCKETCLAW_METADATA_INTERCOM_CONVERSATION_ID=123`. The configured `999` is dropped. In a plain Web session with no metadata, it gets no `ROCKETCLAW_METADATA_*` variable at all.

## Related

- `docs/solutions/security-issues/external-mcp-caller-metadata-shared-the-routing-map.md`: why External MCP metadata is untrusted and has its own field.
- `docs/solutions/logic-errors/external-mcp-thread-lost-metadata-after-copy-free-hidden-runs.md`: which turns get the session's values. Outside MCP servers are now another place those values reach.
- `docs/solutions/runtime-errors/execute-uncapped-results-exceeded-model-context.md`: a background `execute` can outlive its turn; its servers keep the values they started with, which are that turn's.
- `CONCEPTS.md`: External MCP Metadata.
