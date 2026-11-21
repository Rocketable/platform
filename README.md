# Rocketable Platform

Rocketable Platform runs AI agents inside a folder you choose, the workspace. Its service, RocketClaw, serves a web page on your tailnet and also answers Slack mentions, MCP calls, and scheduled jobs. Agents read and edit files, run shell commands, and use tools and skills, but only as far as the permission rules you write allow.

It is written in Go and runs on infrastructure you control. It is not a hosted service.

## Experimental software

Rocketable Platform is experimental. It runs actions chosen by an AI model against your files, shell, connected services, and team chat. Review its configuration, permissions, outputs, and integrations before you use it anywhere sensitive or in production.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## How it works

1. A request arrives from the web page, a Slack mention, MCP, a cron job, or a scheduled message.
2. RocketClaw hands it to its agent harness with the chosen agent.
3. The harness calls the model and runs tools within the agent's permissions.
4. Each finished step is saved in PostgreSQL, and the reply goes back where the request came from.

After a restart, every conversation continues from its last saved step.

- `cmd/rocketclaw`, `internal/rocketclaw`: the service.
- `internal/rocketclaw/harness`: the agent engine. It builds model requests, runs tools, checks permissions, and records each session.
- `cmd/funneld`: a small HTTPS proxy (see [funneld](#funneld)).
- `cmd/harnessfeedback`: an MCP server agents use to report harness problems (see [Harness feedback server](#harness-feedback-server)).

[CONCEPTS.md](CONCEPTS.md) defines the project's vocabulary.

## Getting started

### Requirements

- Linux on amd64 or arm64.
- Go (`go.mod` names the version).
- A PostgreSQL database. CI tests against PostgreSQL 18.
- A Tailscale tailnet with MagicDNS and HTTPS certificates turned on.
- An OpenAI API key (or a key for a service with the same API), or a ChatGPT login.
- For the [emulated shell](#emulated-shell): `bwrap`, `mmdebstrap`, `uidmap`, and cgroup v2.

### Quick start

1. Install: `go install github.com/Rocketable/platform/cmd/rocketclaw@main`.
2. In the workspace folder, create `rocketclaw.json`:

   ```json
   {
     "database_url": "postgres://rocketclaw@localhost:5432/rocketclaw",
     "web": {"tsnet": {"hostname": "rocketclaw", "tags": ["tag:rocketclaw"]}}
   }
   ```

   The tailnet policy must list `tag:rocketclaw` under `tagOwners`.
3. Run `rocketclaw ts login` to put the web page's node on your tailnet.
4. Run `rocketclaw oai login`, or add `"openai": {"api_key": "…"}` to the file.
5. Run `rocketclaw run`, then open `https://rocketclaw.<tailnet>.ts.net`.

Chat with the built-in `main` agent to begin.

### Commands

- `rocketclaw run [--pprof=false|HOST:PORT] [--aws-secrets-manager-arn ARN] [--aws-secrets-manager-users-arn ARN]` starts the server.
- `rocketclaw oai login [provider] [--headless]`, `oai list`, `oai logout [provider]` manage ChatGPT sign-ins.
- `rocketclaw ts login` logs the web page's node in.
- `rocketclaw help` shows usage. `rocketclaw` alone starts the server when `rocketclaw.json` exists.

`rocketclaw run` exits with 0 after a shutdown signal and 1 on an error. It exits with 255 to ask for a restart, after the `rocketclaw_restart` tool or when the web page stops serving. It never restarts itself, so run it under a supervisor.

## Configuration

`rocketclaw.json` in the current folder holds setup and secrets: the database, the workspace (default: the file's folder), and the credentials for providers, Slack, and External MCP.

Any value can be read from AWS Secrets Manager by writing `{"aws": {"arn": "ARN", "key": "KEY"}}` in its place. Because of that, an object whose only key is `aws` is always read as a reference, so don't name a provider, server, or facet `aws`. `--aws-secrets-manager-arn ARN` merges a whole JSON secret over the file; with it, the file is optional.

### Behavior settings

`auto_approver_model`, `slack.channels`, `logging.level`, `overlays`, and `embedded_agents` live in PostgreSQL. The first start copies them from the file; after that, the file's values are ignored with a warning. People listed in `web.admins` (Tailscale logins) edit them on the Config page, and a save applies at once or is refused whole.

If stored settings stop RocketClaw from starting (the error starts with `stored behavior settings:`), fix them with `psql`:

```sh
psql "$DATABASE_URL" -At -c 'SELECT document FROM behavior_settings' > behavior.json
# fix behavior.json, then:
psql "$DATABASE_URL" -v doc="$(cat behavior.json)" <<'SQL'
UPDATE behavior_settings SET document = :'doc'::jsonb;
SQL
```

`DELETE FROM behavior_settings` re-imports from the file on the next start, but loses every Web edit.

### Workspace and overlays

A workspace holds `agents/`, `skills/`, `scripts/`, and `cron/`. Every agent comes from there or from an overlay, except the [built-in agents](#built-in-agents).

`overlays` lists Git repositories, each `address[@ref]`, that add their own `agents/`, `skills/`, `cron/`, and `scripts/`. Built-in files come first, then overlays in order, then the workspace; a later file replaces an earlier one with the same path, except that nothing replaces a built-in agent. A ref is a branch, tag, or full commit hash and cannot contain `/`. Use absolute paths for local overlays.

Overlays are downloaded fresh into `.rocketclaw/overlays/` at every start, reload, and overlay-list change, so edits made there are lost. A failed download stops startup, or fails the reload and keeps the previous copies.

## Web page

The web page is the main way to use RocketClaw. RocketClaw joins your tailnet as its own Tailscale node and serves the page only there, at `https://<hostname>.<tailnet>.ts.net`. Each browser is signed in as its device's Tailscale user. See the [Web README](internal/rocketclaw/web/README.md) for its features.

The tab title is `<name> - RocketClaw`, where `name` is `server_display_name` or the tsnet hostname.

A chat's history is a tree: Undo and Revert move the chat back without deleting anything, and the next message starts a new branch. Neither undoes file edits or other effects.

### Tailnet setup

```json
"web": {"tsnet": {"hostname": "rocketclaw", "tags": ["tag:rocketclaw"]}}
```

The hostname defaults to `rocketclaw` and uses only `a-z`, `0-9`, and `-`. Tags start with `tag:`.

1. With RocketClaw stopped, run `rocketclaw ts login` and follow the link, or set `web.tsnet.auth_key` to a pre-approved, tagged auth key.
2. Confirm in the Tailscale admin console that the node joined your tailnet.
3. Start RocketClaw. It brings the node up before anything else and exits with code 1 if the node needs a login or the tailnet has no HTTPS names.

Keep it safe:

- Tag the node, and allow only named users to reach it and the node to reach nothing. An untagged node acts as the user who logged it in.
- Local processes on a machine signed in to Tailscale as a person, agent tools included, reach the page as that person.
- Tagged devices are refused, and no request header can choose the user.
- Pages on other sites can't send requests to RocketClaw or show it in a frame, except the [remote RocketClaws](#remote-rocketclaws) it trusts. Hosted Pages' own files can still be loaded as images, scripts, or styles.
- Node keys expire after 180 days unless the node is tagged.
- Tailnet HTTPS publishes machine names in public certificate-transparency logs.
- The node's state lives in `rocketclaw/tsnet-<hostname>` under `$XDG_CONFIG_HOME` or `~/.config`. Give two RocketClaws on one machine different hostnames and `--pprof` ports.
- Never use `tailscale funnel`; it puts RocketClaw on the public internet.

See the [transport README](internal/rocketclaw/frontend/rpc/README.md#identity-boundary) for identity details.

### Voice

The Voice page (`/voice`) and each composer's voice button let you talk to an agent. The browser streams audio straight to OpenAI's GPT-Live model, which hands tasks to the agent as Web messages and speaks the final reply.

- Replies over about 990 tokens are shown on screen instead of spoken.
- Typing during a call cancels pending spoken replies and mutes the voice.
- Questions from the agent must be answered in the app.
- Each person has one live call at a time. Ending a call never stops the agent's work.
- The Voice page offers only `sticky: user` agents, such as the built-in `voice`.

Voice always uses the default `openai` provider's credentials:

- `auth: api_key` uses the public `gpt-live-1` model, about $0.05 a minute, silence included.
- `auth: chatgpt` uses Codex's unofficial `gpt-live-1-codex` route, which may stop working at any time.

OpenAI receives your audio, up to 40 recent messages (600 bytes each, 12,000 bytes in total), and the replies it speaks. It never receives tool calls, tool results, reasoning, or attachments.

A call ends when you hang up, the page stays hidden for 30 seconds, or OpenAI ends it. Lost connections retry for up to 15 minutes. The microphone needs HTTPS, so on a phone open `https://<hostname>.<tailnet>.ts.net/voice` with Tailscale connected.

### Remote RocketClaws

One web page can operate several RocketClaws. List the others locally, and list this page on each remote:

```json
"rocketclaws": {"work": {"url": "https://rocketclaw.tail1234.ts.net"}}
```

```json
"trusted_rocketclaws": ["https://team.tail1234.ts.net"]
```

Names use `a-z`, `0-9`, and `-`; addresses are `https` origins with no path. Changes take effect on restart. The browser calls each remote directly and signs in as you. Sessions, search, notifications, and tabs then span every server. A remote that is unreachable or not trusting the page shows as unavailable.

Only list pages you trust: a trusted page can act as whoever opens it.

### OpenCode servers

The web page can open sessions on OpenCode servers:

```json
"opencode": {"devbox": {"url": "http://devbox.example.ts.net:4096", "password": "set-me"}}
```

RocketClaw signs in as `opencode` with the password. Agents call these servers with `opencode_api` under the `opencode` permission. Changes take effect on restart.

### Space permissions

Everyone the page admits sees everything, unless `permissions` limits them to some spaces:

```json
"permissions": {"contractor@example.com": ["Support", "Ops"]}
```

Logins and space names match ignoring case, and each list needs at least one space. A limited person sees and uses only the sessions, agents, crons, pages, and skills of their spaces. They get no OpenCode, Reload, or Behavior Settings. While the built-in `voice` agent is on, keep its space, `Main`, out of every list, because it can reach any space's chats. These limits cover the web page only; each [remote RocketClaw](#remote-rocketclaws) needs its own.

Agents act with their own permissions, not the person's. Give the agents of a limited space none of these, or they can reach beyond it:

- the `rocketclaw_sessions` tools
- a host shell (anything but `shell: emulated`)
- `sudo`
- `task` for agents outside the space
- OpenCode servers
- the `main-update-rocketclaw-json` skill

### Agent editor

**Create New Agent** and **Edit** on the Agents page open an editor where you build an easy or advanced agent with plain-language help, chat with drafts before saving, and compare variants. Teammates can edit a draft together. Test chats with drafts run real tools, so only people without space limits can use it. Built-in agents can't be edited here.

Saving writes the agent to `agents/`, or to an overlay by push or pull request, then reloads. Each agent shows where it comes from: `built-in`, its overlay, or `workspace`.

## Agents

An agent is a Markdown file in `agents/`: frontmatter for settings and a body of instructions. `model` is required, for example `gpt-6.1-sol` or `work/gpt-6.1-sol#high`. One agent file that fails to load stops startup. To include `AGENTS.md`, write ``!`cat AGENTS.md` `` in the body.

Agents come in two formats: advanced (the default, with a `permission` block) and [easy mode](#easy-mode-agents).

### Built-in agents

`main` and `voice` are built into the binary:

- `main`: a `sudo` agent with one session shared by everyone (`sticky: global`).
- `voice`: an easy mode agent with one session per person (`sticky: user`) in the space `Main`, used by the [Voice](#voice) page. It can start or message the chats of any non-sudo, non-sticky agent in any space.

No file replaces them, and other agents can't hand them work. The `embedded_agents` Behavior Setting turns each on or off and picks its model:

```json
"embedded_agents": {"main": {"model": "work/gpt-6.1-sol#high"}, "voice": {"disabled": true}}
```

### Permissions

Each tool gets `allow`, `deny`, or `auto` (reviewed first). When several rules match, the last wins.

- `bash` rules are checked per command in a script, and unparseable scripts are refused. Rules match command text, so they are not a sandbox; use a `shell` wrapper or `shell: emulated` for one.
- Shell commands from a person's turn get `ROCKETCLAW_PRINCIPAL_NAME`, `ROCKETCLAW_PRINCIPAL_EMAIL`, and `ROCKETCLAW_PRINCIPAL_ID` (a SHA-256 of the lowercased email). Per-person rules should use the email or ID, never the name. These rules separate people's work for convenience; they are not a security boundary.
- `sudo: yes` gives an agent every tool, current and future, with every call reviewed. Rules in its `permission` block apply on top, so `task: {main: deny}` blocks one delegation. Skills, `task`, questions, navigation, goals, attachments, scheduled messages, reload, and web search skip review. It does not lift `.env` blocking, workspace confinement, or `maxRecursion`, and subagents don't inherit it.

Under `permission.rocketclaw`, name each tool; wildcards are refused:

- `rocketclaw_sessions`: read any saved conversation, start or message Web sessions, and, for agents without `space:`, move their own session to another space. Reads are capped at 200 conversations or 100 entries per call. Each write later delivers one `<session_reply>` note with the other session's answer.
- `rocketclaw_store`, `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list`, `rocketclaw_kv_store`, `rocketclaw_kv_read`: publish sandboxed Hosted Pages at `/pages/<space>/<path>`, with JSON Page Data. Pages belong to the chat's space: every chat in that space can replace or delete them, and they outlive the chat that stored them. People can open a page beside a chat from the chat tab's menu.
- `rocketclaw_navigate_for_me` (on by default in turns a person starts from Web): open another page of this server's Web in that person's focused window, once.
- `rocketclaw_set_tag`: tag sessions (see the [example agent](internal/rocketclaw/skel/agents/examples/session-tags.example.md)).
- `allow_background: allow`: keep long scripts and subagents running in the background, and steer, queue to, or stop them.
- `rocketclaw_sessions_recipient`: who may write to this agent's sessions: `true` (everyone; the default except for sudo agents), `false`, or a list of agent names.

```yaml
permission:
  rocketclaw:
    rocketclaw_sessions: auto
    allow_background: allow
    rocketclaw_sessions_recipient: [main]
```

### Reviews

`auto` calls go to the guardian, the built-in reviewer, or to a custom reviewer agent named with `auto(name)`. Every review sees where the turn came from (`Web`, `Slack`, `Cron`, `ExternalMCP`, `System`, or `ScheduledMessage`), who asked, and the turn's input. So rules like "cron turns never use write credentials" can be enforced.

A decision model can settle reviews first. Configure OpenAI's Decisions API or TypeSafe under `decision`, and set `auto_approver_decision` (`{"model": "openai/gpt-6-luna", "min_confidence": 0.9}`). A review settles there only when every answer clears `min_confidence` and nothing needs a closer look; everything else goes to the guardian. Agents granted `permission.decision` can also call `decision(...)` from scripts to classify items.

### Code Mode scripts

Agents call `read`, `glob`, `grep`, `apply_patch`, `webfetch`, `bash`, `decision`, platform tools, and MCP tools from Starlark scripts sent to the `execute` tool.

- Every call returns a result, and a failed call does not stop the script. A success acts like its plain value; a failure is falsy, and its `.error` names the tool (`read: File not found: notes.txt`). Every result has `.ok`, `.error`, `.unwrap()` (stops the script on a failure), and `.or_else(x)`. A failure passed into another call is returned without running that call.
- `gather` and `map` return every branch's result; `try_gather` and `try_map` stop at the first failure; `race` returns the first success.
- `read` returns raw text, or a file object for images and PDFs. `bash(command=..., stdin=...)` pipes text or a file into a command.
- `webfetch` returns text only and follows up to 10 redirects, checking each one like a new URL. Redirects to private or internal addresses are refused unless the agent's own rules name that host.
- Only what `main()` returns reaches the model, up to 20 MiB of text and files per call.

Under an `allow` rule, `bash` runs whatever is piped into it unreviewed, so allowing a bare interpreter such as `python3` allows any code. Under `auto` or `sudo`, reviewers see the stdin, and text stdin over 64 KiB is refused.

### Outside MCP tools

`mcp_servers` lists MCP servers that agents call from `execute` scripts. Each sets `command` (run on the host) or `url`, and splits its tools into named facets:

```json
"mcp_servers": {
  "stripe": {
    "url": "https://mcp.stripe.com",
    "tools": {"read": ["list_*", "get_*", "-get_secret_*"], "write": ["create_*", "update_*"]}
  }
}
```

`*` and `?` are wildcards, and a leading `-` removes matches. A tool in no facet is reachable by no agent. A server connects only when a script calls one of its tools or `search`, and gets the turn's `ROCKETCLAW_*` values as environment variables or `X-Rocketclaw-*` headers.

Agents grant facets by name: `mcp: {stripe: [read]}` in an easy agent, or `mcp: {stripe: {read: allow, write: auto}}` under an advanced agent's `permission`. Sudo agents get every facet, reviewed.

### Sticky agents

`sticky` limits how many sessions an agent has:

- `off` (default): every new session starts fresh.
- `user`: one session per person.
- `global`: one session shared by everyone.
- `slack`: answers only in Slack, with one session per channel and one per thread.

Starting a new Web session with a sticky agent reopens its existing session. Sticky sessions can't be forked, and other agents reach them only by writing to their session. Scheduled runs of a `global` agent report into its session; a `user` agent can't be scheduled. External MCP accepts only `sticky: off` agents.

### Easy mode agents

`mode: easy` is a shorter format. Every section left out (or `auto`) turns on everything in it, and a listed section turns on only what it lists, except `mcp` and `opencode`, which are off unless listed. Everything turned on is reviewed by the agent's approver, except what skips review under `sudo`. `model`, `space`, and `workingDirectory` are required.

```yaml
---
mode: easy
model: openai/gpt-6.1-sol#high
space: Marketing
workingDirectory: sessions/marketing/$ROCKETCLAW_CONVERSATION_ID
subagents: [openai/gpt-6-luna#max]
approver:
  model: openai/gpt-6-luna#max
  prompt: "Deny write actions the user did not explicitly request."
secretsARN: ["arn:aws:secretsmanager:us-east-1:123456789012:secret:marketing"]
commands:
  stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"
mcp:
  acme_google_marketing: [read]
allowedNetworkTargets: [api.stripe.com]
routines:
  - "*/5 * * * *": "Check the campaign dashboard for anomalies."
---

Agent instructions.
```

- Commands run in the [emulated shell](#emulated-shell), in `workingDirectory`, unless `shell` names a wrapper program. File tools see only that folder, plus the chat's uploads and the agent's skills, read-only.
- `commands` are host programs the sandbox may start; they get the `secretsARN` secrets, which never enter the sandbox.
- `allowedNetworkTargets` limits `webfetch` and the shell's `curl`.
- The approver sees only the agent's folder, plus whatever its own `approver` keys turn on, which run without review. An `execute` script is reviewed once, whole, except that a `bash` call with text stdin is reviewed again with its stdin.

### Emulated shell

`shell: emulated` runs an agent's shell commands in Bash inside a Linux sandbox: a pinned Ubuntu 26.04 with `python3` (NumPy, SciPy), `node`, and `sqlite3`. Each command starts fresh; only files in `workingDirectory` persist.

- No network, except `curl` GET requests through RocketClaw's `allowedNetworkTargets` rules. Private and link-local addresses are refused by default.
- Each command gets 256 MiB of memory, 64 processes, one CPU, and 16 MiB of output.
- The host needs `bwrap`, unprivileged user namespaces (on Ubuntu, an AppArmor profile for `/usr/bin/bwrap`), cgroup v2 with `Delegate=yes`, and, to build the image, `mmdebstrap` and a subordinate uid range. The first start builds the image in the background, and the web page shows its progress.
- In Docker, also run with `--cap-add SYS_ADMIN` and `--security-opt` set to `apparmor=unconfined`, `seccomp=unconfined`, and `systempaths=unconfined` (one flag each). The entrypoint, as root, must remount `/sys/fs/cgroup` read-write, move the container's processes into a leaf cgroup, enable `cpu`, `memory`, and `pids`, and hand a sibling cgroup to RocketClaw's user. Keep the runtime folder's `sandbox` on a case-sensitive local filesystem.
- To pick up Ubuntu security updates, run `go generate ./internal/rocketclaw/sandbox` and commit the result. `ROCKETCLAW_SANDBOX_PACKAGE_MIRROR` and `ROCKETCLAW_SANDBOX_PACKAGE_CACHE` speed up builds.

### Shadow agents

`agents/<agent>/<shadow>.md` defines a shadow: an alternative agent that gets the same inputs as its parent, so you can compare their answers in the web page.

**Shadows run real tools. This is not a dry run.** Their edits, commands, and external writes really happen, can race with the parent's, and use the shadow's own permissions. Start with limited permissions and non-critical work.

Each shadow adds its own model and tool cost, and may take at most 1.5 times its parent's steps. Scripts can tell they run in a shadow from `ROCKETCLAW_SHADOW`. Shadows can't ask questions, schedule work, or start background jobs.

### Skills

Skills work as in OpenCode. A skill is a `*.md` file directly in `skills/` or a `SKILL.md` up to six folders down; its ID is its file or folder name, so `skills/team/review/SKILL.md` is `review`. The model is offered skills that have a `description`, unless they set `disable-model-invocation: true`. The agent's `skill` rules decide which skills it may load or run.

Type `/` in the web page to list commands and skills. `/review some text` runs the `review` skill, and `/enqueue /review some text` queues it. In the skill, `$ARGUMENTS` is the whole text and `$1`, `$2` are its words or quoted phrases.

## Slack, MCP, and schedules

### Slack

Slack is optional and does one thing: answer mentions from allowed users. Set `slack.bot_token` and `slack.app_token`, then map each channel in `slack.channels` to a `sticky: slack` agent and its allowed Slack user IDs. None is built in, so add one to the workspace or an overlay. A channel named `@` covers every other channel the bot is in.

- A top-level mention is answered at the top of the channel. The agent can move the conversation into the thread with `slack_respond_in_thread(true)`.
- A mention in a thread continues that thread's conversation; a new thread's last 50 messages are read first.
- Slack has no commands; `/` text after a mention reaches the agent as plain text. Use the web page for everything else.
- Each answer ends with a footer that links to the chat in the web page.

Slack app setup:

- Bot events: `app_mention`, `channel_rename`, `group_rename`, `agent_session_stopped`, and `agent_session_title_changed`.
- Bot scopes: `users:read`, `usergroups:read`, `users:read.email`, `channels:read`, `channels:history`, and `groups:read` (private channels only).
- Turn the app into an agent (**Agents** in the app settings; this can't be undone). This gives each thread Slack's working state and a Stop button.

### External MCP

Set `mcp_external.enabled` and `mcp_external.listen_addr`. Clients call `session_prompt` with a conversation ID and a `sticky: off` agent, which you add to the workspace or an overlay; each conversation becomes a Web chat. `mcp_external.users` maps logins to passwords for HTTP Basic auth. Without it, no login is required, and `{}` refuses everyone. `--aws-secrets-manager-users-arn ARN` reads the logins from a secret instead.

### Cron and scheduled messages

A file in `cron/` runs the agent it names with `agent:` on a schedule: a cron expression, a duration such as `30m`, or an `@daily`-style shortcut. A non-empty reply opens a Web chat. A file with no `agent:`, or one naming an agent that can't be scheduled, never runs, and the web page's warning strip lists it.

Any agent except `sticky: user` and `sticky: slack` ones can also schedule itself with `routines` in its own file (see the [easy mode example](#easy-mode-agents)); easy mode agents can only be scheduled this way. Shell snippets in cron files and routine instructions run before the model reads them, without review.

Agents schedule follow-ups with `rocketclaw_schedule_message`, once or repeating, up to `2160h` (90 days) ahead.

### Retention

Ordinary chats are deleted 90 days after their last message. Sticky chats stay, but their messages older than 90 days are deleted. Queued work and pending schedules keep a chat alive.

## Models and providers

The top-level `openai` object is the default provider. Add others under `providers`, and pick one with `provider/model`:

```json
{"openai": {"auth": "chatgpt"}, "providers": {"work": {"auth": "chatgpt"}}}
```

- `auth` is `api_key` (the default; set `api_key`) or `chatgpt`, which `rocketclaw oai login [provider]` sets for you. `api_base_url` points a provider at any compatible service.
- A `#effort` suffix such as `#high` sets reasoning effort; without it, the model's default applies (the guardian uses `low`).
- History is compacted at 200000 tokens unless `autocompaction_threshold` says otherwise.
- `auto_approver_model` runs the guardian (default `gpt-6-luna`), `agent_editor_model` runs the editor's assistant (default: `main`'s model), and `title_model` names sessions (default `openai/gpt-6-luna#low`). The editor's assistant runs as `main`, so it fails while `main` is off.
- `prompt_cache_markers_relative_position` (a negative number) adds Anthropic `cache_control` markers. Set it only for providers that accept them.

ChatGPT credentials are saved in `.rocketclaw/auth.json`.

## Data and storage

- One RocketClaw runs per database; others wait. Schema upgrades run at startup, and an upgraded database refuses older RocketClaw versions.
- Attachments go to `.rocketclaw/attachments` unless `attachments` names another folder or an S3 bucket:

  ```json
  "attachments": {"driver": "s3", "bucket_arn": "arn:aws:s3:::rocketclaw-attachments"}
  ```

  S3 needs `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, and `s3:AbortMultipartUpload`. In a versioned bucket, deleted files keep their earlier versions until a lifecycle rule removes them.
- Back up the database and attachments together.
- `.rocketclaw/` is rebuilt at every start. Only `attachments`, `sandbox`, `uploads`, `overlays`, `.rocketcode`, and the auth files survive.

## Monitoring

RocketClaw logs to standard error, with a health line every minute. It writes an access log of every web page request to standard output, in the Combined Log Format plus the request's duration, with the caller's Tailscale login and without query strings.

Go profiling is served at `127.0.0.1:6060` by default; change it with `--pprof=HOST:PORT` or turn it off with `--pprof=false`. Any local process can read it, and profiles can contain private data, so never expose the port or share profiles. To reach it remotely, use an SSH tunnel:

```sh
ssh -N -L 127.0.0.1:6060:127.0.0.1:6060 trusted-host
```

### PostHog

Set `posthog.project_token` (a public `phc_` token, never a personal key) and optionally `posthog.host` (US Cloud by default) to send:

- log lines at info level or above as events,
- warnings and errors to Error Tracking,
- browser analytics and session replays, which record typed text,
- traces of turns, model calls, and tools, with prompts, replies, and tool data redacted.

## funneld

`funneld` is an HTTPS reverse proxy that forwards public paths to internal URLs, configured in `./funneld.json` (or `--config`, or `FUNNELD_CONFIG`):

```json
{
  "host": "example.ts.net",
  "routes": [{"path": "/service/webhook", "target": "http://localhost:7070/service/webhook"}]
}
```

Run `funneld --help` for its flags.

## Harness feedback server

`cmd/harnessfeedback` is a separate MCP server with one tool, `report(summary, details)`, that agents call to report problems with their harness: a confusing tool error, a misleading description, a missing capability. It asks a model whether a report repeats one from the last 14 days, and posts each distinct problem once to a Slack channel, with repeats as thread replies.

Install it with `go install github.com/Rocketable/platform/cmd/harnessfeedback@main`, add it under `mcp_servers` as a `command` server with a `report` facet, and set `HARNESS_FEEDBACK_SLACK_TOKEN`, `HARNESS_FEEDBACK_SLACK_CHANNEL`, `HARNESS_FEEDBACK_MODEL_URL`, `HARNESS_FEEDBACK_MODEL_KEY`, `HARNESS_FEEDBACK_MODEL`, `HARNESS_FEEDBACK_DB` (a SQLite file), and `HARNESS_FEEDBACK_WEB_ORIGIN` in its `env`. Then grant the `report` facet to the agents that should report. It cannot keep secrets or personal data out of reports, so use a private channel. The bot needs `chat:write` and must be in the channel.

## Development

- `go build ./cmd/rocketclaw` builds RocketClaw, and `go test ./...` runs the Go tests.
- `make test` and `make lint` run the full suites, including PostgreSQL in Docker and the sandbox tests. `make check-cloc-budget` checks the source size budgets.
- `go generate ./internal/rocketclaw/internal/web` rebuilds the bundled web frontends with Bun. Commit the rebuilt files when you change `internal/rocketclaw/web/` or `internal/rocketclaw/oc/`.

[AGENTS.md](AGENTS.md) has the rules coding agents follow here.

Layout:

- `cmd/`: programs.
- `internal/rocketclaw/`: the service, its harness, web page (`web/`), and built-in agents (`skel/`).
- `internal/funneld/`: the proxy.
- `internal/harnessfeedback/`: the harness feedback server.
- `docs/`: plans, specs, investigations, and documented solutions.
- `tools/`, `vendor/`: development tools and Go dependencies.
