---
title: Show web inputs in Slack threads
date: 2026-09-28
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

## Product Contract

- R1: When a web-origin message is consumed in a Slack-managed thread, its text appears as a clearly labeled web input in that same Slack thread. Preserve pasted line breaks and text; do not treat the bot's copy as a second prompt.
- R2: Stashed, waiting, deleted, and unrelated web-only conversation messages do not appear in Slack. Existing web transcript consumption and assistant delivery stay unchanged.

## Planning Contract

Use the existing consumed-input outbound event and Slack thread routing. Emit consumption once on activation or steer drain, preserve its inbound source and original typed text across connector copies, and mirror only web-origin events in Slack-managed threads. The web transcript still needs the augmented attachment references; do not leak those references into Slack. Post literal multiline text in ordered chunks without Slack markdown or mention parsing; an attachment-only input gets a marker rather than a workspace path. Acknowledge delivery through the existing subscriber. Keep this distinct from assistant response slots so it cannot overwrite a reply. Do not add new event types or asynchronous plumbing. A Slack bot's own messages are already ignored by inbound routing.

## Implementation Units

### U1: Route consumed web input to Slack

Files: `internal/rocketclaw/backend/bridge.go`, `internal/rocketclaw/backend/conversations.go`, `internal/rocketclaw/protocol/types.go`, `internal/rocketclaw/protocol/clockwork.go`, `internal/rocketclaw/frontend/slack/events.go`, and their existing tests.

Change the consumed-input branch from unconditional skip to labeled plain-text thread replies for web-origin events in Slack conversations. Remove duplicate turn-time consumption; distinguish queued Slack input already visible in its thread. Check multiline typed text and markup reach the correct channel/thread without mention or markdown parsing, long pasted text is split without loss, attachment references remain in the web transcript but not in Slack, private and Slack-origin input produce no post, and delivery errors acknowledge a failure. Check immediate, steer, and queue consumption and verify no waiting/stashed input is surfaced early.

## Verification Contract

Run focused Slack and backend tests, then `gofmt` on touched Go files, `go test ./...`, `make lint`, and `make test`. Review the actual JJ diff for message order, exact text, unintended routing, defensive code, and source CLOC. Do not post to live Slack to verify this feature.

## Definition of Done

Web inputs consumed in Slack-managed sessions appear as labeled thread replies, without duplicate prompting or premature queue delivery, and the required checks pass. README impact is reviewed.
