# Rocketable Platform

Rocketable Platform is Rocketable's workspace-local AI agent runtime. It turns a repository or working directory into an agent environment where humans can interact through a CLI, Slack, Discord, browser voice mode, or an external MCP endpoint, while agents operate through controlled access to local files, shell commands, tools, skills, attachments, and connected services.

The platform is written in Go and is oriented around internal, workspace-local deployment rather than hosted multi-tenant SaaS.

## Experimental Software

Rocketable Platform is experimental software. It can (and will) run model-generated actions against local files, shell commands, connected services, and team communication channels, so users are responsible for reviewing configuration, permissions, outputs, and integrations before relying on it in any sensitive or production environment.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## Core Capabilities

- Run workspace-aware AI agents with local instructions, agent definitions, skills, attachments, subagents, custom tools, file access, shell commands, web fetches, and explicit permission rules.
- Keep agent work durable through SQLite-backed sessions, replay, checkpoints, connector routing, scheduled messages, restart recovery, and conversation-local goal loops.
- Connect agents to team workflows through Slack, Discord text, Discord voice, browser voice mode, cron jobs, scheduled prompts, and an external MCP HTTP endpoint.
- Route model requests across OpenAI and Anthropic providers while preserving one local agent/tool model.
- Expose optional OpenTelemetry/OpenInference-compatible tracing for agent runs.

## Main Components

### RocketCode

`internal/rocketcode` is the core reasoning runtime. It builds model requests from workspace context, runs the tool loop, enforces permissions, handles supported image and PDF attachments, and records replayable session entries.

Runnable entry points:

- `cmd/rocketcode`: interactive CLI for testing RocketCode in the current workspace.
- `cmd/rocketloop`: non-interactive autonomous loop for a goal supplied by arguments or stdin.

### RocketClaw

`internal/rocketclaw` is the long-running service runtime around RocketCode. It provides persistent conversations, Slack and Discord connectors, voice input/output, browser voice mode, external MCP, cron-defined background prompts, one-shot and recurring scheduled messages, inbound and outbound attachments, graceful restart, and SQLite state under the selected runtime directory.

The runnable entry point is `cmd/rocketclaw`. Run `rocketclaw help` for setup, validation, session inspection, and operational commands.

### Supporting Tools

- `cmd/quickbench`: runs YAML benchmarks through RocketCode with CLI-selected models. See [cmd/quickbench/README.md](cmd/quickbench/README.md).
- `cmd/quickweb`: serves trusted internal static applets with one persistent JSON document per page. See [cmd/quickweb/README.md](cmd/quickweb/README.md).
- `cmd/interviewd`: serves a temporary local HTML form for structured interview questions and prints submitted answers as Markdown.

## Runtime Flow

1. A workspace contains `AGENTS.md` plus optional `agents/`, `skills/`, `scripts/`, and `cron/` definitions.
2. `rocketclaw.json` points RocketClaw at that workspace and enables connectors.
3. RocketClaw builds runtime assets from embedded defaults, configured git overlays, and local workspace overrides.
4. A human message, cron job, scheduled prompt, voice transcript, or MCP request enters RocketClaw and invokes RocketCode with the selected agent.
5. RocketCode runs model/tool turns under configured permissions.
6. RocketClaw publishes progress, final responses, files, reactions, or voice output back through the originating connector.
7. Conversation state, scheduled work, and routing metadata are persisted so the runtime can continue after restart.

For local CLI experimentation, `rocketcode` and `rocketloop` run RocketCode directly in the current working directory.

## Repository Layout

- `cmd/`: runnable binaries.
- `internal/rocketcode/`: core workspace agent runtime.
- `internal/rocketclaw/`: connector service runtime around RocketCode.
- `internal/quickbench/`: benchmark runner implementation.
- `internal/quickweb/`: lightweight static applet server.
- `internal/interviewd/`: structured interview form server.
- `internal/netutil/`: shared networking helpers.
- `vendor/`: vendored Go dependencies.

## Runtime State And Configuration

RocketClaw is configured with `rocketclaw.json` in the working directory. Runtime state is local to the selected workspace:

- `.rocketclaw/state.sqlite3`: sessions, connector routing, scheduled messages, external MCP sessions, restart notifications, and goal-loop state.
- `.rocketclaw/overlays/`: configured git overlay clones for runtime assets.
- `.rocketclaw/.rocketcode/`: RocketCode shell output and transient artifacts.

Generated runtime state should not be treated as source code.

## Development Notes

This repository uses vendored dependencies and standard Go commands:

```sh
go test ./...
```

Important behavior contracts are captured as architecture decision records under:

- `internal/rocketcode/docs/adr/`
- `internal/rocketclaw/docs/adr/`

Those architecture decision records describe the product behavior that refactors are expected to preserve.
