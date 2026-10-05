# Cronjob Slack Presentation Implementation Plan

**Goal:** Render each scheduled cronjob result as one framed Slack root whose visible header is `🔁 filename.md | agentname | RFC3339 timestamp` and whose visible body begins with the cronjob output, without changing machine-readable metadata or cron behavior.

**Architecture:** Keep the change entirely in `Connector.SendCronjobChannelThread`. Build the visible Block Kit header and body there while preserving the existing top-level metadata sentence, root timestamp, overflow replies, attachments, and thread registration. Reaction, one-off cronjob, and parser code remain untouched because the top-level text remains unchanged.

## Global Constraints

- Keep cron execution, result selection, thread registration, replies, attachments, reactions, one-off cronjobs, and target parsing unchanged.
- Use the exact visible header `🔁 filename.md | agentname | RFC3339 timestamp`.
- Keep a native Slack HeaderBlock; truncate only headers exceeding 150 characters to exactly 150 characters ending in `...`.
- Keep top-level text exactly ``Cronjob `relative/path.md` ran at `RFC3339 timestamp` with agent `agentname`.``
- Do not include the cronjob body in top-level text.
- Do not introduce new exported symbols, types, packages, callbacks, or defensive guards.
- Do not modify metric budgets or unrelated code.
- Use `jj`, never `git`; do not commit unless explicitly requested.

## Task 1: Frame Cron Output In Its Slack Root

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go`
- Modify: `internal/rocketclaw/slackconnector/connector_test.go`

- [x] Rename the normal-result test to describe its root-body contract and require one root post, the exact unchanged top-level metadata sentence, and Blocks containing the exact visible header, divider, and body.
- [x] Add a focused behavioral test proving oversized native headers truncate to 150 characters and end in `...`.
- [x] Strengthen the overflow test to prove the root retains its first 48 body chunks before remaining content continues in the root thread.
- [x] Implement the Block Kit root in `SendCronjobChannelThread`, preserving the old top-level metadata sentence without the body.
- [x] Keep attachment upload, failure cleanup, `RegisterCronThread`, and the `delivered` lifecycle unchanged.
- [x] Run `gofmt` and focused `TestSendCronjobChannelThread` tests.

## Task 2: Verify Presentation-Only Scope

**Files:**
- Review: `internal/rocketclaw/slackconnector/connector.go`
- Review: `internal/rocketclaw/slackconnector/connector_test.go`
- Review: `internal/rocketclaw/cronjob/manager.go`
- Review: `internal/rocketclaw/cronjob/manager_test.go`
- Review: `README.md`

- [x] Confirm the cronjob manager and parser files have no diff.
- [x] Confirm the scheduled-root reaction fixture and assertion remain unchanged.
- [x] Confirm reaction and one-off cronjob production code have no diff.
- [x] Run `go test ./...`, `make lint`, and `make test`.
- [x] Run the final `jj diff --git` and Go standards review.
- [x] Confirm README impact; no update is needed because the README does not document the message layout.
