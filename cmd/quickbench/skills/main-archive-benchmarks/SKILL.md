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

3. Edit `bench/elo/criteria.txt` before meaningful ELO. Adjust `elo/judge.txt`, `mocks/bash.json`, and variations if needed.
4. Run:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main run ./bench \
  --model gpt-5.4 \
  --model gpt-5.4-mini
```

Pin one agent for every cell with `--model worker=gpt-5.4-mini`.

## BAR members

| Path | Role |
|------|------|
| `meta.txt` | `name`, `description`, `tags`, `root` |
| `agents/<name>.md` | full RocketCode agent tree (models, prompts, permissions) |
| `variations/<id>/transcript.json` | root turns + final **user** message |
| `variations/<id>/agents/<name>/model.txt` | optional model overlay |
| `variations/<id>/agents/<name>/system.txt` | optional prompt overlay |
| `mocks/tools.json` | static host-tool mocks (`task` is never mocked) |
| `mocks/bash.json` | shell doubles (exact command or `prefix*`); unmocked bash fails closed |
| `elo/criteria.txt` | pairwise judge criteria |
| `elo/judge.txt` | judge model selector |

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
