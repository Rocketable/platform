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
jq '.slack.room' "rocketclaw.json"
```

Set a string value safely:

```bash
tmp=$(mktemp "${TMPDIR:-/tmp}/rocketclaw-json-XXXXXX") && jq --arg v "D0123456789" '.slack.room = $v' "rocketclaw.json" > "$tmp" && jq empty "$tmp" >/dev/null && mv "$tmp" "rocketclaw.json"
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

Use `jq` operations for arrays and object updates instead of rewriting large sections by hand.

If multiple related config changes belong together, prefer a single `jq` filter that performs all of them in one pass.

## Runtime effect

If `rocketclaw.json` changed successfully, call `rocketclaw_restart` exactly once after the successful write. Do not call restart for read-only inspection or unrelated file edits.

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
