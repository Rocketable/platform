import { Effect } from "effect";

export function runPreload(
  fetchAgents: Effect.Effect<unknown>,
  fetchSkills: Effect.Effect<unknown>,
  fetchCron: Effect.Effect<unknown>,
  fetchConfig: Effect.Effect<unknown>,
  onReady: () => void,
) {
  return Effect.runCallback(Effect.gen(function*() {
    yield* Effect.callback<void>((resume) => {
      if (typeof requestIdleCallback === "function") {
        const idle = requestIdleCallback(() => resume(Effect.void));
        return Effect.sync(() => cancelIdleCallback(idle));
      }
      const timer = setTimeout(() => resume(Effect.void), 0);
      return Effect.sync(() => clearTimeout(timer));
    });
    yield* Effect.all([fetchAgents, fetchSkills], { concurrency: "unbounded", discard: true });
    yield* fetchCron;
    yield* fetchConfig;
    onReady();
  }));
}
