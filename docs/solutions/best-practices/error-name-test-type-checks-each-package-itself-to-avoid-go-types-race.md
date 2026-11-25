---
title: The error-name test type-checks each package itself because go/packages' parallel type-checking races in go/types
date: 2026-11-25
category: docs/solutions/best-practices/
module: internal/rocketclaw/internal/errornames
problem_type: best_practice
component: development_workflow
severity: medium
applies_when:
  - Changing how TestErrorNamesFollowTheGoWiki loads or type-checks packages, for example to make it simpler
  - Writing any test that type-checks many packages with golang.org/x/tools/go/packages and runs under -race
  - "A -race run reports a data race whose stacks are all inside go/types and go/packages workers"
symptoms:
  - "CI fails with \"race detected during execution of test\" in TestErrorNamesFollowTheGoWiki, and no misnamed error is listed"
  - "The race report pairs (*Checker).isComplete in go/types/cycles.go with (*Named).unpack in go/types/named.go"
  - The pull request's own checks pass and only a later merge-queue run fails
  - Repeated local -race runs of the same test pass
root_cause: concurrency
resolution_type: test_fix
retire_when: "golang/go#81122 is fixed (CL 841585 or a successor) in a Go release at or below the go line in go.mod. Check the issue and the Go release notes for that version."
tags: [go-packages, go-types, race-detector, export-data, errornames, flaky-test, merge-queue]
---

# The error-name test type-checks each package itself because go/packages' parallel type-checking races in go/types

## Context

`TestErrorNamesFollowTheGoWiki` (`internal/rocketclaw/internal/errornames/errornames_test.go`) checks every package in the module against https://go.dev/wiki/Errors#naming. It asks `golang.org/x/tools/go/packages` only for package metadata and compiled export data (`NeedName | NeedCompiledGoFiles | NeedImports | NeedDeps | NeedExportFile`), then parses and type-checks each package itself with `go/types`, in an `errgroup`, giving every package its own `token.FileSet` and its own `importer.ForCompiler` importer.

That looks like reimplementing what `go/packages` does with `NeedTypes | NeedTypesInfo | NeedSyntax`. Do not switch back while `go.mod` names a Go release without the fix for golang/go#81122.

When `go/packages` type-checks, it runs up to `runtime.GOMAXPROCS(0)` checkers at once (the `cpuLimit` semaphore in `vendor/golang.org/x/tools/go/packages/packages.go`), and those checkers share the `*types.Package` of every common dependency. On Go 1.27, `(*Checker).isComplete` reads a shared generic instance's `fromRHS` without a lock while another checker lazily expands the same instance in `(*Named).unpack`. The race detector reports it, and `make test` and CI run the tests with `-race` (`coverage-profile` in `internal/rocketclaw/Makefile`), so the build fails.

The race does not depend on how dependencies are loaded. With dependency types read from export data it fired in a merge-queue run; loading dependencies from source (`NeedImports | NeedDeps` with `NeedTypes`) only makes it rarer, as the issue's reporter found after about 10 minutes of load-heavy testing instead of about 17 seconds.

## Guidance

- Keep type-checking in the test. Each goroutine builds its own importer, so no two checkers share a `types.Package`, a `types.Named`, or a `token.FileSet`. The only shared values are the loaded `*packages.Package` graph and the universe types, and the checkers only read them. That makes the check race-free by construction on any Go version, not merely less likely to race.
- Keep the `errgroup` closure's own error variable (`errCheck`). Assigning to the test's own `err` from every goroutine would be a race in the test itself.
- Keep `go/packages` for finding packages, test variants, compiled files, and export data. Do not replace it with a hand-written `go list` parser.
- A green local run is not evidence either way. Six local `-race` runs of the version that let `go/packages` type-check found no race; CI found one.

## Why This Matters

Handing type-checking back to `go/packages` would delete code and pass locally, and maybe pass on the pull request too. The race would then fail some later, unrelated merge-queue run, with a report that names no repo code. That is how the first version of this test got through review.

## When to Apply

- Before changing how `TestErrorNamesFollowTheGoWiki` loads or type-checks packages.
- When adding another test or tool that type-checks many packages and runs under `-race`: give each concurrent checker its own importer instead of using `NeedTypes`.
- When a `-race` failure's stacks are all in `go/types`, under `go/packages` loader workers.
- When `go.mod` moves to a Go release that fixes #81122. Only then can the test go back to `NeedTypes | NeedTypesInfo | NeedSyntax`, and only after a merge-queue `-race` run passes that way.

## Examples

What was tried and did not hold up:

- **Letting `go/packages` type-check with dependency types from export data** (`NeedTypes | NeedTypesInfo | NeedSyntax` without `NeedDeps`). It passed the pull request's checks and raced in the merge queue, after 119.6 s in the `rocketclaw` coverage shard.
- **Reading the failure as a naming violation.** The merge queue had tested the change on top of two other pull requests, so new code breaking the rule seemed likely. The log said `race detected during execution of test`, and no name was wrong.
- **Rewriting the test without `go/packages`** around `go list -export` and a sequential checker. It avoided the race, but replaced the standard loader with a hand-written one and took about 35 s under `-race`. Review rejected it.
- **Adding `NeedImports | NeedDeps` and keeping `go/packages`' type-checking.** It passed one merge-queue run, but only lowers the odds, as above.

The current version takes about 7 s under `-race` locally, faster than any of these, because the per-package checks still run in parallel.

## Related

- https://github.com/golang/go/issues/81122, open as of 2026-11-25. The proposed fix, https://go.dev/cl/841585, was unmerged as of the same date.
- `docs/solutions/best-practices/canceled-transaction-commit-error-is-not-deterministic.md`: another failure that shows up only in CI under `-race` and that local repeated runs miss.
