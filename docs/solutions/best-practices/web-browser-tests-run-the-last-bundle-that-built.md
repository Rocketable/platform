---
title: Web Browser Tests Run the Last Bundle That Built, Not the Source
date: 2026-10-26
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: frontend
severity: medium
applies_when:
  - Running a single internal/rocketclaw/web browser test with bun test after editing src/ by hand
  - Checking that a test fails when the code it guards is broken (a mutation check)
  - Adding temporary debug code (window globals, console.log) to ui.tsx to watch a browser test
tags:
  - web
  - browser-tests
  - bun-build
  - tsc
  - dist
  - mutation-check
---

# Web Browser Tests Run the Last Bundle That Built, Not the Source

## Context

The Web browser tests do not compile `src/`. Each one serves the prebuilt bundle in `internal/rocketclaw/internal/web/dist` (for example `src/tabs.browser.test.ts:7` and `src/search-page.browser.test.ts:6`: `const dist = path.resolve(import.meta.dir, "../../internal/web/dist")`). The Go transport tests serve the same directory.

That bundle is written by `bun run build`, which is `tsc --noEmit && bun build.ts` (`package.json:12`). `build.ts` empties `dist` and writes the new bundle (`build.ts:7`, ending with `Built SPA: ...` at `build.ts:38`), but only after `tsc` passes. When `tsc` fails, `build.ts` never runs, `dist` keeps the previous bundle, and the only sign is `error: script "build" exited with code 1`.

`make test` in `internal/rocketclaw/web` is safe, because its `test` target depends on `build` (`Makefile:42`) and stops on that failure. The trap is the quick loop of running `bun run build` and then `bun test src/<file>` by hand, with the build's output hidden or unread.

While building the many-Search-pages change (PR #695), this trap fooled two checks in a row:

- **A mutation check that "survived".** To prove a test guarded a refresh, the guard was broken with `if (seen.current === shown || true) return;`. TypeScript rejects the always-true condition, the build stopped, and the test passed against the old, correct bundle. The test looked useless, but it was fine.
- **Debug output that "never appeared".** Temporary instrumentation (`(window as any).mounts = ...`, and `console.log`) did not type-check, so the browser ran the old bundle without it. That looked like the production build stripping `console.log`, which it does not.

A second mutation, `seen.current = shown; return;`, failed too, with "unreachable code". Only a mutation that type-checks, `if (shown || typing.current || ...)`, produced a real build, and the test then failed as it should.

## Guidance

- **Read the build's last line before trusting any browser test run.** Run `bun run build 2>&1 | tail -1` and continue only when it prints `Built SPA: .../dist/index.html`. Never send the build's output to `/dev/null` in a loop that then runs `bun test`.
- **Make mutations and debug code type-check.** TypeScript rejects always-true conditions, unreachable code after `return`, and loose `window` casts, so a careless mutation never reaches the bundle. Prefer mutations that flip a real condition, such as adding a truthy operand or changing `&&` to `||`, and cast debug globals through `unknown` to a concrete type.
- **When a test or debug run surprises you, check the bundle first.** Before concluding that a test is weak or that logging is stripped, confirm `dist` was rebuilt, for example by checking the build output or the `dist/assets/main-*.js` timestamp.
- **For anything you will rely on, use `make test`**, which builds first and stops on a type error.

## Why This Matters

A stale bundle turns verification into false confidence in both directions. A mutation that never reached the browser says the test does not guard the code, which can lead to deleting or "strengthening" a test that already works. A real regression can also pass, if its source change type-checks badly and the old bundle hides it. Neither shows up as a test failure: the failure is a few lines up, in output the loop discarded.

## When to Apply

- Any hand-run `bun test` of `src/*.browser.test.ts` or `src/*-transport.test.ts` after editing `src/`.
- Mutation checks that prove a new browser test fails without the fix.
- Temporary instrumentation in `ui.tsx` to watch what a browser test sees.

## Examples

Unsafe loop. The build failure is hidden, so the test runs the old bundle:

```bash
bun run build >/dev/null 2>&1; bun test src/tabs.browser.test.ts -t "Search pages are tabs"
```

Safe loop. Stop unless the bundle was actually rebuilt:

```bash
bun run build 2>&1 | tail -1 | grep -q '^Built SPA:' && bun test src/tabs.browser.test.ts -t "Search pages are tabs"
```

A mutation that type-checks and therefore reaches the browser:

```ts
// Rejected by tsc: never built, so the test runs the old bundle.
if (seen.current === shown || true) return;
// Type-checks: built, and the guarded test fails as it should.
if (shown || typing.current || !page?.searches.length) return;
```

Related: `docs/solutions/best-practices/dupword-fix-rewrites-repeated-test-strings.md` describes another way a tool step changes what tests run, and `docs/solutions/performance-issues/search-page-froze-rendering-every-result.md` describes a fix whose browser test depends on a fresh bundle.
