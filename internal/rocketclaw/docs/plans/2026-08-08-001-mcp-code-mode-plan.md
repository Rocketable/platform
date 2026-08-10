---
title: "MCP code mode"
type: feature
date: 2026-08-08
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# MCP code mode

## Goal Capsule

- **Objective:** Let RocketClaw agents use outbound MCP tools only through Starlark code mode: two model tools (`mcp_tool_definitions`, `mcp_run`) that discover and run short scripts which call MCP as `server_toolname(...)`.
- **Authority:** This plan > prior inbound-only MCP docs for agent-facing tools. Inbound `session_prompt` / `mcp_external` stay unchanged.
- **Stop when:** Configured MCP servers work over stdio and streamable HTTP (no auth); agents grant `permission.mcp` with `server.tool` / wildcard subjects; model never sees per-MCP tools; scripts return a string; `mcp_tool_definitions`/`mcp_run` omit when nothing is mcp-allowed; CHEATSHEET, example config, and agent-authoring skill document `permission.mcp` and the two tools; rocketclaw CLOC stays under budget.
- **Out of scope for stop:** OAuth/login flows; exposing MCP tools as first-class RocketCode tools; workflow DSL product changes; code mode on workflow workers. Static stdio `env` and HTTP `headers` are in scope.

---

## Product Contract

### Summary

Agents connect to remote/local MCP servers defined in `rocketclaw.json`, but the model does **not** get one tool per MCP function. Instead it gets a code-mode pair: list available MCP tool schemas, then write a small Starlark script that chains `server_toolname(...)` calls and returns a string. Access is gated by `permission.mcp` with subjects like `github.create_issue` or `github.*`.

### Problem Frame

MCP tool catalogs are large and noisy in the main tool list. Chaining many MCP calls through the model tool loop wastes context and turns. Code mode keeps the model tool surface fixed at two tools while still giving full MCP power inside a short script. RocketClaw today only hosts inbound MCP (`session_prompt`); there is no outbound client or agent MCP config.

**v1 product choice (affirmed):** ship Starlark code mode first—not native per-MCP tools and not a non-script `mcp_call`. One-shot calls pay an extra hop (definitions then run, or a known script); multi-call chaining in one `mcp_run` is the intended win. Thinner paths stay rejected for v1.

### Requirements

- R1. Global MCP server definitions live in `rocketclaw.json` under a key distinct from inbound `mcp_external` (use **`mcp_servers`**).
- R2. Each server supports **stdio** (`command`, optional `args`, optional `env`) or **streamable HTTP** (`url`, optional `headers` map[string]string). Exactly one transport per server. No OAuth/login flows in v1; operators may put static secrets in stdio `env` or HTTP `headers` (e.g. `Authorization: Bearer …`).
- R3. Agent access uses permission bucket **`mcp`**. Subjects are **`server.tool`** flat strings (tool = MCP tool name). Wildcards work via existing glob rules (`github.*`, `*`, later-rule-wins). Actions: **allow / deny / auto / auto(reviewer)** with the **same semantics as `bash` and other tools**: each MCP invocation (each Starlark builtin call) is evaluated independently; on `auto`/`auto(reviewer)`, capture the request, run the existing permission analyzer/reviewer, and gate before `CallTool`—then resume or fail the Starlark call. Not a special mcp-only policy.
- R4. Model-facing tools are only:
  - `mcp_tool_definitions` — discover allowed MCP tools (permission-filtered). **Tool parameter schema advertises configured server names the agent can use** (not the full per-tool catalog in the schema). Caller may request **all** tools or a **subset** via optional query args (server filter + string match on tool name/description). Result string includes server, mcp name, starlark name, description, input schema for matches.
  - `mcp_run` — argument is Starlark source; returns a **string** result (or tool error).
- R5. Inside `mcp_run`, each allowed MCP tool is a builtin `server_toolname(...)` where `server` is the config name and `toolname` is a Starlark-safe form of the MCP tool name (see KTD naming). Kwargs/dict args → JSON object; **validate against that tool’s MCP input JSON schema before CallTool**; mismatch → Starlark fail (do not call MCP).
- R6. Omit **both** mcp tools when the agent has **no actionable mcp allow/auto rule whose server name exists in `mcp_servers`**. Do **not** require ListTools at tool-assembly time. Connect + list happens on first `mcp_tool_definitions` / `mcp_run` call (lazy).
- R7. Do **not** register individual MCP tools on the RocketCode tool list.
- R8. Inbound external MCP (`mcp_external`, `session_prompt`, dual sessions) is untouched.
- R9. Code mode is ordinary CustomTools: not RocketClaw auto-allowed; not injected into workflow workers (those paths deliberately omit RocketClaw custom tools). Inject on the same agent looper assembly paths that already take CustomTools (persistent turns and cron/raw).
- R10. Docs: CHEATSHEET permission buckets + tools table; example `rocketclaw.json`; agent-authoring skill mention of `permission.mcp`.

### Actors

- A1. Operator configuring `mcp_servers` in `rocketclaw.json`
- A2. Agent author writing `permission.mcp` in agent markdown
- A3. Running agent calling `mcp_tool_definitions` / `mcp_run` during a turn
- A4. External MCP server process or HTTP endpoint

### Key Flows

- F1. Discover then run  
  - **Trigger:** Agent with `mcp: "demo.*": allow` and a configured `demo` server.  
  - **Outcome:** Sees both mcp tools; `mcp_tool_definitions` schema lists server `demo`; unfiltered or matched query returns demo tools; script calling `demo_echo(message="hi")` returns string output.
- F1b. Subset query  
  - **Trigger:** Agent allowed tools on `github` and `acme`; calls `mcp_tool_definitions` with server=`github` and/or a string match like `issue`.  
  - **Outcome:** Result lists only matching github tools (e.g. create_issue), not the full multi-server catalog.
- F2. Fine-grained deny  
  - **Trigger:** `mcp: "demo.*": allow` then `mcp: "demo.danger": deny`.  
  - **Outcome:** Definitions omit `danger`; script call to denied tool fails; other demo tools work.
- F3. No grants  
  - **Trigger:** Agent with no mcp allow rules (or only denies).  
  - **Outcome:** Neither `mcp_tool_definitions` nor `mcp_run` appears.
- F4. Stdio + HTTP  
  - **Trigger:** One stdio server and one HTTP server configured; agent allows both.  
  - **Outcome:** Definitions and run work for tools from each transport.

### Acceptance Examples

- AE1. Covers R1–R5, F1. Minimal stdio echo MCP + agent grant → definitions non-empty → `mcp_run` script returns tool text.
- AE2. Covers R3, R6, F2–F3. Wildcard allow + one deny; no grants omits tools.
- AE3. Covers R4, R7. Model tool list contains at most `mcp_tool_definitions` and `mcp_run` from this feature—never raw MCP tool names.
- AE4. Covers R2, F4. HTTP streamable client path (stateless-friendly) works in tests with in-process or local test server.
- AE5. Covers R8–R9. Workflow worker CustomTools still lack mcp tools; inbound MCP tests unchanged.

### Scope Boundaries

**In scope**

- Outbound MCP client lifecycle (connect, list tools, call tool, close)
- Config + validation for `mcp_servers`
- Starlark code-mode runner (separate from workflow DSL)
- Two CustomTools + permission gating
- Docs/example/tests within CLOC budget

**Out of scope**

- OAuth / interactive login / token refresh flows (static `headers` / stdio `env` only)
- Resources/prompts/sampling/elicitation as first-class code-mode features (tools only in v1)
- Promoting MCP tools into main tool list
- Changing workflow Starlark builtins or human `$workflow`
- Per-agent server definitions in agent frontmatter
- Reload of `rocketclaw.json` without process restart (same as other config)

### Deferred to Follow-Up Work

- OAuth and rotating credential plumbing for remote MCP
- MCP resources/prompts in code mode
- Optional code mode inside workflow workers
- Caching/list refresh policy beyond simple process-lifetime cache
- Rich structured return types beyond string (today: string only)

### Product Contract preservation

Product Contract created in this bootstrap (no separate brainstorm). Settled session decisions carried as requirements: global config + agent allow; `server_toolname(...)`; treat as normal tools with permission omit; bucket `permission.mcp` with flat subjects `server.tool` / wildcards (raw MCP tool name); stdio+HTTP no auth.

Canonical agent YAML shape:

```yaml
permission:
  mcp:
    "github.*": allow
    "github.delete_repo": deny
    "acme.search": allow
```

---

## Planning Contract

### Assumptions

- RocketCode permission buckets are open-ended; bucket name `mcp` needs no engine change.
- Subjects are single flat strings; `server.tool` and patterns like `server.*` work because `.` is literal and `*` is a glob (existing `permissionWildcardMatch`).
- SDK already vendored: `github.com/modelcontextprotocol/go-sdk v1.7.0` — `CommandTransport`, `StreamableClientTransport`, `Client.Connect`, `ListTools`, `CallTool`.
- rocketclaw source CLOC budget is raised by **+1500** for this feature (human-approved): failure threshold **19000** (was 17500). Hazard zone scales with Makefile convention. Tests still do not count toward source budget. Implementer updates `SOURCE_CLOC_BUDGET` in `internal/rocketclaw/Makefile` as part of U1/U3—this is an explicit exception to the standing “never edit budget” rule.
- Config changes require daemon restart (not `rocketclaw_reload`).

### Key Technical Decisions

- KTD1. **Config key `mcp_servers`.** Map of name → server spec. Never overload `mcp_external`. Server name: `^[a-z][a-z0-9_]*$` (Starlark-friendly prefix).
- KTD2. **Transport discriminant:** stdio if `command` set; HTTP if `url` set; reject both or neither. Stdio: `command` string, `args` []string, optional `env` map[string]string, optional `cwd` (relative to workspace or absolute under policy—default cwd = workspace root; if set, resolve against workspace). **Child env:** full parent env, then server `env` overrides. Direct exec, no shell. HTTP: `url` string (must parse as http/https; **no private-IP/SSRF filter**; operator-configured URL is intentional); optional `headers` map on the client (static only—no OAuth). Use `StreamableClientTransport{Endpoint, DisableStandaloneSSE: true}` + headers. Never log header or env values.
- KTD3. **Packages:**  
  - `internal/rocketclaw/mcpclient` — registry, connect, list/call, close; **not** `externalmcp`.  
  - `internal/rocketclaw/codemode` — Starlark run + definitions text + name mangling; **not** workflow package.  
  - `harnessbridge` — thin CustomTool wiring only (mirror `dynamic_workflow_tool.go`).
- KTD4. **Connect per `mcp_run` / `mcp_tool_definitions` call, then close.** No process-lifetime sticky MCP sessions across agents or turns. Within a single tool Call, may hold connections open for the duration of that Call (definitions list or one script’s many builtin calls), then Close all servers touched before returning. Optional short-lived list cache **inside one Call only** is fine; do not cache live sessions across Calls. Avoids cross-agent session bleed.
- KTD5. **Permission subjects `server.<mcpToolName>`** using the **raw MCP tool name** (not the Starlark mangled form). **Tool presence (assembly):** show both mcp tools iff there is at least one mcp rule evaluating to allow/auto for some pattern that can only match a **configured** server name (unknown server names never unlock tools). **After ListTools (inside Call):** definitions and builtins = live catalog ∩ permission (allow/auto; deny wins). **Partial failure:** per-server connect/list; include tools from servers that succeed; skip failed servers without failing the whole `mcp_tool_definitions` call; briefly note each failed server+error in the result text. If **every** attempted server fails → tool error. `mcp_run` builtins for a down server fail that builtin call only.
- KTD6. **Per-call permission like bash:** each `server_toolname` invocation must run the **same permission decision path** the looper uses for tools (Evaluate `mcp` + `server.<rawTool>`; on allow proceed; on deny Starlark-fail; on auto/auto(reviewer) invoke the existing reviewer/analyzer with a request describing server, tool, and args, then proceed or fail from that decision). Inject a `PermissionDecide` (or equivalent) callback into `codemode.Run` from harnessbridge—do **not** reimplement a weaker Evaluate-only gate. `mcp_run` / `mcp_tool_definitions` CustomTools: `Permission: "mcp"`; **VisibilitySubjects and Subjects** = currently allowed `server.tool` list (never default to the tool name). Entry-time Subjects check is coarse visibility only; **authoritative enforcement is per builtin call**. Do not put mcp tools on RocketClaw auto-allow list.
- KTD7. **Starlark naming:** builtin name = `server + "_" + sanitize(mcpToolName)` where sanitize maps non `[A-Za-z0-9_]` to `_` and prefixes `_` if needed so the identifier is valid. Collisions after sanitize → config/load error or deterministic suffix; fail closed at registry build/list time with a clear error rather than silent overwrite.
- KTD8. **Script contract:** require `def main():` returning a value; final tool output is always a string (`string` as-is; other values JSON-encoded; **error if main returns None**; empty success is `return ""`).
- KTD9. **Isolation (minimum only):** no `load`; predeclared universe = allowed MCP builtins only (no workflow builtins, no extra modules); **Starlark step budget + context cancel only** (no separate max-CallTool-count or max-result-bytes product limits in v1—permission + turn cancel are the blast-radius controls); print banned or no-op; no recursion. Do **not** share types/APIs with the workflow package. Do **not** port every workflow FileOption—only what this list requires plus options needed for `def main`.
- KTD10. **`mcp_tool_definitions` discovery shape:**
  - **Parameters (schema):** optional `server` (string; must be one of the agent-visible configured server names—enumerate allowed server names in the parameter description or enum when the set is small/stable); optional `match` (string; case-insensitive substring over MCP tool name, starlark name, and description). Omit both → all allowed tools across visible servers. Invalid `server` → tool error.
  - **Schema does not embed the full tool catalog** (avoids huge tool definitions); it **does** surface which **servers** exist for this agent.
  - **Result:** one format only — stable readable **plain text** listing each matching allowed tool: server, mcp name, starlark name, description, input schema. Empty match → clear empty message, not an error.
- KTD13. **MCP content trust:** pass tool descriptions and CallTool text/structured results through to the model as ordinary tool output. No injection wrappers or scrubbers. `IsError` → fail the Starlark builtin (not success string). Operator-chosen MCP servers are outside the trust boundary.
- KTD14. **Input schema validation:** before each CallTool, validate args against the tool’s cached input JSON Schema (required/types as schema says). Prefer existing Go JSON-schema helper already in module graph (`google/jsonschema-go` or SDK path)—smallest correct check. Fail closed on invalid args.
- KTD11. **CLOC:** budget +1500 (see Assumptions). Still prefer thin packages over frameworks; no rocketcode changes unless unavoidable (should be zero).
- KTD12. **Injection sites:** `Bridge.runTurn` customTools assembly and cron/raw CustomTools assembly (same class as attach/dynamic workflow). Not `newWorkflowAgentRunner`.

### High-Level Technical Design

```text
rocketclaw.json
  mcp_servers:
    github: { command, args, env } | { url }
        │
        ▼
mcpclient.Registry  (app start; closed on shutdown)
        │
        ├─ ListTools(server)  // lazy connect + cache
        └─ CallTool(ctx, server, tool, args)
                │
harnessbridge.maybeCodeModeTools(agent.Permission, registry)
        │
        ├─ mcp_tool_definitions  → codemode.DefinitionsText(...)
        └─ mcp_run(code)         → codemode.Run(ctx, code, env)
                                              │
                                              ├─ builtins: github_create_issue(**kwargs) → registry.CallTool
                                              └─ each call: Evaluate("mcp", "github.create_issue")
```

Model tool list stays small. MCP catalog lives inside definitions + script builtins only.

### Patterns to Follow

- Custom tool omit/gate structure: `internal/rocketclaw/harnessbridge/dynamic_workflow_tool.go` (structure only — mcp must use allow|auto, not workflow’s allow-only filter)
- Permission evaluate/wildcards: `internal/rocketcode/permission.go`
- Config load/validate/example: `internal/rocketclaw/config/config.go`, `rocketclaw.example.json`
- Starlark isolation patterns: `internal/rocketclaw/workflow/engine.go` (copy, don’t extend)
- SDK client connect (tests): `internal/rocketclaw/externalmcp/server_test.go`
- SDK transports: `vendor/.../mcp/cmd.go` (`CommandTransport`), `StreamableClientTransport`

### Sequencing

U1 mcpclient + config → U2 codemode Starlark → U3 harnessbridge tools + wiring → U4 docs. Tests ride with each unit. CLOC check after U3.

### Risks

| Risk | Mitigation |
|------|------------|
| CLOC overrun | Thin packages; no generic plugin framework; skip resources/prompts/auth |
| Stdio process leaks | Close sessions at end of every mcp_* Call; app shutdown still drains |
| Name collisions after sanitize | Fail at list time with explicit error |
| Huge tool catalogs in definitions | Still return full allowed set in v1; follow-up can add filters |
| HTTP 401/403 | Surface clear tool error; do not log response bodies that may contain tokens |
| Model writes huge scripts / many MCP calls | Step budget + ctx cancel only (v1); keep script API tiny; no separate CallTool count cap |

---

## Implementation Units

### U1. Config + outbound MCP client registry

**Goal:** Load `mcp_servers` from config and call ListTools/CallTool over stdio and HTTP without auth.

**Requirements:** R1, R2, R8 (no inbound breakage)

**Dependencies:** none

**Files:**

- `internal/rocketclaw/config/config.go`
- `internal/rocketclaw/config/config_test.go`
- `internal/rocketclaw/rocketclaw.example.json`
- `internal/rocketclaw/mcpclient/` (new: registry + tests)
- `internal/rocketclaw/app/app.go` (construct registry at start, close on shutdown—minimal hook)

**Approach:**

- Add `MCPServers map[string]MCPServerConfig` with transport validation.
- Registry holds server specs only. Each `mcp_*` Call: connect needed servers, list/call, **Close before return**. No cross-Call session reuse.
- Collect full tool catalogs via paginated list (`session.Tools` or NextCursor loop)—never a single unpaginated page.
- CallTool: args map → SDK `CallToolParams`. Result rules: **`IsError` true → error from registry** (include concatenated text); success text parts concatenated; success with no text but StructuredContent → JSON string; success with neither → empty string.
- Tests: invalid config matrix; stdio against a tiny test MCP server binary or in-process if feasible; HTTP against `externalmcp`-style local streamable server or SDK in-memory if sufficient for client path.

**Test scenarios:**

- Reject server with both command and url; neither; bad name.
- Example config still loads (empty or sample mcp_servers).
- CallTool stdio happy path returns tool text.
- CallTool HTTP happy path returns tool text.
- CallTool `IsError` surfaces as error (not success string).
- Structured-only success result becomes JSON string.
- Unknown server name errors clearly.
- Close is idempotent / safe.

### U2. Starlark code-mode runner

**Goal:** Run `def main()` scripts with MCP builtins and permission checks; produce definitions text.

**Requirements:** R3–R5

**Dependencies:** U1

**Files:**

- `internal/rocketclaw/codemode/` (new: run, definitions, naming, tests)

**Approach:**

- Input: ctx, source string, permission set, list of allowed tool descriptors (server, mcpName, schema, description), call func.
- Build builtins map from allowed tools only.
- `Run` compiles/executes with strict FileOptions (no load, no recursion, no top-level control if matching workflow safety—or minimal options that still allow `def main`).
- `Definitions` formats allowed tools for the model.
- Unit tests use fake call func (no real MCP) to assert naming, permission deny, return encoding, step cancel.

**Test scenarios:**

- `main` returns string → tool output that string.
- `main` returns dict → JSON string.
- `main` returns None → error.
- Denied subject → error, call func not invoked.
- Invalid args vs input schema → error, call func not invoked.
- Builtin name sanitize (`create-issue` → `server_create_issue`).
- Collision after sanitize → error at env build.
- Cancelled ctx stops run.
- Definitions omit denied tools and include schema/description/starlark name.
- Definitions with `server` filter and `match` substring return only matching tools; no args returns full allowed set.

### U3. Custom tools + bridge injection

**Goal:** Model sees only the two tools when mcp-allowed; they call codemode + registry.

**Requirements:** R4, R6, R7, R9, AE1–AE5

**Dependencies:** U1, U2

**Files:**

- `internal/rocketclaw/harnessbridge/mcp_tools.go` (new)
- `internal/rocketclaw/harnessbridge/mcp_tools_test.go` (new)
- `internal/rocketclaw/harnessbridge/bridge.go` (append tools in runTurn)
- `internal/rocketclaw/harnessbridge/raw_run.go` (inject mcp CustomTools on cron/raw the same way as `runTurn` when agent permissions apply—required, not optional)
- Wiring of registry into Bridge/runtime config as needed (single process-scoped registry shared by conversation bridges and raw/cron)

**Approach:**

- Mirror `maybeDynamicWorkflowTool` structure, but **do not copy workflow’s allow-only filter** — use `Evaluate` allow|auto for visibility/omit (match `toolVisible`).
- Build allowed tool set from permission ∩ configured servers (KTD5 assembly rule); omit both if empty.
- `Permission: "mcp"`; **both** `VisibilitySubjects` and `Subjects` = the same non-empty allowed `server.tool` list (never default Subjects to the tool name `mcp_run` / `mcp_tool_definitions`).
- `mcp_run` Parameters: `{ "code": string }` required.
- `mcp_tool_definitions` Parameters: optional `server`, optional `match` (see KTD10); description lists visible server names.
- Not on RocketClaw auto-allow list (assert in test).
- Integration-style test with temp agent + fake/real registry.

**Test scenarios:**

- No mcp allow → neither tool in CustomTools.
- Allow one server.* → both tools present; definitions schema mentions that server.
- mcp_tool_definitions match/server filters reduce the result set.
- mcp_run executes script via registry mock.
- Tool names are exactly `mcp_tool_definitions` and `mcp_run`.
- Not in auto-allow list for toolModePersistent.
- Workflow prepare path still has no mcp tools.

### U4. Operator docs

**Goal:** Authors can configure servers and grant mcp permissions without reading the plan.

**Requirements:** R10

**Dependencies:** U3 behavior stable

**Files:**

- `cmd/rocketclaw/CHEATSHEET.md`
- `internal/rocketclaw/skel/.../main-create-or-update-agent/SKILL.md` (or current agent skill path)
- Root `README.md` only if MCP agent setup is already described there and would be misleading without a line—prefer CHEATSHEET first; add README blurb only if existing MCP section implies tools are inbound-only and needs one clarifying sentence.

**Approach:**

- Document `mcp_servers` shapes, `permission.mcp` subjects, two tools, Starlark `server_toolname(...)` calling (with sanitize note), omit behavior, no-auth limitation, restart for config.
- State that wildcards (`server.*`, `*`) grant full current **and future** tool-catalog authority for matching names; prefer least-privilege `server.tool` grants; show deny-overrides for dangerous tools.
- Example agent snippet and example server entry.

**Test scenarios:** none beyond existing doc lint if any; human-readable accuracy.

---

## Verification Contract

- `gofmt` on touched Go files
- `go test ./internal/rocketclaw/config ./internal/rocketclaw/mcpclient ./internal/rocketclaw/codemode ./internal/rocketclaw/harnessbridge ./internal/rocketclaw/externalmcp`
- `make -C internal/rocketclaw test` (or repo `make test` if that is the gate)
- `make -C internal/rocketclaw check-cloc-budget` / `make lint` as required by AGENTS.md (`make lint`, `make test` at repo level before final)
- Manual sanity (optional): one stdio MCP and one scripted agent turn in a dev workspace

---

## Definition of Done

- All R1–R10 and AE1–AE5 satisfied
- U1–U4 complete with listed tests green
- rocketclaw source CLOC under updated failure threshold (19000 after +1500 budget)
- No inbound MCP regressions
- CHEATSHEET documents the feature
- Agent-authoring skill mentions `permission.mcp` and the two tools
- README impact considered (update only if existing MCP wording would mislead)

---

## Appendix

### Settled decisions (session)

| Decision | Choice | Rejected |
|----------|--------|----------|
| Server config location | `rocketclaw.json` global + agent allow | Per-agent full server defs; workspace overlay-only |
| Starlark call shape | `server_toolname(...)` | Single `call(server,tool,args)`; module `mcp.call` |
| Allow mechanism | `permission.mcp` with `server.tool` / wildcards | Frontmatter list only |
| Transports | stdio + HTTP; static headers/env OK; no OAuth | OAuth in v1 |
| Empty allow | Omit both tools | Always show empty tools |
| Availability | Ordinary tools / CustomTools paths; not special turn taxonomy | Inventing chat-only scope |

### CLOC note

rocketclaw ~16782 source today. **Budget +1500** (failure **19000**) approved for this feature; update `internal/rocketclaw/Makefile` `SOURCE_CLOC_BUDGET` during implementation. Still prefer thin code; cut cache sophistication before permission correctness if needed.
