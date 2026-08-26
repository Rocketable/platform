# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent and continues across human replies.

A Managed Slack Thread has one active turn at a time. A distinct human reply received during that turn is a Slack Steer when the agent is still in the tool loop, and an Enqueued Slack Message when it is too late to steer. A second Slack delivery of the same root message is not a new send.

### Root Slack Mention

An authorized Slack app mention that creates or targets the root message of a Managed Slack Thread.

A Root Slack Mention can begin the first turn immediately or establish a ready thread for a later human reply, depending on its command form.

### Adhoc Callout

An authorized Slack app mention that starts or takes over a Managed Slack Thread in a public channel, private channel, or group DM the bot has already joined.

Unmapped conversations use the `@` channel entry. Mapped channels keep that room's agents and allowlist. 1:1 DMs are not Adhoc Callouts.

### `@` Channel Entry

A slack.channels row named `@`. It is not a Slack channel. It supplies agents and an allowlist for Adhoc Callouts in unmapped joined channels.

### Slack Steer

A human Slack message accepted while a Managed Slack Thread has an active turn still in the tool loop, and injected into that same turn after the current parallel tool batch completes.

A Slack Steer is marked with hourglass until injection. It does not create thinking or answer placeholders.

### Enqueued Slack Message

A later-turn prompt stashed on a Managed Slack Thread, usually via `$enqueue`, or via a too-late plain send.

An Enqueued Slack Message is marked with envelope until it is popped. Pop posts an incoming-envelope Slack Blocks card, then reserves thinking and answer placeholders. Enqueued Slack Messages persist across restart and run as separate turns.

### Thread Queue

The durable, conversation-local stack of Enqueued Slack Messages, shown and managed by `$queue` together with that conversation's scheduled messages.

`$queue` shows one list in later-work order. Scheduled rows stay in due-time order and can only be cancelled. Enqueued rows can be moved before or after scheduled rows. After a turn ends, a still-continuing goal wins the next slot. Otherwise the next item is the first remaining row that is ready: an Enqueued Slack Message is ready in its list position; a scheduled message is ready at its due time. A not-yet-due scheduled row blocks later rows until it runs or is cancelled. Reorder of Enqueued Slack Messages changes list position, not stash times.

### Buffered Follow-Up

Historical name for a mid-turn Slack message held until the active turn completed, then submitted as the next turn.

Replaced by Slack Steer and Enqueued Slack Message.

### Slack Side Ask

A private, modal-only one-question ask of a channel agent, opened from the 💭 footer button on a completed 💬 answer card in a Managed Slack Thread.

A Slack Side Ask uses thread history up to the clicked card, never posts to the thread, and does not take the thread's active-turn slot. Dismissing the modal aborts only that Side Ask.

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

Each connector chooses the string. Clockwork prints it in the model header. Principal is model-visible only. Authorization uses connector identity, not this string.

## Development MCP

### Development MCP

A separate inbound MCP door for a coding agent to try overlay deltas against the live RocketClaw without writing those files onto the server.

It is off until enabled, uses its own credential, and is not External MCP. After the operator publishes through git, reload or restart picks up the published tree.

### Request-Carried Context

The overlay snapshot a Development MCP lint or run_turn call sends: an optional named base overlay plus file deltas for this call only.

The server does not remember it after the call returns. Conversation ID carries chat history only. A follow-up turn must send context again.

### Overlay Clone

The live checkout of one configured git overlay that reload last installed.

Try paths read or copy this tree. They do not fetch git and must not write it.

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

## Durable State

### State Store

RocketClaw's durable database for sessions, Managed Slack Thread routing, goals, cron, scheduled messages, the Thread Queue, External MCP bindings, and restart handoffs.

The State Store is PostgreSQL. `run` ignores `state.sqlite3`.

### Operator SQLite Migrator

`fc migrate` copies missing v9 `state.sqlite3` rows into the selected PostgreSQL store. The operator runs it; start does not.

After every workspace has moved, SQLite support is deleted. It is not a historical-format migrator.

## Relationships

- A Root Slack Mention creates or targets a Managed Slack Thread.
- An Adhoc Callout creates or takes over a Managed Slack Thread.
- A Root Slack Mention in an unmapped joined channel is an Adhoc Callout when an `@` Channel Entry exists.
- A Slack Steer belongs to one active Managed Slack Thread and is injected into that turn after the current parallel tool batch completes.
- An Enqueued Slack Message belongs to one Managed Slack Thread's Thread Queue until it is popped, removed, or consumed by an explicit failure path.
- A Slack Side Ask is opened from a completed 💬 answer card in a Managed Slack Thread and does not become that thread's turn.
- A BAR is authored, packed, run, and ranked by Quickbench; an ELO Scorer belongs to one BAR.
- A Managed Slack Thread, Thread Queue, and External MCP binding persist in the State Store.
- An Operator SQLite Migrator copies missing `state.sqlite3` rows into the State Store.
- Development MCP lint and run_turn consume Request-Carried Context and read Overlay Clones; Reload replaces those clones.
- A turn uses the Autocompaction Threshold of the Provider that serves its model.
- Code Mode exposes Execute and Host Tools; an oversized Execute result becomes a Spill owned by that RocketCode Turn.

## Flagged ambiguities

- "'turn' had been used for both a Slack conversation occupancy slot and a RocketCode model loop — these are distinct."
