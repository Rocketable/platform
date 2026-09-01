---
title: Slack Bare $cron Lists Channel Jobs
date: 2026-08-31
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/slack
problem_type: best_practice
component: assistant
severity: medium
resolution_type: code_fix
applies_when:
  - Handling Slack $cron or 🔂 with no job name
  - Listing live top-level cron stems for the Slack channel of the call
  - Matching cron frontmatter channel to Slack #name rather than the @ routing row
  - Choosing an ephemeral list reply instead of loading or starting an on-demand cron run
related_components:
  - internal/rocketclaw/backend
tags:
  - slack
  - cron
  - bare-cron
  - dollar-command
  - ephemeral
  - channel-filter
  - on-demand-cron
---

# Slack Bare $cron Lists Channel Jobs

## Context

Bare `$cron` (and `🔂`) used to treat an empty target as a missing job. `LoadOneOffCronjob` rejects a blank stem (`internal/rocketclaw/backend/manager.go:187-189`). Slack then posted that it could not find the cronjob (`internal/rocketclaw/frontend/slack/connector.go:4919-4921`).

Operators need the jobs that *post into this room*, not a failed run. `$workflow` already lists on empty args (`internal/rocketclaw/frontend/slack/connector.go:4517-4534`). Cron is different: every live `cron/*.md` names a Slack channel, and any top-level job can still be *run* from any configured room.

## Guidance

Empty `$cron` args list. Non-empty args still load and run.

1. Resolve the Slack `#name` of the call with `conversations.info`, then pass `"#" + name` into `ListCronjobs` (`internal/rocketclaw/frontend/slack/connector.go:4896-4914`). Do not reuse `socialModeChannel`'s returned name: an unmapped hail can be `"@"` (`internal/rocketclaw/frontend/slack/connector.go:3934-3935`), and cron jobs never target the `@` Channel Entry.
2. Keep listing on `oneOffCronjobRunner`. `Manager.ListCronjobs` loads live runtime definitions and returns stems whose `textChannel` equals that `#name` (`internal/rocketclaw/backend/manager.go:235-253`). Return `[]string` stems (`daily`), not `OneOffCronjob` with an empty prompt.
3. Post the list with `postSlackEphemeral`, matching bare `$workflow`. Empty: `No cronjobs target this channel.` Load/list errors stay ephemeral too.
4. Named `$cron daily` stays a run from any configured channel.

Fix opened in #29, unmerged as of this writing.

## Why This Matters

A "couldn't find" reply on `@bot $cron` hides which jobs belong to the room. Filtering on `@` would always be empty. Returning run payloads for a list leaks file layout into Slack. A public thread reply would start a visible thread under the mention; ephemeral keeps the lookup with the caller.

## When to Apply

- Adding or changing a Slack dollar-command list form next to a run form (`$cron`, `$workflow`).
- Matching cron output rooms to Slack events (Root Slack Mention or Managed Slack Thread).
- Deciding whether a list should use the routing key (`@`) or the real `#channel`.

## Evidence

- `TestListCronjobsFiltersByChannel` keeps `#ops` stems and drops `#triage`.
- `TestHandleAppMentionEventListsChannelCronjobs` and `TestHandleMessageEventConsumesBareCronCommands` assert ephemeral stems and no load/run.

## Related

- [Slack Thread Parent Message Redelivery Enqueued Second Turn](../logic-errors/slack-thread-parent-message-redelivery-enqueued-second-turn.md) — idle `$cron` on a parent message is a different path; do not fold listing into `socialThreadReply`.
- CHEATSHEET: `cmd/rocketclaw/CHEATSHEET.md`
