---
title: Retire cron files and rename the Cron page to Routines - Plan
type: refactor
status: completed
date: 2026-11-24
---

# Retire cron files and rename the Cron page to Routines - Plan

## Goal

Routines become the only way to schedule an agent. RocketClaw stops reading `cron/` folders, deletes the code that exists only for cron files, and calls the Web page that lists scheduled runs "Routines". Since every agent, easy or advanced, can now declare `routines`, cron files only duplicate them.

This is the second of three steps: advanced agents gained routines first; a later step gives easy and advanced agents one shared compiled form.

## Decisions

Session-settled with the user, one at a time:

- **Leftover `cron/` files are ignored silently.** No warning strip; this follows the repository rule for removed features. (user-directed)
- **Past cron-file runs keep their raw run path as their name**, such as `cron/daily.md`. The code that trimmed `cron/` and `.md` for display goes. They still list as Retired and still open; retention deletes them after 90 days. (user-directed, over keeping the trims)
- **The page moves from `/cron` to `/routines`, and saved tabs follow.** A one-time migration rewrites saved `/cron` and `/cron/…` tab paths to `/routines…`. No redirect stays in the code. (user-directed, over keeping `/cron` and over a plain rename)
- **User-visible words change; internal names wait.** Page title, tab label, palette commands and the `cron:` / `is:cron` search filters become Routines, `routine:` and `is:routine`. Run IDs (`cron:` prefix), log events (`cron_completed`), RPC and Go names stay for the third step. (user-directed)
- **Two routines of one agent that come due on the same minute start two runs.** Nothing combines them, because routines of one agent can carry different instructions. The README and the routines skill say so. (user-directed)

## Units

- **U1. Delete cron-file loading.** `config/cron.go` keeps only `CronSchedule`, `ParseCronSchedule` and the definition shape; `LoadCronDefinitions`, `ParseCronDefinition`, `LoadOneOffCronjob` and the frontmatter types go. The cron manager loads routines only, finds a routine by stem only, and drops the same-file coalescing, since every definition now has one schedule. Startup and reload stop warning about cron files. `CheckCronAgent` goes: routines cannot hit its cases, since `routines` on a `sticky: user` or `sticky: slack` agent fail loading.
- **U2. Stop syncing `cron/` folders.** Overlays and the workspace no longer copy `cron/`; easy agents' folders may overlap a folder named `cron`; model-facing text stops listing `cron/` as a runtime folder.
- **U3. One stem rule.** Run names come from `CronStem` alone: routine paths become `<agent> · <schedule>`, any other path stays as written. The origin card and search use the same rule.
- **U4. Rename the page.** Routes `/routines` and `/routines/<stem>`, tab label, page title, palette commands, empty states, search filters `routine:` and `is:routine`, the server's static route and the tab-attention path. Web and Go tests follow.
- **U5. Migrate saved tabs.** Migration 112 rewrites `/cron` and `/cron/…` paths in the saved-tab tables to `/routines…`.
- **U6. Docs and bundled content.** README, CONCEPTS.md, the About page, the component READMEs, and the skills that write cron files (`main-update-cron-or-heartbeat`, `main-convert-agent-to-easy`, `main-managing-and-running-long-running-processes`, `main-create-or-update-agent`) describe routines only.
- **U7. Tests.** Delete tests of cron-file loading; keep shared scheduler behavior (schedule errors, refresh, upcoming times, prompt text) covered through routines. Coverage floor 91.8%.

## Verification

`make lint` and `make test` pass. A routine still runs on schedule, from Run now, and into a global sticky agent's session; a past `cron/daily.md` run still lists and opens; a saved `/cron` tab opens Routines after migration.
