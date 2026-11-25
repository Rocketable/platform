import { expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";
import type { Session, TranscriptEvent } from "./types";
import { timelineLevels } from "./timeline-detail";
import { appScenario, chromium, dist, playwright, screenshots } from "./session-list-app_test";

test("saved sidebar snapshots isolate owners and commit atomically", async () => {
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
      const rows = [{ id: "one", title: "kept", preview: "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", agent: "agent", tags: ["customer", "Needs review"], running: true }];
      await storage.saveCompleteSessions("a", "v1", { rows, version: "v1" }, await storage.loadSnapshotGeneration("a", "v1"));
      const put = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function (...args) {
        const request = put.apply(this, args);
        request.addEventListener("success", () => this.transaction.abort());
        return request;
      };
      let rejected = false;
      try {
        await storage.saveCompleteSessions("a", "v1", { rows: [{ id: "replacement" }], version: "v2" }, await storage.loadSnapshotGeneration("a", "v1"));
      } catch {
        rejected = true;
      } finally {
        IDBObjectStore.prototype.put = put;
      }
      return {
        rejected,
        generation: await storage.loadSnapshotGeneration("a", "v1"),
        ownerMiss: await storage.loadSavedSessions("b", "v1") === undefined,
        protocolMiss: await storage.loadSavedSessions("a", "v2") === undefined,
      };
    });
    expect(initial).toEqual({ rejected: true, generation: 0, ownerMiss: true, protocolMiss: true });
    await page.reload();
    const restored = await page.evaluate(async () => {
      const path = "/session-list.js";
      const storage = await import(path);
      const { rows, version } = await storage.loadSavedSessions("a", "v1");
      await storage.saveCompleteSessions("a", "v1", { rows: [], version: "v3" }, await storage.loadSnapshotGeneration("a", "v1"));
      return { version, running: rows[0].running, fullPreview: rows[0].preview === "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", empty: await storage.loadSavedSessions("a", "v1") };
    });
    // The saved listing keeps rows as listed, so a poll since its version leaves unchanged rows right.
    expect(restored).toEqual({
      version: "v1",
      fullPreview: true,
      running: true,
      empty: { rows: [], version: "v3" },
    });
  } finally {
    server.stop(true);
    if (browser) await browser.close();
  }
}, 30_000);

test("pending saves and owner switches cannot leak owners or overwrite newer snapshots", async () => {
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
    await page.goto(`http://127.0.0.1:${server.port}`);
    const result = await page.evaluate(async () => {
      const modulePath = "/storage.js";
      const storage = await import(modulePath);
      const saved = (rows: object[]) => ({ rows, version: "v" });
      const rows = [{ id: "synthetic", preview: "old synthetic preview", agent: "agent", title: "room" }];
      const activeGeneration = await storage.loadSnapshotGeneration("owner", "protocol");
      await storage.saveCompleteSessions("owner", "protocol", saved(rows), activeGeneration);
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

      const switchGate = holdOpen();
      const pendingOwnerSave = storage.saveCompleteSessions("owner", "protocol", saved([{ ...rows[0], preview: "switched-away preview" }]), activeGeneration);
      await switchGate.held;
      storage.invalidatePendingSaves("owner", "protocol");
      await storage.saveCompleteSessions("other", "protocol", saved([{ id: "other", preview: "other preview" }]), await storage.loadSnapshotGeneration("other", "protocol"));
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
      const activeSave = storage.saveCompleteSessions("owner", "protocol", saved([{ id: "synthetic", preview: "uncommitted owner preview" }]), activeGeneration).catch(() => {});
      await writing.promise;
      storage.invalidatePendingSaves("owner", "protocol");
      holdTransaction = false;
      await activeSave;
      const olderGate = holdOpen();
      const olderSave = storage.saveCompleteSessions("ordering", "protocol", saved([{ id: "row", preview: "older" }]), 0);
      await olderGate.held;
      await storage.saveCompleteSessions("ordering", "protocol", saved([{ id: "row", preview: "newer" }]), 0);
      olderGate.release();
      await olderSave;
      const newest = await storage.loadSavedSessions("ordering", "protocol");
      return {
        newest: newest.rows,
        other: (await storage.loadSavedSessions("other", "protocol")).rows,
        ownerAfterSwitch: (await storage.loadSavedSessions("owner", "protocol")).rows,
      };
    });
    expect(result).toEqual({
      newest: [{ id: "row", preview: "newer" }],
      other: [{ id: "other", preview: "other preview" }],
      ownerAfterSwitch: [{ id: "synthetic", preview: "old synthetic preview", agent: "agent", title: "room" }],
    });
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);


test("sidebar listing objects stay separate from older clients' row arrays", async () => {
  const { chromium: engine } = await import(playwright!);
  const listings: string[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/blank") return new Response("<!doctype html><title>Cache setup</title>", { headers: { "Content-Type": "text/html" } });
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "alice" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "cache-format" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/ListSessions") {
      listings.push(url.searchParams.get("since") ?? "");
      return new Response(`data: ${JSON.stringify({ owner: "alice", sessions: [{ id: "fresh", name: "Fresh list" }], upstreamSuccess: true, version: "fresh" })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    return unpacked(path.join(dist, url.pathname.startsWith("/assets/") ? url.pathname : "index.html"));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const context = await browser.newContext();
    const older = await context.newPage();
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const origin = `http://127.0.0.1:${server.port}`;
    await older.goto(`${origin}/blank`);
    await older.evaluate(async () => {
      const request = indexedDB.open("rocketclaw-session-list", 1);
      request.onupgradeneeded = () => request.result.createObjectStore("snapshots");
      const db = await new Promise<IDBDatabase>((resolve, reject) => { request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error); });
      const tx = db.transaction("snapshots", "readwrite");
      tx.objectStore("snapshots").put([{ id: "old", name: "Old cached list" }], ["alice", "cache-format"]);
      await new Promise<void>((resolve, reject) => { tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error); });
      // Keep an older page's connection open while the current app starts.
      Object.defineProperty(window, "oldSnapshotDB", { value: db });
    });
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions and commands", exact: true }).click({ timeout: 5000 });
    const palette = page.getByRole("dialog");
    await palette.getByText("Fresh list", { exact: true }).waitFor({ timeout: 5000 });
    expect(await palette.getByText("Old cached list", { exact: true }).count()).toBe(0);
    expect(listings[0]).toBe("");
    expect(errors).toEqual([]);
    expect(await older.evaluate(async () => {
      const db = (window as unknown as { oldSnapshotDB: IDBDatabase }).oldSnapshotDB;
      const tx = db.transaction("snapshots", "readonly");
      const request = tx.objectStore("snapshots").get(["alice", "cache-format"]);
      await new Promise<void>((resolve, reject) => { tx.oncomplete = () => resolve(); tx.onabort = () => reject(tx.error); });
      return request.result;
    })).toEqual([{ id: "old", name: "Old cached list" }]);
  } finally { await browser.close(); server.stop(true); }
}, 30_000);

for (const [platform, modifier, alternate] of [["MacIntel", "⌘", "Option"], ["Linux x86_64", "Ctrl", "Alt"]]) {
  test(`actual App command hints match existing bindings on ${platform}`, async () => {
    const { chromium: engine } = await import(playwright!);
    const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
      const url = new URL(request.url);
      if (!url.pathname.startsWith("/api/")) return unpacked(path.join(dist, url.pathname.startsWith("/assets/") ? url.pathname : "index.html"));
      if (url.pathname === "/api/ListSessions") return new Response('data: {"sessions":[],"owner":"tester","upstreamSuccess":true}\n\nevent: complete\ndata: {}\n\n', { headers: { "Content-Type": "text/event-stream" } });
      if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
      if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "command-hints" });
      if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
      if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
      if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      return Response.json({});
    } });
    const browser = await engine.launch({ executablePath: chromium, headless: true });
    try {
      const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
      await page.addInitScript((value: string) => Object.defineProperty(navigator, "platform", { value }), platform);
      await page.goto(`http://127.0.0.1:${server.port}`);
      const draft = page.getByPlaceholder("Message a new session");
      await draft.fill("draft cleared only by invocation");
      await page.keyboard.press(modifier === "⌘" ? "Meta+Shift+p" : "Control+Shift+p");
      const dialog = page.getByRole("dialog", { name: "Run command", exact: true });
      const create = dialog.getByRole("button", { name: "Sessions: New", exact: true });
      await create.waitFor();
      expect(await create.locator("kbd").allTextContents()).toEqual([modifier, alternate, "N"]);
      expect(await dialog.getByRole("button", { name: "Sessions: Search", exact: true }).locator("kbd").count()).toBe(0);
      // Each command with a working shortcut shows it on its row; the sidebar toggle exists only while Cmd/Ctrl+B works.
      const keys = async () => Object.fromEntries(await dialog.locator("li > button").filter({ has: page.locator("kbd") }).evaluateAll((buttons: Element[]) => buttons.map((button) => [button.firstElementChild!.firstElementChild!.textContent, [...button.querySelectorAll("kbd")].map((key) => key.textContent)])));
      expect(await dialog.getByRole("complementary").count()).toBe(0); // No shortcut footer.
      for (const width of [1280, 320]) {
        await page.setViewportSize({ width, height: 800 });
        await dialog.getByRole("button", { name: "Tabs: Toggle sidebar", exact: true }).waitFor({ state: width === 1280 ? "visible" : "detached" });
        expect(await keys()).toEqual({ "Sessions: New": [modifier, alternate, "N"], "Tabs: Next": [modifier, "]"], "Tabs: Previous": [modifier, "["], "Tabs: Show Recent": [modifier, "1"], "Tabs: Show Spaces": [modifier, "2"], "Tabs: Show Parked": [modifier, "3"], ...(width === 1280 ? { "Tabs: Toggle sidebar": [modifier, "B"] } : {}) });
        await dialog.evaluate((node: HTMLElement) => Promise.all(node.getAnimations({ subtree: true }).map((animation) => animation.finished)));
        expect(await dialog.evaluate((node: HTMLElement) => node.scrollWidth <= node.clientWidth && [...node.querySelectorAll("kbd")].every((key) => {
          const bounds = key.getBoundingClientRect(), parent = node.getBoundingClientRect();
          return bounds.width > 0 && bounds.left >= parent.left && bounds.right <= parent.right;
        }))).toBe(true);
        await dialog.screenshot({ path: path.join(screenshots, `command-hints-${platform.split(" ")[0]}-${width}.png`) });
      }
      await dialog.getByPlaceholder("Type a command", { exact: true }).fill("sessions: new");
      await create.waitFor();
      expect(await dialog.locator("li > button").count()).toBe(1);
      await dialog.getByPlaceholder("Type a command", { exact: true }).press("ArrowDown");
      await dialog.getByPlaceholder("Type a command", { exact: true }).press("Enter");
      await dialog.waitFor({ state: "hidden" });
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(await draft.inputValue()).toBe("");
      await page.keyboard.press(modifier === "⌘" ? "Meta+p" : "Control+p");
      const sessions = page.getByRole("dialog", { name: "Go to session or command", exact: true });
      await sessions.getByPlaceholder("Search sessions and commands", { exact: true }).fill("sessions: new");
      await sessions.getByRole("button", { name: "Sessions: New", exact: true }).waitFor();
      expect(await sessions.getByRole("button", { name: "Sessions: New", exact: true }).locator("kbd").allTextContents()).toEqual([modifier, alternate, "N"]);
      // The read-only Shortcuts page lists every key the palette shows, plus the message box's and Escape.
      await sessions.getByPlaceholder("Search sessions and commands", { exact: true }).fill("keyboard");
      await sessions.getByRole("button", { name: "Help: Keyboard Shortcuts", exact: true }).click();
      await page.waitForURL("**/shortcuts");
      const shortcutList = page.getByRole("list", { name: "Keyboard shortcuts" });
      const shortcutKeys: string[][] = await shortcutList.getByRole("listitem").evaluateAll((items: Element[]) => items.map((item) => [...item.querySelectorAll("kbd")].map((key) => key.textContent!)));
      expect(shortcutKeys).toEqual([
        [modifier, "P"], [modifier, "Shift", "P"], [modifier, alternate, "N"], [modifier, "["], [modifier, "]"], [modifier, alternate, "T"], [modifier, "Shift", "A"], [modifier, "Shift", "S"], [modifier, "B"], [modifier, "1"], [modifier, "2"], [modifier, "3"], [modifier, "Shift", "B"], ["Escape"], [modifier, "Enter"], [modifier, alternate, "Enter"]]);
      // Chromium keeps new and closed tabs, windows and tab cycling for itself and never hands these keys to a page
      // (kReservedCommandIds, chrome/browser/ui/accelerator_table.cc). Headless test browsers do hand them over, so a test
      // pressing one passes while the real key does nothing; RocketClaw adds Alt instead, as in mod+Alt+N.
      const reserved = ["T", "W", "N", "Tab"].flatMap((key) => [`${modifier}+${key}`, `${modifier}+Shift+${key}`]);
      expect(shortcutKeys.map((keys) => keys.join("+")).filter((keys) => reserved.includes(keys))).toEqual([]);
      await page.getByRole("dialog").waitFor({ state: "detached" });
      await page.keyboard.press("Escape"); // A page tab closes with Escape.
      await page.waitForURL((url: URL) => url.pathname !== "/shortcuts");
      for (const hasTouch of [true, false]) {
        const shortPage = await browser.newPage({ viewport: { width: 320, height: 256 }, hasTouch, isMobile: hasTouch });
        await shortPage.addInitScript((value: string) => Object.defineProperty(navigator, "platform", { value }), platform);
        await shortPage.goto(`http://127.0.0.1:${server.port}`);
        await shortPage.getByPlaceholder("Message a new session").fill("Short-screen draft");
        await shortPage.keyboard.press(modifier === "⌘" ? "Meta+Shift+p" : "Control+Shift+p");
        const shortMenu = shortPage.getByRole("dialog", { name: "Run command", exact: true });
        await shortMenu.getByRole("button", { name: "Sessions: New", exact: true }).waitFor();
        expect(await shortMenu.getByRole("button", { name: "Sessions: New", exact: true }).locator("kbd").allTextContents()).toEqual([modifier, alternate, "N"]);
        expect(await shortMenu.locator("ul").evaluate((el: HTMLElement) => el.clientHeight >= 44)).toBe(true);
        await shortMenu.getByPlaceholder("Type a command", { exact: true }).fill("sessions: new");
        await shortMenu.getByRole("button", { name: "Sessions: New", exact: true }).click();
        await shortMenu.waitFor({ state: "hidden" });
        await shortPage.waitForFunction(() => document.querySelector("textarea")?.value === "");
        await shortPage.close();
      }
    } finally {
      await browser.close();
      server.stop(true);
    }
  }, 30_000);
}

test("room autocomplete keeps selections and full labels on each search surface", async () => {
  const { chromium: engine } = await import(playwright!);
  const rows: Session[] = [
    { id: "slack-thread:C11:1", kind: "SLACK_THREAD", title: "amber", preview: "Amber result", agent: "main" },
    { id: "slack-thread:C12:1", kind: "SLACK_THREAD", title: "Blue garden", preview: "Blue result", agent: "main" },
    { id: "slack-thread:C13:1", kind: "SLACK_THREAD", title: "cobalt", preview: "Cobalt result", agent: "other" },
  ];
  const queries: string[] = [];
  const longRooms = ["synthetic-observatory-room-with-a-long-shared-prefix-for-wrapping-amber", "synthetic observatory room with a long shared prefix for wrapping blue"];
  const results: Record<string, number[]> = {
    "room:amber": [0], 'room:amber room:"Blue garden"': [0, 1], 'room:"Blue garden"': [1],
    'room:"Blue garden" agent:main': [1], 'room:"Blue garden" agent:other': [], 'room:"Blue garden" agent:main agent:other': [1],
    '😀 room:"Blue garden" room:amber': [],
  };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) return unpacked(path.join(dist, url.pathname.startsWith("/assets/") ? url.pathname : "index.html"));
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "room-search" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }, { name: "other" }] });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: [] });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ jobs: [] });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      queries.push(query);
      const terms = ["room:amber", 'room:"Blue garden"', "agent:main", "agent:other", ...longRooms.map((room) => `room:${JSON.stringify(room)}`)].flatMap((text) => {
        const start = query.indexOf(text);
        return start < 0 ? [] : [{ key: text.split(":")[0], text, start, end: start + text.length }];
      }).sort((a, b) => a.start - b.start);
      return Response.json({ terms, text: "", needle: "", matches: (results[query] ?? []).map((index) => ({ conversationId: rows[index].id, field: "Preview", text: rows[index].preview })), messages: [], mentionIds: [] });
    }
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
    return Response.json({});
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const origin = `http://127.0.0.1:${server.port}`;
    for (const palette of [true, false]) {
      const page = await browser.newPage();
      await page.goto(origin + (palette ? "/" : "/search"));
      await page.locator("footer").waitFor();
      if (palette) await page.keyboard.press("Control+p");
      const surface = palette ? page.getByRole("dialog", { name: "Go to session or command", exact: true }) : page.getByRole("tabpanel", { name: "Search", exact: true });
      const input = surface.getByRole("textbox", { name: palette ? "Search sessions and commands" : "Search sessions", exact: true });
      await input.fill("room:am");
      await surface.getByRole("button", { name: "amber", exact: true }).click();
      await surface.getByRole("button", { name: "room:amber", exact: true }).waitFor();
      await input.fill("room:Blue");
      await input.press("Tab");
      await surface.getByRole("button", { name: 'room:"Blue garden"', exact: true }).waitFor();
      expect(await surface.getByRole("button", { name: "room:amber", exact: true }).count()).toBe(1);
      expect(queries).toContain('room:amber room:"Blue garden"');
      const found = palette ? surface.locator('[data-slot="session-title"]') : page.getByLabel("Search results").locator("h2");
      expect(await found.allTextContents()).toEqual(palette ? ["Amber result", "Blue result"] : ["Amber result(1)", "Blue result(1)"]);
      const removed = page.waitForResponse((response: { url: () => string; request: () => { postDataJSON: () => { query: string } } }) => response.url().endsWith("/api/SearchSessions") && response.request().postDataJSON().query === 'room:"Blue garden"');
      await surface.getByRole("button", { name: "room:amber", exact: true }).click();
      await removed;
      await found.filter({ hasText: "Amber result" }).waitFor({ state: "detached" });
      expect(await found.allTextContents()).toEqual(palette ? ["Blue result"] : ["Blue result(1)"]);
      expect(await surface.getByRole("button", { name: "room:amber", exact: true }).count()).toBe(0);
      expect(await surface.getByRole("button", { name: 'room:"Blue garden"', exact: true }).count()).toBe(1);
      await input.fill("agent:ma");
      await input.press("Enter");
      await surface.getByRole("button", { name: "agent:main", exact: true }).waitFor();
      await input.fill("agent:oth");
      await input.press("Enter");
      await surface.getByRole("button", { name: "agent:other", exact: true }).waitFor();
      expect(await surface.getByRole("button", { name: "agent:main", exact: true }).count()).toBe(1);
      expect(await found.allTextContents()).toEqual(palette ? ["Blue result"] : ["Blue result(1)"]);
      expect(await surface.getByRole("button", { name: 'room:"Blue garden"', exact: true }).count()).toBe(1);
      await surface.getByRole("button", { name: "agent:other", exact: true }).click();
      await surface.getByRole("button", { name: "agent:main", exact: true }).click();
      await surface.getByRole("button", { name: 'room:"Blue garden"', exact: true }).click();
      await input.fill(longRooms.map((room) => `room:${JSON.stringify(room)} `).join(""));
      // The search spinner animates until the response; let it settle before waiting on animations.
      await surface.getByRole("img", { name: "Searching", exact: true }).waitFor({ state: "detached" });
      for (const width of [1280, 320]) {
        await page.setViewportSize({ width, height: 800 });
        await page.evaluate(() => document.fonts.ready);
        await surface.evaluate((node: HTMLElement) => Promise.all(node.getAnimations({ subtree: true }).map((animation) => animation.finished)));
        for (const room of longRooms) {
          const label = `room:${JSON.stringify(room)}`;
          const pill = surface.getByRole("button", { name: label, exact: true });
          await pill.waitFor();
          expect(await pill.innerText()).toBe(label);
          expect(await pill.evaluate((button: HTMLElement) => {
            const text = button.firstElementChild as HTMLElement;
            const bounds = button.getBoundingClientRect();
            const range = document.createRange();
            range.selectNodeContents(text);
            return {
              ellipsis: getComputedStyle(text).textOverflow === "ellipsis",
              fitsText: text.scrollWidth <= text.clientWidth,
              fitsViewport: bounds.left >= 0 && bounds.right <= innerWidth,
              containsText: [...range.getClientRects()].every((rect) => rect.left >= bounds.left && rect.right <= bounds.right && rect.top >= bounds.top && rect.bottom <= bounds.bottom),
            };
          })).toEqual({ ellipsis: false, fitsText: true, fitsViewport: true, containsText: true });
        }
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        await page.screenshot({ path: path.join(screenshots, `room-pills-${palette ? "palette" : "search"}-${width}.png`) });
      }
      if (!palette) {
        const query = '😀 room:"Blue garden" room:amber ';
        await page.goto(`${origin}/search?q=${encodeURIComponent(query)}`);
        await surface.getByRole("button", { name: "room:amber", exact: true }).waitFor();
        expect(await input.inputValue()).toBe("😀 ");
        await surface.getByRole("button", { name: 'room:"Blue garden"', exact: true }).click();
        await page.waitForFunction(() => new URLSearchParams(location.search).get("q") === "😀 room:amber ");
      }
      await page.close();
    }
  } finally { server.stop(true); await browser.close(); }
}, 30_000);

test("actual App renders independent public lifecycle and reconnect replacements at narrow widths", async () => {
  // Build current sources in memory; leave the parent's shared embedded assets untouched.
  const build = await Bun.build({ entrypoints: ["./src/main.tsx"], target: "browser", define: { "process.env.NODE_ENV": '"production"' } });
  expect(build.success).toBe(true);
  const script = await build.outputs[0].text();
  const html = (await unpacked(path.join(dist, "index.html")).text()).replace(/<script type="module" src="[^"]+"><\/script>/, '<script type="module" src="/current.js"></script>');
  const { chromium: engine } = await import(playwright!);
  let running = true;
  let revision = 0;
  let stream = Promise.withResolvers<ReadableStreamDefaultController>();
  const event = (itemId: string, role: string, text: string, extra: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ entryKey: "producer-turn", itemId, role, text, inputId: "", turnId: "turn", complete: true, origin: "canonical", agent: "main", model: "root/model", ...extra });
  const calls = ["A", "B", "C"].map((id) => event(id, "tool", "task\n{}", { toolName: "task", toolCallId: id, parentId: "producer/turn/response", state: "working", complete: false, agent: "canonical-child", model: "child/model" }));
  let messages = [event("input", "user", "Public progress question", { inputId: "consumed" }), ...calls, event("text", "assistant", "Held partial suffix", { state: "working", complete: false })];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/current.js") return new Response(script, { headers: { "Content-Type": "text/javascript" } });
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { stream.resolve(controller); controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "public", agent: "main", title: "Public progress" }], owner: "alice", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      if (url.pathname.startsWith("/assets/")) return unpacked(path.join(dist, url.pathname));
      return new Response(html, { headers: { "Content-Type": "text/html" } });
    }
    const input = await request.json() as { revision?: string; text?: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "public-progress-test" });
      case "/api/Identity": return Response.json({ username: "alice" });
      case "/api/History": return Response.json({ messages: input.revision === String(revision) ? [] : messages, revision: String(revision), reset: !input.revision, replacedKeys: input.revision === String(revision) ? [] : ["producer-turn"], removedKeys: [], entryKeys: ["producer-turn"], running, terminal: "" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main", model: "root/model" }], currentAgent: "main" });
      case "/api/ListConfig": return Response.json({ config: {} });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/Prompt": expect(input.text).toBe("/stop"); running = false; revision++; return Response.json({ privateText: "" });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    // Show each call on its own so the per-call lifecycle summaries stay visible.
    await page.addInitScript((rows: string) => localStorage.setItem("timeline-detail", rows), JSON.stringify({ version: 1, rows: timelineLevels.find((level) => level.id === "everything")!.rows }));
    for (const width of [900, 375]) {
      running = true; revision++;
      messages = [event("input", "user", "Public progress question", { inputId: "consumed" }), ...calls, event("text", "assistant", "Held partial suffix", { state: "working", complete: false })];
      stream = Promise.withResolvers();
      await page.setViewportSize({ width, height: 700 });
      await page.goto(`http://127.0.0.1:${server.port}/s/${Buffer.from("public").toString("base64url")}`);
      const log = page.locator("#transcript-scroll");
      await log.getByText("Held partial suffix", { exact: true }).waitFor();
      expect(await log.locator('[data-slot="bubble-content"]').filter({ hasText: "Held partial suffix" }).count()).toBe(1);
      expect(await log.locator('[data-slot="message"][data-message-id]').count()).toBe(0);
      // Transcript rendering can finish before the composer's local draft hydration.
      await page.getByRole("button", { name: "Stop", exact: true }).and(page.locator(":enabled")).waitFor();
      expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
      messages = [...messages, event("B:outcome", "tool", "B result", { toolCallId: "B", parentId: "producer/turn/response", state: "completed", agent: "canonical-child", model: "child/model" })]; revision++;
      (await stream.promise).enqueue('data: {"conversationId":"public","revision":"hint"}\n\n');
      await log.locator('[data-slot="trace-collapsible-trigger"]').filter({ hasText: "completed" }).waitFor();
      expect(await log.locator('[data-slot="trace-collapsible-trigger"]').filter({ hasText: /^task/ }).evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.textContent?.trim()))).toEqual(["taskworking", "taskcompleted", "taskworking"]);
      expect(await log.locator('[data-slot="message-footer"]').filter({ hasText: "canonical-child (child/model)" }).count()).toBe(3);
      expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
      expect(await log.evaluate((element: HTMLElement) => element.scrollWidth <= element.clientWidth)).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      // No hint: reopening must fetch the missed replacement on its own.
      messages = messages.map((message) => message.itemId === "text" ? { ...message, text: "Short", state: "completed", complete: true } : message); revision++;
      stream = Promise.withResolvers();
      await page.reload();
      await log.getByText("Short", { exact: true }).waitFor();
      expect(await log.getByText("Held partial suffix", { exact: true }).count()).toBe(0);
      expect(await log.getByText("Short", { exact: true }).count()).toBe(1);
      messages = messages.filter((message) => message.itemId !== "text"); revision++;
      (await stream.promise).enqueue('data: {"conversationId":"public","revision":"duplicate"}\n\ndata: {"conversationId":"public","revision":"duplicate"}\n\n');
      await log.getByText("Short", { exact: true }).waitFor({ state: "detached" });
      expect(await log.locator('[data-slot="trace-collapsible-trigger"]').filter({ hasText: /^task/ }).count()).toBe(3);
      await page.getByRole("button", { name: "Stop", exact: true }).click();
      await page.getByRole("button", { name: "Send", exact: true }).waitFor();
    }
  } finally { server.stop(true); await browser.close(); }
}, 60_000);

test("actual App restores, merges, isolates and keeps composer independent", () => appScenario("lists"), 180_000);

test("session titles and turn-rail previews format tags and markup", async () => {
  const { chromium: engine } = await import(playwright!);
  const preview = "<!subteam^S1> *Red alert*";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "alert", preview, agent: "main" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "sidebar-format" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "/workspace" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }] });
      case "/api/SlackNames": return Response.json({ names: { S1: "handle" } });
      case "/api/History": return Response.json({ messages: [{ messageId: "1:0", entryKey: "1", itemId: "1:0", inputId: "", role: "user", text: preview, complete: true, turnId: "" }], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: ["1"], running: false, terminal: "" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await page.goto(`http://127.0.0.1:${server.port}/s/${Buffer.from("alert").toString("base64url")}`);
    await page.locator("footer").waitFor({ state: "attached" });
    await page.keyboard.press("Control+p");
    const title = page.getByRole("dialog", { name: "Go to session or command", exact: true }).locator('[data-slot="session-title"]');
    await title.getByText("@handle Red alert", { exact: true }).waitFor();
    expect(await title.textContent()).toBe("@handle Red alert");
    expect(await title.locator("strong").textContent()).toBe("Red alert");
    expect(await title.getAttribute("title")).toBe("@handle Red alert");
    expect(await title.evaluate((el: HTMLElement) => getComputedStyle(el).whiteSpace)).toBe("nowrap");
    const rail = page.getByRole("navigation", { name: "Conversation turns" });
    await page.keyboard.press("Escape");
    await rail.getByRole("button", { name: "Turn 1: @handle Red alert", exact: true }).waitFor();
    await rail.hover();
    await rail.getByRole("button", { name: "Jump to turn 1: @handle Red alert", exact: true }).waitFor();
    expect(await rail.locator("strong").textContent()).toBe("Red alert");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
