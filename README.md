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
- Can send events, errors, traces, and browser sessions to PostHog Cloud.

## Main parts

- `internal/rocketclaw` (RocketClaw): the long-running service. It connects the web page, Slack, MCP, scheduled jobs, attachments, and the database. Its program is `cmd/rocketclaw`.
- `internal/rocketclaw/harness`: RocketClaw's agent engine. It builds model requests from the workspace, runs tools, checks permissions, and saves a replayable record of each session.
- `cmd/funneld`: a small HTTPS proxy that forwards public paths to internal URLs listed in `funneld.json`.

## Getting started

```sh
go run github.com/Rocketable/platform/cmd/rocketclaw@main
```

Run `rocketclaw help` to list commands. RocketClaw reads `rocketclaw.json` (or the older `femtoclaw.json`) from the current folder. That file points at the workspace, holds the Slack credentials and channels, and turns on other integrations.

A workspace contains `AGENTS.md` and, optionally, `agents/`, `skills/`, `scripts/`, `cron/`, and `workflows/` folders.

### Web page

The web page is the main way to work with RocketClaw. It is built into RocketClaw and serves HTTPS on `0.0.0.0:3000`; plain `http://` does not answer. To change the address, set `"web": {"listen_address": "127.0.0.1:3000"}`. To use your own certificate, set both `"tls_cert_file"` and `"tls_key_file"` under `web` to PEM files; without them RocketClaw makes a self-signed certificate at each start, which browsers warn about until you accept it. Over HTTPS, browsers carry every tab over one connection, so many open tabs never run out of connections. Links RocketClaw hands out use the tsnet node's HTTPS name once that node serves (see [Voice](#voice)), and this machine's Tailscale address otherwise. The browser tab reads `<name> - RocketClaw`, so several servers stay apart: `name` is `"server_display_name"` when set, else the Tailscale node name (`web.tsnet.hostname`, or this machine's node). Changing it requires restart.

RocketClaw identifies each browser by its Tailscale login, found with a Tailscale lookup of the browser's IP address. It refuses browsers it cannot identify. See the [Web README](internal/rocketclaw/web/README.md) for the page's features and optional Sentry error tracking.

#### Voice

The Voice page (`/voice`) and the voice button in each session's composer let you talk to an agent out loud. The browser sends audio straight to OpenAI's GPT-Live voice model. Each task the voice model hands off becomes a Web message in the conversation, marked `media=Voice`. Hand-offs enter the conversation in the order they arrive, without waiting for the agent: each one joins the running turn or starts the next. The voice model hears nothing about the agent's work while it runs. When a turn the call handed off finishes, its final reply is spoken once, as written. A reply over about 990 tokens (3,960 bytes) is not spoken: the voice says it is on the screen, and the Voice page switches to its transcript and shows the reply there as written. A message typed during the call cancels the spoken reply of every earlier hand-off; only a later hand-off is spoken again. It also mutes the voice at once, mid-sentence if need be, until the voice answers something you say after it. When the agent asks a question, the voice model says "I need your input. Please respond in the app." and reads the question and its options. Answer it in the app: spoken words never answer a question. Each person has one live call at a time: starting voice on the desk ends their call on the phone, while other people's calls, even on the same conversation, keep running. The Voice page's conversation, agent, voice and transcript choice belong to the person and are kept by RocketClaw, so they are the same on every device and survive restarts until Reset. Ending a call never stops the agent's work.

Voice has no settings of its own. Every agent uses the default `openai` provider for audio, independently of the provider running its model. Hand-offs still run on the conversation's selected agent and model:

- `auth: api_key`: the public GPT-Live API, model `gpt-live-1`. It costs about $0.05 per minute, silence included. An `api_base_url` must use HTTPS unless its host is `localhost` or a loopback IP; otherwise voice refuses to send the API key.
- `auth: chatgpt`: Codex's internal voice route, model `gpt-live-1-codex`, with the provider's ChatGPT login. This route is unofficial and may stop working at any time. The ChatGPT account's data settings apply.

On both routes, a redirect to another scheme, host, or port drops the voice credentials.

The default provider must have usable credentials even when every agent uses a named provider. Voice calls send conversation context to that default account, and any voice charges belong to it; named providers' credentials and accounts are not used for voice.

Per call, OpenAI receives your microphone audio; up to 40 of the conversation's recent messages, as written, each cut to 600 bytes and 12,000 bytes in total; the final replies it speaks; and the question notices. It never receives tool names, tool arguments, tool results, progress, reasoning, or attachments.

A call ends when you stop it, when the page closes or stays hidden for 30 seconds, or when OpenAI ends the call. There are no idle or no-speech timers. If RocketClaw's connection to OpenAI drops, the `chatgpt` route reconnects, as Codex does, and the Voice page shows "Reconnecting to the agent…" meanwhile; the `api_key` route ends that OpenAI call as lost. The browser starts a new call on the same microphone after any lost connection (to RocketClaw, to OpenAI, or of the browser's audio, including audio not up 20 seconds after OpenAI answers), with sound cues, until you hang up; see the [Web README](internal/rocketclaw/web/README.md#voice).

The microphone needs a secure page: HTTPS with a certificate the browser trusts. For a phone or another device, RocketClaw can join your tailnet as its own Tailscale node (tsnet) and serve the web page over HTTPS. Turn on MagicDNS and HTTPS certificates for the tailnet, then add `web.tsnet`:

```json
"web": {"tsnet": {"hostname": "rocketclaw", "tags": ["tag:rocketclaw"]}}
```

`"tsnet": {}` also works: the hostname defaults to `rocketclaw` and may use only `a-z`, `0-9`, and `-`. Each tag must start with `tag:`.

1. With RocketClaw stopped, run `rocketclaw ts login`. It prints a login link (and asks for device approval if your tailnet requires it), waits until the node is running, and exits. With `"auth_key": "tskey-auth-…"` under `web.tsnet`, it logs in with that key instead, and so does the server when the node is logged out. Prefer a single-use, pre-approved, tagged key; the key sits in the config file like other secrets.
2. In the Tailscale admin console, confirm that the node appears in your own tailnet. The login link adds the node to whichever tailnet signs in with it.
3. Start RocketClaw. It never prints a login link or waits for one: a logged-out node without a key only logs "Tailscale authentication unavailable", and the Web serves its plain listener.
4. On the phone, with Tailscale connected, open `https://<hostname>.<tailnet>.ts.net/voice`.

- Tag the node, for example `tag:rocketclaw` (the tailnet policy must list the tag under `tagOwners`). Add a tailnet rule that lets only named users reach it and lets it reach nothing. An untagged node acts as the Tailscale user who logged it in.
- The node signs each phone in as the phone's Tailscale user. It refuses tagged devices, and no request header can choose the user.
- Node keys expire after 180 days unless the node is tagged or key expiry is disabled for it in the admin console. An expired node needs `rocketclaw ts login` again.
- Turning on tailnet HTTPS publishes machine names, including this node's, in public certificate-transparency logs.
- The node keeps its state outside the workspace, in `rocketclaw/tsnet-<hostname>` under the OS user config directory (`~/Library/Application Support` on macOS, `$XDG_CONFIG_HOME` or `~/.config` on Linux). Tailscale's log upload is off.
- Never use `tailscale funnel`: it would put RocketClaw on the public internet.

The `listen_address` listener identifies each browser with a Tailscale lookup of its IP address. It never trusts `Tailscale-User-*` headers. `127.0.0.1` has no Tailscale user, so a browser on the RocketClaw machine opens it at this machine's Tailscale address, or uses the tsnet name.

Processes on this machine, including agent tools and MCP servers, can still act as the owner: through the `listen_address` listener, or through this machine's own Tailscale address. See the [transport README](internal/rocketclaw/frontend/rpc/README.md#identity-boundary) for the identity details.

#### Remote RocketClaws

One web page can operate several RocketClaws. Name each other RocketClaw and its tsnet HTTPS address in the local `rocketclaw.json`:

```json
"rocketclaws": {"work": {"url": "https://rocketclaw.tail1234.ts.net"}}
```

On each remote, list the local page's address in `trusted_rocketclaws`, exactly as the browser shows it:

```json
"trusted_rocketclaws": ["https://hally.tail1234.ts.net"]
```

Names use only `a-z`, `0-9`, and `-`, and each URL or trusted address is an `https` origin with no path. Changes take effect on restart.

- The session list, Cmd/Ctrl+P, desktop notifications, and Search pages cover every server. Remote chats show their server's name.
- The Top Shelf, above the space picker, holds tabs from any server and stays the same in every space and on every server. Add a tab from its menu or by dragging it there; clicking a shelved tab opens it on its server and space without opening another tab. Each shelved tab names its server and space.
- The space picker lists each server, local first, with its spaces. A remote space opens that server's view under `/r/<name>/`: its tabs, agents, cron, skills, settings, pages, voice, and new chats. Opening a remote chat goes to its server too.
- The browser calls each remote directly, and the remote signs you in with your own Tailscale login, so you act as yourself with that remote's agents and accounts.
- A remote must serve its tsnet name over HTTPS, run a RocketClaw version with this feature, and trust the local page. Otherwise the page shows it as unavailable, with its address. A remote that stops answering keeps its chats listed, greyed out, until it answers again. A remote on another version keeps working with a notice, and some actions may fail.

Each RocketClaw's tsnet listener accepts these calls only from the pages its `trusted_rocketclaws` lists, because the remote signs in whoever's browser opened the page: any page it trusted could act as that person. Other pages, including other pages on the same tailnet, get no access. The `listen_address` listener accepts none.

### Slack

Slack has one job: quick help when an allowed user mentions the bot. Everything else happens in the web page, including cron reports and MCP sessions.

Under `slack.channels`, map each channel to a list of agents and the Slack user IDs allowed to use them. A row named `@` is not a channel: it gives agents and allowed users for mentions in any other channel or group DM the bot is already in.

Only a mention from an allowed user starts work. Messages without a mention, including replies in threads the bot answers in, reactions, and edited messages start nothing, and 1:1 DMs never start work. A top-level mention in a configured channel starts a new thread answered by the channel's first agent; a top-level mention with no text, file, or forward is ignored. A mention in a thread the bot does not know takes that thread over and reads up to 50 earlier messages. A mention in a thread the bot already answers in is that conversation's next turn and adds only the mention itself. It waits for running work and active goals instead of steering them. Mentions keep their files and forwarded threads. The bot marks each accepted mention with 🤖 and accepts it once, even if Slack delivers it again. If it cannot read the thread or take the request, it replies with a short error.

While it works, Slack shows one placeholder message, then the final answer and any files. Mention replies end with a footer: the agent, its state (working, done, failed, or stopped), and an Open in Web link.

Slack threads that earlier versions posted cron reports or MCP sessions into are read-only: the bot ignores every reply and mention in them. Their conversations are Web chats now; keep working there.

Slack has no commands. `$stop`, `$agent`, `$goal`, `$enqueue`, skill names, and any other `$` text reach the agent as plain text. There is no agent picker, steering, queue card, question button, or reaction control. Mention turns cannot use `ask_user_question`, so the agent asks in its reply or hands the work to Web. Use Web to switch agents, run commands and skills, start goals, manage queued work, and stop any turn. Web messages used in a Slack thread are still posted there, and every reply in that conversation still goes to Slack.

Set up the Slack app with the bot event `app_mention` and these bot scopes, then reinstall it:

- `users:read` and `usergroups:read`, so the web page shows names instead of Slack IDs.
- `users:read.email`, so a mention's turn gets the sender's email as `ROCKETCLAW_PRINCIPAL_EMAIL` (see [Agents and permissions](#agents-and-permissions)). Without it, Slack turns get only `ROCKETCLAW_PRINCIPAL_NAME`.
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

Release note for Slack answering mentions only:

- Only mention threads post to Slack. Cron reports and MCP sessions show as Web chats.
- Cron jobs no longer take `channel`; a leftover `channel` line is ignored. Every scheduled run with a non-empty reply opens a new Web chat, also for jobs that never had a channel.
- `session_prompt` ignores `slack_channel`, so older MCP callers keep working.
- Existing cron report and MCP Slack threads become Web chats. They keep their history, and an MCP session keeps its private run; their Slack threads stay read-only and get no new messages.
- Every chat, in Web or Slack, is now deleted 90 days after its last message. Before, Web chats with messages were kept forever.

### MCP and scheduled jobs

MCP clients call `session_prompt` with a conversation ID and an agent. Each MCP conversation shows as a Web chat run by that agent. Continue it or switch its agent from Web.

A job file in `cron/` runs its agent on its schedule. An empty reply reports nothing; any other reply opens a new Web chat with the report, run by the job's agent. The Cron page in Web lists every run, and continuing one opens the same chat. An [easy mode agent](#easy-mode-agents) schedules itself with `routines` in its own file instead; a cron file cannot name it.

Cron definition bodies expand shell snippets (an exclamation mark followed by a backtick-quoted command) before the model reads them. These are trusted commands, like those in agent prompts: they run in the workspace through the selected agent's `shell`, when set, without a tool permission review. Ordinary messages, scheduled messages, and Background Job completion notes stay literal.

Agents can use `rocketclaw_schedule_message` for a follow-up in the current conversation. `send_this_in` accepts positive durations up to `2160h` (90 days), the retention window, including `12h` and `168h`. Set `recurring` to `false` for one message or `true` to repeat at that interval (at least `1m`) until scheduled messages are reset. Pending schedules protect their conversations and explicit owners from normal retention cleanup.

Retention cleanup deletes every chat, in Web or Slack and pinned or not, 90 days after its last message; an MCP chat goes with its private MCP session. A chat with no messages counts from when it was created. Queued work and pending schedules keep a chat.

## How a request runs

1. A request arrives from the web page (including a `$workflow` command), a Slack mention, MCP, a cron job, or a scheduled message.
2. RocketClaw passes it to its agent harness with the chosen agent.
3. The harness calls the model and runs tools, within the agent's permissions.
4. RocketClaw saves each finished step and sends the reply back where the request came from.

On shutdown, tool calls already running finish and are saved, and background jobs are stopped. After a restart, each conversation continues from its last saved step.

## Agents and permissions

Each agent file must set `model` to a real model name, such as `gpt-6.1-sol` or `work/gpt-6.1-sol` (see [Models and providers](#models-and-providers)).

The root `AGENTS.md` goes into every agent's instructions. To leave it out for one agent, add this to that agent's frontmatter:

```yaml
permission:
  rocketclaw:
    load_agents_md: false
```

For the `bash` tool, RocketClaw checks every command in a script on its own, and each command must be allowed. When several rules match, the last one wins. Scripts it cannot parse are refused, even with `bash: allow`.

Bash rules match command text, not everything a program can do. An executable-path wildcard such as `./scripts/*` does not match executable paths containing `..`; name such an executable explicitly if it is intended. This check is not a shell sandbox: an approved script can still launch other programs or follow symlinks. To put a sandbox in front of one agent's shell commands, set `shell` in that agent's frontmatter to the absolute path of a wrapper program; see the [cheatsheet](cmd/rocketclaw/CHEATSHEET.md#agent-frontmatter-and-permissions) for what the wrapper must do. To run them in a bash emulator instead, set `shell: emulated`; see [Emulated shell](#emulated-shell).

A turn that a person starts from Web, voice, or Slack tells its shell commands who asked:

- `ROCKETCLAW_PRINCIPAL_NAME`: the person's name. On Web it is the Tailscale display name, or the login when that is blank. On Slack it is the Slack display name, or the real name when that is blank.
- `ROCKETCLAW_PRINCIPAL_EMAIL`: on Web, the Tailscale login; on Slack, the profile email.
- `ROCKETCLAW_PRINCIPAL_ID`: the lowercase hex SHA-256 of the email after trimming spaces and lowercasing it, so `Alice@Example.com` and `alice@example.com` get the same ID.

When RocketClaw has no email, only the name is set. The same person's values reach the subagents and shadows of that turn, the later turns of a `$goal` they set, and their queued or stashed messages when those run. A steer into a running turn keeps the values that turn started with. Cron turns, External MCP turns, and messages one agent sends another get none of them. The workers of a workflow get the values of the turn that runs them. A name is a display name its owner can change, so use it for attribution only. Permission patterns that grant access per person should use `${ROCKETCLAW_PRINCIPAL_ID}` or `${ROCKETCLAW_PRINCIPAL_EMAIL}`. When a turn lacks a variable that a rule names, an `allow` or `auto` rule is skipped, and a `deny` rule matches as if the variable were `*`. Such a rule covers everything that runs with that person's values: steers and promoted queued messages that other people deliver into the person's running turn, the later turns of the person's `$goal`, and a background subagent that starts after the person's turn ended, which takes the values of whichever top-level turn runs then. Per-person rules keep people's work apart for convenience; they are not an authorization boundary between people. The values say who asked for the turn, not a credential: permission patterns read them from the turn, but a script sees whatever value the command that runs it sets.

Some features are off until you turn them on under `permission.rocketclaw`:

- `rocketclaw_sessions`: one key for the session tools. `rocketclaw_sessions_list`, `rocketclaw_sessions_get`, `rocketclaw_sessions_current_id`, and `rocketclaw_sessions_get_url` let an agent read saved conversations and get a chat's Web link; `rocketclaw_sessions_write` lets it start a Web session or send a message into one. `deny` (the default) leaves them all off. `auto` or `auto(name)` reviews every call first. `allow` runs them without review. The read tools can read every conversation in the database, not only the agent's own. To protect the database, each call is limited: a list needs a `since` time and returns at most 200 conversations, and a read returns at most the 100 newest entries before `before_entry_id`, ending with `[next_before_entry_id=N]` when older entries remain. A list also takes a required `criteria` string: empty lists every stored conversation, and anything else is a Web `/search` query (free text plus `tag:`, `cron:`, `agent:`, `room:`, `model:`, `space:`, `is:` and `sort:`) that returns only the chats `/search` would, with their matching messages. `space:"Blue garden"` matches the complete Birth Space name ignoring case, not the tab's current placement, and excludes sessions with no Birth Space. A read shows each call an `execute` script made as one `code_mode` line with its command, status, last output line and the approver's reason.
- `rocketclaw_store`, `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list`, `rocketclaw_kv_store`, `rocketclaw_kv_read`: let an agent publish Hosted Pages, such as reports, dashboards, and graphs, and the JSON Page Data they read. Each agent has its own files and data. `rocketclaw_store` returns a link, `/pages/<space>/<agent>/<path>`, that only people the Web admits can open; it opens as a tab in the space named after the agent's `space:`, and each space's Page Index lists its pages. Pages run sandboxed, so their scripts cannot act as the viewer, and they can only read their own agent's Page Data. Generated `main` agents allow all six. Agents in remote OpenCode sessions publish pages through their server's MCP bridge (see `cmd/rocketclaw/CHEATSHEET.md`); those pages belong to the session, open in its space, and are deleted with it.
- `rocketclaw_set_tag`: lets an agent tag sessions with labels from groups you define. See the [example agent](internal/rocketclaw/skel/agents/examples/session-tags.example.md).
- `allow_background: allow`: lets long scripts and subagents keep running while the conversation moves on, and lets the agent steer, queue a follow-up behind, or stop a subagent that is still running.

`rocketclaw_sessions_write` starts a Web session or steers, queues, or stops one, in any chat Web lists, and never waits for the other session. Instead, each write that returns a `queue_item_id` gets exactly one `<session_reply>` note, sent to the session that made the call, `task` subagents and hidden cron and External MCP runs included. The note carries the other session's final answer once the turn that took the message ends, or says that a person removed the message from the queue before it ran. It enters the caller's running turn or wakes an idle caller. Pending replies are listed with the caller's Background Jobs and can be stopped there; they need no `allow_background`.

One more setting controls who may write into an agent's sessions:

- `rocketclaw_sessions_recipient`: `true` (the default, except for sudo agents, which default to `false`) lets any agent start, message, or stop this agent's sessions; `false` lets none, not even another session of the same agent; a list of agent names lets only those. The agent checked is the one that makes the call, so a `task` subagent counts as its own agent. A refused call fails before anything is created, queued, or stopped. Reading sessions is not affected.

Name each tool under `permission.rocketclaw`. Wildcards such as `"*"` and the short form `rocketclaw: allow` are not accepted there.

To give one agent every tool and feature, including ones added later, set `sudo: yes` in its frontmatter (`true`, `1`, `on` or `yes`; `false`, `0`, `off` or `no` turn it off). Sudo replaces the agent's `permission` block: every call goes to the guardian for review first, including each `execute` script and every call inside it. Skills, `task` delegation, `ask_user_question`, `rocketclaw_update_goal`, `rocketclaw_attach_files_to_response`, scheduled messages and `rocketclaw_reload` run without review. Web search runs on the provider and is never reviewed. Session tag groups still go under `permission.rocketclaw.rocketclaw_set_tag`, and `permission.rocketclaw.rocketclaw_sessions_recipient` may stay too; `rocketclaw lint` reports any other `permission` entry next to `sudo`. Sudo does not lift `.env` blocking, workspace confinement, or `maxRecursion`, and it does not pass to subagents.

```yaml
sudo: yes
maxRecursion: 2
```

```yaml
permission:
  rocketclaw:
    rocketclaw_sessions: auto
    allow_background: allow
    rocketclaw_sessions_recipient: [main]
```

Release note for session write replies:

- **Breaking:** `rocketclaw_sessions` replaces the per-tool keys `rocketclaw_sessions_list`, `rocketclaw_sessions_get`, `rocketclaw_sessions_current_id`, `rocketclaw_sessions_get_url`, and `rocketclaw_sessions_write`. Agent files that still use them load, but those keys grant nothing, so the tools disappear until you add `rocketclaw_sessions`. `rocketclaw_sessions_write: children` is gone and fails agent loading. `rocketclaw_set_tag` keeps its own setting, and `rocketclaw_stop_background_job` still comes with `allow_background`.
- Newly generated `main` agents have `rocketclaw_sessions: auto`: they can still start Web sessions from Slack, and every session list, read, and write they make is reviewed first, so a Slack mention cannot quietly copy a private External MCP or cron conversation into a thread. Agent files generated earlier with `allow` keep unreviewed access until you change them.
- Agents can no longer stash a message for a person to pop; `mode` is `steer`, `enqueue`, or `stop`. Stash from Web is unchanged.
- Creating a session now queues its first prompt like any other write, so it survives a restart, and the result carries a `queue_item_id`. A `stop` with an empty prompt returns a `queue_item_id` only when a turn was running; its note comes once that turn is over and says how it ended.
- A reply for a turn that failed or was stopped says so and suggests `rocketclaw_sessions_get` with the session ID. When a person or agent stops a reply job, its note says so and waits for the caller's next turn without waking it; the write itself may still run. When a `task` subagent is stopped with its task or its parent's turn, its pending replies are dropped without notes.
- When a reply, or one of its own Background Jobs, wakes a `task` subagent after its `task` call returned, the subagent's new answer goes to its parent as a `<subagent>` note, and so on up to the top-level session. A wake turn that was stopped, or that ran a prompt from a `task` call, reports nothing, since that call gets the answer.
- The `spawned_by` column is dropped from the database.

Release note for agent session control:

- **Breaking:** under `permission.rocketclaw`, every tool must be named. An agent file that uses `rocketclaw: allow`, `"*"`, or another wildcard such as `rocketclaw_sessions_*` fails to load until you list each tool it needs by name.
- `rocketclaw_start_new_thread`, `rocketclaw_list_sessions`, `rocketclaw_get_session`, and `rocketclaw_current_session_id` are removed, with no aliases. Use `rocketclaw_sessions_write` with an empty `session_id`, `rocketclaw_sessions_list`, `rocketclaw_sessions_get`, and `rocketclaw_sessions_current_id`. Agent files are not migrated, and rules on the old names match nothing.
- There is no `rocketclaw_sessions_stop`. To stop a session without sending it anything, call `rocketclaw_sessions_write` with `mode: stop` and an empty `prompt`. Once the stopped turn has saved, that session's transcript gets one line saying which session and agent stopped it.
- There is no `session.spawn_mode` setting. An agent file that still has it fails to load; use `rocketclaw_sessions_write` instead.
- `rocketclaw_sessions_write` defaults to `deny`, so existing agents can no longer start sessions until you allow it. That includes a deployed `agents/main.md` that still says `rocketclaw_start_new_thread: allow`; newly generated `main` agents allow it. Also update prompt text that names the old tool.
- An agent with `auto` or `allow` can write into and stop any chat Web lists, including Slack threads RocketClaw manages, and the replies post to Slack. This holds for every turn of that agent, including cron, External MCP, and Slack mention turns.
- Steering accepts any sender. A steer joins the running turn while that turn still takes input, whoever sent it and whoever started the turn, so a person's Web steer now also joins goal continuations, Completion Note wakes, scheduled messages, and the first turn of a session an agent created. Hidden cron and External MCP runs and `$workflow` turns still take a steer as their next turn.
- `task`'s `continue` is now `{session_id, mode}` for agents with `allow_background: allow`. A still-running subagent is no longer refused: `mode` steers it, queues a follow-up after it, or stops it and runs the prompt next. A steer joins even when a foreground `task` call is waiting on that subagent. Queued follow-ups survive a restart, and no prompt, steer, follow-up, or wake reaches a subagent twice after a restart.
- A continue that names a different `subagent_type` switches the subagent to that type instead of failing. The type must exist, and the caller needs `task` permission for it. A steer switches a running subagent from its next step.
- Every subagent now runs as a session of its own, on the same turn runner as other sessions, so its queue, steers, stops, and restart recovery work as a top-level session's do. A subagent session is not listed in the sidebar or search. Its Background Jobs report to the subagent itself. It acts for its top-level session's running turn: it writes sessions and tags for that session, the sessions it creates keep that turn's creator and agent limits, the files it attaches go out with that turn's reply, and `rocketclaw_sessions_current_id` returns that session's ID. It is not offered `rocketclaw_schedule_message` or `rocketclaw_reset_scheduled_messages`. Its first prompt carries a header naming the session and agent that delegated it.
- Stopping a turn while its foreground `task` call waits also stops the subagent's current turn, and so on down a chain of foreground subagents, unless a background `task` job waits on that turn. The prompt the call sent, if still queued, is not withdrawn but held: it stays in the subagent's queue, shown in Web like a stash, through restarts, while the rest of the queue moves on. The next message sent to the subagent, by `task`, `rocketclaw_sessions_write`, or a person in Web, releases it to run just before that message, and a person's `$goal` releases it to run before the goal; a stash releases nothing, and a person's stash is never released this way. The stopped session gets no note. A restart is not a stop: the call waits again.

Every permission review starts with a header RocketClaw writes outside the transcript: the turn's source (`Web`, `Slack`, `Cron`, `ExternalMCP` or `System`, or `ScheduledMessage` with its creator's source), the person who asked when known, and the agent that sent the message. Subagents, workflow workers and shadows carry their top-level turn's header. The reviewer trusts it over any origin the transcript claims, so a rule such as "cron turns never use write credentials" can be enforced.

### Easy mode agents

An agent file with `mode: easy` uses a shorter format. Leaving `mode` out, or `mode: advanced`, keeps the format described above; any other value fails to load. The rules of an easy file:

- A section left out, or set to `auto`, turns on everything in it. A section written out turns on only what it lists.
- Everything turned on is reviewed before it runs by the agent's approver: the guardian, on `approver.model` when set, with `approver.prompt` added to its rules. As under `sudo`, skills, `task`, web search and the default-on RocketClaw tools (reload, scheduled messages, attaching files, goal updates and questions) run without review.
- `model`, `space` and `workingDirectory` are required. A misspelled or unknown key fails to load, and so do the advanced keys `description`, `reasoningEffort`, `verbosity`, `shell`, `permission`, `sudo`, `guardrail` and `additionalInstructions`.
- The body is the agent's whole instructions. Easy agents never load `AGENTS.md` and get no reply-style line; write ``!`cat AGENTS.md` `` in the body to include it.

```yaml
---
mode: easy
model: openai/gpt-6.1-sol#high
space: Marketing
workingDirectory: sessions/marketing/$ROCKETCLAW_CONVERSATION_ID

subagents:
  - openai/gpt-6-luna#max
  - anthropic/claude-haiku-4-5#high
maxRecursion: 1

approver:
  model: openai/gpt-6-luna#max
  prompt: "Cron turns never use write credentials. Deny write actions the user did not explicitly request."

secretsARN:
  - "arn:aws:secretsmanager:us-east-1:123456789012:secret:marketing-read"

commands:
  stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"

mcp:
  alitu_google_marketing: [read]

rocketclaw: [sessions, tags]
tags:
  - [triage, investigating, resolved]

skills:
  - marketing

allowedNetworkTargets:
  - api.stripe.com
  - localhost:8080
  - https://api.example.com/v1/

routines:
  - "*/5 * * * *": "Check the campaign dashboard for anomalies."
  - "30m"

delegatedFrom: [main]
---

Agent instructions.

!`cat AGENTS.md`
```

The [cheatsheet](cmd/rocketclaw/CHEATSHEET.md#easy-mode-agent-frontmatter) lists every key and what leaving it out means. How an easy agent runs:

- Its shell commands always run in the [emulated shell](#emulated-shell), in its `workingDirectory`. A relative path starts from RocketClaw's working directory. `$VAR`s come from the turn, each made into one safe path segment, or else from RocketClaw's environment; an unset variable fails the turn. `ROCKETCLAW_CONVERSATION_ID` is set on every turn, so the example gives each chat its own folder. The folder may not be or contain the workspace, nor be, contain or lie inside `.rocketclaw`, `agents`, `skills`, `cron`, `scripts` or `workflows`, wherever a link puts them. Subagents and workflow workers use their top-level session's folder.
- `read`, `glob`, `grep`, `apply_patch` and attaching files see only that folder, plus two read-only exceptions at their full host paths, which the shell sees too: the chat's uploads and the folders of the skills the agent may use. Files uploaded from Web go to `.rocketclaw/uploads/<chat>`; Slack files are put into the prompt, not uploaded.
- The emulated shell sees only the turn's `ROCKETCLAW_*` variables and `TMPDIR`. The programs `commands` starts run in host bash with RocketClaw's environment plus the `secretsARN` secrets. Each secret is a JSON object of string values, and each key becomes one variable. Secrets are fetched on every turn; a failure fails the turn and names the ARN and key.
- ``!`…` `` lines in the body, in a routine's instruction and in the `SKILL.md` of a skill the agent loads run in host bash, from the workspace, with the secrets. A skill's lines run before the model's arguments go in, so text the model passes never runs.
- `allowedNetworkTargets` limits `webfetch` with exactly the rules the shell's `curl`, `fetch` and `jb_http` follow.
- `task` takes a `model` from `subagents` and the `tools` the subagent gets, `null` for all of the parent's. The subagent runs the parent's own file, with the same folder, approver and secrets. Advanced agents cannot `task` an easy agent; they write to its sessions with `rocketclaw_sessions`, which `delegatedFrom` limits.
- Each routine runs the agent on a schedule written as in a cron file, with its instruction, or with `Scheduled run (<schedule>).` for a bare schedule. It reports like a cron job, in the agent's `space`. The Cron page lists it as `<agent> · <schedule>`, and `cron:<agent>` finds its runs. Schedules repeat: a one-off RFC 3339 time fails loading, and `rocketclaw_schedule_message` sends a single future run. A cron file cannot name an easy agent. Renaming the agent file leaves earlier routine runs under the old name.
- A shadow must use its parent's mode; one that does not is dropped with a warning. An easy shadow needs the same required keys, and its `space` may differ. Routine runs get no shadows, and a shadow's own `routines` never run.

### Emulated shell

An agent that sets `shell: emulated` runs its shell commands in [just-bash](https://github.com/vercel-labs/just-bash), a bash emulator, instead of host bash: `bash` tool calls, `` !`…` `` lines and workflow workers. It has the usual tools plus `python3`, `js-exec` and `sqlite3`. Each command runs in a fresh process, so files in the working directory persist between calls but variables and `cd` do not.

```yaml
shell: emulated
workingDirectory: justbash/$ROCKETCLAW_CONVERSATION_ID # required
commands:
  stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"
allowedNetworkTargets: # or auto, the default
  - api.stripe.com
  - localhost:8080
  - https://api.example.com/v1/
```

- `workingDirectory` is required: it is the only host folder the commands see, at its real path. Everything else, `TMPDIR` (`/tmp`) included, lives in memory and is gone when the command ends. `$VAR`s come from the turn's environment, and an unset one becomes empty. `ROCKETCLAW_CONVERSATION_ID` is set on every turn, so the example gives each chat its own folder. A relative path starts at the workspace. It may not be or contain the workspace or `.rocketclaw`, nor lie inside `.rocketclaw`.
- `commands` are the host programs the commands can start. `stripe-write a b` runs `stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID a b` with host bash and the host's environment only: variables the commands set never reach it. Its output keeps its exact bytes through pipes and files. Binary data a command prints straight to its output, rather than to a file, comes back garbled: just-bash decodes that output as text.
- `allowedNetworkTargets` limits `curl`, Python's `jb_http` and JavaScript's `fetch`. `auto` allows public addresses and refuses loopback, link-local (including `169.254.169.254`) and private ones. A list allows hosts (optionally with a port) and URL prefixes, with any HTTP method. just-bash has no wildcards, CIDR ranges or per-entry methods, so such entries fail every command.
- Running it needs `node` 22.18 or later and `npm`. At startup and on each reload, RocketClaw installs just-bash into `.rocketclaw/justbash/` with `npm` when an agent uses it; a failed install shows in the Web interface.

Easy mode agents always use the emulated shell, with the differences listed under [Easy mode agents](#easy-mode-agents).

### Shadow agents

Put alternative agent definitions in the directory named after their parent:

```text
agents/some-important-agent.md
agents/some-important-agent/name-of-shadow-agent.md
agents/some-important-agent/another-shadow.md
```

Each immediate Markdown child is a shadow. Use ordinary agent frontmatter,
including a model and permissions, in each file. No extra configuration list is
needed. Deeper directories and directories without a matching parent file do not
declare shadows. Shadows are not choices in the normal agent picker, and cannot
create shadows of their own.

Each parent session has a separate saved session for every shadow. Each parent
turn gives those sessions the same inputs, in the order the parent uses them:
the prepared prompt, attachments, consumed steers, moved-work notices, and
Background Job Completion Notes. Queued or stashed work is copied only when used;
removed work is not copied. A shadow keeps its own replies, tool results, and
history. The parent never waits for a shadow's answer. Model calls, cost, and tool
work increase with each shadow.

A shadow may take at most 1.5 times the parent's steps, rounded down, counted
from the message that starts the turn and again from each steer a person sends
into it. Messages from other agents and system notices, such as Background Job
Completion Notes, do not restart the count. A step is one model response in the
turn itself; subagents, retries, and permission reviews do not count, and a step
repeated after a restart counts once. A shadow that gets ahead of a parent still
running pauses until the parent's next step gives it room. A person's steer
always reaches the shadow, even one out of steps. Once the parent's turn has
ended, a shadow out of steps lets its last tool calls finish and then stops, and
its Web comparison tab shows `Hit the step limit` with both counts.
That note is for people only; the shadow's model never sees it. A shadow whose
answer comes within the limit completes normally.

**Shadows run real tools. This is not a dry run.** They share the workspace and
external services with the parent, so allowed file edits, shell commands, and
external writes can race or happen more than once. Each shadow uses its own
permissions, without borrowing the parent's approvals. Start with
permission-limited definitions and non-critical work.

Scripts run by shadows receive `ROCKETCLAW_SHADOW` with the full shadow agent
name, such as `some-important-agent/name-of-shadow-agent`, alongside the other
`ROCKETCLAW_*` environment variables. Scripts can use it to adjust their behavior
during a shadow run.

The same inputs do not mean identical model conversations: system instructions,
model, independent history, tool results, and compaction can differ. Workflow
turns have no shadows.

Inspect shadows in the Web comparison tabs beside their parent. Their output
stays there, without an extra Slack reply, cron report, or MCP response. Shadows
cannot ask independent human questions, create threads, schedule work, manage
goals, reload/restart the service, or start Background Jobs. Read-only comparison
does not prevent the real tool effects described above.

New shadows on existing sessions begin at the next parent turn, seeded from the
retained parent history without rerunning old work. Removing a file stops future
turns, not work already admitted. Switching agents changes the shadows for future
turns and retains earlier comparisons. Forks get fresh shadow sessions. Revert
hides whole corresponding shadow turns; Redo restores saved comparisons without
rerunning effects. Neither operation undoes files or external writes. Clearing
history clears comparisons but keeps bindings for future work; deleting a parent
deletes its owned shadow sessions.

## Skills

Type `$` in the web page to list commands and skills for the current agent. `$review some text` runs the skill named `review`, and `$stop` stops the current work. To queue a skill instead of running it now, use `$enqueue $review some text`. In Slack, `$` text is plain text for the agent.

Skill templates read their arguments the same way OpenCode commands do: `$ARGUMENTS` is the whole text, and `$1`, `$2`, and so on are single words or quoted phrases.

## Models and providers

The top-level `openai` object is the default model provider. Add more under `providers`:

```json
{
  "openai": {"auth": "chatgpt"},
  "providers": {"work": {"auth": "chatgpt"}}
}
```

```yaml
model: work/gpt-6.1-sol
```

`work/gpt-6.1-sol` uses the `work` provider, and a bare `gpt-6.1-sol` uses `openai`. RocketClaw never switches to another provider on its own. Each provider compacts conversation history at 200000 tokens unless you set `autocompaction_threshold`.

Set `prompt_cache_markers_relative_position` (a negative number) only on a provider that accepts Anthropic `cache_control` markers, such as CLIProxyAPI in front of Claude. RocketClaw then marks one text piece of the last request message, counted from the end: `-2` marks the piece before the last. Automatic permission reviews send the planned action as the last piece, so `-2` lets the next review reuse the cached transcript. Leave it unset for OpenAI and other providers that reject unknown fields.

`auth` sets how a provider signs in: `api_key` (the default) uses `api_key`, and `chatgpt` uses a ChatGPT login. The older name `rocketcode_auth` still works; `auth` wins when both are set.

Sign in per provider with `rocketclaw oai login [provider] [--headless]`. Use `rocketclaw oai list` to see sign-ins and `rocketclaw oai logout [provider]` to remove one. Credentials are normally saved in `.rocketclaw/auth.json`.

## Data and storage

- `database_url` sets the PostgreSQL database. Only one RocketClaw process uses a database at a time; others wait. RocketClaw upgrades the database schema at startup; a database last run by a version before v0.2.0 must be started with v0.2.0 first. Older RocketClaw versions refuse to start on an upgraded database, so you cannot roll back by swapping in an older binary.
- `attachments` sets where uploaded and generated files are kept. By default they go in `<workspace>/.rocketclaw/attachments`. You can pick another folder or an existing S3 bucket:

  ```json
  "attachments": {"driver": "filesystem", "path": "/srv/rocketclaw/attachments"}
  ```

  ```json
  "attachments": {"driver": "s3", "bucket_arn": "arn:aws:s3:::rocketclaw-attachments"}
  ```

  S3 uses the standard AWS credentials and region settings. Grant `s3:PutObject`, `s3:GetObject`, and `s3:DeleteObject` for attachments, plus `s3:DeleteObjectVersion` for a versioned bucket. Grant `s3:ListBucket` so deletion retries can distinguish missing files from denied access. RocketClaw does not create buckets or make files public.
- Back up the database and the attachment storage together. Restoring only the database brings back references to files, not the files.
- `.rocketclaw/` holds generated runtime files, such as cloned overlays, per-session temporary folders, and oversized command output. Do not treat it as source code.

## Monitoring and profiling

RocketClaw logs a health line once a minute with memory, Go scheduler, and database connection figures.

When profiling is enabled, RocketClaw also samples blocking events at an average of one per 10 milliseconds spent blocked and one in 100 mutex contention events.

`rocketclaw run` turns on Go profiling at `127.0.0.1:6060` by default. Use `rocketclaw run --pprof=false` to turn it off. Any process on the machine can reach that port, and profiles can contain private data, so do not expose the port and do not share downloaded profiles. To inspect a remote machine, forward the port over SSH:

```sh
ssh -N -L 127.0.0.1:6060:127.0.0.1:6060 trusted-host
```

### PostHog

Set a PostHog project token to send data to PostHog Cloud. Leave it out to send nothing.

```json
{
  "posthog": {
    "project_token": "phc_...",
    "host": "https://us.i.posthog.com"
  }
}
```

`host` defaults to US Cloud; use `https://eu.i.posthog.com` for EU Cloud. Only a public project token (`phc_...`) is accepted, because the browser receives it. Never use a personal API key here.

With a token, RocketClaw sends:

- Every log line at info level or above as an event. The event is named by the line's `event` field, such as `turn_started` or `cron_completed`, or else by its message, such as `rocketclaw health`. All of the line's fields become event properties.
- Warnings and errors to Error Tracking, with the stack of the code that logged them.
- Browser page views, clicks, heatmaps, Web Vitals, JavaScript errors, and session replays. Replays record typed text, console logs, and network requests in full, and each browser is identified by its Web username.
- Traces of agent turns, model calls, and tools, but only while `instrumentation.enabled` is true. They go to PostHog's AI Observability (model, tokens, latency, and reply text for each model call) and to its distributed tracing. `instrumentation.collector_endpoint` is then optional, and when set, traces also keep going there. Each trace belongs to the person who sent the turn: their email when known, else their principal name. Turns nobody sent, such as cron jobs, have no person. The request messages of each model call are not sent, because they repeat the whole conversation and PostHog rejects exports over 4 MB. Only the message that started the turn is sent, as the trace's input.

Traces carry the text of prompts, replies, and tool calls unless `instrumentation.hide_inputs` or `instrumentation.hide_outputs` hides it. To get console logs and network requests in replays, also turn them on in the PostHog project settings.

## Repository layout

- `cmd/`: runnable programs.
- `internal/rocketclaw/`: the service, with its agent engine in `internal/rocketclaw/harness/`.
- `internal/funneld/`: the HTTPS proxy and its config loading.
- `vendor/`: copies of Go dependencies.

## Development

```sh
go test ./...
```

`make build` builds RocketClaw and funneld. The RocketClaw step rebuilds the web frontend with Bun before building `bin/rocketclaw`. The built frontend in `internal/rocketclaw/internal/web/dist/` is committed, so a Go-only build does not need Bun. When you change `internal/rocketclaw/web/`, commit the rebuilt `dist/` files too.

`make test` in `internal/rocketclaw` starts PostgreSQL in Docker, or uses `ROCKETCLAW_TEST_DATABASE_URL` if set. It runs the Go tests once with race detection and coverage, enforcing the fixed `COVERAGE_REQUIRED` minimum in its Makefile. Generated protobuf code (`*.pb.go`) does not count toward the total. GitHub Actions tests against PostgreSQL 18.
