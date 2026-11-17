---
title: A Go Benchmark Checks Nothing in RocketClaw Unless a Test Calls It
date: 2026-11-14
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: best_practice
component: development_workflow
severity: medium
applies_when:
  - Adding a Benchmark function to cmd/rocketclaw or internal/rocketclaw as evidence for a performance or allocation change
  - Reviewing a performance PR, including automated observabot PRs, whose proof is a benchmark
  - Auditing tests or helpers that nothing in make test or CI runs
symptoms:
  - A Benchmark function that no Test calls and no Makefile or workflow runs
  - A performance fix whose only evidence is benchmark numbers in a PR description
root_cause: missing_workflow_step
resolution_type: test_fix
tags:
  - benchmark
  - testing-benchmark
  - allocation-budget
  - allocs-per-run
  - observabot
  - dead-test
  - make-test
---

# A Go Benchmark Checks Nothing in RocketClaw Unless a Test Calls It

## Context

Neither `make test` nor CI passes `-bench`. Both run RocketClaw's tests through the `coverage-profile` recipe's one `go test` command (`internal/rocketclaw/Makefile:200`) and the `sandbox-test` recipe. CI runs the first in shards (`.github/workflows/test.yml:147`) and the emulated shell's tests through `sandbox-test` (`:194`–`195`), which runs only `TestEmulatedShell` tests (`internal/rocketclaw/Makefile:113`). So a `func BenchmarkX(b *testing.B)` compiles and lints, and never runs. It asserts nothing, and a later change can undo the improvement it measured without any check failing.

An audit on 2026-11-14 found 20 such benchmarks in RocketClaw's test files. Most came from performance PRs that showed numbers once and left the benchmark behind as evidence: 12 from PRs labeled `observabot` (#224, #231, #268, #329, #399, #659, #686, #715, #723, #744, #842, #885), and the rest from feature PRs #149, #440 and #731. None came from `tests-deslop`. Plans asked for this: the code-mode visibility plan's verification contract measured History latency with "U3's fixture benchmark, before and after" (`docs/plans/2026-10-17-0938-feat-code-mode-block-visibility-plan.md`), and the space dashboards plan called its sidebar benchmark a "benchmark gate" (`docs/plans/2026-10-27-2157-feat-space-work-dashboards-plan.md`). Each was measured for its PR, and no check ran it again. The same audit found a one-off script from `tests-deslop` PR #1131, `internal/rocketclaw/check-coverage_test.sh` (deleted by this change), that nothing ran either.

Most of the 20 were not protecting anything a test missed. Of the 20:

- 12 duplicated a check a test already enforced. For example, `TestRemoveSessionEntryNUL` already bounds `removeSessionEntryNUL` with `testing.AllocsPerRun`, `TestSandboxedFileSystemGlobPathAllocations` bounds glob path allocations, and `TestListSessionsSendsChangesSince` bounds session row size, which was the point of the agent-choice benchmark. These were deleted.
- 4 measured no contract worth keeping: CPU profiling overhead on `sha256`, minute-by-minute health logging, and per-request provider log attributes in `backend` and `oai`. These were deleted.
- 3 share a check helper with a test that CI runs (`internal/rocketclaw/backend/origin_pairs_test.go:14`, `:59`, `:88`); the benchmark is only a `-bench` handle on the same check. These were kept.
- 1, `BenchmarkHistoryReviewedScripts`, guarded a rule no test held: History reads the calls of older Execute blocks from each review's saved prompt, and must decode only the prompt's planned-action tail (`internal/rocketclaw/harness/permission_review.go:64`, `:71`), not the transcript before it. It was replaced by a unit test, below.

## Guidance

Treat a benchmark as evidence only once a test calls it and asserts a budget. The 13 benchmarks left hold up one of two ways, 10 the first and 3 the second:

- A test runs the benchmark and bounds its result, for example `result := testing.Benchmark(BenchmarkSidebarFingerprints)` then `require.Less(t, result.AllocedBytesPerOp(), int64(4<<20))` (`internal/rocketclaw/frontend/rpc/sidebar_test.go:96`). The others are at `internal/rocketclaw/backend/store_test.go:422`, `internal/rocketclaw/backend/origin_pairs_test.go:133`, `internal/rocketclaw/backend/store_summaries_test.go:79`, `:551`, `:557`, `internal/rocketclaw/frontend/rpc/http_test.go:56`, `internal/rocketclaw/frontend/rpc/http_compression_test.go:16`, `internal/rocketclaw/internal/web/assets_test.go:148` and `internal/rocketclaw/harness/filesystem_test.go:833`.
- A test and the benchmark share a check helper, and the test bounds it with `testing.AllocsPerRun` (`internal/rocketclaw/backend/origin_pairs_test.go:14`).

When writing such a test:

- Lower the iteration count. `testing.Benchmark` honors `-test.benchtime`, whose default of one second makes a test slow. Tests set it to a fixed count and restore it, and stay nonparallel while they do (`internal/rocketclaw/harness/agents_test.go:471`–`474`, `internal/rocketclaw/harness/skills_test.go:88`–`91`).
- Under `-race`, allocation numbers can lie. `TestHTTPEventFramingAllocationBudget` rebuilds its package with `-race=false` and runs itself again with a fixed benchtime, because the race detector randomly drops `sync.Pool` buffers (`internal/rocketclaw/frontend/rpc/http_test.go:36`–`49`). `make test` runs with `-race` (`internal/rocketclaw/Makefile:200`).
- Bound what the change removed, with headroom, and say why in a comment: a whole extra copy of the input, a second parser pass, a per-row buffer. A tight number fails on unrelated changes.
- Prove the test catches the regression before keeping it: put the old shape back in a scratch change and watch the test fail.

`TestPermissionReviewCodeModeCallDecodesOnlyPlannedAction` (`internal/rocketclaw/harness/permission_review_test.go:126`) shows the shape. It needs no database, unlike the benchmark it replaced, which seeded 100 reviews of about 300 KB each. It builds one review with a 300 KB transcript, runs `CodeModeCall` four times through `testing.Benchmark`, and requires fewer than `len(transcript)/8` allocated bytes per call (`:151`). With an extra `json.Unmarshal` of the whole prompt added to `CodeModeCall`, it failed at 985,252 bytes per call against a 38,398-byte limit.

If no budget is worth asserting, delete the benchmark rather than keeping it as documentation. Numbers in a PR description already record what was measured once.

## Why This Matters

A bare benchmark looks like protection in review and provides none. A plan that names a benchmark as its gate gets a measurement for one PR, not a gate. A later refactor can restore the extra copy or decode it removed, every check stays green, and the regression surfaces as slow chat loading in production.

Automated performance PRs make this recur. Observabot added 12 of the 20 unrun benchmarks over about four weeks; without a rule, the next performance PR adds another.

## When to Apply

- Before adding any `Benchmark` function to RocketClaw: decide what budget the test will assert, or skip the benchmark.
- When reviewing a performance PR: ask which test fails if the change is reverted.
- When auditing: `rg -n '^func (Benchmark\w+)' -g '*_test.go' cmd internal` lists the benchmarks; any whose name no other line in a `_test.go` file mentions is unrun, unless it only wraps a check helper a test also calls.

## Examples

Unrun, before:

```go
func BenchmarkLoadSkill(b *testing.B) {
	fsys := fstest.MapFS{"example/SKILL.md": mapFile("---\nname: example\ndescription: Review a change\n---\n" + strings.Repeat("Check the change and its tests.\n", 100))}
	for b.Loop() {
		_, err := loadSkill(fsys, "example/SKILL.md")
		if err != nil {
			b.Fatal(err)
		}
	}
}
```

Enforced, the shape kept (`internal/rocketclaw/harness/skills_test.go:87`–`112`): the subtest for a file missing its frontmatter sets `test.benchtime` to `4x`, runs `LoadSkills` through `testing.Benchmark`, and requires `cost.AllocedBytesPerOp()` below one and a half times the 64 KiB file, so another full-file copy fails it. Its sibling in `internal/rocketclaw/harness/agents_test.go` holds agents to the same budget.

## Prevention

A check could hold this: a test under `internal/rocketclaw` that parses every `_test.go` file in `cmd/rocketclaw` and `internal/rocketclaw`, and fails on a `Benchmark` function whose name no other test code references. Today it would flag the 3 kept wrappers in `internal/rocketclaw/backend/origin_pairs_test.go`, and the repo has no baseline file for such a rule. Deleting those 3 wrappers (their tests already run the same checks) would let the check start clean.

## Related

- `docs/solutions/logic-errors/permission-review-reasons-read-from-transport-specific-trace.md`: the same History path; its Solution names the review prompt's size as a second trap, which the replacement test now holds.
