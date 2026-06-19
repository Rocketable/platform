# 0001. Runtime Capabilities

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw is a workspace-local Go runtime that connects human inputs, automation inputs, and the embedded RocketCode reasoning loop through a small set of supported connectors and tools.

## Scope

This ADR governs current user-visible runtime capabilities. Configuration, state, and RocketCode embedding details are governed by companion ADRs.

## Context

RocketClaw grew by adding Slack, Discord voice, external MCP, browser voice, cron, scheduled messages, thread routing, attachments, and restart behavior. Future refactors must preserve these capabilities unless the human partner explicitly approves a spec change.

## Normative Contracts

| Area               | Current capability                                                                                                                                                                                                                           |
|--------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Main session       | A persistent `main` RocketCode conversation consumes shared inbound human/automation messages and emits configured outbound messages.                                                                                                         |
| Terminal CLI       | Server mode always exposes a local Unix-domain control socket at `<runtime-dir>/control/control.sock`. `rocketclaw cli` attaches through that socket to the persistent `main` conversation when a server is available, `rocketclaw cli --attach <conversation-id>` attaches directly to an existing server-owned conversation, and `rocketclaw cli --new [agent]` asks the server to start a fresh generated private terminal conversation using the supplied agent or `main` when omitted. When running inside cmux, terminal-local `/new [agent]` opens a new cmux terminal surface attached to a server-created private CLI conversation for the supplied agent or `main` when omitted. Socket-attached CLI clients are non-owning terminal frontends; they do not open writable runtime state, acquire the state-store lock, or start bridges directly. Terminal CLI conversations use the persistent RocketCode bridge, store session entries in the selected runtime SQLite state store, and keep private `--new` and cmux-created sessions inspectable through `rocketclaw fc list` and `rocketclaw fc observe <conversation-id>`. Terminal-originated RocketCode turns may use `ask_user_question`; the server brokers pending question state and sends native terminal question prompts to the matching attached CLI client, which renders predefined options, multiple-selection mode, and custom/free-text answers. If the socket is unavailable and the state-store lock is free, CLI may run embedded fallback, including local terminal question prompts; if the socket is unavailable and the lock is held, CLI fails clearly. |
| Primary text connector | When text is enabled, exactly one primary text connector is enabled and injected through one common primary text connector API: Slack or Discord Text. The enabled text connector accepts configured-human DMs, supports configured social-mode channel mentions with per-channel authorization, routes main conversation text, publishes visible progress and final responses, supports main-session stop controls, hosts managed conversations, supports response-rooted conversations where the transport supports them, supports summaries, direct and repeat-reaction cron requests, inbound and outbound attachments, model-initiated answerable human questions through the `ask_user_question` tool on qualifying human text turns, and persisted conversation-local goal loops with optional `maxTurns:` budgets and optional `checkScript:` completion gates as specified by ADR 0007. Social-mode channel configuration uses an ordered `agents` allowed-agent list; new social-mode conversations use the first configured agent, and authorized managed-conversation replies can switch the persisted conversation-local agent to another configured agent. Shared runtime bridges use the common API and must not carry parallel Slack-specific and Discord-specific implementations for the same text-connector operation. |
| Slack text binding | Slack uses Socket Mode, app mentions, Slack DMs, and Slack threads for managed and response-rooted conversations. Slack social-mode channel replies that ping other people, bots, broadcast targets, or user groups are suppressed unless the RocketClaw bot is also mentioned; Slack channel references do not trigger that suppression. Emoji-prefixed prompts can start managed agent threads, and Slack goal triggers use the same connector-local emoji-prefix normalization as other emoji-prefixed starts before entering the shared goal parser. `:floppy_disk:` summarizes managed threads back to main. |
| Discord Text binding | Discord Text uses Discord DMs, bot mentions, configured guild text channels, and guild threads for managed and response-rooted guild-channel conversations. Discord DM conversations are managed conversations without guild-thread mechanics. Discord Text mirrors the primary text connector contract where Discord delivery surfaces can express it, including connector-local emoji-prefix normalization before the shared goal parser. |
| Discord voice      | RocketClaw joins one configured voice channel, listens only to the configured human speaker, transcribes speech, routes it into the shared conversation, and can speak synthesized responses. Discord voice is separate from Discord text and can coexist with either primary text connector. |
| Browser voice      | `web_ui` serves HTTPS `/voice-mode`, receives browser WebM/Opus microphone audio over WebSocket, routes transcriptions like voice input, and serves synthesized playback. Current browser capture is Chrome-oriented.                        |
| External MCP       | `mcp_external` serves HTTP `/mcp` with exactly one tool, `session_prompt`, accepting input, optional external conversation ID, optional agent, optional Slack channel, optional metadata, and optional inbound attachments, relaying the prompt and any MCP-provided attachments into the primary text connector as one visible native text-and-files message, and returning the final answer plus outbound attachments produced by the shared response attachment path.                          |
| Cron               | `cron/*.md` files are loaded at startup. They can run scheduled or one-off prompts through raw RocketCode runs and can produce internal main-session notes or managed primary text connector channel-conversation output. Replies and summaries for cron-created connector channel conversations follow the connector's existing gates. `*.example.md` files are ignored.                  |
| One-off cron       | Timestamp cron files run once and are deleted after the run attempt. Text connector one-off cron requests can load top-level cron files by stem. DM one-off cron requests can run any top-level cron; channel message and reaction reruns are restricted to cronjobs whose configured `channel` targets the acted-on connector channel. Successful text connector one-off cron runs register the result conversation as a managed cron conversation with the cron agent and output as seed context, so follow-up replies route like scheduled cron result conversations. |
| Scheduled messages | The RocketCode tool can schedule one-shot delayed prompts, durable recurring prompts, and reset scheduled messages. One-shot messages are durable until handled. Recurring schedules are durable until reset, continue from current time after missed or failed occurrences, do not replay missed intervals, and report through the relevant bridge context. |
| Attachments        | Supported image attachments can be passed into RocketCode. Text attachments from text connectors and external MCP are converted into prompt text within size limits. External MCP accepts base64 attachments that flow through the same inbound attachment handling as connector-provided attachments and, when the prompt is relayed to the primary text connector, are attached to that same visible relay message. RocketCode response attachments flow through the shared outbound attachment carrier used by text connector delivery and external MCP result rendering. |
| Restart            | `rocketclaw_restart` schedules a graceful restart, stops intake, drains queues and active bridges, records pending restart notifications, and exits for supervisor restart.                                                                   |

## Non-Goals

- RocketClaw is not a general-purpose Discord bot; Discord text support is scoped to configured Discord text channels, DMs, social-mode mentions, goal loops, and managed conversations.
- RocketClaw is not a multi-workspace SaaS; behavior is scoped to one configured workspace.
- Runtime capabilities here do not document every setup step or every Slack/Discord permission.

## Evidence

- `README.md`
- `internal/app/app.go`
- `internal/slackconnector/connector.go`
- `internal/discordvoice/connector.go`
- `internal/webui/server.go`
- `internal/externalmcp/server.go`
- `internal/cronjob/manager.go`
- `internal/rocketcodebridge/bridge.go`
- `internal/rocketcodebridge/raw_run.go`

## Consequences

- Feature removals require explicit human approval through this ADR corpus.
- Refactors must preserve these capabilities or update this ADR first.
- New connector capabilities should be added here before implementation when they are intentional feature changes.

## Changelog

- 2026-05-25: Initial accepted snapshot.
- 2026-05-25: Added durable recurring scheduled messages until reset, without catch-up replay.
- 2026-06-02: Made cron `slack-channel` output a managed Slack thread whose replies and summaries follow Slack social-mode gates.
- 2026-06-02: Added Discord text as a Slack-alternative primary text connector using a configured guild text channel and managed guild threads.
- 2026-06-02: Renamed cron managed Slack thread routing to canonical `channel`, with `slack-channel` retained as a backward-compatible alias.
- 2026-06-02: Added Slack and Discord Text parity for repeat-reaction one-off cron reruns, including channel-target restrictions.
- 2026-06-04: Added Slack social-mode thread reply suppression for messages pinging others unless the RocketClaw bot is also mentioned.
- 2026-06-09: Excluded Slack channel references from Slack social-mode thread reply suppression.
- 2026-06-11: Added Slack DM and Slack social-mode persisted goal loops governed by ADR 0007.
- 2026-06-11: Added `🏁` as an additional Slack goal-loop trigger alongside `🔁`.
- 2026-06-11: Added Slack social-mode per-channel allowed-user authorization with fallback to top-level social-mode allowed users.
- 2026-06-11: Removed runtime fallback to top-level Slack social-mode allowed users; legacy fallback users are copied into channel allowlists during startup migration.
- 2026-06-12: Specified that external MCP base64 attachments use the shared inbound attachment handling path, including text attachment prompt conversion.
- 2026-06-12: Specified that external MCP returns outbound attachments from the same shared response attachment path used by connector delivery.
- 2026-06-14: Recast Slack and Discord Text as bindings of one primary text connector contract covering social mode, DMs, visible progress, attachments, direct cron requests, and goal loops.
- 2026-06-14: Required one injected common primary text connector API so shared bridges do not carry parallel Slack and Discord implementations.
- 2026-06-14: Added primary text connector main-session stop controls to the capability snapshot.
- 2026-06-14: Specified that text connector goal triggers use connector-local emoji-prefix normalization before the shared goal parser, including Slack's `:repeat:` and `:checkered_flag:` transport text for `🔁` and `🏁`, while preserving Slack/Discord Text symmetry.
- 2026-06-16: Specified that External MCP prompt relay attachments are attached to the same visible primary text connector relay message.
- 2026-06-16: Added canonical social-mode `agents` allowed-agent lists, legacy scalar `agent` migration, and authorized persisted managed-conversation agent switching.
- 2026-06-16: Removed startup config migration from the primary text connector capability; canonical social-mode `agents` lists are required in loaded config.
- 2026-06-16: Added primary text connector `ask_user_question` capability for model-initiated human questions on qualifying human Slack and Discord Text turns.
- 2026-06-17: Specified that successful text connector one-off cron result conversations become managed cron conversations for follow-up replies.
- 2026-06-17: Specified that `ask_user_question` capability covers answerable human questions.
- 2026-06-18: Added terminal CLI runtime capability for attaching to `main` or starting generated private persisted conversations.
- 2026-06-19: Changed terminal CLI to prefer the server-owned Unix control socket, added `--attach`, and specified lock-aware embedded fallback.
- 2026-06-19: Added terminal-originated `ask_user_question` capability through attached CLI clients while preserving server-owned pending question state.
- 2026-06-19: Added cmux terminal-local `/new [agent]` as a way to open server-created private CLI conversations in new cmux terminal surfaces.
