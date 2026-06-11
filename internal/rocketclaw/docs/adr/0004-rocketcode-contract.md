# 0004. RocketCode Contract

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw embeds RocketCode as the reasoning runtime through two app-owned construction paths: the persistent conversation bridge and raw runs. RocketClaw owns which prompt sources are trusted, which tools are injected, and how sessions are persisted and replayed.

## Scope

This ADR governs rocketclaw's contract with the embedded `github.com/Rocketable/rocketcode` library. RocketCode's own public API is documented upstream; this ADR records rocketclaw's embedding policy.

## Context

Several rocketclaw capabilities exist only because of precise RocketCode configuration: prompt shell expansion, stronger skills, custom tools, inter-agent guardrails, session replay, attachments, and raw cron completion. These settings are product behavior, not incidental config.

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

### Inter-Agent Guardrail

- RocketClaw configures RocketCode's inter-agent filter only when the effective runtime agents include a local-only agent named `guardrail` from `agents/guardrail.md`, as constrained by ADR 0003.
- There is no `rocketclaw.json` setting for this behavior.
- Both persistent bridge and raw-run RocketCode construction paths pass the guardrail prompt, model, reasoning effort, verbosity, and permissions into RocketCode when the local-only `guardrail` agent is present.
- The guardrail prompt is a Go `text/template` and receives the delegated prompt or child response text as `{{.ParentAgentPrompt}}`.
- RocketCode runs the guardrail before each `task` delegation. When the guardrail returns `approved:false`, the child agent is not called and the guardrail reason is returned to the caller agent.
- RocketCode runs the guardrail after each child agent final response. When the guardrail returns `approved:false`, the guardrail reason is returned to the caller agent instead of the child response.
- The guardrail response contract is strict JSON with `approved` boolean and `reason` string fields. Invalid or missing guardrail JSON fails closed.
- The guardrail receives tools only through its own `permission` frontmatter under existing RocketCode permission semantics.
- Guardrail execution is not surfaced as parent progress or subagent diagnostics; only rejection reasons are bubbled through the task result.

### Tools Injected By RocketClaw

| Tool                                         | Contract                                                                                                                      |
|----------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------|
| `rocketclaw_restart`                          | Schedules graceful restart for approved runtime config/asset changes.                                                         |
| `rocketclaw_schedule_message`                 | Schedules one-shot delayed prompts or recurring delayed prompts through the owning bridge context. Recurring prompts use optional `recurring: true`, require `send_this_in` from 1m through 1h, persist until reset, and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages`         | Clears scheduled messages for the owning bridge context.                                                                      |
| `rocketclaw_attach_files_to_response`         | Persistent bridge tool that allows RocketCode to attach collected files to the outbound response.                              |
| `rocketclaw_i_want_human_partner_to_see_this` | Required completion tool for raw background runs; its argument is the exact human-visible final message or empty for silence. |

Persistent bridge tools are restart, schedule message, reset scheduled messages, attach files, and path-specific custom tools. Raw-run tools are decision, outbound attachment collection, restart, schedule message, and reset scheduled messages. Raw and persistent schedule-message tools expose the same one-shot and recurring contract.

### Session And Replay

- Persistent conversations use SQLite-backed session storage under `.rocketclaw/state.sqlite3`, opened through the centralized RocketClaw SQLite state-store opener defined by ADR 0005.
- Raw runs can persist into a configured conversation when supplied with `RawRunProgress.SessionService` and `ConversationID`.
- External MCP metadata is injected as a developer message for the turn that supplied it and must not become ambient global state.
- Attachments are converted into RocketCode prompt attachments only when supported by the bridge path.
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
