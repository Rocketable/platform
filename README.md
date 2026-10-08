# Rocketable Platform

Rocketable Platform runs AI agents inside a repository or folder. People talk to the agents through Slack, a built-in web page, or MCP (Model Context Protocol, a standard way for other AI tools to call this one). Agents can read and edit files, run shell commands, use tools and skills, and run saved workflows, but only as far as their permission rules allow.

It is written in Go and runs inside your own workspace. It is not a hosted service shared by many customers.

## Experimental software

Rocketable Platform is experimental. It runs actions chosen by an AI model against your files, shell, connected services, and team chat. Review its configuration, permissions, outputs, and integrations before you use it anywhere sensitive or in production.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## What it does

- Runs agents that follow your workspace's instructions, agent definitions, and skills, under permission rules you write.
- Saves conversations and unfinished work in PostgreSQL. After a restart, work continues from the last finished step.
- Takes requests from Slack, the web page, MCP, scheduled jobs, and one-off or repeating scheduled messages.
- Runs saved workflows written in Starlark, a small Python-like language.
- Sends model requests to OpenAI or to any service with the same API, and can use several services side by side.
- Can send traces of agent runs to OpenTelemetry-compatible tools.

## Main parts

- `internal/rocketcode` (RocketCode): the agent engine. It builds model requests from the workspace, runs tools, checks permissions, and saves a replayable record of each session.
- `internal/rocketclaw` (RocketClaw): the long-running service around RocketCode. It connects Slack, the web page, MCP, scheduled jobs, attachments, and the database. Its program is `cmd/rocketclaw`.
- `cmd/funneld`: a small HTTPS proxy that forwards public paths to internal URLs listed in `funneld.json`.
- `cmd/quickweb`: serves small trusted internal web pages, each with one saved JSON document. See [cmd/quickweb/README.md](cmd/quickweb/README.md).

## Getting started

```sh
go run github.com/Rocketable/platform/cmd/rocketclaw@main
```

Run `rocketclaw help` to list commands. RocketClaw reads `rocketclaw.json` (or the older `femtoclaw.json`) from the current folder. That file points at the workspace, holds the Slack credentials and channels, and turns on other integrations.

A workspace contains `AGENTS.md` and, optionally, `agents/`, `skills/`, `scripts/`, `cron/`, and `workflows/` folders.

### Web page

The web page is built into RocketClaw and listens on `0.0.0.0:3000`. To change that, set `"web": {"listen_address": "127.0.0.1:3000"}`. Links RocketClaw hands out use this machine's Tailscale address.

RocketClaw identifies each browser by IP address, using the `web_users` map (for example `"web_users": {"100.64.0.10": "alice"}`) or, failing that, a Tailscale lookup. It refuses browsers it cannot identify. See the [Web README](internal/rocketclaw/web/README.md) for the page's features and optional Sentry error tracking.

### Slack

Under `slack.channels`, map each channel to a list of agents and the Slack user IDs allowed to use them. Mention the bot in that channel to start a thread. While it works, Slack shows one placeholder message, then the final answer and any files.

Add these bot scopes, then reinstall the Slack app:

- `users:read` and `usergroups:read`, so the web page shows names instead of Slack IDs.
- `channels:read` and `channels:history`, so the bot can expand threads forwarded from public channels. It never joins channels on its own.

See the [command cheatsheet](cmd/rocketclaw/CHEATSHEET.md) for Slack commands.

### MCP and scheduled jobs

MCP clients call `session_prompt` with a conversation ID, an agent, and a configured Slack channel. Each MCP conversation also gets a Slack thread in that channel.

Each active job file in `cron/` names a Slack `channel`. If the job's reply is empty, nothing is posted. Otherwise the reply starts a new thread in that channel.

## How a request runs

1. A request arrives from Slack, the web page, MCP, a cron job, a scheduled message, or a `$workflow` command.
2. RocketClaw passes it to RocketCode with the chosen agent.
3. RocketCode calls the model and runs tools, within the agent's permissions.
4. RocketClaw saves each finished step and sends the reply back where the request came from.

On shutdown, tool calls already running finish and are saved, and background jobs are stopped. After a restart, each conversation continues from its last saved step.

## Agents and permissions

Each agent file must set `model`, either as a model name such as `gpt-5.5` or as a name mapped in `rocketclaw.json` (see [Models and providers](#models-and-providers)).

The root `AGENTS.md` goes into every agent's instructions. To leave it out for one agent, add this to that agent's frontmatter:

```yaml
permission:
  rocketclaw:
    load_agents_md: false
```

For the `bash` tool, RocketCode checks every command in a script on its own, and each command must be allowed. When several rules match, the last one wins. Scripts it cannot parse are refused, even with `bash: allow`.

Some features are off until you turn them on under `permission.rocketclaw`:

- `rocketclaw_list_sessions`, `rocketclaw_get_session`, `rocketclaw_current_session_id`: let an agent read saved conversations. These can read every conversation in the database, not only the agent's own.
- `rocketclaw_set_tag`: lets an agent tag sessions with labels from groups you define. See the [example agent](internal/rocketclaw/skel/agents/examples/session-tags.example.md).
- `allow_background: allow`: lets long scripts and subagents keep running while the conversation moves on.

```yaml
permission:
  rocketclaw:
    rocketclaw_get_session: allow
    allow_background: allow
```

## Skills

Type `$` in Slack or the web page to list commands and skills for the current agent. `$review some text` runs the skill named `review`, and `$stop` stops the current work. To queue a skill instead of running it now, use `$enqueue $review some text`.

Skill templates read their arguments the same way OpenCode commands do: `$ARGUMENTS` is the whole text, and `$1`, `$2`, and so on are single words or quoted phrases.

## Models and providers

The top-level `openai` object is the default model provider. Add more under `providers`, and give models short names under `models`:

```json
{
  "openai": {"rocketcode_auth": "chatgpt"},
  "providers": {"work": {"rocketcode_auth": "chatgpt"}},
  "models": {"coding-high": "work/gpt-5.5"}
}
```

```yaml
model: '{{ model "coding-high" }}'
```

`work/gpt-5.5` uses the `work` provider, and a bare `gpt-5.5` uses `openai`. RocketClaw never switches to another provider on its own. Each provider compacts conversation history at 200000 tokens unless you set `autocompaction_threshold`.

Sign in per provider with `rocketclaw oai login [provider] [--headless]`. Use `rocketclaw oai list` to see sign-ins and `rocketclaw oai logout [provider]` to remove one. Credentials are normally saved in `.rocketclaw/auth.json`.

## Data and storage

- `database_url` sets the PostgreSQL database. Only one RocketClaw process uses a database at a time; others wait. RocketClaw upgrades the database schema at startup. Older RocketClaw versions refuse to start on an upgraded database, so you cannot roll back by swapping in an older binary.
- `attachments` sets where uploaded and generated files are kept. By default they go in `<workspace>/.rocketclaw/attachments`. You can pick another folder or an existing S3 bucket:

  ```json
  "attachments": {"driver": "filesystem", "path": "/srv/rocketclaw/attachments"}
  ```

  ```json
  "attachments": {"driver": "s3", "bucket_arn": "arn:aws:s3:::rocketclaw-attachments"}
  ```

  S3 uses the standard AWS credentials and region settings, and needs `s3:PutObject` and `s3:GetObject`. RocketClaw does not create buckets or make files public.
- Back up the database and the attachment storage together. Restoring only the database brings back references to files, not the files.
- `.rocketclaw/` holds generated runtime files, such as cloned overlays, per-session temporary folders, and oversized command output. Do not treat it as source code.

## Monitoring and profiling

RocketClaw logs a health line once a minute with memory, Go scheduler, and database connection figures.

`rocketclaw run --pprof` turns on Go profiling at `127.0.0.1:6060`. It is off by default. Any process on the machine can reach that port, and profiles can contain private data, so do not expose the port and do not share downloaded profiles. To inspect a remote machine, forward the port over SSH:

```sh
ssh -N -L 127.0.0.1:6060:127.0.0.1:6060 trusted-host
```

## Repository layout

- `cmd/`: runnable programs.
- `internal/rocketcode/`: the agent engine.
- `internal/rocketclaw/`: the service around RocketCode.
- `internal/funneld/`: the HTTPS proxy and its config loading.
- `internal/quickweb/`: the small web page server.
- `internal/netutil/`: shared networking code.
- `vendor/`: copies of Go dependencies.

## Development

```sh
go test ./...
```

`make build` builds RocketClaw and funneld. The RocketClaw step rebuilds the web frontend with Bun before building `bin/rocketclaw`. The built frontend in `internal/rocketclaw/internal/web/dist/` is committed, so a Go-only build does not need Bun. When you change `internal/rocketclaw/web/`, commit the rebuilt `dist/` files too.

`make test` in `internal/rocketclaw` starts PostgreSQL in Docker, or uses `ROCKETCLAW_TEST_DATABASE_URL` if set. GitHub Actions tests against PostgreSQL 18.
