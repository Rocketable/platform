import { expect, test, mock } from "bun:test";
import { Effect } from "effect";
import { runPreload } from "./preload";

test("tab warming starts agents and skills together, then cron/config, and stops after unmount", async () => {
  for (const cancel of [false, true]) {
    const started = Promise.withResolvers<void>();
    const release = Promise.withResolvers<void>();
    const ready = Promise.withResolvers<void>();
    const calls: string[] = [];
    const signals: AbortSignal[] = [];
    const stop = runPreload(
      Effect.promise(async (signal) => { signals.push(signal); calls.push("agents"); await release.promise; }),
      Effect.promise(async (signal) => { signals.push(signal); calls.push("skills"); started.resolve(); await release.promise; }),
      Effect.sync(() => { calls.push("cron"); }),
      Effect.sync(() => { calls.push("config"); }),
      () => { calls.push("ready"); ready.resolve(); },
    );
    await started.promise;
    expect(calls).toEqual(["agents", "skills"]);
    if (cancel) stop();
    release.resolve();
    if (cancel) {
      // Drain the promise continuations of the two in-flight queries.
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(calls).toEqual(["agents", "skills"]);
      expect(signals.map((signal) => signal.aborted)).toEqual([true, true]);
    } else {
      await ready.promise;
      expect(calls).toEqual(["agents", "skills", "cron", "config", "ready"]);
      stop();
    }
  }
});

test("preload cleanup cancels idle and timer scheduling before queries start", async () => {
  const request = Object.getOwnPropertyDescriptor(globalThis, "requestIdleCallback");
  const cancel = Object.getOwnPropertyDescriptor(globalThis, "cancelIdleCallback");
  const scheduled = mock(() => 42);
  const cancelled = mock();
  const fetch = mock();
  const ready = mock();
  try {
    for (const idle of [true, false]) {
      Object.defineProperty(globalThis, "requestIdleCallback", { configurable: true, value: idle ? scheduled : undefined });
      Object.defineProperty(globalThis, "cancelIdleCallback", { configurable: true, value: cancelled });
      const query = Effect.sync(fetch);
      const stop = runPreload(query, query, query, query, ready);
      stop();
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(fetch).not.toHaveBeenCalled();
      expect(ready).not.toHaveBeenCalled();
    }
    expect(scheduled).toHaveBeenCalledTimes(1);
    expect(cancelled).toHaveBeenCalledWith(42);
  } finally {
    if (request) Object.defineProperty(globalThis, "requestIdleCallback", request);
    else Reflect.deleteProperty(globalThis, "requestIdleCallback");
    if (cancel) Object.defineProperty(globalThis, "cancelIdleCallback", cancel);
    else Reflect.deleteProperty(globalThis, "cancelIdleCallback");
  }
});

test("preload cleanup interrupts either sequential query and suppresses readiness", async () => {
  for (const stage of ["cron", "config"]) {
    const started = Promise.withResolvers<AbortSignal>();
    const release = Promise.withResolvers<void>();
    const calls: string[] = [];
    const query = Effect.fnUntraced(function*(name: string) {
      calls.push(name);
      if (name === stage) yield* Effect.promise((signal) => { started.resolve(signal); return release.promise; });
    });
    const stop = runPreload(query("agents"), query("skills"), query("cron"), query("config"), () => { calls.push("ready"); });
    const signal = await started.promise;
    stop();
    release.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(signal.aborted).toBe(true);
    expect(calls).toEqual(stage === "cron" ? ["agents", "skills", "cron"] : ["agents", "skills", "cron", "config"]);
  }
});
