---
title: Deployment binary missing migrations already applied to the live database
date: 2026-09-24
category: runtime-errors
module: internal/rocketclaw
problem_type: runtime_error
component: infrastructure
severity: high
symptoms:
  - "The tested permission-fix binary failed to start against the live database."
  - "Startup reported unknown migration in database: 015_conversation_web_unread.sql."
root_cause: missing_workflow_step
resolution_type: workflow_improvement
tags: [rocketclaw, deployment, database-migrations, source-provenance, rollback]
---

# Deployment binary missing migrations already applied to the live database

## Problem

A Bash-permission fix passed local checks but failed when deployed to the running RocketClaw service. The live database already contained migrations from an unread-message feature absent from the fix's source branch. **The PR base and the required deployment base were different.**

This records the September 24, 2026 recovery from OpenCode session `ses_f2e345140ffeX6QBdssHGoZQS0` (session history), not the service's current deployment state.

## Symptoms

The replacement process exited during startup, and its supervisor kept restarting it:

```text
initialize rocketclaw state store: apply rocketclaw state schema migrations: unknown migration in database: 015_conversation_web_unread.sql
```

## What Didn't Work

Installing the tested permission branch directly did not preserve the running service's migration history. Its passing tests established behavior against test databases, not compatibility with the live database. Restarting the same binary could not supply its missing embedded migrations.

## Solution

The recorded recovery was:

1. Restore the exact previously running binary from its backup and confirm service recovery.
2. Find the source containing the deployed unread-message feature and apply a copy of the permission fix on top of it in a separate deployment revision.
3. Run full tests, lint, race tests, and coverage/size checks on that combined build, then deploy it.
4. Confirm an HTTP API request returns 200 and the Slack connector reports connected.

The permission PR remained focused on the fix. The unread-message feature was a dependency of the deployment build, not an additional change to include in that PR. These recovery steps and checks were recorded in the session history.

## Why This Works

RocketClaw embeds migration SQL into each binary (`internal/rocketclaw/backend/store_schema.go:19-20`). Startup reads the database's `pg_migrations` ledger and rejects any applied migration ID absent from that binary's migration set (`internal/rocketclaw/backend/store_schema.go:51-77`). State-store initialization fails as a result (`internal/rocketclaw/backend/store.go:1819-1821`).

Building on the deployed feature baseline supplied the migrations the database already knew. Recognized, already-applied migrations are skipped (`internal/rocketclaw/backend/store_schema.go:86-93`). Recovery preserved that baseline while adding the fix; it did not remove ledger entries or bypass migration validation.

## Prevention

- Identify the running binary's source revision before choosing the deployment base. A PR's base is not evidence of what is running.
- Compare the live migration ledger with the candidate's embedded migrations before replacing the binary. Matching migration IDs addresses this startup failure; it does not prove all application/schema compatibility.
- When the deployed source differs from the PR base, build and verify the fix on the deployed baseline separately from the review branch.
- Keep the known-running binary available during replacement, then check both HTTP service health and Slack connectivity. Restoring that exact binary worked here; arbitrary older binaries are not necessarily compatible after migrations have run.

## Related

- [Production migration inspection](../../plans/2026-09-09-fast-conversation-sidebar.md#production-migration-inspection--2026-09-09) — comparing deployed source and migration ledgers.
- [Startup queue recovery rollout notes](../../plans/2026-09-20-startup-queue-recovery.md#rollout-notes) — compatibility rehearsal and the limits of binary-only rollback.
