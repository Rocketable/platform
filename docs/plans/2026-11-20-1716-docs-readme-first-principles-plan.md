---
title: README First-Principles Rewrite - Plan
type: docs
date: 2026-11-20
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# README First-Principles Rewrite - Plan

## Goal Capsule

- **Objective:** A person who opens the repository learns what Rocketable Platform is, how to get RocketClaw running, and how to configure each part, from a README short enough to read in one sitting.
- **Means:** Rewrite `README.md` concisely in reader order, describing only current behavior (KTD1, KTD2).
- **Authority:** This plan, then `AGENTS.md`, then the current code for any fact the README states.
- **Execution profile:** One PR, one commit. Documentation only.
- **Stop conditions:** Stop if a statement cannot be confirmed from the code or the previous README; leave it out rather than guess.

---

## Product Contract

### Summary

Replace the long README with a concise one: what this is, how it works, requirements, a quick start, commands, then a short reference per subject. Deleted and deprecated behavior is not documented.

### Problem Frame

The README grew one feature at a time to about 700 lines and 14,700 words. It opened with configuration internals before saying what an agent is, had no requirements or example config, and mixed release notes and legacy behavior into the reference.

### Requirements

- R1. The README opens with what the project is, then the existing experimental-software and warranty notice, unchanged in wording.
- R2. Requirements, a Quick start with a minimal valid `rocketclaw.json`, and Commands come before the reference.
- R3. Each subject (configuration, web page, agents, Slack/MCP/schedules, models, storage, monitoring, `funneld`, development) gets a short section with the settings an operator sets and the caveats that protect them: security, cost, privacy, and data loss.
- R4. The README describes only current behavior: no release notes, upgrade history, legacy names, or "no longer" text.
- R5. Every anchor another tracked file links to still resolves: `#voice`, `#sticky-agents`, `#shadow-agents`, `#remote-rocketclaws`, `#space-permissions`, `#behavior-settings`, and the "Emulated shell" heading named in `internal/rocketclaw/sandbox/runtime_linux.go`.

### Scope Boundaries

- No code changes, and no new documentation files. Fine-grained behavior is left to the code and the Web README.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Concise over exhaustive.** The owner asked for a concise README; detail that only matters inside a feature is dropped, but safety caveats stay (R3).
- KTD2. **Current behavior only.** The owner asked not to document deleted or deprecated behavior, so upgrade notes and legacy names are removed (R4).
- KTD3. **Quick-start facts come from code.** The minimal config uses only fields `Config.Validate` in `internal/rocketclaw/config/config.go` requires; commands match `helpText` in `cmd/rocketclaw/main.go`.

---

## Implementation Units

### U1. Rewrite README.md

- **Goal:** Produce the concise README per R1–R5.
- **Files:** `README.md`.
- **Test expectation:** none -- documentation only.
- **Verification:** see below.

---

## Verification Contract

| Check | How |
|---|---|
| Accuracy (R3) | Each statement traced to the previous README or the code; independent review for wrong or compressed-wrong statements |
| Current behavior only (R4) | No release notes, legacy names, or "no longer" text remain |
| Anchors (R5) | Script checks every `README.md#…` link in tracked files and every in-page link |
| Quick start (R2) | Scratch program loads the Quick start file through `config.Load` and `Config.Validate` |

---

## Definition of Done

- U1 complete and its verification passes, as one commit.
