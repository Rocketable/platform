---
name: quickbench-benchmarks
description: Create quickbench benchmark YAML files for RocketCode-backed LLM evaluation. Use when writing, reviewing, or explaining quickbench benchmark files, expected assertions, inference transcripts, static tools, or examples under cmd/quickbench/examples.
metadata:
  version: "1.0"
---

# Quickbench Benchmarks

Use this skill when the user wants to create or review `quickbench` benchmark YAML files.

Quickbench benchmark files describe benchmark behavior only. They do not select models. Models are selected at runtime with repeatable `--model` flags such as `openai/gpt-5.5?reasoningEffort=high&verbosity=low` or `anthropic/claude-sonnet-4-20250514`.

## File Placement

- Put benchmark files under a directory that will be passed to `quickbench FULL_PATH_TO_DIR`.
- Use `.yaml` or `.yml` extensions.
- Quickbench scans recursively.
- Keep each file self-contained and model-independent.

## Required Shape

A benchmark file should use this structure:

```yaml
name: short benchmark name
description: what behavior this validates
tags: [category, another-category]
runs: 1
timeout: 2m

tools: []

inference:
  - system: System instructions for this benchmark.
  - user: Final prompt being benchmarked.

expected:
  text:
    - regexp: '(?i)expected text'
```

Required in practice:

- `name`: non-empty string.
- `inference`: at least one message and must end with a non-empty `user` message.
- `expected`: optional but benchmarks without assertions are usually not useful.

Optional but recommended:

- `description`: explain the tested behavior.
- `tags`: categories for humans.
- `runs`: positive integer; omitted or `0` means one run.
- `timeout`: Go duration such as `30s`, `2m`, or `1m30s`.

## Inference Transcript

`inference` is a list of one-key maps:

```yaml
inference:
  - system: You are testing one narrow behavior.
  - user: Earlier user turn.
  - assistant: Earlier assistant response.
  - user: Final benchmark prompt.
```

Rules:

- `system` is optional, but if present it must be first.
- At most one `system` message is allowed.
- Prior `user` and `assistant` messages become conversation history.
- The final `user` message is the prompt being benchmarked.
- Supported roles are only `system`, `user`, and `assistant`.

## Text Assertions

Text assertions use Go regular expressions:

```yaml
expected:
  text:
    - regexp: '(?i)refund approved'
    - regexp: '\bcase-[0-9]+\b'
```

Guidance:

- Use `(?i)` for case-insensitive matching.
- Escape backslashes as YAML requires.
- Prefer assertions that check behavior, not exact prose.
- Avoid overly broad regexps such as `.*` unless the real assertion is a tool assertion.

## Static Tools

Static tools are currently the implemented tool backend. They expose a JSON Schema tool declaration to the model, record calls, and return a configured static response.

```yaml
tools:
  - name: choose_route
    description: Pick the route to use.
    parameters:
      type: object
      required: [route]
      properties:
        route:
          type: string
          enum: [fast, cheap, safe]
    static:
      response: '{"ok": true}'
```

Tool rules:

- `name` is required.
- Exactly one backend must be present.
- Use `static.response` for current benchmarks.
- `cli`, `http`, and `mcp` are planned shapes but are not implemented yet and currently fail validation.
- Put enums and required fields in `parameters`; that is how the model sees constraints.

## Tool Assertions

Tool assertions verify observed tool calls:

```yaml
expected:
  tools:
    ordered: false
    calls:
      - name: choose_route
        arguments:
          route: safe
```

Semantics:

- Each expected call must be observed at least once.
- `arguments` uses recursive subset matching.
- Extra observed fields are allowed.
- `ordered: true` requires expected calls to appear in that order.
- `ordered: false` allows calls in any order.

Example subset match:

```yaml
expected:
  tools:
    calls:
      - name: create_ticket
        arguments:
          priority: high
          customer:
            tier: enterprise
```

This passes if the model calls `create_ticket` with those fields plus additional fields.

## Model-Free Benchmarks

Do not put models in benchmark YAML.

Good:

```sh
go run ./cmd/quickbench \
  --model 'openai/gpt-5.5?reasoningEffort=high&verbosity=low' \
  --model 'anthropic/claude-sonnet-4-20250514' \
  cmd/quickbench/examples
```

Bad:

```yaml
models:
  - openai/gpt-5.5
```

## Authoring Checklist

Before saving a benchmark:

- Confirm the file extension is `.yaml` or `.yml`.
- Confirm `name` is present.
- Confirm `inference` ends with a `user` message.
- Confirm any `system` message is first and unique.
- Confirm every expected tool is declared in `tools`.
- Confirm every tool has exactly one backend.
- Confirm current tool benchmarks use `static.response`.
- Confirm regexps compile as Go regexps.
- Confirm no model names appear in the YAML.

## Examples

Use `cmd/quickbench/examples/enum-route.yaml` as the canonical in-repo example.

For a reusable starting point, see `assets/benchmark-template.yaml`.
