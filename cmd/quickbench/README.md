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
mocks/bash.json                  # shell doubles (gh, etc.) — PATH stubs on run
elo/criteria.txt                 # required judge criteria
elo/judge.txt                    # required judge model selector
```

`agents/*.md` are normal RocketCode agent files (frontmatter `model`, permissions, body prompt). Capture copies the workspace agent tree so subagents re-run live via `task` with their own models.

Per-variation overlays change one agent without rewriting the whole tree. CLI can also pin agents for a run:

```bash
go run ./cmd/quickbench run ./bench --model gpt-5.4              # matrix root model
go run ./cmd/quickbench run ./bench --model worker=gpt-5.4-mini  # pin one agent
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

```bash
go run ./cmd/quickbench pack ./cmd/quickbench/examples/hello -o hello.bar
go run ./cmd/quickbench unpack hello.bar -o ./hello-dir
go run ./cmd/quickbench dump ./cmd/quickbench/examples/hello
go run ./cmd/quickbench dump hello.bar --names

# providers from ./quickbench.json (see quickbench.json.example)
go run ./cmd/quickbench run ./cmd/quickbench/examples/hello \
  --model gpt-5.4 \
  --model gpt-5.4-mini

go run ./cmd/quickbench capture \
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
2. `quickbench capture --conversation … -o ./bench`
3. Edit `elo/criteria.txt` (and variations if desired).
4. `quickbench run ./bench --model …`

Skill: `cmd/quickbench/skills/archive-benchmarks/SKILL.md`  
Capture playbook: `cmd/quickbench/agents/slack-to-benchmark.md`

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
