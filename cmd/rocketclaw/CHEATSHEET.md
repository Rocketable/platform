# RocketClaw Cheatsheet

## Slack Text Controls

Type dollar commands. Emoji in this table are reactions RocketClaw listens to, or markers it posts. They are not typed command prefixes.

| Emoji | Dollar Command | Aliases | Surface | What It Does | Notes |
| --- | --- | --- | --- | --- | --- |
|  | `$goal`, `$ goal` |  | Slack | Starts a text goal loop. | Dollar command names are case-insensitive; goal arguments use the existing grammar. Bare `$goal` posts ephemeral parameter help and examples. |
| `🛑`, `⏹️` | `$stop`, `$ stop` | Slack reactions `:octagonal_sign:`, `:stop_button:` | Slack managed threads | Stops the active managed-conversation turn. | React with 🛑 or ⏹️. Typing those glyphs as a message is not a command. Stop feedback is marker-only: RocketClaw adds `❗` and sends no stop text. Dollar stop takes no arguments. |
| `❗` |  | Slack `:exclamation:` | Slack | Interruption or rejection marker. | Added by RocketClaw after stop/interruption and for duplicate active-goal rejection. Humans generally do not use this as a command. |
| `✅` |  | Slack `:white_check_mark:` | Slack | Completion marker. | Added when a goal reaches `complete`. Not added for `blocked`, `stopped`, or `budget_exhausted`. |
|  | `$cron`, `$ cron` |  | Slack | Runs a one-off cron request. Bare `$cron` lists jobs that target the current channel. | Example: `$cron daily`. |
|  | `$workflow <name> [args]` |  | Slack configured channels | Runs a saved Starlark workflow as the foreground managed turn. | Bare `$workflow` lists names and descriptions. Retry after the active turn finishes. `$stop` ends a running workflow. |
|  | `$agent`, `$ agent` |  | Slack root mentions and managed conversations | Selects the initial root agent or switches the persisted managed-thread agent. | Bare root `$agent` or managed-thread `$agent` opens the selector. Root `$agent name` registers a ready thread; `$agent name message` starts the selected agent with `message` as its first turn. In a managed thread, `$agent name` switches. Only the user who sent the control message can use its selector. |
| `🤖` |  | Slack `:robot_face:` | Slack | Processing/accepted marker. | Added when RocketClaw accepts a Slack-originated or relayed turn; removed after final response delivery. |
| `⏳` |  | Slack `:hourglass_flowing_sand:` | Slack | Steer waiting to inject. | Marks a mid-turn Slack Steer. Removed on injection after a tool batch or a no-tool answer. Multiple waiting steers inject together. 🛑 on that hourglass drops the steer and does not stop the turn. |
| `⏫` |  | Slack reactions `:arrow_double_up:`, `:fast_up_button:`, `:black_up_pointing_double_triangle:` | Slack managed threads | Convert a queued envelope into a Slack Steer. | During an active turn only. Idle or non-envelope ⏫ is ignored. |
| `✉️` | `$enqueue <message>` | Slack `:envelope:` | Slack managed threads | Stash a later turn. | During an active turn, stashes without placeholders. While idle, posts 📨 then starts that turn now. |
| `📨` |  | Slack `:incoming_envelope:` | Slack | Enqueued message is starting. | Posted as the consume-card header before placeholders. |
|  | `$queue` |  | Slack managed threads | Show pending steers, then later work. | Ephemeral jump index. Hide closes it; opening `$queue` again dismisses the previous card. Pending-steer rows jump to the hourglass then hide. A Slack `$enqueue` row jumps to the envelope then hides. Envelope 🛑 cancels that enqueue and does not stop the turn. Scheduled and External MCP rows list with no jump and cannot be cancelled from Slack. |
| `📡` |  | Slack `:satellite_antenna:` | Slack | External MCP relay marker. | Added to Slack relay messages created from External MCP prompts. |

Bare `$` and unknown or unavailable dollar commands post the command table permanently. A root help mention keeps the mention as the thread root and posts help as the first reply without starting an agent turn. Bare root `$agent` is not help: it posts the native agent selector as the first reply.
Agent controls are consumed by RocketClaw and do not route to RocketCode as prompts.

## Slack Message Menu

Register one **On messages** shortcut named `RocketClaw Actions` with callback ID `rocketclaw_actions`. The app needs the `commands` scope; reinstall after adding it. Emoji reactions stay as they are.

Slack shows that shortcut on every message. Choosing it opens a modal whose buttons are computed for that message:

| Button | Applies to | Same as |
| --- | --- | --- |
| Interrupt Turn | thinking or answer placeholder | 🛑 on that placeholder |
| Cancel | waiting ⏳ steer or ✉️ envelope | 🛑 on that message |
| Convert to Steer | ✉️ envelope during an active turn | ⏫ on that envelope |
| Ask Side Question | 💬 answer card | opens the Side Ask form |

Unauthorized clicks are silent. If the message is not in a RocketClaw conversation, the modal says so. If the thread is managed but this message has no live control, the modal says there are no actions on this message.

## Slack Channel Scenarios

| Scenario | How To Trigger | Notes |
| --- | --- | --- |
| Start a conversation | Mention the RocketClaw bot/app in a configured channel. | Starts a fresh managed thread using the first agent in that channel's ordered `agents` list. The mention is the first turn. |
| Hail outside a listed room | Mention the bot in a joined public channel, private channel, or group DM. | Needs an `@` row in `slack.channels`. Uses that row's first agent and allowlist. 1:1 DMs stay ignored. |
| Take over a thread | Mention the bot inside an existing unmanaged thread. | Adopts that thread. Bare `@bot` is enough. Includes the newest 50 prior texts. |
| Start with a selected agent | Mention the RocketClaw bot/app with `$agent agent-name` or `$agent agent-name message`. | The no-message form creates a ready thread for the configured agent; the message form starts that agent with only the remainder as its first user-authored prompt. |
| Continue a conversation | Reply in a known managed thread. | Uses only that thread's persisted history. |
| Message with another human mention | Mention RocketClaw too when the message also pings another person, bot, broadcast target, or user group. | Managed-thread replies that ping someone else are suppressed unless RocketClaw is also mentioned. Raw unresolved `@word` text is not treated as a Slack ping. |
| Agent selection or switch | Root `$agent [agent-name] [message]`, or `$agent [agent-name]` in a managed thread. | Bare `$agent` opens a Slack-native selector in either context. Root named selection uses a configured single-token agent name; the optional message starts its first turn. In a managed thread, the named form switches the persisted agent. |
| One-off cron | `$cron daily` or `$cron daily.md`. | Any top-level cronjob can be started from any configured Slack channel. Bare `$cron` lists jobs that target the current channel. |
| Saved workflow | `$workflow audit-routes src/routes`. | Works in an existing managed thread or in an authorized root app mention that creates one. Bare `$workflow` lists available workflows. If a turn is active, wait and retry. A nonempty later-work queue is not busy. `$stop` terminates the run. |
| Stash later work | `$enqueue write the changelog`. | During an active turn, marks ✉️ and stashes a separate later turn. While idle, posts 📨 then starts that message now even if the stack is already nonempty. Bare `$enqueue` posts command help. |
| Review later work | `$queue`. | Posts pending steers, then later work. Hide closes it. Jump to a pending steer or Slack enqueue. ⏫ on an envelope during a turn converts it to a steer. 🛑 on a waiting hourglass drops that steer. Envelope 🛑 drops that enqueue. |
| External MCP conversation | Call `session_prompt` with an external conversation ID, agent, and configured channel. | The ID owns one private MCP session and one managed Slack session on the same thread. The MCP agent stays fixed. MCP history copies into managed history; Slack history does not copy back. |

## Development MCP

A separate inbound door from External MCP. Off until `mcp_development.enabled` is true. Own users file: `rocketclaw.development.users.json` (copy `internal/rocketclaw/rocketclaw.development.users.example.json`, mode 0600). Not `rocketclaw.users.json`. Tools: `rocketclaw_development_list_overlay`, `rocketclaw_development_read_context_from_overlay`, `rocketclaw_development_lint`, `rocketclaw_development_run_turn`, `rocketclaw_development_reload`, `rocketclaw_development_restart`, `rocketclaw_development_list_session`, `rocketclaw_development_observe_session`, `rocketclaw_development_delete_session`. `lint` and `run_turn` take a request `context` (optional `base_overlay` plus file deltas). Chat stays on this door. `list_session`, `observe_session`, and `delete_session` inspect or delete durable Slack/exec/External MCP stored turns, not try-turn chats, and take no overlay context. Observe is a snapshot and may be large. Delete removes stored turns only, with no confirm. When this door is off, those calls do not exist.

## Goal Examples

| Example | Meaning |
| --- | --- |
| `$goal ship the release` | Starts a goal loop with default `maxTurns: 5`. |
| `$goal maxTurns: 10 ship the release` | Starts a finite goal loop with a 10-turn budget. |
| `$goal maxTurns:10 ship the release` | Same as above; goal parameters may attach values directly after `:`. |
| `$goal checkScript: ./scripts/check.sh ship the release` | Starts a goal loop that must pass the workspace-local check script before `complete` can stick. |
| `$goal checkScript:./scripts/check.sh ship the release` | Same as above; `checkScript:` values may attach directly after `:`. |
| `$goal checkScript: "./scripts/check.sh --full" ship the release` | Uses a quoted simple command for the check script. |
| `$goal checkScript:"./scripts/check.sh --full" ship the release` | Same as above with the quoted command attached directly after `:`. |
| `$stop` | Stops the active managed-conversation turn. If an active goal is present, it becomes `stopped`. Reacting with 🛑 or ⏹️ on thinking does the same. |
| `✅` | Marker RocketClaw adds when a goal reaches `complete`. Humans generally do not send it as a command. |

| Goal Parameter | Accepted Values | Meaning |
| --- | --- | --- |
| `maxTurns:` | Omitted | Defaults to `5`. |
| `maxTurns:` | Positive integer | Finite goal budget. Progress shows `_Pursuing Goal (n/m)..._`. |
| `maxTurns:` | `0`, `-1`, `infinite` | Infinite goal budget. Progress shows `_Pursuing Goal..._`. |
| `checkScript:` | Workspace-local safe simple command | Runs when the model calls `rocketclaw_update_goal` with `complete`; failure keeps the goal active. |

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

Return the human-visible value directly from `main`: strings render directly, other JSON-compatible values render as JSON, and only `None` or `""` is silent. Parallel workers share one checkout; assign disjoint file ownership and integrate shared files sequentially. Workflow progress uses Slack plan/task cards, but intermediate values do not enter managed history. `$stop` is terminal, the state store records no resumable workflow progress, and daemon restart requires reinvocation.

`workflow_button` belongs to Slack Workflow Builder. RocketClaw saved workflows do not use it.

## Channel Configuration Example

```json
"slack": {
  "bot_token": "xoxb-...",
  "app_token": "xapp-...",
  "channels": [
    {
      "channel": "#ops",
      "agents": ["main", "factory", "slack-to-benchmark"],
      "allowed_user_ids": ["U0123456789"]
    }
  ]
}
```

New `#ops` conversations use agent `main`. Authorized replies can select another listed agent with `$agent factory` or the native selector.

To expose the shipped quickbench capture agent, include `slack-to-benchmark` in that channel's `agents` list (skel ships the agent file; channel membership is required). Example: `$agent slack-to-benchmark capture this thread into a BAR`. Restart after editing `rocketclaw.json`. See `cmd/quickbench/README.md`.

## Outbound MCP Servers

`mcp_servers` is distinct from inbound `mcp_external`. Server names use the same character set as MCP tool names in the official Go SDK: non-empty, at most 128 characters, and only letters, digits, `_`, `-`, and `.` (for example `sequential-thinking`, `my_server.v2`). Each entry is either stdio (`command` plus optional `args`, `env`, `cwd`) or streamable HTTP (`url` plus optional static `headers`). Set exactly one of `command` or `url`. Static headers and env are fine; OAuth is not supported. Changing `mcp_servers` requires restart. RocketClaw connects for each `execute` call and closes afterward. Starlark builtins sanitize `-`/`.` in server and tool names to `_` (for example `sequential-thinking.foo-bar` → `sequential_thinking_foo_bar`).

```json
"mcp_servers": {
  "demo": { "command": "my-mcp", "args": [], "env": {} },
  "acme": { "url": "http://127.0.0.1:9090/mcp", "headers": { "Authorization": "Bearer …" } }
}
```

Agents use filesystem, shell, fetch, patch, and outbound MCP through the Code Mode `execute` tool (not as separate top-level tools). After the right permission grants, call `execute` with a short Starlark script (`def main()`). Inside the script: host builtins `read(...)`, `apply_patch(...)`, `glob(...)`, `grep(...)`, `webfetch(...)`, `bash(command=r'''…''')` with their normal permission buckets and subjects (including multi-subject `bash`); RocketClaw platform tools such as `ask_user_question(...)`, `rocketclaw_reload(...)`, `rocketclaw_update_goal(...)` (also still available top-level when eligible); MCP as `server_toolname(...)` after `permission.mcp` (permission subjects use raw `server.tool`). execute `code` is a JSON string (JSON still uses `"..."`). Inside it, `bash(command=...)` takes `r'''...'''` only, not Starlark `"..."` or `'...'`. Example: `{"code":"def main():\n    return bash(command=r'''grep -n architecture\\|loop FILE''')\n"}`. `r"..."` is raw but single-line; a real newline needs `r'''...'''`. `$` is valid inside a closed Starlark string. The script is parsed before host tools run; a `codemode.star` error means nothing ran. `bash(...)` results are text-like (`str(result)` before find/split). Discover tools with in-script `search(query="", namespace="", offset=0, limit=10)` (JSON items with path/description/signature/callable). A short Code Mode catalog is also in the system prompt. Skills, `task`, hosted `websearch`, and RocketClaw platform tools stay top-level when eligible.

Concurrent tool calls inside one script use `gather`, `map`, `race`, and `race_first` (default `concurrency=16`, max 64). Example: `gather([lambda: read(filePath="a"), lambda: read(filePath="b")])` runs both reads together and returns ordered results; one failure cancels siblings. `map(paths, lambda p: read(filePath=p), concurrency=8)` maps over items. `race` keeps the first success; `race_first` keeps the first finish (success or error). Nested fan-out is allowed. These names also appear in the system-prompt Code Mode section and in `search`.

## Current State Schema

Set `database_url` on the selected config file. Fresh stores apply embedded SQL migrations. Isolation is one DSN per workspace.

## Model Providers And Credentials

`openai` is the default provider; named entries live under `providers`. An unqualified model uses `openai`, `openai/gpt-5.5` explicitly selects the default, and `work/gpt-5.5` selects `providers.work`. Root and child agents resolve their models independently, with no implicit provider failover. Provider credentials are isolated in the selected workspace and runtime directory. Optional `autocompaction_threshold` on `openai` or a named provider overrides that provider's compaction token threshold; omit it to keep the 200000 default.

| Command | Purpose |
| --- | --- |
| `rocketclaw oai login [provider] [--headless]` | Acquire and save the selected provider's ChatGPT credential; omission selects `openai`. |
| `rocketclaw oai list` | Show provider, default marker, configured auth mode, and local credential presence. |
| `rocketclaw oai logout [provider]` | Remove only the selected local credential; it does not revoke a remote token. |

## RocketClaw Tools

RocketClaw injects these tools into RocketCode turns as **top-level tools and Code Mode builtins inside `execute`**. Call them directly or from Starlark by name, e.g. `ask_user_question(question="…")` or `rocketclaw_update_goal(status="progress", note="…")`. Most are auto-allowed by RocketClaw unless a per-agent permission rule explicitly denies them. `rocketclaw_restart` and `rocketclaw_start_new_thread` are default-deny and require an explicit per-agent `allow`. `rocketclaw_dynamic_workflow` is not RocketClaw auto-allowed; it is gated by `permission.workflow.<stem>`, not by `task`. Workflow workers still do not receive these platform tools.

| Tool | Available In | Permission Default | What It Does |
| --- | --- | --- | --- |
| `rocketclaw_dynamic_workflow` | Persistent parent managed turns when at least one loaded workflow stem is `workflow`-allowed. Never on workflow workers, and not via the RocketClaw auto-allow list. | Gated by `permission.workflow.<stem>`. Not gated by `task`. Not RocketClaw auto-allowed. | Runs a saved Starlark workflow as a nested tool call inside the current turn, streams phase/agent progress into thinking, and returns the final workflow text as the tool result. Not a second managed turn; no human `$workflow` session summary entry. |
| `rocketclaw_restart` | Persistent bridge turns and raw/cron runs. | Default-deny. Requires explicit per-agent `allow`; generated `main` agents include that allow. | Records the restart requester and cancels RocketClaw for supervisor restart after approved runtime config or overlay-list changes. |
| `rocketclaw_schedule_message` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Schedules a one-shot or recurring prompt in the current conversation. Recurring schedules persist until reset and do not replay missed intervals. |
| `rocketclaw_reset_scheduled_messages` | Persistent bridge turns and raw/cron runs. | Treat as part of the schedule-message permission family. Deny `rocketclaw_schedule_message` to block schedule reset behavior. | Clears scheduled messages for the current conversation. |
| `rocketclaw_attach_files_to_response` | Persistent bridge turns and raw/cron runs. | Auto-allow unless explicitly denied. | Attaches collected files to the final outbound response through RocketClaw's shared response-attachment path. |
| `rocketclaw_update_goal` | Persistent bridge turns only, and only when the current text conversation has an active goal. | Auto-allow unless explicitly denied, but hidden when no active goal exists. | Reports goal status as `progress`, `complete`, or `blocked` with an optional note. `complete` runs any configured `checkScript:` before the goal becomes complete. |
| `rocketclaw_start_new_thread` | Qualifying human-originated managed Slack turns, and cron raw fires (scheduled and on-demand `$cron`) when the cron agent explicitly allows it. | Default-deny. Requires explicit per-agent `allow`; missing, `auto`, or `deny` keeps it unavailable. | Creates a fresh managed conversation in the same configured channel and submits the literal tool prompt as its first turn. Scheduled and on-demand cron share this tool and lock the channel and agent to the job. It is never exposed for MCP, `rocketclaw exec`, system turns without a Slack channel, or automatic goal continuation turns. |
| `rocketclaw_i_want_human_partner_to_see_this` | Raw/cron runs only. | Auto-allow in raw/cron tool mode unless explicitly denied by the cron agent. | Required raw-run completion tool. Its argument is the exact human-visible output, or an empty string for silence. |
| `ask_user_question` | Qualifying human-originated Slack turns with a native answer path. | Auto-allow unless explicitly denied, but hidden when the turn has no answer path. | Asks the originating human through native UI, blocks until answered or canceled, and returns selected options and/or custom text. Not exposed for cron/raw, MCP, scheduled/system/automation, automatic goal continuations, or restart recovery continuations. |
| `execute` | Shown when the agent has MCP grants, code-mode host grants (`read`/`edit`/`bash`/…), and/or RocketClaw platform-tool grants. The only model entry for FS/shell/fetch/patch/MCP; platform tools remain top-level too. | Entry gate uses mcp subjects when present, otherwise a matching host or platform-tool permission subject. Nested calls re-check real buckets (read/edit/bash/mcp/rocketclaw/…). Not RocketClaw auto-allowed. | Code Mode: runs a short Starlark script with required `code`. Define `def main()`; call host tools as `read(filePath="…")` / `bash(command=r'''…''')`, platform tools as `ask_user_question(question="…")` / `rocketclaw_reload(reason="…")`, MCP as `server_toolname(**kwargs)`, concurrency via `gather`/`map`/`race`/`race_first` (default concurrency 16, max 64), and `search(query="", namespace="", offset=0, limit=10)` to discover tools and concurrency builtins. `bash(command=...)` takes `r'''...'''` only inside the JSON `code` string. Example: `{"code":"def main():\n    return bash(command=r'''grep -n architecture\\|loop FILE''')\n"}`. Parsed before host tools; failed wrappers are not evidence. Example: `def main():\n  return gather([lambda: read(filePath="a"), lambda: read(filePath="b")])`. Connects MCP per call, then closes. |

For general `permission` syntax, action values, guardrails, and approval reviewers, see Agent Frontmatter And Permissions below. RocketCode is deny-by-default and later matching rules win. RocketClaw's default tool allows are injected after agent permissions unless an explicit deny already matched, so use `deny` rather than `auto` when the intent is to block or force review of a RocketClaw auto-allowed tool. For default-deny tools such as `rocketclaw_restart` and `rocketclaw_start_new_thread`, `auto` is not enough; use explicit `allow`. Outbound MCP tools are never RocketClaw auto-allowed; grant `permission.mcp` explicitly.

## Slack Reactions And Markers

| Emoji | Reaction Name | Meaning |
| --- | --- | --- |
| `🛑` | `octagonal_sign` | Stop reaction on thinking, a waiting hourglass, or a queued envelope. |
| `⏹️` | `stop_button` | Same as 🛑. |
| `❗` | `exclamation` | Interruption or rejection marker. |
| `✅` | `white_check_mark` | Completion marker. |
| `🏁` |  | Goal header RocketClaw posts while a goal is running. |
| `🔁` |  | Cron result header RocketClaw posts. |
| `🤖` | `robot_face` | Slack processing/accepted marker. |
| `⏳` | `hourglass_flowing_sand` | Slack Steer waiting to inject. |
| `✉️` | `envelope` | Waiting Enqueued Slack Message. |
| `📨` |  | Enqueued Slack Message consume card. |
| `📡` | `satellite_antenna` | Slack External MCP relay marker. |
| `⏫` | `arrow_double_up`, `fast_up_button`, `black_up_pointing_double_triangle` | Convert a queued envelope into a Slack Steer. |

## Agent Frontmatter And Permissions

Agents are top-level Markdown files in `agents/*.md`. The filename without `.md` is the agent name. YAML frontmatter is required, followed by the agent prompt body.

```yaml
---
description: Short human-readable purpose
model: gpt-5.5
reasoningEffort: high
verbosity: medium
maxRecursion: 2
guardrail: safety-agent
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
| `model` | Required concrete model or `{{ model "name" }}` placeholder configured in `models`. |
| `reasoningEffort` | Optional model reasoning effort. Avoid `xhigh`; `rocketclaw lint` reports it. |
| `verbosity` | Optional model verbosity. |
| `maxRecursion` | Optional task delegation depth for inferences started with this agent. Omitted or `-1` is unlimited, `0` disables `task`, and positive integers allow that many levels. |
| `guardrail` | Optional loaded agent name that gates task delegations to this agent before the child runs and after its response. The guardrail must approve or reject with strict JSON containing `approved` and `reason`; it does not transform the delegated prompt or child response. |
| `additionalInstructions` | Optional RocketClaw normal-reply prompt-header override for this selected agent. Use it for response-format guidance, such as telling an agent to avoid Markdown for Slack, prefer Markdown for technical answers, keep answers terse, or follow another surface-specific style. When omitted, RocketClaw uses `Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.` It does not affect internal notes or raw cron runs. |
| `schema.output` | Optional JSON Schema mapping. When set, that agent's own turns (including `task` children) use OpenAI structured output. Guardrail and permission-review child runs keep their fixed decision schemas. Workflow `agent(..., schema=...)` still overrides for that call. Object schemas are sent with `additionalProperties: false`; list every property in `required`. |
| `permission` | Optional singular permission map. Omit it when the agent needs no tools. Do not use plural `permissions`; RocketCode ignores it and `rocketclaw lint` reports it. |

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

RocketCode denies tools by default. `permission` grants are grouped by bucket, and each bucket maps subjects to actions. Later matching rules override earlier matching rules, so put broad allows before narrower denies when subtracting access.

Permission actions:

| Action | Meaning |
| --- | --- |
| `allow` | Run matching tool calls without automatic review. |
| `deny` | Block matching tool calls. Use it after a broader allow to subtract specific subjects. |
| `auto` | Ask RocketCode's embedded `guardian` automatic reviewer. RocketClaw enables automatic review. Review failure, invalid JSON, or denial blocks the call. |
| `auto(agent-name)` | Ask the named loaded custom reviewer agent. Do not create an agent named `guardian`; that name is reserved. Custom reviewer agents should normally set `reasoningEffort: low` because each automatic permission review has 90 seconds to return a valid decision. |

Guardrails and approval reviewers are separate mechanisms. `guardrail` reviews inter-agent delegation and child responses; `auto` and `auto(agent-name)` review tool execution permission.

Permission buckets:

| Bucket | Subject |
| --- | --- |
| `read` | Workspace-relative paths for nested `read` inside `execute` (not a top-level tool). An `edit` allow also permits reading the same path unless a `read` rule matched first. |
| `glob` | Workspace-relative search paths for nested `glob` inside `execute` (`path`, or `.` when omitted). |
| `grep` | Search patterns for nested `grep` inside `execute`. |
| `webfetch` | URLs for nested `webfetch` inside `execute`. |
| `websearch` | Coarse hosted web search toggle (still a top-level hosted tool), usually `websearch: allow`, `deny`, or `auto`. |
| `edit` | Workspace-relative paths for nested `apply_patch` inside `execute`. |
| `bash` | Parsed shell call expressions for nested `bash` inside `execute`. Multi-command scripts need every parsed call allowed. |
| `skill` | Skill names visible to `find_skills` and loadable by `skill`. |
| `task` | Subagent names visible and callable through `task` only. `maxRecursion` can still hide `task` when the delegation budget is exhausted; it does not gate the nested workflow tool. |
| `workflow` | Workflow stems for `rocketclaw_dynamic_workflow`. |
| `mcp` | Outbound MCP as `server.tool` or wildcards such as `demo.*`. Gates nested MCP builtins inside `execute`. Host grants alone can still surface `execute` without mcp. |

Run `rocketclaw lint` after agent, skill, or script edits. It checks write-to-execute risk, read-plus-execute leakage, task delegation cycles, delegation-chain escalation, external-content contamination, plural `permissions`, missing guardrails, and excessive `reasoningEffort`.

## Subcommands

Running `rocketclaw` without a subcommand starts the server when `femtoclaw.json` or `rocketclaw.json` is present. If neither config exists, it prints help instead. When both configs exist, `femtoclaw.json` is selected for legacy compatibility.

| Command | Purpose |
| --- | --- |
| `rocketclaw run` | Starts the RocketClaw server/runtime and fails if the selected configuration is missing or invalid. |
| `rocketclaw exec [--timeout <duration>] <agent> <prompt>` | Run one agent once, non-interactively, and print the run as JSONL. |
| `rocketclaw setup` | Interactively creates or updates `rocketclaw.json`, root setup files, workspace overlays, and `.rocketclaw/`. |
| `rocketclaw setup files list` | Lists embedded setup payload files. |
| `rocketclaw setup files get <path>` | Prints one embedded setup payload file. |
| `rocketclaw doctor` | Validates the loaded configuration and RocketCode availability. |
| `rocketclaw lint [next|current]` | Checks effective RocketCode agent-system safety. Defaults to `next`. |
| `rocketclaw agent-graph [next|current]` | Prints the effective RocketCode task delegation and guardrail graph as Graphviz/DOT. Defaults to `next`. |
| `rocketclaw oai login [provider] [--headless]` | Authenticates the selected provider and writes its credential to the selected runtime `auth.json`; omission selects `openai`. |
| `rocketclaw oai list` | Lists configured provider auth modes and local credential presence without displaying credentials. |
| `rocketclaw oai logout [provider]` | Removes only the selected provider's local credential; omission selects `openai`. |
| `rocketclaw help`, `rocketclaw -h`, `rocketclaw --help` | Prints top-level help. |

For `lint` and `agent-graph`, `next` builds a temporary startup-equivalent runtime view from embedded assets, overlays, and workspace files. `current` inspects the selected generated runtime directory as it exists now.
