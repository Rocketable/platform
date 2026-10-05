---
title: Code Mode Approval - Plan
type: feat
date: 2026-09-28
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Code Mode Approval - Plan

## Goal Capsule

- **Objective:** RocketClaw operators can choose whether a reviewer approves a complete Code Mode script before any of its commands run.
- **Means:** An independent `permission.rocketclaw.code_mode_approve` entry check, followed by the existing per-tool checks (KTD1).
- **Authority:** The user request governs the key, allowed values, default, and whole-script timing. Existing RocketCode permissions govern nested calls.
- **Stop condition:** Do not ship if ordinary scripts acquire a new approval requirement, a denied script can run a nested tool, or an explicitly selected reviewer is bypassed.
- **Execution:** One scoped implementation and verification pass; ship as a PR without touching unrelated working-copy changes.

## Product Contract

### Summary

Add an explicit whole-script approval rule for Code Mode, independent of the permissions on tools used by the script.

### Problem Frame

Today `execute` borrows a permission bucket and synthetic subject from available tools. An unrelated `allow` can skip review of the complete script, so operators cannot consistently require whole-script approval.

### Requirements

- R1. An agent may set `permission.rocketclaw.code_mode_approve` to `allow`, `auto`, or `auto(custom-agent)` to gate the full Code Mode call before its script runs. (session-settled: user-directed — chosen over the implicit borrowed gate: it cannot guarantee review of the entire script.)
- R2. In the absence of that setting, the whole-script gate always allows the call, independently of the agent's other permission grants. (session-settled: user-directed — chosen over a borrowed default: the user requires default allow.)
- R3. An `auto` decision uses the built-in reviewer; `auto(custom-agent)` uses the named loaded reviewer, and denial prevents all script execution.
- R4. Approval of the whole script does not bypass per-tool permissions; nested commands still require their own grants or reviews.

### Scope Boundaries

- Do not change how individual host or MCP calls are authorized, or add a new model-facing tool.
- Do not modify unrelated Web UI files already changed in the working copy.

## Planning Contract

### Key Technical Decisions

- KTD1. **Dedicated entry subject.** Change `execute`'s entry gate to bucket `rocketclaw`, subject `code_mode_approve`, and retain the full `code` argument in its review request. This replaces, rather than augments, the borrowed synthetic subject. (session-settled: user-directed — chosen over implicit borrowing: a separate decision must always see the whole script.)
- KTD2. **Default allow at the entry gate.** Treat no explicit `code_mode_approve` rule as allow without adding an agent permission rule that might affect other RocketClaw tools. Nested tool checks retain deny-by-default.

### Assumptions

- A `rocketclaw` wildcard rule should not silently select a whole-script reviewer or deny the entry when `code_mode_approve` is absent; an explicitly configured key is required to change the default. This is an implementation assumption to check against existing permission semantics.

## Implementation Units

### U1. Make the entry gate explicit

- **Goal:** Gate complete scripts before execution without borrowing a nested tool's permission.
- **Requirements:** R1, R2, R3, R4; KTD1, KTD2.
- **Files:** `internal/rocketcode/mcp_tools.go`, `internal/rocketcode/looper.go`, `internal/rocketcode/mcp_tools_test.go`, `internal/rocketcode/looper_test.go`.
- **Approach:** Reuse the existing top-level review decision and reviewer resolution. Select one constant entry subject; preserve per-tool nested gates. Keep default allow local to the Code Mode entry check.
- **Tests:** A script with a read grant and no entry setting runs without whole-script review. Explicit `auto` and `auto(custom-agent)` receive the full code even alongside unrelated allows. Reviewer denial runs no nested tool. Entry approval still cannot authorize a denied nested tool. MCP-only and host-only scripts follow the same entry path.
- **Verification:** Targeted RocketCode tests pass; no borrowed-gate path remains.

### U2. Document and validate RocketClaw agent settings

- **Goal:** Make the new key discoverable and ensure loaded agent definitions route it correctly.
- **Requirements:** R1–R4.
- **Dependencies:** U1.
- **Files:** `cmd/rocketclaw/CHEATSHEET.md`, `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/SKILL.md`, `internal/rocketclaw/backend/definitions_test.go`.
- **Approach:** Show the key under `permission.rocketclaw`; document the default and its separation from nested grants, without changing other RocketClaw tool auto-allows.
- **Tests:** Loaded agent with `auto(custom-agent)` retains the rule; omitted key leaves entry allowed while nested permissions remain as configured.
- **Verification:** Agent-definition tests and documentation agree with runtime behavior.

## Verification Contract

- Run targeted RocketCode and RocketClaw permission tests, then `gofmt` on touched Go files, `go test ./...`, `make lint`, and `make test` from the workspace root. Check the source CLOC budget without editing `SOURCE_CLOC_BUDGET`.
- Inspect `jj diff --git` for unrelated files, error naming, unnecessary helpers, injected behavior nil guards, and reviewer bypasses.

## Definition of Done

- An explicit Code Mode approval decision precedes every script execution, with default allow when unset; nested checks still apply.
- Reviewer failures fail closed; regression tests cover default, built-in, custom, and nested-denial paths.
- Docs describe the public agent configuration; required verification passes; experimental or abandoned code is removed.
