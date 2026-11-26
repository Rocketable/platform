---
title: Wrapcheck Cross-Package Helper Error
date: 2026-08-31
last_updated: 2026-11-25
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: development_workflow
severity: low
resolution_type: code_fix
applies_when:
  - Deduplicating an identical helper by exporting it from a lower package for another package to call
  - A package returns an error that originated in another package
  - wrapcheck fails make lint if that error is returned unwrapped
  - "Reviewers ask to drop fmt.Errorf(\"%w\", err) with no extra prefix"
  - The returned error is a gRPC status whose message reaches a client
related_components:
  - internal/rocketclaw/protocol
  - cmd/rocketclaw
tags:
  - wrapcheck
  - dry
  - cloc
  - error-wrapping
---

# Wrapcheck Cross-Package Helper Error

## Context

Deduplicating an identical unexported helper by moving it into another package changes wrapcheck's view of the same `return err`. Same-package returns stay clean. Cross-package returns of the helper's error fail wrapcheck unless they are wrapped with `%w`.

This landed from [PR #27](https://github.com/Rocketable/platform/pull/27). That PR kept one copy of a shell-word helper in `internal/rocketclaw/protocol` and wrapped its error in `internal/rocketclaw/backend`. Both sides were deleted with goal check scripts in [PR #757](https://github.com/Rocketable/platform/pull/757). The rule did not change.

The same boundary shows up wherever backend returns another package's error. `holdRunLock` returns the error from pglock's `Client.Do`, and wraps it with no prefix because the caller adds none:

```1988:1993:internal/rocketclaw/backend/store.go
	err := client.Do(ctx, runLockName, func(lockCtx context.Context, _ *pglock.Lock) error {
		return work.Run(lockCtx)
	})
	if err != nil {
		return fmt.Errorf("%w", err)
	}
```

`fmt.Errorf("%w", err)` adds no prefix. `Error()` stays the same as the wrapped error; `errors.Is` / `errors.As` still work. wrapcheck fails on an unwrapped cross-package `return err`. The repo wrapping convention is `%w`, not a wrapcheck check for the `%w` verb itself.

wrapcheck is enabled for `internal/rocketclaw/...` (`internal/rocketclaw/.golangci.yml:49`; the `lint` target in `internal/rocketclaw/Makefile` lints `./...`, which excludes `cmd/rocketclaw`). Default wrapcheck does not report errors from same-package function calls. It does report errors returned from another package, and from exported interface methods even in the same package. Do not add `//nolint` or disable wrapcheck without human approval (`AGENTS.md`).

`runServe` calls `loadRuntimeConfig` (`cmd/rocketclaw/serve.go:34`), which already lives in package `main` (`cmd/rocketclaw/main.go:81`), and wraps with a real prefix: `fmt.Errorf("load config: %w", err)`.

## Guidance

When identical helpers show up across packages:

1. Keep one copy at the lowest layer that all callers can import.
2. Callers in the same package may `return err`. Callers in another package must wrap. Use `return fmt.Errorf("%w", err)` when there is no extra context to add. wrapcheck accepts other wraps too; `%w` is the repo convention.
3. Do not "clean up" that empty wrap to `return err`. wrapcheck will fail. Do not `//nolint:wrapcheck` without approval, and do not turn wrapcheck off.
4. If the duplicate is already in the same package, call it. Same-package reuse needs no wrap.

Empty `%w` is a lint-boundary adapter, not a message. Add a real prefix only when the call site has new context.

### Not for gRPC status errors whose message a client reads

Wrapping a gRPC status error, even with an empty `%w`, changes the message a reader gets back. `status.FromError` finds the wrapped status and keeps its code, but replaces the message with the wrapper's whole `err.Error()` (`vendor/google.golang.org/grpc/status/status.go`, `FromError`). So `status.Convert(fmt.Errorf("%w", err)).Message()` is `rpc error: code = Unknown desc = web reload: invalid staged assets` instead of `web reload: invalid staged assets`, and the Web API's JSON error body changes with it.

When a cross-package helper hands a gRPC error to code that reports it, return a `*status.Status` instead of an `error`: `status.New(code, msg)` for errors you create and `status.Convert(err)` for the call's error. The caller turns it back into an error with `.Err()`. wrapcheck only looks at returned `error` values, so it has nothing to flag, and the code and message stay exactly what the server sent. `invokeJSON` in `internal/rocketclaw/frontend/rpc/http.go` does this for both `POST /api/<Method>` and the `/rpc_mcp` tools, and `TestHTTPReload` pins the unchanged `/api` error body.

## Why This Matters

Reviewers treat `fmt.Errorf("%w", err)` as noise and ask to return `err`. That edit is a wrapcheck failure the moment the helper lives in another package. The next person "fixes" lint by putting the helper back, disabling wrapcheck, or adding `//nolint`. All three fight the repo rules.

Keeping one copy at the lowest layer avoids two copies drifting apart. Callers in other packages still have to wrap, because that layer is external to them for wrapcheck.

## When to Apply

- Deduplicating an unexported helper by exporting it from a lower package.
- Returning an error from `internal/rocketclaw/protocol`, a library such as pglock, or any other package out of `internal/rocketclaw/backend`.
- Reviewing a `fmt.Errorf("%w", err)` with no prefix in a wrapcheck-enabled module.
- Deduplicating identical functions in `internal/rocketclaw`. (`make lint` lints `./...` from `internal/rocketclaw`, so `cmd/rocketclaw` is not wrapcheck-checked today.)

Do not apply this to same-package returns. Do not treat it as a reason to copy `loadRuntimeConfig` into `serve.go`. Do not apply the empty `%w` to a gRPC status error whose message a client reads; return a `*status.Status` instead, as above.

## Evidence

Cross-package: backend must wrap pglock's error, even with no extra text (`holdRunLock`, quoted above).

Same-package reuse of `loadRuntimeConfig`, with a real wrap prefix at the serve call site:

```34:37:cmd/rocketclaw/serve.go
	selected, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
```

## Related

- [PR #27](https://github.com/Rocketable/platform/pull/27)
- [PR #757](https://github.com/Rocketable/platform/pull/757)
