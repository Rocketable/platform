# Slack Thinking Streams Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stream eligible human Slack thinking messages without changing the separate answer placeholder, and render links on every MCP message surface.

**Architecture:** Extend the existing in-memory placeholder slots with stream metadata. Human event handlers reserve a native thinking stream before posting the normal answer placeholder; recipient-less paths keep their task cards. MCP requests use `mrkdwn`, while task-card details convert only Slack HTTP(S) link markup into native rich-text links.

**Tech Stack:** Go 1.26.2+, `github.com/slack-go/slack`, Slack Web API, `testify`, `httptest`, Jujutsu.

## Global Constraints

- Preserve the separate thinking message and answer placeholder (`\u200B`) and their creation order.
- Do not stream partial answers.
- Do not add configuration, persisted recipient state, dependencies, packages, or exported symbols.
- Keep External MCP, cron, automation, and recovery on the existing task-card path when no human recipient is available.
- Do not commit unless the human partner explicitly requests a commit.
- Do not modify or revert the concurrent emergency-safe-word changes.

---

### Task 1: Render MCP Links

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:526,839-860,1035-1060`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`

**Interfaces:**
- Consumes: existing `slackMCPBlockMessages`, `slackThinkingBlocks`, and `slack.RichTextSectionElement`.
- Produces: private `slackRichTextElements(string) []slack.RichTextSectionElement` for literal text plus native HTTP(S) links.

- [ ] **Step 1: Write failing MCP request and thinking-link tests**

Add assertions that an MCP request section has `"type":"mrkdwn"` and retains `<https://example.com|Example>`. Add a direct rendering test using `slackThinkingBlocks` and JSON decoding that expects:

```json
{"type":"text","text":"See "}
{"type":"link","url":"https://example.com","text":"Example"}
{"type":"text","text":" now"}
```

- [ ] **Step 2: Run the focused tests and confirm failure**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run 'Test(SendExternalMCPRelayRendersLinks|SlackThinkingBlocksRenderLinks)' -count=1
```

Expected: request body is `plain_text`, and thinking details contain one literal text element.

- [ ] **Step 3: Implement the minimal rendering changes**

Change the MCP request call to:

```go
messages := slackMCPBlockMessages("MCP request", relay.ExternalConversationID, relay.Agent, text, slack.MarkdownType)
```

In `slackThinkingBlocks`, create each section from `slackRichTextElements(line)...`. The parser scans for `<http://...>` and `<https://...>` tokens, emits surrounding literal text, and emits `slack.NewRichTextSectionLinkElement(url, label, nil)` for valid complete tokens. It leaves malformed tokens literal.

- [ ] **Step 4: Run focused tests**

Run the command from Step 2. Expected: PASS.

---

### Task 2: Reserve Human Thinking Streams

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:72-114,164-177,924-965,1290-1492,1601-1692,2169-2232`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`

**Interfaces:**
- Consumes: `slack.Client.StartStreamContext`, `AppendStreamContext`, `StopStreamContext`, and existing pending/reply slot maps.
- Produces: `slackReplySlots` fields `ThinkingStream bool` and `StreamedThinking string`; placeholder creation accepts an optional recipient user ID.

- [ ] **Step 1: Write a failing placeholder-order test**

Create a test server recording `/chat.startStream` and `/chat.postMessage`. Set `connector.teamID = "T123"`, reserve placeholders for user `U123`, and assert this order and payload:

```text
/chat.startStream channel=C123 thread_ts=111.222 recipient_team_id=T123 recipient_user_id=U123 markdown_text=_Thinking..._
/chat.postMessage channel=C123 thread_ts=111.222 text=\u200B
```

- [ ] **Step 2: Run the placeholder test and confirm failure**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run TestCreateReplyPlaceholdersStartsHumanThinkingStreamBeforeAnswer -count=1
```

Expected: current code calls `/chat.postMessage` twice.

- [ ] **Step 3: Implement stream reservation**

Store `auth.TeamID` beside `botUserID`. Add stream metadata to `slackReplySlots`. When both team and recipient user IDs are non-empty, call `StartStreamContext` with `MsgOptionTS`, `MsgOptionRecipientTeamID`, `MsgOptionRecipientUserID`, and `MsgOptionMarkdownText(placeholder)`, then post the answer placeholder. Otherwise retain the existing pair of `chat.postMessage` calls.

- [ ] **Step 4: Pass originating users at human reservation sites**

Pass `ev.User` from managed replies and app mentions. Pass the buffered message principal when promoting a human stack. Pass an empty recipient from MCP, cron, and connector-created fallback paths.

- [ ] **Step 5: Run the placeholder and existing reservation tests**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run 'Test(CreateReplyPlaceholders|SendExternalMCPRelayCreatesAnswerPlaceholder)' -count=1
```

Expected: PASS, including existing recipient-less behavior.

---

### Task 3: Append and Stop Human Streams

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:200-292,642-715,764-889`
- Test: `internal/rocketclaw/slackconnector/connector_test.go:1869-1951`

**Interfaces:**
- Consumes: stream metadata from Task 2.
- Produces: synchronous append-only thinking delivery and stream completion through the existing `SendResponse` worker.

- [ ] **Step 1: Replace the existing end-to-end thinking test with a failing stream lifecycle test**

Record `/chat.startStream`, `/chat.appendStream`, `/chat.update`, and `/chat.stopStream`. Reserve a human stream, send cumulative progress `first thought` then `first thought\nsecond thought`, send a partial answer, and complete with `Final answer`. Assert:

```text
startStream markdown_text=_Thinking..._
appendStream markdown_text=\n\nfirst thought
appendStream markdown_text=\nsecond thought
update answer-placeholder text=Final answer
stopStream thinking-timestamp
```

Assert that no partial answer is sent.

- [ ] **Step 2: Run the lifecycle test and confirm failure**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run TestSendResponseStreamsHumanThinkingBeforeFinalAnswer -count=1
```

Expected: current code schedules `chat.update` instead of appending or stopping a stream.

- [ ] **Step 3: Implement synchronous suffix appends**

For stream slots, require each cumulative `ProgressText` to extend `StreamedThinking`, append only the suffix with `AppendStreamContext`, and update the stored slot. Do not create a timer or `slackThinkingState` for stream slots. Recipient-less slots continue through `bufferProgressText` unchanged.

- [ ] **Step 4: Stop streams during completion and cleanup**

After final-answer delivery, call `StopStreamContext` for stream slots instead of completing a task card. Stop a partial stream before deletion when answer-placeholder creation fails, pending placeholders are cleaned up, or `AbortResponse` releases state.

- [ ] **Step 5: Run focused lifecycle, empty-answer, abort, and task-card tests**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run 'Test(SendResponseStreamsHumanThinkingBeforeFinalAnswer|SendResponseDeletesPlaceholdersForEmptyFinal|AbortResponse|SendResponseCreatesTaskCard)' -count=1
```

Expected: PASS; recipient-less task-card tests remain unchanged.

---

### Task 4: Format, Standards Review, and Full Verification

**Files:**
- Review: `internal/rocketclaw/slackconnector/connector.go`
- Review: `internal/rocketclaw/slackconnector/connector_test.go`
- Review: `internal/rocketclaw/docs/adr/0002-behavior-contracts.md`

**Interfaces:**
- Consumes: completed Tasks 1-3.
- Produces: formatted, lint-clean, fully verified change.

- [ ] **Step 1: Run `gofmt`**

```sh
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
```

- [ ] **Step 2: Review the actual Go diff against project standards**

Run `jj diff --git` and remove single-use one-line wrappers, defensive guards, unnecessary exported symbols, non-`err...` error variable names, new context storage, and unrelated edits. Confirm answer-placeholder order, recipient-less task cards, MCP Blocks, queue order, and stream stop ordering.

- [ ] **Step 3: Run all required verification**

```sh
go test ./...
make lint
make test
```

Expected: all commands exit successfully, including CLOC and coverage budgets.

- [ ] **Step 4: Inspect final repository state**

```sh
jj status
jj diff --git
```

Confirm only approved ADR, connector, tests, design, plan, and ignored progress-log changes are attributable to this work. Do not alter concurrent user changes.
