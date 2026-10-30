---
title: Wrapcheck Cross-Package Helper Error
date: 2026-08-31
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

```2101:2106:internal/rocketclaw/backend/store.go
	err := client.Do(ctx, runLockName, func(lockCtx context.Context, _ *pglock.Lock) error {
		return work.Run(lockCtx)
	})
	if err != nil {
		return fmt.Errorf("%w", err)
	}
```

`fmt.Errorf("%w", err)` adds no prefix. `Error()` stays the same as the wrapped error; `errors.Is` / `errors.As` still work. wrapcheck fails on an unwrapped cross-package `return err`. The repo wrapping convention is `%w`, not a wrapcheck check for the `%w` verb itself.

wrapcheck is enabled for `internal/rocketclaw/...` (`internal/rocketclaw/.golangci.yml:49`; `internal/rocketclaw/Makefile:25` lints `./...`, which excludes `cmd/rocketclaw`). Default wrapcheck does not report same-package returns. It does report errors returned from another package. Do not add `//nolint` or disable wrapcheck without human approval (`AGENTS.md`).

`runServe` calls `loadRuntimeConfig` (`cmd/rocketclaw/serve.go:33`), which already lives in package `main` (`cmd/rocketclaw/main.go:82`), and wraps with a real prefix: `fmt.Errorf("load config: %w", err)`.

## Guidance

When identical helpers show up across packages:

1. Keep one copy at the lowest layer that all callers can import.
2. Callers in the same package may `return err`. Callers in another package must wrap. Use `return fmt.Errorf("%w", err)` when there is no extra context to add. wrapcheck accepts other wraps too; `%w` is the repo convention.
3. Do not "clean up" that empty wrap to `return err`. wrapcheck will fail. Do not `//nolint:wrapcheck` without approval, and do not turn wrapcheck off.
4. If the duplicate is already in the same package, call it. Same-package reuse needs no wrap.

Empty `%w` is a lint-boundary adapter, not a message. Add a real prefix only when the call site has new context.

## Why This Matters

Reviewers treat `fmt.Errorf("%w", err)` as noise and ask to return `err`. That edit is a wrapcheck failure the moment the helper lives in another package. The next person "fixes" lint by putting the helper back, disabling wrapcheck, or adding `//nolint`. All three fight the repo rules.

Keeping one copy at the lowest layer avoids two copies drifting apart. Callers in other packages still have to wrap, because that layer is external to them for wrapcheck.

## When to Apply

- Deduplicating an unexported helper by exporting it from a lower package.
- Returning an error from `internal/rocketclaw/protocol`, a library such as pglock, or any other package out of `internal/rocketclaw/backend`.
- Reviewing a `fmt.Errorf("%w", err)` with no prefix in a wrapcheck-enabled module.
- Deduplicating identical functions in `internal/rocketclaw`. (`make lint` lints `./...` from `internal/rocketclaw`, so `cmd/rocketclaw` is not wrapcheck-checked today.)

Do not apply this to same-package returns. Do not treat it as a reason to copy `loadRuntimeConfig` into `serve.go`.

## Evidence

Cross-package: backend must wrap pglock's error, even with no extra text (`holdRunLock`, quoted above).

Same-package reuse of `loadRuntimeConfig`, with a real wrap prefix at the serve call site:

```33:36:cmd/rocketclaw/serve.go
	selected, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
```

## Related

- [PR #27](https://github.com/Rocketable/platform/pull/27)
- [PR #757](https://github.com/Rocketable/platform/pull/757)
