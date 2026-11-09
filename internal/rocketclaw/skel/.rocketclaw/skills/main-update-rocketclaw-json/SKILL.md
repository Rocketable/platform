---
name: main-update-rocketclaw-json
description: Use this skill to inspect or update rocketclaw.json (configuration file) for this rocketclaw session with jq
---
## Purpose

Use this skill whenever the human wants to inspect or modify `rocketclaw.json` in the current working directory.

This skill exists to shorten the learning loop around routine rocketclaw configuration changes.

Use `jq` for both reads and writes.

Do not hand-edit `rocketclaw.json` with markdown patches or generic text editing when this skill applies.

## Runtime path

Only use this runtime path:

- `./rocketclaw.json`

This means the `rocketclaw.json` in the current working directory, not some other repository or home-directory copy.

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

If direct file tools are blocked for `rocketclaw.json`, that is expected in some environments. Use `jq` through the shell anyway.

## File handling

If `./rocketclaw.json` exists:

- use it as the source of truth
- for reads, query it with `jq`
- for writes, write through a temporary file, validate the result, and then replace the original atomically

If `./rocketclaw.json` does not exist:

- do not invent a new config file silently
- tell the human the file is missing or ask what they want to do

## Safe jq patterns

Prefer passing values with `--arg` and `--argjson` instead of hard-coding shell-escaped data into the filter.

Examples:

Read a value:

```bash
jq '.slack.channels' "rocketclaw.json"
```

Set the first configured channel safely:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg v "#ops" '.slack.channels[0].channel = $v' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

Set a boolean or number safely:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --argjson v true '.some_flag = $v' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

Delete a key:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq 'del(.obsolete_key)' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

Append to an array:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg v "new-word" '.reaction_words += [$v]' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

If a write command fails after creating a temp file, clean up the temp file before finishing.

## Natural-language change handling

When the human asks for a config change in plain English:

- translate it into the smallest correct `jq` mutation
- preserve unrelated configuration
- preserve the correct JSON type
- if the target path or intended type is ambiguous, ask a clarifying question before writing

Slack uses direct `slack.channels` mappings. Each mapping requires `channel`, a non-empty ordered `agents` list, and non-empty `allowed_user_ids`. An `@` row is a hail fallback, not a Slack channel. Slack only answers mentions: cron and External MCP never post there. One External MCP conversation ID remains bound to one private MCP session and one Web chat. MCP history shows in that chat, but chat history does not copy back.

An easy agent's `mcp: {<server>: [read]}` or `[read, write]` needs that group in `mcp_servers.<server>.tools`: `tools.read` and `tools.write` are lists of tool-name patterns without the server prefix, such as `"get_*"`. Keep the groups disjoint: the same pattern in both, or a pattern in one matching an entry of the other, fails config loading (`tools.read "…" and tools.write "…" match the same tool`); a tool in neither group is reachable only through `auto`. A group an easy agent names that is empty or missing fails agent loading (`mcp: server "<server>" has no "<group>" tool group`). For example:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --argjson r '["get_*","list_*"]' --argjson w '["create_*"]' '.mcp_servers.demo.tools = {read: $r, write: $w}' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

`decision` configures decision-model providers, keyed `openai` or `typesafe`, each with its own `api_key` and an optional `api_base_url` (which includes the API version). Any other key, or a provider without `api_key`, fails config loading. An agent whose `approver.decision.model` or easy `decision` list names a provider missing here fails to load; an advanced `permission.decision` rule naming one still loads, but its calls return `stop_reason` `"error"`. `auto_approver_decision` is the decision model the built-in `guardian` asks first, `{"model": "<provider>/<model>", "min_confidence": 0.9}`; its provider must be under `decision`, and `min_confidence` is 0 to 1 (default 0.9). Ask the human for the API key; never invent or reuse a chat provider's key. The decision model reads only its own prompt, so before setting `auto_approver_decision`, tell the human that easy agents with `approver.prompt` but no `approver.decision.prompt` are not pre-gated, list them, and offer to copy their rules into `approver.decision.prompt` with `main-create-or-update-agent` so they are. For example, with the key the human gave in place of `<api-key>`:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg k '<api-key>' '.decision.typesafe = {api_key: $k} | .auto_approver_decision = {model: "typesafe/jev-latest", min_confidence: 0.9}' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

`permissions` maps a Tailscale login to the list of Web space names it may use, such as `{"contractor@example.com": ["Support", "Ops"]}`. A listed login sees only those spaces, their sessions, and the agents whose `space:` names one of them; logins not listed keep full access. Logins and space names match ignoring case. A blank login, one that starts or ends with spaces, the same login twice ignoring case, an empty space list, or a blank or space-padded space name fail config loading, so reuse an existing key's spelling. It takes effect on restart. Agents act with their own permissions: tell the human when an agent of that space has the session tools, a host shell, this skill, `sudo`, or `task` into other spaces, because it can reach beyond the space for whoever prompts it. For example:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg l "contractor@example.com" --arg s "Support" '.permissions[$l] = ((.permissions[$l] // []) + [$s] | unique)' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
```

Use `jq` operations for arrays and object updates instead of rewriting large sections by hand.

If multiple related config changes belong together, prefer a single `jq` filter that performs all of them in one pass.

## Runtime effect

If `rocketclaw.json` changed successfully, call `rocketclaw_restart` exactly once after the successful write. Do not call restart for read-only inspection or unrelated file edits.

RocketClaw always enables RocketCode automatic permission review for agent permission rules that use `auto` or `auto(<agent-name>)`; there is no `rocketclaw.json` flag for this behavior.

If the request was read-only:

- do not mention a restart unless the human asked

## Workflow

1. Determine whether the request is read-only or a write.
2. Confirm that `./rocketclaw.json` exists and `jq` is available.
3. Ask only for any missing path or type details.
4. For reads, query the file with `jq` and report the answer.
5. For writes, apply the `jq` mutation through a temp file, validate the result, and replace the original only after success.
6. If `rocketclaw.json` changed successfully, finish all requested config edits first, then optionally tell the human you are applying them now and call `rocketclaw_restart` exactly once.

## Important

- Use `jq`, not manual text editing, for `rocketclaw.json` changes.
- Use the `rocketclaw.json` in the current working directory only.
- Do not guess missing JSON paths or intended types.
- Never leave a partial or invalid JSON file behind.
- Preserve unrelated configuration.
- Make all requested config changes before calling `rocketclaw_restart`; do not call it between intermediate edits.
- Only call `rocketclaw_restart` after a successful `rocketclaw.json` write.
- If no file changed, do not call `rocketclaw_restart`.
