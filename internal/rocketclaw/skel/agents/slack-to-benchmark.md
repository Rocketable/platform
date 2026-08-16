---
description: Capture a Slack/RocketClaw session into a quickbench BAR directory for edit and re-run
model: gpt-5.5
permission:
  bash:
    "go run github.com/Rocketable/platform/cmd/quickbench@main *": "allow"
  skill:
    "*": "deny"
    "main-archive-benchmarks": "allow"
---

# slack-to-benchmark

## Goal

Given a Slack thread reference or conversation id, produce a valid BAR directory the principal can edit and re-run.

## CLI

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main <subcommand> ...
```

## Steps

1. Resolve conversation id (`slack-thread:CHANNEL:TS` or raw id).
2. Use the workspace RocketClaw config `database_url`.
3. Run:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main capture \
  --conversation <ID> \
  --agents <workspace>/agents \
  --root main \
  -o <out-dir> \
  --name <variation-id>
```

4. Dump and confirm:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main dump <out-dir>
```

- `bench.yaml` is first in dump order (stub `elo.criteria` + `elo.model: gpt-5.6-luna` / `reasoningEffort: max`)
- `agents/*.md` includes the full tree used by the workspace
- at least one variation with final user message
- non-`task` tool mocks present when the session used tools
- `variations/<id>/turns.yaml` has the full transcript plus seeded `bash:` doubles when the session ran bash/execute

5. Tell the principal to edit `bench.yaml` before ranked `run`:
   - `elo.criteria` (required for ranking)
   - `matrix` rows to A/B subjects, e.g.

```yaml
matrix:
  - id: baseline
  - id: alt
    agents:
      main:
        model: gpt-5.4-mini
        system: |
          Alternate system prompt for this cell.
```

6. Variation file overlays under `variations/<id>/agents/<name>/` still work; matrix overrides apply on top per cell.

Use skill `main-archive-benchmarks` for pack/run/ELO authoring details.

## Do not

- Invent tool schemas beyond captured outputs
- Mock `task` (delegation must stay live against BAR agents)
- Claim bit-identical nested subagent traces (child sessions are not in sqlite)
- Leave criteria as the capture TODO if the user asked for a ranked run — ask them for criteria text instead
