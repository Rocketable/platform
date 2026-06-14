# 0007. Text Goal Loops

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw supports goal loops for the enabled primary text connector, started from either the `🔁` or `🏁` text trigger. A goal loop is conversation-local: it belongs to a normal managed connector conversation/session rather than to a separate goal-owned thread/session. Top-level DM and social-mode goal starts create or reuse the normal managed connector conversation first, then persist goal state for that managed conversation. Goal starts inside an existing managed text conversation attach the goal to that existing conversation/session. Each goal turn is visibly delivered in the owning managed connector conversation and continues through that persistent bridge until the goal reaches a terminal state or its turn budget is exhausted. Goal starts may include an optional `checkScript:` completion gate.

## Scope

This ADR governs primary text connector goal-loop behavior, including trigger grammar, optional completion check scripts, managed-conversation routing, agent selection, existing-conversation starts, persistence, visible connector delivery, stop and interruption emojis, completion reactions, restart recovery, turn accounting, continuation ordering, the RocketCode goal-update tool, and connector-specific bindings.

## Context

RocketClaw already has managed text conversations, persisted thread routing, durable scheduled messages, and restart recovery for scheduled prompts. Goal loops need stronger semantics than recurring scheduled messages: they have a user-defined objective, turn budget, active or terminal status, and model-facing update tool. Keeping this state in the existing persistent bridge and SQLite state store preserves restart behavior without creating a second scheduler. Keeping goals conversation-local lets humans talk normally in a managed connector conversation, then start goal chasing when ready without moving to a separate goal-owned session.

## Normative Contracts

### Trigger Grammar

- In a text connector DM, a configured-human message starts a goal loop when its trimmed text begins with `🔁` or `🏁`.
- In text connector social mode, an allowed app/bot mention starts a goal loop when the text remaining after RocketClaw bot mention stripping begins with `🔁` or `🏁`.
- In an existing managed text conversation, an authorized human message starts a goal loop for that existing conversation when its trimmed text begins with `🔁` or `🏁`.
- If the target managed conversation already has an active goal, a new goal start must be rejected with a `❗` reaction and a visible message explaining that a goal is already in progress.
- The trigger syntax is `(🔁|🏁) [maxTurns: VALUE] [checkScript: VALUE] OBJECTIVE`.
- `maxTurns:` is an optional leading Smalltalk-style keyword parameter. It consumes the next whitespace-delimited value.
- `checkScript:` is an optional leading Smalltalk-style keyword parameter. It consumes either the next whitespace-delimited value or one quoted command-line string, for example `checkScript: ./scripts/check.sh` or `checkScript: "./scripts/check.sh --linter-mode"`.
- Omitted `maxTurns:` defaults to `20`.
- Omitted `checkScript:` means completion is agent-declared with no script gate.
- Accepted infinite values are `0`, `-1`, and case-insensitive `infinite`; all are normalized to persisted `MaxTurns: 0`.
- Positive integer `maxTurns:` values are persisted as written.
- Values below `-1`, non-integer values other than `infinite`, missing `maxTurns:` values, missing or empty `checkScript:` values, malformed `checkScript:` quoting, impermissible `checkScript:` commands, and empty objectives must be rejected with a helpful connector conversation reply and must not start or persist a goal loop.
- `maxTurns:` and `checkScript:` appearing after non-parameter objective text are part of the objective, not parameters.
- If surfaced to humans, infinite is reported as `maxTurns: 0`.
- Rejected goal starts must obey ADR 0002: if connector-visible progress/final-response state was already reserved before the rejection, the rejection text consumes the reserved connector final-response machinery.

### Check Scripts

- A `checkScript:` value is a bash-style command line constrained to exactly one safe simple command.
- Accepted check commands have one top-level shell statement, one call expression, no assignments, no redirects, a static executable first word, and static literal arguments only. Quoted arguments are allowed when they are just strings.
- Check commands must not contain command lists, pipelines, background execution, subshells, command substitution, process substitution, parameter expansion, arithmetic expansion, glob expansion, brace expansion, redirects, assignments, or shell execution inside arguments.
- The check executable must resolve to an executable workspace-local file, and the resolved path must stay inside the workspace.
- External interpreter trampolines such as `bash -c ...` are rejected because the first command word must be a workspace-local executable file.
- The whole rendered simple command subject, including arguments, must be allowed by the active goal agent's `bash` permission. A permission match for one argument set does not allow a different argument set.
- The trusted workspace script contents are outside the connector `checkScript:` shape guardrail; the workspace executable may run multiple commands internally.

### Conversation Creation And Agent Selection

- A top-level DM goal start creates or reuses a normal managed DM conversation rooted at the triggering DM message, then starts the goal inside that conversation/session. DM goal starts use agent `main`.
- A top-level social-mode goal start creates or uses a normal managed channel conversation rooted at the app/bot mention message, then starts the goal inside that conversation/session.
- Social-mode goal starts use the agent configured for the mentioned channel in the enabled connector's canonical social-mode channel mapping.
- Social-mode channel-only authorization, allowed-user checks, unconfigured-channel ignoring, and existing mention-stripping behavior remain in force.
- A goal start inside an existing managed text conversation reuses that managed conversation's existing conversation ID, session, output target, root message, and agent.
- The persisted goal objective is the user's parsed objective text. Social-mode kickoff prompts may include recent channel context, but that contextual wrapper is not the persisted objective.

### Connector Bindings

- Slack DM goal starts create normal managed Slack DM threads rooted at the triggering DM message. Slack social-mode goal starts create or use normal managed Slack channel threads rooted at app-mention messages. Slack social-mode goal starts use canonical `slack.social_mode.channels[]`; runtime connector behavior never consults legacy `slack.social_mode.channel_agents`.
- Discord DM goal starts create or reuse normal managed Discord DM conversations without guild-thread mechanics. Discord social-mode goal starts create or use normal managed Discord guild-thread conversations rooted at bot-mention messages. Discord social-mode goal starts use canonical `discord_text.social_mode.channels[]`.

### Implementation Shape

- Goal-loop ownership is connector-neutral after the enabled primary text connector has accepted and authorized the trigger. Persistent bridges and managed-conversation bridge managers use one injected primary text connector API for conversation targets, visible progress, replies, stop markers, completion markers, summaries, and restart recovery.
- The runtime must inject exactly one primary text connector binding, Slack or Discord Text, according to configuration. Shared bridge code must not contain two parallel Slack and Discord implementations for the same goal-loop or managed-conversation operation.
- Slack-specific and Discord-specific mechanics remain inside the connector binding implementations: Slack timestamps and threads, Discord channel/thread/message IDs, transport-native reactions, and transport-native progress surfaces.

### State And Turn Accounting

- Goal state is persisted in `<runtime-dir>/state.sqlite3` as part of RocketClaw's existing state JSON, keyed by the owning managed-conversation ID.
- Goal state records at least objective, normalized max turns, optional check script, turns used, status, timestamps, and optional terminal note.
- Goal statuses are `active`, `complete`, `blocked`, `stopped`, and `budget_exhausted`.
- A managed conversation may run many goals over its lifetime, but only one goal may be active at a time.
- A new goal may start in a managed conversation that has no goal state or whose existing goal is terminal.
- The initial kickoff turn counts as turn `1`.
- Automatic continuation turns count against the goal turn budget.
- Human re-steering messages sent during an active goal are processed with goal steering but do not increment `TurnsUsed`.
- After each successful budget-counting goal turn, RocketClaw increments `TurnsUsed` for the active goal before deciding whether to continue.
- When `MaxTurns > 0 && TurnsUsed >= MaxTurns`, RocketClaw marks the goal `budget_exhausted` and does not enqueue another continuation.
- When `MaxTurns == 0`, RocketClaw does not stop the loop for turn budget.

### Continuation And Stop Semantics

- Goal continuations are owned by the persistent bridge for the managed connector conversation.
- Goal continuations are not implemented as recurring scheduled messages.
- Every goal-loop kickoff, human re-steering turn, and automatic continuation turn must be delivered as a visible assistant turn in the owning text conversation. Goal continuation turns must not run as silent internalization-only turns.
- An active goal continuation prompt must include the objective and current turn-budget state, and must instruct the agent to keep making progress until it can mark the goal complete or blocked. When a check script exists, the prompt must include the check command and explain that declaring `complete` runs it, and that check failure means the agent must use the failure output to continue working instead of declaring done.
- RocketClaw injects a persistent-bridge goal-update tool while an active goal exists for the current conversation.
- The goal-update tool lets the agent set status to `complete` or `blocked`, with an optional note.
- When an active goal has a check script, `complete` first validates the stored check script again, checks the active goal agent's `bash` permission for the whole command subject, and runs the command using RocketCode bash execution behavior.
- A check-script exit code of `0` allows the goal to be marked `complete`.
- A non-zero exit, timeout, execution error, validation failure, or permission denial keeps the goal active and returns the reason and available output to the agent as a normal tool result so the agent can continue working.
- `blocked` does not run check scripts.
- A goal marked `complete`, `blocked`, `stopped`, or `budget_exhausted` must not receive automatic continuations.
- A configured-human DM message, or an allowed social-mode user message, consisting only of `🛑` or `⏹️` in an active managed conversation must interrupt the current turn. If the interrupted conversation has an active goal, RocketClaw must mark that goal `stopped`.
- A `🛑` or `⏹️` reaction by the configured human in DM mode, or by an allowed social-mode user in social mode, on the managed conversation root, any message in the managed conversation, or the active progress surface must interrupt the current turn. If the interrupted conversation has an active goal, RocketClaw must mark that goal `stopped`.
- Interruption must clear queued bridge work and connector buffered/stacked work for the interrupted managed conversation.
- Stop feedback is only a persistent `❗` reaction. RocketClaw must not send explanatory stop text.
- If interruption is requested by sending `🛑` or `⏹️`, RocketClaw must add `❗` to the original turn-start message for the interrupted active turn.
- If interruption is requested by reacting to the active progress surface, RocketClaw must add `❗` to the original turn-start message for the interrupted active turn, not to the progress surface. Normal progress-surface cleanup still applies.
- Social-mode goal-loop starts and stops use the channel-only authorization rule from ADR 0002.
- Human replies already queued for the managed conversation must run before any subsequent automatic goal continuation.
- After a successful human re-steering turn, RocketClaw must enqueue an automatic goal continuation if the goal remains active and its turn budget is not exhausted.

### Completion Reactions

- When a goal reaches `complete`, RocketClaw must add the `✅` connector reaction to the goal conversation root message.
- When a goal reaches `complete`, RocketClaw must also add the `✅` connector reaction to the last connector-visible message in the goal conversation when that message can be identified.
- `blocked`, `stopped`, and `budget_exhausted` do not add the completion reaction.

### Restart Recovery

- Active persisted text goal loops survive RocketClaw restart.
- Startup uses persisted managed-conversation state and output targets to ensure the managed bridge exists for each active text goal loop and enqueues one continuation for each active goal.
- Restart recovery does not replay missed turns and does not enqueue more than one startup continuation per active goal.
- Terminal goals do not restart.

## Non-Goals

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

- The implementation must keep connector trigger parsing separate from persistent bridge continuation ownership.
- Persisted state changes must continue using the centralized RocketClaw SQLite opener and existing state JSON path.
- Tests must cover trigger parsing, malformed rejection, agent selection, existing-conversation goal starts, visible connector turn delivery, stop emoji messages and reactions, interruption marker targeting, completion reactions, persistence, restart recovery, budget exhaustion, tool-based terminal statuses, human re-steering turn accounting, and human-reply-before-continuation ordering.
- Tests must cover duplicate active-goal rejection and starting a later goal after the previous goal is terminal.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Added visible Slack-thread delivery for every goal turn, stop emoji messages and reactions, and completion checkmark reactions.
- 2026-06-11: Added `🏁` as an additional Slack goal-loop trigger alongside `🔁`.
- 2026-06-11: Switched social-mode goal-loop agent selection to canonical `channels[]` with legacy `channel_agents` fallback and channel-aware authorization.
- 2026-06-11: Removed live goal-loop agent fallback to legacy `channel_agents`; startup config migration must produce canonical `channels[]` entries.
- 2026-06-11: Switched Slack social-mode goal-loop authorization to the channel-only authorization rule after startup migration.
- 2026-06-12: Added optional `checkScript:` goal-start parameter, completion-check execution semantics, and ADR 0002 rejection delivery requirements.
- 2026-06-14: Made Slack goal loops conversation-local inside normal managed threads, added existing-thread goal starts, removed `paused`, made human re-steering budget-neutral, and replaced stop-only semantics with active-turn interruption and `❗` marker targeting.
- 2026-06-14: Specified one active goal per managed conversation, duplicate active-goal rejection, and sequential goal reuse after terminal states.
- 2026-06-14: Recast goal-loop contracts as a generic primary text connector contract with Slack and Discord Text bindings for DM, social-mode, threading, progress, interruption, completion reactions, and restart recovery.
- 2026-06-14: Required goal-loop bridge ownership to use one injected connector-neutral primary text API rather than parallel Slack and Discord bridge implementations.
