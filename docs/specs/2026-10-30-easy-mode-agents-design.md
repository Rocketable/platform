# Easy Mode Agents

## Goal

Add a second agent file format, **easy mode**, chosen with `mode: easy` in the frontmatter. Easy mode is meant to become the default, and the current format (**advanced mode**) is meant to be deprecated later.

Easy and advanced are distinct formats, not one translated into the other. Both compile into a shared internal representation that the runtime uses.

The easy-mode rule, applied everywhere unless a key says otherwise:

- **A section left out (or set to `auto`)** turns on everything in it.
- **A section written out** turns on only what it lists. Everything else in that section is off.
- **Everything that is on is reviewed** by the agent's approver before it runs. Easy mode has no `allow`.

## Example

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
  - "arn::marketing-read"

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

## Non-Goals

- No translation layer from easy to advanced files, or the reverse.
- No migration of existing advanced files. Advanced keeps working unchanged, except for the `mode` validation and the global fixes listed under "Changes outside easy mode".
- No network wildcards, CIDR ranges, or per-entry HTTP methods. Easy mode supports exactly what webfetch's rules express.
- No network limits for `commands` programs or MCP servers.
- No warning when an easy-style file forgets `mode: easy`. It loads as advanced and ignores the easy keys; this confusion goes away when easy becomes the default.

## Selecting The Format

- R1. `mode` accepts `easy` or `advanced`. Omitted means `advanced` for now; the default flips to `easy` when advanced is deprecated.
- R2. Any other `mode` value fails loading, in both formats. This includes the OpenCode values `primary`, `subagent`, and `all`. No production code reads `mode` today; remove the `mode:` lines left in test fixtures (59 `primary`, 10 `subagent`, and 1 `invalid`, in 17 `_test.go` files). Production agent files are already clean, so no release note is needed.
- R3. Easy files are strict: any unknown or misspelled key fails loading.

## Keys

### Required

- R4. `model`: a real model name, `model` or `provider/model`, with an optional `#effort` suffix. The same syntax applies to every `subagents` entry and to `approver.model`.
- R5. `space`: a Web space name, with the same meaning as advanced. Required in easy files.
- R6. `workingDirectory`: see "Files and the emulated shell".

### Permission sections

Each follows the easy-mode rule. Every use is reviewed by the approver.

- R7. `subagents`
  - Omitted or `auto`: subagents may use only the agent's own model.
  - `[]`: no subagents.
  - A list: exactly these models. Each entry uses the `model` syntax (R4) and must resolve, or loading fails.
  - A subagent is a model, not an agent file. Easy agents delegate with the `task` tool, where `subagent_type` is replaced by:
    - `model`: one entry from this list, which the tool's description shows. Anything else is refused.
    - `tools`: tool names from the parent's own tools, as in workflow `worker(tools=…)`. Omitted means all of the parent's tools. A tool the parent lacks is refused.
  - The subagent inherits the parent's section limits, approver, `workingDirectory`, `commands`, and secrets unchanged.
  - Background runs, `continue`, and stopping work as for `task` today. `task` is offered to a subagent only while depth remains under `maxRecursion`.
- R8. `maxRecursion`
  - Omitted: `1`, so subagents cannot have subagents.
  - `N` (0 or more): N levels. `0` means no subagents, whatever `subagents` says.
  - Below 0 fails loading.
- R9. `mcp`
  - Omitted or `auto`: every tool of every server.
  - `{}`: no MCP.
  - `{server: [read]}`, `{server: [read, write]}`: that server's tools in those groups.
  - `{server: auto}`: every tool of that server, grouped or not.
  - `rocketclaw.json` gains, per server, `mcp_servers.<name>.tools.read` and `.tools.write`: lists of tool-name patterns using the existing permission wildcards. A tool matching both lists is a config error. A tool in neither list is reachable only through `auto`.
- R10. `skills`
  - Omitted or `auto`: all skills.
  - `[]`: none.
  - A list of folders under `skills/`: every skill inside each folder, at any depth. A skill's folder has the skill's name, so naming it enables just that skill. A folder that does not exist fails loading.
- R11. `rocketclaw`
  - Omitted or `auto`: all six groups below.
  - `[]`: none.
  - A list of group names: only those groups.

  | Group | Turns on |
  |---|---|
  | `sessions` | `rocketclaw_sessions` (list, get, current ID, URL, write) |
  | `pages` | `rocketclaw_store`, `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list`, `rocketclaw_kv_store`, `rocketclaw_kv_read` |
  | `tags` | `rocketclaw_set_tag` and `rocketclaw_get_tags`, with the groups from top-level `tags` |
  | `background` | background scripts and subagents, and stopping and steering them |
  | `workflow` | `rocketclaw_dynamic_workflow` |
  | `restart` | `rocketclaw_restart` |

  The tools that are always on and unreviewed under `sudo` stay always on and are not listed: reload, schedule and reset messages, attach files, update goal, and ask the user a question.
- R12. `tags`: tag groups as a list of lists, as in `permission.rocketclaw.rocketclaw_set_tag` today. Without a top-level `tags`, the `tags` group is off, even when `rocketclaw` is omitted or `auto`. When `rocketclaw` is an explicit list, listing the `tags` group without a top-level `tags`, or the reverse, fails loading.
- R13. `allowedNetworkTargets`
  - Omitted or `auto`: public addresses only. The address a name resolves to is checked, and that connection is used.
  - A list of hosts, `host:port` entries, and URL prefixes: those targets, with any HTTP method.
  - Wildcards, CIDR ranges, and per-entry `methods` fail loading.
- R14. `delegatedFrom`
  - Omitted or `true`: any agent may start, message, or stop this agent's chats.
  - `false`: none may.
  - A list of agent names: only those agents.

  This is the easy-mode name for advanced `permission.rocketclaw.rocketclaw_sessions_recipient`.

### Exceptions to "omitted means everything"

- R15. `commands`
  - A map from command name to host command line. Entries are trusted as written; there is no check for shell-like entries.
  - Omitted means no commands.
- R16. `secretsARN`
  - A list of AWS Secrets Manager ARNs.
  - Omitted means no secrets.

### Approver

- R17. `approver`: one approver per agent, used for every review in that agent.
  - Omitted: the built-in `guardian` on the global `auto_approver_model`.
  - `approver.model` replaces the model.
  - `approver.prompt` is added to the guardian prompt as tenant allow/deny rules. It never replaces the built-in rules or the answer format.
- R18. Review skips: the calls `sudo` runs without review today run without review in easy mode too (see `cmd/rocketclaw/CHEATSHEET.md`, `sudo`).

### Routines

- R19. `routines` is a list. Each entry is either a schedule string, or a one-entry map from schedule to instruction.
- R20. Schedules use the cron-file parser exactly: 5-field cron, Go durations such as `30m`, `@daily`-style shortcuts, or a one-off RFC 3339 time. `@every` and phrases such as `every 30min` fail loading.
- R21. An entry with an instruction sends that instruction as the run's prompt. A bare entry sends a fixed message naming the schedule, such as `Scheduled run (30m).`
- R22. Reporting matches cron files: an empty reply reports nothing, and any other reply opens a Web chat with the report, in the agent's `space`.
- R23. A routine is identified by its agent plus its schedule text. Editing an instruction keeps the identity; changing a schedule starts a new one. The same schedule twice in one agent fails loading.
  - Its runs record a path made of the agent file and the schedule, such as `agents/marketing.md#*/5 * * * *`, in the column cron runs use today. Run history, retention, and the Cron page work unchanged, with no new database column.
  - The Cron page shows each routine as its own job, labeled `marketing · */5 * * * *`, with its instruction (or the fixed message), next run times, and past runs.
  - `cron:marketing` matches every routine run of agent `marketing`. A cron file named `cron/marketing.md` matches the same search; that overlap is accepted.
- R24. Routines replace cron files for easy agents. A `cron/*.md` file whose `agent` is an easy agent fails loading.

### Other keys

- R25. `schema.output`: optional, with the same meaning as advanced.
- R26. Not allowed in easy files, so writing them fails loading under R3:
  - `description`
  - `reasoningEffort` (use the `model` suffix)
  - `verbosity`
  - `shell`
  - `permission`
  - `sudo`
  - `guardrail`
  - `additionalInstructions`
- R27. Easy agents never load the root `AGENTS.md`, and RocketClaw adds no reply-style line. The body is the agent's whole instruction set. Authors who want `AGENTS.md` write ``!`cat AGENTS.md` `` in the body.

## Files And The Emulated Shell

- R28. Easy agents always use the emulated shell (real Bash in a bubblewrap sandbox, docs/specs/2026-11-01-linux-native-just-bash-spec.md; it replaced just-bash). Host programs are reachable only through `commands`.
- R29. `workingDirectory` is required.
  - A relative path starts from RocketClaw's own working directory; an absolute path is used as written.
  - `$VAR`s may be used. An unset variable fails the command; it is never replaced by an empty string.
  - `ROCKETCLAW_CONVERSATION_ID` is made safe for a path where it is put into the path. The variable's own value does not change.
  - The existing check stays: the folder may not be or contain the workspace or `.rocketclaw`, nor lie inside `.rocketclaw`.
  - RocketClaw never deletes the folder.
- R30. The file tools `read`, `glob`, `grep`, `apply_patch`, and `rocketclaw_attach_files_to_response` are limited to `workingDirectory`, so they see the same files as the shell. There are two read-only exceptions. Each is visible at its real host path to both the file tools and the emulated shell (a read-only bind mount), so one path works everywhere:
  - **Uploads.** Files people upload to an easy agent's chat are saved under `.rocketclaw` (for example `.rocketclaw/uploads/<chat>/<id>/<name>`) instead of the workspace `artifacts/uploads/`. Only the current chat's folder is visible; subagents see their top-level chat's folder. To change an upload, the agent copies it into `workingDirectory`.
  - **Skills.** The folder of every skill the agent has, with all its reference files and scripts. Skill scripts run inside the sandbox. A host program a script calls (`gh`, …) works only if the sandbox has it or `commands` provides it under that name.

  Symlinks are not used for either exception. The sandbox's mounts and `os.Root` both refuse paths through symlinks that leave their root, and allowing symlinks would let the agent create its own links out of the sandbox. A fixed `workingDirectory` shared by many chats also cannot hold one link per chat.
- R31. The emulated shell sees only the `ROCKETCLAW_*` turn variables and `TMPDIR`. It never sees RocketClaw's process environment or any secret.
- R32. `commands` programs run in host bash with RocketClaw's environment plus every `secretsARN` secret. Keeping secrets apart between commands is the agent author's job.
- R33. ``!`…` `` lines in the body, and in the `SKILL.md` of any skill the agent loads, run unreviewed in host bash, outside the emulator, from the workspace folder, with RocketClaw's environment plus every `secretsARN` secret. Failures stay silent, as in advanced.
- R34. `allowedNetworkTargets` limits the emulated shell (`curl`, its only network access) and the `webfetch` tool, with identical rules: both use one client: the same origin and prefix matching, redirect blocking, and resolved-address private-range check under `auto`. One shared table of test cases runs against both. `webfetch` has no private-address check today, so it needs one.

## Shadows

- R35. A shadow's `mode` must match its parent's. A mismatched shadow fails loading.
- R36. Everything required in an easy parent is required in an easy shadow. A shadow's `space` may differ from its parent's.
- R37. A shadow may declare `routines`, but they are ignored. Only the parent's routines run.
- R38. Turns started by a routine run without shadows. Shadows join only turns started by a person or by another agent.

## Changes Outside Easy Mode

These were found while settling easy mode and apply to both formats.

- R39. `ROCKETCLAW_CONVERSATION_ID` is set on every turn. Today only External MCP turns get it (`internal/rocketclaw/backend/bridge.go`, `turnShellEnv`). As a result the README example `workingDirectory: <folder>/$ROCKETCLAW_CONVERSATION_ID` puts every non-MCP chat of an agent in the same folder.
- R40. Every permission review gets a trusted header written by RocketClaw, outside the transcript. It names the turn's source (web, slack, cron, mcp, scheduled message, agent) and, when known, the principal's name and email. Today the approver cannot tell a cron prompt from a person's request.
- R41. Remove the `rocketclaw.json` `models` section and the Go-template model lookup (`{{ model "name" }}`, `Config.RenderAgentModel`). Every model is written by its real name everywhere: agent `model`, `subagents`, `approver.model`, and workflow `worker(model=…)`, which today accepts only `models` names. Remove the Web settings model list, the README and CHEATSHEET text and examples, and the tests that exercise the lookup. Agent files and workflows that still use short names must be rewritten with real model names.

## Known Facts Behind The Decisions

- Agent frontmatter is decoded into a generic map. Unknown keys are silently ignored in advanced files today.
- RocketClaw never lists a provider's models.
- Skill names are flat and unique (`^[a-z0-9]+(-[a-z0-9]+)*$`), each equal to its folder name. Folders may nest at any depth under `skills/`.

## Documentation

README (Agents and permissions, Emulated shell, MCP and scheduled jobs, Models and providers) and `cmd/rocketclaw/CHEATSHEET.md` need an easy-mode section when this ships. The misleading `workingDirectory` example in the README needs updating with R39, and the `models` text and examples go with R41.
