# 0004. RocketCode Contract

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw embeds RocketCode as the reasoning runtime through two app-owned construction paths: the persistent conversation bridge and raw runs. RocketClaw owns which prompt sources are trusted, which tools are injected, and how sessions are persisted and replayed.

## Scope

This ADR governs rocketclaw's contract with the embedded `github.com/Rocketable/rocketcode` library. RocketCode's own public API is documented upstream; this ADR records rocketclaw's embedding policy.

## Context

Several rocketclaw capabilities exist only because of precise RocketCode configuration: prompt shell expansion, stronger skills, custom tools, per-agent guardrails, session replay, attachments, and raw cron completion. These settings are product behavior, not incidental config.

## Normative Contracts

### Construction Paths

| Path              | File                                  | Purpose                                                                                | Prompt input expansion |
|-------------------|---------------------------------------|----------------------------------------------------------------------------------------|------------------------|
| Persistent bridge | `internal/rocketclaw/harnessbridge/bridge.go`  | Main, thread, Slack, Discord text, browser, Discord voice, scheduled, and external MCP conversation turns. | `InputPrompts: false`  |
| Raw run           | `internal/rocketclaw/harnessbridge/raw_run.go` | Cron and one-off cron background turns.                                                | `InputPrompts: true`   |

Both paths enable `PrimaryPrompts`, `SubagentPrompts`, and `SkillPrompts` shell expansion. Persistent bridge input text remains literal. Raw input text expands because cron bodies are trusted workspace files. Both paths construct RocketCode with a provider registry that can route provider-qualified OpenAI and Anthropic model requests.

### Provider Selection

- RocketClaw passes OpenAI and Anthropic provider clients into RocketCode when configured.
- Model strings may use `openai/<model>` or `anthropic/<model>` in agent frontmatter and runtime defaults. Unprefixed model strings mean OpenAI.
- ChatGPT OAuth applies only to OpenAI provider requests. Anthropic provider requests use only the configured Anthropic API key and optional Anthropic base URL.
- Missing Anthropic credentials are an error only when a selected model requires Anthropic.
- Hosted OpenAI tools, including hosted `websearch`, are not exposed to Anthropic model requests.

### Prompt And Definition Loading

- Agents load from `.rocketclaw/agents` plus workspace overlays according to bridge mode.
- Skills load from `.rocketclaw/skills` plus workspace overlays according to bridge mode.
- Primary agent prompt expansion happens during RocketCode construction.
- Subagent prompt expansion happens when the `task` tool launches another agent.
- Skill content expansion happens when the `skill` tool loads skill content.
- `AGENTS.md` root workspace instructions remain literal.

### Subdelegation Recursion Limit

- Agents may declare optional YAML frontmatter field `maxRecursion`.
- Omitted `maxRecursion` means unlimited subdelegation.
- `maxRecursion: -1` means unlimited subdelegation.
- `maxRecursion: 0` means the agent that starts the RocketCode inference cannot delegate through the `task` tool.
- Positive `maxRecursion` values allow that many `task` delegation levels from the agent that started the RocketCode inference.
- The recursion budget is per inference delegation path, not a shared total across sibling task calls.
- The agent that starts the inference owns the recursion budget for that delegation tree. Child agents' own `maxRecursion` values are ignored inside an inherited delegation tree and apply only when that child agent starts a separate RocketCode inference.
- Values below `-1` and non-integer YAML values make the agent definition invalid.

### Per-Agent Guardrail

- RocketClaw does not configure a global RocketCode inter-agent filter and has no `rocketclaw.json` setting for this behavior.
- RocketCode loads `guardrail: <agent-name>` from agent frontmatter. The declaring target agent is guarded by the named loaded agent.
- Missing guardrail target agents fail RocketCode construction in both persistent bridge and raw-run paths.
- `agents/guardrail.md` is not special; it is a normal agent named `guardrail` when present.
- The guardrail agent uses its normal prompt. RocketCode provides the reviewed material as the guardrail run's user message; there is no required or special `{{.Payload}}` or `{{.Message}}` template placeholder.
- For the outbound delegation check, the guardrail message is:

```text
Current Action: delegation
The agent <originatingAgent> wants to delegate to <delegatedAgentName>:
<delegated prompt>
```

- For the inbound response check, the guardrail message is:

```text
Current Action: response
The agent <originatingAgent> wants to delegate to <delegatedAgentName>:
<delegated prompt>

And the response from <delegatedAgentName> to <originatingAgent>:
<child response>
```

- `originatingAgent` is the agent that called the `task` tool, and `delegatedAgentName` is the guarded target agent.
- RocketCode runs the guardrail before each outbound `task` delegation to a guarded target. When the guardrail returns `approved:false`, the child agent is not called and the guardrail reason is returned to the caller agent.
- RocketCode runs the guardrail after each inbound guarded child agent final response. When the guardrail returns `approved:false`, the guardrail reason is returned to the caller agent instead of the child response.
- The guardrail response contract is strict JSON with `approved` boolean and `reason` string fields. Invalid or missing guardrail JSON fails closed.
- The guardrail receives tools only through its own `permission` frontmatter under existing RocketCode permission semantics and uses its own prompt, model, reasoning effort, verbosity, tools, and skills.
- Guardrail execution is not recursively guarded and is not surfaced as parent progress or subagent diagnostics; only rejection reasons are bubbled through the task result.

### Tools Injected By RocketClaw

| Tool                                         | Contract                                                                                                                      |
|----------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------|
| `rocketclaw_restart`                          | Schedules graceful restart for approved runtime config/asset changes.                                                         |
| `rocketclaw_schedule_message`                 | Schedules one-shot delayed prompts or recurring delayed prompts through the owning bridge context. Recurring prompts use optional `recurring: true`, require `send_this_in` from 1m through 1h, persist until reset, and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages`         | Clears scheduled messages for the owning bridge context.                                                                      |
| `rocketclaw_update_goal`                       | Persistent bridge tool visible only when the owning conversation has an active text connector goal loop; sets the goal status to `complete` or `blocked` with an optional note. |
| `rocketclaw_attach_files_to_response`         | Persistent bridge tool that allows RocketCode to attach collected files to the outbound response through the shared outbound attachment carrier.                              |
| `rocketclaw_i_want_human_partner_to_see_this` | Required completion tool for raw background runs; its argument is the exact human-visible final message or empty for silence. |

Persistent bridge tools are restart, schedule message, reset scheduled messages, active-goal update when applicable, attach files, and path-specific custom tools. Raw-run tools are decision, outbound attachment collection, restart, schedule message, and reset scheduled messages. Raw and persistent schedule-message tools expose the same one-shot and recurring contract.

### Goal-Loop Prompting

- When a persistent bridge conversation has an active text connector goal loop, RocketClaw may add goal steering to the turn prompt.
- Goal steering includes the persisted objective and current turn-budget state.
- Goal steering instructs the agent to keep making progress until it can mark the goal `complete` or `blocked` through `rocketclaw_update_goal`.
- Goal-loop human objectives and continuation text remain persistent-bridge input and do not enable shell interpolation.

### Session And Replay

- Persistent conversations use SQLite-backed session storage under `.rocketclaw/state.sqlite3`, opened through the centralized RocketClaw SQLite state-store opener defined by ADR 0005.
- Raw runs can persist into a configured conversation when supplied with `RawRunProgress.SessionService` and `ConversationID`.
- External MCP metadata is injected as a developer message for the turn that supplied it and must not become ambient global state.
- Attachments are normalized before RocketCode prompt construction through the shared inbound attachment path. Supported image attachments become RocketCode prompt attachments. Text attachments from text connectors and external MCP become literal prompt text before the persistent bridge builds the RocketCode input. Unsupported or over-budget attachments are omitted from RocketCode attachment input and represented through attachment warnings or fallback text.
- RocketCode response attachments collected through `rocketclaw_attach_files_to_response` become shared outbound attachment values owned by the persistent bridge result. Connector delivery and blocking caller delivery, including external MCP `session_prompt` results, adapt those same outbound attachment values at the edge instead of maintaining separate attachment pipelines.
- Response checkpoint seeding through replay compaction is OpenAI-only. Checkpoints created by non-OpenAI provider turns fail seeding with a clear unsupported-provider error until provider-specific compaction is approved.

### ChatGPT Codex Backend Requests

- When RocketClaw backs OpenAI RocketCode requests with ChatGPT OAuth, Codex backend requests use Codex-compatible request identity rather than a RocketClaw-specific persona.
- Codex backend requests send `originator: codex_cli_rs`.
- Codex backend requests send `User-Agent: codex_cli_rs/0.0.0 (RocketClaw)`.
- Codex backend requests send `Authorization: Bearer <access token>`.
- Codex backend requests send `ChatGPT-Account-ID` when the saved OAuth token has an account ID.
- Codex backend requests send a stable per-transport `session_id` and `x-client-request-id`; both headers use the same generated value for the lifetime of that transport.
- OAuth token requests send `Accept: application/json`.
- RocketClaw does not add conversation-level Codex request identity, `thread-id`, `x-codex-*`, WebSocket, turn-state headers, or `prompt_cache_key` under this contract.

### Diagnostics And Skills

- RocketClaw enables `ExperimentalStrongerSkills` in both persistent and raw paths.
- Diagnostics are enabled for the persistent bridge and for raw runs when progress reporting is configured.
- RocketClaw sets RocketCode's maximum parallel tool calls to 16 in both persistent and raw paths.

## Non-Goals

- This ADR does not duplicate RocketCode's full API documentation.
- This ADR does not allow human/external input shell execution in the persistent bridge.
- This ADR does not require preserving deprecated RocketCode APIs when rocketclaw's behavior can be preserved through a newer API.

## Evidence

- `internal/rocketclaw/harnessbridge/bridge.go`
- `internal/rocketclaw/harnessbridge/raw_run.go`
- `internal/rocketclaw/harnessbridge/store.go`
- `internal/rocketcode/rocketcode.go`
- `internal/rocketcode/looper.go`
- `internal/rocketcode/prompts.go`
- `internal/rocketcode/tools.go`
- `internal/rocketcode/tasks.go`

## Consequences

- Changing RocketCode config flags is a behavior change and requires ADR approval when it changes meaning.
- Dependency upgrades must be checked against this embedding contract, especially prompt expansion, tools, inter-agent guardrails, session replay, attachments, and raw-run completion.
- Tests should verify observable RocketCode input/output behavior, not only that config structs are constructed.

## Changelog

- 2026-05-25: Initial accepted snapshot.
- 2026-05-25: Added optional recurring scheduled-message contract for persistent and raw RocketCode paths.
- 2026-06-02: Set RocketCode maximum parallel tool calls to 16 for persistent and raw RocketClaw paths.
- 2026-06-02: Added Discord text as a persistent-bridge source whose human input remains literal.
- 2026-06-05: Linked persistent conversation SQLite storage to the centralized RocketClaw state-store opener in ADR 0005.
- 2026-06-06: Added ChatGPT Codex backend request identity and header contract for RocketClaw-backed RocketCode requests.
- 2026-06-10: Added `maxRecursion` agent frontmatter contract for limiting RocketCode task subdelegation depth.
- 2026-06-10: Added the local-only `guardrail` agent contract for RocketCode inter-agent delegation and response filtering.
- 2026-06-11: Added provider-qualified OpenAI and Anthropic RocketCode embedding contracts, with ChatGPT OAuth and checkpoint compaction remaining OpenAI-only.
- 2026-06-11: Added persistent-bridge Slack goal-loop steering and `rocketclaw_update_goal` tool contract governed by ADR 0007.
- 2026-06-12: Specified shared inbound attachment normalization for Slack and external MCP before persistent RocketCode prompt construction.
- 2026-06-12: Specified shared outbound response attachment values for connector delivery and external MCP result rendering.
- 2026-06-12: Replaced RocketClaw-configured global guardrail with RocketCode per-target-agent `guardrail` frontmatter and explicit guardrail request messages.
- 2026-06-14: Removed `paused` from the active goal-update tool and goal-steering contract.
- 2026-06-14: Recast goal-loop steering and attachment normalization around the generic text connector contract.
