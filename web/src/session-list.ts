import { TRPCClientError } from "@trpc/client";
import type { Session, SessionBatch } from "./grpc";

export const MISSING_SUMMARY = "loading...";
export const SESSION_HISTORY_CHANNEL = "rocketclaw-session-history";

const epochs = new Map<string, number>();
let globalEpoch = 0;
const pendingSaves = new Map<IDBTransaction, string>();
const pendingClears = new Map<string, Promise<void>>();

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
    try {
      tx.abort();
    } catch (error) {
      // A commit may finish before its completion event reaches this task.
      if (!(error instanceof DOMException && error.name === "InvalidStateError")) throw error;
    }
  }
}

function openSnapshots(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open("rocketclaw-session-list", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("snapshots");
    request.onerror = () => reject(request.error);
    request.onsuccess = () => resolve(request.result);
  });
}

// Capture before opening the network enumeration, not when its rows are saved.
// The string metadata key cannot collide with the [owner, protocol] row key.
export async function loadSnapshotGeneration(owner: string, protocol: string): Promise<number> {
  await pendingClears.get(scopeKey(owner, protocol))?.catch(() => {});
  const db = await openSnapshots();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction("snapshots", "readonly");
      const request = tx.objectStore("snapshots").get(scopeKey(owner, protocol));
      tx.oncomplete = () => resolve(request.result ?? 0);
      tx.onabort = () => reject(tx.error);
    });
  } finally {
    db.close();
  }
}

// The caller must confirm owner with the backend before reading saved rows.
// IndexedDB supplies origin isolation; the key adds user and protocol isolation.
export async function loadSavedSessions(owner: string, protocol: string): Promise<Session[] | undefined> {
  const epoch = snapshotEpoch(owner, protocol);
  const db = await openSnapshots();
  try {
    if (epoch !== snapshotEpoch(owner, protocol)) {
      return undefined;
    }
    return await new Promise((resolve, reject) => {
      const tx = db.transaction("snapshots", "readonly");
      const request = tx.objectStore("snapshots").get([owner, protocol]);
      tx.oncomplete = () => resolve(epoch === snapshotEpoch(owner, protocol) ? request.result : undefined);
      tx.onabort = () => reject(tx.error);
    });
  } finally {
    db.close();
  }
}

// Only a successfully exhausted, summary-complete enumeration may be saved.
// Resolve on transaction completion: request success alone is not a commit.
export async function saveCompleteSessions(owner: string, protocol: string, rows: Session[], generation: number | undefined): Promise<void> {
  if (generation === undefined) return; // Storage was unavailable before enumeration.
  invalidatePendingSaves(owner, protocol);
  const epoch = snapshotEpoch(owner, protocol);
  await pendingClears.get(scopeKey(owner, protocol))?.catch(() => {});
  const db = await openSnapshots();
  try {
    if (epoch !== snapshotEpoch(owner, protocol)) {
      return;
    }
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction("snapshots", "readwrite");
      pendingSaves.set(tx, scopeKey(owner, protocol));
      tx.oncomplete = () => { pendingSaves.delete(tx); resolve(); };
      tx.onabort = () => { pendingSaves.delete(tx); reject(tx.error); };
      const store = tx.objectStore("snapshots");
      const request = store.get(scopeKey(owner, protocol));
      request.onsuccess = () => {
        // Read/check/write share a transaction: a paused tab cannot overwrite
        // a deletion even if its BroadcastChannel notification is still queued.
        if ((request.result ?? 0) === generation) store.put(rows, [owner, protocol]);
      };
    });
  } finally {
    db.close();
  }
}

// History deletion retains the discoverable conversation and its routing data.
// Invalidate first so a delayed save cannot resurrect the preview after this commit.
export async function clearSavedSessionHistory(owner: string, protocol: string, id: string): Promise<void> {
  invalidatePendingSaves(owner, protocol);
  const scope = scopeKey(owner, protocol);
  const clearing = Promise.resolve(pendingClears.get(scope)).catch(() => {}).then(async () => {
    const db = await openSnapshots();
    try {
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction("snapshots", "readwrite");
        tx.oncomplete = () => resolve();
        tx.onabort = () => reject(tx.error);
        const store = tx.objectStore("snapshots");
        const generation = store.get(scope);
        generation.onsuccess = () => store.put((generation.result ?? 0) + 1, scope);
        const key = [owner, protocol];
        const request = store.get(key);
        request.onsuccess = () => {
          const rows: Session[] | undefined = request.result;
          if (rows !== undefined) {
            store.put(stripSessionHistory(rows, id), key);
          }
        };
      });
    } finally {
      db.close();
    }
  });
  pendingClears.set(scope, clearing);
  try {
    await clearing;
    const channel = new BroadcastChannel(SESSION_HISTORY_CHANNEL);
    channel.postMessage({ owner, protocol, id });
    channel.close();
  } finally {
    if (pendingClears.get(scope) === clearing) pendingClears.delete(scope);
  }
}

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

export type EnumerationResult = {
  received: Session[];
  upstreamSuccess: boolean;
  summariesComplete: boolean;
  exhausted: boolean;
  // An owner mismatch or explicit authentication rejection requires live identity revalidation.
  mismatch: boolean;
};

export async function readSessionEnumeration(
  owner: string,
  batches: AsyncIterable<SessionBatch> | Promise<AsyncIterable<SessionBatch>>,
  baseline: () => Session[],
  onProgress: (rows: Session[], summaries: ReadonlyMap<string, boolean>, summariesComplete: boolean) => void,
  signal?: AbortSignal,
): Promise<EnumerationResult> {
  const byID = new Map<string, Session>();
  const summaries = new Map<string, boolean>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let published = false;
  let dirty = false;
  let upstreamSuccess = false;
  let summariesComplete = true;
  let exhausted = false;
  let mismatch = false;
  const cancel = () => clearTimeout(timer);
  const flush = () => {
    timer = undefined;
    if (!dirty || signal?.aborted || mismatch) return;
    dirty = false;
    published ||= byID.size > 0;
    onProgress(mergeSessionRows(baseline(), [...byID.values()]), new Map(summaries), summariesComplete);
    summaries.clear();
  };
  signal?.addEventListener("abort", cancel, { once: true });
  try {
    for await (const batch of await batches) {
      if (signal?.aborted) break;
      if (batch.owner !== owner) {
        mismatch = true;
        upstreamSuccess = summariesComplete = false;
        break;
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
        cancel();
        flush();
      } else if (timer === undefined) timer = setTimeout(flush, 16);
    }
    exhausted = !mismatch && !signal?.aborted;
  } catch (error) {
    mismatch = error instanceof TRPCClientError && error.data?.code === "UNAUTHORIZED";
    // Retain the received prefix, but failed streams cannot promote a snapshot.
  } finally {
    cancel();
    signal?.removeEventListener("abort", cancel);
  }
  flush();
  return { received: [...byID.values()], upstreamSuccess, summariesComplete, exhausted, mismatch };
}
