---
title: Agent sudo mode
status: approved, implemented
date: 2026-10-29
---

# Agent sudo mode

## Problem

New features are opt-in through the `permission:` block, often with exact names (the `rocketclaw` bucket refuses wildcards). People don't know a feature exists or is off, so most agents never get it.

## Behavior

One top-level agent frontmatter key:

```yaml
sudo: yes   # true/1/on/yes or false/0/off/no, case-insensitive
```

Parsing notes:
- yaml.v3 reads `yes`/`on` as strings and `1` as an int, so the parser reads the raw scalar text.
- Any other value fails to load.
- `sudo: no` behaves the same as leaving the key out.

With `sudo` on, the agent sees every tool the current turn can offer, and sudo replaces the `permission:` block. Features added later are included automatically.

| Call | With `sudo: yes` |
|---|---|
| `execute` (whole script) | reviewed |
| inside `execute`: `read`, `glob`, `grep`, `edit`, `bash`, `webfetch` | reviewed, each call |
| every MCP tool on every configured server, `opencode_api`, custom tools | reviewed |
| `rocketclaw_dynamic_workflow` (every workflow) | reviewed |
| `rocketclaw_store`, `_read`, `_delete`, `_list`, `_kv_store`, `_kv_read` | reviewed |
| `rocketclaw_sessions_list`, `_get`, `_get_url`, `_current_id`, `_write` (any session; the `children` scope is ignored) | reviewed |
| `rocketclaw_set_tag`, `rocketclaw_get_tags` (only when tag groups are declared) | reviewed |
| `rocketclaw_restart`, `rocketclaw_stop_background_job` | reviewed |
| `skill`, `find_skills`, `task` (every subagent, shown in its list) | **no review** |
| `ask_user_question`, `rocketclaw_update_goal`, `rocketclaw_attach_files_to_response`, `rocketclaw_schedule_message`, `rocketclaw_reset_scheduled_messages`, `rocketclaw_reload` | **no review** (RocketClaw's existing default-on allows) |
| `websearch` | never reviewed (runs on the provider) |
| `load_execute_result` | never gated (unchanged) |

Settings that turn on: background mode (`allow_background`) and `load_agents_md`.

Session tag groups stay at `permission.rocketclaw.rocketclaw_set_tag`. That is the only `permission:` entry allowed next to `sudo`; `rocketclaw lint` reports anything else as `RC011`.

These stay as they are (hard limits, or tools that depend on context):
- `.env` blocking
- `os.Root` workspace confinement
- the bash pre-check on paths outside the workspace
- the repeated-call guard
- `maxRecursion`
- the handoff and workflow-worker tool cuts
- shadows losing tools
- tools that depend on the turn
- `auto` being denied wherever `AutoApprovePermissions` is off (handoff)

Sudo is per agent and does not pass to subagents.

## Decisions (approved 2026-10-29)

- Every call is `auto` (guardian-reviewed), except skills, delegation, and conversation helpers.
- Sudo overrides `permission:`. Lint reports `RC011` for anything except tag groups.
- Session tags and `rocketclaw_sessions_*` are enabled.
- The hard limits stay.
- Goal-check scripts are removed from `$goal` in a separate PR.

## Implementation

- `internal/rocketcode/agents.go`: parse `sudo`. When on, the set is `PermissionSet{Sudo: true}` plus `skill: "*": allow` and `task: "*": allow`, and the authored block is ignored.
- `internal/rocketcode/permission.go`:
  - unmatched subjects return `auto` under sudo (`code_mode_approve` included; `load_agents_md` stays `allow`);
  - `hasRuleForPermission` makes every bucket visible;
  - new `PermissionSet.Enabled` gives exact-allow feature switches (background, restart, workflows, tag prompt) the value "on" under sudo.
- `internal/rocketcode/mcp_tools.go`: every configured MCP server is visible.
- `internal/rocketcode/skills.go`: adds a "Sudo Mode" line to the system prompt.
- `internal/rocketclaw/backend`:
  - tag rules become `auto` under sudo;
  - restart, workflow and tag-prompt checks use `Enabled`;
  - `sessions_write: children` is ignored under sudo.
- `internal/rocketclaw/agentlint`: new `RC011`. `RC003` already fires for a sudo agent without `maxRecursion`, through its `task: "*"` edge to itself.
