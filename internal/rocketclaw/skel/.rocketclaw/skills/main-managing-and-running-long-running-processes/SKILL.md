---
name: main-managing-and-running-long-running-processes
description: YOU MUST USE THIS SKILL when starting, supervising, stopping, checking, or troubleshooting long-running processes, scripts, servers, watchers, jobs, daemons, or tmux sessions outside subagent tasks
---

# Purpose

Use this skill whenever the human wants to start, supervise, stop, restart, inspect, troubleshoot, or monitor a long-running process or script.

This includes servers, watchers, daemons, background jobs, local services, polling scripts, long-lived CLIs, and any command expected to keep running after the current response.

Do not use subagent tasks as process supervisors. Subagents are for delegated work, not for owning long-running processes.

# Mandatory Rules

- Use `tmux` for long-running process sessions.
- Keep a Markdown ledger for every managed process.
- Update the ledger before and after start, stop, restart, and health-check actions.
- If a process needs recurring supervision, create or update the needed `cron/` monitor.
- If the cron monitor needs a dedicated agent or broader permissions, create or update the needed `agents/` file.
- Do not use `&`, `nohup`, `disown`, shell backgrounding, or a subagent task instead of `tmux` unless the human explicitly approves a different mechanism.

# Do Not Use For

Do not use this skill for short-lived commands that complete during the current turn, such as normal tests, builds, formatters, one-shot scripts, or read-only inspection commands.

# Ledger

The default ledger path is:

`LONG_RUNNING_PROCESSES.md`

If the human names a different ledger, use that path instead.

Create the ledger when it does not exist. Keep it in Markdown and preserve existing entries. Use this predictable structure:

```markdown
# Long-Running Processes

| Name | Owner | Purpose | tmux Session | Command | CWD | Started | Expected Lifetime | Health Check | Cron Monitor | Agent | Status | Last Checked |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| example | Human Partner | local dev server | rocketclaw-example | npm run dev | /path/to/project | 2026-05-20T15:04:05Z | until stopped | curl -fsS http://localhost:3000 | cron/example.md | agents/example-monitor.md | running | 2026-05-20T15:05:00Z |
```

Status values should be plain and current, such as `planned`, `starting`, `running`, `stopped`, `failed`, `missing`, or `unknown`.

# tmux Workflow

First verify `tmux` is available:

```bash
command -v tmux
```

If `tmux` is missing, do not start the process with another long-running mechanism. Tell the human that `tmux` is required for this skill and ask whether they want to install it or explicitly approve another mechanism.

Choose a stable session name. Prefer lowercase letters, digits, and hyphens. Include enough context to avoid collisions, for example:

```bash
session='rocketclaw-example'
```

Before starting, check whether the session already exists:

```bash
tmux has-session -t "$session"
```

Start the command in the intended working directory:

```bash
tmux new-session -d -s "$session" -c "$PWD" -- "$command"
```

Verify the session exists after starting:

```bash
tmux has-session -t "$session"
```

Inspect recent output:

```bash
tmux capture-pane -pt "$session" -S -200
```

List known sessions:

```bash
tmux list-sessions
```

Stop a managed session only when the human requested it or when the ledger/cron instructions clearly authorize it:

```bash
tmux kill-session -t "$session"
```

# Cron Monitor Creation

If a managed process needs recurring supervision and no suitable cron monitor exists, create or update one under `cron/`.

Use the `main-update-cron-or-heartbeat` skill for all `cron/` edits.

The cron monitor should:

- check the relevant `tmux` session with `tmux has-session`
- capture recent pane output with `tmux capture-pane`
- run the process-specific health check when one exists
- update `LONG_RUNNING_PROCESSES.md` with status and last checked time
- notify the human only when the process is missing, unhealthy, repeatedly failing, or blocked on human action
- call `rocketclaw_i_want_human_partner_to_see_this("")` when there is nothing actionable to report

Keep the cron schedule conservative. If the human did not specify a cadence, ask for one instead of guessing for expensive or noisy checks. For cheap local checks, a short interval such as `5m` is usually reasonable, but still confirm when impact is unclear.

# Cron Agent Creation

If the cron monitor needs an agent that does not exist or lacks necessary permissions, create or update one under `agents/`.

Use the `main-create-or-update-agent` skill for all `agents/` edits.

The cron agent should have narrow, concrete permissions only. Typical permissions are:

```yaml
permission:
  bash:
    "tmux *": allow
    "date *": allow
    "command -v tmux": allow
  read:
    "LONG_RUNNING_PROCESSES.md": allow
  edit:
    "LONG_RUNNING_PROCESSES.md": allow
```

Add process-specific `bash`, `read`, `edit`, `grep`, or `glob` permissions only when the monitor actually needs them. Do not write broad `bash: allow`, broad `edit: allow`, or catch-all permission grants.

# Troubleshooting

If `tmux` is missing:

- do not start the process with shell backgrounding
- report that `tmux` is required
- ask whether to install `tmux` or use an explicitly approved alternate supervisor

If the tmux server is unavailable or reports an error:

- run `tmux list-sessions` to confirm the failure mode
- capture the error in the ledger status
- do not claim the process is running until `tmux has-session` succeeds

If the session is missing:

- mark the ledger status as `missing`
- check whether the process was intentionally stopped
- notify the human if the process was expected to be running

If the command exits immediately:

- capture pane output if available
- mark the ledger status as `failed`
- record the likely failure reason from the command output
- do not repeatedly restart unless the human requested restart behavior

If the ledger is stale:

- inspect `tmux` directly
- update the ledger with the observed state and current timestamp
- create or repair the cron monitor if recurring freshness is required

If the cron file is missing:

- use `main-update-cron-or-heartbeat` to create it under `cron/`
- make the cron instructions update the ledger and stay silent unless action is needed

If the cron schedule is invalid:

- use `main-update-cron-or-heartbeat` schedule validation rules
- do not write ambiguous schedules

If the cron agent is missing:

- use `main-create-or-update-agent` to create it under `agents/`
- grant only the exact permissions needed by the monitor

If the cron agent lacks permissions:

- identify the exact denied operation
- update the agent with the narrowest matching `bash`, `read`, `edit`, `grep`, or `glob` permission
- do not broaden unrelated permissions

If the monitor cannot update the ledger:

- verify the agent has `read` and `edit` access to the ledger path
- verify the ledger path in the cron instructions matches the actual file
- report only if human action is required

If the monitor is noisy:

- adjust the cron body so normal healthy checks call `rocketclaw_i_want_human_partner_to_see_this("")`
- notify only for missing, failed, unhealthy, or blocked states

# Final Checklist

Before calling the work complete, verify:

- `tmux` exists
- the session exists after start, or the ledger accurately says it does not
- recent pane output was checked when a session exists
- `LONG_RUNNING_PROCESSES.md` reflects the current state
- a cron monitor exists when recurring supervision is required, or the ledger records that none was requested
- the cron monitor uses `tmux` and updates the ledger
- the cron monitor stays silent when there is nothing actionable
- the cron agent exists when needed
- the cron agent has exact needed permissions for `bash`, `read`, `edit`, and any required `grep` or `glob`
- no subagent task is being used as a long-running process supervisor
