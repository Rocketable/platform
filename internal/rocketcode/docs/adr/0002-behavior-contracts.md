# 0002. Behavior Contracts

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketCode preserves behavior contracts that are more important than internal code shape. Refactors, simplification, dependency updates, and CLOC-reduction work must preserve these contracts unless the human partner explicitly approves a spec change.

## Scope

This ADR governs regression-sensitive behavior for prompt framing, replay, output, compaction, permissions, and filesystem safety. Tool inventories and agent/skill loading details are governed by companion ADRs.

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

- Completed non-interrupted turns append `SessionEntry` rows with version `1`, type `turn`, UTC timestamp, model, replay input, replay output, and output trace.
- Session history is loaded lazily on the first non-empty prompt and ordered by stored row id.
- Empty prompt input with no attachments closes that response channel and does not call the model.
- Replay preserves user and developer messages, assistant messages, reasoning encrypted content, compaction items, function calls, function-call outputs, and supported web-search calls using RocketCode's existing OpenAI Responses-shaped replay encoding, regardless of the provider used for a turn.
- Newly persisted provider-routed turns store provider-qualified model names. Existing unqualified stored model names remain readable as OpenAI models.
- Response output items that cannot be converted back into replay input are recorded in output trace rather than silently becoming prompt input.
- Replay decode errors identify the entry, item, and kind involved.

### Model Turn Loop

- Each model cycle builds history from session entries, runtime system prompt, latest input, previous outputs, and tool outputs.
- Each model cycle routes to the provider selected by the active model string. Provider adapters must preserve prompt framing, local tool call dispatch, tool-output continuation, diagnostics, and replay semantics.
- History before the latest compaction point is pruned so replay starts from the compaction boundary when one exists.
- Compaction steering, when configured, is appended as developer text for compaction behavior. OpenAI Responses context compaction remains provider-specific; Anthropic compaction is unsupported until an explicit provider-specific contract is approved.
- Tool outputs are appended to model input and the turn continues until the model returns no function calls.
- Three repeated identical tool calls are converted into a tool-output failure for the model.
- Tool call permission denial, unknown tool names, and malformed tool permissions are returned as model-visible tool failures rather than process-fatal errors.
- Context cancellation during tool dispatch is fatal to that dispatch and does not become an ordinary tool failure.

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
- `ask`, `external_directory`, and `doom_loop` permission names are unsupported.
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
- `cmd/rocketcode/main.go`
- `cmd/rocketloop/main.go`

## Consequences

- Behavior-preserving simplification must verify prompt framing, replay, output text, permission gates, and safety boundaries independently.
- Behavior-preserving simplification must keep linter checks active unless an exact linter suppression has explicit human approval.
- Any change that intentionally alters these contracts must update this ADR first and receive explicit human approval.
- Tests should assert observable contracts rather than only implementation structure.

## Changelog

- 2026-06-11: Initial accepted snapshot.
- 2026-06-11: Added linter-disable guardrail for behavior-preserving refactors.
- 2026-06-11: Added provider-routing replay and turn-loop contracts for OpenAI and Anthropic model requests.
