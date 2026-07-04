# 0003. Configuration, State, And Operations

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw keeps runtime configuration and first-party workspace assets in explicit workspace files, persists runtime continuity and ChatGPT OAuth state in the selected runtime directory, requires restart for runtime configuration changes, supports validated reload for effective runtime asset rematerialization, and observes effective scheduled cron definition content changes through the scheduled cron runner's global ticker scan without requiring config reread or restart.

## Scope

This ADR governs current configuration files, state files, setup outputs, restart boundaries, and operational expectations.

## Context

RocketClaw is operated by humans and agents in a shared workspace. Its behavior depends on filesystem assets, SQLite state, setup-generated scaffolding, and connector configuration. These operational contracts must remain clear and compact.

## Normative Contracts

| File or directory | Contract |
| --- | --- |
| `rocketclaw.json` | Main runtime config. Relative `workspace` resolves relative to the config file. At least one of Slack or external MCP must be enabled. Slack is the primary text connector. Optional `overlays` entries name git repositories whose `agents/`, `skills/`, `cron/`, and `scripts/` trees are applied during startup. RocketCode model requests use first-party OpenAI Responses through the configured OpenAI RocketCode auth path. |
| `femtoclaw.json` | Legacy runtime config. If present, startup and operational commands load it instead of `rocketclaw.json` and use `.femtoclaw/` as the generated runtime directory. It supports the same optional `overlays` entries as `rocketclaw.json`. |
| `rocketclaw.users.json` | Optional external MCP Basic Auth users next to `rocketclaw.json`. If present, it must be a JSON object and file mode `0600`. Missing means MCP runs without auth. |
| `AGENTS.md` | Workspace instruction file generated when missing. Loaded literally; no shell interpolation. |
| `agents/`, `skills/`, `scripts/` | User-overridable workspace overlays for agent, skill, and script assets. Agent files may declare `guardrail: <agent-name>` to reference another loaded agent as a per-target task guardrail. `rocketclaw_reload` can rematerialize these runtime assets from the already-loaded configuration and fresh content for already-loaded remote overlays after staged validation succeeds; restart remains valid when a full process restart is desired. Local workspace overlays are applied from disk after embedded assets and configured git overlays. Startup and successful reload expose effective runtime script files from `<runtime-dir>/scripts/` as symlinks under workspace `scripts/`, preserving existing regular workspace script files. Startup and successful reload remove existing generated script symlinks before scanning local workspace overlays so stale runtime outputs are never treated as local overlay sources. When `<workspace>/.git` is a directory, startup and successful reload maintain a managed block in `.git/info/exclude` for the generated RocketClaw workspace script symlinks so they do not pollute git status. |
| `.rocketclaw/` | Generated runtime directory. Setup and startup may create or maintain it. |
| `.femtoclaw/` | Legacy generated runtime directory used only when `femtoclaw.json` is selected. |
| `<runtime-dir>/overlays/` | Managed parent directory for configured git overlay clones. Startup preserves the parent directory, reconciles its children against the current `overlays` config entries, removes unconfigured clone directories, and discards uncommitted or untracked changes inside active configured clone directories before fetching and applying them. |
| `<runtime-dir>/state.sqlite3` | Persists RocketCode sessions including text connector thread routing, response checkpoints, external MCP sessions, scheduled messages with recurrence metadata, scheduled cron per-file execution state, text connector goal-loop state, restart notifications, tool-created conversation routing/session state, source conversation ID, seed marker when source seed state exists, first submitted prompt, and other seed markers. Opened and initialized through the centralized SQLite state-store opener defined by ADR 0005. Tool-created conversation seed state is recorded before the first prompt. |
| `<runtime-dir>/auth.json` | Workspace-local ChatGPT OAuth credential for RocketCode Codex requests. Written by `rocketclaw oai login` with `0600` permissions. It is runtime state, not setup payload. RocketClaw owns this credential file and must not read, import, or write Codex CLI credentials such as `~/.codex/auth.json`. |
| `<runtime-dir>/.gitignore` | Setup-generated runtime-directory ignore file that ignores `auth.json` so workspace-local ChatGPT OAuth material is not accidentally added to source control. |
| `<runtime-dir>/.rocketcode/` | RocketCode shell output and transient runtime artifacts. |
| `cron/` | User-overridable workspace cron definitions. Effective `cron/*.md` definitions load at startup from the merged runtime view. `rocketclaw_reload` can rematerialize effective cron definitions from the already-loaded configuration after staged cron validation succeeds. Scheduled cron definition content changes under the effective `<runtime-dir>/cron/` directory are observed by the scheduled cron runner's global ticker scan at the next scheduled decision point without rereading runtime configuration or restarting. Runtime configuration and overlay-list changes still require restart. `*.example.md` is ignored. Local one-off cron files can be deleted after a run attempt; one-off cron definitions supplied only by a git overlay may reappear on restart until removed from the source repository. |
| `main-update-cortex.sh` | Setup-generated helper for updating the Cortex index in `AGENTS.md`. |

### Config Defaults And Normalization

- Empty or omitted `openai.api_key` is valid unless an enabled path requires first-party OpenAI API-key credentials, such as a selected first-party OpenAI API-key request path.
- There is no top-level `openai_compatible` runtime configuration contract.
- Empty logging level defaults to `debug`.
- Empty or omitted `thread_agents` uses the baseline `:thread:` and `:twisted_rightward_arrows:` routes; a non-empty custom map replaces the baseline.
- Empty or omitted `overlays` means no intermediate git overlays. Non-empty entries are applied in array order after embedded assets and before local workspace overlays.
- Both persistent bridge and raw-run construction paths enable RocketCode automatic permission review unconditionally for `auto` permission rules.
- The enabled primary text connector may define social-mode channel mappings. The canonical mapping shape is one connector channel, one ordered non-empty `agents` list, and non-empty `allowed_user_ids`.
- Slack binding: canonical `slack.social_mode.channels[]` is the Slack social-mode channel mapping loaded by config and used by runtime connector behavior.

### Git Overlays

- Overlay entries may use shorthand `github.com/org/repo`, shorthand with a ref suffix like `github.com/org/repo@main` or `github.com/org/repo@<commit>`, or explicit git clone URLs copied from GitHub such as HTTPS, SSH, or SCP-like `git@github.com:org/repo.git`.
- Private GitHub overlays should use an explicit authenticated clone URL, usually the copied SSH form with an optional ref suffix such as `git@github.com:Rocketable/alitu-cs.git@main`.
- Omitted refs use the remote default branch HEAD. Explicit refs select that branch, tag, or commit.
- Startup fetches overlays with the `git` command-line client, materializes only `agents/`, `skills/`, `cron/`, and `scripts/`, and fails startup when a configured overlay cannot be fetched or applied.
- Startup stores configured overlay clones under `<runtime-dir>/overlays/<human-readable-slug>/`. Slugs are human-readable and may collide; the `overlays` config order is the only application order, and filesystem listing order or clone directory names never determine merge order.
- Startup reconciles `<runtime-dir>/overlays/` against the current config before applying overlays: unconfigured child directories are removed, active configured clone directories are force-cleaned, uncommitted and untracked changes are discarded, and the configured ref is fetched and checked out/reset.
- Effective runtime assets are built in this order: embedded RocketClaw assets, configured git overlays in config order, then local workspace `agents/`, `skills/`, `cron/`, and `scripts/`.
- Configured git overlays may materialize `agents/guardrail.md`; that file is a normal agent named `guardrail` and has no path-based special behavior.
- Runtime asset files copied from configured git overlays and local workspace overlays preserve the source executable bit: executable source files materialize as `0755` and non-executable source files materialize as `0644`. File extensions do not make overlay files executable.
- Embedded setup files are seeded separately from overlays; embedded `.sh` setup files materialize as executable setup helpers.
- Before effective runtime assets are built, startup and successful reload remove workspace `scripts/` symlinks that resolve into `.rocketclaw/` or `.femtoclaw/` so stale generated links cannot be copied back as local overlay input. After effective runtime assets are built, startup and successful reload recreate symlinks for files from the selected `<runtime-dir>/scripts/`. Regular workspace script files and symlinks to other locations are preserved.
- When `<workspace>/.git` is a directory, startup and successful reload update only `.git/info/exclude` with a managed block for the generated RocketClaw workspace script symlink paths. Startup and successful reload preserve unrelated exclude content, replace any previous RocketClaw managed block, remove the block when there are no generated script symlinks, and do not touch `scripts/.gitignore` or workspace `.gitignore`. Non-git workspaces and git worktrees whose `.git` is not a directory are ignored by this behavior.
- Git overlay-list changes require restart. Content changes in already-loaded overlay repositories can be fetched and applied by `rocketclaw_reload` after the overlay source commits are available to fetch.
- `rocketclaw_reload` uses the already-loaded runtime configuration and overlay list, and fetches or re-clones fresh remote content for those already-loaded overlay entries. It does not reread `rocketclaw.json` or `femtoclaw.json`, discover added, removed, reordered, or changed overlay config entries, or apply changed config values.
- RocketCode runtime prompts include an overlay section when configured overlays are active. The section explains overlays, enumerates configured overlays in application order with original spec, normalized git URL, ref, and clone path, and instructs agents to update overlay clone paths, commit and push overlay changes before reload or restart, and treat generated effective runtime files as non-source-of-truth outputs.

### Setup And Operation

- `rocketclaw setup` creates or updates setup-controlled files, asks for human partner and agent names, and replaces placeholders in files it creates. Setup examples and generated documentation must describe first-party OpenAI Responses RocketCode deployments and the applicable OpenAI API-key and ChatGPT OAuth credential requirements.
- `rocketclaw setup` asks whether to configure Slack as the primary text connector.
- `rocketclaw doctor` validates the loaded config and RocketCode availability.
- `rocketclaw lint [next|current]` checks agent-system safety for the selected config and runtime directory as specified by ADR 0006.
- Config selection prefers legacy `femtoclaw.json` when present, selecting `.femtoclaw/`; otherwise `rocketclaw.json` selects `.rocketclaw/`.
- `rocketclaw setup files list` and `setup files get <path>` expose embedded setup payloads.
- `rocketclaw fc list` is a read-only operational command for stored RocketCode session summaries. It supports optional bounded inspection flags `--since`, `--until`, and `--limit`, and optional output flag `--no-message-preview`. `--since` accepts either a duration relative to command execution time, such as `24h`, or an RFC3339/RFC3339Nano timestamp; `--until` accepts an RFC3339/RFC3339Nano timestamp. The selected time range is based on each session's latest stored entry timestamp, includes sessions with `LastUpdated >= since`, and excludes sessions with `LastUpdated >= until`. `--limit N` selects the `N` most recently updated sessions, with `0` meaning no limit. Without `--no-message-preview`, output includes the last user and assistant message preview columns; with it, output includes only conversation ID, turn count, and last update time.
- ChatGPT auth is required only for selected first-party OpenAI RocketCode paths that use ChatGPT OAuth. ChatGPT refresh tokens are rotating, single-owner credentials and must remain under RocketClaw's selected `<runtime-dir>/auth.json` ownership.
- ChatGPT-backed RocketCode requests refresh credentials before sending when the access token is locally expired or within 120s of expiry. When Codex returns `401 Unauthorized` for a replayable request, RocketClaw reloads stored auth and retries once with a newer same-account stored token when present; otherwise it force-refreshes with the refresh token, persists the result, and retries once. Non-replayable requests return the original `401`; repeated `401`, terminal refresh failure, or failed refresh is surfaced with re-login guidance.
- RocketCode requests use the configured first-party OpenAI RocketCode auth path. OpenAI-compatible provider configuration is not used.
- Startup migrates legacy state into `.rocketclaw/state.sqlite3` when applicable; rollback after destructive migration requires backup restore.
- Startup rehydrates active persisted text connector goal loops according to ADR 0007. This is runtime state recovery and does not require configuration hot reload.

## Non-Goals

- This ADR is not a step-by-step installation guide.
- This ADR does not list every Slack setup screen.
- This ADR does not promise hot reload for runtime configuration. Configuration and overlay-list changes still require restart. Runtime asset rematerialization is limited to validated `rocketclaw_reload` from the already-loaded configuration. Scheduled cron content changes under the effective runtime directory are governed by the scheduled cron global-ticker scan contract above.

## Evidence

- `README.md`
- `SETUP.md`
- `SLACK_SETUP.md`
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
- 2026-06-11: Added optional Anthropic RocketCode provider configuration and clarified that ChatGPT OAuth remains OpenAI-only.
- 2026-06-11: Added Slack goal-loop state to `<runtime-dir>/state.sqlite3` and specified startup rehydration per ADR 0007.
- 2026-06-11: Added canonical Slack social-mode `channels[]` config with per-channel allowed users and legacy `channel_agents` compatibility.
- 2026-06-11: Replaced live Slack social-mode `channel_agents` compatibility with startup migration into canonical `channels[]` only.
- 2026-06-11: Made top-level Slack social-mode `allowed_user_ids` migration input only; startup migration copies it into channel allowlists before runtime decoding.
- 2026-06-12: Added bounded read-only `rocketclaw fc list` inspection flags `--since`, `--until`, `--limit`, and `--no-message-preview`.
- 2026-06-12: Specified `.git/info/exclude` maintenance for generated RocketClaw workspace script symlinks when the workspace has a directory `.git` repository.
- 2026-06-12: Replaced path-special `agents/guardrail.md` with normal agent files referenced by per-agent `guardrail` frontmatter.
- 2026-06-14: Defined social-mode channel mappings as a generic primary text connector config shape with Slack and Discord Text bindings.
- 2026-06-16: Specified that startup removes generated workspace script symlinks before local workspace overlays are scanned, then recreates current runtime script symlinks after effective runtime assets are built.
- 2026-06-16: Updated social-mode channel config migration to emit canonical `agents` lists and normalize legacy scalar channel `agent` entries.
- 2026-06-16: Ended temporary config migration compatibility for legacy social-mode channel mappings; config loading now requires canonical `channels[].agents` and per-channel `allowed_user_ids`.
- 2026-06-17: Added optional `rocketcode.auto_approve_permissions` runtime config for enabling RocketCode automatic permission review in persistent and raw-run paths.
- 2026-06-18: Added `rocketclaw cli` operational contract, state-store lock behavior, and terminal private-session persistence.
- 2026-06-19: Added server-owned Unix control socket operations, `rocketclaw cli --attach`, and lock-aware socket attach/fallback semantics.
- 2026-06-19: Added control-socket operational contract for terminal-originated `ask_user_question` question and answer messages.
- 2026-06-19: Added cmux `/new [agent]` operational expectations for caller-context terminal surface creation.
- 2026-06-22: Added top-level `openai_compatible` runtime configuration for named OpenAI-compatible RocketCode providers.
- 2026-06-23: Made first-party OpenAI credentials conditional and documented compatible-only RocketCode text deployment expectations.
- 2026-06-24: Removed the optional `rocketcode.auto_approve_permissions` runtime config knob and specified that RocketClaw always enables RocketCode automatic permission review in persistent bridge and raw-run paths.
- 2026-06-25: Added persistence and control-socket operational contracts for tool-created `rocketclaw_start_new_thread` conversations, including matching-client cmux-open requests and attach-command fallback.
- 2026-06-25: Specified tool-created conversation persistence ordering: inherited source-context seed state is recorded before the first submitted prompt, and cmux-open requests are sent only after private conversation creation.
- 2026-06-25: Corrected the canonical social-mode config shape to the ordered `agents` list used by current routing and tool target-agent constraints.
- 2026-06-26: Removed Anthropic and OpenAI-compatible chat-completions configuration support; OpenAI-compatible providers always use the Responses API.
- 2026-06-26: Removed remaining OpenAI-compatible Responses configuration support; RocketClaw RocketCode requests now use first-party OpenAI Responses through the configured OpenAI RocketCode auth path only.
- 2026-07-01: Removed Discord Text, Discord voice, browser voice, and OpenAI-backed audio runtime configuration contracts.
- 2026-07-01: Removed terminal CLI, control-socket, terminal private-session, terminal question, and cmux operational contracts.
- 2026-07-02: Removed `minimum_wait_after_human_interaction` from runtime configuration defaults and setup-generated config.
- 2026-07-04: Added global-ticker scanning of effective scheduled cron definition content changes without config reread or restart, while preserving restart for config and overlay-list changes, and added scheduled cron execution state to the SQLite state store contract.
- 2026-07-04: Added validated `rocketclaw_reload` for effective runtime asset rematerialization from already-loaded configuration, while preserving restart requirements for configuration and overlay-list changes.
