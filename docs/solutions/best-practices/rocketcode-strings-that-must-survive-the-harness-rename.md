---
title: RocketCode strings that must survive the harness rename
date: 2026-10-29
last_updated: 2026-10-30
category: docs/solutions/best-practices/
module: internal/rocketclaw/harness
problem_type: best_practice
component: development_workflow
severity: high
applies_when:
  - Finishing the RocketCode-to-harness rename, or any grep-and-replace of `rocketcode`
  - Renaming a string literal that looks like a name but is written to the database, disk, telemetry, or a model prompt
tags:
  - rocketcode
  - harness
  - rename
  - persisted-strings
  - trace-types
  - span-names
related_components:
  - database
  - observability
---

# RocketCode strings that must survive the harness rename

## Context

RocketCode used to be a separate component, `internal/rocketcode`, with its own size budget and CI job. It was folded into RocketClaw as `internal/rocketclaw/harness`, with `codemode` and `mcpclient` beside it. The move renamed import paths, the package, comments, docs, and the config key `rocketcode_auth` (now `auth`, with `rocketcode_auth` still read as an alias in `internal/rocketclaw/config/config.go`).

Many strings still spell `rocketcode`. Some look like leftovers that a follow-up should finish renaming. Several of them are not names at all: they are values already stored in the database, folders already on disk, telemetry keys that dashboards query, or text the model reads. A repo-wide `rocketcode` -> `harness` sweep breaks them without any test failing: the tests pin these literals in files the same sweep rewrites, and no test holds data saved under the old spelling.

## Guidance

Before renaming a `rocketcode` string, decide which group it belongs to.

**Keep as is (stored or external contracts):**

- **Turn trace types** stored in `turns.output_trace_json`: `rocketcode_public_progress`, `rocketcode_code_mode_call`, `rocketcode_code_mode_review` (`internal/rocketclaw/harness/public_progress.go:70`, `:99-100`). Readers match the type exactly (`public_progress.go:70`, `:128`), so renaming hides every progress line and Script Call Record already saved. The progress type is also written by raw SQL, not only by Go (`internal/rocketclaw/backend/store_dao.go:1279`), so a Go-only rename would also split new writes between two spellings.
- **Runtime folders** under the runtime directory: `<runtimeDir>/.rocketcode/spill` (`internal/rocketclaw/backend/raw_run.go:276`; the harness's own fallback is `internal/rocketclaw/harness/execute_results.go:23`), `<runtimeDir>/.rocketcode/tmp/<conversation>` (`internal/rocketclaw/backend/bridge.go:2597-2612`), and the hand-off and raw-run scratch folders (`internal/rocketclaw/backend/handoff.go:33`, `internal/rocketclaw/backend/raw_run.go:65`). Existing spills and retained output live there, and resetting the runtime directory keeps `.rocketcode` by name (`internal/rocketclaw/skel/skel.go:597`). Permission rules a person wrote may also name these paths; the built-in shell-temp grant is derived from the configured path and moves with it.
- **Telemetry names**: the tracer `rocketcode` (`internal/rocketclaw/backend/bridge.go:2736`, `internal/rocketclaw/backend/raw_run.go:140`), spans `rocketcode.turn`, `rocketcode.provider`, `rocketcode.tool.*`, the span event `rocketcode.provider.diagnostic` (`internal/rocketclaw/harness/observability.go:95`), and the `rocketcode.*` attributes (`internal/rocketclaw/harness/observability.go:119`; `internal/rocketclaw/harness/looper.go:803`, `:807`, `:1321`). PostHog and Phoenix views are keyed on them; see `docs/solutions/best-practices/posthog-needs-genai-span-attributes.md`.
- **MCP client name** `rocketcode-mcp-client` (`internal/rocketclaw/mcpclient/registry.go:24`), which outbound MCP servers receive and may log or allowlist.
- **Config key** `rocketcode_auth`, the old name of a provider's `auth` (`internal/rocketclaw/config/config.go:217`; `cmd/rocketclaw/oai.go:215-218` reads it and rewrites it to `auth`). Existing `rocketclaw.json` files still use it.
- **Model-facing text**: the compaction checkpoint ("compacted by RocketCode", `internal/rocketclaw/harness/looper.go:2560`), the automatic permission reviewer's prompt (`internal/rocketclaw/harness/permission_review.go:211`, `:216`), and the shipped skills that name it (`internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/SKILL.md`, `internal/rocketclaw/skel/.rocketclaw/skills/main-update-rocketclaw-json/SKILL.md:120`). Changing them is a prompt change and needs the same care as one, not a rename.

**Safe to rename:** Go identifiers and test names (`prepareRocketCode`, `rocketcodeConfig`, `rocketcodeRetainedDir`, `loadRocketCodeDefinitions`, ...), comments, and current docs. These are code, so the compiler and tests catch mistakes.

If a stored or external name really has to change, treat it as a migration: keep reading the old value (as `config.go` does for `rocketcode_auth`), or rewrite the stored data, and tell the people who own the dashboards.

## Why This Matters

The broken cases fail quietly. After a repo-wide sweep, a renamed trace type still passes the tests (they were rewritten too), but the Web loses the progress and code-mode records of every turn saved before the change. A renamed span silently empties the dashboards. A renamed spill folder orphans existing files, and a runtime reset would delete the old folder instead of keeping it. None of these show up as a compile error or a failing unit test, so code review and CI do not stop them.

## When to Apply

- A follow-up "finish the RocketCode rename" or "remove leftover rocketcode" task.
- Any repo-wide string replacement that touches `internal/rocketclaw/harness`, `internal/rocketclaw/backend`, `internal/rocketclaw/mcpclient`, `internal/rocketclaw/config`, `internal/rocketclaw/skel`, or `cmd/rocketclaw`.

## Examples

Find every candidate first, then sort them into the groups above rather than replacing them in bulk:

```sh
grep -rni 'rocketcode' cmd internal --exclude-dir=node_modules
```

This is case-insensitive and has no file-type filter on purpose: it also finds `RocketCode` in prompts and identifiers, and in non-Go files.

Safe (identifier):

```go
// before
root, agents, skills, resolver, err := prepareRocketCode(locked, agent, logger, toolModeWorkflow)
// after
root, agents, skills, resolver, err := prepareHarness(locked, agent, logger, toolModeWorkflow)
```

Not safe (stored value, read back by exact match):

```go
// public_progress.go: changing this literal hides all saved progress
raw, _ := json.Marshal(publicProgressTrace{Type: "rocketcode_public_progress", Progress: snapshot})
```

## Related

- `docs/solutions/best-practices/posthog-needs-genai-span-attributes.md`: what PostHog reads from the `rocketcode.*` spans.
- `docs/solutions/logic-errors/permission-review-reasons-read-from-transport-specific-trace.md`: readers find saved records in `OutputTrace` by their trace type.
- `docs/solutions/runtime-errors/execute-uncapped-results-exceeded-model-context.md`: the spill files kept under `.rocketcode/spill`.
- `CONCEPTS.md`, the Harness entry: RocketCode as the Harness's former name.
