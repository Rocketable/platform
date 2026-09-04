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

A Slack Steer is marked with hourglass until injection. It does not create thinking or answer placeholders. Adding ⏫ to a live queued envelope during an active turn converts that Enqueued Slack Message into a Slack Steer. Adding 🛑 to a waiting hourglass message drops that steer and does not stop the turn.

### Enqueued Slack Message

A later-turn prompt stashed on a Managed Slack Thread via `$enqueue`, via External MCP `session_prompt` while that paired thread has an active turn, or via a human reply while an on-demand `$cron` is still running on that thread. A `$cron` follow-up is not a Slack Steer and does not start a turn until the one-off finishes.

A Slack `$enqueue` is marked with envelope until it is popped. An External MCP stash has no in-thread envelope; it is visible in `$queue` until pop. Pop posts an incoming-envelope Slack Blocks card, then reserves thinking and answer placeholders. Enqueued Slack Messages persist across restart and run as separate turns.

### Thread Queue

The durable, conversation-local stack of Enqueued Slack Messages, shown and managed by `$queue` together with that conversation's scheduled messages. Rows stashed from External MCP on the paired thread appear on the same list.

`$queue` is an ephemeral jump index of pending Slack Steers (at the top) and that later-work list. Opening it dismisses the previous card. Hide closes it. A pending-steer row jumps to the hourglass message and then hides the card. A Slack `$enqueue` row jumps to the envelope message and then hides the card. Adding 🛑 to a waiting hourglass message drops that steer and does not stop the turn. Adding 🛑 to a queued envelope removes the item and does not stop the turn. Adding 🛑 to thinking or answer still stops the turn. Adding ⏫ to a live queued envelope during an active turn converts it to a Slack Steer. Scheduled and External MCP rows list with no jump and cannot be cancelled from Slack. There is no Up / Down / Remove / Steer on the card and no later-work reorder. After a turn ends, a still-continuing goal wins the next slot. Otherwise the next item is the first remaining row that is ready: an Enqueued Slack Message is ready in its list position; a scheduled message is ready at its due time. A not-yet-due scheduled row blocks later rows until it runs or is cancelled.

### Buffered Follow-Up

Historical name for a mid-turn Slack message held until the active turn completed, then submitted as the next turn.

Replaced by Slack Steer and Enqueued Slack Message.

### Slack Message Menu

A single Slack `...` menu shortcut, RocketClaw Actions, that opens a modal whose buttons match the live controls for that message (the same work as emoji reactions). Unauthorized clicks are silent. A message outside a RocketClaw conversation, or a managed-thread message with no live control, gets a short close-only explanation.

## Quickbench

### BAR (Benchmark Archive)

A portable benchmark unit for quickbench. On disk it is a `.bar` file (txtar) or an equivalent unpacked directory. A BAR holds a single `bench.yaml` (metadata + ELO model/criteria), the full RocketCode agent tree, root conversation turns, static host-tool mocks, bash doubles, and optional per-agent variation overlays so multi-agent runs can be reconstructed and ranked. Subagent `task` calls re-execute live against the BAR agents rather than frozen child traces.

### ELO Scorer

The only v1 scoring definition inside a BAR: a crisp criteria prompt plus a judge model and variant. Pairwise comparisons of subject-model outputs produce an ELO ranking; better/worse is defined only by that criteria prompt.

### Quickbench

The single CLI/product surface that packs, unpacks, dumps, runs, and ELO-ranks BARs, and pairs with a RocketClaw skill/subagent that captures Slack sessions into BARs.

## Prompt Provenance

### Principal

The human actor a connector attributes to a human-originated prompt.

Each connector chooses the string. The backend prints it in the model header. Principal is model-visible only. Authorization uses connector identity, not this string.

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

A Spill is granted as an exact-file read for the rest of that RocketCode Turn and is deleted when the turn ends. Re-reading a Spill must reuse the file this turn already booked, not infer a path from the spill directory.

### RocketCode Turn

One model loop in RocketCode. It owns that loop's Spills.

A RocketCode Turn is not the active-turn slot on a Managed Slack Thread. Slack occupancy often drives one RocketCode Turn, but the two lifetimes are not the same object.

## Process layout

### Backend

The one RocketClaw engine: conversation execution, later-work, cron, skills, agent definitions, overlay clones, Reload, and Restart.

### Frontend

A surface the process assembler constructs: Slack or External MCP. Frontends never import the backend.

### Protocol

The shared language frontends and backend import. A frontend drops protocol messages it does not handle.

## Durable State

### State Store

RocketClaw's durable database for sessions, Managed Slack Thread routing, goals, cron, scheduled messages, the Thread Queue, External MCP bindings, and restart handoffs.

The State Store is PostgreSQL.

## Relationships

- A Root Slack Mention creates or targets a Managed Slack Thread.
- An Adhoc Callout creates or takes over a Managed Slack Thread.
- A Root Slack Mention in an unmapped joined channel is an Adhoc Callout when an `@` Channel Entry exists.
- A Slack Steer belongs to one active Managed Slack Thread and is injected into that turn after the current parallel tool batch completes.
- An Enqueued Slack Message belongs to one Managed Slack Thread's Thread Queue until it is popped, removed, or consumed by an explicit failure path.
- A BAR is authored, packed, run, and ranked by Quickbench; an ELO Scorer belongs to one BAR.
- A Managed Slack Thread, Thread Queue, and External MCP binding persist in the State Store.
- Reload replaces Overlay Clones.
- Frontends and the backend import Protocol. Frontends do not import the backend.
- The process assembler constructs the backend and the frontends.
- A turn uses the Autocompaction Threshold of the Provider that serves its model.
- Code Mode exposes Execute and Host Tools; an oversized Execute result becomes a Spill owned by that RocketCode Turn.

## Flagged ambiguities

- "'turn' had been used for both a Slack conversation occupancy slot and a RocketCode model loop — these are distinct."
