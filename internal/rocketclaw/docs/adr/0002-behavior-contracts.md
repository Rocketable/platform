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
| Text connector/MCP human input in the persistent bridge | No                   | External/human input must remain literal.                                |
| Scheduled-message prompt text in the persistent bridge         | No                   | It follows persistent bridge input rules unless explicitly reclassified. |
| `rocketclaw_start_new_thread` first task prompt text in the persistent bridge | No                   | Model/tool-supplied prompt text must remain literal.                     |
| `AGENTS.md` workspace instructions                             | No                   | Root instructions are loaded literally.                                  |

Expansion uses RocketCode semantics: pattern ``!`command` ``, workspace-root cwd, stdout insertion only, and command failures do not fail prompt preparation.

Every RocketClaw-originated `rocketcode.PromptInput` must be framed with one trusted runtime-generated prompt header before the prompt body:

```text
[Origin media=Media principal=Name additional_instructions="Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."]

<body>
```

For prompts without a human principal, the `principal=Name` field is omitted. For example, raw-run cron prompts also omit `additional_instructions`, so their header is:

```text
[Cron media=Text]

<body>
```

The first bracket token is `Origin`. It names the RocketClaw runtime source category responsible for the prompt, not a connector-provided display string. Accepted origin values are product enum values chosen by RocketClaw code from runtime context: `Slack`, `Cron`, `ExternalMCP`, and `System`. New origin values require product-contract review because model-visible provenance changes.

`Media` names the interaction medium inside that origin. The accepted media value is the product enum value `Text`, chosen by RocketClaw code from runtime context. `Text` covers typed connector messages, External MCP prompts, scheduled/system text, and cron prompt files. New media values require product-contract review.

`Name` is the principal: the RocketClaw-known human actor responsible for a human-originated prompt. It should be the best runtime-authenticated or connector-derived human name available for that turn, such as a configured human profile display name, connector account name, Basic Auth username, or, when no safer human name is available, that connector's stable user identifier. Principal is model-visible provenance only; authorization and permissions must continue to use RocketClaw runtime metadata and connector checks, not the prompt header text. Non-human prompts, system prompts, automated continuations, scheduled-message prompts, and cron/raw-run prompts omit `principal` entirely.

`additional_instructions` is trusted turn guidance for how RocketCode should handle the prompt body. It is not provenance. It must not be derived from external prompt body text. When present, it is serialized as a quoted string value in the header. Persistent bridge prompt text must not include separate model-visible instruction paragraphs or `User message:` labels; the instruction belongs in the header and the prompt body begins after the blank line.

Normal persistent-bridge reply turns use this default `additional_instructions` text:

```text
Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.
```

A selected agent may override that normal-reply default by declaring a non-empty string `additionalInstructions` field in trusted agent frontmatter. Missing, empty, or non-string `additionalInstructions` values do not override the default. This override applies only to normal persistent-bridge reply turns.

Raw-run cron prompts use `[Cron media=Text]` and omit `additional_instructions`.

Field order is fixed: first `Origin`, then `media=Media`, then `principal=Name` when present, then `additional_instructions="..."` when present. The header is generated from RocketClaw runtime metadata and trusted runtime-selected instruction text, never from prompt body text. Header field values must be serialized so they cannot introduce whitespace-separated fields, line breaks, `=`, `[`, or `]` into the trusted header. RocketClaw may map runtime values to safe header text, but must not recover missing or unsafe provenance or instruction values from the prompt body. The body remains the source prompt body after the blank line, with the same attachment warning, text attachment, and fallback semantics that apply before framing. This framing does not change the shell-interpolation classification above: persistent bridge human and external input remains literal, and raw cron input retains raw-run expansion behavior.

### Message Flow

- Each persistent conversation processes its work in arrival order. Independent conversations may run at the same time. A private MCP session and its managed Slack session have separate queues, but only one of them may run at a time. The waiting side starts after the active side saves its turn and finishes any required copy into managed history.
- Text connector stacked or buffered messages must preserve prompt order and avoid duplicated deliveries.
- Pending `ask_user_question` free-text answers from authorized Slack users must be consumed by the pending question and must not also route as normal RocketCode prompts, managed-thread replies, goal steering, cron requests, or stop controls.
- `rocketclaw_start_new_thread` tool-created first prompts are queued into the newly created conversation only and must not also route as input to the originating conversation, goal steering, cron requests, or stop controls. The originating turn continues normally and the tool result reports the created conversation ID and openable surface information. The final response for the originating turn remains delivered to the originating conversation.
- Inbound attachment handling must share one semantic path across text connectors and external MCP after source-specific acquisition. Supported images are passed to RocketCode as attachments. Text attachments are appended to the user prompt as literal text within configured size limits. Unsupported, empty, oversized, or inaccessible attachments produce attachment warnings and existing fallback behavior without enabling prompt shell interpolation.
- For a thread shared through Slack, the sender's message is the request.
- Put the shared material in a separate, clearly labeled section. It is reference material, not instructions. Text from the shared material cannot run shell commands.
- Show Slack's visible preview once. Use its main text when present. Otherwise use Slack's alternate summary. Do not combine duplicate versions of the same preview.
- Follow a share only when Slack explicitly says it is a shared thread and identifies both its source channel and the start of that thread. Ordinary links and pasted Slack links do not count.
- Slack may provide the source details in more than one place. Missing details may be filled from another place only when every supplied detail agrees. Visible labels, footer text, and the sender's current conversation cannot identify the source.
- Read the full thread only when RocketClaw's Slack app confirms that the source channel is public. Do not use other accounts, search for the thread, join a channel automatically, or read a private channel or direct message.
- Read the entire thread or none of it. If any part cannot be read, keep only the preview.
- Show each message once, from oldest to newest.
- If the source is invalid, conflicting, private, unavailable, or unreadable, keep the preview and do not show a partial thread.
- Limit the whole shared-material section to 256 KiB (about 262 KB), including headings and shortening notices.
- Put the preview first. If it is too long, shorten it and say so. Use the remaining space for the full thread. If the thread is too long, shorten it and say so.
- Files from the source thread follow the existing Slack file rules. RocketClaw's Slack app checks each file's type and size. Keep source order and avoid extra copies when Slack identifies the same file more than once.
- If one file cannot be read, show the existing warning for that file and keep the rest of the thread.
- Treat shared threads mentioned inside the source thread as text only. Do not follow them.
- Every normal text-visible assistant turn with a text connector target reserves or identifies a visible turn-start location before final response delivery. Progress is visible, interruption can target the active turn through the progress surface, final response delivery consumes or supersedes transient progress state, and post-reservation branches must not abandon pending progress state.
- Text-visible RocketCode tool progress diagnostics include the tool name for top-level tool calls before call details, so visible progress and logs distinguish real tool calls from ordinary question-shaped progress text. Text-visible RocketCode subagent progress diagnostics include a stable per-dispatch ordinal immediately after `subagent`, formatted as `(n/total)`, including `(1/1)` when a model response dispatches exactly one subagent task. Nested subagent, guardrail, and automatic permission-review progress diagnostics render as breadcrumb paths joined by ` → `. The breadcrumb path preserves delegation depth and child-run identity before the diagnostic payload, for example `subagent(1/1) → review → guardrail(delegation) → safety → reasoning: ...`, `subagent(1/1) → review → guardrail(response) → safety → result: approve: Looks safe.`, and `subagent(1/1) → review → auto-approver → guardian → result: allow: Low-risk action.`.
- Goal-loop kickoff turns, human re-steering turns, and automatic continuations are queued through the owning managed-conversation persistent bridge and must be delivered as visible assistant turns in the owning text conversation. Human replies already queued for that managed conversation must run before any subsequent automatic goal continuation.
- Slack binding: Slack stacked messages use Slack buffering semantics. Every normal Slack-visible assistant turn reserves a thinking message plus a separate answer placeholder (`\u200B`), while every Slack goal-loop turn reserves a goal-progress message plus a separate answer placeholder (`\u200B`). Thinking and goal-progress updates render one `task_card` Block without a separate label section. The card uses the turn ID as `task_id`, `_Thinking..._` or the goal-progress phrase as its title, and `in_progress` status while the turn runs. Every activity appears from oldest to newest in `details`, with one rich-text section per activity. Slack link markup in an activity uses rich-text link elements so links render; other activity text remains literal. On successful turn completion, RocketClaw keeps the thinking message, sets the card title to `Complete`, and changes the card status to `complete`. The answer remains a separate message. Thinking and answer output may arrive internally out of order without changing their reserved Slack message order. The fallback message text keeps its existing ordering. No output, sources, feedback, or action controls are added.
- Pre-release Slack thinking-card evaluation: commits on `main` may change only the transport and native layout used inside the single thinking message so the behavior can be tested before a manual release is cut. The answer placeholder, answer delivery implementation, thinking-before-answer message order, activity contents and order, routing, queueing, attachments, reactions, and completion behavior must remain unchanged. Variant A starts one thinking stream with `chat.startStream`, posts the existing answer placeholder second, and uses `task_display_mode=timeline`. One turn-status task remains `in_progress`. Each newly received thinking activity is appended once inside that same thinking message as a separate completed `task_update` entry; prior activities are not sent again. The exact activity text is the task entry's `title`. An activity longer than Slack's 256-character task-update limit is split into ordered continuation entries by counting Unicode code points. For each entry, RocketClaw chooses the latest available boundary at or before 256 code points in this order: immediately after a newline; immediately after `.`, `!`, or `?` followed by whitespace; immediately after any other whitespace; otherwise exactly at 256 code points. Boundary punctuation and whitespace remain in one of the entries, and joining every continuation title recreates the original activity exactly. Each activity and continuation entry has a stable ID containing its activity and continuation order. Live Variant A failed because Slack rendered every activity as a separate top-level task card. Variant B keeps the same bounded activity `task_update` entries but uses `task_display_mode=plan`, so Slack groups them as tasks inside one `plan` Block in the single thinking message. `plan_update` sets the Plan title to `_Thinking..._` or the goal-progress phrase while work runs, then changes it to `Complete` through `chat.stopStream` after the unchanged answer delivery finishes. Variant B does not add an overall turn-status task inside the Plan. A manual release must not be cut from an evaluation commit. A later ADR changelog entry must select the final behavior or restore the non-streaming task-card behavior before release.
- Slack binding: configured-channel thread replies that contain Slack-resolved direct pings to another user or bot, broadcast target, or user group must be skipped silently unless the same message also contains the RocketClaw bot mention. Slack channel references do not trigger this skip. Skipped replies must not create placeholders, reactions, connector replies, attachment processing, or thread-router submissions. Raw unresolved `@word` text and non-pinging Slack markup such as dates do not trigger this skip.
- Slack channel configuration uses canonical `agents`, an ordered list of allowed agent names. The first configured agent is the default agent for newly started managed conversations. Empty or duplicate `agents` entries are normalized out before routing.
- Every External MCP call must include an external conversation ID, an agent, and a configured Slack channel. RocketClaw does not create the public ID. An unknown ID creates one private MCP session, one managed Slack session, and one Slack thread. Later calls must use the same channel. A missing or changed channel is rejected. Different external IDs stay separate, even in the same channel.
- The first call selects the private MCP agent. That agent must be loaded, but it does not need to be in the Slack channel's `agents` list. It never changes for that external conversation. Every later call must still include an agent. If it names a different agent, RocketClaw logs the mismatch and uses the original agent.
- The managed session starts with the first agent in the channel's `agents` list. Authorized Slack users may switch the managed agent. This does not change the private MCP agent.
- MCP turns run only in the private session. Slack turns run only in the managed session. An MCP turn waits while a managed turn is running, and a managed turn waits while an MCP turn is running.
- RocketCode saves each completed turn as a `SessionEntry`. This saved entry contains the prompt, tool calls, tool results, assistant answer, and other information needed to replay the turn. After an MCP turn completes, RocketClaw copies its saved entry into managed history before either session may start more work. Slack-created entries are never copied into private MCP history.
- A provider compaction item is a shortened replacement for older model context. RocketClaw does not copy compaction or compaction-summary items into managed history because managed history already contains its own older context. If a saved turn contains both compaction data and normal turn data, RocketClaw copies the normal data.
- The first MCP turn's metadata is saved once as `mcp_external_metadata` in both sessions. It becomes the conversation's base metadata. Base metadata is exposed as normalized shell environment variables to both private MCP turns and managed Slack turns in the paired conversation. For example, the metadata key `phase` becomes `ROCKETCLAW_METADATA_PHASE`. Later calls cannot replace existing base keys. New keys on later calls apply only to that MCP turn and do not become shell environment variables for later managed Slack turns. The managed copy of that turn includes those new keys, so managed history still contains the metadata that the MCP agent saw.
- Slack Blocks must label MCP request messages and show the external conversation ID and private MCP agent. Each request message uses a dedicated header Block for `MCP request`, an identity context Block, and a divider before the request body. The request body uses Slack Markdown so links and formatting render. Notification-bearing Slack mention text from an MCP request remains literal and must not notify users, user groups, `@here`, `@channel`, or `@everyone`. This rule applies to the visible Blocks and fallback text. One Slack message may use up to Slack's 50-Block limit. If request text remains, RocketClaw posts continuation replies in the same thread before creating thinking and answer placeholders. The MCP agent still receives one complete, unchanged request. Request attachments stay on the thread's root message. Blank attachment-only requests use `events.AttachmentNamesSpeech(attachments)` when names exist and `Attached files.` otherwise. Fallback Slack text remains present for notifications and accessibility.
- Slack Blocks must also label MCP progress and final responses and show the external conversation ID and private MCP agent. Each progress or final-response message uses a dedicated header Block, an identity context Block, and a divider before the body. Agent-authored progress and response bodies use Slack Markdown. A file-only response keeps a labeled answer message that names the attached files, or says `Attached files.` when no names exist. Ordinary Slack turns keep their current appearance.
- External MCP blocking replies return the same outbound response attachments that the persistent bridge publishes through connector delivery. RocketCode-produced response attachments use one shared internal carrier before each edge adapts them to connector upload or MCP result content.
- An unfinished MCP turn is stored under the private MCP conversation ID. An unfinished Slack turn is stored under the managed Slack conversation ID. Startup recovery restores work in the session that owned it and still allows only one of the two sessions to run. Recovery cannot complete the old process's HTTP request. A later call for the same external ID waits behind recovery on either session and waits only for its own answer. Its Slack request message must not appear before the recovered turn finishes.
- When a target agent declares `guardrail: <agent-name>`, RocketCode gates both directions through the named guardrail agent: the outbound `task` delegation prompt before the child agent runs, and the inbound child final response before the task result is returned to the caller agent. The guardrail approves or rejects; it does not transform the delegated prompt or child response.
- Guardrail rejections do not run the rejected child prompt or expose the rejected child response; the guardrail reason is returned through the task result so the caller agent can continue from the rejection.

### Routing And Delivery

- Every Slack-visible response has an explicit managed-thread target supplied by the originating or runtime-created conversation.
- Runtime app wiring injects Slack behind one connector-neutral API for managed conversations, progress surfaces, reactions, cron-created conversations, tool-created conversations, attachments, goal starts, goal stops, and completion markers.
- Every new managed Slack conversation starts with empty history. A Slack human, tool, or schedule normally provides its first turn. For an MCP-created thread, the first saved managed entry is the copy of the first private MCP turn. Later managed turns use managed history, including copied MCP entries. They never write back into private MCP history.
- A Slack-originated `rocketclaw_start_new_thread` tool call creates a fresh managed conversation in the originating configured channel and submits the literal tool prompt as its first model-visible input. The call posts a new bot-authored top-level root in the originating configured channel and returns the managed conversation ID plus Slack permalink when available. If `agent` is omitted, the call defaults to the originating conversation's persisted agent. The target agent must belong to the originating channel's configured `agents` list.

### Tool-Created Conversation Prompting

- A `rocketclaw_start_new_thread` call persists the new conversation with empty history before submitting its first task prompt.
- The tool input fields are `title`, required; `prompt`, required; and `agent`, optional. There is no `target` field. The target surface is inferred from the originating turn.
- The first model-visible prompt for a tool-created conversation uses normal persistent-bridge framing with `Origin=System`, `Media=Text`, no `principal`, and normal trusted `additional_instructions`. Its body is exactly the literal `<prompt>` tool argument. The title and source surface remain Slack-visible setup information and are not copied into model context.

- `<prompt>` is the literal model/tool-provided task text. It does not receive shell interpolation and must not be treated as trusted workspace prompt source.
- Slack tool-created root messages contain concise human-visible setup information. Their visible text is exactly:

```text
New thread: <title>

Started by RocketClaw from this conversation.

Task:
<prompt>
```

- Slack `🔁`/`🏁` goal-loop starts from authorized configured-channel mentions or messages inside existing managed conversations create or reuse normal managed conversations, persist goal state by managed-conversation ID, and use ADR 0007 trigger grammar, agent selection, turn-budget, duplicate active-goal rejection, sequential goal reuse, and terminal-status semantics. Slack applies connector-local emoji-prefix normalization before entering the shared goal parser, including `:repeat:` and `:checkered_flag:` transport text.
- Slack text connector turns whose model-visible prompt body begins with `💡` after Slack emoji-alias normalization are translated into RocketCode direct skill invocation input for the RocketCode conversation that already owns the turn; the lightbulb is connector syntax and is not model-visible prompt text. The trigger grammar is `💡 <skill-name> [arguments]`, where `<skill-name>` is the first whitespace-delimited token after the lightbulb and `[arguments]` is the remaining text after the skill name. Slack accepts `:light_bulb:` and `:electric_light_bulb:` through the same leading emoji alias normalization used by other emoji-prefixed starts. RocketClaw must not render skill content itself, create a new conversation, change the selected agent, or bypass existing routing, authorization, attachment handling, prompt provenance framing, shell-interpolation classification, skill visibility, or skill permission rules. Missing skill names, unknown skills, or skills not visible to the active agent fail through RocketCode direct skill invocation before model request and must be surfaced clearly without treating the turn as an ordinary prompt or an internal runtime failure.
- App/bot mentions, managed conversation replies, goal-loop starts/stops, direct channel cron requests, and channel cron rerun reactions must authorize users with the configured channel's per-channel allowed-user list.
- Authorized managed conversation replies may switch that conversation's persisted agent by sending exactly `🎛 agent-name` as the whole message, where `agent-name` is one of the configured `agents` entries for the channel that owns the conversation. Authorized replies that send exactly bare `🎛` must receive a Slack-native agent selector populated from that channel's current `agents` entries. A selector choice may switch the persisted agent only when the interacting Slack user is the same authorized user who sent the bare control and the selected agent remains configured for the channel. Agent-switch and selector controls must not create placeholders, reactions, prompt context, attachment processing, or thread-router submissions as model input. Invalid choices must not change the persisted agent. Slack rejects invalid or failed switches with private ephemeral feedback and acknowledges successful switches with a normal visible thread reply.
- Managed-conversation stop controls must interrupt the active managed turn when an authorized human sends `🛑` or `⏹️` as a message in the managed conversation, or adds either emoji as a reaction to the managed conversation root, any message in the managed conversation, or the active progress surface. They also cancel managed work that is queued or waiting for a paired private MCP turn. They never interrupt private MCP work. If the interrupted managed conversation has an active goal, RocketClaw must mark that goal `stopped`. Stop controls must clear queued bridge work and connector-buffered work for the interrupted managed conversation and send no stop text. RocketClaw must add a persistent `❗` reaction to the original turn-start message for the interrupted active managed turn. For every connector-buffered message discarded by the stop, RocketClaw must remove its `⌛` reaction and add a persistent `❗` reaction to that discarded message.
- Goal loops that reach `complete` must add a `✅` reaction to the goal conversation root and to the last connector-visible message in the goal conversation when that message can be identified.
- Slack responses are delivered to the explicit managed-thread target owned by the conversation. A missing reply target is an error.
- Slack binding: Slack routes replies in persisted managed-conversation threads to their existing conversations.
- Slack binding: External MCP requests use `chat.postMessage` with fallback text and MCP request Blocks. For attachments, RocketClaw privately uploads the files and adds them to that same message with `chat.update file_ids`. The message keeps the same timestamp and remains the thread root or reply target.
- Every active non-example cron definition requires a non-empty `channel` naming a configured Slack channel. The `rocketclaw_i_want_human_partner_to_see_this` argument is the complete human-visible output decision: an empty value completes silently. Non-empty scheduled output starts a fresh managed thread in the required channel; successful human-requested one-off output may use and register its originating result thread. A registered cron thread's RocketCode history begins with its first later authorized human reply.
- Slack binding: repeat-reaction one-off cron reruns accept deterministic top-level targets such as `:repeat_one: daily`, `🔂 daily`, whole-message `daily` or `daily.md`, and scheduled cron thread roots containing `cron/daily.md`; `cron/foo.md` is normalized to `foo` before `LoadOneOffCronjob`, which remains the final validation gate.
- Slack channel cron requests and repeat reactions require configured-channel authorization and may run only cronjobs whose required `channel` targets the acted-on channel. Invalid, ambiguous, unauthorized, or wrong-channel requests must not run; otherwise-authorized invalid requests receive a helpful thread reply.
- Successful Slack one-off cron runs may register the result thread as a managed cron conversation after final result delivery succeeds, using the loaded cron agent. Its RocketCode history begins with the first later authorized human reply. Registration occurs only for valid, authorized, matching-channel requests whose final result delivery succeeds.
- Raw cron runs must call `rocketclaw_i_want_human_partner_to_see_this`; normal assistant replies do not complete the background run.
- Scheduled cron execution uses one logical execution state per cron file. Multiple schedules declared by the same cron file share that file's execution state. Runs for the same cron file start serially and must not overlap within the active scheduled-cron generation, while one-off cron runs bypass scheduled serialization and may overlap scheduled or one-off runs.
- Scheduled cron uses coalesced no-backlog semantics. If a schedule is due while the same cron file is already running, RocketClaw must not start an overlapping scheduled run and must not accumulate one run per missed trigger. After the running job completes, the next ticker evaluation may start the cron again only if the current definition and schedule are due at that time.
- Scheduled cron does not catch up missed wall-clock intervals after daemon downtime, restart, or scheduled definition changes. Persisted cron state is for preventing overlap and preserving scheduling/execution state, not for replaying downtime, pre-observation intervals, or missed-trigger backlogs; no per-trigger pending backlog such as a `pending_count` column is required.
- Scheduled cron uses one global ticker loop for scheduled cron decisions. On each tick, the daemon scans the effective `<runtime-dir>/cron/` definitions, observes scheduled cron definition content changes, determines which scheduled definitions are due, and starts due jobs subject to the per-file serialized execution state above. This is a ticker-scan architecture contract, not a requirement to use filesystem watchers. The daemon must not reread `rocketclaw.json` or `femtoclaw.json`, fetch changed overlay lists, interrupt active RocketCode turns, or interrupt already-running cron jobs to observe cron file content changes. Already-running scheduled cron jobs continue with the definition snapshot they started with. Future scheduled cron starts use the latest observed definition and schedule state.
- `rocketclaw_reload` must validate staged effective runtime assets before committing them. Staging uses embedded assets, fetches or re-clones fresh remote content for overlay entries already loaded from the current runtime configuration, and reapplies local workspace overlays from disk. If staged RocketCode agent/skill definitions or staged cron definitions are invalid, the tool result must report reload failure to the model and live runtime assets must remain unchanged. Successful reload commits the staged runtime assets, after which scheduled cron observes committed cron content through the ticker-scan contract above. Reload must not reread runtime configuration, observe added/removed/reordered/changed overlay config entries, replace cron scheduler machinery, mutate pending restart notifications, interrupt active RocketCode turns, or interrupt already-running cron jobs.
- MCP result rendering must preserve the text reply as the first content item and expose the agent used for the turn plus response attachments through protocol-native content and structured data without changing connector delivery behavior.
- External MCP relay attachment same-message delivery does not merge progress placeholders, normal assistant response attachments, or cron-created channel-conversation attachments into the relay message; those surfaces keep their existing delivery contracts.

### Restart And Shutdown Cancellation

- `rocketclaw_restart` is for explicit runtime configuration changes such as `rocketclaw.json`, and remains valid for runtime asset changes when a full process restart is desired. Configuration-file changes and overlay-list changes still require `rocketclaw_restart`.
- Scheduled cron execution state lives in `<runtime-dir>/state.sqlite3` and must track enough state to prevent per-file overlap and persist scheduling/execution state across scheduler machinery.
- Restart must record pending restart notification/requester state before canceling the runtime context.
- Restart and signal-triggered shutdown must cancel the runtime context and then return through ordinary process teardown. They must not wait for managed bridge idleness, outbound delivery, active RocketCode turns, active cron jobs, or a clean interruption point. Active turns and active cron jobs may be cut off by runtime cancellation.
- Restart and signal-triggered shutdown do not create a separate shutdown phase for `ask_user_question`. Pending questions are in-memory only, have no shutdown timeout, are not recovered after restart, and are ended by owning-turn/runtime cancellation rather than by a model-visible shutdown timeout result. Unanswered pending connector question messages may be deleted during ordinary cleanup when reachable, but deletion failure must not block shutdown or restart. Already answered question messages are left in place.
- Restart recovery must rehydrate active persisted text goal loops from persisted managed-conversation state and output targets by starting their managed conversation bridges and queuing one continuation per active goal, without replaying missed turns.
- Active RocketCode root-turn recovery uses a durable active-turn handoff stored through the centralized harnessbridge state store. RocketClaw must not open a separate state database for this handoff, and must not infer incomplete-turn recovery from completed `SessionEntry` history.
- The old process writes active-turn state before provider inference, before local tool or subagent dispatch, after each completed tool output is known, after compaction recovery replaces replay, on normal completion, and on interruption. The state records the owning conversation, root turn identity, replay-safe input accumulated so far, known completed tool outputs, open model-emitted function calls, and relevant agent/model/provider trace metadata without relying on process-local channels, contexts, callbacks, or exact helper names.
- On interruption, the active-turn state becomes the restart handoff: incomplete function calls receive model-visible aborted or uncertain outputs only where replay validity requires them, incomplete `task` calls receive task-specific uncertainty, and a model-visible recovery instruction tells the model the runtime restarted and it must inspect current environment and conversation state before retrying, continuing, or reporting completion.
- During startup, RocketClaw opens the normal centralized store, reads remaining active-turn handoff rows before connector intake, treats each remaining valid row as a restart handoff, chooses at most one row per conversation, reconstructs RocketCode replay from the stored checkpoint, and enqueues the recovered root turn through the owning conversation's normal bridge sequencing path. Outside normal 30-day state cleanup, RocketClaw must never delete the only durable record of an unfinished turn. Startup recovery keeps the original handoff row until a replacement recovery checkpoint or completed `SessionEntry` is durable. Unknown, corrupt, duplicate, raw/cron, or unrecoverable rows must be logged and deleted rather than retained in the restart table or allowed to block all connector startup unless a future ADR explicitly chooses fail-closed startup. If startup recovery is interrupted by shutdown or cancellation, the row must be left untouched for a later startup attempt.
- Recovery is model-guided continuation from reconstructed replay, not exact OpenAI provider resume, old-process local tool completion, automatic tool rerun, or subagent task-tree resume. Same-conversation follow-ups and relays queue behind the recovered turn; different conversations may proceed independently when the normal bridge architecture allows it.
- Active-turn rows are cleared after normal completion once the completed `SessionEntry` is durable. Startup recovery clears the original handoff row only after a replacement checkpoint or completed `SessionEntry` is durable. Errors caused by shutdown or cancellation must leave the handoff row untouched for startup recovery. Permanent deeper checkpoint, replay, or recovery failure is logged and the row is deleted instead of being retained as a failed state or retried forever. Slack progress, relay, and final-response surfaces for recovered turns must be reused, completed, superseded, or kept model-visible-only according to existing delivery machinery; recovery must not blindly duplicate old visible progress or relay messages.
- Restart must not be triggered for ordinary memory, ledger, audit, report, source-code, generated artifact, log, transcript, or data-file edits.

### Permissions And Tools

- Task permission defaults must not become permissive by accident.
- Agent `maxRecursion` budgets are stricter than `task` permission grants; a permitted task target remains unavailable once the active inference's recursion budget is exhausted.
- Agent-system safety linting and graph inspection for permissions, delegation graphs, suppressions, and write-to-execute risk are governed by ADR 0006.
- `rocketclaw_restart` must be default-deny. It is visible/callable only when the active agent explicitly allows RocketCode permission bucket `rocketclaw` subject `rocketclaw_restart`. The generated `main` agent must explicitly allow `rocketclaw_restart` so fresh deployments can restart themselves after approved runtime configuration changes; newly added agents do not inherit restart permission unless their own agent definition grants it.
- Cron agents may selectively deny tools.
- A per-agent guardrail agent may use tools only when its own `permission` frontmatter allows those tools.
- RocketClaw tools are part of runtime behavior and must remain visible to RocketCode according to the bridge mode that owns the turn.
- The `ask_user_question` tool is a persistent-bridge RocketClaw tool visible only during human-initiated managed Slack turns that have a native answer path: `SourceSlack`, `Human == true`, and `SlackReply != nil`. It is available for authorized human thread turns, including goal kickoff and human re-steering when the turn otherwise qualifies. It is not visible for MCP-originated turns, cron/raw runs, scheduled-message turns, system/automation turns, automatic goal continuations, or restart recovery continuations.
- `ask_user_question` asks the authorized human in the originating Slack conversation using native UI. Custom/free-text is always available. Slack custom/free-text answers must render as an in-message custom-answer button alongside any choice controls or as the sole action when there are no choices; pressing that button opens a Slack Block Kit text input surface for the answer, and Slack custom/free-text answers must not depend on a normal conversation reply. Slack choice answers use native buttons or selects. Calls may omit native choice options because custom/free-text is always an answer path. Unauthorized users must not resolve pending questions.
- `ask_user_question` blocks the calling RocketCode tool call until the pending question is answered or the owning turn is canceled. Its result reports selected option values, optional custom text, and the source connector. If the owning active turn is interrupted before answer, RocketClaw cancels the pending question and deletes its unanswered connector message when one exists. When a pending Slack question is answered, RocketClaw deletes the answered connector question message so follow-up response messages retain their natural order.
- The text goal-loop update tool is a persistent-bridge tool visible only for conversations with an active text connector goal, and it may report `progress`, `complete`, or `blocked` with an optional explanatory `note`. `progress` records the note while keeping the persisted goal active. `complete` and `blocked` are terminal tool statuses. The `note` field is the structured mirror of the visible goal `Progress summary:` and explains status notes, what is going on, what changed, what the agent is thinking, where the goal is heading next, what was completed, or what is blocking progress. Human stop emoji behavior may interrupt the active turn and set the goal to `stopped` without using the tool.
- The `rocketclaw_start_new_thread` tool is a persistent-bridge RocketClaw tool visible only during human-initiated managed Slack turns when the active agent has an explicit per-agent `allow` rule for RocketClaw permission subject `rocketclaw_start_new_thread`, `SourceSlack`, `Human == true`, and `SlackReply != nil`. Missing permission, `auto`, or `deny` keeps the tool hidden and unavailable. The tool is never visible or callable for an MCP-originated or cron-originated turn, scheduled-message turn, system/automation turn, automatic goal continuation, or restart recovery continuation. The tool accepts title, prompt, and optional agent; it must reject unsupported target agents without creating a conversation. The target agent must be loaded and configured for the originating channel. The tool prompt is the literal first model-visible task in the fresh conversation, receives normal RocketClaw prompt provenance framing, and does not enable shell interpolation.

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
- 2026-07-01: Changed bare `🎛` social-mode agent switching from immediate cycle to Slack-native requester-scoped selectors, and changed successful Slack switch acknowledgements to visible thread replies.
- 2026-07-01: Clarified per-target-agent guardrails as approve-or-reject gates rather than prompt or response transformers.
- 2026-07-01: Removed Discord Text, Discord voice, and browser voice behavior contracts.
- 2026-07-01: Removed terminal CLI behavior contracts, including terminal input, live observation, slash commands, private terminal conversations, terminal `ask_user_question`, terminal `rocketclaw_start_new_thread`, cmux behavior, and control-socket behavior.
- 2026-07-01: Added Slack `💡 <skill-name> [arguments]` triggers for RocketCode direct skill invocation and their routing, permission, and prompt-framing constraints.
- 2026-07-02: Specified breadcrumb rendering for nested subagent, guardrail, and automatic permission-review progress diagnostics in connector-visible thinking.
- 2026-07-02: Removed the automated inbound post-human quiet-window contract while preserving shared inbound human-priority ordering over automation.
- 2026-07-04: Specified External MCP new-conversation agent selection without default-agent behavior; the request must name a loaded agent.
- 2026-07-04: Specified SQLite-backed per-file scheduled cron execution sequencing, coalesced no-backlog semantics, no downtime or pre-observation catch-up replay, global-ticker scanning of effective scheduled cron definitions, and restart boundaries.
- 2026-07-04: Specified `rocketclaw_reload` validation-before-commit behavior and its non-interruption boundaries.
- 2026-07-07: Specified that `ask_user_question` becomes inert during restart or signal-triggered shutdown drain by having shutdown intentionally take over existing pending waits with a model-visible timeout result and short-circuit later calls without posting new connector question messages.
- 2026-07-07: Clarified that managed and response-rooted inherited-context seed compaction uses the RocketClaw seed compaction model selection contract.
- 2026-07-07: Replaced no-timeout graceful restart/shutdown drain with immediate runtime cancellation, preserving restart notification/requester recording before cancellation and removing waits for inbound handoff, bridge idleness, outbound delivery, active RocketCode turns, active cron jobs, and `ask_user_question` shutdown timeout takeover.
- 2026-07-07: Added centralized active-turn durable state lifecycle for restart handoff, reconstructed-replay recovery before connector intake, Slack surface non-duplication, and External MCP old-request and same-conversation queueing semantics.
- 2026-07-07: Clarified active-turn row-existence semantics: no status column/state is required, completed rows are deleted after durable completion, invalid/duplicate/raw/cron/unrecoverable rows are logged and deleted, and shutdown/cancellation-caused errors preserve rows for later startup recovery.
- 2026-07-08: Clarified no-loss startup recovery: never delete the only durable record of an unfinished turn.
- 2026-07-08: Made `rocketclaw_restart` default-deny unless the active agent explicitly allows the `rocketclaw_restart` permission subject, while requiring generated `main` agents to allow it.
- 2026-07-10: Defined how RocketClaw uses threads shared through Slack: the sender's message stays the request, the shared material stays reference, and full threads are read only from confirmed public channels with clear size and file rules.
- 2026-07-15: Defined message flow around configured Slack channels and persisted managed threads with explicit conversation-owned delivery targets.
- 2026-07-15: Defined new Slack conversations to begin with their initiating prompt and to replay only their own thread-local history.
- 2026-07-15: Required configured Slack channel targets for External MCP and cron, bound each External MCP conversation ID to one shared Slack thread history, and defined cron completion through its explicit human-visible output decision.
- 2026-07-15: Defined Slack connector availability as unconditional runtime behavior.
- 2026-07-16: Required routing fields on every MCP call. Added separate private and managed sessions on one Slack thread, fixed and switchable agents, one-way history and metadata copying without compactions, one-at-a-time turns, correct recovery ownership, and MCP Slack Blocks.
- 2026-07-16: Defined oversized MCP request continuation messages, plain request bodies, Slack-Markdown response bodies, labeled file-only responses, managed-only stop controls, and the 30-day cleanup exception for unfinished work.
- 2026-07-17: Required first-call MCP metadata environment variables in both paired sessions, replaced stale buffered-message hourglasses with interruption reactions on stop, and required dedicated MCP Slack headers, identity context, and dividers.
- 2026-07-17: Kept separate thinking and answer messages, made thinking sections always expanded, and removed progress blockquotes.
- 2026-07-17: Replaced plain progress with one task card, using the newest activity as title and chronological older details, and kept the card with `complete` status after successful turn completion.
- 2026-07-18: Set completed task-card titles to `Complete` and moved every activity into chronological details.
- 2026-07-18: Moved thinking and goal-progress labels into the running task-card title and removed the separate label section.
- 2026-07-21: Removed emergency safe words and their process-exit behavior from Slack message handling.
- 2026-07-21: Used native Slack Markdown streams for human-originated thinking messages with identified recipients while keeping the separate answer placeholder and the existing task card for turns without recipients. Rendered links in task-card activities with rich-text link elements. Changed MCP request bodies from plain text to Slack Markdown so links and formatting render.
- 2026-07-21: Kept notification-bearing Slack mention text from MCP requests literal so MCP request messages cannot notify users, user groups, or broadcast targets.
- 2026-07-22: Reverted human thinking messages from native Slack Markdown streams to the existing task-card updates because Markdown streaming changed wildcard text, removed task-card folding and per-activity sections, and removed the completed task-card state. Kept separate thinking and answer placeholders and kept link rendering improvements.
- 2026-07-22: Authorized two sequential pre-release test-environment variants that change only thinking-card updates: first native `task_update` streaming, then streamed task-card Blocks only if the first variant fails. Required the answer implementation and all established task-card behavior to remain unchanged, and prohibited a manual release until a later ADR entry selects or removes the experiment.
- 2026-07-22: Changed pre-release Variant A from one cumulative task update to one thinking stream containing an overall turn-status task plus one bounded task entry for each newly received thinking activity. Activities remain ordered inside one thinking message and are never cumulatively resent.
- 2026-07-22: Defined each Variant A activity chunk as a completed task title, and defined deterministic Unicode-safe continuation boundaries that preserve every character and the original activity order.
- 2026-07-22: Recorded that live Variant A rendered every activity as a separate task card inside the thinking message, which failed the required one-card layout. Activated the already authorized Variant B streamed-Blocks evaluation.
- 2026-07-22: Changed Variant B to Slack's native Plan display: one `plan` Block in the thinking message, `task_update` activity entries inside its `tasks`, and `plan_update` for the running and completed Plan title.
