import { describe, expect, test } from "bun:test";
import { API } from "typescript/unstable/async";
import { isFunctionDeclaration } from "typescript/unstable/ast/is";
import { RPCError } from "./api";
import type { Session, SessionBatch } from "./types";
import {
  listedRows,
  mergeSessionRows,
  readSessionEnumeration,
  saveCompleteSessions,
  searchIsAuthoritative,
  shouldCommitSnapshot,
  stripSessionHistory,
} from "./session-list";

const alice = (sessions: Session[], flags: Partial<SessionBatch> = {}): SessionBatch => ({
  sessions,
  owner: "alice",
  upstreamSuccess: false,
  ...flags,
});

async function* batchesOf(rows: SessionBatch[]) {
  yield* rows;
}

// Keep the last-word check private; browser tests cover its mounted callers.
const api = new API({ cwd: import.meta.dir });
const source = (await (await api.updateSnapshot({ openProject: "../tsconfig.json" })).getProjects()[0].program.getSourceFile(`${import.meta.dir}/ui.tsx`))!;
await api.close();
const functions = source.statements.filter((node) => isFunctionDeclaration(node) && node.name?.text === "typingWord").map((node) => node.getText(source)).join("\n");
const javascript = new Bun.Transpiler({ loader: "tsx" }).transformSync(`${functions}\nexport { typingWord };`);
const { typingWord } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("only an unfinished operator as the last word is left out while typing", () => {
  for (const [query, word, unfinished] of [
    ["is:", "is:", true],
    ["is:pin", "is:pin", true],
    ["IS:PINNE", "IS:PINNE", true],
    ["sort:", "sort:", true],
    ["sort:ne", "sort:ne", true],
    ["outage is:pin", "is:pin", true],
    ["cron:", "cron:", true],
    ["outage TAG:", "TAG:", true],
    ["agent:", "agent:", true],
    ["room:", "room:", true],
    ["space:", "space:", true],
    ['space:"Blue gar', 'space:"Blue gar', true],
    ['space:"Blue \\"gar', 'space:"Blue \\"gar', true],
    ['space:"Blue garden"', 'garden"', false],
    ["space:Main ", "", false],
    ['tag:"Needs rev', 'tag:"Needs rev', true],
    ['outage room:"general \\"ch', 'room:"general \\"ch', true],
    ["tag:bug", "tag:bug", false],
    ["tag:bu", "tag:bu", false],
    ["agent:ma", "agent:ma", false],
    ["is:pinned", "is:pinned", false],
    ["sort:oldest", "sort:oldest", false],
    ["is:foo", "is:foo", false],
    ['tag:"Needs review"', 'review"', false],
    ["tag:bug ", "", false],
    ["outage", "outage", false],
    ["", "", false],
  ] as const) expect(typingWord(query)).toEqual({ word, unfinished });
});

describe("session list reconciliation", () => {
  test("does not save when storage was unavailable before enumeration", async () => {
    await expect(saveCompleteSessions("alice", "v1", [{ id: "new", preview: "fresh" }], undefined)).resolves.toBeUndefined();
  });

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

  test("rebuilds a listing made since a held version in the server's order", () => {
    const held: Session[] = [{ id: "a", preview: "1" }, { id: "b", preview: "1" }, { id: "gone", preview: "1" }];
    expect(listedRows(held, [{ id: "b", preview: "2" }, { id: "new", preview: "fresh" }], ["new", "b", "a"])).toEqual([
      { id: "new", preview: "fresh" },
      { id: "b", preview: "2" },
      { id: "a", preview: "1" },
    ]);
    expect(listedRows(held, [], ["a", "unknown"])).toBeUndefined();
    expect(listedRows(held, [{ id: "only" }], undefined)).toEqual([{ id: "only" }]);
  });

  test("reports the completion's version and order", async () => {
    async function* batches() {
      yield alice([{ id: "b" }]);
      yield alice([], { upstreamSuccess: true, version: "v2", order: ["a", "b"] });
    }
    const result = await readSessionEnumeration("alice", batches(), () => [], () => {});
    expect([result.version, result.order]).toEqual(["v2", ["a", "b"]]);
  });

  test("commits only after success and exhaustion", () => {
    expect(shouldCommitSnapshot({ upstreamSuccess: true, exhausted: true, mismatch: false })).toBe(true);
    expect(shouldCommitSnapshot({ upstreamSuccess: true, exhausted: false, mismatch: false })).toBe(false);
    expect(shouldCommitSnapshot({ upstreamSuccess: false, exhausted: true, mismatch: false })).toBe(false);
    expect(shouldCommitSnapshot({ upstreamSuccess: true, exhausted: true, mismatch: true })).toBe(false);
  });

  test("strips preview and timestamp without removing routing metadata", () => {
    expect(stripSessionHistory([{ id: "keep", title: "room", preview: "secret", updatedAt: "1", agent: "main" }], "keep")).toEqual([
      { id: "keep", title: "room", preview: "", updatedAt: "", agent: "main" },
    ]);
  });

  test("search is not authoritative while refreshing or before the list is complete", () => {
    expect(searchIsAuthoritative({ refreshing: false, enumerationComplete: true })).toBe(true);
    expect(searchIsAuthoritative({ refreshing: true, enumerationComplete: true })).toBe(false);
    expect(searchIsAuthoritative({ refreshing: false, enumerationComplete: false })).toBe(false);
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
    expect(result).toEqual({ received: [{ id: "new", preview: "fresh" }], upstreamSuccess: false, exhausted: false, mismatch: true });
    expect(shouldCommitSnapshot(result)).toBe(false);

    async function* lateFailure() {
      yield alice([{ id: "one" }], { upstreamSuccess: true });
      throw new Error("cut");
    }
    const failed = await readSessionEnumeration("alice", lateFailure(), () => [], () => {});
    expect(failed.exhausted).toBe(false);
    expect(failed.upstreamSuccess).toBe(true);
    expect(shouldCommitSnapshot(failed)).toBe(false);

    const denied = new RPCError("denied", 16);
    for (const batches of [Promise.reject(denied), (async function* () { yield alice([{ id: "prefix" }]); throw denied; })()]) {
      const rejected = await readSessionEnumeration("alice", batches, () => prior, () => {});
      expect(rejected.mismatch).toBe(true);
      expect(shouldCommitSnapshot(rejected)).toBe(false);
    }
  });

  test("empty successful exhaustion is committable", async () => {
    const result = await readSessionEnumeration("alice", batchesOf([alice([], { upstreamSuccess: true })]), () => [{ id: "old" }], () => {});
    expect(result.received).toEqual([]);
    expect(shouldCommitSnapshot(result)).toBe(true);
  });

  test("publishes an immediate row and coalesced prefix before a held tail", async () => {
    const tail = Promise.withResolvers<void>();
    const consumed = Promise.withResolvers<void>();
    const published = Promise.withResolvers<void>();
    const rows = Array.from({ length: 446 }, (_, i) => ({ id: `row-${i}`, preview: `preview-${i}` }));
    const expected = rows.map((row, i) => i === 1 || i === 3 ? { id: row.id } : i === 2 ? { ...row, preview: "replacement" } : row);
    let baseline: Session[] = [];
    let publications = 0;
    async function* stream() {
      yield alice([rows[0]]);
      expect(baseline).toEqual([rows[0]]);
      for (const row of rows.slice(1)) yield alice([row]);
      yield alice([{ id: "row-1" }, { id: "row-2" }, { id: "row-3" }]);
      yield alice([{ id: "row-2", preview: "replacement" }]);
      consumed.resolve();
      await tail.promise;
      yield alice([], { upstreamSuccess: true });
    }
    const reading = readSessionEnumeration("alice", stream(), () => baseline, (merged) => {
      baseline = merged;
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
    } finally {
      tail.resolve();
    }
    const result = await reading;
    expect(result.received).toEqual(expected);
    expect(result.exhausted).toBe(true);
    expect(shouldCommitSnapshot(result)).toBe(true);
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
