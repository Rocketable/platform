# Starlark Error Locations

## Goal

Make workflow evaluation failures actionable by including the relevant workflow source location and function in the error delivered to the caller.

## Error Format

When workflow initialization or execution returns a `*starlark.EvalError`, RocketClaw reports the innermost non-builtin Starlark frame in this form:

```text
call workflow: workflows/find-and-summarize.star:78:81 in main: not enough arguments for format string
```

The existing operation prefix remains unchanged. Errors that do not contain a `*starlark.EvalError`, including infrastructure and context failures, keep their current formatting.

## Implementation

At the `workflow.Run` boundary, inspect initialization and call errors with `errors.AsType[*starlark.EvalError]`. Descend through nested evaluation errors produced when a callback failure crosses its parent Starlark call, then walk the deepest captured call stack from innermost to outermost and select the first frame whose filename is not the Starlark builtin pseudo-file. Wrap the original error with the selected position and function name so unwrapping behavior is preserved.

Do not modify vendored Starlark, emit the full backtrace, add logging, or change workflow execution semantics.

## Tests

Add focused behavioral tests covering:

- a direct failure in `main`, asserting the workflow filename, line and column, function name, and original message;
- a failure inside a fan-out callback, asserting the callback source frame rather than an internal builtin frame;
- preservation of the underlying `*starlark.EvalError` through the returned error chain.

Existing tests continue to cover non-evaluation infrastructure and cancellation errors.
