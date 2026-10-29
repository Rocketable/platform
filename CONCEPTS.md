# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent. In Slack, only mentions start its turns; replies without a mention start nothing.

A Managed Slack Thread has one active turn at a time. A mention received during that turn waits in the Thread Queue and runs as the next turn; it never steers. Web can also work in the conversation: Web input it consumes and every reply are posted into the thread. A second Slack delivery of an accepted mention is not a new send.

### Root Slack Mention

An authorized Slack app mention at a channel's top level. It starts a new Managed Slack Thread whose first turn is the mention. A bare Root Slack Mention with no text, file, or forward is ignored.

### Adhoc Callout

An authorized Slack app mention that starts or takes over a Managed Slack Thread in a public channel, private channel, or group DM the bot has already joined. Taking over a thread RocketClaw does not know brings in a bounded number of its earlier messages, and a bare mention is enough there.

Unmapped conversations use the `@` Channel Entry. Mapped channels keep that room's agents and allowlist. 1:1 DMs are not Adhoc Callouts.

### `@` Channel Entry

A configured Slack channel entry named `@` that supplies agents and an allowlist for Adhoc Callouts in joined channels that have no entry of their own. It is not a Slack channel.

### Report Thread

A Slack thread an earlier version posted a cron report or an External MCP session into: its conversation, now a Web chat created by cron or bound to the session, or an unrecorded thread whose root RocketClaw's own bot posted. The last case covers threads whose record expired.

A Report Thread is read-only. RocketClaw ignores every reply and mention in it, with no reaction and no reply, and posts nothing more into it. Read-only means RocketClaw ignores input, not that Slack locks the thread. Work on it continues in Web.

### Thread Queue

The durable, per-conversation list of work waiting to run as later turns: accepted Slack mentions, work queued or stashed from Web, messages another session's agent sends, External MCP prompts that arrive while the conversation's turn is active, and prompts a parent sends into its Subagent Session.

Each item is a waiting turn of its conversation, queued, stashed, or held, keyed by its input ID; starting it makes it the running turn. Saved items that never started run after a restart without another incoming message. An agent-written item carries its sender, the sending session and agent, shown where a person's Principal would be; it is never a person's input, even after a person promotes it. Only a person stashes items. An agent may also stop the running turn: its stop goes ahead of every earlier item, and a stop without a prompt adds no item, only a line in the transcript naming the sender once the stopped turn has saved. A session write from another session's agent is refused before it adds or stops anything when the receiving agent's recipient setting does not accept the writing agent. When the turn that took such an item ends, or a person removes the item, the writing session gets a reply Completion Note.

A Slack mention is accepted at most once per conversation, keyed by its Slack message. Web shows the items together with the conversation's scheduled messages and can promote or remove them, and also pop or reorder agent-written items; Slack is not told. After a turn ends, a still-continuing goal wins the next slot; otherwise the next item is the first ready one, where a queued item is ready in its place and a scheduled message at its due time. A not-yet-due scheduled item blocks later items until it runs or is cancelled.

## Prompt Provenance

### Principal

The human actor a connector attributes to a human-originated prompt.

Each connector chooses the string. The backend prints it in the model header, which Web also exposes through the message footer's info icon. Authorization uses connector identity, not this string.

## Conversation Work

### Goal

An objective a person sets on a conversation from Web that keeps its agent taking turns toward it until the agent marks it complete, someone stops it, or its turn budget, when it has one, is spent.

While a Goal continues, its next turn takes the conversation's next slot ahead of waiting Thread Queue items, and its turns carry the Principal of the person who set it. Stopping the conversation's running turn stops its Goal too.

### Workflow

A saved Starlark program that runs a task as agent calls made by named workers, grouped in phases. A person starts one from Web with `$workflow`, and an agent whose permissions allow it can run one too. A workflow turn has no Shadow Sessions, and a steer sent during it runs as the next turn instead of joining it.

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

A named OpenAI-compatible credential and endpoint used to serve model requests. A model name without a Provider qualifier uses the default Provider; a qualified name selects that Provider.

Root and child agents resolve their Provider independently. There is no implicit failover from one Provider to another.

### Autocompaction Threshold

The token count at which a turn asks its Provider to compact conversation history.

Each Provider can set its own Autocompaction Threshold. Unset means the runtime default. A child turn uses a different threshold only when it resolves a different Provider.

## RocketCode

### Code Mode

The RocketCode agent style that asks the model to write a short script and run it through Execute instead of calling Host Tools as top-level model tools.

### Execute

The model-facing tool that runs a Code Mode script and returns one string to the model.

Host tools inside the script return full strings to the script. Only the string Execute sends back to the model is clipped when it is oversized.

### Host Tool

A sandbox capability the Code Mode script can call directly, as opposed to a model-facing tool such as Execute. Host tools include filesystem, shell, fetch, and embedder-custom tools.

### Emulated Shell

An agent's opt-in shell mode in which its shell commands run in a bash emulator instead of host bash.
*Avoid:* just-bash shell

Each command runs in a fresh emulator process. Only the agent's declared working directory reaches the host disk; everything else, temporary files included, lives in memory and is gone when the command ends. The commands can start only the host programs the agent approves, and those programs see only the host's environment, never variables the emulated script set. Network access follows the agent's allowed targets. An agent that asked for the Emulated Shell but never received one has its shell commands fail rather than fall back to host bash.

### Script Call Record

A small record of one Host Tool or MCP call a Code Mode script made, which Web lists under the script's Execute call.

It holds the call's name, its clipped arguments, a status, a one-line summary of the output, and the outcome and reason of the call's Permission Review when it had one; it never holds the output. While its RocketCode Turn runs, each record is saved on its own when the call starts and when it finishes, so the turn's other saves never rewrite it; the finished turn keeps its records. An Execute call keeps a bounded number of records, and none are added after the turn ends.

### Spill

A turn-scoped file that holds the full Execute result when that result is too large to send to the model.

A Spill has an opaque result ID registered only with its owning RocketCode Turn. The model retrieves bounded pages through a dedicated top-level tool, without gaining filesystem read access. The ID expires and the file is deleted when the turn ends.

### RocketCode Turn

One model loop in RocketCode. It owns that loop's Spills.

A RocketCode Turn is not the active-turn slot on a Managed Slack Thread. Slack occupancy often drives one RocketCode Turn, but the two lifetimes are not the same object.

### Delegation History

A session history of work one tool call delegated: its Permission Reviews, made before the call runs and during it, or its Task subagent with the subagent's Guardrail checks.

Its conversation ID extends the producing conversation's ID with a segment derived from the tool call, so it stays unique when a provider reuses call IDs and stays the same when a turn resumes; a delegation inside it adds another segment. A Task subagent's Delegation History is a Subagent Session, which a later Task call can continue. Review histories have no Managed Slack Thread or Web chat of their own. All of them are deleted and forked with their producing conversation; that, not the shape of the ID, is what sets them apart, since other conversation IDs can contain the same separator.

### Permission Review

An automatic check, by a reviewer agent, of a tool call or a whole Execute script that an agent's permissions mark for review, made before it runs. It allows or denies with a reason; a denial, an unreadable answer, or a failed review blocks the call. Its decision is read from the reviewer's final answer, whichever way the model was reached.
*Avoid:* approval, approver check

### Guardrail

An agent that an agent type names to check every Task delegation to that type: each prompt before the subagent receives it, and each answer before the parent sees it. A Guardrail approves or rejects and never rewrites the prompt or the answer; a rejected prompt never reaches the subagent. Not a Permission Review, which judges tool calls rather than delegations.

### Subagent Session

A Task subagent run as a session like any other, with the same Thread Queue, restart recovery, and history saving as top-level sessions, that records its parent session, is not listed in the sidebar or message search, and cannot be written to by other sessions' agents.

A Task call writes its prompt into the session's Thread Queue and answers with the turn that runs it; the subagent's guardrail checks are saved in the session's history but never read back by its turns. It acts for its top-level session's running turn: session tags and session writes land on that session, though replies to its writes come back to the subagent and the recipient check uses the subagent's own agent; sessions it creates keep that turn's agent, creator, and agent limits, files it attaches go out with that turn's reply, and its delegation depth counts from that session's agent. It cannot schedule messages. Stopping a session's turn while its foreground Task call waits stops the subagent's current turn too, unless a Background Job waits for that turn; the prompt the call sent, if still queued, is held in the subagent's Thread Queue until the next message sent there, or a goal, releases it to run just before that message or goal, and no note reaches the stopped session.

A Completion Note can wake a subagent after its Task call already returned. When that wake turn ends, its answer goes to the parent as a Task Completion Note, reviewed by the subagent type's response guardrail when it has one; a parent that is itself a subagent reports its own wake turn the same way, so the answer climbs to the top-level session. A stopped wake turn, or one that took a Task call's prompt, reports nothing.

### Background Job

An Execute script or Task subagent that keeps running after its tool call returned, owned by the conversation (a Subagent Session included) that started it. Only agents whose permissions allow background work can start one, and a human can move running work into one from Web.

A Background Job is identified by the tool call that started it. It is saved as a turn of its owner that does not hold the owner's running-turn slot, and it is listed and woken where its owner reports to, if anywhere. It ends as completed, failed, stopped, or killed, and each ending produces one Completion Note, kept on the job's turn. An oversized result of a background Execute is retained privately for a limited time instead of becoming a Spill, and the Spill paging tool reads it from any turn of the owning conversation. A background Task job waits for its Subagent Session's turn; stopping it stops that turn and the subagent's own jobs.

A session write's reply is also a Background Job of the writing session, which needs no permission for background work. It waits for the other session's turn that takes the write, and the State Store settles it when that turn ends or a person removes the write. Stopping it leaves the other session alone. A subagent stopped with its Task job or its parent's turn drops its pending replies without notes.

### Completion Note

The system message that tells a Background Job's owner how the job ended, consumed exactly once.

It enters a running turn of the owner at its next step, or wakes the owner with a new turn. A note for a script killed by a restart waits for the owner's next turn instead, unless the owner is a Private Producer Conversation. A turn that fails or stops before saving leaves the notes it read for the owner's next turn without waking it again, unless the owner is a Private Producer Conversation, which may have no next turn: its notes wake it again at growing intervals until a wake saves, and only the first failure reaches its destination, saying the wake retries.

A reply note answers one session write. It goes to the exact session that wrote, a Subagent Session or Private Producer Conversation included, and carries the write's queue item ID, the other session, its agent, the write mode, and how the other session's turn ended, with that turn's final answer framed as another session's answer rather than the user's instructions. A write that only stopped a running turn gets a note once that turn is over; a write a person removed, or a reply someone stopped, gets a note saying so, and a stopped reply's note waits for the owner's next turn without waking it. No guardrail reviews reply notes.

### Shadow Agent

An alternative agent, defined in a directory named after its parent agent, that runs the parent's work for comparison. Each parent session owns a separate Shadow Session for each shadow. Shadows are not ordinary agent-picker choices and do not spawn more shadows.

### Shadow Session

A saved comparison child that receives each parent turn's consumed inputs in order, with its own history, model, tool results, and failures.

Web pairs turns by their recorded source and turn identity, not prompt text or completion order. A shadow may take a bounded multiple of its parent's model responses, counted from the turn's first message and again from each steer a person sends: ahead of a running parent it pauses, and after the parent's turn ends it stops once out of steps, with a Web-only note. Workflow turns have no shadows. Shadow output is never delivered to Slack or External MCP. Shadows run permitted tools for real in the shared workspace; separating their output is not a dry-run sandbox.

### Private Producer Conversation

A hidden conversation that runs work for a human-visible destination conversation, such as an External MCP case or a one-off cron run.

It records that destination as the one it reports to. The destination's history shows the run's visible messages in place, live while a turn runs, without copying them; what only the run's own context needs stays out of the destination. Web lists only the destination, and session tags set during the turn land on the destination.
*Avoid:* hidden run

### External MCP Metadata

The key-value pairs an External MCP caller attaches to its session, which the agent sees both as a message in its context and as environment variables for its tools.

The session's first turn records the pairs, together with the conversation's identity, in its Private Producer Conversation; later turns keep the recorded values and see new keys for that turn only. Turns in the session's Web chat read the same recorded pairs, with the conversation identity naming the chat, and a Subagent Session's turns read those of the top-level session and turn it works for, with its own conversation identity.

## Web Voice

### Voice Session

A live spoken call between a person on the Web and OpenAI's voice model, attached to one conversation.

Audio flows between the browser and OpenAI; RocketClaw creates the call with the default `openai` Provider credentials, independently of the conversation agent's Provider, and holds its control connection while the browser's request stays open. The call ends when that request ends, or earlier when RocketClaw or OpenAI ends it, and records why; it has no idle or no-speech timers. With a ChatGPT sign-in RocketClaw reconnects a dropped control connection; with an API key a drop ends the call as lost. A person has at most one live Voice Session: starting another, on any conversation or device, ends their older one, while other people's sessions, even on the same conversation, keep running. Ending a Voice Session never stops the conversation's turns.

### Voice Settings

One person's Web voice state, kept by the server and the same on every device: their current voice conversation for the Voice page, the agent for their next one, their voice, and whether the Voice page shows the transcript.

Two people never share Voice Settings, even in one browser. Retention cleanup never deletes a person's current voice conversation, even with no messages; Reset forgets it, and ordinary cleanup applies after that. A call uses the person's voice when the default `openai` Provider offers it, and that Provider's default voice otherwise.

### Voice Hand-off

A task the voice model passes to the conversation's agent during a Voice Session.

Each one becomes a Web user message marked as voice, holding the request and what the person and the voice model said since the previous hand-off, taken the moment the hand-off arrives. Hand-offs are admitted in arrival order without waiting for the agent. Nothing about the agent's work reaches the voice model while it runs; the final reply of a turn the session handed off is spoken once, as written, unless it is too long to speak or a message typed later in the call supersedes it. A pending question is announced once and answered in the app, never by a hand-off. Not to be confused with a Delegation History, which is work a tool call delegated inside RocketCode.

## Web Spaces

### Space

A named place in the Web that holds tabs, shared by everyone.

The server keeps one list, oldest first, with names unique ignoring case, and the last one cannot be deleted. An agent or cron definition names its space by name; tabs and sessions refer to a space by its ID, so a rename keeps them in it. Where each tab sits, and which space a person is in, is that person's own and follows them across devices; only the space's Shared Group is the same for everyone.

### Shared Group

The one grey tab group named Shared that every space has, shown first in the Web sidebar. Its tabs are the same for every Web user, and anyone can add a tab to it or remove one, which removes it for everyone. A tab can be in a space's Shared Group and in a person's own tabs at once. Its order and collapsing it are each person's own; tabs a person has not placed follow, oldest added first. Deleting a space moves its Shared Group's tabs into the first remaining space's.

### Search Page

A Web tab for searching sessions, one of as many as a person opens, each with its own address under `/search`.

It holds its own searches, shown as tabs inside the page, and can be named; unnamed, it is titled by the query it shows. Its name and searches are the person's own and are saved with their tabs, so they follow the page into groups and other spaces and across devices. Added to a space's Shared Group, it becomes one page everyone in that space sees and edits, its searches and which one is shown included, while the person's own copy stays as it was; whoever takes it out of Shared keeps that one page as their own, together with any searches of their own copy. A Search page never opens as a preview tab, Escape does not close it, and closing its tab discards it. Cmd/Ctrl+P finds every space's Search pages by name or query once something is typed.

### Birth Space

The Space a session was born in.

It is its agent's space, or for a cron's sessions the cron's, else the cron agent's, else for a Web session, or a remote OpenCode session created from the Web, the space the person was in; a forked remote session takes its source's. A named space that does not exist yet is created with the session. A session opens in its Birth Space; a person may move its tab elsewhere and later restore it. Deleting the space clears it. Sessions from before Birth Spaces, and those started outside the Web other than cron report chats, have none.

### Unread

A session's per-person mark that a turn the person sent a Web message into has ended and they have not looked at it since. Other people's turns, and turns with no Web sender, mark no one, except in a session in some space's Shared group, where any turn's end marks every Web person. Having the session's tab active in a focused window clears the mark on all of that person's devices.

## Hosted Pages

### Hosted Page

A file an agent published, served by RocketClaw under a URL that names the agent's space, the agent, and the file path.

It belongs to the agent, not to a space: when the agent's space changes, its pages move with it and links under the old space name redirect. Only people the Web admits can open one. Page scripts run walled off from RocketClaw and can only read their own agent's Page Data.

An agent in a remote OpenCode session publishes through its coding server's MCP bridge instead. Such a page belongs to that session, which stands in for the agent: it lives in the session's Birth Space, and it is deleted with the session's RocketClaw copy or with that space. A session with no Birth Space cannot publish pages.

### Page Data

An agent's JSON key-value entries, which the agent reads and writes and its Hosted Pages may only read. Each agent, shadow agents included, has its own, and so does each remote OpenCode session that publishes pages.

### Page Index

The list of Hosted Pages a Web space shows: HTML pages of agents whose space matches the space's name, ignoring case, plus agents with no space, and HTML pages of remote OpenCode sessions in that space.

## Process layout

### Backend

The one RocketClaw engine: conversation execution, the Thread Queue, cron, skills, agent definitions, Overlay Clones, Reload, and Restart.

### Frontend

A surface the process builds around the Backend that brings work in and carries results out: Web, Slack, External MCP, or cron.

### Protocol

The shared language frontends and backend import. A frontend drops protocol messages it does not handle.

## Durable State

### State Store

RocketClaw's durable database for sessions, Managed Slack Thread routing, goals, cron, scheduled messages, the Thread Queue, External MCP bindings, and the Step Journal.

Each fact lives in one place. There is one record for every conversation of every kind, with what it is, whether Web lists it, where it reports to, and its facts: Slack thread, summary, tags, goal, External MCP binding, and shadow parent. Every turn is one record, whether waiting (Thread Queue items and one-time reminders), running, delivering, or done, Background Jobs included. Saved history, the Step Journal, and cron lines with recurring reminders each live in their own place, as do attachments, Slack channel facts, Voice Settings, Hosted Pages, Spaces and tabs, the run lock, and shadow comparison turns. Data a release retires is kept aside until a later release drops it.

### Step Journal

The durable, conversation-local record of every completed step of in-progress work. Steps include model responses, tool call results, Code Mode host and MCP calls, workflow worker results, pending questions, and accepted steers.

A running turn saves each item of its record once, as a step of its own, instead of rewriting the whole record. An interrupted turn resumes from its last recorded step. Completed steps return their recorded results instead of running again. A clean shutdown lets running calls finish, so only a call cut off by a crash or a forced exit, which started without a recorded result, is reported to the model as interrupted. A turn's steps are cleared when it ends; a stopped or failed turn keeps its record and consumed inputs on its turn, where History reads them.

## Relationships

- A Root Slack Mention creates a Managed Slack Thread.
- An Adhoc Callout creates or takes over a Managed Slack Thread.
- A Root Slack Mention in an unmapped joined channel is an Adhoc Callout when an `@` Channel Entry exists.
- A mention in a Managed Slack Thread becomes one Thread Queue item and runs as its own turn.
- A Report Thread ignores Slack input; its conversation continues in Web.
- A Managed Slack Thread, Thread Queue, External MCP binding, and Step Journal persist in the State Store.
- Reload replaces Overlay Clones.
- Frontends and the Backend share Protocol.
- A turn uses the Autocompaction Threshold of the Provider that serves its model.
- Code Mode exposes Execute and Host Tools; an oversized Execute result becomes a Spill owned by that RocketCode Turn.

## Flagged ambiguities

- "'turn' had been used for both a Slack conversation occupancy slot and a RocketCode model loop — these are distinct."

## Retired

- **Slack Steer**: a human Slack reply injected into a running turn. Removed when Slack replies and mentions stopped steering; steers now come from Web and from agents.
- **Enqueued Slack Message**: a later-turn prompt stashed from Slack with `$enqueue`. Removed with Slack's commands; waiting Slack work is now an accepted mention in the Thread Queue.
- **Buffered Follow-Up**: a mid-turn Slack message held until the active turn completed. Replaced by Slack Steer, itself later removed; a mid-turn mention now waits in the Thread Queue.
- **Quickbench, BAR, ELO Scorer**: an internal benchmarking tool that ran agent variations and ranked them with an ELO judge, with its benchmark units and scorers. Removed when Slack, External MCP, and cron moved onto one Backend.
