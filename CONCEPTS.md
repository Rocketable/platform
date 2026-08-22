# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent and continues across human replies.

A Managed Slack Thread has one active turn at a time. Follow-ups received during that turn are buffered and submitted after the turn completes.

### Root Slack Mention

An authorized Slack app mention that creates or targets the root message of a Managed Slack Thread.

A Root Slack Mention can begin the first turn immediately or establish a ready thread for a later human reply, depending on its command form.

### Adhoc Callout

An authorized Slack app mention that starts or takes over a Managed Slack Thread in a public channel, private channel, or group DM the bot has already joined.

Unmapped conversations use the `@` channel entry. Mapped channels keep that room's agents and allowlist. 1:1 DMs are not Adhoc Callouts.

### `@` Channel Entry

A slack.channels row named `@`. It is not a Slack channel. It supplies agents and an allowlist for Adhoc Callouts in unmapped joined channels.

### Buffered Follow-Up

A human Slack message accepted while a Managed Slack Thread has an active turn and held for submission after that turn completes.

A Buffered Follow-Up remains associated with its thread until promotion submits it or an explicit failure path consumes it.

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

## Durable State

### State Store

RocketClaw's durable database for sessions, Managed Slack Thread routing, goals, cron, scheduled messages, External MCP bindings, and restart handoffs.

The State Store is PostgreSQL. `run` ignores `state.sqlite3`.

### Operator SQLite Migrator

`fc migrate` copies missing v9 `state.sqlite3` rows into the selected PostgreSQL store. The operator runs it; start does not.

After every workspace has moved, SQLite support is deleted. It is not a historical-format migrator.

## Relationships

- A Root Slack Mention creates or targets a Managed Slack Thread.
- An Adhoc Callout creates or takes over a Managed Slack Thread.
- A Root Slack Mention in an unmapped joined channel is an Adhoc Callout when an `@` Channel Entry exists.
- A Buffered Follow-Up belongs to one active Managed Slack Thread and is promoted after the active turn completes.
- A Slack Side Ask is opened from a completed 💬 answer card in a Managed Slack Thread and does not become that thread's turn.
- A BAR is authored, packed, run, and ranked by Quickbench; an ELO Scorer belongs to one BAR.
- A Managed Slack Thread, Buffered Follow-Up, and External MCP binding persist in the State Store.
- An Operator SQLite Migrator copies missing `state.sqlite3` rows into the State Store.
- Development MCP lint and run_turn consume Request-Carried Context and read Overlay Clones; Reload replaces those clones.
