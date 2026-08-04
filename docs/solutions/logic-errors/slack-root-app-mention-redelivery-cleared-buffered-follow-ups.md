---
title: Slack Root App-Mention Redelivery Cleared Buffered Follow-Ups
date: 2026-08-04
category: docs/solutions/logic-errors/
module: internal/rocketclaw/slackconnector
problem_type: logic_error
component: assistant
symptoms:
  - "A root Slack app-mention redelivery cleared an already-buffered follow-up message."
  - "The buffered follow-up was not delivered after the original root turn completed."
root_cause: logic_error
resolution_type: code_fix
severity: high
tags:
  - slack
  - app-mention
  - redelivery
  - buffered-follow-up
  - message-loss
  - stack-state
---

# Slack Root App-Mention Redelivery Cleared Buffered Follow-Ups

## Problem

RocketClaw could lose a Slack follow-up when Slack redelivered the root app mention while its first turn was still active. The root handler begins the thread stack before starting the root turn, and a follow-up arriving during that turn is supposed to remain buffered until completion (`internal/rocketclaw/slackconnector/connector.go:2422-2470`, `internal/rocketclaw/slackconnector/connector.go:2195-2219`).

## Symptoms

- A follow-up could be accepted and marked as buffered, then silently disappear after the root app mention was delivered again.
- The root response could complete normally while no buffered follow-up was submitted because redelivery had replaced the stack contents with an empty slice (`internal/rocketclaw/slackconnector/connector.go:1665-1683`).

## What Didn't Work

The original stack initialization unconditionally assigned `nil` to `c.stacks[key]`. That treated every root delivery as a new stack, even when the key already represented an active turn with buffered messages.

Relying on later promotion could not recover the message. Promotion reads the current slice; after the reset makes it empty, it deletes the stack and returns without submitting anything (`internal/rocketclaw/slackconnector/connector.go:1665-1683`).

## Solution

Make `beginSlackStack` idempotent for an existing key: initialize only a missing map entry and preserve its current slice while holding the connector mutex.

```go
func (c *Connector) beginSlackStack(key string) {
	c.mu.Lock()
	if _, ok := c.stacks[key]; !ok {
		c.stacks[key] = nil
	}
	c.mu.Unlock()
}
```

The regression test `TestHandleAppMentionEventPreservesBufferedReplyAcrossRootRedelivery` buffers a distinct follow-up, redelivers the same root event from the router start callback, verifies that the stack still contains the follow-up, then sends a completed root response and verifies that the follow-up is promoted (`internal/rocketclaw/slackconnector/connector_test.go:7511-7562`).

## Why This Works

The stack map uses two related states: an absent key means no active stack, while a present key means an active turn; the slice value contains follow-ups received during that turn. `bufferSlackStack` relies on key presence as the active-state check and appends to the existing slice (`internal/rocketclaw/slackconnector/connector.go:1649-1663`).

Preserving an existing key therefore preserves the buffered messages without introducing a second redelivery mechanism. On the normal completion path, after progress cleanup succeeds and the turn has a non-empty ID and thread key, `finishResponse` calls `promoteSlackStack` for the same thread key. Promotion removes buffered reactions, combines buffered messages, and submits the resulting inbound message (`internal/rocketclaw/slackconnector/connector.go:875-915`, `internal/rocketclaw/slackconnector/connector.go:1685-1703`).

The fix and regression were merged in [PR #1](https://github.com/Rocketable/platform/pull/1).

## Prevention

- Treat stack initialization as an idempotent state transition: create a missing key, but never overwrite an active key's buffered slice.
- Preserve the distinction between stack activity and stack contents; key presence is the active sentinel, and slice entries are queued messages (`internal/rocketclaw/slackconnector/connector.go:1649-1655`).
- Treat redelivery as at-least-once delivery of the existing logical turn, not as permission to create a fresh lifecycle state.
- Keep the lifecycle states distinct: an absent key is inactive, a present empty slice is active with no follow-up, a present non-empty slice is active with buffered work, and deletion is the terminal handoff.
- Keep stack existence, buffering, preservation, and removal under the connector's mutex so the lifecycle transition remains atomic.
- Keep a regression sequence that exercises buffering, root redelivery, completion, and promotion in that order. Extend it when the buffering contract changes to cover multiple follow-ups and ordering.
- Assert the exact follow-up text and both buffered-reaction operations, not only that the root turn completed.

## Related Issues

- [PR #1](https://github.com/Rocketable/platform/pull/1) contains the merged fix and regression test.
