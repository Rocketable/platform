# 0002. Behavior Contracts

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw preserves a small set of behavior contracts that are more important than internal code shape. Refactors must preserve these contracts unless the human partner explicitly approves a spec change.

## Scope

This ADR governs regression-sensitive runtime behavior: message flow, prompt framing, command interpolation, routing, delivery, restart, and safety boundaries.

## Context

RocketClaw has lost features when refactors treated behavior as removable plumbing. These contracts make the product behavior explicit so code size pressure and cleanup work do not erase capabilities.

## Normative Contracts

### Prompt Shell Interpolation

| Source                                                         | ``!`cmd` `` expands? | Rationale                                                                |
|----------------------------------------------------------------|----------------------|--------------------------------------------------------------------------|
| Primary agent prompts                                          | Yes                  | Trusted workspace prompt source.                                         |
| Subagent prompts                                               | Yes                  | Trusted workspace prompt source.                                         |
| Skill contents loaded by the skill tool                        | Yes                  | Trusted workspace prompt source.                                         |
| Raw/cron prompt input                                          | Yes                  | Cron bodies are trusted workspace files sent through raw runs.           |
| Slack/Discord text/Discord voice/browser/MCP human input in the persistent bridge | No                   | External/human input must remain literal.                                |
| Scheduled-message prompt text in the persistent bridge         | No                   | It follows persistent bridge input rules unless explicitly reclassified. |
| `AGENTS.md` workspace instructions                             | No                   | Root instructions are loaded literally.                                  |

Expansion uses RocketCode semantics: pattern ``!`command` ``, workspace-root cwd, stdout insertion only, and command failures do not fail prompt preparation.

### Message Flow

- Shared inbound messages are queued through the event bus and consumed by the main bridge.
- Automated inbound messages honor `minimum_wait_after_human_interaction` before processing.
- Slack stacked messages must preserve prompt order and avoid duplicated deliveries.
- Inbound attachment handling must share one semantic path across Slack and external MCP after source-specific acquisition. Supported images are passed to RocketCode as attachments. Text attachments are appended to the user prompt as literal text within configured size limits. Unsupported, empty, oversized, or inaccessible attachments produce attachment warnings and existing fallback behavior without enabling prompt shell interpolation.
- Slack social-mode-gated channel thread replies that contain Slack-resolved direct pings to another user or bot, broadcast target, or user group must be skipped silently unless the same message also contains the RocketClaw bot mention. Slack channel references do not trigger this skip. Skipped replies must not create placeholders, reactions, connector replies, attachment processing, or thread-router submissions. Raw unresolved `@word` text and non-pinging Slack markup such as dates do not trigger this skip. Emergency safe words are checked before this skip.
- Every normal Slack-visible assistant turn with a Slack target reserves its Slack reply location up front by posting a thinking placeholder (`_Thinking..._`) followed by an answer placeholder (`\u200B`). Slack placeholder pairs have one semantic and one implementation path: once reserved, the pair must be consumed through the normal Slack final-response machinery. Error, rejection, and abort text after reservation is final response content for the reserved turn. The final-response machinery updates the answer placeholder for short final content, deletes the answer placeholder before chunked final replies, deletes placeholders when there is no final content, deletes the thinking placeholder, and clears pending placeholder state. Post-reservation branches must not bypass this path with side-channel thread replies, ad hoc placeholder deletion, or abandoned pending placeholder state. Pre-reservation validation rejections may still use direct connector replies because no placeholder pair exists yet. Intentionally standalone progress/post-text messages are not assistant-turn final answers and do not consume the reserved answer placeholder.
- Slack thinking placeholder updates render accumulated RocketCode progress as a quote block with the newest progress line first, while preserving chronological accumulation internally.
- Slack-visible RocketCode subagent progress diagnostics include a stable per-dispatch ordinal immediately after `subagent`, formatted as `(n/total)`, including `(1/1)` when a model response dispatches exactly one subagent task.
- Slack goal-loop automatic continuations are queued through the owning managed-thread persistent bridge and must be delivered as visible Slack assistant turns in the owning Slack thread. Human replies already queued for that managed thread must run before any subsequent automatic goal continuation.
- Discord and browser voice transcriptions enter the same shared flow as other main-session input.
- External MCP conversations are isolated by external conversation ID; omitted ID starts a new isolated conversation.
- External MCP blocking replies return the same outbound response attachments that the persistent bridge publishes through connector delivery. RocketCode-produced response attachments use one shared internal carrier before each edge adapts them to connector upload or MCP result content.
- When a local-only `agents/guardrail.md` is present, every RocketCode `task` delegation prompt is filtered before the child agent runs, and every child agent final response is filtered before the task result is returned to the caller agent.
- Guardrail rejections do not run the rejected child prompt or expose the rejected child response; the guardrail reason is returned through the task result so the caller agent can continue from the rejection.

### Routing And Delivery

- Main conversation output targets are controlled by app wiring, not by individual input sources.
- The primary text output target is configured as either Slack DM or Discord text, never both.
- Slack response-rooted threads remain isolated from main until summarized.
- Slack response-rooted threads and explicitly pre-seeded managed threads seed inherited main-session context from the latest available compaction point when one exists; if no compaction point exists, they may compact the full selected main-session history.
- Slack DM `🔁`/`🏁` goal-loop prompts and Slack social-mode `@BotName 🔁`/`@BotName 🏁` goal-loop mentions open managed Slack threads, persist goal state by managed-thread conversation ID, and use ADR 0007 trigger grammar, agent selection, turn-budget, and terminal-status semantics.
- Slack social-mode app mentions, managed thread replies, goal-loop starts/stops, summary reactions, and channel cron rerun reactions must authorize users with the configured channel's per-channel allowed-user list.
- Slack goal loops must stop when an authorized human sends `🛑` or `⏹️` as a message in the active goal thread, or adds either emoji as a reaction to the goal thread root or any message in the active goal thread.
- Slack goal loops that reach `complete` must add a `✅` reaction to the goal thread root and to the last Slack message in the goal thread when that message can be identified.
- Discord text managed threads remain isolated from main until summarized, matching Slack managed-thread semantics where Discord guild threads can express them.
- Slack thread replies use persisted checkpoints when available; older responses without checkpoints receive an explanatory thread reply instead of silently losing context.
- Discord text replies to checkpointed assistant messages can start response-rooted guild threads with inherited context. Discord DMs do not provide thread semantics.
- Discord text responses are delivered to the originating Discord channel or thread when a Discord reply target exists; otherwise they are delivered to the configured Discord text channel.
- Cron final verbatim output with `channel` starts a managed Slack or Discord Text channel thread according to the enabled primary text connector; otherwise cron output is internalized into the main session as configured by the cron path. `slack-channel` remains a backward-compatible alias. Replies and summaries for cron-created connector channel threads follow existing connector gates.
- Slack and Discord Text maintain parity for repeat-reaction one-off cron reruns. Both connectors accept deterministic top-level targets such as `:repeat_one: daily`, `🔂 daily`, whole-message `daily` or `daily.md`, and scheduled cron thread roots containing `cron/daily.md`; `cron/foo.md` is normalized to `foo` before `LoadOneOffCronjob`, which remains the final validation gate.
- Slack DM repeat reactions by the configured human may run any top-level cron. Slack channel repeat reactions require social mode authorization and may only run cronjobs whose configured `channel` targets the reacted Slack channel. Discord Text repeat reactions by the configured human may only run cronjobs whose configured `channel` targets the reacted Discord channel. Invalid, ambiguous, unauthorized, or wrong-channel reactions must not run; otherwise-authorized invalid requests receive a helpful connector-thread reply.
- Raw cron runs must call `rocketclaw_i_want_human_partner_to_see_this`; normal assistant replies do not complete the background run.
- MCP result rendering must preserve the text reply as the first content item and expose response attachments through protocol-native content plus structured attachment data without changing Slack delivery behavior.

### Restart And Draining

- `rocketclaw_restart` is for explicit runtime configuration changes such as `rocketclaw.json`, `agents/`, `skills/`, or `cron/` changes.
- Restart and signal-triggered shutdown must stop cron from starting new jobs, wait for already-started cron jobs to finish, wait for inbound handoff and main/thread bridge idleness, stop inbound and bridges, wait for outbound drain, stop connectors, and preserve pending restart notifications. This sequence has no timeout.
- Restart recovery must rehydrate active persisted Slack goal loops by starting their managed thread bridges and queuing one continuation per active goal, without replaying missed turns.
- Restart must not be triggered for ordinary memory, ledger, audit, report, source-code, generated artifact, log, transcript, or data-file edits.

### Permissions And Tools

- Task permission defaults must not become permissive by accident.
- Agent `maxRecursion` budgets are stricter than `task` permission grants; a permitted task target remains unavailable once the active inference's recursion budget is exhausted.
- Agent-system safety linting and graph inspection for permissions, delegation graphs, suppressions, and write-to-execute risk are governed by ADR 0006.
- Cron agents may selectively deny tools.
- The inter-agent guardrail agent may use tools only when its own `permission` frontmatter allows those tools.
- RocketClaw tools are part of runtime behavior and must remain visible to RocketCode according to the bridge mode that owns the turn.
- The Slack goal-loop update tool is a persistent-bridge tool visible only for conversations with an active goal, and it may only set the active goal to `complete`, `blocked`, or `paused` with an optional note. Human stop emoji behavior may set the goal to `stopped` without using the tool.

## Non-Goals

- This ADR does not specify exact implementation structure.
- This ADR does not require tests for deleted internals; tests should cover current observable contracts.
- This ADR does not make external human input executable.

## Evidence

- `internal/rocketclaw/harnessbridge/bridge.go`
- `internal/rocketclaw/harnessbridge/raw_run.go`
- `internal/rocketcode/prompts.go`
- `internal/rocketcode/looper.go`
- `internal/cronjob/manager.go`
- `internal/slackconnector/connector.go`
- `internal/app/thread_bridges.go`
- `internal/events/bus.go`
- `internal/rocketclaw/harnessbridge/bridge_test.go`
- `internal/rocketclaw/harnessbridge/raw_run_test.go`

## Consequences

- Behavior-preserving refactors must verify queue order, prompt framing, delivery/silence, and routing separately.
- Code deletion is acceptable only when the surviving code still satisfies these contracts.
- If a bug reveals a behavior worth preserving, the human partner can promote it into this ADR before or during the fix.

## Changelog

- 2026-05-25: Initial accepted snapshot.
- 2026-05-25: Added Slack reply placeholder-pair reservation contract for normal Slack-visible assistant turns.
- 2026-06-02: Specified managed continuation and summary behavior for cron `slack-channel` threads.
- 2026-06-02: Added Discord text routing and managed-thread behavior as the mutually exclusive primary text alternative to Slack.
- 2026-06-02: Renamed cron managed Slack thread routing to canonical `channel`, with `slack-channel` retained as a backward-compatible alias.
- 2026-06-02: Added Slack and Discord Text parity and channel-target authorization for repeat-reaction one-off cron reruns.
- 2026-06-04: Added silent Slack social-mode channel-thread reply suppression for messages pinging others unless the RocketClaw bot is also mentioned.
- 2026-06-07: Specified that restart and signal-triggered shutdown share the same graceful drain sequence and configured timeout.
- 2026-06-08: Specified that inherited main-session thread seeding reuses the latest available compaction point before compacting selected history, while preserving full-history fallback when no compaction exists.
- 2026-06-08: Specified Slack-visible subagent progress diagnostic ordinals for identifying concurrent duplicate subagent task calls.
- 2026-06-09: Excluded Slack channel references from silent Slack social-mode channel-thread reply suppression.
- 2026-06-09: Specified newest-first rendering for Slack thinking quote-block progress updates.
- 2026-06-09: Removed the graceful shutdown timeout and specified the no-timeout shutdown order.
- 2026-06-10: Specified that `maxRecursion` subdelegation budgets override otherwise-permitted `task` grants when exhausted.
- 2026-06-10: Added local-only guardrail filtering for RocketCode task delegation prompts and child final responses.
- 2026-06-11: Linked agent-system safety linting to ADR 0006.
- 2026-06-11: Linked agent-system graph inspection to ADR 0006.
- 2026-06-11: Added Slack goal-loop routing, continuation ordering, restart recovery, and goal-update tool contracts governed by ADR 0007.
- 2026-06-11: Added visible Slack-thread delivery, stop emoji controls, and completion checkmark reactions for Slack goal loops.
- 2026-06-11: Added `🏁` as an additional Slack goal-loop trigger alongside `🔁`.
- 2026-06-11: Added channel-aware Slack social-mode authorization for social actions and cron rerun reactions.
- 2026-06-11: Removed runtime fallback to top-level Slack social-mode allowed users; social-mode authorization is channel-only after startup migration.
- 2026-06-12: Specified universal post-reservation Slack placeholder consumption through the normal final-response path.
- 2026-06-12: Specified shared Slack and external MCP inbound attachment semantics, including literal text attachment prompt conversion.
- 2026-06-12: Specified shared outbound response attachment semantics for connector delivery and external MCP blocking replies.
