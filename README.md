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

### RocketClaw

`internal/rocketclaw` is the long-running service runtime around RocketCode. It provides thread-local conversations in configured Slack channels, saved Starlark workflows, external MCP, cron-defined background prompts, one-shot and recurring scheduled messages, inbound and outbound attachments, supervisor restart, and PostgreSQL state selected by `database_url`.

The runnable entry point is `cmd/rocketclaw`. Run `rocketclaw help` for setup, validation, session inspection, and operational commands.

Slack native forwarded-thread expansion requires the bot scopes `channels:read` and `channels:history`; reinstall the Slack app after adding scopes. RocketClaw expands only source channels Slack confirms are public and that the bot can already read. It never auto-joins a channel. Private, inaccessible, malformed, or partially unreadable source threads retain only Slack's forwarded preview.

Slack configuration uses direct `slack.channels` mappings. Each mapping names a channel, an ordered non-empty `agents` list, and its authorized `allowed_user_ids`. An ordinary authorized app mention in a configured channel starts a fresh managed thread whose initiating message is its first turn. An `@` channel row is not a Slack channel; it supplies agents and an allowlist for hails in any other joined public channel, private channel, or group DM. A hail in an unmanaged thread takes that thread over and includes prior messages. 1:1 DMs never start this way. The bot still never auto-joins. A root `$agent` mention opens the native agent selector; selecting an agent registers a ready thread for that agent so the next human reply is the first turn. A root `$agent <name>` mention can also select a configured agent directly: without a message it registers a ready thread, while a following message starts the selected agent with that message as its first turn. A command-help mention is another exception: RocketClaw posts permanent help as the first thread reply without adding either message to agent history. Later replies use only that thread's persisted history.

External MCP exposes `session_prompt`. Every call supplies an external conversation ID, agent, and configured Slack channel. A new ID creates one private MCP session and one managed Slack session on the same Slack thread. The MCP agent remains fixed; the managed agent starts from the channel configuration and can be switched from Slack. MCP history is copied into managed history, but Slack history is never copied back. Later calls keep the same channel and Slack thread. Slack Blocks label MCP requests and responses with their conversation ID and agent.

Every active `cron/*.md` definition declares a quoted `channel` that matches a configured Slack channel. Empty completion output is silent; non-empty output starts a fresh managed thread in that channel.

### Supporting Tools

- `cmd/funneld`: serves a small HTTPS reverse-proxy funnel from public mount paths to target base URLs configured by `funneld.json`.
- `cmd/quickbench`: BAR benchmarks (pack/unpack/dump/capture/run + ELO) through RocketCode. See [cmd/quickbench/README.md](cmd/quickbench/README.md).
- `cmd/quickweb`: serves trusted internal static applets with one persistent JSON document per page. See [cmd/quickweb/README.md](cmd/quickweb/README.md).

## Runtime Flow

1. A workspace contains `AGENTS.md` plus optional `agents/`, `skills/`, `scripts/`, `cron/`, and `workflows/` definitions.
2. `rocketclaw.json` points RocketClaw at that workspace, provides Slack credentials and channels, and configures optional integrations.
3. RocketClaw builds runtime assets from embedded defaults, configured git overlays, and local workspace overrides.
4. A human message, `$workflow` command, cron job, scheduled prompt, or MCP request enters RocketClaw and invokes RocketCode with the selected agent.
5. RocketCode runs model/tool turns under configured permissions.
6. RocketClaw publishes progress, final responses, files, or reactions back through the originating connector.
7. Conversation state, active-turn handoffs, scheduled work, and routing metadata are persisted so restart recovery can refire interrupted turns as model-guided continuations from uncertain state.

Saved workflows run only as foreground managed turns. Each workflow launches fresh isolated custom workers, keeps intermediate values out of managed history, and persists a compact terminal summary of completed, failed, stopped, and skipped phases so later turns can explain what happened. Successful runs also record and deliver the final value. Slack shows phase progress and each worker's latest attributed activity with plan/task cards. Fan-out workers share one checkout, so parallel writers must own disjoint files. The state store does not persist resumable workflow progress: `$stop` ends the run, and daemon restart requires a new `$workflow` invocation.

## Repository Layout

- `cmd/`: runnable binaries.
- `internal/rocketcode/`: core workspace agent runtime.
- `internal/rocketclaw/`: connector service runtime around RocketCode.
- `internal/funneld/`: HTTPS funnel proxy daemon and JSON route config loading.
- `internal/quickbench/`: BAR format, capture, run matrix, and ELO ranking.
- `internal/quickweb/`: lightweight static applet server.
- `internal/netutil/`: shared networking helpers.
- `vendor/`: vendored Go dependencies.

## Runtime State And Configuration

RocketClaw is configured with `rocketclaw.json` in the working directory. Runtime state is local to the selected workspace:

- `database_url` in `rocketclaw.json` or `femtoclaw.json`: PostgreSQL store for private MCP and managed Slack sessions, active-turn restart handoffs, managed Slack routing, External MCP bindings to both sessions, scheduled messages, cron execution state, restart notifications, and goal-loop state. One DSN is one store. `run` ignores `.rocketclaw/state.sqlite3`. `fc migrate` copies missing v9 rows into that store.
- `.rocketclaw/overlays/`: configured git overlay clones for runtime assets.
- `.rocketclaw/.rocketcode/tmp/<session-id>/`: per-conversation shell TMPDIR (not shared across sessions).
- `.rocketclaw/workflows/`: effective saved Starlark workflows assembled from embedded, overlay, and workspace `workflows/` assets.

Generated runtime state should not be treated as source code.

Fresh RocketClaw stores apply embedded SQL migrations on first open. Startup does not import SQLite and does not migrate historical SQLite formats. `fc migrate` copies missing v9 rows. `fc check` pings PostgreSQL and does not repair a file.

Store tests run against the last three supported PostgreSQL majors from https://www.postgresql.org/versions.json. Local `make test` in `internal/rocketclaw` uses Docker for the newest of those, or `ROCKETCLAW_TEST_DATABASE_URL` if set. GitHub Actions runs all three.

Agent files must declare `model` frontmatter. Use a concrete model such as `gpt-5.5`, or map a deployment-specific name in `rocketclaw.json` or `femtoclaw.json`:

```yaml
model: '{{ model "coding-high" }}'
```

```json
"models": {"coding-high": "software-development-sol"}
```

The top-level `openai` object is the default provider. Add named providers under `providers`; `openai/gpt-5.5` explicitly selects the default and `work/gpt-5.5` selects the named provider. Unqualified root models use `openai`, while a child agent resolves its own model independently. RocketClaw never fails over implicitly between providers.

```json
{
  "openai": {"rocketcode_auth": "chatgpt"},
  "providers": {"work": {"rocketcode_auth": "chatgpt"}},
  "models": {"coding-high": "work/gpt-5.5"}
}
```

Manage each provider's local credential separately with `rocketclaw oai login [provider] [--headless]`, `rocketclaw oai list`, and `rocketclaw oai logout [provider]`; omission means `openai`. Credentials are stored in the selected config's workspace under its selected runtime directory, normally `.rocketclaw/auth.json`.

`funneld` is configured with `funneld.json` by default, or with `--config` / `FUNNELD_CONFIG`. Its config declares a certificate `host`, an optional `cert_cache`, and routes from public mount paths to target base URLs.

## Development Notes

This repository uses vendored dependencies and standard Go commands:

```sh
go test ./...
```
