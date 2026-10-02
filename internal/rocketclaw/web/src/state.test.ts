import { expect, spyOn, test } from "bun:test";
import { Effect } from "effect";
import { AsyncResult, AtomRegistry } from "effect/reactivity";
import { histories, invalidate, queries, registry } from "./state";

test("request atoms share reads, retain navigation data, and expose refresh failures", async () => {
  const windowBefore = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", { configurable: true, value: new EventTarget() });
  let fail = false;
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async () => fail
    ? Response.json({ message: "unavailable", code: 14 }, { status: 503 })
    : Response.json({ agents: [{ name: "main" }], currentAgent: "main" }), { preconnect: () => {} }));
  const atom = queries.agents({ conversationId: "atom-sharing" });
  let unmount = registry.mount(atom);
  try {
    const [one, two] = await Promise.all([
      Effect.runPromise(AtomRegistry.getResult(registry, atom)),
      Effect.runPromise(AtomRegistry.getResult(registry, queries.agents({ conversationId: "atom-sharing" }))),
    ]);
    expect(one).toEqual(two);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    unmount();
    await Effect.runPromise(Effect.sleep(10));
    unmount = registry.mount(atom);
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, atom))).toEqual(one);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    fail = true;
    invalidate("agents");
    await expect(Effect.runPromise(AtomRegistry.getResult(registry, atom, { suspendOnWaiting: true }))).rejects.toMatchObject({ message: "unavailable" });
    const result = registry.get(atom);
    expect(AsyncResult.isFailure(result)).toBe(true);
    expect(AsyncResult.value(result)).toMatchObject({ value: one });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  } finally {
    unmount();
    registry.reset();
    fetchMock.mockRestore();
    if (windowBefore) Object.defineProperty(globalThis, "window", windowBefore);
    else Reflect.deleteProperty(globalThis, "window");
  }
});

test("polling waits for slow responses, pauses while hidden, and leaves the catalog idle", async () => {
  const windowBefore = Object.getOwnPropertyDescriptor(globalThis, "window");
  const documentBefore = Object.getOwnPropertyDescriptor(globalThis, "document");
  const visibility = { visibilityState: "visible" };
  Object.defineProperty(globalThis, "window", { configurable: true, value: new EventTarget() });
  Object.defineProperty(globalThis, "document", { configurable: true, value: visibility });
  const release = Promise.withResolvers<void>();
  const calls: object[] = [];
  let aborted = false;
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async (_url: URL | RequestInfo, init?: RequestInit) => {
    const input = JSON.parse(String(init?.body));
    calls.push(input);
    if (input.conversationId) {
      init?.signal?.addEventListener("abort", () => { aborted = true; }, { once: true });
      await release.promise;
    }
    return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
  }, { preconnect: () => {} }));
  const slow = queries.agents({ conversationId: "slow-poll" });
  const stops = [registry.mount(slow), registry.mount(queries.agents())];
  try {
    await Effect.runPromise(Effect.sleep(2200));
    expect(calls).toEqual([{ conversationId: "slow-poll" }, {}]);
    expect(aborted).toBe(false);
    release.resolve();
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, slow))).toMatchObject({ currentAgent: "main" });
    visibility.visibilityState = "hidden";
    window.dispatchEvent(new Event("visibilitychange"));
    await Effect.runPromise(Effect.sleep(2200));
    expect(calls).toHaveLength(2);
    visibility.visibilityState = "visible";
    window.dispatchEvent(new Event("visibilitychange"));
    await Effect.runPromise(Effect.sleep(2200));
    expect(calls).toEqual([{ conversationId: "slow-poll" }, {}, { conversationId: "slow-poll" }]);
  } finally {
    release.resolve();
    stops.forEach((stop) => stop());
    registry.reset();
    fetchMock.mockRestore();
    if (windowBefore) Object.defineProperty(globalThis, "window", windowBefore);
    else Reflect.deleteProperty(globalThis, "window");
    if (documentBefore) Object.defineProperty(globalThis, "document", documentBefore);
    else Reflect.deleteProperty(globalThis, "document");
  }
}, 10_000);

test("full history readers observe streamed replacements after initial loading", async () => {
  const windowBefore = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", { configurable: true, value: new EventTarget() });
  const initial = { messages: [], origin: "", delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" };
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(async () => Response.json(initial), { preconnect: () => {} }));
  const atom = queries.history({ id: "live-preview" });
  const unmount = registry.mount(atom);
  try {
    const view = await Effect.runPromise(AtomRegistry.getResult(registry, atom));
    expect(registry.get(histories("live-preview"))).toEqual(view);
    const latest = { ...view, revision: "updated", running: true };
    registry.set(histories("live-preview"), latest);
    expect(await Effect.runPromise(AtomRegistry.getResult(registry, atom))).toEqual(latest);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  } finally {
    unmount();
    registry.reset();
    fetchMock.mockRestore();
    if (windowBefore) Object.defineProperty(globalThis, "window", windowBefore);
    else Reflect.deleteProperty(globalThis, "window");
  }
});
