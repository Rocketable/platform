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
  - slack-steer
  - message-loss
  - stack-state
---

# Slack Root App-Mention Redelivery Cleared Buffered Follow-Ups

## Problem

RocketClaw could lose a mid-turn Slack message when Slack redelivered the root app mention while its first turn was still active. The root handler begins the thread stack before starting the root turn, and a follow-up arriving during that turn is supposed to remain on the stack (`internal/rocketclaw/slackconnector/connector.go:2449-2472`). Mid-turn plains are now Slack Steers (hourglass, same turn), not next-turn Buffered Follow-Ups.

## Symptoms

- A follow-up could be accepted onto the stack, then silently disappear after the root app mention was delivered again.
- The root response could complete normally while the stack had been replaced with an empty slice.

## What Didn't Work

The original stack initialization unconditionally assigned `nil` to `c.stacks[key]`. That treated every root delivery as a new stack, even when the key already represented an active turn with pending messages.

Relying on later promotion could not recover the message. After the reset makes the slice empty, there is nothing left to inject or enqueue.

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

The regression test `TestHandleAppMentionEventPreservesBufferedReplyAcrossRootRedelivery` buffers a distinct follow-up, redelivers the same root event from the router start callback, and verifies that the stack still contains that text and that turn completion does not wipe it or concatenate it into a next-turn submit (`internal/rocketclaw/slackconnector/connector_test.go:9575-9628`).

## Why This Works

The stack map uses two related states: an absent key means no active stack, while a present key means an active turn; the slice value contains Slack Steers received during that turn. `bufferSlackStack` relies on key presence as the active-state check and appends to the existing slice (`internal/rocketclaw/slackconnector/connector.go:2457-2472`).

Preserving an existing key therefore preserves pending steers without introducing a second redelivery mechanism. `promoteSlackStack` no longer concatenates stack text into a next turn. It only deletes an empty sentinel (`internal/rocketclaw/slackconnector/connector.go:2474-2486`). Later work lives on the Thread Queue. Live steers inject after the current tool batch, or become Enqueued Slack Messages if the turn is already writing the final answer.

## Prevention

- Treat stack initialization as an idempotent state transition: create a missing key, but never overwrite an active key's pending-steer slice.
- Preserve the distinction between stack activity and stack contents; key presence is the active sentinel, and slice entries are pending Slack Steers (`internal/rocketclaw/slackconnector/connector.go:2457-2463`).
- Treat redelivery as at-least-once delivery of the existing logical turn, not as permission to create a fresh lifecycle state.
- Keep the lifecycle states distinct: an absent key is inactive, a present empty slice is active with no pending steer, a present non-empty slice is active with pending steers, and deletion is the terminal handoff.
- Keep stack existence, buffering, preservation, and removal under the connector's mutex so the lifecycle transition remains atomic.
- Keep a regression sequence that exercises buffering, root redelivery, and completion in that order. Do not reintroduce concat-on-promote assertions.
- Assert the exact follow-up text and the hourglass reaction, not only that the root turn completed.

## Related Issues

- [PR #1](https://github.com/Rocketable/platform/pull/1) contains the merged fix and regression test.
