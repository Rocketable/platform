# Rocketable Platform

Rocketable Platform is the codebase for Rocketable's workspace-local AI agent runtime. It lets a team run long-lived AI agents inside a real project workspace, connect those agents to human-facing channels such as Slack and Discord, give them controlled access to files and tools, and preserve enough state for ongoing conversations, scheduled work, and operational recovery.

At a high level, the platform turns a repository or working directory into an agent environment:

- Humans interact with agents through a CLI, Slack, Discord, browser voice mode, or an external MCP endpoint.
- Agents run through RocketCode, a reasoning loop that loads workspace instructions, agents, skills, attachments, and permission rules from local files.
- RocketClaw wraps RocketCode in service connectors for persistent team workflows: DMs, channel mentions, managed threads, voice input/output, cron jobs, scheduled prompts, goal loops, and restart-safe state.
- Supporting tools provide benchmarking, lightweight internal web app hosting, and structured interview collection.

The project is written in Go and is currently oriented around internal, workspace-local deployment rather than a hosted multi-tenant SaaS.

## Experimental Software

Rocketable Platform is experimental software. It can (and will) run model-generated actions against local files, shell commands, connected services, and team communication channels, so users are responsible for reviewing configuration, permissions, outputs, and integrations before relying on it in any sensitive or production environment.

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to warranties of merchantability, fitness for a particular purpose, noninfringement, availability, accuracy, or error-free operation. To the maximum extent permitted by law, Rocketable, Inc. and contributors are not liable for any claim, damage, data loss, service interruption, security issue, business loss, or other liability arising from use of, inability to use, or reliance on this software or its outputs.

See [LICENSE](LICENSE) for the full license terms.

## What It Does

Rocketable Platform is built around a few practical jobs:

- Run AI agents with access to a project workspace, including local files, shell commands, web fetches, skills, subagents, attachments, and custom tools.
- Gate agent capabilities with explicit permission rules, sandboxed command execution, and optional automatic permission review.
- Keep conversations durable through SQLite-backed session history, replay, checkpoints, scheduled messages, connector thread routing, and goal-loop state.
- Bring agents into team communication channels through Slack Socket Mode, Discord text, Discord voice, browser voice mode, and an external MCP HTTP endpoint.
- Let agents run background or scheduled work from trusted workspace cron files and report results back into managed conversations.
- Support model-provider routing across OpenAI and Anthropic models while preserving one local agent/tool model.
- Provide observability hooks through OpenTelemetry/OpenInference-compatible tracing.

## Main Components

### RocketCode

`internal/rocketcode` is the core agent runtime. It builds model requests from local workspace context and runs the tool loop until an assistant turn is complete.

RocketCode supports:

- workspace `AGENTS.md` instructions
- file-based agent definitions and skills
- OpenAI and Anthropic provider routing
- interactive and non-interactive execution
- built-in tools for reading files, applying patches, searching, shell commands, web fetches, skills, and subagent tasks
- attachments for supported images and PDFs
- permission checks and sandboxed shell execution
- replayable session entries and diagnostics
- embedder-provided custom tools

The runnable entry points are:

- `cmd/rocketcode`: interactive CLI for testing RocketCode in the current workspace.
- `cmd/rocketloop`: non-interactive autonomous loop for a goal supplied by arguments or stdin.

### RocketClaw

`internal/rocketclaw` is the service runtime that embeds RocketCode and connects it to long-running human workflows.

RocketClaw supports:

- a persistent main RocketCode conversation
- Slack DMs, Slack app mentions, and managed Slack threads
- Discord text DMs, channel mentions, and managed guild threads
- Discord voice input/output
- browser voice mode over HTTPS and WebSocket
- an external MCP endpoint with a `session_prompt` tool
- cron-defined background prompts
- one-shot and recurring scheduled messages
- text-channel goal loops started from `🔁` or `🏁`
- inbound and outbound attachment handling
- graceful restart and restart recovery
- SQLite state under the selected runtime directory

The runnable entry point is `cmd/rocketclaw`. Run `rocketclaw help` for the current command list. The main operational commands are `run`, `setup`, `doctor`, `lint`, `agent-graph`, `oai login`, and `fc` session inspection.

### Quickbench

`cmd/quickbench` runs YAML benchmarks through RocketCode with CLI-selected models. It is useful for checking agent behavior against repeatable scenarios and comparing providers.

See [cmd/quickbench/README.md](cmd/quickbench/README.md) for configuration and examples.

### Quickweb

`cmd/quickweb` serves static internal applets from the current directory and gives each page one persistent JSON document through `/data`. It is intended for trusted internal networks or VPN access.

See [cmd/quickweb/README.md](cmd/quickweb/README.md) for endpoints, flags, and operational notes.

### Interviewd

`cmd/interviewd` collects structured interview questions, serves them as a temporary local HTML form, and prints submitted answers as Markdown.

## How The Pieces Fit Together

The usual production-style flow is:

1. A workspace contains `AGENTS.md`, optional `agents/`, `skills/`, `scripts/`, and `cron/` definitions.
2. `rocketclaw.json` points RocketClaw at that workspace and enables one or more connectors.
3. RocketClaw builds the effective runtime assets from embedded defaults, configured git overlays, and local workspace overrides.
4. A human message, cron job, scheduled prompt, voice transcript, or MCP request enters RocketClaw.
5. RocketClaw creates or resumes the appropriate persistent conversation and invokes RocketCode with the selected agent.
6. RocketCode runs model/tool turns under the configured permissions.
7. RocketClaw publishes progress, final responses, files, reactions, or voice output back through the originating connector.
8. Conversation state, scheduled work, and routing metadata are persisted so the runtime can continue after restart.

For local CLI experimentation, `rocketcode` and `rocketloop` skip the connector layer and run RocketCode directly in the current working directory.

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

RocketClaw is configured with `rocketclaw.json` in the working directory.

Runtime state is intentionally local to the selected workspace:

- `.rocketclaw/state.sqlite3` stores sessions, connector routing, scheduled messages, external MCP sessions, restart notifications, and goal-loop state.
- `.rocketclaw/overlays/` stores configured git overlay clones for runtime assets.
- `.rocketclaw/.rocketcode/` stores RocketCode shell output and transient artifacts.

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
