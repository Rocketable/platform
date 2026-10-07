# Rocketable Platform

Rocketable Platform is Rocketable's workspace-local AI agent runtime. It turns a repository or working directory into an agent environment where humans can interact through Slack or an external MCP endpoint, while agents operate through controlled access to local files, shell commands, tools, skills, saved Starlark workflows, attachments, and connected services.

The platform is written in Go and is oriented around internal, workspace-local deployment rather than hosted multi-tenant SaaS.

## Experimental Software

Rocketable Platform is experimental software. It can (and will) run model-generated actions against local files, shell commands, connected services, and team communication channels, so users are responsible for reviewing configuration, permissions, outputs, and integrations before relying on it in any sensitive or production environment.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## Core Capabilities

- Run workspace-aware AI agents with local instructions, agent definitions, skills, attachments, subagents, custom tools, file access, shell commands, web fetches, and explicit permission rules.
- Keep agent work durable through PostgreSQL-backed sessions, a per-conversation step journal, connector routing, scheduled messages, and conversation-local goal loops, so a restart resumes in-progress work without repeating finished steps.
- Connect agents to team workflows through Slack, cron jobs, scheduled prompts, and an external MCP HTTP endpoint.
- Run checked-in Starlark workflows as foreground managed turns with isolated custom workers and Slack phase and worker activity progress.
- Route model requests through independently configured default and named OpenAI-compatible providers while preserving one local agent/tool model.
- Expose optional OpenTelemetry/OpenInference-compatible tracing for agent runs.

## Main Components

### RocketCode

`internal/rocketcode` is the core reasoning runtime. It builds model requests from workspace context, runs the tool loop, enforces permissions, handles supported image and PDF attachments, and records replayable session entries. Hosts embed it through `New` / `NewWithProviders` and drive turns with `Loop`.

The agent-facing `bash` tool checks every parsed operation before any part runs: commands, declarations, assignments, redirections, expansions, compound commands and their children, and background execution. Grant operations separately or grant a whole script. Rules are applied in declaration order: the last matching component or whole-script rule wins for each operation, and every operation must end up allowed. `export FOO=bar` does not grant `declare -x FOO=bar`. A wildcard command rule such as `scripts/cmd *` does not grant a later `export` or a redirection.

For example, `export FOO=bar; scripts/cmd arg` can use separate `"export FOO=bar": allow` and `"scripts/cmd arg": allow` rules, or one `"export FOO=*; scripts/cmd *": allow` rule under `bash`. Whole-script wildcards match values and arguments within the same parsed operation tree; they cannot swallow additional commands, redirections, or substitutions. This applies to both `allow` and `deny`. Component subjects use the shell printer's spelling (for example `>output` for a redirection); background and negation use `&` and `!`. Parse failures, unsupported syntax, CR/NUL bytes, and unresolved executable names such as `$COMMAND` deny the entire script even with `bash: allow`.

Permissions authorize the written operations, including their runtime effects. Allowing an expansion, arithmetic expression, declaration, program, or interpreter is not a restriction on the values it can produce or the internal work it can perform. Nested commands present in the submitted syntax are still checked separately.

The default shell runner requires `/bin/bash` and ignores `$SHELL`. It disables startup files and inherited shell functions/options so execution matches the permission parser. This runner also executes enabled prompt shell snippets.

For external MCP conversations, guardrails and automatic permission reviewers receive the same thread and turn-only metadata developer messages as the main agent. Their shell tools share the turn's environment, while their own permissions still control tool access.

Permission matching preserves backslashes as literal characters; it does not treat them as `/`. This follows Unix path semantics and applies to both allow and deny rules.

RocketCode's default model is `gpt-6-luna`. The built-in automatic permission reviewer uses this runtime default unless `auto_approver_model` is configured. Guardrails and named reviewers use their own agent models.

Root `AGENTS.md` instructions are included in each agent's system prompt by default.
To omit them for one agent, set this boolean in its Markdown frontmatter:

```yaml
permission:
  rocketclaw:
    load_agents_md: false
```

Omitted or `true` keeps the default. Each child, guardrail, and named permission
reviewer uses its own setting; `rocketclaw` wildcard rules do not change it.
Workspace context and normal file-read permissions are unchanged. This switch
does not remove instructions already present in conversation history.

### RocketClaw

`internal/rocketclaw` is the long-running service runtime around RocketCode. It provides thread-local conversations in configured Slack channels, saved Starlark workflows, external MCP, cron-defined background prompts, one-shot and recurring scheduled messages, inbound and outbound attachments, supervisor restart, and PostgreSQL state selected by `database_url`.

The runnable entry point is `cmd/rocketclaw`. Run `rocketclaw help` for validation and operational commands.

RocketClaw embeds its Web interface, including when run directly from the Go module:

```sh
go run github.com/Rocketable/platform/cmd/rocketclaw@main
```

Web always starts at `0.0.0.0:3000`. To change its bind address, set
`"web": {"listen_address": "127.0.0.1:3000"}` in `rocketclaw.json` or `femtoclaw.json`.
Links RocketClaw hands out, such as the session URL `rocketclaw_start_new_thread`
returns, use this machine's Tailscale IPv4 (`tailscale ip -4`) and the port from that
address: `http://<tailscale-ip>:<port>`.
No Web environment variables or socket setup are required. Browser access uses
the configured `web_users` IP-to-username mapping, or Tailscale WhoIs when the IP has no mapping.
New human messages use the configured name or Tailscale display name as their author;
admission, ownership, and routing still use the login name. Missing or blank display
names fall back to the login. Web shows each recorded author above the message text
and keeps the exact prompt header in its info details.

Optional frontend error and performance tracking uses `web.sentry` in the same
config file. Set a public Sentry DSN to report JS/TS and React errors,
`console.error`, handled request failures, page/request timings, click
responsiveness, long tasks, and Web Vitals without session replay. It is disabled
by default. Errors are not sampled; performance traces default to 10% sampling.
See [frontend error and performance tracking](internal/rocketclaw/web/README.md#frontend-error-and-performance-tracking)
for the settings, source-map uploads, and data-collection details.

Web reads the same persisted transcript during a turn and after a refresh,
including recorded reasoning summaries and tool calls/results. Its live connection
carries only conversation-change signals; each signal triggers a delta read that
replaces changed entries and removes deleted ones. Reconnecting catches up from
the last applied revision. Private producer output stays private until history sync
commits; encrypted or unrecorded provider output is not reconstructed.

Slack shows one in-progress placeholder, then the final answer and attachments.
It does not stream partial answers, reasoning, tools, or workflow progress.
Final Slack delivery acknowledgement still controls queue progression independently
of Web reads.

The compiled SPA in `internal/rocketclaw/internal/web/dist/` is committed with its
frontend source changes, so Go-only builds need no Bun installation. When building
from a checkout, `make -C internal/rocketclaw build` (also invoked by root
`make build`) installs the locked frontend dependencies and rebuilds the SPA with
Bun before compiling `bin/rocketclaw`. Commit the rebuilt `dist/` files alongside
changes in `internal/rocketclaw/web/`.

#### Health logs and private profiling

At INFO level, `rocketclaw health` reports once a minute after the database pool
opens, including while waiting for the run lock. `process_start` identifies one
process lifetime; startup logs include the Go version and available embedded
`build_revision`/`build_dirty` metadata. A missing revision is not a known commit.

| Field | Meaning |
| --- | --- |
| `sample_interval_seconds` | Actual elapsed interval used for counter deltas/rates |
| `go_heap_objects_bytes` | Go heap object memory, **not** process RSS |
| `go_alloc_bytes_per_second` | Allocated bytes per second over that interval |
| `go_gc_cpu_seconds_interval` | Estimated GC CPU-seconds, **not** wall-clock pause time |
| `go_goroutines` | Current goroutine count |
| `go_scheduler_observations_interval`, `go_scheduler_p99_upper_seconds` | New scheduling-delay observations and approximate p99 bucket upper bound; no p99 when there are no new observations |
| `db_pool_in_use`, `db_pool_idle`, `db_pool_open` | Database connection counts |
| `db_pool_wait_count_interval`, `db_pool_wait_duration_ms_interval` | Pool-connection waits, **not** SQL query durations |

Provider events distinguish physical HTTP attempts (including retries) from
Codex SDK-envelope returns: do not count both as separate network attempts.
HTTP return timing is not full generation time. Tool/progress observation elapsed
times describe when RocketClaw observed a state, not exact tool execution duration.
Use existing conversation, turn, and operation IDs to follow work; these scalar
logs do not reconstruct historical profiles or traces.

Start with `rocketclaw run --pprof` to enable Go profiling at the fixed
`127.0.0.1:6060` address. It is off by default, is not saved in config, and is
separate from public Web. An occupied port fails startup; a later serving failure
is logged without stopping agent work or retrying. All standard pprof endpoints
are available, including cmdline, symbol, block, mutex, CPU profiles, and traces.
Block and mutex profiling still require enabling collection separately.
The diagnostic server does not serve gRPC debug requests or events.

For an authorized remote inspection, keep forwarding private:

```sh
ssh -N -L 127.0.0.1:6060:127.0.0.1:6060 trusted-host
```

On the requester's machine, run short captures **one at a time** and keep downloads
private under that repository's `.tmp/` (never publish them):

```sh
mkdir -p .tmp/private-profiles
chmod 700 .tmp/private-profiles
umask 077
export TMPDIR="$PWD/.tmp/private-profiles" PPROF_TMPDIR="$PWD/.tmp/private-profiles"
curl --fail -o .tmp/private-profiles/heap.pprof 'http://127.0.0.1:6060/debug/pprof/heap'
curl --fail -o .tmp/private-profiles/cpu.pprof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'
curl --fail -o .tmp/private-profiles/trace.out 'http://127.0.0.1:6060/debug/pprof/trace?seconds=5'
go tool pprof .tmp/private-profiles/heap.pprof
go tool pprof .tmp/private-profiles/cpu.pprof
go tool trace -http=127.0.0.1:0 .tmp/private-profiles/trace.out
```

RocketClaw streams diagnostics over HTTP and writes no diagnostic files. Profiles
and especially traces add temporary CPU/memory overhead and can reveal private
runtime details. Loopback is not authentication: local processes can access this
port. Do not expose it through public listeners, proxies, or tunnels. CPU and
trace capture use Go's native durations, conflicts, and request cancellation;
there are no application recording limits or always-on CPU/block/mutex captures.

Web shows Slack user and user-group tags by name and searches messages by those names; this requires the bot scopes `users:read` and `usergroups:read`; reinstall the Slack app after adding them. Names refresh every 8 hours. Without a scope, the affected tags show their Slack ID.

Slack native forwarded-thread expansion requires the bot scopes `channels:read` and `channels:history`; reinstall the Slack app after adding scopes. See `cmd/rocketclaw/CHEATSHEET.md`. RocketClaw expands only source channels Slack confirms are public and that the bot can already read. It never auto-joins a channel. Private, inaccessible, malformed, or partially unreadable source threads retain only Slack's forwarded preview.

Slack configuration uses direct `slack.channels` mappings. Each mapping names a channel, an ordered non-empty `agents` list, and its authorized `allowed_user_ids`. An ordinary authorized app mention in a configured channel starts a fresh managed thread whose initiating message is its first turn. An `@` channel row is not a Slack channel; it supplies agents and an allowlist for hails in any other joined public channel, private channel, or group DM. A hail in an unmanaged thread takes that thread over and includes prior messages. 1:1 DMs never start this way. The bot still never auto-joins. A root `$agent` mention opens the native agent selector; selecting an agent registers a ready thread for that agent so the next human reply is the first turn. A root `$agent <name>` mention can also select a configured agent directly: without a message it registers a ready thread, while a following message starts the selected agent with that message as its first turn. A command-help mention is another exception: RocketClaw posts permanent help as the first thread reply without adding either message to agent history. Later replies use only that thread's persisted history.

External MCP exposes `session_prompt`. Every call supplies an external conversation ID, agent, and configured Slack channel. A new ID creates one private MCP session and one managed Slack session on the same Slack thread. The MCP agent remains fixed; the managed agent starts from the channel configuration and can be switched from Slack. MCP history is copied into managed history, but Slack history is never copied back. Later calls keep the same channel and Slack thread. Slack Blocks label MCP requests and responses with their conversation ID and agent.

Origin display and search read the first call's extra details from the external session record, not from tool-output history. Later calls do not replace these original details. Metadata prompts sent to the model remain in message history.

Permission patterns can use `${ROCKETCLAW_*}` variables from the run's shell
environment, for example `edit: {'.tmp/${ROCKETCLAW_METADATA_FRUIT}/*': allow}`.
Each agent resolves its patterns when it starts, including delegated agents,
guardrails, and permission reviewers. Missing or empty variables fail that start.
Inserted values are literal; only wildcards written in the pattern grant wildcard
access. Paired threads retain their stored metadata when continued from the web.

Every active `cron/*.md` definition declares a quoted `channel` that matches a configured Slack channel. The cron agent's reply is its output: an empty reply is silent; a non-empty reply starts a fresh managed thread in that channel.

Agents call every RocketClaw platform tool inside Execute; the model's top-level tools are only `execute`, `load_execute_result`, `task`, `skill`, and `find_skills` (plus `websearch` when allowed, which the provider runs). Each call made inside an Execute script is saved with the turn and shown in Web history, `rocketclaw_get_session`, and reply attachments like any other tool call.

Scheduled follow-ups run in the destination conversation with its history and
selected agent, not in the private cron or External MCP run. A silent cron with
no destination creates or reuses a web-only conversation when its first pending
follow-up becomes due. Later follow-ups reuse it, including after a restart;
they do not post to Slack. Silence applies to the original report, not to future
scheduled work. Opening a cron's history in Web does not claim its follow-ups or
rearm schedules that have already run.
If creating the destination or copying pending work fails temporarily, RocketClaw
keeps that work saved and tries again after one second while the conversation worker runs.

#### Agent session inspection

Agents can inspect durable conversations with `rocketclaw_current_session_id`,
`rocketclaw_list_sessions`, and `rocketclaw_get_session`, inside Execute only.
Grant them in the agent's frontmatter; none is allowed by default:

```yaml
permission:
  rocketclaw:
    rocketclaw_current_session_id: allow
    rocketclaw_list_sessions: allow
    rocketclaw_get_session: allow
```

Use `deny` to block a tool, or `auto` for the existing approval flow. List and get
permissions grant access across the selected State Store, including Slack, exec,
cron, and private External MCP histories—not just the agent's current conversation.

To look back past compaction, call `rocketclaw_current_session_id()` with no
arguments in an Execute script. It returns only the bare conversation ID, with
no JSON wrapper or added newline. Pass that ID to `rocketclaw_get_session(conversation_id="...")`
to read stored entries, including those before the compaction point.
This is the owning bridge's durable conversation ID: a private External MCP bridge
returns its private history ID, not its public external ID or managed destination,
even though its session tag tools act on that destination.
Child agents that inherit these tools return the same owning conversation ID,
subject to their own permissions; they do not get a separate child-run history ID.

List requires all four fields: `since` (Go duration relative to now, such as `24h`,
or RFC3339Nano), `until` (RFC3339Nano), `limit`, and `include_message_preview`.
Time bounds use each conversation's maximum entry timestamp: `since` is inclusive
and `until` exclusive. Use empty strings for no time bounds and `limit: 0` for
unlimited results. Zero and negative durations retain their normal relative-time
meaning; negative limits are errors. Set `include_message_preview` explicitly to
true or false. All three tools use the existing strict provider schemas.

Without bounds or a positive limit, results sort by conversation ID. Otherwise
they sort newest first, then by ID. List returns TSV with a header and one session
per row. Columns are `conversation_id`, `turns`
(stored entry count), and `last_updated` (the last entry's timestamp in entry-ID
order), plus `last_user_message` and `last_assistant_message` previews, which may
be empty. Both preview columns are omitted when disabled. No matches returns only
the header.

For example, call `rocketclaw_list_sessions(since="24h", until="", limit=10, include_message_preview=True)` inside Execute,
then pass a returned ID to `rocketclaw_get_session(conversation_id="...")`.
Get returns TSV with `timestamp`, `role`, and `content` columns, one readable
message or event per row in stored entry-ID order, not timestamp order. It includes
pre-compaction messages and marks compaction boundaries; encrypted payloads are
never printed. User and developer messages keep their stored prompt header, such as
`[Slack … principal=…]`, as the first paragraph, so readers can tell who sent them.
Function calls show their name, call ID and arguments; results show
their call ID and text. Reasoning shows stored plaintext summaries and content.
Trace-only events follow each entry's replay items because their interleaving is
not recorded. Unsupported events and non-text tool results have explicit omission
markers rather than serialized objects. A stored item that cannot be decoded becomes
an `event` row naming its entry ID, list, and item index instead of failing the
read. An unknown ID returns only the header.
Get requires a nonblank ID.

Both TSV outputs escape literal backslashes as `\\`, tabs as `\t`, carriage returns
as `\r`, and newlines as `\n` inside cells, preserving one physical line per row.
Headers and rows end with a newline. Outputs have no JSON wrappers; inputs remain
strict JSON objects. All three tools are read-only: they do not mark conversations read or
start turns. Full histories can be large; normal Execute output clipping and
spill handling still apply.

#### Agent session tags

Opt an agent into durable session tags with `permission.rocketclaw.rocketclaw_set_tag` in its
existing permission configuration. Tag groups grant tag access; other tool rules
keep their existing meaning:

```yaml
permission:
  rocketclaw:
    rocketclaw_set_tag:
      - [triage, investigating, resolved]
      - [customer, internal]
```

This exposes `rocketclaw_set_tag(tag)` and `rocketclaw_get_tags()` inside
Execute. Each agent, including a child, uses its own configured groups;
without groups, neither tool nor its generated guidance is shown. Explicit
workflow worker tool lists still limit access, and handoff generation has no tools.
See the opt-in [agent example](internal/rocketclaw/skel/agents/examples/session-tags.example.md).
Copy it to `agents/session-tags.md` to enable that agent, or add the field to an
existing agent and reload. Shipped agents remain untagged by default.

Set an inactive tag to replace the active tags in its group. Set an active tag
again to clear that group. Other groups stay unchanged. Names are exact,
case-sensitive strings, not wildcard patterns; an unconfigured tag returns an
error without changing metadata. Groups must be nonempty lists of nonempty
strings, with no duplicate names anywhere in an agent's groups. An absent or
empty outer list disables tagging; invalid configuration fails definition loading
or staged reload without replacing live definitions.

Both tools return text containing `{"tags":[...]}`, sorted lexically, or
`{"tags":[]}` when empty. They accept no conversation ID and use the calling
agent's owning conversation, including for children and workflows. In a private
producer turn with a human-visible destination, such as an External MCP case or a
one-off cron run, they use that destination instead, so the tags appear on its Web
row; `rocketclaw_current_session_id` still returns the private history ID. All
permitted callers can read its complete active tag list. Tags survive restart,
compaction, history-only deletion, agent changes, and configuration reloads.
Regrouping does not reconcile old tags until a set call touches that group. Fresh
sessions and forks start empty; Sync copies history, not tags. Upgrading copied
External MCP private-history tags once to untagged destinations. Permanent pruning
and failed session cleanup remove metadata.

Web shows tags beside the agent name on session rows. Cmd/Ctrl+P and
the Search page accept `tag:customer`, `tag:customer outage`, and JSON-quoted names
such as `tag:"Needs review"`. Repeated tag filters require every named tag. Labels
refresh through the existing two-second session-list cycle, not a pushed tag event.
Tags do not change activity.

Web lists cron-origin chats in Cmd/Ctrl+P and the Search page; use `is:cron` to show only
cron chats, or `cron:HEARTBEAT` for one job's chats (the exact filename without
`.md`; quote names containing spaces, such as `cron:"Weekly report"`). These
filters follow the chat's recorded origin, not its current agent or
message text, and still apply after human follow-ups.

Migration `019_session_tags.sql` runs through normal State Store startup before
tag-enabled code reads metadata. It needs no backfill. After it runs, binaries
without migration 019 cannot start against the upgraded database: the migration
loader rejects unknown ledger entries. Keep the tag table and migration ledger;
dropping metadata loses tags. A binary-only downgrade is not supported.

#### Background Jobs

An agent can leave long `execute` scripts and `task` subagents running while the
conversation moves on. It is off by default; enable it per agent with an exact rule:

```yaml
permission:
  rocketclaw:
    allow_background: allow
```

Only `allow` and `deny` are accepted. `rocketclaw: allow` and `rocketclaw` wildcard
rules do not enable it, and each agent, including a subagent, uses its own setting.
Denied agents keep their tool schemas unchanged.

For allowed agents, `execute` gains `background` and a short `description` label,
and `task` gains `background` and `continue`. A background call returns a job ID
at once. When the job finishes, fails, or is stopped, its Completion Note enters
the conversation's running turn at the next step, or starts a new system turn when
it is idle; a Slack wake reply lands in the same thread. A hidden cron or External
MCP run's wake reaches the same destination as the original run. When a turn that
read notes fails or stops before saving, a conversation reads them at its next turn
without another wake; a hidden run, which may have no next turn, wakes again 1
minute later, then 5 minutes later, then every 30 minutes until a wake saves, also
across restarts. Only the first failure is posted to its destination, saying that
the wake retries; later failed retries post nothing there. A note for a job
started by a subagent wakes that subagent instead, and its parent is not told.
Every `task` result of an allowed agent reports a continue ID that a later `task`
call passes as `continue` to resume that subagent with its earlier history.

Background work cannot use `ask_user_question`,
`rocketclaw_attach_files_to_response`, or `rocketclaw_restart`; other tools keep the context of the turn that started it.
`task` subagents never get `ask_user_question`, whether or not they run in the
background. Allowed agents can stop a job of their conversation with
`rocketclaw_stop_background_job`; Slack has no other stop control, and `$stop`
stops only the running turn and its foreground work. In Web, **Move to background**
moves running work of allowed agents, and the running jobs are listed above the
composer with Stop (see the [Web README](internal/rocketclaw/web/README.md)).

Shutdown kills Background Jobs, including their bash, while foreground bash still
finishes. After a restart, background subagents resume and later report. A killed
script is reported at the conversation's next turn without starting one; a hidden
cron or External MCP run wakes instead so its destination learns the work died.
There is no limit on concurrent jobs. Migration `028_background_jobs.sql` stores
jobs and their notes; as with other migrations, a binary-only downgrade is not
supported.

### Invoking Skills

Send bare `$` in Slack, or type a leading `$` in the web composer, to discover built-in commands followed by skills allowed for the selected agent. Web suggestions update when you switch agents; selecting a suggestion inserts its prefix without sending.

Use `$review arguments` to invoke a skill named `review`. Built-in names keep their meaning: `$stop` stops work, while `$skill stop inspect the logs` explicitly invokes a skill named `stop`. Skill names match their definitions. Missing or disallowed skills cannot execute.

Skill arguments follow the documented OpenCode command convention:

- `$ARGUMENTS` receives the full argument suffix, retaining internal whitespace and quotes.
- Positions start at `$1`; single- and double-quoted phrases count as one argument, with grouping quotes removed. The highest position in the template receives that argument and all remaining arguments. Missing positions become empty; `$0` stays literal.
- With no argument placeholders, nonempty direct-call arguments follow the skill body after a blank line.

For example, template `Compare $1 against $2` with `"first area" second third` becomes `Compare first area against second third`. Template `$1 / $3 / $0` with `first` becomes `first /  / $0`.

Arguments are substituted before shell blocks run, when the existing skill-shell expansion setting is enabled. Skill instructions load immediately before their associated request. An idle call starts a turn; during active work, Slack calls steer, web Send queues, and web Cmd/Ctrl+Enter steers. Use `$enqueue $review args` or `$enqueue $skill stop args` to explicitly queue a call.

When a web message is consumed in a Slack-managed thread, RocketClaw posts its text there in a `📡 web` Slack block. Waiting or stashed web messages do not appear in Slack until consumed.

Queued calls retain their original invocation and attachments. Availability and shell blocks are evaluated only when consumed, using the executing agent. Promoting a queued call loads it as a steer once; removing it runs no skill shell blocks. See the [command cheatsheet](cmd/rocketclaw/CHEATSHEET.md) for queue controls.

### Supporting Tools

- `cmd/funneld`: serves a small HTTPS reverse-proxy funnel from public mount paths to target base URLs configured by `funneld.json`.
- `cmd/quickweb`: serves trusted internal static applets with one persistent JSON document per page. See [cmd/quickweb/README.md](cmd/quickweb/README.md).

## Runtime Flow

1. A workspace contains `AGENTS.md` plus optional `agents/`, `skills/`, `scripts/`, `cron/`, and `workflows/` definitions.
2. `rocketclaw.json` points RocketClaw at that workspace, provides Slack credentials and channels, and configures optional integrations.
3. RocketClaw builds runtime assets from embedded defaults, configured git overlays, and local workspace overrides.
4. A human message, `$workflow` command, cron job, scheduled prompt, or MCP request enters RocketClaw and invokes RocketCode with the selected agent.
5. RocketCode runs model/tool turns under configured permissions.
6. RocketClaw signals persisted transcript changes to Web and delivers final responses, files, or reactions through the originating connector.
7. Conversation state, every completed step of in-progress turns, scheduled work, queued messages, and routing metadata are persisted. Shutdown stops each turn at its next step: tool calls already running, including bash, finish and are recorded first, while subagents, Code Mode scripts, and workflows stop between their own steps. Background Jobs are killed instead. A second stop signal exits at once. After a restart, each conversation continues its interrupted turn from the last recorded step, then starts saved unstarted messages, without waiting for new input.

Saved workflows run only as foreground managed turns. Each workflow launches fresh isolated custom workers, keeps intermediate values out of managed history, and persists a compact terminal summary of completed, failed, stopped, and skipped phases so later turns can explain what happened. Successful runs also record and deliver the final value. Slack shows one in-progress placeholder and the final result, without phase or worker activity cards. Fan-out workers share one checkout, so parallel writers must own disjoint files. `$stop` ends the run. A daemon restart resumes the run without repeating completed workers.

## Repository Layout

- `cmd/`: runnable binaries.
- `internal/rocketcode/`: core workspace agent runtime.
- `internal/rocketclaw/`: connector service runtime around RocketCode.
- `internal/funneld/`: HTTPS funnel proxy daemon and JSON route config loading.
- `internal/quickweb/`: lightweight static applet server.
- `internal/netutil/`: shared networking helpers.
- `vendor/`: vendored Go dependencies.

## Runtime State And Configuration

RocketClaw is configured with `rocketclaw.json` in the working directory. Runtime state is local to the selected workspace:

- `database_url` in `rocketclaw.json` or `femtoclaw.json`: PostgreSQL store for private MCP and managed Slack sessions, in-progress turns and their step journal, managed Slack routing, External MCP bindings to both sessions, scheduled messages, cron execution state, and goal-loop state. One DSN is one store. The release that introduces the step journal is a clean cutover. Stop all work on the old version, rehearse on a copy of the deployed database, and do not roll back by swapping only the binary: startup rejects migrations it does not know.
- `web_users`: optionally maps browser IP addresses to usernames, for example `"web_users": {"100.64.0.10": "alice"}`. Explicit mappings take precedence for identity and message attribution; otherwise Go uses `tailscale whois --json` to identify the browser by its Tailscale login name and attribute new messages to its display name (or login if the display name is missing or blank). Successful lookups are cached per IP for five minutes, so identity changes can take that long to apply. Unidentified addresses and tagged devices without a manual mapping are denied. Tailscale lookup requires the CLI on the RocketClaw process's PATH and access to its local Tailscale service. Mapping changes require a restart, not an asset reload.
- `.rocketclaw/overlays/`: configured git overlay clones for runtime assets.
- `.rocketclaw/.rocketcode/tmp/<session-id>/`: per-conversation shell TMPDIR (not shared across sessions).
- `.rocketclaw/.rocketcode/spill/<turn-id>/`: oversized execute output for the current turn (deleted when the turn ends).
- `.rocketclaw/.rocketcode/spill/retained/<conversation-hash>/`: oversized output of background `execute` scripts, kept for 7 days.
- `.rocketclaw/workflows/`: effective saved Starlark workflows assembled from embedded, overlay, and workspace `workflows/` assets.

Generated runtime state should not be treated as source code.

Execute keeps full host-tool results inside Starlark, but clips oversized returns to a 2000-line / 50 KiB head. Its footer supplies an opaque `result_id`, not a file path. Call `load_execute_result` at the top level (not inside Starlark), for example:

```json
{"result_id":"<ID from Execute>","start_line":2001,"limit":10,"line_numbers":true}
```

Pages are bounded to 2000 source lines and 50 KiB including numbering and footer. `start_line` is 1-based; 0 means 1. `limit=0` means 2000; larger limits are capped at 2000. Negative values are invalid. Follow `next_start_line` until `EOF`. A line too large for a fresh page returns a UTF-8-safe prefix with an omission marker and advances to the next line; the omitted tail cannot be fetched with this line-only tool, but full storage stays intact. Loading needs no filesystem read grant and creates no new spill. IDs belong only to the current RocketCode Turn and expire on terminal success, error, or interrupt. Restarting the same unfinished journaled turn preserves its original IDs without changing filesystem permissions. A script running as a Background Job instead keeps its oversized output for 7 days, and any turn of the same conversation can load it in that window. `Config.SpillDir` placement is unchanged.

The loader reads only regular files and rejects symlinks in the stored path. Workspace read/edit tools reject the configured spill directory, and file searches exclude its contents. Bash rejects explicit spill paths in arguments, redirections, and `workdir`, even with broad allow rules. These checks do not isolate Bash from the filesystem: scripts and variables can construct paths indirectly. `load_execute_result` is reserved; custom tools cannot register that name.

Fresh RocketClaw stores apply embedded SQL migrations on first open. Startup does not import SQLite and does not migrate historical SQLite formats.
Concurrent startups serialize schema upgrades on one database connection. Cancellation stops migration work; each migration commits its schema changes and ledger entry together, so earlier successful migrations survive a later failure.
Only one RocketClaw process runs against a database at a time. Additional processes
wait for the pglock run lock to become available; interrupting startup cancels that wait.

GitHub Actions runs the full core and web checks in parallel on separate runners on PR updates and pushes to `main`, without a duplicate push run on PR branches. Each runner has its own PostgreSQL 18 service, the only major used for `cmd/rocketclaw`. Local `make test` in `internal/rocketclaw` uses Docker for the newest supported major from https://www.postgresql.org/versions.json, or `ROCKETCLAW_TEST_DATABASE_URL` if set.

Agent files must declare `model` frontmatter. Use a concrete model such as `gpt-5.5`, or map a deployment-specific name in `rocketclaw.json` or `femtoclaw.json`:

```yaml
model: '{{ model "coding-high" }}'
```

```json
"models": {"coding-high": "software-development-sol"}
```

The top-level `openai` object is the default provider. Add named providers under `providers`; `openai/gpt-5.5` explicitly selects the default and `work/gpt-5.5` selects the named provider. Only the first slash separates the provider from the API model ID, so selectors such as `anthropic/api/claude-sonnet-5-5` are valid. Unqualified root models use `openai`, while a child agent resolves its own model independently. RocketClaw never fails over implicitly between providers. Set `autocompaction_threshold` on `openai` or a named provider to override that provider's compaction token count; omit it to keep the 200000 default.

```json
{
  "openai": {"rocketcode_auth": "chatgpt"},
  "providers": {"work": {"rocketcode_auth": "chatgpt"}},
  "models": {"coding-high": "work/gpt-5.5"}
}
```

Manage each provider's local credential separately with `rocketclaw oai login [provider] [--headless]`, `rocketclaw oai list`, and `rocketclaw oai logout [provider]`; omission means `openai`. Credentials are stored in the selected config's workspace under its selected runtime directory, normally `.rocketclaw/auth.json`.

### Attachment storage

PostgreSQL stores attachment metadata, byte size, and conversation ownership. Original file bytes live in the top-level `attachments` driver. Omit this setting to use `<workspace>/.rocketclaw/attachments` (or `.femtoclaw/attachments` for the legacy runtime directory). Startup and reload preserve that directory. An explicit relative path is resolved from `workspace`.

```json
"attachments": {"driver": "filesystem", "path": "/srv/rocketclaw/attachments"}
```

For S3, select an existing bucket by ARN:

```json
"attachments": {"driver": "s3", "bucket_arn": "arn:aws:s3:::rocketclaw-attachments"}
```

S3 uses the AWS SDK's standard credential and region configuration: for example, an IAM role or shared AWS profile, and `AWS_REGION` or the shared profile's region. The bucket ARN does not encode a region. Grant `s3:PutObject` and `s3:GetObject` for the bucket's objects, plus any permissions required by its encryption configuration. RocketClaw does not create buckets or make objects public.

Originals are immutable: filesystem writes publish complete files with a no-overwrite hard link, and S3 writes use `If-None-Match: *`. Metadata is committed only after storage succeeds. Reusing an already recorded ID preserves its original bytes and metadata. A storage object without a matching database row is an orphan; a collision fails rather than attaching new metadata to old bytes. A database commit failure after a successful write can leave such an orphan. Turns in progress record response attachments by ID only; a resumed turn or a redelivery reads the bytes back from storage, so PostgreSQL's JSON size limit does not cap attachment size.

Back up the database **and** the attachment directory or bucket together. Restoring only PostgreSQL restores references, not file contents. Keep the same storage location across restarts and restores; changing drivers or locations does not copy existing originals. Downloads still require an authorized conversation reference in surviving history or its queue. Mutable workspace copies used in prompts do not replace stored originals.

`funneld` is configured with `funneld.json` by default, or with `--config` / `FUNNELD_CONFIG`. Its config declares a certificate `host`, an optional `cert_cache`, and routes from public mount paths to target base URLs.

## Development Notes

This repository uses vendored dependencies and standard Go commands:

```sh
go test ./...
```
