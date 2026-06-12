# 0007. Slack Goal Loops

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw supports Slack-only goal loops started from either the `🔁` or `🏁` text trigger. A goal loop starts a managed Slack thread, persists goal state in the existing RocketClaw state store, visibly runs each turn in that Slack thread, and continues through the owning persistent bridge until the goal is marked stopped or its turn budget is exhausted.

## Scope

This ADR governs Slack DM and Slack social-mode goal-loop behavior, including trigger grammar, managed-thread routing, agent selection, persistence, visible thread delivery, stop emojis, completion reactions, restart recovery, turn accounting, continuation ordering, and the RocketCode goal-update tool.

## Context

RocketClaw already has managed Slack threads, persisted thread routing, durable scheduled messages, and restart recovery for scheduled prompts. Goal loops need stronger semantics than recurring scheduled messages: they have a user-defined objective, turn budget, active or terminal status, and model-facing stop tool. Keeping this state in the existing persistent bridge and SQLite state store preserves restart behavior without creating a second scheduler.

## Normative Contracts

### Trigger Grammar

- In Slack DM, a configured-human message starts a goal loop when its trimmed text begins with `🔁` or `🏁`.
- In Slack social mode, an allowed app mention starts a goal loop when the text remaining after RocketClaw bot mention stripping begins with `🔁` or `🏁`.
- The trigger syntax is `(🔁|🏁) [maxTurns: VALUE] OBJECTIVE`.
- `maxTurns:` is an optional leading Smalltalk-style keyword parameter. It consumes the next whitespace-delimited value.
- Omitted `maxTurns:` defaults to `20`.
- Accepted infinite values are `0`, `-1`, and case-insensitive `infinite`; all are normalized to persisted `MaxTurns: 0`.
- Positive integer `maxTurns:` values are persisted as written.
- Values below `-1`, non-integer values other than `infinite`, missing `maxTurns:` values, and empty objectives must be rejected with a helpful Slack thread reply and must not start a goal loop.
- `maxTurns:` appearing after non-parameter objective text is part of the objective, not a parameter.
- If surfaced to humans, infinite is reported as `maxTurns: 0`.

### Thread Creation And Agent Selection

- A Slack DM goal loop opens a managed Slack thread rooted at the triggering DM message.
- Slack DM goal loops use agent `main`.
- A Slack social-mode goal loop opens a managed Slack channel thread rooted at the app-mention message.
- Slack social-mode goal loops use the agent configured for the mentioned channel in canonical `slack.social_mode.channels[]`; runtime connector behavior never consults legacy `slack.social_mode.channel_agents`.
- Slack social-mode channel-only authorization, allowed-user checks, unconfigured-channel ignoring, and existing mention-stripping behavior remain in force.
- The persisted goal objective is the user's parsed objective text. Social-mode kickoff prompts may include recent channel context, but that contextual wrapper is not the persisted objective.

### State And Turn Accounting

- Goal state is persisted in `<runtime-dir>/state.sqlite3` as part of RocketClaw's existing state JSON, keyed by the owning managed-thread conversation ID.
- Goal state records at least objective, normalized max turns, turns used, status, timestamps, and optional terminal note.
- Goal statuses are `active`, `complete`, `blocked`, `paused`, `stopped`, and `budget_exhausted`.
- The initial kickoff turn counts as turn `1`.
- After each successful goal turn, RocketClaw increments `TurnsUsed` for the active goal before deciding whether to continue.
- When `MaxTurns > 0 && TurnsUsed >= MaxTurns`, RocketClaw marks the goal `budget_exhausted` and does not enqueue another continuation.
- When `MaxTurns == 0`, RocketClaw does not stop the loop for turn budget.

### Continuation And Stop Semantics

- Goal continuations are owned by the persistent bridge for the managed Slack thread.
- Goal continuations are not implemented as recurring scheduled messages.
- Every goal-loop kickoff and automatic continuation turn must be delivered as a visible Slack assistant turn in the owning Slack thread. Goal continuation turns must not run as silent internalization-only turns.
- An active goal continuation prompt must include the objective and current turn-budget state, and must instruct the agent to keep making progress until it can mark the goal complete, blocked, or paused.
- RocketClaw injects a persistent-bridge goal-update tool while an active goal exists for the current conversation.
- The goal-update tool lets the agent set status to `complete`, `blocked`, or `paused`, with an optional note.
- A goal marked `complete`, `blocked`, `paused`, `stopped`, or `budget_exhausted` must not receive automatic continuations.
- A configured-human Slack DM message, or an allowed Slack social-mode user message, consisting only of `🛑` or `⏹️` in an active goal thread must mark that goal `stopped` and stop the loop.
- A `🛑` or `⏹️` Slack reaction by the configured human in DM mode, or by an allowed Slack social-mode user in social mode, on the active goal thread root or any message in the active goal thread must mark that goal `stopped` and stop the loop.
- Slack social-mode goal-loop starts and stops use the channel-only authorization rule from ADR 0002.
- Human replies already queued for the managed thread must run before any subsequent automatic goal continuation.

### Completion Reactions

- When a goal reaches `complete`, RocketClaw must add the `✅` Slack reaction to the goal thread root message.
- When a goal reaches `complete`, RocketClaw must also add the `✅` Slack reaction to the last Slack message in the goal thread when that message can be identified.
- `blocked`, `paused`, `stopped`, and `budget_exhausted` do not add the completion reaction.

### Restart Recovery

- Active persisted Slack goal loops survive RocketClaw restart.
- Startup ensures the managed thread bridge exists for each active Slack goal loop and enqueues one continuation for each active goal.
- Restart recovery does not replay missed turns and does not enqueue more than one startup continuation per active goal.
- Terminal goals do not restart.

## Non-Goals

- This ADR does not add Discord text goal loops.
- This ADR does not add reaction-based goal-loop starts.
- This ADR does not add a runtime config knob for default max turns.
- This ADR does not create a separate SQLite database or scheduler.
- This ADR does not define human pause, resume, clear, or status commands beyond the stop emoji behavior and the model-facing goal-update tool.
- This ADR does not add a separate judge model; termination is agent/tool-driven plus turn budget.

## Evidence

- `internal/rocketclaw/slackconnector/connector.go`
- `internal/rocketclaw/app/thread_bridges.go`
- `internal/rocketclaw/harnessbridge/bridge.go`
- `internal/rocketclaw/harnessbridge/store.go`
- `internal/rocketclaw/docs/adr/0001-runtime-capabilities.md`
- `internal/rocketclaw/docs/adr/0002-behavior-contracts.md`
- `internal/rocketclaw/docs/adr/0003-configuration-state-and-operations.md`
- `internal/rocketclaw/docs/adr/0004-rocketcode-contract.md`
- `internal/rocketclaw/docs/adr/0005-sqlite-state-store.md`

## Consequences

- The implementation must keep Slack connector trigger parsing separate from persistent bridge continuation ownership.
- Persisted state changes must continue using the centralized RocketClaw SQLite opener and existing state JSON path.
- Tests must cover trigger parsing, malformed rejection, agent selection, visible Slack-thread turn delivery, stop emoji messages and reactions, completion reactions, persistence, restart recovery, budget exhaustion, tool-based terminal statuses, and human-reply-before-continuation ordering.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Added visible Slack-thread delivery for every goal turn, stop emoji messages and reactions, and completion checkmark reactions.
- 2026-06-11: Added `🏁` as an additional Slack goal-loop trigger alongside `🔁`.
- 2026-06-11: Switched social-mode goal-loop agent selection to canonical `channels[]` with legacy `channel_agents` fallback and channel-aware authorization.
- 2026-06-11: Removed live goal-loop agent fallback to legacy `channel_agents`; startup config migration must produce canonical `channels[]` entries.
- 2026-06-11: Switched Slack social-mode goal-loop authorization to the channel-only authorization rule after startup migration.
