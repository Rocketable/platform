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
- after all requested `agents/` overlay edits are complete, call `rocketclaw_restart` exactly once so the runtime reloads the updated agent definitions. Do not call restart for memory, ledger, audit, report, workspace, source-code, generated artifact, log, transcript, or data-file edits.
- never edit `.rocketclaw` directly

## Required inputs

For creation, you must know:
- target name under `agents/`, description, and purpose
- agent kind and exact opt-ins: bash command patterns, writable path patterns, skill names, task agent names, or another named permission bucket

For updates, you must know:
- the current agent name or top-level `agents/<name>.md` path, change type, and requested edits

For rename, you must also know:
- the target name under `agents/`

Ask only for the missing items.

## Permission handling

Do not ask the human to write raw `permission` YAML. For creation, ask what kind of agent they want and which exact opt-ins it needs: bash command patterns for execution agents, writable path patterns for file-modifying agents, skill names, task agent names, or another named permission bucket.

RocketCode denies all tools by default. Write only the explicit `allow` rules the agent needs. Do not write a top-level `"*": deny`, and do not add `"*": deny` as the first rule in every permission bucket. Use `deny` only after a broader `allow` when you need to subtract narrower access.

A scalar `permission: allow` does not grant global access. Use explicit permission buckets instead.

Built-in permission buckets:
- `read`: root-relative file paths for the `read` tool
- `glob`: requested glob patterns for the `glob` tool
- `grep`: requested search patterns for the `grep` tool
- `webfetch`: requested URLs for the `webfetch` tool
- `websearch`: coarse hosted web search toggle only
- `bash`: shell command call expressions parsed out of the command
- `edit`: root-relative file paths touched by `apply_patch`
- `skill`: skill names for `find_skills` and `skill`
- `task`: subagent names for the `task` tool

For edit-only agents, an `edit` allow also permits reading the same path unless a `read` rule matched first. Do not add a top-level deny that would block this fallback.

For bash agents, rocketcode checks permissions against each parsed shell call. Multi-command scripts need every parsed call allowed.

Safety rules enforced by `rocketclaw lint`:
- Write XOR execute: do not give one agent `edit` access to files that can influence its own `bash` calls.
- Read plus constrained execute can leak script internals into creative shell use; avoid letting an agent read scripts it can execute unless the human explicitly accepts the risk.
- Delegation chains can escalate access; do not let a writer agent delegate to another agent that can execute the written script.
- Web-capable agents using `websearch` or `webfetch` must not write files that internal/private agents read unless the human explicitly accepts external-content contamination risk.
- If the human accepts a specific risk, prefer precise `#nolint RCxxx: reason` on the contributing rule or field.

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
```

If the agent needs no tools, omit the `permission` block.

## Runtime and tool behavior

Account for these rocketcode runtime facts when designing agent instructions and permissions:

- RocketCode targets Unix-like systems only: Linux and macOS. Do not add Windows-specific agent instructions unless the human explicitly asks for a policy change.
- Built-in filesystem tools hard-deny env files such as `.env`, `.env.local`, and `.env.production` regardless of permissions. `.env.example` remains readable and editable.
- Symlink aliases are rejected for direct read, edit, and search targets. Directory grep pre-filters files before invoking `rg`; built-in `rg` calls use `--no-config` and `--no-follow`.
- The shell tool statically denies common direct env-file and external-path attempts such as `cat .env`, `cat ../outside`, and `cd /tmp`, but this is only a preflight guardrail, not an OS-enforced shell sandbox for dynamically generated paths.
- Prompt shell expansion with ``!`command` `` is opt-in per prompt source. Do not rely on it unless the runtime enables it.
- Prompt shell expansion runs from the workspace root, captures stdout only, honors `$SHELL` only when it is `sh`, `bash`, or `zsh`, and otherwise falls back to `sh`.

## Name and path handling

If the human gives only a simple agent name, convert it into a safe markdown filename:
- lowercase
- use hyphens between words
- avoid spaces and punctuation
- add the `.md` suffix

Do not create nested agent paths. Current rocketcode loads only top-level markdown files directly under `.rocketclaw/agents`.

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

The resulting frontmatter must include at least:
- `description`

The optional `maxRecursion` frontmatter field controls task subdelegation depth for inferences started with that agent:
- omitted or `-1` means unlimited subdelegation
- `0` disables subdelegation
- a positive integer caps delegation to that many task levels
- invalid values fail agent loading

Preserve any other valid frontmatter keys unless the human asked to change them.

The body of the file should contain the agent's purpose and operating instructions based on what the human asked for.

When updating an existing agent, preserve body content that the human did not ask to change.

The body must also end with an extra instruction that explicitly tells the agent to use `MEMORY.md`.

## Workflow

1. Determine whether the request is a create, in-place update, or rename.
2. Gather only the missing required inputs.
3. For an existing agent, read the current effective file from `.rocketclaw`.
4. Summarize the agent change you are about to make.
5. Create or update the overlay file in `agents/`.
6. If this is a rename and an old overlay file exists at the previous path, remove that old overlay file.
7. If you changed one or more `agents/` overlay files or relevant `scripts/` files, finish all requested edits first, run `rocketclaw lint`, resolve or explicitly suppress findings approved by the human, then optionally tell the human you are applying them now and call `rocketclaw_restart` exactly once.

## Important

- Do not write the file into `.rocketclaw`; write it into `agents/`.
- Always copy existing agent content from `.rocketclaw` into `agents/` before editing it.
- RocketCode denies unmatched permissions by default. Do not write top-level or per-bucket default deny rules.
- Do not write broad `edit: allow` or `bash: allow`; prefer exact `edit` or exact `bash` opt-ins, not both.
- Run `rocketclaw lint` after requested agent or relevant script edits and before `rocketclaw_restart`.
- Always end the agent instructions with an explicit instruction to use `MEMORY.md`.
- Make all requested agent-definition edits before calling `rocketclaw_restart`; do not call it between intermediate edits.
- Be truthful about overlay limitations when a rename cannot remove an old embedded source path.
- If no agent-definition file changed, do not call `rocketclaw_restart`.
