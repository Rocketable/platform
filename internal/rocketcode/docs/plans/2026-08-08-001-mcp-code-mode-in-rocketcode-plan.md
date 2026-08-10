---
title: "MCP code mode in RocketCode"
type: feature
date: 2026-08-08
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# MCP code mode in RocketCode

## Goal Capsule

- **Objective:** Own outbound MCP code mode inside **RocketCode** so nested MCP calls use the same allow/deny/auto-approver path as `bash`. RocketClaw only configures servers and enables the feature.
- **Authority:** This plan > prior rocketclaw-local code mode placement. Product surface (`mcp_tool_definitions`, `mcp_run`, `permission.mcp`, Starlark `server_toolname`) stays; **home package moves**.
- **Stop when:** Code mode packages live under `internal/rocketcode`; nested `auto` runs the real approver; RocketClaw is thin wiring + docs; prior rocketclaw `codemode`/`mcpclient`/harness tools removed or reduced to config pass-through; tests green; CLOC budgets held.
- **Out of scope:** Changing inbound externalmcp; OAuth flows; promoting MCP tools as first-class model tools; workflow Starlark DSL.

---

## Product Contract

### Summary

Agents still get two tools and Starlark MCP chaining. Implementation moves into RocketCode so permission.auto on each in-script MCP call hits the same auto-approver as bash. RocketClaw supplies `mcp_servers` from `rocketclaw.json` into `rocketcode.Config`.

### Problem Frame

Code mode was built under RocketClaw. Nested MCP builtins only `Evaluate`d permissions and treated `auto` as allow, skipping RocketCode’s approver. Auto-approver lives in RocketCode; nested gates must too.

### Requirements

- R1. **RocketCode owns** outbound MCP client, Starlark code-mode runner, and model tools `mcp_tool_definitions` + `mcp_run`.
- R2. Nested MCP builtin calls use the **same** allow/deny/auto(+reviewer) path as top-level tools (including auto-approver model when action is auto).
- R3. Product contracts from prior plan remain: `permission.mcp` subjects `server.tool`; Starlark `server_toolname`; definitions with optional `server`/`match` and server names in schema; connect-per-call; stdio+HTTP+headers; schema validate args; stdio inherits parent env with server env overrides and default cwd = workspace root.
- R4. RocketClaw: load `mcp_servers` → `rocketcode.Config`; no duplicate permission policy; remove rocketclaw-local codemode/mcpclient/harness mcp tool bodies.
- R5. Tests in rocketcode prove nested auto invokes reviewer (or deny when auto-approve disabled); rocketclaw tests prove wiring/omit still work.
- R6. Docs (CHEATSHEET) note code mode is RocketCode-backed; no behavior regression for allow/deny.

### Actors

- A1. RocketCode embedder (RocketClaw, CLI, tests)
- A2. Agent with `permission.mcp`
- A3. Auto-approver model

### Key Flows

- F1. `mcp: "github.create_issue": auto` → script calls `github_create_issue` → approver runs → allow/deny → CallTool or fail.
- F2. `allow` → CallTool without approver.
- F3. `deny` → Starlark fail, no CallTool.
- F4. RocketClaw with empty mcp_servers / no grants → tools omitted.

### Acceptance Examples

- AE1. RocketCode unit test: auto subject triggers permission review path (mock/spy or recorded reviewer), deny blocks CallTool.
- AE2. allow path CallTool succeeds without review.
- AE3. RocketClaw still injects tools when config+grants present; workflow workers still lack them.
- AE4. Prior product AEs (definitions filters, starlark main, IsError) still pass under new packages.

### Scope Boundaries

**In scope:** Move packages; permission gate for nested calls; RocketClaw thin wiring; delete old rocketclaw implementations; CLOC.

**Out of scope:** New product tools; OAuth; changing workflow engine.

### Deferred

- Richer MCP resources/prompts in code mode
- Sharing Starlark isolation with workflow package (keep separate)

### Product Contract preservation

Prior product decisions carried (session-settled: user-directed): code mode in RocketCode not RocketClaw; auto like bash per nested call; two tools; permission.mcp; server_toolname.

---

## Planning Contract

### Assumptions

- Existing rocketclaw mcpclient/codemode/mcp_tools are the migration source (behavior already largely correct except nested auto).
- RocketCode CLOC budget ~9000 with headroom; rocketclaw budget 19000 — move lines from claw to code.
- `AutoApprovePermissions` + `AutoApproverModel` already on rocketcode.Config; looper already has review path.

### Key Technical Decisions

- KTD1. **Packages under rocketcode:** `internal/rocketcode/mcpclient`, `internal/rocketcode/codemode` (move/adapt from rocketclaw). Tools registered as built-in optional tools or via Config factory when MCP servers configured.
- KTD2. **Config on rocketcode.Config:** `MCPServers map[string]MCPServerConfig` + workspace path for cwd (or pass workspace on Config already via other fields — use explicit `MCPWorkspace string` or existing sandbox root). Prefer: servers on Config; workspace from existing Config paths used for tools (check rocketcode.Config for WorkDir/Root).
- KTD3. **Nested permission gate (session-settled: user-directed):** Extract reusable check from looper `permissionDecision` + `PermissionReviewer.reviewPermission`. Expose to code-mode builtins via context or Call-time gate set by looper before CustomTool/builtin execution. Nested MCP calls must not treat auto as allow.
- KTD4. **Registration:** When `len(MCPServers)>0`, rocketcode tool factory adds `mcp_tool_definitions` and `mcp_run` (filtered by agent permission.mcp like today). Embedders do not reimplement tools.
- KTD5. **RocketClaw:** Map `config.MCPServers` → rocketcode.Config; delete `internal/rocketclaw/codemode`, `internal/rocketclaw/mcpclient`, `harnessbridge/mcp_tools.go` injection of local tools; keep CHEATSHEET.
- KTD6. **Entry Subjects for mcp_run:** Keep coarse visibility subjects; authoritative per-call gate is nested Check inside builtins (KTD3).
- KTD7. **CLOC:** Delete rocketclaw copies when rocketcode owns them; no dual maintenance.

### High-Level Design

```text
rocketclaw.json mcp_servers
        → rocketcode.Config.MCPServers
        → toolFactory adds mcp_* tools
        → model calls mcp_run
        → looper permission on mcp_run
        → Call → Starlark
        → builtin → NestedPermissionCheck (same as bash auto)
        → mcpclient.CallTool
```

### Patterns

- `internal/rocketcode/looper.go` permissionDecision + review
- `internal/rocketcode/custom_tools.go` Tool shape
- Existing `internal/rocketclaw/codemode` + `mcpclient` as move source
- Prior plan: `internal/rocketclaw/docs/plans/2026-08-08-001-mcp-code-mode-plan.md` product KTDs

### Sequencing

U1 rocketcode permission gate → U2 move mcpclient+codemode+tools into rocketcode → U3 rocketclaw thin wiring + delete old → U4 tests/docs/CLOC.

### Risks

| Risk | Mitigation |
|------|------------|
| Nested review needs looper/factory state | Gate set on ctx from looper before tool Call |
| CLOC rocketcode | Move not copy; delete claw packages |
| Double registration | Only rocketcode registers tools |

---

## Implementation Units

### U1. Nested permission check in RocketCode

**Goal:** One API the looper and nested MCP builtins share for allow/deny/auto+review.

**Requirements:** R2, AE1–AE2

**Files:**

- `internal/rocketcode/looper.go` (extract/reuse)
- `internal/rocketcode/permission_review.go` / new `permission_check.go`
- `internal/rocketcode/permission_check_test.go`
- `internal/rocketcode/custom_tools.go` or looper Call path (attach gate to ctx)

**Approach:**

- Implement `CheckToolPermission(ctx, req) error` (or method on gate) covering Evaluate multi-subject rules, AutoApprovePermissions, and reviewer invoke with args JSON.
- Looper tool dispatch uses it (or stays equivalent).
- Before `tool.Call`, `ctx = withPermissionGate(ctx, gate)`.
- Tests: allow; deny; auto without AutoApprove → deny; auto with mock reviewer allow/deny.

**Test scenarios:** AE1, AE2; recursive auto denied; empty subjects deny.

### U2. Move MCP client + codemode + tools into RocketCode

**Goal:** Full code mode feature inside rocketcode.

**Requirements:** R1, R3, AE4

**Dependencies:** U1

**Files:**

- `internal/rocketcode/mcpclient/` (from rocketclaw)
- `internal/rocketcode/codemode/` (from rocketclaw; decide uses gate)
- `internal/rocketcode/mcp_tools.go` (from harnessbridge mcp_tools, adapted)
- `internal/rocketcode/tools.go` / `rocketcode.go` registration
- `internal/rocketcode/rocketcode.go` Config fields
- Move tests

**Approach:**

- Port packages; fix imports.
- Builtins call `PermissionGateFrom(ctx).Check(..., "mcp", subject, args)`.
- Factory registers tools when MCP servers configured.
- Keep behavior from prior plan (definitions filters, session open/close per call, etc.).

**Test scenarios:** Prior codemode/mcpclient/mcp_tools tests ported; nested auto test with gate.

### U3. RocketClaw thin wiring + delete duplicates

**Goal:** RocketClaw only maps config; no local code mode engine.

**Requirements:** R4, R6, AE3

**Dependencies:** U2

**Files:**

- `internal/rocketclaw/config` (keep mcp_servers)
- `internal/rocketclaw/harnessbridge/bridge.go`, `raw_run.go` (pass MCPServers into rocketcode.Config; remove maybeMCPTools local)
- Delete `internal/rocketclaw/codemode`, `mcpclient`, `harnessbridge/mcp_tools.go`
- `cmd/rocketclaw/CHEATSHEET.md` tweak if needed
- Makefile CLOC if lines moved

**Approach:**

- `rocketcodeConfig` sets MCPServers + workspace from claw config.
- Remove claw mcpRegistry field if unused.
- Ensure workflow path still has no mcp tools (no servers or permissions).

**Test scenarios:** AE3; claw harness tests updated; claw packages gone.

### U4. Verification pass

**Goal:** Full test/lint/cloc green.

**Files:** makefiles, any stragglers

**Verification:** `make test`, `make lint`, both package CLOC budgets.

---

## Verification Contract

- `go test ./internal/rocketcode/... ./internal/rocketclaw/...`
- `make lint` && `make test`
- rocketcode + rocketclaw `check-cloc-budget`
- No imports of deleted claw packages

## Definition of Done

- R1–R6, AE1–AE4
- Nested auto uses RocketCode approver
- Single implementation location (rocketcode)
- Docs accurate
- README impact considered

## Appendix

### Settled decisions (session)

| Decision | Class | Rejected |
|----------|-------|----------|
| Code mode in RocketCode | user-directed | Keep in RocketClaw with local auto hack |
| Nested auto = bash auto path | user-directed | auto≈allow inside script |
| Product two-tool surface | prior settled | One tool only / native MCP tools |
