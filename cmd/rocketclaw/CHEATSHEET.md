# RocketClaw Cheatsheet

## Text Connector Emoji

| Emoji | Aliases | Surface | What It Does | Notes |
| --- | --- | --- | --- | --- |
| `🔁` | Slack `:repeat:` | Slack | Starts a text goal loop. | Syntax: `🔁 [maxTurns: VALUE|maxTurns:VALUE] [checkScript: VALUE|checkScript:VALUE] OBJECTIVE`. |
| `🏁` | Slack `:checkered_flag:` | Slack | Starts a text goal loop. | Same grammar as `🔁`. |
| `🛑`, `⏹️` | Slack reactions `:octagonal_sign:`, `:stop_button:` | Slack | Stops the active managed-conversation turn. | Works as an exact message or reaction in a managed thread. Stop feedback is marker-only: RocketClaw adds `❗` and sends no stop text. |
| `❗` | Slack `:exclamation:` | Slack | Interruption or rejection marker. | Added by RocketClaw after stop/interruption and for duplicate active-goal rejection. Humans generally do not use this as a command. |
| `✅` | Slack `:white_check_mark:` | Slack | Completion marker. | Added when a goal reaches `complete`. Not added for `blocked`, `stopped`, or `budget_exhausted`. |
| `🔂` | `:repeat_one:`, Slack reaction `repeat_one` | Slack | Runs a one-off cron request by text prefix or reaction. | Examples: `🔂 daily`, `🔂 daily.md`. Reaction reruns inspect the acted-on message and require exactly one deterministic cron target. |
| `🎛` | `:control_knobs:` | Slack managed conversations | Switches the persisted agent for the managed conversation. | `🎛 agent-name` switches to a configured channel agent. Bare `🎛` opens an agent selector usable only by the user who sent the control message. Does not route to RocketCode as prompt input. |
| `🤖` | Slack `:robot_face:` | Slack | Processing/accepted marker. | Added when RocketClaw accepts a Slack-originated or relayed turn; removed after final response delivery. |
| `⏳` | Slack `:hourglass_flowing_sand:` | Slack | Buffered or in-progress marker. | Marks stacked or buffered Slack messages. Removed when processing advances. |
| `📡` | Slack `:satellite_antenna:` | Slack | External MCP relay marker. | Added to Slack relay messages created from External MCP prompts. |

## Slack Channel Scenarios

| Scenario | How To Trigger | Notes |
| --- | --- | --- |
| Start a conversation | Mention the RocketClaw bot/app in a configured channel. | Starts a fresh managed thread using the first agent in that channel's ordered `agents` list. The mention is the first turn. |
| Continue a conversation | Reply in a known managed thread. | Uses only that thread's persisted history. |
| Message with another human mention | Mention RocketClaw too when the message also pings another person, bot, broadcast target, or user group. | Managed-thread replies that ping someone else are suppressed unless RocketClaw is also mentioned. Raw unresolved `@word` text is not treated as a Slack ping. |
| Agent switch | `🎛 agent-name` or bare `🎛` as the whole message. | `agent-name` must be in the channel's configured `agents` list. Bare `🎛` opens a Slack-native selector for that list. |
| One-off cron | `🔂 daily`, `🔂 daily.md`, or a supported `repeat_one` reaction. | Requests and reruns can run cronjobs whose required `channel` matches the acted-on configured channel. |
| External MCP conversation | Call `session_prompt` with an external conversation ID and configured channel. | The ID owns one Slack thread and one history shared by MCP turns and authorized Slack replies. Later calls use the same channel. |

## Goal Examples

| Example | Meaning |
| --- | --- |
| `🏁 ship the release` | Starts a goal loop with default `maxTurns: 5`. |
| `🏁 maxTurns: 10 ship the release` | Starts a finite goal loop with a 10-turn budget. |
| `🏁 maxTurns:10 ship the release` | Same as above; goal parameters may attach values directly after `:`. |
| `🏁 checkScript: ./scripts/check.sh ship the release` | Starts a goal loop that must pass the workspace-local check script before `complete` can stick. |
| `🏁 checkScript:./scripts/check.sh ship the release` | Same as above; `checkScript:` values may attach directly after `:`. |
| `🏁 checkScript: "./scripts/check.sh --full" ship the release` | Uses a quoted simple command for the check script. |
| `🏁 checkScript:"./scripts/check.sh --full" ship the release` | Same as above with the quoted command attached directly after `:`. |
| `🔁 ship the release` | Same goal-loop grammar as `🏁`; `🏁` and `🔁` are equivalent triggers. |
| `🛑` or `⏹️` | Stops the active managed-conversation turn. If an active goal is present, it becomes `stopped`. |
| `✅` | Marker RocketClaw adds when a goal reaches `complete`. Humans generally do not send it as a command. |

| Goal Parameter | Accepted Values | Meaning |
| --- | --- | --- |
| `maxTurns:` | Omitted | Defaults to `5`. |
| `maxTurns:` | Positive integer | Finite goal budget. Progress shows `_Pursuing Goal (n/m)..._`. |
| `maxTurns:` | `0`, `-1`, `infinite` | Infinite goal budget. Progress shows `_Pursuing Goal..._`. |
| `checkScript:` | Workspace-local safe simple command | Runs when the model calls `rocketclaw_update_goal` with `complete`; failure keeps the goal active. |

## Channel Configuration Example

```json
"slack": {
  "bot_token": "xoxb-...",
  "app_token": "xapp-...",
  "channels": [
    {
      "channel": "#ops",
      "agents": ["main", "factory"],
      "allowed_user_ids": ["U0123456789"]
    }
  ]
}
```

New `#ops` conversations use agent `main`. Authorized replies can select `factory` with `🎛 factory` or the native selector.

## State Upgrades

State schema upgrades are one-way. Back up `.rocketclaw/state.sqlite3` before upgrading when rollback may be needed; rollback requires restoring that backup.

## RocketClaw Tools

RocketClaw injects these tools into RocketCode turns. Most are auto-allowed by RocketClaw unless a per-agent permission rule explicitly denies them. `rocketclaw_restart` and `rocketclaw_start_new_thread` are default-deny and require an explicit per-agent `allow`.

| Tool | Available In | Permission Default | What It Does |
| --- | --- | --- | --- |
| `rocketclaw_restart` | Persistent bridge turns and raw/cron runs. | Default-deny. Requires explicit per-agent `allow`; generated `main` agents include that allow. | Records the restart requester and cancels RocketClaw for supervisor restart after approved runtime config or overlay-list changes. |
| `rocketclaw_schedule_message` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Schedules a one-shot or recurring prompt in the current conversation. Recurring schedules persist until reset and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages` | Persistent bridge turns and raw/cron runs. | Treat as part of the schedule-message permission family. Deny `rocketclaw_schedule_message` to block schedule reset behavior. | Clears scheduled messages for the current conversation. |
| `rocketclaw_attach_files_to_response` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Attaches collected files to the final outbound response through RocketClaw's shared response-attachment path. |
| `rocketclaw_update_goal` | Persistent bridge turns only, and only when the current text conversation has an active goal. | Auto-allow unless explicitly denied, but hidden when no active goal exists. | Reports goal status as `progress`, `complete`, or `blocked` with an optional note. `complete` runs any configured `checkScript:` before the goal becomes complete. |
| `rocketclaw_start_new_thread` | Qualifying human-originated managed Slack turns. | Default-deny. Requires explicit per-agent `allow`; missing, `auto`, or `deny` keeps it unavailable. | Creates a fresh managed conversation in the same configured channel and submits the literal tool prompt as its first turn. It is never exposed for cron, MCP, scheduled/system/automation, or automatic goal continuation turns. |
| `rocketclaw_i_want_human_partner_to_see_this` | Raw/cron runs only. | Auto-allow in raw/cron tool mode unless explicitly denied by the cron agent. | Required raw-run completion tool. Its argument is the exact human-visible output, or an empty string for silence. |
| `ask_user_question` | Qualifying human-originated Slack turns with a native answer path. | Auto-allow unless explicitly denied, but hidden when the turn has no answer path. | Asks the originating human through native UI, blocks until answered or canceled, and returns selected options and/or custom text. Not exposed for cron/raw, MCP, scheduled/system/automation, automatic goal continuations, or restart recovery continuations. |

For general `permission` syntax, action values, guardrails, and approval reviewers, see Agent Frontmatter And Permissions below. RocketCode is deny-by-default and later matching rules win. RocketClaw's default tool allows are injected after agent permissions unless an explicit deny already matched, so use `deny` rather than `auto` when the intent is to block or force review of a RocketClaw auto-allowed tool. For default-deny tools such as `rocketclaw_restart` and `rocketclaw_start_new_thread`, `auto` is not enough; use explicit `allow`.

## Emoji Translation Table

| Emoji | Alias or Reaction Name | Meaning |
| --- | --- | --- |
| `🔁` | `:repeat:` | Goal loop prefix. |
| `🏁` | `:checkered_flag:` | Goal loop prefix. |
| `🛑` | `:octagonal_sign:`, `octagonal_sign` | Stop command or reaction. |
| `⏹️` | `:stop_button:`, `stop_button` | Stop command or reaction. |
| `❗` | `:exclamation:`, `exclamation` | Interruption or rejection marker. |
| `✅` | `:white_check_mark:`, `white_check_mark` | Completion marker. |
| `🔂` | `:repeat_one:`, `repeat_one` | Cron one-off request prefix or reaction. |
| `🎛` | `:control_knobs:` | Managed conversation agent switch command. |
| `🤖` | `:robot_face:`, `robot_face` | Slack processing/accepted marker. |
| `⏳` | `:hourglass_flowing_sand:`, `hourglass_flowing_sand` | Slack buffered or in-progress marker. |
| `📡` | `:satellite_antenna:`, `satellite_antenna` | Slack External MCP relay marker. |

## Agent Frontmatter And Permissions

Agents are top-level Markdown files in `agents/*.md`. The filename without `.md` is the agent name. YAML frontmatter is required, followed by the agent prompt body.

```yaml
---
description: Short human-readable purpose
model: gpt-5.5
reasoningEffort: high
verbosity: medium
maxRecursion: 2
guardrail: safety-agent
additionalInstructions: Reply in plain text suitable for Slack.
permission:
  read:
    "README.md": allow
    "docs/**": allow
  edit:
    "docs/**": allow
  bash:
    "go test ./...": allow
    "make lint": auto
  skill:
    "main-*": allow
  task:
    "reviewer": allow
---

Agent instructions go here.
```

Known frontmatter fields:

| Field | Meaning |
| --- | --- |
| `description` | Required short purpose shown when selecting agents. |
| `model` | Required concrete model or `{{ model "name" }}` placeholder configured in `models`. |
| `reasoningEffort` | Optional model reasoning effort. Avoid `xhigh`; `rocketclaw lint` reports it. |
| `verbosity` | Optional model verbosity. |
| `maxRecursion` | Optional task delegation depth for inferences started with this agent. Omitted or `-1` is unlimited, `0` disables `task`, and positive integers allow that many levels. |
| `guardrail` | Optional loaded agent name that gates task delegations to this agent before the child runs and after its response. The guardrail must approve or reject with strict JSON containing `approved` and `reason`; it does not transform the delegated prompt or child response. |
| `additionalInstructions` | Optional RocketClaw normal-reply prompt-header override for this selected agent. Use it for response-format guidance, such as telling an agent to avoid Markdown for Slack, prefer Markdown for technical answers, keep answers terse, or follow another surface-specific style. When omitted, RocketClaw uses `Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.` It does not affect internal notes or raw cron runs. |
| `permission` | Optional singular permission map. Omit it when the agent needs no tools. Do not use plural `permissions`; RocketCode ignores it and `rocketclaw lint` reports it. |

Deployment-specific model names can be kept out of shared agents:

```json
"models": {
  "coding-high": "software-development-sol"
}
```

```yaml
model: '{{ model "coding-high" }}'
```

Changing `models` requires restart. Agent changes using an existing mapping can use reload.

RocketCode denies tools by default. `permission` grants are grouped by bucket, and each bucket maps subjects to actions. Later matching rules override earlier matching rules, so put broad allows before narrower denies when subtracting access.

Permission actions:

| Action | Meaning |
| --- | --- |
| `allow` | Run matching tool calls without automatic review. |
| `deny` | Block matching tool calls. Use it after a broader allow to subtract specific subjects. |
| `auto` | Ask RocketCode's embedded `guardian` automatic reviewer. RocketClaw enables automatic review. Review failure, invalid JSON, or denial blocks the call. |
| `auto(agent-name)` | Ask the named loaded custom reviewer agent. Do not create an agent named `guardian`; that name is reserved. Custom reviewer agents should normally set `reasoningEffort: low` because each automatic permission review has 90 seconds to return a valid decision. |

Guardrails and approval reviewers are separate mechanisms. `guardrail` reviews inter-agent delegation and child responses; `auto` and `auto(agent-name)` review tool execution permission.

Permission buckets:

| Bucket | Subject |
| --- | --- |
| `read` | Workspace-relative file paths for `read`. An `edit` allow also permits reading the same path unless a `read` rule matched first. |
| `glob` | Requested glob patterns for `glob`. |
| `grep` | Requested search patterns for `grep`. |
| `webfetch` | Requested URLs for `webfetch`. |
| `websearch` | Coarse hosted web search toggle, usually `websearch: allow`, `deny`, or `auto`. |
| `edit` | Workspace-relative file paths touched by `apply_patch`. |
| `bash` | Parsed shell command call expressions. Multi-command scripts need every parsed call allowed. |
| `skill` | Skill names visible to `find_skills` and loadable by `skill`. |
| `task` | Subagent names visible and callable through `task`. `maxRecursion` can still hide `task` when the delegation budget is exhausted. |

Run `rocketclaw lint` after agent, skill, or script edits. It checks write-to-execute risk, read-plus-execute leakage, task delegation cycles, delegation-chain escalation, external-content contamination, plural `permissions`, missing guardrails, and excessive `reasoningEffort`.

## Subcommands

Running `rocketclaw` without a subcommand starts the server when `femtoclaw.json` or `rocketclaw.json` is present. If neither config exists, it prints help instead. When both configs exist, `femtoclaw.json` is selected for legacy compatibility.

| Command | Purpose |
| --- | --- |
| `rocketclaw run` | Starts the RocketClaw server/runtime and fails if the selected configuration is missing or invalid. |
| `rocketclaw setup` | Interactively creates or updates `rocketclaw.json`, root setup files, workspace overlays, and `.rocketclaw/`. |
| `rocketclaw setup files list` | Lists embedded setup payload files. |
| `rocketclaw setup files get <path>` | Prints one embedded setup payload file. |
| `rocketclaw doctor` | Validates the loaded configuration and RocketCode availability. |
| `rocketclaw lint [next|current]` | Checks effective RocketCode agent-system safety. Defaults to `next`. |
| `rocketclaw agent-graph [next|current]` | Prints the effective RocketCode task delegation and guardrail graph as Graphviz/DOT. Defaults to `next`. |
| `rocketclaw oai login [--headless]` | Authenticates RocketClaw to ChatGPT for RocketCode model requests and writes the selected runtime `auth.json`. |
| `rocketclaw fc list [--since 24h|RFC3339] [--until RFC3339] [--limit N] [--no-message-preview]` | Lists stored RocketCode sessions. |
| `rocketclaw fc observe [--follow|-f] <conversation-id>` | Prints one conversation's stored session entries as JSONL. The conversation ID is required. |
| `rocketclaw fc delete <conversation-id>` | Deletes a stored session when the daemon does not own the state store. |
| `rocketclaw help`, `rocketclaw -h`, `rocketclaw --help` | Prints top-level help. |

For `lint` and `agent-graph`, `next` builds a temporary startup-equivalent runtime view from embedded assets, overlays, and workspace files. `current` inspects the selected generated runtime directory as it exists now.
