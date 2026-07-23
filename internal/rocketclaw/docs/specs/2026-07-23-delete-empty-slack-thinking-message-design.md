# Delete Empty Slack Thinking Messages

## Problem

RocketClaw creates Slack thinking and answer placeholders before starting a turn. A provider can return a reasoning item whose summary is empty. The harness bridge correctly suppresses that empty summary, but the Slack connector currently completes the untouched thinking stream as a card with no visible details. Slack then displays a blank message next to the final answer.

## Behavior

When a Slack turn completes without any non-whitespace thinking or progress text, RocketClaw must delete the pre-created thinking message and keep the final answer.

When a turn has non-empty thinking or progress text, RocketClaw must preserve the existing behavior and complete the thinking card.

Placeholder creation remains unchanged so users still receive immediate acknowledgement while a turn is running.

## Design

The Slack connector owns placeholder lifecycle decisions. During final response completion, it already loads the turn's `slackThinkingState`. If that state contains no non-whitespace progress text, completion will use the existing response cleanup path while the thinking message timestamp is still present. That path stops an active Slack stream, deletes the thinking message, preserves a non-empty answer, removes the reaction, and clears connector state.

If the state contains progress text, the connector will continue completing the thinking stream or card exactly as it does today.

No provider parsing, harness bridge, persistence, event schema, or public API changes are required.

## Error Handling

Cleanup retains the existing best-effort Slack behavior. Stop-stream and delete failures are logged by the established cleanup path and do not replace a successfully delivered final answer with an error.

## Testing

Update the existing no-progress completion test to assert that:

- the final answer replaces the answer placeholder;
- the thinking stream is stopped when applicable;
- the thinking placeholder is deleted;
- no synthetic completed thinking card is produced.

Existing tests continue to cover preserving and completing thinking cards when progress exists.
