# 0003. Configuration, State, And Operations

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw keeps runtime configuration and first-party workspace assets in explicit workspace files, persists runtime continuity and ChatGPT OAuth state in the selected runtime directory, and requires restart for configuration/asset changes that affect RocketCode or cron discovery.

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
| `agents/guardrail.md` | Optional local-only inter-agent guardrail agent. When present in the local workspace overlay, it activates RocketClaw's RocketCode inter-agent filter. Configured git overlays and embedded setup payloads must not provide or override this file. Changes require restart to affect running RocketCode definitions. |
| `.rocketclaw/` | Generated runtime directory. Setup and startup may create or maintain it. |
| `.femtoclaw/` | Legacy generated runtime directory used only when `femtoclaw.json` is selected. |
| `<runtime-dir>/overlays/` | Managed parent directory for configured git overlay clones. Startup preserves the parent directory, reconciles its children against the current `overlays` config entries, removes unconfigured clone directories, and discards uncommitted or untracked changes inside active configured clone directories before fetching and applying them. |
| `<runtime-dir>/state.sqlite3` | Persists RocketCode sessions, Slack/Discord text thread routing, response checkpoints, external MCP sessions, scheduled messages with recurrence metadata, restart notifications, and seed markers. Opened and initialized through the centralized SQLite state-store opener defined by ADR 0005. |
| `<runtime-dir>/auth.json` | Workspace-local ChatGPT OAuth credential for RocketCode Codex requests. Written by `rocketclaw oai login` with `0600` permissions. It is runtime state, not setup payload, and STT/TTS do not read it. RocketClaw owns this credential file and must not read, import, or write Codex CLI credentials such as `~/.codex/auth.json`. |
| `<runtime-dir>/.gitignore` | Setup-generated runtime-directory ignore file that ignores `auth.json` so workspace-local ChatGPT OAuth material is not accidentally added to source control. |
| `<runtime-dir>/.rocketcode/` | RocketCode shell output and transient runtime artifacts. |
| `cron/` | User-overridable workspace cron definitions. Effective `cron/*.md` definitions load only at startup from the merged runtime view. `*.example.md` is ignored. Changes require restart. Local one-off cron files can be deleted after a run attempt; one-off cron definitions supplied only by a git overlay may reappear on restart until removed from the source repository. |
| `main-update-cortex.sh` | Setup-generated helper for updating the Cortex index in `AGENTS.md`. |

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
- Startup stores configured overlay clones under `<runtime-dir>/overlays/<human-readable-slug>/`. Slugs are human-readable and may collide; the `overlays` config order is the only application order, and filesystem listing order or clone directory names never determine merge order.
- Startup reconciles `<runtime-dir>/overlays/` against the current config before applying overlays: unconfigured child directories are removed, active configured clone directories are force-cleaned, uncommitted and untracked changes are discarded, and the configured ref is fetched and checked out/reset.
- Effective runtime assets are built in this order: embedded RocketClaw assets, configured git overlays in config order, then local workspace `agents/`, `skills/`, `cron/`, and `scripts/`.
- Configured git overlays may not materialize `agents/guardrail.md`; that path is reserved for the local workspace overlay only. A configured git overlay containing `agents/guardrail.md` is applied as if that single file were absent.
- Runtime asset files copied from configured git overlays and local workspace overlays preserve the source executable bit: executable source files materialize as `0755` and non-executable source files materialize as `0644`. File extensions do not make overlay files executable.
- Embedded setup files are seeded separately from overlays; embedded `.sh` setup files materialize as executable setup helpers.
- After effective runtime assets are built, startup removes workspace `scripts/` symlinks that resolve into `.rocketclaw/` or `.femtoclaw/`, then recreates symlinks for files from the selected `<runtime-dir>/scripts/`. Regular workspace script files and symlinks to other locations are preserved.
- Git overlay changes require restart; RocketClaw does not hot-reload overlay repositories.
- RocketCode runtime prompts include an overlay section when configured overlays are active. The section explains overlays, enumerates configured overlays in application order with original spec, normalized git URL, ref, and clone path, and instructs agents to update overlay clone paths, commit and push overlay changes before restart, and treat generated effective runtime files as non-source-of-truth outputs.

### Setup And Operation

- `rocketclaw setup` creates or updates setup-controlled files, asks for human partner and agent names, and replaces placeholders in files it creates.
- `rocketclaw setup` asks for one primary text connector: Slack, Discord text, or none. Discord text setup targets a guild text channel so managed thread semantics are available.
- `rocketclaw doctor` validates the loaded config and RocketCode availability.
- `rocketclaw lint [next|current]` checks agent-system safety for the selected config and runtime directory as specified by ADR 0006.
- Config selection prefers legacy `femtoclaw.json` when present, selecting `.femtoclaw/`; otherwise `rocketclaw.json` selects `.rocketclaw/`.
- `rocketclaw setup files list` and `setup files get <path>` expose embedded setup payloads.
- ChatGPT auth for RocketCode requires `rocketclaw oai login`; STT/TTS always use API-key auth through audio keys or `api_key` fallback. ChatGPT refresh tokens are rotating, single-owner credentials and must remain under RocketClaw's selected `<runtime-dir>/auth.json` ownership.
- ChatGPT-backed RocketCode requests refresh credentials before sending when the access token is locally expired or within 120s of expiry. When Codex returns `401 Unauthorized` for a replayable request, RocketClaw reloads stored auth and retries once with a newer same-account stored token when present; otherwise it force-refreshes with the refresh token, persists the result, and retries once. Non-replayable requests return the original `401`; repeated `401`, terminal refresh failure, or failed refresh is surfaced with re-login guidance.
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
- `internal/rocketclaw/oai/oauth.go`
- `internal/rocketclaw/skel/skel.go`
- `internal/skel/skel.go`
- `internal/rocketclaw/harnessbridge/store.go`
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
- 2026-06-05: Linked `<runtime-dir>/state.sqlite3` operations to the centralized SQLite state-store opener in ADR 0005.
- 2026-06-06: Documented workspace-local ChatGPT OAuth state, runtime ignore protection for `auth.json`, and Codex-style `401` auth recovery.
- 2026-06-06: Specified RocketClaw-owned ChatGPT OAuth credentials, no Codex CLI auth-file sharing, rotating refresh-token ownership, terminal refresh re-login guidance, and 120s access-token refresh skew.
- 2026-06-07: Added `graceful_shutdown_timeout` to runtime config, shared by the restart and signal-triggered shutdown sequence, defaulting to the existing `5m` drain budget.
- 2026-06-08: Specified managed persistent configured overlay clones under `<runtime-dir>/overlays/`, startup reconciliation and force-clean behavior for active and removed overlay clones, config-order-only overlay application, and RocketCode prompt disclosure of active overlay sources and update instructions.
- 2026-06-09: Removed `graceful_shutdown_timeout` from runtime config.
- 2026-06-10: Removed `main-split-markdown-files.sh` from the setup-generated helper contract.
- 2026-06-10: Added local-only `agents/guardrail.md` as the optional inter-agent guardrail source and prohibited configured git overlays from materializing that path.
- 2026-06-11: Added `rocketclaw lint [next|current]` as an operational command governed by ADR 0006.
