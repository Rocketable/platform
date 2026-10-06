# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent and continues across human replies.

A Managed Slack Thread has one active turn at a time. A distinct human reply received during that turn is a Slack Steer. An explicit `$enqueue` is an Enqueued Slack Message. A second Slack delivery of the same root message is not a new send.

### Root Slack Mention

An authorized Slack app mention that creates or targets the root message of a Managed Slack Thread.

A Root Slack Mention can begin the first turn immediately or establish a ready thread for a later human reply, depending on its command form.

### Adhoc Callout

An authorized Slack app mention that starts or takes over a Managed Slack Thread in a public channel, private channel, or group DM the bot has already joined.

Unmapped conversations use the `@` channel entry. Mapped channels keep that room's agents and allowlist. 1:1 DMs are not Adhoc Callouts.

### `@` Channel Entry

A slack.channels row named `@`. It is not a Slack channel. It supplies agents and an allowlist for Adhoc Callouts in unmapped joined channels.

### Slack Steer

A human Slack message accepted while a Managed Slack Thread has an active turn, and injected into that same turn after the current parallel tool batch completes, or when the model answers without tools. Every waiting steer injects in one drain.

A Slack Steer is marked with hourglass until injection. It does not create another in-progress placeholder. Adding ⏫ to a live queued envelope during an active turn converts that Enqueued Slack Message into a Slack Steer. Adding 🛑 to a waiting hourglass message drops that steer and does not stop the turn.

### Enqueued Slack Message

A later-turn prompt stashed on a Managed Slack Thread via `$enqueue`, or via External MCP `session_prompt` while that paired thread has an active turn.

A Slack `$enqueue` is marked with envelope until it is popped. An External MCP stash has no in-thread envelope; it is visible in `$queue` until pop. Pop posts an incoming-envelope Slack Blocks card, then reserves one in-progress placeholder. Enqueued Slack Messages persist across restart. After restart, saved rows that never started run as separate turns without another incoming message.

### Thread Queue

The durable, conversation-local stack of Enqueued Slack Messages, shown and managed by `$queue` together with that conversation's scheduled messages. Rows stashed from External MCP on the paired thread appear on the same list.

`$queue` is an ephemeral jump index of pending Slack Steers (at the top) and that later-work list. Opening it dismisses the previous card. Hide closes it. A pending-steer row jumps to the hourglass message and then hides the card. A Slack `$enqueue` row jumps to the envelope message and then hides the card. Adding 🛑 to a waiting hourglass message drops that steer and does not stop the turn. Adding 🛑 to a queued envelope removes the item and does not stop the turn. Adding 🛑 to the in-progress placeholder still stops the turn. Adding ⏫ to a live queued envelope during an active turn converts it to a Slack Steer. Scheduled and External MCP rows list with no jump and cannot be cancelled from Slack. There is no Up / Down / Remove / Steer on the card and no later-work reorder. After a turn ends, a still-continuing goal wins the next slot. Otherwise the next item is the first remaining row that is ready: an Enqueued Slack Message is ready in its list position; a scheduled message is ready at its due time. A not-yet-due scheduled row blocks later rows until it runs or is cancelled.

### Buffered Follow-Up

Historical name for a mid-turn Slack message held until the active turn completed, then submitted as the next turn.

Replaced by Slack Steer and Enqueued Slack Message.

## Prompt Provenance

### Principal

The human actor a connector attributes to a human-originated prompt.

Each connector chooses the string. The backend prints it in the model header, which Web also exposes through the message footer's info icon. Authorization uses connector identity, not this string.

## Runtime Assets

### Overlay Clone

The live checkout of one configured git overlay that reload last installed.

### Reload

Hot-load of published overlay files the live daemon can apply without a process restart.

Reload rebuilds the live overlay clones. It is not interchangeable with Restart.

### Restart

A process restart required when the overlay list or runtime config changed.

Restart is not a substitute for Reload when only overlay file contents changed.

## Model Providers

### Provider

A named OpenAI-compatible credential and endpoint used to serve model requests. The default Provider is `openai`; other Providers live under `providers` and are selected by a `provider/model` qualifier.

Root and child agents resolve their Provider independently. There is no implicit failover from one Provider to another.

### Autocompaction Threshold

The token count at which a turn asks its Provider to compact conversation history.

Each Provider can set its own Autocompaction Threshold. Unset means the runtime default. A child turn uses a different threshold only when it resolves a different Provider.

## RocketCode

### Code Mode

The RocketCode agent style that asks the model to write a short Starlark script and run it through Execute instead of calling host tools as top-level model tools.

### Execute

The model-facing tool that runs a Code Mode script and returns one string to the model.

Host tools inside the script return full strings to the script. Only the string Execute sends back to the model is clipped when it is oversized.

### Host Tool

A sandbox capability the Code Mode script can call directly, as opposed to a model-facing tool such as Execute. Host tools include filesystem, shell, fetch, and embedder-custom tools.

### Spill

A turn-scoped file that holds the full Execute result when that result is too large to send to the model.

A Spill has an opaque result ID registered only with its owning RocketCode Turn. The model retrieves bounded pages through the top-level `load_execute_result` tool, without gaining filesystem read access. The ID expires and the file is deleted when the turn ends.

### RocketCode Turn

One model loop in RocketCode. It owns that loop's Spills.

A RocketCode Turn is not the active-turn slot on a Managed Slack Thread. Slack occupancy often drives one RocketCode Turn, but the two lifetimes are not the same object.

### Delegation History

The session history of what one tool call delegated: its automatic permission reviews, guardrail checks, and Task subagent, each a finished turn. Its conversation ID is the producing conversation's ID plus `/<tool call ID>`; a delegation inside it adds another `/<tool call ID>`.

A Delegation History has no Managed Slack Thread or web chat of its own. It is deleted and forked with its producing conversation.

### Private Producer Conversation

A hidden conversation that runs work for a human-visible destination conversation, such as an External MCP case or a one-off cron run. Its turns carry that destination as their sync destination: Sync copies the history there, Web lists only the destination, and session tags set during the turn land on the destination.

## Process layout

### Backend

The one RocketClaw engine: conversation execution, later-work, cron, skills, agent definitions, overlay clones, Reload, and Restart.

### Frontend

A surface the process assembler constructs: Slack or External MCP. Frontends never import the backend.

### Protocol

The shared language frontends and backend import. A frontend drops protocol messages it does not handle.

## Durable State

### State Store

RocketClaw's durable database for sessions, Managed Slack Thread routing, goals, cron, scheduled messages, the Thread Queue, External MCP bindings, and the Step Journal.

The State Store is PostgreSQL.

### Step Journal

The durable, conversation-local record of every completed step of in-progress work. Steps include model responses, tool call results, Task subagent turns, Code Mode host and MCP calls, workflow worker results, pending questions, and accepted Slack Steers.

An interrupted turn resumes from its last recorded step. Completed steps return their recorded results instead of running again. A clean shutdown lets running calls finish, so only a call cut off by a crash or a forced exit, which started without a recorded result, is reported to the model as interrupted. A turn's steps are cleared when it finishes.

## Relationships

- A Root Slack Mention creates or targets a Managed Slack Thread.
- An Adhoc Callout creates or takes over a Managed Slack Thread.
- A Root Slack Mention in an unmapped joined channel is an Adhoc Callout when an `@` Channel Entry exists.
- A Slack Steer belongs to one active Managed Slack Thread and is injected into that turn after the current parallel tool batch completes.
- An Enqueued Slack Message belongs to one Managed Slack Thread's Thread Queue until it is popped, removed, or consumed by an explicit failure path.
- A BAR is authored, packed, run, and ranked by Quickbench; an ELO Scorer belongs to one BAR.
- A Managed Slack Thread, Thread Queue, External MCP binding, and Step Journal persist in the State Store.
- Reload replaces Overlay Clones.
- Frontends and the backend import Protocol. Frontends do not import the backend.
- The process assembler constructs the backend and the frontends.
- A turn uses the Autocompaction Threshold of the Provider that serves its model.
- Code Mode exposes Execute and Host Tools; an oversized Execute result becomes a Spill owned by that RocketCode Turn, recovered through the top-level `load_execute_result` tool.

## Flagged ambiguities

- "'turn' had been used for both a Slack conversation occupancy slot and a RocketCode model loop — these are distinct."
