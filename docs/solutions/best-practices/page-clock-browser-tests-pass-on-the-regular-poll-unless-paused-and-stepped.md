---
title: Page-Clock Browser Tests Pass on the Regular Poll Unless the Clock Is Paused and Stepped
date: 2026-11-24
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: best_practice
component: frontend
severity: medium
applies_when:
  - Writing a Web browser test that must show something happens sooner than a timer-driven poll would make it happen, such as a ring, push or stream event
  - Using page.clock.install, fastForward, pauseAt or runFor in an internal/rocketclaw/web browser test
  - Changing a timed wait in the Web (the session list's 2-second listing wait in ui.tsx, or any other setTimeout chain) that clock-driven tests step through
  - A clock-driven browser test passes even after the behavior it covers is broken on purpose
symptoms:
  - A ring test still passed with the ring handler emptied and the bundle rebuilt
  - "`pauseAt` failed with `Error: Cannot fast-forward to the past`"
  - "session-poll.browser.test.ts timed out waiting for the second saved listing after the listing wait became `delay(500).then(() => delay(1500))`"
root_cause: wrong_api
resolution_type: test_fix
tags:
  - playwright
  - page-clock
  - fake-timers
  - browser-tests
  - mutation-check
  - polling
  - listing-ring
---

# Page-Clock Browser Tests Pass on the Regular Poll Unless the Clock Is Paused and Stepped

## Context

The Web's session list fetches each server's listing on a 2-second wait, and a ring on that server's `/listing`
stream cuts the wait short (`ServerSessions` in `internal/rocketclaw/web/src/ui.tsx`). The browser tests in
`internal/rocketclaw/web/src/listing-ring.browser.test.ts` must prove that a ring, not the poll, caused each fetch.
Three wrong beliefs about Playwright's page clock let those tests pass with rings broken, and one of them also broke an
unrelated test when the product's wait changed shape.

1. **`page.clock.install()` does not stop time.** It installs fake timers that keep following real time. Time stops
   only after `pauseAt` (Playwright's clock guide installs, lets the page load, and then calls `pauseAt`). The first
   ring tests installed the clock and never paused it, so the 2-second poll ran on its own during every assertion and
   could satisfy it with rings broken. A code review caught this, not a test.
2. **A flush that jumps past a poll leaves the next poll inside the assertion window.** After pausing, the tests
   first moved the clock forward to let any pending poll run. Advancing a fixed amount could run one poll and then
   carry on, so the next poll's 2 seconds started part-way through the flush and came due inside the 1.5-second window
   the ring assertion then checked. With rings broken and the bundle rebuilt, both tests still passed. They failed
   only after the flush stopped at the first listing fetch.
3. **A wait made of two chained timers does not finish under a clock jump.** During the review fixes the listing wait
   briefly became `delay(500).then(() => delay(1500))`. `session-poll.browser.test.ts` then timed out after
   `pauseAt(Date.now() + 2_000)`. In playwright-core 1.63, `fastForward` and `pauseAt` move every overdue timer to the
   target time and fire each one once (`_innerFastForwardTo`). The second timer only exists after the first fires, so
   it is due 1.5 seconds after the target. Under `pauseAt` it never fires. Under `fastForward` it waits for 1.5
   seconds of real time after the jump.

A smaller trap on the way: calling `pauseAt` with a time the test read earlier failed with `Cannot fast-forward to the
past`, because the unpaused clock had already moved on. Read the time inside the page and pause a little ahead of it:
`page.clock.pauseAt(await page.evaluate(() => Date.now() + 1_000))`.

## Guidance

**Pause the clock once the page has loaded, then step it in small slices with a real pause between slices.** The ring
tests do this with an `advance` helper:

```ts
async function advance(page, done: () => boolean, ms: number) {
  for (let spent = 0; spent < ms && !done(); spent += 100) {
    await page.clock.runFor(100);
    await Bun.sleep(50);
  }
  return done();
}
```

`runFor` fires every timer due inside its slice, including timers created during the slice. The short real sleep
gives the page time for the work a timer starts but the clock does not control, such as fetches, stream events and
promise continuations, before the clock moves again. The helper stops as soon as the condition holds, so it never
runs past the event the test is waiting for.

**Flush pending polls up to the first fetch, not by a fixed amount.** After pausing, the tests run
`advance(page, () => local.counts.listings > loaded, 2_000)`, wait until the fetch count stops changing, and only
then ring. The next poll is then a full 2 seconds away, and every ring assertion uses a window shorter than that
(1.5 seconds). Only a ring can make those assertions pass. The last step of the first test still checks that the
poll runs when the stream is down, by giving `advance` 3 seconds after the stream is refused.

**Keep a timed wait to one timer that an event cuts short, and add a second timer only when it is needed.** The
listing loop's `next` waits on one `delay(2000, ...)` that a ring aborts. Only when the ring arrives within half a
second of the end of the previous fetch does it wait out the rest of that half second with a second `delay`. A plain poll never
has a second timer, so clock jumps in other tests still reach the fetch.

**Prove a timing test with a mutation check.** Empty the code under test (here, make the ring do nothing), rebuild
the bundle (browser tests run the last bundle that built), and confirm every affected test fails. The paused-clock
version with the fixed-length flush passed this check, which is how mistake 2 was found.

## Why This Matters

A timing test that passes on the regular poll proves nothing about the faster path, and it stays green while that
path is broken. Repository rules forbid source comments, so the test file cannot explain why it pauses, flushes to
the first fetch, and keeps each window under 2 seconds. Someone tidying it up, for example by replacing `advance`
with one `fastForward`, would bring the false pass back without any test failing.

The chained-timer trap reaches beyond the test being written: a harmless-looking refactor of a product wait into two
timers breaks other tests that jump the clock, and the error is a timeout far from the change.

## When to Apply

- A browser test asserts that something happens because of an event, while a timer would make the same thing happen
  later.
- A test uses `fastForward` or `pauseAt` across a wait that the page builds from more than one timer, or from a timer
  that starts after a fetch or a promise resolves.
- A product change splits one timed wait into several, or adds a delay after a fetch; check every clock-driven test
  that steps through it (`grep -rn 'page.clock' internal/rocketclaw/web/src`).

## Examples

Before, in outline, the ring assertion ran on the live clock:

```ts
await page.clock.install();
await page.goto(url);
local.ring();
await until(() => local.counts.listings === before + 1);
```

After, the clock is paused, flushed to the first fetch, and stepped inside a window shorter than the poll:

```ts
await page.clock.install();
await page.goto(url);
await page.getByRole("tab", { name: "Chat work" }).waitFor();
await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1_000));
const loaded = local.counts.listings;
await advance(page, () => local.counts.listings > loaded, 2_000);
const before = await settled(() => local.counts.listings);
local.ring();
expect(await advance(page, () => local.counts.listings === before + 1, 1_500)).toBe(true);
```

Product wait, before (in outline) and after:

```ts
// breaks clock jumps: the second timer exists only after the first fires
void delay(500, signal).then(() => delay(1500, signal)).then(tick);

// one timer a ring cuts short, plus a top-up only after a ring
void delay(2000, AbortSignal.any([ac.signal, signal])).then(async () => {
  const floor = fetched + 500 - Date.now();
  if (floor > 0) await delay(floor, ac.signal);
  await tick();
});
```

## Related

- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: rebuild the bundle before a
  mutation check, or the check runs the old code.
- `docs/solutions/architecture-patterns/fence-the-hidden-opencode-view-and-test-it-where-it-can-fail.md`: another
  browser test that had to be made to fail where the behavior can fail.
- `docs/solutions/logic-errors/stale-opencode-sync-ended-a-new-opencode-park-early.md`: the change these ring tests
  were written for.
