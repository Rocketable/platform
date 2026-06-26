# RocketClaw Emoji Cheatsheet

| Canonical Form | Aliases | Surface | What It Does | Notes |
| --- | --- | --- | --- | --- |
| `:thread:` | `🧵` | Slack, Discord Text | Starts a managed conversation using the configured `thread_agents` entry. | Default config maps `:thread:` to agent `main` without pre-seeding. Leading known aliases are normalized before matching. |
| `:twisted_rightward_arrows:` | `🔀` | Slack, Discord Text | Starts a managed conversation using the configured `thread_agents` entry. | Default config maps this to agent `main` with `pre_seed: true`. |
| Configured `thread_agents` prefix | Config-specific | Slack, Discord Text | Starts a managed conversation with the configured agent and pre-seed behavior. | Prefixes may be aliases or Unicode emoji. Unknown aliases remain literal, so custom aliases can still be configured. |
| `🔁` | Slack `:repeat:` | Slack, Discord Text | Starts a text goal loop. | Syntax: `🔁 [maxTurns: VALUE] [checkScript: VALUE] OBJECTIVE`. |
| `🏁` | Slack `:checkered_flag:` | Slack, Discord Text | Starts a text goal loop. | Same grammar as `🔁`. |
| `🛑`, `⏹️` | Slack reactions `:octagonal_sign:`, `:stop_button:` | Slack, Discord Text | Stops the active main or managed-conversation turn. | Works as an exact message. Also works as a stop reaction on supported managed/main turn surfaces. Stop feedback is marker-only: RocketClaw adds `❗` and sends no stop text. Discord uses the Unicode emoji. |
| `❗` | Slack `:exclamation:` | Slack, Discord Text | Interruption or rejection marker. | Added by RocketClaw after stop/interruption and for duplicate active-goal rejection. Humans generally do not use this as a command. |
| `✅` | Slack `:white_check_mark:` | Slack, Discord Text | Completion marker. | Added when a goal reaches `complete`; Slack also uses it for successful summary completion. Not added for `blocked`, `stopped`, or `budget_exhausted`. |
| `💾` | Slack `:floppy_disk:` | Slack, Discord Text | Summarizes a managed conversation back to main. | Requires the configured/authorized human or social-mode allowed user. |
| `🔂` | `:repeat_one:`, Slack reaction `repeat_one`, Discord `repeat_one` | Slack, Discord Text | Runs a one-off cron request by text prefix or reaction. | Examples: `🔂 daily`, `🔂 daily.md`. Reaction reruns inspect the acted-on message and require exactly one deterministic cron target. |
| `🎛` | `:control_knobs:` | Slack, Discord Text social-mode managed conversations | Switches or cycles the persisted agent for the managed conversation. | `🎛 agent-name` switches to a configured channel agent. Bare `🎛` cycles to the next configured agent. Does not route to RocketCode as prompt input. |
| Slack `:robot_face:` | None | Slack | Processing/accepted marker. | Added when RocketClaw accepts a Slack-originated or relayed turn; removed after final response delivery. |
| Slack `:hourglass_flowing_sand:` | None | Slack | Buffered or in-progress marker. | Marks stacked/buffered Slack messages and summary-in-progress state. Removed when processing advances or summary finishes. |
| Slack `:calling:` | None | Slack | Discord voice relay marker. | Added to Slack relay messages created from Discord voice input. |
| Slack `:studio_microphone:` | None | Slack | Browser voice relay marker. | Added to Slack relay messages created from browser voice input. |
| Slack `:satellite_antenna:` | None | Slack | External MCP relay marker. | Added to Slack relay messages created from External MCP prompts. |

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

Result: `:factory: build the release` starts a managed conversation with agent `factory` and prompt `build the release`.

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

Result: `:twisted_rightward_arrows: continue from main context` starts a managed conversation with agent `main` and pre-seeds it from existing context.
