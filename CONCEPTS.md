# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Slack Conversations

### Managed Slack Thread

A Slack thread that RocketClaw persists as a conversation owned by a selected agent and continues across human replies.

A Managed Slack Thread has one active turn at a time. Follow-ups received during that turn are buffered and submitted after the turn completes.

### Root Slack Mention

An authorized Slack app mention that creates or targets the root message of a Managed Slack Thread.

A Root Slack Mention can begin the first turn immediately or establish a ready thread for a later human reply, depending on its command form.

### Buffered Follow-Up

A human Slack message accepted while a Managed Slack Thread has an active turn and held for submission after that turn completes.

A Buffered Follow-Up remains associated with its thread until promotion submits it or an explicit failure path consumes it.

## Quickbench

### BAR (Benchmark Archive)

A portable benchmark unit for quickbench. On disk it is a `.bar` file (txtar) or an equivalent unpacked directory. A BAR holds a single `bench.yaml` (metadata + ELO model/criteria), the full RocketCode agent tree, root conversation turns, static host-tool mocks, bash doubles, and optional per-agent variation overlays so multi-agent runs can be reconstructed and ranked. Subagent `task` calls re-execute live against the BAR agents rather than frozen child traces.

### ELO Scorer

The only v1 scoring definition inside a BAR: a crisp criteria prompt plus a judge model and variant. Pairwise comparisons of subject-model outputs produce an ELO ranking; better/worse is defined only by that criteria prompt.

### Quickbench

The single CLI/product surface that packs, unpacks, dumps, runs, and ELO-ranks BARs, and pairs with a RocketClaw skill/subagent that captures Slack sessions into BARs.

## Relationships

- A Root Slack Mention creates or targets a Managed Slack Thread.
- A Buffered Follow-Up belongs to one active Managed Slack Thread and is promoted after the active turn completes.
- A BAR is authored, packed, run, and ranked by Quickbench; an ELO Scorer belongs to one BAR.
