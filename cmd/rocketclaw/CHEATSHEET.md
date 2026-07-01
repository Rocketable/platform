# RocketClaw Cheatsheet

## Text Connector Emoji

| Emoji | Aliases | Surface | What It Does | Notes |
| --- | --- | --- | --- | --- |
| `🧵` | `:thread:` | Slack, Discord Text | Starts a managed conversation using the configured `thread_agents` entry. | Default config maps `🧵` to agent `main` without pre-seeding. Leading known aliases are normalized before matching. |
| `🔀` | `:twisted_rightward_arrows:` | Slack, Discord Text | Starts a managed conversation using the configured `thread_agents` entry. | Default config maps this to agent `main` with `pre_seed: true`. |
| Configured `thread_agents` emoji or prefix | Config-specific | Slack, Discord Text | Starts a managed conversation with the configured agent and pre-seed behavior. | Prefixes may be aliases or Unicode emoji. Unknown aliases remain literal, so custom aliases can still be configured. |
| `🔁` | Slack `:repeat:` | Slack, Discord Text | Starts a text goal loop. | Syntax: `🔁 [maxTurns: VALUE|maxTurns:VALUE] [checkScript: VALUE|checkScript:VALUE] OBJECTIVE`. |
| `🏁` | Slack `:checkered_flag:` | Slack, Discord Text | Starts a text goal loop. | Same grammar as `🔁`. |
| `🛑`, `⏹️` | Slack reactions `:octagonal_sign:`, `:stop_button:` | Slack, Discord Text | Stops the active main or managed-conversation turn. | Works as an exact message. Also works as a stop reaction on supported managed/main turn surfaces. Stop feedback is marker-only: RocketClaw adds `❗` and sends no stop text. Discord uses the Unicode emoji. |
| `❗` | Slack `:exclamation:` | Slack, Discord Text | Interruption or rejection marker. | Added by RocketClaw after stop/interruption and for duplicate active-goal rejection. Humans generally do not use this as a command. |
| `✅` | Slack `:white_check_mark:` | Slack, Discord Text | Completion marker. | Added when a goal reaches `complete`; Slack also uses it for successful summary completion. Not added for `blocked`, `stopped`, or `budget_exhausted`. |
| `💾` | Slack `:floppy_disk:` | Slack, Discord Text | Summarizes a managed conversation back to main. | Requires the configured/authorized human or social-mode allowed user. |
| `🔂` | `:repeat_one:`, Slack reaction `repeat_one`, Discord `repeat_one` | Slack, Discord Text | Runs a one-off cron request by text prefix or reaction. | Examples: `🔂 daily`, `🔂 daily.md`. Reaction reruns inspect the acted-on message and require exactly one deterministic cron target. |
| `🎛` | `:control_knobs:` | Slack, Discord Text social-mode managed conversations | Switches the persisted agent for the managed conversation. | `🎛 agent-name` switches to a configured channel agent. Bare `🎛` opens an agent selector usable only by the user who sent the control message. Does not route to RocketCode as prompt input. |
| `🤖` | Slack `:robot_face:` | Slack | Processing/accepted marker. | Added when RocketClaw accepts a Slack-originated or relayed turn; removed after final response delivery. |
| `⏳` | Slack `:hourglass_flowing_sand:` | Slack | Buffered or in-progress marker. | Marks stacked/buffered Slack messages and summary-in-progress state. Removed when processing advances or summary finishes. |
| `📲` | Slack `:calling:` | Slack | Discord voice relay marker. | Added to Slack relay messages created from Discord voice input. |
| `🎙️` | Slack `:studio_microphone:` | Slack | Browser voice relay marker. | Added to Slack relay messages created from browser voice input. |
| `📡` | Slack `:satellite_antenna:` | Slack | External MCP relay marker. | Added to Slack relay messages created from External MCP prompts. |

## DM And Social Mode Scenarios

| Scenario | How To Trigger | Notes |
| --- | --- | --- |
| DM thread | `🧵 prompt` in a configured-human DM. | Starts a managed DM conversation using the default `main` agent unless `thread_agents` changes the prefix mapping. |
| DM thread with seed | `🔀 prompt` in a configured-human DM. | Starts a managed DM conversation with `pre_seed: true`, seeding from existing main context when available. |
| DM thread from a DM response | Reply in a supported response-rooted thread under a RocketClaw DM response. | Response-rooted text conversations stay isolated from `main` until summarized. Slack supports response-rooted Slack threads; Discord DMs do not provide guild-thread mechanics. |
| Custom emoji for special DM threads | Configure a `thread_agents` prefix such as `🏭` or `:factory:`. | A non-empty `thread_agents` map replaces the defaults, so include `🧵` and `🔀` mappings if you still want them. |
| Internalize thread | `💾` in a managed or response-rooted conversation. | Summarizes the conversation back to `main` as an internalized note. RocketClaw does not send a normal visible answer for the internalized note itself. |
| Social Mode start | Mention the RocketClaw bot/app in a configured social-mode channel. | New social-mode conversations use the first agent in that channel's configured `agents` list. Literal `@AgentName` is not an agent-selection command. |
| Social Mode with another human mention | Mention RocketClaw too when the message also pings another person, bot, broadcast target, or user group. | Slack social-mode thread replies that ping someone else are suppressed unless RocketClaw is also mentioned. Raw unresolved `@word` text is not treated as a Slack ping. |
| Social Mode internalize thread | `💾` from an authorized user in the managed conversation. | Uses the same summary/internalize behavior as DM managed conversations. |
| Social Mode agent switch | `🎛 agent-name` or bare `🎛` as the whole message. | `agent-name` must be in the channel's configured `agents` list. Bare `🎛` opens a connector-native selector for that list. |
| One-off cron | `🔂 daily`, `🔂 daily.md`, or a supported `repeat_one` reaction. | DM one-off cron can run any top-level cron. Channel requests and reruns are restricted to cronjobs targeting that connector channel. |

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
| `🛑` or `⏹️` | Stops the active main or managed-conversation turn. If an active goal is present, it becomes `stopped`. |
| `✅` | Marker RocketClaw adds when a goal reaches `complete`. Humans generally do not send it as a command. |

| Goal Parameter | Accepted Values | Meaning |
| --- | --- | --- |
| `maxTurns:` | Omitted | Defaults to `5`. |
| `maxTurns:` | Positive integer | Finite goal budget. Progress shows `_Pursuing Goal (n/m)..._`. |
| `maxTurns:` | `0`, `-1`, `infinite` | Infinite goal budget. Progress shows `_Pursuing Goal..._`. |
| `checkScript:` | Workspace-local safe simple command | Runs when the model calls `rocketclaw_update_goal` with `complete`; failure keeps the goal active. |

## `thread_agents` Examples

Alias prefix mapped to a custom agent:

```json
"thread_agents": {
  ":thread:": {
    "agent": "main",
    "pre_seed": false
  },
  ":factory:": {
    "agent": "factory",
    "pre_seed": false
  }
}
```

Result: `🏭 build the release` starts a managed conversation with agent `factory` and prompt `build the release`.

Unicode emoji prefix mapped to a custom agent:

```json
"thread_agents": {
  "🏭": {
    "agent": "factory",
    "pre_seed": false
  }
}
```

Result: `🏭 build the release` starts a managed conversation with agent `factory` and prompt `build the release`.

Pre-seeded default thread:

```json
"thread_agents": {
  ":twisted_rightward_arrows:": {
    "agent": "main",
    "pre_seed": true
  }
}
```

Result: `🔀 continue from main context` starts a managed conversation with agent `main` and pre-seeds it from existing context.

## RocketClaw Tools

RocketClaw injects these tools into RocketCode turns. Most are auto-allowed by RocketClaw unless a per-agent permission rule explicitly denies them. `rocketclaw_start_new_thread` is the main exception: it is default-deny and requires an explicit per-agent `allow`.

| Tool | Available In | Permission Default | What It Does |
| --- | --- | --- | --- |
| `rocketclaw_restart` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Schedules a graceful RocketClaw restart after approved runtime config, agent, skill, cron, script, or overlay changes. |
| `rocketclaw_schedule_message` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Schedules a one-shot or recurring prompt in the current conversation. Recurring schedules persist until reset and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages` | Persistent bridge turns and raw/cron runs. | Treat as part of the schedule-message permission family. Deny `rocketclaw_schedule_message` to block schedule reset behavior. | Clears scheduled messages for the current conversation. |
| `rocketclaw_attach_files_to_response` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Attaches collected files to the final outbound response through RocketClaw's shared response-attachment path. |
| `rocketclaw_update_goal` | Persistent bridge turns only, and only when the current text conversation has an active goal. | Auto-allow unless explicitly denied, but hidden when no active goal exists. | Reports goal status as `progress`, `complete`, or `blocked` with an optional note. `complete` runs any configured `checkScript:` before the goal becomes complete. |
| `rocketclaw_start_new_thread` | Qualifying human-originated Slack, Discord Text, or terminal CLI turns with a native originating surface. | Default-deny. Requires explicit per-agent `allow`; missing, `auto`, or `deny` keeps it unavailable. | Creates a new managed conversation on the same native surface, inherits source conversation context, and submits the tool prompt as the first task. It is never exposed for cron, MCP, voice, web, scheduled/system/automation, or automatic goal continuation turns. Discord DM calls fail clearly because DMs cannot host guild-thread mechanics. |
| `rocketclaw_i_want_human_partner_to_see_this` | Raw/cron runs only. | Auto-allow in raw/cron tool mode unless explicitly denied by the cron agent. | Required raw-run completion tool. Its argument is the exact human-visible output, or an empty string for silence. |
| `ask_user_question` | Qualifying human-originated Slack, Discord Text, or terminal CLI turns with a native answer path. | Auto-allow unless explicitly denied, but hidden when the turn has no answer path. | Asks the originating human through native UI, blocks until answered or canceled, and returns selected options and/or custom text. Not exposed for cron/raw, MCP, voice, web, scheduled/system/automation, automatic goal continuations, or restart recovery continuations. |

For general `permission` syntax, action values, guardrails, and approval reviewers, see Agent Frontmatter And Permissions below. RocketCode is deny-by-default and later matching rules win. RocketClaw's default tool allows are injected after agent permissions unless an explicit deny already matched, so use `deny` rather than `auto` when the intent is to block or force review of a RocketClaw auto-allowed tool.

## Emoji Translation Table

| Emoji | Alias or Reaction Name | Meaning |
| --- | --- | --- |
| `🧵` | `:thread:` | Default managed conversation prefix. |
| `🔀` | `:twisted_rightward_arrows:` | Pre-seeded managed conversation prefix. |
| `🏭` | `:factory:` | Example custom `thread_agents` prefix. |
| `🔁` | `:repeat:` | Goal loop prefix. |
| `🏁` | `:checkered_flag:` | Goal loop prefix. |
| `🛑` | `:octagonal_sign:`, `octagonal_sign` | Stop command or reaction. |
| `⏹️` | `:stop_button:`, `stop_button` | Stop command or reaction. |
| `❗` | `:exclamation:`, `exclamation` | Interruption or rejection marker. |
| `✅` | `:white_check_mark:`, `white_check_mark` | Completion marker. |
| `💾` | `:floppy_disk:`, `floppy_disk` | Managed conversation summary command. |
| `🔂` | `:repeat_one:`, `repeat_one` | Cron one-off request prefix or reaction. |
| `🎛` | `:control_knobs:` | Managed conversation agent switch command. |
| `🤖` | `:robot_face:`, `robot_face` | Slack processing/accepted marker. |
| `⏳` | `:hourglass_flowing_sand:`, `hourglass_flowing_sand` | Slack buffered or in-progress marker. |
| `📲` | `:calling:`, `calling` | Slack Discord voice relay marker. |
| `🎙️` | `:studio_microphone:`, `studio_microphone` | Slack browser voice relay marker. |
| `📡` | `:satellite_antenna:`, `satellite_antenna` | Slack External MCP relay marker. |

## Agent Frontmatter And Permissions

Agents are top-level Markdown files in `agents/*.md`. The filename without `.md` is the agent name. YAML frontmatter is required, followed by the agent prompt body.

```yaml
---
description: Short human-readable purpose
model: gpt-5.4
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
| `model` | Optional unprefixed OpenAI Responses model ID. Missing or empty inherits the runtime/default model. |
| `reasoningEffort` | Optional model reasoning effort. Avoid `xhigh`; `rocketclaw lint` reports it. |
| `verbosity` | Optional model verbosity. |
| `maxRecursion` | Optional task delegation depth for inferences started with this agent. Omitted or `-1` is unlimited, `0` disables `task`, and positive integers allow that many levels. |
| `guardrail` | Optional loaded agent name that reviews task delegations to this agent before the child runs and after its response. The guardrail must return strict JSON with `approved` and `reason`. |
| `additionalInstructions` | Optional RocketClaw normal-reply prompt-header override for this selected agent. Use it for response-format guidance, such as telling an agent to avoid Markdown for Slack/TTS, prefer Markdown for technical answers, keep answers terse, or follow another surface-specific style. When omitted, RocketClaw uses `Reply in plain text suitable for both Slack and text-to-speech. Avoid markdown unless it is necessary.` It does not affect internal notes or raw cron runs. |
| `permission` | Optional singular permission map. Omit it when the agent needs no tools. Do not use plural `permissions`; RocketCode ignores it and `rocketclaw lint` reports it. |

RocketCode denies tools by default. `permission` grants are grouped by bucket, and each bucket maps subjects to actions. Later matching rules override earlier matching rules, so put broad allows before narrower denies when subtracting access.

Permission actions:

| Action | Meaning |
| --- | --- |
| `allow` | Run matching tool calls without automatic review. |
| `deny` | Block matching tool calls. Use it after a broader allow to subtract specific subjects. |
| `auto` | Ask RocketCode's embedded `guardian` automatic reviewer. RocketClaw enables automatic review. Review failure, invalid JSON, or denial blocks the call. |
| `auto(agent-name)` | Ask the named loaded custom reviewer agent. Do not create an agent named `guardian`; that name is reserved. |

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
| `rocketclaw cli` | Attaches a terminal CLI to the server-owned `main` conversation, or uses embedded fallback only when no server owns state. |
| `rocketclaw cli --new [agent]` | Asks the server to create a private persisted terminal conversation using `agent`, or `main` when omitted. |
| `rocketclaw cli --attach <conversation-id>` | Attaches the terminal CLI to an existing server-owned conversation. |
| `rocketclaw setup` | Interactively creates or updates `rocketclaw.json`, root setup files, workspace overlays, and `.rocketclaw/`. |
| `rocketclaw setup files list` | Lists embedded setup payload files. |
| `rocketclaw setup files get <path>` | Prints one embedded setup payload file. |
| `rocketclaw doctor` | Validates the loaded configuration and RocketCode availability. |
| `rocketclaw doctor tts [-text <text>]` | Runs a TTS diagnostic probe. |
| `rocketclaw doctor stt -file <audio-file>` | Runs an STT diagnostic probe. |
| `rocketclaw lint [next|current]` | Checks effective RocketCode agent-system safety. Defaults to `next`. |
| `rocketclaw agent-graph [next|current]` | Prints the effective RocketCode task delegation and guardrail graph as Graphviz/DOT. Defaults to `next`. |
| `rocketclaw oai login [--headless]` | Authenticates RocketClaw to ChatGPT for RocketCode model requests and writes the selected runtime `auth.json`. |
| `rocketclaw fc list [--since 24h|RFC3339] [--until RFC3339] [--limit N] [--no-message-preview]` | Lists stored RocketCode sessions. |
| `rocketclaw fc observe [--follow|-f] [conversation-id]` | Prints stored session entries as JSONL; defaults to `main`. |
| `rocketclaw fc delete <conversation-id>` | Deletes a stored session when the daemon does not own the state store. |
| `rocketclaw help`, `rocketclaw -h`, `rocketclaw --help` | Prints top-level help. |

For `lint` and `agent-graph`, `next` builds a temporary startup-equivalent runtime view from embedded assets, overlays, and workspace files. `current` inspects the selected generated runtime directory as it exists now.
