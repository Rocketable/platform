# 0001. Runtime Capabilities

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw is a workspace-local Go runtime that connects human inputs, automation inputs, and the embedded RocketCode reasoning loop through a small set of supported connectors and tools.

## Scope

This ADR governs current user-visible runtime capabilities. Configuration, state, and RocketCode embedding details are governed by companion ADRs.

## Context

RocketClaw grew by adding Slack, external MCP, cron, scheduled messages, thread routing, attachments, and restart behavior. Future refactors must preserve these capabilities unless the human partner explicitly approves a spec change.

## Normative Contracts

| Area               | Current capability                                                                                                                                                                                                                           |
|--------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Main session       | A persistent `main` RocketCode conversation consumes shared inbound human/automation messages and emits configured outbound messages.                                                                                                         |
| Primary text connector | When text is enabled, Slack is the primary text connector and is injected through the common primary text connector API. The enabled text connector accepts configured-human DMs, supports configured social-mode channel mentions with per-channel authorization, routes main conversation text, publishes visible progress and final responses, supports main-session stop controls, hosts managed conversations, supports response-rooted conversations where the transport supports them, supports summaries, direct and repeat-reaction cron requests, inbound and outbound attachments, model-initiated answerable human questions through the `ask_user_question` tool on qualifying human text turns, model-initiated new managed conversations through the default-deny `rocketclaw_start_new_thread` tool on explicitly allowed qualifying text turns, and persisted conversation-local goal loops with optional `maxTurns:` budgets and optional `checkScript:` completion gates as specified by ADR 0007. Social-mode channel configuration uses an ordered `agents` allowed-agent list; new social-mode conversations use the first configured agent, authorized managed-conversation replies can switch the persisted conversation-local agent to another configured agent by name, and authorized managed-conversation replies that send bare `🎛` open a Slack-native agent selector usable only by the authorized user who sent the bare control message. |
| Slack text binding | Slack uses Socket Mode, app mentions, Slack DMs, and Slack threads for managed and response-rooted conversations. Slack social-mode channel replies that ping other people, bots, broadcast targets, or user groups are suppressed unless the RocketClaw bot is also mentioned; Slack channel references do not trigger that suppression. Emoji-prefixed prompts can start managed agent threads, Slack goal triggers use the same connector-local emoji-prefix normalization as other emoji-prefixed starts before entering the shared goal parser, and allowed `rocketclaw_start_new_thread` tool calls create a new bot-authored top-level root message in the originating Slack channel or DM with no parent thread timestamp and manage a Slack thread rooted at that message. `:floppy_disk:` summarizes managed threads back to main. |
| External MCP       | `mcp_external` serves HTTP `/mcp` with exactly one tool, `session_prompt`, accepting input, optional external conversation ID, optional agent, optional Slack channel, optional metadata, and optional inbound attachments. New external MCP conversations use the requested agent or the configured default agent; existing external MCP conversations use the agent already persisted for that external conversation ID, ignoring and logging any mismatched requested agent. External MCP relays the prompt and any MCP-provided attachments into the primary text connector as one visible native text-and-files message, and returns the final answer, the agent used for the turn, and outbound attachments produced by the shared response attachment path. `rocketclaw_start_new_thread` is never exposed through External MCP or any other MCP-originated turn.                          |
| Cron               | `cron/*.md` files are loaded at startup. They can run scheduled or one-off prompts through raw RocketCode runs. Every completed scheduled cron adds a silent run summary to `main`. The `rocketclaw_i_want_human_partner_to_see_this` argument is separate extra output for the human: an empty string sends no connector output, and a non-empty string opens or uses the managed cron output thread for that text. Cron output threads use the configured `channel` when present, otherwise the primary text connector's default Slack DM room when Slack is enabled. Extra human-visible cron output is never delivered as main-session verbatim output. Replies and summaries for cron-created connector conversations follow the connector's existing gates. Cron-originated turns, including raw, scheduled, one-off, and cron-created conversation continuations, never receive `rocketclaw_start_new_thread`. `*.example.md` files are ignored.                  |
| One-off cron       | Timestamp cron files run once and are deleted after the run attempt. Text connector one-off cron requests can load top-level cron files by stem. DM one-off cron requests can run any top-level cron; channel message and reaction reruns are restricted to cronjobs whose configured `channel` targets the acted-on connector channel. Successful text connector one-off cron runs register the result conversation as a managed cron conversation with the cron agent and output as seed context, so follow-up replies route like scheduled cron result conversations. |
| Scheduled messages | The RocketCode tool can schedule one-shot delayed prompts, durable recurring prompts, and reset scheduled messages. One-shot messages are durable until handled. Recurring schedules are durable until reset, continue from current time after missed or failed occurrences, do not replay missed intervals, and report through the relevant bridge context. |
| Attachments        | Supported image attachments can be passed into RocketCode. Text attachments from text connectors and external MCP are converted into prompt text within size limits. External MCP accepts base64 attachments that flow through the same inbound attachment handling as connector-provided attachments and, when the prompt is relayed to the primary text connector, are attached to that same visible relay message. RocketCode response attachments flow through the shared outbound attachment carrier used by text connector delivery and external MCP result rendering. |
| Restart            | `rocketclaw_restart` schedules a graceful restart, stops intake, drains queues and active bridges, records pending restart notifications, and exits for supervisor restart.                                                                   |

## Non-Goals

- RocketClaw is not a multi-workspace SaaS; behavior is scoped to one configured workspace.
- Runtime capabilities here do not document every setup step or every Slack permission.

## Evidence

- `README.md`
- `internal/app/app.go`
- `internal/slackconnector/connector.go`
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
- 2026-06-24: Specified that existing External MCP conversation IDs keep their persisted agent when a request names a different agent, and that External MCP responses include the agent used for the turn.
- 2026-06-25: Added bare `🎛` social-mode managed-conversation agent cycling across configured channel `agents`.
- 2026-06-25: Added default-deny `rocketclaw_start_new_thread` capability for explicitly allowed terminal CLI and primary text connector turns, including Slack root-thread creation, Discord guild-thread creation, Discord DM clear failure, and terminal cmux attach fallback semantics.
- 2026-06-25: Specified that `rocketclaw_start_new_thread` terminal sessions inherit source conversation context and treat the tool prompt as the first task prompt rather than as the seed itself.
- 2026-06-25: Clarified that `rocketclaw_start_new_thread` is never exposed through MCP-originated or cron-originated turns.
- 2026-06-26: Specified that scheduled cron visible output without `channel` uses the enabled primary text connector's default Slack DM room or Discord guild channel.
- 2026-06-26: Clarified that no-`channel` scheduled cron results keep the existing main-session internalization path after any text-sink delivery attempt.
- 2026-06-29: Removed the legacy cron `slack-channel` frontmatter alias; cron text routing uses `channel` only.
- 2026-06-30: Clarified scheduled cron wording by separating the always-silent main summary from extra human-visible output.
- 2026-07-01: Changed bare `🎛` social-mode agent switching from immediate cycle to Slack-native requester-scoped selectors.
- 2026-07-01: Removed Discord Text, Discord voice, and browser voice from supported runtime capabilities.
- 2026-07-01: Removed terminal CLI, terminal private conversations, cmux terminal creation, and control-socket runtime capabilities.
