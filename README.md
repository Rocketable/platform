# Rocketable Platform

Rocketable Platform runs AI agents inside a repository or folder. People talk to the agents through a built-in web page, Slack, or MCP (Model Context Protocol, a standard way for other AI tools to call this one). Agents can read and edit files, run shell commands, use tools and skills, and run saved workflows, but only as far as their permission rules allow.

It is written in Go and runs inside your own workspace. It is not a hosted service shared by many customers.

## Experimental software

Rocketable Platform is experimental. It runs actions chosen by an AI model against your files, shell, connected services, and team chat. Review its configuration, permissions, outputs, and integrations before you use it anywhere sensitive or in production.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## What it does

- Runs agents that follow your workspace's instructions, agent definitions, and skills, under permission rules you write.
- Saves conversations and unfinished work in PostgreSQL. After a restart, work continues from the last finished step.
- Takes requests from the web page, Slack mentions, MCP, scheduled jobs, and one-off or repeating scheduled messages.
- Runs saved workflows written in Starlark, a small Python-like language.
- Sends model requests to OpenAI or to any service with the same API, and can use several services side by side.
- Can send traces of agent runs to OpenTelemetry-compatible tools.

## Main parts

- `internal/rocketcode` (RocketCode): the agent engine. It builds model requests from the workspace, runs tools, checks permissions, and saves a replayable record of each session.
- `internal/rocketclaw` (RocketClaw): the long-running service around RocketCode. It connects the web page, Slack, MCP, scheduled jobs, attachments, and the database. Its program is `cmd/rocketclaw`.
- `cmd/funneld`: a small HTTPS proxy that forwards public paths to internal URLs listed in `funneld.json`.
- `cmd/quickweb`: serves small trusted internal web pages, each with one saved JSON document. See [cmd/quickweb/README.md](cmd/quickweb/README.md).

## Getting started

```sh
go run github.com/Rocketable/platform/cmd/rocketclaw@main
```

Run `rocketclaw help` to list commands. RocketClaw reads `rocketclaw.json` (or the older `femtoclaw.json`) from the current folder. That file points at the workspace, holds the Slack credentials and channels, and turns on other integrations.

A workspace contains `AGENTS.md` and, optionally, `agents/`, `skills/`, `scripts/`, `cron/`, and `workflows/` folders.

### Web page

The web page is the main way to work with RocketClaw. It is built into RocketClaw and listens on `0.0.0.0:3000`. To change that, set `"web": {"listen_address": "127.0.0.1:3000"}`. Links RocketClaw hands out use this machine's Tailscale address.

RocketClaw identifies each browser by IP address, using the `web_users` map (for example `"web_users": {"100.64.0.10": "alice"}`) or, failing that, a Tailscale lookup. It refuses browsers it cannot identify. See the [Web README](internal/rocketclaw/web/README.md) for the page's features and optional Sentry error tracking.

#### Voice

The Voice page (`/voice`) and the voice button in each session's composer let you talk to an agent out loud. The browser sends audio straight to OpenAI's GPT-Live voice model. Each task the voice model hands off becomes a Web message in the conversation, marked `media=Voice`. The agent's progress reaches the voice model silently, and the final reply is spoken. A conversation has one live call at a time: starting voice on the desk ends the call on the phone. Ending a call never stops the agent's work.

Voice has no settings of its own. It uses the provider behind the conversation agent's model:

- `rocketcode_auth: api_key`: the public GPT-Live API, model `gpt-live-1`. It costs about $0.05 per minute, silence included.
- `rocketcode_auth: chatgpt`: Codex's internal voice route, model `gpt-live-1-codex`, with the provider's ChatGPT login. This route is unofficial and may stop working at any time. The ChatGPT account's data settings apply.

Per call, OpenAI receives your microphone audio, up to about 12,000 characters of the conversation's recent messages, the agent's public progress (tool names with their state, and partial reply text), its replies, and its questions. It never receives tool arguments, tool results, reasoning, or attachments. A call ends when you stop it, when the page closes or stays hidden for 30 seconds, when nobody speaks within a minute of starting, and after 5 minutes with nothing said and no work running.

The microphone needs a secure page. A browser on the RocketClaw machine itself can use `http://127.0.0.1:3000`, with the `127.0.0.1` mapping below; `localhost` may connect from `::1`, which needs its own mapping. Phones and other devices need HTTPS, which Tailscale can provide (the tailnet needs HTTPS certificates turned on):

```sh
tailscale serve --bg http://127.0.0.1:3000
```

- Point `serve` at `127.0.0.1`, not `localhost`, which may resolve to `::1`.
- Every request through `serve` comes from `127.0.0.1`, so map it to yourself, using your Tailscale login: `"web_users": {"127.0.0.1": "you@example.com"}`.
- Never use `tailscale funnel`: it would put RocketClaw on the public internet.
- Limit who can reach the `serve` port with tailnet ACLs.

With that mapping, every process on this machine and every tailnet device that can reach the `serve` port acts as you. Agent tools and MCP servers running here are such processes. Optionally set `"web": {"listen_address": "127.0.0.1:3000"}` to close the direct path. That also breaks the Tailscale-IP links RocketClaw hands out. See the [transport README](internal/rocketclaw/frontend/rpc/README.md#identity-boundary) for the identity details.

### Slack

Slack has three jobs: showing cron reports, showing MCP sessions, and quick help when an allowed user mentions the bot. Everything else happens in the web page.

Under `slack.channels`, map each channel to a list of agents and the Slack user IDs allowed to use them. A row named `@` is not a channel: it gives agents and allowed users for mentions in any other channel or group DM the bot is already in.

Only a mention from an allowed user starts work. Messages without a mention, including replies in threads the bot answers in, reactions, and edited messages start nothing, and 1:1 DMs never start work. A top-level mention in a configured channel starts a new thread answered by the channel's first agent; a top-level mention with no text, file, or forward is ignored. A mention in a thread the bot does not know takes that thread over and reads up to 50 earlier messages. A mention in a thread the bot already answers in is that conversation's next turn and adds only the mention itself. It waits for running work and active goals instead of steering them. Mentions keep their files and forwarded threads. The bot marks each accepted mention with 🤖 and accepts it once, even if Slack delivers it again. If it cannot read the thread or take the request, it replies with a short error.

While it works, Slack shows one placeholder message, then the final answer and any files. Mention replies and cron reports end with a footer: the agent, its state (working, done, failed, or stopped), and an Open in Web link.

Cron report threads and MCP threads are read-only in Slack: the bot ignores every reply and mention in them, even after their record expires. Open the conversation in Web to keep working.

Slack has no commands. `$stop`, `$agent`, `$goal`, `$enqueue`, skill names, and any other `$` text reach the agent as plain text. There is no agent picker, steering, queue card, question button, or reaction control. Mention turns cannot use `ask_user_question`, so the agent asks in its reply or hands the work to Web. Use Web to switch agents, run commands and skills, start goals, manage queued work, and stop any turn. Web messages used in a Slack thread are still posted there, and every reply in that conversation still goes to Slack.

Set up the Slack app with the bot event `app_mention` and these bot scopes, then reinstall it:

- `users:read` and `usergroups:read`, so the web page shows names instead of Slack IDs.
- `channels:read` and `channels:history`, so the bot can expand threads forwarded from public channels. It never joins channels on its own.

The `message.*` event subscriptions and Interactivity are no longer used and can be removed.

Slack's own Stop button needs two more steps: turn the app into an agent (**Agents** in the app settings, manifest `features.agent_view`), which cannot be undone, and subscribe to the bot event `agent_session_stopped`. Then each turn a mention started shows Slack's working state until it ends, and Stop from an allowed user ends the turn and its goal, like Web `$stop`. Slack drops the working state after one hour, and RocketClaw does not renew it. A workspace without agent sessions logs the refusal and runs turns normally; stop them from the footer's Web link. `agent_view` also adds an agent chat to the app's Messages tab that RocketClaw never answers.

See the [cheatsheet](cmd/rocketclaw/CHEATSHEET.md) for the full Slack app setup.

Release note for mention-only Slack:

- Slack is now mention-only, and cron and MCP threads are read-only.
- Edited messages don't start work, even when the edit adds a mention.
- Mentions wait behind an active goal in their conversation.
- Queue cards and agent-picker buttons posted by earlier versions do nothing, and so do ⏫ and 🛑 reactions.
- Mention replies and cron reports end with a footer that links to Web. Where the workspace supports Slack agent sessions, an allowed user can stop a running mention turn with Slack's Stop; elsewhere, open the footer link and stop the turn in Web.
- Slack no longer shows questions. Question buttons posted by earlier versions do nothing, and a question still waiting fails when its turn resumes.

### MCP and scheduled jobs

MCP clients call `session_prompt` with a conversation ID, an agent, and a configured Slack channel. Each MCP conversation also gets a read-only Slack thread in that channel. Switch its agent or continue it from Web.

Each active job file in `cron/` names a Slack `channel`. If the job's reply is empty, nothing is posted. Otherwise the reply starts a new read-only thread in that channel; follow up from its conversation in Web.

## How a request runs

1. A request arrives from the web page (including a `$workflow` command), a Slack mention, MCP, a cron job, or a scheduled message.
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

Bash rules match command text, not everything a program can do. An executable-path wildcard such as `./scripts/*` does not match executable paths containing `..`; name such an executable explicitly if it is intended. This check is not a shell sandbox: an approved script can still launch other programs or follow symlinks. To put a sandbox in front of one agent's shell commands, set `customShell` in that agent's frontmatter to the absolute path of a wrapper program; see the [cheatsheet](cmd/rocketclaw/CHEATSHEET.md#agent-frontmatter-and-permissions) for what the wrapper must do.

Some features are off until you turn them on under `permission.rocketclaw`:

- `rocketclaw_list_sessions`, `rocketclaw_get_session`, `rocketclaw_current_session_id`: let an agent read saved conversations. These can read every conversation in the database, not only the agent's own. To protect the database, each call is limited: a list needs a `since` time and returns at most 200 conversations, and a read returns at most the 100 newest entries before `before_entry_id`, ending with `[next_before_entry_id=N]` when older entries remain. A list also takes a required `criteria` string: empty lists every stored conversation, and anything else is a Web `/search` query (free text plus `tag:`, `cron:`, `agent:`, `room:`, `is:` and `sort:`) that returns only the chats `/search` would, with their matching messages.
- `rocketclaw_set_tag`: lets an agent tag sessions with labels from groups you define. See the [example agent](internal/rocketclaw/skel/agents/examples/session-tags.example.md).
- `allow_background: allow`: lets long scripts and subagents keep running while the conversation moves on.

```yaml
permission:
  rocketclaw:
    rocketclaw_get_session: allow
    allow_background: allow
```

## Skills

Type `$` in the web page to list commands and skills for the current agent. `$review some text` runs the skill named `review`, and `$stop` stops the current work. To queue a skill instead of running it now, use `$enqueue $review some text`. In Slack, `$` text is plain text for the agent.

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

When profiling is enabled, RocketClaw also samples blocking events at an average of one per 10 milliseconds spent blocked and one in 100 mutex contention events.

`rocketclaw run` turns on Go profiling at `127.0.0.1:6060` by default. Use `rocketclaw run --pprof=false` to turn it off. Any process on the machine can reach that port, and profiles can contain private data, so do not expose the port and do not share downloaded profiles. To inspect a remote machine, forward the port over SSH:

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

`make test` in `internal/rocketclaw` starts PostgreSQL in Docker, or uses `ROCKETCLAW_TEST_DATABASE_URL` if set. It runs the Go tests once with race detection and coverage, enforcing the fixed `COVERAGE_REQUIRED` minimum in its Makefile. GitHub Actions tests against PostgreSQL 18.
