import { RPCError } from "./api";
import type { Remote, Session, SessionBatch } from "./types";

export const SESSION_HISTORY_CHANNEL = "rocketclaw-session-history";

const epochs = new Map<string, number>();
let globalEpoch = 0;
const pendingSaves = new Map<IDBTransaction, string>();

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
      tx.oncomplete = () => resolve(epoch === snapshotEpoch(owner, protocol) ? request.result?.map((row: Session) => row.running || row.background ? { ...row, running: false, background: false } : row) : undefined);
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

export function mergeSessionRows(baseline: Session[], received: Session[]): Session[] {
  const seen = new Set(received.map((row) => row.id));
  return [...received, ...baseline.filter((row) => !seen.has(row.id))];
}

// A listing made since a version the caller holds sends only the rows changed since; its order names every row, so
// the held rows fill in the rest and a row missing from it is gone. Undefined when a row is in neither, which a full
// listing repairs. A listing without an order is complete as received.
export function listedRows(held: Session[], received: Session[], order: string[] | undefined): Session[] | undefined {
  if (!order) return received;
  const byID = new Map([...held, ...received].map((row) => [row.id, row]));
  const rows = order.flatMap((id) => byID.get(id) ?? []);
  return rows.length === order.length ? rows : undefined;
}

export function stripSessionHistory(rows: Session[], id: string): Session[] {
  return rows.map((row) => (row.id === id ? { ...row, preview: "", updatedAt: "" } : row));
}

export function shouldCommitSnapshot(flags: { upstreamSuccess: boolean; exhausted: boolean; mismatch: boolean }): boolean {
  return flags.upstreamSuccess && flags.exhausted && !flags.mismatch;
}

export function searchIsAuthoritative(flags: { refreshing: boolean; enumerationComplete: boolean }): boolean {
  return !flags.refreshing && flags.enumerationComplete;
}

export type EnumerationResult = {
  received: Session[];
  upstreamSuccess: boolean;
  exhausted: boolean;
  // An owner mismatch or explicit authentication rejection requires live identity revalidation.
  mismatch: boolean;
  remote?: Remote;
  // From the completion message: the listing's version and every session ID in order.
  version?: string;
  order?: string[];
};

export async function readSessionEnumeration(
  owner: string,
  batches: AsyncIterable<SessionBatch> | Promise<AsyncIterable<SessionBatch>>,
  baseline: () => Session[],
  onProgress: (rows: Session[]) => void,
  signal?: AbortSignal,
): Promise<EnumerationResult> {
  const byID = new Map<string, Session>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let published = false;
  let dirty = false;
  let upstreamSuccess = false;
  let exhausted = false;
  let mismatch = false;
  let remote: Remote | undefined;
  let version: string | undefined;
  let order: string[] | undefined;
  const cancel = () => clearTimeout(timer);
  const flush = () => {
    timer = undefined;
    if (!dirty || signal?.aborted || mismatch) return;
    dirty = false;
    published ||= byID.size > 0;
    onProgress(mergeSessionRows(baseline(), [...byID.values()]));
  };
  signal?.addEventListener("abort", cancel, { once: true });
  try {
    for await (const batch of await batches) {
      if (signal?.aborted) break;
      if (batch.owner !== owner) {
        mismatch = true;
        upstreamSuccess = false;
        break;
      }
      for (const row of batch.sessions) byID.set(row.id, row);
      upstreamSuccess = batch.upstreamSuccess;
      if (upstreamSuccess) {
        ({ version, order } = batch);
        remote = { servers: batch.opencodeServers ?? {}, agentTags: Object.fromEntries(Object.entries(batch.agentTags ?? {}).map(([agent, choices]) => [agent, choices.tags ?? []])) };
      }
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
    mismatch = error instanceof RPCError && error.code === 16;
    // Retain the received prefix, but failed streams cannot promote a snapshot.
  } finally {
    cancel();
    signal?.removeEventListener("abort", cancel);
  }
  flush();
  return { received: [...byID.values()], upstreamSuccess, exhausted, mismatch, remote, version, order };
}
