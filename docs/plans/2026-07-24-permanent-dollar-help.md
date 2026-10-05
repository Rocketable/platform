# Permanent Dollar Help Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make command help a permanent Slack reply and let root command-help mentions establish a managed thread without creating an agent turn.

**Architecture:** Add a normal thread-registration method that persists/starts a managed thread without submitting inbound content. Root help registers with the channel's first configured agent and posts the existing table as the first reply; managed-thread help posts the same permanent reply. Neither the root command nor help enters agent history.

**Tech Stack:** Go 1.26.2+, Slack Web API, SQLite managed-conversation store, Jujutsu

## Global Constraints

- Help is permanent for every command-help outcome.
- Root help keeps the app mention as root and posts help as the first reply.
- Root help registers the first configured channel agent.
- Do not call `StartThread`, submit inbound content, or create session history for the root command/help.
- The next human reply is the first agentic turn.
- Existing help table content and context restrictions remain unchanged.

---

### Task 1: Register And Post Permanent Help

**Files:**
- Modify: `internal/rocketclaw/harnessbridge/primary_text_router.go`
- Modify: `internal/rocketclaw/app/thread_bridges.go`
- Test: `internal/rocketclaw/app/thread_bridges_test.go`
- Modify: `internal/rocketclaw/slackconnector/connector.go`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`
- Modify: `internal/rocketclaw/slackconnector/inert_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: `RegisterThread(events.TextConversationTarget, string) (created bool, err error)`
- Consumes: existing permanent Slack `chat.postMessage` and command-help table

- [ ] **Step 1: Write failing manager and connector tests**

Manager test: call `RegisterThread`, assert persisted agent, empty creator, zero session entries, and no bridge submission. Then submit a later reply and assert it is the first submission.

Connector tests: root `@agent $` and other root help outcomes assert one registration and one permanent `chat.postMessage` with `thread_ts` equal to the root timestamp and the existing table blocks. Assert no `StartThread`, ordinary reply submission, placeholders, reactions, or outbound bus messages. Managed-thread help asserts permanent `chat.postMessage`, not `chat.postEphemeral`.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/rocketclaw/app ./internal/rocketclaw/slackconnector -run 'Test(ThreadBridgeManagerRegistersThreadWithoutSubmitting|HandleAppMentionEventPostsPermanentDollarCommandHelp|HandleMessageEventPostsPermanentDollarCommandHelp)' -count=1`

Expected: FAIL because `RegisterThread` and permanent help behavior do not exist.

- [ ] **Step 3: Implement normal thread registration**

Add `RegisterThread` to `PrimaryTextRouter`. Return `created=false` without changing the persisted agent when the thread already exists. Otherwise use `ensureStartedThread` with empty `CreatedBy` and persist through the existing managed-thread store path. Do not enqueue or submit an inbound message.

- [ ] **Step 4: Post permanent help**

For managed-thread help, call `PostMessageContext` with fallback text, `MsgOptionTS(threadTS)`, and the existing table block.

For root help, post the permanent help with `thread_ts=ev.TimeStamp`, then call `RegisterThread` with `{ChannelID: ev.Channel, ThreadID: ev.TimeStamp}` and the selected first agent. Delete the posted help if registration fails or reports an existing thread. Return before placeholders or `StartThread`.

- [ ] **Step 5: Update interface stubs and README**

Implement `RegisterThread` in test stubs/inert routers. Update README's Slack initiation description to state that a root `$` help message registers the thread without becoming an agent turn.

- [ ] **Step 6: Verify and commit**

Run: `gofmt -w internal/rocketclaw/harnessbridge/primary_text_router.go internal/rocketclaw/app/thread_bridges.go internal/rocketclaw/app/thread_bridges_test.go internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/slackconnector/inert_test.go`

Run: `go test ./...`

Run: `make lint`

Run: `make test`

Expected: all pass, including race, coverage, metrics, and CLOC gates.

Run: `jj commit -m "internal/rocketclaw: make dollar help permanent"`
