---
name: main-update-cron-or-heartbeat
description: Mandatory skill for any request to create or modify cron/ (aka the heartbeat definition, aka the cronjob configuration)
---
## Purpose

Use this skill whenever the human wants to create, update, or otherwise modify the cronjob instructions used by rocketclaw.

This skill is mandatory for any `cron/` and `cron/HEARTBEAT.md` change.

Do not edit `cron/*.md` outside this skill.

If the request touches heartbeat behavior, heartbeat content, or the `schedule:` frontmatter value, this is the skill that must be used.

If the request touches cron behavior, cron content, or the `schedule:` frontmatter value, this is the skill that must be used.

## Runtime paths

Only use this runtime path:

- `cron`

## Heartbeat interval

Changing the `schedule:` interval is allowed, but the value must conform to RFC3339 timestamp syntax, Go `time.ParseDuration` syntax, or to cronjob syntax (`man 5 crontab`). It can be a single value or a list of values, except timestamp schedules must be single values:

Valid Examples:
- Go `time.ParseDuration` format
```
---
schedule: "5s"
---
```
- Crontab format
```
---
schedule: "*/5 * * * *"
---
```
- One-off timestamp format
```
---
schedule: "2026-05-21T15:04:05Z"
---
```
- Many schedules
```
---
schedule:
  - "5s"
  - "*/5 * * * *"
---
```

The `schedule:` frontmatter value is the cronjob frequency or cadence.

Cronjobs may also set `slack-channel:` to route non-empty `rocketclaw_i_want_human_partner_to_see_this` output into a managed Slack channel thread instead of the default main outputs:

```
---
schedule: "0 9 * * 1-5"
slack-channel: "#triage"
---
```

Replies and `:floppy_disk:` summaries for those cron-created channel threads follow the existing Slack social-mode gate.

Timestamp schedules are one-off crons. A one-off cron is a durable `cron/*.md` file that survives rocketclaw restarts until due, runs through normal cronjob execution and output routing, and self-deletes after one completed run attempt. Do not combine a timestamp schedule with any other schedule.

Do not confuse one-off crons with `rocketclaw_schedule_message`. `rocketclaw_schedule_message` creates a short-lived delayed prompt inside the current conversation/session, including the current Slack thread when applicable, and its eventual output appears in that same session/thread. One-off crons are file-backed cron definitions and their output is handled exactly like any other cronjob output.

Interpret requests like these as referring to the `schedule:` value:

- "run the heartbeat more frequently"
- "slow the heartbeat down"
- "set the heartbeat to every 5 minutes"
- "what is the heartbeat frequency right now?"

That means:

- if the human asks about the heartbeat frequency, read it from the `schedule:` frontmatter value
- if the human asks to change how often the heartbeat runs, update the `schedule:` frontmatter value
- if the human only asks to change heartbeat content or instructions, leave `schedule:` untouched

Examples of valid durations include:

- `15m`
- `90s`
- `1h30m`

Examples of valid one-off timestamps include:

- `2026-05-21T15:04:05Z`
- `2026-05-21T15:04:05.123456789Z`

If `go` is available, validate any intended interval before writing it.

You can use this exact validation pattern:

```bash
s='THE INTENDED DURATION GOES HERE'; f=$(mktemp /tmp/pd-XXXXXX.go); printf '%s\n' 'package main; import ("fmt"; "os"; "time"); func main() { d, err := time.ParseDuration(os.Args[1]); if err != nil { fmt.Println("bad:", err); os.Exit(1) }; fmt.Println("ok:", d) }' > "$f"; go run "$f" "$s"; rm -f "$f"
```

Check for Go first with either:

- `which go`
- `command -v go`

If `go` is available and the duration fails validation, do not write the file until the human gives you a valid duration.

If `go` is not available, be conservative:

- only write the requested interval if it is already clearly in normal `time.ParseDuration` format
- if it looks ambiguous or invalid, ask the human to clarify instead of guessing

## Body and instruction updates

When the human asks to change cronjob body/content/instructions (including heartbeat instructions), ask what the expected output should be for `rocketclaw_i_want_human_partner_to_see_this`, specifically:

- contents
- structure
- rules for silence, if any

Do this only for body/instruction changes. Do not ask these questions for schedule-only edits.

## File handling

If `cron/HEARTBEAT.md` does not exist, copy from `cron/HEARTBEAT.example.md` into `cron/HEARTBEAT.md`.

When `cron/HEARTBEAT.md` already exists:

- read it
- do not create a new file
- preserve the existing `schedule:` value unless the human explicitly asked to change it
- update the existing file in place
- update only the heartbeat body/content
