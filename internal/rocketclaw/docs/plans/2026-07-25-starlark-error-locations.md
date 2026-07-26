# Starlark Error Locations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Include the innermost workflow filename, line, column, and function in Starlark evaluation errors.

**Architecture:** Format `*starlark.EvalError` values only at the `workflow.Run` boundary. Select the innermost call frame with a real source filename and wrap the original error so callers retain error-chain inspection.

**Tech Stack:** Go 1.26, Starlark-Go, standard `errors` package, Go testing.

## Global Constraints

- Keep the operation prefixes `initialize workflow` and `call workflow` unchanged.
- Do not modify vendored Starlark or emit full backtraces.
- Non-Starlark infrastructure and context errors retain their current formatting.
- Preserve the underlying `*starlark.EvalError` in the returned error chain.
- Use `jj diff --git` for repository diffs and do not create a commit unless explicitly requested.

---

### Task 1: Add Source Locations To Workflow Evaluation Errors

**Files:**
- Modify: `internal/rocketclaw/workflow/engine.go:151-169`
- Test: `internal/rocketclaw/workflow/engine_test.go`

**Interfaces:**
- Consumes: `starlark.EvalError.CallStack`, whose last real-source frame is the innermost user frame.
- Produces: `workflowEvalError(error) error`, returning the original error wrapped as `<filename>:<line>:<column> in <function>: <original error>` when a source frame exists.

- [x] **Step 1: Write failing direct and callback error tests**

Add a test that executes source named `runtime/workflows/test.star`, triggers an error directly in `main`, and asserts the returned text contains `runtime/workflows/test.star:<line>:<column> in main: not enough arguments for format string`. Assert `errors.AsType[*starlark.EvalError](err)` still succeeds.

Add a callback case whose named `audit` function fails during `pipeline`; assert the location names `audit`, not a builtin or `main` frame.

- [x] **Step 2: Run the focused tests and verify RED**

Run:

```sh
go test ./internal/rocketclaw/workflow -run TestRunReportsStarlarkErrorLocations -count=1
```

Expected: FAIL because current errors contain only `call workflow: not enough arguments for format string`.

- [x] **Step 3: Implement minimal boundary formatting**

Add this private formatter in `engine.go` and apply it to `errInit` and `errCall` before existing wrapping:

```go
func workflowEvalError(err error) error {
	errEval, ok := errors.AsType[*starlark.EvalError](err)
	if !ok {
		return err
	}

	for {
		errNested, ok := errors.AsType[*starlark.EvalError](errEval.Unwrap())
		if !ok {
			break
		}
		errEval = errNested
	}

	for _, v := range slices.Backward(errEval.CallStack) {
		frame := v
		if frame.Pos.Filename() != "<builtin>" {
			return fmt.Errorf("%s in %s: %w", frame.Pos, frame.Name, err)
		}
	}

	return err
}
```

Use the formatter only on evaluation errors; preserve existing context-error joining and operation prefixes.

- [x] **Step 4: Run focused tests and verify GREEN**

Run:

```sh
gofmt -w internal/rocketclaw/workflow/engine.go internal/rocketclaw/workflow/engine_test.go
go test ./internal/rocketclaw/workflow -run TestRunReportsStarlarkErrorLocations -count=1
```

Expected: PASS.

- [x] **Step 5: Run repository verification**

Run:

```sh
go test ./...
make lint
make test
```

Expected: all commands pass, including race, coverage, and CLOC checks.

- [x] **Step 6: Inspect the final change**

Run:

```sh
go run golang.org/x/tools/gopls@latest check internal/rocketclaw/workflow/engine.go internal/rocketclaw/workflow/engine_test.go
jj diff --git
jj status
```

Expected: no diagnostics; only the intended workflow error formatting, tests, design, and plan are changed.
