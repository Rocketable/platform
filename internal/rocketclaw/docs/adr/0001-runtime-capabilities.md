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
| Slack DM           | Slack Socket Mode accepts configured-human DM messages, buffers stacked messages, routes the main Slack conversation, and publishes responses. Slack DM is a primary text connector and is mutually exclusive with Discord text.              |
| Slack social mode  | Configured app mentions in allowed Slack channels can start channel-scoped conversations for configured agents. Unconfigured channels are ignored.                                                                                           |
| Slack threads      | Emoji-prefixed prompts can start managed agent threads. Replies to newer AI responses can start response-rooted threads with inherited context. `:floppy_disk:` summarizes managed threads back to main.                                     |
| Discord text       | Discord text connects one configured guild text channel, accepts configured-human messages, routes the main conversation, publishes responses, and can host managed guild-thread conversations. Discord text is a primary text connector and is mutually exclusive with Slack DM. |
| Discord voice      | RocketClaw joins one configured voice channel, listens only to the configured human speaker, transcribes speech, routes it into the shared conversation, and can speak synthesized responses. Discord voice is separate from Discord text and can coexist with either primary text connector. |
| Browser voice      | `web_ui` serves HTTPS `/voice-mode`, receives browser WebM/Opus microphone audio over WebSocket, routes transcriptions like voice input, and serves synthesized playback. Current browser capture is Chrome-oriented.                        |
| External MCP       | `mcp_external` serves HTTP `/mcp` with exactly one tool, `session_prompt`, accepting input, optional external conversation ID, optional agent, optional Slack channel, optional metadata, and optional attachments.                          |
| Cron               | `cron/*.md` files are loaded at startup. They can run scheduled or one-off prompts through raw RocketCode runs and can produce internal main-session notes or managed Slack/Discord Text channel-thread output. Replies and summaries for cron-created connector channel threads follow the connector's existing gates. `*.example.md` files are ignored.                  |
| One-off cron       | Timestamp cron files run once and are deleted after the run attempt. Slack and Discord Text on-demand cron requests can load top-level cron files by stem. Slack DM on-demand cron can run any top-level cron; channel reaction reruns in Slack and Discord Text are restricted to cronjobs whose configured `channel` targets the reacted channel.                                                                                                    |
| Scheduled messages | The RocketCode tool can schedule one-shot delayed prompts, durable recurring prompts, and reset scheduled messages. One-shot messages are durable until handled. Recurring schedules are durable until reset, continue from current time after missed or failed occurrences, do not replay missed intervals, and report through the relevant bridge context. |
| Attachments        | Supported image attachments can be passed into RocketCode. Slack text attachments are converted into prompt text within size limits. External MCP supports base64 attachments.                                                                |
| Restart            | `rocketclaw_restart` schedules a graceful restart, stops intake, drains queues and active bridges, records pending restart notifications, and exits for supervisor restart.                                                                   |

## Non-Goals

- RocketClaw is not a general-purpose Discord bot; Discord text support is scoped to the configured guild channel and managed threads.
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
