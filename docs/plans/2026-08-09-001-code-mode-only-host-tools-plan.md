---
title: "code-mode-only host tools"
date: 2026-08-09
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# Code-mode-only host tools

## Goal Capsule

- **Objective:** Stop exposing filesystem/shell/fetch tools as direct model tools. The model uses one tool, `execute`, and does that work inside a short Starlark script.
- **Package split (load-bearing):**
  - **`internal/rocketcode` owns ~all behavior.** Hiding the six tools from the model, keeping them inside `execute`, subagent (`task`) children, schemas, nested permissions. Any embedder (RocketClaw, CLI, tests) gets the new surface automatically once RocketCode changes.
  - **`internal/rocketclaw` owns only embedder glue + docs.** (1) Workflow agent `RestrictTools` filter currently **removes** `execute` — must stop doing that. (2) CHEATSHEET + agent-authoring skill wording. RocketClaw does **not** reimplement code mode, host tools, or permission evaluation.
- **Authority:** This plan > prior MCP code-mode plans for model tool surface. Nested permission behavior stays as implemented today.
- **Stop when:** On agent turns (main, `task` children, workflow-invoked agents), the provider tool list has no `read` / `apply_patch` / `glob` / `grep` / `webfetch` / `bash`; those still work inside `execute` with the same permissions; RocketClaw workflow agents keep `execute`; docs match; lint/tests green under CLOC budgets.
- **Product Contract preservation:** Product Contract created in this bootstrap (no prior requirements-only artifact).

## Ownership map

| Concern | Owner | Not owner |
|---------|--------|-----------|
| Which tools the **model** sees | **rocketcode** (`toolsFor` / `looper.Tools` / `buildParams`) | rocketclaw |
| Implementations of `read`/`bash`/… | **rocketcode** (already) | rocketclaw |
| Binding those into Starlark inside `execute` | **rocketcode** | rocketclaw |
| Nested allow/deny/auto for scripted calls | **rocketcode** | rocketclaw |
| `task` child tool lists | **rocketcode** (same `toolsFor`) | rocketclaw |
| MCP + `execute` registration | **rocketcode** | rocketclaw only passes `mcp_servers` config (already done) |
| Workflow agent tool allowlist (`RestrictTools`) | **rocketclaw** `harnessbridge` | rocketcode (generic RestrictTools API only) |
| Operator docs (CHEATSHEET, skill) | **rocketclaw** | rocketcode has no CHEATSHEET |
| RocketClaw custom tools (`rocketclaw_*`, `ask_user_question`) | **rocketclaw** injects; stay top-level | rocketcode only hosts them as custom tools |

**Size expectation:** U1 (rocketcode) is the real feature. U2 (rocketclaw) is a small filter flip + documentation.

## Product Contract

### Problem

Code mode (`execute`) already lets scripts call host tools and MCP. The model still also sees the old one-shot tools (`read`, `bash`, …). That doubles the surface and fights “compose in a script.”

### Requirements

Tagged **RC** = rocketcode, **CL** = rocketclaw.

- R1. **[RC]** These tools are **not** sent to the model as top-level tools: `read`, `apply_patch`, `glob`, `grep`, `webfetch`, `bash`.
- R2. **[RC]** The same six remain available **inside** `execute` as Starlark builtins, with the same permission buckets/subjects as today (including multi-subject `bash` and nested auto-approver).
- R3. **[RC]** MCP stays code-mode-only (already true).
- R4. **[RC]** Still top-level when eligible: `execute`, `task`, `skill`, `find_skills`, `websearch`. **[CL]** RocketClaw custom tools stay top-level when injected (`rocketclaw_*`, `ask_user_question`, …) — no change to injection list required beyond docs.
- R5. **[RC]** **Every agent** RocketCode builds uses this rule: primary agent and `task` subagents. No dual “children still get direct read” mode inside RocketCode.
- R6. **[CL]** **Workflows call agents; agents call `execute`.** Workflow Starlark is not a FS API. Workflow **agent** runners must **keep** `execute` (today `raw_run.go` strips it — **rocketclaw bug/policy relative to this product**). They must not rely on the six direct host tools (RocketCode already won’t offer them after R1).
- R7. **[RC]** Agent permission YAML stays bucket-based (`read`, `edit`, `bash`, …). No new `permission.execute` product surface; nested calls stay authoritative.
- R8. **[CL]** CHEATSHEET and agent-authoring skill describe the new surface in plain language.

### Actors

- A1. Primary agent on a managed turn (RocketCode runtime; RocketClaw hosts it)
- A2. Subagent started via `task` (RocketCode)
- A3. Agent step inside a RocketClaw workflow (RocketCode runtime + RocketClaw RestrictTools)
- A4. Operator reading CHEATSHEET / writing agent frontmatter (RocketClaw docs)

### Key flows

- F1. **[RC]** Agent needs file → `execute` + `read(...)` → nested `read` permission.
- F2. **[RC]** Agent needs shell → `execute` + `bash(command=...)` → nested multi-subject bash.
- F3. **[RC]** Parent `task` → child has no top-level `read`; child uses `execute`.
- F4. **[CL+RC]** Workflow starts an agent → RocketClaw must not strip `execute` → agent uses RocketCode code mode only for FS/shell.

### Acceptance examples

- AE1. **[RC]** Agent with `permission.read: {"*": allow}`: model tools include `execute`, exclude `read`; scripted `read` works.
- AE2. **[RC]** No host/MCP grants → no `execute`.
- AE3. **[RC]** `task` child: no direct six in child model tools.
- AE4. **[CL]** Workflow RestrictTools: `execute` retained when RocketCode would offer it; `task` still stripped if that remains workflow policy; six host names absent (redundant if RC already omits them).
- AE5. **[RC]** Nested bash deny still fails inside script.

### Scope

**In scope — rocketcode**

- Tool assembly: model surface vs internal host registry for the six
- Code-mode host allowlist binding + description catalog
- Starlark kwargs/schema fix for common host calls
- `task` child inherits same assembly
- Unit/integration tests under `internal/rocketcode/`

**In scope — rocketclaw**

- `RestrictTools` filter in workflow agent runner (`execute` stay / `task` strip policy)
- CHEATSHEET + `main-create-or-update-agent` skill
- Thin harnessbridge test for workflow filter
- No second MCP/code-mode implementation

**Out of scope**

- Moving `skill` / `find_skills` / `task` / `websearch` into code mode
- Rewriting Starlark or MCP client
- Nested webfetch image/PDF attachment fidelity (text flatten OK)
- New `permission.execute` bucket
- RocketClaw auto-allow list redesign

### Key decisions (product)

- KD1. One FS/shell/fetch path: scripts only.
- KD2. Orchestration stays top-level (`task`, skills, websearch, rocketclaw_*, ask_user).
- KD3. Uniform for all RocketCode agents; workflows only call those agents.

## Planning Contract

### Assumptions

- AS1. “etc.” = the six sandbox tools, not every RocketCode tool.
- AS2. Workflow product intent = workflow → agents → execute.
- AS3. Nested webfetch attachment flatten is OK.

### Key technical decisions

- KTD1. **[RC]** **Split model surface from code-mode host registry.** Today `codeModeHostToolsFromContext` reads `looper.Tools` (same map as the provider). Deleting host tools from that map alone empties `execute`. Keep host implementations available to code mode without listing them as model tools.
- KTD2. **[RC]** **Explicit code-mode host allowlist** = the six in R1. Do not bind every `Call` tool (blocks nesting `ask_user_question` / `rocketclaw_restart` / `task` inside scripts).
- KTD3. **[RC]** **`execute` description catalog** from internal host registry ∩ visibility, not from model-visible tools.
- KTD4. **[RC]** **Starlark arg schemas:** `functionTool` marks all properties required; fix code-mode host schemas/defaults so `bash(command="…")` and `read(file_path="…")` work.
- KTD5. **[CL]** **Workflow RestrictTools:** stop stripping `execute`. Optionally strip the six names if present (defense). Keep stripping `task` unless proven otherwise.
- KTD6. **[RC]** **`task` children** use the same `toolsFor` — no full-host bypass.
- KTD7. **[RC]** **`execute` entry** still when MCP grants **or** any of the six would be permission-visible.
- KTD8. **[RC]** Diagnostics list model tools only; no requirement to dump internal registry.
- KTD9. **CLOC:** rocketcode budget is the pressure point; rocketclaw change should be tiny.

### Technical design (directional) — rocketcode core

```text
[rocketcode] permissions + factory
        │
        ├─► internalHostTools (six) ──► execute catalog + Starlark builtins
        │                                    + nested CheckNestedToolCall
        │
        └─► modelTools (no six) = skill/task/websearch/custom + execute?
                  │
                  └─► buildParams → provider
```

### Technical design — rocketclaw edge only

```text
[rocketclaw] newWorkflowAgentRunner
        │
        ├─► rocketcode.New(...)     # already gets RC model surface after U1
        └─► RestrictTools(names)    # CHANGE: keep execute; drop task; do not drop execute
```

RocketClaw does **not** own internalHostTools.

### Risks

| Risk | Owner | Mitigation |
|------|--------|------------|
| Hide six without internal registry → broken execute | RC | KTD1 first |
| Models guess kwargs | RC | KTD4 + description |
| Workflow agents lose FS | CL | KTD5 |
| Docs still show top-level read | CL | R8 |
| Nested tools silent in diagnostics | RC | Accept / out of scope |

### Sequencing

1. **U1 rocketcode** (must land first — CL alone cannot hide tools from the model)
2. **U2 rocketclaw** (filter + docs; depends on U1 behavior existing)
3. **U3** full `make lint` / `make test`

## Implementation Units

### U1. [rocketcode] Hide six from model; keep them inside execute

**Package:** `internal/rocketcode` only.  
**Goal:** Provider never sees the six; `execute` still runs them under nested permissions; `task` children match.

**Primary files (all under `internal/rocketcode/`)**

- `tools.go` — assembly / omit six from model map
- `mcp_tools.go` — host registry bind + catalog
- `permission_gate.go` / `looper.go` / `rocketcode.go` — only if split needs a field or context
- Tests: `mcp_tools_test.go`, tools/looper tests as needed

**Do not touch in U1:** `internal/rocketclaw/**`, `cmd/rocketclaw/**`

**Approach**

- Smallest split so code-mode hosts survive when absent from model `Tools`
- Allowlist bind = six only
- Schema/defaults for common kwargs
- Same `toolsFor` for `task` children

**Test scenarios (rocketcode)**

- Read grant → model has `execute`, no `read`/`bash`/…
- Scripted `read` / nested deny / multi-subject bash deny
- `bash(command="echo hi")` works
- `task` child model tools lack the six
- Host path works even when six names are not keys of model `Tools`

**Verify:** `go test ./internal/rocketcode/...` (+ rocketcode lint via make)

### U2. [rocketclaw] Workflow keep execute + docs

**Package:** rocketclaw embedder + docs only.  
**Goal:** Workflow-invoked agents can use `execute`; operators read correct docs.

**Primary files**

- `internal/rocketclaw/harnessbridge/raw_run.go` — RestrictTools name filter
- `internal/rocketclaw/harnessbridge/*_test.go` — workflow filter assertion
- `cmd/rocketclaw/CHEATSHEET.md`
- `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/SKILL.md`

**Do not touch in U2:** RocketCode host implementations, permission engine, Starlark runner (unless a one-line comment cross-ref is unavoidable — prefer zero RC edits)

**Approach**

- Filter change: **keep** `execute`; **strip** `task` (status quo); strip six names only if still present
- Docs: FS/shell/fetch/patch via `execute` only; example one-liner; permissions unchanged; workflows call agents, agents call `execute`
- No new custom tools; no MCP config changes

**Test scenarios (rocketclaw)**

- After RestrictTools: `execute` present when RC would register it; `task` absent; no `read`/`bash` in tool map

**Verify:** `go test ./internal/rocketclaw/harnessbridge/...`

### U3. Repo gate

**Both packages:** `make lint`, `make test`. No budget edits without human approval.

## Verification Contract

| Check | Package |
|-------|---------|
| `go test ./internal/rocketcode/...` | RC |
| `go test ./internal/rocketclaw/harnessbridge/...` | CL |
| `make lint` / `make test` | both |
| Sanity: model list has `execute` not `read` | RC behavior; CL hosts it |

## Definition of Done

- R1–R5, R7 proven in **rocketcode** tests
- R6, R8 proven in **rocketclaw** filter test + docs
- Clear commit split preferred: one RC change, one CL change (same as MCP code-mode stack style)
- README: prefer CHEATSHEET; root README only if it still documents direct tools

## Appendix

### Why rocketclaw cannot do this alone

RocketClaw injects custom tools and calls `RestrictTools` for workflows. It does **not** build `read`/`bash` or decide the default model tool list — that is `rocketcode.toolsFor`. Hiding the six without RocketCode changes is impossible. Conversely, after U1, main/task agents are already correct even if U2 is delayed; only **workflow** agents stay broken until U2 restores `execute` on that path.

### Settled in planning conversation

- Subagents: same execute-only host rule (RC).
- Workflows call agents; agents call `execute` (CL filter + RC surface).
