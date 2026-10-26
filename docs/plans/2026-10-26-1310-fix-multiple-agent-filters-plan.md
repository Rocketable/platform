---
title: "fix: Preserve multiple agent search filters"
date: 2026-10-26
type: fix
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Preserve multiple agent search filters

## Goal Capsule

**Objective:** Search for conversations belonging to any of several agents.
**Means:** Keep repeated agent terms and match them as alternatives, like room terms.
**Authority:** Multiple agent filters replace the existing single-agent behavior.
**Execution:** Extend existing tests first, then update the shared filter paths.
**Stop conditions:** Required verification is unavailable or another change already delivers this behavior.

## Product Contract

### Requirements

- R1. Selecting another agent retains earlier agent filters, including while the first response is pending.
- R2. Repeated agent terms match any listed agent, with exact, case-sensitive values. Other kinds of filters still narrow the results.
- R3. Removing one agent pill leaves the other terms intact. Saved queries and links retain all agent terms.
- R4. The shared search behavior applies to message search and the session palette without changing visibility or ordering.

### Scope Boundaries

No new syntax, query fields, API, persistence format, dependencies, or permission changes. Do not change other repeated-filter semantics.

## Assumptions

Multiple agents mean alternatives, following existing multiple-room behavior. Requiring one conversation to belong to all agents would make the feature unusable.

## Planning Contract

### Key Technical Decisions

- KTD1. Delete agent-specific replacement and its now-unused response callback queue from `internal/rocketclaw/web/src/ui.tsx`; use the existing append path for every suggested filter.
- KTD2. Extend `SidebarChat.passes` in `internal/rocketclaw/backend/session_search.go` with an independent agent alternative group, alongside the existing room group. Preserve every other filter and sort rule.
- KTD3. Update existing browser and backend table cases, and the Web README. Use synthetic agent values only.

### High-Level Technical Design

The existing flow stays the same: query and pills → server parsing → independent room and agent alternative groups plus remaining conjunctive filters → ordered results. No additional layer is introduced.

## Implementation Units

### U1. Keep and match multiple agent filters

**Requirements:** R1–R4, using KTD1–KTD3.
**Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/search-page.browser.test.ts`, `internal/rocketclaw/backend/session_search.go`, `internal/rocketclaw/backend/session_search_test.go`, `internal/rocketclaw/web/README.md`, rebuilt `internal/rocketclaw/internal/web/dist`.
**Execution note:** Run updated browser and backend expectations against unchanged production code and observe the expected failures before implementing.
**Test scenarios:** Pick two different agents while the first request is held; both pills and saved query remain. Remove either pill independently. Repeated typed agents return the ordered union; an unknown agent does not mask a valid one; duplicate terms do not duplicate results; quoted values work. Combined room, tag and agent filters still intersect. A message-text match from either selected agent remains visible.

## Verification Contract

Rebuild the tracked Web bundle before browser checks. Run focused browser and backend cases, Web lint and the complete Web test target. Run gofmt on touched Go files, `go test ./...`, `make lint`, and `make test`, keeping coverage and source budgets unchanged. Use a dedicated Docker PostgreSQL 18 container and scratch files only under `.tmp/`. Inspect all callers of the changed helpers and the final diff.

## Definition of Done

Both selected agents remain in the query, search returns their union narrowed by other filters, regressions pass, documentation and bundle match source, and independent review has no blocking finding.
