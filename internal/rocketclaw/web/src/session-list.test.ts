import { describe, expect, test } from "bun:test";
import { Deferred, Effect, Exit, Fiber, Stream } from "effect";
import { TestClock } from "effect/testing";
import { RPCError } from "./api";
import type { Session, SessionBatch } from "./types";
import {
  mergeSessionRows,
  MISSING_SUMMARY,
  readSessionEnumeration,
  rowPreview,
  searchIsAuthoritative,
  shouldCommitSnapshot,
  stripSessionHistory,
} from "./session-list";

const alice = (sessions: Session[], flags: Partial<SessionBatch> = {}): SessionBatch => ({
  sessions,
  owner: "alice",
  upstreamSuccess: false,
  summariesComplete: true,
  ...flags,
});

describe("session list reconciliation", () => {
  test("keeps prior rows and replaces matching IDs in server prefix order", () => {
    const prior: Session[] = [
      { id: "old", preview: "keep", updatedAt: "1" },
      { id: "same", preview: "before", updatedAt: "1" },
    ];
    const received: Session[] = [
      { id: "new", preview: "fresh", updatedAt: "2" },
      { id: "same", preview: "after", updatedAt: "2" },
    ];
    expect(mergeSessionRows(prior, received)).toEqual([
      { id: "new", preview: "fresh", updatedAt: "2" },
      { id: "same", preview: "after", updatedAt: "2" },
      { id: "old", preview: "keep", updatedAt: "1" },
    ]);
  });

  test("commits only after success, summary completeness and exhaustion", () => {
    expect(shouldCommitSnapshot({ upstreamSuccess: true, summariesComplete: true, exhausted: true, mismatch: false })).toBe(true);
    expect(shouldCommitSnapshot({ upstreamSuccess: true, summariesComplete: true, exhausted: false, mismatch: false })).toBe(false);
    expect(shouldCommitSnapshot({ upstreamSuccess: true, summariesComplete: false, exhausted: true, mismatch: false })).toBe(false);
    expect(shouldCommitSnapshot({ upstreamSuccess: false, summariesComplete: true, exhausted: true, mismatch: false })).toBe(false);
    expect(shouldCommitSnapshot({ upstreamSuccess: true, summariesComplete: true, exhausted: true, mismatch: true })).toBe(false);
  });

  test("strips preview and timestamp without removing routing metadata", () => {
    expect(stripSessionHistory([{ id: "keep", title: "room", preview: "secret", updatedAt: "1", agent: "main", settled: true }], "keep")).toEqual([
      { id: "keep", title: "room", preview: "", updatedAt: "", agent: "main", settled: true },
    ]);
  });

  test("uses loading... only for live missing summaries", () => {
    expect(rowPreview({ id: "x", preview: "text" }, true)).toBe("text");
    expect(rowPreview({ id: "x" }, true)).toBe(MISSING_SUMMARY);
    expect(rowPreview({ id: "x", preview: "" }, false)).toBe("");
    expect(MISSING_SUMMARY).toBe("loading...");
  });

  test("search is not authoritative while refreshing or summaries are missing", () => {
    expect(searchIsAuthoritative({ refreshing: false, enumerationComplete: true, summariesComplete: true })).toBe(true);
    expect(searchIsAuthoritative({ refreshing: true, enumerationComplete: true, summariesComplete: true })).toBe(false);
    expect(searchIsAuthoritative({ refreshing: false, enumerationComplete: true, summariesComplete: false })).toBe(false);
    expect(searchIsAuthoritative({ refreshing: false, enumerationComplete: false, summariesComplete: true })).toBe(false);
  });

  test("merges a prefix, ignores owner mismatch, and does not treat a late throw as exhaustion", async () => {
    const prior: Session[] = [{ id: "old", preview: "keep" }];
    const seen: Session[][] = [];
    const result = await Effect.runPromise(readSessionEnumeration(
      "alice",
      Stream.make(alice([{ id: "new", preview: "fresh" }]), alice([{ id: "new", preview: "fresh" }], { owner: "bob", upstreamSuccess: true })),
      () => prior,
      (rows) => seen.push(rows),
    ));
    expect(seen).toEqual([[{ id: "new", preview: "fresh" }, { id: "old", preview: "keep" }]]);
    expect(result).toEqual({ received: [{ id: "new", preview: "fresh" }], upstreamSuccess: false, summariesComplete: false, exhausted: false, mismatch: true });
    expect(shouldCommitSnapshot(result)).toBe(false);

    const lateFailure = Stream.concat(Stream.make(alice([{ id: "one" }], { upstreamSuccess: true, summariesComplete: true })), Stream.fail(new Error("cut")));
    const failed = await Effect.runPromise(readSessionEnumeration("alice", lateFailure, () => [], () => {}));
    expect(failed.exhausted).toBe(false);
    expect(failed.upstreamSuccess).toBe(true);
    expect(shouldCommitSnapshot(failed)).toBe(false);

    const denied = new RPCError("denied", 16);
    for (const batches of [Stream.fail(denied), Stream.concat(Stream.make(alice([{ id: "prefix" }])), Stream.fail(denied))]) {
      const rejected = await Effect.runPromise(readSessionEnumeration("alice", batches, () => prior, () => {}));
      expect(rejected.mismatch).toBe(true);
      expect(shouldCommitSnapshot(rejected)).toBe(false);
    }
  });

  test("empty successful exhaustion is committable", async () => {
    const result = await Effect.runPromise(readSessionEnumeration("alice", Stream.make(alice([], { upstreamSuccess: true, summariesComplete: true })), () => [{ id: "old" }], () => {}));
    expect(result.received).toEqual([]);
    expect(shouldCommitSnapshot(result)).toBe(true);
  });

  test("merges repeated IDs and keeps an earlier missing summary non-authoritative", async () => {
    const observed: { summaries: [string, boolean][]; complete: boolean }[] = [];
    const result = await Effect.runPromise(readSessionEnumeration("alice", Stream.make(
      alice([{ id: "legacy" }], { summariesComplete: false }),
      alice([{ id: "empty", preview: "" }]),
      alice([{ id: "empty", preview: "new message" }]),
      alice([{ id: "missing" }], { summariesComplete: false }),
      alice([{ id: "replaced" }], { summariesComplete: false }),
      alice([{ id: "replaced", preview: "ready" }]),
      alice([], { upstreamSuccess: true, summariesComplete: false }),
    ), () => [], (_rows, summaries, complete) => observed.push({ summaries: [...summaries], complete })));
    expect(result.received).toEqual([{ id: "legacy" }, { id: "empty", preview: "new message" }, { id: "missing" }, { id: "replaced", preview: "ready" }]);
    expect(observed).toEqual([
      { summaries: [["legacy", false]], complete: false },
      { summaries: [["empty", true], ["missing", false], ["replaced", true]], complete: false },
    ]);
    expect(shouldCommitSnapshot(result)).toBe(false);
  });

  test("publishes an immediate row and coalesced prefix before a held tail", async () => {
    await Effect.runPromise(Effect.gen(function* () {
      const tail = yield* Deferred.make<void>();
      const consumed = yield* Deferred.make<void>();
      const rows = Array.from({ length: 446 }, (_, i) => ({ id: `row-${i}`, preview: `preview-${i}` }));
      const expected = rows.map((row, i) => i === 1 || i === 3 ? { id: row.id } : i === 2 ? { ...row, preview: "replacement" } : row);
      const loading = new Set<string>();
      let baseline: Session[] = [];
      let publications = 0;
      const stream = Stream.make(alice([rows[0]])).pipe(Stream.concat(Stream.suspend(() => {
        expect(baseline).toEqual([rows[0]]);
        return Stream.fromIterable([
          ...rows.slice(1).map((row) => alice([row])),
          alice([{ id: "row-1" }, { id: "row-2" }, { id: "row-3" }], { summariesComplete: false }),
          alice([{ id: "row-2", preview: "replacement" }]),
        ]);
      })), Stream.concat(Stream.fromEffect(Effect.gen(function* () {
        yield* Deferred.succeed(consumed, undefined);
        yield* Deferred.await(tail);
        return alice([], { upstreamSuccess: true });
      }))));
      const reading = yield* Effect.forkChild(readSessionEnumeration("alice", stream, () => baseline, (merged, summaries) => {
        baseline = merged;
        for (const [id, ready] of summaries) {
          if (ready) loading.delete(id);
          else loading.add(id);
        }
        publications += 1;
      }));
      yield* Deferred.await(consumed);
      // Saved hydration arriving between transport and display must survive the flush.
      baseline = [...baseline, { id: "saved", preview: "keep" }];
      expect(publications).toBe(1);
      yield* TestClock.adjust(15);
      expect(publications).toBe(1);
      yield* TestClock.adjust(1);
      expect(publications).toBe(2);
      expect(baseline).toEqual([...expected, { id: "saved", preview: "keep" }]);
      expect([...loading]).toEqual(["row-1", "row-3"]);
      yield* Deferred.succeed(tail, undefined);
      const result = yield* Fiber.join(reading);
      expect(result.received).toEqual(expected);
      expect(result.exhausted).toBe(true);
      expect(shouldCommitSnapshot(result)).toBe(false);
    }).pipe(Effect.provide(TestClock.layer())));
  });

  test("flushes a failed prefix but cancels pending publication on interruption or owner mismatch", async () => {
    await Effect.runPromise(Effect.gen(function* () {
      for (const ending of ["error", "interrupt", "mismatch", "denied"] as const) {
        const consumed = yield* Deferred.make<void>();
        const cleaning = yield* Deferred.make<void>();
        const release = yield* Deferred.make<void>();
        const seen: Session[][] = [];
        const stream = Stream.make(alice([{ id: "first" }]), alice([{ id: "pending" }], { upstreamSuccess: true })).pipe(Stream.concat(
          ending === "mismatch" ? Stream.make(alice([{ id: "foreign" }], { owner: "bob" })) : ending === "denied" ? Stream.fail(new RPCError("denied", 16)) : ending === "error" ? Stream.fail(new Error("cut")) :
            Stream.fromEffect(Deferred.succeed(consumed, undefined).pipe(
              Effect.andThen(Effect.never),
              Effect.ensuring(Deferred.succeed(cleaning, undefined).pipe(Effect.andThen(Deferred.await(release)))),
            )),
        ));
        const reading = yield* Effect.forkChild(readSessionEnumeration("alice", stream, () => [], (rows) => seen.push(rows)));
        if (ending === "interrupt") {
          yield* Deferred.await(consumed);
          const stopping = yield* Effect.forkChild(Fiber.interrupt(reading));
          yield* Deferred.await(cleaning);
          yield* TestClock.adjust(25);
          const duringCleanup = seen.length;
          yield* Deferred.succeed(release, undefined);
          yield* Fiber.join(stopping);
          expect(duringCleanup).toBe(1);
        }
        const result = yield* Fiber.await(reading);
        expect(seen).toEqual(ending === "error" ? [[{ id: "first" }], [{ id: "first" }, { id: "pending" }]] : [[{ id: "first" }]]);
        expect(Exit.hasInterrupts(result)).toBe(ending === "interrupt");
        if (Exit.isSuccess(result)) {
          expect(result.value.received).toEqual([{ id: "first" }, { id: "pending" }]);
          expect(result.value.mismatch).toBe(ending === "mismatch" || ending === "denied");
          expect(shouldCommitSnapshot(result.value)).toBe(false);
        }
        yield* TestClock.adjust(25);
        expect(seen.length).toBe(ending === "error" ? 2 : 1);
      }
    }).pipe(Effect.provide(TestClock.layer())));
  });
});
