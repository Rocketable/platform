# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent. In Slack, only mentions start its turns; replies without a mention start nothing.

A Managed Slack Thread has one active turn at a time. A mention received during that turn waits in the Thread Queue and runs as the next turn; it never steers. Web can also work in the conversation: Web input it consumes and every reply are posted into the thread. A second Slack delivery of an accepted mention is not a new send. While any of its turns runs, whichever surface started it, Slack shows the thread as working, or as needing someone while the turn waits for an answer in Web, and an allowlisted user's Stop in Slack ends that turn; besides the turn's checklist and any Web input it consumes, nothing is posted until its answer arrives as a new reply.

### Root Slack Mention

An authorized Slack app mention at a channel's top level. It starts a new Managed Slack Thread whose first turn is the mention. A bare Root Slack Mention with no text, file, or forward is ignored.

### Adhoc Callout

An authorized Slack app mention that starts or takes over a Managed Slack Thread in a public channel, private channel, or group DM the bot has already joined. Taking over a thread RocketClaw does not know brings in a bounded number of its earlier messages, and a bare mention is enough there.

Unmapped conversations use the `@` Channel Entry. Mapped channels keep that room's agents and allowlist. 1:1 DMs are not Adhoc Callouts.

### `@` Channel Entry

A configured Slack channel entry named `@` that supplies agents and an allowlist for Adhoc Callouts in joined channels that have no entry of their own. It is not a Slack channel.

### Report Thread

A Slack thread an earlier version posted a cron report or an External MCP session into: its conversation, now a Web chat created by cron or bound to the session, or an unrecorded thread whose root RocketClaw's own bot posted. The last case covers threads whose record expired.

A Report Thread is read-only. RocketClaw ignores every reply and mention in it, showing no working state and posting no reply, and posts nothing more into it. Read-only means RocketClaw ignores input, not that Slack locks the thread. Work on it continues in Web.

### Thread Queue

The durable, per-conversation list of work waiting to run as later turns: accepted Slack mentions, work queued or stashed from Web, messages another session's agent sends, External MCP prompts that arrive while the conversation's turn is active, and prompts a parent sends into its Subagent Session.

Each item is a waiting turn of its conversation, queued, stashed, or held, keyed by its input ID; starting it makes it the running turn. Saved items that never started run after a restart without another incoming message. An agent-written item carries its sender, the sending session and agent, shown where a person's Principal would be; it is never a person's input, even after a person promotes it. Only a person stashes items. An agent may also stop the running turn: its stop goes ahead of every earlier item, and a stop without a prompt adds no item, only a line in the transcript naming the sender once the stopped turn has saved. A session write from another session's agent is refused before it adds or stops anything when the receiving agent's recipient setting does not accept the writing agent. When the turn that took such an item ends, or a person removes the item, the writing session gets a reply Completion Note.

A Slack mention is accepted at most once per conversation, keyed by its Slack message. Web shows the items together with the conversation's scheduled messages and can promote or remove them, and also pop or reorder agent-written items; Slack is not told. After a turn ends, a still-continuing goal wins the next slot; otherwise the next item is the first ready one, where a queued item is ready in its place and a scheduled message at its due time. A not-yet-due scheduled item blocks later items until it runs or is cancelled.

## Prompt Provenance

### Principal

The human actor a connector attributes to a human-originated prompt.

Each connector chooses the string. The backend prints it in the model header, and Web also shows it with the message. Authorization uses connector identity, not this string.

## Conversation Work

### Goal

An objective a person sets on a conversation from Web that keeps its agent taking turns toward it until the agent marks it complete, someone stops it, or its turn budget, when it has one, is spent.

While a Goal continues, its next turn takes the conversation's next slot ahead of waiting Thread Queue items, and its turns act for the person who set it: their tools see that person's name and email, though the model header names no Principal, since a goal turn is not a person's prompt. Stopping the conversation's running turn stops its Goal too.

### Steer

Input sent into a conversation's running turn so the agent takes it into that turn instead of waiting for a turn of its own.

A steer joins a running turn that still takes input, whoever sent it and whoever started the turn; otherwise it runs as the next turn. Workflow turns, and the hidden runs of cron and External MCP, always take a steer as their next turn. Steers come from Web and from agents; a Slack mention never steers.

### Scheduled Message

A message an agent schedules into its own conversation to arrive after a delay, once or repeating until the conversation's schedule is reset; when due it runs as a turn of that conversation.

Thread Queue items placed after a Scheduled Message wait behind it until it runs or is cancelled.

### Workflow

A saved Starlark program that runs a task as agent calls made by named workers, grouped in phases. A person starts one from Web with `$workflow`, and an agent whose permissions allow it can run one too. A workflow turn has no Shadow Sessions, and a steer sent during it runs as the next turn instead of joining it.

## Runtime Assets

### Overlay Clone

The live checkout of one configured overlay, a git repository whose agents, skills, workflows, cron definitions, and scripts RocketClaw layers over its own, as Reload last installed it.

### Reload

Hot-load of published overlay files the live daemon can apply without a process restart.

Reload rebuilds the live overlay clones. It is not interchangeable with Restart.

### Restart

A process restart required when the overlay list or runtime config changed.

Restart is not a substitute for Reload when only overlay file contents changed.

## Model Providers

### Provider

A named account that serves model requests: its sign-in and, for an API-key Provider, the OpenAI-compatible endpoint it calls. A model name without a Provider qualifier uses the default Provider; a qualified name selects that Provider.

Root and child agents resolve their Provider independently. There is no implicit failover from one Provider to another.

Each Provider can set its own Autocompaction Threshold, the token count at which a turn asks that Provider to compact conversation history; unset means the runtime default. A child turn uses a different threshold only when it resolves a different Provider.

A Provider authenticates with either an API key or a ChatGPT sign-in. A ChatGPT sign-in Provider is served by ChatGPT's Codex backend, not the public API, so RocketClaw reshapes its requests for that backend. There, compaction is a separate request RocketClaw makes when a response reaches the Provider's Autocompaction Threshold or the backend reports the context is full.

## Harness

RocketClaw's agent engine: it runs the model loop, tools, and permissions for every turn.
*Avoid:* RocketCode, its former name from when it was a separate component; older plans and specs, and some config and stored names, still use it.

### Code Mode

The harness agent style that asks the model to write a short script and run it through Execute instead of calling Host Tools as top-level model tools.

### Execute

The model-facing tool that runs a Code Mode script and returns one string to the model.

Host tools inside the script return full strings to the script. Only the string Execute sends back to the model is clipped when it is oversized.

### Host Tool

A sandbox capability the Code Mode script can call directly, as opposed to a model-facing tool such as Execute. Host tools include filesystem, shell, fetch, and RocketClaw's own tools.

### Emulated Shell

An agent's opt-in shell mode, always on for an Easy Agent, in which its shell commands run in real Bash inside a Linux sandbox instead of host bash.
*Avoid:* sandboxed bash, bubblewrap shell

Each command runs in a fresh sandbox with no network. Only the agent's declared working directory reaches the host disk writable, with its uploads and skill folders read-only; temporary files are the command's own and gone when it ends. The commands can start only the host programs the agent declares, and those programs see only the host's environment plus the agent's secrets, never variables the sandboxed script set. Its curl fetches through RocketClaw under the agent's allowed targets. An agent that asked for the Emulated Shell but cannot get one has its shell commands fail rather than fall back to host bash.

### Script Call Record

A small record of one Host Tool or MCP call a Code Mode script made, which Web lists under the script's Execute call and an agent reading the session also sees. When the whole script had a Permission Review, one more record holds only that decision.

It holds the call's name, its clipped arguments, a status, a one-line summary of the output, and the outcome and reason of the call's Permission Review when it had one; it never holds the output. While its Harness Turn runs, each record is saved on its own when the call starts and when it finishes, so the turn's other saves never rewrite it; the finished turn keeps its records. An Execute call keeps a bounded number of records, and none are added after the turn ends.

### Spill

A turn-scoped file that holds the full Execute result when that result is too large to send to the model.

A Spill has an opaque result ID registered only with its owning Harness Turn. The model retrieves bounded pages through a dedicated top-level tool, without gaining filesystem read access. The ID expires and the file is deleted when the turn ends; a turn cut off by a shutdown keeps its Spills, and resuming it brings them back.

### Harness Turn

One model loop in the harness. It owns that loop's Spills.

A Harness Turn is not a conversation's running turn. A conversation's turn usually drives one Harness Turn, but a workflow turn drives several, and the two lifetimes are not the same object.

### Delegation History

A session history of work one tool call delegated: its Permission Reviews, made before the call runs and during it, or its Task subagent.

Its conversation ID extends the producing conversation's ID with a segment derived from the tool call, so it stays unique when a provider reuses call IDs and stays the same when a turn resumes; a delegation inside it adds another segment. A Task subagent's Delegation History is a Subagent Session, which a later Task call can continue. Review histories have no Managed Slack Thread or Web chat of their own. All of them are deleted and forked with their producing conversation; that, not the shape of the ID, is what sets them apart, since other conversation IDs can contain the same separator.

### Permission Review

An automatic check, by a reviewer agent, of a tool call or a whole Execute script that an agent's permissions mark for review, made before it runs. It allows or denies with a reason; a denial, an unreadable answer, or a failed review blocks the call. Its decision is read from the reviewer's final answer, whichever way the model was reached.
*Avoid:* approval, approver check

When a decision model is configured, a review asks it first and keeps its answer only when every part is confident, nothing needs a closer look at files or state, and the answers agree with the guardian's outcome rules; otherwise the reviewer reviews as usual. The embedded guardian uses the decision model an easy reviewed agent or the runtime names; a custom reviewer uses one only when its own `approver.decision` names one. The decision model reads its own prompt, not the chat reviewer's added rules. A decision-model answer is saved as the reviewer's would be.

### Task

The model-facing tool through which an agent delegates a prompt to another agent type, or an Easy Agent to itself on one of its subagent models; the work runs as a Subagent Session, and its answer returns as the tool's result. Another agent cannot Task an Easy Agent.

### Easy Agent

An agent whose file sets `mode: easy`. A section its file leaves out turns on everything in it, a section it writes turns on only what it lists, and everything on is reviewed by the agent's own approver. Its shell commands always run in the Emulated Shell, in its own folder, and it gets only the commands, secrets, MCP tools, skills, models, and Routines its file names.

### Sticky Session

The one Web session a sticky agent keeps: per person for `sticky: user`, matched by login rather than display name, and one for everyone for `sticky: global`. Each chat records at creation whether it is born sticky, and that never changes; a chat born `off` never becomes a Sticky Session. A new Web session or session-tool creation resumes the agent's Sticky Session and starts one only when there is none. A personal sticky request without an identifiable person is refused. A `global` agent's cron and Routine runs report into its Sticky Session, opening it when there is none; a `user` agent cannot be scheduled. The chat stays in its original space, cannot be forked, and keeps its row while messages older than 90 days expire. A session switches only to agents of the kind it was born as, skipping agents that already have that kind of session for its person, or for everyone with `global`. A sticky agent is top-level: it may delegate, including to itself, but no other agent delegates to it or names it as a permission reviewer. Slack threads and External MCP conversations use only sticky: off agents and keep separate histories. The Voice page calls only sticky: user agents, each on the person's own Sticky Session.

### Routine

A schedule an Easy Agent declares in its own file, in place of a cron file, that runs the agent with the routine's instruction and reports as a cron job does. Its identity is its agent and schedule text, and its runs have no Shadow Sessions.

### Subagent Session

A Task subagent run as a session like any other, with the same Thread Queue, restart recovery, and history saving as top-level sessions, that records its parent session, is not listed in the sidebar or message search, and cannot be written to by other sessions' agents.

A Task call writes its prompt into the session's Thread Queue and answers with the turn that runs it. It acts for its top-level session's running turn: session tags and session writes land on that session, though replies to its writes come back to the subagent and the recipient check uses the subagent's own agent; sessions it creates keep that turn's agent, creator, and agent limits, files it attaches go out with that turn's reply, and its delegation depth counts from that session's agent. It cannot schedule messages. Stopping a session's turn while its foreground Task call waits stops the subagent's current turn too, unless a Background Job waits for that turn; the prompt the call sent, if still queued, is held in the subagent's Thread Queue until the next message sent there, or a goal, releases it to run just before that message or goal, and no note reaches the stopped session.

A Completion Note can wake a subagent after its Task call already returned. When that wake turn ends, its answer goes to the parent as a Task Completion Note; a parent that is itself a subagent reports its own wake turn the same way, so the answer climbs to the top-level session. A stopped wake turn, or one that took a Task call's prompt, reports nothing.

### Background Job

An Execute script or Task subagent that keeps running after its tool call returned, owned by the conversation (a Subagent Session included) that started it. Only agents whose permissions allow background work can start one, and a human can move running work into one from Web.

A Background Job is identified by the tool call that started it. It is saved as a turn of its owner that does not hold the owner's running-turn slot, and it is listed and woken where its owner reports to, if anywhere. It ends as completed, failed, stopped, or killed, and each ending produces one Completion Note, kept on the job's turn. An oversized result of a background Execute is retained privately for a limited time instead of becoming a Spill, and the Spill paging tool reads it from any turn of the owning conversation. A background Task job waits for its Subagent Session's turn; stopping it stops that turn and the subagent's own jobs.

A session write's reply is also a Background Job of the writing session, which needs no permission for background work. It waits for the other session's turn that takes the write, and the State Store settles it when that turn ends or a person removes the write. Stopping it leaves the other session alone. A subagent stopped with its Task job or its parent's turn drops its pending replies without notes.

### Completion Note

The system message that tells a Background Job's owner how the job ended, consumed exactly once.

It enters a running turn of the owner at its next step, or wakes the owner with a new turn. A note for a script killed by a restart waits for the owner's next turn instead, unless the owner is a Private Producer Conversation. A turn that fails or stops before saving leaves the notes it read for the owner's next turn without waking it again, unless the owner is a Private Producer Conversation, which may have no next turn: its notes wake it again at growing intervals until a wake saves, and only the first failure reaches its destination, saying the wake retries.

A reply note answers one session write. It goes to the exact session that wrote, a Subagent Session or Private Producer Conversation included, and carries the write's queue item ID, the other session, its agent, the write mode, and how the other session's turn ended, with that turn's final answer framed as another session's answer rather than the user's instructions. A write that only stopped a running turn gets a note once that turn is over; a write a person removed, or a reply someone stopped, gets a note saying so, and a stopped reply's note waits for the owner's next turn without waking it.

### Shadow Agent

An alternative agent, declared under one parent agent, that runs the parent's work for comparison. Each parent session owns a separate Shadow Session for each shadow. Shadows are not ordinary agent-picker choices and do not spawn more shadows.

### Shadow Session

A saved comparison child that receives each parent turn's consumed inputs in order, with its own history, model, tool results, and failures.

Web pairs turns by their recorded source and turn identity, not prompt text or completion order. A shadow may take a bounded multiple of its parent's model responses, counted from the turn's first message and again from each steer a person sends: ahead of a running parent it pauses, and after the parent's turn ends it stops once out of steps, with a Web-only note. Workflow turns have no shadows. Shadow output is never delivered to Slack or External MCP. Shadows run permitted tools for real in the shared workspace; separating their output is not a dry-run sandbox.

### Private Producer Conversation

A hidden conversation that runs work for a human-visible destination conversation, such as an External MCP case or a one-off cron run.

It records that destination as the one it reports to. The destination's history shows the run's visible messages in place, live while a turn runs, without copying them; what only the run's own context needs stays out of the destination. Web lists only the destination, and session tags set during the turn land on the destination.
*Avoid:* hidden run

### External MCP Session

A conversation an outside MCP client drives by sending prompts under its own conversation ID to a chosen agent. It shows as a Web chat, where people can continue it or switch its agent, while the client's turns run in a Private Producer Conversation that reports to that chat.

### External MCP Metadata

The key-value pairs an External MCP caller attaches to its session, which the agent sees both as a message in its context and as environment variables for its tools.

The session's first turn records the pairs, together with the conversation's identity, in its Private Producer Conversation; later turns keep the recorded values and see new keys for that turn only. Turns in the session's Web chat read the same recorded pairs, with the conversation identity naming the chat, and a Subagent Session's turns read those of the top-level session and turn it works for, with its own conversation identity. The pairs are untrusted and kept apart from the routing and provenance facts RocketClaw records on a prompt, so whatever keys a caller sends, it cannot choose the agent, pose as a sender, or change how the turn is queued.

## Web Voice

### Voice Session

A live spoken call between a person on the Web and OpenAI's voice model, attached to one conversation.

Audio flows between the browser and OpenAI; RocketClaw creates the call with the default Provider's credentials, independently of the conversation agent's Provider, and holds its control connection while the browser's request stays open. The call ends when that request ends, or earlier when RocketClaw or OpenAI ends it, and records why; it has no idle or no-speech timers. With a ChatGPT sign-in RocketClaw reconnects a dropped control connection; with an API key a drop ends the call as lost. A person has at most one live Voice Session: starting another, on any conversation or device, ends their older one, while other people's sessions, even on the same conversation, keep running. Ending a Voice Session never stops the conversation's turns.

### Voice Settings

One person's Web voice state, kept by the server and the same on every device: the sticky: user agent whose Sticky Session the Voice page calls, their voice, and whether the Voice page shows the transcript.

Two people never share Voice Settings, even in one browser. A call uses the person's voice when the default Provider offers it, and that Provider's default voice otherwise.

### Voice Hand-off

A task the voice model passes to the conversation's agent during a Voice Session.

Each one becomes a Web user message marked as voice, holding the request and what the person and the voice model said since the previous hand-off, taken the moment the hand-off arrives. Hand-offs are admitted in arrival order without waiting for the agent. Nothing about the agent's work reaches the voice model while it runs; the final reply of a turn the session handed off is spoken once, as written, unless it is too long to speak or a message typed later in the call supersedes it. A pending question is announced once and answered in the app, never by a hand-off. Not to be confused with a Delegation History, which is work a tool call delegated inside the harness.

## Web Spaces

### Space

A named place in the Web that holds tabs, shared by everyone except people whose Space Limit leaves it out.

The server keeps one list, oldest first, with names unique ignoring case, and the last one cannot be deleted. An agent or cron definition names its space by name; tabs and sessions refer to a space by its ID, so a rename keeps them in it. Where each tab sits, and which space a person is in, is that person's own and follows them across devices; only the space's Shared Group is the same for everyone.

### Tab Group

A named, colored run of one person's adjacent tabs in one Space, which can be collapsed to its header.

A group sits where its first tab is and lives in that tab's Space; moving it to another Space moves all its tabs. It goes away when its last tab closes or leaves it, unless the person keeps it: a kept group stays where it was, across reloads and devices, offers a new session inside it while empty and expanded, and goes once it is both empty and no longer kept. The Shared Group and the Top Shelf are groups with rules of their own.

### Preview Tab

The one italic tab a Web window shows while the person arrows through the session palette, holding the highlighted session.

It belongs to that window alone: it is never saved, it never becomes the tab its space returns to, and other windows' tab changes leave it where it is unless they open that same session or delete its space. Choosing the session keeps it as an ordinary tab; dismissing the palette, typing, or running a command closes it. Only sessions that would open in the current space are previewed.

### Closed Tab

One of the last 50 tabs a person closed, which the server keeps, newest first, with the Space it was in, its group and position, and a Search Page's searches, so the Web's Recently closed page or Cmd/Ctrl+Alt+T reopens it there, or in the current space when that Space is gone. New session and Preview Tabs never become one; reopening it or deleting its session takes it off the list. A Closed Tab whose park time is still to come, or whose park waits for its session's turn to end, is never dropped from the 50; when that time comes, the server puts the tab back by itself.

### Shared Group

The one grey tab group named Shared that every space has, shown first in the Web sidebar. Its tabs are the same for every Web user who can see its space, and any of them can add a tab to it or remove one, which removes it for everyone. A tab can be in a space's Shared Group and in a person's own tabs at once. Its order and collapsing it are each person's own; tabs a person has not placed follow, oldest added first. Deleting a space moves its Shared Group's tabs into the first remaining space's.

A Shared Group is a tab group several people share, and its tabs behave for each of them like their own: something new on a Shared session, Cron, Pages or Dashboard tab makes it waiting with a notification for everyone who can see its space, and an agent's question ends a park on it. A Shared Search Page's finished search marks only the person who entered it, whose search ran. Each person's Unread and Parked on a Shared tab are their own: a tab one person parked stays in the group, dimmed for them only. A Shared tab has no Close: Remove from Shared takes it out for everyone.

### Search Page

A Web tab for searching sessions, one of as many as a person opens, each with its own address.

It holds its own searches, shown as tabs inside the page, and can be named; unnamed, it is titled by the query it shows. Its name and searches are the person's own and are saved with their tabs, so they follow the page into groups and other spaces and across devices. Added to a space's Shared Group, it becomes one page everyone in that space sees and edits, its searches and which one is shown included, while the person's own copy stays as it was; whoever takes it out of Shared keeps that one page as their own, together with any searches of their own copy. Escape does not close a Search page, and closing its tab discards it unless it is reopened as a Closed Tab. The Web's session search palette finds every space's Search pages by name or query once something is typed.

The server stores each copy of a page as its own row, with one row per search. A Search page's search runs on the page's server, not in the browser, and keeps running when the person switches tabs, closes the window or loses the connection; a new query for the same search replaces the run before it, and the page receives the result when the person comes back. When a search the person entered finishes and they are not looking at the tab, that person alone gets one Unread mark with a notification, also on a Shared page. Reopening the page or new sessions in the list run a search again without marking. Results from Remote RocketClaws are still fetched by the browser while the page is open, and a Dashboard's lanes are not run this way.

### Remote OpenCode Session

A session on a configured OpenCode coding server that RocketClaw keeps a synced copy of, so the Web lists, opens, creates, forks, and deletes it beside RocketClaw's own sessions; its agent runs on that server, not in the Harness.

### Dashboard

A Search Page shown as a board in one of several styles, whose searches are its lanes, tracking the work of the space its tab sits in.

Each session in that space appears once, in the first lane it matches. A session without a Birth Space counts in its agent's space, or in every space when its agent has none; a person with a Space Limit never sees such a session. A new Dashboard starts in the space's Shared Group, and any Dashboard or Search Page can be duplicated.

The server watches every open Dashboard that has a lane flagged to notify, every 5 seconds, whether or not any window is open. When a session enters a flagged lane, it gives the Dashboard tab an Unread mark with a notification for everyone who has that Dashboard open: in their own tabs, on a Top Shelf this server keeps, or in the Shared Group of a space they can see. The notification names the lane and the newest session, and how many more arrived. A session that a lane of unread sessions (`is:unread`) lists only for a quiet mark, such as the person's own Mark as unread, has not arrived. The first check after the server starts, and the first after a lane's query, order or flag changes, only records what each lane holds.

### Remote RocketClaw

Another RocketClaw server declared in the local server's configuration, whose chats, spaces and pages the local Web page lists and operates beside its own; the browser talks to it directly, and it identifies each person by their own Tailscale login. Its spaces appear under it in the space picker and hold only its own chats. Not to be confused with a Remote OpenCode Session, which RocketClaw keeps a synced copy of.

### Top Shelf

One person's own tab group above the space picker, shown only when the local RocketClaw has Remote RocketClaws, holding tabs from any server. It is the same in every space and on every server, and opening one of its tabs shows that page on its own server and in its own space without creating a tab, as a Shared Group tab opens; a Search Page opened from it is edited in place. Tabs join, leave and reorder as in any group, and each names the server and space it comes from. A deleted session leaves it.

The local server stores it, one row per shelf tab. A tab can be on the shelf and in the strip at once. A shelf tab whose server is offline shows greyed and offers only Remove from Top Shelf. Its Unread and Parked live on the tab's own server, and no other server learns what the shelf holds: a shelf tab of the local server reaches its holder as their own tab does, while another server's session that is only on the shelf reaches them only for turns they sent.

### Birth Space

The Space a session was born in.

It is its agent's space, or for a cron's sessions the cron's, else the cron agent's, else for a Web session, or a remote OpenCode session created from the Web, the space the person was in; a forked remote session takes its source's. A named space that does not exist yet is created with the session. A session opens in its Birth Space; a person may move its tab elsewhere and later restore it. Deleting the space clears it. Sessions from before Birth Spaces, and those started outside the Web other than cron report chats, have none.

### Space Limit

The reach of a person the config's `permissions` maps to one or more Spaces: those Spaces, the sessions in them, and the agents whose `space:` names one of them. A person `permissions` does not list has no Space Limit and reaches everything.

A session is in a Space when its Birth Space is that Space, or, with no Birth Space, when its current agent's `space:` names it. Subagent Sessions and permission-review histories follow their top-level session, Shadow Sessions their parent, and cron run traces their cron's space. A Remote OpenCode Session is never within a Space Limit. A limited person takes a turn only while the session's current agent is theirs, and steers or answers a running turn only while that turn's agent is theirs too; a session someone switched to an outside agent stays readable to them, and they can switch it back. A Space Limit applies to people in the Web only, not to agents: an agent in the Space still acts with its own permissions.

### Unread

One person's attention state on one tab, which the server keeps for every kind of tab, by the tab's location: read, waiting, or Parked. New session, the Preview Tab and a kept group's placeholder have none. A waiting tab shows a bold title on every device, and the window title's count and Cmd/Ctrl+Alt+A count it.

The server writes every mark; the browser only reads them and reports which tab is in view. Something new makes a tab waiting: a session's turn ends or its agent starts asking; a search the person entered on a Search Page finishes; a session enters a Dashboard's flagged lane; a cron's run ends, once its last turn ends with nothing left to wake it, for that cron's tab and the Cron overview of its space; an HTML file of a Hosted Page is stored, for that page's tab and the Page Index of its space. Agents, Skills, Settings, Shortcuts, Voice and Recently closed get only what the person sets. A session's turn end or question reaches the people who sent or steered that turn from the Web, those with the session in their own tabs, those with it on a Top Shelf this server keeps, and everyone who sees it in a space's Shared Group; Cron and Pages tabs reach the people who have them as their own tab, on that shelf, or Shared. The browser notifies only for a tab the person has, there or on another server's Top Shelf: news on a tab they closed waits, quiet, until they open it again, and the end of a park brings a closed tab back. A person with a Space Limit is never marked for a space outside it. Nobody is marked for the tab they viewed in the last 10 seconds, or the last 4 for a question or a park's end.

A mark raises a desktop notification in the person's open windows unless it is quiet. A mark is quiet when the person set it with Mark as unread, when it was written while the tab was Parked or was there when they parked it, and once they open the tab again after closing it. A window whose tabs are older than its session list names holds its notifications until it reloads them, so a tab closed in another window never notifies. Viewing the tab in a focused window, or Mark as read, clears the mark on all of the person's devices, even while the agent still waits on their answer, whose Needs you badge stays. Marked unread on the tab in view, a mark stays until they go to another tab and come back. Closing a tab keeps its mark.
*Avoid:* muted, snoozed

### Parked

A tab a person set aside, until they unpark it, until a time they chose, or, for a session, until its next turn ends. It keeps its Space, group and position, but the Web shows it in a Parked section at the bottom of the tab strip, collapsed by default, which lists the person's parked tabs from every space with when each park ends. A parked Shared or Top Shelf row stays where it is, dimmed for that person.

A parked tab is never bold and raises no notification: anything new on it leaves a quiet Unread mark. Unparking returns it to its place, bold if something happened that the person has not seen, without a notification either way. It comes back by itself, bold with a notification, when its time is up, when the turn a park until it is done waited for ends, with the reply's own notification, or when its agent asks a question, which makes the park due at once, also on a Shared tab or one parked and closed. Closing a tab parked until unparked ends the park; one parked until a time, or until it is done, stays parked and comes back when the park ends. Close others, Close to the right, Close all and Close group close this space's parked tabs too.

The server checks a person's park times each time one of their windows asks for the session list, which an open window does every few seconds, so a park ends on the first such poll after its time; one that comes due while no window is open shows bold without a notification when a window opens. A park time that comes due on a closed tab brings the tab back where it was: in its Space, in its group when that still exists, and at its old position when possible.

## Hosted Pages

### Hosted Page

A file an agent published, served by RocketClaw under a URL that names the agent's space, the agent, and the file path.

It belongs to the agent, not to a space: when the agent's space changes, its pages move with it and links under the old space name redirect. Only people the Web admits can open one, and a person with a Space Limit only when the page's space is in it. Page scripts run walled off from RocketClaw and can only read their own agent's Page Data.

An agent in a remote OpenCode session publishes through its coding server's MCP bridge instead. Such a page belongs to that session, which stands in for the agent: it lives in the session's Birth Space, and it is deleted with the session's RocketClaw copy or with that space. A session with no Birth Space cannot publish pages.

### Page Data

An agent's JSON key-value entries, which the agent reads and writes and its Hosted Pages may only read. Each agent, shadow agents included, has its own, and so does each remote OpenCode session that publishes pages.

### Page Index

The list of Hosted Pages a Web space shows: HTML pages of agents whose space matches the space's name, ignoring case, plus agents with no space, except for a person with a Space Limit, and HTML pages of remote OpenCode sessions in that space.

## Process layout

### Backend

The one RocketClaw engine: conversation execution, the Thread Queue, skills, agent definitions, Overlay Clones, Reload, and Restart.

### Frontend

A surface the process builds around the Backend that brings work in and carries results out: Web, Slack, External MCP, or cron.

### Protocol

The shared language frontends and backend import. A frontend drops protocol messages it does not handle.

## Durable State

### State Store

RocketClaw's durable database for sessions, Managed Slack Thread routing, goals, cron, Scheduled Messages, the Thread Queue, External MCP bindings, and the Step Journal.

Each fact lives in one place. There is one record for every conversation of every kind, with what it is, whether Web lists it, where it reports to, and its facts: Slack thread, summary, tags, goal, External MCP binding, and shadow parent. Every turn is one record, whether waiting (Thread Queue items and one-time Scheduled Messages), running, delivering, or done, Background Jobs included. Saved history, the Step Journal, and cron lines with recurring Scheduled Messages each live in their own place, as do attachments, Slack channel facts, Voice Settings, Hosted Pages, Spaces and tabs, the run lock, and shadow comparison turns. Data a release retires is kept aside until a later release drops it, with one exception: the release that brought one attention state per tab deleted the old attention data outright on upgrade (mutes, snoozes, unread marks, and the tab rows kept only to hold a mark) and dropped the Top Shelf and Search Page JSON once it had unpacked them into rows.

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
- Code Mode exposes Execute and Host Tools; an oversized Execute result becomes a Spill owned by that Harness Turn.

## Flagged ambiguities

- "'turn' is used for both a conversation's running turn and a harness model loop — these are distinct."
- "'preview tab' once meant a saved tab that the next opened location replaced; it now means only the palette's Preview Tab, and every other opened location gets an ordinary tab."

## Retired

- **Slack Steer**: a human Slack reply injected into a running turn. Removed when Slack replies and mentions stopped steering; steers now come from Web and from agents.
- **Enqueued Slack Message**: a later-turn prompt stashed from Slack with `$enqueue`. Removed with Slack's commands; waiting Slack work is now an accepted mention in the Thread Queue.
- **Buffered Follow-Up**: a mid-turn Slack message held until the active turn completed. Replaced by Slack Steer, itself later removed; a mid-turn mention now waits in the Thread Queue.
- **Quickbench, BAR, ELO Scorer**: an internal benchmarking tool that ran agent variations and ranked them with an ELO judge, with its benchmark units and scorers. Removed when Slack, External MCP, and cron moved onto one Backend.
- **Mute, Snooze**: a person's switch that stopped a session tab's desktop notifications, for good or until a time, while its Unread title still showed. Replaced by Parked.
