import { RPCError } from "./api";
import type { Remote, Session, SessionBatch, TabAttention } from "./types";

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
    const request = indexedDB.open("rocketclaw-session-list-v2", 1);
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

// A complete listing as saved: its rows as listed, the version the server named it by, the server's metadata, and the
// attention rows of tabs that are not sessions.
export type SavedListing = { rows: Session[]; version: string; remote?: Remote; tabs?: TabAttention[] };

// The caller must confirm owner with the backend before reading saved rows.
// IndexedDB supplies origin isolation; the key adds user and protocol isolation.
export async function loadSavedSessions(owner: string, protocol: string): Promise<SavedListing | undefined> {
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
export async function saveCompleteSessions(owner: string, protocol: string, listing: SavedListing, generation: number | undefined): Promise<void> {
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
        // a snapshot from a later generation.
        if ((request.result ?? 0) === generation) store.put(listing, [owner, protocol]);
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

const compare = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

// A listing made since the held version sends only the rows changed since and names the rows removed since, so the
// held rows fill in the rest, in the server's order (sidebarSessions): pinned first, then newest updatedAt to the
// nanosecond, then ID. Any other listing sent every row, in that order.
export function listedRows(held: SavedListing, result: Pick<EnumerationResult, "received" | "since" | "removed">): Session[] {
  if (result.since !== held.version) return result.received;
  const removed = new Set(result.removed);
  const rows = mergeSessionRows(held.rows, result.received).filter((row) => !removed.has(row.id));
  // RFC 3339 times in UTC compare as text once their fractions have nine digits.
  const updated = new Map(rows.map((row) => [row, row.updatedAt?.replace(/(\.\d+)?Z$/, (_, fraction = ".") => `${fraction.padEnd(10, "0")}Z`) ?? ""]));
  return rows.toSorted((a, b) => Number(!!b.pinned) - Number(!!a.pinned) || compare(updated.get(b)!, updated.get(a)!) || compare(a.id, b.id));
}

// Tab rows sent since the held version replace the held ones by location; a row with nothing set leaves.
export function listedTabs(held: SavedListing, result: Pick<EnumerationResult, "tabs" | "since">): TabAttention[] {
  const tabs = new Map([...result.since === held.version ? held.tabs ?? [] : [], ...result.tabs ?? []].map((tab) => [tab.path, tab]));
  return [...tabs.values()].filter((tab) => tab.unreadAt || tab.parked || tab.until);
}

export function searchIsAuthoritative(flags: { refreshing: boolean; enumerationComplete: boolean }): boolean {
  return !flags.refreshing && flags.enumerationComplete;
}

export type EnumerationResult = {
  received: Session[];
  complete: boolean;
  // An owner mismatch or explicit authentication rejection requires live identity revalidation.
  mismatch: boolean;
  remote?: Remote;
  // From the completion message: the listing's version, its tab rows and, when sent since a held version, that version
  // and the IDs removed since.
  version?: string;
  since?: string;
  removed?: string[];
  tabs?: TabAttention[];
  // From the first message: the person's tab-state revision.
  tabRevision?: string;
};

// onRemote receives the server's metadata from the listing's first message, before any row.
export async function readSessionEnumeration(
  owner: string,
  batches: AsyncIterable<SessionBatch> | Promise<AsyncIterable<SessionBatch>>,
  baseline: () => Session[],
  onProgress: (rows: Session[]) => void,
  signal?: AbortSignal,
  onRemote: (remote: Remote) => void = () => {},
): Promise<EnumerationResult> {
  const byID = new Map<string, Session>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let dirty = false;
  let upstreamSuccess = false;
  let complete = false;
  let mismatch = false;
  let remote: Remote | undefined;
  let version: string | undefined;
  let since: string | undefined;
  let removed: string[] | undefined;
  let tabs: TabAttention[] | undefined;
  let tabRevision: string | undefined;
  const cancel = () => clearTimeout(timer);
  const flush = () => {
    timer = undefined;
    if (!dirty || signal?.aborted || mismatch) return;
    dirty = false;
    onProgress(mergeSessionRows(baseline(), [...byID.values()]));
  };
  signal?.addEventListener("abort", cancel, { once: true });
  try {
    for await (const batch of await batches) {
      if (signal?.aborted) break;
      if (batch.owner !== undefined) {
        if (batch.owner !== owner) {
          mismatch = true;
          upstreamSuccess = false;
          break;
        }
        remote = { servers: batch.opencodeServers ?? {}, agentTags: Object.fromEntries(Object.entries(batch.agentTags ?? {}).map(([agent, choices]) => [agent, choices.tags ?? []])) };
        tabRevision = batch.tabRevision;
        onRemote(remote);
      }
      upstreamSuccess = batch.upstreamSuccess ?? false;
      if (upstreamSuccess) ({ version, since, removed, tabs } = batch);
      if (!batch.sessions) continue;
      const first = byID.size === 0;
      for (const row of batch.sessions) byID.set(row.id, row);
      dirty = true;
      // First useful row is immediate; subsequent display work coalesces over
      // 16 ms without awaiting the timer or slowing transport consumption.
      // Full-list work remains per publication; incremental rendering is the
      // next step if even frame-sized publications become too expensive.
      if (first && byID.size > 0) {
        cancel();
        flush();
      } else if (timer === undefined) timer = setTimeout(flush, 16);
    }
    complete = upstreamSuccess && !mismatch && !signal?.aborted;
  } catch (error) {
    mismatch = error instanceof RPCError && error.code === 16;
    // Retain the received prefix, but failed streams cannot promote a snapshot.
  } finally {
    cancel();
    signal?.removeEventListener("abort", cancel);
  }
  flush();
  return { received: [...byID.values()], complete, mismatch, remote, version, since, removed, tabs, tabRevision };
}
