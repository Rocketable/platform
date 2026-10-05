# Remove Cron Reaction Trigger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the Slack `repeat_one` reaction trigger while preserving text-command one-off cron requests and allowing them from any configured Slack channel.

**Architecture:** Delete the reaction-only dispatch and handler from the Slack connector. Remove the cron-definition channel gate from `handleOnDemandCronRequest`; scheduled delivery remains unchanged because it uses the cron manager's configured channel path.

**Tech Stack:** Go 1.26, Slack Events API, Jujutsu (`jj`).

## Global Constraints

- Preserve messages such as `:repeat_one: daily` and `🔂 daily`.
- Preserve `octagonal_sign` and `stop_button` reaction behavior.
- Allow a text-command one-off request to start any top-level cronjob from any configured Slack channel.
- Preserve scheduled cron delivery to each definition's configured `channel`.
- Preserve one-off cron loading, user authorization, execution, output, and registration.
- Add no feature flag, compatibility handler, rejection response, or replacement trigger.
- Remove reaction-specific code, tests, and documentation completely.
- Do not modify metric budgets or unrelated code.
- Use `jj`, never `git`; do not commit unless explicitly requested.

---

### Task 1: Delete The Reaction Entry Point

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:37,1848-1939`
- Modify: `internal/rocketclaw/slackconnector/connector_test.go:6314-6516`
- Modify: `cmd/rocketclaw/CHEATSHEET.md:12,26,100`

**Interfaces:**
- Preserves: message-event calls to `handleOnDemandCronRequest`.
- Preserves: stop-reaction dispatch through `handleReactionAddedEvent`.
- Removes: `slackOnDemandCronReaction` and `handleOnDemandCronReaction`.
- Removes: the `channelName` argument and cron-definition channel rejection from `handleOnDemandCronRequest`.

- [x] **Step 1: Make `repeat_one` an unsupported reaction in the test**

Replace `TestHandleReactionAddedEventIgnoresUnauthorizedCronReaction` with an authorized unsupported-reaction test:

```go
func TestHandleReactionAddedEventIgnoresCronReaction(t *testing.T) {
	runner := newOneOffCronjobLoaderStub()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, nil, nil, nil, runner)
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", "repeat_one", "171234.5678"))

	assert.Empty(t, runner.targetsSnapshot())
}
```

- [x] **Step 2: Run the test and verify the current reaction trigger fails it**

Run:

```bash
go test ./internal/rocketclaw/slackconnector -run TestHandleReactionAddedEventIgnoresCronReaction -count=1
```

Expected: FAIL because the current dispatcher performs Slack API calls for the authorized `repeat_one` reaction.

- [x] **Step 3: Delete the reaction-only production path**

In `connector.go`:

- Change the constant declaration to retain `slackOnDemandCronPrefix` and `slackRobotReaction` but remove `slackOnDemandCronReaction`.
- Restrict the reaction allowlist to `slackGoalStopSignReaction` and `slackGoalStopButtonReaction`.
- Delete the `if reaction == slackOnDemandCronReaction` branch.
- Delete `handleOnDemandCronReaction` entirely.
- Remove `channelName` from `handleOnDemandCronRequest` and its message-event call.
- Delete the `loaded.TextChannel` comparison and "not configured to run in this Slack channel" rejection.

Do not change the message-event path:

```go
if strings.HasPrefix(text, slackOnDemandCronPrefix) || strings.HasPrefix(text, "🔂") {
	if target, ok := cronjob.OnDemandCronTarget(text, slackOnDemandCronPrefix, "🔂"); ok {
		c.handleOnDemandCronRequest(ctx, ev, target, replyTarget)
		return
	}
}
```

- [x] **Step 4: Delete obsolete reaction behavior tests**

Delete these tests while retaining the new unsupported-reaction test and all stop-reaction tests:

```text
TestHandleReactionAddedEventRunsOnDemandCron
TestHandleReactionAddedEventRejectsInvalidCronReactionTarget
TestHandleReactionAddedEventRejectsCronForDifferentChannel
TestHandleReactionAddedEventRerunsScheduledCronThreadRoot
```

- [x] **Step 5: Remove reaction claims from the cheat sheet**

Update the three `repeat_one` rows so they describe only text-prefix requests:

```markdown
| `🔂` | `:repeat_one:` | Slack | Runs a one-off cron request by text prefix. | Examples: `🔂 daily`, `🔂 daily.md`. |
| One-off cron | `🔂 daily` or `🔂 daily.md`. | Any top-level cronjob can be started from any configured Slack channel. |
| `🔂` | `:repeat_one:` | Cron one-off request prefix. |
```

- [x] **Step 6: Format and run focused tests**

Run:

```bash
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go test ./internal/rocketclaw/slackconnector -run 'TestHandleReactionAddedEvent|TestHandleMessageEventRunsOnDemandCronInSlackThread' -count=1
```

Expected: PASS. Confirm the text-command test still runs a cronjob and stop-reaction tests still interrupt managed work.

The retained text-command test must load a cronjob whose `TextChannel` differs from the requesting Slack channel and assert that the cronjob still runs.

- [x] **Step 7: Run mandatory verification and inspect scope**

Run:

```bash
go test ./...
make lint
make test
jj diff --git
```

Expected: all commands pass. The final diff contains only the connector deletion, focused test deletion/replacement, cheat-sheet updates, and approved spec/plan. Confirm `cronjob/manager.go` and metric budgets have no diff.

- [x] **Step 8: Confirm README impact**

The README does not document the reaction trigger. Confirm no README update is needed.
