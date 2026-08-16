# quickbench

BAR-based RocketCode benchmarks: pack/unpack, dump, capture from Slack/session state, run a variation×model matrix, and rank with pairwise ELO.

## BAR layout

A BAR is a `.bar` txtar file or an equivalent directory:

Pack/dump order puts principal-edit files first:

```text
bench.yaml                       # name, root, matrix, elo.model / criteria
variations/<id>/turns.yaml       # turns + bash doubles + tool mocks
variations/<id>/agents/<name>/model.txt   # optional per-agent model overlay
variations/<id>/agents/<name>/system.txt  # optional per-agent prompt overlay
agents/<name>.md                 # full RocketCode agent tree (required)
```

### `bench.yaml` and the subject matrix

Cells = every `variations/<id>` × every `matrix` row. Omit `matrix` for a single `default` cell that keeps `agents/*.md` as written.

Under each matrix row, `agents.<name>` may set **`model`**, **`system`**, or **both**. Unset fields keep the BAR agent (or variation overlay) value.

```yaml
name: hello
description: minimal BAR sample
root: main
matrix:
  # Baseline: no overrides.
  - id: default

  # Model only (optional query flags: reasoningEffort, verbosity).
  - id: mini-root
    agents:
      main:
        model: gpt-5.4-mini
  - id: luna-max
    agents:
      main:
        model: gpt-5.6-luna?reasoningEffort=max

  # System prompt only (replaces that agent's markdown body / prompt).
  - id: warmer-prompt
    agents:
      main:
        system: |
          Greet the user warmly in one short sentence. Be friendly.

  # Model + system together on the same agent.
  - id: mini-and-warmer
    agents:
      main:
        model: gpt-5.4-mini
        system: |
          Greet the user warmly in one short sentence. Be friendly.

  # Multi-agent: override root and a subagent independently.
  - id: cheap-worker
    agents:
      main:
        model: gpt-5.6-luna?reasoningEffort=max
      worker:
        model: gpt-5.4-mini
        system: |
          Stay terse. Return only the delegated result.

elo:
  model: gpt-5.6-luna
  reasoningEffort: max          # judge; capture default is max
  criteria: |
    Prefer a warm, natural greeting. Penalize verbosity.
```

`system` is the agent **prompt body** (what normally sits under the agent frontmatter), not the transcript. Variation file overlays under `variations/<id>/agents/<name>/` still apply first; matrix overrides apply on top for that cell.

`agents/*.md` are normal RocketCode agent files (frontmatter `model`, permissions, body prompt). Capture copies the workspace agent tree so subagents re-run live via `task` with their own models.

`variations/<id>/turns.yaml` holds the captured conversation and that variation's doubles:

```yaml
turns:
  - role: user
    text: Say hello in one short sentence.
  - role: assistant
    text: |
      [execute] rg --files -g '*.py'
      scripts/foo.py
bash:
  - command: rg --files -g '*.py'
    output: |
      scripts/foo.py
tools:
  - name: echo
    description: echo
    parameters:
      type: object
    response: pong
```

Roles are `user` or `assistant`. Run uses the last user turn as the prompt. `task` is never mocked.

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
# matrix comes from bench.yaml
go run github.com/Rocketable/platform/cmd/quickbench@main run ./cmd/quickbench/examples/hello

go run github.com/Rocketable/platform/cmd/quickbench@main capture \
  --conversation slack-thread:C0123:1710000000.000100 \
  --agents /path/to/workspace/agents \
  --root main \
  -o ./captured
```

Capture reads `database_url` from the selected workspace RocketClaw config. Default agents dir is `./agents`. Capture copies the full agent tree, freezes non-`task` tool outputs as mocks, and writes stub `bench.yaml` (`elo.model: gpt-5.6-luna`, `reasoningEffort: max`, TODO criteria) — edit `elo.criteria` before ranking is meaningful. Runs skip ELO when criteria still contain the capture TODO marker.

**Fidelity notes**

- Agent markdown is copied **verbatim** from the workspace (permissions included). If `task` allows a named subagent, that agent file must be present.
- Nested subagent *internal* turns are not in the state store. Re-run re-executes `task` live against BAR agents.
- Live CLIs like `gh` are not reproducible in bench. Capture seeds `turns.yaml` `bash:` from observed bash/execute calls. Run injects `rocketcode.Config.ShellCommand` so matching is done in Go against the full command string (exact / `prefix*`); emission is a tiny `/bin/sh -c`. Principal can edit doubles. Unmocked commands fail with `quickbench: unmocked bash command`.

## Capture → edit → run

1. Resolve the Slack thread to a conversation id (`slack-thread:CHANNEL:THREAD_TS`) or use a raw session id.
2. `go run github.com/Rocketable/platform/cmd/quickbench@main capture --conversation … -o ./bench`
3. Edit `bench.yaml` (`elo.criteria`, optional `elo.model` / `reasoningEffort`).
4. Edit `bench.yaml` `matrix` if comparing models/agents.
5. `go run github.com/Rocketable/platform/cmd/quickbench@main run ./bench`

ELO ranks the **new run’s final assistant text**, plus a trace-metrics line (turns, tool calls, permission allow/deny after any auto review, tokens in/out/reasoning/cache). It does not re-read the captured `turns.yaml`.

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
