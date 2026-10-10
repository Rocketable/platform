# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent. In Slack, only mentions start its turns; replies without a mention start nothing.

A Managed Slack Thread has one active turn at a time. A mention received during that turn waits in the Thread Queue and runs as the next turn; it never steers. Web can also work in the conversation: Web input it consumes and every reply are posted into the thread. A second Slack delivery of an accepted mention is not a new send.

### Root Slack Mention

An authorized Slack app mention at a channel's top level. It starts a new Managed Slack Thread whose first turn is the mention. A bare Root Slack Mention with no text, file, or forward is ignored.

### Adhoc Callout

An authorized Slack app mention that starts or takes over a Managed Slack Thread in a public channel, private channel, or group DM the bot has already joined. Taking over a thread RocketClaw does not know includes up to 50 earlier messages, and a bare mention is enough there.

Unmapped conversations use the `@` channel entry. Mapped channels keep that room's agents and allowlist. 1:1 DMs are not Adhoc Callouts.

### `@` Channel Entry

A slack.channels row named `@`. It is not a Slack channel. It supplies agents and an allowlist for Adhoc Callouts in unmapped joined channels.

### Report Thread

A Slack thread that shows a cron report or an External MCP session: a cron-created Slack-thread conversation, a conversation bound to an External MCP session, or an unrecorded thread whose root RocketClaw's own bot posted. The last case covers roots posted before their thread is recorded and threads whose record expired.

A Report Thread is read-only. RocketClaw ignores every reply and mention in it, with no reaction and no reply, while its sessions keep posting into it. Read-only means RocketClaw ignores input, not that Slack locks the thread. Work on it continues in Web.

### Slack Steer

Historical name for a human Slack reply injected into a running turn. Removed: Slack replies and mentions no longer steer. Steering is a Web feature.

### Enqueued Slack Message

Historical name for a later-turn prompt stashed from Slack with `$enqueue`. Removed with Slack's commands. Waiting Slack work is now an accepted mention in the Thread Queue.

### Thread Queue

The durable, conversation-local list of later work: accepted Slack mentions, work queued or stashed from Web, and External MCP `session_prompt` calls that arrive while the paired thread has an active turn. Saved rows that never started run after a restart without another incoming message.

Each accepted mention is one item with ID `slack:<channel>:<message ts>`, accepted at most once per conversation. Web shows these rows together with the conversation's scheduled messages and can promote or remove them; Slack is not told. After a turn ends, a still-continuing goal wins the next slot. Otherwise the next item is the first remaining row that is ready: a queued row is ready in its list position; a scheduled message is ready at its due time. A not-yet-due scheduled row blocks later rows until it runs or is cancelled.

### Buffered Follow-Up

Historical name for a mid-turn Slack message held until the active turn completed, then submitted as the next turn.

Replaced by Slack Steer, which was later removed. A mid-turn mention now waits in the Thread Queue.

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

A session history of work one tool call delegated, each a finished turn: its automatic permission reviews, or its Task subagent with the subagent's guardrail checks. Its conversation ID is the producing conversation's ID plus `/<tool call ID>-review-<hash of the reviewed call's tool call key>` for its reviews, before it runs and during it, such as of a Code Mode script's tool calls, or `/<tool call ID>-<hash of the call's tool call key>` for a Task subagent, the ID a later `task` call continues; a delegation inside it adds another such segment. The hash keeps the ID unique when a provider reuses call IDs and the same when a turn resumes. Histories saved before this keep their old `/<tool call ID>`.

A Delegation History has no Managed Slack Thread or web chat of its own. It is deleted and forked with its producing conversation. That, not a `/` in the ID, is what sets it apart: other conversation IDs can contain a `/` too, such as a web chat continuing a cron run, whose ID includes the job's path.

### Background Job

An Execute script or Task subagent that keeps running after its tool call returned, owned by the conversation (or subagent) that started it. Only agents allowed by `permission.rocketclaw.allow_background` can start one, and a human can move running work into one from Web.

A Background Job's ID is the tool call key that started it. It ends as completed, failed, stopped, or killed, and each ending produces one Completion Note. An oversized result of a background Execute is retained privately for 7 days instead of becoming a Spill, and `load_execute_result` reads it from any turn of the owning conversation.

### Completion Note

The system message that tells a Background Job's owner how the job ended. It enters a running turn of the owner at its next step, or wakes the owner with a new turn. A note for a script killed by a restart waits for the owner's next turn instead, unless the owner is a hidden cron or External MCP run. A turn that fails or stops before saving leaves the notes it read for the owner's next turn, without waking it again, unless the owner is a hidden cron or External MCP run, which may have no next turn: its notes wake it again 1 minute later, then 5 minutes later, then every 30 minutes until a wake saves. Only the first failure reaches its destination, saying the wake retries. It is consumed exactly once.

### Private Producer Conversation

A hidden conversation that runs work for a human-visible destination conversation, such as an External MCP case or a one-off cron run. Its turns carry that destination as their sync destination: Sync copies the history there, Web lists only the destination, and session tags set during the turn land on the destination.

## Web Voice

### Voice Session

A live spoken call between a person on the Web and OpenAI's voice model, attached to one conversation. Audio flows between the browser and OpenAI; RocketClaw creates the call with the conversation agent's Provider credentials and holds its control connection while the browser's request stays open. The call ends when that request ends, or earlier when RocketClaw or OpenAI ends it. A conversation has at most one live Voice Session: starting another ends the older one. Ending a Voice Session never stops the conversation's turns.

### Voice Hand-off

A task the voice model passes to the conversation's agent during a Voice Session. Each one becomes a Web user message marked `media=Voice`: the request, plus what the person and the voice model said since the previous hand-off as quoted context. A hand-off made while a question the session read out is pending answers that question instead. The agent's progress returns to the voice model as silent context, and the final reply of a turn the session handed off is spoken once. Not to be confused with a Delegation History, which is work a tool call delegated inside RocketCode.

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

The durable, conversation-local record of every completed step of in-progress work. Steps include model responses, tool call results, Task subagent turns, Code Mode host and MCP calls, workflow worker results, pending questions, and accepted steers.

An interrupted turn resumes from its last recorded step. Completed steps return their recorded results instead of running again. A clean shutdown lets running calls finish, so only a call cut off by a crash or a forced exit, which started without a recorded result, is reported to the model as interrupted. A turn's steps are cleared when it finishes.

## Relationships

- A Root Slack Mention creates a Managed Slack Thread.
- An Adhoc Callout creates or takes over a Managed Slack Thread.
- A Root Slack Mention in an unmapped joined channel is an Adhoc Callout when an `@` Channel Entry exists.
- A mention in a Managed Slack Thread becomes one Thread Queue item and runs as its own turn.
- A Report Thread ignores Slack input; its conversation continues in Web.
- A BAR is authored, packed, run, and ranked by Quickbench; an ELO Scorer belongs to one BAR.
- A Managed Slack Thread, Thread Queue, External MCP binding, and Step Journal persist in the State Store.
- Reload replaces Overlay Clones.
- Frontends and the backend import Protocol. Frontends do not import the backend.
- The process assembler constructs the backend and the frontends.
- A turn uses the Autocompaction Threshold of the Provider that serves its model.
- Code Mode exposes Execute and Host Tools; an oversized Execute result becomes a Spill owned by that RocketCode Turn, recovered through the top-level `load_execute_result` tool.

## Flagged ambiguities

- "'turn' had been used for both a Slack conversation occupancy slot and a RocketCode model loop — these are distinct."
