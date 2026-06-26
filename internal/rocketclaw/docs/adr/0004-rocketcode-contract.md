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
| Persistent bridge | `internal/rocketclaw/harnessbridge/bridge.go`  | Main, thread, Slack, Discord text, terminal CLI, browser, Discord voice, scheduled, and external MCP conversation turns. | `InputPrompts: false`  |
| Raw run           | `internal/rocketclaw/harnessbridge/raw_run.go` | Cron and one-off cron background turns.                                                | `InputPrompts: true`   |

Both paths enable `PrimaryPrompts`, `SubagentPrompts`, and `SkillPrompts` shell expansion. Persistent bridge input text remains literal. Raw input text expands because cron bodies are trusted workspace files. Both paths construct RocketCode with a first-party OpenAI Responses client using the configured RocketClaw OpenAI RocketCode auth path. Both paths set RocketCode's `AutoApprovePermissions` flag to true unconditionally.

Both construction paths must pass RocketClaw-originated `rocketcode.PromptInput` text using the trusted prompt header defined in ADR 0002. The persistent bridge derives `Origin`, `Media`, optional `Name`, and the required or omitted `additional_instructions` from the inbound turn's RocketClaw runtime source, media, principal metadata, turn handling semantics, and selected agent frontmatter according to ADR 0002. Raw-run cron inputs use `Origin=Cron`, `Media=Text`, and omit `principal` and `additional_instructions`. Adding this header must not alter `InputPrompts` expansion settings, which prompt bodies are eligible for shell interpolation, attachment normalization, session replay, tool injection, connector delivery behavior, or authorization decisions.

### Provider Selection

- RocketClaw treats first-party OpenAI Responses as the only supported RocketCode model provider surface.
- Model strings in agent frontmatter and runtime defaults are unprefixed OpenAI model IDs. Provider-qualified model strings such as `openai/...`, `openai-compatible/...`, and `anthropic/...` are construction errors.
- Every loaded agent may omit `model` frontmatter or set it empty; missing or empty agent models inherit the runtime/default OpenAI model. RocketClaw startup must preserve this inheritance across embedded, generated, configured-overlay, and workspace-overlay agents.
- RocketClaw passes one first-party OpenAI client into RocketCode. ChatGPT OAuth and OpenAI API-key behavior follow `openai.rocketcode_auth`.
- Hosted OpenAI tools, including hosted `websearch`, remain OpenAI-only tools governed by RocketCode permissions.

### Prompt And Definition Loading

- Agents load from `.rocketclaw/agents` plus workspace overlays according to bridge mode.
- Skills load from `.rocketclaw/skills` plus workspace overlays according to bridge mode.
- Primary agent prompt expansion happens during RocketCode construction.
- Subagent prompt expansion happens when the `task` tool launches another agent.
- Skill content expansion happens when the `skill` tool loads skill content.
- `AGENTS.md` root workspace instructions remain literal.
- Agents may declare optional YAML frontmatter field `additionalInstructions`. In the persistent bridge, a non-empty string value overrides the default normal-reply `additional_instructions` text defined in ADR 0002 for turns handled by that selected agent. Missing, empty, or non-string values do not override the default. This field does not affect internal-note turns or raw-run cron prompts.

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

### Automatic Permission Review

- RocketClaw does not implement its own automatic permission reviewer and does not expose a `rocketclaw.json` flag for automatic permission review.
- Persistent bridge and raw-run construction paths always enable RocketCode automatic permission review. RocketCode `auto` permission rules therefore route to RocketCode's automatic reviewer instead of failing closed because of RocketClaw configuration.
- RocketCode owns reviewer resolution, embedded guardian behavior, custom `auto(name)` reviewer execution, reserved reviewer-name validation, and fail-closed review semantics.
- The RocketClaw linter, agent graph, and goal-check script validation continue to use deterministic allow/deny permission evaluation unless a separate ADR explicitly expands them to model automatic review outcomes.

### Tools Injected By RocketClaw

| Tool                                         | Contract                                                                                                                      |
|----------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------|
| `rocketclaw_restart`                          | Schedules graceful restart for approved runtime config/asset changes.                                                         |
| `rocketclaw_schedule_message`                 | Schedules one-shot delayed prompts or recurring delayed prompts through the owning bridge context. Recurring prompts use optional `recurring: true`, require `send_this_in` from 1m through 1h, persist until reset, and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages`         | Clears scheduled messages for the owning bridge context.                                                                      |
| `rocketclaw_update_goal`                       | Persistent bridge tool visible only when the owning conversation has an active text connector goal loop; reports `progress`, `complete`, or `blocked` with an optional explanatory `note`. |
| `rocketclaw_attach_files_to_response`         | Persistent bridge tool that allows RocketCode to attach collected files to the outbound response through the shared outbound attachment carrier.                              |
| `ask_user_question`                            | Persistent bridge tool visible only for qualifying human-originated Slack, Discord Text, or terminal CLI turns with a native answer path. The tool asks the originating human through that surface, blocks until answered or canceled, and returns selected option values, optional custom text, and the answer source. |
| `rocketclaw_i_want_human_partner_to_see_this` | Required completion tool for raw background runs; its argument is the exact human-visible final message or empty for silence. |

Persistent bridge tools are restart, schedule message, reset scheduled messages, active-goal update when applicable, attach files, `ask_user_question` when the originating human turn qualifies, and path-specific custom tools. Raw-run tools are decision, outbound attachment collection, restart, schedule message, and reset scheduled messages. Raw and persistent schedule-message tools expose the same one-shot and recurring contract.

### Goal-Loop Prompting

- When a persistent bridge conversation has an active text connector goal loop, RocketClaw may add goal steering to the turn prompt.
- Goal steering includes the persisted objective and current turn-budget state.
- Goal steering instructs the agent to keep making progress until it can report `progress`, mark the goal `complete`, or mark the goal `blocked` through `rocketclaw_update_goal`. Goal steering requires visible goal responses to end with a `Progress summary:` section whose substance is mirrored into the tool's `note` field.
- Goal-loop human objectives and continuation text remain persistent-bridge input and do not enable shell interpolation.

### Session And Replay

- Persistent conversations use SQLite-backed session storage under `.rocketclaw/state.sqlite3`, opened through the centralized RocketClaw SQLite state-store opener defined by ADR 0005.
- Raw runs can persist into a configured conversation when supplied with `RawRunProgress.SessionService` and `ConversationID`.
- External MCP metadata is injected as a developer message for the turn that supplied it and must not become ambient global state.
- Attachments are normalized before RocketCode prompt construction through the shared inbound attachment path. Supported image attachments become RocketCode prompt attachments. Text attachments from text connectors and external MCP become literal prompt text before the persistent bridge builds the RocketCode input. Unsupported or over-budget attachments are omitted from RocketCode attachment input and represented through attachment warnings or fallback text.
- RocketCode response attachments collected through `rocketclaw_attach_files_to_response` become shared outbound attachment values owned by the persistent bridge result. Connector delivery and blocking caller delivery, including external MCP `session_prompt` results, adapt those same outbound attachment values at the edge instead of maintaining separate attachment pipelines.
- Response checkpoint seeding through replay compaction uses first-party OpenAI Responses. The purpose of this step is to seed a new conversation from prior main-session context without making the human repeat context. Provider-family parity and cross-provider replay projection are not part of this contract.

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

### Observability

- RocketClaw may initialize OpenTelemetry tracing from instrumentation settings in the RocketClaw configuration file.
- RocketClaw instrumentation configuration must be supplied exclusively through the RocketClaw configuration file. RocketClaw must not read environment variables to decide whether or how to configure instrumentation.
- When tracing is configured, the persistent bridge and raw-run paths may create OpenInference-compatible trace and session context for RocketCode turns.
- Persistent bridge turns may emit root `AGENT` spans carrying conversation/session id, turn id, source, kind, label, publish state, attachment count, selected agent, and redacted input/output attributes.
- Raw runs may emit root `AGENT` spans carrying the configured conversation/session id when one is available, selected agent, and redacted input/output attributes.
- RocketClaw-owned OpenAI provider clients passed into RocketCode may include OpenInference OpenTelemetry middleware so provider requests are child spans of the active RocketCode turn.
- Observability must not alter prompt expansion settings, prompt text, attachment normalization, session replay, provider selection, ChatGPT OAuth request identity, permission behavior, tool injection, raw-run completion, diagnostics, connector delivery, or persisted conversation state.
- OpenInference input and output redaction settings supplied through the RocketClaw configuration file must be honored by RocketClaw-authored spans. Structural metadata such as span kind, agent name, model, source, conversation id, turn id, and counts may still be emitted.
- Tracing export failures must not fail or interrupt persistent bridge turns, raw runs, connector delivery, scheduled prompts, or cron completion.

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
- OpenInference semantic conventions for span attributes.
- OpenTelemetry trace API and SDK contracts.

## Consequences

- Changing RocketCode config flags is a behavior change and requires ADR approval when it changes meaning.
- Adding or changing RocketClaw observability must preserve RocketCode embedding semantics and remain a side effect only.
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
- 2026-06-15: Updated RocketCode embedding contracts for goal-loop `progress` updates and `Progress summary:` text mirrored into `rocketclaw_update_goal.note`.
- 2026-06-17: Added RocketClaw embedding contract for passing `rocketcode.auto_approve_permissions` into RocketCode automatic permission review.
- 2026-06-17: Added OpenTelemetry/OpenInference observability contract for Phoenix-compatible tracing around RocketClaw-owned RocketCode turns, configured exclusively through the RocketClaw configuration file.
- 2026-06-19: Documented `ask_user_question` as a conditional persistent-bridge RocketClaw tool and expanded its native answer path to terminal CLI turns.
- 2026-06-22: Added RocketClaw embedding contracts for named OpenAI-compatible RocketCode providers, strict prefixed model validation, hosted-tool exclusion, and compatible response checkpoint seed compaction.
- 2026-06-23: Required RocketClaw's persistent bridge and raw-run RocketCode prompt inputs to prepend trusted runtime-generated origin media/principal headers without changing prompt body semantics or shell-expansion settings.
- 2026-06-23: Clarified conditional first-party OpenAI construction and credentials, loaded-agent resolved-model validation, compatible-only deployments, and seed replay projection boundaries.
- 2026-06-23: Required Anthropic response checkpoint seeding parity, including internal capability parity, through Anthropic-compatible summarization and replay projection instead of treating Anthropic as unsupported.
- 2026-06-23: Required RocketClaw to enforce RocketCode's non-empty model frontmatter requirement for every loaded agent across embedded, generated, configured-overlay, and workspace-overlay sources.
- 2026-06-23: Strengthened RocketClaw provider-family parity requirements for provider construction, shared tools, and response checkpoint seeding.
- 2026-06-24: Removed RocketClaw pass-through of `rocketcode.auto_approve_permissions` and required persistent bridge and raw-run construction paths to set RocketCode `AutoApprovePermissions` true unconditionally.
- 2026-06-25: Specified that persistent bridge prompt headers may include trusted `additional_instructions`, normal reply turns may override the default through selected agent `additionalInstructions` frontmatter, internal notes cannot be overridden, and raw-run cron headers omit the field.
- 2026-06-26: Removed Anthropic and OpenAI-compatible chat-completions embedding support; RocketClaw constructs only first-party OpenAI Responses and named OpenAI-compatible Responses providers.
- 2026-06-26: Removed remaining OpenAI-compatible Responses provider support, provider-family parity, provider-qualified model strings, and required agent model declarations from the RocketClaw/RocketCode contract; RocketClaw now embeds RocketCode with first-party OpenAI Responses only.
