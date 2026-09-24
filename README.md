# Rocketable Platform

Rocketable Platform is Rocketable's workspace-local AI agent runtime. It turns a repository or working directory into an agent environment where humans can interact through Slack or an external MCP endpoint, while agents operate through controlled access to local files, shell commands, tools, skills, saved Starlark workflows, attachments, and connected services.

The platform is written in Go and is oriented around internal, workspace-local deployment rather than hosted multi-tenant SaaS.

## Experimental Software

Rocketable Platform is experimental software. It can (and will) run model-generated actions against local files, shell commands, connected services, and team communication channels, so users are responsible for reviewing configuration, permissions, outputs, and integrations before relying on it in any sensitive or production environment.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## Core Capabilities

- Run workspace-aware AI agents with local instructions, agent definitions, skills, attachments, subagents, custom tools, file access, shell commands, web fetches, and explicit permission rules.
- Keep agent work durable through PostgreSQL-backed sessions, replay, active-turn checkpoints, connector routing, scheduled messages, restart recovery, and conversation-local goal loops.
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

### RocketClaw

`internal/rocketclaw` is the long-running service runtime around RocketCode. It provides thread-local conversations in configured Slack channels, saved Starlark workflows, external MCP, cron-defined background prompts, one-shot and recurring scheduled messages, inbound and outbound attachments, supervisor restart, and PostgreSQL state selected by `database_url`.

The runnable entry point is `cmd/rocketclaw`. Run `rocketclaw help` for validation and operational commands.

RocketClaw embeds its Web interface, including when run directly from the Go module:

```sh
go run github.com/Rocketable/platform/cmd/rocketclaw@main
```

Web always starts at `0.0.0.0:3000`. To change its bind address, set
`"web": {"listen_address": "127.0.0.1:3000"}` in `rocketclaw.json` or `femtoclaw.json`.
No Web environment variables or socket setup are required. Browser access uses
the configured `web_users` IP-to-username mapping, or Tailscale WhoIs when the IP has no mapping.

The compiled SPA in `internal/rocketclaw/internal/web/dist/` is committed with its
frontend source changes, so Go-only builds need no Bun installation. When building
from a checkout, `make -C internal/rocketclaw build` (also invoked by root
`make build`) installs the locked frontend dependencies and rebuilds the SPA with
Bun before compiling `bin/rocketclaw`. Commit the rebuilt `dist/` files alongside
changes in `internal/rocketclaw/web/`.

Slack native forwarded-thread expansion requires the bot scopes `channels:read` and `channels:history`; reinstall the Slack app after adding scopes. Message `...` menu actions require the `commands` scope and one message shortcut (`rocketclaw_actions`); reinstall after adding it. See `cmd/rocketclaw/CHEATSHEET.md`. RocketClaw expands only source channels Slack confirms are public and that the bot can already read. It never auto-joins a channel. Private, inaccessible, malformed, or partially unreadable source threads retain only Slack's forwarded preview.

Slack configuration uses direct `slack.channels` mappings. Each mapping names a channel, an ordered non-empty `agents` list, and its authorized `allowed_user_ids`. An ordinary authorized app mention in a configured channel starts a fresh managed thread whose initiating message is its first turn. An `@` channel row is not a Slack channel; it supplies agents and an allowlist for hails in any other joined public channel, private channel, or group DM. A hail in an unmanaged thread takes that thread over and includes prior messages. 1:1 DMs never start this way. The bot still never auto-joins. A root `$agent` mention opens the native agent selector; selecting an agent registers a ready thread for that agent so the next human reply is the first turn. A root `$agent <name>` mention can also select a configured agent directly: without a message it registers a ready thread, while a following message starts the selected agent with that message as its first turn. A command-help mention is another exception: RocketClaw posts permanent help as the first thread reply without adding either message to agent history. Later replies use only that thread's persisted history.

External MCP exposes `session_prompt`. Every call supplies an external conversation ID, agent, and configured Slack channel. A new ID creates one private MCP session and one managed Slack session on the same Slack thread. The MCP agent remains fixed; the managed agent starts from the channel configuration and can be switched from Slack. MCP history is copied into managed history, but Slack history is never copied back. Later calls keep the same channel and Slack thread. Slack Blocks label MCP requests and responses with their conversation ID and agent.

Every active `cron/*.md` definition declares a quoted `channel` that matches a configured Slack channel. Empty completion output is silent; non-empty output starts a fresh managed thread in that channel.

#### Agent session inspection

Agents can inspect durable conversations with `rocketclaw_current_session_id`,
`rocketclaw_list_sessions`, and `rocketclaw_get_session`, directly or inside Execute.
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
arguments (`{}` for a direct call). It returns only the bare conversation ID, with
no JSON wrapper or added newline. Pass that ID to `rocketclaw_get_session(conversation_id="...")` in the next
call to read stored entries, including those before the compaction point.
This is the owning bridge's durable conversation ID: a private External MCP bridge
returns its private history ID, not its public external ID or managed destination.
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
never printed. Function calls show their name, call ID and arguments; results show
their call ID and text. Reasoning shows stored plaintext summaries and content.
Trace-only events follow each entry's replay items because their interleaving is
not recorded. Unsupported events and non-text tool results have explicit omission
markers rather than serialized objects. An unknown ID returns only the header.
Get requires a nonblank ID.

Both TSV outputs escape literal backslashes as `\\`, tabs as `\t`, carriage returns
as `\r`, and newlines as `\n` inside cells, preserving one physical line per row.
Headers and rows end with a newline. Outputs have no JSON wrappers; inputs remain
strict JSON objects. All three tools are read-only: they do not mark conversations read or
start turns. Full histories can be large; normal Execute output clipping and
spill handling still apply.

### Invoking Skills

Send bare `$` in Slack, or type a leading `$` in the web composer, to discover built-in commands followed by skills allowed for the selected agent. Web suggestions update when you switch agents; selecting a suggestion inserts its prefix without sending.

Use `$review arguments` to invoke a skill named `review`. Built-in names keep their meaning: `$stop` stops work, while `$skill stop inspect the logs` explicitly invokes a skill named `stop`. Skill names match their definitions. Missing or disallowed skills cannot execute.

Skill arguments follow the documented OpenCode command convention:

- `$ARGUMENTS` receives the full argument suffix, retaining internal whitespace and quotes.
- Positions start at `$1`; single- and double-quoted phrases count as one argument, with grouping quotes removed. The highest position in the template receives that argument and all remaining arguments. Missing positions become empty; `$0` stays literal.
- With no argument placeholders, nonempty direct-call arguments follow the skill body after a blank line.

For example, template `Compare $1 against $2` with `"first area" second third` becomes `Compare first area against second third`. Template `$1 / $3 / $0` with `first` becomes `first /  / $0`.

Arguments are substituted before shell blocks run, when the existing skill-shell expansion setting is enabled. Skill instructions load immediately before their associated request. An idle call starts a turn; during active work, Slack calls steer, web Send queues, and web Cmd/Ctrl+Enter steers. Use `$enqueue $review args` or `$enqueue $skill stop args` to explicitly queue a call.

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
6. RocketClaw publishes progress, final responses, files, or reactions back through the originating connector.
7. Conversation state, active-turn handoffs, scheduled work, queued messages, and routing metadata are persisted so restart recovery can refire interrupted turns and start saved unstarted messages without waiting for new input.

Saved workflows run only as foreground managed turns. Each workflow launches fresh isolated custom workers, keeps intermediate values out of managed history, and persists a compact terminal summary of completed, failed, stopped, and skipped phases so later turns can explain what happened. Successful runs also record and deliver the final value. Slack shows phase progress and each worker's latest attributed activity with plan/task cards. Fan-out workers share one checkout, so parallel writers must own disjoint files. The state store does not persist resumable workflow progress: `$stop` ends the run, and daemon restart requires a new `$workflow` invocation.

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

- `database_url` in `rocketclaw.json` or `femtoclaw.json`: PostgreSQL store for private MCP and managed Slack sessions, active-turn restart handoffs, managed Slack routing, External MCP bindings to both sessions, scheduled messages, cron execution state, restart notifications, and goal-loop state. One DSN is one store.
- `web_users`: optionally maps browser IP addresses to usernames, for example `"web_users": {"100.64.0.10": "alice"}`. Explicit mappings take precedence; otherwise Go uses `tailscale whois --json` to identify the browser as its Tailscale login name. Successful lookups are cached per IP for five minutes, so identity changes can take that long to apply. Unidentified addresses and tagged devices without a manual mapping are denied. Tailscale lookup requires the CLI on the RocketClaw process's PATH and access to its local Tailscale service. Mapping changes require a restart, not an asset reload.
- `.rocketclaw/overlays/`: configured git overlay clones for runtime assets.
- `.rocketclaw/.rocketcode/tmp/<session-id>/`: per-conversation shell TMPDIR (not shared across sessions).
- `.rocketclaw/.rocketcode/spill/<turn-id>/`: oversized execute output for the current turn (deleted when the turn ends).
- `.rocketclaw/workflows/`: effective saved Starlark workflows assembled from embedded, overlay, and workspace `workflows/` assets.

Generated runtime state should not be treated as source code.

Fresh RocketClaw stores apply embedded SQL migrations on first open. Startup does not import SQLite and does not migrate historical SQLite formats.
Concurrent startups serialize schema upgrades on one database connection. Cancellation stops migration work; each migration commits its schema changes and ledger entry together, so earlier successful migrations survive a later failure.
Only one RocketClaw process runs against a database at a time. Additional processes
wait for the pglock run lock to become available; interrupting startup cancels that wait.

Store tests run against the last three supported PostgreSQL majors from https://www.postgresql.org/versions.json. Local `make test` in `internal/rocketclaw` uses Docker for the newest of those, or `ROCKETCLAW_TEST_DATABASE_URL` if set. GitHub Actions runs all three.

Agent files must declare `model` frontmatter. Use a concrete model such as `gpt-5.5`, or map a deployment-specific name in `rocketclaw.json` or `femtoclaw.json`:

```yaml
model: '{{ model "coding-high" }}'
```

```json
"models": {"coding-high": "software-development-sol"}
```

The top-level `openai` object is the default provider. Add named providers under `providers`; `openai/gpt-5.5` explicitly selects the default and `work/gpt-5.5` selects the named provider. Unqualified root models use `openai`, while a child agent resolves its own model independently. RocketClaw never fails over implicitly between providers. Set `autocompaction_threshold` on `openai` or a named provider to override that provider's compaction token count; omit it to keep the 200000 default.

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

Originals are immutable: filesystem writes publish complete files with a no-overwrite hard link, and S3 writes use `If-None-Match: *`. Metadata is committed only after storage succeeds. Reusing an already recorded ID preserves its original bytes and metadata. A storage object without a matching database row is an orphan; a collision fails rather than attaching new metadata to old bytes. A database commit failure after a successful write can leave such an orphan.

Back up the database **and** the attachment directory or bucket together. Restoring only PostgreSQL restores references, not file contents. Keep the same storage location across restarts and restores; changing drivers or locations does not copy existing originals. Downloads still require an authorized conversation reference in surviving history or its queue. Mutable workspace copies used in prompts do not replace stored originals.

`funneld` is configured with `funneld.json` by default, or with `--config` / `FUNNELD_CONFIG`. Its config declares a certificate `host`, an optional `cert_cache`, and routes from public mount paths to target base URLs.

## Development Notes

This repository uses vendored dependencies and standard Go commands:

```sh
go test ./...
```
