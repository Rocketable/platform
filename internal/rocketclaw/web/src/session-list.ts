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

export type SavedListing = { rows: Session[]; version: string; remote?: Remote; tabs?: TabAttention[] };

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

export async function saveCompleteSessions(owner: string, protocol: string, listing: SavedListing, generation: number | undefined): Promise<void> {
  if (generation === undefined) return;
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

export function listedRows(held: SavedListing, result: Pick<EnumerationResult, "received" | "since" | "removed">): Session[] {
  if (result.since !== held.version) return result.received;
  const removed = new Set(result.removed);
  const rows = mergeSessionRows(held.rows, result.received).filter((row) => !removed.has(row.id));
  const updated = new Map(rows.map((row) => [row, row.updatedAt?.replace(/(\.\d+)?Z$/, (_, fraction = ".") => `${fraction.padEnd(10, "0")}Z`) ?? ""]));
  return rows.sort((a, b) => compare(updated.get(b)!, updated.get(a)!) || compare(a.id, b.id));
}

export function listedTabs(held: SavedListing, result: Pick<EnumerationResult, "tabs" | "since">): TabAttention[] {
  const tabs = [...result.since === held.version ? held.tabs ?? [] : [], ...result.tabs ?? []].reduce((byPath, tab) => byPath.set(tab.path, tab), new Map<string, TabAttention>());
  return [...tabs.values()].filter((tab) => tab.unreadAt || tab.parked || tab.until || tab.unfollowed);
}

export function searchIsAuthoritative(flags: { refreshing: boolean; enumerationComplete: boolean }): boolean {
  return !flags.refreshing && flags.enumerationComplete;
}

export type EnumerationResult = {
  received: Session[];
  complete: boolean;
  mismatch: boolean;
  remote?: Remote;
  version?: string;
  since?: string;
  removed?: string[];
  tabs?: TabAttention[];
  tabRevision?: string;
  navigate?: string;
};

export async function readSessionEnumeration(
  owner: string,
  batches: AsyncIterable<SessionBatch>,
  baseline: () => Session[],
  onProgress: (rows: Session[]) => void,
  signal: AbortSignal,
  onRemote: (remote: Remote) => void,
): Promise<EnumerationResult> {
  const byID = new Map<string, Session>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let dirty = false;
  let upstreamSuccess = false;
  const result: EnumerationResult = {
    received: [], complete: false, mismatch: false, remote: undefined,
    version: undefined, since: undefined, removed: undefined, tabs: undefined, tabRevision: undefined, navigate: undefined,
  };
  const cancel = () => clearTimeout(timer);
  const flush = () => {
    timer = undefined;
    if (!dirty || signal.aborted || result.mismatch) return;
    dirty = false;
    onProgress(mergeSessionRows(baseline(), [...byID.values()]));
  };
  signal.addEventListener("abort", cancel, { once: true });
  try {
    for await (const batch of batches) {
      if (signal.aborted) break;
      if (batch.owner !== undefined) {
        if (batch.owner !== owner) {
          result.mismatch = true;
          upstreamSuccess = false;
          break;
        }
        result.remote = { servers: batch.opencodeServers ?? {}, agentTags: Object.fromEntries(Object.entries(batch.agentTags ?? {}).map(([agent, choices]) => [agent, choices.tags ?? []])) };
        ({ tabRevision: result.tabRevision, navigate: result.navigate } = batch);
        onRemote(result.remote);
      }
      upstreamSuccess = batch.upstreamSuccess ?? false;
      if (upstreamSuccess) ({ version: result.version, since: result.since, removed: result.removed, tabs: result.tabs } = batch);
      if (!batch.sessions) continue;
      const first = byID.size === 0;
      for (const row of batch.sessions) byID.set(row.id, row);
      dirty = true;
      if (first && byID.size > 0) {
        cancel();
        flush();
      } else if (timer === undefined) timer = setTimeout(flush, 16);
    }
    result.complete = upstreamSuccess && !result.mismatch && !signal.aborted;
  } catch (error) {
    result.mismatch = error instanceof RPCError && error.code === 16;
  } finally {
    cancel();
    signal.removeEventListener("abort", cancel);
  }
  flush();
  result.received = [...byID.values()];
  return result;
}
