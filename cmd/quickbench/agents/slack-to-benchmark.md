# slack-to-benchmark

## Goal

Given a Slack thread reference or conversation id, produce a valid BAR directory the principal can edit and re-run.

## Steps

1. Resolve conversation id (`slack-thread:CHANNEL:TS` or raw id).
2. Locate `state.sqlite3` (workspace `.rocketclaw/state.sqlite3` unless told otherwise).
3. Run:

```bash
go run ./cmd/quickbench capture \
  --conversation <ID> \
  --db <path-to-state.sqlite3> \
  --agents <workspace>/agents \
  --root main \
  -o <out-dir> \
  --name <variation-id>
```

4. Open the BAR (`quickbench dump <out-dir>`) and confirm:
   - `agents/*.md` includes the full tree used by the workspace
   - at least one variation with final user message
   - stub `elo/criteria.txt` present
   - non-`task` tool mocks present when the session used tools
5. Tell the principal to edit `elo/criteria.txt` before `quickbench run`.
6. To A/B one agent only, add variation overlays under `variations/<id>/agents/<name>/` or run with `--model <name>=SEL`.

## Do not

- Invent tool schemas beyond captured outputs
- Mock `task` (delegation must stay live against BAR agents)
- Claim bit-identical nested subagent traces (child sessions are not in sqlite)
- Leave criteria as the capture TODO if the user asked for a ranked run — ask them for criteria text instead
