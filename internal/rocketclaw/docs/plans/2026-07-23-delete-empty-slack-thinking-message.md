# Delete Empty Slack Thinking Message Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete the pre-created Slack thinking message when a completed turn produced no non-whitespace thinking or progress.

**Architecture:** Keep immediate placeholder creation and all upstream response processing unchanged. Make the final Slack completion branch choose the existing cleanup lifecycle when `slackThinkingState.Text` is empty; turns with real progress continue through the current completed-card lifecycle.

**Tech Stack:** Go 1.26.2+, `github.com/slack-go/slack`, `net/http/httptest`, `stretchr/testify`, Jujutsu.

## Global Constraints

- Preserve immediate Slack placeholder creation.
- Preserve completed thinking cards when non-empty progress exists.
- Keep the change inside `internal/rocketclaw/slackconnector`.
- Reuse the existing `finishResponse` cleanup path; add no new helper, type, field, callback, package, or exported symbol.
- Do not add defensive guards or change Slack error handling.
- Do not modify `SOURCE_CLOC_BUDGET` or any other metric budget.
- Use `jj`, never `git`; do not commit unless explicitly requested.

---

### Task 1: Delete A No-Progress Thinking Stream

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector_test.go:3731-3785`
- Modify: `internal/rocketclaw/slackconnector/connector.go:710-790`

**Interfaces:**
- Consumes: `(*Connector).finishResponse(context.Context, *events.OutboundMessage, *slackReplySlots, bool, bool)`.
- Produces: the existing `(*Connector).SendResponse(context.Context, *events.OutboundMessage) error` contract with no empty thinking message after completion.

- [x] **Step 1: Change the existing no-progress test into the streamed production regression**

Replace `TestSendResponseCompletesThinkingWithoutProgress` with this streamed production regression:

```go
func TestSendResponseDeletesThinkingStreamWithoutProgress(t *testing.T) {
	var (
		operations                []string
		updated, stopped, deleted url.Values
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.NoError(t, r.ParseForm()) {
			return
		}

		operations = append(operations, r.URL.Path)

		switch r.URL.Path {
		case "/chat.startStream":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.1"})
		case "/chat.postMessage":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.2"})
		case "/chat.update":
			updated = cloneValues(r.PostForm)
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": r.PostForm.Get("ts")})
		case "/chat.stopStream":
			stopped = cloneValues(r.PostForm)
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.1"})
		case "/chat.delete":
			deleted = cloneValues(r.PostForm)
			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected Slack API path %q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	connector := newTestConnector(server.URL)
	connector.teamID = "T123"
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	_, err := connector.createReplyPlaceholders(t.Context(), replyTarget, slackImmediatePlaceholder, "T123", "U123")
	require.NoError(t, err)

	msg := events.NewOutboundMessage(events.SourceSlack, "test", "final answer", events.OutputTargetSlack)
	msg.TurnID = "turn-thread"
	msg.Complete = true
	msg.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(t.Context(), msg))

	assert.Equal(t, []string{
		"/chat.startStream",
		"/chat.postMessage",
		"/chat.update",
		"/chat.stopStream",
		"/chat.delete",
		"/reactions.remove",
	}, operations)
	assert.Equal(t, "final answer", updated.Get("text"))
	assert.Equal(t, "555.2", updated.Get("ts"))
	assert.Equal(t, "555.1", stopped.Get("ts"))
	assert.Empty(t, stopped.Get("chunks"))
	assert.Equal(t, "555.1", deleted.Get("ts"))
}
```

- [x] **Step 2: Run the focused regression and verify the current behavior fails**

Run:

```bash
go test ./internal/rocketclaw/slackconnector -run '^TestSendResponseDeletesThinkingStreamWithoutProgress$' -count=1
```

Expected: FAIL because current completion calls `/chat.stopStream` with a `Complete` plan update and never calls `/chat.delete` for the thinking stream.

- [x] **Step 3: Route empty thinking state through existing cleanup**

In `finishCompleteResponse`, immediately after loading `pending := c.thinking[msg.TurnID]`, add the no-progress completion branch before placeholder defaulting and completed-card rendering:

```go
if strings.TrimSpace(pending.Text) == "" {
	c.finishResponse(ctx, msg, slots, hasSlots, strings.TrimSpace(msg.Text) == "")
	return nil
}
```

This leaves `slots.ThinkingTS` and `slots.thinkingStream` intact for `finishResponse`, which stops the stream, deletes the thinking message, preserves a non-empty answer, removes the robot reaction, and clears reply state.

Delete the now-unreachable synthetic `Complete` text and placeholder-default fallbacks. Every remaining non-empty thinking state comes from `bufferProgressText`, which stores both the selected placeholder and progress text.

- [x] **Step 4: Format and run focused Slack connector tests**

Run:

```bash
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go test ./internal/rocketclaw/slackconnector -run '^(TestSendResponseDeletesThinkingStreamWithoutProgress|TestSendResponseKeepsHumanThinkingTaskCardLifecycle|TestSendResponseCompletesThinkingPlanStreamAfterUnchangedAnswer)$' -count=1
```

Expected: PASS. The no-progress case deletes the thinking stream, while both progress-bearing cases still complete their thinking cards.

Update existing Slack lifecycle assertions that expected a synthetic completed card without progress. Keep progress-bearing expectations unchanged, and make manually constructed concurrency fixtures match production's invariant that queued activities have corresponding `Text` and `Placeholder` state.

- [x] **Step 5: Inspect the focused diff against Go and scope constraints**

Run:

```bash
jj diff --git -- internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
```

Expected: production changes remain local to `finishCompleteResponse`; tests encode the new no-progress contract and retain progress-bearing completion coverage. No new symbols, wrappers, guards, mutexes, context storage, injected dependencies, or nonconforming error variables.

### Task 2: Verify Repository-Wide Behavior

**Files:**
- Review: `internal/rocketclaw/slackconnector/connector.go`
- Review: `internal/rocketclaw/slackconnector/connector_test.go`
- Review: `internal/rocketclaw/docs/specs/2026-07-23-delete-empty-slack-thinking-message-design.md`
- Review: `README.md`

**Interfaces:**
- Consumes: Task 1's updated Slack completion behavior.
- Produces: verified repository state with unchanged public APIs and documented README assessment.

- [x] **Step 1: Run all mandatory verification**

Run each command separately:

```bash
go test ./...
make lint
make test
```

Expected: all commands exit successfully, including coverage and CLOC budget checks invoked by repository make targets.

- [x] **Step 2: Perform the final standards and semantic review**

Run:

```bash
jj diff --git
jj status
```

Verify the original invariants explicitly: no-progress turns delete only the thinking message; final answers remain delivered; progress-bearing turns retain completed thinking cards; queue order and outbound routing are unchanged; the diff contains no unrelated edits or defensive code.

- [x] **Step 3: Assess README impact**

Read `README.md` for any documented Slack thinking-placeholder contract. Expected: no README update is needed because this is a correction to an empty-message corner case, not a user-configurable feature or public API change.
