---
name: main-update-rocketclaw-json
description: Use this skill to inspect or update setup fields of the rocketclaw configuration file (femtoclaw.json when it exists, else rocketclaw.json) for this rocketclaw session with jq
---
## Purpose

Use this skill whenever the human wants to inspect or modify setup fields of the configuration file RocketClaw loads from the current working directory: `femtoclaw.json` when it exists, else `rocketclaw.json`.

This skill covers setup fields only. It does not cover the Behavior Settings, which are stored in PostgreSQL and edited by a Web admin (a user listed in `web.admins`) on the Web Config page: `auto_approver_model`, `slack.channels`, `slack.ambient_decision_model`, `logging.level`, `overlays`, and `embedded_agents` (whether the built-in `main` and `voice` agents are on, and their models). After the first start, RocketClaw ignores their copies in the configuration file, so editing them in the file changes nothing and reading them from the file can be stale. The Web Config page shows their live values. When the human asks for a change to one of them, do not edit the file: tell the human to make the change on the Web Config page, where a save applies at once without a restart.

This skill exists to shorten the learning loop around routine rocketclaw configuration changes.

Use `jq` for both reads and writes.

Do not hand-edit the configuration file with markdown patches or generic text editing when this skill applies.

## Runtime path

RocketClaw loads `./femtoclaw.json` when it exists, even if `./rocketclaw.json` exists too, and `./rocketclaw.json` otherwise. Only use that file, in the current working directory, not some other repository or home-directory copy.

Each shell command starts fresh, so begin every command that touches the configuration file by picking it:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json
```

Then refer to the file as `"$cfg"`, as every example below does.

## Required inputs

For read-only inspection, you must know:

- what field, value, or section the human wants to inspect

For writes, you must know:

- what config change the human wants
- enough type information to express it correctly in `jq`

Ask only for the missing pieces.

## jq requirement

`jq` is mandatory for this skill.

Before using the skill, check that `jq` exists with:

- `command -v jq`

If `jq` is missing, stop and tell the human instead of falling back to manual editing.

If direct file tools are blocked for the configuration file, that is expected in some environments. Use `jq` through the shell anyway.

## File handling

If `"$cfg"` exists:

- use it as the source of truth
- for reads, query it with `jq`
- for writes, write through a temporary file, validate the result, and then replace the original atomically

If neither `./femtoclaw.json` nor `./rocketclaw.json` exists:

- do not invent a new config file silently
- tell the human the file is missing or ask what they want to do

## Safe jq patterns

Prefer passing values with `--arg` and `--argjson` instead of hard-coding shell-escaped data into the filter.

Examples:

Read a value:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; jq '.web.tsnet.hostname' "$cfg"
```

Set a string safely:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg v "rocketclaw" '.web.tsnet.hostname = $v' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

Set a boolean or number safely:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --argjson v true '.mcp_external.enabled = $v' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

Delete a key:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq 'del(.obsolete_key)' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

Append to an array:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg v "alice@example.com" '.web.admins += [$v]' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

If a write command fails after creating a temp file, clean up the temp file before finishing.

## Natural-language change handling

When the human asks for a config change in plain English:

- translate it into the smallest correct `jq` mutation
- preserve unrelated configuration
- preserve the correct JSON type
- if the target path or intended type is ambiguous, ask a clarifying question before writing

If the request touches a Behavior Setting, such as the auto-approver model, the title models, the Slack ambient decision model, a Slack channel's agents or allowed users, or the overlay list, do not write it with `jq`; point the human to the Web Config page instead.

Agents reach an MCP server's tools only through its facets: `mcp_servers.<server>.tools` maps each facet name (letters, digits, `_`, `-`) to a list of tool-name patterns without the server prefix, such as `"get_*"`. `*` matches any text and `?` one character; an entry starting with `-`, such as `"-get_secret_*"`, removes those tools from that facet wherever it appears. Facets may overlap, so a `sudo` facet of `["*"]` beside narrower `read` and `write` facets is fine. A tool in no facet is reachable by no agent, sudo agents included. An easy agent grants facets as `mcp: {<server>: [read]}`, an advanced one as `permission.mcp: {<server>: {read: allow, write: auto}}`; a facet an agent names that the server lacks fails agent loading (`server "<server>" has no "<facet>" facet`). Server names may not contain `.`. For example:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --argjson r '["get_*","list_*","-get_secret_*"]' --argjson w '["create_*"]' --argjson a '["*"]' '.mcp_servers.demo.tools = {read: $r, write: $w, sudo: $a}' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

`decision` configures decision-model providers, keyed `openai` or `typesafe`, each with its own `api_key` and an optional `api_base_url` (which includes the API version). Any other key, or a provider without `api_key`, fails config loading. An agent whose `approver.decision.model` or easy `decision` list names a provider missing here fails to load; an advanced `permission.decision` rule naming one still loads, but its calls return `stop_reason` `"error"`. `auto_approver_decision` is the decision model the built-in `guardian` asks first, `{"model": "<provider>/<model>", "min_confidence": 0.9}`; its provider must be under `decision`, and `min_confidence` is 0 to 1 (default 0.9). Ask the human for the API key; never invent or reuse a chat provider's key. The decision model reads only its own prompt, so before setting `auto_approver_decision`, tell the human that easy agents with `approver.prompt` but no `approver.decision.prompt` are not pre-gated, list them, and offer to copy their rules into `approver.decision.prompt` with `main-create-or-update-agent` so they are. For example, with the key the human gave in place of `<api-key>`:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg k '<api-key>' '.decision.typesafe = {api_key: $k} | .auto_approver_decision = {model: "typesafe/jev-latest", min_confidence: 0.9}' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

`permissions` maps a Tailscale login to the list of Web space names it may use, such as `{"contractor@example.com": ["Support", "Ops"]}`. A listed login sees only those spaces, their sessions, and the agents whose `space:` names one of them; logins not listed keep full access. Logins and space names match ignoring case. A blank login, one that starts or ends with spaces, the same login twice ignoring case, an empty space list, or a blank or space-padded space name fail config loading, so reuse an existing key's spelling. It takes effect on restart. Agents act with their own permissions: tell the human when an agent of that space has the session tools, a host shell, this skill, `sudo`, or `task` into other spaces, because it can reach beyond the space for whoever prompts it. For example:

```bash
cfg=rocketclaw.json; [ -e femtoclaw.json ] && cfg=femtoclaw.json; tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg l "contractor@example.com" --arg s "Support" '.permissions[$l] = ((.permissions[$l] // []) + [$s] | unique)' "$cfg" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "$cfg"
```

Use `jq` operations for arrays and object updates instead of rewriting large sections by hand.

If multiple related config changes belong together, prefer a single `jq` filter that performs all of them in one pass.

## Runtime effect

If the configuration file changed successfully, call `rocketclaw_restart` exactly once after the successful write. Do not call restart for read-only inspection or unrelated file edits.

RocketClaw always enables RocketCode automatic permission review for agent permission rules that use `auto` or `auto(<agent-name>)`; there is no configuration file flag for this behavior.

If the request was read-only:

- do not mention a restart unless the human asked

## Workflow

1. Determine whether the request is read-only or a write.
2. Pick the configuration file (`femtoclaw.json` when it exists, else `rocketclaw.json`), confirm it exists, and confirm `jq` is available.
3. Ask only for any missing path or type details.
4. For reads, query the file with `jq` and report the answer.
5. For writes, apply the `jq` mutation through a temp file, validate the result, and replace the original only after success.
6. If the configuration file changed successfully, finish all requested config edits first, then optionally tell the human you are applying them now and call `rocketclaw_restart` exactly once.

## Important

- Use `jq`, not manual text editing, for configuration file changes.
- Never edit Behavior Settings in the configuration file; ask the human to change them on the Web Config page.
- Use only the configuration file RocketClaw loads from the current working directory: `femtoclaw.json` when it exists, else `rocketclaw.json`.
- Do not guess missing JSON paths or intended types.
- Never leave a partial or invalid JSON file behind.
- Preserve unrelated configuration.
- Make all requested config changes before calling `rocketclaw_restart`; do not call it between intermediate edits.
- Only call `rocketclaw_restart` after a successful configuration file write.
- If no file changed, do not call `rocketclaw_restart`.
