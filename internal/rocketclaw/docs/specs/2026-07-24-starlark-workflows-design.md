# Starlark Workflows

## Goal

Add saved, bridge-native RocketClaw workflows written in Starlark. A human invokes a checked-in workflow with `$workflow <name> [args]`; the managed bridge runs it as the active foreground turn, shows phase progress in Slack, and records the command, final result when available, and compact terminal phase summary in managed conversation history.

## Files And Invocation

Workspace workflows live at `workflows/<name>.star` and materialize at `.rocketclaw/workflows/<name>.star`. Names are lowercase hyphenated stems no longer than 64 characters. `meta.name` must equal the filename stem.

```python
meta = {
    "name": "audit-routes",
    "description": "Audit route handlers and verify every finding",
    "phases": ["discover", "audit", "verify"],
}

researcher = worker(
    name = "researcher",
    instructions = "Find concrete evidence and cite exact files.",
    model = "coding-low",
    tools = ["read", "glob", "grep"],
)

def main(args):
    found = phase("discover", lambda: agent(
        "List route files under %s" % args,
        worker = researcher,
        schema = {
            "type": "object",
            "required": ["files"],
            "properties": {
                "files": {"type": "array", "items": {"type": "string"}},
            },
        },
    ))
    audits = phase("audit", lambda: pipeline(
        found["files"],
        lambda file: agent("Audit %s" % file, worker = researcher, label = file),
    ))
    return phase("verify", lambda: agent(
        "Verify and synthesize:\n%s" % (audits,),
        worker = researcher,
    ))
```

Bare `$workflow` shows available names and descriptions. A root app mention may create a managed thread and launch a workflow. The trimmed text after the workflow name becomes the `main(args)` string.

## Starlark Contract

- `worker(name, instructions, model=None, tools=None)` defines an immutable workflow-local worker. The model string names an existing configured model mapping. Its tools can only narrow the invoking managed agent's access.
- `agent(prompt, worker=None, label="", schema=None)` runs one fresh isolated RocketCode worker. Without a schema it returns text; with a schema it returns native Starlark JSON values.
- `parallel(callables)` runs zero-argument callbacks concurrently and preserves declaration order.
- `pipeline(items, fn)` runs one callback per item concurrently and preserves input order.
- `phase(name, fn)` executes one unique phase and drives one stable Slack task card. A run has at most 100 declared, dynamic, and implicit phases in total.
- Calls outside a phase belong to an implicit `run` phase.
- Nested `parallel` or `pipeline` calls are rejected by static validation and a runtime depth check. Authors flatten input before fan-out.
- `while` is enabled inside functions. Recursion, top-level control, global reassignment, and module loading are disabled.
- Top level permits only function definitions, literal JSON-compatible constants, literal `meta`, and pure literal `worker(...)` declarations.
- Only `None` and an empty string are silent. Strings render directly; other JSON-compatible values render as JSON.

The workflow source has no filesystem, shell, network, Slack, clock, random, or user-input API. Workflow-supplied instructions and prompts never expand ``!`shell` `` syntax.

## Worker Policy

Every `agent()` call starts from the active managed agent's model and permissions. A custom worker replaces the agent-specific prompt while retaining root workspace instructions. A model override uses an existing configured model key. A tool list is an exact subset of the available RocketCode tools.

Workflow workers do not receive `task` or RocketClaw behavior tools such as restart, reload, scheduling, goal updates, thread creation, questions, attachments, or direct delivery. Parallel workers share the checkout; workflow authors must assign disjoint writes.

## Execution And Limits

The managed bridge owns queue order, paired MCP locking, cancellation, and final delivery. The workflow engine owns Starlark evaluation and calls an injected isolated-agent runner. RocketCode remains unaware of workflows.

Each run permits up to 16 concurrent callback/agent workers, 1,000 callbacks and agent calls, 100 total phases, and a shared 10-million-step Starlark budget. Every concurrent callback uses a distinct `starlark.Thread`. Callback captures are frozen before publication. Results preserve source/input order.

Infrastructure errors cancel sibling work and fail the run. Model conclusions such as uncertainty or no findings are normal successful values. There is no Starlark catch API and no rollback of completed workspace side effects.

## Lifecycle

Workflow execution is in memory and foreground-only. The bridge serializes it with ordinary managed turns and `$stop` cancels it terminally. Daemon shutdown or crash ends the run; after restart the human invokes the workflow again. No call cache or resumable workflow state is added to SQLite.

Every started workflow persists one compact terminal summary containing its complete, error, stopped, and skipped phase outcomes. Later turns receive that summary as developer context. Successful entries also retain the normal paired user command and assistant result before final delivery. Intermediate worker prompts, values, tools, and reasoning never enter managed history.

## Slack

Workflow progress reuses `chat.startStream` with `task_display_mode=plan`. There is one stable task card per phase. Cards transition through pending, in-progress, complete, error, or the connector-neutral skipped state. Slack projects skipped phases as completed cards titled `phase · skipped`; fan-out titles show `complete/scheduled` progress without accumulating task details. Worker findings remain private to the script. The direct final result uses RocketClaw's separate answer message.

`$stop` remains the control. Slack Workflow Builder buttons are not used.

## Authoring Skill

The embedded skeleton includes `.rocketclaw/skills/main-create-or-update-workflow/SKILL.md`. It teaches effective and overlay paths, the DSL, flattened fan-out, direct results, permissions, shared-write ownership, and one atomic `rocketclaw_reload` after edits. Persisted agent-authored files requested by a human are supported; automatic ephemeral model-generated workflows are not.

## Out Of Scope

- Automatic or ultracode workflow generation.
- Saving ephemeral runs.
- Manual pause/resume or resume after `$stop`.
- Durable workflow recovery after daemon restart.
- Per-agent Slack cards or worker transcripts.
- Isolated Jujutsu workspaces and merge-back.
- Standalone workflow CLI or dashboard.
- Mid-run human questions.

## Verification And Budget

Implementation uses TDD and must pass `gofmt`, `go test ./...`, `make lint`, and `make test`. During implementation, the human explicitly approved raising the RocketClaw CLOC budget to 16,500; the RocketCode budget remains 9,000. Code must stay in its honest owning packages; if the complete behavior cannot fit, implementation stops and reports the conflict.
