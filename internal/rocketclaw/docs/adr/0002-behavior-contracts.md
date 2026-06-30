# 0002. Behavior Contracts

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw preserves a small set of behavior contracts that are more important than internal code shape. Refactors must preserve these contracts unless the human partner explicitly approves a spec change.

## Scope

This ADR governs regression-sensitive runtime behavior: message flow, prompt framing, command interpolation, routing, delivery, restart, and safety boundaries.

## Context

RocketClaw has lost features when refactors treated behavior as removable plumbing. These contracts make the product behavior explicit so code size pressure and cleanup work do not erase capabilities.

## Normative Contracts

### Prompt Shell Interpolation

| Source                                                         | ``!`cmd` `` expands? | Rationale                                                                |
|----------------------------------------------------------------|----------------------|--------------------------------------------------------------------------|
| Primary agent prompts                                          | Yes                  | Trusted workspace prompt source.                                         |
| Subagent prompts                                               | Yes                  | Trusted workspace prompt source.                                         |
| Skill contents loaded by the skill tool                        | Yes                  | Trusted workspace prompt source.                                         |
| Raw/cron prompt input                                          | Yes                  | Cron bodies are trusted workspace files sent through raw runs.           |
| Text connector/Discord voice/browser/MCP human input in the persistent bridge | No                   | External/human input must remain literal.                                |
| Scheduled-message prompt text in the persistent bridge         | No                   | It follows persistent bridge input rules unless explicitly reclassified. |
| `rocketclaw_start_new_thread` first task prompt text in the persistent bridge | No                   | Model/tool-supplied prompt text must remain literal.                     |
| `AGENTS.md` workspace instructions                             | No                   | Root instructions are loaded literally.                                  |

Expansion uses RocketCode semantics: pattern ``!`command` ``, workspace-root cwd, stdout insertion only, and command failures do not fail prompt preparation.

Every RocketClaw-originated `rocketcode.PromptInput` must be framed with one trusted runtime-generated prompt header before the prompt body:

```text
[Origin media=Media principal=Name additional_instructions="Reply in plain text suitable for both Slack and text-to-speech. Avoid markdown unless it is necessary."]

<body>
```

For prompts without a human principal, the `principal=Name` field is omitted. For example, raw-run cron prompts also omit `additional_instructions`, so their header is:

```text
[Cron media=Text]

<body>
```

The first bracket token is `Origin`. It names the RocketClaw runtime source category responsible for the prompt, not a connector-provided display string. Accepted origin values are product enum values chosen by RocketClaw code from runtime context: `Slack`, `Discord`, `Cron`, `ExternalMCP`, `Terminal`, `Web`, and `System`. New origin values require product-contract review because model-visible provenance changes.

`Media` names the interaction medium inside that origin. Accepted media values are product enum values chosen by RocketClaw code from runtime context: `Text` and `Voice`. `Text` covers typed connector messages, terminal input, External MCP prompts, scheduled/system text, and cron prompt files. `Voice` covers transcribed speech prompts. New media values require product-contract review.

`Name` is the principal: the RocketClaw-known human actor responsible for a human-originated prompt. It should be the best runtime-authenticated or connector-derived human name available for that turn, such as a configured human profile display name, connector account name, Basic Auth username, terminal user name, or, when no safer human name is available, that connector's stable user identifier. Principal is model-visible provenance only; authorization and permissions must continue to use RocketClaw runtime metadata and connector checks, not the prompt header text. Non-human prompts, system prompts, automated continuations, scheduled-message prompts, and cron/raw-run prompts omit `principal` entirely.

`additional_instructions` is trusted turn guidance for how RocketCode should handle the prompt body. It is not provenance. It must not be derived from external prompt body text. When present, it is serialized as a quoted string value in the header. Persistent bridge prompt text must not include separate model-visible instruction paragraphs, `User message:` labels, or `Internal note:` labels; the instruction belongs in the header and the prompt body begins after the blank line.

Normal persistent-bridge reply turns use this default `additional_instructions` text:

```text
Reply in plain text suitable for both Slack and text-to-speech. Avoid markdown unless it is necessary.
```

A selected agent may override that normal-reply default by declaring a non-empty string `additionalInstructions` field in trusted agent frontmatter. Missing, empty, or non-string `additionalInstructions` values do not override the default. This override applies only to normal persistent-bridge reply turns.

Persistent-bridge internal-note turns use this fixed `additional_instructions` text:

```text
Internalize the following note into the active conversation state exactly as written. Respect the content of the message and do not paraphrase, summarize, translate, or normalize whitespace. Do not reply or acknowledge it unless the human explicitly asks you to.
```

Agent frontmatter must not override the internal-note instruction. Raw-run cron prompts use `[Cron media=Text]` and omit `additional_instructions`.

Field order is fixed: first `Origin`, then `media=Media`, then `principal=Name` when present, then `additional_instructions="..."` when present. The header is generated from RocketClaw runtime metadata and trusted runtime-selected instruction text, never from prompt body text. Header field values must be serialized so they cannot introduce whitespace-separated fields, line breaks, `=`, `[`, or `]` into the trusted header. RocketClaw may map runtime values to safe header text, but must not recover missing or unsafe provenance or instruction values from the prompt body. The body remains the source prompt body after the blank line, with the same attachment warning, text attachment, and fallback semantics that apply before framing. This framing does not change the shell-interpolation classification above: persistent bridge human and external input remains literal, and raw cron input retains raw-run expansion behavior.

### Message Flow

- Shared inbound messages are queued through the event bus and consumed by the main bridge.
- Terminal CLI observation of `main` must be non-consuming. A socket-attached `rocketclaw cli` process attached to `main` must display matching inbound and outbound events from Slack, Discord Text, Discord voice, browser voice, External MCP, cron, system, and terminal CLI sources without stealing messages from the main bridge or connector delivery sinks. SQLite session polling is not sufficient for this live observation contract because persisted session rows appear only after completed RocketCode turns.
- Automated inbound messages honor `minimum_wait_after_human_interaction` before processing.
- Text connector stacked or buffered messages must preserve prompt order and avoid duplicated deliveries.
- Pending `ask_user_question` free-text answers from authorized Slack or Discord Text users, or from the matching attached terminal CLI client for a terminal-originated question, must be consumed by the pending question and must not also route as normal RocketCode prompts, managed-thread replies, goal steering, cron requests, stop controls, summaries, or response-rooted replies.
- `rocketclaw_start_new_thread` tool-created first prompts are queued into the newly created conversation only and must not also route as input to the originating conversation, main conversation, goal steering, cron requests, stop controls, summaries, or response-rooted replies. The originating turn continues normally and the tool result reports the created conversation ID and openable surface information. The final response for the originating turn remains delivered to the originating conversation.
- Inbound attachment handling must share one semantic path across text connectors and external MCP after source-specific acquisition. Supported images are passed to RocketCode as attachments. Text attachments are appended to the user prompt as literal text within configured size limits. Unsupported, empty, oversized, or inaccessible attachments produce attachment warnings and existing fallback behavior without enabling prompt shell interpolation.
- Every normal text-visible assistant turn with a text connector target reserves or identifies a visible turn-start location before final response delivery. Progress is visible, interruption can target the active turn through the progress surface, final response delivery consumes or supersedes transient progress state, and post-reservation branches must not abandon pending progress state.
- Text-visible RocketCode tool progress diagnostics include the tool name for top-level tool calls before call details, so visible progress and logs distinguish real tool calls from ordinary question-shaped progress text. Text-visible RocketCode subagent progress diagnostics include a stable per-dispatch ordinal immediately after `subagent`, formatted as `(n/total)`, including `(1/1)` when a model response dispatches exactly one subagent task.
- Goal-loop kickoff turns, human re-steering turns, and automatic continuations are queued through the owning managed-conversation persistent bridge and must be delivered as visible assistant turns in the owning text conversation. Human replies already queued for that managed conversation must run before any subsequent automatic goal continuation.
- Slack binding: Slack stacked messages use Slack buffering semantics; normal Slack-visible assistant turns reserve a thinking placeholder (`_Thinking..._`) plus answer placeholder (`\u200B`), while Slack goal-loop turns reserve a goal-progress placeholder (`_Pursuing Goal..._` for infinite goals or `_Pursuing Goal (n/m)..._` for finite goals) plus answer placeholder (`\u200B`). Once reserved, the pair must be consumed through normal Slack final-response machinery. Slack thinking and goal-progress placeholder updates render accumulated RocketCode progress as a quote block with the newest progress line first while preserving chronological accumulation internally.
- Discord Text binding: Discord-visible assistant turns use a visible progress message as the progress surface. Discord goal-loop progress messages must visibly distinguish goal work with `_Pursuing Goal..._` for infinite goals or `_Pursuing Goal (n/m)..._` for finite goals while preserving the accumulated RocketCode progress text.
- Slack binding: Slack social-mode-gated channel thread replies that contain Slack-resolved direct pings to another user or bot, broadcast target, or user group must be skipped silently unless the same message also contains the RocketClaw bot mention. Slack channel references do not trigger this skip. Skipped replies must not create placeholders, reactions, connector replies, attachment processing, or thread-router submissions. Raw unresolved `@word` text and non-pinging Slack markup such as dates do not trigger this skip. Emergency safe words are checked before this skip.
- Discord Text binding: Discord guild-channel conversations may use guild threads; Discord DM conversations are managed text conversations without guild-thread semantics.
- Social-mode channel configuration uses canonical `agents`, an ordered list of allowed agent names. Legacy scalar `agent` is not supported by config loading or routing. The first configured agent is the default agent for newly started social-mode managed conversations. Empty or duplicate `agents` entries are normalized out before routing.
- Discord and browser voice transcriptions enter the same shared flow as other main-session input.
- External MCP conversations are isolated by external conversation ID; omitted ID starts a new isolated conversation. For a new external MCP conversation, the requested agent or configured default agent becomes the persisted conversation agent. For an existing external MCP conversation ID, the persisted agent is authoritative; if a request supplies a different agent, RocketClaw logs the mismatch and continues the turn with the persisted agent instead of rejecting the request or changing the persisted agent.
- External MCP prompt relays into the primary text connector must deliver MCP-provided relay attachments as part of the same visible native connector message as the relay text. If the relay text is blank and attachments exist, the relay text is `events.AttachmentNamesSpeech(attachments)` when names exist and `Attached files.` otherwise.
- External MCP blocking replies return the same outbound response attachments that the persistent bridge publishes through connector delivery. RocketCode-produced response attachments use one shared internal carrier before each edge adapts them to connector upload or MCP result content.
- When a target agent declares `guardrail: <agent-name>`, RocketCode filters both directions through the named guardrail agent: the outbound `task` delegation prompt before the child agent runs, and the inbound child final response before the task result is returned to the caller agent.
- Guardrail rejections do not run the rejected child prompt or expose the rejected child response; the guardrail reason is returned through the task result so the caller agent can continue from the rejection.

### Routing And Delivery

- Main conversation output targets are controlled by app wiring, not by individual input sources.
- Terminal CLI input attached to `main` publishes a normal literal human prompt into the shared main inbound flow. Socket-attached CLI clients send prompt requests to the server over the control socket; the server owns history loading, event observation, prompt routing, private CLI bridge creation, terminal-originated `ask_user_question` brokering, session persistence, and private-session summaries. Terminal CLI `--new` input routes only to its generated private conversation and does not consume shared main inbound messages. `rocketclaw cli --attach <conversation-id>` attaches terminal input, rendering, and terminal question-answer handling to the selected server-owned conversation.
- Terminal CLI slash commands are parsed locally before prompt submission. `/exit` closes the current CLI session. Unknown slash commands are local CLI errors and must not route to RocketCode as prompts. `/new [agent]` is cmux-only terminal-local control: outside cmux it reports a clear local error; inside cmux it asks the running RocketClaw server or transport to create a fresh private CLI conversation, creates a new cmux terminal surface in the caller workspace context rather than the focused cmux context, and sends `rocketclaw cli --attach <conversation-id>` to that new surface. `/new` does not fork or seed from the current conversation.
- A terminal-originated `rocketclaw_start_new_thread` tool call creates a fresh private CLI conversation owned by the server, inherits seed state from the originating RocketCode conversation, then submits the runtime-generated first prompt. It may request the same attached CLI client that owns the originating turn to open a cmux surface for the new conversation, using the same caller-context cmux behavior as `/new`. If no matching cmux-capable client is attached or cmux opening fails, the tool must still return the created conversation ID and `rocketclaw cli --attach <conversation-id>` command. Tool-created terminal conversations are not live-linked to the source conversation; later turns remain isolated. If `agent` is omitted for a terminal-originated tool call, the target agent defaults to `main`.
- Terminal CLI output renders in the invoking terminal without altering configured connector output targets. It must preserve ordinary terminal scrollback and text selection, must not use an alternate screen or full-screen repaint, and must keep the active input prompt at the bottom by clearing only the current prompt line, printing new matching events above it, and redrawing the prompt with the current input buffer.
- Closing a terminal CLI `--new` conversation through `/exit`, end-of-file, or normal terminal shutdown must ask whether to append a summary to `main` with a default negative answer. Accepting the prompt summarizes the private conversation through the existing persistent-bridge summary path and publishes the summary as an internalized note to `main`; declining leaves `main` untouched while preserving the private session.
- The primary text output target is configured as either Slack DM or Discord text, never both.
- Runtime app wiring injects the enabled primary text connector behind one connector-neutral API for managed conversations, main-session interruption markers, progress surfaces, reactions, summaries, response-rooted conversations, cron-created conversations, tool-created conversations, attachments, goal starts, goal stops, and completion markers. Shared bridges and bridge managers must depend on this common API rather than owning parallel Slack and Discord branches for the same operation. Slack and Discord transport differences belong in their connector binding implementations.
- Managed text conversations and response-rooted text conversations remain isolated from main until summarized. When those conversations start with inherited main-session context, RocketClaw seeds them from the latest available compaction boundary when one exists; if no compaction boundary exists, RocketClaw may compact the selected main-session history first. This seeding flow uses first-party OpenAI Responses compaction and RocketCode Responses replay semantics.
- A primary-text-originated `rocketclaw_start_new_thread` tool call creates a new managed text conversation on the originating connector surface only, inherits seed state from the originating RocketCode conversation, then submits the runtime-generated first prompt. It must not select an arbitrary configured target or fall back to another connector channel. Slack tool-created conversations post a new bot-authored top-level root message in the originating Slack channel or DM with no parent thread timestamp, including when invoked from an existing Slack thread, manage the Slack thread rooted at that message, and return the managed conversation ID plus Slack permalink when available. Discord guild text tool-created conversations post a new bot-authored root message in the originating guild text channel, or in the parent guild text channel when invoked from an existing guild thread, create a sibling guild thread from that root message, manage that guild thread, and return the managed conversation ID plus Discord thread URL when available. Discord thread names use the tool `title`, subject to existing connector truncation behavior. Discord DM tool-created conversations fail clearly before creating or persisting a new conversation because DMs have no guild-thread mechanics and no fallback guild target is implied. If `agent` is omitted, text calls from an existing managed conversation default to that conversation's persisted agent; other text calls default to the current turn's active agent. Social-mode calls must reject target agents outside the originating channel's configured `agents` list.

### Tool-Created Conversation Prompting

- A `rocketclaw_start_new_thread` call always inherits context from the originating RocketCode conversation as conversation seed state before the first task prompt is submitted. Inherited context is always enabled in v1 and has no tool option. The seed is persisted before the first prompt. If the source conversation has no seedable state, RocketClaw creates no seed entry and still submits the first prompt. The inherited context is not pasted into the visible Slack or Discord root message.
- The tool input fields are `title`, required; `prompt`, required; and `agent`, optional. There is no `target` field. The target surface is inferred from the originating turn.
- The first model-visible prompt for a tool-created conversation is a runtime-generated persistent-bridge prompt with `Origin=System`, `Media=Text`, and no `principal`. It uses normal RocketClaw prompt framing, including trusted `additional_instructions` for normal persistent-bridge reply turns, and its body is exactly:

```text
A RocketClaw agent started this new thread from an existing conversation.

Title: <title>
Started from: <surface summary>
Source conversation ID: <source conversation id>
Requesting agent: <current agent>
Target agent: <target agent>

Task:
<prompt>
```

- `<prompt>` is the literal model/tool-provided task text. It does not receive shell interpolation and must not be treated as trusted workspace prompt source.
- Slack and Discord tool-created root messages are concise human-visible setup messages, not inherited context carriers. Their visible text is exactly:

```text
New thread: <title>

Started by RocketClaw from this conversation.

Task:
<prompt>
```

- Text connector `🔁`/`🏁` goal-loop starts from DMs, social-mode mentions, or authorized messages inside existing managed conversations create or reuse normal managed connector conversations, persist goal state by managed-conversation ID, and use ADR 0007 trigger grammar, agent selection, turn-budget, duplicate active-goal rejection, sequential goal reuse, and terminal-status semantics. Each connector applies the same connector-local emoji-prefix normalization to goal starts that it applies to other emoji-prefixed starts before entering the shared goal parser; for Slack this includes `:repeat:` and `:checkered_flag:` transport text for those trigger emojis.
- Social-mode app/bot mentions, managed conversation replies, goal-loop starts/stops, summary reactions, direct channel cron requests, and channel cron rerun reactions must authorize users with the configured connector channel's per-channel allowed-user list.
- Authorized social-mode managed conversation replies may switch that conversation's persisted agent by sending exactly `🎛 agent-name` as the whole message, where `agent-name` is one of the configured `agents` entries for the social-mode channel that owns the conversation, or may cycle the persisted agent to the next configured `agents` entry by sending exactly bare `🎛` as the whole message. Bare `🎛` cycling follows configured `agents` order and wraps from the last entry to the first. If the persisted current agent is not in the configured `agents` list, bare `🎛` selects the first configured agent. Agent-switch and agent-cycle control messages must not create placeholders, reactions, prompt context, attachment processing, or thread-router submissions as model input. Invalid agent names must be rejected without changing the persisted conversation agent. Slack rejects or acknowledges with private ephemeral feedback. Discord Text rejects or acknowledges with a normal visible channel reply.
- Main-session stop controls for the enabled primary text connector must interrupt the active main turn when the configured human sends `🛑` or `⏹️` as an exact top-level DM message, or adds a connector stop reaction to the main-session turn surface. Slack stop reactions are `:octagonal_sign:` and `:stop_button:`. Discord Text stop reactions are `🛑` and `⏹️`. Main-session stop controls must clear queued main bridge work and connector-buffered main work, send no stop text, and add only a persistent `❗` reaction to the original turn-start message for the interrupted active main turn.
- Managed-conversation stop controls must interrupt the active turn when an authorized human sends `🛑` or `⏹️` as a message in the managed conversation, or adds either emoji as a reaction to the managed conversation root, any message in the managed conversation, or the active progress surface. If the interrupted conversation has an active goal, RocketClaw must mark that goal `stopped`. Stop controls must clear queued bridge work and connector-buffered work for the interrupted managed conversation, send no stop text, and add only a persistent `❗` reaction to the original turn-start message for the interrupted active turn.
- Goal loops that reach `complete` must add a `✅` reaction to the goal conversation root and to the last connector-visible message in the goal conversation when that message can be identified.
- Text connector responses are delivered to the originating connector conversation when a reply target exists; otherwise they are delivered to the configured primary text target.
- Slack binding: Slack managed conversations use Slack threads; Slack thread replies use persisted checkpoints when available, and older responses without checkpoints receive an explanatory thread reply instead of silently losing context.
- Slack binding: Slack External MCP relays with attachments post the relay text with `chat.postMessage`, privately upload the relay files, and then update that same message with `chat.update file_ids`; the original message timestamp remains the reply target, thread root or thread reply message, reaction target, and progress-placeholder parent.
- Discord Text binding: Discord guild-channel managed conversations use guild threads, response-rooted Discord text replies to checkpointed assistant messages can start response-rooted guild threads with inherited context, and Discord DMs do not provide guild-thread semantics.
- Discord Text binding: Discord External MCP relays with attachments use one multipart `Create Message` request containing `payload_json.content` and `files[n]`; top-level relay threads are created from that multipart root message, and existing-thread relays send the multipart message directly in the existing thread.
- Every completed scheduled cron publishes one silent run summary to `main`. The `rocketclaw_i_want_human_partner_to_see_this` argument is separate extra output for the human: an empty string sends no connector output, and a non-empty string is submitted to the cron text delivery sink. With an enabled primary text connector, non-empty extra output starts or uses a managed conversation through that connector: if `channel` is present, that value is the target; otherwise the target is the primary text connector's configured default Slack DM room or Discord guild channel. Non-empty extra cron output must not be sent as main-session verbatim output. With no enabled primary text connector, the inert delivery sink accepts the output and the silent main summary still runs. Replies and summaries for cron-created connector conversations follow existing connector gates.
- Text connectors maintain parity for repeat-reaction one-off cron reruns. Connectors accept deterministic top-level targets such as `:repeat_one: daily`, `🔂 daily`, whole-message `daily` or `daily.md`, and scheduled cron thread roots containing `cron/daily.md`; `cron/foo.md` is normalized to `foo` before `LoadOneOffCronjob`, which remains the final validation gate.
- Text connector DM direct cron requests by the configured human may run any top-level cron. Text connector channel cron requests and repeat reactions require social-mode authorization and may only run cronjobs whose configured `channel` targets the acted-on connector channel. Invalid, ambiguous, unauthorized, or wrong-channel requests must not run; otherwise-authorized invalid requests receive a helpful connector-thread reply.
- Successful text connector one-off cron runs must register the result conversation as a managed cron conversation after final result delivery succeeds, using the loaded cron agent and cron output as seed context. Follow-up replies in that result conversation must route to the cron agent through the managed-conversation bridge. Failed, invalid, unauthorized, or wrong-channel one-off cron requests must not register managed conversations.
- Raw cron runs must call `rocketclaw_i_want_human_partner_to_see_this`; normal assistant replies do not complete the background run.
- MCP result rendering must preserve the text reply as the first content item and expose the agent used for the turn plus response attachments through protocol-native content and structured data without changing connector delivery behavior.
- External MCP relay attachment same-message delivery does not merge progress placeholders, normal assistant response attachments, or cron-created channel-conversation attachments into the relay message; those surfaces keep their existing delivery contracts.

### Restart And Draining

- `rocketclaw_restart` is for explicit runtime configuration changes such as `rocketclaw.json`, `agents/`, `skills/`, or `cron/` changes.
- Restart and signal-triggered shutdown must stop cron from starting new jobs, wait for already-started cron jobs to finish, wait for inbound handoff and main/thread bridge idleness, stop inbound and bridges, wait for outbound drain, stop connectors, and preserve pending restart notifications. This sequence has no timeout.
- Restart and signal-triggered shutdown must delete unanswered pending `ask_user_question` connector messages before stopping the primary text connector and cancel unanswered terminal questions before shutdown completes. Delete failures are warnings only and must not block shutdown. Already answered question messages are left in place. Pending questions are in-memory only, have no timeout, and are not recovered after restart.
- Restart recovery must rehydrate active persisted text goal loops from persisted managed-conversation state and output targets by starting their managed conversation bridges and queuing one continuation per active goal, without replaying missed turns.
- Restart must not be triggered for ordinary memory, ledger, audit, report, source-code, generated artifact, log, transcript, or data-file edits.

### Permissions And Tools

- Task permission defaults must not become permissive by accident.
- Agent `maxRecursion` budgets are stricter than `task` permission grants; a permitted task target remains unavailable once the active inference's recursion budget is exhausted.
- Agent-system safety linting and graph inspection for permissions, delegation graphs, suppressions, and write-to-execute risk are governed by ADR 0006.
- Cron agents may selectively deny tools.
- A per-agent guardrail agent may use tools only when its own `permission` frontmatter allows those tools.
- RocketClaw tools are part of runtime behavior and must remain visible to RocketCode according to the bridge mode that owns the turn.
- The `ask_user_question` tool is a persistent-bridge RocketClaw tool visible only during human-initiated turns that have a native answer path: Slack requires `SourceSlack`, `Human == true`, and `SlackReply != nil`; Discord Text requires `SourceDiscordText`, `Human == true`, and `DiscordReply != nil`; terminal CLI requires `SourceTerminalCLI`, `Human == true`, and a matching attached CLI client for the conversation. The tool is available in main-session text turns, managed text conversation turns, and terminal-originated CLI conversation turns, including human goal kickoff and human re-steering turns when the originating turn otherwise qualifies. The tool is not visible for cron/raw runs, External MCP turns, Discord voice, browser voice, scheduled-message turns, system/automation turns, automatic goal continuations, or restart recovery continuations.
- `ask_user_question` asks the authorized human in the originating Slack, Discord Text, or terminal CLI conversation using native UI for that surface. Custom/free-text is always available. Slack custom/free-text answers must render as an in-message custom-answer button alongside any choice controls or as the sole action when there are no choices; pressing that button opens a Slack Block Kit text input surface for the answer, and Slack custom/free-text answers must not depend on a normal conversation reply. Slack choice answers use native buttons or selects. Discord Text choice answers use native buttons or selects, and Discord Text custom/free-text answers consume the next authorized reply in the same connector conversation. Terminal CLI choice answers use native terminal rendering for options and multiple-selection mode, and terminal custom/free-text answers are returned over the control socket by the matching attached CLI client. Calls may omit native choice options because custom/free-text is always an answer path. Unauthorized users and non-matching CLI clients must not resolve pending questions.
- `ask_user_question` blocks the calling RocketCode tool call until the pending question is answered or the owning turn is canceled. Its result reports selected option values, optional custom text, and the source connector or terminal CLI source. If the owning active turn is interrupted before answer, RocketClaw cancels the pending question and deletes its unanswered connector message when one exists. When a pending Slack or Discord Text question is answered, RocketClaw deletes the answered connector question message so follow-up response messages retain their natural order. If a terminal-originated question has no matching attached CLI client, or the matching CLI client disconnects before answering, the tool call fails clearly rather than routing the question to another connector.
- The text goal-loop update tool is a persistent-bridge tool visible only for conversations with an active text connector goal, and it may report `progress`, `complete`, or `blocked` with an optional explanatory `note`. `progress` records the note while keeping the persisted goal active. `complete` and `blocked` are terminal tool statuses. The `note` field is the structured mirror of the visible goal `Progress summary:` and explains status notes, what is going on, what changed, what the agent is thinking, where the goal is heading next, what was completed, or what is blocking progress. Human stop emoji behavior may interrupt the active turn and set the goal to `stopped` without using the tool.
- The `rocketclaw_start_new_thread` tool is a persistent-bridge RocketClaw tool visible only during human-initiated turns when the active agent has an explicit per-agent `allow` rule for RocketClaw permission subject `rocketclaw_start_new_thread` and the current turn has a native originating surface: Slack requires `SourceSlack`, `Human == true`, and `SlackReply != nil`; Discord Text requires `SourceDiscordText`, `Human == true`, and `DiscordReply != nil`; terminal CLI requires `SourceTerminalCLI`, `Human == true`, and a server-owned conversation. Missing permission, `auto`, or `deny` keeps the tool hidden and unavailable. The tool is never visible or callable for any MCP-originated turn or cron-originated turn, including External MCP turns, cron/raw runs, scheduled cron turns, one-off cron turns, and cron-created conversation continuations. The tool is also not visible for Discord voice, browser voice, scheduled-message turns, system/automation turns, automatic goal continuations, or restart recovery continuations. Discord DM calls fail clearly before persistence because Discord DMs cannot host guild-thread mechanics and the tool does not infer a guild fallback target. The tool accepts title, prompt, and optional agent; it must reject unsupported target agents without creating a conversation. The target agent must be a loaded agent and must obey existing surface-specific agent constraints, including the originating social-mode channel's configured `agents` list when the originating surface is social mode. The tool prompt is literal model/tool-provided first task text, receives normal RocketClaw prompt provenance framing only as part of the runtime-generated first prompt for the newly created conversation, and does not enable shell interpolation.

## Non-Goals

- This ADR does not specify exact implementation structure.
- This ADR does not require tests for deleted internals; tests should cover current observable contracts.
- This ADR does not make external human input executable.

## Evidence

- `internal/rocketclaw/harnessbridge/bridge.go`
- `internal/rocketclaw/harnessbridge/raw_run.go`
- `internal/rocketcode/prompts.go`
- `internal/rocketcode/looper.go`
- `internal/cronjob/manager.go`
- `internal/slackconnector/connector.go`
- `internal/app/thread_bridges.go`
- `internal/events/bus.go`
- `internal/rocketclaw/harnessbridge/bridge_test.go`
- `internal/rocketclaw/harnessbridge/raw_run_test.go`

## Consequences

- Behavior-preserving refactors must verify queue order, prompt framing, delivery/silence, and routing separately.
- Code deletion is acceptable only when the surviving code still satisfies these contracts.
- If a bug reveals a behavior worth preserving, the human partner can promote it into this ADR before or during the fix.

## Changelog

- 2026-05-25: Initial accepted snapshot.
- 2026-05-25: Added Slack reply placeholder-pair reservation contract for normal Slack-visible assistant turns.
- 2026-06-02: Specified managed continuation and summary behavior for cron `slack-channel` threads.
- 2026-06-02: Added Discord text routing and managed-thread behavior as the mutually exclusive primary text alternative to Slack.
- 2026-06-02: Renamed cron managed Slack thread routing to canonical `channel`, with `slack-channel` retained as a backward-compatible alias.
- 2026-06-02: Added Slack and Discord Text parity and channel-target authorization for repeat-reaction one-off cron reruns.
- 2026-06-04: Added silent Slack social-mode channel-thread reply suppression for messages pinging others unless the RocketClaw bot is also mentioned.
- 2026-06-07: Specified that restart and signal-triggered shutdown share the same graceful drain sequence and configured timeout.
- 2026-06-08: Specified that inherited main-session thread seeding reuses the latest available compaction point before compacting selected history, while preserving full-history fallback when no compaction exists.
- 2026-06-08: Specified Slack-visible subagent progress diagnostic ordinals for identifying concurrent duplicate subagent task calls.
- 2026-06-09: Excluded Slack channel references from silent Slack social-mode channel-thread reply suppression.
- 2026-06-09: Specified newest-first rendering for Slack thinking quote-block progress updates.
- 2026-06-09: Removed the graceful shutdown timeout and specified the no-timeout shutdown order.
- 2026-06-10: Specified that `maxRecursion` subdelegation budgets override otherwise-permitted `task` grants when exhausted.
- 2026-06-10: Added local-only guardrail filtering for RocketCode task delegation prompts and child final responses.
- 2026-06-11: Linked agent-system safety linting to ADR 0006.
- 2026-06-11: Linked agent-system graph inspection to ADR 0006.
- 2026-06-11: Added Slack goal-loop routing, continuation ordering, restart recovery, and goal-update tool contracts governed by ADR 0007.
- 2026-06-11: Added visible Slack-thread delivery, stop emoji controls, and completion checkmark reactions for Slack goal loops.
- 2026-06-11: Added `🏁` as an additional Slack goal-loop trigger alongside `🔁`.
- 2026-06-11: Added channel-aware Slack social-mode authorization for social actions and cron rerun reactions.
- 2026-06-11: Removed runtime fallback to top-level Slack social-mode allowed users; social-mode authorization is channel-only after startup migration.
- 2026-06-12: Specified universal post-reservation Slack placeholder consumption through the normal final-response path.
- 2026-06-12: Specified shared Slack and external MCP inbound attachment semantics, including literal text attachment prompt conversion.
- 2026-06-12: Specified shared outbound response attachment semantics for connector delivery and external MCP blocking replies.
- 2026-06-12: Replaced local-only global guardrail filtering with per-target-agent `guardrail` frontmatter.
- 2026-06-14: Updated Slack goal-loop contracts for conversation-local goals, existing-thread starts, budget-neutral human re-steering, removal of `paused`, and stop controls that interrupt active turns with `❗` marker-only feedback.
- 2026-06-14: Added Slack goal-loop duplicate active-goal rejection and sequential goal reuse after terminal states.
- 2026-06-14: Recast Slack and Discord Text behavior as one text connector contract with connector-specific bindings for progress, threading, social routing, goal loops, cron, reactions, and attachments.
- 2026-06-14: Required shared runtime bridges to use one injected connector-neutral primary text API instead of parallel Slack and Discord operation branches.
- 2026-06-14: Added main-session stop controls for the enabled primary text connector with marker-only `❗` feedback.
- 2026-06-14: Specified that primary text connector goal-loop progress surfaces visibly distinguish goal work with `_Pursuing goal..._`, including Slack placeholders and Discord progress messages.
- 2026-06-14: Specified that each text connector applies connector-local emoji-prefix normalization to goal starts before the shared goal parser, including Slack's `:repeat:` and `:checkered_flag:` transport text, while preserving Slack/Discord Text symmetry.
- 2026-06-15: Changed text goal-loop defaults and progress contracts to `maxTurns: 5`, finite `_Pursuing Goal (n/m)..._` progress markers, visible `Progress summary:` sections mirrored into `rocketclaw_update_goal.note`, non-terminal `progress` tool updates, and developer-context injection of latest active goal notes.
- 2026-06-16: Specified same-message External MCP relay attachment delivery for Slack and Discord Text while keeping progress, assistant response, and cron attachment delivery separate.
- 2026-06-16: Added canonical social-mode `agents` list configuration, legacy scalar `agent` migration, first-agent defaults for new conversations, and authorized `🎛 agent-name` persisted conversation-agent switching with connector-specific feedback.
- 2026-06-16: Removed legacy scalar social-mode `agent` config migration; canonical `agents` lists are required in loaded config.
- 2026-06-16: Added `ask_user_question` visibility, authorization, answer-consumption, cancellation, shutdown deletion, and non-recovery contracts for human Slack and Discord Text turns.
- 2026-06-17: Specified successful text connector one-off cron result conversations as managed cron conversations for follow-up replies.
- 2026-06-17: Required top-level tool progress diagnostics to include tool names and required `ask_user_question` calls to provide at least one answer path.
- 2026-06-18: Required Slack `ask_user_question` custom/free-text answers to use an in-message custom-answer button that opens a Slack Block Kit text input surface, instead of consuming normal conversation replies.
- 2026-06-18: Removed `allow_custom` from the `ask_user_question` contract and made custom/free-text an always-available answer path, with Slack always rendering an in-message custom-answer button.
- 2026-06-18: Changed answered `ask_user_question` message lifecycle from in-place answered markers to deletion of the answered connector question message, preserving follow-up message order.
- 2026-06-18: Added terminal CLI live observation, main/private routing, terminal rendering, and private-session summary contracts.
- 2026-06-19: Specified socket-attached terminal CLI routing, server-owned runtime behavior, and direct `--attach` conversation selection.
- 2026-06-19: Expanded `ask_user_question` visibility and answer handling to qualifying terminal-originated CLI turns through the matching attached CLI client.
- 2026-06-19: Added terminal CLI local slash-command handling and cmux `/new [agent]` caller-context surface creation semantics.
- 2026-06-23: Required every RocketClaw-originated RocketCode prompt input to include a trusted runtime-generated origin media header, with human principals included only for human-originated prompts, while preserving prompt body semantics and existing shell-interpolation classifications.
- 2026-06-23: Required product, operational, and internal provider parity for managed and response-rooted thread seeding across OpenAI, OpenAI-compatible, and Anthropic selections.
- 2026-06-23: Clarified provider/mode projection for response-thread and pre-seeded managed-conversation inherited context.
- 2026-06-25: Added bare `🎛` social-mode managed-conversation agent cycling in configured `agents` order, with wraparound, first-agent fallback when the persisted current agent is no longer configured, and the same authorization, feedback, and no-model-input control-message semantics as named agent switching.
- 2026-06-25: Added trusted `additional_instructions` prompt header field, exact default normal-reply and internal-note instructions, optional normal-reply override from agent `additionalInstructions` frontmatter, and raw-run cron omission of the field.
- 2026-06-25: Specified default-deny `rocketclaw_start_new_thread` visibility, explicit `allow` requirement, literal first prompt handling, originating-surface routing, Slack/Discord/terminal creation behavior, Discord DM clear failure, and originating-turn delivery isolation.
- 2026-06-25: Specified `rocketclaw_start_new_thread` inherited source-context seeding, first model-visible prompt body, human-visible root message body, optional agent defaults, and prompt-as-task rather than prompt-as-seed semantics.
- 2026-06-25: Clarified that `rocketclaw_start_new_thread` is never visible or callable from MCP-originated or cron-originated turns.
- 2026-06-26: Removed Anthropic and OpenAI-compatible chat-completions provider parity requirements; RocketClaw supports only first-party OpenAI Responses and configured OpenAI-compatible Responses providers for RocketCode model turns.
- 2026-06-26: Changed scheduled cron visible output without `channel` to create a managed primary-text conversation in the default Slack DM room or Discord guild channel when a primary text connector is enabled, while preserving main-session fallback when no primary text connector is enabled.
- 2026-06-26: Removed OpenAI-compatible Responses parity from managed and response-rooted conversation seeding; inherited context seeding now uses first-party OpenAI Responses compaction and RocketCode Responses replay semantics only.
- 2026-06-26: Clarified that no-`channel` scheduled cron results continue through main-session internalization after the cron text delivery sink accepts visible output.
- 2026-06-29: Removed the legacy cron `slack-channel` frontmatter alias; cron text routing uses `channel` only.
- 2026-06-30: Clarified scheduled cron wording by separating the always-silent main summary from extra human-visible output.
