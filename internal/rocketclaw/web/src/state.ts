import { Cause, Effect, Fiber, Option } from "effect";
import { AsyncResult, Atom, AtomRegistry } from "effect/reactivity";
import { useAtomValue } from "@effect/atom-react";
import { useCallback, useRef, useState } from "react";
import { queries as requests, rpc } from "./api";
import type { HistoryView } from "./types";

export const registry = AtomRegistry.make();
const invalidations = Atom.family((_key: string) => Atom.make(0));
export function invalidate(key: string) {
  registry.update(invalidations(key), (version) => version + 1);
}

// Families own request sharing, refresh and disposal; React only renders results.
function resource<Args extends unknown[], A>(key: string, load: (...args: Args) => Effect.Effect<A, Error>, interval: number | ((...args: Args) => number) = 0, staleTime = 0) {
  const family = Atom.family((encoded: string) => {
    const args = JSON.parse(encoded) as Args;
    const source = Atom.make((get) => {
      get(invalidations(key));
      return load(...args);
    }).pipe(Atom.setIdleTTL(key === "handoff" ? 0 : "5 minutes"));
    const cached = source.pipe(Atom.swr({ staleTime, revalidateOnFocus: true, focusSignal: Atom.windowFocusSignal }), Atom.setIdleTTL(0));
    const millis = typeof interval === "number" ? interval : interval(...args);
    if (!millis) return cached;
    return Atom.transform(cached, (get) => {
      get(Atom.windowFocusSignal);
      const result = get(cached);
      if (!result.waiting) {
        const timer = Effect.runFork(Effect.sleep(millis).pipe(Effect.map(() => { if (document.visibilityState !== "hidden") get.refresh(cached); })));
        get.addFinalizer(() => { Effect.runFork(Fiber.interrupt(timer)); });
      }
      return result;
    });
  });
  return (...args: Args) => family(JSON.stringify(args));
}

export const histories = Atom.family((_id: string) => Atom.make<HistoryView | undefined>(undefined).pipe(Atom.setIdleTTL("5 minutes")));
const historyRequests = resource("history", (input: { id: string; sourceConversationId?: string }) => requests.history(input).pipe(
  Effect.tap((view) => Effect.sync(() => { if (!input.sourceConversationId) registry.set(histories(input.id), view); })),
));
const historyViews = Atom.family((key: string) => {
  const input = JSON.parse(key) as Parameters<typeof historyRequests>[0];
  const source = historyRequests(input);
  return input.sourceConversationId ? source : Atom.transform(source, (get) => {
    const result = get(source);
    const view = get(histories(input.id));
    return view ? AsyncResult.map(result, () => view) : result;
  });
});

export const queries = {
  identity: resource("identity", requests.identity, 2000),
  protocol: resource("protocol", requests.protocol, 2000),
  agents: resource("agents", requests.agents, (input?: Parameters<typeof requests.agents>[0]) => input === undefined ? 0 : 2000, 60_000),
  skills: resource("skills", requests.skills, 0, 60_000),
  cronJobs: resource("cronJobs", requests.cronJobs, 0, 10_000),
  config: resource("config", requests.config, 0, 60_000),
  queue: resource("queue", requests.queue, 2000),
  history: (input: Parameters<typeof historyRequests>[0]) => historyViews(JSON.stringify(input)),
  origin: resource("origin", (_owner: string | null, _protocol: string | null, id: string) => requests.history({ id, originOnly: true }), 0, 10_000),
  searchMessages: resource("searchMessages", requests.searchMessages),
  handoff: resource("handoff", (id: string) => rpc("Handoff", { id }), 0, Infinity),
};
const disabled = Atom.make(AsyncResult.initial<never, Error>());

export function useRemote<A>(atom: Atom.Atom<AsyncResult.AsyncResult<A, Error>>, enabled = true) {
  const result = useAtomValue(enabled ? atom : disabled);
  const refetch = useCallback(() => registry.refresh(atom), [atom]);
  return {
    data: Option.getOrUndefined(AsyncResult.value(result)),
    error: AsyncResult.isFailure(result) ? Cause.squash(result.cause) as Error : undefined,
    isPending: AsyncResult.isInitial(result),
    isLoading: AsyncResult.isInitial(result) && result.waiting,
    isFetching: result.waiting,
    isError: AsyncResult.isFailure(result),
    isSuccess: AsyncResult.isSuccess(result),
    refetch,
  };
}

export function useAction<A, B>({ action, onSuccess }: { action: (input: A) => Effect.Effect<B, Error>; onSuccess?: (value: B, input: A) => void }) {
  const current = useRef<{ input: A }>(undefined);
  const [atom] = useState(() => Atom.make<AsyncResult.AsyncResult<B, Error>>(AsyncResult.initial()));
  const result = useAtomValue(atom);
  const execute = (input: A) => {
    const invocation = current.current = { input };
    registry.set(atom, AsyncResult.waiting(registry.get(atom)));
    return Effect.runPromise(action(input).pipe(
      Effect.tap((value) => Effect.sync(() => onSuccess?.(value, input))),
      Effect.onExit((exit) => Effect.sync(() => { if (current.current === invocation) registry.set(atom, AsyncResult.fromExit(exit)); })),
    ));
  };
  return {
    execute,
    fire: (input: A) => { void execute(input).catch(() => {}); },
    variables: current.current?.input,
    isPending: result.waiting,
    error: AsyncResult.isFailure(result) ? Cause.squash(result.cause) as Error : undefined,
  };
}
