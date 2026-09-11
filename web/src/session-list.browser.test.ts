import { expect, test } from "bun:test";
import { existsSync } from "node:fs";
import http from "node:http";
import path from "node:path";
import { Effect, Layer } from "effect";
import { status } from "@grpc/grpc-js";
import { GrpcError, RocketclawTest, type RocketclawApi, type Session, type SessionBatch } from "./grpc";
import { createRPCHandler } from "./transport";
import { WhoisTest } from "./whois";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;

test.skipIf(!playwright || !chromium)("saved sidebar snapshots isolate owners and commit atomically", async () => {
  const build = await Bun.build({ entrypoints: ["./src/session-list.ts"], target: "browser" });
  expect(build.success).toBe(true);
  const script = await build.outputs[0].text();
  const { chromium: engine } = await import(playwright!);
  let browser;
  const server = Bun.serve({
    hostname: "127.0.0.1", port: 0,
    fetch(request) {
      return new URL(request.url).pathname === "/session-list.js"
        ? new Response(script, { headers: { "Content-Type": "text/javascript" } })
        : new Response("<!doctype html><title>Sidebar storage test</title>", { headers: { "Content-Type": "text/html" } });
    },
  });
  try {
    browser = await engine.launch({ executablePath: chromium, headless: true });
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.port}`);
    const initial = await page.evaluate(async () => {
      const path = "/session-list.js";
      const storage = await import(path);
      const rows = [{ id: "one", title: "kept", preview: "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", agent: "agent", settled: true }];
      await storage.saveCompleteSessions("a", "v1", rows, await storage.loadSnapshotGeneration("a", "v1"));
      const put = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function (...args) {
        const request = put.apply(this, args);
        request.addEventListener("success", () => this.transaction.abort());
        return request;
      };
      let rejected = false;
      let deletionRejected = false;
      try {
        await storage.saveCompleteSessions("a", "v1", [{ id: "replacement" }], await storage.loadSnapshotGeneration("a", "v1"));
      } catch {
        rejected = true;
      }
      try {
        await storage.clearSavedSessionHistory("a", "v1", "one");
      } catch {
        deletionRejected = true;
      } finally {
        IDBObjectStore.prototype.put = put;
      }
      return {
        rejected,
        deletionRejected,
        generation: await storage.loadSnapshotGeneration("a", "v1"),
        ownerMiss: await storage.loadSavedSessions("b", "v1") === undefined,
        protocolMiss: await storage.loadSavedSessions("a", "v2") === undefined,
      };
    });
    expect(initial).toEqual({ rejected: true, deletionRejected: true, generation: 0, ownerMiss: true, protocolMiss: true });
    await page.reload();
    const restored = await page.evaluate(async () => {
      const path = "/session-list.js";
      const storage = await import(path);
      const rows = await storage.loadSavedSessions("a", "v1");
      await storage.clearSavedSessionHistory("a", "v1", "one");
      const cleared = await storage.loadSavedSessions("a", "v1");
      await storage.saveCompleteSessions("a", "v1", [], await storage.loadSnapshotGeneration("a", "v1"));
      return { fullPreview: rows[0].preview === "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", cleared, empty: await storage.loadSavedSessions("a", "v1") };
    });
    expect(restored).toEqual({
      fullPreview: true,
      cleared: [{ id: "one", title: "kept", preview: "", updatedAt: "", agent: "agent", settled: true }],
      empty: [],
    });
  } finally {
    server.stop(true);
    if (browser) await browser.close();
  }
}, 30_000);

test.skipIf(!playwright || !chromium)("pending saves, delayed hydration and owner switches cannot beat deletion or leak owners", async () => {
  const build = await Bun.build({ entrypoints: ["./src/session-list.ts"], target: "browser" });
  expect(build.success).toBe(true);
  const script = await build.outputs[0].text();
  const { chromium: engine } = await import(playwright!);
  const server = Bun.serve({
    hostname: "127.0.0.1", port: 0,
    fetch(request) {
      return new URL(request.url).pathname === "/storage.js"
        ? new Response(script, { headers: { "Content-Type": "text/javascript" } })
        : new Response("<!doctype html><title>Sidebar storage races</title>", { headers: { "Content-Type": "text/html" } });
    },
  });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    const deletingPage = await context.newPage();
    await page.goto(`http://127.0.0.1:${server.port}`);
    await deletingPage.goto(`http://127.0.0.1:${server.port}`);
    // No broadcast subscriber in A: the durable transaction check must protect
    // storage even when a paused tab has not processed its notification yet.
    await page.exposeFunction("deleteElsewhere", () => deletingPage.evaluate(async () => {
      const modulePath = "/storage.js";
      const storage = await import(modulePath);
      await storage.clearSavedSessionHistory("owner", "protocol", "synthetic");
    }));
    const result = await page.evaluate(async () => {
      const modulePath = "/storage.js";
      const storage = await import(modulePath);
      const rows = [{ id: "synthetic", preview: "old synthetic preview", agent: "agent", title: "room" }];
      const beforeDelete = await storage.loadSnapshotGeneration("owner", "protocol");
      await storage.saveCompleteSessions("owner", "protocol", rows, beforeDelete);
      const open = IDBFactory.prototype.open;
      const holdOpen = () => {
        const held = Promise.withResolvers<void>();
        const release = Promise.withResolvers<void>();
        IDBFactory.prototype.open = function (...args) {
          const request = open.apply(this, args);
          IDBFactory.prototype.open = open;
          Object.defineProperty(request, "onsuccess", {
            set(handler) {
              request.addEventListener("success", (event) => {
                held.resolve();
                release.promise.then(() => handler.call(request, event));
              }, { once: true });
            },
          });
          return request;
        };
        return { held: held.promise, release: () => { IDBFactory.prototype.open = open; release.resolve(); } };
      };

      const saveGate = holdOpen();
      const pendingSave = storage.saveCompleteSessions("owner", "protocol", rows, beforeDelete);
      await saveGate.held;
      await (window as unknown as { deleteElsewhere: () => Promise<void> }).deleteElsewhere();
      const afterDelete = await storage.loadSavedSessions("owner", "protocol");
      saveGate.release();
      await pendingSave;
      const afterOldSave = await storage.loadSavedSessions("owner", "protocol");

      const afterDeletion = await storage.loadSnapshotGeneration("owner", "protocol");
      await storage.saveCompleteSessions("owner", "protocol", rows, afterDeletion);
      const loadGate = holdOpen();
      const pendingLoad = storage.loadSavedSessions("owner", "protocol");
      await loadGate.held;
      await (window as unknown as { deleteElsewhere: () => Promise<void> }).deleteElsewhere();
      loadGate.release();
      const delayedHydration = await pendingLoad;

      const activeGeneration = await storage.loadSnapshotGeneration("owner", "protocol");
      const switchGate = holdOpen();
      const pendingOwnerSave = storage.saveCompleteSessions("owner", "protocol", rows, activeGeneration);
      await switchGate.held;
      storage.invalidatePendingSaves("owner", "protocol");
      await storage.saveCompleteSessions("other", "protocol", [{ id: "other", preview: "other preview" }], await storage.loadSnapshotGeneration("other", "protocol"));
      switchGate.release();
      await pendingOwnerSave;

      // Keep a real write transaction active while the owner is invalidated.
      const put = IDBObjectStore.prototype.put;
      const writing = Promise.withResolvers<void>();
      let holdTransaction = true;
      IDBObjectStore.prototype.put = function (...args) {
        IDBObjectStore.prototype.put = put;
        const request = put.apply(this, args);
        const keepActive = () => {
          writing.resolve();
          if (holdTransaction) this.get(["owner", "protocol"]).onsuccess = keepActive;
        };
        request.addEventListener("success", keepActive);
        return request;
      };
      const activeSave = storage.saveCompleteSessions("owner", "protocol", [{ id: "synthetic", preview: "uncommitted owner preview" }], activeGeneration).catch(() => {});
      await writing.promise;
      storage.invalidatePendingSaves("owner", "protocol");
      holdTransaction = false;
      await activeSave;
      const olderGate = holdOpen();
      const olderSave = storage.saveCompleteSessions("ordering", "protocol", [{ id: "row", preview: "older" }], 0);
      await olderGate.held;
      await storage.saveCompleteSessions("ordering", "protocol", [{ id: "row", preview: "newer" }], 0);
      olderGate.release();
      await olderSave;
      const newest = await storage.loadSavedSessions("ordering", "protocol");
      const deleteGate = holdOpen();
      const pendingDelete = storage.clearSavedSessionHistory("ordering", "protocol", "row");
      await deleteGate.held;
      const postDeleteSave = storage.loadSnapshotGeneration("ordering", "protocol").then((generation: number) =>
        storage.saveCompleteSessions("ordering", "protocol", [{ id: "row", preview: "after deletion" }], generation));
      // Let the newer save reach its transaction if it does not wait for deletion.
      await storage.loadSavedSessions("ordering", "protocol");
      deleteGate.release();
      await Promise.all([pendingDelete, postDeleteSave]);
      return {
        newest,
        postDelete: await storage.loadSavedSessions("ordering", "protocol"),
        afterDelete: afterDelete[0].preview,
        afterOldSave: afterOldSave[0].preview,
        routing: afterOldSave[0].title,
        delayedHydration,
        other: await storage.loadSavedSessions("other", "protocol"),
        ownerAfterSwitch: await storage.loadSavedSessions("owner", "protocol"),
      };
    });
    expect(result).toEqual({
      newest: [{ id: "row", preview: "newer" }],
      postDelete: [{ id: "row", preview: "after deletion" }],
      afterDelete: "",
      afterOldSave: "",
      routing: "room",
      delayedHydration: [{ id: "synthetic", preview: "", updatedAt: "", agent: "agent", title: "room" }],
      other: [{ id: "other", preview: "other preview" }],
      ownerAfterSwitch: [{ id: "synthetic", preview: "", updatedAt: "", agent: "agent", title: "room" }],
    });
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

const built = existsSync(path.resolve(import.meta.dir, "../.next"));

test.skipIf(!playwright || !chromium || !built)("actual App restores, merges, isolates, deletes and keeps composer independent", async () => {
  const { default: next } = await import("next");
  const { chromium: engine } = await import(playwright!);
  let identityHold = Promise.withResolvers<void>();
  let promptHold = Promise.withResolvers<string>();
  const createHold = Promise.withResolvers<string>();
  const deletion = Promise.withResolvers<void>();
  const wireTail = Promise.withResolvers<void>();
  const ownerTail = Promise.withResolvers<void>();
  let blocked = Promise.withResolvers<void>();
  const ctrl = {
    username: "alice",
    protocol: "test-protocol",
    identityError: false,
    listCalls: 0,
    prompt: [] as string[],
    holdPrompt: false,
    promptStarted: Promise.withResolvers<void>(),
    holdCreate: false,
    createStarted: Promise.withResolvers<void>(),
    deleteFail: false,
    deleteHold: Promise.resolve(),
    deleteStarted: Promise.withResolvers<void>(),
    yieldBatches: async function* (): AsyncGenerator<SessionBatch> {},
  };
  const row = (id: string, preview: string): Session => ({
    id, title: "room", preview, updatedAt: "2026-09-09T00:00:00.123456Z", agent: "main", settled: false,
  });
  const batch = (sessions: Session[], flags: Partial<SessionBatch> = {}): SessionBatch => ({
    sessions, owner: flags.owner ?? ctrl.username, upstreamSuccess: flags.upstreamSuccess ?? false, summariesComplete: flags.summariesComplete ?? true,
  });
  const complete = (sessions: Session[], owner = ctrl.username) => async function* () {
    if (sessions.length > 0) yield batch(sessions, { owner });
    yield batch([], { owner, upstreamSuccess: true, summariesComplete: true });
  };
  const api: RocketclawApi = {
    listSessionEntries: () => Effect.succeed([{ id: "1", type: "turn" }]),
    loadSessionEntries: () => Effect.succeed([]),
    deleteSessionEntries: () => {
      ctrl.deleteStarted.resolve();
      return ctrl.deleteFail ? Effect.fail(new GrpcError({ message: "nope" })) : Effect.promise(() => ctrl.deleteHold.then(() => "1"));
    },
    listSessions: async function* () {
      ctrl.listCalls += 1;
      await blocked.promise;
      yield* ctrl.yieldBatches();
    },
    identity: () => ctrl.identityError
      ? Effect.fail(new GrpcError({ message: "unauthenticated" }))
      : Effect.promise(() => identityHold.promise.then(() => ctrl.username)),
    createSession: () => {
      ctrl.createStarted.resolve();
      return ctrl.holdCreate ? Effect.promise(() => createHold.promise) : Effect.succeed("web-session:new");
    },
    prompt: (_principal, id, text) => {
      ctrl.prompt.push(`${id}:${text}`);
      ctrl.promptStarted.resolve();
      return ctrl.holdPrompt ? Effect.promise(() => promptHold.promise) : Effect.succeed("");
    },
    listCronJobs: () => Effect.succeed([]),
    runCronJob: () => Effect.succeed(""),
    history: () => Effect.succeed([]),
    listAgents: () => Effect.succeed({ agents: [{ name: "main", model: "gpt" }, { name: "other", model: "gpt" }], currentAgent: "main" }),
    listSkills: () => Effect.succeed([]),
    listConfig: () => Effect.succeed({}),
    settleSession: () => Effect.void,
    protocol: () => Effect.succeed(ctrl.protocol),
    listQueue: () => Effect.succeed([]),
    removeQueueItem: () => Effect.void,
    steerQueueItem: () => Effect.void,
    reorderQueue: () => Effect.void,
    join: () => Effect.void,
  };
  const app = next({ dev: false, dir: path.resolve(import.meta.dir, "..") });
  await app.prepare();
  const rpc = createRPCHandler(Layer.merge(RocketclawTest(api), WhoisTest(() => Effect.succeed("principal"))));
  const handle = app.getRequestHandler();
  let listResponse: http.ServerResponse;
  const server = http.createServer((req, res) => {
    if (req.url?.startsWith("/trpc/sessions")) listResponse = res;
    return req.url?.startsWith("/trpc") ? rpc(req, res) : handle(req, res);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = (server.address() as import("node:net").AddressInfo).port;
  const origin = `http://127.0.0.1:${port}`;
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  const shown = async (page: { getByText: (text: string, opts?: { exact?: boolean }) => { waitFor: (opts: { state: "visible" | "hidden"; timeout?: number }) => Promise<void>; count: () => Promise<number> } }, text: string) => {
    await page.getByText(text, { exact: true }).waitFor({ state: "visible", timeout: 15_000 });
  };
  const hidden = async (page: { getByText: (text: string, opts?: { exact?: boolean }) => { count: () => Promise<number> } }, text: string) => {
    expect(await page.getByText(text, { exact: true }).count()).toBe(0);
  };
  const snapshot = async ([owner, protocol]: string[]) => {
    const request = indexedDB.open("rocketclaw-session-list", 1);
    const db = await new Promise<IDBDatabase>((resolve) => { request.onsuccess = () => resolve(request.result); });
    try {
      const tx = db.transaction("snapshots", "readonly");
      const saved = tx.objectStore("snapshots").get([owner, protocol]);
      await new Promise<void>((resolve) => { tx.oncomplete = () => resolve(); });
      return saved.result;
    } finally { db.close(); }
  };
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await context.newPage();
    await page.addInitScript(() => {
      const open = IDBFactory.prototype.open;
      Object.defineProperty(window, "__delayOpen", { value: sessionStorage.getItem("delaySnapshotOpen") === "1", writable: true });
      sessionStorage.removeItem("delaySnapshotOpen");
      Object.defineProperty(window, "__openHeld", { value: false, writable: true });
      Object.defineProperty(window, "__releaseOpen", { value: () => {}, writable: true });
      Object.defineProperty(window, "__snapshotPuts", { value: 0, writable: true });
      const put = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function (...args) {
        if (this.name === "snapshots") (window as unknown as { __snapshotPuts: number }).__snapshotPuts += 1;
        return put.apply(this, args);
      };
      IDBFactory.prototype.open = function (...args) {
        const request = open.apply(this, args);
        const state = window as unknown as { __delayOpen: boolean; __openHeld: boolean; __releaseOpen: () => void };
        if (!state.__delayOpen) return request;
        state.__delayOpen = false;
        const release = Promise.withResolvers<void>();
        state.__releaseOpen = () => release.resolve();
        Object.defineProperty(request, "onsuccess", {
          set(handler) {
            request.addEventListener("success", (event) => {
              state.__openHeld = true;
              void release.promise.then(() => handler.call(request, event));
            }, { once: true });
          },
        });
        return request;
      };
    });
    await page.goto(origin);
    await hidden(page, "saved preview");
    await hidden(page, "Huge");
    await page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve, reject) => {
        const request = indexedDB.open("rocketclaw-session-list", 1);
        request.onupgradeneeded = () => request.result.createObjectStore("snapshots");
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction("snapshots", "readwrite");
        tx.oncomplete = () => resolve();
        tx.onabort = () => reject(tx.error);
        tx.objectStore("snapshots").put([{ id: "kept", title: "room", preview: "Huge\n" + "start\0" + "x".repeat(17 * 1024 * 1024) + "\0needle-17mib", agent: "main" }], ["alice", "test-protocol"]);
      });
      db.close();
    });
    identityHold.resolve();
    await shown(page, "Huge");
    await hidden(page, "will vanish");
    await page.getByPlaceholder("Search or agent: or room:").fill("needle-17mib");
    await shown(page, "Huge");
    await page.getByPlaceholder("Search or agent: or room:").fill("no-such-preview");
    await shown(page, "loading...");
    await hidden(page, "No matches");
    await page.getByPlaceholder("Search or agent: or room:").fill("");

    const putsBeforeLive = await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts);
    ctrl.yieldBatches = complete([row("kept", "saved preview"), row("gone", "will vanish"), row("slack-thread:C:1", "filter preview")]);
    blocked.resolve();
    await shown(page, "saved preview");
    await shown(page, "will vanish");
    expect(await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts)).toBe(putsBeforeLive + 1);

    blocked = Promise.withResolvers();
    await page.getByText("Refreshing", { exact: true }).waitFor({ state: "visible", timeout: 15_000 });
    await shown(page, "saved preview");
    const heldCalls = ctrl.listCalls;

    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "Sessions" }).click();
    expect(await page.getByText("saved preview", { exact: true }).count()).toBeGreaterThan(1);
    await page.keyboard.press("Escape");
    expect(ctrl.listCalls).toBe(heldCalls);

    await page.getByRole("button", { name: "main" }).click();
    await page.getByRole("button", { name: "other" }).click();
    ctrl.holdPrompt = true;
    await page.getByPlaceholder("Message a new session").fill("hello while held");
    await page.getByRole("button", { name: "Send" }).click();
    await ctrl.promptStarted.promise;
    expect(ctrl.prompt).toEqual(["web-session:new:hello while held"]);
    await page.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
    await page.getByPlaceholder("Queue a follow-up · ⌘⏎ steers").waitFor();
    const createdResponse = page.waitForResponse("**/trpc/prompt*");
    promptHold.resolve("private reply for created session");
    await (await createdResponse).finished();
    await shown(page, "private reply for created session");
    expect(await page.getByPlaceholder("Message or $command").inputValue()).toBe("");
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.getByPlaceholder("Message or $command").fill("created draft to discard");
    await page.getByRole("link", { name: "RocketClaw", exact: true }).click();
    await page.waitForURL(origin + "/");
    expect(await page.getByPlaceholder("Message a new session").inputValue()).toBe("");
    await hidden(page, "private reply for created session");
    promptHold = Promise.withResolvers();

    ctrl.holdCreate = true;
    ctrl.createStarted = Promise.withResolvers();
    ctrl.promptStarted = Promise.withResolvers();
    await page.getByPlaceholder("Message a new session").fill("send from abandoned home");
    await page.getByRole("button", { name: "Send" }).click();
    await ctrl.createStarted.promise;
    await page.getByRole("link").filter({ hasText: "will vanish" }).click();
    await page.waitForURL("**/s/Z29uZQ");
    await page.getByRole("link", { name: "RocketClaw", exact: true }).click();
    await page.waitForURL(origin + "/");
    await page.getByPlaceholder("Message a new session").fill("unrelated new draft");
    createHold.resolve("web-session:abandoned");
    await ctrl.promptStarted.promise;
    const abandonedResponse = page.waitForResponse("**/trpc/prompt*");
    promptHold.resolve("private reply for abandoned home");
    await (await abandonedResponse).finished();
    await page.getByPlaceholder("Message a new session").press("End");
    expect(page.url()).toBe(origin + "/");
    expect(await page.getByPlaceholder("Message a new session").inputValue()).toBe("unrelated new draft");
    await hidden(page, "private reply for abandoned home");
    expect(ctrl.prompt.filter((item) => item === "web-session:abandoned:send from abandoned home")).toHaveLength(1);
    ctrl.holdCreate = false;
    promptHold = Promise.withResolvers();

    await page.getByRole("link").filter({ hasText: "saved preview" }).click();
    await page.waitForURL("**/s/a2VwdA");
    ctrl.promptStarted = Promise.withResolvers();
    await page.getByPlaceholder("Message or $command").fill("held in A");
    await page.getByRole("button", { name: "Send" }).click();
    await ctrl.promptStarted.promise;
    await page.getByRole("link").filter({ hasText: "will vanish" }).click();
    await page.waitForURL("**/s/Z29uZQ");
    await page.getByPlaceholder("Message or $command").fill("B draft");
    await hidden(page, "Thinking");
    expect(await page.getByRole("button", { name: "Stop", exact: true }).count()).toBe(0);
    const promptResponse = page.waitForResponse("**/trpc/prompt*");
    promptHold.resolve("private reply for A");
    await (await promptResponse).finished();
    // A completed render after the response must not clear B's draft or append A's reply.
    await page.getByPlaceholder("Message or $command").press("End");
    expect(await page.getByPlaceholder("Message or $command").inputValue()).toBe("B draft");
    await hidden(page, "private reply for A");
    ctrl.holdPrompt = false;
    const selectedURL = page.url();
    await page.getByPlaceholder("Search or agent: or room:").fill("agent:main");
    await page.keyboard.press("Enter");
    await page.getByPlaceholder("Search", { exact: true }).fill("room:room");
    await page.keyboard.press("Enter");
    await page.getByPlaceholder("Search", { exact: true }).fill("preview");
    await shown(page, "filter preview");
    ctrl.yieldBatches = async function* () {
      for (const session of [row("kept", "merged-live"), row("extra", "prefix-only"), ...Array.from({ length: 446 }, (_, i) => ({ ...row(i < 2 ? `slack-thread:C:${i + 2}` : `prefix-${i}`, `partial preview ${i}`), agent: i === 1 ? "other" : "main", settled: i === 7 }))]) {
        yield batch([session]);
      }
      await new Promise(() => {});
    };
    blocked.resolve();
    await shown(page, "partial preview 0");
    await hidden(page, "merged-live");
    // One matching arrival proves progress; each structured filter excludes its own mismatch.
    await hidden(page, "partial preview 1");
    await hidden(page, "partial preview 2");
    await shown(page, "agent:main");
    await shown(page, "room:room");
    await shown(page, "filter preview");
    expect(await page.getByPlaceholder("Search", { exact: true }).inputValue()).toBe("preview");
    await page.getByRole("button", { name: "agent:main", exact: true }).click();
    await page.getByRole("button", { name: "room:room", exact: true }).click();
    await page.getByPlaceholder("Search or agent: or room:").fill("");
    await shown(page, "merged-live");
    await shown(page, "prefix-only");
    await shown(page, "will vanish");

    expect(page.url()).toBe(selectedURL);
    for (let i = 0; i < 8; i++) await shown(page, `partial preview ${i}`);
    const sidebarItems = await page.locator("aside li").allTextContents();
    expect(sidebarItems.at(-2)).toBe("Settled");
    expect(sidebarItems.at(-1)).toContain("partial preview 7");
    expect(await page.locator("aside").getByRole("button", { name: "Unsettle", exact: true }).count()).toBe(1);
    await page.getByPlaceholder("Search or agent: or room:").fill("partial preview 445");
    await shown(page, "partial preview 445");
    await page.getByPlaceholder("Search or agent: or room:").fill("");

    await page.evaluate(() => sessionStorage.setItem("delaySnapshotOpen", "1"));
    await page.reload();
    await page.waitForFunction(() => (window as unknown as { __openHeld: boolean }).__openHeld);
    await shown(page, "merged-live");
    await hidden(page, "will vanish");
    await page.evaluate(() => (window as unknown as { __releaseOpen: () => void }).__releaseOpen());
    await shown(page, "will vanish");
    await shown(page, "merged-live");

    blocked = Promise.withResolvers();
    await page.reload();
    await shown(page, "saved preview");
    await hidden(page, "prefix-only");

    ctrl.yieldBatches = async function* () {
      yield batch([row("kept", "should-not-save")], { upstreamSuccess: true, summariesComplete: true });
      throw new Error("late");
    };
    blocked.resolve();
    await page.waitForTimeout(500);
    blocked = Promise.withResolvers();
    await page.reload();
    await shown(page, "saved preview");
    await hidden(page, "should-not-save");

    ctrl.yieldBatches = async function* () {
      yield batch([row("kept", "wire-terminal")], { upstreamSuccess: true, summariesComplete: true });
      await wireTail.promise;
    };
    blocked.resolve();
    await shown(page, "wire-terminal");
    listResponse!.destroy();
    await shown(page, "Stale");
    await page.getByPlaceholder("Search or agent: or room:").fill("missing-stale-preview");
    await shown(page, "loading...");
    await hidden(page, "No matches");
    await page.getByPlaceholder("Search or agent: or room:").fill("");
    wireTail.resolve();
    blocked = Promise.withResolvers();
    await page.reload();
    await shown(page, "saved preview");

    const putsBeforeIncomplete = await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts);
    const beforeIncomplete = await page.evaluate(snapshot, ["alice", "test-protocol"]);
    ctrl.yieldBatches = async function* () {
      yield batch([row("kept", ""), row("ghost", "")], { summariesComplete: false });
      yield batch([], { upstreamSuccess: true, summariesComplete: false });
    };
    blocked.resolve();
    await page.getByText("loading...", { exact: true }).first().waitFor({ state: "visible", timeout: 15_000 });
    await page.getByPlaceholder("Search or agent: or room:").fill("zzz-nope");
    await shown(page, "loading...");
    await shown(page, "Stale");
    expect(await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts)).toBe(putsBeforeIncomplete);
    expect(await page.evaluate(snapshot, ["alice", "test-protocol"])).toEqual(beforeIncomplete);
    ctrl.yieldBatches = complete([row("kept", "backfilled preview")]);
    await shown(page, "No matches");
    await hidden(page, "loading...");
    await hidden(page, "Stale");
    await page.getByPlaceholder("Search or agent: or room:").fill("");
    await shown(page, "backfilled preview");
    await page.waitForFunction(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts > 0);
    expect(await page.evaluate(snapshot, ["alice", "test-protocol"])).toEqual([row("kept", "backfilled preview")]);
    blocked = Promise.withResolvers();
    await page.reload();
    await shown(page, "backfilled preview");

    ctrl.yieldBatches = complete([]);
    blocked.resolve();
    await page.getByText("backfilled preview", { exact: true }).waitFor({ state: "hidden", timeout: 15_000 });
    await hidden(page, "saved preview");
    await page.getByPlaceholder("Search or agent: or room:").fill("empty-search");
    await shown(page, "No matches");
    await hidden(page, "loading...");
    await page.getByPlaceholder("Search or agent: or room:").fill("");
    blocked = Promise.withResolvers();
    await page.reload();
    await hidden(page, "saved preview");

    ctrl.yieldBatches = complete([row("kept", "saved preview"), row("gone", "will vanish")]);
    blocked.resolve();
    await shown(page, "saved preview");
    await page.getByRole("link").filter({ hasText: "saved preview" }).click();
    await page.getByText("Session entries").click();
    await page.getByPlaceholder("Search or agent: or room:").fill("saved");
    blocked = Promise.withResolvers();
    await page.evaluate(() => {
      (window as unknown as { __delayOpen: boolean; __openHeld: boolean }).__delayOpen = true;
      (window as unknown as { __openHeld: boolean }).__openHeld = false;
    });
    ctrl.yieldBatches = complete([row("kept", "saved preview"), row("gone", "will vanish")]);
    blocked.resolve();
    await page.waitForFunction(() => (window as unknown as { __openHeld: boolean }).__openHeld);
    blocked = Promise.withResolvers();
    page.once("dialog", (dialog: { accept: () => Promise<void> }) => dialog.accept());
    await page.getByRole("button", { name: "Delete entries", exact: true }).click();
    await page.getByText("Deleted 1 entries.").waitFor();
    await hidden(page, "saved preview");
    expect(await page.getByPlaceholder("Search or agent: or room:").inputValue()).toBe("saved");
    await page.evaluate(() => (window as unknown as { __releaseOpen: () => void }).__releaseOpen());
    await page.waitForTimeout(200);
    await hidden(page, "saved preview");
    blocked = Promise.withResolvers();
    await page.reload();
    await hidden(page, "saved preview");
    await shown(page, "will vanish");

    // Both pages share IndexedDB. Keep A's pre-deletion HTTP stream alive even
    // when deletion in B cancels A's enumeration, then deliver its old tail.
    ctrl.yieldBatches = complete([row("kept", "cross-tab secret"), row("gone", "current row")]);
    blocked.resolve();
    await shown(page, "cross-tab secret");
    const stalePage = await page.context().newPage();
    const crossTabTail = Promise.withResolvers<void>();
    try {
      await stalePage.addInitScript(() => {
        window.fetch = new Proxy(window.fetch, {
          apply(target, receiver, [input, init]) {
            return Reflect.apply(target, receiver, [input, String(input).includes("/trpc/sessions") ? { ...init, signal: undefined } : init]);
          },
        });
      });
      ctrl.yieldBatches = async function* () {
        yield batch([row("kept", "cross-tab secret"), row("gone", "current row")]);
        await crossTabTail.promise;
        yield batch([row("kept", "cross-tab secret")], { upstreamSuccess: true });
      };
      const oldRequest = stalePage.waitForRequest("**/trpc/sessions*");
      await stalePage.goto(origin);
      await shown(stalePage, "cross-tab secret");
      const request = await oldRequest;
      const oldFinished = stalePage.waitForEvent("requestfinished", { predicate: (value: unknown) => value === request });
      blocked = Promise.withResolvers();
      await page.getByText("Session entries").click();
      page.once("dialog", (dialog: { accept: () => Promise<void> }) => dialog.accept());
      await page.getByRole("button", { name: "Delete entries", exact: true }).click();
      await page.getByText("Deleted 1 entries.").waitFor();
      crossTabTail.resolve();
      await oldFinished;
      await stalePage.getByText("cross-tab secret", { exact: true }).waitFor({ state: "hidden", timeout: 5000 });
      await shown(stalePage, "current row");
      await stalePage.getByPlaceholder("Search or agent: or room:").fill("cross-tab secret");
      await hidden(stalePage, "cross-tab secret");
      await stalePage.reload();
      await shown(stalePage, "current row");
      await hidden(stalePage, "cross-tab secret");
      expect(await stalePage.evaluate(snapshot, ["alice", "test-protocol"])).toEqual([
        { ...row("kept", ""), updatedAt: "" }, row("gone", "current row"),
      ]);
    } finally {
      crossTabTail.resolve();
      await stalePage.close();
    }

    ctrl.deleteFail = true;
    ctrl.yieldBatches = complete([row("kept", "saved preview"), row("gone", "will vanish")]);
    blocked.resolve();
    await shown(page, "will vanish");
    await page.getByRole("link").filter({ hasText: "will vanish" }).click();
    await page.getByText("Session entries").click();
    page.once("dialog", (dialog: { accept: () => Promise<void> }) => dialog.accept());
    await page.getByRole("button", { name: "Delete entries", exact: true }).click();
    await page.getByText("nope", { exact: true }).waitFor();
    await shown(page, "will vanish");

    ctrl.deleteFail = false;
    ctrl.deleteHold = deletion.promise;
    ctrl.deleteStarted = Promise.withResolvers();
    page.once("dialog", (dialog: { accept: () => Promise<void> }) => dialog.accept());
    await page.getByRole("button", { name: "Delete entries", exact: true }).click();
    await ctrl.deleteStarted.promise;
    // Leave Alice's HTTP stream alive despite the App abort so its tail really arrives after Bob.
    await page.evaluate(() => {
      window.fetch = new Proxy(window.fetch, {
        apply(target, receiver, [input, init]) {
          return Reflect.apply(target, receiver, [input, String(input).includes("/trpc/sessions") ? { ...init, signal: undefined } : init]);
        },
      });
    });
    const ownerStarted = Promise.withResolvers<void>();
    const ownerRequest = page.waitForRequest("**/trpc/sessions*");
    ctrl.yieldBatches = async function* () {
      yield batch([row("gone", "alice held preview")], { owner: "alice" });
      ownerStarted.resolve();
      await ownerTail.promise;
      yield* complete([row("gone", "late alice preview")], "alice")();
    };
    await ownerStarted.promise;
    await shown(page, "alice held preview");
    const oldRequest = await ownerRequest;
    const oldResponse = page.waitForEvent("requestfinished", { predicate: (request: unknown) => request === oldRequest });
    blocked = Promise.withResolvers();
    ctrl.username = "bob";
    await page.evaluate(() => {
      (window as unknown as { __delayOpen: boolean; __openHeld: boolean }).__delayOpen = true;
      (window as unknown as { __openHeld: boolean }).__openHeld = false;
    });
    await page.waitForFunction(() => (window as unknown as { __openHeld: boolean }).__openHeld);
    await hidden(page, "will vanish");
    await hidden(page, "bob preview");
    await page.evaluate(() => (window as unknown as { __releaseOpen: () => void }).__releaseOpen());
    await hidden(page, "will vanish");
    ctrl.yieldBatches = complete([row("gone", "bob preview")], "bob");
    blocked.resolve();
    await shown(page, "bob preview");
    await hidden(page, "will vanish");
    blocked = Promise.withResolvers();
    ownerTail.resolve();
    await oldResponse;
    await shown(page, "bob preview");
    await hidden(page, "late alice preview");
    expect(await page.evaluate(snapshot, ["bob", "test-protocol"])).toEqual([row("gone", "bob preview")]);
    deletion.resolve();
    await page.getByText("Deleted 1 entries.").waitFor();
    await shown(page, "bob preview");

    await page.reload();
    await shown(page, "bob preview");
    await page.waitForFunction(async () => {
      const request = indexedDB.open("rocketclaw-session-list", 1);
      const db = await new Promise<IDBDatabase>((resolve) => { request.onsuccess = () => resolve(request.result); });
      try {
        const tx = db.transaction("snapshots", "readonly");
        const saved = tx.objectStore("snapshots").get(["alice", "test-protocol"]);
        await new Promise<void>((resolve) => { tx.oncomplete = () => resolve(); });
        return saved.result?.find((item: Session) => item.id === "gone")?.preview === "";
      } finally {
        db.close();
      }
    });

    const protocolReload = page.waitForEvent("load");
    ctrl.protocol = "test-protocol-2";
    await protocolReload;
    await shown(page, "Refreshing");
    await hidden(page, "bob preview");
    ctrl.yieldBatches = complete([row("gone", "new protocol preview")], "bob");
    blocked.resolve();
    await shown(page, "new protocol preview");

    for (const rejection of ["mismatch", "unauthorized"] as const) {
      identityHold = Promise.withResolvers();
      ctrl.yieldBatches = async function* () {
        if (rejection === "unauthorized") throw new GrpcError({ message: "denied", code: status.UNAUTHENTICATED });
        yield batch([row("other-owner", "unconfirmed owner preview")], { owner: "unconfirmed" });
      };
      await page.getByText("new protocol preview", { exact: true }).waitFor({ state: "hidden", timeout: 15_000 });
      await hidden(page, "unconfirmed owner preview");
      await page.getByPlaceholder("Search or agent: or room:").fill("preview");
      await hidden(page, "new protocol preview");
      blocked = Promise.withResolvers();
      ctrl.yieldBatches = complete([row("gone", "new protocol preview")], "bob");
      identityHold.resolve();
      await shown(page, "new protocol preview");
      await page.getByPlaceholder("Search or agent: or room:").fill("");
      blocked.resolve();
    }

    // Complete live identity fetches in the same task, so observer
    // notifications may coalesce away reset's intermediate pending result.
    await page.evaluate(() => {
      const original = window.fetch;
      Object.defineProperty(window, "__restoreIdentityFetch", { value: () => { window.fetch = original; } });
      window.fetch = new Proxy(original, {
        apply(target, receiver, [input, init]) {
          if (String(input).includes("/trpc/identity")) {
            const response = new Response();
            response.json = async () => ({ result: { data: "bob" } });
            return Promise.resolve(response);
          }
          return Reflect.apply(target, receiver, [input, init]);
        },
      });
    });
    ctrl.yieldBatches = async function* () {
      ctrl.yieldBatches = complete([row("gone", "fast same-owner recovery")], "bob");
      yield batch([], { owner: "unconfirmed" });
    };
    await shown(page, "fast same-owner recovery");
    await page.evaluate(() => (window as unknown as { __restoreIdentityFetch: () => void }).__restoreIdentityFetch());

    ctrl.identityError = true;
    await page.waitForTimeout(3000);
    await hidden(page, "bob preview");
    await hidden(page, "new protocol preview");
    await hidden(page, "fast same-owner recovery");

    ctrl.identityError = false;
    for (const failure of ["unavailable", "quota"] as const) {
      ctrl.yieldBatches = complete([row("live-storage-failure", failure === "quota" ? "prior committed preview" : "live without storage")], "bob");
      const storagePage = await browser.newPage();
      await storagePage.addInitScript((mode: string) => {
        if (mode === "unavailable") {
          IDBFactory.prototype.open = function () { throw new DOMException("Storage unavailable", "SecurityError"); };
        } else {
          const put = IDBObjectStore.prototype.put;
          Object.defineProperty(window, "__committed", { value: false, writable: true });
          IDBObjectStore.prototype.put = function (...args) {
            this.transaction.addEventListener("complete", () => { (window as unknown as { __committed: boolean }).__committed = true; });
            return put.apply(this, args);
          };
        }
      }, failure);
      try {
        await storagePage.goto(origin);
        if (failure === "quota") {
          await shown(storagePage, "prior committed preview");
          await storagePage.waitForFunction(() => (window as unknown as { __committed: boolean }).__committed);
          const prior = await storagePage.evaluate(snapshot, ["bob", ctrl.protocol]);
          expect(prior).toEqual([row("live-storage-failure", "prior committed preview")]);
          await storagePage.evaluate(() => {
            Object.defineProperty(window, "__quotaFailed", { value: false, writable: true });
            IDBObjectStore.prototype.put = function () {
              (window as unknown as { __quotaFailed: boolean }).__quotaFailed = true;
              throw new DOMException("Storage full", "QuotaExceededError");
            };
          });
          ctrl.yieldBatches = complete([row("live-storage-failure", "live without storage")], "bob");
          await storagePage.waitForFunction(() => (window as unknown as { __quotaFailed: boolean }).__quotaFailed);
          expect(await storagePage.evaluate(snapshot, ["bob", ctrl.protocol])).toEqual(prior);
        }
        await shown(storagePage, "live without storage");
        await storagePage.getByPlaceholder("Message a new session").fill(`send with ${failure} storage`);
        await storagePage.getByRole("button", { name: "Send" }).click();
        await storagePage.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
        expect(ctrl.prompt.some((item) => item.includes(`send with ${failure} storage`))).toBe(true);
      } finally {
        await storagePage.close();
      }
    }
  } finally {
    blocked.resolve();
    identityHold.resolve();
    promptHold.resolve("");
    createHold.resolve("web-session:abandoned");
    deletion.resolve();
    wireTail.resolve();
    ownerTail.resolve();
    await browser.close();
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await app.close();
  }
}, 180_000);
