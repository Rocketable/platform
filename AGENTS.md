# AGENTS.md

## Most Fundamental Instruction

Be extraordinarily skeptical of your own correctness and stated assumptions. You are not a cynic; you are a careful critical thinker who hates being wrong.

When appropriate, broaden the inquiry beyond the stated assumptions to identify unconventional opportunities, risks, and pattern matches that widen the aperture of solutions.

Before calling anything "done" or "working", take a second look and red-team whether it is really done or really working.

## Bootstrap

On start, you must always read:
- https://docs.jj-vcs.dev/latest/git-command-table/
- https://go.dev/doc/effective_go
- https://go.dev/wiki/CodeReviewComments
- https://go.dev/blog/gofix
- https://go.dev/blog/testing-time
- https://go.dev/blog/osroot
- https://go.dev/blog/cleanups-and-weak
- https://go.dev/blog/synctest
- https://pkg.go.dev/iter
- https://dmitri.shuralyov.com/idiomatic-go/entries/2
- https://go.dev/blog/context-and-structs

## General Workflow

- Read the relevant source code before asking clarifying questions or proposing edits.
- **NON-NEGOTIABLE TEMPORARY-DIRECTORY POLICY:** The only acceptable location for temporary files, scratch data, throwaway artifacts, clones, Git worktrees, and Jujutsu workspaces is `<repo-root>/.tmp/`. Never use `/tmp`, `$TMPDIR`, system temporary directories, or temporary directories anywhere else. Create `<repo-root>/.tmp/` when needed. Every repository skill must include and enforce this policy; if any skill conflicts with it, this policy wins.
- Whenever you create a Markdown file of any kind, including a spec or plan, and detect that you are running inside cmux, immediately open it in a preview tab with `cmux markdown open <path> --focus true`.
- Place documents produced by Superpowers, Compound Engineering, or similar workflows under the narrowest affected component's `docs` directory, such as `internal/rocketclaw/docs` or `internal/rocketcode/docs`. Use purpose-based paths that contain no workflow or tool branding, including `superpower`, `superpowers`, `compound-engineering`, `ce`, or variants. Before placing a document at repository scope for a repository-wide change, ask the human partner/principal for approval.
- Never use `git`. Use `jj` for repository inspection and history, and always use `jj diff --git` for diffs.
- Use `go doc` and `gopls` (use `go run golang.org/x/tools/gopls@latest --help`) often to find issues and opportunities to improve Go code.
- Always react to GPT comments (`// GPT:`) by doing what the human partner asked and then deleting the comment when you accomplish the stated goal.
- Do not disable linters with `//nolint`, config changes, or command flags without explicit human approval. If a linter finding seems wrong, first fix the code or ask for approval with the exact suppression and rationale.
- Prefer the same abstraction in tests that production code uses.
- Rocketable Platform targets Unix-like systems only: Linux and macOS. Do not add Windows-specific code paths or preserve Windows compatibility unless the human partner explicitly changes this policy.
- In the final response, state whether README impact was considered and whether a README update was needed.

## Change Discipline

- For bug fixes, make the smallest root-cause-aware change that fits the existing structure.
- Fix the lowest layer that can correctly solve the problem. Do not add higher-layer cleanup, post-processing, or guardrail logic unless that is the actual requirement.
- If two fixes are correct, choose the one with fewer new types, helpers, fields, callbacks, packages, lines, and moving parts.
- Treat user-stated behavior, mechanisms, and invariants as requirements. Do not swap in an "equivalent" mechanism without calling out the semantic difference and getting approval.
- Reuse existing concepts first. Do not add a new kind, type, field, helper, package, wrapper, callback, or exported symbol unless the existing code cannot express the change.
- Keep feature-local logic private. Do not export new functions or types unless another package truly needs them.
- Prefer existing domain types over parallel mirror types.
- When removing a config field, API option, feature branch, or behavior, remove its field, call sites, docs, examples, normalization, filtering, and dedicated tests. Do not replace removed behavior with new explicit validation, rejection paths, compatibility shims, migration code, or error-message tests unless the human partner explicitly asks for that preserved rejection contract.
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
- When a nil or NilChecks value would be used to mean disabled, absent behavior, or safe fallback, use an explicit inert implementation or the type's zero value instead. Do not encode optional behavior as nil checks.
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
- Before adding any new test, search for existing tests that already exercise the affected public behavior. Prefer updating or deleting those tests over adding parallel coverage, especially for removed behavior, signature changes, config shape changes, or defaulting changes.
- If a removed field or option is ignored by normal decoding after deletion, do not add a test that asserts it is rejected. Unknown-field rejection is new behavior unless it already existed for that config surface.
- For simplification work, avoid adding broad new tests unless they are required to protect behavior during deletion.
- For message-flow changes, verify queue order, prompt framing, silent or delivery behavior, and outbound routing separately. Do not assume fixing one fixes the others.

## Go Coding Standards

- Mandatory Go work lifecycle: before touching Go code, during edits, before tests, and before final response, explicitly apply these standards to the actual touched files and changed hunks. Do not rely on memory or intent. If any item fails, fix it before continuing.
- Before editing Go code, convert the bootstrap materials into active constraints for the current task: Effective Go and CodeReviewComments for style/API shape, gofix/Go 1.26 materials for modern standard-library idioms, testing-time/synctest for concurrency and timing tests, osroot for filesystem safety, cleanups/weak only when truly needed, iter for sequence APIs, mutex-hat guidance for synchronization layout, and context-and-structs for context lifetimes.
- During Go edits, enforce simplicity continuously: no speculative features, no one-use helpers unless they materially clarify dense code, no new abstractions/types/fields/callbacks/packages unless existing concepts cannot express the change, no defensive guards for impossible internal states, no unnecessary exported symbols, no context stored in structs, no extra goroutine or timer machinery unless the behavior requires it, and no multiple-mutex design unless the lifecycle genuinely demands it.
- If a helper is only called once, inline it by default. Only extract it when it is reused or materially clarifies a dense block.
- Single-use single-line functions are always violations and must be inlined.
- New or touched one-line delegating wrappers that only delegate to another function, pass default arguments, rename a call, or preserve an old call shape are violations even when used more than once; call the target directly or give the function a real body that owns behavior.
- Before running tests, inspect the actual touched diff for every Go standard violation: error variable names (`errCombined`, `errRead`, `errClose`, not `combinedErr`, `readErr`, `closeErr`), error type names ending in `Error`, single-use helpers to inline, one-line delegating wrappers to delete by inlining, defensive guards to delete, accidental abstraction growth, mutex-hat placement, context misuse, unused context parameters in interface implementations, exported names, changed-line necessity, queue/order semantics for message flow, and source CLOC impact.
- Before final response, repeat the touched-diff standards pass after formatting/lint/test tools have modified files. Verify the user's original invariants explicitly, verify all required tests including CLOC/coverage budgets, and do not report success while any standard, budget, or semantic invariant remains failing.
- Before finalizing Go edits, inspect touched constructors and callback/interface fields. Injected behavior dependencies must be real or explicit inert values; do not use nil as the disabled/optional marker, and do not add constructor fallback defaults for them.
- When designing Go interfaces, do not add `context.Context` by convention. Include `context.Context` only when the method implementation must observe cancellation/deadlines or pass the context to downstream work. If every current implementation would name the parameter `_ context.Context`, the interface is wrong: remove the context parameter or redesign the API around the operation that actually needs cancellation.
- Avoid struct and interface embedding in Go; use named fields and explicit forwarding methods so ownership and method sets stay visible.
- Avoid `sync/atomic`; if you find use of package `sync/atomic`, ask the human partner for permission first.
- For goroutine coordination, use https://pkg.go.dev/golang.org/x/sync/errgroup and https://pkg.go.dev/github.com/alitto/pond/v2.
- If you are using multiple mutexes in the same struct, consider whether you are making things overcomplex. Prefer reading once at the start and using the resulting value over time, or consolidating lifetimes under a single mutex hat.
- Whenever you feel like adding a mutex, question why you need this mutex instead of using another mutex that encapsulates the lifetime of the variables you must work with.
- In Go, error variables always start with `err` and error types always end with `Error`. For example: `errWriter` and `WriterError`.
- Before finalizing Go edits, review every new or renamed error variable in the touched diff and rename nonconforming locals such as `runErr`, `waitErr`, or `parseErr` to `errRun`, `errWait`, or `errParse`.
- Bias toward strong error types for new error contexts when practical. Prefer typed errors with `Unwrap` over ad hoc string-only `fmt.Errorf` wrappers when callers may benefit from structured operation context.
- Prefer strong types over `map[string]any`. Use `map[string]any` only at truly dynamic boundaries where the key set is not known at compile time; otherwise define a small struct with explicit fields. In tests, typed request/result structs are still required when the shape is known; keep `map[string]any` confined to the unavoidable protocol/schema boundary and do not let it spread into helper APIs or assertions.
- Use Go-style enum types for operation/category fields instead of raw strings.
- Use `errors.AsType[T]()` for typed error extraction instead of legacy `errors.As` target variables.
- Use all appropriate features of Go 1.26.2 or newer.

## Verification

- Before finalizing Go changes, run `gofmt` on touched files.
- Before finalizing Go changes, run `go test ./...`.
- Before finalizing Go changes, run `make lint`.
- Before finalizing Go changes, run `make test`.
- If any required verification cannot be run, stop and report why instead of claiming success.

## RocketClaw-Specific Requirements

- Slack is RocketClaw's primary text connector.

## RocketCode-Specific Requirements

- When asked to "match OpenCode", first determine whether parity is required for user-visible behavior/output, API shape/signature, or underlying implementation.
- Default to matching behavior and output first unless the user explicitly asks for API or implementation parity too.
- Cite the exact upstream OpenCode reference files in comments and tests when implementing parity-sensitive behavior.
- For tool-like methods such as `Read`, `Glob`, and `Grep`, prefer tests that assert exact user-visible output when practical.
- Cover empty results, ordering, truncation notices, invalid input behavior, and returned path format.
- Assertions should verify the contract, not just that output is non-empty.
- When testing `sandboxedFileSystem`, create and mutate test files through `*os.Root` methods such as `root.WriteFile`, `root.Mkdir`, `root.OpenFile`, and related APIs.
- Do not use host-level helpers like `os.WriteFile(dir + "/file.txt", ...)` in sandbox tests, because that bypasses the sandbox contract and can hide integration mistakes.
- If a test intentionally uses host filesystem APIs, add a short comment explaining why bypassing the sandbox is necessary.
- If code shells out to external tools like `rg`, validate the requested path through `*os.Root` first.
- Run external commands inside the resolved sandbox directory. Do not let host-level commands bypass the sandbox contract.
- Tests for sandboxed command behavior should still create fixtures through `*os.Root`.
- Before finalizing a filesystem-related RocketCode change, verify production code accesses files through the intended abstraction, tests build fixtures through the same abstraction, and assertions cover the contract rather than only non-empty output.

## CLOC And Metrics

- Never edit `SOURCE_CLOC_BUDGET` in `Makefile`.
- Never move, create, or hide first-party project code under `vendor/`, `third_party/`, generated-code paths, ignored paths, test-only files, or any metric-excluded directory to evade CLOC, coverage, lint, review, or ownership checks.
- Metric constraints are requirements, not obstacles to route around. If a real implementation exceeds a budget, reduce first-party production code honestly, delete or simplify existing code, or stop and report the budget conflict.

## Four Coding Principles

These behavioral guidelines reduce common LLM coding mistakes. They bias toward caution over speed; for trivial tasks, use judgment.

### 1. Think Before Coding

Do not assume. Do not hide confusion. Surface tradeoffs.

- State your assumptions explicitly before implementing. If uncertain, ask.
- If multiple interpretations exist, present them; do not pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop, name what is confusing, and ask.

### 2. Simplicity First

Minimum code that solves the problem. Nothing speculative.

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that was not requested.
- No error handling for impossible scenarios.
- No defensive guards for impossible states or internal misuse unless the human partner explicitly asked for defensive coding.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

Touch only what you must. Clean up only your own mess.

- Do not "improve" adjacent code, comments, or formatting.
- Do not refactor things that are not broken.
- Match existing style, even if you would do it differently.
- If you notice unrelated dead code, mention it; do not delete it.
- Remove imports, variables, and functions that your changes made unused.
- Do not remove pre-existing dead code unless asked.

Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

Define success criteria. Loop until verified.

- "Add validation" means write tests for invalid inputs, then make them pass.
- "Fix the bug" means write a test that reproduces it, then make it pass.
- "Refactor X" means ensure tests pass before and after.

For multi-step tasks, state a brief plan:

```text
1. [Step] -> verify: [check]
2. [Step] -> verify: [check]
3. [Step] -> verify: [check]
```

Strong success criteria let you loop independently. Weak criteria such as "make it work" require clarification.
