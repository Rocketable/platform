# 0001. Runtime Capabilities

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketCode is a workspace-local Go reasoning runtime with an interactive CLI, a non-interactive autonomous loop CLI, and an embeddable library API. It uses provider-routed model requests, an OpenAI Responses-shaped internal replay model, workspace-local agent and skill definitions, sandbox-aware tools, and replayable session history.

## Scope

This ADR governs current user-visible runtime capabilities for `internal/rocketcode`, `cmd/rocketcode`, and `cmd/rocketloop`. Tool-specific contracts, agent and skill loading, permissions, and output details are governed by companion ADRs.

## Context

RocketCode is under tight source-line budget pressure. Future simplification and deletion work must preserve current behavior unless the human partner explicitly approves a spec change. This ADR records the baseline capabilities that must survive line-reduction work.

## Normative Contracts

| Area | Current capability |
| --- | --- |
| Library runtime | `rocketcode.New` constructs a `Looper` from an OpenAI client, config, rooted workspace filesystem, parsed agents, parsed skills, default agent name, and optional diagnostics writer. Provider-aware construction may also supply OpenAI, Anthropic, and named OpenAI-compatible clients explicitly. RocketCode startup validates that every loaded agent declares a frontmatter model and that each declared model uses a configured provider. |
| Default agent | The standalone commands require a loaded `main` agent. Missing default agent is a startup error. Missing or empty `model` frontmatter on any loaded agent is a startup error. |
| Workspace root | Standalone `rocketcode` and `rocketloop` use the process current working directory as the workspace root and open it through `*os.Root`. |
| Root instructions | `AGENTS.md`, when present in the workspace root, is loaded literally into the system prompt and followed by a current-workspace block containing the host workspace root. |
| Model defaults | Empty model defaults to OpenAI `gpt-5.4`. Empty reasoning effort defaults to `high`. Empty compact threshold defaults to `200000`. |
| Model selection | Models must be provider-qualified as `openai/<model>`, `anthropic/<model>`, or `openai-compatible/<provider>/<model>`. Unprefixed model strings and empty model reference parts are startup errors. Every loaded agent must declare non-empty `model` frontmatter; runtime/default models do not fill missing agent model declarations. Agent frontmatter `model` values may select a different provider from the runtime default. |
| Model request | OpenAI runtime turns use the OpenAI Responses API with stored responses disabled, encrypted reasoning content included, reasoning summary enabled when reasoning effort is set, context compaction enabled, and OpenAI parallel tool calls enabled. Anthropic runtime turns use the Anthropic Messages API through an adapter that preserves RocketCode's turn-loop, replay, local tool semantics, and Anthropic server-side compaction blocks. OpenAI-compatible runtime turns use the configured named compatible provider in either `responses` mode or `chat_completions` mode; missing selected compatible providers fail at startup and, if somehow reached, fail clearly before request dispatch. |
| Rate limits and context limits | Provider rate limits retry after at least one minute when the provider exposes retryable rate-limit status, considering provider retry/reset headers where available. OpenAI-compatible `chat_completions` rate-limit errors follow the same retry semantics as OpenAI when SDK status, code, or type fields expose a retryable status. OpenAI Responses and OpenAI-compatible `chat_completions` context-length API errors trigger explicit progressive compaction of older safe replay blocks and retry before surfacing a runtime error. Other failed responses and API errors surface as runtime errors, with provider diagnostics emitted when diagnostics are enabled. |
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
| `ROCKETCODE_MODEL` | Overrides the default model string for standalone commands. It must use `openai/<model>`, `anthropic/<model>`, or `openai-compatible/<provider>/<model>`. Unprefixed values are startup errors. |
| `ROCKETCODE_REASONING_EFFORT` | Overrides the default reasoning effort for standalone commands. |
| `ROCKETCODE_DIAG` | Any non-empty value enables diagnostics. |
| `ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS` | Any non-empty value enables stronger skill replay behavior. |
| `ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS` | Empty, `0`, or `false` disables shell expansion. `1` or `true` enables primary, subagent, and skill prompts but not input prompts. Comma tokens may include `primary`, `subagent`, `skill`, `input`, and `all`; unknown tokens are errors. |
| `ROCKETCODE_COMPACT_THRESHOLD` | Overrides compact threshold and must be a positive integer. |
| `ROCKETCODE_COMPACTION_STEERING` | Adds compaction steering text when set. |
| `ROCKETCODE_OPENAI_COMPATIBLE_PROVIDER` | Names the single standalone OpenAI-compatible provider. If any `ROCKETCODE_OPENAI_COMPATIBLE_*` variable is set, provider, API key, base URL, and mode are all required. |
| `ROCKETCODE_OPENAI_COMPATIBLE_API_KEY` | Supplies the standalone OpenAI-compatible provider API key. |
| `ROCKETCODE_OPENAI_COMPATIBLE_BASE_URL` | Supplies the standalone OpenAI-compatible provider base URL. |
| `ROCKETCODE_OPENAI_COMPATIBLE_MODE` | Supplies the standalone OpenAI-compatible provider mode, either `responses` or `chat_completions`. |

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
