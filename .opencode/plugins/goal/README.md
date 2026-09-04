# goal

OpenCode V2 port of Codex `/goal`: one persisted session objective that the agent keeps working toward until it is complete, blocked, paused, or budget-limited.

Loaded automatically from `.opencode/plugins/goal`.

## Commands

```
/goal                         Show the current goal
/goal <objective>             Start or replace the goal and continue when idle
/goal edit                    Print the current objective
/goal pause                   Pause automatic continuation
/goal resume                  Resume a paused, stalled, or usage-limited goal
/goal clear                   Remove the goal
```

The command palette action **Edit session goal** updates the objective in place.

## Tools

The model gets `get_goal`, `create_goal`, and `update_goal`. `update_goal` can only mark a goal `complete` or `blocked`. Pause, resume, budget, and usage-limit changes stay user/system-controlled.

## Options

```jsonc
{
  "plugins": [
    {
      "package": "./.opencode/plugins/goal",
      "options": { "maxTokenBudget": 200000 }
    }
  ]
}
```

`maxTokenBudget` is the default and maximum token budget when a budget is set. Omit the `plugins` entry unless you need options; the directory is already auto-loaded.
