---
name: main-archive-benchmarks
description: YOU MUST USE THIS SKILL to capture RocketClaw Slack/session threads into quickbench BAR archives, author BAR members, pack/unpack/dump, run variation×model matrices, and ELO-rank outputs
---

# main-archive-benchmarks

## When to use

- Turn a Slack thread / RocketClaw session into a re-runnable BAR
- Author or edit BAR members (agents, variations, mocks, bash doubles, ELO criteria)
- Pack, unpack, dump, or run BARs and print ELO ladders

For a focused capture-only handoff, task the `slack-to-benchmark` agent.

## CLI

Always invoke quickbench with:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main <subcommand> ...
```

Do not assume a local checkout of platform or an installed `quickbench` binary. Prefer `@main` so operators get the published tool.

Verify once with:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main --help
```

## Capture loop

1. Identify conversation id:
   - Slack thread: `slack-thread:CHANNEL_ID:THREAD_TS`
   - Or the raw RocketClaw conversation id
2. Capture (agents dir required for full tree fidelity):

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main capture \
  --conversation slack-thread:C012:1710000000.000100 \
  --db "$WORKSPACE/.rocketclaw/state.sqlite3" \
  --agents "$WORKSPACE/agents" \
  --root main \
  -o ./bench
```

Default DB is `./.rocketclaw/state.sqlite3`; default agents dir is `./agents`.

3. Edit `bench/bench.yaml` first: `elo.criteria`, optional `elo.model` / `reasoningEffort`, and `matrix` rows. Adjust `variations/*/turns.yaml` (`turns`, `bash`, `tools`) if needed.
4. Run:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main run ./bench
```

## Matrix (model and/or system prompt)

Each matrix row is one subject config. Per agent you may set `model`, `system`, or both:

```yaml
matrix:
  - id: default
  - id: model-only
    agents:
      main:
        model: gpt-5.4-mini
  - id: system-only
    agents:
      main:
        system: |
          Be warmer. One short sentence.
  - id: model-and-system
    agents:
      main:
        model: gpt-5.6-luna?reasoningEffort=max
        system: |
          Be warmer. One short sentence.
  - id: multi-agent
    agents:
      main:
        model: gpt-5.6-luna?reasoningEffort=max
      worker:
        model: gpt-5.4-mini
        system: |
          Stay terse. Return only the delegated result.
```

- `model` — OpenAI model id, optional `?reasoningEffort=…` / `?verbosity=…`
- `system` — replaces that agent’s prompt body for the cell
- omit a field to keep the BAR agent (after variation overlays)
- cells = every `variations/<id>` × every matrix row

## BAR members

| Path | Role |
|------|------|
| `bench.yaml` | name, root, `matrix` (model/system), `elo.*` (edit first) |
| `variations/<id>/turns.yaml` | `turns` + `bash` doubles + `tools` mocks; run uses last user |
| `variations/<id>/agents/<name>/model.txt` | optional model overlay |
| `variations/<id>/agents/<name>/system.txt` | optional prompt overlay |
| `agents/<name>.md` | full RocketCode agent tree (models, prompts, permissions) |

Fidelity: capture copies workspace `agents/*.md` and portable `session_entries`. Subagent internals are not in sqlite; re-run re-executes `task` against the BAR agent tree. Live host CLIs are not used on re-run — only bash doubles.

## Pack / dump

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main pack ./bench -o bench.bar
go run github.com/Rocketable/platform/cmd/quickbench@main dump bench.bar
go run github.com/Rocketable/platform/cmd/quickbench@main dump bench.bar --names
```

## Do not

- Invent tool schemas beyond captured outputs
- Mock `task` (delegation stays live against BAR agents)
- Claim bit-identical nested subagent traces
- Leave capture TODO criteria if the human asked for a ranked run — ask for criteria text instead
- Use a checkout-only `go run ./cmd/quickbench` unless the human is developing platform itself
