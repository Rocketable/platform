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
- Every normal Slack-visible assistant turn with a Slack target reserves its Slack reply location up front by posting a thinking placeholder (`_Thinking..._`) followed by an answer placeholder (`\u200B`). The answer placeholder is later updated for a short final answer or deleted before chunked final replies. Intentionally standalone progress/post-text messages are not assistant-turn final answers and do not consume the reserved answer placeholder.
- Discord and browser voice transcriptions enter the same shared flow as other main-session input.
- External MCP conversations are isolated by external conversation ID; omitted ID starts a new isolated conversation.

### Routing And Delivery

- Main conversation output targets are controlled by app wiring, not by individual input sources.
- The primary text output target is configured as either Slack DM or Discord text, never both.
- Slack response-rooted threads remain isolated from main until summarized.
- Discord text managed threads remain isolated from main until summarized, matching Slack managed-thread semantics where Discord guild threads can express them.
- Slack thread replies use persisted checkpoints when available; older responses without checkpoints receive an explanatory thread reply instead of silently losing context.
- Discord text replies to checkpointed assistant messages can start response-rooted guild threads with inherited context. Discord DMs do not provide thread semantics.
- Discord text responses are delivered to the originating Discord channel or thread when a Discord reply target exists; otherwise they are delivered to the configured Discord text channel.
- Cron final verbatim output with `channel` starts a managed Slack or Discord Text channel thread according to the enabled primary text connector; otherwise cron output is internalized into the main session as configured by the cron path. `slack-channel` remains a backward-compatible alias. Replies and summaries for cron-created connector channel threads follow existing connector gates.
- Slack and Discord Text maintain parity for repeat-reaction one-off cron reruns. Both connectors accept deterministic top-level targets such as `:repeat_one: daily`, `🔂 daily`, whole-message `daily` or `daily.md`, and scheduled cron thread roots containing `cron/daily.md`; `cron/foo.md` is normalized to `foo` before `LoadOneOffCronjob`, which remains the final validation gate.
- Slack DM repeat reactions by the configured human may run any top-level cron. Slack channel repeat reactions require social mode authorization and may only run cronjobs whose configured `channel` targets the reacted Slack channel. Discord Text repeat reactions by the configured human may only run cronjobs whose configured `channel` targets the reacted Discord channel. Invalid, ambiguous, unauthorized, or wrong-channel reactions must not run; otherwise-authorized invalid requests receive a helpful connector-thread reply.
- Raw cron runs must call `rocketclaw_i_want_human_partner_to_see_this`; normal assistant replies do not complete the background run.

### Restart And Draining

- `rocketclaw_restart` is for explicit runtime configuration changes such as `rocketclaw.json`, `agents/`, `skills/`, or `cron/` changes.
- Restart must stop intake, wait for inbound handoff, wait for main and thread bridge idleness, wait for outbound drain, and preserve pending restart notifications.
- Restart must not be triggered for ordinary memory, ledger, audit, report, source-code, generated artifact, log, transcript, or data-file edits.

### Permissions And Tools

- Task permission defaults must not become permissive by accident.
- Cron agents may selectively deny tools.
- RocketClaw tools are part of runtime behavior and must remain visible to RocketCode according to the bridge mode that owns the turn.

## Non-Goals

- This ADR does not specify exact implementation structure.
- This ADR does not require tests for deleted internals; tests should cover current observable contracts.
- This ADR does not make external human input executable.

## Evidence

- `internal/rocketcodebridge/bridge.go`
- `internal/rocketcodebridge/raw_run.go`
- `vendor/github.com/Rocketable/rocketcode/prompts.go`
- `vendor/github.com/Rocketable/rocketcode/looper.go`
- `internal/cronjob/manager.go`
- `internal/slackconnector/connector.go`
- `internal/app/thread_bridges.go`
- `internal/events/bus.go`
- `internal/rocketcodebridge/bridge_test.go`
- `internal/rocketcodebridge/raw_run_test.go`

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
