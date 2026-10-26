---
title: Wrapcheck Cross-Package Helper Error
date: 2026-08-31
category: docs/solutions/best-practices/
module: internal/rocketclaw/protocol
problem_type: best_practice
component: development_workflow
severity: low
resolution_type: code_fix
applies_when:
  - Deduplicating an identical helper by exporting it from protocol for backend to call
  - Backend returns an error that originated in another package
  - wrapcheck fails make lint if that error is returned unwrapped
  - "Reviewers ask to drop fmt.Errorf(\"%w\", err) with no extra prefix"
related_components:
  - internal/rocketclaw/backend
  - cmd/rocketclaw
tags:
  - wrapcheck
  - dry
  - cloc
  - protocol
  - error-wrapping
---

# Wrapcheck Cross-Package Helper Error

## Context

Deduplicating an identical unexported helper by moving it into another package changes wrapcheck's view of the same `return err`. Same-package returns stay clean. Cross-package returns of the helper's error fail wrapcheck unless they are wrapped with `%w`.

This landed from [PR #27](https://github.com/Rocketable/platform/pull/27).

The kept copy is `protocol.StaticGoalCheckWord` (`internal/rocketclaw/protocol/goal.go:110`). That is the lowest layer both `$goal` parsing and backend check-script validation need. Same-package `consumeGoalCheckScriptValue` still returns the helper error unwrapped (`internal/rocketclaw/protocol/goal.go:94-96`). Backend calls across the package boundary and wraps:

```81:83:internal/rocketclaw/backend/goal_check.go
		value, err := protocol.StaticGoalCheckWord(arg)
		if err != nil {
			return "", fmt.Errorf("%w", err)
```

`fmt.Errorf("%w", err)` adds no prefix. `Error()` stays the same as the wrapped error; `errors.Is` / `errors.As` still work. wrapcheck fails on an unwrapped cross-package `return err`. The repo wrapping convention is `%w`, not a wrapcheck check for the `%w` verb itself.

wrapcheck is enabled for RocketClaw (`internal/rocketclaw/.golangci.yml:49`). Default wrapcheck does not report same-package returns. It does report errors returned from another package. Do not add `//nolint` or disable wrapcheck without human approval (`AGENTS.md`).

`runServe` calls `loadRuntimeConfig` (`cmd/rocketclaw/serve.go:32`), which already lives in package `main` (`cmd/rocketclaw/main.go:82`), and wraps with a real prefix: `fmt.Errorf("load config: %w", err)`.

## Guidance

When identical helpers show up across packages:

1. Keep one copy at the lowest layer that all callers can import. For goal-check word parsing that is protocol, not backend.
2. Callers in the same package may `return err`. Callers in another package must wrap. Use `return fmt.Errorf("%w", err)` when there is no extra context to add. wrapcheck accepts other wraps too; `%w` is the repo convention.
3. Do not "clean up" that empty wrap to `return err`. wrapcheck will fail. Do not `//nolint:wrapcheck` without approval, and do not turn wrapcheck off.
4. If the duplicate is already in the same package, call it. Same-package reuse needs no wrap.

Empty `%w` is a lint-boundary adapter, not a message. Add a real prefix only when the call site has new context.

## Why This Matters

Reviewers treat `fmt.Errorf("%w", err)` as noise and ask to return `err`. That edit is a wrapcheck failure the moment the helper lives in another package. The next person "fixes" lint by putting the helper back, disabling wrapcheck, or adding `//nolint`. All three fight the repo rules.

Keeping the helper in protocol avoids two copies of the static-word walk. Backend still has to wrap because protocol is external to backend for wrapcheck.

## When to Apply

- Deduplicating an unexported helper by exporting it from a lower package.
- Returning an error from `internal/rocketclaw/protocol` (or any other package) out of `internal/rocketclaw/backend`.
- Reviewing a `fmt.Errorf("%w", err)` with no prefix in a wrapcheck-enabled module.
- Deduplicating identical functions in `cmd/rocketclaw` or `internal/rocketclaw`.

Do not apply this to same-package returns (`internal/rocketclaw/protocol/goal.go:96`). Do not treat it as a reason to copy `loadRuntimeConfig` into `serve.go`.

## Evidence

Same-package: protocol may return the helper error unwrapped.

```94:96:internal/rocketclaw/protocol/goal.go
		value, err := StaticGoalCheckWord(word)
		if err != nil {
			return "", "", err
```

Cross-package: backend must wrap, even with no extra text.

```81:83:internal/rocketclaw/backend/goal_check.go
		value, err := protocol.StaticGoalCheckWord(arg)
		if err != nil {
			return "", fmt.Errorf("%w", err)
```

Lowest-layer helper (keep here, do not re-copy into backend):

```109:110:internal/rocketclaw/protocol/goal.go
// StaticGoalCheckWord returns the literal text of a shell word with no expansions.
func StaticGoalCheckWord(word *syntax.Word) (string, error) {
```

Same-package reuse of `loadRuntimeConfig`, with a real wrap prefix at the serve call site:

```32:34:cmd/rocketclaw/serve.go
	selected, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
```

## Related

- [PR #27](https://github.com/Rocketable/platform/pull/27)
