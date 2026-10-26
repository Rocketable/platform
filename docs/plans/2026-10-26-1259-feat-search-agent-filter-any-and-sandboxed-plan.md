---
title: Search Agent Filter Matches Any Agent and Sandboxed Runs - Plan
type: feat
date: 2026-10-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Search Agent Filter Matches Any Agent and Sandboxed Runs - Plan

## Goal Capsule

- **Objective:** On the Search page and in Cmd/Ctrl+P session search, a person can filter by several agents at once and find a chat by the agent that did hidden (sandboxed) work for it, such as a Cron or External MCP run.
- **Means:** `agent:` terms combine with OR like `room:` terms and also match the agents of the conversations whose entries a chat shows (KTD1, KTD2). The picker adds agent pills instead of replacing them (KTD3).
- **Authority:** Requirements R1–R4 win on behavior; KTDs win on mechanism.
- **Stop conditions:** Stop if matching hidden-run agents would show a chat whose hidden-run entries the person cannot otherwise see in that chat.
- **Execution profile:** One backend change (`internal/rocketclaw/backend`) and one web change (`internal/rocketclaw/web`). Implementation and verification happen locally; the calling pipeline ships it.

## Product Contract

### Summary

`agent:` becomes a "match any" filter. Several `agent:` terms keep a chat that matches any one of them. A chat matches an agent when that agent is its own agent or the agent of a hidden run whose entries it shows. Picking an agent from the suggestions adds a pill and keeps earlier agent pills.

### Problem Frame

Today `room:` terms match any listed room, but `agent:` terms must all match the chat's single agent, so two `agent:` terms never match anything. The picker also deletes the earlier `agent:` pill when you choose another one. A chat fed by hidden runs, such as Cron runs reporting to it or External MCP runs, shows those runs' messages under the "sandboxed" view. Searching for the agent that wrote them does not find the chat, because only the chat's own agent counts.

### Requirements

**Combining agents**

- R1. Several `agent:` terms keep a chat that matches at least one of them. All other filter keys keep their current rule: every term must match, and `room:` terms match any one.
- R2. Choosing an agent from the `agent:` suggestions, by click, arrows, Tab, or Enter, adds that agent's term and keeps the existing `agent:` terms, the same way rooms already behave.

**Sandboxed agents**

- R3. An `agent:X` term matches a chat when X is the chat's own agent or the agent of a conversation whose entries the chat shows: a hidden run that reports to it (Cron, External MCP) or the Cron run it continues.
- R4. Matching is exact and case-sensitive, as for the chat's own agent today.

### Key Decisions

- **`agent:` counts sandboxed-run agents too.** (session-settled: user-directed — chosen over "agents with `customShell`" and "comparison shadow agents": the user means the agent doing sandboxed work for a chat in External MCP and Cron sessions.) Governs R3.

### Scope Boundaries

- Free-text search's `Agent` field match (`fieldAgent`) stays on the chat's own agent only. Not built: the request covers the `agent:` filter, and free text already matches the origin card text, which names the Cron or External MCP agent.
- The suggestions list does not change. It already offers every primary agent from `ListAgents`, which includes the agents hidden runs use.
- Remote OpenCode sessions keep matching their selected agent. They have no hidden runs.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **A chat's sandboxed agents are the agents of the conversations it shows entries from.** That is the same relation `searchableSQL`'s `shown` uses in `internal/rocketclaw/backend/message_search.go`: conversations with `reports_to` equal to the chat, plus the conversation the chat `continues`. Using that relation keeps "matches agent X" in line with "shows X's sandboxed messages". The `ChatOriginFacts` agents (`ProducerAgent`, `Binding.Agent`) were considered, but they name only the run that created the chat, so a chat that later receives runs from other agents would be missed. Governs R3.
- KTD2. **Load sandboxed agents in one query, only when the query has an `agent:` term.** This follows the `modelAgents` and `messagePrincipals` patterns in `internal/rocketclaw/backend/session_search.go`: a `map[chat][]agent` read alongside the principals read in the existing errgroup, then passed to `SidebarChat.passes`. In `passes`, agent terms use the same any-match bookkeeping as room terms. Searches without an `agent:` term issue no extra query. Write the SQL as CTEs, with no subqueries (AGENTS.md).
- KTD3. **Agent picks use the room path in `usePills.pick`.** Delete the agent-only branch, which removed earlier terms by server offsets and waited for a later response. With it goes `useSessionSearch`'s `next` and `waiting` queue, whose only caller is that branch. This is a net deletion in `internal/rocketclaw/web/src/ui.tsx`.

### Assumptions

- Hidden Cron and External MCP runs bind to their chat through `reports_to` (`startTurnDB` and `bindProducer` in `internal/rocketclaw/backend/store_dao.go`), and Web chats that continue a Cron run use `continues`. A test fixture that links a run only through `cron_run`, or not at all, is fixed in the test to bind the way production does (see U1). That is not a reason to read `ChatOriginFacts`.

---

## Implementation Units

### U1. Backend: `agent:` matches any term and sandboxed-run agents

- **Goal:** `SearchSessions` keeps chats matching any `agent:` term, counting the chat's own agent and its sandboxed-run agents.
- **Requirements:** R1, R3, R4; KTD1, KTD2.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/backend/session_search.go`
  - `internal/rocketclaw/backend/session_search_test.go`
  - `internal/rocketclaw/backend/session_tools.go`
- **Approach:**
  1. Add a reader for chat → sandboxed agents (KTD1), run inside the existing errgroup only when `search.Terms` holds an `agent:` term (KTD2).
  2. Give `passes` the chat's sandboxed agents and switch `keyAgent` to the room-style any-match: a chat with agent terms passes when at least one matches its own agent or a sandboxed agent.
  3. Update the `passes` and `SearchSessions` doc comments to say agent terms match any one.
  4. Update the query-language sentence in the `rocketclaw_sessions_list` tool description, which currently says "Room terms match either room; other filters use AND". It should say that room and agent terms each match any one, that `agent:` also matches the agent of a hidden Cron or External MCP run the chat shows, and that other filters use AND. Agents write `criteria` from this text.
- **Patterns to follow:** `keyRoom` handling in `passes`; `modelAgents` (conditional load); `messagePrincipals` (map read with `readRows`).
- **Test scenarios:** Extend the table in `TestSearchSessionsReadsTermsAndFilters`. Leave `newSessionSearchFixture` unchanged: eight tests share it, and binding runs there changes principal, message-hit, and session-tool expectations. Instead, before the table in this test only, bind runs with direct updates the way production does, as `TestCheckSessionTarget` does in `session_tools_test.go`.
  - `agent:main agent:other` now returns the chats of both agents (today it returns none).
  - `room:support room:C9 agent:main agent:other` returns chats that match a room and either agent.
  - `agent:producer` returns `searchChatX`, the External MCP chat whose own agent is `main`, once `external_mcp:private` (agent `producer`) reports to it.
  - A Web chat continuing a Cron run whose run agent differs from the chat's agent matches `agent:<run agent>`. In the test, give the daily Cron run an agent other than `main` and link it to `searchChatD` through `continues` or `reports_to`. The fixture links them only through `cron_run`, which KTD1 does not read.
  - `agent:Producer` (wrong case) matches nothing.
  - Hidden runs themselves never appear as results; only the chats they report to do.
- **Verification:** The table passes, and searches without an `agent:` term issue no sandboxed-agent query (no test needed; check by reading the code).

### U2. Web: agent picks keep earlier agents

- **Goal:** Picking an agent adds a pill and keeps earlier `agent:` pills on both search surfaces.
- **Requirements:** R2; KTD3.
- **Dependencies:** none (U1 makes the result meaningful).
- **Files:**
  - `internal/rocketclaw/web/src/ui.tsx`
  - `internal/rocketclaw/web/src/session-list.browser.test.ts`
  - `internal/rocketclaw/web/src/search-page.browser.test.ts`
  - `internal/rocketclaw/web/README.md`
- **Approach:**
  1. In `usePills.pick`, write every key's term the same way (`change(head + term)`), removing the agent-only branch and its comment.
  2. Remove `next` and the `waiting` ref from `useSessionSearch` once nothing calls them.
  3. In the web README's search section, replace "Choosing an agent replaces earlier agent filters; choosing a room keeps earlier rooms. Multiple room filters match either room." with the new rule, and say `agent:` also matches the agent of a hidden Cron or External MCP run the chat shows. Also change "Other repeated filters use AND" so it excludes `room:` and `agent:`.
- **Patterns to follow:** the existing room-pick path in `usePills.pick`.
- **Test scenarios:**
  - In `session-list.browser.test.ts` ("room autocomplete keeps selections and full labels on each search surface"), after picking `agent:main` then `agent:other`, both pills are present on the palette and on `/search`. This replaces the `count() === 0` assertion for `agent:main`.
  - In the same test, the sent query contains both terms. Add the matching entry to the fake server's `results` table so the found rows reflect it.
  - In `search-page.browser.test.ts`, the agent-pick block that holds the first `agent:main` response and expects `agent:main` to be replaced instead expects both pills to stay and the stored query to be `agent:main agent:other `. Delete the `hold` gate, which only served the deleted in-flight replacement path.
- **Verification:** Both browser tests pass, and `make lint` (react-doctor) in `internal/rocketclaw/web` is clean.

---

## Verification Contract

| Check | Command | Applies to |
|---|---|---|
| Go format | `gofmt -l` on touched Go files | U1 |
| Backend search tests | `go test ./internal/rocketclaw/backend/ -run TestSearchSessions` | U1 |
| Full Go suite | `go test ./...` and `make test` at repo root | U1 |
| Go lint | `make lint` at repo root | U1 |
| Web lint | `make lint` in `internal/rocketclaw/web` | U2 |
| Web browser tests | `bun test src/session-list.browser.test.ts src/search-page.browser.test.ts` in `internal/rocketclaw/web` | U2 |

## Definition of Done

- R1–R4 hold, as shown by the U1 and U2 test scenarios.
- No `agent:`-specific replacement logic remains in `ui.tsx`, and `next`/`waiting` are gone if they have no callers.
- The web README describes the new `agent:` rule. The root README needs no change, because `criteria` already defers to the `/search` language.
- The touched Go diff passes the AGENTS.md Go standards pass (error names, no one-use helpers, no defensive guards), and the SQL uses CTEs with no subqueries.
- No experimental or abandoned code remains in the diff.
