# RocketClaw Cheatsheet

## Slack

The Web interface is the primary way to work. Slack has one job: quick help when an allowlisted user @mentions RocketClaw. Cron reports and External MCP sessions are Web chats. Slack has no `$` commands, help, agent selector, goals, queue, steering, or reaction controls; its only control is the native Stop on a running mention turn. In a mention, `$stop`, `$agent`, `$<skill>`, and any other `$` text reach the agent as ordinary text. Use Web to run commands and skills, switch agents, start goals, manage queued work, and stop any turn.

## Skill Calls (Web)

| Invocation | Meaning |
| --- | --- |
| `$review "first area" second third` | Invoke the allowed skill named `review`. |
| `$skill stop inspect the logs` | Invoke a skill whose name collides with a built-in; `$stop` itself still stops work. |
| `$enqueue $review args` | Queue the raw skill call as later work. |
| `$enqueue $skill stop args` | Queue an explicit skill call. |

The web dollar picker lists commands before the selected agent's allowed skills. Keyboard or mouse selection inserts a prefix without sending; switching agents updates the skill suggestions.

Arguments use the documented OpenCode command convention: `$ARGUMENTS` retains the complete suffix's internal whitespace and quotes; `$1` and later positions remove single/double grouping quotes. The highest position consumes all remaining parsed arguments, missing positions become empty, and `$0` remains literal. Thus `Compare $1 against $2` with `"first area" second third` becomes `Compare first area against second third`; `$1 / $3 / $0` with `first` becomes `first /  / $0`. A body without placeholders gets nonempty direct arguments after a blank line. Substitution precedes enabled skill-shell expansion.

While idle, sending starts a turn. During active work, web Send queues and web Cmd/Ctrl+Enter steers. A steer joins the running turn while that turn still takes input, whoever sent it (a person in Web or an agent through `rocketclaw_sessions_write`) and whoever started the turn, including goal continuations, Completion Note wakes, scheduled messages, and the first turn of a session an agent created. It runs as the next turn instead when no turn is running, the turn is already giving its final answer, or the turn is a hidden cron or External MCP run or a `$workflow` turn. Queued skills are checked and loaded only when consumed. Promoting a queued call with the web promotion control injects the skill before its request once, without a later duplicate; deleting it executes no skill shell blocks. Attachments remain part of the request, not invocation arguments.

## Slack Event Subscriptions And Scopes

In the Slack app's **Event Subscriptions**, subscribe to the bot event [`app_mention`](https://docs.slack.dev/reference/events/app_mention) (scope `app_mentions:read`), plus the rename events below. RocketClaw no longer handles message events, so the `message.channels`, `message.groups`, `message.im`, and `message.mpim` subscriptions are not needed and can be removed.

Under **OAuth & Permissions**, keep `chat:write` for replies, `reactions:write` for the 🤖 marker, `files:read` and `files:write` for attachments, and `channels:history` to read a thread RocketClaw takes over and natively forwarded threads. When an `@` row covers private channels or group DMs, also keep `groups:history` and `mpim:history`. Reinstall the app after changing scopes.

RocketClaw no longer uses Slack interactivity, so **Interactivity & Shortcuts** can be turned off. RocketClaw ignores presses on buttons posted by earlier versions.

## Slack Native Stop

While a mention turn runs, Slack can show its native working state with a Stop button. That uses Slack's [agent sessions](https://docs.slack.dev/ai/agent-sessions), which only apps declared as agents can create:

1. In the app settings, open **Agents** in the sidebar and turn on the Agent messaging experience (manifest `features.agent_view`). This cannot be undone. It adds the `assistant:write` scope; reinstall the app.
2. Under **Event Subscriptions**, subscribe to the bot event [`agent_session_stopped`](https://docs.slack.dev/reference/events/agent_session_stopped). Without it, Slack shows a working line with no Stop button.

RocketClaw sets a mention turn's thread to `processing` when the turn starts, and again when it resumes after a restart. It sets `active` when the turn ends, however it ends. Stop from a user on the channel row's allowlist ends the turn and stops the thread's active goal, like Web `$stop`; queued mentions stay queued, and the reply's footer says `stopped`. A press from anyone else changes nothing. A Stop that arrives after a newer turn started interrupts nothing. Only turns started by a Slack mention get the working state: report threads, Web-started turns, goal continuations, and background wakes get none. The 🤖 reaction still marks each accepted mention.

Slack ends the working state after one hour, and RocketClaw does not renew it, so a longer turn loses its Stop button; stop it in Web through the footer link. A workspace without agent sessions answers `feature_disabled` or `not_authorized`. RocketClaw logs that and runs the turn normally, without Stop; the footer link is then the way to stop it.

Before turning on `agent_view`, note that it makes the app's Messages tab an agent chat, and RocketClaw never answers there: it ignores 1:1 DMs. **App Home** → **Show Tabs** → **Messages Tab** (manifest `features.app_home.messages_tab_enabled`) turns that tab off. Slack's docs don't say whether an agent app may keep the tab off, or whether channel-thread sessions still work with it off, so check in your workspace.

## Slack Channel Rename Subscriptions

In the Slack app's **Event Subscriptions**, subscribe to the bot events [`channel_rename`](https://docs.slack.dev/reference/events/channel_rename) and [`group_rename`](https://docs.slack.dev/reference/events/group_rename). They require the bot scopes `channels:read` and `groups:read`, respectively. Reinstall the app after adding scopes. Private-channel rename events are available only for channels the bot belongs to.

## Slack Names in Web

Web resolves Slack tags such as `<@U123>` and `<!subteam^S123>` to names, and message search matches those names. Add the bot scopes `users:read` and `usergroups:read` under **OAuth & Permissions**, then reinstall the app. RocketClaw reloads the names every 8 hours. Without a scope, the affected tags show their Slack ID instead.

Add `users:read.email` too, then reinstall the app, so a Slack mention's turn gets the sender's email in `ROCKETCLAW_PRINCIPAL_EMAIL` and `ROCKETCLAW_PRINCIPAL_ID`. Without it, Slack turns get only `ROCKETCLAW_PRINCIPAL_NAME`.

## Slack Channel Scenarios

| Scenario | How To Trigger | Notes |
| --- | --- | --- |
| Ask for quick help | Mention the RocketClaw bot/app in a configured channel. | Starts a fresh managed thread using the first agent in that channel's ordered `agents` list. The mention is the first turn. A bare top-level mention with no text, file, or forward is ignored. |
| Mention outside a listed room | Mention the bot in a joined public channel, private channel, or group DM. | Needs an `@` row in `slack.channels`. Uses that row's first agent and allowlist. 1:1 DMs stay ignored. |
| Take over a thread | Mention the bot inside a thread RocketClaw does not know. | Adopts that thread. Bare `@bot` is enough. Includes the newest 50 prior texts. Ignored when RocketClaw posted the thread's root. |
| Continue a conversation | Mention the bot again in a thread it answers in. | Next turn of that thread's conversation; only the mention is added. Replies without a mention start nothing. |
| Mention while the agent works | Mention the bot during a running turn. | Waits and runs as the next turn; it never steers. Mentions also wait behind an active goal. Each mention is accepted once, even if Slack delivers it again. |
| Stop a running turn | Press Slack's Stop on a running mention turn, or use **Stop turn** or `$stop` in Web. | Slack's Stop needs agent sessions (see Slack Native Stop) and an allowlisted user. Report threads and Web-started turns have no Slack stop control. |
| Old report thread | A Slack thread an earlier version posted a cron report or External MCP session into. | Read-only: replies and mentions are ignored. Its conversation is a Web chat now. |
| External MCP conversation | Call `session_prompt` with an external conversation ID and an agent; a `slack_channel` field is ignored. | The ID owns one private MCP session and one Web chat, both run by the agent the first call named. The MCP agent stays fixed. The Web chat shows MCP history; MCP history does not show what people add in Web. Nothing is posted to Slack. |

## Goal Examples

Start goals from the Web composer; Slack has no `$goal`.

| Example | Meaning |
| --- | --- |
| `$goal ship the release` | Starts a goal loop with default `maxTurns: 5`. |
| `$goal maxTurns: 10 ship the release` | Starts a finite goal loop with a 10-turn budget. |
| `$goal maxTurns:10 ship the release` | Same as above; goal parameters may attach values directly after `:`. |
| `$stop` | Stops the active managed-conversation turn. If an active goal is present, it becomes `stopped`. |
| `✅` | Marker RocketClaw adds when a goal reaches `complete`. Humans generally do not send it as a command. |

| Goal Parameter | Accepted Values | Meaning |
| --- | --- | --- |
| `maxTurns:` | Omitted | Defaults to `5`. |
| `maxTurns:` | Positive integer | Finite goal budget. Progress shows `_Pursuing Goal (n/m)..._`. |
| `maxTurns:` | `0`, `-1`, `infinite` | Infinite goal budget. Progress shows `_Pursuing Goal..._`. |

## Saved Starlark Workflows

Author source lives at `workflows/<name>.star`; RocketClaw runs the effective `.rocketclaw/workflows/<name>.star`. Names are lowercase hyphenated stems of at most 64 characters, and `meta.name` must match. Use the bundled `main-create-or-update-workflow` skill to create, update, review, or troubleshoot workflows, then activate all requested edits with exactly one `rocketclaw_reload` call.

```python
meta = {"name": "summarize", "description": "Summarize a requested scope", "phases": ["run"]}
reader = worker(name = "reader", instructions = "Read and summarize the requested scope.", tools = ["read", "glob", "grep"])

def main(args):
    return phase("run", lambda: agent(args, worker = reader))
```

Exact builtins:

- `worker(name, instructions, model=None, tools=None)` defines a workflow-local worker. Omit `model` and `tools` to inherit the invoking agent; `tools=[]` grants no tools, explicit `task` is rejected, and `skill` also grants its required `find_skills` companion. Explicit tools may only narrow access. Workers never receive RocketClaw behavior tools.
- `agent(prompt, worker=None, label="", schema=None)` runs a fresh isolated worker and returns text or schema-shaped native Starlark data.
- `parallel(callables)` runs zero-argument callbacks concurrently and preserves declaration order.
- `pipeline(items, fn)` concurrently maps items and preserves input order.
- `phase(name, fn)` runs a phase once. Declared phase names must be unique; when `meta.phases` is present, only those named phases may run. Calls outside named phases still use the implicit `run` phase even when `run` is not declared. Declared, dynamic, and implicit phases count toward a limit of 100 total phases.

Fan-out is flat: nested `parallel` or `pipeline` calls are rejected. A run allows 16 concurrent fan-out callbacks, 1,000 callback dispatches, 1,000 agent calls, 100 total phases, and a shared 10-million-step Starlark budget. Bound every `while` loop and stop on success, no progress, blocked work, or an attempt limit.

Prompt shell expansion is disabled for workflow worker instructions, input prompts, and loaded skill bodies, so syntax such as `` !`command` `` remains literal. This is intentional to preserve the workflow permission boundary.

Return the human-visible value directly from `main`: strings render directly, other JSON-compatible values render as JSON, and only `None` or `""` is silent. Parallel workers share one checkout; assign disjoint file ownership and integrate shared files sequentially. Slack shows one in-progress placeholder and the final result, without workflow progress cards. Intermediate values do not enter managed history. `$stop` is terminal. A daemon restart resumes the run without repeating completed workers.

`workflow_button` belongs to Slack Workflow Builder. RocketClaw saved workflows do not use it.

## Channel Configuration Example

```json
"slack": {
  "bot_token": "xoxb-...",
  "app_token": "xapp-...",
  "channels": [
    {
      "channel": "#ops",
      "agents": ["main", "factory"],
      "allowed_user_ids": ["U0123456789"]
    }
  ]
}
```

Mentions in `#ops` start conversations with agent `main`. Switch a conversation to another listed agent, such as `factory`, from Web.

## Outbound MCP Servers

`mcp_servers` is distinct from inbound `mcp_external`. Server names use the same character set as MCP tool names in the official Go SDK: non-empty, at most 128 characters, and only letters, digits, `_`, `-`, and `.` (for example `sequential-thinking`, `my_server.v2`). Each entry is either stdio (`command` plus optional `args`, `env`, `cwd`) or streamable HTTP (`url` plus optional static `headers`). Set exactly one of `command` or `url`. Static headers and env are fine; OAuth is not supported. Changing `mcp_servers` requires restart. RocketClaw connects for each `execute` call and closes afterward. Starlark builtins sanitize `-`/`.` in server and tool names to `_` (for example `sequential-thinking.foo-bar` → `sequential_thinking_foo_bar`).

```json
"mcp_servers": {
  "demo": { "command": "my-mcp", "args": [], "env": {} },
  "acme": { "url": "http://127.0.0.1:9090/mcp", "headers": { "Authorization": "Bearer …" } }
}
```

`opencode` lists remote OpenCode servers whose root sessions appear in the Web session list. Names follow the `mcp_servers` rule. Each entry needs `url` (the server's `opencode serve` address), and `password` (a pairing token: `opencode pair`, then open the `/auth/connect/<code>` link with `Accept: application/json`). The Web reaches each server's API under `/oc/<name>/api/` on its own listeners and adds the password itself; the browser never holds it. That route admits only callers the Web admits (`web_users` or a Tailscale user), and each of them gets full control of that OpenCode server, including its shell. Changing `opencode` requires restart.

```json
"opencode": {
  "devbox": { "url": "http://devbox.example.ts.net:4096", "password": "…" }
}
```

Each `opencode` server also gets an MCP bridge at `<web origin>/opencode/<name>/mcp`, on the Web's TLS and tailnet listeners. Agents in that server's OpenCode sessions use it to act on their own RocketClaw session: `get_tags` and `toggle_tag` (any tag), and the Hosted Page tools `store`, `read`, `delete`, `list`, `kv_store`, and `kv_read`, which work like the agent page tools without `source`. Each call acts on the session OpenCode names in `_meta["ai.opencode/sessionID"]`, a subagent's on its parent's. A session RocketClaw does not list yet is refused. Its pages belong to the session: they open in its space, and are deleted with the session or with that space. Only sessions created or forked from the Web have a space; sessions started in OpenCode can tag themselves but cannot publish pages. Add the bridge to the OpenCode server's own config, with `ROCKETCLAW_MCP_AUTH` set to the base64 of `opencode:<password>`:

```json
"mcp": { "servers": { "rocketclaw": { "type": "remote", "url": "https://rocketclaw.example.ts.net/opencode/devbox/mcp", "oauth": false, "headers": { "Authorization": "Basic {env:ROCKETCLAW_MCP_AUTH}" } } } }
```

The bridge skips the tailnet sign-in and accepts password attempts from anyone who reaches the Web, so give every bridged server a randomly generated password: it already grants that server's shell. OpenCode must verify RocketClaw's certificate: use the tailnet listener or `web.tls_cert_file`, never disabled TLS verification, since the header carries the password. A bridge that failed to connect when its OpenCode folder loaded, as while RocketClaw restarts, stays failed until it is reconnected from OpenCode's `/mcps` or OpenCode restarts.

Agents use filesystem, shell, fetch, patch, and outbound MCP through the Code Mode `execute` tool (not as separate top-level tools). After the right permission grants, call `execute` with a short Starlark script (`def main()`). Inside the script: host builtins `read(...)`, `apply_patch(...)`, `glob(...)`, `grep(...)`, `webfetch(...)`, `bash(command=r'''…''')` with their normal permission buckets and subjects (including multi-subject `bash`); RocketClaw platform tools such as `ask_user_question(...)`, `rocketclaw_reload(...)`, `rocketclaw_update_goal(...)` (also still available top-level when eligible); MCP as `server_toolname(...)` after `permission.mcp` (permission subjects use raw `server.tool`). execute `code` is a JSON string (JSON still uses `"..."`). Inside it, `bash(command=...)` takes `r'''...'''` only, not Starlark `"..."` or `'...'`. Example: `{"code":"def main():\n    return bash(command=r'''grep -nE 'architecture|loop' FILE''')\n"}`. `r"..."` is raw but single-line; a real newline needs `r'''...'''`. `$` is valid inside a closed Starlark string. The script is parsed before host tools run; a `codemode.star` error means nothing ran. `bash(...)` results are text-like (`str(result)` before find/split). Discover tools with in-script `search(query="", namespace="", offset=0, limit=10)` (JSON items with path/description/signature/callable). A short Code Mode catalog is also in the system prompt. Skills, `task`, hosted `websearch`, and RocketClaw platform tools stay top-level when eligible.

To review the entire `execute` script before any nested call, set `permission.rocketclaw.code_mode_approve` in the agent frontmatter to `auto` (built-in guardian) or `auto(release-reviewer)` (named loaded agent). Set it to `allow` or omit it to skip whole-script review; the default is always `allow`. This decision never grants nested tool permissions, which are checked separately.

To let an agent run work in the background, set `permission.rocketclaw.allow_background: allow`. It accepts only `allow` or `deny`, defaults to `deny`, and changes only through that exact key. Each agent, including a subagent, uses its own setting. Every agent's `execute` takes `description` (a 3-5 word label shown to people); an allowed agent's `execute` gains `background`, its `task` gains `background` and `continue`, and it gets `rocketclaw_stop_background_job`. With `background: true` the call returns a job ID at once and the work becomes a Background Job; its result arrives later as a Completion Note (`<execute …>` or `<subagent …>` with `id`, `state`, and `description`). The note enters the owner's running turn at its next step, or starts a new system turn when the conversation is idle. If the turn that read a note fails or stops before saving, the conversation's next turn reads it without another wake, except that a hidden cron or External MCP run wakes again 1 minute later, then 5 minutes later, then every 30 minutes until a wake saves; only its first failure is posted to the destination, saying the wake retries. Every subagent is a session of its own, on the same turn runner as any other: its turns, queue, steers, stops, restart recovery, and history work exactly as a top-level session's do, and its own Background Jobs report to it, so a note for one of them wakes the subagent, and that turn stays in the subagent's history. When such a wake turn, or one a `<session_reply>` note starts, comes after the subagent's `task` call returned, its answer also goes to the parent as a `<subagent>` Completion Note (`<task_result>` inside), and so on up to the top-level session; a stopped wake turn, or one that took a prompt from a `task` call, reports nothing. Subagent sessions are not listed in the sidebar or message search, and `rocketclaw_sessions_write` cannot target them. Every `task` result of an allowed agent starts with a `continue ID:` line. To give that direct subagent another prompt on top of its earlier history, pass `continue: {session_id: "<continue ID>", mode: "steer"}` (`mode` is `steer`, `enqueue`, or `stop`); an empty `session_id` starts a new subagent and ignores `mode`. A continue is a `rocketclaw_sessions_write` into the subagent's session. The `subagent_type` a continue names wins: an unknown type is refused, the caller needs the same `task` permission for it as for starting one, and the subagent switches to that type, so the prompt and everything after it run with that type's prompt, tools, permissions, and model. `steer` joins the subagent's running turn at its next step while that turn still takes input, whoever started that turn, and otherwise runs as its next turn; a steer naming another type switches that turn from its next model step, the step in progress keeps the old type, and the switch survives a restart. `enqueue` runs the prompt after the work already queued in the subagent's session, and `stop` stops its running turn and runs the prompt next, ahead of that work; a foreground `task` call waiting on the stopped turn fails, and the subagent's own Background Jobs keep running. A `steer` that joins a running turn returns at once and says so; otherwise the call waits for the turn that runs its prompt (`background: false`) or reports it with a Completion Note (`background: true`). Queued prompts wait in the subagent's Thread Queue and survive a restart. After any restart, graceful or crash, a resumed `task` call finds the prompt it already delivered instead of delivering it again, and a call whose subagent turn already finished returns that turn's saved answer without a new model call. Denied agents keep the plain `execute` and `task` schemas, and a call with `background: true` or a non-empty `continue` fails.

Background work refuses `ask_user_question`, `rocketclaw_attach_files_to_response`, and `rocketclaw_restart` with a message to use them in the turn that receives the result; other tools work. An oversized background `execute` result keeps its full output privately for 7 days, and `load_execute_result` reads it from any later turn of the same conversation. `$stop` stops only the running turn and the foreground work it waits on, including the turn of a subagent a foreground `task` call waits on. Shutdown kills Background Jobs, including their bash. After a restart, subagent sessions resume their turns like any session, background `task` calls wait for them again, and a killed script is reported `killed` at the conversation's next turn without starting one, except that a hidden cron or External MCP run wakes so its destination hears about it. Slack has no move or stop control; ask an allowed agent to stop a job.

Concurrent tool calls inside one script use `gather`, `map`, `race`, and `race_first` (default `concurrency=16`, max 64). Example: `gather([lambda: read(filePath="a"), lambda: read(filePath="b")])` runs both reads together and returns ordered results; one failure cancels siblings. `map(paths, lambda p: read(filePath=p), concurrency=8)` maps over items. `race` keeps the first success; `race_first` keeps the first finish (success or error). Nested fan-out is allowed. These names also appear in the system-prompt Code Mode section and in `search`.

## Current State Schema

Set `database_url` on the selected config file. Fresh stores apply embedded SQL migrations. Isolation is one DSN per workspace.

Attachment originals use the top-level `attachments` setting: `{"driver":"filesystem","path":"/srv/rocketclaw/attachments"}` or `{"driver":"s3","bucket_arn":"arn:aws:s3:::rocketclaw-attachments"}`. The default is `attachments` under the workspace's selected runtime directory; relative paths are workspace-relative. S3 uses the standard AWS SDK credentials and region configuration. Originals are never overwritten, and PostgreSQL keeps only their metadata and ownership. Back up both PostgreSQL and the original-file store; changing the configured location does not move old files. See the README's attachment storage section for operational details.

## Model Providers And Credentials

`openai` is the default provider; named entries live under `providers`. An unqualified model uses `openai`, `openai/gpt-6.1-sol` explicitly selects the default, and `work/gpt-6.1-sol` selects `providers.work`. Root and child agents resolve their models independently, with no implicit provider failover. Provider credentials are isolated in the selected workspace and runtime directory. Optional `autocompaction_threshold` on `openai` or a named provider overrides that provider's compaction token threshold; omit it to keep the 200000 default. Optional `prompt_cache_markers_relative_position` (negative) adds an Anthropic `cache_control` marker to that text piece of the last request message, counted from the end; set it only on providers that accept the marker, such as CLIProxyAPI in front of Claude. `-2` caches permission review transcripts.

| Command | Purpose |
| --- | --- |
| `rocketclaw oai login [provider] [--headless]` | Acquire and save the selected provider's ChatGPT credential; omission selects `openai`. |
| `rocketclaw oai list` | Show provider, default marker, configured auth mode, and local credential presence. |
| `rocketclaw oai logout [provider]` | Remove only the selected local credential; it does not revoke a remote token. |

## Web Voice

Voice has no config fields. Every agent uses the default `openai` provider for audio, independently of its model: `api_key` uses the public GPT-Live API (`gpt-live-1`, about $0.05 per minute including silence); `chatgpt` uses its `rocketclaw oai login` credential on Codex's unofficial route (`gpt-live-1-codex`). The microphone needs HTTPS, or a browser on the RocketClaw machine at `http://127.0.0.1:3000` (with the local mapping below). For a phone or another device, RocketClaw runs its own Tailscale node (tsnet):

| Step | Setting |
| --- | --- |
| Tailnet | Turn on MagicDNS and HTTPS certificates. Turning on HTTPS publishes machine names in public certificate-transparency logs. |
| Config | `"web": {"tsnet": {}}`, or `{"tsnet": {"hostname": "rocketclaw", "tags": ["tag:rocketclaw"], "auth_key": "tskey-auth-…"}}`. The hostname defaults to `rocketclaw` (`a-z`, `0-9`, `-`); tags start with `tag:`; `auth_key` is optional (prefer single-use, pre-approved, tagged). |
| First login | With RocketClaw stopped, `rocketclaw ts login`: it uses `auth_key` or prints a login link, waits for device approval if needed, and exits once the node runs. The server never prints a login link: a logged-out node without a key only logs "Tailscale authentication unavailable" and the Web serves its plain listener. |
| Check | Confirm the node appears in your own tailnet. |
| Phone | `https://<hostname>.<tailnet>.ts.net/voice`, signed in as the phone's Tailscale user. Tagged devices are refused. |
| Tailnet rule | Recommended: tag the node and admit only named users to it, with nothing outbound. An untagged node acts as its owner. Untagged node keys expire after 180 days. |
| State | `rocketclaw/tsnet-<hostname>` under the OS user config directory, outside the workspace. Tailscale log upload is off. Never `tailscale funnel`. |
| Local browser | `"web_users": {"127.0.0.1": "<your Tailscale login>"}` for `https://127.0.0.1:3000`. The `listen_address` listener serves HTTPS (`web.tls_cert_file` and `web.tls_key_file`, or a self-signed certificate made at each start), uses `web_users`, then `tailscale whois`, and never trusts `Tailscale-User-*` headers. |

Local processes, agent tools included, can still act as the owner through the `listen_address` listener or this machine's own Tailscale address.

Hand-offs enter the conversation in arrival order without waiting for the turn. Nothing about the agent's work is sent while it runs. The final reply of a turn the call handed off is spoken once, as written, unless it exceeds about 990 tokens (3,960 bytes): then the voice says it is on the screen, and the Voice page turns on its transcript and shows the reply as written; a message typed during the call cancels the spoken reply of every earlier hand-off and mutes the voice in the browser until it answers speech heard since, as Codex does. Questions are announced ("I need your input. Please respond in the app.") with their options and answered in the app. There are no idle or no-speech timers. A dropped OpenAI connection reconnects on `chatgpt` (showing "Reconnecting to the agent…") and ends the call as `lost` on `api_key`. Every call that loses its audio (including audio not up 20 s after the answer), RocketClaw, or OpenAI starts a new call on the same conversation 1 s later, doubling to 10 s, with a falling tone, a tick every 5 s, and a rising tone, until hung up. On `api_key`, a plain-HTTP `api_base_url` is refused unless its host is `localhost` or a loopback IP. On both routes, a redirect to another scheme, host, or port drops the voice credentials. OpenAI receives the audio, the seed as written, spoken final replies, and question notices: no tool names, progress, or reasoning.

## RocketClaw Tools

RocketClaw injects these tools into agent turns as **Code Mode builtins inside `execute` only**; none is a top-level tool. Call them from Starlark by name, e.g. `ask_user_question(question="…")` or `rocketclaw_update_goal(status="progress", note="…")`. Most are auto-allowed by RocketClaw unless a per-agent permission rule explicitly denies them. `rocketclaw_restart` is default-deny and requires an explicit per-agent `allow`. The session read tools and `rocketclaw_sessions_write` are default-deny and need an explicit `rocketclaw_sessions` rule (see Agent Frontmatter And Permissions). `rocketclaw_dynamic_workflow` is not RocketClaw auto-allowed; it is gated by `permission.workflow.<stem>`, not by `task`. Workflow workers still do not receive these platform tools.

| Tool | Available In | Permission Default | What It Does |
| --- | --- | --- | --- |
| `rocketclaw_dynamic_workflow` | Persistent parent managed turns when at least one loaded workflow stem is `workflow`-allowed. Never on workflow workers, and not via the RocketClaw auto-allow list. | Gated by `permission.workflow.<stem>`. Not gated by `task`. Not RocketClaw auto-allowed. | Runs a saved Starlark workflow as a nested tool call inside the current turn and returns the final workflow text as the tool result. Slack does not stream phase/agent progress. Not a second managed turn; no human `$workflow` session summary entry. |
| `rocketclaw_restart` | Persistent bridge turns and raw/cron runs. | Default-deny. Requires explicit per-agent `allow`; generated `main` agents include that allow. | Cancels RocketClaw for supervisor restart after approved runtime config or overlay-list changes. In-progress work resumes after the restart. |
| `rocketclaw_schedule_message` | Persistent bridge turns and raw/cron runs of top-level sessions, hidden cron and External MCP runs included. Never subagent sessions. | Auto-allow unless explicitly denied. | Schedules a one-shot or recurring prompt in the current conversation. Recurring schedules persist until reset and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages` | Persistent bridge turns and raw/cron runs of top-level sessions, hidden cron and External MCP runs included. Never subagent sessions. | Treat as part of the schedule-message permission family. Deny `rocketclaw_schedule_message` to block schedule reset behavior. | Clears scheduled messages for the current conversation. |
| `rocketclaw_attach_files_to_response` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Attaches collected files to the final outbound response through RocketClaw's shared response-attachment path. |
| `rocketclaw_update_goal` | Persistent bridge turns only, and only when the current text conversation has an active goal. | Auto-allow unless explicitly denied, but hidden when no active goal exists. | Reports goal status as `progress`, `complete`, or `blocked` with an optional note. |
| `rocketclaw_sessions_list` | Every bridge turn, cron and External MCP runs and subagent sessions included. | Default-deny. Needs an `allow`, `auto`, or `auto(name)` rule on `rocketclaw_sessions`, the one key for all five session tools. | Lists stored conversations, newest first, with all five fields: `criteria`, `since` (required), `until`, `limit` (1 to 200), and `include_message_preview`. Empty `criteria` lists every stored conversation, private MCP and cron ones included; other `criteria` is a Web `/search` query that returns only the chats Web lists, with up to 3 matching messages each. |
| `rocketclaw_sessions_get` | Same as `rocketclaw_sessions_list`. | Same as `rocketclaw_sessions_list`. | Reads up to 100 stored entries of any conversation, private ones included, older than `before_entry_id` (0 for the newest). Ends with `[next_before_entry_id=N]` when older entries remain. |
| `rocketclaw_sessions_current_id` | Same as `rocketclaw_sessions_list`. | Same as `rocketclaw_sessions_list`. | Returns the owning conversation ID. Subagents get their own session's ID. |
| `rocketclaw_sessions_get_url` | Same as `rocketclaw_sessions_list`. | Same as `rocketclaw_sessions_list`. | Returns the Web URL of a top-level session, one Web lists: `https://<hostname>.<tailnet>.ts.net/s/…` once the `web.tsnet` node serves, otherwise `http://<tailscale-ip>:<port>/s/…`, using this machine's Tailscale IPv4 and the port from `web.listen_address`. Fails for Delegation Histories, private cron and External MCP conversations, and unknown IDs. |
| `rocketclaw_sessions_write` | Every bridge turn of an agent whose `rocketclaw_sessions` rule is not `deny`: Web, Slack mentions, cron, External MCP, goal continuations, and scheduled messages alike, and subagent sessions whose own rule offers it, which write for their top-level session's running turn, with its creator and limits and, when `agent` is empty, its agent; the recipient check and the reply use the subagent's own agent and session. Never in workflow workers. | Same as `rocketclaw_sessions_list`; under `auto` every write is reviewed first (see Agent Frontmatter And Permissions). Generated `main` agents set `rocketclaw_sessions: auto`, so each of their session reads and writes is reviewed. | Takes `session_id`, `mode`, `prompt`, `title`, and `agent`, all required. With an empty `session_id`, creates a Web session named `title`, run by `agent` (empty for the caller's agent), and queues `prompt` as its first turn; `mode` is ignored and nothing is posted to Slack. The new session does not see the caller's history, so the prompt must carry all needed context. Slack turns may only pick agents allowed in their channel, and cron runs are locked to the job's agent. With a `session_id`, delivers `prompt` to that session without waiting for its turn: `steer` joins its running turn while that turn takes input, otherwise runs as its next turn; `enqueue` runs after the work already queued; `stop` stops its turn and goal, as `$stop` does (its Background Jobs keep running), then runs `prompt` before everything already queued. Only `stop` on an existing session takes an empty `prompt`: it then just stops the turn and goal, queues nothing, and, once the stopped turn has saved, posts `Stopped by session <id> (agent <name>)` into that session's transcript, exactly once. That line starts no turn; a person sees it in Web and the model reads it the next time the session runs. The target must be a top-level session, one Web lists (Slack-thread chats included), and not the caller's own. Every call, create and stop included, fails before anything is created, queued, or stopped unless the target agent's `rocketclaw_sessions_recipient` accepts the calling agent. The receiver sees a `[System …]` header naming the sending session and agent and saying its final answer returns to the sender automatically, and the prompt is never treated as a person's input. Returns JSON with `outcome` (`created`, `steered`, `queued`, or `stopped`), `session_id`, `url`, `queue_item_id` (absent only for an empty-prompt stop when no turn was running), and `turn_was_running` for `stop`; share `url` so a person can open the session. The URL is looked up first, so a failed lookup creates or delivers nothing. A write cut off by a crash is reported as interrupted, not repeated. Each call with a `queue_item_id` gets exactly one `<session_reply queue_item_id=… session_id=… agent=… mode=… state=… url=…>` Completion Note in the session that made the call, hidden runs included: when the target's turn that took `prompt` ends (`completed`, `failed`, or `stopped`, with that turn's final answer; a steer's answer covers the whole turn it joined), when a person removes the queued message in Web (`removed`), or, for an empty-prompt stop, once the stopped turn is over. A failed or stopped outcome suggests `rocketclaw_sessions_get` with the target's ID. The reply is a Background Job of the caller, listed in its Web job list, that needs no `allow_background`; stopping it leaves the target alone and sends a `stopped` note at the caller's next turn without waking it. A subagent stopped with its task job or its parent's turn drops its pending replies without notes. |
| `rocketclaw_store` | Every bridge turn of an agent that allows it. Subagent sessions act as their top-level session's agent; shadow agents have their own pages. | Default-deny. Needs an explicit `allow` on this name. Generated `main` agents allow it. | Stores a Hosted Page file at `path` from `content`, `content_base64`, or a workspace `source` file (10 MiB cap), replacing any file there, and returns its link: `<web origin>/pages/<agent space or ->/<agent>/<path>`, on the same origin as `rocketclaw_sessions_get_url`. The link opens as a tab in the viewer's space of that name, otherwise in their current space; a link under an old space name redirects. `/pages-raw/<agent>/<path>` serves the file sandboxed (opaque origin), so page scripts cannot use the API as the viewer; pages read Page Data by `postMessage` from their tab. |
| `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list` | Same as `rocketclaw_store`. | Same as `rocketclaw_store`. | Read a file (text, or base64 when not UTF-8), delete a file (`path`) or Page Data entry (`key`), and list the agent's files with links and its Page Data keys. |
| `rocketclaw_kv_store`, `rocketclaw_kv_read` | Same as `rocketclaw_store`. | Same as `rocketclaw_store`. | Store and read the agent's Page Data: any JSON value per `key`, replaced whole. The agent's pages may read it; nothing writes it from the browser. |
| `ask_user_question` | Human Web turns, including Web turns in Slack-thread conversations. | Auto-allow unless explicitly denied, but hidden on every other turn. | Asks the human through a card above the Web composer, blocks until answered or canceled, and returns selected options and/or custom text. Not exposed for Slack mentions, cron/raw, MCP, scheduled/system/automation, or automatic goal continuations. A question still waiting at restart stays listed in Web, and answering it continues the turn. |
| `rocketclaw_stop_background_job` | Persistent bridge turns, cron runs included, of agents with `allow_background: allow`. | Shown only with the exact `permission.rocketclaw.allow_background: allow` rule; RocketClaw auto-allow does not show it. | Stops a running Background Job by `job_id`: one of this conversation or of a hidden cron or External MCP run that reports here. Stopping a `task` job also stops its subagent's running turn and the subagent's own Background Jobs. Stopping a `rocketclaw_sessions_write` reply only stops waiting for it; the target session is untouched. Its note says the job was stopped by agent. A job that already finished returns `had already finished`. |
| `execute` | Shown when the agent has MCP grants, code-mode host grants (`read`/`edit`/`bash`/…), and/or RocketClaw platform-tool grants. The only model entry for FS/shell/fetch/patch/MCP and every RocketClaw platform tool. Each call made inside a script is saved as a small record of its own, written when the call starts and when it finishes (its arguments clipped to 4 KiB, a one-line output summary, its status, and the approver's reason when it ran; never its output), and shown under the script's row. | `permission.rocketclaw.code_mode_approve` checks the full script before execution; omitted means `allow`. Nested calls re-check real buckets (read/edit/bash/mcp/rocketclaw/…). | Code Mode: runs a short Starlark script with required `code`. Define `def main()`; call host tools as `read(filePath="…")` / `bash(command=r'''…''')`, platform tools as `ask_user_question(question="…")` / `rocketclaw_reload(reason="…")`, MCP as `server_toolname(**kwargs)`, concurrency via `gather`/`map`/`race`/`race_first` (default concurrency 16, max 64), and `search(query="", namespace="", offset=0, limit=10)` to discover tools and concurrency builtins. `bash(command=...)` takes `r'''...'''` only inside the JSON `code` string. Example: `{"code":"def main():\n    return bash(command=r'''grep -nE 'architecture|loop' FILE''')\n"}`. Parsed before host tools; failed wrappers are not evidence. Example: `def main():\n  return gather([lambda: read(filePath="a"), lambda: read(filePath="b")])`. Connects MCP per call, then closes. |


For general `permission` syntax, action values, and approval reviewers, see Agent Frontmatter And Permissions below. Tools are deny-by-default and later matching rules win. RocketClaw's default tool allows are injected after agent permissions unless an explicit deny already matched, so use `deny` rather than `auto` when the intent is to block or force review of a RocketClaw auto-allowed tool. For the default-deny `rocketclaw_restart`, `auto` is not enough; use explicit `allow`. Outbound MCP tools are never RocketClaw auto-allowed; grant `permission.mcp` explicitly.

## Slack Reactions And Markers

RocketClaw reads no reactions. These are markers it posts:

| Emoji | Reaction Name | Meaning |
| --- | --- | --- |
| `🤖` | `robot_face` | Added when RocketClaw accepts a mention; removed after final response delivery. |
| `✅` | `white_check_mark` | Added when a goal in a Slack thread's conversation reaches `complete`. |
| `🏁` |  | Goal header RocketClaw posts while a goal is running. |
| `📡` | `satellite_antenna` | `📡 web` heads Web input mirrored into a Slack thread. |

## Agent Frontmatter And Permissions

Agents are top-level Markdown files in `agents/*.md`. The filename without `.md` is the agent name. YAML frontmatter is required, followed by the agent prompt body.

```yaml
---
description: Short human-readable purpose
model: gpt-6.1-sol#high
verbosity: medium
maxRecursion: 2
shell: /usr/local/bin/agent-sandbox
additionalInstructions: Reply in plain text suitable for Slack.
schema:
  output:
    type: object
    properties:
      answer:
        type: string
    required:
      - answer
permission:
  read:
    "README.md": allow
    "docs/**": allow
  edit:
    "docs/**": allow
  bash:
    "go test ./...": allow
    "make lint": auto
  skill:
    "main-*": allow
  task:
    "reviewer": allow
  workflow:
    "audit-routes": allow
  mcp:
    "demo.*": allow
    "demo.danger": deny
---

Agent instructions go here.
```

Agents that previously used `task` grants to launch workflows must add `workflow:` allows. That is a clean break: `task` no longer enables `rocketclaw_dynamic_workflow`.

Outbound MCP is deny-by-default and omitted from the model tool list until `permission.mcp` grants a configured `mcp_servers` name. Prefer least-privilege `server.tool` subjects; wildcards such as `demo.*` or `*` grant the full current and future tool catalog for matching names. Later denies subtract dangerous tools.

Known frontmatter fields:

| Field | Meaning |
| --- | --- |
| `description` | Required short purpose shown when selecting agents. |
| `model` | Required concrete model or `{{ model "name" }}` placeholder configured in `models`, with an optional `#effort` reasoning-effort suffix such as `gpt-6.1-sol#high`. Avoid `#xhigh`; `rocketclaw lint` reports it. |
| `reasoningEffort` | Deprecated; use the `model` suffix. A `model` suffix wins over this field. |
| `verbosity` | Optional model verbosity. |
| `maxRecursion` | Optional task delegation depth for inferences started with this agent. Omitted or `-1` is unlimited, `0` disables `task`, and positive integers allow that many levels. |
| `shell` | Optional shell for this agent's commands instead of `/bin/bash`: either the absolute path of a program, called as `<shell> <command>`, or `emulated` for the built-in bash emulator (see [Emulated shell](../../README.md#emulated-shell)). Any other value stops this agent from loading. A path names one program; the value cannot carry extra arguments. Omit it to keep default bash. See the wrapper contract below. |
| `space` | Optional Web space name, as one string; any other value stops this agent from loading. A Web session started with this agent is born in that space, created when missing, and opens there. A new Web session offers every agent; a session whose agent has a `space` offers only agents with the same `space`, ignoring case, or every agent its Slack channel allows when none of those has that `space`. |
| `additionalInstructions` | Optional RocketClaw normal-reply prompt-header override for this selected agent. Use it for response-format guidance, such as telling an agent to avoid Markdown for Slack, prefer Markdown for technical answers, keep answers terse, or follow another surface-specific style. When omitted, RocketClaw uses `Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.` It does not affect internal notes or raw cron runs. |
| `schema.output` | Optional JSON Schema mapping. When set, that agent's own turns (including `task` children) use OpenAI structured output. Permission-review child runs keep their fixed decision schema. Workflow `agent(..., schema=...)` still overrides for that call. Object schemas are sent with `additionalProperties: false`; list every property in `required`. |
| `permission` | Optional singular permission map. Omit it when the agent needs no tools. Do not use plural `permissions`; RocketClaw ignores it and `rocketclaw lint` reports it. |
| `sudo` | Optional `true`/`1`/`on`/`yes` or `false`/`0`/`off`/`no`, any case; other values stop the agent from loading. When on, it replaces `permission`: every tool and feature is offered, and every call requires automatic approval by `guardian`, including the whole `execute` script, except `skill`, `find_skills`, `task` and the default-on RocketClaw tools (`rocketclaw_reload`, scheduled messages, `rocketclaw_attach_files_to_response`, `rocketclaw_update_goal`, `ask_user_question`), which run without review. Only `permission.rocketclaw.rocketclaw_set_tag` tag groups and `permission.rocketclaw.rocketclaw_sessions_recipient` may stay next to it; `rocketclaw lint` reports anything else (`RC011`). Hard limits such as `.env` blocking and `maxRecursion` still apply. |

When an agent sets `shell` to a program path, RocketClaw runs each shell command made on that agent's behalf as `<shell> <command>`: its `bash` tool calls, the ``!`…` `` lines in its prompt and in the skills it loads, and, for the root agent, input-prompt expansion. RocketClaw does not check at load time that the program exists. If it cannot start, the `bash` tool reports the error to the agent and does not fall back to bash. A wrapper program must follow this contract:

- It receives the whole command string as its only argument. There is no `-c`. The command can start with `-`, so treat `$1` as data: pass it as the `-c` argument or after `--`, never where the wrapper or a sandbox tool parses options.
- It should run that string with bash, for example `bash --noprofile --norc -p -c "$1"`. The permission check still parses every command as bash, so running it with anything else makes the check describe a different command than the one that runs.
- A wrapper that is itself a bash script should start with `#!/bin/bash -p`, so an inherited `BASH_ENV` or exported functions cannot change it before it hands off the command.
- The working directory and `TMPDIR` are set on the wrapper process. Carry both into the sandbox, and make the workspace available at the same path inside it.
- The environment is the full RocketClaw process environment, which can include provider credentials. Forward only the variables commands need.
- Workflow workers get the RocketClaw env, such as `ROCKETCLAW_CONVERSATION_ID`, of the turn that runs them.
- Keep the wrapper, and any file it reads to configure the sandbox, outside the workspace and outside every path the agent can write. An agent that can modify its wrapper can turn it into plain host bash.
- Do not let agents edit a sandboxed agent's file. Editing it can remove `customShell`, and goal checks reread the file each time they run. `rocketclaw lint` reports `edit` permissions that cover a sandboxed agent's file or a wrapper inside the workspace.
- The wrapper gets the same timeout and termination signals bash would. At the timeout RocketClaw stops the wrapper's process group, and a few seconds later the `bash` call ends even if a program outside that group still holds its output open. Stopping work inside the sandbox is the wrapper's job.
- ``!`…` `` expansion has no timeout and ignores errors. A missing wrapper produces empty text, and a hung wrapper blocks the turn.
- The field applies only to the agent that declares it. A subagent or permission reviewer without its own `shell` runs default bash. Workflow workers use the root agent's `shell`.
- File tools and MCP servers still run on the host. `shell` covers shell commands only.

Deployment-specific model names can be kept out of shared agents:

```json
"models": {
  "coding-high": "software-development-sol"
}
```

```yaml
model: '{{ model "coding-high" }}'
```

Changing `models` requires restart. Agent changes using an existing mapping can use reload.

RocketClaw denies tools by default. `permission` grants are grouped by bucket, and each bucket maps subjects to actions. Later matching rules override earlier matching rules, so put broad allows before narrower denies when subtracting access.

Permission actions:

| Action | Meaning |
| --- | --- |
| `allow` | Run matching tool calls without automatic review. |
| `deny` | Block matching tool calls. Use it after a broader allow to subtract specific subjects. |
| `auto` | Ask the embedded `guardian` automatic reviewer. RocketClaw enables automatic review. Review failure, invalid JSON, or denial blocks the call. |
| `auto(agent-name)` | Ask the named loaded custom reviewer agent. Do not create an agent named `guardian`; that name is reserved. Custom reviewer agents should normally use a `#low` model suffix because each automatic permission review has 90 seconds to return a valid decision. |

`auto` and `auto(agent-name)` review tool execution permission. Finished automatic reviews and Task subagents are saved, and the web chat opens them from the tool row that started them.

Permission buckets:

| Bucket | Subject |
| --- | --- |
| `read` | Workspace-relative paths for nested `read` inside `execute` (not a top-level tool). An `edit` allow also permits reading the same path unless a `read` rule matched first. |
| `glob` | Workspace-relative search paths for nested `glob` inside `execute` (`path`, or `.` when omitted). |
| `grep` | Search patterns for nested `grep` inside `execute`. |
| `webfetch` | URLs for nested `webfetch` inside `execute`. |
| `websearch` | Coarse hosted web search toggle (still a top-level hosted tool), usually `websearch: allow`, `deny`, or `auto`. |
| `edit` | Workspace-relative paths for nested `apply_patch` inside `execute`. |
| `bash` | Every parsed operation inside `execute`, including declarations, redirections, expansions, and nested commands. Allow components separately or match a whole script; whole-script wildcards vary values/arguments without swallowing additional operations. Last matching rule wins per component, for both allow and deny. No implicit equivalence between `export` and `declare -x`. Invalid/unsupported syntax and unresolved executable names deny the entire script. Grants authorize each operation's runtime effects. |
| `skill` | Skill names visible to `find_skills` and loadable by `skill`. |
| `task` | Subagent names visible and callable through `task` only. `maxRecursion` can still hide `task` when the delegation budget is exhausted; it does not gate the nested workflow tool. |
| `workflow` | Workflow stems for `rocketclaw_dynamic_workflow`. |
| `mcp` | Outbound MCP as `server.tool` or wildcards such as `demo.*`. Gates nested MCP builtins inside `execute`. Host grants alone can still surface `execute` without mcp. |
| `rocketclaw` | RocketClaw platform tool names, such as `rocketclaw_store` or `rocketclaw_restart`, the `rocketclaw_sessions` key that covers all five session tools, and the settings `code_mode_approve`, `load_agents_md`, `allow_background`, and `rocketclaw_sessions_recipient`. Only exact names count: a wildcard key such as `"*"` or `rocketclaw_sessions_*`, or the scalar form `rocketclaw: allow`, fails agent loading and asks for each tool by name. A `rocketclaw_set_tag` tag-group list also grants `rocketclaw_get_tags`. |

`rocketclaw_sessions` takes one of these values, and it decides whether the agent gets `rocketclaw_sessions_list`, `rocketclaw_sessions_get`, `rocketclaw_sessions_current_id`, `rocketclaw_sessions_get_url`, and `rocketclaw_sessions_write`:

| Value | Meaning |
| --- | --- |
| `deny` | Default, the same as leaving it out. None of the five tools is offered. |
| `auto` or `auto(name)` | All five, with every call reviewed first by `guardian` or the named reviewer agent. |
| `allow` | All five, without review. |

The reads cover every stored conversation, private ones included. Writes reach any top-level session: any chat Web lists, whatever agent runs it, including Slack threads RocketClaw manages; replies there post to Slack. Writes never reach a Delegation History, a private cron or External MCP conversation, or the caller's own session. Each agent, including a subagent, uses its own setting. The old per-tool keys, such as `rocketclaw_sessions_get: allow` or `rocketclaw_sessions_write: allow`, still load but grant nothing; `rocketclaw_sessions_write: children` fails agent loading.

`rocketclaw_sessions_recipient` is set on the agent being written to, and decides which agents may create, steer, enqueue into, or stop its sessions:

| Value | Meaning |
| --- | --- |
| `true` | Default, the same as leaving it out, except for sudo agents. Any agent may. |
| `false` | No agent may, including another session of the same agent. Default for sudo agents. |
| list of agent names | Only those agents may. The list must not be empty or repeat a name. |

The agent checked is the one that makes the call; for a `task` subagent, that is its own agent, not its parent's. A refused call fails before anything is created, queued, or stopped, with an error naming both agents and the setting. Replies and subagent reports are not writes and are never refused. The setting does not limit reads.

```yaml
permission:
  rocketclaw:
    rocketclaw_sessions: auto
    rocketclaw_sessions_recipient: [main]
```

Run `rocketclaw lint` after agent, skill, or script edits. It checks write-to-execute risk, read-plus-execute leakage, task delegation cycles, delegation-chain escalation, external-content contamination, plural `permissions`, excessive reasoning effort, `edit` permissions that could change a sandboxed agent's file or wrapper, and `permission` entries beside `sudo`.

## Subcommands

Running `rocketclaw` without a subcommand starts the server when `femtoclaw.json` or `rocketclaw.json` is present. If neither config exists, it prints help instead. When both configs exist, `femtoclaw.json` is selected for legacy compatibility.

| Command | Purpose |
| --- | --- |
| `rocketclaw run` | Starts the RocketClaw server/runtime and fails if the selected configuration is missing or invalid. |
| `rocketclaw lint [next|current]` | Checks effective agent-system safety. Defaults to `next`. |
| `rocketclaw agent-graph [next|current]` | Prints the effective task delegation graph as Graphviz/DOT. Defaults to `next`. |
| `rocketclaw oai login [provider] [--headless]` | Authenticates the selected provider and writes its credential to the selected runtime `auth.json`; omission selects `openai`. |
| `rocketclaw oai list` | Lists configured provider auth modes and local credential presence without displaying credentials. |
| `rocketclaw oai logout [provider]` | Removes only the selected provider's local credential; omission selects `openai`. |
| `rocketclaw ts login` | Logs the Web's Tailscale node (`web.tsnet`) in with `web.tsnet.auth_key` or a printed login link, waits until it runs, and exits. Run it with RocketClaw stopped. |
| `rocketclaw help`, `rocketclaw -h`, `rocketclaw --help` | Prints top-level help. |

For `lint` and `agent-graph`, `next` builds a temporary startup-equivalent runtime view from embedded assets, overlays, and workspace files. `current` inspects the selected generated runtime directory as it exists now.
