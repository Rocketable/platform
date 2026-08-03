---
name: main-create-or-update-council
description: Use when a human asks to create, update, rename, or review a RocketClaw council, coordinator, panel, board, fan-out, or group of sub-agents
---

# Council Authoring

## Runtime Shape

RocketClaw loads agent definitions from top-level `agents/*.md` files. A current council uses one coordinator and prefixed member agents:

```text
agents/scope-council.md
agents/scope-council-spec-auditor.md
agents/scope-council-minority-reporter.md
```

The coordinator name is the public council target. Member names are ordinary loaded agent names grouped by prefix. Use `main-create-or-update-agent` for each file and this skill for the roster and permission graph.

## Inputs

Ask for the council name, purpose, coordinator and member descriptions, models, member capabilities, caller agent files, direct surfaces, and any intentional delegation cycles. Ask for one item at a time.

Every agent needs a non-empty `description` and `model`. Write descriptions as useful selection guidance because the task roster displays them. Check the active root `maxRecursion` budget for each delegation path: caller -> coordinator -> member requires two task hops; a direct surface -> coordinator -> member requires one.

## Permission Graph

Start each agent with an explicit permission set and add only required capabilities.

1. **Caller to coordinator:** Add one exact task grant to each caller agent:

   ```yaml
   permission:
     task:
       "scope-council": allow
   ```

2. **Coordinator to members:** Add one exact task grant for every member to `scope-council.md`:

   ```yaml
   permission:
     task:
       "scope-council-spec-auditor": allow
       "scope-council-minority-reporter": allow
   ```

3. **Member capabilities:** Give each member its own `model`, instructions, and tools. Use `read`/`glob`/`grep` for analysts, narrow `edit` paths for editors, exact `bash` commands for test runners, and named `task` targets for further delegation.

Exact task rules keep the graph auditable. Wildcard rules widen the graph to every matching loaded agent. Follow `main-create-or-update-agent` for permission buckets, write/execute separation, web-content trust, guardrails, and lint.

## Coordinator Behavior

Write the coordinator body with the current member roster. Instruct it to:

- Emit independent `task` calls in the same model turn.
- Run independent work in parallel and sequence real dependencies.
- Put the complete synthesis in its final assistant message.
- Identify consulted, intentionally skipped, and unavailable members in the report.

RocketClaw dispatches up to 16 same-turn tool calls concurrently in its bridge runs. The coordinator body owns council-specific instructions. The parent receives the member's final assistant message as its task result.

Slack selectors use each channel's configured `agents` list. MCP and `rocketclaw exec` address loaded top-level agent names. Verify every configured surface name against `rocketclaw agent-graph next` before activation.

## File And Activation Workflow

1. Read each existing effective file from `.rocketclaw/agents/<name>.md`.
2. Copy existing files into matching `agents/<name>.md` overlays before editing; create new files in `agents/`.
3. Add exact caller-to-coordinator and coordinator-to-member task rules.
4. Run `rocketclaw lint` after all requested edits.
5. Use `rocketclaw_reload` for agent-only edits.
6. Use `main-update-rocketclaw-json` and `rocketclaw_restart` when `rocketclaw.json` or overlay-list configuration changes.
7. Complete review-only requests with a report and preserve the active runtime state.
8. Report the flat agent names, permission edges, recursion budget, surface names, lint result, and activation result.

Keep a one-way coordinator-to-member graph for ordinary councils. For an intentional cycle, give every participant a bounded `maxRecursion` value and resolve the corresponding `RC003` lint finding with the human's approval.
