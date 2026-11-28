---
title: Accept Only the Current Session Link Spelling - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Accept Only the Current Session Link Spelling - Plan

## Goal Capsule

- **Objective:** A chat, OpenCode session or routine link opens its page only when it uses the spelling RocketClaw writes today. Any other link, a garbled one included, ends at "not found" (or names no session) and never breaks the page or the server.
- **Means:** The three link decoders accept only unpadded URL-safe base64 and read anything else as an ID no session has (KTD1, KTD2).
- **Authority:** Owner-approved decision below, then this plan, then repository `AGENTS.md`.
- **Execution profile:** One pull request, one commit. Lightweight.
- **Stop conditions:** Stop and report if any writer in the product still produces a raw, percent-encoded or padded ID, or if either source CLOC (Go or TypeScript) does not go down.
- **Finisher:** The implementing agent ships the PR and waits for CI; merging stays with the owner.

---

## Product Contract

### Summary

Delete the code that still accepts the old spellings of session IDs in `/s/`, `/oc/` and `/routines/` links, keep one safe path for garbled links, and update the tests that only covered the old spellings.

### Problem Frame

Links name a session by its ID in unpadded URL-safe base64. That is what `tabPathSQL`, `threadBridgeManager.WebURL`, the relay, the mention notifications and the Web's `encodeSessionId` write. Older versions wrote the raw ID, a percent-encoded ID or padded base64, so three decoders still try each form in turn: `TabConversation` in `internal/rocketclaw/backend/store_dao.go`, `decodeSessionID` in `internal/rocketclaw/backend/attention.go`, and `decodeSessionId` in `internal/rocketclaw/web/src/session-id.ts`. Nothing writes those forms any more, so the fallbacks are dead weight, and they make a raw ID typed into a URL open a real session.

### Requirements

**Server**

- R1. `TabConversation` resolves a `/s/` or `/oc/` path only when its ID is unpadded URL-safe base64. Raw, percent-encoded, padded and standard-alphabet IDs no longer resolve to their session.
- R2. A routine run marks a `/routines/<id>` tab only when `<id>` is the unpadded URL-safe base64 of the stem's Latin-1 bytes, which is how the Web spells it.

**Web**

- R3. `decodeSessionId` accepts only unpadded URL-safe base64.

**Garbled links**

- R4. A garbled ID never throws or crashes. Where the server checks that a session is visible it answers not found. The Web opens it as a session that does not exist ("This chat doesn't exist.", or no routine of that name).

**Writers and tests**

- R5. No writer in Go or TypeScript produces an old spelling (Slack link text and notifications included).
- R6. Tests that only covered old spellings are updated or deleted. Each decoder keeps a test of the current form and of a garbled link, through that same decoder.
- R7. Go source CLOC and TypeScript source CLOC both go down.

### Key Decisions

- KD1. Accept only the current spelling. (session-settled: user-approved — chosen over keeping the raw, percent-encoded and padded fallbacks: no writer produces them today.) Governs R1, R2, R3.
- KD2. Garbled links fail safely and are tested. (session-settled: user-approved — chosen over dropping every check: decoding a garbled path from a URL must not throw or crash.) Governs R4, R6.

### Scope Boundaries

- The SQL that rewrites a saved tab path to its session's canonical path (`COALESCE(tabPathSQL, path)`) stays. It still corrects `/s/` versus `/oc/` for a session's kind.
- Saved tabs, shared tabs and list rows that an old version stored under an old spelling are not migrated. They now behave like links to a session that does not exist. The owner approved this.
- The `/server/<base64>/session/...` link and OpenCode `/oc/<server>/api/` proxy paths are unrelated and unchanged.

#### Deferred to Follow-Up Work

- None.

### Success Criteria

- An old-spelling or garbled link to an existing session shows not found on the Web and gets `NotFound` from the tab RPCs. The current spelling opens the session as before.

---

## Planning Contract

### Key Technical Decisions

- KTD1. Each decoder makes exactly one call that accepts the current form. (session-settled: user-approved — chosen over keeping percent-unescaping, padding repair and the raw fallback: nothing writes those forms.) Go uses `base64.RawURLEncoding.DecodeString` on the text after the prefix, which already refuses `=`, `+`, `/`, `%` and a bad length. The Web uses one regular expression that admits only base64url characters in a length `atob` can decode, then `atob` after mapping `-_` to `+/`. With that guard `atob` cannot throw, so no `try`/`catch` is needed.
- KTD2. A garbled ID decodes to an ID no session has, not `""`: `"\uFFFD"` in `TabConversation`, and `"\u00ff"` in the Web's `decodeSessionId`, because the Web turns the ID back into a path with `btoa`, which throws on characters outside Latin-1. Callers read `""` as "this path is not a session": `changeTabs`, `addToList` and `changeAttention` in `internal/rocketclaw/frontend/rpc/tabs.go` would skip the visibility check, `ShareTab` would share the garbled path, and the Web would show a blank new chat. `"\uFFFD"` is a non-empty ID no conversation can have. It keeps the not-found result the old raw fallback gave and matches how `TabConversation` already replaces invalid UTF-8 bytes. `decodeSessionID` returns `""` on a decode error because its only use compares the result to a non-empty routine stem.
- KTD3. `decodeSessionID` keeps the byte-to-rune (Latin-1) mapping. Routine stems such as `marketing · 30m` contain `·`, and the Web encodes them with `btoa`, which works on Latin-1 bytes.
- KTD4. `TabConversation` keeps `strings.ToValidUTF8`, because a valid base64 ID can still decode to bytes that are not UTF-8, which Postgres text refuses.

### Assumptions

- A1. The tool descriptions agents read (`rocketclaw_navigate_for_me` in `internal/rocketclaw/backend/session_tools.go` and `internal/rocketclaw/skel/builtin/about.md`) say `/s/<id>`, which can lead an agent to write a raw ID that used to work. Their wording changes to say the ID is base64url, with no new lines, and so do the `rocketclaw_navigate_for_me` paragraph in `README.md` and the `path` and `paths` field comments in `web.proto` that `/rpc_mcp` publishes. This is the only writer-side change.
- A2. Browser `localStorage` values that an old Web version stored with raw IDs are left alone. They now open a session that does not exist.

### Writers verified (R5)

- Go: `tabPathSQL` (`store_dao.go`, unpadded, `-_`), `threadBridgeManager.WebURL` and the thread start URL (`thread_bridges.go`, `RawURLEncoding`), used by Slack link text in `frontend/slack/connector.go`, `bridge.go` and `session_tools.go`; `frontend/rpc/relay.go`, `frontend/rpc/session_mentions.go` (mention notifications) and `frontend/rpc/server.go` (`AttentionSent`), all `RawURLEncoding`; `internal/harnessfeedback/run.go` (`RawURLEncoding`). Migration `088_web_top_shelf_rows.sql` uses the same SQL as `tabPathSQL`.
- Web: `sessionPath`, `rowPath` and `routinePath` in `web/src/ui.tsx` all use `encodeSessionId`, which strips `=` and maps `+/` to `-_`.

---

## Implementation Units

### U1. Server decoders

- **Goal:** `TabConversation` and `decodeSessionID` accept only the current spelling.
- **Requirements:** R1, R2, R4, R7.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/backend/store_dao.go`, `internal/rocketclaw/backend/attention.go`, `internal/rocketclaw/backend/store_test.go`, `internal/rocketclaw/backend/attention_test.go`, `internal/rocketclaw/frontend/rpc/tabs_test.go`, `internal/rocketclaw/frontend/rpc/reads_test.go`.
- **Approach:**
  1. In `TabConversation`, drop `url.PathUnescape`, the `+/` replacement and the `=` trim. Decode with `base64.RawURLEncoding.DecodeString`. On error return `"\uFFFD"` (KTD2), otherwise keep `strings.ToValidUTF8` (KTD4).
  2. In `decodeSessionID`, drop the unescape, the whitespace stripping and the padding repair. Decode with `base64.RawURLEncoding.DecodeString`, return `""` on error, and keep the Latin-1 mapping (KTD3).
  3. Remove any imports this leaves unused.
- **Patterns to follow:** The writers' `base64.RawURLEncoding.EncodeToString`.
- **Test scenarios:**
  - `store_test.go`: sharing, closing and listing tabs that used `/s/a` (raw for `a`) now use `/s/YQ`. The assertion messages that describe "a legacy spelling" or "another spelling" drop that clause.
  - `tabs_test.go`: the saved state and the park request use `/s/YQ` instead of `/s/a`. `/s/_w` (valid base64, invalid UTF-8) stays as a session that does not exist. The share code map gains `/s/a` → `NotFound`: a garbled link to an existing session `a` is not found.
  - `reads_test.go`: viewing `x` uses `/s/eA`. The "legacy spelling" wording goes.
  - `attention_test.go` (`TestRoutineRunEndMarksRoutineTabs`): drop the percent-encoded and raw routine paths. Share the current-form routine path, and give bob only `/routines` so his mark comes from the shared tab. Carol's `/routines/daily` stays as a garbled path that is never marked.
- **Verification:** The touched packages pass through `coverage-profile`.

### U2. Web decoder

- **Goal:** `decodeSessionId` accepts only the current spelling and reads a garbled one as a session that does not exist.
- **Requirements:** R3, R4, R6, R7.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/web/src/session-id.ts`, `internal/rocketclaw/web/src/session-id.test.ts`, `internal/rocketclaw/web/src/session-list-app_test.ts`, `internal/rocketclaw/web/src/revert-transport.test.ts`, `internal/rocketclaw/internal/web/dist/` (regenerated).
- **Approach:** Replace the body with the KTD1 guard plus `atob`, returning `"\u00ff"` when the guard fails (KTD2). Tests that built padded links with bare `btoa(...)` (`session-list-app_test.ts` `nope`, `revert-transport.test.ts` `chat`) strip the padding. Regenerate the embedded dist with `go -C internal/rocketclaw generate ./internal/web`.
- **Test scenarios:**
  - The round-trip test of an ID with colons and slashes stays, with a placeholder ID.
  - The percent-encoded test is replaced by one garbled-link test: a bad length (`a`), padding (`YQ==`) and a raw ID with a colon each decode to `"\u00ff"`.
  - `long-chat.browser.test.ts`: `/s/a`, `/s/YQ==`, a raw `/s/` ID and `/oc/a` show the missing-chat and missing-OpenCode-session messages, and opening a stored `/s/a` tab does too, with no page error.
- **Verification:** `make -C internal/rocketclaw/web test-files` for the session-id unit test and the browser tests that build session links (`notifications.browser.test.ts`, `listing-ring.browser.test.ts`, `long-chat.browser.test.ts`, and `session-list.browser.test.ts`, which runs `session-list-app_test.ts`). `revert-transport.test.ts` runs from `TestRevertRealBackendBrowser` in the `./frontend/rpc` package run. `make -C internal/rocketclaw/web lint` passes.

### U3. Agent-facing link wording

- **Goal:** The text agents read names the current spelling (A1).
- **Requirements:** R5.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/backend/session_tools.go`, `internal/rocketclaw/skel/builtin/about.md`.
- **Approach:** Change `/s/<id>` and `/oc/<id>` to say the ID is base64url, on the existing lines.
- **Test expectation:** none -- wording only; no test asserts this text.

---

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Go lint | `make -C internal/rocketclaw lint` |
| Go tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend ./frontend/rpc"` |
| Web lint | `make -C internal/rocketclaw/web lint` (react-doctor and TS budget) |
| Web tests | `bun install` in `internal/rocketclaw/web`, then `make -C internal/rocketclaw/web test-files TESTS="src/session-id.test.ts src/notifications.browser.test.ts src/listing-ring.browser.test.ts src/long-chat.browser.test.ts src/session-list.browser.test.ts"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, and `make -C internal/rocketclaw cloc` and `make -C internal/rocketclaw/web cloc` before and after: Go source below 45230 and TypeScript source below 11849 |
| CI | The PR's CI is green |

## Definition of Done

- R1 to R7 hold, and every gate above passes.
- The regenerated `internal/rocketclaw/internal/web/dist/` is committed with the Web change.
- No abandoned attempt, comment or unused import is left in the diff, and the PR text names no internal deployment, person or channel.
