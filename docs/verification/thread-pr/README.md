# Thread PR protocol: bounded verification

This executable PlusCal model checks a small abstraction of thread PR discovery,
the metadata/file reader, and temporary OpenCode shell cleanup. **It does not
prove the TypeScript implementation correct.** Four configurations pass; other
configurations deliberately produce counterexamples, including implementation
limits. Passing `make check` means both kinds matched their
expected outcomes, not that all requested safety properties hold in production.

## Run

From the repository root:

```sh
make -C docs/verification/thread-pr translate
make -C docs/verification/thread-pr check
# A single expected counterexample, still checked by the runner:
make -C docs/verification/thread-pr aba
```

Requires Java 11+, `curl`, `shasum`, and Make. The runner downloads the same
v1.8.0 tools artifact and verifies the same SHA-256 as
`../tab-attention/Makefile`:
`e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f`.
The local Java used for the recorded run was OpenJDK 21.0.13.0.3. The pinned
artifact's TLC banner reported `2026.11.06.222357 (rev: d8fb231)`.

All tools, translator scratch copies/backups, Java temporary files, TLC metadata,
and full logs/counterexample traces go to **repository-root
`.tmp/run/thread-pr-model/`**, never `docs/.tmp` or system temporary directories.
There are no model state constraints, sampling, or simulation flags. Each
passing configuration exhausts its finite reachable graph. Negative invariant
runs stop at the first counterexample; their counts are explored prefixes.
The runner requires TLC exit 12/13 and the exact named violation; a parse error
does not count as an expected failure. Individual `.cfg` files can also be used
with TLC directly, which returns nonzero for those counterexamples.

`ThreadPR.tla` holds the first-party PlusCal algorithm, properties, and generated
translation. Edit the algorithm/properties, then translate; do not hand-edit
between the translation markers. Scratch copies are only translator artifacts,
not an alternative model hidden outside review.

## Bounds and meaning

The cases are checked separately, **not as a proved composition**:

- Discovery: two history pages, one successful create candidate, rejected view,
  failed-create and mention evidence, one independent checkout lookup with
  success/absence/failure, and one optional thread navigation.
- Reader: an initially accepted metadata snapshot, one file-page request and at
  most one additional metadata request (manual refresh or effect restart), two
  thread/source identities, one navigation/source change, and optional cancel.
  If the metadata request slot was already used, navigation checks departing
  completions but omits the new view's further requests. Arbitrarily many pages,
  refreshes, tabs, and concurrent readers are not covered.
- Revision is the tuple `(base SHA, head SHA, changed_files)`, each coordinate
  abstracted to 0/1. The passing reader permits at most one tuple-coordinate
  change. `aba.cfg` permits two changes, including restoring the old value.
  Patch identity means the tuple/source that served the page, not its text.
- Shell: one POST, remote allocation, browser receipt of its ID, a finite poll
  result/error, two output pages, `finally`, DELETE dispatch and response.
  Cancellation may occur before dispatch or at any subsequent boundary. It is
  not process death. No shell is allocated if cancellation wins before POST.

`source` means the API/directory context used in the reader key. The host login is
assumed stable during a read; changing credentials under the same API/directory
is not a modeled source change. The model does not inspect paths, hosts,
credentials, shell text or GitHub data. `epoch` and identity fields are
**ghost bookkeeping**, not additional production guards: real completion guards
are request-local abort signals and React/query ownership. File results are
checked against their accepted metadata, **not against the latest live branch**;
a later branch update does not invalidate an already accepted snapshot.

`Evidence`/`ProvenCreate` separate `recordedSuccess` from `parserAccepts`.
The former represents native bash `status === "ok"`, or completed/exited
OpenCode bash/shell with exit 0. The latter represents the restricted command
and URL parser returning exactly one reference. A successful checkout `view`
has no create evidence. Mentioned links and failed creates have no success
evidence. The model checks propagation of these predicates, **not whether any
arbitrary command/output string actually satisfies them**. Truthful tool
records, the meaning of `gh pr create`, and parser soundness are assumptions.
Native history supplies a last-line, length-limited summary, not the complete
shell output; its uniqueness test cannot establish full-output uniqueness.

## Async steps and fairness

The PlusCal labels name event-loop transitions:

| Label | Modeled boundary and concrete implementation |
| --- | --- |
| `NavigateOrRefresh` | User navigation, source dependency change, `load(true/false)`, `cancel()`, or an external revision change. `ui.tsx`'s `ThreadPRContent key={id}` remounts on thread changes. `thread-pr-panel.tsx`'s reader key includes both URL and source, so a same-URL source change also remounts. |
| `HistoryAndCheckout` | `thread-pr.ts:loadThreadPRs` page completions and final result; independently, `opencode-pr.ts:discoverOpenCodePR` success/no-PR/access-error branches. `useThreadPRs` uses separate `threadPRs`/`checkoutPR` query keys and unions results. |
| `RequestCompletion` | `github-pr-preview.tsx:usePreview`'s metadata continuation, and `github-pr.ts:loadPRFiles`'s pre-check, page response, post-check, then the preview's signal-guarded commit. Each is a separate model step. Manual refresh clears files before awaiting metadata; source changes now start a fresh reader. |
| `ShellAwaitBoundary` | `opencode-pr.ts:run`: initial abort check; POST without signal; remote allocation distinct from ID receipt; poll/output awaits with signal; `finally` DELETE without signal. Publication also abstracts the preview's final abort guard. |

No atomic action contains an entire async request sequence. Server allocation
can occur before a cancelled creation response is received. A files response
does not atomically include either revision check or the UI commit. The model
collapses synchronous field updates and abstracts response parsing as successful;
it sometimes allows extra scheduling boundaries, for example ID receipt before
the post-creation abort check. It does not prove React scheduling, TanStack Query
cache semantics, or cancellation delivery. The keyed-remount and query-isolation
contracts above are explicit assumptions backed by source inspection/tests.

`World` is unfair: the person need never navigate, cancel or refresh, and the
branch need never change. It can stutter forever. Discovery, Reader and Shell
workers are individually **weakly fair**: a continuously enabled worker
eventually advances. With finitely many acyclic requests this prevents starvation
of any pending operation. This assumes browser execution continues, requests
resolve, cursors terminate, and the modeled poll reaches a terminal result.
The positive shell case additionally assumes a valid creation ID is received
and DELETE succeeds. It implies every *known* shell, and every allocated shell
in that response-reliable case, is eventually deleted. Cancellation eventually
settles; it need not settle immediately while waiting for POST/DELETE.
There is no fairness assumption that forces browser recovery or successful
network responses. Browser loss disables the shell worker, making cleanup
impossible in that case even though its weak-fairness assumption still holds.

## Properties and results

Recorded on 2026-11-12 with `make check`, one TLC worker, 1 GiB heap; the full
matrix completed in about 16 seconds. `check.log`, `translate.log`, and one
`<configuration>.log` per run retain the evidence under the artifact root.

| Passing configuration | Checked contracts | Generated / distinct states | Depth |
| --- | --- | ---: | ---: |
| `discovery` | Only proven creates owned; no wrong-thread history/checkout publication; known history remains in the union despite checkout failure; discovery settles | 168 / 52 | 7 |
| `reader` | No stale-context or aborted completion commit; displayed thread/source match; patch source/revision match metadata; cancellation stops loading; requests settle | 2,272 / 724 | 11 |
| `shell` | No publication after cancellation; known and allocated shells eventually deleted; cancellation settles | 62 / 27 | 11 |
| `source-guards` | Completion abort/epoch/context safety, displayed-context and patch-source consistency, and settlement with same-URL source-keyed remounts | 2,272 / 724 | 11 |

| Expected counterexample | What changes / what fails | Generated / distinct | Depth |
| --- | --- | ---: | ---: |
| `no-abort` | Mutate away request-local abort guard; `NoCancelledCommit` | 45 / 28 | 4 |
| `stale-navigation` | Same mutation; `NoStaleCommit` after navigation | 98 / 58 | 4 |
| `no-post` | Remove post-files revision check; `PatchMatchesMetadata` | 870 / 384 | 7 |
| `own-checkout` | Treat checkout result as creation provenance; `OnlyProvenCreateOwned` | 22 / 15 | 3 |
| `drop-history` | Checkout failure erases known history; `HistorySurvivesCheckoutFailure` | 136 / 48 | 6 |
| `abort-create` | Cancel creation response after server allocation, losing ID; `CreatedShellEventuallyDeleted` | 63 / 28 | 11 |
| `skip-delete` | Omit `finally` DELETE; `KnownShellEventuallyDeleted` | 52 / 23 | 10 |
| `source-change` | Mutate the reader key back to URL-only (`retain-source`), retaining old result; `DisplayedContextMatches` | 2 / 2 | 2 |
| `source-patch` | Same key mutation lets files start from a new source against retained old-source metadata; `PatchMatchesMetadata` | 773 / 359 | 7 |
| `aba` | **No mutation:** allow A→B→A between revision checks; `PatchMatchesMetadata` | 3,310 / 1,252 | 8 |
| `browser-loss` | **No mutation:** browser dies with known shell; `KnownShellEventuallyDeleted` | 102 / 43 | 11 |
| `delete-failure` | **No mutation:** DELETE returns failure without removing shell; `KnownShellEventuallyDeleted` | 62 / 27 | 11 |
| `witness-cancel` | Reachability canary, not a production mutation: `NeverCancelled` fails | 2 / 2 | 2 |

Depth is TLC's reported search depth, not a maximum request count. Safety
counterexample prefixes are not exhaustive verification results. Liveness
counterexamples end in a fair stuttering behavior with an undeleted shell.

## Finding fixed, and remaining limits

1. **Source transition state, fixed:** the initial model found that the former
   URL-only reader key retained old metadata/files while fetching a new source.
   The reader key now includes URL and API/directory source. `source-guards`
   checks this fix; `source-change` and `source-patch` restore the former key as
   negative controls. The OpenCode browser regression delays new-source metadata
   and checks that the prior title/tree is gone before the new response arrives.
2. **Revision sandwich is not a snapshot:** pre-check A → branch B → files B →
   branch A → post-check A → commit passes both checks but publishes B against A.
   Base rollback/force-push alone suffices. The passing reader only establishes
   safety under its one-change/no-return bound and coherent API responses.
   Immutable revision-bound retrieval or a weaker documented guarantee needs
   an explicit product choice; adding more unversioned checks is not a proof.
3. **Cleanup is conditional:** `finally` performs one awaited DELETE. Browser
   death cannot execute it; DELETE failure has no retry/durable owner here.
   Merely dispatching DELETE is not proof of deletion. Losing the POST response
   after allocation, malformed creation JSON, nonterminating polls/output, or
   server behavior outside this protocol likewise fall outside the positive
   guarantee. This model neither verifies nor assumes a server-side reaper.

The model supplied the source-transition counterexample; the feature change fixes
it with a keyed remount and separately exercises the real UI. The remaining
counterexamples are retained findings, not properties claimed to hold.

## Existing test mapping and exclusions

Paths below are under `internal/rocketclaw/web/src/`. These tests are separate
implementation evidence; TLC does not execute them:

- `thread-pr.test.ts`: restricted create commands/dry-run/quoted and compound
  commands; reject multiple accepted URLs; native pagination and successful
  calls; paged OpenCode tool/shell history rejects failures/running/text links.
- `opencode-pr.test.ts`: checkout uses configured server/directory, not session
  prompting; excludes PRs finished before session creation; distinguishes
  absence/access failure and retains source for a recorded PR; cancellation
  after POST still DELETEs; output pagination and endpoint restrictions.
  `discoverOpenCodePR`'s timestamp/branch policy is input to the checkout result,
  not modeled date arithmetic. Its errors never erase the separate history set.
- `github-pr.test.ts`: URL boundaries, response-field filtering/order, missing
  patches, and base/head changes before/during a file page. These exercise the
   concrete parser and revision checks but do not cover ABA or
  a `changed_files`-only revision change.
- `github-pr.browser.test.ts`: late cancelled metadata never appears; file
  cancellation preserves prior files; refresh clears pages; unmount/navigation
  abort; thread pane rediscovery/tab changes and draft preservation. Its active
  recorder/privacy, layout, accessibility and rendering assertions are separate
   executable evidence, **not formally verified here**. `opencode.browser.test.ts`
   also covers source-keyed remounting while the replacement metadata is pending.
- Native `recordedSuccess` also maps to
  `internal/rocketclaw/harness/public_progress.go:CodeModeCall.finish`: nonzero
  bash error codes become `error`; `Summary` keeps the last nonempty line and is
  length-limited. No Go file was edited or tested in this unit.

No direct proof is provided for TS/React execution, parser string soundness,
privacy/telemetry/token storage, rendering/Pierre/layout, host or shell security,
authorization, API/server correctness, cursor completeness, or crash recovery.
The separate cases do not verify every interleaving of the complete product.
The feature owner must rerun relevant real tests and revisit this mapping after
changing the implementation. A source checksum snapshot for this run is retained
as `sources.sha256` beside the logs.

README impact was considered: this model README is required;
`internal/rocketclaw/web/README.md` links here and documents the revision and
cleanup limits. The repository-root README does not need a change.
