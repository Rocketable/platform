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

Historical name for a human Slack reply injected into a running turn. Removed: Slack replies and mentions no longer steer. Steers come from Web and from agents through `rocketclaw_sessions_write`.

### Enqueued Slack Message

Historical name for a later-turn prompt stashed from Slack with `$enqueue`. Removed with Slack's commands. Waiting Slack work is now an accepted mention in the Thread Queue.

### Thread Queue

The durable, conversation-local list of later work: accepted Slack mentions, work queued or stashed from Web, messages another session's agent queues, stashes, or sends with `stop` through `rocketclaw_sessions_write` or, into a Subagent Session, through `task`, and External MCP `session_prompt` calls that arrive while the paired thread has an active turn. Saved rows that never started run after a restart without another incoming message.

An agent-written item carries its sender: the sending session and agent. Web shows that sender where a person's Principal would be, and the item can be popped, promoted, reordered, or removed like any other. It is never a person's input, even after a person promotes it. A `stop` write goes ahead of every earlier item. A `stop` write without a prompt adds no item: it only posts a line naming the sender into the transcript, once the stopped turn has saved.

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

### Script Call Record

A small record of one host tool or MCP call a Code Mode script made. While its RocketCode Turn runs, each record has its own database row, written when the call starts and when it finishes, so the turn's other progress saves never rewrite it; the finished turn keeps its records in its trace. It holds the call's name, its arguments clipped to 4 KiB, a status, a one-line summary of the output, and the approver's outcome and reason when the approver checked the call. It never holds the output. The Web lists these records under the script's Execute call. An Execute call keeps at most 200, and none are added after the turn ends.

### Spill

A turn-scoped file that holds the full Execute result when that result is too large to send to the model.

A Spill has an opaque result ID registered only with its owning RocketCode Turn. The model retrieves bounded pages through the top-level `load_execute_result` tool, without gaining filesystem read access. The ID expires and the file is deleted when the turn ends.

### RocketCode Turn

One model loop in RocketCode. It owns that loop's Spills.

A RocketCode Turn is not the active-turn slot on a Managed Slack Thread. Slack occupancy often drives one RocketCode Turn, but the two lifetimes are not the same object.

### Delegation History

A session history of work one tool call delegated: its automatic permission reviews, or its Task subagent with the subagent's guardrail checks. Its conversation ID is the producing conversation's ID plus `/<tool call ID>-review-<hash of the reviewed call's tool call key>` for its reviews, before it runs and during it, such as of a Code Mode script's tool calls, or `/<tool call ID>-<hash of the call's tool call key>` for a Task subagent, the ID a later `task` call continues; a delegation inside it adds another such segment. The hash keeps the ID unique when a provider reuses call IDs and the same when a turn resumes. Histories saved before this keep their old `/<tool call ID>`.

A Task subagent's Delegation History is a Subagent Session. Review histories have no Managed Slack Thread or web chat of their own. All of them are deleted and forked with their producing conversation. That, not a `/` in the ID, is what sets them apart: other conversation IDs can contain a `/` too, such as a web chat continuing a cron run, whose ID includes the job's path.

### Subagent Session

A Task subagent run as a session like any other, on the same turn runner as top-level sessions: the same Thread Queue for steer, enqueue, and stop, the same restart recovery, and the same history saving. It differs only in data: it records its parent session, and it is not listed in the sidebar or message search, nor can `rocketclaw_sessions_write` target it. A `task` call writes its prompt into the session, as `rocketclaw_sessions_write` does, and answers with the turn that runs it; its guardrail checks are saved in the session's history but never read back by its turns. It acts for its top-level session's running turn: session tags and `rocketclaw_sessions_write` land on that session, sessions it creates keep that turn's agent, creator, and agent limits, files it attaches go out with that turn's reply, and its delegation depth counts from that session's agent. It is not offered `rocketclaw_schedule_message` or `rocketclaw_reset_scheduled_messages`. Stopping a session's turn while its foreground `task` call waits stops the subagent's current turn too, unless a background job waits for that turn; the prompt the call sent, if still queued, is not withdrawn but held in the subagent's Thread Queue until the next message sent there releases it to run just before that message, or a `$goal` before the goal, and no note reaches the stopped session.

### Background Job

An Execute script or Task subagent that keeps running after its tool call returned, owned by the conversation (a Subagent Session included) that started it. Only agents allowed by `permission.rocketclaw.allow_background` can start one, and a human can move running work into one from Web.

A Background Job's ID is the tool call key that started it. It ends as completed, failed, stopped, or killed, and each ending produces one Completion Note. An oversized result of a background Execute is retained privately for 7 days instead of becoming a Spill, and `load_execute_result` reads it from any turn of the owning conversation. A background Task job waits for its Subagent Session's turn; stopping it stops that turn and the subagent's own jobs.

### Completion Note

The system message that tells a Background Job's owner how the job ended. It enters a running turn of the owner at its next step, or wakes the owner with a new turn. A note for a script killed by a restart waits for the owner's next turn instead, unless the owner is a hidden cron or External MCP run. A turn that fails or stops before saving leaves the notes it read for the owner's next turn, without waking it again, unless the owner is a hidden cron or External MCP run, which may have no next turn: its notes wake it again 1 minute later, then 5 minutes later, then every 30 minutes until a wake saves. Only the first failure reaches its destination, saying the wake retries. It is consumed exactly once.

### Shadow Agent

An alternative agent defined by an immediate Markdown child of its parent's
matching `agents/` directory. Each parent session owns a separate Shadow Session
for each shadow. Shadows are not ordinary agent-picker choices and do not spawn
more shadows.

### Shadow Session

A saved comparison child that receives each parent turn's ordered consumed
inputs, with its own history, model, tool results, and failures. Web pairs turns
by their recorded source and turn identity, not prompt text or completion order.
Shadow output has no connector delivery. Shadows execute permitted tools for
real in the shared workspace; output separation is not a dry-run sandbox.

### Private Producer Conversation

A hidden conversation that runs work for a human-visible destination conversation, such as an External MCP case or a one-off cron run. Its turns carry that destination as their sync destination: while a turn runs, the destination's history shows it live; Sync then copies the history there. Web lists only the destination, and session tags set during the turn land on the destination.

## Web Voice

### Voice Session

A live spoken call between a person on the Web and OpenAI's voice model, attached to one conversation. Audio flows between the browser and OpenAI; RocketClaw creates the call with the conversation agent's Provider credentials and holds its control connection while the browser's request stays open. The call ends when that request ends, or earlier when RocketClaw or OpenAI ends it; it has no idle or no-speech timers. Its end reason is one of `stopped`, `replaced`, `expired`, `safety`, `closed`, `lost`, or `error`. On the Codex route RocketClaw reconnects a dropped control connection; on the API-key route a drop ends the call as `lost`. A person has at most one live Voice Session: starting another, on any conversation or device, ends their older one, while other people's sessions, even on the same conversation, keep running. Ending a Voice Session never stops the conversation's turns.

### Voice Settings

One person's Web voice state, kept by the server and the same on every device: their current voice conversation for the Voice page, the agent for their next one, their voice, and whether the Voice page shows the transcript. Two people never share Voice Settings, even in one browser. Retention cleanup never deletes a person's current voice conversation, even with no messages; Reset forgets it, and ordinary cleanup applies after that. A call uses the person's voice when the conversation agent's provider offers it, and that provider's default otherwise.

### Voice Hand-off

A task the voice model passes to the conversation's agent during a Voice Session. Each one becomes a Web user message marked `media=Voice`: Codex's `<realtime_delegation>` block, holding the request and what the person and the voice model said since the previous hand-off, taken the moment the hand-off arrives. Hand-offs are admitted in arrival order without waiting for the agent. Nothing about the agent's work reaches the voice model while it runs; the final reply of a turn the session handed off is spoken once, as written, unless it is over about 990 tokens or a message typed later in the call supersedes it. A pending question is announced once and answered in the app, never by a hand-off. Not to be confused with a Delegation History, which is work a tool call delegated inside RocketCode.

## Web Spaces

### Space

A named place in the Web that holds tabs, shared by everyone: the server keeps one list, oldest first, with names unique ignoring case, and the last one cannot be deleted. An agent or cron names its space by name with `space:`; tabs and sessions refer to a space by its ID, so a rename keeps them in it. Where each tab sits, and which space a person is in, is that person's own and follows them across devices.

### Birth Space

The Space a session was born in: its agent's `space:`, or for a cron's sessions the cron's, else the cron agent's, else for a Web session the space the person was in. A named space that does not exist yet is created with the session. A session opens in its Birth Space; a person may move its tab elsewhere and later restore it. Deleting the space clears it. Sessions from before Birth Spaces, and those started outside the Web other than cron report threads, have none.

## Hosted Pages

### Hosted Page

A file an agent published with `rocketclaw_store`, served by RocketClaw under a URL that names the agent's space, the agent, and the file path. It belongs to the agent, not to a space: when the agent's `space:` changes, its pages move with it and links under the old space name redirect. Only people the Web admits can open one. Page scripts run walled off from RocketClaw and can only read their own agent's Page Data.

### Page Data

An agent's JSON key-value entries, written with `rocketclaw_kv_store`. The agent reads and writes them; its Hosted Pages may only read them. Each agent, shadow agents included, has its own.

### Page Index

The list of Hosted Pages a Web space shows: HTML pages of agents whose space matches the space's name, ignoring case, plus agents with no space.

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

The durable, conversation-local record of every completed step of in-progress work. Steps include model responses, tool call results, Code Mode host and MCP calls, workflow worker results, pending questions, and accepted steers.

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
