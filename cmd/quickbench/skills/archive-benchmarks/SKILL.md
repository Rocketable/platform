---
name: archive-benchmarks
description: Author BAR benchmarks and capture RocketClaw Slack/session threads into quickbench BAR archives for ELO ranking. Use when creating, packing, capturing, or running BAR files under cmd/quickbench.
---

# archive-benchmarks

## When to use

- Turn a Slack thread / RocketClaw session into a re-runnable BAR
- Author or edit BAR members (variations, mocks, ELO criteria)
- Pack/unpack/dump/run via `go run ./cmd/quickbench`

## Capture loop

1. Identify conversation id:
   - Slack thread: `slack-thread:CHANNEL_ID:THREAD_TS`
   - Or the raw RocketClaw conversation id
2. Capture:

```bash
go run ./cmd/quickbench capture \
  --conversation slack-thread:C012:1710000000.000100 \
  --db "$WORKSPACE/.rocketclaw/state.sqlite3" \
  -o ./bench
```

3. Edit `bench/elo/criteria.txt` (required before meaningful ELO). Adjust `elo/judge.txt` and variations if needed.
4. Run:

```bash
go run ./cmd/quickbench run ./bench --model gpt-5.4 --model gpt-5.4-mini
```

## BAR members

| Path | Role |
|------|------|
| `meta.txt` | `name`, `description`, `tags`, `root` |
| `agents/<name>.md` | full RocketCode agent tree (models, prompts, permissions) |
| `variations/<id>/transcript.json` | root turns + final **user** message |
| `variations/<id>/agents/<name>/model.txt` | optional model overlay |
| `variations/<id>/agents/<name>/system.txt` | optional prompt overlay |
| `mocks/tools.json` | static host-tool mocks (`task` is never mocked) |
| `elo/criteria.txt` | pairwise judge criteria |
| `elo/judge.txt` | judge model selector |

Fidelity: capture copies workspace `agents/*.md` and portable `session_entries`. Subagent internals are not in sqlite; re-run re-executes `task` with the BAR agent tree so you can change one agent's model or prompt without changing another.

## Pack / dump

```bash
go run ./cmd/quickbench pack ./bench -o bench.bar
go run ./cmd/quickbench dump bench.bar
```

See `cmd/quickbench/README.md` and `cmd/quickbench/agents/slack-to-benchmark.md`.
