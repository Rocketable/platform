# 0007. Text Goal Loops

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw supports goal loops for configured Slack channels, started from either the `🔁` or `🏁` text trigger. A goal loop is conversation-local: it belongs to a normal managed Slack thread/session rather than to a separate goal-owned session. An authorized top-level app mention creates a fresh managed Slack conversation with the parsed objective as its first model-visible turn, then persists goal state for that conversation. Goal starts inside an existing managed conversation attach to that conversation and its thread-local history. Each goal turn is visibly delivered in the owning thread and continues through that persistent bridge until the goal reaches a terminal state or its turn budget is exhausted. Goal starts may include an optional `checkScript:` completion gate.

## Scope

This ADR governs Slack goal-loop behavior, including trigger grammar, optional completion check scripts, managed-conversation routing, agent selection, existing-conversation starts, persistence, visible delivery, stop and interruption emojis, completion reactions, restart recovery, turn accounting, continuation ordering, and the RocketCode goal-update tool.

## Context

RocketClaw already has managed text conversations, persisted thread routing, durable scheduled messages, and restart recovery for scheduled prompts. Goal loops need stronger semantics than recurring scheduled messages: they have a user-defined objective, turn budget, active or terminal status, and model-facing update tool. Keeping this state in the existing persistent bridge and SQLite state store preserves restart behavior without creating a second scheduler. Keeping goals conversation-local lets humans talk normally in a managed connector conversation, then start goal chasing when ready without moving to a separate goal-owned session.

## Normative Contracts

### Trigger Grammar

- In a configured Slack channel, an allowed app/bot mention starts a goal loop when the text remaining after RocketClaw bot mention stripping begins with `🔁` or `🏁`.
- In an existing managed Slack conversation, an authorized human message starts a goal loop for that existing conversation when its trimmed text begins with `🔁` or `🏁`.
- Slack applies the same connector-local emoji-prefix normalization to goal starts that it applies to other emoji-prefixed starts before entering the shared goal parser, including accepting Slack's `:repeat:` and `:checkered_flag:` transport text for `🔁` and `🏁` in every goal-start location.
- If the target managed conversation already has an active goal, a new goal start must be rejected with a `❗` reaction and a visible message explaining that a goal is already in progress.
- The shared trigger syntax is `(🔁|🏁) [maxTurns: VALUE|maxTurns:VALUE] [checkScript: VALUE|checkScript:VALUE] OBJECTIVE` after connector-local emoji-prefix normalization. Slack text-prefix normalization accepts `:repeat:` and `:checkered_flag:` in place of the emoji token.
- `maxTurns:` is an optional leading Smalltalk-style keyword parameter. It consumes either the value attached immediately after the colon or the next whitespace-delimited value.
- `checkScript:` is an optional leading Smalltalk-style keyword parameter. It consumes either the value attached immediately after the colon, the next whitespace-delimited value, or one quoted command-line string, for example `checkScript:./scripts/check.sh`, `checkScript: ./scripts/check.sh`, `checkScript:"./scripts/check.sh --linter-mode"`, or `checkScript: "./scripts/check.sh --linter-mode"`.
- Omitted `maxTurns:` defaults to `5`.
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

- A top-level goal start creates a fresh managed channel conversation rooted at the app/bot mention message, then starts the goal inside that conversation/session.
- Top-level goal starts use the first agent configured for the mentioned channel in canonical `slack.channels[]`.
- Channel authorization, allowed-user checks, unconfigured-channel ignoring, and mention-stripping behavior remain in force.
- A goal start inside an existing managed text conversation reuses that managed conversation's existing conversation ID, session, output target, root message, and agent.
- The persisted goal objective and first model-visible input are the user's parsed objective text plus current-turn attachments or explicitly shared reference material.

### Slack Binding

- Slack goal starts create or use managed channel threads rooted at app-mention messages and use canonical `slack.channels[]`.
- Slack goal-loop turns use `_Pursuing Goal..._` as their infinite-goal fallback task-card progress placeholder and `_Pursuing Goal (n/m)..._` as their finite-goal fallback task-card progress placeholder, including kickoff turns, human re-steering turns, and automatic continuations. Non-goal fallback task cards use `_Thinking..._`. During the pre-release Plan evaluation governed by ADR 0002, Plan titles are plain text: `Pursuing Goal...`, `Pursuing Goal (n/m)...`, and `Thinking...` without surrounding underscore emphasis markers.

### Implementation Shape

- Goal-loop ownership is connector-neutral after Slack has accepted and authorized the trigger. Persistent bridges and managed-conversation bridge managers use one injected primary text connector API for conversation targets, visible progress, replies, stop markers, completion markers, and restart recovery.
- The runtime injects Slack as the primary text connector binding. Shared bridge code uses that binding for goal-loop and managed-conversation targets, visible progress, replies, stop markers, completion markers, and restart recovery.
- Slack-specific mechanics remain inside the connector binding implementation: Slack timestamps and threads, transport-native reactions, and transport-native progress surfaces.

### State And Turn Accounting

- Goal state is persisted in `<runtime-dir>/state.sqlite3`, keyed by the owning managed-conversation ID. The runtime may store goal state in normalized SQLite tables rather than the legacy aggregate state JSON, as long as the centralized RocketClaw SQLite opener and migration contracts remain preserved.
- Goal state records at least objective, normalized max turns, optional check script, turns used, status, timestamps, optional terminal note, `GoalState.SlackRecipientTeamID`, and `GoalState.SlackRecipientUserID`. SQLite stores those recipient values in `conversation_goals.slack_recipient_team_id` and `conversation_goals.slack_recipient_user_id`.
- Goal statuses are `active`, `complete`, `blocked`, `stopped`, and `budget_exhausted`.
- A managed conversation may run many goals over its lifetime, but only one goal may be active at a time.
- A new goal may start in a managed conversation that has no goal state or whose existing goal is terminal.
- The initial kickoff turn counts as turn `1`.
- Finite goal progress markers display the current visible budget slot as `n/m`, where `m` is `MaxTurns` and `n` is the turn that is about to consume or reuse the next budget-counting slot.
- A finite kickoff turn displays `1/m`.
- A finite automatic continuation displays `TurnsUsed+1/m` before that continuation is accounted.
- A human re-steering turn during an active finite goal displays `TurnsUsed+1/m` but does not increment `TurnsUsed`.
- Automatic continuation turns count against the goal turn budget.
- Human re-steering messages sent during an active goal are processed with goal steering but do not increment `TurnsUsed`.
- After each successful budget-counting goal turn, RocketClaw increments `TurnsUsed` for the active goal before deciding whether to continue.
- When `MaxTurns > 0 && TurnsUsed >= MaxTurns`, RocketClaw marks the goal `budget_exhausted` and does not enqueue another continuation.
- When `MaxTurns == 0`, RocketClaw does not stop the loop for turn budget.

### Continuation And Stop Semantics

- Goal continuations are owned by the persistent bridge for the managed connector conversation.
- Goal continuations are not implemented as recurring scheduled messages.
- Every goal-loop kickoff, human re-steering turn, and automatic continuation turn must be delivered as a visible assistant turn in the owning text conversation. Goal continuation turns must not run as silent internalization-only turns.
- An active goal continuation prompt must include the objective and current turn-budget state, and must instruct the agent to keep making progress until it can report progress, mark the goal complete, or mark the goal blocked. When a check script exists, the prompt must include the check command and explain that declaring `complete` runs it, and that check failure means the agent must use the failure output to continue working instead of declaring done.
- RocketClaw injects a persistent-bridge goal-update tool while an active goal exists for the current conversation.
- RocketClaw may inject the `ask_user_question` tool during human goal kickoff and human re-steering turns when the originating Slack message qualifies under ADR 0002. Automatic goal continuations and restart recovery continuations must not receive `ask_user_question`.
- The goal-update tool lets the agent report status `progress`, `complete`, or `blocked`, with an optional `note`.
- The goal-update tool's `note` field is the structured explanatory field for status notes, what is going on, what changed, what the agent is thinking, where the goal is heading next, what was completed, and what is blocking progress. The `note` should mirror the substance of the visible `Progress summary:` section.
- `progress` is non-terminal: it records the latest note on the active goal, keeps persisted status `active`, does not run check scripts, does not consume an extra turn by itself, and does not enqueue extra work by itself.
- `complete` and `blocked` are terminal tool statuses and record their final note on the goal.
- When an active goal has a check script, `complete` first validates the stored check script again, checks the active goal agent's `bash` permission for the whole command subject, and runs the command using RocketCode bash execution behavior.
- A check-script exit code of `0` allows the goal to be marked `complete`.
- A non-zero exit, timeout, execution error, validation failure, or permission denial keeps the goal active and returns the reason and available output to the agent as a normal tool result so the agent can continue working.
- `blocked` does not run check scripts.
- Every visible goal-loop assistant response must end with a `Progress summary:` section. For `progress`, the summary explains what changed this turn, the current state, and the next concrete step. For `complete`, the summary explains what was achieved and any validation or check result. For `blocked`, the summary explains what happened, the concrete blocker, and what human input, access, or decision is needed.
- On later active goal turns, when the active goal has a latest persisted note, RocketClaw injects the latest goal state and note as runtime-authored `developer` context so the agent can tell that the context was injected by RocketClaw. This injected recap must not be represented as a duplicate assistant transcript message.
- A goal marked `complete`, `blocked`, `stopped`, or `budget_exhausted` must not receive automatic continuations.
- An allowed channel user message consisting only of `🛑` or `⏹️` in an active managed conversation must interrupt the current turn. If the interrupted conversation has an active goal, RocketClaw must mark that goal `stopped`.
- A `🛑` or `⏹️` reaction by an allowed channel user on the managed conversation root, any message in the managed conversation, or the active progress surface must interrupt the current turn. If the interrupted conversation has an active goal, RocketClaw must mark that goal `stopped`.
- Interruption must clear queued bridge work and connector buffered/stacked work for the interrupted managed conversation.
- Stop feedback is only a persistent `❗` reaction. RocketClaw must not send explanatory stop text.
- If interruption is requested by sending `🛑` or `⏹️`, RocketClaw must add `❗` to the original turn-start message for the interrupted active turn.
- If interruption is requested by reacting to the active progress surface, RocketClaw must add `❗` to the original turn-start message for the interrupted active turn, not to the progress surface. Normal progress-surface cleanup still applies.
- Goal-loop starts and stops use the configured-channel authorization rule from ADR 0002.
- Human replies already queued for the managed conversation must run before any subsequent automatic goal continuation.
- After a successful human re-steering turn, RocketClaw must enqueue an automatic goal continuation if the goal remains active and its turn budget is not exhausted.

### Completion Reactions

- When a goal reaches `complete`, RocketClaw must add the `✅` connector reaction to the goal conversation root message.
- When a goal reaches `complete`, RocketClaw must also add the `✅` connector reaction to the last connector-visible message in the goal conversation when that message can be identified.
- `blocked`, `stopped`, and `budget_exhausted` do not add the completion reaction.

### Restart Recovery

- Active persisted text goal loops survive RocketClaw restart.
- Startup uses persisted managed-conversation state, output targets, `GoalState.SlackRecipientTeamID`, and `GoalState.SlackRecipientUserID` to ensure the managed bridge exists for each active text goal loop and enqueues one continuation for each active goal. The continuation uses the persisted recipient so its thinking message has the same Slack Plan UI as the kickoff turn.
- Restart recovery does not replay missed turns and does not enqueue more than one startup continuation per active goal.

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
- Persisted state changes must continue using the centralized RocketClaw SQLite opener and must preserve restart recovery and migration of legacy goal state.
- Tests must cover trigger parsing, malformed rejection, agent selection, existing-conversation goal starts, visible connector turn delivery, stop emoji messages and reactions, interruption marker targeting, completion reactions, persistence, restart recovery, budget exhaustion, tool-based terminal statuses, human re-steering turn accounting, and human-reply-before-continuation ordering.
- Tests must cover duplicate active-goal rejection and starting a later goal after the previous goal is terminal.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Added visible Slack-thread delivery for every goal turn, stop emoji messages and reactions, and completion checkmark reactions.
- 2026-06-11: Added `🏁` as an additional Slack goal-loop trigger alongside `🔁`.
- 2026-06-11: Switched social-mode goal-loop agent selection to canonical `channels[]` with legacy `channel_agents` fallback and channel-aware authorization.
- 2026-06-11: Removed live goal-loop agent fallback to legacy `channel_agents`; startup config migration must produce canonical `channels[]` entries.
- 2026-06-11: Switched Slack social-mode goal-loop authorization to the channel-only authorization rule after startup migration.
- 2026-06-16: Removed startup config migration compatibility; social-mode goal-loop agent selection requires canonical `channels[]` entries in loaded config.
- 2026-06-12: Added optional `checkScript:` goal-start parameter, completion-check execution semantics, and ADR 0002 rejection delivery requirements.
- 2026-06-14: Made Slack goal loops conversation-local inside normal managed threads, added existing-thread goal starts, removed `paused`, made human re-steering budget-neutral, and replaced stop-only semantics with active-turn interruption and `❗` marker targeting.
- 2026-06-14: Specified one active goal per managed conversation, duplicate active-goal rejection, and sequential goal reuse after terminal states.
- 2026-06-14: Recast goal-loop contracts as a generic primary text connector contract with Slack and Discord Text bindings for DM, social-mode, threading, progress, interruption, completion reactions, and restart recovery.
- 2026-06-14: Required goal-loop bridge ownership to use one injected connector-neutral primary text API rather than parallel Slack and Discord bridge implementations.
- 2026-06-14: Specified `_Pursuing goal..._` as the primary text connector goal-loop progress marker for Slack placeholders and Discord progress messages.
- 2026-06-14: Specified that each text connector applies connector-local emoji-prefix normalization to goal starts before the shared goal parser, including Slack's `:repeat:` and `:checkered_flag:` transport text, while preserving Slack/Discord Text symmetry.
- 2026-06-15: Changed omitted `maxTurns:` default to `5`, added finite `_Pursuing Goal (n/m)..._` progress markers, required visible `Progress summary:` sections, extended the goal-update tool with non-terminal `progress` and explanatory `note`, and specified developer-context injection of latest active goal notes.
- 2026-06-16: Specified that `ask_user_question` is available for human goal kickoff and re-steering turns only, not automatic or restart-recovery continuations.
- 2026-07-01: Allowed goal-start `maxTurns:` and `checkScript:` values to be attached immediately after the colon as well as separated by whitespace.
- 2026-07-01: Removed Discord Text and terminal goal-loop contracts, leaving Slack as the goal-loop connector binding.
- 2026-07-01: Allowed goal state to move from legacy aggregate state JSON into normalized SQLite state tables while preserving centralized opener, migration, and restart-recovery contracts.
- 2026-07-15: Defined goal-loop starts for authorized users in configured Slack channels and existing managed threads.
- 2026-07-15: Defined the parsed objective, current-turn attachments, and explicitly shared reference material as the initial input for a new goal thread.
- 2026-07-22: Added persisted Slack stream recipient team and user IDs to goal state so automatic and restart continuations use the same Plan thinking UI as human goal turns. Defined plain-text Plan titles without underscore emphasis markers while retaining existing fallback task-card titles.
