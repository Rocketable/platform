---
name: main-convert-agent-to-easy
description: YOU MUST USE THIS SKILL to convert a rocketclaw agent from advanced mode to easy mode, including its shadows and the cron files that run it
---
## Purpose

Use this skill when the human asks to convert, migrate, or switch an advanced agent to easy mode.

The two formats do not mean the same thing, so a conversion is never a pure rewrite. Map what maps, then ask the human about everything else before writing anything. Never guess a value this skill tells you to ask for.

Follow `main-create-or-update-agent` for the easy format itself (its "Easy mode agents" section), for overlay paths, and for `rocketclaw lint` and `rocketclaw_reload`. This skill adds the mapping and the checks around it.

## Runtime paths

- current effective agent: `.rocketclaw/agents/<name>.md`; its shadows: `.rocketclaw/agents/<name>/*.md`
- current effective cron files: `.rocketclaw/cron/*.md`
- writable overlays: `agents/`, `cron/`
- configured git overlay clones: `.rocketclaw/overlays/<clone>/agents/`, `.rocketclaw/overlays/<clone>/cron/`
- effective skills: `.rocketclaw/skills/`

Never edit `.rocketclaw` directly. Write no scratch files; if one is unavoidable, put it under `.tmp/` in the workspace.

## Step 1: find where everything comes from

For each agent to convert, read the effective file and find:

1. Its origin: the workspace overlay `agents/<name>.md`, a git overlay clone `.rocketclaw/overlays/*/agents/<name>.md` (later overlays in the `overlays` list of `rocketclaw.json` win), or the embedded skeleton.
2. Its shadows under `.rocketclaw/agents/<name>/`. A shadow whose mode differs from its parent's is dropped with a warning, so every shadow is converted too, with the same questions.
3. The cron files that run it: every `.rocketclaw/cron/*.md` (not `*.example.md`) whose `agent` is `<name>`, and the origin of each. For each local cron to delete, also inspect same-path copies in every git overlay and the embedded skeleton: deletion exposes the highest remaining layer, which may target this agent even if a lower copy does not.
4. Other agents that point at it: `task` rules whose subject matches `<name>` (wildcards included) and `auto(<name>)` reviewer rules in any effective agent.

Use `rg` over `.rocketclaw/agents` and `.rocketclaw/cron` for steps 3 and 4.

## Step 2: stop on blockers

Stop and tell the human, without writing anything, when:

- The agent is `main`, or any agent whose job is to edit `agents/`, `cron/`, `skills/`, `workflows/`, `scripts/`, `rocketclaw.json` or the workspace itself. An easy agent's files are limited to its `workingDirectory`, which may not be or contain the workspace, nor be, contain or lie inside those folders. Cron files without `agent` run `main`.
- A cron file that runs the agent comes from a git overlay or the embedded skeleton, including one exposed by deleting a local override. A cron file naming an easy agent fails startup and reload, and a workspace overlay cannot delete it. The human must remove it from its source first. Check the resulting layers before any writes, not just the currently effective cron files.
- Another agent uses it as a reviewer through `auto(<name>)`. Easy agents always have the shell and file tools, so they make poor reviewers. Ask whether to keep this agent advanced, or to change those rules first through `main-create-or-update-agent`.

## Step 3: map what maps

Start the new frontmatter with `mode: easy`. Write only easy keys. A section left out turns on everything in it, so when the advanced agent had none of a section, write that section empty (`{}` or `[]`) instead of leaving it out.

| Advanced | Easy |
| --- | --- |
| `model` | `model`, unchanged. If `reasoningEffort` is set and `model` has no `#effort` suffix, add `#<reasoningEffort>`. A model without `provider/` uses `openai`, which is always configured; any other provider must be under `providers` in `rocketclaw.json`. |
| `reasoningEffort` | Dropped after folding it into `model`. |
| `space` | `space`. Required: ask when missing. |
| `schema.output` | `schema.output`, unchanged. |
| `permission.task`, `maxRecursion` | No `task` allow, or `maxRecursion: 0`, means `subagents: []`. Otherwise ask (see "Questions"). Easy `maxRecursion` counts levels of the agent's own subagents; omitted means `1`, and `-1` fails loading. |
| `shell: emulated` with `workingDirectory`, `commands`, `allowedNetworkTargets` | Same three keys, unchanged. Drop `shell`. |
| `permission.webfetch` | `allowedNetworkTargets`, which limits both `webfetch` and the shell's `curl`. Automatically map only an equivalent origin/path-prefix allow, preserving scheme, port and path (`https://go.dev/*` becomes `https://go.dev/`, not `go.dev`). Easy matching ignores queries and uses path prefixes: a query restriction, exact path, wildcard host or non-prefix wildcard is not equivalent. Explain the extra access and ask whether to accept it or stay advanced; leave files unchanged until answered. A `*` allow means leave the key out (public addresses only). No rule means `allowedNetworkTargets: []` (no network), unless the human says its shell commands needed the network. |
| `permission.skill` | `skills`: the folders, relative to `.rocketclaw/skills/`, of every skill the rules allow; expand wildcards and denies against the folders that exist now. Only a plain `*` allow with no deny may be left out. No rule means `skills: []`. |
| `permission.mcp` | `mcp`: a plain `*` allow with no deny means leave the key out. `server.*` allowed with no deny becomes `server: auto`. Narrower rules need the server's tool groups, `mcp_servers.<name>.tools.read` and `.write` in `rocketclaw.json`: use `[read]` or `[read, write]` when a group matches what was allowed; otherwise ask. No rule means `mcp: {}`. |
| `permission.rocketclaw` | `rocketclaw`, as a list of the groups below that were on; `[]` when none was. Leave it out only when all six were on. |
| `permission.rocketclaw.rocketclaw_sessions_recipient` | `delegatedFrom`, same value. When absent, `true`, except `false` for a `sudo` agent. |
| `permission.rocketclaw.rocketclaw_set_tag` tag groups | `tags`, same lists, plus the `tags` group. |
| `permission.decision` | `decision`: the `provider/model` names the rules allow, expanding wildcards into the models the human names; their calls now run without review, so list each `auto` rule in the review question. No rule means leave the key out (no `decision` tool). |
| `approver.decision` | Dropped; tell the human. It applied only when another agent used this one as `auto(<name>)`. Never copy it into the easy `approver.decision`, which checks this agent's own calls. |
| `permission.rocketclaw.load_agents_md` | Not on means nothing. On, which is the default, means ask about a prompt-shell line reading `AGENTS.md` (see "Questions" and "Body"). |
| cron files that run it | `routines` (see "Cron files"). |
| body | Kept, with the changes under "Body". |

`rocketclaw` groups:

| Group | On when the advanced agent had |
| --- | --- |
| `sessions` | `rocketclaw_sessions` not `deny` |
| `pages` | any of `rocketclaw_store`, `rocketclaw_read`, `rocketclaw_delete`, `rocketclaw_list`, `rocketclaw_kv_store`, `rocketclaw_kv_read` allowed (the group turns on all six) |
| `tags` | `rocketclaw_set_tag` tag groups |
| `background` | `allow_background: allow` |
| `workflow` | any `permission.workflow` allow (the group turns on every workflow) |
| `restart` | `rocketclaw_restart: allow`; `auto` never allowed a restart, so it stays off |

`sudo: yes` agents already have everything on and reviewed, which is what easy does with sections left out. Leave the sections out, and still ask the questions below.

Dropped with no easy equivalent; tell the human each one that was set: `description`, `verbosity`, `guardrail`, `shell` (a wrapper path), `sudo`, `permission.rocketclaw.code_mode_approve` (easy reviews every whole script), a `websearch` deny (easy agents always have web search), and every `deny` rule. Also tell the human when the agent had no `bash`, `read`, `edit`, `glob` or `grep` rules: easy agents always have the shell and file tools, reviewed and limited to their folder.

## Questions

Collect every open question first, tell the human how many there are, then ask them one per turn. Ask only what applies to this agent:

1. `space`, when missing.
2. `workingDirectory`, when the agent was not `shell: emulated`. It becomes the only folder its shell and file tools can write. Suggest `sessions/<name>/$ROCKETCLAW_CONVERSATION_ID` (a folder per chat, so files do not carry between chats) or `sessions/<name>` (one folder shared by every chat).
3. Files: list the `read`, `edit`, `glob` and `grep` paths the agent had. It will see only its folder, its chat's uploads and its skills. Ask how it should reach each path: copy the data into its folder, a prompt-shell line in the body for read-only context, a `commands` entry, or keep the agent advanced.
4. Host programs: list the programs its `bash` rules allowed. Shell commands now run in the emulated shell (Ubuntu with the usual tools, `python3`, `node`, `sqlite3`, and `curl` limited by `allowedNetworkTargets`). For each program the sandbox lacks, such as `gh`, `aws`, `kubectl`, or a script under `scripts/`, ask for the `commands` entry: a name and the host command line it runs. A `commands` program runs in host bash from the agent's folder, not the workspace, so give a workspace script its absolute path. A `bash` allow narrower than its program, such as `kubectl rollout *`, widens to the whole program as a `commands` entry; propose approver rules that keep the narrowing. Agents with a `shell` wrapper path were sandboxed by it; ask the same question.
5. Secrets: `commands` programs already get RocketClaw's environment, as host bash did. Ask for `secretsARN` only when the human wants to add secrets.
6. Delegation: list the agents its `task` rules allowed. An easy agent can `task` only itself, on the models in `subagents`. Ask for the `subagents` models (`[]` for none) and `maxRecursion`; to reach the other agents it can use the `sessions` group with `rocketclaw_sessions_write`.
7. Callers: list the agents whose `task` rules reach it. No agent can `task` an easy agent; ask whether to switch those callers to `rocketclaw_sessions` through `main-create-or-update-agent`, or keep this agent advanced. Callers that switch must be in this agent's `delegatedFrom` when it is a list, or their writes are refused.
8. Review: everything turned on is now reviewed by one approver, the guardian, except skills, `task`, the `decision` models it lists, web search and the always-on RocketClaw tools (reload, scheduled messages, attaching files, goal updates, questions). List each other `allow` that skipped review, each `deny` rule, each `auto(<reviewer>)` rule, and each `auto` rule in `permission.decision`. Ask for `approver.prompt` rules that keep the intent of the denies (for example "Deny any `rm`"), and write each of them into `approver.decision.prompt` too: with `approver.prompt` alone the agent is never pre-gated by a decision model, and with both, the decision model reads only `approver.decision.prompt`. Ask whether `approver.model` should change. Leaving `approver.decision.model` out keeps the decision model `rocketclaw.json` sets, as the advanced agent had. Tell the human that the approver's prompt guides a model's review and is not a guaranteed block, unlike `deny`.
9. `AGENTS.md`: the agent loaded it, and easy agents do not. Ask whether to add a prompt-shell line running `cat AGENTS.md` to the body.
10. `additionalInstructions`, when set: ask whether to move its text into the body.
11. Each cron question under "Cron files".

## Cron files

Each local `cron/<file>.md` that runs the agent becomes routine entries:

- Each schedule becomes one entry, with the cron body as its instruction: `- "<schedule>": "<body>"`. A file with several schedules gives several entries with the same instruction. Use a YAML block scalar for a body with several lines.
- A one-off RFC 3339 schedule cannot be a routine. Ask whether to drop it, keep the agent advanced, or schedule it once later with `rocketclaw_schedule_message`.
- Routines report in the agent's `space`. When the cron file's `space` differs, ask which one to keep.
- The same schedule twice fails loading. When two cron files share a schedule, ask how to merge their instructions.
- Tell the human that past runs stay under `cron/<file>.md`, and routine runs are recorded as `agents/<name>.md#<schedule>`.

After writing the agent, delete each converted local `cron/<file>.md`, or reload fails.

## Body

- Keep the body's instructions.
- A prompt-shell expression is an exclamation mark (U+0021), immediately followed by a backtick (U+0060), the command, and a closing backtick. Construct it in the agent body; do not replace it with the command's current output.
- Advanced bodies end with an instruction to use `MEMORY.md`. Easy tools cannot reach `MEMORY.md`, which is in the workspace. Replace that instruction with a prompt-shell expression running `cat MEMORY.md`, and tell the human the agent can now read its memory but not update it.
- Every prompt-shell expression now runs in host bash from the workspace, with the agent's secrets. When the agent had a `shell` wrapper, these lines ran inside it before; tell the human they now run outside any sandbox.
- Add the prompt-shell expression running `cat AGENTS.md` and the `additionalInstructions` text as the human answered.

## Step 4: confirm, write, apply

1. Show the human the complete new file for the agent and each shadow, the routines, the files to delete, and a short list of what behaves differently. Write nothing until they confirm.
2. Write `agents/<name>.md` and each shadow `agents/<name>/<shadow>.md`. Delete the converted `cron/` files. Make the agreed changes to other agents through `main-create-or-update-agent`.
3. Run `rocketclaw lint` and fix its findings with the human.
4. Call `rocketclaw_reload` exactly once, after every edit.
5. When the agent came from a git overlay, tell the human that `agents/<name>.md` now overrides that overlay's copy, so later changes to the overlay's copy are hidden. Suggest committing the new file to the overlay repository and then deleting the workspace copy.
