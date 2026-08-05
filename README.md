# Rocketable Platform

Rocketable Platform is Rocketable's workspace-local AI agent runtime. It turns a repository or working directory into an agent environment where humans can interact through Slack or an external MCP endpoint, while agents operate through controlled access to local files, shell commands, tools, skills, saved Starlark workflows, attachments, and connected services.

The platform is written in Go and is oriented around internal, workspace-local deployment rather than hosted multi-tenant SaaS.

## Experimental Software

Rocketable Platform is experimental software. It can (and will) run model-generated actions against local files, shell commands, connected services, and team communication channels, so users are responsible for reviewing configuration, permissions, outputs, and integrations before relying on it in any sensitive or production environment.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## Core Capabilities

- Run workspace-aware AI agents with local instructions, agent definitions, skills, attachments, subagents, custom tools, file access, shell commands, web fetches, and explicit permission rules.
- Keep agent work durable through SQLite-backed sessions, replay, active-turn checkpoints, connector routing, scheduled messages, restart recovery, and conversation-local goal loops.
- Connect agents to team workflows through Slack, cron jobs, scheduled prompts, and an external MCP HTTP endpoint.
- Run checked-in Starlark workflows as foreground managed turns with isolated custom workers and Slack phase and worker activity progress.
- Route model requests through independently configured default and named OpenAI-compatible providers while preserving one local agent/tool model.
- Run `openresponsesd` as a separate local OpenResponses-shaped API daemon that can route to OpenAI Responses, OpenAI-compatible Chat Completions, or Anthropic Messages upstreams.
- Expose optional OpenTelemetry/OpenInference-compatible tracing for agent runs.

## Main Components

### RocketCode

`internal/rocketcode` is the core reasoning runtime. It builds model requests from workspace context, runs the tool loop, enforces permissions, handles supported image and PDF attachments, and records replayable session entries.

Runnable entry points:

- `cmd/rocketcode`: interactive CLI for testing RocketCode in the current workspace.
- `cmd/rocketloop`: non-interactive autonomous loop for a goal supplied by arguments or stdin.

### RocketClaw

`internal/rocketclaw` is the long-running service runtime around RocketCode. It provides thread-local conversations in configured Slack channels, saved Starlark workflows, external MCP, cron-defined background prompts, one-shot and recurring scheduled messages, inbound and outbound attachments, supervisor restart, and SQLite state under the selected runtime directory.

The runnable entry point is `cmd/rocketclaw`. Run `rocketclaw help` for setup, validation, session inspection, and operational commands.

Slack native forwarded-thread expansion requires the bot scopes `channels:read` and `channels:history`; reinstall the Slack app after adding scopes. RocketClaw expands only source channels Slack confirms are public and that the bot can already read. It never auto-joins a channel. Private, inaccessible, malformed, or partially unreadable source threads retain only Slack's forwarded preview.

Slack configuration uses direct `slack.channels` mappings. Each mapping names a channel, an ordered non-empty `agents` list, and its authorized `allowed_user_ids`. An ordinary authorized app mention in a configured channel starts a fresh managed thread whose initiating message is its first turn. A root `$agent` mention opens the native agent selector; selecting an agent registers a ready thread for that agent so the next human reply is the first turn. A root `$agent <name>` mention can also select a configured agent directly: without a message it registers a ready thread, while a following message starts the selected agent with that message as its first turn. A command-help mention is another exception: RocketClaw posts permanent help as the first thread reply without adding either message to agent history. Later replies use only that thread's persisted history.

External MCP exposes `session_prompt`. Every call supplies an external conversation ID, agent, and configured Slack channel. A new ID creates one private MCP session and one managed Slack session on the same Slack thread. The MCP agent remains fixed; the managed agent starts from the channel configuration and can be switched from Slack. MCP history is copied into managed history, but Slack history is never copied back. Later calls keep the same channel and Slack thread. Slack Blocks label MCP requests and responses with their conversation ID and agent.

Every active `cron/*.md` definition declares a quoted `channel` that matches a configured Slack channel. Empty completion output is silent; non-empty output starts a fresh managed thread in that channel.

### Supporting Tools

- `cmd/openresponsesd`: serves `/healthz`, `/v1/responses`, and `/v1/responses/compact` with optional bearer auth and provider routing configured by `openresponsesd.json`.
- `cmd/funneld`: serves a small HTTPS reverse-proxy funnel from public mount paths to target base URLs configured by `funneld.json`.
- `cmd/quickbench`: runs YAML benchmarks through RocketCode with CLI-selected models. See [cmd/quickbench/README.md](cmd/quickbench/README.md).
- `cmd/quickweb`: serves trusted internal static applets with one persistent JSON document per page. See [cmd/quickweb/README.md](cmd/quickweb/README.md).
- `cmd/interviewd`: serves a temporary local HTML form for structured interview questions and prints submitted answers as Markdown.

## Runtime Flow

1. A workspace contains `AGENTS.md` plus optional `agents/`, `skills/`, `scripts/`, `cron/`, and `workflows/` definitions.
2. `rocketclaw.json` points RocketClaw at that workspace, provides Slack credentials and channels, and configures optional integrations.
3. RocketClaw builds runtime assets from embedded defaults, configured git overlays, and local workspace overrides.
4. A human message, `$workflow` command, cron job, scheduled prompt, or MCP request enters RocketClaw and invokes RocketCode with the selected agent.
5. RocketCode runs model/tool turns under configured permissions.
6. RocketClaw publishes progress, final responses, files, or reactions back through the originating connector.
7. Conversation state, active-turn handoffs, scheduled work, and routing metadata are persisted so restart recovery can refire interrupted turns as model-guided continuations from uncertain state.

Saved workflows run only as foreground managed turns. Each workflow launches fresh isolated custom workers, keeps intermediate values out of managed history, and persists a compact terminal summary of completed, failed, stopped, and skipped phases so later turns can explain what happened. Successful runs also record and deliver the final value. Slack shows phase progress and each worker's latest attributed activity with plan/task cards. Fan-out workers share one checkout, so parallel writers must own disjoint files. SQLite does not persist resumable workflow progress: `$stop` ends the run, and daemon restart requires a new `$workflow` invocation.

For local CLI experimentation, `rocketcode` and `rocketloop` run RocketCode directly in the current working directory.

## Repository Layout

- `cmd/`: runnable binaries.
- `internal/rocketcode/`: core workspace agent runtime.
- `internal/rocketclaw/`: connector service runtime around RocketCode.
- `internal/openresponsesd/`: OpenResponses-shaped daemon, config loading, HTTP/WebSocket handling, and provider adapters.
- `internal/funneld/`: HTTPS funnel proxy daemon and JSON route config loading.
- `internal/quickbench/`: benchmark runner implementation.
- `internal/quickweb/`: lightweight static applet server.
- `internal/interviewd/`: structured interview form server.
- `internal/netutil/`: shared networking helpers.
- `vendor/`: vendored Go dependencies.

## Runtime State And Configuration

RocketClaw is configured with `rocketclaw.json` in the working directory. Runtime state is local to the selected workspace:

- `.rocketclaw/state.sqlite3`: private MCP and managed Slack sessions, active-turn restart handoffs, managed Slack routing, External MCP bindings to both sessions, scheduled messages, cron execution state, restart notifications, and goal-loop state.
- `.rocketclaw/overlays/`: configured git overlay clones for runtime assets.
- `.rocketclaw/.rocketcode/`: RocketCode shell output and transient artifacts.
- `.rocketclaw/workflows/`: effective saved Starlark workflows assembled from embedded, overlay, and workspace `workflows/` assets.

Generated runtime state should not be treated as source code.

Fresh RocketClaw runtime directories initialize the current SQLite schema directly with schema marker `user_version = 9`. Existing state and configuration must already use current formats; startup does not migrate historical formats.

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

`openresponsesd` is configured with `openresponsesd.json` by default, or with `--config` / `OPENRESPONSESD_CONFIG`. Its documented credential environment variables are `OPENRESPONSESD_OPENAI_API_KEY` and `OPENRESPONSESD_ANTHROPIC_API_KEY`; bearer-token auth can be set in config or overridden locally with `--auth-token` / `OPENRESPONSESD_AUTH_TOKEN`.

`funneld` is configured with `funneld.json` by default, or with `--config` / `FUNNELD_CONFIG`. Its config declares a certificate `host`, an optional `cert_cache`, and routes from public mount paths to target base URLs.

## Development Notes

This repository uses vendored dependencies and standard Go commands:

```sh
go test ./...
```
