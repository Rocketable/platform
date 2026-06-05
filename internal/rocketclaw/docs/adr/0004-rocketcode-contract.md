# 0004. RocketCode Contract

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw embeds RocketCode as the reasoning runtime through two app-owned construction paths: the persistent conversation bridge and raw runs. RocketClaw owns which prompt sources are trusted, which tools are injected, and how sessions are persisted and replayed.

## Scope

This ADR governs rocketclaw's contract with the embedded `github.com/Rocketable/rocketcode` library. RocketCode's own public API is documented upstream; this ADR records rocketclaw's embedding policy.

## Context

Several rocketclaw capabilities exist only because of precise RocketCode configuration: prompt shell expansion, stronger skills, custom tools, session replay, attachments, and raw cron completion. These settings are product behavior, not incidental config.

## Normative Contracts

### Construction Paths

| Path              | File                                  | Purpose                                                                                | Prompt input expansion |
|-------------------|---------------------------------------|----------------------------------------------------------------------------------------|------------------------|
| Persistent bridge | `internal/rocketcodebridge/bridge.go`  | Main, thread, Slack, Discord text, browser, Discord voice, scheduled, and external MCP conversation turns. | `InputPrompts: false`  |
| Raw run           | `internal/rocketcodebridge/raw_run.go` | Cron and one-off cron background turns.                                                | `InputPrompts: true`   |

Both paths enable `PrimaryPrompts`, `SubagentPrompts`, and `SkillPrompts` shell expansion. Persistent bridge input text remains literal. Raw input text expands because cron bodies are trusted workspace files.

### Prompt And Definition Loading

- Agents load from `.rocketclaw/agents` plus workspace overlays according to bridge mode.
- Skills load from `.rocketclaw/skills` plus workspace overlays according to bridge mode.
- Primary agent prompt expansion happens during RocketCode construction.
- Subagent prompt expansion happens when the `task` tool launches another agent.
- Skill content expansion happens when the `skill` tool loads skill content.
- `AGENTS.md` root workspace instructions remain literal.

### Tools Injected By RocketClaw

| Tool                                         | Contract                                                                                                                      |
|----------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------|
| `rocketclaw_restart`                          | Schedules graceful restart for approved runtime config/asset changes.                                                         |
| `rocketclaw_schedule_message`                 | Schedules one-shot delayed prompts or recurring delayed prompts through the owning bridge context. Recurring prompts use optional `recurring: true`, require `send_this_in` from 1m through 1h, persist until reset, and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages`         | Clears scheduled messages for the owning bridge context.                                                                      |
| `rocketclaw_attach_files_to_response`         | Persistent bridge tool that allows RocketCode to attach collected files to the outbound response.                              |
| `rocketclaw_i_want_human_partner_to_see_this` | Required completion tool for raw background runs; its argument is the exact human-visible final message or empty for silence. |

Persistent bridge tools are restart, schedule message, reset scheduled messages, attach files, and path-specific custom tools. Raw-run tools are decision, outbound attachment collection, restart, schedule message, and reset scheduled messages. Raw and persistent schedule-message tools expose the same one-shot and recurring contract.

### Session And Replay

- Persistent conversations use SQLite-backed session storage under `.rocketclaw/state.sqlite3`, opened through the centralized RocketClaw SQLite state-store opener defined by ADR 0005.
- Raw runs can persist into a configured conversation when supplied with `RawRunProgress.SessionService` and `ConversationID`.
- External MCP metadata is injected as a developer message for the turn that supplied it and must not become ambient global state.
- Attachments are converted into RocketCode prompt attachments only when supported by the bridge path.

### Diagnostics And Skills

- RocketClaw enables `ExperimentalStrongerSkills` in both persistent and raw paths.
- Diagnostics are enabled for the persistent bridge and for raw runs when progress reporting is configured.
- RocketClaw sets RocketCode's maximum parallel tool calls to 16 in both persistent and raw paths.

## Non-Goals

- This ADR does not duplicate RocketCode's full API documentation.
- This ADR does not allow human/external input shell execution in the persistent bridge.
- This ADR does not require preserving deprecated RocketCode APIs when rocketclaw's behavior can be preserved through a newer API.

## Evidence

- `internal/rocketcodebridge/bridge.go`
- `internal/rocketcodebridge/raw_run.go`
- `internal/rocketcodebridge/store.go`
- `vendor/github.com/Rocketable/rocketcode/rocketcode.go`
- `vendor/github.com/Rocketable/rocketcode/looper.go`
- `vendor/github.com/Rocketable/rocketcode/prompts.go`
- `vendor/github.com/Rocketable/rocketcode/tools.go`
- `vendor/github.com/Rocketable/rocketcode/tasks.go`

## Consequences

- Changing RocketCode config flags is a behavior change and requires ADR approval when it changes meaning.
- Dependency upgrades must be checked against this embedding contract, especially prompt expansion, tools, session replay, attachments, and raw-run completion.
- Tests should verify observable RocketCode input/output behavior, not only that config structs are constructed.

## Changelog

- 2026-05-25: Initial accepted snapshot.
- 2026-05-25: Added optional recurring scheduled-message contract for persistent and raw RocketCode paths.
- 2026-06-02: Set RocketCode maximum parallel tool calls to 16 for persistent and raw RocketClaw paths.
- 2026-06-02: Added Discord text as a persistent-bridge source whose human input remains literal.
- 2026-06-05: Linked persistent conversation SQLite storage to the centralized RocketClaw state-store opener in ADR 0005.
