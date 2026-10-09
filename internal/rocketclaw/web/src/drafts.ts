export type DraftContent = { text: string; files: { id: string; file: File }[]; agent: string };
let pending: Promise<unknown> = Promise.resolve();

// IndexedDB clones File bytes; transcript and execution state never enter this store.
export function draftContent(key: string, value?: DraftContent): Promise<DraftContent | undefined> {
  const work = pending.then(async () => {
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const open = indexedDB.open("rocketclaw-drafts", 1);
      open.onupgradeneeded = () => open.result.createObjectStore("content");
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error);
    });
    try {
      return await new Promise<DraftContent | undefined>((resolve, reject) => {
        const tx = db.transaction("content", value ? "readwrite" : "readonly");
        const store = tx.objectStore("content");
        const request = value ? store.put(value, key) : store.get(key);
        tx.oncomplete = () => resolve(value ? undefined : request.result as DraftContent | undefined);
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
    } finally { db.close(); }
  });
  pending = work.catch(() => {});
  return work;
}
