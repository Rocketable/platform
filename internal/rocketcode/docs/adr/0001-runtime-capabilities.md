# 0001. Runtime Capabilities

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketCode is a workspace-local Go reasoning runtime with an interactive CLI, a non-interactive autonomous loop CLI, and an embeddable library API. It uses Responses API model requests, an OpenAI Responses-shaped internal replay model, workspace-local agent and skill definitions, sandbox-aware tools, and replayable session history.

## Scope

This ADR governs current user-visible runtime capabilities for `internal/rocketcode`, `cmd/rocketcode`, and `cmd/rocketloop`. Tool-specific contracts, agent and skill loading, permissions, and output details are governed by companion ADRs.

## Context

RocketCode is under tight source-line budget pressure. Future simplification and deletion work must preserve current behavior unless the human partner explicitly approves a spec change. This ADR records the baseline capabilities that must survive line-reduction work.

## Normative Contracts

| Area | Current capability |
| --- | --- |
| Library runtime | `rocketcode.New` constructs a `Looper` from an OpenAI Responses client, config, rooted workspace filesystem, parsed agents, parsed skills, default agent name, and optional diagnostics writer. Agent model declarations, when present, select first-party OpenAI Responses model IDs only; missing or empty agent model declarations inherit the runtime/default model. |
| Default agent | The standalone commands require a loaded `main` agent. Missing default agent is a startup error. Missing or empty `model` frontmatter on loaded agents is allowed and inherits the runtime/default model. |
| Workspace root | Standalone `rocketcode` and `rocketloop` use the process current working directory as the workspace root and open it through `*os.Root`. |
| Root instructions | `AGENTS.md`, when present in the workspace root, is loaded literally into the system prompt and followed by a current-workspace block containing the host workspace root. |
| Model defaults | Empty model defaults to OpenAI `gpt-5.4`. Empty reasoning effort defaults to `high`. Empty compact threshold defaults to `200000`. |
| Model selection | Models are unprefixed first-party OpenAI Responses model IDs such as `gpt-5.4`. Empty runtime/default model uses OpenAI `gpt-5.4`. Legacy `openai/<model>` strings are accepted only as first-party OpenAI aliases and normalized to `<model>`. Other provider-qualified model strings, including `openai-compatible/...` and `anthropic/...`, and empty model reference parts are startup errors. Agent frontmatter `model` values, when non-empty, may select a different OpenAI model ID from the runtime default. |
| Model request | Runtime turns use the first-party OpenAI Responses API with stored responses disabled, encrypted reasoning content included, reasoning summary enabled when reasoning effort is set, context compaction enabled, and OpenAI parallel tool calls enabled. |
| Rate limits and context limits | OpenAI rate limits retry after at least one minute when OpenAI exposes retryable rate-limit status, considering retry/reset headers where available. OpenAI Responses context-length API errors trigger explicit progressive compaction of older safe replay blocks and retry before surfacing a runtime error. Other failed responses and API errors surface as runtime errors, with diagnostics emitted when diagnostics are enabled. |
| Interactive CLI | `cmd/rocketcode` starts an interactive prompt named `rocketcode> `, reads terminal input, runs turns through the default agent, and prints line-oriented response output. |
| Interactive exit | `/exit`, `/quit`, and stdin EOF exit normally. Runtime errors print to stderr and exit status `1`. |
| Interactive role prefix | In `cmd/rocketcode`, an input line whose trimmed text starts with case-sensitive `developer:` is sent as a developer-role prompt with the prefix removed. Other input is user-role prompt text. |
| Interactive attachments | `@attach:path` tokens are removed from prompt text and loaded as prompt attachments when the referenced workspace file is supported and no larger than 5 MiB. |
| Interactive session | `cmd/rocketcode` persists completed, non-interrupted turns in `.tmp/session.sqlite` and reloads ordered history lazily on the first non-empty prompt. |
| Non-interactive CLI | `cmd/rocketloop` runs an autonomous loop toward a goal supplied either by positional arguments or stdin, but not both. Empty goal is an error. |
| Non-interactive flags | `rocketloop` supports `--script`, `--max-loops`, and `--script-output-limit`. Negative loop or output-limit values are errors. |
| Non-interactive output | `rocketloop` writes JSONL events to stdout for chat responses, goal claims, critic verdicts, script results, and loop results. |
| Goal verification | `rocketloop` requires the main agent to call `goal_achieved`; a critic agent must approve with `critic_verdict`; rejected or missing claims become developer feedback for the next loop. |
| Script verification | When `rocketloop --script` is set, script exit `0` ends successfully. Nonzero script output becomes developer feedback and the loop continues until success, error, or loop exhaustion. |
| Non-interactive session | `rocketloop` uses in-memory sessions only. It does not persist or resume `.tmp/session.sqlite`. |
| Interrupts | Interrupting an active `rocketcode` turn cancels that turn, emits `(interrupted)` commentary, does not append the interrupted turn to session history, and leaves the loop available for further input. |
| Tool loop | A turn may iterate across model responses and tool outputs until the model response contains no tool calls. Unknown, denied, or repeated identical tool calls are returned to the model as tool-output failures rather than process-fatal errors. |
| Parallel tools | `Config.ParallelToolCalls` limits local concurrent tool execution when greater than zero. A zero value leaves local dispatch unlimited. |

## Environment Configuration

| Environment variable | Contract |
| --- | --- |
| `ROCKETCODE_MODEL` | Overrides the default OpenAI model ID for standalone commands. Legacy `openai/<model>` values are accepted only as first-party OpenAI aliases and normalized to `<model>`. OpenAI-compatible, Anthropic, and other provider-qualified values are startup errors. |
| `ROCKETCODE_REASONING_EFFORT` | Overrides the default reasoning effort for standalone commands. |
| `ROCKETCODE_DIAG` | Any non-empty value enables diagnostics. |
| `ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS` | Any non-empty value enables stronger skill replay behavior. |
| `ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS` | Empty, `0`, or `false` disables shell expansion. `1` or `true` enables primary, subagent, and skill prompts but not input prompts. Comma tokens may include `primary`, `subagent`, `skill`, `input`, and `all`; unknown tokens are errors. |
| `ROCKETCODE_COMPACT_THRESHOLD` | Overrides compact threshold and must be a positive integer. |
| `ROCKETCODE_COMPACTION_STEERING` | Adds compaction steering text when set. |

## Non-Goals

- This ADR does not document every internal type or every test-only helper.
- This ADR does not require preserving deprecated implementation shape when current observable behavior is preserved.
- This ADR does not make human input shell-executable by default.

## Evidence

- `cmd/rocketcode/main.go`
- `cmd/rocketloop/main.go`
- `internal/rocketcode/rocketcode.go`
- `internal/rocketcode/looper.go`
- `internal/rocketcode/replay.go`
- `internal/rocketcode/prompts.go`

## Consequences

- Source-line reductions must preserve these runtime capabilities unless this ADR is updated first and approved by the human partner.
- Changes to standalone environment variables, CLI flags, session persistence, prompt roles, interrupt behavior, or loop verification semantics are behavior changes.
- Refactors should verify interactive and non-interactive paths separately.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Added provider-qualified OpenAI and Anthropic model selection while preserving OpenAI defaults and unprefixed model compatibility.
- 2026-06-19: Added OpenAI Responses context-length recovery through explicit progressive compaction and retry.
- 2026-06-22: Updated rate-limit behavior to follow Codex retry classification by default, add a retryable `too_many_requests` provider extension, use typed provider errors, and expose RocketCode-owned retry diagnostics and backoff for surfaced provider errors.
- 2026-06-22: Added Anthropic beta server-side compaction replay support to runtime capabilities.
- 2026-06-22: Added OpenAI-compatible provider runtime capability and replaced unprefixed model compatibility with strict provider-qualified model validation.
- 2026-06-23: Clarified resolved loaded-agent model validation, empty agent model inheritance, missing compatible-provider failure, chat-completions rate-limit retry parity, and compatible chat context recovery.
- 2026-06-23: Required every loaded agent to declare non-empty model frontmatter instead of inheriting the runtime/default model.
- 2026-06-24: Added OpenAI-compatible chat-completions automatic compaction after successful above-threshold responses.
- 2026-06-26: Removed Anthropic and OpenAI-compatible chat-completions support; RocketCode now supports only first-party OpenAI Responses and named OpenAI-compatible Responses providers.
- 2026-06-26: Removed remaining OpenAI-compatible Responses provider support, provider-qualified model syntax, and required agent model declarations; RocketCode model behavior returns to unprefixed first-party OpenAI Responses model IDs with missing agent models inheriting the runtime/default model.
- 2026-06-30: Allowed legacy `openai/<model>` strings as aliases normalized to unprefixed first-party OpenAI model IDs while keeping other provider-qualified model strings invalid.
