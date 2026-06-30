# 0002. Behavior Contracts

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketCode preserves behavior contracts that are more important than internal code shape. Refactors, simplification, dependency updates, and CLOC-reduction work must preserve these contracts unless the human partner explicitly approves a spec change.

## Scope

This ADR governs regression-sensitive behavior for prompt framing, replay, output, compaction, permissions, observability, and filesystem safety. Tool inventories and agent/skill loading details are governed by companion ADRs.

## Context

RocketCode behavior is spread across command wiring, runtime loop code, tool wrappers, and tests. Making the current contracts explicit reduces the risk that line-count pressure removes user-visible behavior by accident.

## Normative Contracts

### Prompt Sources

| Source | Current contract |
| --- | --- |
| Active agent prompt | Loaded from the selected agent. In standalone commands this is `main`. Shell interpolation occurs only when config enables primary prompt expansion. |
| Subagent prompt | Loaded from the selected subagent when the `task` tool launches it. Shell interpolation occurs only when config enables subagent prompt expansion and does not mutate the loaded agent definition. |
| Skill content | Loaded by the `skill` tool. Shell interpolation occurs only when config enables skill prompt expansion. |
| Prompt input | Human or loop input remains literal unless config explicitly enables input prompt expansion. Standalone env defaults do not enable input expansion. |
| `AGENTS.md` | Loaded literally. Shell interpolation does not apply. |
| Current workspace block | Appended to root instructions and exposes the host workspace root in the system prompt. |

Shell interpolation, when enabled, uses RocketCode prompt expansion semantics: ``!`command` `` patterns run from the workspace root, insert stdout, and do not make prompt preparation fail when the command fails.

### Session And Replay

- Completed non-interrupted turns append `SessionEntry` rows with version `1`, type `turn`, UTC timestamp, model, replay input, replay output, output trace, and replay-neutral provider token-usage metadata when providers return it.
- Session history is loaded lazily on the first non-empty prompt and ordered by stored row id.
- Empty prompt input with no attachments closes that response channel and does not call the model.
- Replay preserves user and developer messages, assistant messages, reasoning encrypted content, compaction items, function calls, function-call outputs, and supported web-search calls using RocketCode's OpenAI Responses-shaped replay encoding.
- Persisted OpenAI token-usage metadata records numeric token counts only and must not affect replay input, prompt framing, model selection, tool execution, or model-visible results.
- Newly persisted turns store unprefixed OpenAI model IDs. Runtime model selection accepts unprefixed OpenAI model IDs and legacy `openai/<model>` aliases normalized to `<model>`. Other provider-qualified model names, Anthropic model names, and empty explicit model reference parts are construction errors that prevent RocketCode startup. Loaded agents, including guardrail, reviewer, and subagent agents, may omit `model` frontmatter or set it empty; missing or empty agent models inherit the runtime/default model.
- Compaction replay items remain Responses-shaped `compaction` items in durable replay. OpenAI encrypted compaction payloads and readable summary metadata are preserved when supplied.
- Response output items that cannot be converted back into replay input are recorded in output trace rather than silently becoming prompt input.
- Replay decode errors identify the entry, item, and kind involved.

### Model Turn Loop

#### Provider Scope

- RocketCode supports first-party OpenAI Responses as its only model provider surface.
- Provider-family parity, cross-provider replay projection, compatible-provider routing, Anthropic routing, and chat-completions routing are not product capabilities.

- Each model cycle builds history from session entries, runtime system prompt, latest input, previous outputs, and tool outputs.
- Each model cycle sends history to the first-party OpenAI Responses client using the active OpenAI model ID. Prompt framing, local tool call dispatch, tool-output continuation, diagnostics, and replay semantics must be preserved.
- History before the latest compaction point is pruned so replay starts from the compaction boundary when one exists.
- Compaction steering, when configured, is appended as developer text after stored compaction items. OpenAI Responses context compaction is the only provider compaction path.
- OpenAI Responses `context_length_exceeded` API errors trigger an explicit `/responses/compact` recovery path before the turn fails. Recovery compacts older safe replay blocks progressively, retries after each successful compaction, preserves tool calls with their corresponding tool outputs, does not compact unanswered tool calls, keeps the newest active block unchanged, and persists only compaction items needed for a successful retry.
- Before sending stored conversation history to OpenAI, RocketCode preserves its Responses-shaped replay input. No target-provider projection is required.
- OpenAI surfaced provider errors are classified with strong provider error types so RocketCode owns user-visible retry diagnostics and backoff. Rate-limit handling follows Codex semantics by default: direct HTTP 429 transport errors do not retry, `usage_limit_reached` and `usage_not_included` are terminal, and retryable failed/stream responses with `rate_limit_exceeded` retry with a bounded retry budget. As a provider-specific extension, direct HTTP 429 errors whose SDK-provided `code` or `type` is `too_many_requests` are retryable. Retry delay uses typed HTTP retry metadata when present, otherwise bounded exponential backoff is used. Provider messages are diagnostic text, not retry-control input.
- Tool outputs are appended to model input and the turn continues until the model returns no function calls.
- Three repeated identical tool calls are converted into a tool-output failure for the model.
- Tool call permission denial, unknown tool names, and malformed tool permissions are returned as model-visible tool failures rather than process-fatal errors.
- Context cancellation during tool dispatch is fatal to that dispatch and does not become an ordinary tool failure.

### Observability

- RocketCode may emit OpenTelemetry spans using OpenInference-compatible semantic attributes for agent turns, provider cycles, and tool calls only when configured through RocketCode's configuration object.
- RocketCode observability configuration must be supplied exclusively through the RocketCode configuration object. RocketCode must not read environment variables to decide whether or how to configure instrumentation.
- Observability must not alter prompt framing, replay input, provider routing, permission decisions, tool execution order, diagnostics output, persisted session entries, or model-visible tool results.
- Configured server/operator logging and tracing for guardrail and automatic permission reviewer child runs are side effects only. They may include child-run messages, reasoning summaries, and diagnostics, but must not alter prompt framing, replay input, parent output, permission decisions, tool execution order, persisted session entries, or model-visible tool results.
- Provider rate-limit diagnostics include Codex-compatible request tracking and rate-limit metadata when present, using known headers such as `x-request-id`, `x-oai-request-id`, `cf-ray`, `x-codex-active-limit`, Codex primary/secondary limit headers, Codex credits headers, `x-codex-promo-message`, and `x-codex-rate-limit-reached-type`. Diagnostics must not dump arbitrary response headers.
- Provider retry and provider error diagnostics are also emitted as OpenTelemetry span events on the active provider span when observability is enabled. These events include the diagnostic phase, HTTP status, provider code/type, retry attempt, retry delay, response id, and allowlisted request/rate-limit metadata when present. Retry diagnostics must not mark the provider span as failed when the provider operation ultimately succeeds.
- Tool observability must cover successful tool calls, unknown tool names, permission denials, automatic permission review denials, repeated-call doom-loop failures, and tool execution failures without changing their existing model-visible failure text.
- OpenInference input and output redaction settings supplied through the RocketCode configuration object must be honored by RocketCode-authored spans. When input or output redaction is enabled, structural metadata such as span kind, agent name, model, tool name, call id, status, and counts may still be emitted.
- Tracing export failures must not fail or interrupt RocketCode turns.

### Output Contracts

| Output source | Contract |
| --- | --- |
| `cmd/rocketcode` assistant commentary | Printed as `[assistant commentary] ...`. |
| `cmd/rocketcode` assistant final message | Printed as `[assistant message] ...`. |
| `cmd/rocketcode` reasoning summary | Printed as `[reasoning summary] ...`. |
| `cmd/rocketcode` tool diagnostics | Printed as `[assistant tool] ...` JSON. |
| Runtime diagnostics | When enabled, print `agent:`, `tools:`, `skills:`, then `system_prompt:` fenced with `---`. |
| `rocketloop` | Writes JSONL events and does not use the interactive line prefixes. |
| Task result | Successful and guardrail-blocked child results are returned inside `<task_result>` wrappers. |
| Skill search | Empty corpus returns `No skills are currently available.`. No matches returns `No matching skills found.`. Matches use `## Matching skills` with bullet entries. |
| Skill load | Normal skill output is wrapped in `<skill_content name="...">` and includes `# skill: ...`, base directory, relative-path guidance, and sampled skill files. |

### Permissions And Safety

- Tool visibility is permission-gated. Deny-by-default must not become permissive by accident.
- Later matching permission rules overwrite earlier matching rules.
- `apply_patch`, `write`, and `patch` permission names normalize to the `edit` permission bucket.
- `auto` is a supported permission action that requires `Config.AutoApprovePermissions` and routes a matching tool call through automatic permission review. The reviewed tool call executes only when the reviewer returns valid structured output with `outcome:"allow"`. When automatic permission approval is disabled, matching `auto` rules fail closed as model-visible tool failures. `ask`, `external_directory`, and `doom_loop` permission names are unsupported.
- Automatic permission review is fail-closed: reviewer `outcome:"deny"`, invalid reviewer JSON, missing required review fields, invalid enum values, empty `rationale`, model errors, tool errors, context cancellation, timeout, missing reviewer, and recursive automatic review all prevent tool execution and are returned as model-visible tool failures rather than process-fatal errors.
- `edit` allow grants read visibility when no explicit read rule matched.
- Permission subjects support wildcard matching with `*` and `?`, slash normalization, and `~` or `$HOME` expansion.
- `.env` and `.env.*` basenames are blocked, while `.env.example` remains allowed.
- Absolute paths must resolve under the workspace root. Paths that escape the root are rejected.
- Reads, patches, glob targets, grep targets, glob results, and grep results must not follow symlinks.

### Engineering Guardrails

- Linters are part of the behavior-preservation safety system. Do not disable linters through `//nolint`, configuration changes, command flags, or equivalent suppressions unless the human partner explicitly approves the exact suppression and rationale.
- When a linter finding appears inconvenient during CLOC-reduction or refactoring work, fix the code or stop and ask; do not hide the finding.

## Non-Goals

- This ADR does not require exact internal helper names or file boundaries.
- This ADR does not require preserving tests that only cover removed internals.
- This ADR does not permit weakening workspace-root, `.env`, or symlink safety to save lines.

## Evidence

- `internal/rocketcode/looper.go`
- `internal/rocketcode/replay.go`
- `internal/rocketcode/prompts.go`
- `internal/rocketcode/permission.go`
- `internal/rocketcode/filesystem.go`
- `internal/rocketcode/tasks.go`
- `internal/rocketcode/skills.go`
- OpenInference semantic conventions for span attributes.
- OpenTelemetry trace API and SDK contracts.
- `cmd/rocketcode/main.go`
- `cmd/rocketloop/main.go`

## Consequences

- Behavior-preserving simplification must verify prompt framing, replay, output text, permission gates, and safety boundaries independently.
- Behavior-preserving observability work must verify that traces are side effects only and do not change turn, tool, permission, replay, or persistence semantics.
- Behavior-preserving simplification must keep linter checks active unless an exact linter suppression has explicit human approval.
- Any change that intentionally alters these contracts must update this ADR first and receive explicit human approval.
- Tests should assert observable contracts rather than only implementation structure.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Added linter-disable guardrail for behavior-preserving refactors.
- 2026-06-11: Added provider-routing replay and turn-loop contracts for OpenAI and Anthropic model requests.
- 2026-06-17: Added fail-closed `auto` permission action semantics gated by `Config.AutoApprovePermissions`.
- 2026-06-17: Added OpenTelemetry/OpenInference observability contract for agent, provider, and tool spans, configured exclusively through the RocketCode configuration object.
- 2026-06-19: Added explicit OpenAI Responses context-length recovery with progressive safe-block compaction.
- 2026-06-22: Added Codex-compatible rate-limit retry and diagnostics contracts for surfaced OpenAI provider errors with a retryable `too_many_requests` provider extension.
- 2026-06-22: Added provider retry/error OpenTelemetry span events for provider diagnostics without changing final span status on successful retries.
- 2026-06-22: Added Anthropic beta server-side compaction block persistence and replay semantics.
- 2026-06-23: Added replay-neutral provider token-usage metadata to completed session entries when providers return token counts.
- 2026-06-22: Added OpenAI-compatible provider model routing, strict prefixed model validation, compatible `responses` and `chat_completions` modes, and cross-provider compaction replay projection semantics.
- 2026-06-23: Clarified loaded-agent resolved-model startup validation, chat-completions prompt framing, target-specific replay projection, encrypted reasoning omission, user checkpoint rehydration notices, and compatible chat retry parity.
- 2026-06-23: Required every loaded agent to declare non-empty model frontmatter instead of inheriting the runtime/default model.
- 2026-06-23: Added provider-family parity requirements for OpenAI, OpenAI-compatible, and Anthropic runtime capabilities, including shared replay projection semantics and test expectations.
- 2026-06-24: Added OpenAI-compatible chat-completions ChatGPT-style automatic compaction after successful above-threshold responses.
- 2026-06-24: Required automatic permission review to execute only on valid structured `outcome:"allow"` output and fail closed on missing fields, invalid enums, empty rationale, timeout, or recursive review.
- 2026-06-26: Removed Anthropic and OpenAI-compatible chat-completions behavior contracts; RocketCode provider behavior now covers first-party OpenAI Responses and OpenAI-compatible Responses only.
- 2026-06-26: Removed remaining OpenAI-compatible Responses behavior contracts, provider-family parity, cross-provider replay projection, provider-qualified model persistence, and required agent model declarations; RocketCode provider behavior now covers first-party OpenAI Responses only.
- 2026-06-30: Allowed legacy `openai/<model>` strings as aliases normalized to unprefixed first-party OpenAI model IDs while keeping other provider-qualified model strings invalid.
- 2026-06-30: Allowed configured server/operator logging and tracing of guardrail and automatic permission reviewer child-run messages, reasoning summaries, and diagnostics as side effects that must not change replay, parent output, permissions, tool results, or session persistence.
