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
| Primary text connector | When text is enabled, exactly one primary text connector is enabled and injected through one common primary text connector API: Slack or Discord Text. The enabled text connector accepts configured-human DMs, supports configured social-mode channel mentions with per-channel authorization, routes main conversation text, publishes visible progress and final responses, supports main-session stop controls, hosts managed conversations, supports response-rooted conversations where the transport supports them, supports summaries, direct and repeat-reaction cron requests, inbound and outbound attachments, and supports persisted conversation-local goal loops with optional `maxTurns:` budgets and optional `checkScript:` completion gates as specified by ADR 0007. Shared runtime bridges use the common API and must not carry parallel Slack-specific and Discord-specific implementations for the same text-connector operation. |
| Slack text binding | Slack uses Socket Mode, app mentions, Slack DMs, and Slack threads for managed and response-rooted conversations. Slack social-mode channel replies that ping other people, bots, broadcast targets, or user groups are suppressed unless the RocketClaw bot is also mentioned; Slack channel references do not trigger that suppression. Emoji-prefixed prompts can start managed agent threads. `:floppy_disk:` summarizes managed threads back to main. |
| Discord Text binding | Discord Text uses Discord DMs, bot mentions, configured guild text channels, and guild threads for managed and response-rooted guild-channel conversations. Discord DM conversations are managed conversations without guild-thread mechanics. Discord Text mirrors the primary text connector contract where Discord delivery surfaces can express it. |
| Discord voice      | RocketClaw joins one configured voice channel, listens only to the configured human speaker, transcribes speech, routes it into the shared conversation, and can speak synthesized responses. Discord voice is separate from Discord text and can coexist with either primary text connector. |
| Browser voice      | `web_ui` serves HTTPS `/voice-mode`, receives browser WebM/Opus microphone audio over WebSocket, routes transcriptions like voice input, and serves synthesized playback. Current browser capture is Chrome-oriented.                        |
| External MCP       | `mcp_external` serves HTTP `/mcp` with exactly one tool, `session_prompt`, accepting input, optional external conversation ID, optional agent, optional Slack channel, optional metadata, and optional inbound attachments, and returning the final answer plus outbound attachments produced by the shared response attachment path.                          |
| Cron               | `cron/*.md` files are loaded at startup. They can run scheduled or one-off prompts through raw RocketCode runs and can produce internal main-session notes or managed primary text connector channel-conversation output. Replies and summaries for cron-created connector channel conversations follow the connector's existing gates. `*.example.md` files are ignored.                  |
| One-off cron       | Timestamp cron files run once and are deleted after the run attempt. Text connector on-demand cron requests can load top-level cron files by stem. DM on-demand cron can run any top-level cron; channel message and reaction reruns are restricted to cronjobs whose configured `channel` targets the acted-on connector channel.                                                                                                    |
| Scheduled messages | The RocketCode tool can schedule one-shot delayed prompts, durable recurring prompts, and reset scheduled messages. One-shot messages are durable until handled. Recurring schedules are durable until reset, continue from current time after missed or failed occurrences, do not replay missed intervals, and report through the relevant bridge context. |
| Attachments        | Supported image attachments can be passed into RocketCode. Text attachments from text connectors and external MCP are converted into prompt text within size limits. External MCP accepts base64 attachments that flow through the same inbound attachment handling as connector-provided attachments. RocketCode response attachments flow through the shared outbound attachment carrier used by text connector delivery and external MCP result rendering. |
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
