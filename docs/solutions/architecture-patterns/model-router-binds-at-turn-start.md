---
title: Model Router Binds at Turn Start
date: 2026-08-24
category: docs/solutions/architecture-patterns/
module: internal/rocketcode
problem_type: architecture_pattern
component: service_layer
severity: medium
applies_when:
  - "A routed agent uses a Model Router and Model Options"
  - "Choosing whether to invoke the Model Router from runTask or runTurn"
  - "The incoming message is available only at runTurn"
  - "Routed agents omit a host model"
  - "Model Options must share one provider because session history is filtered by DisplayModel provider"
tags:
  - model-router
  - model-options
  - run-turn
  - run-task
  - harnessbridge
  - display-model
---

# Model Router Binds at Turn Start

## Context

A Model Router is a loaded agent named by another agent's `modelRouter` frontmatter. Before the routed agent runs, the router receives that turn's incoming message and the routed agent's Model Options and must return one allowed model / reasoning effort / verbosity triple. There is no fallback outside that list. A Model Router cannot itself have a Model Router.

`New` cannot see the incoming message. `Loop` is multi-turn. The work model must be chosen from that turn's text, then rebound onto the same looper before the provider call. Binding at construct time would freeze one pick for the whole `Loop`. Binding again in `runTask` would double-route a `task` child, because that child already enters `Loop`.

This tree already implements the turn-start bind. The shipping PR is still open (pending merge), not landed: https://github.com/Rocketable/platform/pull/5

## Guidance

Bind once per user or task turn, inside `runTurn` → `prepareTurn` → `bindModelRouter`. Do not resolve the work model in `New`. Do not also run the router in `runTask`.

**Turn start is the only bind.** `runTurn` calls `prepareTurn` before it writes the session record or talks to the work provider (`internal/rocketcode/looper.go:629-644`). `prepareTurn` expands the prompt, then binds (`internal/rocketcode/model_router.go:18-28`). The router sees the same `input.Text` the turn will send.

**`bindModelRouter` is a no-op unless this looper is routed and can spawn a child.** It returns immediately when `l.agent.ModelRouter == ""` or `l.factory == nil` (`internal/rocketcode/model_router.go:31-34`). Otherwise it runs the router, resolves the pick, and overwrites this looper's client and model fields (`internal/rocketcode/model_router.go:36-53`). A failed pick, invalid JSON, or a triple not in Model Options fails the turn. There is no host-model fallback (`internal/rocketcode/model_router.go:133-139`).

**`runModelRouter` is a hidden structured child run, cloned from the guardrail pattern.** It loads the named router agent, resolves that agent's own static model, and loops it with a one-shot user prompt that contains the incoming message plus the allowed options (`internal/rocketcode/model_router.go:56-123`). The last assistant message must unmarshal and match one Model Options entry exactly. Router output is operator-only. It is not written into the parent session.

**`New` stubs provider identity. It does not pick.** When `agent.ModelRouter != ""`, `resolveActiveAgentModel` does not call `resolveModel`. It returns a nil client, a `ProviderOrigin` from the first Model Options model, and `DisplayModel` equal to that first option (`internal/rocketcode/rocketcode.go:558-561`). The factory stays on the root looper so `runTurn` can spawn the router.

That stub exists for harnessbridge, not for the work call. After `New`, Slack and raw runs filter session history with `providerForModel(looper.DisplayModel)` (`internal/rocketclaw/harnessbridge/bridge.go:1301-1302`). `providerForModel` takes the prefix before `/`, or `openai` when there is no prefix (`internal/rocketclaw/harnessbridge/provider_replay.go:16-23`). `New` / `validateAgentGraph` already requires every Model Options model to share one provider (`internal/rocketcode/rocketcode.go:615-627`), so the first option's string is a safe pre-bind stand-in.

**`runTask` skips resolve. It does not bind.** When the task target has `ModelRouter` set, `runTask` does not call `resolveModel`. It sets origin to that shared provider and display model to the first option, then builds the child with a nil client and the factory attached (`internal/rocketcode/tasks.go:207-262`). The child's `Loop` → `runTurn` is what binds, using the task prompt as the incoming message.

Keep the factory on every routed looper that must bind. A routed root without `factory` would no-op bind and then fail because `Client` is nil. `New` / `validateModelRouters` already forbids a router-on-router. A routed agent cannot be a guardrail.

## Why This Matters

- Follow-ups can change models. Each `Loop` line is a new `runTurn`. Binding at `New` would reuse the first pick for every later human message.
- `task` stays one route. The parent constructs a stub child; the child binds once on the task prompt. A second `runModelRouter` in `runTask` would run the router twice on the same prompt.
- Fail-closed stays at the pick. A miss never falls back to `config.Model` or a host `model` field.
- Session filter stays pre-`Loop`. Harnessbridge needs a provider string before the router runs. Mixed providers are rejected at `New` / `validateModelOptionProviders` so that string cannot lie.
- Message handling does not change. The router is a hidden child.

## When to Apply

Apply this bind shape when adding or changing Model Router behavior, constructing a looper whose work model is not known until the incoming prompt exists, or reviewing a change that looks like it should also route in `runTask` or resolve the pick in `New`.

Do not apply it to unrouted agents, guardrails, permission reviewers, mid-turn switching after the routed agent has started, or giving the router agent its own Model Router.

Until https://github.com/Rocketable/platform/pull/5 merges, treat this as the working-tree contract, not as already-on-default.

## Evidence

- Human turn: `Loop` calls `runTurn` → `prepareTurn` → `bindModelRouter`. A second human line repeats the bind; the pick may differ.
- Rejected pick: a triple not in Model Options fails `prepareTurn`. `runTurn` never builds a work-model request (`internal/rocketcode/model_router.go:137-139`).
- Task prompt: `runTask` skips `resolveModel` (`internal/rocketcode/tasks.go:207-216`). Bind uses the task prompt as the router user text (`internal/rocketcode/tasks_test.go:16-40`).
- Construct without a work client: `NewWithModelResolver` for routed `main` succeeds with empty `loop.Model` and `DisplayModel` from the first option (`internal/rocketcode/models_test.go:221-240`).

## Related

- Open PR (pending merge): https://github.com/Rocketable/platform/pull/5
- No overlapping learnings in `docs/solutions/` (corpus is two unrelated logic-error docs).
