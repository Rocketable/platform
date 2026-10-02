import { Deferred, Effect, Fiber, Stream } from "effect";
import { RPCError } from "./api";
import type { Session, SessionBatch } from "./types";

export const MISSING_SUMMARY = "loading...";
export const SESSION_HISTORY_CHANNEL = "rocketclaw-session-history";

const epochs = new Map<string, number>();
let globalEpoch = 0;
const pendingSaves = new Map<IDBTransaction, string>();
const pendingClears = new Map<string, Set<Deferred.Deferred<void>>>();

function scopeKey(owner: string, protocol: string) {
  return JSON.stringify([owner, protocol]);
}

function snapshotEpoch(owner: string, protocol: string) {
  return `${globalEpoch}:${epochs.get(scopeKey(owner, protocol)) ?? 0}`;
}

// Identity changes, deletions and abandoned enumerations must beat in-flight
// IndexedDB work. Network abort does not cancel an already-opened IDB request.
export function invalidatePendingSaves(owner?: string, protocol?: string) {
  const key = owner === undefined || protocol === undefined ? undefined : scopeKey(owner, protocol);
  if (key === undefined) {
    globalEpoch += 1;
  } else {
    epochs.set(key, (epochs.get(key) ?? 0) + 1);
  }
  for (const [tx, scope] of pendingSaves) {
    if (key !== undefined && scope !== key) continue;
    abortTransaction(tx);
  }
}

function abortTransaction(tx: IDBTransaction) {
  try {
    tx.abort();
  } catch (error) {
    // A commit may finish before its completion event reaches this task.
    if (!(error instanceof DOMException && error.name === "InvalidStateError")) throw error;
  }
}

const openSnapshots = Effect.acquireRelease(Effect.callback<IDBDatabase, Error>((resume, signal) => {
  try {
    const request = indexedDB.open("rocketclaw-session-list", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("snapshots");
    request.onerror = () => resume(Effect.fail(request.error!));
    request.onsuccess = () => {
      // IDB open cannot be cancelled; close a handle arriving after interruption.
      if (signal.aborted) request.result.close();
      else resume(Effect.succeed(request.result));
    };
  } catch (error) {
    resume(Effect.fail(error as Error));
  }
}), (db) => Effect.sync(() => db.close()), { interruptible: true });

function snapshotTransaction<A>(db: IDBDatabase, mode: IDBTransactionMode, use: (store: IDBObjectStore) => A): Effect.Effect<A, Error> {
  return Effect.callback((resume) => {
    let tx: IDBTransaction | undefined;
    try {
      const transaction = tx = db.transaction("snapshots", mode);
      transaction.onabort = () => {
        pendingSaves.delete(transaction);
        resume(Effect.fail(transaction.error ?? new DOMException("Transaction aborted", "AbortError")));
      };
      const result = use(transaction.objectStore("snapshots"));
      transaction.oncomplete = () => { pendingSaves.delete(transaction); resume(Effect.succeed(result)); };
    } catch (error) {
      if (tx) abortTransaction(tx);
      resume(Effect.fail(error as Error));
    }
    return Effect.sync(() => {
      if (tx) { pendingSaves.delete(tx); abortTransaction(tx); }
    });
  });
}

// Capture before opening the network enumeration, not when its rows are saved.
// The string metadata key cannot collide with the [owner, protocol] row key.
export const loadSnapshotGeneration = Effect.fnUntraced(function* (owner: string, protocol: string) {
  const scope = scopeKey(owner, protocol);
  yield* Effect.forEach(pendingClears.get(scope) ?? [], Deferred.await, { discard: true });
  const db = yield* openSnapshots;
  const request = yield* snapshotTransaction(db, "readonly", (store) => store.get(scope));
  return (request.result ?? 0) as number;
}, Effect.scoped);

// The caller must confirm owner with the backend before reading saved rows.
// IndexedDB supplies origin isolation; the key adds user and protocol isolation.
export const loadSavedSessions = Effect.fnUntraced(function* (owner: string, protocol: string) {
  const epoch = snapshotEpoch(owner, protocol);
  const db = yield* openSnapshots;
  if (epoch !== snapshotEpoch(owner, protocol)) return undefined;
  const request = yield* snapshotTransaction(db, "readonly", (store) => store.get([owner, protocol]));
  return epoch === snapshotEpoch(owner, protocol) ? (request.result as Session[] | undefined)?.map((row) => row.running ? { ...row, running: false } : row) : undefined;
}, Effect.scoped);

// Only a successfully exhausted, summary-complete enumeration may be saved.
// Resolve on transaction completion: request success alone is not a commit.
export const saveCompleteSessions = Effect.fnUntraced(function* (owner: string, protocol: string, rows: Session[], generation: number | undefined) {
  if (generation === undefined) return; // Storage was unavailable before enumeration.
  invalidatePendingSaves(owner, protocol);
  const epoch = snapshotEpoch(owner, protocol);
  const scope = scopeKey(owner, protocol);
  yield* Effect.forEach(pendingClears.get(scope) ?? [], Deferred.await, { discard: true });
  const db = yield* openSnapshots;
  if (epoch !== snapshotEpoch(owner, protocol)) return;
  yield* snapshotTransaction(db, "readwrite", (store) => {
    pendingSaves.set(store.transaction, scope);
    const request = store.get(scope);
    request.onsuccess = () => {
      // Read/check/write share a transaction: a paused tab cannot overwrite
      // a deletion even if its BroadcastChannel notification is still queued.
      if ((request.result ?? 0) === generation) store.put(rows, [owner, protocol]);
    };
  });
}, Effect.scoped);

// History deletion retains the discoverable conversation and its routing data.
// Invalidate first so a delayed save cannot resurrect the preview after this commit.
export const clearSavedSessionHistory = Effect.fnUntraced(function* (owner: string, protocol: string, id: string) {
  invalidatePendingSaves(owner, protocol);
  const scope = scopeKey(owner, protocol);
  const { previous } = yield* Effect.acquireRelease(Effect.sync(() => {
    const pending = pendingClears.get(scope) ?? new Set<Deferred.Deferred<void>>();
    const previous = [...pending];
    const clearing = Deferred.makeUnsafe<void>();
    pending.add(clearing);
    pendingClears.set(scope, pending);
    return { pending, previous, clearing };
  }), ({ pending, clearing }) => Effect.sync(() => {
    pending.delete(clearing);
    if (pending.size === 0) pendingClears.delete(scope);
    Deferred.doneUnsafe(clearing, Effect.void);
  }));
  yield* Effect.forEach(previous, Deferred.await, { discard: true });
  const db = yield* openSnapshots;
  yield* snapshotTransaction(db, "readwrite", (store) => {
    const generation = store.get(scope);
    generation.onsuccess = () => store.put((generation.result ?? 0) + 1, scope);
    const key = [owner, protocol];
    const request = store.get(key);
    request.onsuccess = () => {
      const rows: Session[] | undefined = request.result;
      if (rows !== undefined) store.put(stripSessionHistory(rows, id), key);
    };
  });
  yield* Effect.try(() => {
    const channel = new BroadcastChannel(SESSION_HISTORY_CHANNEL);
    try { channel.postMessage({ owner, protocol, id }); }
    finally { channel.close(); }
  });
}, Effect.scoped);

export function mergeSessionRows(baseline: Session[], received: Session[]): Session[] {
  const seen = new Set(received.map((row) => row.id));
  return [...received, ...baseline.filter((row) => !seen.has(row.id))];
}

export function stripSessionHistory(rows: Session[], id: string): Session[] {
  return rows.map((row) => (row.id === id ? { ...row, preview: "", updatedAt: "" } : row));
}

export function shouldCommitSnapshot(flags: { upstreamSuccess: boolean; summariesComplete: boolean; exhausted: boolean; mismatch: boolean }): boolean {
  return flags.upstreamSuccess && flags.summariesComplete && flags.exhausted && !flags.mismatch;
}

export function searchIsAuthoritative(flags: { refreshing: boolean; enumerationComplete: boolean; summariesComplete: boolean }): boolean {
  return !flags.refreshing && flags.enumerationComplete && flags.summariesComplete;
}

export function rowPreview(session: Session, loading: boolean): string {
  return session.preview || (loading ? MISSING_SUMMARY : "");
}

export const readSessionEnumeration = Effect.fnUntraced(function* (
  owner: string,
  batches: Stream.Stream<SessionBatch, Error>,
  baseline: () => Session[],
  onProgress: (rows: Session[], summaries: ReadonlyMap<string, boolean>, summariesComplete: boolean) => void,
) {
  const byID = new Map<string, Session>();
  const summaries = new Map<string, boolean>();
  let timer: Fiber.Fiber<void> | undefined;
  let published = false;
  let dirty = false;
  let upstreamSuccess = false;
  let summariesComplete = true;
  let exhausted = false;
  let mismatch = false;
  const flush = Effect.sync(() => {
    timer = undefined;
    if (!dirty || mismatch) return;
    dirty = false;
    published ||= byID.size > 0;
    onProgress(mergeSessionRows(baseline(), [...byID.values()]), new Map(summaries), summariesComplete);
    summaries.clear();
  });
  const reading = yield* Effect.forkScoped(Stream.runForEachWhile(batches, Effect.fnUntraced(function* (batch) {
    if (batch.owner !== owner) {
      mismatch = true;
      upstreamSuccess = summariesComplete = false;
      return false;
    }
    for (const row of batch.sessions) {
      byID.set(row.id, row);
      summaries.set(row.id, batch.summariesComplete);
    }
    upstreamSuccess = batch.upstreamSuccess;
    summariesComplete &&= batch.summariesComplete;
    dirty = true;
    // First useful row is immediate; subsequent display work coalesces over
    // 16 ms without awaiting the timer or slowing transport consumption.
    // Full-list work remains per publication; incremental rendering is the
    // next step if even frame-sized publications become too expensive.
    if (!published && byID.size > 0) {
      if (timer) yield* Fiber.interrupt(timer);
      yield* flush;
    } else if (timer === undefined) timer = yield* Effect.forkScoped(Effect.sleep(16).pipe(Effect.andThen(flush)));
    return true;
  })).pipe(
    Effect.tap(() => Effect.sync(() => { exhausted = !mismatch; })),
    Effect.catch((error) => Effect.sync(() => {
      // An owner mismatch or authentication rejection requires live identity revalidation.
      mismatch = error instanceof RPCError && error.code === 16;
      // Retain the received prefix, but failed streams cannot promote a snapshot.
    })),
  ));
  // Cancel display work before waiting for potentially asynchronous transport cleanup.
  yield* Fiber.join(reading).pipe(Effect.ensuring(Effect.suspend(() => timer ? Fiber.interrupt(timer) : Effect.void)));
  yield* flush;
  return { received: [...byID.values()], upstreamSuccess, summariesComplete, exhausted, mismatch };
}, Effect.scoped);
