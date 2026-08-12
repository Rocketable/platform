# quickbench

BAR-based RocketCode benchmarks: pack/unpack, dump, capture from Slack/session state, run a variation×model matrix, and rank with pairwise ELO.

## BAR layout

A BAR is a `.bar` txtar file or an equivalent directory:

```text
meta.txt                         # name, root, ...
agents/<name>.md                 # full RocketCode agent tree (required)
variations/<id>/transcript.json  # root conversation; final message is user
variations/<id>/agents/<name>/model.txt   # optional per-agent model overlay
variations/<id>/agents/<name>/system.txt  # optional per-agent prompt overlay
mocks/tools.json                 # static host-tool mocks (task is never mocked)
mocks/bash.json                  # shell doubles (gh, etc.) via ShellCommand on run
elo/criteria.txt                 # required judge criteria
elo/judge.txt                    # required judge model selector
```

`agents/*.md` are normal RocketCode agent files (frontmatter `model`, permissions, body prompt). Capture copies the workspace agent tree so subagents re-run live via `task` with their own models.

Per-variation overlays change one agent without rewriting the whole tree. CLI can also pin agents for a run:

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main run ./bench --model gpt-5.4
go run github.com/Rocketable/platform/cmd/quickbench@main run ./bench --model worker=gpt-5.4-mini
```

`transcript.json` is a JSON array of `{role, text}` with roles `user` or `assistant` only.

Static tools (not `task`):

```json
[
  {
    "name": "echo",
    "description": "echo",
    "parameters": { "type": "object" },
    "response": "pong"
  }
]
```

## CLI

Preferred (any machine with Go, no checkout required):

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main <subcommand> ...
```

From a platform checkout: `go run ./cmd/quickbench …`.

```bash
go run github.com/Rocketable/platform/cmd/quickbench@main pack ./cmd/quickbench/examples/hello -o hello.bar
go run github.com/Rocketable/platform/cmd/quickbench@main unpack hello.bar -o ./hello-dir
go run github.com/Rocketable/platform/cmd/quickbench@main dump ./cmd/quickbench/examples/hello
go run github.com/Rocketable/platform/cmd/quickbench@main dump hello.bar --names

# providers from ./quickbench.json (see quickbench.json.example)
go run github.com/Rocketable/platform/cmd/quickbench@main run ./cmd/quickbench/examples/hello \
  --model gpt-5.4 \
  --model gpt-5.4-mini

go run github.com/Rocketable/platform/cmd/quickbench@main capture \
  --conversation slack-thread:C0123:1710000000.000100 \
  --db /path/to/workspace/.rocketclaw/state.sqlite3 \
  --agents /path/to/workspace/agents \
  --root main \
  -o ./captured
```

Default capture DB path is `./.rocketclaw/state.sqlite3`; default agents dir is `./agents`. Capture copies the full agent tree, freezes non-`task` tool outputs as mocks, and writes stub `elo/*` files — edit criteria before ranking is meaningful. Runs skip ELO when criteria still contain the capture TODO marker.

**Fidelity notes**

- Agent markdown is copied **verbatim** from the workspace (permissions included). If `task` allows a named subagent, that agent file must be present.
- Nested subagent *internal* turns are not in sqlite. Re-run re-executes `task` live against BAR agents.
- Live CLIs like `gh` are not reproducible in bench. Capture seeds `mocks/bash.json` from observed bash/execute calls. Run injects `rocketcode.Config.ShellCommand` so matching is done in Go against the full command string (exact / `prefix*`); emission is a tiny `/bin/sh -c`. Principal can edit doubles. Unmocked commands fail with `quickbench: unmocked bash command`.

## Capture → edit → run

1. Resolve the Slack thread to a conversation id (`slack-thread:CHANNEL:THREAD_TS`) or use a raw session id.
2. `go run github.com/Rocketable/platform/cmd/quickbench@main capture --conversation … -o ./bench`
3. Edit `elo/criteria.txt` (and variations if desired).
4. `go run github.com/Rocketable/platform/cmd/quickbench@main run ./bench --model …`

## RocketClaw shipping

Source of truth (edit these):

- skill: `cmd/quickbench/skills/main-archive-benchmarks/SKILL.md`
- agent: `cmd/quickbench/agents/slack-to-benchmark.md`

Copy into the embedded skel:

```bash
go generate ./internal/rocketclaw/skel
```

That writes:

- `internal/rocketclaw/skel/.rocketclaw/skills/main-archive-benchmarks/SKILL.md`
- `internal/rocketclaw/skel/agents/slack-to-benchmark.md`

`main` allows skill `main-*`, tasks `slack-to-benchmark`, and bash for `go run …/cmd/quickbench@main *` (wired in skel `agents/main.md`, not generated).

### Hook `slack-to-benchmark` into a Slack channel

RocketClaw only starts agents listed on a channel. After setup/sync ships the skel agent, add it to `rocketclaw.json`:

```json
"slack": {
  "bot_token": "xoxb-...",
  "app_token": "xapp-...",
  "channels": [
    {
      "channel": "#ops",
      "agents": ["main", "slack-to-benchmark"],
      "allowed_user_ids": ["U0123456789"]
    }
  ]
}
```

- First name in `agents` is the default root agent for new mentions (`main` above).
- Restart RocketClaw after editing `rocketclaw.json` (config is not hot-reloaded).
- In `#ops`, start a capture thread with:
  - `@bot $agent slack-to-benchmark capture this thread into a BAR`
  - or `@bot $agent slack-to-benchmark`, then send the capture request in the thread
  - or open `🎛` / bare `$agent` and pick `slack-to-benchmark`
- From `main` in the same channel, you can also task `slack-to-benchmark` or use skill `main-archive-benchmarks` without switching root agent.
- Conversation id for CLI capture is `slack-thread:CHANNEL_ID:THREAD_TS` (channel id like `C…`, not the `#name`).

## Providers

`quickbench.json` in the working directory:

```json
{
  "providers": {
    "openai": {
      "apiKey": "{{env.OPENAI_API_KEY}}",
      "baseURL": ""
    }
  }
}
```

Model selectors are unprefixed OpenAI model ids with optional query flags: `gpt-5.4?reasoningEffort=low&verbosity=medium`.
