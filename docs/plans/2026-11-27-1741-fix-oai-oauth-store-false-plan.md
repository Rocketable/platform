---
title: Always Send store false from the ChatGPT OAuth Transport - Plan
type: fix
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Always Send store false from the ChatGPT OAuth Transport - Plan

## Goal Capsule

- **Objective:** every Responses request that RocketClaw sends with a ChatGPT sign-in asks the backend not to store the response, whatever the caller set.
- **Means:** `cleanCodexRequest` in `internal/rocketclaw/oai/oauth.go` overwrites `store` with `false` (KTD1).
- **Authority:** the owner's rule in the invoking request, then this plan, then the repository's `AGENTS.md`.
- **Execution profile:** one PR, one unit.
- **Stop conditions:** stop and report if the change needs edits to `cleanCodexInputItem`, to the compaction retry's kept compaction ID, or to any package other than `oai`, or if it cannot stay in hunks separate from the open OAuth cleanup PR on the same file.
- **Finishing:** the implementing agent builds, verifies, and opens the PR. The owner merges.

---

## Product Contract

### Summary

The ChatGPT OAuth transport sets `store` to `false` on every Responses request body it cleans. Its own compaction request already does this.

### Problem Frame

The transport passes through whatever `store` value the caller sent. Every current caller sends `false`, but nothing enforces it, so a new caller could send `true` or leave `store` out. The owner's rule is that store must always be false.

### Requirements

- R1. Every Responses request the transport sends carries `"store": false`: the initial request, the retry after a 401, the retry after compaction, and the request built from a `/responses/compact` call.
- R2. A request that set `store` to `true`, or left it out, goes out with `store` set to `false`.
- R3. Input items are cleaned by the existing rules for the `store` value actually sent (`false`), never by the caller's original value.

### Key Decisions

- **Store is always false.** (session-settled: user-directed — chosen over passing the caller's value through: the owner's rule is "Store must always be false.") Governs R1, R2.

### Scope Boundaries

- Not changing `cleanCodexInputItem` or the compaction retry, which keeps the compaction item's ID.
- Not removing the `store` read or the `stripIDs` parameter. The open OAuth cleanup PR on the same file does that, and this change must not conflict with it.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Set `body["store"]` to `false` in `cleanCodexRequest`, right after the `instructions` default and before the `store` read, and mark the body changed only when the raw value was not already `false`.** `cleanCodexRequest` cleans every body the transport sends: the initial request, the 401 retry (which re-reads the cleaned body), and the compaction retry (which re-marshals the cleaned map). The compaction request already hard-codes `false`. Placing the write before the `store` read means the existing read sees `false`, so IDs are stripped to match (R3) with no edit to the stripping code. It also keeps this hunk apart from the open OAuth cleanup PR, which deletes the `store` read a few lines below. Checking the raw value keeps bodies that already say `false` byte-for-byte unchanged. (session-settled: user-directed — chosen over a wider change: the owner asked for the smallest change, about two lines.)
- KTD2. **Delete `TestStripInputIDsKeepsIDsWhenStoreTrue`.** It asserts that a `store: true` request keeps item IDs, which R2 and R3 remove. The open OAuth cleanup PR deletes the same lines, so the two PRs merge cleanly.

### Assumptions

- `TestCleanCodexRequestPreservesUntouchedBodies` cases with no `store` field need `"store":false` in their fixtures to stay unchanged bodies. That keeps the test's meaning: an already clean body is not rewritten.

---

## Implementation Units

### U1. Force store false in the request cleanup

- **Goal:** R1, R2, R3.
- **Requirements:** R1, R2, R3, KTD1, KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/oai/oauth.go`, `internal/rocketclaw/oai/oauth_test.go`.
- **Approach:**
  1. Add the `store` write in `cleanCodexRequest` per KTD1.
  2. Add one test that cleans a request with `store: true` and one with no `store`, and decodes the sent body into a typed struct with a raw JSON `store` field.
  3. Add `"store":false` to the `PreservesUntouchedBodies` fixtures that lack it, and delete `TestStripInputIDsKeepsIDsWhenStoreTrue` per KTD2.
- **Test scenarios:**
  - A body `{"store":true,"input":[]}` goes out with `store` present and `false`.
  - A body `{"input":[]}` goes out with `store` present and `false`.
  - The existing `TestStripInputIDsRemovesIDsWhenStoreFalse` and compaction tests keep passing unchanged.
- **Verification:** the new test fails without the `oauth.go` change and passes with it.

---

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./oai"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, source CLOC baseline 45253 |

## Definition of Done

- U1 lands as one commit in one PR, with every gate above passing.
- R1 to R3 are rechecked against the diff.
- The diff does not touch `cleanCodexInputItem`, the compaction retry, or the lines the open OAuth cleanup PR changes, apart from the identical test deletion.
- No leftover code from abandoned attempts.
