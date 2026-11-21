# Rocketable Platform

Rocketable Platform runs AI agents inside a repository or folder. People talk to the agents through a built-in web page, Slack, or MCP (Model Context Protocol, a standard way for other AI tools to call this one). Agents can read and edit files, run shell commands, use tools and skills, but only as far as their permission rules allow.

It is written in Go and runs inside your own workspace. It is not a hosted service shared by many customers.

## Experimental software

Rocketable Platform is experimental. It runs actions chosen by an AI model against your files, shell, connected services, and team chat. Review its configuration, permissions, outputs, and integrations before you use it anywhere sensitive or in production.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## What it does

- Runs agents that follow your workspace's instructions, agent definitions, and skills, under permission rules you write.
- Saves conversations and unfinished work in PostgreSQL. After a restart, work continues from the last finished step.
- Takes requests from the web page, Slack mentions, MCP, scheduled jobs, and one-off or repeating scheduled messages.
- Sends model requests to OpenAI or to any service with the same API, and can use several services side by side.
- Can send events, errors, traces, and browser sessions to PostHog Cloud.

## Main parts

- `internal/rocketclaw` (RocketClaw): the long-running service. It connects the web page, Slack, MCP, scheduled jobs, attachments, and the database. Its program is `cmd/rocketclaw`.
- `internal/rocketclaw/harness`: RocketClaw's agent engine. It builds model requests from the workspace, runs tools, checks permissions, and saves a replayable record of each session.
- `cmd/funneld`: a small HTTPS proxy that forwards public paths to internal URLs listed in `funneld.json`.

## Getting started

```sh
go run github.com/Rocketable/platform/cmd/rocketclaw@main
```

Run `rocketclaw help` to list commands. RocketClaw reads `rocketclaw.json` (or the older `femtoclaw.json`) from the current folder. When `femtoclaw.json` exists, RocketClaw uses it, even if `rocketclaw.json` is there too, and its runtime folder is `<workspace>/.femtoclaw` instead of `<workspace>/.rocketclaw`. The file holds setup and secrets: it points at the workspace and the database, holds the Slack, provider, and External MCP credentials, and turns on other integrations. Values can also come from AWS Secrets Manager. Any value can be written as `{"aws": {"arn": "ARN", "key": "KEY"}}`, and RocketClaw replaces it with the string under `KEY` in that JSON secret. So `aws` is a reserved name: every object whose only key is `aws` is read as such a reference. A map with a single entry named `aws`, such as one MCP server or provider called `aws`, fails to load with `aws object requires arn and key`; give it another name. `rocketclaw run --aws-secrets-manager-arn ARN` merges that JSON secret over the file, and with it the file is optional: the secret alone can hold the whole configuration, and the workspace defaults to the current folder. `rocketclaw oai login`, `oai list`, `oai logout`, and `ts login` take the same flag.

`rocketclaw run` exits with code 0 after a shutdown signal, with 1 on an error, including a Web node that fails its startup check (see [Tailnet setup](#tailnet-setup)), and with 255 when it asks to be started again: after the `rocketclaw_restart` tool, or when the Web page stops serving while it runs. It does not start itself again.

Behavior Settings live in PostgreSQL instead: `auto_approver_model`, `slack.channels`, `logging.level`, and `overlays`. The first start copies them from the file once every startup check passes. After that, RocketClaw ignores them in the file and logs a warning naming each one it ignored. The people named in the file's `web.admins` (their Tailscale logins, for example `"web": {"admins": ["alice@example.com"]}`) edit them on the Config page, one control per setting. A save is checked whole and applies to the running server at once, or is refused and changes nothing. Everyone else sees the page read-only.

If the stored settings stop RocketClaw from starting (the error starts with `stored behavior settings:`), fix them in place with `psql` on the `database_url` database:

```sh
psql "$DATABASE_URL" -At -c 'SELECT document FROM behavior_settings' > behavior.json
# fix behavior.json, then:
psql "$DATABASE_URL" -v doc="$(cat behavior.json)" <<'SQL'
UPDATE behavior_settings SET document = :'doc'::jsonb;
SQL
```

Deleting the row (`DELETE FROM behavior_settings`) makes the next start import from the file again, but it discards every edit made on the Web, so use it only as a last resort.

A failed overlay download also stops RocketClaw from starting, with an error that names the overlay as written in the list and shows Git's message; that error does not start with `stored behavior settings:`. Check the network, the Git credentials, and the ref. Only when the overlay address itself is wrong does the fix belong in the stored settings, with the `psql` steps above.

A workspace may contain `agents/`, `skills/`, `scripts/`, and `cron/` folders. RocketClaw ships four agents: `main`, a `sudo: yes`, `sticky: global` agent with one shared session; `assistant`, a `sudo: yes`, `sticky: off` agent for External MCP, which accepts only `sticky: off` agents; `slack`, the same assistant as a `sticky: slack` agent for Slack channels (see [Sticky agents](#sticky-agents)); and `voice`. At every start, RocketClaw copies each built-in agent file into the workspace's `agents/` folder only when that file is missing: your edits to them stay, and a deleted one comes back.

### Overlays

The `overlays` setting lists Git repositories that add agents, skills, cron jobs, and scripts to RocketClaw. Each entry is a Git address, optionally followed by `@<ref>`:

- The address is `github.com/org/repo` (RocketClaw adds `https://`), `https://…`, `git@host:org/repo.git`, `ssh://…`, or a local path. Prefer an absolute path: Git reads a `./` or `../` path from the overlay's download folder under `.rocketclaw/overlays/`, not from the workspace.
- The ref is a branch, a tag, or a full commit hash. Without one, RocketClaw takes the repository's default branch. A ref cannot contain `/`: `github.com/org/repo@feature/x` is read as one address, and its download fails.

From each overlay RocketClaw takes only `agents/`, `skills/`, `cron/`, and `scripts/`. It stacks the layers in this order: the built-in files first, then the overlays in list order, then the workspace's own folders last. A file in a later layer replaces the same file in an earlier one.

RocketClaw downloads every overlay fresh, as a shallow Git fetch into `.rocketclaw/overlays/`, at every start or restart, every reload (including the one after an agent editor save), and every Config page save that changes the overlay list. A failed download at startup stops RocketClaw (see [Getting started](#getting-started)). A failed download during a reload or a save leaves the copies in use in place, and that reload or save fails with the error. Each download replaces the previous copy, so an edit made inside `.rocketclaw/overlays/` is lost unless it was pushed to the overlay's repository.

### Web page

The web page is the main way to work with RocketClaw. It is built into RocketClaw, which joins your tailnet as its own Tailscale node (tsnet) and serves the page only there, over HTTPS on port 443 of the node's name, `https://<hostname>.<tailnet>.ts.net`. `web.tsnet` is required; see [Tailnet setup](#tailnet-setup). Over HTTPS, browsers carry every tab over one connection, so many open tabs never run out of connections. Links RocketClaw hands out use the node's HTTPS name; until the node serves, RocketClaw has no link to hand out. RocketClaw does not start without the page: it brings the node up right after it takes its run lock, before anything else, and stops if the node cannot serve (see [Tailnet setup](#tailnet-setup)). If the page stops serving later, while RocketClaw runs, RocketClaw asks to be restarted: it stops its work as it does on shutdown and exits with code 255 (see [Getting started](#getting-started)), and its next start checks the node again. The browser tab reads `<name> - RocketClaw`, so several servers stay apart: `name` is `"server_display_name"` when set, else `web.tsnet.hostname`. `server_display_name` is read only from the config file (or its AWS Secrets Manager secret) and holds for the life of the process; changing it takes a restart.

RocketClaw identifies each browser by its Tailscale login, which the node looks up for each connection. It refuses browsers it cannot identify. See the [Web README](internal/rocketclaw/web/README.md) for the page's features.

#### Voice

The Voice page (`/voice`) and the voice button in each session's composer let you talk to an agent out loud. The browser sends audio straight to OpenAI's GPT-Live voice model. Each task the voice model hands off becomes a Web message in the conversation, marked `media=Voice`. Hand-offs enter the conversation in the order they arrive, without waiting for the agent: each one joins the running turn or starts the next. The voice model hears nothing about the agent's work while it runs. When a turn the call handed off finishes, its final reply is spoken once, as written. A reply over about 990 tokens (3,960 bytes) is not spoken: the voice says it is on the screen, and the Voice page switches to its transcript and shows the reply there as written. A message you type during the call cancels the spoken reply of every earlier hand-off; only a later hand-off is spoken again. It also mutes the voice at once, mid-sentence if need be, until the voice answers something you say after it. Messages other people type in the same chat neither cancel nor mute anything in your call, even when they join the turn running your hand-off. When the agent asks a question, the voice model says "I need your input. Please respond in the app." and reads the question and its options. Answer it in the app: spoken words never answer a question. Each person has one live call at a time: starting voice on the desk ends their call on the phone, while other people's calls, even on the same conversation, keep running. The Voice page offers only agents with `sticky: user` (see [Sticky agents](#sticky-agents)) and calls the person's own session of the chosen one, starting it the first time. RocketClaw ships one such agent, `agents/voice.md`: an [easy mode agent](#easy-mode-agents) with every section turned on behind review, whose instructions cover working behind a voice call. The page selects the person's last choice, else `voice`, else `main`, else the first agent offered. Its agent, voice and transcript choice belong to the person and are kept by RocketClaw, so they are the same on every device and survive restarts. Ending a call never stops the agent's work.

Voice has no settings of its own. Every agent uses the default `openai` provider for audio, independently of the provider running its model. Hand-offs still run on the conversation's selected agent and model:

- `auth: api_key`: the public GPT-Live API, model `gpt-live-1`. It costs about $0.05 per minute, silence included. An `api_base_url` must use HTTPS unless its host is `localhost` or a loopback IP; otherwise voice refuses to send the API key.
- `auth: chatgpt`: Codex's internal voice route, model `gpt-live-1-codex`, with the provider's ChatGPT login. Codex still uses this model ID after the GPT-Live-1.1 rollout; RocketClaw offers its 25 subscription voices, with Cove as the default. This route is unofficial and may stop working at any time. The ChatGPT account's data settings apply.

On both routes, a redirect to another scheme, host, or port drops the voice credentials.

The default provider must have usable credentials even when every agent uses a named provider. Voice calls send conversation context to that default account, and any voice charges belong to it; named providers' credentials and accounts are not used for voice.

Per call, OpenAI receives your microphone audio; up to 40 of the conversation's recent messages, as written, each cut to 600 bytes and 12,000 bytes in total; the final replies it speaks; and the question notices. It never receives tool names, tool arguments, tool results, progress, reasoning, or attachments.

A call ends when you stop it, when the page closes or stays hidden for 30 seconds, or when OpenAI ends the call. There are no idle or no-speech timers. If RocketClaw's connection to OpenAI drops, the `chatgpt` route reconnects, as Codex does, and the Voice page shows "Reconnecting to the agent…" meanwhile; the `api_key` route ends that OpenAI call as lost. The browser starts a new call on the same microphone after any lost connection (to RocketClaw, to OpenAI, or of the browser's audio, including audio not up 20 seconds after OpenAI answers), with sound cues, until it comes back or you hang up; after 15 minutes without coming back, the call ends with a message; see the [Web README](internal/rocketclaw/web/README.md#voice).

The microphone needs a secure page: HTTPS with a certificate the browser trusts, which the tailnet's HTTPS name provides. On a phone, with Tailscale connected, open `https://<hostname>.<tailnet>.ts.net/voice`.

#### Tailnet setup

Turn on MagicDNS and HTTPS certificates for the tailnet, then add `web.tsnet`:

```json
"web": {"tsnet": {"hostname": "rocketclaw", "tags": ["tag:rocketclaw"]}}
```

`"tsnet": {}` also works: the hostname defaults to `rocketclaw` and may use only `a-z`, `0-9`, and `-`. Each tag must start with `tag:`.

1. With RocketClaw stopped, run `rocketclaw ts login`. It prints a login link (and asks for device approval if your tailnet requires it), waits until the node is running, and exits. With `"auth_key": "tskey-auth-…"` under `web.tsnet`, it logs in with that key instead, and so does the server when the node is logged out. Prefer a single-use, pre-approved, tagged key; the key sits in the config file like other secrets. It checks only `web.tsnet`, so the rest of the configuration, such as `database_url`, can come later. When the settings live in an AWS secret, add `--aws-secrets-manager-arn ARN`, as for `rocketclaw run`.
2. In the Tailscale admin console, confirm that the node appears in your own tailnet. The login link adds the node to whichever tailnet signs in with it.
3. Start RocketClaw. It connects to the database and waits for its run lock, so that one RocketClaw at a time runs on a database. Right after it holds the lock, before migrating the database or starting anything else, it brings the node up, logging it in with `auth_key` when the node is logged out, and waits until the node runs, has an HTTPS name and listens on port 443. A node waiting for device approval is waited for, with a warning. If anything else stops it, RocketClaw releases the lock and exits with code 1 with an error that says what to fix: a node that needs a login (``run `rocketclaw ts login`, or set web.tsnet.auth_key to a valid key``), a tailnet without HTTPS names (`enable MagicDNS and HTTPS certificates in the Tailscale admin console`), or an error from the node itself. It never prints a login link.
4. With Tailscale connected, open `https://<hostname>.<tailnet>.ts.net`.

- Tag the node, for example `tag:rocketclaw` (the tailnet policy must list the tag under `tagOwners`). Add a tailnet rule that lets only named users reach it and lets it reach nothing. An untagged node acts as the Tailscale user who logged it in.
- The node signs each browser in as its device's Tailscale user. It refuses tagged devices, and no request header can choose the user.
- The node refuses every request that a page from another site makes in your browser, reads and images included, except from the [remote RocketClaws](#remote-rocketclaws) it trusts. Opening a RocketClaw address as a page still works from a link, a bookmark, or the address bar, but no other site can show RocketClaw, its Hosted Pages included, in a frame. Browsers that do not say which site a request comes from, such as Safari before 16.4, still cannot show another site RocketClaw's attachments or other answers. Hosted Pages' own files are the other exception: any page can still load them as images, scripts, or styles, because a hosted page runs sandboxed and the browser counts it as another site even on RocketClaw's own address. For the same reason a Hosted Page cannot show its own files in a frame.
- Node keys expire after 180 days unless the node is tagged or key expiry is disabled for it in the admin console. An expired node needs `rocketclaw ts login` again.
- Turning on tailnet HTTPS publishes machine names, including this node's, in public certificate-transparency logs.
- The node keeps its state outside the workspace, in `rocketclaw/tsnet-<hostname>` under the user config directory (`$XDG_CONFIG_HOME`, or `~/.config`). The run lock keeps a second RocketClaw on the same database from starting its node, but nothing stops two RocketClaws on one machine with different databases and the same hostname from sharing this folder, and so the same node: give each its own hostname. Each also needs its own profiling port: start the second with, for example, `--pprof=127.0.0.1:6061`, or with `--pprof=false`, or it stops at startup with `address already in use` (see [Monitoring and profiling](#monitoring-and-profiling)). Tailscale's log upload is off.
- Never use `tailscale funnel`: it would put RocketClaw on the public internet.

A browser on the RocketClaw machine uses the tsnet name too. Processes on a device signed in to Tailscale act as that device's user when they reach the node, so on an untagged machine signed in as the owner, local processes, including agent tools and MCP servers, can act as the owner. See the [transport README](internal/rocketclaw/frontend/rpc/README.md#identity-boundary) for the identity details.

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

- The session list, Cmd/Ctrl+P, desktop notifications, and Search pages cover every server. Remote chats show their server's name. Each server decides which of its own tabs wait for you and notify you; the page shows what every server says.
- The tab strip is one list of every server's cards: a card for each of its spaces, titled "space · server", which holds the space's Shared tabs above your own. You order the cards yourself by dragging a card's title, or with Shift+Arrow on it, and the order follows you across servers and devices. Every server's cards work alike: their tabs open, close, move, and take their full menu, and changes save on that server. A card's title folds it, and its **…** menu's **Go to space**, like opening one of its tabs, opens a remote space in that server's view under `/r/<name>/`: its tabs, agents, cron, skills, settings, pages, voice, and new chats. Opening a remote chat goes to its server too.
- The browser calls each remote directly, and the remote signs you in with your own Tailscale login, so you act as yourself with that remote's agents and accounts.
- A remote must serve its tsnet name over HTTPS, run a RocketClaw version with this feature, and trust the local page. Otherwise the page shows it as unavailable, with its address. A remote that stops answering keeps its chats listed, greyed out, until it answers again. A remote on another version keeps working with a notice, and some actions may fail.

Each RocketClaw's tsnet listener accepts these calls only from the pages its `trusted_rocketclaws` lists, because the remote signs in whoever's browser opened the page: any page it trusted could act as that person. Other pages, including other pages on the same tailnet, get no access, not even to read.

#### OpenCode servers

The web page can also start and open sessions on OpenCode servers. Name each server under `opencode` in the config file:

```json
"opencode": {"devbox": {"url": "http://devbox.example.ts.net:4096", "password": "set-me"}}
```

`opencode.<name>.url` is the server's `http` or `https` address, and `opencode.<name>.password` is required: RocketClaw signs in to the server as the user `opencode` with that password. Names use only letters, digits, `_`, `-`, and `.`. Changes take effect on restart. Agents call these servers with the `opencode_api` tool, under the `opencode` permission; an easy agent lists the ones it may call under `opencode` (see [Easy mode agents](#easy-mode-agents)).

#### Space permissions

Everyone the web page admits sees every space, session, and agent, unless `permissions` lists them. To let a person reach only some spaces, map their Tailscale login to the names of those spaces:

```json
"permissions": {"contractor@example.com": ["Support", "Ops"], "bob@example.com": ["Ops"]}
```

A listed person is limited to the spaces listed for them. People not listed keep full access. Logins and space names match ignoring case. The config does not load if a login is blank, starts or ends with spaces, or appears twice ignoring case, or if its list is empty or holds a blank space name or one that starts or ends with spaces. An empty list would otherwise read as no spaces but leave the person unlimited. Changes take effect on restart.

A limited person:

- Sees only their spaces. A listed space that does not exist yet is created when their web page loads. They cannot create, rename, or delete spaces, and they change Shared groups only in their spaces.
- Sees only the sessions in their spaces, everywhere the page lists, searches, or opens sessions. A session is in a space when its Birth Space is that space, or, when it has no Birth Space, when its current agent's `space:` names that space. Subagent and permission-review histories follow their top-level session. Remote OpenCode sessions are in no space for them. Slack threads and MCP chats whose agent is theirs are in their space, and what they send into a Slack thread still posts to Slack.
- Sees and uses only agents whose `space:` names one of their spaces. Agents with no `space:` are hidden. So are crons, cron runs, Hosted Pages, and Page Data of other spaces or of agents with no space.
- Takes turns only while the session's current agent is one of theirs: prompts, steering, popping, or editing queued items, answers to questions, voice, and Handoff summaries. They run a cron only when the cron and its agent are both in their spaces. When someone else switches a session to an agent outside their spaces, they can still read it, and they can switch it back with `/agent`. The composer does that on their next send.
- Gets no OpenCode in the Web (no OpenCode sessions, servers, or `/oc/` proxy); an agent of theirs reaches only the OpenCode servers it is given, as below. They cannot Reload, and cannot edit Behavior Settings, even when `web.admins` names them.
- Sees only the skills that at least one of their agents may use. The Config page shows them only their own Web and Tailscale logins and the Slack channels, each listing only their agents, beside their Appearance, Timeline, and Tabs choices.

Slack names stay visible to them.

These limits cover the web page only. Slack, MCP, and cron keep their own allowlists. Each [remote RocketClaw](#remote-rocketclaws) applies its own `permissions`, so list the person there too.

Agents act with their own permissions, not the person's. An agent in a limited person's space can reach beyond that space for whoever prompts it when it has the `rocketclaw_sessions` tools, a shell that runs on the host (anything but `shell: emulated`), the `main-update-rocketclaw-json` skill, `sudo`, `task` permission for agents outside the space, or OpenCode servers (an `opencode` permission, or an easy agent's `opencode` list), whose `opencode_api` tool runs commands and changes files, settings and credentials there. Give the agents of a limited space none of these. RocketClaw does not check this for you.

### Slack

Slack has one job: quick help when an allowed user mentions the bot. Everything else happens in the web page, including cron reports and MCP sessions.

Slack is optional. Without `slack.bot_token` and `slack.app_token`, RocketClaw starts without Slack and `slack.channels` may be empty. With the tokens, both are required and `slack.channels` needs at least one row.

Under `slack.channels`, map each channel to a list of agents and the Slack user IDs allowed to use them. A row named `@` is not a channel: it gives agents and allowed users for mentions in any other channel or group DM the bot is already in.

Only a mention from an allowed user starts work. Messages without a mention, including replies in threads the bot answers in, reactions, and edited messages start nothing, and 1:1 DMs never start work. A top-level mention in a configured channel starts a new thread answered by the channel's first agent; a top-level mention with no text, file, or forward is ignored. A mention in a thread the bot does not know takes that thread over and reads up to 50 earlier messages. A mention in a thread the bot already answers in is that conversation's next turn and adds only the mention itself. It waits for running work and active goals instead of steering them. Mentions keep their files and forwarded threads. Slack shows the thread working as soon as the bot accepts a mention, and the bot accepts each mention once, even if Slack delivers it again. If it cannot read the thread or take the request, it replies with a short error.

While a turn runs, the thread shows Slack's own working state with a Stop button, or that it needs someone while a question the turn asked in Web waits. A turn that runs scripts or subagents posts one checklist of them, updated as each finishes. When the turn ends, its answer and any files post as new replies. The answer ends with a footer: the agent, how the turn ended (done, failed, or stopped), and an Open in Web link. A turn without an answer posts the footer alone.

Slack threads that earlier versions posted cron reports or MCP sessions into are read-only: the bot ignores every reply and mention in them. Their conversations are Web chats now; keep working there.

Slack has no commands. `/stop`, `/agent`, `/goal`, `/enqueue`, skill names, and any other `/` text after the mention reach the agent as plain text. Start the message with the mention: Slack takes a message that begins with `/` as one of its own slash commands and never posts it. There is no agent picker, steering, queue card, question button, or reaction control. Mention turns cannot use `ask_user_question`, so the agent asks in its reply or hands the work to Web. Use Web to switch agents, run commands and skills, start goals, manage queued work, and stop any turn. Web messages used in a Slack thread are still posted there, and every reply in that conversation still goes to Slack.

Set up the Slack app with the bot events `app_mention`, `channel_rename` and `group_rename` and these bot scopes, then reinstall it:

- `users:read` and `usergroups:read`, so the web page shows names instead of Slack IDs.
- `users:read.email`, so a mention's turn gets the sender's email as `ROCKETCLAW_PRINCIPAL_EMAIL` (see [Agents and permissions](#agents-and-permissions)). Without it, Slack turns get only `ROCKETCLAW_PRINCIPAL_NAME`.
- `channels:read` and `channels:history`, so the bot can expand threads forwarded from public channels. It never joins channels on its own. `channels:read` also delivers `channel_rename`.
- `groups:read`, only for private channels, so the bot receives `group_rename`.

With `channel_rename` and `group_rename`, a renamed channel's new name shows in the Web sidebar right away; without them, it shows after a restart or the next mention in that channel.

The `message.*` event subscriptions and Interactivity are no longer used and can be removed.

The bot uses Slack's agent sessions, which need two more steps: turn the app into an agent (**Agents** in the app settings, manifest `features.agent_view`), which cannot be undone, and subscribe to the bot events `agent_session_stopped` and `agent_session_title_changed`. Then every turn in a mention thread, whichever surface started it, shows Slack's working state until it ends, renewed before Slack's one-hour limit, and Stop from an allowed user ends the turn and its goal, like Web `/stop`. The thread's session in Slack is titled after the mention that created it, and renaming it, in Web or by an allowed user in Slack, renames it in both. `agent_view` also adds an agent chat to the app's Messages tab that RocketClaw never answers.

Release note for Slack agent sessions:

- Slack's agent sessions are now required (see above). Without them, a mention thread shows no working state and has no Stop.
- Mentions no longer get a 🤖 reaction or a *Thinking...* message. Slack shows the thread working at once, and the answer posts as a new reply when the turn ends instead of replacing a placeholder.
- Turns in a mention thread started from Web, by a goal continuation, or by a background wake also show the working state, and Slack's Stop stops them.
- A turn that runs longer than an hour keeps its working state and Stop. A turn whose final never reached Slack no longer looks like it is working forever.
- While a Web question waits, the thread shows that it needs someone.
- A turn that runs scripts or subagents posts one checklist of them, edited as they finish.
- Subscribe the app to `agent_session_title_changed`: session titles now follow Web chat names both ways.

Release note for Slack sessions:

- Only `sticky: slack` agents answer in Slack. Point each Slack channel at one, such as the shipped `slack` agent. A channel whose agent is not `sticky: slack` logs a warning, shows a warning strip in Web, and ignores mentions; startup and reload no longer fail for it.
- A channel keeps one Channel Session with its agent. A mention at the top of the channel is answered at the top of the channel, and a busy Channel Session makes the next mention wait.
- The agent can call `slack_respond_in_thread(true)` to move the rest of its turn into the mention's thread, which gets its own Thread Session. The Channel Session records the thread's session ID.
- A mention in a thread, including a thread under the agent's own answer, goes to that thread's Thread Session. It waits while the thread's root mention is still being handled in the channel.
- Existing Slack threads keep their history and continue when their agent is the channel's agent.

Release note for mention-only Slack:

- Slack is now mention-only, and cron and MCP threads are read-only.
- Edited messages don't start work, even when the edit adds a mention.
- Mentions wait behind an active goal in their conversation.
- Queue cards and agent-picker buttons posted by earlier versions do nothing, and so do ⏫ and 🛑 reactions.
- Mention replies and cron reports end with a footer that links to Web. Where the workspace supports Slack agent sessions, an allowed user can stop a running mention turn with Slack's Stop; elsewhere, open the footer link and stop the turn in Web.
- Slack no longer shows questions. Question buttons posted by earlier versions do nothing, and a question still waiting fails when its turn resumes.

Release note for Slack answering mentions only:

- Only mention threads post to Slack. Cron reports and MCP sessions show as Web chats.
- Cron jobs no longer take `channel`; a leftover `channel` line is ignored. A scheduled run with a non-empty reply opens a Web chat, or reports into its global sticky agent's existing chat.
- `session_prompt` ignores `slack_channel`, so older MCP callers keep working.
- Existing cron report and MCP Slack threads become Web chats. They keep their history, and an MCP session keeps its private run; their Slack threads stay read-only and get no new messages.
- Ordinary chats, in Web or Slack, are deleted 90 days after their last message. Sticky chats keep their row, but their messages still expire after 90 days. Before, Web chats with messages were kept forever.

### MCP and scheduled jobs

MCP clients call `session_prompt` with a conversation ID and an agent with `sticky: off` (or no `sticky` setting). Each MCP conversation keeps its own Web chat run by that agent. Sticky agents are not exposed through External MCP. Continue the chat or switch between non-sticky agents from Web.

Set `mcp_external.enabled` to `true` and `mcp_external.listen_addr` to serve External MCP. `mcp_external.users` maps each login to its password, for example `"users": {"ci-bot": "set-me"}`, and callers send them with HTTP Basic auth. Without `mcp_external.users`, External MCP asks callers for no login at all; an empty object `{}` refuses every caller. Like any value in `rocketclaw.json`, a password can come from AWS Secrets Manager. `rocketclaw run --aws-secrets-manager-users-arn ARN` instead takes all the logins from that JSON secret, in the same login-to-password shape, replacing `mcp_external.users`. RocketClaw reads the logins when it starts.

A job file in `cron/` runs its agent on its repeating schedule: a 5-field cron expression, a Go duration such as `30m`, an `@daily`-style shortcut, or a list of them. For a single future run, an agent uses `rocketclaw_schedule_message`. An empty reply reports nothing; any other reply opens a new Web chat with the report, run by the job's agent, or reuses that agent's global sticky chat. The Cron page in Web lists every run, and continuing one opens the same chat. An [easy mode agent](#easy-mode-agents) schedules itself with `routines` in its own file instead; a cron file cannot name it.

Cron definition bodies expand shell snippets (an exclamation mark followed by a backtick-quoted command) before the model reads them. These are trusted commands, like those in agent prompts: they run in the workspace through the selected agent's `shell`, when set, without a tool permission review. Ordinary messages, scheduled messages, and Background Job completion notes stay literal.

Agents can use `rocketclaw_schedule_message` for a follow-up in the current conversation. `send_this_in` accepts positive durations up to `2160h` (90 days), the retention window, including `12h` and `168h`. Set `recurring` to `false` for one message or `true` to repeat at that interval (at least `1m`) until scheduled messages are reset. Pending schedules protect their conversations and explicit owners from normal retention cleanup.

Retention cleanup deletes ordinary chats, in Web or Slack, 90 days after their last message; an ordinary MCP chat goes with its private MCP session. A chat with no messages counts from when it was created. A cron run that no chat shows, such as one with an empty reply, is deleted the same way 90 days after its last message, with its subagent sessions; a run shown in a chat goes with that chat, or with its expired messages in a sticky chat. Queued work and pending schedules keep an ordinary chat or run. Sticky chat rows remain, but messages older than 90 days are deleted even while the chat is active, and so are the attachments only those messages used. Live turns, queued input and stashed drafts remain intact, and so do the attachments that newer messages, turns, queued input or pending schedules still use.

Release note for repeating cron schedules:

- A cron file whose `schedule` is an RFC 3339 time no longer runs once and deletes itself; it now fails startup and reload as an invalid schedule. Delete or reschedule such files before upgrading.

Release note for External MCP logins:

- External MCP logins moved from `rocketclaw.users.json` into `rocketclaw.json` as `mcp_external.users`, the same login-to-password object. RocketClaw no longer reads `rocketclaw.users.json`: before upgrading, copy its contents into `mcp_external.users`, or External MCP will ask callers for no login. Then delete the old file.

## How a request runs

1. A request arrives from the web page, a Slack mention, MCP, a cron job, or a scheduled message.
2. RocketClaw passes it to its agent harness with the chosen agent.
3. The harness calls the model and runs tools, within the agent's permissions.
4. RocketClaw saves each finished step and sends the reply back where the request came from.

On shutdown, tool calls already running finish and are saved, and background jobs are stopped. After a restart, each conversation continues from its last saved step.

## Agents and permissions

Each agent file must set `model` to a real model name, such as `gpt-6.1-sol` or `work/gpt-6.1-sol` (see [Models and providers](#models-and-providers)).

RocketClaw does not read the workspace's `AGENTS.md` by itself. To give an agent its contents, write ``!`cat AGENTS.md` `` in the agent's body.

For the `bash` tool, RocketClaw checks every command in a script on its own, and each command must be allowed. When several rules match, the last one wins. Scripts it cannot parse are refused, even with `bash: allow`.

Bash rules match command text, not everything a program can do. An executable-path wildcard such as `./scripts/*` does not match executable paths containing `..`; name such an executable explicitly if it is intended. This check is not a shell sandbox: an approved script can still launch other programs or follow symlinks. To put a sandbox in front of one agent's shell commands, set `shell` in that agent's frontmatter to the absolute path of a wrapper program. To run them in a bash emulator instead, set `shell: emulated`; see [Emulated shell](#emulated-shell).

A turn that a person starts from Web, voice, or Slack tells its shell commands who asked:

- `ROCKETCLAW_PRINCIPAL_NAME`: the person's name. On Web it is the Tailscale display name, or the login when that is blank. On Slack it is the Slack display name, or the real name when that is blank.
- `ROCKETCLAW_PRINCIPAL_EMAIL`: on Web, the Tailscale login; on Slack, the profile email.
- `ROCKETCLAW_PRINCIPAL_ID`: the lowercase hex SHA-256 of the email after trimming spaces and lowercasing it, so `Alice@Example.com` and `alice@example.com` get the same ID.

When RocketClaw has no email, only the name is set. The same person's values reach the subagents and shadows of that turn, the later turns of a `/goal` they set, and their queued or stashed messages when those run. A person who edits a queued or stashed message in Web makes it theirs: it runs as their Web message, with their values, whoever wrote it, even another agent. A steer into a running turn keeps the values that turn started with. Cron turns, External MCP turns, and messages one agent sends another get none of them. A name is a display name its owner can change, so use it for attribution only. Permission patterns that grant access per person should use `${ROCKETCLAW_PRINCIPAL_ID}` or `${ROCKETCLAW_PRINCIPAL_EMAIL}`. When a turn lacks a variable that a rule names, an `allow` or `auto` rule is skipped, and a `deny` rule matches as if the variable were `*`. Such a rule covers everything that runs with that person's values: steers and promoted queued messages that other people deliver into the person's running turn, the later turns of the person's `/goal`, and a background subagent that starts after the person's turn ended, which takes the values of whichever top-level turn runs then. Per-person rules keep people's work apart for convenience; they are not an authorization boundary between people. The values say who asked for the turn, not a credential: permission patterns read them from the turn, but a script sees whatever value the command that runs it sets.

Some features are off until you turn them on under `permission.rocketclaw`:

- `rocketclaw_sessions`: one key for the session tools. `rocketclaw_sessions_list`, `rocketclaw_sessions_get`, `rocketclaw_sessions_current_id`, and `rocketclaw_sessions_get_url` let an agent read saved conversations and get a chat's Web link; `rocketclaw_sessions_write` lets it start a Web session or send a message into one. `rocketclaw_sessions_move` moves the agent's own session to another space, named ignoring case, for everyone: that space becomes the session's Birth Space, and tabs people already have stay where they are. Only an agent without `space:` gets it, since an agent with one keeps its sessions in its space. `deny` (the default) leaves them all off. `auto` or `auto(name)` reviews every call first. `allow` runs them without review. The read tools can read every conversation in the database, not only the agent's own. To protect the database, each call is limited: a list needs a `since` time and returns at most 200 conversations, and a read returns at most the 100 newest entries before `before_entry_id`, ending with `[next_before_entry_id=N]` when older entries remain. A list also takes a required `criteria` string: empty lists every stored conversation, and anything else is a Web `/search` query (free text plus `tag:`, `cron:`, `agent:`, `room:`, `model:`, `space:`, `is:` and `sort:`) that returns only the chats `/search` would, with their matching messages. `space:"Blue garden"` matches the complete Birth Space name ignoring case, not the tab's current placement, and excludes sessions with no Birth Space. A read shows each call an `execute` script made as one `code_mode` line with its command, status, last output line and the approver's reason.
- `rocketclaw_store`, `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list`, `rocketclaw_kv_store`, `rocketclaw_kv_read`: let an agent publish Hosted Pages, such as reports, dashboards, and graphs, and the JSON Page Data they read. Each agent has its own files and data. `rocketclaw_store` returns a link, `/pages/<space>/<agent>/<path>`, that only people the Web admits can open; it opens as a tab in the space named after the agent's `space:`, and the Page Index lists every page, grouped by space. Pages run sandboxed, so their scripts cannot act as the viewer, and they can only read their own agent's Page Data. Chrome shows no PDF in a sandboxed frame, so a PDF page's tab offers an Open PDF link that opens the file in its own browser tab. The generated `main` agent has all six through `sudo`. Agents in remote OpenCode sessions publish pages through their server's MCP bridge; those pages belong to the session, open in its space, and are deleted with it. Moving one of those pages' tabs to another space moves the session, and with it all its pages, there.
- `rocketclaw_set_tag`: lets an agent tag sessions with labels from groups you define. See the [example agent](internal/rocketclaw/skel/agents/examples/session-tags.example.md).
- `allow_background: allow`: lets long scripts and subagents keep running while the conversation moves on, and lets the agent steer, queue a follow-up behind, or stop a subagent that is still running.

`rocketclaw_sessions_write` starts a Web session or steers, queues, or stops one, in any chat Web lists, and never waits for the other session. Instead, each write that returns a `queue_item_id` gets exactly one `<session_reply>` note, sent to the session that made the call, `task` subagents and hidden cron and External MCP runs included. The note carries the other session's final answer once the turn that took the message ends, or says that a person removed the message from the queue before it ran. It enters the caller's running turn or wakes an idle caller. Pending replies are listed with the caller's Background Jobs and can be stopped there; they need no `allow_background`.

One more setting controls who may write into an agent's sessions:

- `rocketclaw_sessions_recipient`: `true` (the default, except for sudo agents, which default to `false`) lets any agent start, message, or stop this agent's sessions; `false` lets none, not even another session of the same agent; a list of agent names lets only those. The agent checked is the one that makes the call, so a `task` subagent counts as its own agent. A refused call fails before anything is created, queued, or stopped. Reading sessions is not affected.

Name each tool under `permission.rocketclaw`. Wildcards such as `"*"` and the short form `rocketclaw: allow` are not accepted there.

To give one agent every tool and feature, including ones added later, set `sudo: yes` in its frontmatter (`true`, `1`, `on` or `yes`; `false`, `0`, `off` or `no` turn it off). Sudo replaces the agent's `permission` block: every call goes to the guardian for review first, including each `execute` script and every call inside it. Its outside MCP tools are every [facet](#outside-mcp-tools) of every configured server; a tool in no facet stays out of reach. Skills, `task` delegation, `ask_user_question`, `rocketclaw_update_goal`, `rocketclaw_attach_files_to_response`, scheduled messages and `rocketclaw_reload` run without review. Web search runs on the provider and is never reviewed. Session tag groups still go under `permission.rocketclaw.rocketclaw_set_tag`, and `permission.rocketclaw.rocketclaw_sessions_recipient` may stay too. Sudo does not lift `.env` blocking, workspace confinement, or `maxRecursion`, and it does not pass to subagents.

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
- `rocketclaw_sessions` defaults to `deny`, so existing agents can no longer start sessions until you allow it. That includes a deployed `agents/main.md` that still says `rocketclaw_start_new_thread: allow`; newly generated `main` agents allow it. Also update prompt text that names the old tool.
- An agent with `auto` or `allow` can write into and stop any chat Web lists, including Slack threads RocketClaw manages, and the replies post to Slack. This holds for every turn of that agent, including cron, External MCP, and Slack mention turns.
- Steering accepts any sender. A steer joins the running turn while that turn still takes input, whoever sent it and whoever started the turn, so a person's Web steer now also joins goal continuations, Completion Note wakes, scheduled messages, and the first turn of a session an agent created. Hidden cron and External MCP runs still take a steer as their next turn.
- `task`'s `continue` is now `{session_id, mode}` for agents with `allow_background: allow`. A still-running subagent is no longer refused: `mode` steers it, queues a follow-up after it, or stops it and runs the prompt next. A steer joins even when a foreground `task` call is waiting on that subagent. Queued follow-ups survive a restart, and no prompt, steer, follow-up, or wake reaches a subagent twice after a restart.
- A continue that names a different `subagent_type` switches the subagent to that type instead of failing. The type must exist, and the caller needs `task` permission for it. A steer switches a running subagent from its next step.
- Every subagent now runs as a session of its own, on the same turn runner as other sessions, so its queue, steers, stops, and restart recovery work as a top-level session's do. A subagent session is not listed in the sidebar or search. Its Background Jobs report to the subagent itself. It acts for its top-level session's running turn: it writes sessions and tags for that session, the sessions it creates keep that turn's creator and agent limits, the files it attaches go out with that turn's reply, and `rocketclaw_sessions_current_id` returns that session's ID. It is not offered `rocketclaw_schedule_message` or `rocketclaw_reset_scheduled_messages`. Its first prompt carries a header naming the session and agent that delegated it.
- Stopping a turn while its foreground `task` call waits also stops the subagent's current turn, and so on down a chain of foreground subagents, unless a background `task` job waits on that turn. The prompt the call sent, if still queued, is not withdrawn but held: it stays in the subagent's queue, shown in Web like a stash, through restarts, while the rest of the queue moves on. The next message sent to the subagent, by `task`, `rocketclaw_sessions_write`, or a person in Web, releases it to run just before that message, and a person's `/goal` releases it to run before the goal; a stash releases nothing, and a person's stash is never released this way. The stopped session gets no note. A restart is not a stop: the call waits again.

Every permission review starts with a header RocketClaw writes outside the transcript: the turn's source (`Web`, `Slack`, `Cron`, `ExternalMCP` or `System`, or `ScheduledMessage` with its creator's source), the person who asked when known, and the agent that sent the message. Subagents and shadows carry their top-level turn's header. The reviewer trusts it over any origin the transcript claims, so a rule such as "cron turns never use write credentials" can be enforced.

A decision model can settle reviews first. Configure OpenAI's Decisions API or TypeSafe under `decision` in `rocketclaw.json`, and name the model in `auto_approver_decision` (`{"model": "openai/gpt-6-luna", "min_confidence": 0.9}`), or in an easy agent's `approver.decision`. The guardian's reviews then ask that model about risk, user authorization, prompt injection and the outcome, and whether judging the call needs files or state it cannot see. They act on its answer only when every answer is confident enough (`min_confidence`, 0.9 by default), nothing needs a closer look, and the outcome follows the guardian's rules. Anything less, and every error, goes to the guardian as before. A custom reviewer (`auto(name)`) asks a decision model first only when its own frontmatter sets `approver.decision.model`, and then sends its own prompt plus `approver.decision.prompt` as the rules. The decision model and the chat reviewer each read their own prompt, so an easy agent whose `approver.prompt` has no `approver.decision.prompt` is never pre-gated, and a rule that must hold goes in both. TypeSafe receives the rules with each question rather than next to the conversation it judges. Agents can also ask decision models typed questions themselves: Code Mode scripts call `decision(model=..., state={...}, questions={...})`, optionally with `images=[...]` holding workspace image paths the agent may read or image file objects the script holds, which need no read permission and which reviewers see as descriptions, and get the answers back as a dict with the tokens used, so one script can sort many items at once. Every decision call is traced with its token counts. An agent gets this tool only when its `permission.decision` rules, or an easy agent's `decision` list, name the models it may call. Each provider's `api_base_url` can point at another service that speaks the same protocol.

Permission reviews also receive the current turn's submitted input and incorporated steers in order, even after compaction or a restart. These are input events, not cached grants: later restrictions and withdrawals still apply, and agent-originated input is not relabeled as a person's approval. Attachment contents are omitted. This evidence is saved for new turns; inputs already discarded from an older checkpoint cannot be reconstructed.

The complete input evidence is included even when the transcript still contains the same text. Large inputs therefore increase both review prompt size and checkpoint writes; compaction does not reduce this evidence.

### Code Mode scripts

Agents call `read`, `glob`, `grep`, `apply_patch`, `webfetch` and `bash` from Starlark scripts they send to the `execute` tool, along with `decision`, platform tools and outside MCP tools.

- Every call returns a result, a success or a failure, and the script goes on either way. A success is the value itself: `if`, comparisons, `str()`, methods, indexing and JSON all act on the value, as if the script had the plain value (so a `bash` call that succeeds without printing anything is falsy; `.ok` says whether it succeeded), and it adds `.ok` (`True`), `.error` (`None`), `.unwrap()` and `.or_else(x)` (both give the value). A call that fails, such as a missing file, a denied permission, arguments the tool rejects (positional arguments too: `read("notes.md")` fails with `read: only keyword arguments are allowed; call read(filePath=...)` and `read` does not run), an HTTP error, an MCP error or a non-zero `bash` exit, returns a failure. Every failure, whatever the tool, works the same way: `.ok` is `False` and `.error` is `<tool>: <message>` (such as `read: File not found: notes.txt`, `bash: exit status 2` or `webfetch: HTTP 404`; an MCP tool is named `server.tool`); it is falsy; it equals only another failure with the same `.error` and is never `in` a list, tuple, dict or set; `<` and the other orderings raise; `json.decode` and every method or attribute other than these and the details below give back the same failure (`bad.upper()` is `bad`), so a chain of calls carries the first failure; `.or_else(x)` gives `x`; and `.unwrap()` stops the script with exactly `.error`.
- A failure passed to another call, as an argument or inside a list or dict argument, is that call's result, and the call does not run: in `bash(command=r'''wc -l''', stdin=read(filePath="data.csv"))`, a missing file means `bash` never runs and `.error` is the read's.
- Details have the same names on a success and a failure, and a failure's JSON includes them: `bash` has `.output` and `.error_code`, `webfetch` `.url`, `.status`, `.headers`, `.output` and the rest of its details, `read` `.path` and `.name`, `glob` and `grep` `.pattern` and `.path` (`grep` also `.include`), an MCP call `.server` and `.tool`, and `decision` `.model`. So `read(filePath="out/missing.png")` fails with `.path` `out/missing.png` and `.name` `missing.png`, and inside returned JSON it is `{"error":"read: File not found: out/missing.png","name":"missing.png","ok":false,"path":"out/missing.png"}`. To work on a failed `bash` call's text, use `r.output.split("\n")`.
- `str(r)` of a success is the value's text; of a failure it is `.error`, then a new line and the output if there is any: `bash: exit status 2\nFAIL: TestLogin`, `webfetch: HTTP 404\nNot here`. Returning a result from `main()` gives the model the same text, in the foreground and in a background note.
- `.unwrap()` on a failure, `fail()`, Starlark errors and a stopped turn still stop the script, except inside a `gather`, `map`, `try_gather`, `try_map`, `race` or `race_first` branch: there the crash becomes an error result for that branch (`gather: fail: boom`), and the script goes on.
- `read` returns a file's raw text, without line numbers (`.numbered()` gives the lines with their numbers, such as `1: ready`), or a file object for PNG, JPEG, GIF, WebP and PDF. Other binary files, BMP and TIFF included, are refused with `Cannot read binary file: <path>`; convert them in the shell first. `read` has no size limit, and when the server runs out of file descriptors it waits and tries again.
- Images are turned upright from their EXIF orientation, as Codex and Pi do: a JPEG or WebP that `read` returns or an MCP tool sends, and a JPEG or WebP attached in Web or Slack. Only an image whose orientation says it is turned or mirrored is changed: it is re-encoded as a JPEG (quality 90) without its metadata or color profile, a WebP's transparency becomes white, and a WebP read from a file is renamed to `.jpg`. Its `size`, `sha256`, `width` and `height` describe the upright image, and saving it with `bash` stdin saves the upright JPEG. Such an image over 10,240,000 pixels is refused with `image too large to straighten (N MP; limit 10.24 MP)`: `read` and the MCP block return that failure, and an attachment is skipped with a warning. Other images are passed on untouched. Scripts have no image-editing methods; edit images in the shell.
- `glob` and `grep` return lists of paths and matches; returned on their own, they read as before. MCP results keep image and audio blocks as file objects. A `bash` call's output in a script is exactly what the command printed, so a command that prints nothing gives empty text (the `(no output)` placeholder is only shown when `bash` is called outside a script).
- Text results, such as `read` text, a `bash` call's output, a `webfetch` page, an MCP call's text and `glob` paths, work like strings: `len()`, `+`, slicing and indexing act on their text, and `==`, `!=`, `<`, `<=`, `>`, `>=`, `in` and `not in` compare them as plain strings. `"".join`, dict keys and string-method arguments still need `str(x)`.
- `json.decode` returns a result like a tool call: a decoded object, array or string is a success with `.ok` `True` that works like the plain value (an object or array can still be changed; its `type()` is `decoded_dict` or `decoded_list`), a number, `true`, `false` or `null` stays a plain value, text that is not JSON gives a failure such as `json.decode: at offset 1, unexpected character 'o'` instead of stopping the script, and a failure passed to it comes back unchanged.
- `gather` and `map` return every result in order, failures and crashed branches included. `try_gather` and `try_map` stop at the first failure or crash, cancel the rest and return that failure; when nothing fails they return a list that also has `.ok`, `.error`, `.unwrap()` and `.or_else(x)`. `race` returns the first success, with failures and crashes counting as losses, or the first failure when every branch loses; `race_first` returns whatever finishes first.
- `bash(command=..., stdin=...)` feeds text or a file object to the command, in the host and the emulated shell alike, with no size limit. Permission rules still match only the command. An `allow` rule runs the call unreviewed whatever stdin holds, so allowing a bare interpreter such as `python3` also allows any code piped into it. An `auto` rule, and sudo, show the reviewer text stdin in full up to 64 KiB and refuse larger text stdin before review with `stdin too large to review (max 64 KiB); write it to a file first`. A file is shown to reviewers as a description: name, type, size, dimensions, checksum and where it came from.
- File objects are accepted only by `bash`'s `stdin`, `decision`'s `images` and `main()`'s return value. Anywhere else the call fails with an error that names the tool and the argument, such as `bash: argument command got a file object; pass chart.base64(), a path, or text`.
- `webfetch` works like OpenCode's. It returns markdown (the default), text or HTML, and refuses images, PDFs and other non-text content as failed calls: `Unsupported fetched image content type: <mime>` and `Unsupported fetched file content type: <mime>`. Its result also carries, like `curl -v`, the final `url`, `status`, `status_text`, `content_type`, the response `headers`, every redirect hop (`url`, `status`, `location`), the `request` sent (method, URL, headers) and `timing_ms` (`total`, `first_byte`); `Authorization`, `Cookie` and `Set-Cookie` read `<redacted>`. An HTTP error such as 404 is a failure that keeps all of these. It follows at most 10 redirects and checks each hop like a first URL. An advanced agent's `allow` rule follows the hop, `deny` refuses it, and `auto` or no matching rule sends it to the guardian when the server answers, with the original URL, the redirect chain, the hop and the script line. An easy agent follows a hop its `allowedNetworkTargets` lists (or, under `auto`, any public address) and sends any other hop to the approver the same way. A hop to a private or internal address (loopback, private ranges, link-local including `169.254.169.254`, IPv6 unique-local, and the rest of the ranges easy `auto` refuses) is refused without review in every mode, unless the agent's own rules name that host: an advanced rule whose URL pattern spells out the host, or an easy agent's `allowedNetworkTargets` entry; no reviewer can approve one. It refuses URLs that carry a user name or password, drops the `#fragment` before the permission check, and waits 30 seconds by default and 120 at most, review time not counted. It has no body size limit.

What `main()` returns is what the model gets: text as text, a file as the real image or PDF (a file the model cannot take, such as MCP audio, as a line saying it was not attached), a failure as its `.error` followed by its output if it has any (a failed `bash` call's output, a failed `webfetch`'s body), and a list or dict as JSON with each file attached and marked `{"attachment": n}`. Inside that JSON a success is its value's JSON (a successful `webfetch` gives its details and `output`) and a failure is `{"ok": false, "error": ..., "output": ..., ...}` with its details (`error_code`, `status`, `headers`, ...): `return {"notes": read(filePath="notes.md"), "tests": bash(command=r'''make test''')}` gives `{"notes":"ready","tests":{"error":"bash: exit status 2","error_code":"2","ok":false,"output":"FAIL: TestLogin"}}` when the tests fail. Nothing the script read but did not return reaches the model. One `execute` returns at most 20 MiB of text and files together, counted in raw bytes. Text over 2000 lines or 50 KiB is clipped as before. Files that do not fit are held back while later, smaller ones still go, and the answer ends with a line such as `Not attached (over this call's 20 MiB budget): 2 big.pdf (15 MiB). Fetch with load_execute_result(result_id="…", attachment=2).` A file the model cannot take reads like `Not attached: audio-1.wav (audio/wav, 48 KB): the model cannot take audio/wav`. Held-back files expire with the text: at the end of the turn, or after 7 days for a background run. A returned failure also marks the `execute` call failed in the Web timeline, in traces and in the background job's status; a returned list or dict that holds failures counts as completed; the model gets the same text either way. A background run's Completion Note carries exactly the answer the call would have given in the foreground, for a success, a returned failure or a crash, with its files; the note itself only adds which job it is. Files go as they are, without resizing (only turned upright, as above), so a provider may still reject a very large image or PDF.

Release note for typed Code Mode results:

- **Breaking:** a failed call no longer stops the script. Scripts that relied on `webfetch` or MCP errors stopping them must check `.ok` or call `.unwrap()`.
- **Breaking:** `gather` and `map` return every result, failures included, and a crash inside a branch no longer stops the script: it becomes that branch's error result. `try_gather` and `try_map` stop at the first failure or crash and cancel the rest; `race` returns the first success, or the first failure when every branch fails.
- **Breaking:** a failure never equals or is `in` a plain value, and is not `in` a list even next to itself: compare `r.error` instead of the result. `.error` now starts with the tool's name (`read: File not found: x`), and a failed `bash` call's `.error` is `bash: exit status N` instead of its output.
- **Breaking:** `str()` of a failure, like a failure returned from `main()`, is `.error` followed by the output: `str(read(filePath="x"))` is `read: File not found: x`, and a failed `bash` call gives `bash: exit status 2` and its output on the next line. Use `r.output` for the output alone.
- **Breaking:** string methods on a failed `bash` call return the failure instead of working on the output: use `r.output.split(...)`. A failed `bash` or `webfetch` call's `type()` is `error`.
- **Breaking:** a successful `bash` call is truthy only when it printed something, like its text: `if bash(command=r'''grep -q x f'''):` is now always false; check `.ok`. Its text in a script is empty instead of `(no output)` when the command printed nothing.
- **Breaking:** a failure passed to another call, directly or inside a list or dict argument, is returned without running that call.
- **Breaking:** `json.decode` of text that is not JSON returns a failure instead of stopping the script, and a decoded object or array's `type()` is `decoded_dict` or `decoded_list`.
- **Breaking:** `read` returns raw text. Scripts that parsed its numbered lines use `.lines()`, or `.numbered()`, which gives `N: line` lines without the old `<path>` wrapper and footer.
- **Breaking:** `webfetch` refuses images and PDFs, and `glob` and `grep` results inside a returned list or dict become JSON arrays.
- **Breaking:** `webfetch` redirects to private or internal addresses are refused unless the agent's rules name them, and an advanced agent's redirect that no rule matches goes to the guardian instead of being followed.
- **Breaking:** Background job notes no longer carry a `state=` attribute; their body is the call's answer.

### Outside MCP tools

`mcp_servers` in `rocketclaw.json` lists the outside MCP servers agents may call from `execute` scripts. Each server sets either `command` (with optional `args`, `env` and `cwd`, run on the host) or `url` (with optional `headers`), and names its facets under `tools`: named slices of its tools that agents are granted by name. Server names use letters, digits, `_` and `-`.

```json
"mcp_servers": {
  "stripe": {
    "url": "https://mcp.stripe.com",
    "headers": {"Authorization": {"aws": {"arn": "arn:aws:secretsmanager:us-east-1:123456789012:secret:stripe", "key": "bearer"}}},
    "tools": {
      "read": ["list_*", "get_*", "retrieve_balance", "-get_secret_*"],
      "write": ["create_*", "update_*"],
      "sudo": ["*"]
    }
  }
}
```

- A facet is a list of tool names. `*` stands for any text and `?` for one character; there are no regular expressions. An entry starting with `-` removes the tools it matches from that facet, wherever it sits in the list. Facet names use letters, digits, `_` and `-`, and facets may overlap.
- A tool in no facet is reachable by no agent, sudo agents included.
- Credentials in `env` and `headers` can come from AWS Secrets Manager with the `{"aws": ...}` reference described in [Getting started](#getting-started). For the same reason, a server whose only facet is named `aws` fails to load; name it otherwise.
- Every server gets the `ROCKETCLAW_*` variables the agent's shell sees for that turn, such as `ROCKETCLAW_CONVERSATION_ID` and each `ROCKETCLAW_METADATA_*`. A `command` server gets them as environment variables; a `url` server gets them as headers, with `_` turned into `-` and an `X-` prefix (`X-Rocketclaw-Metadata-Ticket`). Config cannot set them: RocketClaw drops every `ROCKETCLAW_*` key in `env` and every `X-Rocketclaw-*` header in `headers`, and sends only the turn's values. A `command` server starts for each `execute` call whose script calls one of its tools or `search`, so it only sees that turn's values. A script that does neither connects to no MCP server. A value that cannot go in a header, such as one with a line break, makes calls to that `url` server fail.

Inside a script an MCP call returns its text (`str(r)`, `r.text`), its content blocks (`r.content`: strings, image and audio files, and resource dicts) and its structured content (`r.structured`). A tool error comes back as a failed result instead of stopping the script. An image block reaches the model only when the script returns that file.

Agents get outside MCP tools only by naming a server and its facets. A file that names none gets none.

- An easy agent lists facets: `mcp: {stripe: [read, write]}`. Its approver uses `approver.mcp` the same way.
- An advanced agent gives each facet a level under `permission`: `allow` runs the facet's tools without review, and `auto` sends each call to the reviewer. When a tool sits in several granted facets, the more careful level wins.

  ```yaml
  permission:
    mcp:
      stripe: {read: allow, write: auto}
  ```

- A sudo agent gets every facet of every server, reviewed.
- A `task` subagent started with a `tools` list gets no outside MCP tools; one started with `tools: null` keeps its agent's facets.

An unknown server or facet fails agent loading, and one agent file that fails to load stops RocketClaw from starting.

Release note for MCP facets:

- **Breaking:** an easy agent whose file leaves out `mcp:` no longer gets every MCP tool; it gets none. `mcp: auto` and `{server: auto}` fail to load; list facets instead.
- **Breaking:** advanced agents' raw `mcp` rules such as `"memory.*": allow`, a scalar `mcp: allow`, and `deny` or `auto(name)` levels fail to load. Define facets on the server and grant them as `{server: {facet: allow|auto}}`.
- **Breaking:** sudo agents get only tools in some facet. Give a server a facet such as `all: ["*"]` to keep every tool.
- **Breaking:** `task` no longer accepts MCP tools by `server.tool` name, and a `tools` list leaves out every MCP tool.
- `tools.read` and `tools.write` keep working as two facets named `read` and `write`, and may now overlap. Server names may no longer contain `.`.
- Fix agent files before upgrading and keep the old ones for a rollback: the new release refuses old-form files and the old release refuses new-form ones.

### Agent editor

The Agents page in the Web page has **Create New Agent** and, on each agent, **Edit**. They open the agent editor, where you pick an easy or advanced agent, fill in its settings and instructions with plain-language help, talk to it before it is saved, compare variants and the live agent side by side, and get suggestions from an assistant. Drafts are stored on the server, and teammates who open a draft's link edit it with you live. Only people with no space limits can use the editor, because a draft can give itself any permission and its test chats run real tools.

Each agent on the Agents page names where it comes from: `built-in`, the overlay as written in `overlays`, or `workspace`. A file that matches the one in the layer below it counts as that layer, so a built-in agent that RocketClaw copied into `agents/` shows as `built-in` while the copy is unchanged. Opening an agent shows its permission rules, written the way its instructions list them, and its prompt.

Saving writes one variant to the workspace's `agents/` folder, or to a Git overlay by a push to its branch or a pull request, chosen each time; an overlay pinned to a tag or commit cannot be saved to. The save checks the agent first, then runs the same reload as `rocketclaw_reload`. It rewrites the agent file in a standard order, dropping comments, and the editor shows that difference before saving. Pushes use the host's Git credentials. Pull requests use `gh` when it is installed and logged in; otherwise the editor links to GitHub's page for opening one. The draft then waits for the merge, and anyone can click **Stop waiting**, which leaves the pull request open. See the [Web README](internal/rocketclaw/web/README.md#agent-editor) for the details.

### Sticky agents

`sticky` in an agent's frontmatter, advanced or easy, limits how many Web sessions the agent has:

- `off`, or left out: every new session starts fresh.
- `user`: one session per person, by Tailscale login. A new Web session with the agent opens the person's existing one instead.
- `global`: one session for everyone, for multiplayer work. A new Web session with the agent opens the existing one, whoever started it.
- `slack`: the agent answers only in Slack. Each Slack channel it serves has one Channel Session, answered at the top of the channel, and each thread has one Thread Session. A turn started by a mention at the top of the channel can call `slack_respond_in_thread`; the first `true` moves the rest of the turn into the mention's thread for good. The agent is not offered for new Web sessions, cron, routines, External MCP or Voice, but its sessions open in Web. Whether the agent is `sticky: slack` is checked when each turn starts; a turn whose agent no longer is logs a warning and does not run.

Any other value stops the agent from loading. Each chat records when it is created whether it is a sticky chat, and that never changes. The existing session is the chat created as that agent's sticky chat: the person's own, by login, for `user`, or the one shared chat for `global`. A chat created any other way, including one created before the agent became sticky, never becomes it. A new one starts only when there is none, and the database allows only one, so simultaneous requests or cron runs open the same session. An existing sticky chat stays in its current space even if the agent's configured space changes. The Web page warns with an alert when it opens the existing session, both when you pick the agent on New session and when your first message goes there. `/agent` switches a session only to an agent of the kind the session was born as: an ordinary session to `sticky: off` agents, a `user` session to `user` agents the person has no sticky session with yet, a `global` session to `global` agents without a shared session yet, and a Slack session to the `sticky: slack` agents its channel lists. This holds even after the session's own agent changes its setting. A sticky agent's session cannot be forked. A sticky agent is a top-level agent: it can delegate, including to itself when its permissions allow, but no other agent runs it with the `task` tool or names it as a permission reviewer. Other agents reach it by writing to its session with the session tools. New Web sessions resume (New session, its voice button, Handoff's new session, and the Voice page). Session-tool creation also reuses the sticky chat, without renaming it; a personal sticky request without an identifiable person is refused, never assigned by display name.

Slack answers only with `slack` agents (below). External MCP conversations allow only `sticky: off` agents, including agents that omit the setting; External MCP does not offer or accept sticky agents. Each keeps its separate history, and neither joins a Web sticky chat.

Cron files and routines follow their agent. Each run of a `global` agent, from a cron file, an easy agent's `routines`, or the Cron page's Run now, reports into the agent's session instead of a chat of its own, opening that session when there is none. A global agent's cron must use the agent's space; an explicit different space fails loading. A run has no person to own a `user` session, so a `user` agent cannot be scheduled: a cron file naming it, or `routines` on it, fail loading, and Run now refuses it. The Voice page offers only `sticky: user` agents (see [Voice](#voice)).

### Easy mode agents

An agent file with `mode: easy` uses a shorter format. Leaving `mode` out, or `mode: advanced`, keeps the format described above; any other value fails to load. The rules of an easy file:

- A section left out, or set to `auto`, turns on everything in it. A section written out turns on only what it lists. `mcp` and `opencode` are the exceptions: they turn on only the [facets](#outside-mcp-tools) or servers they list, and nothing when left out.
- `opencode` lists the servers configured under `opencode` in `rocketclaw.json` that the agent may call with `opencode_api`, which can run commands and change files, settings and credentials on them. Left out or `[]`, the agent has no `opencode_api`; with a list, its calls to any other server are refused. `auto` and an unknown server fail loading.
- Everything turned on is reviewed before it runs by the agent's approver: the guardian, on `approver.model` when set, with `approver.prompt` added to its rules. `approver.decision` (`model`, `minConfidence`, `prompt`) sets the decision model asked first and its own rules. With `approver.prompt` set and no `approver.decision.prompt`, reviews are never pre-gated; write the rules in both to pre-gate them. As under `sudo`, skills, `task`, web search and the default-on RocketClaw tools (reload, scheduled messages, attaching files, goal updates and questions) run without review, and so does the `decision` tool for the models the `decision` list names.
- The approver works in the agent's `workingDirectory` and kind of shell, and has no `workingDirectory` of its own. Its `read`, `glob` and `grep` see that folder as the agent's do, not the workspace. It has only those three tools unless `approver` gives it more, with the keys the agent itself uses: `rocketclaw`, `tags`, `skills`, `mcp`, `allowedNetworkTargets`, `commands` and `secretsARN`. They are written and checked exactly as the agent's are, with three differences. A key left out of `approver` turns on nothing, where the same key left out of the agent turns on everything, except `mcp`, which stays off for both. Nothing reviews the approver: whatever its keys turn on runs at once. And `approver.rocketclaw` takes only `sessions`, `pages` and `tags`; `background` and `restart` fail to load. `tags` are set on the chat it is reviewing. Its `sessions` writes go out under the name `guardian`, so an agent whose `delegatedFrom` is a list must name `guardian` to accept them. `commands` gives it a shell with those commands, which get only the approver's own `secretsARN`, never the agent's. `allowedNetworkTargets` gives it `webfetch` and sets what that shell's `curl` reaches; left out, the shell reaches no network. It uses its tools only while it reviews: a review the decision model settles first never reaches them.
- An `execute` script is reviewed once, whole, before it runs, and that approves every call it makes: its calls are not reviewed again. The approver sees the script's code but not the values it reads or fetches while it runs. The exception is a `bash` call with text `stdin`: it is reviewed again when it runs, with its stdin, and refused when that text is over 64 KiB. A file on `stdin` adds no review. A call the file does not turn on is still refused.
- `model`, `space` and `workingDirectory` are required. A misspelled or unknown key fails to load, and so do the advanced keys `description`, `reasoningEffort`, `verbosity`, `permission`, `sudo` and `additionalInstructions`.
- The body is the agent's whole instructions. Easy agents get no reply-style line.

```yaml
---
mode: easy
model: openai/gpt-6.1-sol#high
space: Marketing
workingDirectory: sessions/marketing/$ROCKETCLAW_CONVERSATION_ID

subagents:
  - openai/gpt-6-luna#max
  - anthropic/claude-haiku-4-5#high
  - "openai/gpt-6*"
maxRecursion: 1

approver:
  model: openai/gpt-6-luna#max
  prompt: "Cron turns never use write credentials. Deny write actions the user did not explicitly request."
  decision:
    model: typesafe/jev-latest
    prompt: "Cron turns never use write credentials. Deny write actions the user did not explicitly request."
  rocketclaw: [tags]
  tags:
    - [reviewed, escalated]
  commands:
    campaign-owner: "campaigns owner"
  secretsARN:
    - "arn:aws:secretsmanager:us-east-1:123456789012:secret:marketing-review"

secretsARN:
  - "arn:aws:secretsmanager:us-east-1:123456789012:secret:marketing-read"

commands:
  stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"

mcp:
  acme_google_marketing: [read]

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

How an easy agent runs:

- Its shell commands run in the [emulated shell](#emulated-shell), in its `workingDirectory`, unless `shell` names the absolute path of a wrapper program. A wrapper runs on the host like an advanced agent's: it gets each command as its one argument and starts in the `workingDirectory`. As with every shell, RocketClaw refuses a `workdir` outside the folder and a command whose plain paths given to `cat`, `cd`, `cp`, `rm` and the like reach outside it; only reads may reach the chat's uploads and the agent's skills. That check reads the command's text, so a program the command runs, such as `python3 -c`, can still leave the folder unless the wrapper stops it. Instead of the secrets, the `commands` programs and the `curl` limits, it gets the agent's `secretsARN`, `commands` and `allowedNetworkTargets`, with `null` for a key the file leaves out or sets to `auto`, and the folder's resolved path as `workingDirectory`, as one JSON object in `ROCKETCLAW_ENVIRONMENT`, and decides what to do with them. File tools stay inside the folder either way. A relative path starts from RocketClaw's working directory. `$VAR`s come from the turn, each made into one safe path segment, or else from RocketClaw's environment; an unset variable fails the turn. `ROCKETCLAW_CONVERSATION_ID` is set on every turn, so the example gives each chat its own folder. The folder may not be or contain the workspace, nor be, contain or lie inside `.rocketclaw`, `agents`, `skills`, `cron` or `scripts`, wherever a link puts them. Subagents use their top-level session's folder.
- `read`, `glob`, `grep`, `apply_patch` and attaching files see only that folder, plus two read-only exceptions at their full host paths, which the shell sees too: the chat's uploads and the folders of the skills the agent may use. Files uploaded from Web go to `.rocketclaw/uploads/<chat>`; Slack files are put into the prompt, not uploaded.
- The emulated shell sees only the turn's `ROCKETCLAW_*` variables. The programs `commands` starts run in host bash with RocketClaw's environment plus the `secretsARN` secrets. Each secret is a JSON object of string values, and each key becomes one variable. Secrets are fetched on every turn; a failure fails the turn and names the ARN and key.
- ``!`…` `` lines in the body and in the `SKILL.md` of a skill the agent loads run in host bash, from the workspace, with the secrets. A skill's lines run before the model's arguments go in, so text the model passes never runs. Those in a routine's instruction run in the agent's own shell, from the `workingDirectory`, as `bash` calls do: in the emulated shell, or through a wrapper with RocketClaw's environment and `ROCKETCLAW_ENVIRONMENT` but not the secrets.
- `allowedNetworkTargets` limits `webfetch` with exactly the rules the shell's `curl` follows: both use the same client. The one difference: a `webfetch` redirect to a public address outside the list can reach it once the approver approves that hop; the shell's `curl` never asks.
- `task` takes a `model` from `subagents` and the `tools` the subagent gets, `null` for all of the parent's; a list leaves out every outside MCP tool. An entry ending in `*`, such as `anthropic/*`, allows every model whose name, `#effort` included, starts with the text before the `*`; a `*` anywhere else fails loading. The subagent runs the parent's own file, with the same folder, approver and secrets. Advanced agents cannot `task` an easy agent; they write to its sessions with `rocketclaw_sessions`, which `delegatedFrom` limits.
- Each routine runs the agent on a schedule written as in a cron file, with its instruction, or with `Scheduled run (<schedule>).` for a bare schedule. It reports like a cron job, in the agent's `space`. The Cron page lists it as `<agent> · <schedule>`, and `cron:<agent>` finds its runs. Schedules repeat; `rocketclaw_schedule_message` sends a single future run. A cron file cannot name an easy agent. Renaming the agent file leaves earlier routine runs under the old name.
- A shadow must use its parent's mode; one that does not is dropped with a warning. An easy shadow needs the same required keys, and its `space` may differ. Routine runs get no shadows, and a shadow's own `routines` never run.

### Emulated shell

An agent that sets `shell: emulated` runs its shell commands in real Bash inside a Linux sandbox instead of host bash: `bash` tool calls and `` !`…` `` lines. The sandbox has a pinned Ubuntu 26.04 LTS with the usual tools plus `python3` (with NumPy and SciPy), `node` (also as `js-exec`) and `sqlite3`. As on 26.04 itself, `ls`, `cat`, `head` and the other core utilities are the uutils (Rust) coreutils; GNU's are there as `gnuls`, `gnucat` and so on. Each command runs in a fresh Bash, so files in the folder persist between calls but variables, functions and `cd` do not.

```yaml
shell: emulated
workingDirectory: agents-work/$ROCKETCLAW_CONVERSATION_ID # required
commands:
  stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"
allowedNetworkTargets: # or auto, the default
  - api.stripe.com
  - localhost:8080
  - https://api.example.com/v1/
```

- `workingDirectory` works as an easy agent's: it is the only writable host folder, shown at its real path, and it resolves the same way. The chat's uploads and the agent's skills show read-only. `/tmp` and `$HOME` are the command's own: they hold up to 256 MiB and are gone when it ends. The `bash` tool's path check also runs here, against this folder, and lets commands use `/tmp` too.
- `commands` are the host programs the commands can start, under names of letters, digits, `_`, `.`, `+` and `-`. `stripe-write a b` runs `stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID a b` in host bash, in the folder, with RocketClaw's environment and, for an easy mode agent, its `secretsARN` secrets (an advanced agent has none): nothing the script sets, exports or passes reaches them, and the secrets never reach the sandbox. A command named like a builtin (`echo`) wins over it; `builtin echo` still reaches Bash's. Input and output keep their exact bytes, streamed both ways.
- The sandbox has no network. `curl` fetches through RocketClaw with webfetch's rules: `auto` allows public addresses and refuses loopback, link-local (including `169.254.169.254`) and private ones; a list allows hosts (optionally with a port) and URL prefixes. It sends GET only and takes `-s`, `-S`, `-f`, `-L` and `-o` (inside the folder); other options fail. Python, Node, `wget` and any downloaded program cannot reach the network at all.
- Each command is limited to 256 MiB of memory, 64 processes, one CPU, 16 MiB of output, files of 256 MiB and responses of 10 MiB, with the `bash` tool's timeout. A command stopped by a limit says so at the end of its output. Background jobs end with the command.
- RocketClaw runs on Linux, amd64 or arm64, only. The host needs `bwrap` (bubblewrap), unprivileged user namespaces (on Ubuntu 23.10 and later, an AppArmor profile that allows them for `/usr/bin/bwrap`), and cgroup v2 with the service started under `Delegate=yes`. The first start with an emulated agent builds the Ubuntu image under the runtime folder, which takes a minute or two and needs `mmdebstrap` and a subordinate uid range for RocketClaw's user (`/etc/subuid`, from the `uidmap` package); startup does not wait, the Web interface shows the sandbox as preparing, and commands wait for it. While Ubuntu's archive is unreachable or failing, the build keeps trying, after 30 seconds and then twice as long each time up to every 15 minutes; the Web shows the last failure and the next try, and the server log has each failure in full. Later starts reuse the image. The image is pinned to one archive snapshot (`internal/rocketclaw/sandbox/snapshot.gen.go`) and release, with every package version in `packages.<arch>.lock` beside it, and a build whose packages differ is refused.
- To take Ubuntu's latest security updates, run `go generate ./internal/rocketclaw/sandbox` and commit what it changes. It moves to the newest archive snapshot, builds the image for amd64 and arm64, and rewrites the snapshot, the locks and the SBOMs, unless no package changed, in which case it changes nothing. It takes minutes. Each RocketClaw builds the new image at its next start. A new Ubuntu release is a change of `suite` in `rootfs_linux.go`, then the same command. If anything is missing, the Web interface shows why and nothing runs outside the sandbox.
- Two environment variables speed up image builds, both when RocketClaw builds its image and in `go generate`: `ROCKETCLAW_SANDBOX_PACKAGE_MIRROR` names an Ubuntu apt mirror to download packages from first, with the pinned snapshot as the fallback for any package it cannot serve (the package lists always come from the snapshot); `ROCKETCLAW_SANDBOX_PACKAGE_CACHE` names a folder that keeps downloaded packages and package lists between builds, and after each successful build drops the packages that image did not install. `make sandbox-test` keeps its cache in the repository's `.tmp/sandbox-test/packages` and passes `SANDBOX_TEST_PACKAGE_MIRROR` on as the mirror.
- In a Docker container, the sandbox also needs, beyond `bubblewrap`, `mmdebstrap` and `uidmap` in the image and a subordinate uid range for RocketClaw's user: `--cap-add SYS_ADMIN` and `--security-opt apparmor=unconfined`, `seccomp=unconfined` and `systempaths=unconfined` (bubblewrap's mounts, `pivot_root` and `/proc`, which Docker's defaults deny); an entrypoint that, as root, remounts `/sys/fs/cgroup` read-write, moves the container's processes into a leaf cgroup, enables `cpu`, `memory` and `pids`, and hands a sibling cgroup to RocketClaw's user before starting it there; on an Ubuntu host, the AppArmor profile for `/usr/bin/bwrap`; and the runtime folder's `sandbox` on a case-sensitive local filesystem.

An easy mode agent uses the emulated shell unless its `shell` names the absolute path of a wrapper program; the differences are listed under [Easy mode agents](#easy-mode-agents).

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
model, independent history, tool results, and compaction can differ.

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

Type `/` in the web page to list commands and skills for the current agent. `/review some text` runs the skill named `review`, and `/stop` stops the current work. To queue a skill instead of running it now, use `/enqueue /review some text`. A first word that is neither a command nor a skill the agent can use stays plain text, so `/tmp is full` or `/Users/ann/notes failed` reaches the agent as written; `/skill name` always asks for a skill and reports one the agent cannot use. In Slack, `/` text is plain text for the agent.

Skill templates read their arguments the same way OpenCode commands do: `$ARGUMENTS` is the whole text, and `$1`, `$2`, and so on are single words or quoted phrases.

## Models and providers

The top-level `openai` object in the config file is the default model provider. Add more under `providers`:

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

An `#effort` ending, as in `work/gpt-6.1-sol#high`, sets how hard the model thinks. An advanced agent's older `reasoningEffort` key does the same, and the ending wins. An agent that sets neither sends no effort, so the model uses its own default. That holds wherever the agent runs: as a chat's agent, as a `task` subagent, which never takes its caller's effort, and as a custom reviewer (`auto(name)`). The guardian thinks at `low` unless an easy agent's `approver.model` names another effort.

The agent editor's assistant runs on `main`'s model. To give it another one, set `"agent_editor_model"` in `rocketclaw.json`, written as an agent file's `model` is (`work/gpt-6.1-sol#high`).

Web sessions get their titles from `openai/gpt-6-luna#low`: a person's first Web message names an unnamed session, and **Regenerate title** names it again from the conversation. To use another model, set `"title_model"` in `rocketclaw.json` the same way.

The guardian, the built-in reviewer that `auto` permissions and `sudo` agents send calls to, runs on `auto_approver_model`, a Behavior Setting (see [Getting started](#getting-started)), written as `gpt-6-luna` or `work/gpt-6-luna`. Left empty, it uses `gpt-6-luna` on `openai`. An easy agent's `approver.model` replaces it for that agent's reviews, and a custom reviewer (`auto(name)`) runs on its own agent's model instead.

Set `prompt_cache_markers_relative_position` (a negative number) only on a provider that accepts Anthropic `cache_control` markers, such as CLIProxyAPI in front of Claude. RocketClaw then marks one text piece of the last request message, counted from the end: `-2` marks the piece before the last. Automatic permission reviews send the planned action as the last piece, so `-2` lets the next review reuse the cached transcript. Leave it unset for OpenAI and other providers that reject unknown fields.

`auth` sets how a provider signs in: `api_key` (the default) uses `api_key`, and `chatgpt` uses a ChatGPT login.

Sign in per provider with `rocketclaw oai login [provider] [--headless]`. It sets that provider's `auth` to `chatgpt` in the config file, so you do not write `auth` yourself: a named provider needs only an entry such as `"providers": {"work": {}}`, and the default `openai` provider needs none. The command checks only that the provider exists, not the settings the server needs, such as `database_url`. If RocketClaw is running with another `auth` for that provider, restart it afterwards. Use `rocketclaw oai list` to see sign-ins and `rocketclaw oai logout [provider]` to remove one. Credentials are normally saved in `.rocketclaw/auth.json`.

With `--aws-secrets-manager-arn ARN`, the three commands read providers from the config file merged with that secret, as `rocketclaw run` does. For a provider that only the secret configures, `oai login` writes just `"auth": "chatgpt"` for it into the local config file, adding the provider's entry, and creating the file with mode 0600 when there is none; the secret is left alone. If the secret itself sets that provider's `auth` to anything other than `chatgpt`, the secret would win, so `oai login` stops before signing in and asks you to change it in the secret. If the secret already sets `"auth": "chatgpt"`, it only saves the credentials.

## Data and storage

- `database_url` sets the PostgreSQL database. Only one RocketClaw process uses a database at a time; others wait, without changing the database. RocketClaw upgrades the database schema at startup, once it holds the database and its Web node is up; a database last run by a version before v0.2.0 must be started with v0.2.0 first. Older RocketClaw versions refuse to start on an upgraded database, so you cannot roll back by swapping in an older binary.
- `attachments` sets where uploaded and generated files are kept. By default they go in `<workspace>/.rocketclaw/attachments`. You can pick another folder or an existing S3 bucket:

  ```json
  "attachments": {"driver": "filesystem", "path": "/srv/rocketclaw/attachments"}
  ```

  ```json
  "attachments": {"driver": "s3", "bucket_arn": "arn:aws:s3:::rocketclaw-attachments"}
  ```

  S3 uses the standard AWS credentials and region settings. Grant `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, and `s3:AbortMultipartUpload` for attachments. Files of 16 MiB or more are sent as multipart uploads by the AWS SDK's S3 transfer manager, in 8 MiB parts, five at a time; the multipart calls need only `s3:PutObject`, apart from aborting a failed one, which needs `s3:AbortMultipartUpload`. A bucket lifecycle rule that aborts incomplete multipart uploads removes any RocketClaw could not abort. RocketClaw deletes a file with a plain delete: in a versioned bucket, the file's earlier versions stay until a lifecycle rule removes them. RocketClaw does not create buckets or make files public.
- Back up the database and the attachment storage together. Restoring only the database brings back references to files, not the files.
- `.rocketclaw/` holds generated runtime files, such as cloned overlays, per-session temporary folders, and oversized command output. Do not treat it as source code.
- At every start, RocketClaw empties the runtime folder (`.rocketclaw/`, or `.femtoclaw/` with `femtoclaw.json`) and builds it again. It keeps only `.rocketcode`, `attachments`, `sandbox`, `uploads`, `auth.json`, `auth.json.lock`, entries whose names start with `.auth.json-`, and `overlays`. Anything else, including files you put there by hand, is deleted.

## Monitoring and profiling

RocketClaw logs a health line once a minute with memory, Go scheduler, and database connection figures.

When profiling is enabled, RocketClaw also samples blocking events at an average of one per 10 milliseconds spent blocked and one in 100 mutex contention events.

`rocketclaw run` turns on Go profiling at `127.0.0.1:6060` by default, and so do `--pprof` and `--pprof=true`. Use `--pprof=HOST:PORT` to serve it elsewhere, for example `rocketclaw run --pprof=127.0.0.1:6061`, or `--pprof=false` to turn it off. Any other value stops RocketClaw at startup with an error. Keep the host `127.0.0.1`: even there, any process on the machine can reach the port, and profiles can contain private data, so do not expose the port and do not share downloaded profiles. To inspect a remote machine, forward its profiling port over SSH; for the default port:

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
- Traces of agent turns, model calls, and tools. They go to PostHog's AI Observability (model, tokens, and latency for each model call) and to its distributed tracing. Each trace belongs to the person who sent the turn: their email when known, else their principal name. Turns nobody sent, such as cron jobs, have no person. Traces never include the text of prompts, conversation history, tool arguments, replies, or tool results: each of those reads `__REDACTED__`.

To get console logs and network requests in replays, also turn them on in the PostHog project settings.

The Config page's Flags section shows **PostHog** as On when a project token is set and Off when it is not.

## Repository layout

- `cmd/`: runnable programs.
- `internal/rocketclaw/`: the service, with its agent engine in `internal/rocketclaw/harness/`.
- `internal/funneld/`: the HTTPS proxy and its config loading.
- `vendor/`: copies of Go dependencies.

## Development

```sh
go test ./...
```

`go build ./cmd/rocketclaw` builds RocketClaw. `go generate ./internal/rocketclaw/internal/web` rebuilds the web frontend and OpenCode's UI with Bun into `internal/rocketclaw/internal/web/dist/` and `oc-dist/`, then replaces every file there with a gzipped `<name>.gz`. RocketClaw sends those bytes as they are to browsers that accept gzip and unpacks them only for the rest. Both folders are committed, so a Go-only build does not need Bun. When you change `internal/rocketclaw/web/` or `internal/rocketclaw/oc/`, commit the rebuilt files too.

`make test` in `internal/rocketclaw` first runs `go generate` on the frontends, the protobuf code (which needs `protoc` and `protoc-gen-go` at the versions in the `*.pb.go` headers) and the mocks, then lints. Then it starts PostgreSQL in Docker, or uses `ROCKETCLAW_TEST_DATABASE_URL` if set. It runs the Go tests once with race detection and coverage, then the emulated shell's acceptance tests (`make sandbox-test`, which need Linux with bubblewrap, mmdebstrap and a delegated cgroup), and enforces the fixed `COVERAGE_REQUIRED` minimum in its Makefile on both together, as CI does. Generated protobuf code (`*.pb.go`) does not count toward the total. GitHub Actions tests against PostgreSQL 18.
