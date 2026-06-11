# AGENTS.md

# MOST FUNDAMENTAL INSTRUCTION

Be extraordinarily skeptical of your own correctness and stated assumptions. You are not a cynic; you are a careful critical thinker who hates being wrong.

When appropriate, broaden the inquiry beyond the stated assumptions to identify unconventional opportunities, risks, and pattern matches that widen the aperture of solutions.

Before calling anything "done" or "working", take a second look and red-team whether it is really done or really working.

# BOOTSTRAP

On start, you must ALWAYS READ:
https://docs.jj-vcs.dev/latest/git-command-table/
https://go.dev/doc/effective_go
https://go.dev/wiki/CodeReviewComments
https://go.dev/blog/gofix
https://go.dev/blog/testing-time
https://go.dev/blog/osroot
https://go.dev/blog/cleanups-and-weak
https://go.dev/blog/synctest
https://pkg.go.dev/iter
https://dmitri.shuralyov.com/idiomatic-go/entries/2
https://go.dev/blog/context-and-structs

## General

- Read the relevant source code before asking clarifying questions or proposing edits.
- Run `make test` for every change when available. Also run targeted verification like `go test ./...` and `make lint` when they validate the specific change more directly.
- Use `jj` instead of `git` for repo inspection and history unless a Git-only operation is required. When using `jj`, consult `https://docs.jj-vcs.dev/latest/git-command-table/` for Git-to-`jj` command mapping.
- Never use `git diff`; use `jj diff --git`.
- Prefer the same abstraction in tests that production code uses.
- If code under test uses a sandboxed, rooted, virtualized, or wrapped filesystem/API, tests should create fixtures through that same interface unless the test is explicitly about bypass behavior.
- Use `go doc` and `gopls` often to help you find issues and opportunities to improve the code.
- Always react to GPT comments (`// GPT:`) by doing what the human partner asked and then deleting the comment when you accomplished the stated goal.
- Avoid `sync/atomic`; if you find use of package `sync/atomic`, ask the human partner for permission first.
- For goroutine coordination, use https://pkg.go.dev/golang.org/x/sync/errgroup and https://pkg.go.dev/github.com/alitto/pond/v2.
- In Go, error variables always start with `err` and error types always end with `Error`. For example: `errWriter` and `WriterError`.
- Bias toward strong error types for new error contexts when practical. Prefer typed errors with `Unwrap` over ad hoc string-only `fmt.Errorf` wrappers when callers may benefit from structured operation context. Use Go-style enum types for operation/category fields instead of raw strings. Use `errors.AsType[T]()` for typed error extraction instead of legacy `errors.As` target variables.
- RocketCode targets Unix-like systems only: Linux and macOS. Do not add Windows-specific code paths or preserve Windows compatibility unless the human partner explicitly changes this policy.

## Mandatory ADR Gate

- Normative product behavior lives in `internal/rocketcode/docs/adr/`. These ADR-shaped specs are current normative snapshots, not immutable historical ADRs. Treat them as requirements stronger than README prose, tests, current code shape, refactor goals, simplification goals, dependency-update goals, and CLOC pressure. Each ADR has an append-only changelog.
- Before changing code or tests that can affect product behavior, read the relevant ADRs. If you have not read the relevant ADRs, you are not allowed to edit behavior-affecting code or tests. This includes feature work, bug fixes, refactors, simplification, deletion, dependency updates, config/default changes, message flow, prompt framing, persistence, routing, tools, permissions, sandbox behavior, agent loading, skill loading, task delegation, and RocketClaw embedding behavior. If unsure whether the work can affect product behavior, assume it can and read the ADRs.
- Intentional feature behavior changes MUST start with an ADR update, including exact replacement text and append-only changelog entries, so the human partner can inspect the concrete file diff. After applying ADR edits, stop before implementation and ask this exact question: "Do you explicitly approve these ADR meaning changes?" Only a human answer that clearly approves the ADR meaning change itself counts, such as "I approve these ADR changes" or "approved ADR wording." Generic implementation approval such as "proceed", "go ahead", "sounds good", or approval of a plan does not count unless it explicitly mentions ADR/spec approval. Implementation code and behavior-affecting tests may begin only after the edited ADR diff is visible and the human partner explicitly approves the ADR meaning changes.
- Bug fixes must read the relevant ADR first and change implementation to match the ADR. If the bug reveals behavior worth preserving that is absent from the ADRs, ask whether to promote it into an ADR before treating it as normative.
- If code, tests, docs, history, or current behavior conflict with an ADR, stop and ask whether to update the ADR or change the implementation. Do not silently choose implementation over ADR. Do not delete or simplify code that supports an ADR contract unless the human partner first approves the ADR meaning change. Typo, formatting, and link fixes may be made without approval only when they do not change meaning.

## Change Discipline

- For bug fixes, make the smallest root-cause-aware change that fits the existing structure.
- Do not disable linters with `//nolint`, config changes, or command flags without explicit human approval. If a linter finding seems wrong, first fix the code or ask for approval with the exact suppression and rationale.
- Fix the lowest layer that can correctly solve the problem. Do not add higher-layer cleanup, post-processing, or guardrail logic unless that is the actual requirement.
- If two fixes are correct, choose the one with fewer new types, helpers, fields, callbacks, packages, lines, and moving parts.
- Treat user-stated behavior, mechanisms, and invariants as requirements. Do not swap in an "equivalent" mechanism without calling out the semantic difference and getting approval.
- Reuse existing concepts first. Do not add a new kind, type, field, helper, package, wrapper, callback, or exported symbol unless the existing code cannot express the change.
- Keep feature-local logic private. Do not export new functions or types unless another package truly needs them.
- Prefer existing domain types over parallel mirror types.
- Prefer standard-library helpers such as `slices.Contains`, `slices.Clone`, `slices.SortFunc`, `slices.CompactFunc`, and `cmp.Compare` over custom bookkeeping.
- Do not add instrumentation, counters, logging, extra state, or extra indirection unless required for correctness or explicitly requested.
- Do not add indirection around hard exits, panics, clocks, callbacks, or process control unless the user explicitly asks for it or the subsystem already uses that pattern.
- Keep local fixes local in code structure. If a change that should be small starts touching 3 or more packages, introducing new concepts, or turning into a rewrite, stop and restate the smallest literal implementation before continuing.

## Simplification

- When the user asks to simplify, default to deleting code, branches, helpers, wrappers, state, and tests that no longer buy their keep. Prefer subtraction over rearrangement.
- Do not treat file splits, renames, package moves, or abstraction swaps as simplification unless they also reduce code and concepts.
- If the user describes a change as simple, small, "10 lines", "just", or reacts negatively to complexity, take that literally. Bias immediately toward the most direct implementation.
- When the user pushes back on complexity, remove complexity immediately. Do not defend, elaborate, or refine the abstraction; simplify it.
- If the user's goal is to reduce size, optimize for net line deletion, not aesthetic refactoring. Measure success by smaller diffs and fewer lines, not cleaner file boundaries.
- For "simplify" work, prefer these in order: delete dead behavior, collapse duplicate control flow, merge parallel state paths, inline one-use helpers, compress repetitive tests.
- Do not answer a request to simplify by introducing a new framework, new abstraction layer, or same-size rewrite. If the code is not getting shorter, stop and reconsider.
- When features must remain, simplify by unifying implementations underneath them rather than preserving separate code paths per mode, source, or case.
- If a simplification pass increases lines first with a promise to reduce them later, that is usually the wrong direction.
- When the user says "make it smaller", "simplify", or complains about size, treat line count as a first-class constraint, not a side effect.
- After any simplification pass, re-check the user's original invariants explicitly. Passing tests are not enough if semantics drift.

## Defensive Code

- Do not write defensive code unless the human partner explicitly asks for it.
- Defensive code means nil guards for values that cannot be nil by contract, fallback defaults for impossible states, catch-all branches for unreachable cases, silent normalization for programmer errors, extra validation for internal call paths, wrapper functions that only protect against misuse, or speculative timeouts/retries/limits not required by the stated behavior.
- Treat defensive code as a bug: delete it or ask before adding it.
- If the code's contract is unclear, stop and ask instead of adding a guardrail.
- Before finalizing any change, actively remove defensive guards you added or touched.
- In particular, do not add or preserve `if ctx == nil { ctx = context.Background() }`, `if value == nil { return nil }`, double-start/double-stop checks, not-started checks, fallback initialization for required constructor fields, or silent handling for invalid internal call ordering unless the user explicitly asked for defensive behavior or an external/public API contract requires it.
- Tests should not assert misuse behavior for deleted guards; update tests to exercise the real contract instead.

## Injected Behavior Dependencies

- For injected behavior dependencies, do not use `nil` to mean disabled, optional, or not configured.
- Injected behavior dependencies include function callbacks, interfaces, service clients, senders, publishers, loggers, schedulers, runners, routers, bridges, and lifecycle hooks.
- Pass either the real dependency or an explicit inert implementation at the call site.
- Constructors should assign what they are given, not silently manufacture fallback defaults, unless the API is external/public and already documents nil as valid.
- Unavailable behavior belongs in a clear inert implementation, such as a private `inertX` type or inert callback, not in `if dep == nil` / `if callback != nil` branches.
- This rule applies only to behavior injection; it does not forbid nil checks for data state, decoded payload fields, optional API response fields, cache entries, timers, maps, slices, or pointer values where nil is part of the domain model.
- When changing code that touches an injected behavior dependency, search for existing nil checks on that dependency, replace optional nil behavior with explicit inert implementations, update tests and call sites to pass inert dependencies explicitly, and before finalizing grep for remaining `dep == nil` / `dep != nil` guards for the touched dependency names.

## Tests

- Keep regression tests minimal, behavioral, and targeted to the reported failure. Prefer one narrow contract test over scaffolding-driven tests.
- When simplifying, remove or compress repetitive tests along with the code. Prefer one table-driven test over many near-duplicates if coverage stays equivalent.
- Do not add tests for behavior that is being removed.
- For simplification work, avoid adding broad new tests unless they are required to protect behavior during deletion.
- For message-flow changes, verify queue order, prompt framing, silent or delivery behavior, and outbound routing separately. Do not assume fixing one fixes the others.

## OpenCode Parity

- When asked to “match OpenCode”, first determine whether parity is required for:
  1. user-visible behavior/output
  2. API shape/signature
  3. underlying implementation
- Default to matching behavior and output first unless the user explicitly asks for API or implementation parity too.
- Cite the exact upstream OpenCode reference files in comments and tests when implementing parity-sensitive behavior.

## Tool Output Contracts

- For tool-like methods such as `Read`, `Glob`, and `Grep`, prefer tests that assert exact user-visible output when practical.
- Cover empty results, ordering, truncation notices, invalid input behavior, and returned path format.
- Assertions should verify the contract, not just that output is non-empty.

## Filesystem Tests

- When testing `sandboxedFileSystem`, create and mutate test files through `*os.Root` methods such as `root.WriteFile`, `root.Mkdir`, `root.OpenFile`, and related APIs.
- Do not use host-level helpers like `os.WriteFile(dir + "/file.txt", ...)` in those tests, because that bypasses the sandbox contract and can hide integration mistakes.
- If a test intentionally uses host filesystem APIs, add a short comment explaining why bypassing the sandbox is necessary.

## Sandboxed External Commands

- If code shells out to external tools like `rg`, validate the requested path through `*os.Root` first.
- Run the external command inside the resolved sandbox directory.
- Do not let host-level commands bypass the sandbox contract.
- Tests for sandboxed command behavior should still create fixtures through `*os.Root`.

## Implementation Checks

- Before finalizing a filesystem-related change, verify:
  1. Production code accesses files through the intended abstraction.
  2. Tests build fixtures through the same abstraction.
  3. Assertions cover the contract, not just non-empty output.

## Go Coding Standards

- Mandatory Go work lifecycle: before touching Go code, during edits, before tests, and before final response, explicitly apply the standards below to the actual touched files and changed hunks. Do not rely on memory or intent. If any item fails, fix it before continuing.
- Before editing Go code, convert the bootstrap materials into active constraints for the current task: Effective Go and CodeReviewComments for style/API shape, gofix/Go 1.26 materials for modern standard-library idioms, testing-time/synctest for concurrency and timing tests, osroot for filesystem safety, cleanups/weak only when truly needed, iter for sequence APIs, mutex-hat guidance for synchronization layout, and context-and-structs for context lifetimes. Keep the smallest root-cause-aware implementation that satisfies the user-stated behavior.
- During Go edits, enforce simplicity continuously: no speculative features, no one-use helpers unless they materially clarify dense code, no new abstractions/types/fields/callbacks/packages unless existing concepts cannot express the change, no defensive guards for impossible internal states, no unnecessary exported symbols, no context stored in structs, no extra goroutine or timer machinery unless the behavior requires it, and no multiple-mutex design unless the lifecycle genuinely demands it. Single-line helper functions are violations and must be inlined to their call sites.
- Before running tests, inspect the actual touched diff for every Go standard violation: error variable names (`errCombined`, `errRead`, `errClose`, not `combinedErr`, `readErr`, `closeErr`), error type names ending in `Error`, single-use helpers to inline, all single-use single-line functions to delete by inlining, defensive guards to delete, accidental abstraction growth, mutex-hat placement, context misuse, exported names, changed-line necessity, and queue/order semantics for message flow. Fix the diff first; do not use tests as a substitute for this review.
- Before final response, repeat the touched-diff standards pass after formatting/lint/test tools have modified files. Verify the user's original invariants explicitly, verify `make test`, and do not report success while any standard or semantic invariant remains failing.
- Before finalizing Go edits, inspect touched constructors and callback/interface fields. Injected behavior dependencies must be real or explicit inert values; do not use nil as the disabled/optional marker, and do not add constructor fallback defaults for them.
- Before finalizing Go edits, review every new or renamed error variable in the touched diff and rename nonconforming locals such as `runErr`, `waitErr`, or `parseErr` to `errRun`, `errWait`, or `errParse`.
- Avoid single-use functions. If a helper is only called once, inline it by default. Only extract it when it is reused or materially clarifies a dense block. Single-line helper functions are not allowed; inline them to their call sites.
- Use `go doc`; use all features of Go 1.26.2 or newer when appropriate.
- Avoid struct and interface embedding in Go; use named fields and explicit forwarding methods so ownership and method sets stay visible.
- Architecturally, if you are using multiple mutexes in the same struct, it means you are making things overcomplex. Consider either reading once at the start and using the resulting value over time, or consolidating lifetimes under a single mutex hat.

## Verification

- Before finalizing Go changes, run:
  1. `gofmt` on touched files
  2. `go test ./...`
  3. `make lint`
  4. `make test` when available
- If `make test` is the authoritative superset in this repo, prefer it and note any additional targeted verification that was also useful.

## Review Checklist

- Ask: `Does this test accidentally bypass the layer being tested?`
- Ask: `Would this still catch a bug in the sandbox/root wrapper itself?`
- Ask: `Did I red-team the changed behavior before calling it done?`

# 4 CODING PRINCIPLES

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- No defensive guards for impossible states or internal misuse unless the human partner explicitly asked for defensive coding.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" -> "Write tests for invalid inputs, then make them pass"
- "Fix the bug" -> "Write a test that reproduces it, then make it pass"
- "Refactor X" -> "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] -> verify: [check]
2. [Step] -> verify: [check]
3. [Step] -> verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

---


coding-standard(error handling): use errors.AsType
```
$ go doc errors AsType
package errors // import "errors"

func AsType[E error](err error) (E, bool)
    AsType finds the first error in err's tree that matches the type E, and if
    one is found, returns that error value and true. Otherwise, it returns the
    zero value of E and false.

    The tree consists of err itself, followed by the errors obtained by
    repeatedly calling its Unwrap() error or Unwrap() []error method. When
    err wraps multiple errors, AsType examines err followed by a depth-first
    traversal of its children.

    An error err matches the type E if the type assertion err.(E) holds,
    or if the error has a method As(any) bool such that err.As(target) returns
    true when target is a non-nil *E. In the latter case, the As method is
    responsible for setting target.
```
