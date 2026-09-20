import { expect, test } from "bun:test";
import { existsSync } from "node:fs";
import path from "node:path";
import type { Attachment, QueueItem, Session, SessionBatch, TranscriptEvent } from "./types";
import { RPCError } from "./api";

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
      const rows = [{ id: "one", title: "kept", preview: "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", agent: "agent", settled: true, running: true }];
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
      return { running: rows[0].running, fullPreview: rows[0].preview === "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", cleared, empty: await storage.loadSavedSessions("a", "v1") };
    });
    expect(restored).toEqual({
      fullPreview: true,
      running: false,
      cleared: [{ id: "one", title: "kept", preview: "", updatedAt: "", agent: "agent", settled: true, running: false }],
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

const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const built = existsSync(path.join(dist, "index.html"));

test.skipIf(!playwright || !chromium || !built)("actual App restores, merges, isolates and keeps composer independent", async () => {
  const { chromium: engine } = await import(playwright!);
  let identityHold = Promise.withResolvers<void>();
  let promptHold = Promise.withResolvers<string>();
  let createHold = Promise.withResolvers<string>();
  let uploadHold = Promise.withResolvers<void>();
  const cronHold = Promise.withResolvers<string>();
  let cronRunCalls = 0;
  const cronHistory: string[] = [];
  const cronOpens: string[] = [];
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
    holdInterventions: false,
    interventions: new Map<string, ReturnType<typeof Promise.withResolvers<string>>>(),
    queue: [] as QueueItem[],
    holdUpload: false,
    uploadError: "",
    uploadStarted: Promise.withResolvers<void>(),
    currentAgents: new Map<string, string>(),
    promptError: false,
    attachmentPrompts: [] as { id: string; text: string; delivery?: string; attachmentIds?: string[] }[],
    promptStarted: Promise.withResolvers<void>(),
    holdCreate: false,
    createStarted: Promise.withResolvers<void>(),
    history: [] as TranscriptEvent[],
    settledRows: [] as Session[],
    settleCalls: [] as { id: string; settled: boolean }[],
    updateError: false,
    createdAgents: [] as string[],
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
  const uploadedFiles: { meta: Attachment; data: Buffer }[] = [];
  const imageBytes = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1kAAAAASUVORK5CYII=", "base64");
  const image: Attachment = { id: "history-image", name: "history.png", mimeType: "image/png", size: String(imageBytes.length), conversationId: "private-source", originalUnverified: true };
  const downloadConversations: string[] = [];
  const jobs = [
      { stem: "daily", status: "", lastRun: "", nextRun: "", schedule: "24h", body: "Daily prompt", agent: "planner", channel: "#ops", upcoming: [new Date(Date.now() + 3_600_000).toISOString(), new Date(Date.now() + 18 * 3_600_000).toISOString()], origin: "cron/daily.md" },
      { stem: "unused", status: "", lastRun: "", nextRun: "", upcoming: [] },
      { stem: "daily", status: "ran", lastRun: new Date(Date.now() - 3_600_000).toISOString(), nextRun: "cron-chat", origin: "cron-source" },
      { stem: "retired", status: "ran", lastRun: "2026-09-01T00:00:00Z", nextRun: "old-chat", origin: "old-source" },
      { stem: "silent", status: "ran", lastRun: new Date(Date.now() - 3_600_000).toISOString(), nextRun: "", origin: "cron:silent-source" },
  ];
  let listResponse: ReadableStreamDefaultController;
  let transcriptStream = Promise.withResolvers<ReadableStreamDefaultController>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(req) {
    const url = new URL(req.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) {
      transcriptStream.resolve(controller);
      controller.enqueue(": connected\n\n");
    } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(new ReadableStream({ async start(controller) {
      listResponse = controller;
      ctrl.listCalls++;
      try {
        await blocked.promise;
        for await (const value of ctrl.yieldBatches()) controller.enqueue(`data: ${JSON.stringify(value)}\n\n`);
        controller.enqueue("event: complete\ndata: {}\n\n");
        controller.close();
      } catch (error) {
        if (req.signal.aborted) return;
        try {
          controller.enqueue(`event: error\ndata: ${JSON.stringify({ message: (error as Error).message, code: (error as RPCError).code ?? 13 })}\n\n`);
          controller.close();
        } catch { /* The wire-cut case already closed the stream. */ }
      }
    } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/UploadAttachment") {
      const data = Buffer.from(await req.arrayBuffer());
      const meta = { id: `upload-${uploadedFiles.length}`, conversationId: url.searchParams.get("conversationId")!, name: url.searchParams.get("name")!, mimeType: req.headers.get("content-type") ?? "application/octet-stream", size: String(data.length) };
      uploadedFiles.push({ meta, data });
      ctrl.uploadStarted.resolve();
      if (ctrl.holdUpload) await uploadHold.promise;
      if (ctrl.uploadError === meta.name) return new Response("upload rejected", { status: 500 });
      return Response.json(meta);
    }
    if (url.pathname === "/api/DownloadAttachment") {
      downloadConversations.push(url.searchParams.get("conversationId")!);
      const uploaded = uploadedFiles.find(({ meta }) => meta.id === url.searchParams.get("id"));
      if (uploaded) {
        if (!ctrl.history.some((line) => line.attachments?.some((file) => file.id === uploaded.meta.id))) return new Response("not referenced", { status: 404 });
        return new Response(new Uint8Array(uploaded.data), { headers: { "Content-Type": uploaded.meta.mimeType } });
      }
      return new Response(imageBytes, { headers: { "Content-Type": image.mimeType, "Content-Disposition": `${url.searchParams.has("download") ? "attachment" : "inline"}; filename="${image.name}"` } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() && url.pathname !== "/" ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await req.json() as { id: string; itemId: string; messageId: string; name?: string; agent?: string; text: string; delivery?: "STEER" | "QUEUE"; attachmentIds?: string[]; stem: string; sourceConversationId?: string; conversationId?: string; settled: boolean; pinned?: boolean };
    try {
      switch (url.pathname) {
        case "/api/Protocol": return Response.json({ protoSha256: ctrl.protocol });
        case "/api/Identity":
          if (ctrl.identityError) throw new RPCError("unauthenticated", 16);
          await identityHold.promise;
          return Response.json({ username: ctrl.username });
        case "/api/CreateSession":
          if (input.sourceConversationId) {
            cronOpens.push(input.sourceConversationId);
            ctrl.currentAgents.set("web:cron:silent-source", "other");
            return Response.json({ id: "web:cron:silent-source" });
          }
          ctrl.createdAgents.push(input.agent ?? "");
          ctrl.createStarted.resolve();
          { const id = ctrl.holdCreate ? await createHold.promise : "web-session:new";
            ctrl.currentAgents.set(id, input.agent ?? "main");
            return Response.json({ id }); }
        case "/api/Prompt":
          if (input.attachmentIds?.length) ctrl.attachmentPrompts.push(input);
          ctrl.prompt.push(`${input.id}:${input.text}`);
          ctrl.promptStarted.resolve();
          if (input.text.startsWith("$agent ")) {
            ctrl.currentAgents.set(input.id, input.text.slice(7));
            return Response.json({ privateText: `Switched to ${input.text.slice(7)}` });
          }
          if (input.text === "$stop") return Response.json({ privateText: "Stopped" });
          if (ctrl.promptError) throw new RPCError("Send failed; retry", 13);
          if (ctrl.holdInterventions) {
            const id = input.delivery === "QUEUE" ? `server-${input.messageId}` : input.messageId;
            ctrl.queue.push({ id, text: input.text, delivery: input.delivery });
            if (input.delivery === "QUEUE") return Response.json({ privateText: "" });
            const pending = Promise.withResolvers<string>();
            ctrl.interventions.set(id, pending);
            return Response.json({ privateText: await pending.promise });
          }
          return Response.json({ privateText: ctrl.holdPrompt ? await promptHold.promise : "" });
        case "/api/ListCronJobs": return Response.json({ jobs });
        case "/api/RunCronJob": cronRunCalls++; return Response.json({ id: await cronHold.promise });
        case "/api/History":
          if (input.id === "cron:silent-source" || input.id === "web:cron:silent-source") {
            cronHistory.push(input.id);
            return Response.json({ messages: [{ role: "assistant", text: "Silent run trace", complete: true }] });
          }
          if (input.sourceConversationId) {
            cronHistory.push(input.sourceConversationId);
            return Response.json({ messages: [{ role: "assistant", text: "Daily run report", complete: true, snapshot: false, turnId: "" }] });
          }
          return Response.json({ messages: ctrl.history });
        case "/api/ListAgents": return Response.json({ agents: input.conversationId === "web:cron:silent-source" ? [{ name: "other", model: "gpt" }] : [{ name: "other", model: "gpt" }, { name: "main", model: "gpt" }], currentAgent: input.conversationId ? ctrl.currentAgents.get(input.conversationId) ?? "main" : "" });
        case "/api/ListSkills": return Response.json({ skills: input.agent === "main" ? [{ name: "review", description: "Review changes" }, { name: "stop", description: "Inspect logs" }] : [] });
        case "/api/ListConfig": return Response.json({ config: { webAutoSettleAfter: "1h30m0s", tailscaleUser: "connected@example.com" } });
        case "/api/SettleSession":
          ctrl.settleCalls.push({ id: input.id, settled: input.settled });
          ctrl.settledRows = ctrl.settledRows.map((session) => session.id === input.id ? { ...session, settled: input.settled } : session);
          ctrl.yieldBatches = complete(ctrl.settledRows);
          return Response.json({});
        case "/api/UpdateSession":
          if (ctrl.updateError) throw new RPCError("Could not save session", 13);
          ctrl.settledRows = ctrl.settledRows.map((session) => session.id === input.id ? { ...session, ...input, name: input.name === undefined ? session.name : input.name.trim() } : session);
          ctrl.yieldBatches = complete(ctrl.settledRows);
          return Response.json({});
        case "/api/ListQueue": return Response.json({ items: ctrl.queue });
        case "/api/SteerQueueItem":
          ctrl.queue = ctrl.queue.map((item) => item.id === input.itemId ? { ...item, delivery: "STEER" } : item);
          return Response.json({});
        default: return Response.json({});
      }
    } catch (error) { return Response.json({ message: (error as Error).message, code: (error as RPCError).code }, { status: (error as RPCError).code === 16 ? 401 : 500 }); }
  } });
  const port = server.port;
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
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, hasTouch: true });
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
    await page.emulateMedia({ colorScheme: "dark" });
    await page.getByRole("button", { name: "Theme system", exact: true }).waitFor();
    expect(await page.getByRole("button", { name: "Theme system", exact: true }).innerText()).toBe("");
    await page.waitForFunction(() => document.documentElement.classList.contains("dark"));
    await page.getByRole("button", { name: "Theme system", exact: true }).click();
    expect(await page.getByRole("button", { name: "Theme light", exact: true }).innerText()).toBe("");
    await page.waitForFunction(() => !document.documentElement.classList.contains("dark"));
    await page.getByRole("button", { name: "Theme light", exact: true }).click();
    expect(await page.getByRole("button", { name: "Theme dark", exact: true }).innerText()).toBe("");
    await page.waitForFunction(() => document.documentElement.classList.contains("dark"));
    await page.emulateMedia({ colorScheme: "light" });
    expect(await page.evaluate(() => document.documentElement.classList.contains("dark"))).toBe(true);
    expect(await page.evaluate(() => localStorage.getItem("theme"))).toBe("dark");
    await page.getByRole("button", { name: "Theme dark", exact: true }).click();
    await page.waitForFunction(() => !document.documentElement.classList.contains("dark"));
    await page.emulateMedia({ colorScheme: "dark" });
    await page.waitForFunction(() => document.documentElement.classList.contains("dark"));
    await page.emulateMedia({ colorScheme: "light" });
    await page.waitForFunction(() => !document.documentElement.classList.contains("dark"));
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
    expect(await page.evaluate(async () => { await document.fonts.ready; return document.fonts.check("16px Inter") && getComputedStyle(document.body).fontFamily.startsWith("Inter"); })).toBe(true);
    expect(await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts)).toBe(putsBeforeLive + 1);

    blocked = Promise.withResolvers();
    await page.waitForRequest("**/api/ListSessions*");
    await hidden(page, "Refreshing");
    await shown(page, "saved preview");
    const heldCalls = ctrl.listCalls;

    const navigation = page.locator("footer");
    const compactHeight = (await navigation.boundingBox())!.height;
    const navigationAgent = navigation.getByRole("link", { name: "Agents", exact: true });
    expect((await navigationAgent.boundingBox())!.width).toBe(48);
    expect((await navigationAgent.locator("svg").boundingBox())!.width).toBe(24);
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).click();
    expect(await navigationAgent.isVisible()).toBe(false);
    expect((await navigation.boundingBox())!.height).toBeLessThan(compactHeight);
    expect(await navigation.getByRole("button", { name: "Show bottom navigation" }).getAttribute("aria-expanded")).toBe("false");
    await navigation.getByRole("button", { name: "Show bottom navigation" }).press("Enter");
    expect(await navigationAgent.isVisible()).toBe(true);
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).press("Space");
    await navigation.getByRole("button", { name: "Show bottom navigation" }).click();
    expect((await navigation.boundingBox())!.height).toBe(compactHeight);

    const desktopSidebar = page.locator("#session-sidebar");
    const menuPosition = await page.getByRole("button", { name: "Hide sidebar", exact: true }).boundingBox();
    expect((await desktopSidebar.boundingBox())!.y).toBe(0);
    expect(menuPosition!.y + menuPosition!.height).toBeGreaterThan(page.viewportSize()!.height - 8);
    expect(await page.locator("header:visible").count()).toBe(0);
    await desktopSidebar.getByPlaceholder("Search or agent: or room:").fill("saved");
    await page.getByRole("button", { name: "Hide sidebar", exact: true }).click();
    expect(await desktopSidebar.isVisible()).toBe(false);
    expect(await page.getByRole("button", { name: "Show sidebar", exact: true }).boundingBox()).toEqual(menuPosition);
    expect(await page.getByRole("button", { name: "Show sidebar", exact: true }).getAttribute("aria-expanded")).toBe("false");
    await page.getByRole("button", { name: "Show sidebar", exact: true }).press("Enter");
    expect(await desktopSidebar.isVisible()).toBe(true);
    expect(await page.getByRole("button", { name: "Hide sidebar", exact: true }).boundingBox()).toEqual(menuPosition);
    expect(await desktopSidebar.getByPlaceholder("Search or agent: or room:").inputValue()).toBe("saved");
    await desktopSidebar.getByPlaceholder("Search or agent: or room:").fill("");

    await page.setViewportSize({ width: 390, height: 844 });
    const mobileSessions = page.getByRole("button", { name: "Sessions", exact: true });
    const mobileSessionsBox = (await mobileSessions.boundingBox())!;
    expect(mobileSessionsBox.x).toBe(8);
    expect(mobileSessionsBox.y).toBe(8);
    const themeBox = (await page.getByRole("button", { name: /^Theme / }).boundingBox())!;
    expect(mobileSessionsBox.width).toBe(themeBox.width);
    expect(mobileSessionsBox.height).toBe(themeBox.height);
    expect(await navigation.getByRole("button", { name: "Sessions", exact: true }).count()).toBe(0);
    await page.waitForFunction(() => document.querySelector('button[aria-label="Hide bottom navigation"]')!.getBoundingClientRect().height >= 44);
    expect((await navigation.getByRole("button", { name: "Hide bottom navigation" }).boundingBox())!.height).toBeGreaterThanOrEqual(44);
    expect((await navigation.getByRole("link", { name: "Agents", exact: true }).boundingBox())!.width).toBeGreaterThanOrEqual(44);
    const agentsBounds = (await navigation.getByRole("link", { name: "Agents", exact: true }).boundingBox())!;
    const skillsBounds = (await navigation.getByRole("link", { name: "Skills", exact: true }).boundingBox())!;
    expect(skillsBounds.x - agentsBounds.x - agentsBounds.width).toBeGreaterThanOrEqual(8);
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).tap();
    expect(await navigationAgent.isVisible()).toBe(false);
    expect(await mobileSessions.isVisible()).toBe(true);
    await navigation.getByRole("button", { name: "Show bottom navigation" }).tap();
    expect(await navigationAgent.isVisible()).toBe(true);
    expect(await navigation.getByRole("button", { name: "Hide bottom navigation" }).evaluate((element: HTMLElement) => {
      const style = getComputedStyle(element);
      return [style.borderTopWidth, style.boxShadow, style.backgroundColor];
    })).toEqual(["0px", "none", "rgba(0, 0, 0, 0)"]);
    const swipeHandle = (await navigation.getByRole("button", { name: "Hide bottom navigation" }).boundingBox())!;
    const touch = await context.newCDPSession(page);
    const x = swipeHandle.x + swipeHandle.width / 2;
    const y = swipeHandle.y + swipeHandle.height / 2;
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: y + 40 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await navigation.getByRole("button", { name: "Show bottom navigation" }).waitFor();
    expect(await navigationAgent.isVisible()).toBe(false);
    const showHandle = (await navigation.getByRole("button", { name: "Show bottom navigation" }).boundingBox())!;
    const showY = showHandle.y + showHandle.height / 2;
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y: showY }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: showY - 40 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).waitFor();
    expect(await navigationAgent.isVisible()).toBe(true);
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: y + 40 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchCancel", touchPoints: [] });
    expect(await navigationAgent.isVisible()).toBe(true);
    expect((await navigation.getByRole("button", { name: "Hide bottom navigation" }).locator("..").boundingBox())!.height).toBe(16);
    const routeBeforeSwipe = page.url();
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 150, y: 300 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: 155, y: 400 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    expect(await page.getByRole("dialog", { name: "Sessions", exact: true }).count()).toBe(0);
    const composerBox = (await page.locator("textarea").boundingBox())!;
    const composerY = composerBox.y + composerBox.height / 2;
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 100, y: composerY }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: 200, y: composerY }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    expect(await page.getByRole("dialog", { name: "Sessions", exact: true }).count()).toBe(0);
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 150, y: 300 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: 250, y: 305 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    const mobileSidebar = page.getByRole("dialog", { name: "Sessions", exact: true });
    await mobileSidebar.waitFor();
    await page.waitForFunction(() => document.activeElement === document.querySelector('[data-sidebar-swipe="close"]'));
    await mobileSidebar.getByPlaceholder("Search or agent: or room:").click({ trial: true });
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 180, y: 300 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: 80, y: 305 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await mobileSidebar.waitFor({ state: "hidden" });
    expect(page.url()).toBe(routeBeforeSwipe);
    await touch.detach();
    await page.getByRole("button", { name: "Sessions" }).click();
    expect((await page.getByRole("dialog").boundingBox())!.y).toBe(0);
    expect(await page.getByRole("link", { name: "RocketClaw", exact: true, includeHidden: true }).count()).toBe(0);
    expect(await page.getByText("saved preview", { exact: true }).count()).toBeGreaterThan(1);
    await page.keyboard.press("Escape");
    expect(ctrl.listCalls).toBe(heldCalls);

    const skillComposer = page.getByPlaceholder("Message a new session");
    await skillComposer.fill("$ski");
    await skillComposer.press("Tab");
    expect(await skillComposer.inputValue()).toBe("$skill ");
    await page.getByRole("button", { name: "$skill review [args] Review changes", exact: true }).waitFor();
    await skillComposer.fill("$skill st");
    await skillComposer.press("Tab");
    expect(await skillComposer.inputValue()).toBe("$skill stop ");
    expect(ctrl.prompt).toEqual([]);
    await skillComposer.fill("");

    await skillComposer.fill("retained through browser history");
    await page.getByRole("link", { name: "Agents", exact: true }).click();
    await page.waitForURL("**/agents");
    await page.goBack();
    await page.waitForURL(origin + "/");
    expect(await skillComposer.inputValue()).toBe("retained through browser history");
    await page.goForward();
    await page.waitForURL("**/agents");
    await page.getByRole("link", { name: "Agents", exact: true }).click();
    await page.waitForURL(origin + "/");
    expect(await skillComposer.inputValue()).toBe("retained through browser history");
    await skillComposer.fill("");

    await page.getByRole("combobox", { name: "Choose agent" }).click();
    await page.getByRole("option", { name: "other gpt" }).click();
    ctrl.holdPrompt = true;
    await page.getByPlaceholder("Message a new session").fill("hello\n\nwhile held");
    await page.getByRole("button", { name: "Send" }).click();
    await ctrl.promptStarted.promise;
    expect(ctrl.prompt).toEqual(["web-session:new:hello\n\nwhile held"]);
    await page.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
    await page.getByPlaceholder("Queue a follow-up · ⌘⏎ steers").waitFor();
    expect(await page.locator("textarea").isEnabled()).toBe(true);
    expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
    // Selecting chat text offers quoting without replacing the native context menu.
    await page.locator("textarea").fill("My draft");
    const selectedQuote: string = await page.locator("#transcript-scroll").evaluate((element: HTMLElement) => {
      const range = document.createRange();
      range.selectNodeContents(element.querySelector('[aria-label="Turn 1"]')!);
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
      return selection.toString();
    });
    await page.getByRole("button", { name: "Quote", exact: true }).waitFor();
    expect(await page.locator("#transcript-scroll").evaluate((element: HTMLElement) => element.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })))).toBe(true);
    await page.getByRole("button", { name: "Quote", exact: true }).click();
    expect(selectedQuote).toBe("hello\n\nwhile held");
    const quotedDraft = "My draft\n\n> hello\n> \n> while held\n\n";
    expect(await page.locator("textarea").inputValue()).toBe(quotedDraft);
    expect(await page.locator("textarea").evaluate((element: HTMLTextAreaElement) => [document.activeElement === element, element.selectionStart, element.selectionEnd])).toEqual([true, quotedDraft.length, quotedDraft.length]);
    await page.keyboard.type("My comment");
    expect(await page.locator("textarea").inputValue()).toBe(quotedDraft + "My comment");
    await page.getByRole("button", { name: "Quote", exact: true }).waitFor({ state: "hidden" });
    await page.locator("textarea").evaluate((element: HTMLTextAreaElement) => element.select());
    expect(await page.getByRole("button", { name: "Quote", exact: true }).count()).toBe(0);
    await page.locator("textarea").fill("");
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 844 });
      const word = await page.locator('#transcript-scroll [data-slot="bubble-content"] div').first().evaluate((element: HTMLElement) => {
        const range = document.createRange();
        range.setStart(element.firstChild!, 0);
        range.setEnd(element.firstChild!, 5);
        const rect = range.getBoundingClientRect();
        return { left: rect.left, right: rect.right, y: rect.top + rect.height / 2 };
      });
      await page.mouse.move(word.left, word.y);
      await page.mouse.down();
      await page.mouse.move(word.right, word.y, { steps: 8 });
      await page.mouse.up();
      const quoteButton = page.getByRole("button", { name: "Quote", exact: true });
      await quoteButton.waitFor();
      const bounds = await quoteButton.boundingBox();
      expect(bounds!.x).toBeGreaterThanOrEqual(0);
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
      if (width === 1280) {
        await quoteButton.click();
        expect(await page.locator("textarea").inputValue()).toBe("> hello\n\n");
        await page.locator("textarea").fill("");
      } else {
        await page.keyboard.press("Escape");
        await quoteButton.waitFor({ state: "hidden" });
      }
    }
    // The original Prompt is still blocked while interventions use the same conversation.
    ctrl.holdPrompt = false;
    ctrl.holdInterventions = true;
    const interventionIds: string[] = [];
    const chat = page.getByRole("region", { name: "Messages", exact: true });
    const parking = page.getByRole("region", { name: "Pending steers", exact: true });
    for (const delivery of ["QUEUE", "STEER", "STEER"] as const) {
      await page.locator("textarea").fill("identical follow-up");
      const intervention = page.waitForRequest("**/api/Prompt");
      if (delivery === "QUEUE") await page.locator("textarea").press("Enter");
      else {
        const steerBox = await page.getByRole("button", { name: "Steer", exact: true }).last().boundingBox();
        const sendBox = await page.getByRole("button", { name: "Send", exact: true }).boundingBox();
        expect(steerBox!.height).toBeGreaterThanOrEqual(44);
        expect(steerBox!.x + steerBox!.width).toBeLessThanOrEqual(sendBox!.x);
        expect(steerBox!.y).toBe(sendBox!.y);
        await page.screenshot({ path: path.join(process.env.TMPDIR!, "chat-steer-mobile.png") });
        await page.getByRole("button", { name: "Steer", exact: true }).last().click();
      }
      const request = (await intervention).postDataJSON();
      expect(request).toEqual({ id: "web-session:new", text: "identical follow-up", delivery, messageId: expect.any(String) });
      interventionIds.push(request.messageId);
      expect(await chat.getByText("identical follow-up", { exact: true }).count()).toBe(0);
      if (delivery === "STEER") await parking.getByText("identical follow-up", { exact: true }).nth(interventionIds.length - 2).waitFor();
    }
    expect(new Set(interventionIds).size).toBe(3);
    const stream = await transcriptStream.promise;
    stream.enqueue(`data: ${JSON.stringify({ role: "assistant", turnId: "intervention-run", text: "Before steering", complete: false })}\n\n`);
    for (const [index, id] of [interventionIds[2], interventionIds[1]].entries()) {
      stream.enqueue(`data: ${JSON.stringify({ role: "user", messageId: id, text: "identical follow-up" })}\n\n`);
      await chat.getByText("identical follow-up", { exact: true }).nth(index).waitFor();
      ctrl.interventions.get(id)!.resolve("");
    }
    await parking.waitFor({ state: "hidden" });
    await chat.getByText("identical follow-up", { exact: true }).nth(1).waitFor();
    // A stale queue poll must not resurrect either consumed steer.
    expect(await page.locator("[data-queue-id]").count()).toBe(1);
    stream.enqueue(`data: ${JSON.stringify({ role: "assistant", turnId: "intervention-run", text: "Before steering\nAfter steering", complete: false })}\n\n`);
    await chat.getByText("After steering", { exact: true }).waitFor();
    const queuedId = `server-${interventionIds[0]}`;
    await page.locator(`[data-queue-id="${queuedId}"]`).getByRole("button", { name: "Steer", exact: true }).click();
    await parking.getByText("identical follow-up", { exact: true }).waitFor();
    expect(await chat.getByText("identical follow-up", { exact: true }).count()).toBe(2);
    stream.enqueue(`data: ${JSON.stringify({ role: "user", messageId: queuedId, text: "identical follow-up" })}\n\n`);
    await parking.waitFor({ state: "hidden" });
    await chat.getByText("identical follow-up", { exact: true }).nth(2).waitFor();
    const ordered = await chat.locator('[data-slot="bubble-content"]').allTextContents();
    expect(ordered.slice(-5)).toEqual(["Before steering", "identical follow-up", "identical follow-up", "After steering", "identical follow-up"]);
    ctrl.queue = [];
    ctrl.holdInterventions = false;
    const stopOriginal = page.waitForRequest("**/api/Prompt");
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    expect((await stopOriginal).postDataJSON()).toEqual({ id: "web-session:new", text: "$stop" });
    await page.locator("textarea").fill("newer draft while the turn runs");
    const createdResponse = page.waitForResponse("**/api/Prompt*");
    promptHold.resolve("private reply for created session");
    await (await createdResponse).finished();
    await shown(page, "private reply for created session");
    expect(await page.getByPlaceholder("Message or $command").inputValue()).toBe("newer draft while the turn runs");
    await page.setViewportSize({ width: 1280, height: 800 });
    // A consumed picker must follow a later explicit switch, including after navigation.
    ctrl.holdPrompt = false;
    await page.locator("textarea").fill("$agent main");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await shown(page, "Switched to main");
    await page.getByRole("combobox", { name: "Choose agent" }).filter({ hasText: "main" }).waitFor();
    await page.getByRole("link").filter({ hasText: "will vanish" }).click();
    await page.goBack();
    await page.locator("textarea").fill("stay with main");
    const switchedResponse = page.waitForResponse("**/api/Prompt");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await switchedResponse;
    expect(ctrl.prompt.slice(-2)).toEqual(["web-session:new:$agent main", "web-session:new:stay with main"]);
    ctrl.holdPrompt = true;
    await page.locator("textarea").fill("created draft to discard");
    await page.locator("footer").getByRole("button", { name: "New session", exact: true }).click();
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
    await page.locator("footer").getByRole("button", { name: "New session", exact: true }).click();
    await page.waitForURL(origin + "/");
    await page.getByPlaceholder("Message a new session").fill("unrelated new draft");
    createHold.resolve("web-session:abandoned");
    await ctrl.promptStarted.promise;
    const abandonedResponse = page.waitForResponse("**/api/Prompt*");
    promptHold.resolve("private reply for abandoned home");
    await (await abandonedResponse).finished();
    await page.getByPlaceholder("Message a new session").press("End");
    expect(page.url()).toBe(origin + "/");
    expect(await page.getByPlaceholder("Message a new session").inputValue()).toBe("unrelated new draft");
    await hidden(page, "private reply for abandoned home");
    expect(ctrl.prompt.filter((item) => item === "web-session:abandoned:send from abandoned home")).toHaveLength(1);
    ctrl.holdCreate = false;
    promptHold = Promise.withResolvers();

    // Browser Back restores the same home draft before creation resolves.
    await page.locator("footer").getByRole("button", { name: "New session", exact: true }).click();
    createHold = Promise.withResolvers();
    ctrl.holdCreate = true;
    ctrl.createStarted = Promise.withResolvers();
    ctrl.promptStarted = Promise.withResolvers();
    await page.locator("textarea").fill("restored home submission");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await ctrl.createStarted.promise;
    await page.getByRole("link").filter({ hasText: "will vanish" }).click();
    await page.goBack();
    await page.waitForURL(origin + "/");
    expect(await page.locator("textarea").inputValue()).toBe("restored home submission");
    createHold.resolve("web-session:restored");
    await page.waitForURL(`**/s/${Buffer.from("web-session:restored").toString("base64url")}`);
    await ctrl.promptStarted.promise;
    await shown(page, "restored home submission");
    await page.locator("textarea").fill("restored fresh draft");
    const restoredResponse = page.waitForResponse("**/api/Prompt");
    promptHold.resolve("restored reply");
    await restoredResponse;
    await shown(page, "restored reply");
    expect(await page.locator("textarea").inputValue()).toBe("restored fresh draft");
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
    const promptResponse = page.waitForResponse("**/api/Prompt*");
    promptHold.resolve("private reply for A");
    await (await promptResponse).finished();
    // A completed render after the response must not clear B's draft or append A's reply.
    await page.getByPlaceholder("Message or $command").press("End");
    expect(await page.getByPlaceholder("Message or $command").inputValue()).toBe("B draft");
    await hidden(page, "private reply for A");
    // A late failure must not complete a newer, still-blocked steer in this session.
    promptHold = Promise.withResolvers();
    ctrl.promptStarted = Promise.withResolvers();
    await page.locator("textarea").fill("older failing turn");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await ctrl.promptStarted.promise;
    const olderTurn = promptHold;
    promptHold = Promise.withResolvers();
    ctrl.promptStarted = Promise.withResolvers();
    await page.locator("textarea").fill("newer held steer");
    await page.locator("textarea").press("Control+Enter");
    await ctrl.promptStarted.promise;
    await page.locator("textarea").fill("newer unsent draft");
    const olderFailure = page.waitForResponse((response: { request: () => { postDataJSON: () => { text: string } }; url: () => string }) => response.url().endsWith("/api/Prompt") && response.request().postDataJSON().text === "older failing turn");
    olderTurn.reject(new RPCError("older turn failed late", 13));
    await (await olderFailure).finished();
    // Removal of its optimistic row proves the browser processed the rejection.
    await page.getByText("older failing turn", { exact: true }).waitFor({ state: "hidden" });
    expect(await page.locator("textarea").inputValue()).toBe("newer unsent draft");
    expect(await page.locator("textarea").getAttribute("placeholder")).toBe("Queue a follow-up · ⌘⏎ steers");
    await hidden(page, "older turn failed late");
    await page.locator("textarea").fill("");
    expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
    await page.locator("textarea").fill("newer unsent draft");
    const newerCompletion = page.waitForResponse("**/api/Prompt");
    promptHold.resolve("newer turn completed");
    await newerCompletion;
    await shown(page, "newer turn completed");
    expect(await page.locator("textarea").inputValue()).toBe("newer unsent draft");
    ctrl.holdPrompt = false;
    const selectedURL = page.url();
    await page.getByPlaceholder("Search or agent: or room:").fill("agent:main");
    await page.keyboard.press("Enter");
    await page.locator("#session-sidebar").getByPlaceholder("Search", { exact: true }).fill("room:room");
    await page.keyboard.press("Enter");
    await page.locator("#session-sidebar").getByPlaceholder("Search", { exact: true }).fill("preview");
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
    expect(await page.locator("#session-sidebar").getByPlaceholder("Search", { exact: true }).inputValue()).toBe("preview");
    await page.getByRole("button", { name: "agent:main", exact: true }).click();
    await page.getByRole("button", { name: "room:room", exact: true }).click();
    await page.getByPlaceholder("Search or agent: or room:").fill("");
    await shown(page, "merged-live");
    await shown(page, "prefix-only");
    await shown(page, "will vanish");

    expect(page.url()).toBe(selectedURL);
    for (let i = 0; i < 7; i++) await shown(page, `partial preview ${i}`);
    await hidden(page, "partial preview 7");
    expect(await page.locator("aside").getByRole("button", { name: "Unsettle", exact: true }).count()).toBe(0);
    await page.getByPlaceholder("Search or agent: or room:").fill("partial preview is:settled");
    await shown(page, "partial preview 7");
    await shown(page, "partial preview 0");
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
    listResponse!.close();
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
    await shown(page, "will vanish");
    await page.getByRole("link").filter({ hasText: "will vanish" }).click();
    // Leave Alice's HTTP stream alive despite the App abort so its tail really arrives after Bob.
    await page.evaluate(() => {
      window.fetch = new Proxy(window.fetch, {
        apply(target, receiver, [input, init]) {
          return Reflect.apply(target, receiver, [input, String(input).includes("/api/ListSessions") ? { ...init, signal: undefined } : init]);
        },
      });
    });
    const ownerStarted = Promise.withResolvers<void>();
    const ownerRequest = page.waitForRequest("**/api/ListSessions*");
    ctrl.yieldBatches = async function* () {
      yield batch([row("gone", "alice held preview")], { owner: "alice" });
      ownerStarted.resolve();
      await ownerTail.promise;
      yield* complete([row("gone", "late alice preview")], "alice")();
    };
    await ownerStarted.promise;
    await shown(page, "alice held preview");
    const oldRequest = await ownerRequest;
    const oldResponse = page.waitForEvent("requestfailed", { predicate: (request: unknown) => request === oldRequest });
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
    await page.reload();
    await shown(page, "bob preview");

    const protocolReload = page.waitForEvent("load");
    ctrl.protocol = "test-protocol-2";
    await protocolReload;
    await hidden(page, "Refreshing");
    await hidden(page, "bob preview");
    ctrl.yieldBatches = complete([row("gone", "new protocol preview")], "bob");
    blocked.resolve();
    await shown(page, "new protocol preview");

    for (const rejection of ["mismatch", "unauthorized"] as const) {
      identityHold = Promise.withResolvers();
      ctrl.yieldBatches = async function* () {
        if (rejection === "unauthorized") throw new RPCError("denied", 16);
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
          if (String(input).includes("/api/Identity")) {
            const response = new Response();
            response.json = async () => ({ username: "bob" });
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
        const sent = storagePage.waitForResponse("**/api/Prompt");
        await storagePage.getByRole("button", { name: "Send" }).click();
        await storagePage.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
        await sent;
        expect(ctrl.prompt.some((item) => item.includes(`send with ${failure} storage`))).toBe(true);
      } finally {
        await storagePage.close();
      }
    }
    const runningPage = await context.newPage();
    for (const width of [1280, 390]) {
      ctrl.yieldBatches = complete([{ ...row("running-chat", "latest user message"), running: true }], "bob");
      await runningPage.setViewportSize({ width, height: 844 });
      await runningPage.goto(origin);
      if (width === 390) await runningPage.getByRole("button", { name: "Sessions", exact: true }).click();
      const sidebar = width === 390 ? runningPage.getByRole("dialog", { name: "Sessions" }) : runningPage.locator("#session-sidebar");
      const indicator = sidebar.getByRole("img", { name: "Turn running", exact: true });
      await indicator.waitFor();
      await sidebar.evaluate(async (element: HTMLElement) => {
        await Promise.all(element.getAnimations().map((animation) => animation.finished));
      });
      const link = sidebar.getByRole("link").filter({ hasText: "latest user message" });
      await link.click({ trial: true }); // Measure after the mobile sheet finishes moving.
      await sidebar.evaluate(async () => { await Promise.all(document.getAnimations().filter((animation) => animation instanceof CSSTransition).map((animation) => animation.finished)); });
      const before = await link.boundingBox();
      ctrl.yieldBatches = complete([{ ...row("running-chat", "latest assistant reply"), running: false }], "bob");
      await sidebar.getByText("latest assistant reply", { exact: true }).waitFor();
      expect(await indicator.count()).toBe(0);
      expect(await sidebar.getByRole("link").filter({ hasText: "latest assistant reply" }).boundingBox()).toEqual(before);
    }
    await runningPage.close();
    ctrl.settledRows = [
      row("slack-thread:C:active", "matching active"),
      { ...row("slack-thread:C:settled", "matching settled"), settled: true },
      { ...row("slack-thread:C:other", "matching other agent"), agent: "other", settled: true },
      { ...row("web-session:settled", "matching other room"), settled: true },
    ];
    ctrl.yieldBatches = complete(ctrl.settledRows);
    const settledPage = await context.newPage();
    await settledPage.goto(origin);
    const settledSidebar = settledPage.locator("#session-sidebar");
    await shown(settledSidebar, "matching active");
    await hidden(settledSidebar, "matching settled");
    await settledSidebar.getByPlaceholder("Search or agent: or room:").fill("agent:main");
    await settledPage.keyboard.press("Enter");
    await settledSidebar.getByPlaceholder("Search", { exact: true }).fill("room:room");
    await settledPage.keyboard.press("Enter");
    await settledSidebar.getByPlaceholder("Search", { exact: true }).fill("IS:SETTLED matching");
    await shown(settledSidebar, "matching active");
    await shown(settledSidebar, "matching settled");
    await hidden(settledSidebar, "matching other agent");
    await hidden(settledSidebar, "matching other room");
    await settledSidebar.getByPlaceholder("Search", { exact: true }).fill("");
    const footer = settledPage.locator("footer");
    await footer.getByRole("link", { name: "Settled", exact: true }).click();
    await settledPage.waitForURL("**/settled");
    const settledMain = settledPage.locator("main");
    await shown(settledMain, "matching settled");
    await hidden(settledMain, "matching active");
    await settledMain.getByPlaceholder("Search or agent: or room:").fill("no-such-chat");
    await shown(settledMain, "No matches");
    await settledMain.getByPlaceholder("Search or agent: or room:").fill("matching settled");
    await settledMain.getByRole("button", { name: "Unsettle", exact: true }).click();
    await shown(settledMain, "No matches");
    expect(ctrl.settleCalls).toEqual([{ id: "slack-thread:C:settled", settled: false }]);
    await shown(settledSidebar, "matching settled");
    ctrl.yieldBatches = complete(ctrl.settledRows.map((session) => ({ ...session, settled: false })));
    await settledMain.getByPlaceholder("Search or agent: or room:").fill("");
    await shown(settledMain, "No settled chats");
    await footer.getByRole("link", { name: "Settled", exact: true }).click();
    await settledPage.waitForURL(`${origin}/`);
    for (const name of ["Settled", "Cron", "Agents", "Skills", "Config"]) {
      const tab = footer.getByRole("link", { name, exact: true });
      await tab.click();
      await settledPage.waitForURL(`${origin}/${name.toLowerCase()}`);
      expect(await tab.getAttribute("href")).toBe("/");
      if (name === "Config") {
        await shown(settledPage.locator("main"), "web.auto_settle_after");
        await shown(settledPage.locator("main"), "Configured user");
        await shown(settledPage.locator("main"), "connected@example.com");
        await shown(settledPage.locator("main"), "1h30m0s");
      }
      await tab.click();
      await settledPage.waitForURL(`${origin}/`);
      expect(await tab.getAttribute("href")).toBe(`/${name.toLowerCase()}`);
    }
    for (const width of [1280, 390]) {
      await settledPage.setViewportSize({ width, height: 844 });
      if (width === 390) await settledPage.getByRole("button", { name: "Sessions", exact: true }).click();
      const sidebar = width === 390 ? settledPage.getByRole("dialog", { name: "Sessions" }) : settledSidebar;
      const chat = sidebar.getByRole("link").filter({ hasText: "matching active" });
      const chatPath = await chat.getAttribute("href");
      await chat.click();
      await settledPage.waitForURL(`${origin}${chatPath}`);
      if (width === 390) await sidebar.waitFor({ state: "hidden" });
      await settledPage.getByPlaceholder("Message or $command").fill("keep this chat draft");
      for (const name of ["Settled", "Cron", "Agents", "Skills", "Config"]) {
        const tab = footer.getByRole("link", { name, exact: true });
        await tab.click();
        await settledPage.waitForURL(`${origin}/${name.toLowerCase()}`);
        expect(await tab.getAttribute("href")).toBe(chatPath);
        expect(await tab.getAttribute("aria-current")).toBe("true");
        expect(await footer.locator('[aria-current="true"]').count()).toBe(1);
        expect(await tab.evaluate((element: HTMLElement) => element.classList.contains("bg-primary"))).toBe(true);
        const heading = settledMain.getByRole("heading", { name, exact: true });
        const mainBox = (await settledMain.boundingBox())!;
        const headerBox = (await heading.locator("..").boundingBox())!;
        const headingBox = (await heading.boundingBox())!;
        expect(headerBox.x).toBe(mainBox.x + Math.max(0, (mainBox.width - 768) / 2) + 16);
        const close = settledMain.getByRole("button", { name: "Close", exact: true });
        if (width === 390) {
          const toggle = settledPage.getByRole("button", { name: "Sessions", exact: true });
          const toggleBox = (await toggle.boundingBox())!;
          expect(toggleBox.x).toBe(headerBox.x);
          expect(headingBox.x).toBe(toggleBox.x + toggleBox.width + 8);
          expect(toggleBox.y).toBe(headingBox.y);
          expect(await close.count()).toBe(0);
          await toggle.click();
          await settledPage.getByRole("dialog", { name: "Sessions", exact: true }).waitFor();
          await settledPage.keyboard.press("Escape");
          await settledPage.getByRole("dialog", { name: "Sessions", exact: true }).waitFor({ state: "hidden" });
        } else {
          expect(headingBox.x).toBe(headerBox.x);
          await close.waitFor();
        }
        expect(headingBox.y).toBe(mainBox.y + 16);
        expect(headingBox.height).toBe(28);
        expect(await heading.evaluate((element: HTMLElement) => getComputedStyle(element).padding)).toBe("0px");
        if (name === "Settled" || name === "Cron") {
          const search = settledMain.getByRole("textbox").first();
          const searchBox = (await (name === "Settled" ? search.locator("..") : search).boundingBox())!;
          expect(searchBox.x).toBe(headerBox.x);
          expect(searchBox.y).toBe(headingBox.y + headingBox.height + 24);
        }
        if (width === 1280 && name === "Agents") await settledPage.keyboard.press("Escape");
        else if (width === 1280 && name === "Skills") await close.click();
        else await tab.click();
        await settledPage.waitForURL(`${origin}${chatPath}`);
        expect(await footer.locator('[aria-current="true"]').count()).toBe(0);
      }
      await footer.getByRole("link", { name: "Cron", exact: true }).click();
      await settledPage.waitForURL(`${origin}/cron`);
      await footer.getByRole("link", { name: "Skills", exact: true }).click();
      await settledPage.waitForURL(`${origin}/skills`);
      await footer.getByRole("link", { name: "Skills", exact: true }).click();
      await settledPage.waitForURL(`${origin}${chatPath}`);
      if (width === 390) await settledPage.keyboard.press("Escape");
      expect(await settledPage.getByPlaceholder("Message or $command").inputValue()).toBe("keep this chat draft");
      const newChat = footer.getByRole("button", { name: "New session", exact: true });
      const groupBox = await newChat.locator("..").boundingBox();
      if (width === 390) {
        const scrollBox = (await newChat.locator("../..").boundingBox())!;
        expect(Math.abs(scrollBox.x + scrollBox.width / 2 - width / 2)).toBeLessThan(1);
        expect(scrollBox.x + scrollBox.width).toBeLessThanOrEqual(width);
      } else {
        expect(Math.abs(groupBox!.x + groupBox!.width / 2 - width / 2)).toBeLessThan(1);
      }
      expect(await newChat.locator("..").getByRole("link").count()).toBe(5);
      const creations = ctrl.createdAgents.length;
      await newChat.click();
      await settledPage.waitForURL(`${origin}/`);
      expect(ctrl.createdAgents.length).toBe(creations);
      if (width === 390) await settledPage.keyboard.press("Escape");
      expect(await settledPage.getByPlaceholder("Message a new session").inputValue()).toBe("");
      await settledPage.getByRole("combobox", { name: "Choose agent" }).click();
      await settledPage.getByRole("option", { name: "other gpt", exact: true }).click();
      await settledPage.getByPlaceholder("Message a new session").fill("fresh draft");
      await footer.getByRole("link", { name: "Config", exact: true }).click();
      await settledPage.waitForURL(`${origin}/config`);
      await footer.getByRole("link", { name: "Config", exact: true }).click();
      await settledPage.waitForURL(`${origin}/`);
      if (width === 390) await settledPage.keyboard.press("Escape");
      expect(await settledPage.getByPlaceholder("Message a new session").inputValue()).toBe("fresh draft");
      await settledPage.getByRole("combobox", { name: "Choose agent" }).filter({ hasText: "other" }).waitFor();
      await newChat.click();
      if (width === 390) await settledPage.keyboard.press("Escape");
      expect(await settledPage.getByPlaceholder("Message a new session").inputValue()).toBe("");
      await settledPage.getByRole("combobox", { name: "Choose agent" }).filter({ hasText: "main" }).waitFor();
      expect(ctrl.createdAgents.length).toBe(creations);
    }
    await settledPage.close();
    const cronPage = await context.newPage();
    for (const width of [1280, 390]) {
      await cronPage.setViewportSize({ width, height: 844 });
      await cronPage.goto(`${origin}/cron`);
      const daily = cronPage.getByRole("region", { name: "daily", exact: true });
      await daily.locator("summary").first().waitFor();
      const search = cronPage.getByRole("textbox", { name: "Search", exact: true });
      const searchBounds = await search.boundingBox();
      const gridBounds = await cronPage.getByRole("button", { name: "Preview latest run of daily", exact: true }).boundingBox();
      expect(searchBounds.y + searchBounds.height).toBeLessThan(gridBounds.y);
      for (const text of ["DAILY", "prompt", "24h", "planner", "#ops", "cron/daily.md"]) {
        await search.fill(text);
        await cronPage.getByRole("region", { name: "retired", exact: true }).waitFor({ state: "hidden" });
        expect(await daily.isVisible()).toBe(true);
        expect(await cronPage.getByRole("button", { name: /^Preview latest run of / }).count()).toBe(1);
      }
      await search.fill("Sep 1, 2026");
      await daily.waitFor({ state: "hidden" });
      expect(await cronPage.getByRole("region", { name: "retired", exact: true }).isVisible()).toBe(true);
      await search.fill("no delivered chat");
      await cronPage.getByRole("region", { name: "silent", exact: true }).waitFor();
      expect(await cronPage.getByRole("button", { name: /^Preview latest run of / }).count()).toBe(1);
      await search.fill("no-such-cron");
      await cronPage.getByRole("region", { name: "silent", exact: true }).waitFor({ state: "hidden" });
      expect(await cronPage.getByRole("button", { name: /^Preview latest run of / }).count()).toBe(0);
      await search.fill("");
      await daily.locator("summary").first().waitFor();
      expect(await daily.locator("details[open]").count()).toBe(0);
      expect(await daily.getByRole("link").isVisible()).toBe(false);
      await daily.locator("summary").first().click();
      expect(await daily.getByRole("link").getAttribute("href")).toBe("/s/Y3Jvbi1jaGF0");
      expect(await daily.getByText("Daily prompt", { exact: true }).isVisible()).toBe(false);
      expect(await daily.locator("dl").isVisible()).toBe(false);
      await daily.getByText("Definition", { exact: true }).click();
      expect(await daily.getByText("Daily prompt", { exact: true }).isVisible()).toBe(true);
      expect(await daily.locator("dt").allTextContents()).toEqual(["Schedule", "Agent", "Channel", "Source"]);
      expect(await daily.locator("dd").allTextContents()).toEqual(["24h", "planner", "#ops", "cron/daily.md"]);
      expect(await daily.locator("dl").isVisible()).toBe(true);
      await daily.getByText("Definition", { exact: true }).click();
      expect(await daily.locator("dl").isVisible()).toBe(false);
      await daily.locator("summary").first().click();
      expect(await daily.getByRole("link").isVisible()).toBe(false);
      expect(await daily.getByText("Daily prompt", { exact: true }).isVisible()).toBe(false);
      await daily.locator("summary").first().press("Enter");
      expect(await daily.getByRole("link").isVisible()).toBe(true);
      expect(await daily.getByText("Daily prompt", { exact: true }).isVisible()).toBe(false);
      expect(await cronPage.locator('[aria-label^="Expected daily at "]').count()).toBe(2);
      const expected = cronPage.getByRole("button", { name: /^Expected daily at / }).first();
      await expected.hover();
      await cronPage.getByRole("tooltip").waitFor();
      expect(await cronPage.getByRole("tooltip").textContent()).toMatch(/^Expected daily at .* UTC$/);
      await expected.press("Escape");
      expect(await cronPage.getByRole("tooltip").count()).toBe(0);
      await expected.blur();
      await expected.focus();
      expect(await cronPage.getByRole("tooltip").isVisible()).toBe(true);
      await expected.press("Escape");
      await expected.tap();
      expect(await cronPage.getByRole("tooltip").isVisible()).toBe(true);
      const bounds = await cronPage.getByRole("tooltip").boundingBox();
      expect(bounds.x).toBeGreaterThanOrEqual(0);
      expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
      const silent = cronPage.getByRole("button", { name: /^Preview silent at / });
      await silent.tap();
      const silentPreview = cronPage.getByRole("region", { name: "Run preview", exact: true });
      expect(await silentPreview.getByText("No delivered chat · Run traces", { exact: true }).isVisible()).toBe(true);
      await silentPreview.getByText("Silent run trace", { exact: true }).waitFor();
      expect(cronHistory.includes("cron:silent-source")).toBe(true);
      const opensBefore = cronOpens.length;
      await silentPreview.getByRole("button", { name: /Open chat/ }).click();
      await cronPage.waitForURL(`**/s/${btoa("web:cron:silent-source").replaceAll("=", "")}`);
      await cronPage.locator("#transcript-scroll").getByText("Silent run trace", { exact: true }).waitFor();
      expect(cronOpens.slice(opensBefore)).toEqual(["cron:silent-source"]);
      await cronPage.getByPlaceholder("Message or $command").fill("Continue this run");
      await cronPage.getByRole("combobox", { name: "Choose agent" }).filter({ hasText: "other" }).waitFor();
      await cronPage.getByRole("combobox", { name: "Choose agent" }).click();
      await cronPage.getByRole("option").first().waitFor();
      expect(await cronPage.getByRole("option").count()).toBe(1);
      await cronPage.keyboard.press("Escape");
      ctrl.promptStarted = Promise.withResolvers<void>();
      await cronPage.getByRole("button", { name: "Send", exact: true }).click();
      await ctrl.promptStarted.promise;
      expect(ctrl.prompt).toContain("web:cron:silent-source:Continue this run");
      await cronPage.goto(`${origin}/cron`);
      const silentRuns = cronPage.getByRole("region", { name: "silent", exact: true });
      await silentRuns.locator("summary").first().click();
      await silentRuns.getByRole("button", { name: /Open chat/ }).click();
      await cronPage.waitForURL(`**/s/${btoa("web:cron:silent-source").replaceAll("=", "")}`);
      await cronPage.locator("#transcript-scroll").getByText("Silent run trace", { exact: true }).waitFor();
      expect(cronOpens.slice(opensBefore)).toEqual(["cron:silent-source", "cron:silent-source"]);
      expect(cronRunCalls).toBe(0);
      await cronPage.goto(`${origin}/cron`);
      expect(await cronPage.getByRole("button", { name: "Preview latest run of retired", exact: true }).isEnabled()).toBe(true);
      await cronPage.getByRole("region", { name: "retired", exact: true }).locator("summary").click();
      expect(await cronPage.getByRole("region", { name: "retired", exact: true }).getByRole("link").getAttribute("href")).toBe("/s/b2xkLWNoYXQ");
      expect(await cronPage.getByRole("button", { name: "Preview latest run of unused", exact: true }).isDisabled()).toBe(true);
      await cronPage.getByRole("button", { name: "Preview latest run of daily", exact: true }).click({ position: { x: 5, y: 5 } });
      const preview = cronPage.getByRole("region", { name: "Run preview", exact: true });
      await preview.getByText("Daily run report", { exact: true }).waitFor();
      expect(cronHistory.at(-1)).toBe("cron-source");
      await preview.getByRole("link").click();
      await cronPage.waitForURL("**/s/Y3Jvbi1jaGF0");
    }
    await cronPage.goto(`${origin}/cron`);
    const runGroup = cronPage.getByRole("region", { name: "daily", exact: true });
    const runButton = runGroup.getByRole("button", { name: "Run daily", exact: true });
    await runButton.click();
    const confirmation = cronPage.getByRole("dialog", { name: "Run daily?", exact: true });
    await confirmation.waitFor();
    await confirmation.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(cronRunCalls).toBe(0);
    expect(await runGroup.locator("details[open]").count()).toBe(0);
    await runButton.click();
    await confirmation.getByRole("button", { name: "Run", exact: true }).click();
    await cronPage.getByRole("button", { name: "Running daily", exact: true }).waitFor();
    expect(await cronPage.getByRole("button", { name: "Running daily", exact: true }).isDisabled()).toBe(true);
    expect(await runGroup.locator("details[open]").count()).toBe(0);
    cronHold.resolve("new-cron-chat");
    await cronPage.waitForURL("**/s/bmV3LWNyb24tY2hhdA");
    await cronPage.close();

    ctrl.history = Array.from({ length: 20 }, (_, i) => [
      { role: "user", text: `Prompt ${i + 1}` },
      { role: "thinking", text: `Trace ${i + 1}\n` + "Working through the task.\n".repeat(40) },
      { role: "assistant", text: `Reply ${i + 1}` },
      { role: "assistant", text: "Repeated reply" },
      { role: "assistant", text: "Repeated reply" },
    ].map((item) => ({ ...item, turnId: "", complete: true, snapshot: false }))).flat();
    const transcriptPage = await browser.newPage();
    for (const width of [1280, 390]) {
      transcriptStream = Promise.withResolvers();
      // Keep the preview list overflowing even with the preset's compact spacing.
      await transcriptPage.setViewportSize({ width, height: 600 });
      await transcriptPage.goto(`${origin}/s/${Buffer.from(`jump-${width}`).toString("base64url")}`);
      const rail = transcriptPage.getByRole("navigation", { name: "Conversation turns" });
      await rail.waitFor();
      expect(await rail.getByRole("button").count()).toBe(20);
      expect(await transcriptPage.getByText("Repeated reply", { exact: true }).count()).toBe(40);
      const scroll = transcriptPage.locator("#transcript-scroll");
      expect(await scroll.evaluate((el: HTMLElement) => el.offsetWidth - el.clientWidth)).toBeGreaterThan(0);
      const preview = rail.getByText("1. Prompt 1", { exact: true });
      const marker = rail.getByRole("button", { name: "Turn 1: Prompt 1", exact: true });
      const markerPosition = await marker.boundingBox();
      expect(await preview.isVisible()).toBe(false);
      await rail.hover();
      await preview.waitFor();
      expect(await preview.isVisible()).toBe(true);
      expect(await marker.boundingBox()).toEqual(markerPosition);
      const previewBox = rail.getByRole("group", { name: "Message previews" });
      const box = await previewBox.boundingBox();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(width);
      const position = await scroll.evaluate((el: HTMLElement) => el.scrollTop);
      await previewBox.hover();
      await transcriptPage.mouse.wheel(0, 400);
      await transcriptPage.waitForFunction(() => document.querySelector('nav[aria-label="Conversation turns"] > div')!.scrollTop > 0);
      expect(await scroll.evaluate((el: HTMLElement) => el.scrollTop)).toBe(position);
      await preview.click();
      await transcriptPage.waitForFunction(() => document.querySelector("#transcript-scroll")!.scrollTop < 50);
      expect(await transcriptPage.getByRole("region", { name: "Turn 1", exact: true }).evaluate((el: HTMLElement) => el === document.activeElement)).toBe(true);
      (await transcriptStream.promise).enqueue(`data: ${JSON.stringify({ role: "assistant", text: `Live reply ${width}`, turnId: "live", complete: false, snapshot: false })}\n\n`);
      await transcriptPage.getByText(`Live reply ${width}`, { exact: true }).waitFor({ state: "attached" });
      expect(await scroll.evaluate((el: HTMLElement) => el.scrollTop)).toBeLessThan(50);
      await transcriptPage.getByRole("button", { name: "Scroll to latest", exact: true }).click();
      await transcriptPage.waitForFunction(() => {
        const el = document.querySelector("#transcript-scroll")!;
        return el.scrollHeight - el.clientHeight - el.scrollTop < 2;
      });
      (await transcriptStream.promise).enqueue(`data: ${JSON.stringify({ role: "assistant", text: `\nFollowing latest ${width}\n` + "More streamed text.\n".repeat(40), turnId: "live", complete: false, snapshot: false })}\n\n`);
      await transcriptPage.getByText(`Following latest ${width}`, { exact: false }).waitFor({ state: "attached" });
      await transcriptPage.waitForFunction(() => {
        const el = document.querySelector("#transcript-scroll")!;
        return el.scrollHeight - el.clientHeight - el.scrollTop < 2;
      });
      await rail.getByRole("button", { name: "Turn 20: Prompt 20", exact: true }).focus();
      await transcriptPage.keyboard.press("Enter");
      const last = transcriptPage.getByRole("region", { name: "Turn 20", exact: true });
      expect(await last.evaluate((el: HTMLElement) => el === document.activeElement)).toBe(true);
      await transcriptPage.mouse.move(0, 0);
      await previewBox.waitFor({ state: "hidden" });
      await last.locator("summary").click();
      await rail.getByRole("button", { name: "Turn 1: Prompt 1", exact: true }).click();
      await transcriptPage.waitForFunction(() => document.querySelector("#transcript-scroll")!.scrollTop < 50);
    }
    const codeText = "  echo '<literal>'\n" + "  long output\n".repeat(100) + "x".repeat(400) + "\n";
    ctrl.history = [
      { role: "user", text: "Show the output" },
      { role: "tool", text: "bash\n{}", toolName: "bash", toolCallId: "bounded" },
      { role: "tool", text: codeText, toolCallId: "bounded" },
      { role: "assistant", text: "Before\n```sh\n" + codeText + "```\nAfter" },
    ].map((item) => ({ ...item, turnId: "", complete: true, snapshot: false }));
    await transcriptPage.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    for (const width of [1280, 390]) {
      transcriptStream = Promise.withResolvers();
      await transcriptPage.setViewportSize({ width, height: 600 });
      await transcriptPage.goto(`${origin}/s/${Buffer.from(`code-${width}`).toString("base64url")}`);
      await transcriptPage.locator('pre[aria-label="sh"]').waitFor();
      expect(await transcriptPage.locator("#transcript-scroll pre").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["bash", "sh"]);
      for (const label of ["bash", "sh"]) {
        const panel = transcriptPage.locator(`pre[aria-label="${label}"]`);
        await panel.waitFor();
        expect(await panel.textContent()).toBe(label === "bash" ? `Arguments\n{}\n\nResult\n${codeText}` : codeText);
        expect(await panel.evaluate((el: HTMLElement) => el.clientHeight)).toBeLessThanOrEqual(256);
        expect(await panel.evaluate((el: HTMLElement) => el.scrollHeight > el.clientHeight && el.scrollWidth > el.clientWidth)).toBe(true);
        await panel.focus();
        await transcriptPage.keyboard.press("ArrowDown");
        await transcriptPage.waitForFunction((label: string) => document.querySelector(`pre[aria-label="${label}"]`)!.scrollTop > 0, label);
        await transcriptPage.getByRole("button", { name: `Expand ${label}`, exact: true }).click();
        const overlay = transcriptPage.getByRole("dialog", { name: label, exact: true });
        await overlay.waitFor();
        expect(await overlay.locator("pre").textContent()).toBe(label === "bash" ? `Arguments\n{}\n\nResult\n${codeText}` : codeText);
        expect(await overlay.locator("pre").evaluate((el: HTMLElement) => el.clientHeight)).toBeGreaterThan(256);
        expect(await transcriptPage.locator(`#transcript-scroll pre[aria-label="${label}"]`).evaluate((el: HTMLElement) => el.clientHeight)).toBeLessThanOrEqual(256);
        await overlay.getByRole("button", { name: `Copy ${label}`, exact: true }).click();
        expect(await transcriptPage.evaluate(() => navigator.clipboard.readText())).toBe(label === "bash" ? `Arguments\n{}\n\nResult\n${codeText}` : codeText);
        await transcriptPage.keyboard.press("Escape");
        await overlay.waitFor({ state: "hidden" });
        expect(await transcriptPage.getByRole("button", { name: `Expand ${label}`, exact: true }).evaluate((el: HTMLElement) => el === document.activeElement)).toBe(true);
      }
      expect(await transcriptPage.locator("#transcript-scroll").evaluate((el: HTMLElement) => el.scrollWidth <= el.clientWidth)).toBe(true);
      const copy = transcriptPage.getByRole("button", { name: "Copy sh", exact: true });
      await copy.click();
      await transcriptPage.getByRole("status").filter({ hasText: "Copied" }).first().waitFor();
      expect(await transcriptPage.evaluate(() => navigator.clipboard.readText())).toBe(codeText);
      // Exercise the plain-HTTP path with the secure-context API unavailable.
      await transcriptPage.evaluate(() => Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true }));
      await transcriptPage.getByRole("button", { name: "Copy bash", exact: true }).click();
      await transcriptPage.waitForFunction(() => Array.from(document.querySelectorAll('[role="status"]')).filter((el) => el.textContent === "Copied").length === 2);
      await transcriptPage.evaluate(() => Reflect.deleteProperty(navigator, "clipboard"));
      expect(await transcriptPage.evaluate(() => navigator.clipboard.readText())).toBe(`Arguments\n{}\n\nResult\n${codeText}`);
      const tool = transcriptPage.locator("details").filter({ has: transcriptPage.locator('pre[aria-label="bash"]') }).last();
      await tool.getByRole("button", { name: "Collapse tool ↑", exact: true }).click();
      expect(await tool.locator("pre").isVisible()).toBe(false);
      await tool.locator("summary").click();
      expect(await tool.locator("pre").isVisible()).toBe(true);
      // A partial streamed fence is rendered as code before its closing fence arrives.
      (await transcriptStream.promise).enqueue(`data: ${JSON.stringify({ role: "assistant", text: "~~~py\n  streamed", turnId: "fenced-stream", complete: false, snapshot: true })}\n\n`);
      await transcriptPage.locator('pre[aria-label="py"]').waitFor();
      expect(await transcriptPage.locator('pre[aria-label="py"]').textContent()).toBe("  streamed");
    }
    await transcriptPage.close();
    ctrl.history = [];
    for (const width of [1280, 390]) {
      ctrl.settledRows = [row("recent", "Most recent message"), row("named", "Original preview"), { ...row("settled-pin", "Settled pin preview"), pinned: true, settled: true }];
      ctrl.yieldBatches = complete(ctrl.settledRows);
      const detailsPage = await browser.newPage({ viewport: { width, height: 844 } });
      await detailsPage.goto(`${origin}/s/${Buffer.from("named").toString("base64url")}`);
      await detailsPage.getByRole("combobox", { name: "Choose agent" }).waitFor();
      if (width === 390) {
        expect(await detailsPage.locator("main").getByRole("button", { name: "Pin session", exact: true }).count()).toBe(0);
        expect(await detailsPage.locator("main").getByRole("button", { name: "Name session", exact: true }).count()).toBe(0);
      }
      expect(await detailsPage.getByRole("button", { name: "Steer", exact: true }).isDisabled()).toBe(true);
      if (width === 390) {
        await detailsPage.setViewportSize({ width: 320, height: 568 });
        const selector = detailsPage.getByRole("combobox", { name: "Choose agent" });
        expect((await selector.boundingBox())!.height).toBeGreaterThanOrEqual(44);
        const selectorBox = (await selector.boundingBox())!;
        const sendBox = (await detailsPage.getByRole("button", { name: "Send", exact: true }).boundingBox())!;
        expect(selectorBox.width).toBeLessThanOrEqual(144);
        expect(Math.abs(selectorBox.y - sendBox.y)).toBeLessThan(2);
        await selector.click();
        await detailsPage.getByRole("option", { name: "main gpt", exact: true }).waitFor();
        expect((await detailsPage.locator('[data-slot="select-content"]').boundingBox())!.width).toBeGreaterThan(selectorBox.width);
        expect(await detailsPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        await detailsPage.screenshot({ path: path.join(process.env.TMPDIR!, "chat-agent-320.png") });
        await detailsPage.keyboard.press("Escape");
        await detailsPage.screenshot({ path: path.join(process.env.TMPDIR!, "chat-composer-320.png") });
        await detailsPage.setViewportSize({ width, height: 844 });
      }
      if (width === 390) await detailsPage.getByRole("button", { name: "Sessions", exact: true }).click();
      const sidebar = width === 390 ? detailsPage.getByRole("dialog", { name: "Sessions", exact: true }) : detailsPage.locator("#session-sidebar");
      const namedRow = sidebar.locator("li").filter({ hasText: "Original preview" });
      const rowBox = await namedRow.boundingBox();
      const linkBox = await namedRow.locator("a").boundingBox();
      expect(linkBox!.width).toBe(rowBox!.width);
      await namedRow.hover();
      await namedRow.getByRole("button", { name: "Name session", exact: true }).click();
      const rowDialog = detailsPage.getByRole("dialog", { name: "Name session", exact: true });
      await rowDialog.getByLabel("Session name").fill("Sidebar rename");
      await rowDialog.getByRole("button", { name: "Save", exact: true }).click();
      await rowDialog.waitFor({ state: "hidden" });
      const renamedRow = sidebar.locator("li").filter({ hasText: "Sidebar rename" });
      await renamedRow.hover();
      await renamedRow.getByRole("button", { name: "Name session", exact: true }).click();
      await rowDialog.getByLabel("Session name").fill("");
      await rowDialog.getByRole("button", { name: "Save", exact: true }).click();
      await rowDialog.waitFor({ state: "hidden" });
      await namedRow.hover();
      await namedRow.getByRole("button", { name: "Pin session", exact: true }).click();
      await namedRow.getByRole("button", { name: "Unpin session", exact: true }).waitFor();
      expect(await sidebar.locator('li a[href^="/s/"]').first().innerText()).toContain("Original preview");
      expect(new URL(detailsPage.url()).pathname).toBe(`/s/${Buffer.from("named").toString("base64url")}`);
      const search = sidebar.getByPlaceholder("Search or agent: or room:");
      await search.fill("IS:PINNED");
      await sidebar.getByText("Settled pin preview", { exact: true }).waitFor();
      expect(await sidebar.locator('li a[href^="/s/"]').count()).toBe(2);
      await search.fill("is:pinned is:settled Settled");
      expect(await sidebar.locator('li a[href^="/s/"]').count()).toBe(1);
      await search.fill("");
      if (width === 390) {
        await detailsPage.keyboard.press("Escape");
        await detailsPage.close();
        continue;
      }
      await detailsPage.locator("main").getByRole("button", { name: "Unpin session", exact: true }).click();
      await detailsPage.locator("main").getByRole("button", { name: "Pin session", exact: true }).waitFor();
      await detailsPage.locator("main").getByRole("button", { name: "Name session", exact: true }).click();
      const dialog = detailsPage.getByRole("dialog", { name: "Name session", exact: true });
      await dialog.getByLabel("Session name").fill("  Release notes  ");
      ctrl.updateError = true;
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.getByRole("alert").waitFor();
      expect(await dialog.getByLabel("Session name").inputValue()).toBe("  Release notes  ");
      ctrl.updateError = false;
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.waitFor({ state: "hidden" });
      await detailsPage.locator("main").getByRole("button", { name: "Name session", exact: true }).waitFor();
      await detailsPage.reload();
      await detailsPage.locator("main").getByRole("button", { name: "Pin session", exact: true }).waitFor();
      await sidebar.getByText("Release notes", { exact: true }).waitFor();
      await search.fill("Release notes");
      expect(await sidebar.locator('li a[href^="/s/"]').count()).toBe(1);
      await search.fill("Original preview");
      expect(await sidebar.locator('li a[href^="/s/"]').count()).toBe(1);
      await search.fill("");
      const pinBox = await detailsPage.locator("main").getByRole("button", { name: "Pin session", exact: true }).boundingBox();
      const sendBox = await detailsPage.getByRole("button", { name: "Send", exact: true }).boundingBox();
      expect(pinBox!.x + pinBox!.width).toBeLessThanOrEqual(sendBox!.x);
      expect(Math.abs(pinBox!.y - sendBox!.y)).toBeLessThan(8);
      const rename = detailsPage.locator("main").getByRole("button", { name: "Name session", exact: true });
      expect(await rename.innerText()).toBe("");
      await detailsPage.locator("textarea").hover();
      await rename.hover();
      await detailsPage.locator('[data-slot="tooltip-content"]').filter({ hasText: "Rename session" }).waitFor();
      expect(await detailsPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await detailsPage.locator("main").getByRole("button", { name: "Name session", exact: true }).click();
      expect(await dialog.getByLabel("Session name").inputValue()).toBe("Release notes");
      await dialog.getByLabel("Session name").fill("");
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.waitFor({ state: "hidden" });
      await sidebar.getByText("Original preview", { exact: true }).waitFor();
      await detailsPage.screenshot({ path: path.join(process.env.TMPDIR!, `session-details-${width}.png`) });
      await detailsPage.close();
    }
    ctrl.history = [{ role: "tool", text: "Delivered image", toolCallId: "result", turnId: "", complete: true, snapshot: false, attachments: [image] }];
    transcriptStream = Promise.withResolvers();
    const attachmentPage = await browser.newPage();
    // Direct HTTP deployments have getRandomValues, but not secure-context randomUUID.
    await attachmentPage.addInitScript(() => Object.defineProperty(crypto, "randomUUID", { value: undefined }));
    await attachmentPage.goto(`${origin}/s/${Buffer.from("visible-files").toString("base64url")}`);
    await attachmentPage.getByRole("img", { name: "history.png", exact: true }).waitFor();
    await attachmentPage.getByText(`${image.size} bytes · Original unverified`, { exact: true }).waitFor();
    await attachmentPage.waitForFunction(() => (document.querySelector('img[alt="history.png"]') as HTMLImageElement)?.naturalWidth === 1);
    const downloadLink = attachmentPage.getByRole("link", { name: "Download history.png", exact: true });
    expect(await downloadLink.getAttribute("href")).toContain("conversationId=visible-files");
    const downloading = attachmentPage.waitForEvent("download");
    await downloadLink.click();
    expect((await downloading).suggestedFilename()).toBe("history.png");
    const live = { ...image, id: "live-image", name: "live.png", originalUnverified: false };
    ctrl.history.push(
      { role: "user", text: "queued message now consumed", turnId: "", complete: true, snapshot: false },
      { role: "assistant", text: "reply to queued message", turnId: "", complete: true, snapshot: false },
      { role: "user", text: "steering message consumed", turnId: "", complete: true, snapshot: false },
      { role: "assistant", text: "", turnId: "", complete: true, snapshot: false, attachments: [live] },
    );
    for (const [index, message] of ctrl.history.slice(1).entries()) {
      (await transcriptStream.promise).enqueue(`data: ${JSON.stringify({ ...message, turnId: `files-${index}`, ...(message.role === "user" ? { messageId: `consumed-${index}` } : {}) })}\n\n`);
    }
    await attachmentPage.getByRole("region", { name: "Messages", exact: true }).getByText("queued message now consumed", { exact: true }).waitFor();
    const reconciled = await attachmentPage.getByRole("region", { name: "Messages", exact: true }).innerText();
    expect(reconciled.indexOf("queued message now consumed")).toBeLessThan(reconciled.indexOf("reply to queued message"));
    expect(reconciled.indexOf("reply to queued message")).toBeLessThan(reconciled.indexOf("steering message consumed"));
    await attachmentPage.getByRole("img", { name: "live.png", exact: true }).waitFor();
    expect(downloadConversations.every((id) => id === "visible-files")).toBe(true);
    const picker = attachmentPage.waitForEvent("filechooser");
    await attachmentPage.getByRole("button", { name: "Add files", exact: true }).click();
    await (await picker).setFiles([{ name: "keep.bin", mimeType: "application/octet-stream", buffer: Buffer.from([0, 255, 1, 2]) }, { name: "remove.txt", mimeType: "text/plain", buffer: Buffer.from("remove") }]);
    await attachmentPage.getByRole("button", { name: "Remove remove.txt", exact: true }).click();
    expect(await attachmentPage.getByRole("button", { name: "Remove remove.txt", exact: true }).count()).toBe(0);
    await attachmentPage.locator("textarea").evaluate((element: HTMLElement) => {
      const dataTransfer = new DataTransfer();
      dataTransfer.items.add(new File([new Uint8Array([3, 4, 0, 255])], "drop.bin"));
      element.dispatchEvent(new DragEvent("drop", { bubbles: true, dataTransfer }));
    });
    await attachmentPage.getByRole("button", { name: "Remove drop.bin", exact: true }).waitFor();
    await attachmentPage.locator("textarea").fill("  exact file draft\n");
    await attachmentPage.locator("footer").getByRole("link", { name: "Config", exact: true }).click();
    await attachmentPage.locator("footer").getByRole("link", { name: "Config", exact: true }).click();
    await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    await attachmentPage.locator("#session-sidebar").getByRole("link").filter({ hasText: "Most recent message" }).click();
    await attachmentPage.waitForURL(`**/s/${Buffer.from("recent").toString("base64url")}`);
    expect(await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).count()).toBe(0);
    await attachmentPage.goBack();
    await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    const beforeFailedUpload = ctrl.prompt.length;
    await attachmentPage.getByRole("combobox", { name: "Choose agent" }).click();
    await attachmentPage.getByRole("option", { name: "other gpt" }).click();
    ctrl.uploadError = "drop.bin";
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await attachmentPage.getByText("Upload failed: drop.bin", { exact: true }).waitFor();
    expect(ctrl.prompt.length).toBe(beforeFailedUpload);
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    expect(await attachmentPage.getByRole("button", { name: /^Remove .*\.bin$/ }).count()).toBe(2);
    expect(await attachmentPage.locator("textarea").isEnabled()).toBe(true);
    expect(await attachmentPage.getByRole("combobox", { name: "Choose agent" }).innerText()).toBe("other");
    expect(["keep.bin", "drop.bin"].map((name) => [...uploadedFiles.findLast(({ meta }) => meta.name === name)!.data])).toEqual([[0, 255, 1, 2], [3, 4, 0, 255]]);
    ctrl.uploadError = "";
    ctrl.promptError = true;
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await attachmentPage.getByText("Send failed; retry", { exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    expect(await attachmentPage.getByRole("button", { name: /^Remove .*\.bin$/ }).count()).toBe(2);
    expect(ctrl.attachmentPrompts.at(-1)).toMatchObject({ id: "visible-files", text: "  exact file draft\n", delivery: "STEER" });
    expect(ctrl.attachmentPrompts.at(-1)?.attachmentIds?.map((id) => uploadedFiles.find(({ meta }) => meta.id === id)?.meta.name)).toEqual(["keep.bin", "drop.bin"]);
    expect(uploadedFiles.slice(-2).map(({ data }) => [...data])).toEqual([[0, 255, 1, 2], [3, 4, 0, 255]]);
    ctrl.promptError = false;
    ctrl.holdUpload = true;
    ctrl.uploadStarted = Promise.withResolvers();
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await ctrl.uploadStarted.promise;
    const beforeUploadDispatch = ctrl.attachmentPrompts.length;
    await attachmentPage.locator("textarea").dispatchEvent("keydown", { key: "Enter" });
    await attachmentPage.locator("#session-sidebar").getByRole("link").filter({ hasText: "Most recent message" }).click();
    await attachmentPage.goBack();
    await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    expect(ctrl.attachmentPrompts.length).toBe(beforeUploadDispatch);
    uploadHold.resolve();
    ctrl.holdUpload = false;
    await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).waitFor({ state: "hidden" });
    expect(ctrl.attachmentPrompts.at(-1)).toMatchObject({ text: "  exact file draft\n", delivery: "STEER" });
    const sentAttachments = ctrl.attachmentPrompts.at(-1)!.attachmentIds!.map((id) => uploadedFiles.find(({ meta }) => meta.id === id)!.meta);
    expect(sentAttachments.map(({ name }) => name)).toEqual(["keep.bin", "drop.bin"]);
    expect(sentAttachments.map(({ id }) => [...uploadedFiles.find(({ meta }) => meta.id === id)!.data])).toEqual([[0, 255, 1, 2], [3, 4, 0, 255]]);
    await attachmentPage.getByRole("link", { name: "Download keep.bin", exact: true }).waitFor();
    await attachmentPage.getByRole("link", { name: "Download drop.bin", exact: true }).waitFor();
    expect(await attachmentPage.getByRole("link", { name: "Download keep.bin", exact: true }).evaluate(async (link: HTMLAnchorElement) => [...new Uint8Array(await (await fetch(link.href)).arrayBuffer())])).toEqual([0, 255, 1, 2]);
    expect(await attachmentPage.getByText("exact file draft", { exact: true }).count()).toBe(1);
    ctrl.history.push({ role: "user", text: "  exact file draft\n", turnId: "", complete: true, snapshot: false, attachments: sentAttachments });
    await attachmentPage.locator("#session-sidebar").getByRole("link").filter({ hasText: "Most recent message" }).click();
    await attachmentPage.goBack();
    await attachmentPage.getByRole("link", { name: "Download keep.bin", exact: true }).waitFor();
    expect(await attachmentPage.getByText("exact file draft", { exact: true }).count()).toBe(1);
    // The active turn makes Enter queue; the in-flight lock prevents a second upload/send.
    await attachmentPage.locator('input[type="file"]').setInputFiles({ name: "queued.bin", mimeType: "application/octet-stream", buffer: Buffer.from("queue") });
    promptHold = Promise.withResolvers();
    ctrl.holdPrompt = true;
    ctrl.promptStarted = Promise.withResolvers();
    const sends = ctrl.attachmentPrompts.length;
    await attachmentPage.locator("textarea").press("Enter");
    await ctrl.promptStarted.promise;
    await attachmentPage.locator("textarea").dispatchEvent("keydown", { key: "Enter" });
    expect(ctrl.attachmentPrompts.length).toBe(sends + 1);
    expect(ctrl.attachmentPrompts.at(-1)?.delivery).toBe("QUEUE");
    expect(await attachmentPage.getByRole("region", { name: "Messages", exact: true }).getByRole("link", { name: "Download queued.bin", exact: true }).count()).toBe(0);
    expect(Buffer.from(uploadedFiles.at(-1)!.data).toString()).toBe("queue");
    expect(await attachmentPage.locator("textarea").isEnabled()).toBe(true);
    expect(await attachmentPage.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
    await attachmentPage.locator('input[type="file"]').setInputFiles({ name: "steer.bin", mimeType: "application/octet-stream", buffer: Buffer.from("steer") });
    ctrl.promptStarted = Promise.withResolvers();
    await attachmentPage.locator("textarea").press("Control+Enter");
    await ctrl.promptStarted.promise;
    expect(ctrl.attachmentPrompts.at(-1)?.delivery).toBe("STEER");
    await attachmentPage.getByRole("region", { name: "Pending steers", exact: true }).getByRole("link", { name: "Download steer.bin", exact: true }).waitFor();
    expect(await attachmentPage.getByRole("region", { name: "Messages", exact: true }).getByRole("link", { name: "Download steer.bin", exact: true }).count()).toBe(0);
    const stopped = attachmentPage.waitForResponse((response: { url: () => string; request: () => { postDataJSON: () => { text: string } } }) => response.url().endsWith("/api/Prompt") && response.request().postDataJSON().text === "$stop");
    await attachmentPage.getByRole("button", { name: "Stop", exact: true }).click();
    await stopped;
    await attachmentPage.locator("textarea").fill("newer attachment draft");
    promptHold.resolve("");
    ctrl.holdPrompt = false;
    await attachmentPage.getByRole("button", { name: "Remove queued.bin", exact: true }).waitFor({ state: "hidden" });
    await attachmentPage.getByRole("button", { name: "Remove steer.bin", exact: true }).waitFor({ state: "hidden" });
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("newer attachment draft");
    await attachmentPage.locator('input[type="file"]').setInputFiles({ name: "reset.bin", mimeType: "application/octet-stream", buffer: Buffer.from("reset") });
    await attachmentPage.getByRole("button", { name: "New session", exact: true }).click();
    await attachmentPage.waitForURL(`${origin}/`);
    expect(await attachmentPage.getByRole("button", { name: "Remove reset.bin", exact: true }).count()).toBe(0);
    await attachmentPage.locator('input[type="file"]').setInputFiles({ name: "home.bin", mimeType: "application/octet-stream", buffer: Buffer.from("home") });
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await attachmentPage.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
    await attachmentPage.getByRole("button", { name: "Remove home.bin", exact: true }).waitFor({ state: "hidden" });
    expect(ctrl.createdAgents.at(-1)).toBe("main");
    expect(ctrl.attachmentPrompts.at(-1)).toMatchObject({ id: "web-session:new", text: "", delivery: "STEER" });
    await attachmentPage.close();

    ctrl.history = [];
    ctrl.holdPrompt = true;
    ctrl.holdUpload = true;
    uploadHold = Promise.withResolvers();
    ctrl.uploadStarted = Promise.withResolvers();
    promptHold = Promise.withResolvers();
    ctrl.promptStarted = Promise.withResolvers();
    const photoPage = await browser.newPage();
    await photoPage.goto(`${origin}/s/${Buffer.from("photo-session").toString("base64url")}`);
    await photoPage.locator("textarea").fill("What's this? \n");
    await photoPage.locator('input[type="file"]').setInputFiles({ name: "photo.png", mimeType: "image/png", buffer: imageBytes });
    await photoPage.getByRole("button", { name: "Send", exact: true }).click();
    await ctrl.uploadStarted.promise;
    expect(await photoPage.getByRole("region", { name: "Turn 1", exact: true }).getByText("What's this?", { exact: true }).textContent()).toBe("What's this? \n");
    await photoPage.waitForFunction(() => (document.querySelector('img[alt="photo.png"]') as HTMLImageElement)?.naturalWidth === 1, null, { timeout: 3000 });
    uploadHold.resolve();
    ctrl.holdUpload = false;
    await ctrl.promptStarted.promise;
    const sentPhoto = uploadedFiles.find(({ meta }) => meta.id === ctrl.attachmentPrompts.at(-1)!.attachmentIds![0])!.meta;
    expect(await photoPage.getByText("What's this?", { exact: true }).textContent()).toBe("What's this? \n");
    await photoPage.waitForFunction(() => (document.querySelector('img[alt="photo.png"]') as HTMLImageElement)?.naturalWidth === 1, null, { timeout: 3000 });
    const localPhotoURL = await photoPage.getByRole("img", { name: "photo.png", exact: true }).getAttribute("src");
    expect(localPhotoURL.startsWith("blob:")).toBe(true);
    ctrl.history = [{ role: "user", text: "What's this? \n", turnId: "", complete: true, snapshot: false, attachments: [sentPhoto] }];
    await photoPage.locator("#session-sidebar").getByRole("link").filter({ hasText: "Most recent message" }).click();
    await photoPage.goBack();
    await photoPage.waitForFunction(() => (document.querySelector('img[alt="photo.png"]') as HTMLImageElement)?.src.includes("/api/DownloadAttachment"));
    expect(await photoPage.getByText("What's this?", { exact: true }).count()).toBe(1);
    expect(await photoPage.getByRole("img", { name: "photo.png", exact: true }).count()).toBe(1);
    expect(await photoPage.evaluate(async (url: string) => { try { await fetch(url); return true; } catch { return false; } }, localPhotoURL)).toBe(false);
    promptHold.resolve("");
    ctrl.holdPrompt = false;
    await photoPage.reload();
    await photoPage.waitForFunction(() => (document.querySelector('img[alt="photo.png"]') as HTMLImageElement)?.naturalWidth === 1);
    expect(await photoPage.getByText("What's this?", { exact: true }).textContent()).toBe("What's this? \n");
    expect(await photoPage.getByRole("img", { name: "photo.png", exact: true }).count()).toBe(1);
    await photoPage.locator("textarea").fill("What's this? \n");
    await photoPage.locator('input[type="file"]').setInputFiles({ name: "photo.png", mimeType: "image/png", buffer: imageBytes });
    ctrl.promptStarted = Promise.withResolvers();
    await photoPage.getByRole("button", { name: "Send", exact: true }).click();
    await ctrl.promptStarted.promise;
    const secondPhoto = uploadedFiles.find(({ meta }) => meta.id === ctrl.attachmentPrompts.at(-1)!.attachmentIds![0])!.meta;
    ctrl.history.push({ ...ctrl.history[0], attachments: [secondPhoto] });
    await photoPage.locator("#session-sidebar").getByRole("link").filter({ hasText: "Most recent message" }).click();
    await photoPage.goBack();
    await photoPage.waitForFunction(() => { const images = [...document.querySelectorAll<HTMLImageElement>('img[alt="photo.png"]')]; return images.length === 2 && images.every((image) => image.src.includes("/api/DownloadAttachment") && image.naturalWidth === 1); });
    expect(await photoPage.getByText("What's this?", { exact: true }).allTextContents()).toEqual(["What's this? \n", "What's this? \n"]);
    expect(await photoPage.getByRole("img", { name: "photo.png", exact: true }).count()).toBe(2);
    await photoPage.close();
    const desktopPage = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await desktopPage.goto(origin);
    const desktopAgent = desktopPage.locator("footer").getByRole("link", { name: "Agents", exact: true });
    await desktopAgent.waitFor();
    expect((await desktopAgent.boundingBox())!.width).toBe(32);
    expect((await desktopAgent.locator("svg").boundingBox())!.width).toBe(24);
    for (const name of ["Hide sidebar", "New session", "Settled", "Cron", "Agents", "Skills", "Config"]) {
      const control = desktopPage.locator("footer").getByRole(name === "Hide sidebar" || name === "New session" ? "button" : "link", { name, exact: true });
      await control.hover();
      await desktopPage.locator('[data-slot="tooltip-content"]').filter({ hasText: name }).waitFor();
      await desktopPage.mouse.move(0, 0);
      await desktopPage.locator('[data-slot="tooltip-content"]').filter({ hasText: name }).waitFor({ state: "hidden" });
    }
    await desktopPage.keyboard.press("Tab");
    await desktopAgent.focus();
    await desktopPage.locator('[data-slot="tooltip-content"]').filter({ hasText: "Agents" }).waitFor();
    await desktopPage.keyboard.press("Escape");
    await desktopPage.getByRole("button", { name: "Hide bottom navigation" }).click();
    expect(await desktopAgent.isVisible()).toBe(false);
    await desktopPage.getByRole("button", { name: "Show bottom navigation" }).click();
    await desktopPage.setViewportSize({ width: 320, height: 640 });
    await desktopPage.locator("footer").getByRole("link", { name: "Config", exact: true }).click();
    await desktopPage.waitForURL("**/config");
    expect(await desktopPage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await desktopPage.close();
  } finally {
    blocked.resolve();
    identityHold.resolve();
    promptHold.resolve("");
    uploadHold.resolve();
    createHold.resolve("web-session:abandoned");
    wireTail.resolve();
    ownerTail.resolve();
    await browser.close();
    server.stop(true);
  }
}, 180_000);
