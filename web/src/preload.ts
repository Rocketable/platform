import { Effect, Fiber } from "effect";

const idle = Effect.callback<void>((resume) => {
  if (typeof requestIdleCallback !== "function") {
    const timer = setTimeout(() => resume(Effect.void), 0);
    return Effect.sync(() => clearTimeout(timer));
  }
  const id = requestIdleCallback(() => resume(Effect.void));
  return Effect.sync(() => cancelIdleCallback(id));
});

export const preloadTabs = Effect.fn("preload-tabs")(function* (
  fetchAgents: () => Promise<unknown>,
  fetchSkills: () => Promise<unknown>,
  fetchCron: () => Promise<unknown>,
  fetchConfig: () => Promise<unknown>,
) {
  yield* idle;
  yield* Effect.all([Effect.promise(fetchAgents), Effect.promise(fetchSkills)], { concurrency: 2 });
  yield* Effect.promise(fetchCron);
  yield* Effect.promise(fetchConfig);
});

export function runPreload(
  fetchAgents: () => Promise<unknown>,
  fetchSkills: () => Promise<unknown>,
  fetchCron: () => Promise<unknown>,
  fetchConfig: () => Promise<unknown>,
  onReady: () => void,
) {
  const fiber = Effect.runFork(preloadTabs(fetchAgents, fetchSkills, fetchCron, fetchConfig).pipe(Effect.tap(() => Effect.sync(onReady))));
  return () => {
    Effect.runFork(Fiber.interrupt(fiber));
  };
}
