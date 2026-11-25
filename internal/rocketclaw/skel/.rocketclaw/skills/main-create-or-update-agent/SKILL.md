---
name: main-create-or-update-agent
description: YOU MUST USE THIS SKILL to create, update, or rename a rocketclaw agent by editing agents/
---
## Purpose

Use this skill whenever the human wants to create, update, or rename a rocketclaw agent.

This includes:
- creating a brand new agent
- updating an existing agent in place
- renaming an existing agent

## Runtime paths

Only use these runtime paths:
- current effective agent: `.rocketclaw/agents/<...>.md`
- writable overlay agent: `agents/<...>.md`

`agents/` is the writable overlay directory. That means:
- read the current effective agent from `.rocketclaw`
- copy the current effective content from `.rocketclaw` into `agents/` before editing an existing agent
- make the requested edits in `agents/`
- after all requested `agents/` overlay edits are complete, call `rocketclaw_reload` exactly once so the runtime validates and reloads the updated agent definitions. Use `rocketclaw_restart` instead only when setup fields in `rocketclaw.json` changed. Model mappings, Slack channel agents, and the overlay list are Behavior Settings that an admin changes on the Web Config page; they apply on save and need neither reload nor restart. Do not call reload or restart for memory, ledger, audit, report, workspace, source-code, generated artifact, log, transcript, or data-file edits.
- never edit `.rocketclaw` directly

## Built-in agents

`main` is built into RocketClaw and has no file under `.rocketclaw/agents`. Never create, copy, edit, or rename anything to `agents/main.md` or `agents/main/`: RocketClaw ignores those files and logs a warning. An admin turns the built-in agent on or off and picks its model on the Web Config page, under Behavior settings. For any other change, create a new agent with another name.

No agent can delegate to `main`: never give an agent a `task` rule for it, an `auto(main)` reviewer, or a plan to write into its sessions.

## Required inputs

For creation, you must know:
- whether the agent is easy or advanced (see [Easy mode agents](#easy-mode-agents)); for easy agents, gather that section's inputs instead of the two items below
- target name under `agents/`, description, a real model name (optionally `provider/model` and `#effort`), and purpose
- agent kind and exact opt-ins: bash command patterns, writable path patterns, skill names, task agent names, or another named permission bucket
- whether the agent's own scripts need the `decision` tool, and which decision models; for a custom reviewer, whether its reviews should ask a decision model first, and what rules that model gets (see [Reviews and decision models](#reviews-and-decision-models))

For updates, you must know:
- the current agent name or top-level `agents/<name>.md` path, change type, and requested edits

For rename, you must also know:
- the target name under `agents/`

Ask only for the missing items.

## Permission handling

This section and the `sudo` and `description` guidance below apply to advanced agents only; easy agents have neither key. The `shell` guidance below applies to both, with the easy differences it names.

Do not ask the human to write raw `permission` YAML. For creation, ask what kind of agent they want and which exact opt-ins it needs: bash command patterns for execution agents, writable path patterns for file-modifying agents, skill names, task agent names, or another named permission bucket.

RocketCode denies all tools by default. Write only the explicit `allow` rules the agent needs. Use `auto` only when the human wants automatic model review for a matching operation instead of deterministic allow or deny. Do not write a top-level `"*": deny`, and do not add `"*": deny` as the first rule in every permission bucket. Use `deny` only after a broader `allow` when you need to subtract narrower access.

A scalar `permission: allow` does not grant global access. Use explicit permission buckets instead.

If the human wants an agent with every tool and feature, set `sudo: yes` and omit `permission`, except `permission.rocketclaw.rocketclaw_set_tag` tag groups and `permission.rocketclaw.rocketclaw_sessions_recipient`. Sudo offers every tool and sends every call to `guardian` for review, except skills, `task`, and the default-on RocketClaw tools. Also set a bounded `maxRecursion`, because a sudo agent can delegate to every agent, itself included.

Built-in permission buckets:
- `read`: root-relative file paths for the `read` tool
- `glob`: requested glob patterns for the `glob` tool
- `grep`: requested search patterns for the `grep` tool
- `webfetch`: requested URLs for the `webfetch` tool
- `websearch`: coarse hosted web search toggle only
- `bash`: shell command call expressions parsed out of the command
- `edit`: root-relative file paths touched by `apply_patch`
- `skill`: skill names for `find_skills` and `skill`
- `task`: subagent names for the `task` tool only
- `decision`: `provider/model` names the Code Mode `decision` tool may call, such as `"typesafe/*": allow`; scripts call `decision(model=..., state={...}, questions={...}, images=[...])` inside `execute`, where images are workspace files the agent may `read`; the tool exists only when `decision` providers are configured in `rocketclaw.json`
- `read` / `edit` / `glob` / `grep` / `webfetch` / `bash`: same subjects as before; unlock nested builtins inside Code Mode `execute` (not separate top-level tools)
- `mcp`: outbound MCP by facet, a map from a configured `mcp_servers` name to a map from one of its facets (`mcp_servers.<name>.tools` in `rocketclaw.json`) to `allow` or `auto`; unlocks Code Mode `execute` MCP builtins for the granted facets' tools. When granted facets overlap, the more careful level wins. Raw tool names, wildcards, `deny`, and `auto(<agent>)` fail loading, and so does an unknown server or facet
- `rocketclaw`: exact RocketClaw tool and setting names only; a wildcard key such as `"*"` or `rocketclaw_sessions_*`, or the scalar form `rocketclaw: allow`, fails agent loading
- `rocketclaw.code_mode_approve`: optional whole-script Code Mode approval before `execute` runs; `allow` by default, independent of other RocketClaw rules and nested tool grants
- `rocketclaw.allow_background`: `allow` lets the agent run `execute` scripts and `task` subagents in the background, steer, queue behind, or stop a running subagent through `task`'s `continue`, and gives it `rocketclaw_stop_background_job`; only `allow` or `deny`, `deny` by default, independent of other RocketClaw rules
- `rocketclaw.rocketclaw_sessions`: lets the agent read saved conversations and start, message, and stop other Web sessions; `deny` (default), `auto`, `auto(<agent-name>)`, or `allow`
- `rocketclaw.rocketclaw_sessions_recipient`: which agents may start, message, or stop this agent's sessions; `true` (default, except `false` for sudo agents), `false`, or a list of agent names

For edit-only agents, an `edit` allow also permits reading the same path unless a `read` rule matched first. Do not add a top-level deny that would block this fallback.

For bash agents, rocketcode checks permissions against each parsed shell call. Multi-command scripts need every parsed call allowed.

Automatic permission review rules use `auto` or `auto(<agent-name>)`. Bare `auto` uses RocketCode's embedded `guardian` reviewer. `auto(<agent-name>)` uses that loaded custom reviewer agent. RocketClaw always enables RocketCode automatic review; failed review, invalid reviewer output, recursive review, timeout, or rejection denies the tool call. Do not create or rename an agent to `guardian`, because that name is reserved for the embedded reviewer. Custom reviewer agents should normally use a `#low` model suffix, such as `model: gpt-6.1-sol#low`, because each automatic permission review has 90 seconds to return a valid decision. A custom reviewer may also set `approver.decision`: its reviews then ask that decision model first, with the reviewer's body plus `approver.decision.prompt` as the rules, and the reviewer runs only when the answers are not confident or consistent. See [Reviews and decision models](#reviews-and-decision-models).

If the human wants whole-script review rather than just reviews of nested calls, set `permission.rocketclaw.code_mode_approve: auto` or `auto(<agent-name>)`. Omit it (or set `allow`) when no whole-script review is requested.

If the human wants the agent to leave long scripts or subagents running while the conversation moves on, set `permission.rocketclaw.allow_background: allow`. Omit it otherwise. Any value other than `allow` or `deny` fails agent loading. Background work cannot ask questions, attach files, or restart RocketClaw.

If the human wants the agent to read other conversations, start Web sessions, or send messages into other sessions, set `permission.rocketclaw.rocketclaw_sessions`. This one key offers `rocketclaw_sessions_list`, `rocketclaw_sessions_get`, `rocketclaw_sessions_current_id`, `rocketclaw_sessions_get_url`, and `rocketclaw_sessions_write`, plus `rocketclaw_sessions_move`, which moves the agent's own session to another space for everyone, when the agent has no `space:`:

- `auto` or `auto(<agent-name>)`: every read and write is reviewed first; writes reach any chat Web lists, including Slack threads RocketClaw manages
- `allow`: the same, without review

Omit it otherwise; the default is `deny`. The reads cover every conversation, private ones included. Older per-tool keys such as `rocketclaw_sessions_get: allow` or `rocketclaw_sessions_write: allow` still load but grant nothing, and `rocketclaw_sessions_write: children` fails agent loading. For example:

```yaml
permission:
  rocketclaw:
    rocketclaw_sessions: auto
```

There is no separate stop tool: `rocketclaw_sessions_write` with `mode: stop` and an empty `prompt` stops a session. Each write that returns a `queue_item_id` comes back later as one `<session_reply>` note carrying the other session's answer, so tell the agent not to wait, poll, or write again for it.

If the human wants to protect an agent from other agents, set `permission.rocketclaw.rocketclaw_sessions_recipient` on the protected agent:

- `true` (default): any agent may start, message, or stop its sessions
- `false` (default for sudo agents): no agent may, not even another session of the same agent
- a list of agent names, such as `[main]`: only those agents may

The check uses the agent that makes the call, so a `task` subagent is checked as its own agent, not as its parent's. A refused write fails before anything is created, queued, or stopped.

The Hosted Page tools `rocketclaw_store`, `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list`, `rocketclaw_kv_store`, and `rocketclaw_kv_read` each need their own `allow` rule too. Grant them to agents that publish reports, dashboards, or graphs. Pages belong to the space of the chat that publishes them, which for an agent with a `space:` is that space; every chat in the space can replace them, so an agent that needs a stable address of its own publishes under a folder, such as `reporter/q3.html`.

Prefer exact `allow` for low-risk deterministic opt-ins. Use `auto` only for operations where the human wants a reviewer judgment at runtime, for example:

```yaml
permission:
  bash:
    "tmux *": allow
    "systemctl restart *": auto
    "kubectl apply *": auto(release-reviewer)
```

Safety rules:
- Write XOR execute: do not give one agent `edit` access to files that can influence its own `bash` calls.
- Read plus constrained execute can leak script internals into creative shell use; avoid letting an agent read scripts it can execute unless the human explicitly accepts the risk.
- Delegation chains can escalate access; do not let a writer agent delegate to another agent that can execute the written script.
- Web-capable agents using `websearch` or `webfetch` must not write files that internal/private agents read unless the human explicitly accepts external-content contamination risk.

Later matching rules override earlier matching rules, so allow broad access first and deny narrower subjects after it when a subtraction is required:

```yaml
permission:
  bash:
    "*": allow
    "rm *": deny
    "sudo *": deny
```

Write a permission block like this, deleting irrelevant buckets:

```yaml
permission:
  read:
    "README.md": allow
    "docs/*.md": allow
  glob:
    "**/*.go": allow
  grep:
    "TODO": allow
  webfetch:
    "https://go.dev/*": allow
  websearch: allow
  edit:
    "owned/path/**": allow
  bash:
    "exact command *": allow
  skill:
    "docs-helper": allow
    "go-*": allow
  task:
    "reviewer": allow
    "test-*": allow
  mcp:
    demo: {read: allow, write: auto}
  decision:
    "typesafe/*": allow
```

If the agent needs no tools, omit the `permission` block. Outbound MCP tools stay omitted until `permission.mcp` grants a facet of a configured server. A `sudo` agent gets every facet of every server, reviewed. A tool in no facet is reachable by no agent; if the agent needs one, add it to a facet with `main-update-rocketclaw-json`.

## Reviews and decision models

Every `auto` check goes to a reviewer, and the reviewer can be pre-gated by a decision model: a fast model that answers the review's questions with probabilities. Its answer settles the check only when every answer is confident and follows the guardian's outcome rules; otherwise the reviewer runs as usual. Whole-script checks (`code_mode_approve`) are pre-gated the same way.

| Agent and rule | Reviewer | Decision model asked first | Its rules |
| --- | --- | --- | --- |
| Advanced, `auto` (or `sudo`) | `guardian` on `auto_approver_model` | `auto_approver_decision.model` in `rocketclaw.json`, if set | the guardian's policy |
| Advanced, `auto(<agent-name>)` | that agent | its `approver.decision.model`, otherwise none | its body + its `approver.decision.prompt` |
| Easy, everything reviewed | `guardian` on `approver.model`, then `auto_approver_model` | `approver.decision.model`, then `auto_approver_decision.model`; none when `approver.prompt` is set without `approver.decision.prompt` | the guardian's policy + `approver.decision.prompt` |

The chat reviewer and the decision model each read their own prompt. When you write rules that must hold, put each rule in both: `approver.prompt` and `approver.decision.prompt` for an easy agent, or the body and `approver.decision.prompt` for a custom reviewer. A rule only one of them reads can be skipped. An easy agent with `approver.prompt` and no `approver.decision.prompt` is never pre-gated, so its rules always reach the guardian.

`approver.decision` takes `model` (`provider/model`), `minConfidence` (0 to 1; default `auto_approver_decision.min_confidence`, then 0.9) and `prompt`. Its provider, and the provider of every model in an easy agent's `decision` list, must be configured under `decision` in `rocketclaw.json`, or the agent fails to load. An advanced `permission.decision` rule naming an unconfigured provider still loads, but every call to it returns `stop_reason` `"error"`, so check its provider too. Use `main-update-rocketclaw-json` to add a provider.

An advanced agent's `approver:` takes only `decision`, and it applies only when another agent names this one in `auto(<agent-name>)`. It never changes how this agent's own calls are checked: those go to `guardian` on the config's settings, or to the custom reviewer the rule names. A custom reviewer may call its own `decision` tool while it reviews, with `allow` rules only.

Advanced agent:

```yaml
---
description: Release helper
model: gpt-6.1-sol#high
permission:
  rocketclaw:
    code_mode_approve: auto(release-reviewer)
  bash:
    "git *": auto
    "deploy *": auto(release-reviewer)
  decision:
    "typesafe/*": allow
---
```

Custom reviewer:

```yaml
---
description: Reviews deploys
model: gpt-6.1-sol#low
approver:
  decision:
    model: openai/gpt-6-luna
    minConfidence: 0.95
    prompt: Deny production deploys outside business hours.
---
Deny production deploys outside business hours. Allow staging deploys.
```

Easy agent:

```yaml
---
mode: easy
model: gpt-6.1-sol
space: Marketing
workingDirectory: sessions/marketing/$ROCKETCLAW_CONVERSATION_ID
approver:
  model: gpt-6.1-sol#low
  prompt: Never email outside the company.
  decision:
    model: openai/gpt-6-luna
    minConfidence: 0.85
    prompt: Never email outside the company.
decision: [typesafe/jev-latest]
---
```

## Runtime and tool behavior

Account for these rocketcode runtime facts when designing agent instructions and permissions:

- RocketCode runs on Linux only. Do not add macOS- or Windows-specific agent instructions unless the human explicitly asks for a policy change.
- Built-in filesystem tools hard-deny env files such as `.env`, `.env.local`, and `.env.production` regardless of permissions. `.env.example` remains readable and editable.
- Symlink aliases are rejected for direct read, edit, and search targets. Directory grep pre-filters files before invoking `rg`; built-in `rg` calls use `--no-config` and `--no-follow`.
- The shell tool statically denies common direct env-file and external-path attempts such as `cat .env`, `cat ../outside`, and `cd /tmp`, but this is only a preflight guardrail, not an OS-enforced shell sandbox for dynamically generated paths.
- `task` subagents never get `ask_user_question` or `rocketclaw_navigate_for_me`, whatever their permissions.
- Each `task` subagent runs as a session of its own, hidden from the sidebar, with its own permissions and Background Jobs; its `rocketclaw_sessions_write` and session tags act for its top-level session, but replies to its writes come back to the subagent itself. When a reply or one of its own Background Jobs wakes a subagent after its `task` call returned, its new answer reaches its parent as a note.
- Prompt shell expansion with ``!`command` `` works in agent bodies, routine instructions, the `SKILL.md` of loaded skills, and approver prompts. RocketClaw does not read `AGENTS.md` by itself; write ``!`cat AGENTS.md` `` in the body to include it. A command's output and the arguments passed to a skill stay literal.
- Prompt shell expansion runs from the workspace root and captures stdout only. The default runner uses `/bin/bash`, ignores `$SHELL`, and disables startup files and inherited shell functions/options. For an agent that sets `shell`, these lines run through that shell instead.

## Name and path handling

If the human gives only a simple agent name, convert it into a safe markdown filename:
- lowercase
- use hyphens between words
- avoid spaces and punctuation
- add the `.md` suffix

This skill is for individual top-level agents, which current RocketCode loads directly under `.rocketclaw/agents`. For a current flat council pattern, use `main-create-or-update-council` to coordinate the prefixed coordinator and member files.

The simple default write target is:

`agents/<name>.md`

When updating an existing agent, derive the overlay path by taking the effective runtime path in `.rocketclaw/agents/<name>.md` and writing the edited result into the matching path under `agents/<name>.md`.

When renaming an agent, write the updated copy to the new top-level target path in `agents/<new-name>.md`.

## File handling

If the requested agent already exists:

- read the current effective file from `.rocketclaw/agents/...`
- use that content as the starting point
- preserve existing instructions and frontmatter fields unless the human asked to change them
- write the updated result into `agents/`

If the requested change is a rename:

- read the source agent from `.rocketclaw`
- write the updated copy to the new target path in `agents/`
- if an old overlay file already exists at the previous `agents/` path, remove that old overlay file after writing the new one
- if the old source exists only in embedded `.rocketclaw` and there is no old overlay file to remove, be explicit with the human that the overlay can add the new target path but cannot delete the old embedded source path

If the requested agent does not exist, create it directly in `agents/`.

## File format

Create or update the agent as a markdown agent file with YAML frontmatter.

The resulting advanced frontmatter must include at least:
- `description`
- `model`

The `model` value is required. Use a real model name such as `gpt-6.1-sol` or `provider/model`, optionally with an `#effort` suffix such as `#high`. If the human has not specified one, ask before writing the file.

When updating an existing agent, preserve the existing non-empty `model` unless the human explicitly asked to change it. If the existing agent has no `model` or an empty `model`, ask the human which model to use and add it.

The optional `maxRecursion` frontmatter field controls task subdelegation depth for inferences started with that agent:
- omitted or `-1` means unlimited subdelegation
- `0` disables subdelegation
- a positive integer caps delegation to that many task levels
- invalid values fail agent loading

The optional `shell` frontmatter field runs every shell command for that agent instead of `/bin/bash`. Its value is either an absolute path to a program, called as `<shell> <command>`, or `emulated`, which runs the commands in a built-in bash emulator and requires `workingDirectory`. That includes the agent's ``!`…` `` prompt lines and those in the skills it loads. Any other value fails agent loading. In an easy agent, leaving `shell` out means `emulated`; a wrapper starts in its `workingDirectory` and gets its `secretsARN`, `commands`, `allowedNetworkTargets` and resolved `workingDirectory` as one JSON object in `ROCKETCLAW_ENVIRONMENT` instead of the secrets, the commands' programs and the `curl` limits. Either way, an easy agent's ``!`…` `` lines in its body and in the skills it loads run in host bash; only those in a routine's instruction run in its `shell`. When handling it:
- set, change, or remove `shell` only when the human explicitly asks, and use the exact value they give
- never create or edit a `shell` program inside the workspace
- carry `shell` over unchanged on updates and renames
- never give an agent `edit` permission over a sandboxed agent's file or its wrapper program; editing either can remove the sandboxing
- the setting covers only the agent that declares it: task subagents and permission reviewers without their own `shell` run plain `/bin/bash`. When the human asks to sandbox an agent, list the agents it delegates to, and ask whether each needs `shell` too

A `shell` program must:
- take the whole command as its only argument (there is no `-c`) and run it with bash, for example `bash --noprofile --norc -p -c "$1"`, because permission checks parse every command as bash
- treat that argument as data even when it starts with `-`: pass it as the `-c` argument or after `--`, never where the program or a sandbox tool parses options
- start with `#!/bin/bash -p` when it is itself a bash script
- carry its working directory and `TMPDIR` into the sandbox, with the workspace at the same path inside it
- forward only the environment variables commands need, because it receives the full RocketClaw environment, which can include provider credentials
- stop the sandboxed command when it receives `SIGTERM` at the timeout
- live, with any file it reads for configuration, outside the workspace and outside every path an agent can write

A missing program makes ``!`…` `` lines expand to empty text and makes `bash` calls fail with the start error. File tools and MCP servers still run outside the program.

The optional `schema.output` frontmatter field is a YAML JSON Schema for that agent's own final response. When present, RocketCode requests OpenAI structured output. Do not put it on permission-reviewer agents; those runs keep a fixed decision schema. Invalid schemas fail agent loading.

The optional `space` frontmatter field is one Web space name string. A Web session started with the agent is born in that space, which is created when missing, and a session whose agent has a `space` offers only agents with the same `space`, ignoring case, unless none of the agents its Slack channel allows has it; the agent's routines run in it too. Set or change it only when the human asks, and carry it over on updates and renames. A non-string value fails agent loading.

The optional `sticky` frontmatter field, in advanced and easy agents alike, is `off` (the default), `user`, `global` or `slack`; any other value fails agent loading. `user` keeps one Web session per person and `global` one shared by everyone: a new Web session with the agent opens that existing one instead. Such a session switches only to agents with the same `sticky` value and cannot be forked. A sticky agent is never another agent's `task` subagent or permission reviewer, though it may delegate, including to itself unless it is the built-in `main`. A `global` agent's routine runs report into its one session; a `user` agent cannot be scheduled, so never give it `routines`. `slack` makes the agent answer only in Slack: one Channel Session per channel and one Thread Session per thread; it is not offered for new Web sessions, cannot be scheduled either, and only `slack` agents answer in Slack channels. Set or change it only when the human asks, and carry it over on updates and renames.

The optional `routines` frontmatter field, in advanced and easy agents alike, lists the schedules the agent runs on by itself, and is the only way to schedule an agent; to add or change them, use `main-update-cron-or-heartbeat`. Carry it over on updates and renames. Renaming an agent leaves its earlier routine runs under the old name.

Preserve any other valid frontmatter keys unless the human asked to change them.

The body of the file should contain the agent's purpose and operating instructions based on what the human asked for.

When updating an existing agent, preserve body content that the human did not ask to change.

The body must also end with an extra instruction that explicitly tells the agent to use `MEMORY.md`.

## Easy mode agents

An agent file with `mode: easy` uses a shorter format. Write it when the human asks for an easy agent, or for one that works in its own folder, turns whole features on or off, and has every action reviewed by an approver. Otherwise write the advanced format above. Leaving `mode` out means advanced; any value other than `easy` or `advanced` fails loading.

A section left out, or set to `auto`, turns on everything in it, reviewed by the agent's approver; a written section turns on only what it lists. The approver reviews each `execute` script once, whole, and that approves every call the script makes. For creation, ask for the target name, `model`, `space`, `workingDirectory` and purpose, and which sections to narrow. Do not ask for a description, permission opt-ins, `shell` or `sudo`.

Keys:
- `mode: easy`
- `model` (required): a real model name, optionally `provider/model` and `#effort`; its provider must be configured in `rocketclaw.json`
- `space` (required): Web space name
- `sticky`: `off` (default), `user`, `global` or `slack`, as for advanced agents
- `workingDirectory` (required): the agent's own folder, the only one its shell and file tools can write, for example `sessions/<name>/$ROCKETCLAW_CONVERSATION_ID`; relative to RocketClaw's working directory; it may not be or contain the workspace, nor be, contain or lie inside `.rocketclaw`, `agents`, `skills` or `scripts`, wherever a link puts them
- `subagents`: models `task` may run this agent on, each like `model`; `[]` for none; default its own `model`. An entry ending in `*` (quote it in YAML, such as `"anthropic/*"`) allows every model whose name, `#effort` included, starts with the text before the `*`; a `*` anywhere else fails loading
- `maxRecursion`: subagent levels, 0 or more; `0` turns `task` off; default `1`
- `approver`: `model` replaces the review model, `prompt` is added to the guardian's rules, and `decision` (`model`, `minConfidence`, `prompt`) replaces the decision model reviews ask first and gives it its own rules; default `guardian` on `auto_approver_model`, after `auto_approver_decision.model` when set; see [Reviews and decision models](#reviews-and-decision-models)
- `decision`: decision models the Code Mode `decision` tool may call, each `provider/model` with its provider configured under `decision`; calls run without review; default no `decision` tool
- `secretsARN`: Secrets Manager ARNs, fetched each turn, given only to `commands` programs and the ``!`…` `` lines in the body and in loaded skills
- `commands`: map from command name to the host command line it runs
- `mcp`: a map from server to a list of its facets, such as `{demo: [read]}`; facets come from `mcp_servers.<name>.tools` in `rocketclaw.json` (use `main-update-rocketclaw-json`); left out or `{}` means no MCP tools, unlike the other sections; `auto` and an unknown server or facet fail loading
- `opencode`: a list of OpenCode servers configured under `opencode` in `rocketclaw.json`, such as `[build-box]`, that the `opencode_api` tool may call, each call reviewed; it can run commands and change files, settings and credentials there, so add a server only when the human asks for it by name; left out or `[]` means no `opencode_api` tool, unlike the other sections; `auto` and an unknown server fail loading
- `rocketclaw`: `[]` for none, or a list of the groups `sessions`, `pages`, `tags`, `background`, `restart`; default all five
- `tags`: tag groups as a list of lists; when `rocketclaw` is a list, `tags` and its `tags` group need each other
- `skills`: `[]` for none, or folders under `skills/`; a missing folder fails loading; default all skills
- `allowedNetworkTargets`: hosts, `host:port` entries and URL prefixes; wildcards and CIDR ranges fail loading; default public addresses only
- `routines`: the agent's schedules; use `main-update-cron-or-heartbeat`
- `delegatedFrom`: `true` (default), `false`, or the agent names that may start, message or stop its chats
- `schema.output`: as for advanced agents

Any other key fails loading with `<key>: not an easy-mode key`, including the advanced keys `description`, `reasoningEffort`, `verbosity`, `shell`, `permission`, `sudo` and `additionalInstructions`. `approver` takes only `model`, `prompt` and `decision`, `approver.decision` only `model`, `minConfidence` and `prompt`, and `schema` only `output`.

Each `commands` entry becomes a program of that name in the agent's shell. A plain call of that name runs it instead of any tool or builtin with the same name; `builtin`, `command` or a full path such as `/bin/echo` still reach the original. Name it with 1-64 letters, digits, `_`, `.`, `+` or `-`, starting with a letter, digit or `_` and not with `rocketclaw`. Calling `<name> a b` runs `<command line> "$@"` in host bash with `a` and `b` as `$@`:

- each argument arrives as one word and is not expanded again
- the line's `$VAR`s come from RocketClaw's environment, the turn's `ROCKETCLAW_*` variables and the secrets, never from variables the shell sets or exports
- the arguments go only to the line's last command, so a line with `;`, `&&` or `|` that needs them elsewhere is written `bash -c '<script using "$1" or "$@">' <name>`
- it runs in the host folder that matches the shell's current folder inside `workingDirectory`, or in `workingDirectory` itself when the shell is elsewhere

In the shell an entry chains like any program: pipes, redirects, `&&`, `||`, `$(…)` and loops work, input and output stream through, and its exit code comes back. Each call takes at most 16 MiB of input, or that call fails with exit code 126. All calls in one `bash` run together return at most 16 MiB of output, stdout and stderr counted; at that limit the whole run stops with exit code 137. The agent's tools do not list its `commands`, so name each one in the body with what it takes and an example call.

```yaml
commands:
  pr: gh pr
  restart: bash -c 'kubectl --context prod rollout restart "deployment/$1" && kubectl --context prod rollout status "deployment/$1"' restart
```

With these, `pr view 12 --json title | jq -r .title` runs `gh pr view 12 --json title` on the host and filters it in the shell, and `restart api && pr comment 12 --body "api restarted"` runs the second call only when the first succeeds.

The body is the agent's whole instructions, and easy agents' tools see only `workingDirectory`. ``!`…` `` lines in the body run in host bash from the workspace, so end the body with ``!`cat MEMORY.md` `` instead of the `MEMORY.md` instruction, and add ``!`cat AGENTS.md` `` when the human wants it included.

``!`…` `` lines also work in `approver.prompt` and `approver.decision.prompt`, for example to load rules from a file. They run in host bash from the workspace without the agent's secrets, run again on every review, so a slow one slows every review, and expand to empty text when they fail, which drops the rule without warning. Write such a prompt as a block scalar (`prompt: |`). YAML reads a value that starts with `!` as a tag: ``prompt: !`cat rules.md` `` fails to load, and `prompt: !foo bar` loads silently as `bar`.

```yaml
---
mode: easy
model: openai/gpt-6.1-sol#high
space: Marketing
workingDirectory: sessions/marketing/$ROCKETCLAW_CONVERSATION_ID
approver:
  prompt: "Deny write actions the user did not explicitly request."
  decision:
    prompt: "Deny write actions the user did not explicitly request."
mcp:
  acme_google_marketing: [read]
rocketclaw: [sessions, pages]
skills:
  - marketing
routines:
  - "0 9 * * 1-5": "Check the campaign dashboard for anomalies."
---

Agent instructions.

!`cat AGENTS.md`

!`cat MEMORY.md`
```

When the effective file has `mode: easy`, keep it easy: never add advanced keys, and carry over its sections, `space`, `workingDirectory` and `routines` unless the human asked to change them. Switch an agent between modes only when the human asks, and then write only the new mode's keys. To convert an advanced agent to easy, use `main-convert-agent-to-easy`.

## Workflow

1. Determine whether the request is a create, in-place update, or rename.
2. Gather only the missing required inputs.
3. For an existing agent, read the current effective file from `.rocketclaw`.
4. Summarize the agent change you are about to make.
5. Create or update the overlay file in `agents/`.
6. If this is a rename and an old overlay file exists at the previous path, remove that old overlay file.
7. If the agent names a decision model, check that its provider is under `decision` in `rocketclaw.json`; if not, add it with `main-update-rocketclaw-json` before reloading.
8. If you changed one or more `agents/` overlay files or relevant `scripts/` files, finish all requested edits first, then optionally tell the human you are applying them now and call `rocketclaw_reload` exactly once. Use `rocketclaw_restart` only when setup fields in `rocketclaw.json` changed.

## Important

- Do not write the file into `.rocketclaw`; write it into `agents/`.
- Always copy existing agent content from `.rocketclaw` into `agents/` before editing it.
- RocketCode denies unmatched permissions by default. Do not write top-level or per-bucket default deny rules.
- Do not write broad `edit: allow` or `bash: allow`; prefer exact `edit` or exact `bash` opt-ins, not both.
- Always end the agent instructions with an explicit instruction to use `MEMORY.md`, or, for easy agents, with ``!`cat MEMORY.md` ``.
- Never add advanced keys to a `mode: easy` file.
- Never write `approver.model` or `approver.prompt` in an advanced file; it takes only `approver.decision`, and only for its reviews of other agents.
- Write each review rule that must hold in both the chat reviewer's prompt and `approver.decision.prompt`.
- Make all requested agent-definition edits before calling `rocketclaw_reload` or `rocketclaw_restart`; do not call either between intermediate edits.
- Be truthful about overlay limitations when a rename cannot remove an old embedded source path.
- If no agent-definition file changed, do not call `rocketclaw_restart`.
