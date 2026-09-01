---
title: "Drop Emoji Message Commands - Plan"
type: refactor
date: 2026-09-01
topic: drop-emoji-message-commands
artifact_contract: unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop Emoji Message Commands - Plan

## Goal Capsule

- **Objective:** A Slack human starts text commands with `$goal`, `$stop`, `$cron`, `$workflow`, or `$agent`. Typing the old emoji prefix no longer runs that command. Reacting with the existing control emojis still does the same work.
- **Means:** Stop translating leading emoji and Slack colon aliases in message text into dollar commands. Leave reaction handlers unchanged. (KTD1)
- **Product authority:** Product Contract below is source of WHAT. Planning Contract is HOW.
- **Product Contract preservation:** n/a (ce-plan-bootstrap)
- **Open blockers:** None.
- **Stop conditions:** AE1–AE4 have tests. `gofmt` on touched files. Touched-package tests, `make lint`, and `make test` pass.

## Product Contract

### Summary

Drop the emoji-prefix and colon-alias counterparts of Slack dollar commands on user-initiated messages. Dollar commands stay. Reaction emoji workflows stay.

### Problem Frame

Dollar commands are canonical, but message text still accepts emoji prefixes (`🏁`, `🔁`, `🛑`, `⏹️`, `🔂`, `⏩`, `🎛`) and Slack colon aliases as the same commands. Humans should type `$…`. Reactions are a different control surface and already work.

### Key Decisions

- Drop user-initiated emoji-prefix message commands (session-settled: user-directed — chosen over keeping both prefixes: users should use the dollar command). Governs R1.
- Keep reaction emoji workflows (session-settled: user-directed — chosen over changing reactions too: only user-initiated emoji-prefix messages are in scope). Governs R2.

### Requirements

- R1. A Slack message whose text starts with a former command emoji or its colon alias is not a command.
- R2. Adding 🛑 / ⏹️ to a message still stops, cancels, or drops the same way as today. Adding ⏫ to a live queued envelope during an active turn still converts it to a Slack Steer.
- R3. `$goal`, `$stop`, `$cron`, `$workflow`, `$agent`, `$enqueue`, and `$queue` keep today's message-text behavior.
- R4. Live command help and `cmd/rocketclaw/CHEATSHEET.md` do not teach emoji prefixes or colon aliases as commands.

### Key Flows

- F1. Message command
  - **Trigger:** Authorized human sends `$goal ship it` or `🏁 ship it`.
  - **Steps:** Only the dollar form parses as a command. The emoji form is ordinary message text.
  - **Covered by:** R1, R3
- F2. Reaction control
  - **Trigger:** Authorized human adds 🛑 to thinking, or ⏫ to a live envelope during an active turn.
  - **Steps:** Existing reaction handlers run. No change.
  - **Covered by:** R2

### Acceptance Examples

- AE1. `$goal ship it` still starts a goal. `🏁 ship it`, `🔁 ship it`, and `:checkered_flag: ship it` do not. Covers R1, R3.
- AE2. `$stop` still stops the active managed turn. A message whose text is `🛑` or `:octagonal_sign:` does not. Adding 🛑 to thinking still stops. Covers R1, R2, R3.
- AE3. `$cron daily`, `$workflow name`, and `$agent name` still dispatch. `🔂 daily`, `⏩ name`, and `🎛 name` do not. Covers R1, R3.
- AE4. CHEATSHEET and in-product help no longer say RocketClaw translates emoji or colon aliases into dollar commands. Covers R4.

### Scope Boundaries

- Reaction handlers, Slack Message Menu, bot-posted markers (🤖 ✅ ❗ ⏳ ✉️ 📨 📡 🏁-in-goal-headers 🔁-in-cron-headers), and `$enqueue` / `$queue` stay.
- Do not add an error or help reply for leftover emoji-prefix text.
- Do not rewrite historical plans or specs.

## Assumptions

- Slack colon aliases (`:checkered_flag:`, `:repeat:`, `:octagonal_sign:`, `:stop_button:`, `:repeat_one:`, `:repeat-one:`, `:fast_forward_button:`, `:control_knobs:`) are the same counterparts as the glyphs and drop with them.
- After the drop, those messages are ordinary user text. No new rejection path.
- Emoji glyphs beside dollar commands in the help table may stay as labels, not as "type this."
- `$enqueue` has no message-text emoji prefix today. ✉️ stays a marker.

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete message-text emoji translation.** Remove emoji and colon-alias branches from `canonicalSlackCommand`. If that leaves a one-line wrapper around `slackDollarCommand`, inline it at `parseCanonicalSlackCommand`. Delete `canonicalizeLeadingSlackEmoji` and `slackEmojiGlyph` when unused. Keep reaction name constants. (session-settled: user-directed — chosen over keeping both prefixes: users should use the dollar command). Governs R1.
- KTD2. **No rejection shim.** Former emoji-prefix messages follow the ordinary non-command path. Do not add validation, help, or error copy for them.

### Patterns to follow

- Message parse: `canonicalSlackCommand` / `parseCanonicalSlackCommand` / `slackDollarCommand` in `internal/rocketclaw/frontend/slack/connector.go`.
- Reactions: `handleReactionAddedEvent` in the same file. Do not touch it except to keep tests green.
- Removal discipline: delete alias call sites, tests that asserted alias dispatch, and live docs that teach the alias. Do not add unknown-field-style rejection tests.

## Implementation Units

### U1. Drop message-text emoji command aliases

- **Goal:** Only `$` message text parses as a Slack command.
- **Requirements:** R1, R2, R3. Instantiates KTD1, KTD2.
- **Dependencies:** none
- **Files:** `internal/rocketclaw/frontend/slack/connector.go`, `internal/rocketclaw/frontend/slack/slack_emoji.go`, `internal/rocketclaw/frontend/slack/connector_test.go`
- **Approach:**
  1. Strip emoji and colon-alias translation from message command parsing per KTD1.
  2. Update `TestCanonicalSlackCommand`: keep dollar and ordinary-text rows; former alias rows become `ok: false` or are deleted.
  3. In behavioral tables, delete emoji/colon message subcases. Keep the dollar subcase. Do not add new error-path tests for leftover emoji text.
  4. Leave `handleReactionAddedEvent` tests unchanged.
- **Execution note:** Prefer deleting alias coverage over adding parallel "emoji is ignored" tests. One parser contract update is enough for R1.
- **Test scenarios:**
  - `$goal ship it` still canonicalizes as a command. Covers AE1.
  - `🏁 ship it`, `:checkered_flag: ship it`, `🛑`, `🔂 daily`, `⏩ name`, and `🎛 name` are not commands. Covers AE1, AE2, AE3.
  - Existing `$stop` / `$cron` / `$workflow` / `$agent` message tests still pass. Covers AE1–AE3.
  - Existing 🛑 and ⏫ reaction tests still pass. Covers AE2 / R2.
- **Verification:** Parser and message-event tests match AE1–AE3. Reaction tests still pass.

### U2. Stop teaching emoji prefixes as commands

- **Goal:** Live help no longer presents emoji prefixes as something a human types.
- **Requirements:** R4.
- **Dependencies:** U1
- **Files:** `cmd/rocketclaw/CHEATSHEET.md`, `internal/rocketclaw/frontend/slack/connector.go`, `internal/rocketclaw/frontend/slack/connector_test.go`, `cmd/quickbench/README.md`, `docs/solutions/best-practices/slack-bare-cron-lists-channel-jobs.md`
- **Approach:**
  1. Remove the CHEATSHEET claim that RocketClaw translates emoji and Slack aliases into dollar commands. Drop goal examples and scenario notes that teach `🏁`, `🔁`, `🔂`, `🎛` as typed commands. Keep reaction and marker rows.
  2. Help-table tests may keep emoji glyphs as labels next to dollar commands.
  3. Replace the quickbench `🎛` start path with bare `$agent`. Update the bare-cron learning so it does not teach `🔂` as a live command.
- **Test scenarios:**
  - Help table still documents dollar commands. Covers AE4.
  - CHEATSHEET does not say emoji or colon aliases dispatch. Covers AE4.
- **Verification:** AE4 holds. No remaining live doc tells a human to type the old prefixes.

## Verification Contract

- `gofmt` on touched files
- `go test ./internal/rocketclaw/frontend/slack -count=1`
- `make lint`
- `make test`

## Definition of Done

- U1 and U2 verification fields observed
- Reaction stop and ⏫ convert-to-steer still work
- Dollar commands still work
- README: no root README update (CHEATSHEET is the command surface; quickbench README only if it still teaches `🎛` as a typed command)
