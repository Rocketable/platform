import { expect, test } from "bun:test";
import { runPreload } from "./preload";

test("tab warming starts agents and skills together, then cron/config, and stops after unmount", async () => {
  for (const cancel of [false, true]) {
    const started = Promise.withResolvers<void>();
    const release = Promise.withResolvers<void>();
    const ready = Promise.withResolvers<void>();
    const calls: string[] = [];
    const stop = runPreload(
      async () => { calls.push("agents"); await release.promise; },
      async () => { calls.push("skills"); started.resolve(); await release.promise; },
      async () => { calls.push("cron"); },
      async () => { calls.push("config"); },
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
    } else {
      await ready.promise;
      expect(calls).toEqual(["agents", "skills", "cron", "config", "ready"]);
      stop();
    }
  }
});
