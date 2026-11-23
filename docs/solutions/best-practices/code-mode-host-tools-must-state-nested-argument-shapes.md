---
title: Code Mode Host Tools Must State Nested Argument Shapes in Their First Description Line
date: 2026-11-23
category: docs/solutions/best-practices/
module: internal/rocketclaw/harness
problem_type: best_practice
component: execute
severity: medium
applies_when:
  - Adding or changing a Code Mode host tool, including a RocketClaw custom tool, whose parameters hold an object or a list of objects
  - Writing or editing a host tool description that models read through the Code Mode catalog or search
  - Models call a host tool, fail with invalid arguments, and retry one field at a time
  - Changing hostToolSignature, codeModeSystemPrompt or the Code Mode search result
symptoms:
  - "ask_user_question: invalid arguments: validate args: ... has type \"string\", want \"object\""
  - "ask_user_question: invalid arguments: validate args: ... required: missing properties: [\"description\" \"value\"]"
  - Two to six failed execute calls before one ask_user_question call succeeded
  - A model called ask_user_question as a top-level tool, got tool not found, and stopped trying to ask
root_cause: inadequate_documentation
resolution_type: documentation_update
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/codemode
tags:
  - code-mode
  - host-tools
  - tool-description
  - ask-user-question
  - model-behavior
  - eval
---

# Code Mode Host Tools Must State Nested Argument Shapes in Their First Description Line

## Context

In Code Mode, host tools exist only inside `execute` scripts. The model learns about them from two places, and neither shows argument types:

- **The catalog in the system prompt.** `codeModeSystemPrompt` lists each host tool as its signature plus the first line of its description, cut to 120 characters (`internal/rocketclaw/harness/mcp_tools.go:122`–`125`). The signature comes from `hostToolSignature` (`mcp_tools.go:576`), which writes every parameter as `name=...`. So `ask_user_question` showed as `ask_user_question(details=..., multiple=..., options=..., question=...)`.
- **`search(...)` inside a script.** It returns the same untyped signature plus the full description (`mcp_tools.go:542`–`556`). It never returns the JSON schema.

The schema is still enforced. A call that breaks it fails with `invalid arguments: validate args: ...` (`internal/rocketclaw/codemode/codemode.go:329`), and the message names only the first problem.

`ask_user_question` requires `question`, `details`, `options` and `multiple`, and every option must be an object with `label`, `value` and `description`. Its old description never said so. Real sessions on Wallace (Claude Opus 5.5, conversation `T3K3WU4KAWGZV45XSSCAKA6T35`) and on a local instance (entries 4835 and 7198) show the same sequence:

1. Options passed as plain strings: `has type "string", want "object"`.
2. A search, which returned the untyped signature.
3. Options as `{label}` or `{label, value}`: `required: missing properties: ["description" "value"]`.
4. Fields added one per retry, then `multiple` added, once as `false`, which Starlark rejects (`undefined: false`).

That took 2 to 6 failed `execute` calls per question. Each retry ran the whole script again, including any `bash` call in front of the question.

The `decision` host tool avoids the same trap another way: its first line tells the model to search first for the format, and the lines `search` returns spell out its input and output (`internal/rocketclaw/harness/decision.go:114`–`131`).

## Guidance

When a host tool takes an object or a list of objects, put the shape in the **first line** of its description, within 120 characters, so the catalog shows it without a search. Name each nested field and say how to write booleans in Starlark. Put the rest of the guidance on the following lines; `search` returns them.

The fixed `ask_user_question` description in `askUserQuestionTool` (`internal/rocketclaw/backend/bridge.go:3198`) starts:

```text
Ask the human partner and wait. options: list of {label, value, description} dicts; multiple: True or False.
The question shows where they started this turn. All four arguments are required: pass details="" or options=[] when you have none, and multiple=True only when several options may be picked.
```

`TestAskUserQuestionToolDescriptionRejectsCatchAllOptions` in `internal/rocketclaw/backend/bridge_test.go` checks that the first line stays within 120 characters and names the shape. That test covers this one tool only.

The same change reworded the catalog heading to "These tools cannot be called directly. They only work inside code you pass to execute:" (`mcp_tools.go:117`), close to OpenCode's wording.

## Why This Matters

A model that cannot see the shape guesses from habit: plain strings for choices, JSON `false` for booleans. Every wrong guess costs a full script run and a model round trip, and side effects in the script run again.

The effect was measured. Claude Opus 5.5 and GPT-6.2-sol subagents each got the real RocketClaw prompt and tool list, built from this code, in two scenarios: the Wallace conversation at the point where the agent had to ask, and a user who asked to be asked with buttons. Three independent subagents ran each model, prompt version and scenario (36 runs):

| Prompt version | Valid `ask_user_question` calls, of those attempted | Runs that called the tool at the top level |
|---|---|---|
| Old description, old heading (main before the fix) | 0 of 9 | 0 of 12 |
| New description, old heading | 10 of 10 | 0 of 12 |
| New description, new heading | 9 of 9 | 0 of 12 |

Runs that did other work first, such as reading the agent file, made no call to count.

The new description fixed every call for both models. The eval could not measure the heading change, because no run in any version called the tool at the top level. The subagents ran inside OpenCode, whose own instructions also say tools work only inside scripts, which probably hides that mistake. The prompts, results and `score.py` lived under the workspace's `.tmp/eval/` during this work and were not committed.

### Alternatives considered

- **Add a hint to `tool not found`** (`internal/rocketclaw/harness/looper.go:1757`) when the name is a Code Mode host tool. Not done. OpenCode and pi return a plain "not found" error with no hint. Codex instead runs any registered tool, wherever it is called from. OpenCode prevents the mistake in its prompt instead, which is what the new heading copies.
- **Typed signatures in the catalog and search**, as OpenCode renders them from the JSON schema. This would fix every host and MCP tool at once, but it grows the prompt and changes every tool's search output. Not done.
- **Include the schema in the `invalid arguments` error.** This would make the second try right for every tool, but the first call still fails. Not done.

## When to Apply

- Before adding a host tool whose parameters hold objects or lists of objects.
- When reviewing a host tool description: read only its first 120 characters and ask whether a model could write a valid call from them and the untyped signature.
- When a session shows repeated `invalid arguments` failures on one tool.

These host tools had the same gap when this was written and were not changed:

- `rocketclaw_attach_files_to_response` (`attachFilesTool`, `internal/rocketclaw/backend/bridge.go:3094`) requires `path`, `name`, `mime_type`, `content` and `content_base64` on every attachment (`bridge.go:3108`). Its description does not name them.
- `opencode_api` (`openCodeAPITool`, `internal/rocketclaw/backend/opencode.go:704`) takes `params` as a list of `{name, value}` objects (`opencode.go:713`). That shape is not in the first 120 characters.

A generic test could enforce this rule: every host tool whose schema holds object items must name those item fields in its first description line. It would fail on the two tools above until their descriptions change, so it was not added here.

## Examples

Before, the catalog line gave no shape:

```text
- ask_user_question(details=..., multiple=..., options=..., question=...) // Ask the human partner a question where they started this turn and wait for their answer. The options array is only fo...
```

and a typical first call was:

```python
ask_user_question(question="How often should the new cron run?", options=["Hourly", "Daily", "Weekly"], multiple=False)
```

After, the catalog line carries the shape:

```text
- ask_user_question(details=..., multiple=..., options=..., question=...) // Ask the human partner and wait. options: list of {label, value, description} dicts; multiple: True or False.
```

and the first call was valid:

```python
ask_user_question(question="How often should the new cron run?", details="", multiple=False, options=[{"label": "Hourly", "value": "hourly", "description": "Run once an hour"}, ...])
```

## Related

- `docs/solutions/logic-errors/review-request-claiming-its-own-trust-reads-as-injection.md`: another fix to text a model reads, also checked against real models rather than only with string tests.
- `docs/solutions/runtime-errors/execute-uncapped-results-exceeded-model-context.md`: which tools are top-level and which are Code Mode host tools.
- `docs/plans/2026-08-09-001-code-mode-only-host-tools-plan.md`: its risk table already named "Models guess kwargs".
