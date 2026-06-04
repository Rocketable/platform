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
| `rocketclaw.json` | Main runtime config. Relative `workspace` resolves relative to the config file. At least one of Discord voice, Discord text, Slack, external MCP, or web UI must be enabled. Slack and Discord text are mutually exclusive primary text connectors. Optional `overlays` entries name git repositories whose `agents/`, `skills/`, `cron/`, and `scripts/` trees are applied during startup. |
| `femtoclaw.json` | Legacy runtime config. If present, startup and operational commands load it instead of `rocketclaw.json` and use `.femtoclaw/` as the generated runtime directory. It supports the same optional `overlays` entries as `rocketclaw.json`. |
| `rocketclaw.users.json` | Optional external MCP Basic Auth users next to `rocketclaw.json`. If present, it must be a JSON object and file mode `0600`. Missing means MCP runs without auth. |
| `AGENTS.md` | Workspace instruction file generated when missing. Loaded literally; no shell interpolation. |
| `agents/`, `skills/`, `scripts/` | User-overridable workspace overlays for agent, skill, and script assets. Changes require restart to affect running RocketCode definitions. Local workspace overlays are applied after embedded assets and configured git overlays. Startup exposes effective runtime script files from `<runtime-dir>/scripts/` as symlinks under workspace `scripts/`, preserving existing regular workspace script files. |
| `.rocketclaw/` | Generated runtime directory. Setup and startup may create or maintain it. |
| `.femtoclaw/` | Legacy generated runtime directory used only when `femtoclaw.json` is selected. |
| `<runtime-dir>/state.sqlite3` | Persists RocketCode sessions, Slack/Discord text thread routing, response checkpoints, external MCP sessions, scheduled messages with recurrence metadata, restart notifications, and seed markers. |
| `<runtime-dir>/.rocketcode/` | RocketCode shell output and transient runtime artifacts. |
| `cron/` | User-overridable workspace cron definitions. Effective `cron/*.md` definitions load only at startup from the merged runtime view. `*.example.md` is ignored. Changes require restart. Local one-off cron files can be deleted after a run attempt; one-off cron definitions supplied only by a git overlay may reappear on restart until removed from the source repository. |
| `main-update-cortex.sh` | Setup-generated helper for updating the Cortex index in `AGENTS.md`. |
| `main-split-markdown-files.sh` | Setup-generated helper for splitting oversized memory/context markdown files. |

### Config Defaults And Normalization

- Empty `openai.stt_model` defaults to `whisper-1`.
- Empty `openai.tts_model` defaults to `tts-1`.
- Empty `openai.tts_voice` defaults to `alloy`.
- Empty logging level defaults to `debug`.
- Empty `minimum_wait_after_human_interaction` means `0s`; setup writes `5m` explicitly.
- Empty or omitted `thread_agents` uses the baseline `:thread:` and `:twisted_rightward_arrows:` routes; a non-empty custom map replaces the baseline.
- Empty or omitted `overlays` means no intermediate git overlays. Non-empty entries are applied in array order after embedded assets and before local workspace overlays.
- `discord_text.enabled` requires `discord_text.token`, `discord_text.channel_id`, and `discord_text.human_user_id`.
- `slack.enabled` and `discord_text.enabled` must not both be true.

### Git Overlays

- Overlay entries may use shorthand `github.com/org/repo`, shorthand with a ref suffix like `github.com/org/repo@main` or `github.com/org/repo@<commit>`, or explicit git clone URLs copied from GitHub such as HTTPS, SSH, or SCP-like `git@github.com:org/repo.git`.
- Private GitHub overlays should use an explicit authenticated clone URL, usually the copied SSH form with an optional ref suffix such as `git@github.com:Rocketable/alitu-cs.git@main`.
- Omitted refs use the remote default branch HEAD. Explicit refs select that branch, tag, or commit.
- Startup fetches overlays with the `git` command-line client, materializes only `agents/`, `skills/`, `cron/`, and `scripts/`, and fails startup when a configured overlay cannot be fetched or applied.
- Effective runtime assets are built in this order: embedded RocketClaw assets, configured git overlays in config order, then local workspace `agents/`, `skills/`, `cron/`, and `scripts/`.
- Runtime asset files copied from configured git overlays and local workspace overlays preserve the source executable bit: executable source files materialize as `0755` and non-executable source files materialize as `0644`. File extensions do not make overlay files executable.
- Embedded setup files are seeded separately from overlays; embedded `.sh` setup files materialize as executable setup helpers.
- After effective runtime assets are built, startup removes workspace `scripts/` symlinks that resolve into `.rocketclaw/` or `.femtoclaw/`, then recreates symlinks for files from the selected `<runtime-dir>/scripts/`. Regular workspace script files and symlinks to other locations are preserved.
- Git overlay changes require restart; RocketClaw does not hot-reload overlay repositories.

### Setup And Operation

- `rocketclaw setup` creates or updates setup-controlled files, asks for human partner and agent names, and replaces placeholders in files it creates.
- `rocketclaw setup` asks for one primary text connector: Slack, Discord text, or none. Discord text setup targets a guild text channel so managed thread semantics are available.
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
- 2026-06-02: Added Discord text configuration as the mutually exclusive Slack alternative primary text connector.
- 2026-06-04: Added config-driven git overlays for intermediate `agents/`, `skills/`, `cron/`, and `scripts/` runtime assets.
- 2026-06-04: Exposed effective runtime scripts as workspace `scripts/` symlinks while preserving regular workspace script files.
- 2026-06-04: Clarified that private GitHub overlays should use explicit clone URLs when authentication matters.
- 2026-06-04: Specified executable-bit preservation for configured git overlays and local workspace overlays.
- 2026-06-04: Recorded that embedded `.sh` setup files are seeded as executable setup helpers outside the overlay executable-bit contract.
