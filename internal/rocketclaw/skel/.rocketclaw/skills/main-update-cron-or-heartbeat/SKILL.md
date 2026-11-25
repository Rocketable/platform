---
name: main-update-cron-or-heartbeat
description: Mandatory skill for any request to create or change when an agent runs on its own, through the routines in its agent file (also called a cron, cronjob, schedule or heartbeat)
---
## Purpose

Use this skill whenever the human wants to create, update, remove, or ask about an agent's schedule: the `routines` in its agent file.

Routines are the only way to schedule an agent. People may still call one a cron, a cronjob, a schedule or the heartbeat; each means a routine.

This skill is mandatory for any `routines` change. Do not edit `routines` outside this skill.

## Runtime paths

Only use these runtime paths:

- current effective agent: `.rocketclaw/agents/<agent>.md`
- writable overlay agent: `agents/<agent>.md`

Edit the agent file the way `main-create-or-update-agent` says: read the effective file, copy it into `agents/` before editing, never edit `.rocketclaw` directly, keep every other key and the body unchanged, and call `rocketclaw_reload` exactly once after all edits. A schedule that is not valid, or one listed twice, fails loading only that agent: the reload still succeeds, that agent and all its routines stay off, and the reload's answer says `Agent <name> was not loaded: routines: …`. Fix it before you finish.

## Which agents can have routines

- Any agent file, advanced or easy, can have `routines`.
- `main` is built in and has no agent file, so it cannot have routines. To schedule work for it, create a new agent through `main-create-or-update-agent`.
- `routines` on a `sticky: user` or `sticky: slack` agent fail loading with `routines: a sticky: user agent cannot have routines` (or `sticky: slack`). Never add them there.
- A `sticky: global` agent's routine runs report into its one shared session instead of a chat per run.
- Only top-level agents' routines run. A shadow's own `routines` never run, and routine runs get no shadows.

When the human has not said which agent should run a new routine, ask.

## Routines

```yaml
routines:
  - "*/5 * * * *": "Check the campaign dashboard for anomalies."
  - "30m"
```

- Each entry is a schedule alone, or a one-entry map from schedule to instruction.
- An entry with an instruction sends it as the prompt; a bare entry sends `Scheduled run (<schedule>).`
- The instruction expands prompt-shell expressions like an agent body does (see `main-create-or-update-agent`); they run through the agent's `shell` when it has one. Use a YAML block scalar for an instruction with several lines.
- A routine runs in its agent's `space`. There is no `space` per routine: when a routine must run in another space, it needs its own agent in that space.
- A non-empty reply opens a new Web chat with the report, run by the agent; an empty reply reports nothing.
- A routine is its agent plus its schedule text: editing the instruction keeps it, changing the schedule starts a new one, and the same schedule twice in one file fails loading. Renaming the agent leaves its earlier routine runs under the old name.
- A routine whose previous run is still running is not started again until that run finishes.
- The Web's Routines page (`/routines`) lists each routine as `<agent> · <schedule>`. Its play button, or "Routines: Run" in the command palette, runs one now.

Routines repeat. For a single future run, use `rocketclaw_schedule_message`: it creates a delayed prompt inside the current conversation/session, including the current Slack thread when applicable, and its eventual output appears in that same session/thread.

## Overlapping routines

Two routines of one agent that come due on the same minute start two runs side by side. Nothing combines them, because each can carry a different instruction. For example, these both run on Monday at 9:00:

```yaml
routines:
  - "0 9 * * 1-5": "Triage new issues."
  - "0 9 * * 1": "Write the weekly strategy note."
```

When the human wants one run, write schedules that do not overlap: `"0 9 * * 2-5"` for the triage, plus a Monday entry whose instruction does both. Before adding or changing a schedule, compare it with the agent's other routines, and when two would come due on the same minute, tell the human and ask whether that is what they want.

## Schedules

A schedule is one of:

- a 5-field cron expression (`man 5 crontab`), such as `"*/5 * * * *"`
- a Go `time.ParseDuration` duration greater than zero, such as `15m`, `90s` or `1h30m`
- an `@daily`-style shortcut, such as `@hourly`, `@daily`, `@weekly` or `@monthly`

`@every` fails loading; write the duration instead. Quote every schedule in YAML. One entry holds one schedule; for several schedules, write several entries.

Interpret requests like these as referring to a routine's schedule:

- "run the heartbeat more frequently"
- "slow the cron down"
- "set it to every 5 minutes"
- "what is the heartbeat frequency right now?"

That means:

- if the human asks how often an agent runs, read the schedules from its `routines`
- if the human asks to change how often it runs, change that entry's schedule and keep its instruction
- if the human only asks to change what it does, change the instruction and leave the schedule untouched
- if it is unclear which agent or routine they mean, list the agents with routines and ask

If `go` is available, validate any intended duration before writing it.

You can use this exact validation pattern:

```bash
s='THE INTENDED DURATION GOES HERE'; mkdir -p .tmp; f=$(mktemp .tmp/pd-XXXXXX.go); printf '%s\n' 'package main; import ("fmt"; "os"; "time"); func main() { d, err := time.ParseDuration(os.Args[1]); if err != nil { fmt.Println("bad:", err); os.Exit(1) }; fmt.Println("ok:", d) }' > "$f"; go run "$f" "$s"; rm -f "$f"
```

Check for Go first with either:

- `which go`
- `command -v go`

If `go` is available and the duration fails validation, do not write the file until the human gives you a valid duration.

If `go` is not available, be conservative:

- only write the requested interval if it is already clearly in normal `time.ParseDuration` format or a 5-field cron expression
- if it looks ambiguous or invalid, ask the human to clarify instead of guessing

## Instruction updates

When the human asks to create a routine or change what one does, ask what its reply should be, specifically:

- contents
- structure
- rules for silence, if any

Do this only for instruction changes. Do not ask these questions for schedule-only edits.

## Migrating leftover cron files

RocketClaw no longer reads `cron/` folders, in the workspace or in overlays, and ignores files left there without a warning. If `cron/*.md` (such as `cron/HEARTBEAT.md`) or `.rocketclaw/overlays/<clone>/cron/*.md` still exists, it no longer runs. When the human asks about one, or you find one while doing this skill, tell them and offer to migrate it:

1. Read the file. Its `agent:` names the agent that ran it; its `schedule:` holds one schedule or a list; its body is the prompt.
2. Add one `routines` entry per schedule to that agent, with the body as the instruction, following the rules above. When the file had no `agent:`, or names `main` or a `sticky: user` or `sticky: slack` agent, ask which agent should run it.
3. When the file's `space:` differs from the agent's `space`, the routine would run in the agent's space instead. Ask whether that is fine, or whether the job needs its own agent in that space.
4. Delete the workspace `cron/` file. A file in an overlay clone cannot be deleted from the workspace: tell the human to move it into an agent in that overlay's repository and delete it there.
5. Call `rocketclaw_reload` once, after all edits.
