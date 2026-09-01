---
title: "Bare $cron Lists Channel Jobs - Plan"
type: feat
date: 2026-08-31
topic: bare-cron-list
artifact_contract: unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# Bare $cron Lists Channel Jobs - Plan

## Goal Capsule

- **Objective:** A human who types `@bot $cron` with no job name sees the cronjobs that post into that Slack channel.
- **Means:** Bare `$cron` (and `🔂`) lists top-level cron stems whose frontmatter `channel` matches the Slack `#name` of the call. Named `$cron <job>` still runs. Listing is ephemeral, like bare `$workflow`.
- **Product authority:** Product Contract below is source of WHAT. Planning Contract is HOW.
- **Product Contract preservation:** n/a (ce-plan-bootstrap)
- **Open blockers:** None.
- **Stop conditions:** AE1–AE3 have tests. `gofmt` on touched files. Touched-package tests, `make lint`, and `make test` pass.

## Product Contract

### Summary

Bare `$cron` in a Slack channel lists the live cronjobs that target that channel. It does not start a job.

### Problem Frame

`$cron <job>` runs a top-level cron file from any configured Slack channel. Bare `$cron` currently tries to load an empty target and replies that the job was not found. Operators need a channel-local index of which jobs post here.

### Actors

- A1. Authorized Slack human in a configured or adhoc-allowed channel
- A2. Slack connector command dispatch
- A3. Cron manager reading live runtime `cron/*.md` definitions

### Requirements

- R1. `@bot $cron` with no job name lists cronjobs whose frontmatter `channel` equals the Slack channel of the call (`#name`, not the `@` routing row).
- R2. The same bare command in a managed thread lists for that thread's Slack channel.
- R3. `$cron <job>` (and `🔂 <job>`) still runs a top-level job from any configured channel.
- R4. The list shows the stems a human would pass to `$cron` (`daily`, not `cron/daily.md`).
- R5. If no job targets the channel, say so. Do not start a turn.
- R6. Help text and `cmd/rocketclaw/CHEATSHEET.md` mention bare listing.

### Key Flows

- F1. Root mention list
  - **Trigger:** A1 sends `@bot $cron` with no job argument.
  - **Steps:** A2 resolves the Slack `#name`, A3 returns matching stems, A2 posts the list to A1 only.
  - **Covered by:** R1, R4, R5
- F2. Named run unchanged
  - **Trigger:** A1 sends `$cron daily`.
  - **Steps:** A2 loads and runs that job as today.
  - **Covered by:** R3

### Acceptance Examples

- AE1. `@bot $cron` in `#social` with `daily.md` and `weekly.md` targeting `#social` and `ops.md` targeting `#ops` lists `daily` and `weekly` only. Covers R1, R4.
- AE2. Bare `$cron` / `🔂` / `:repeat_one:` in a managed `#social` thread lists the same way and does not call load/run. Covers R2, R3.
- AE3. Bare `$cron` in a channel with no matching jobs replies that none target this channel. Covers R5.

### Scope Boundaries

- Named on-demand run, scheduling, and cron file format stay as they are.
- Do not restrict which jobs can be *run* from a channel; listing is filter-only.
- Do not list example templates (`*.example.md`).

## Assumptions

- Listing is ephemeral to the caller, matching bare `$workflow` (not a public thread reply).
- List rows are stems only (no schedule or prompt body).
- Channel identity is Slack `#name` from `conversations.info`, so adhoc `@` rooms still filter on the real channel.

## Planning Contract

### Key Technical Decisions

- KTD1. **List through the existing on-demand cron runner, not the thread router.** Slack already depends on `oneOffCronjobRunner` for `$cron`. Add `ListCronjobs(channel string) ([]string, error)` there and implement it on `backend.Manager` via `loadDefinitionsIn`. Governs R1, R4.
- KTD2. **Filter in the manager; Slack only joins names.** Do not return `OneOffCronjob` with an empty prompt. Governs R4.
- KTD3. **Bare args stay in `handleOnDemandCronRequest`.** Empty target lists; nonempty target keeps today's load/run path. Governs R2, R3.
- KTD4. **Ephemeral copy matches `$workflow`.** Error: `I couldn't list cronjobs: …`. Empty: `No cronjobs target this channel.` Governs R5.

### Technical Design

`backend.Manager.ListCronjobs` loads live runtime cron definitions and returns stems whose `textChannel` matches the requested `#name`. Slack, on empty `$cron` args, looks up the conversation name, calls `ListCronjobs`, and `postSlackEphemeral`s the joined stems.

### Patterns to follow

- Bare `$workflow` listing in `internal/rocketclaw/frontend/slack/connector.go` (`handleWorkflowRequest` empty-args branch).
- `LoadOneOffCronjob` / `loadDefinitionsIn` in `internal/rocketclaw/backend/manager.go`.

## Implementation Units

### U1. List stems for a channel

- **Goal:** Manager returns sorted-by-filename stems for one `#channel`.
- **Files:** `internal/rocketclaw/backend/manager.go`, `internal/rocketclaw/backend/manager_test.go`
- **Approach:** Reuse `loadDefinitionsIn`. Skip prompt body. Skip `.example.md` via existing loader.
- **Verification:** `TestListCronjobsFiltersByChannel` — `#ops` returns `daily` and `heartbeat`, omits `#triage`, unknown channel is empty.
- **Done when:** AE1 filter behavior is proven at the manager.

### U2. Bare `$cron` posts that list

- **Goal:** Root mention and managed-thread bare `$cron` list; named `$cron` still runs.
- **Files:** `internal/rocketclaw/frontend/slack/connector.go`, `internal/rocketclaw/frontend/slack/connector_test.go`, `internal/rocketclaw/frontend/slack/inert_test.go`, `cmd/rocketclaw/CHEATSHEET.md`
- **Approach:** Empty target in `handleOnDemandCronRequest` lists; help table uses `$cron [job]`.
- **Verification:** `TestHandleAppMentionEventListsChannelCronjobs`, `TestHandleMessageEventConsumesBareCronCommands`, existing named-run tests still pass, help-table assertion updated.
- **Done when:** AE1–AE3 and R6 hold.

## Verification Contract

- `gofmt` on touched files
- `go test ./internal/rocketclaw/backend ./internal/rocketclaw/frontend/slack -count=1 -run 'TestListCronjobsFiltersByChannel|TestHandleMessageEventConsumesBareCronCommands|TestHandleAppMentionEventListsChannelCronjobs|TestHandleAppMentionEventRunsOnDemandCron'`
- `make lint`
- `make test`

## Definition of Done

- U1 and U2 verification fields observed
- Named `$cron` still runs from any configured channel
- README: no update (CHEATSHEET is the command surface)
