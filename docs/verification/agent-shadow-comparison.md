# Agent shadow comparison verification

Authority: `docs/plans/2026-10-11-1632-feat-agent-shadow-comparison-plan.md`.
Current plan SHA-256: `61b1b1eb283c370473fc2179f815b1db68d669f926912ef51f313318997d1ebe`.
The implementation and initial review used
`472cb6714e578e12137af3759b42a88fe65b57656055bff1d06f684448db6a8e`;
the only later plan change clarifies inherited admission locking below.

## Current state

U1–U8 are implemented and locally verified. All eight confirmed review fixes
have follow-up validation. Root Go tests, lint, race tests, coverage, source
budgets, Web lint, and rebuilt browser tests pass. Publication is the remaining
step; this receipt records local proof, not a claim that a PR exists.
Temporary fixtures, PostgreSQL, browser installs, and logs live under `.tmp/`.
Tests use real PostgreSQL with isolated schemas and the built Web application.

## Behavioral evidence

| Requirements | Contract | Runnable checks and witnessed behavior |
|---|---|---|
| R1–R4, R14–R15 | Discovery and fixed ownership | `TestLoadAgents`, `TestShadowBindingsCreation`, `TestShadowActivationRollback`, `TestShadowSeedAndFork`, `TestShadowImplicitCreation`: sorted immediate children, independent seeds, no nested fan-out, transactional admission, fresh fork children |
| R3, R5–R6, R13 | Identical consumed inputs | `TestShadowLiveFinalDrain` and shadow capture/recovery tests: ordered inputs, steers and final-boundary waiting; recovery reuses canonical framing instead of reconstructing it from mutable state |
| R3, R7, R14 | Independent execution | Shadow workflow and isolation tests: saved source survives file change/deletion, admitted agent survives switching, real tools execute under child permissions, prohibited root/task/review tools stay absent |
| R8 | Parent-only delivery | Strengthened live-final tests inspect all outbound events, including starts and consumed input cards; child requests contain no connector routing/control metadata |
| R13–R14 | Durable lifecycle | Shadow/revert/store tests: startup pruning retains pending families, mid-turn revert stops the whole correlated child turn, redo does not repeat effects, branch commit removes child suffixes, clear preserves bindings, removal traverses owned children/delegations |
| R14 | Blob cleanup | Store tests: failed physical deletion retains SQL tombstones, retries are idempotent, S3 deletion targets the stored version; live External MCP removal and first-reconciliation/clear races settle safely |
| R4, R12, R15 | Read-only inspection | `TestShadowPublicMutationsAreReadOnly`, ordinary enumeration and RPC authorization tests: parent-authorized reads, private producer visibility, child/delegation mutation denial, hidden ordinary listings, parent session-tool binding inspection |
| R11–R14 | Snapshot metadata | Shadow history tests: source-aware IDs, pagination, child-only revisions/notifications, retained copies, revert cutoffs, completed/failed/stopped settlement |
| R9–R12, R15 | Web comparison | `shadows.browser.test.ts`: shared-grid alignment within one pixel, 1440px/390px layouts, keyboard tabs/resizing, delayed images, missing correlations, bounded cancellation-safe reads, invalid-tab fallback, no child streams. Final rebuilt suite passed. |
| R2–R3, R5, R8–R12 | Real vertical slice | `TestShadowsRealBackendBrowser` / `shadows-transport.test.ts`: two actual parent turns and two independently held shadows; identical provider input, independent replies, parent finishes before children, child-only refresh through one parent stream, durable paired rows, completed statuses, keyboard switching, HTTP child-write denial |

## Observed commands

All commands set `TMPDIR="$PWD/.tmp/go-tmp"`. Store/browser commands also set
`ROCKETCLAW_TEST_DATABASE_URL`, `ROCKETCLAW_PLAYWRIGHT_MODULE`, and
`ROCKETCLAW_CHROMIUM` to the repository-local fixtures.

| Check | Result | Local log |
|---|---|---|
| U3 focused real-store race tests, repeated | Passed | `.tmp/rocketclaw/u3-focused-race.log` |
| U4 host shadow/workflow race checks | Passed | `.tmp/rocketclaw/u4-host-accepted.log` |
| U5 focused family/lifecycle race checks and settlement | Passed | `.tmp/rocketclaw/u5-host-status-accepted.log` |
| U5 cleanup/recovery repeats | Passed | `.tmp/rocketclaw/u5-repeat-final.log` |
| Full root Go tests after metadata/public guards | Passed | `.tmp/rocketclaw/shadow-integrated-go-test-all.log` |
| Full root lint after integration fixes | Passed | `.tmp/rocketclaw/shadow-full-lint-integrated-final.log` |
| Web transcript/API/shadow/delegation/tab checks | Passed | `.tmp/rocketclaw/shadow-u7-host-accepted.log` |
| Real backend/browser vertical slice with `-race -count=1` | Passed, 28 browser assertions | `.tmp/rocketclaw/u8-vertical-real-proof.log` |
| Root `go test ./...`, including added lifecycle rollback tests | Passed | `.tmp/rocketclaw/shadow-final-go-test-all-before-review-fixes.log` |
| Root `make test`, before review fixes | Behavior tests passed; RocketClaw coverage 85.1% failed the 85.5% gate | `.tmp/rocketclaw/shadow-final-make-test-corrected.log` |
| `cmd/rocketclaw` real asset-sync and MCP index regression, `-race` | Passed; both shadows stay unavailable as standalone agents across reload | `.tmp/rocketclaw/shadow-cmd-index-proof.log` |
| Integrated lifecycle/history/upload ownership checks after review fixes, `-race -count=1` | Passed | `.tmp/rocketclaw/shadow-host-integrated-lifecycle.log` |
| Integrated RocketClaw coverage attempt during remaining worker-lock fix | Failed worker-lock regression; partial aggregate profile 85.4%, below 85.5% | `.tmp/rocketclaw/shadow-integrated-review-coverage.log` |
| Settlement rollback/retry including deferred commit failure, `-race` | Passed | `.tmp/rocketclaw/shadow-settlement-rollback-proof.log` |
| RocketClaw full race/coverage check after settlement regressions | Passed; aggregate 85.5% meets 85.5% | `.tmp/rocketclaw/shadow-settlement-coverage.log` |
| Serialized root `go test ./...` after settlement regressions | Passed | `.tmp/rocketclaw/shadow-serialized-root-go-test.log` |
| Full legacy `TestSessionEntries` RPC contract, `-race -count=1` | Passed, including durable attachments | `.tmp/rocketclaw/shadow-legacy-rpc-contract-proof.log` |
| Serialized root `make lint` | Passed, zero Go lint issues | `.tmp/rocketclaw/shadow-serialized-root-lint.log` |
| Serialized root `make test` | Passed; RocketClaw coverage 85.5%, RocketCode race suite and all Go source budgets pass | `.tmp/rocketclaw/shadow-serialized-root-make-test.log` |
| Root `go test ./...` after generator/lint formatting | Passed | `.tmp/rocketclaw/shadow-post-format-root-go-test.log` |
| Final Web `make lint` and rebuilt `make test` | Passed; zero React Doctor findings, 169 Bun tests, 2,961 assertions, real Go/RPC browser fixtures | `.tmp/rocketclaw/shadow-ui-final-lint.log`, `.tmp/rocketclaw/shadow-ui-final-test.log` |
| Final delayed-read/older-page browser regressions | Passed; 3 tests, 22 assertions | `.tmp/rocketclaw/shadow-ui-final-regressions.log` |
| Host real backend/browser vertical slice against final assets, `-race -count=1` | Passed; 28 browser assertions | `.tmp/rocketclaw/shadow-final-real-u8.log` |
| Final combined root Go, lint, and test checks, run serially | Passed; coverage 85.5% and all Go budgets | `.tmp/rocketclaw/shadow-publish-go-test.log`, `.tmp/rocketclaw/shadow-publish-lint.log`, `.tmp/rocketclaw/shadow-publish-make-test.log` |
| Final source counts | Passed; RocketClaw 22,935/23,500, RocketCode 10,840/12,500, Web 5,806/7,500 | `.tmp/rocketclaw/shadow-publish-cloc.log` |

## Review resolution

The complete feature was reviewed against `f7b7e258` at
`224c60b3c21db164afb7b89c19f5ce35af9ae363`. Thirteen focused reviewers ran;
a separate validator confirmed eight defects. All used the host model, so this
was not a cross-model review. The full local receipt is
`.tmp/ce-code-review/20261011-host-review/review.json`.

- Clearing a visible destination must also clear its private producer's comparisons.
- History-only clear must retain uploaded files still needed by queued or unfinished work.
- Invalid shadow definitions must not prevent healthy parent or sibling execution.
- Successful settled comparisons must release redundant execution payloads.
- Mirrored upload cards and downloads must resolve the actual recorded owner.
- Worker ownership reads must not hold the global manager lock across SQL.
- In-flight child reads must preserve a trailing child-only update.
- Loaded older comparisons must refresh while their shadows are still running.

The parent-drain mutex/SQL concern was traced to inherited admission ownership:
the base already journals steers and claims notes under the same per-bridge
mutex. Releasing a transaction-level history lock at commit before publishing
memory state exposes stale routing; merely unlocking around SQL is incorrect.
A general fix needs a new pinned-connection/session-lock lifetime and changes
to queue deletion, movement, scheduled admission, revert, and note completion.
Instead, the plan's broad lock/I/O constraint was narrowed to preserve that
existing atomic critical section. No lock crosses a shadow-output wait or a
provider/tool/connector wait; global worker-manager SQL must remain outside its
mutex. The user-visible input, rollback, routing, and comparison requirements
are unchanged. Slow persistence can still delay this bridge's admissions.

The lifecycle, upload, worker-lock, and Web refresh fixes have passing regression
evidence and final integrated gates above. Build-dependent gates ran serially:
concurrent asset builds and generated-mock reads invalidated the earlier root
Go/Web attempts, not a product-test failure.
Independent follow-up validation confirmed backend findings 1, 2, 3, 4, 6, and 7
with no remaining backend finding:
`.tmp/rocketclaw/shadow-backend-followup-validation.json`. The validation detected
a later lint-formatting edit in the settlement test; the subsequent host root
`make test` and `go test ./...` cover that final version. All inspected production
file fingerprints were unchanged. Two gopls modernization suggestions at
unchanged lines were left outside this feature's scope; gofix reports no changes
and all changed Go files are gofmt-clean.
Independent UI follow-up confirmed findings 9 and 10 with no remaining finding:
`.tmp/rocketclaw/shadow-ui-followup-validation.json`. All nine final source/asset
fingerprints still match after the final combined root checks. The host's Go
standards pass checked changed hunks for naming, contexts, dependency injection,
mutex ownership, unnecessary guards/wrappers, and scope; no violation remained.
The newly added rollback tests were outside the pinned review snapshot and were
inspected separately by the host. They verify transaction rollback, retry,
retained work, and settlement rather than deleted malformed-child blocking.

Pre-fix source CLOC: RocketClaw 22,838 / 23,500; RocketCode 10,840 / 12,500;
Web 5,770 / 7,500. These numbers do not replace the final metric gates.

## Failure evidence and limits

Test-first failures exposed absent discovery/storage, child connector leakage,
uncaptured workflows, premature pruning, incomplete family cleanup, mid-turn
rewind, and lost blob-deletion ownership. Host settlement tests failed while
completed children remained running. Direct child mutation tests failed before
runtime entrypoint restrictions were added. The vertical slice used proof-first
verification; its initial failures were fixture placement and overly strict
multi-paragraph text selectors, not production regressions.

Earlier passing milestone checks do not replace the final full-suite, coverage,
CLOC, simplification, and review gates recorded above.
No linter suppressions, coverage exemptions, or budget increases were added.
The isolated `TestSessionEntries/durable_attachments` filter is not a valid
standalone fixture: its parent assertions depend on an earlier subtest clearing
the workflow list. The full parent test passes; the earlier line-1606 failure
does not reproduce when the whole contract runs. No production change was made
for that fixture-order issue.

The pipeline browser smoke used the installed `agent-browser` CLI with the final
assets and a presentation-only fixture at `http://127.0.0.1:3001/s/cGFyZW50`.
Desktop (1440px) and narrow-screen (390px) checks passed: two paired rows had
exactly equal top coordinates, switching tabs selected the second shadow, the
comparison sheet opened and navigated back to the parent, and there was no
horizontal overflow or browser error. An initial incomplete fixture omitted the
cron list; returning its empty job list removed that fixture error. Screenshot:
`.tmp/rocketclaw/shadow-agent-browser-desktop.png`. The server and browser were
closed. This smoke does not replace the real backend/provider/browser proof.

README impact was considered. Root and Web READMEs and `CONCEPTS.md` now explain
discovery, comparison, read-only children, recovery/cleanup, workflow behavior,
duplicate real side effects, and S3 deletion permissions.

## 2026-10-12 integration verification

The combined voice/shadow tree was rebased onto `2f7f8659` and simplified without
changing the source budget or removing features. RocketClaw now measures
**23,489 production lines**, below the unchanged 23,500 limit. Coverage is
**85.8%**, above the required 85.5%.

The final tree passed `go test ./...`, root `make lint`, root `make test`, Web
`make lint test`, and every component's `check-cloc-budget` target. Root tests
include RocketClaw and RocketCode race checks, PostgreSQL recovery/ownership
checks, voice tests, and the real backend/browser shadow comparison. Final logs
are under `.tmp/simplify-shadow-budget/` with the `budget-final` suffix.

Existing shell and captured-workflow tests now verify that ordinary shadows and
explicit workflow workers receive the full owning identity in
`ROCKETCLAW_SHADOW`, without changing captured parent environment maps. Mobile
browser checks cover the Shadows control beside independent origin filters,
short tabs, turn-only headings, and parent-turn navigation.

The final dedicated review attempt could not run its required reviewer
collection with the available controls. Its failed receipts are retained under
`.tmp/ce-code-review/`; a parent manual diff scan and the bounded reduction
reviews found no remaining issue. This is not an independent cross-model review.
Gopls reported one modernization suggestion on an unchanged attachment line,
not a code error. The final Go standards pass found no changed-hunk violation.

README impact was considered: the shadow environment variable is documented;
internal simplifications need no additional README change. No deployment or
demo-control operation was performed, and merging PR #299 remains the user's
decision.
