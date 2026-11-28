---
title: A Fake OpenCode Event Stream Without Keepalives Reconnects Every 45 Seconds and Hides Stuck Loads
date: 2026-11-27
category: docs/solutions/best-practices/
module: internal/rocketclaw/oc
problem_type: best_practice
component: frontend
severity: high
applies_when:
  - Writing a host test in internal/rocketclaw/oc/host/mount.test.tsx or a browser test that serves internal/rocketclaw/oc/host/test/server.ts, where the test checks that a failed or stalled load recovers, or stays stuck
  - Probing an OpenCode view by watching it for longer than 45 seconds against a fake event stream
  - Changing the fake server's /event stream, or the OpenCode client's idle watchdog (defaultIdleTimeout in connection.ts)
  - A recovery test or probe passes even though nothing in the code under test retries
symptoms:
  - Browser probes that stalled one transcript request showed the OpenCode view recovering on its own within about a minute
  - The same stall against an event stream that sent keepalives left the session with no messages for more than 2 minutes, and leaving for a chat and coming back did not help
root_cause: incomplete_setup
related_components:
  - internal/rocketclaw/web
  - docs/verification/oc-tab-recovery
tags:
  - opencode
  - test-fake
  - event-stream
  - keepalive
  - idle-watchdog
  - false-pass
  - mutation-check
---

# A Fake OpenCode Event Stream Without Keepalives Reconnects Every 45 Seconds and Hides Stuck Loads

## Context

An OpenCode tab could stay without its messages after a network hiccup until the page was reloaded. The cause was
that a failed transcript load of the shown session was never tried again while the event stream stayed connected.
The fix added retries in the copied `internal/rocketclaw/oc/packages/app/src/session/session-resolution.ts` (the
`rocketclaw:`-marked `sync.transcript` and metadata branches of `run`). The model is in
`docs/verification/oc-tab-recovery/`.

Finding the bug took longer than it should have, because every early probe against the fake OpenCode server showed
the view recovering by itself. The fake server was rescuing it:

1. The real OpenCode server writes a keepalive comment on its event stream every 15 seconds (the doc comment on
   `idleTimeout` in `ClientConnectionOptions`, `internal/rocketclaw/oc/packages/client/src/solid/connection.ts`).
2. The client aborts a stream that receives no bytes for `defaultIdleTimeout`, 45 seconds (`touch` in `connect`, same
   file). `createClientConnection` in `internal/rocketclaw/oc/packages/app/src/runtime/server/client.tsx` passes no
   `idleTimeout`, so the view uses 45 seconds.
3. The fake server's `stream()` in `internal/rocketclaw/oc/host/test/server.ts` sends one `server.connected` event
   and then nothing. So with the fake, the watchdog aborts the stream every 45 seconds with "Event stream stalled",
   and the client reconnects.
4. A reconnect looks like a network recovery to the view. While the connection is not `connected`, `data.ts`
   invalidates every loaded resource (the `createEffect` that calls `sync.invalidate()` in
   `internal/rocketclaw/oc/packages/client/src/solid/data.ts`). When it is `connected` again, session resolution
   dispatches `connected` for the shown session and loads it again (the `on(connected, ...)` computation in
   `session-resolution.ts`). That reload filled in the stuck transcript.

So against the fake, a stuck load of the shown session was reloaded by a reconnect within about 45 seconds. Against a real server the stream stays
alive, no reconnect comes, and the session stays empty. The bug was confirmed only after a probe served its own
event stream that sent `: keepalive` comments every 10 seconds. With that stream, one stalled transcript request left
the session empty for more than 2 minutes.

## Guidance

**Treat the fake's silent event stream as a hidden 45-second retry.** Any test or probe that runs longer than
45 seconds against `internal/rocketclaw/oc/host/test/server.ts` gets a free reconnect and reload of every session it shows. A result seen
after that point says nothing about how the code behaves against a real server.

**To show that something stays stuck, or recovers without a reconnect, do one of these:**

- Send keepalives on the event stream, the way the real server does. The probe that found the bug did this in its
  own `Bun.serve` handler for `/oc/box/api/event` (example below).
- Or finish the whole check well before 45 seconds. The regression tests in `mount.test.tsx` do this: they mock
  `TIMELINE_SYNC_TIMEOUT_MS` down to 1 second and wait at most 8 seconds for the transcript, so the watchdog cannot
  fire before the assertion.

**Do not use `server.streams` to prove that no reconnect happened.** The fake increments it when a stream opens and
decrements it when a stream closes. After a reconnect the count is the same as before. The recovery test in
`mount.test.tsx` asserts `server.streams` is unchanged, but the guard that actually rules out a reconnect rescue is
its 8-second time limit.

**Prove a recovery test with a mutation check.** Remove the retry, and confirm the test fails. A test the reconnect
can rescue passes with the retry removed.

**Recommended follow-up, not done here:** give the fake server's `/event` stream a keepalive comment on a timer, like
the real server's. Then no host or browser test gets the free reconnect. This is a change to a shared test fixture.
Turning it on may show tests that pass today only because of the reconnect. Each of those then needs its own
look.

## Why This Matters

A fake that misses a real server's keepalive does not just leave a gap in coverage. It adds a recovery path that
production does not have, and that path runs for every test that lasts long enough. This bug lived in exactly that
gap: "a load fails while the stream stays connected". Every test and probe of that case passed because the stream
did not stay connected. Nothing in the test output says a reconnect happened, and repository rules forbid comments
that could warn the next reader. So the trap is invisible from the current tree.

## When to Apply

- A test or probe asserts that an OpenCode view recovers from a failed, stalled or refused request.
- A probe watches an OpenCode view for longer than 45 seconds, for example to show that it stays blank.
- A change touches `stream()` in `internal/rocketclaw/oc/host/test/server.ts`, the client's `defaultIdleTimeout` or keepalive handling, or
  the reconnect behavior in `data.ts` or `session-resolution.ts`.
- An upstream OpenCode merge changes the server's keepalive interval or the client's watchdog.

## Examples

The fake server's stream today: one event, then silence until the client aborts.

```ts
start(controller) {
  const event = { type: "server.connected", id: "evt_1", created: 0, data: {} }
  controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`))
  signal?.addEventListener("abort", () => {
    close()
    controller.error(signal.reason)
  })
},
```

The probe's stream that showed the real behavior: the same first event, then a keepalive comment every 10 seconds, so
the 45-second watchdog never fires.

```ts
if (url.pathname === "/oc/box/api/event") {
  let timer: ReturnType<typeof setInterval>;
  return new Response(new ReadableStream({
    start(c) {
      c.enqueue(`data: ${JSON.stringify({ type: "server.connected", id: "evt_1", created: 0, data: {} })}\n\n`);
      timer = setInterval(() => { try { c.enqueue(": keepalive\n\n"); } catch { clearInterval(timer); } }, 10_000);
      request.signal.addEventListener("abort", () => clearInterval(timer));
    },
    cancel() { clearInterval(timer); },
  }), { headers: { "content-type": "text/event-stream" } });
}
```

The host regression test stays inside the window instead: the timeline gives up after 1 second, the stalled load
fails after 1.5 seconds, and the transcript must appear within 8 seconds, long before a reconnect could rescue it.

```ts
mock.module("@/session/timeline/sync-machine", () => ({ ...syncMachine, TIMELINE_SYNC_TIMEOUT_MS: 1_000 }))

view.show("box", linked)
await answered(view, linked, 8_000)
```

## Related

- `docs/solutions/best-practices/page-clock-browser-tests-pass-on-the-regular-poll-unless-paused-and-stepped.md`: the
  same trap in the Web, where the regular 2-second poll made ring tests pass with rings broken. The fix there is the
  same idea: keep each assertion window shorter than the background path that could rescue it.
- `docs/solutions/architecture-patterns/fence-the-hidden-opencode-view-and-test-it-where-it-can-fail.md`: other ways
  the same fake server and host tests made a check pass without its fix.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: rebuild `oc-dist` before a
  mutation check of a browser test.
- `docs/verification/oc-tab-recovery/README.md`: the model of the recovery fix this investigation led to.
