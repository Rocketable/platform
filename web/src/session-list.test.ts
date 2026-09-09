import { describe, expect, test } from "bun:test";
import { TRPCClientError } from "@trpc/client";
import type { Session, SessionBatch } from "./grpc";
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

async function* batchesOf(rows: SessionBatch[]) {
  yield* rows;
}

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
    const result = await readSessionEnumeration(
      "alice",
      batchesOf([alice([{ id: "new", preview: "fresh" }]), alice([{ id: "new", preview: "fresh" }], { owner: "bob", upstreamSuccess: true })]),
      () => prior,
      (rows) => seen.push(rows),
    );
    expect(seen).toEqual([[{ id: "new", preview: "fresh" }, { id: "old", preview: "keep" }]]);
    expect(result).toEqual({ received: [{ id: "new", preview: "fresh" }], upstreamSuccess: false, summariesComplete: false, exhausted: false, mismatch: true });
    expect(shouldCommitSnapshot(result)).toBe(false);

    async function* lateFailure() {
      yield alice([{ id: "one" }], { upstreamSuccess: true, summariesComplete: true });
      throw new Error("cut");
    }
    const failed = await readSessionEnumeration("alice", lateFailure(), () => [], () => {});
    expect(failed.exhausted).toBe(false);
    expect(failed.upstreamSuccess).toBe(true);
    expect(shouldCommitSnapshot(failed)).toBe(false);

    const denied = TRPCClientError.from({ error: { message: "denied", code: -32001, data: { code: "UNAUTHORIZED" } } });
    for (const batches of [Promise.reject(denied), (async function* () { yield alice([{ id: "prefix" }]); throw denied; })()]) {
      const rejected = await readSessionEnumeration("alice", batches, () => prior, () => {});
      expect(rejected.mismatch).toBe(true);
      expect(shouldCommitSnapshot(rejected)).toBe(false);
    }
  });

  test("empty successful exhaustion is committable", async () => {
    const result = await readSessionEnumeration("alice", batchesOf([alice([], { upstreamSuccess: true, summariesComplete: true })]), () => [{ id: "old" }], () => {});
    expect(result.received).toEqual([]);
    expect(shouldCommitSnapshot(result)).toBe(true);
  });

  test("merges repeated IDs and keeps an earlier missing summary non-authoritative", async () => {
    const observed: { summaries: [string, boolean][]; complete: boolean }[] = [];
    const result = await readSessionEnumeration("alice", batchesOf([
      alice([{ id: "legacy" }], { summariesComplete: false }),
      alice([{ id: "empty", preview: "" }]),
      alice([{ id: "empty", preview: "new message" }]),
      alice([{ id: "missing" }], { summariesComplete: false }),
      alice([{ id: "replaced" }], { summariesComplete: false }),
      alice([{ id: "replaced", preview: "ready" }]),
      alice([], { upstreamSuccess: true, summariesComplete: false }),
    ]), () => [], (_rows, summaries, complete) => observed.push({ summaries: [...summaries], complete }));
    expect(result.received).toEqual([{ id: "legacy" }, { id: "empty", preview: "new message" }, { id: "missing" }, { id: "replaced", preview: "ready" }]);
    expect(observed).toEqual([
      { summaries: [["legacy", false]], complete: false },
      { summaries: [["empty", true], ["missing", false], ["replaced", true]], complete: false },
    ]);
    expect(shouldCommitSnapshot(result)).toBe(false);
  });

  test("publishes an immediate row and coalesced prefix before a held tail", async () => {
    const tail = Promise.withResolvers<void>();
    const consumed = Promise.withResolvers<void>();
    const published = Promise.withResolvers<void>();
    const rows = Array.from({ length: 446 }, (_, i) => ({ id: `row-${i}`, preview: `preview-${i}` }));
    const expected = rows.map((row, i) => i === 1 || i === 3 ? { id: row.id } : i === 2 ? { ...row, preview: "replacement" } : row);
    const loading = new Set<string>();
    let baseline: Session[] = [];
    let publications = 0;
    async function* stream() {
      yield alice([rows[0]]);
      expect(baseline).toEqual([rows[0]]);
      for (const row of rows.slice(1)) yield alice([row]);
      yield alice([{ id: "row-1" }, { id: "row-2" }, { id: "row-3" }], { summariesComplete: false });
      yield alice([{ id: "row-2", preview: "replacement" }]);
      consumed.resolve();
      await tail.promise;
      yield alice([], { upstreamSuccess: true });
    }
    const reading = readSessionEnumeration("alice", stream(), () => baseline, (merged, summaries) => {
      baseline = merged;
      for (const [id, ready] of summaries) {
        if (ready) loading.delete(id);
        else loading.add(id);
      }
      publications += 1;
      if (merged.length === rows.length + 1) published.resolve();
    });
    await consumed.promise;
    // Saved hydration arriving between transport and display must survive the flush.
    baseline = [...baseline, { id: "saved", preview: "keep" }];
    try {
      expect(publications).toBe(1);
      await published.promise;
      expect(baseline).toEqual([...expected, { id: "saved", preview: "keep" }]);
      expect([...loading]).toEqual(["row-1", "row-3"]);
    } finally {
      tail.resolve();
    }
    const result = await reading;
    expect(result.received).toEqual(expected);
    expect(result.exhausted).toBe(true);
    expect(shouldCommitSnapshot(result)).toBe(false);
  });

  test("flushes a failed prefix but cancels pending publication on abort or owner mismatch", async () => {
    for (const ending of ["error", "abort", "mismatch"] as const) {
      const ac = new AbortController();
      const seen: Session[][] = [];
      async function* stream() {
        yield alice([{ id: "first" }]);
        yield alice([{ id: "pending" }], { upstreamSuccess: true });
        if (ending === "abort") ac.abort();
        if (ending === "mismatch") yield alice([{ id: "foreign" }], { owner: "bob" });
        else throw new Error("cut");
      }
      const result = await readSessionEnumeration("alice", stream(), () => [], (rows) => seen.push(rows), ac.signal);
      expect(seen).toEqual(ending === "error" ? [[{ id: "first" }], [{ id: "first" }, { id: "pending" }]] : [[{ id: "first" }]]);
      expect(result.received).toEqual([{ id: "first" }, { id: "pending" }]);
      expect(shouldCommitSnapshot(result)).toBe(false);
      await Bun.sleep(25);
      expect(seen.length).toBe(ending === "error" ? 2 : 1);
    }
  });
});
