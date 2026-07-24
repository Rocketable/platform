---
name: main-create-or-update-workflow
description: Use when creating, updating, renaming, reviewing, or troubleshooting RocketClaw workflows/*.star files
---

# Workflow Authoring

## Paths

- Effective path: `.rocketclaw/workflows/<name>.star`.
- Writable overlay: `workflows/<name>.star`. Never edit generated `.rocketclaw` content.
- Before updating or renaming, copy the complete effective file to the overlay, then edit.
- Use a lowercase hyphenated top-level stem of at most 64 characters. Match `meta.name` to it.
- Use only the workspace `.tmp/` directory for scratch files.

## Contract

Define literal `meta` with matching non-empty `name` and `description`, plus an optional unique phase list. Define `def main(args)` with one required argument. `args` is the trimmed text after `$workflow <name>`.

## Quick Reference

- `worker(name, instructions, model=None, tools=None)` defines a workflow-local worker. Omit `model` to inherit, or use an existing configured model mapping key. Omit `tools` to inherit, use `tools=[]` for no tools, never include `task`, and remember that `skill` also grants its required `find_skills` companion. Never invent a model, named agent file, or permission.
- `agent(prompt, worker=None, label="", schema=None)` runs one isolated worker, returning text or schema-shaped native Starlark JSON values.
- `parallel(callables)` concurrently runs zero-argument callbacks in declaration order.
- `pipeline(items, fn)` concurrently applies one callback per item in input order.
- `phase(name, fn)` executes one unique named phase once and reports its progress. Calls outside named phases use the implicit `run` phase even when strict metadata does not declare it. Declared, dynamic, and implicit phases count toward a limit of 100 total phases.

Instructions, models, and tools are workflow-local. `tools` only narrows the invoking agent; workers never receive RocketClaw behavior tools.

## Safety

- A nested fan-out is invalid: flatten inputs before calling `parallel` or `pipeline`.
- For write workflows, planning output has `parallel_batches` of disjoint file ownership sets and a separate `shared_files` integration set. Run `parallel_batches` workers first. After all parallel workers finish, run exactly one sequential worker for `shared_files`. If exclusive ownership is impossible, return `BLOCKED` instead of dispatching overlapping writers.
- Bound convergence loops; exit on success, no progress, blocked work, or attempt limit.
- Return the direct Slack result from `main`. Use a final synthesis worker when the result needs prose.
- Only `None` and empty string are silent. Other JSON-compatible results render as JSON.
- Prompt shell expansion is disabled for workflow worker instructions, input prompts, and loaded skill bodies, so syntax such as `` !`command` `` remains literal. This intentionally preserves the workflow permission boundary.
- Starlark has no direct filesystem, shell, network, Slack, or user-input API.
- `$stop` is terminal. Daemon shutdown loses in-memory progress; rerun the workflow.

## Complete Example

```python
meta = {
    "name": "audit-routes",
    "description": "Discover, audit, and verify route handlers",
    "phases": ["discover", "audit", "verify"],
}

route_schema = {
    "type": "object",
    "required": ["files"],
    "properties": {
        "files": {"type": "array", "items": {"type": "string"}},
    },
}

discoverer = worker(
    name = "discoverer",
    instructions = "Find route files in the requested scope.",
    tools = ["read", "glob", "grep"],
)
auditor = worker(
    name = "auditor",
    instructions = "Audit one route file and cite concrete evidence.",
    tools = ["read", "grep"],
)
verifier = worker(
    name = "verifier",
    instructions = "Verify findings and synthesize concise final prose.",
    tools = ["read", "grep"],
)

def main(args):
    found = phase("discover", lambda: agent(
        "List route files under %s" % args,
        worker = discoverer,
        schema = route_schema,
    ))
    audits = phase("audit", lambda: pipeline(
        found["files"],
        lambda file: agent("Audit %s" % file, worker = auditor, label = file),
    ))
    return phase("verify", lambda: agent(
        "Verify and synthesize these audits:\n%s" % (audits,),
        worker = verifier,
    ))
```

## Activation

Finish every requested workflow edit, then call `rocketclaw_reload` exactly once. Never restart merely for workflow changes. If reload reports validation errors, report them and do not claim the workflow is active.
