# 0003. Configuration, State, And Operations

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw keeps runtime configuration and first-party workspace assets in explicit workspace files, persists runtime continuity in `.rocketclaw/state.sqlite3`, and requires restart for configuration/asset changes that affect RocketCode or cron discovery.

## Scope

This ADR governs current configuration files, state files, setup outputs, restart boundaries, and operational expectations.

## Context

RocketClaw is operated by humans and agents in a shared workspace. Its behavior depends on filesystem assets, SQLite state, setup-generated scaffolding, and connector configuration. These operational contracts must remain clear and compact.

## Normative Contracts

| File or directory | Contract |
| --- | --- |
| `rocketclaw.json` | Main runtime config. Relative `workspace` resolves relative to the config file. At least one of Discord voice, Slack, external MCP, or web UI must be enabled. |
| `femtoclaw.json` | Legacy runtime config. If present, startup and operational commands load it instead of `rocketclaw.json` and use `.femtoclaw/` as the generated runtime directory. |
| `rocketclaw.users.json` | Optional external MCP Basic Auth users next to `rocketclaw.json`. If present, it must be a JSON object and file mode `0600`. Missing means MCP runs without auth. |
| `AGENTS.md` | Workspace instruction file generated when missing. Loaded literally; no shell interpolation. |
| `agents/`, `skills/` | User-overridable workspace overlays for agent and skill assets. Changes require restart to affect running RocketCode definitions. |
| `.rocketclaw/` | Generated runtime directory. Setup and startup may create or maintain it. |
| `.femtoclaw/` | Legacy generated runtime directory used only when `femtoclaw.json` is selected. |
| `<runtime-dir>/state.sqlite3` | Persists RocketCode sessions, Slack thread routing, response checkpoints, external MCP sessions, scheduled messages with recurrence metadata, restart notifications, and seed markers. |
| `<runtime-dir>/.rocketcode/` | RocketCode shell output and transient runtime artifacts. |
| `cron/` | Runtime cron definitions. `cron/*.md` loads only at startup. `*.example.md` is ignored. Changes require restart. |
| `main-update-cortex.sh` | Setup-generated helper for updating the Cortex index in `AGENTS.md`. |
| `main-split-markdown-files.sh` | Setup-generated helper for splitting oversized memory/context markdown files. |

### Config Defaults And Normalization

- Empty `openai.stt_model` defaults to `whisper-1`.
- Empty `openai.tts_model` defaults to `tts-1`.
- Empty `openai.tts_voice` defaults to `alloy`.
- Empty logging level defaults to `debug`.
- Empty `minimum_wait_after_human_interaction` means `0s`; setup writes `5m` explicitly.
- Empty or omitted `thread_agents` uses the baseline `:thread:` and `:twisted_rightward_arrows:` routes; a non-empty custom map replaces the baseline.

### Setup And Operation

- `rocketclaw setup` creates or updates setup-controlled files, asks for human partner and agent names, and replaces placeholders in files it creates.
- `rocketclaw doctor` validates the loaded config and RocketCode availability.
- Config selection prefers legacy `femtoclaw.json` when present, selecting `.femtoclaw/`; otherwise `rocketclaw.json` selects `.rocketclaw/`.
- `rocketclaw setup files list` and `setup files get <path>` expose embedded setup payloads.
- ChatGPT auth for RocketCode requires `rocketclaw oai login`; STT/TTS always use API-key auth through audio keys or `api_key` fallback.
- Startup migrates legacy state into `.rocketclaw/state.sqlite3` when applicable; rollback after destructive migration requires backup restore.

## Non-Goals

- This ADR is not a step-by-step installation guide.
- This ADR does not list every Slack or Discord setup screen.
- This ADR does not promise hot reload for configuration, agents, skills, or cron files.

## Evidence

- `README.md`
- `SETUP.md`
- `SLACK_SETUP.md`
- `DISCORD_SETUP.md`
- `internal/config/config.go`
- `internal/skel/skel.go`
- `internal/rocketcodebridge/store.go`
- `internal/cronjob/manager.go`
- `internal/app/app.go`

## Consequences

- Operational behavior changes require this ADR to be updated before implementation.
- Refactors must not silently change persistence, restart requirements, setup outputs, or config defaults.
- New stateful features must declare where their state lives and whether restart is required.

## Changelog

- 2026-05-25: Initial accepted snapshot.
- 2026-05-25: Recorded recurrence metadata as part of scheduled-message persistence.
- 2026-06-02: Added legacy `femtoclaw.json` and `.femtoclaw/` runtime-directory compatibility for upgraded installations.
