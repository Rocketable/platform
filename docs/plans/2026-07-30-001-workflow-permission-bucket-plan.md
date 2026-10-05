---
title: "workflow permission bucket"
type: feature
date: 2026-07-30
artifact_contract: unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: plan-bootstrap
execution: code
---

# workflow permission bucket

## Goal Capsule

- **Objective:** Gate `rocketclaw_dynamic_workflow` with an explicit `permission.workflow` bucket whose subjects are workflow stems, and restore `permission.task` to subagent names only (for the `task` tool).
- **Authority:** This plan > prior dynamic-workflow plan’s KTD3 shared-`task` choice for workflow ACL only. Nested tool execution and thinking progress stay as implemented.
- **Stop when:** An agent needs `workflow: "<name>": allow` to see/call the tool for that workflow; `task` allows no longer enable workflows; CHEATSHEET and agent skill document the split; tests assert the new contract.
- **Out of scope for stop:** Merging launch into the `task` tool; human `$workflow` permission model; agentlint graph edges for workflows; dual-read compatibility with old `task` grants for workflows.

---

## Product Contract

### Summary

`rocketclaw_dynamic_workflow` currently uses `Permission: "task"` and evaluates `task` against workflow names. That couples subagent grants to workflow launch (including `task: "*"`). Introduce bucket **`workflow`** so authors grant workflow stems separately from subagents, while keeping the dedicated RocketClaw tool.

### Problem Frame

Shared `task` subjects made “treat workflows like agents for permissions” mean the same string namespace and bucket. That overloads `task`, collides names, and expands blast radius of broad task wildcards. Product direction after analysis: keep separate launch tool; split the ACL bucket.

### Requirements

- R1. `rocketclaw_dynamic_workflow` uses permission bucket **`workflow`** (not `task`) for call-time subjects and visibility filtering.
- R2. Subjects remain workflow stems (filename / `meta.name`); patterns and later-rule-wins behave like other buckets via existing `PermissionSet.Evaluate`.
- R3. `task` bucket is documented and enforced as **subagent names for the `task` tool only**; it does not gate `rocketclaw_dynamic_workflow`.
- R4. Tool omitted when no loaded workflow is `workflow`-allowed (same omit-on-empty-allow behavior as today with `task`).
- R5. Clean break: do **not** treat legacy `task: "<workflow>": allow` as sufficient for workflow launch after this change.
- R6. Docs (CHEATSHEET tools table + permission buckets; agent-authoring skill) describe `workflow` and remove shared-namespace / `task` workflow wording.
- R7. Tests and any agentlint/docs that asserted `task` for workflows move to `workflow`.

### Actors

- A1. Agent author (YAML `permission.workflow`)
- A2. Managed agent calling `rocketclaw_dynamic_workflow`
- A3. Operator auditing grants after upgrade

### Key Flows

- F1. Allowed workflow
  - **Trigger:** `permission.workflow."audit-routes": allow` and loaded workflow `audit-routes`.
  - **Outcome:** Tool visible; call with that name succeeds under looper permission check.
- F2. Task-only grant does not launch workflow
  - **Trigger:** Only `permission.task."audit-routes": allow` (or `task: "*"`), no `workflow` allow.
  - **Outcome:** Tool omitted or call denied for workflow name; subagent `task` still works for agents.
- F3. Independent wildcards
  - **Trigger:** `workflow: "audit-*": allow` without granting all subagents.
  - **Outcome:** Matching workflows allowed; unrelated `task` subagents unchanged.

### Acceptance Examples

- AE1. Covers R1–R2, F1. Agent has only `workflow: "audit-routes": allow`. Tool lists `audit-routes`; call returns nested result as today.
- AE2. Covers R3, R5, F2. Agent has `task: "*": allow` and no `workflow` rules. Tool does not expose workflows; `task` tool still lists allowed subagents.
- AE3. Covers F3. `workflow: "audit-*": allow` + deny one stem → later-rule-wins same as other buckets.

### Scope Boundaries

**In scope**

- Tool permission field + evaluate/filter paths
- Tests for tool + docs/skills
- Clarifying `task` docs back to subagents-only

**Out of scope**

- Changing nested run engine, progress, or tool name
- Folding launch into `task` tool
- Migrating existing agent YAML in customer workspaces (clean break; authors re-grant under `workflow`)
- agentlint treating workflow grants as delegation graph edges (still deferred unless trivial)

### Deferred to Follow-Up Work

- Optional agentlint warning when `task` patterns match loaded workflow stems (collision hygiene)
- Optional skel default agent examples that demonstrate `workflow:` grants

---

## Planning Contract

### Assumptions

- Bucket name is **`workflow`** (singular), parallel to `task` / `skill`.
- RocketCode permission buckets are open-ended (`normalizePermissionName` accepts unknown names); no allowlist change required in `permission.go` unless tests hardcode bucket lists.
- Human `$workflow` remains ungated by agent YAML (unchanged).
- Existing in-repo agents that only had `task` grants for workflow demos (if any) are test/docs fixtures updated in this change—not a shipped migration.

### Key Technical Decisions

- KTD1. **Bucket `workflow`, tool stays `rocketclaw_dynamic_workflow`.** Fixes ACL/API split without overloading the `task` tool schema or RocketCode nesting.
- KTD2. **`Permission: "workflow"` and `Evaluate("workflow", name)`** everywhere the tool currently uses `"task"` (construction filter, Subjects already return name; looper uses tool.Permission).
- KTD3. **Clean break from `task` for workflows.** No dual-read of `task` allows. Upgrade note in CHEATSHEET: re-grant under `workflow:`.
- KTD4. **Omit short-circuit:** if the simplified code has a “any task allow” skip before load, replace with “any workflow allow” (or equivalent scan of `workflow` bucket for allow rules)—do not leave a `task` short-circuit.
- KTD5. **agentlint `taskEdges` unchanged** (still only `Evaluate("task", agentName)` for agent-to-agent graph). Do not add workflow names to the task graph unless a follow-up explicitly wants that.

### High-Level Technical Design

```text
Before:  permission.task ──► task tool (subagents)
                        └─► rocketclaw_dynamic_workflow (workflow stems)

After:   permission.task      ──► task tool (subagents only)
         permission.workflow  ──► rocketclaw_dynamic_workflow (workflow stems)
```

Looper behavior unchanged: custom tool declares `Permission` + `Subjects` → evaluate per subject.

### Patterns to Follow

- Bucket usage: `internal/rocketcode/tasks.go` (`Permission: "task"`)
- Open-ended buckets: `internal/rocketcode/permission.go` `normalizePermissionName` / `Evaluate`
- Current tool: `internal/rocketclaw/harnessbridge/dynamic_workflow_tool.go`
- Docs tables: `cmd/rocketclaw/CHEATSHEET.md` permission buckets section
- Agent skill: `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/SKILL.md`

### Sequencing

U1 code + tests → U2 docs. Docs may ship in same change as U1.

---

## Implementation Units

### U1. Switch tool ACL to `workflow` bucket

**Goal:** All runtime gating for `rocketclaw_dynamic_workflow` uses `workflow` subjects; `task` no longer enables the tool.

**Requirements:** R1–R5, R7, AE1–AE3

**Dependencies:** none

**Files:**

- `internal/rocketclaw/harnessbridge/dynamic_workflow_tool.go`
- `internal/rocketclaw/harnessbridge/dynamic_workflow_tool_test.go`
- Any other harnessbridge tests that set `task` allows solely to expose the workflow tool

**Approach:**

- Set `Permission: "workflow"`.
- Filter and evaluate with `Evaluate("workflow", name) == PermissionAllow` (same action bar as today).
- Update tool description text that mentions `permission.task` / shared subagent namespace → `permission.workflow`.
- Replace any pre-load short-circuit that scans for `task` allows with a `workflow` allow scan (behavior: no workflow allows → omit without loading, if that optimization exists).
- Tests: rewrite permission matrix from `task` to `workflow`; add AE2 case (`task: "*"` alone does not expose tool); keep wildcard/deny-wins under `workflow`; keep nested run tests unchanged except permission setup.

**Test scenarios:**

- Happy: `workflow: "audit-routes": allow` → visibility/call ok.
- Happy: `workflow: "audit-*": allow` → matching stems only.
- Regression AE2: `task: "*": allow` only → tool omitted; optional assert `task` tool still would see subagents if agents present.
- Error: deny after allow on `workflow` for a stem → not visible.
- Edge: empty `workflow` allows → omit; load failure still omit.
- Call-time: Subjects still return workflow name; looper deny path works with `workflow` deny (if covered via custom tool permission tests or existing matrix).

**Verification:** `go test ./internal/rocketclaw/harnessbridge/ -run DynamicWorkflow|MaybeDynamic|AllowedWorkflow|Workflow`

**Execution note:** Prefer updating existing table-driven permission tests in place over parallel duplicate suites.

---

### U2. Docs and agent skill

**Goal:** Operators and authors grant `workflow:` for the tool; `task` docs no longer claim workflow stems.

**Requirements:** R6

**Dependencies:** U1 (wording matches code)

**Files:**

- `cmd/rocketclaw/CHEATSHEET.md` (tools table row for `rocketclaw_dynamic_workflow`; permission buckets table; YAML examples that currently put workflow names under `task`)
- `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/SKILL.md` (`task` bullet and examples; add `workflow` bucket)
- Optionally one line in workflow authoring skill if present

**Approach:**

- Tools table: default = explicit `permission.workflow.<stem>`; not rocketclaw auto-allow; not gated by `task`.
- Buckets table: new row `workflow` | workflow stems for `rocketclaw_dynamic_workflow`.
- `task` row: subagents only; remove shared-namespace / wildcard-enables-workflows prose.
- Upgrade note (one short paragraph): existing agents that relied on `task` grants to launch workflows must add `workflow:` allows.
- Skill: list `workflow` next to other buckets; example YAML with `workflow: "audit-routes": allow` separate from `task: "reviewer": allow`.

**Test expectation:** none — docs only.

**Verification:** Manual skim of CHEATSHEET tools + buckets for internal consistency (no leftover “task subjects are workflow stems”).

---

## Verification Contract

- `gofmt` on touched Go files
- `go test ./internal/rocketclaw/harnessbridge/...` (and app/workflow only if touched)
- `make lint`
- `make test`
- Doc skim: CHEATSHEET + agent skill

## Definition of Done

- U1–U2 complete
- AE1–AE3 covered by tests where code-backed
- No production path evaluates `task` for `rocketclaw_dynamic_workflow`
- `task` tool behavior unchanged
- Nested workflow execution/progress unchanged
- README impact: none unless root README documents buckets (default CHEATSHEET/skills only)

---

## Risks & Dependencies

| Risk | Mitigation |
| --- | --- |
| Silent capability drop on upgrade for agents that used `task` for workflows | CHEATSHEET upgrade note; clean break is intentional (R5) |
| Authors confuse `workflow` bucket with human `$workflow` command | Docs: bucket is agent YAML only; `$workflow` remains human Slack control |
| agentlint false sense that task graph includes workflows | KTD5: leave taskEdges agent-only |

## System-Wide Impact

- **Permissions:** New first-class bucket in product docs; no change to core parse allowlist.
- **Security:** Narrows blast radius of `task: "*"` relative to workflow launch.
- **Agent surface:** Tool name unchanged; grants must move.

## Documentation / Operational Notes

- No automatic migration of workspace agent files.
- After deploy: audit agents that should launch workflows and add `permission.workflow` rules.

## Sources & Research

- `internal/rocketclaw/harnessbridge/dynamic_workflow_tool.go` — current `task` gating
- `internal/rocketcode/tasks.go` — `task` tool remains subagent-only
- `internal/rocketcode/permission.go` — open-ended bucket names
- `internal/rocketclaw/agentlint/agentlint.go` — `taskEdges` uses `task` only
- `cmd/rocketclaw/CHEATSHEET.md` — current overloaded docs
- Prior analysis in-session: separate tool + separate bucket vs unified `task` launch

**Product Contract preservation:** plan-bootstrap; supersedes shared-`task` product choice from `2026-07-29-001-rocketclaw-dynamic-workflow-tool-plan.md` for ACL only.

**External research:** skipped — local permission model is authoritative.
