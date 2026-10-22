import { expect, test } from "bun:test";
import path from "node:path";
import type { Attachment, PromptDelivery, QueueItem, Session, SessionBatch, TranscriptEvent } from "./types";
import { RPCError } from "./api";
import { timelineLevels } from "./timeline-detail";

const screenshots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;

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
      await storage.saveCompleteSessions("a", "v1", rows, await storage.loadSnapshotGeneration("a", "v1"));
      const put = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function (...args) {
        const request = put.apply(this, args);
        request.addEventListener("success", () => this.transaction.abort());
        return request;
      };
      let rejected = false;
      try {
        await storage.saveCompleteSessions("a", "v1", [{ id: "replacement" }], await storage.loadSnapshotGeneration("a", "v1"));
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
      const rows = await storage.loadSavedSessions("a", "v1");
      await storage.saveCompleteSessions("a", "v1", [], await storage.loadSnapshotGeneration("a", "v1"));
      return { running: rows[0].running, fullPreview: rows[0].preview === "start\0" + "x".repeat(17 * 1024 * 1024) + "\0end", empty: await storage.loadSavedSessions("a", "v1") };
    });
    expect(restored).toEqual({
      fullPreview: true,
      running: false,
      empty: [],
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
      const rows = [{ id: "synthetic", preview: "old synthetic preview", agent: "agent", title: "room" }];
      const activeGeneration = await storage.loadSnapshotGeneration("owner", "protocol");
      await storage.saveCompleteSessions("owner", "protocol", rows, activeGeneration);
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
      const pendingOwnerSave = storage.saveCompleteSessions("owner", "protocol", [{ ...rows[0], preview: "switched-away preview" }], activeGeneration);
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
      return {
        newest,
        other: await storage.loadSavedSessions("other", "protocol"),
        ownerAfterSwitch: await storage.loadSavedSessions("owner", "protocol"),
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

const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const [platform, modifier, alternate] of [["MacIntel", "⌘", "Option"], ["Linux x86_64", "Ctrl", "Alt"]]) {
  test(`actual App command hints match existing bindings on ${platform}`, async () => {
    const { chromium: engine } = await import(playwright!);
    const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
      const url = new URL(request.url);
      if (!url.pathname.startsWith("/api/")) return new Response(Bun.file(path.join(dist, url.pathname.startsWith("/assets/") ? url.pathname : "index.html")));
      if (url.pathname === "/api/ListSessions") return new Response('data: {"sessions":[],"owner":"tester","upstreamSuccess":true,"summariesComplete":true}\n\nevent: complete\ndata: {}\n\n', { headers: { "Content-Type": "text/event-stream" } });
      if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
      if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "command-hints" });
      if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
      if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
      if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "LEFT" } });
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
        expect(await keys()).toEqual({ "Sessions: New": [modifier, alternate, "N"], "Tabs: Next": [modifier, "]"], "Tabs: Previous": [modifier, "["], ...(width === 1280 ? { "Tabs: Toggle sidebar": [modifier, "B"] } : {}) });
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
      expect(await shortcutList.getByRole("listitem").evaluateAll((items: Element[]) => items.map((item) => [...item.querySelectorAll("kbd")].map((key) => key.textContent)))).toEqual([
        [modifier, "P"], [modifier, "Shift", "P"], [modifier, alternate, "N"], [modifier, "["], [modifier, "]"], [modifier, "B"], ["Escape"], [modifier, "Enter"], [modifier, alternate, "Enter"]]);
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
    { id: "slack-thread:C11:1", title: "amber", preview: "Amber result", agent: "main" },
    { id: "slack-thread:C12:1", title: "Blue garden", preview: "Blue result", agent: "main" },
    { id: "slack-thread:C13:1", title: "cobalt", preview: "Cobalt result", agent: "other" },
  ];
  const queries: string[] = [];
  const longRooms = ["synthetic-observatory-room-with-a-long-shared-prefix-for-wrapping-amber", "synthetic observatory room with a long shared prefix for wrapping blue"];
  const results: Record<string, number[]> = {
    "room:amber": [0], 'room:amber room:"Blue garden"': [0, 1], 'room:"Blue garden"': [1],
    'room:"Blue garden" agent:main': [1], 'room:"Blue garden" agent:other': [],
    '😀 room:"Blue garden" room:amber': [],
  };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) return new Response(Bun.file(path.join(dist, url.pathname.startsWith("/assets/") ? url.pathname : "index.html")));
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "room-search" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }, { name: "other" }] });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: [] });
    if (url.pathname === "/api/ListWorkflows") return Response.json({ workflows: [] });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ jobs: [] });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      queries.push(query);
      const terms = ["room:amber", 'room:"Blue garden"', "agent:main", "agent:other", ...longRooms.map((room) => `room:${JSON.stringify(room)}`)].flatMap((text) => {
        const start = query.indexOf(text);
        return start < 0 ? [] : [{ key: text.split(":")[0], text, start, end: start + text.length }];
      }).sort((a, b) => a.start - b.start);
      return Response.json({ terms, text: "", needle: "", matches: (results[query] ?? []).map((index) => ({ conversationId: rows[index].id, field: "Preview", text: rows[index].preview })), messages: [], mentionIds: [], indexComplete: true, summariesComplete: true });
    }
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "TOP" } });
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
      expect(await surface.getByRole("button", { name: "agent:main", exact: true }).count()).toBe(0);
      expect(await surface.getByRole("button", { name: 'room:"Blue garden"', exact: true }).count()).toBe(1);
      await surface.getByRole("button", { name: "agent:other", exact: true }).click();
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
  const html = (await Bun.file(path.join(dist, "index.html")).text()).replace(/<script type="module" src="[^"]+"><\/script>/, '<script type="module" src="/current.js"></script>');
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
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "public", agent: "main", title: "Public progress" }], owner: "alice", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      if (url.pathname.startsWith("/assets/")) return new Response(Bun.file(path.join(dist, url.pathname)));
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
      case "/api/Prompt": expect(input.text).toBe("$stop"); running = false; revision++; return Response.json({ privateText: "" });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "TOP" } });
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

test("actual App restores, merges, isolates and keeps composer independent", async () => {
  const { chromium: engine } = await import(playwright!);
  let identityHold = Promise.withResolvers<void>();
  let promptHold = Promise.withResolvers<string>();
  let createHold = Promise.withResolvers<string>();
  let uploadHold = Promise.withResolvers<void>();
  const cronHold = Promise.withResolvers<string>();
  let cronRunCalls = 0;
  const cronHistory: string[] = [];
  const historyRequests: { id: string }[] = [];
  const searchQueries: string[] = [];
  const manySearchHold = Promise.withResolvers<void>();
  const lastSearchHold = Promise.withResolvers<void>();
  // Fixed SearchSessions fixtures: the conversation IDs, in server order, and the filter terms each query returns.
  const searches: Record<string, string[]> = {
    "needle-17mib": ["kept"], "PREVIEW": ["slack-thread:C:1", "kept"], "ORIGINAL-KEY": ["kept"], "preview": ["slack-thread:C:1"], "partial preview 445": ["prefix-445"],
    "is:pinned fixture 0": ["perf-0"], "IS:PINNED": ["named", "pinned"], "Release notes": ["named"], "Original preview": ["named"], "agent:main room:room matching": ["slack-thread:C:active", "slack-thread:C:resolved"],
  };
  const searchTerms: Record<string, string[]> = { "agent:main": ["agent:main"], "agent:main room:ro": ["agent:main"], "agent:main room:room": ["agent:main", "room:room"], "agent:main room:room matching": ["agent:main", "room:room"], "tag:customer": ["tag:customer"] };
  const cronOpens: string[] = [];
  const wireTail = Promise.withResolvers<void>();
  const ownerTail = Promise.withResolvers<void>();
  let blocked = Promise.withResolvers<void>();
  let listStarted = Promise.withResolvers<void>();
  const ctrl = {
    username: "alice",
    principal: "Alice Smith",
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
    popError: false,
    attachmentPrompts: [] as { id: string; text: string; messageId: string; delivery?: string; attachmentIds?: string[] }[],
    promptStarted: Promise.withResolvers<void>(),
    holdCreate: false,
    createStarted: Promise.withResolvers<void>(),
    history: [] as TranscriptEvent[],
    running: false,
    // Like the server, a held Prompt makes only its own conversation report running.
    prompting: new Set<string>(),
    lastInputId: "",
    rows: [] as Session[],
    updateError: false,
    searchError: false,
    createdAgents: [] as string[],
    yieldBatches: async function* (): AsyncGenerator<SessionBatch> {},
  };
  const row = (id: string, preview: string): Session => ({
    id, title: "room", preview, updatedAt: "2026-09-09T00:00:00.123456Z", agent: "main",
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
  const transcriptStreams = new Map<string, ReturnType<typeof Promise.withResolvers<ReadableStreamDefaultController>>>();
  const transcriptStream = (id: string) => {
    if (!transcriptStreams.has(id)) transcriptStreams.set(id, Promise.withResolvers<ReadableStreamDefaultController>());
    return transcriptStreams.get(id)!;
  };
  const historyResponse = (id: string, messages: TranscriptEvent[], origin = "", revision = "") => {
    const running = ctrl.running || ctrl.prompting.has(id);
    const previous: TranscriptEvent[] = revision ? JSON.parse(revision).messages : [];
    const entryKeys = [...new Set(messages.map((message) => message.entryKey))];
    const replacedKeys = entryKeys.filter((key) => !revision || JSON.stringify(messages.filter((message) => message.entryKey === key)) !== JSON.stringify(previous.filter((message) => message.entryKey === key)));
    const removedKeys = [...new Set(previous.map((message) => message.entryKey))].filter((key) => !entryKeys.includes(key));
    return Response.json({ messages: messages.filter((message) => replacedKeys.includes(message.entryKey)), origin, revision: JSON.stringify({ messages, running }), reset: !revision, replacedKeys, removedKeys, entryKeys, running, terminal: "" });
  };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(req) {
    const url = new URL(req.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) {
      transcriptStream(url.searchParams.get("id")!).resolve(controller);
      controller.enqueue(": connected\n\n");
    } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(new ReadableStream({ async start(controller) {
      listResponse = controller;
      ctrl.listCalls++;
      listStarted.resolve();
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
        if (![...ctrl.history, ...ctrl.queue].some((line) => line.attachments?.some((file) => file.id === uploaded.meta.id))) return new Response("not referenced", { status: 404 });
        return new Response(new Uint8Array(uploaded.data), { headers: { "Content-Type": uploaded.meta.mimeType } });
      }
      return new Response(imageBytes, { headers: { "Content-Type": image.mimeType, "Content-Disposition": `${url.searchParams.has("download") ? "attachment" : "inline"}; filename="${image.name}"` } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() && url.pathname !== "/" ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await req.json() as { id: string; revision?: string; query: string; itemId: string; messageId: string; name?: string; agent?: string; text: string; delivery?: PromptDelivery; attachmentIds?: string[]; stem: string; sourceConversationId?: string; conversationId?: string; pinned?: boolean };
    try {
      switch (url.pathname) {
        case "/api/Protocol": return Response.json({ protoSha256: ctrl.protocol });
        case "/api/Identity":
          if (ctrl.identityError) throw new RPCError("unauthenticated", 16);
          await identityHold.promise;
          return Response.json({ username: ctrl.username, principal: ctrl.principal });
        case "/api/Handoff": return Response.json({ document: "Handoff document" });
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
          if (input.delivery === "STASH") {
            if (ctrl.promptError) throw new RPCError("Stash failed; retry", 13);
            ctrl.queue.push({ id: input.messageId, text: input.text, delivery: "STASH", principal: ctrl.principal, attachments: input.attachmentIds?.map((id) => uploadedFiles.find(({ meta }) => meta.id === id)!.meta) });
            return Response.json({ privateText: "" });
          }
          if (input.text.startsWith("$agent ")) {
            ctrl.currentAgents.set(input.id, input.text.slice(7));
            return Response.json({ privateText: `Switched to ${input.text.slice(7)}` });
          }
          if (input.text === "$stop") { ctrl.running = false; ctrl.prompting.delete(input.id); return Response.json({ privateText: "Stopped" }); }
          if (ctrl.promptError) throw new RPCError("Send failed; retry", 13);
          ctrl.prompting.add(input.id);
          ctrl.lastInputId = input.messageId;
          if (ctrl.holdInterventions) {
            const id = input.delivery === "QUEUE" ? `server-${input.messageId}` : input.messageId;
            ctrl.queue.push({ id, text: input.text, delivery: input.delivery, principal: ctrl.principal });
            if (input.delivery === "QUEUE") return Response.json({ privateText: "" });
            const pending = Promise.withResolvers<string>();
            ctrl.interventions.set(id, pending);
            return Response.json({ privateText: await pending.promise });
          }
          if (ctrl.holdPrompt) { const privateText = await promptHold.promise; ctrl.prompting.delete(input.id); return Response.json({ privateText }); }
          ctrl.prompting.delete(input.id);
          return Response.json({ privateText: "" });
        case "/api/ListCronJobs": return Response.json({ jobs });
        case "/api/RunCronJob": cronRunCalls++; return Response.json({ id: await cronHold.promise });
        case "/api/SearchSessions":
          searchQueries.push(input.query);
          if (input.query === "is:pinned fixture 0") await manySearchHold.promise;
          if (input.query === "is:pinned not-in-any-row") await lastSearchHold.promise;
          if (ctrl.searchError) throw new RPCError("search unavailable", 13);
          return Response.json({
            terms: (searchTerms[input.query] ?? []).map((text) => ({ key: text.slice(0, text.indexOf(":")), text, start: input.query.indexOf(text), end: input.query.indexOf(text) + text.length })),
            text: input.query, needle: input.query.toLowerCase(), matches: (searches[input.query] ?? []).map((conversationId) => ({ conversationId, field: "Preview", text: "" })), messages: [], mentionIds: [], indexComplete: true, summariesComplete: true,
          });
        case "/api/History":
          historyRequests.push(input);
          if (input.id === "cron:silent-source" || input.id === "web:cron:silent-source") {
            cronHistory.push(input.id);
            return historyResponse(input.id, [{ role: "assistant", text: "Silent run trace", complete: true, turnId: "", entryKey: "silent", itemId: "silent:0", inputId: "" }], "", input.revision);
          }
          if (input.sourceConversationId) {
            cronHistory.push(input.sourceConversationId);
            return historyResponse(input.id, [{ role: "assistant", text: "Daily run report", complete: true, turnId: "", entryKey: "daily", itemId: "daily:0", inputId: "" }], "", input.revision);
          }
          return historyResponse(input.id, ctrl.history, "", input.revision);
        case "/api/ListAgents": return Response.json({ agents: input.conversationId === "web:cron:silent-source" ? [{ name: "other", model: "gpt" }] : [{ name: "other", model: "gpt", customShell: "/usr/local/bin/agent-sandbox" }, { name: "main", model: "gpt" }], currentAgent: input.conversationId ? ctrl.currentAgents.get(input.conversationId) ?? "main" : "" });
        case "/api/ListSkills": return Response.json({ skills: input.agent === "main" ? [{ name: "review", description: "Review changes" }, { name: "stop", description: "Inspect logs" }] : input.agent ? [] : [{ name: "release", description: "Ship a release", content: "release steps" }] });
        case "/api/ListConfig": return Response.json({ config: { tailscaleUser: "connected@example.com" } });
        case "/api/UpdateSession":
          if (ctrl.updateError) throw new RPCError("Could not save session", 13);
          if (!ctrl.rows.some((session) => session.id === input.id)) return Response.json({});
          ctrl.rows = ctrl.rows.map((session) => session.id === input.id ? { ...session, ...input, name: input.name === undefined ? session.name : input.name.trim() } : session);
          ctrl.yieldBatches = complete(ctrl.rows);
          return Response.json({});
        case "/api/ListQueue": return Response.json({ items: ctrl.queue });
        case "/api/PopQueueItem":
          if (ctrl.popError) throw new RPCError("Pop failed; retry", 13);
          ctrl.queue = ctrl.queue.map((item) => item.id === input.itemId ? { ...item, delivery: "QUEUE" } : item);
          return Response.json({});
        case "/api/SteerQueueItem":
          ctrl.queue = ctrl.queue.map((item) => item.id === input.itemId ? { ...item, delivery: "STEER" } : item);
          return Response.json({});
        case "/api/EditQueueItem":
          ctrl.queue = ctrl.queue.map((item) => item.id === input.itemId ? { ...item, text: input.text } : item);
          return Response.json({});
        case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "TOP" } });
        default: return Response.json({});
      }
    } catch (error) { return Response.json({ message: (error as Error).message, code: (error as RPCError).code }, { status: (error as RPCError).code === 16 ? 401 : 500 }); }
  } });
  const port = server.port;
  const origin = `http://127.0.0.1:${port}`;
  const browser = await engine.launch({ executablePath: chromium, headless: true, args: ["--disable-features=OverscrollHistoryNavigation"] });
  const shown = async (page: { getByText: (text: string, opts?: { exact?: boolean }) => { first: () => { waitFor: (opts: { state: "visible" | "hidden"; timeout?: number }) => Promise<void> }; count: () => Promise<number> } }, text: string) => {
    // A session's tab title can repeat its row title.
    await page.getByText(text, { exact: true }).first().waitFor({ state: "visible", timeout: 15_000 });
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
    // Session rows are observed through the Cmd+P palette once the app has mounted.
    const openSessions = async (target: typeof page) => {
      await target.locator("footer").waitFor({ state: "attached" });
      await target.keyboard.press("Control+p");
    };
    const goToSession = async (target: typeof page, text: string) => {
      await openSessions(target);
      await target.getByRole("dialog", { name: "Go to session or command", exact: true }).getByRole("button").filter({ hasText: text }).click();
    };
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "platform", { value: "MacIntel" });
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
    await page.keyboard.press("Control+p");
    const sessionPalette = page.getByRole("dialog", { name: "Go to session or command", exact: true });
    const search = sessionPalette.getByPlaceholder("Search sessions and commands", { exact: true });
    await shown(sessionPalette, "Huge");
    await hidden(sessionPalette, "will vanish");
    expect(historyRequests).toEqual([]);
    await search.fill("needle-17mib"); // Text past the title's first line is the body, which Cmd+P never matches.
    await hidden(sessionPalette, "Huge");
    await search.fill("HUG");
    await shown(sessionPalette, "Huge");
    await search.fill("no-such-preview");
    await shown(sessionPalette, "loading...");
    await hidden(sessionPalette, "No matches");
    await page.keyboard.press("Escape");
    historyRequests.length = 0;

    const putsBeforeLive = await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts);
    ctrl.yieldBatches = complete([row("kept", "saved preview"), row("gone", "will vanish"), row("slack-thread:C:1", "filter preview")]);
    listStarted = Promise.withResolvers<void>();
    const refreshRequest = listStarted.promise;
    blocked.resolve();
    blocked = Promise.withResolvers();
    await page.keyboard.press("Control+p");
    await shown(sessionPalette, "saved preview");
    await shown(sessionPalette, "will vanish");
    expect(await page.evaluate(async () => { await document.fonts.ready; return document.fonts.check("16px Inter") && getComputedStyle(document.body).fontFamily.startsWith("Inter"); })).toBe(true);
    await page.waitForFunction((expected: number) => (window as unknown as { __snapshotPuts: number }).__snapshotPuts >= expected, putsBeforeLive + 1);
    expect(await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts)).toBe(putsBeforeLive + 1);

    await refreshRequest;
    await hidden(page, "Refreshing");
    await shown(sessionPalette, "saved preview");
    const heldCalls = ctrl.listCalls;
    await page.keyboard.press("Escape");
    await sessionPalette.waitFor({ state: "hidden" });

    const navigation = page.locator("footer");
    const compactHeight = (await navigation.boundingBox())!.height;
    const navigationSearch = navigation.getByRole("button", { name: "Search sessions and commands", exact: true });
    expect(await navigation.locator("#bottom-navigation button:visible").count()).toBe(4);
    expect(await navigation.getByRole("link").count()).toBe(0);
    expect((await navigationSearch.boundingBox())!.width).toBe(48);
    expect((await navigationSearch.locator("svg").boundingBox())!.width).toBe(24);
    await page.mouse.move(0, 0);
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).click();
    expect(await navigationSearch.isVisible()).toBe(false);
    expect((await navigation.boundingBox())!.height).toBeLessThan(compactHeight);
    expect(await navigation.getByRole("button", { name: "Show bottom navigation" }).getAttribute("aria-expanded")).toBe("false");
    await navigation.getByRole("button", { name: "Show bottom navigation" }).press("Enter");
    expect(await navigationSearch.isVisible()).toBe(true);
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).press("Space");
    await navigation.getByRole("button", { name: "Show bottom navigation" }).click();
    expect((await navigation.boundingBox())!.height).toBe(compactHeight);

    expect(await page.locator("header:visible").count()).toBe(0);
    await page.keyboard.press("Control+p");
    expect(await sessionPalette.locator("ul").evaluate((node: HTMLElement) => getComputedStyle(node).scrollbarWidth)).toBe("thin");
    await search.waitFor();
    await sessionPalette.getByRole("button").filter({ hasText: "saved preview" }).waitFor();
    // The palette scales in; measure alignment only after its opening animation settles.
    await sessionPalette.evaluate((node: HTMLElement) => Promise.all(node.getAnimations({ subtree: true }).map((animation) => animation.finished)));
    const channelRow = sessionPalette.getByRole("button").filter({ hasText: "filter preview" });
    expect(await channelRow.locator("span[title]:not([data-slot])").getAttribute("title")).toBe("room · main");
    expect((await channelRow.locator("span[title]:not([data-slot])").boundingBox())!.x).toBe((await channelRow.locator('[data-slot="session-title"]').boundingBox())!.x);
    const webRow = sessionPalette.getByRole("button").filter({ hasText: "saved preview" });
    expect(await webRow.locator("span[title]:not([data-slot])").textContent()).toBe("main");
    expect(await webRow.locator("time").isVisible()).toBe(true);
    expect(historyRequests).toEqual([]); // Empty Cmd+P does not load histories.
    searchQueries.length = 0;
    for (const term of ["   ", "agent:", "room:", "is:", "is:pin", "sort:", "sort:ne"]) {
      await search.fill(term);
      await page.waitForTimeout(300); // Past the search pause.
      expect(searchQueries).toEqual([]); // Unfinished operators send nothing.
    }
    await search.fill("agent:");
    await sessionPalette.getByRole("button", { name: "main gpt", exact: true }).click();
    await sessionPalette.getByRole("button", { name: "agent:main", exact: true }).waitFor();
    expect(page.url()).toBe(origin + "/");
    await search.fill("room:");
    await search.press("Tab");
    await sessionPalette.getByRole("button", { name: "room:room", exact: true }).waitFor();
    expect(searchQueries).toEqual(["agent:main", "agent:main room:room"]);
    await sessionPalette.getByRole("button", { name: "room:room", exact: true }).click();
    await sessionPalette.getByRole("button", { name: "agent:main", exact: true }).click();
    expect(historyRequests).toEqual([]); // Search is the server's; it never reads per-chat histories.
    searchQueries.length = 0;
    await search.fill("PREVIEW"); // Plain text matches titles here, in the session list's order, with no request.
    await sessionPalette.getByRole("button").filter({ hasText: "will vanish" }).waitFor({ state: "detached" });
    await sessionPalette.getByRole("button").filter({ hasText: "filter preview" }).waitFor();
    expect(await sessionPalette.getByRole("button").locator('[data-slot="session-title"]').allTextContents()).toEqual(["saved preview", "filter preview"]);
    await search.fill("no-such-origin");
    await sessionPalette.getByText("loading...", { exact: true }).waitFor();
    await search.fill("SAVED");
    expect(searchQueries).toEqual([]);
    const opened = page.waitForResponse((response: { url: () => string; request: () => { postDataJSON: () => { id: string } } }) => response.url().endsWith("/api/History") && response.request().postDataJSON().id === "kept");
    await search.press("Enter");
    await opened;
    await page.waitForURL(`${origin}/s/${Buffer.from("kept").toString("base64url")}`);
    await page.getByPlaceholder("Message or $command").waitFor();
    expect(historyRequests.some((request) => request.id === "kept")).toBe(true);
    await transcriptStream("kept").promise;
    await navigation.getByRole("button", { name: "New session", exact: true }).click();
    await page.waitForURL(origin + "/");
    transcriptStreams.clear();
    await page.keyboard.press("Control+Shift+p");
    const commandPalette = page.getByRole("dialog", { name: "Run command", exact: true });
    await commandPalette.getByPlaceholder("Type a command", { exact: true }).waitFor();
    expect(await commandPalette.locator("ul").evaluate((node: HTMLElement) => getComputedStyle(node).scrollbarWidth)).toBe("thin");
    expect(await commandPalette.getByRole("button").allTextContents()).toEqual(["Cron: Dashboard", "Cron: Run", "Help: Keyboard Shortcuts", "List Agents", "List Skills",
      "Output: MarkdownCurrent · Render messages as Markdown, including headings and tables.", "Output: RawShow messages with Slack formatting.", "Pages: Index", "Sessions: New⌘OptionN", "Sessions: Search", "Settings", "Tabs: Add to new group", "Tabs: Close", "Tabs: Close all", "Tabs: Close others", "Tabs: Close to the right", "Tabs: Move tabs to left", "Tabs: Next⌘]", "Tabs: Pin", "Tabs: Previous⌘[",
      // Timeline levels follow the Config slider, with its descriptions, and mark the active level.
      "Timeline: Messages onlyHide all activity.", "Timeline: QuietGroup subagents and skills. Hide other activity.", "Timeline: CompactCurrent · Group all activity with details collapsed.",
      "Timeline: DetailedExpand execute output. Show subagents and skills separately and group other activity.", "Timeline: EverythingShow all activity separately and expanded.", "Voice"]);
    await page.keyboard.press("Escape");
    await commandPalette.waitFor({ state: "hidden" });
    // The search button opens one palette: sessions only when empty, matching commands above sessions once typed.
    await page.evaluate(() => localStorage.setItem("command-history", JSON.stringify(["tab-close-others"])));
    await navigationSearch.click();
    await sessionPalette.getByRole("button").filter({ hasText: "saved preview" }).waitFor();
    expect(await sessionPalette.getByRole("button", { name: "Settings", exact: true }).count()).toBe(0);
    const commandRows = sessionPalette.locator("ul > li > button").filter({ hasNot: page.locator('[data-slot="session-title"]') });
    await search.fill("tabs: close");
    await sessionPalette.getByRole("button", { name: "Tabs: Close", exact: true }).waitFor();
    // An exact match leads; recent then alphabetical contiguous matches follow, ahead of the sessions, and the Search hand-off comes last.
    expect(await commandRows.allTextContents()).toEqual(["Tabs: Close", "Tabs: Close others", "Tabs: Close all", "Tabs: Close to the right", "Search everywhere for 'tabs: close'"]);
    expect(await sessionPalette.locator("ul > li > button").first().textContent()).toBe("Tabs: Close");
    await search.fill("tabs: close t");
    await sessionPalette.getByRole("button", { name: "Tabs: Close all", exact: true }).waitFor({ state: "detached" });
    expect(await commandRows.allTextContents()).toEqual(["Tabs: Close to the right", "Tabs: Close others", "Search everywhere for 'tabs: close t'"]); // A contiguous match beats a recent fuzzy one.
    await search.fill("cron: run"); // A bare key is not a filter.
    await sessionPalette.getByRole("button", { name: "Cron: Run", exact: true }).waitFor();
    await search.fill("is:pinned cron: run");
    await sessionPalette.getByRole("button", { name: "Cron: Run", exact: true }).waitFor({ state: "detached" });
    await search.fill("cron: run");
    await search.press("Enter");
    const cronPicker = page.getByRole("dialog", { name: "Run cron", exact: true }).getByPlaceholder("Search cron jobs", { exact: true });
    await cronPicker.waitFor();
    expect(await cronPicker.evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
    await page.keyboard.press("Escape");
    await cronPicker.waitFor({ state: "hidden" });
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForFunction(() => document.querySelector('button[aria-label="Hide bottom navigation"]')!.getBoundingClientRect().height >= 44);
    expect((await navigation.getByRole("button", { name: "Hide bottom navigation" }).boundingBox())!.height).toBeGreaterThanOrEqual(44);
    expect((await navigationSearch.boundingBox())!.width).toBeGreaterThanOrEqual(44);
    const newBounds = (await navigation.getByRole("button", { name: "New session", exact: true }).boundingBox())!;
    const navigationSearchBounds = (await navigationSearch.boundingBox())!;
    expect(navigationSearchBounds.x - newBounds.x - newBounds.width).toBeGreaterThanOrEqual(8);
    await page.mouse.move(0, 0);
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).tap();
    expect(await navigationSearch.isVisible()).toBe(false);
    await navigation.getByRole("button", { name: "Show bottom navigation" }).tap();
    expect(await navigationSearch.isVisible()).toBe(true);
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
    expect(await navigationSearch.isVisible()).toBe(false);
    const showHandle = (await navigation.getByRole("button", { name: "Show bottom navigation" }).boundingBox())!;
    const showY = showHandle.y + showHandle.height / 2;
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y: showY }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: showY - 40 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await navigation.getByRole("button", { name: "Hide bottom navigation" }).waitFor();
    expect(await navigationSearch.isVisible()).toBe(true);
    await touch.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: y + 40 }] });
    await touch.send("Input.dispatchTouchEvent", { type: "touchCancel", touchPoints: [] });
    expect(await navigationSearch.isVisible()).toBe(true);
    expect((await navigation.getByRole("button", { name: "Hide bottom navigation" }).locator("..").boundingBox())!.height).toBe(16);
    await touch.detach();
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
    await navigationSearch.click();
    const listAgents = sessionPalette.getByRole("button", { name: "List Agents", exact: true });
    await search.fill("lag");
    await listAgents.waitFor();
    await search.fill("lg"); // Fuzzy matching starts at three letters.
    await listAgents.waitFor({ state: "detached" });
    await search.fill("lag");
    await search.press("Enter"); // A fuzzy command on top yields to the sessions; none match, so Search takes the text.
    await page.waitForURL("**/search?q=lag");
    await page.goBack();
    await page.waitForURL(origin + "/");
    await navigationSearch.click();
    await search.fill("lag");
    await listAgents.waitFor();
    await search.press("ArrowDown");
    await search.press("ArrowUp");
    await search.press("Enter"); // Picked with the arrows, it runs without waiting for session results.
    await page.waitForURL("**/agents");
    await page.getByRole("button", { name: /^other/ }).click();
    expect(await page.getByRole("heading", { name: "Shell" }).locator("..").getByText("/usr/local/bin/agent-sandbox").textContent()).toBe("/usr/local/bin/agent-sandbox");
    await page.goBack();
    await page.waitForURL(origin + "/");
    expect(await skillComposer.inputValue()).toBe("retained through browser history");
    await page.goForward();
    await page.waitForURL("**/agents");
    await page.goBack();
    await page.waitForURL(origin + "/");
    expect(await skillComposer.inputValue()).toBe("retained through browser history");
    await skillComposer.fill("");

    await page.getByRole("button", { name: "Choose agent", exact: true }).click();
    await page.getByRole("menuitemradio", { name: "other gpt" }).click();
    ctrl.holdPrompt = true;
    await page.getByPlaceholder("Message a new session").fill("hello\n\nwhile held");
    // A send during an identity refresh has no provisional author, so send just after a refresh.
    await page.waitForResponse("**/api/Identity");
    await page.getByPlaceholder("Message a new session").press("Enter");
    await ctrl.promptStarted.promise;
    expect(ctrl.prompt).toEqual(["web-session:new:hello\n\nwhile held"]);
    expect(await page.locator('[data-slot="bubble-content"] [data-slot="message-author"]').textContent()).toBe("Alice Smith");
    await page.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
    await page.getByPlaceholder("Queue a follow-up · ⌘⏎ steers").waitFor();
    await page.locator("textarea:enabled").waitFor();
    expect(await page.locator("textarea").evaluate((input: HTMLTextAreaElement) => document.activeElement === input)).toBe(true);
    expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
    // Selecting chat text offers quoting without replacing the native context menu.
    await page.locator("textarea").fill("My draft");
    const selectedQuote: string = await page.locator("#transcript-scroll").evaluate((element: HTMLElement) => {
      const range = document.createRange();
      range.selectNodeContents(element.querySelector('[aria-label="Turn 1"] [data-slot="bubble-content"] div')!);
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
      const longDraft = "A long draft line\n".repeat(100);
      await page.locator("textarea").fill(longDraft);
      expect(await page.locator("textarea").evaluate((element: HTMLTextAreaElement) => element.scrollHeight > element.clientHeight)).toBe(true);
      const sendBounds = await page.getByRole("button", { name: "Send", exact: true }).boundingBox();
      expect(sendBounds!.y).toBeGreaterThanOrEqual(0);
      expect(sendBounds!.y + sendBounds!.height).toBeLessThanOrEqual(844);
      await page.locator("textarea").press("Control+End");
      expect(await page.locator("textarea").evaluate((element: HTMLTextAreaElement) => element.scrollTop > 0)).toBe(true);
      expect(await page.locator("textarea").inputValue()).toBe(longDraft);
      await page.locator("textarea").fill("");
      await page.locator('#transcript-scroll [data-slot="bubble-content"] p').first().click({ trial: true }); // Measure after the responsive layout finishes resizing.
      const word = await page.locator('#transcript-scroll [data-slot="bubble-content"] p').first().evaluate((element: HTMLElement) => {
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
    ctrl.history = [{ entryKey: "intervention-run", itemId: "intervention-run:0", inputId: ctrl.lastInputId, role: "user", text: "hello\n\nwhile held", turnId: "intervention-run", complete: true }];
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
        await page.screenshot({ path: path.join(screenshots, "chat-steer-mobile.png") });
        await page.getByRole("button", { name: "Steer", exact: true }).last().click();
      }
      const request = (await intervention).postDataJSON();
      expect(request).toEqual({ id: "web-session:new", text: "identical follow-up", delivery, messageId: expect.any(String) });
      if (delivery === "QUEUE") {
        await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
        expect(await page.locator("textarea").evaluate((input: HTMLTextAreaElement) => document.activeElement === input)).toBe(true);
        await page.keyboard.type("Next draft");
        expect(await page.locator("textarea").inputValue()).toBe("Next draft");
        await page.evaluate((pathname: string) => { history.pushState(null, "", pathname); dispatchEvent(new PopStateEvent("popstate")); }, `/s/${Buffer.from("focus-other").toString("base64url")}`);
        await page.waitForFunction(() => document.querySelector("textarea")?.value === "" && !document.querySelector("fieldset:disabled"));
        transcriptStreams.delete("web-session:new");
        await page.goBack();
        await page.waitForFunction(() => document.querySelector("textarea")?.value === "Next draft" && !document.querySelector("fieldset:disabled"));
        expect(await page.locator("textarea").evaluate((input: HTMLTextAreaElement) => document.activeElement === input)).toBe(false);
      }
      interventionIds.push(request.messageId);
      expect(await chat.getByText("identical follow-up", { exact: true }).count()).toBe(0);
      if (delivery === "STEER") await parking.getByText("identical follow-up", { exact: true }).nth(interventionIds.length - 2).waitFor();
    }
    expect(new Set(interventionIds).size).toBe(3);
    const stream = await transcriptStream("web-session:new").promise;
    ctrl.history.push({ entryKey: "intervention-run", itemId: "intervention-run:1", inputId: "", role: "assistant", turnId: "intervention-run", text: "Before steering", complete: false });
    stream.enqueue(`data: ${JSON.stringify({ conversationId: "web-session:new", revision: "hint" })}\n\n`);
    for (const [index, id] of [interventionIds[2], interventionIds[1]].entries()) {
      ctrl.history.push({ entryKey: "intervention-run", itemId: `intervention-run:${index + 2}`, inputId: id, role: "user", text: "identical follow-up", turnId: "intervention-run", complete: true });
      stream.enqueue(`data: ${JSON.stringify({ conversationId: "web-session:new", revision: "hint" })}\n\n`);
      await chat.getByText("identical follow-up", { exact: true }).nth(index).waitFor();
      ctrl.interventions.get(id)!.resolve("");
    }
    await parking.waitFor({ state: "hidden" });
    await chat.getByText("identical follow-up", { exact: true }).nth(1).waitFor();
    // A stale queue poll must not resurrect either consumed steer.
    expect(await page.locator("[data-queue-id]").count()).toBe(1);
    ctrl.history.push({ entryKey: "intervention-run", itemId: "intervention-run:4", inputId: "", role: "assistant", turnId: "intervention-run", text: "After steering", complete: false });
    stream.enqueue(`data: ${JSON.stringify({ conversationId: "web-session:new", revision: "hint" })}\n\n`);
    await chat.getByText("After steering", { exact: true }).waitFor();
    const queuedId = `server-${interventionIds[0]}`;
    await page.locator(`[data-queue-id="${queuedId}"]`).getByRole("button", { name: "Steer", exact: true }).click();
    await parking.getByText("identical follow-up", { exact: true }).waitFor();
    expect(await chat.getByText("identical follow-up", { exact: true }).count()).toBe(2);
    ctrl.history.push({ entryKey: "intervention-run", itemId: "intervention-run:5", inputId: queuedId, role: "user", text: "identical follow-up", turnId: "intervention-run", complete: true });
    stream.enqueue(`data: ${JSON.stringify({ conversationId: "web-session:new", revision: "hint" })}\n\n`);
    await parking.waitFor({ state: "hidden" });
    await chat.getByText("identical follow-up", { exact: true }).nth(2).waitFor();
    const ordered = await chat.locator('[data-slot="bubble-content"]').allTextContents();
    expect(ordered.slice(-5)).toEqual(["Before steering", "identical follow-up", "identical follow-up", "After steering", "identical follow-up"]);
    expect(await chat.locator('[data-slot="message"][data-align="end"] [data-slot="bubble"][data-align="end"][data-variant="secondary"]').filter({ hasText: "identical follow-up" }).count()).toBe(3);
    expect(await chat.locator('[data-slot="message"][data-align="start"] [data-slot="bubble"][data-align="start"][data-variant="ghost"]').filter({ hasText: "After steering" }).count()).toBe(1);
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
    await page.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "main" }).waitFor();
    await goToSession(page, "will vanish");
    await page.goBack();
    await page.locator("textarea").fill("stay with main");
    const switchedResponse = page.waitForResponse("**/api/Prompt");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await switchedResponse;
    expect(ctrl.prompt.slice(-2)).toEqual(["web-session:new:$agent main", "web-session:new:stay with main"]);
    ctrl.holdPrompt = true;
    await page.locator("textarea").fill("created draft to discard");
    await page.keyboard.press("Control+Alt+n");
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
    await goToSession(page, "will vanish");
    await page.waitForURL("**/s/Z29uZQ");
    await page.keyboard.press("Meta+Alt+n");
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
    await goToSession(page, "will vanish");
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

    await goToSession(page, "saved preview");
    await page.waitForURL("**/s/a2VwdA");
    ctrl.promptStarted = Promise.withResolvers();
    await page.getByPlaceholder("Message or $command").fill("held in A");
    await page.getByRole("button", { name: "Send" }).click();
    await ctrl.promptStarted.promise;
    // A held prompt must not make another session's History report it as running.
    const otherHistory = await fetch(`${origin}/api/History`, { method: "POST", body: JSON.stringify({ id: "gone" }) });
    expect((await otherHistory.json()).running).toBe(false);
    await goToSession(page, "will vanish");
    await page.waitForURL("**/s/Z29uZQ");
    await page.getByPlaceholder("Message or $command").fill("B draft");
    await hidden(page, "Working…");
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
    await page.keyboard.press("Control+p");
    await search.fill("preview");
    await shown(sessionPalette, "filter preview");
    ctrl.yieldBatches = async function* () {
      for (const session of [row("kept", "merged-live"), row("extra", "prefix-only"), ...Array.from({ length: 446 }, (_, i) => ({ ...row(i < 2 ? `slack-thread:C:${i + 2}` : `prefix-${i}`, `partial preview ${i}`), agent: i === 1 ? "other" : "main" }))]) {
        yield batch([session]);
      }
      await new Promise(() => {});
    };
    blocked.resolve();
    await search.fill("");
    await shown(sessionPalette, "merged-live");
    await shown(sessionPalette, "prefix-only");
    await shown(sessionPalette, "will vanish");

    expect(page.url()).toBe(selectedURL);
    for (let i = 0; i < 8; i++) await shown(sessionPalette, `partial preview ${i}`);
    await search.fill("partial preview 445");
    await shown(sessionPalette, "partial preview 445");
    await page.keyboard.press("Escape");

    await page.evaluate(() => sessionStorage.setItem("delaySnapshotOpen", "1"));
    await page.reload();
    await page.waitForFunction(() => (window as unknown as { __openHeld: boolean }).__openHeld);
    await page.keyboard.press("Control+p");
    await shown(sessionPalette, "merged-live");
    await hidden(sessionPalette, "will vanish");
    await page.evaluate(() => (window as unknown as { __releaseOpen: () => void }).__releaseOpen());
    await shown(sessionPalette, "will vanish");
    await shown(sessionPalette, "merged-live");

    blocked = Promise.withResolvers();
    await page.reload();
    await openSessions(page);
    await shown(sessionPalette, "saved preview");
    await hidden(sessionPalette, "prefix-only");

    ctrl.yieldBatches = async function* () {
      yield batch([row("kept", "should-not-save")], { upstreamSuccess: true, summariesComplete: true });
      throw new Error("late");
    };
    blocked.resolve();
    await page.waitForTimeout(500);
    blocked = Promise.withResolvers();
    await page.reload();
    await openSessions(page);
    await shown(sessionPalette, "saved preview");
    await hidden(sessionPalette, "should-not-save");

    ctrl.yieldBatches = async function* () {
      yield batch([row("kept", "wire-terminal")], { upstreamSuccess: true, summariesComplete: true });
      await wireTail.promise;
    };
    blocked.resolve();
    await shown(sessionPalette, "wire-terminal");
    await page.keyboard.press("Control+p");
    await sessionPalette.evaluate(async (node: HTMLElement) => { await Promise.all(node.getAnimations().map((animation) => animation.finished)); });
    const searchBounds = await search.boundingBox();
    const rowBounds = await sessionPalette.getByText("wire-terminal", { exact: true }).boundingBox();
    listResponse!.close();
    const staleIndicator = page.getByRole("img", { name: "Conversation list may be out of date" });
    await staleIndicator.waitFor({ state: "visible" });
    expect(await search.boundingBox()).toEqual(searchBounds);
    expect(await sessionPalette.getByText("wire-terminal", { exact: true }).boundingBox()).toEqual(rowBounds);
    await search.focus();
    await page.keyboard.press("Shift+Tab");
    await shown(page, "Conversation list may be out of date");
    await search.fill("tag:missing-stale");
    await shown(sessionPalette, "loading...");
    await hidden(sessionPalette, "No matches");
    wireTail.resolve();
    blocked = Promise.withResolvers();
    await page.reload();
    await openSessions(page);
    await shown(sessionPalette, "saved preview");

    const putsBeforeIncomplete = await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts);
    const beforeIncomplete = await page.evaluate(snapshot, ["alice", "test-protocol"]);
    ctrl.yieldBatches = async function* () {
      yield batch([row("kept", ""), row("ghost", "")], { summariesComplete: false });
      yield batch([], { upstreamSuccess: true, summariesComplete: false });
    };
    blocked.resolve();
    await page.getByText("loading...", { exact: true }).first().waitFor({ state: "visible", timeout: 15_000 });
    await page.keyboard.press("Control+p");
    await search.fill("tag:zzz-nope");
    await shown(sessionPalette, "loading...");
    await staleIndicator.waitFor({ state: "visible" });
    expect(await page.evaluate(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts)).toBe(putsBeforeIncomplete);
    expect(await page.evaluate(snapshot, ["alice", "test-protocol"])).toEqual(beforeIncomplete);
    ctrl.yieldBatches = complete([row("kept", "backfilled preview")]);
    await shown(sessionPalette, "No matches");
    await hidden(sessionPalette, "loading...");
    expect(await staleIndicator.count()).toBe(0);
    await search.fill("");
    await shown(sessionPalette, "backfilled preview");
    await page.waitForFunction(() => (window as unknown as { __snapshotPuts: number }).__snapshotPuts > 0);
    expect(await page.evaluate(snapshot, ["alice", "test-protocol"])).toEqual([row("kept", "backfilled preview")]);
    blocked = Promise.withResolvers();
    await page.reload();
    await openSessions(page);
    await shown(sessionPalette, "backfilled preview");

    ctrl.yieldBatches = complete([]);
    blocked.resolve();
    await page.getByText("backfilled preview", { exact: true }).first().waitFor({ state: "hidden", timeout: 15_000 });
    await hidden(page, "backfilled preview");
    await hidden(page, "saved preview");
    await page.keyboard.press("Control+p");
    await search.fill("empty-search");
    await shown(sessionPalette, "No matches");
    await hidden(sessionPalette, "loading...");
    blocked = Promise.withResolvers();
    await page.reload();
    await openSessions(page);
    await sessionPalette.waitFor();
    await hidden(sessionPalette, "saved preview");

    ctrl.yieldBatches = complete([row("kept", "saved preview"), row("gone", "will vanish")]);
    blocked.resolve();
    await shown(sessionPalette, "saved preview");
    await shown(sessionPalette, "will vanish");
    await sessionPalette.getByRole("button").filter({ hasText: "will vanish" }).click();
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
      yield batch([{ ...row("gone", "alice held preview"), tags: ["alice-only-tag"] }], { owner: "alice" });
      ownerStarted.resolve();
      await ownerTail.promise;
      yield* complete([row("gone", "late alice preview")], "alice")();
    };
    await ownerStarted.promise;
    await page.keyboard.press("Control+p");
    await shown(sessionPalette, "alice held preview");
    await sessionPalette.locator('span[title="main · alice-only-tag"]').waitFor();
    const oldRequest = await ownerRequest;
    // Wait for Alice's stream to end however it ends. The test's own timeout bounds this, not a 30s
    // waitForEvent timer started many steps before the tail is released.
    const oldResponse = new Promise<void>((resolve) => {
      for (const event of ["requestfinished", "requestfailed"]) page.on(event, (request: unknown) => { if (request === oldRequest) resolve(); });
    });
    blocked = Promise.withResolvers();
    ctrl.username = "bob";
    await page.evaluate(() => {
      (window as unknown as { __delayOpen: boolean; __openHeld: boolean }).__delayOpen = true;
      (window as unknown as { __openHeld: boolean }).__openHeld = false;
    });
    await page.waitForFunction(() => (window as unknown as { __openHeld: boolean }).__openHeld);
    await hidden(sessionPalette, "will vanish");
    await hidden(sessionPalette, "bob preview");
    expect(await sessionPalette.locator('span[title*="alice-only-tag"]').count()).toBe(0);
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
    await openSessions(page);
    await shown(sessionPalette, "bob preview");

    const protocolReload = page.waitForEvent("load");
    ctrl.protocol = "test-protocol-2";
    await protocolReload;
    await openSessions(page);
    await sessionPalette.waitFor();
    await hidden(page, "Refreshing");
    await hidden(sessionPalette, "bob preview");
    ctrl.yieldBatches = complete([row("gone", "new protocol preview")], "bob");
    blocked.resolve();
    await shown(page, "new protocol preview");

    // Recovery title matches must find Bob's replacement row, not the earlier Alice rows.
    for (const rejection of ["mismatch", "unauthorized"] as const) {
      await page.keyboard.press("Control+p");
      await sessionPalette.waitFor();
      identityHold = Promise.withResolvers();
      ctrl.yieldBatches = async function* () {
        if (rejection === "unauthorized") throw new RPCError("denied", 16);
        yield batch([row("other-owner", "unconfirmed owner preview")], { owner: "unconfirmed" });
      };
      await page.getByText("new protocol preview", { exact: true }).first().waitFor({ state: "hidden", timeout: 15_000 });
      await hidden(page, "new protocol preview");
      await hidden(page, "unconfirmed owner preview");
      await page.keyboard.press("Control+p");
      await search.fill("preview");
      await hidden(page, "new protocol preview");
      blocked = Promise.withResolvers();
      ctrl.yieldBatches = complete([row("gone", "new protocol preview")], "bob");
      identityHold.resolve();
      await shown(sessionPalette, "new protocol preview");
      await page.keyboard.press("Escape");
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
    await page.keyboard.press("Control+p");
    await shown(sessionPalette, "fast same-owner recovery");
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
        await openSessions(storagePage);
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
        await storagePage.keyboard.press("Escape");
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
      ctrl.yieldBatches = complete([{ ...row("slack-thread:C:running-chat", "latest user message"), running: true }], "bob");
      await runningPage.setViewportSize({ width, height: 844 });
      await runningPage.goto(origin);
      await openSessions(runningPage);
      const runningPalette = runningPage.getByRole("dialog", { name: "Go to session or command", exact: true });
      const indicator = runningPalette.getByRole("img", { name: "Turn running", exact: true });
      await indicator.waitFor();
      await runningPalette.evaluate(async (element: HTMLElement) => {
        await Promise.all(element.getAnimations().map((animation) => animation.finished));
      });
      const runningRow = runningPalette.getByRole("button").filter({ hasText: "latest user message" });
      const before = await runningRow.boundingBox();
      expect(await runningRow.locator("span[title]:not([data-slot])").getAttribute("title")).toBe("room · main");
      expect((await runningRow.locator("span[title]:not([data-slot])").boundingBox())!.x).toBe((await runningRow.locator('[data-slot="session-title"]').boundingBox())!.x);
      expect((await indicator.boundingBox())!.x).toBeGreaterThan((await runningRow.locator("span[title]:not([data-slot])").boundingBox())!.x);
      ctrl.yieldBatches = complete([{ ...row("slack-thread:C:running-chat", "latest assistant reply"), running: false }], "bob");
      await runningPalette.getByText("latest assistant reply", { exact: true }).waitFor();
      expect(await indicator.count()).toBe(0);
      expect(await runningPalette.getByRole("button").filter({ hasText: "latest assistant reply" }).boundingBox()).toEqual(before);
    }
    await runningPage.close();
    // Exercise the palette layout and suggestions against the built App.
    const matrixRows = [
      { ...row("slack-thread:C:winner", "row and origin union"), name: "Winner", pinned: true, forkedFrom: "original", tags: ["customer", "Needs review", 'say "hello"'] },
      row("slack-thread:C:another", "Another unpinned chat"),
      { ...row("slack-thread:C:other-agent", "Other agent"), agent: "other", pinned: true },
      { ...row("slack-thread:D:other-room", "Other room"), title: "different", pinned: true },
      row("slack-thread:C:unpinned", "Unpinned chat"),
      { ...row("slack-thread:C:customer-union", "Customer chat"), tags: ["customer"] },
    ];
    ctrl.yieldBatches = complete(matrixRows);
    for (const hasTouch of [false, true]) {
      const layoutPage = await browser.newPage({ hasTouch, viewport: { width: 390, height: 844 } });
      await layoutPage.goto(origin);
      await layoutPage.getByRole("button", { name: "Search sessions and commands", exact: true }).waitFor();
      await layoutPage.keyboard.press("Control+p");
      const layoutDialog = layoutPage.getByRole("dialog", { name: "Go to session or command", exact: true });
      await layoutDialog.waitFor();
      for (const width of [390, 1024, 1280]) {
        await layoutPage.setViewportSize({ width, height: 844 });
        // Keyboards resize the visual viewport without resizing the layout viewport.
        await layoutPage.evaluate(() => {
          const viewport = window.visualViewport!;
          Object.defineProperty(viewport, "height", { configurable: true, value: 360 });
          Object.defineProperty(viewport, "offsetTop", { configurable: true, value: 24 });
          viewport.dispatchEvent(new Event("resize"));
          viewport.dispatchEvent(new Event("scroll"));
        });
        await layoutPage.waitForFunction((touch: boolean) => {
          const style = getComputedStyle(document.querySelector('[role="dialog"]')!);
          return Math.abs(parseFloat(style.top) - (touch ? 40 : 168.8)) < 0.1 && Math.abs(parseFloat(style.maxHeight) - (touch ? 328 : 633)) < 0.1;
        }, hasTouch);
        const layout = await layoutDialog.evaluate((element: HTMLElement) => ({
          top: parseFloat(getComputedStyle(element).top),
          maxHeight: parseFloat(getComputedStyle(element).maxHeight),
          height: element.offsetHeight,
        }));
        expect(layout.top).toBeCloseTo(hasTouch ? 40 : 168.8, 0);
        expect(layout.maxHeight).toBeCloseTo(hasTouch ? 328 : 633, 0);
        if (hasTouch) expect(layout.height).toBeLessThanOrEqual(328);
      }
      await layoutPage.close();
    }
    const matrix = await context.newPage();
    const suggestionAgents = [{ name: "main", model: "gpt" }, ...Array.from({ length: 10 }, (_, i) => ({ name: `agent${i}` })), { name: "other", model: "gpt" }];
    await matrix.route("**/api/ListAgents", (route: { fulfill(options: { json: unknown }): Promise<void> }) => route.fulfill({ json: { agents: suggestionAgents } }));
    await matrix.goto(origin);
    await openSessions(matrix);
    const matrixDialog = matrix.getByRole("dialog", { name: "Go to session or command", exact: true });
    const matrixSearch = matrixDialog.getByPlaceholder("Search sessions");
    await matrixDialog.getByText("Winner", { exact: true }).waitFor();
    await matrix.keyboard.press("Escape");
    await matrixDialog.waitFor({ state: "hidden" });
    for (const entry of ["Meta+p", "Control+p"]) {
      await matrix.keyboard.press(entry);
      await matrixSearch.waitFor();
      expect(await matrixSearch.inputValue()).toBe("");
      await matrix.waitForFunction(() => document.activeElement?.getAttribute("placeholder") === "Search sessions and commands");
      expect(await matrixSearch.evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
      expect(await matrixDialog.getByRole("button", { name: /^(agent:|room:)/ }).count()).toBe(0);
      expect(await matrixDialog.locator('li > button.bg-accent [data-slot="session-title"]').innerText()).toBe("Winner");
      await matrixSearch.press("ArrowDown");
      expect(await matrixDialog.locator('li > button.bg-accent [data-slot="session-title"]').innerText()).toBe("Another unpinned chat");
      await matrix.keyboard.press("Escape");
      await matrixDialog.waitFor({ state: "hidden" });
    }
    await matrix.keyboard.press("Control+p");
    await matrixSearch.fill("tag:customer ");
    const matrixPills = matrixDialog.getByRole("button", { name: /^tag:/ });
    await matrixPills.first().waitFor();
    expect(await matrixSearch.inputValue()).toBe("");
    await matrixPills.first().click();
    const operatorChoices = matrixDialog.getByRole("button", { name: /^(?:pinned|forked|newest|oldest)$/ });
    await matrixSearch.fill("is:");
    await matrixDialog.getByRole("button", { name: "pinned", exact: true }).waitFor();
    expect(await operatorChoices.allTextContents()).toEqual(["pinned", "forked"]);
    await matrixSearch.fill("sort:");
    await matrixDialog.getByRole("button", { name: "newest", exact: true }).waitFor();
    expect(await operatorChoices.allTextContents()).toEqual(["newest", "oldest"]);
    await matrixSearch.fill("agent:");
    const lastSuggestion = matrixDialog.getByRole("button", { name: "agent9", exact: true });
    await lastSuggestion.waitFor();
    for (let i = 0; i < 10; i++) {
      await matrixSearch.press("ArrowDown");
      await matrixDialog.getByRole("button", { name: `agent${i}`, exact: true }).and(matrixDialog.locator(".bg-accent")).waitFor();
    }
    expect(await lastSuggestion.evaluate((node: HTMLElement) => {
      const bounds = node.getBoundingClientRect();
      return node.classList.contains("bg-accent") && node.contains(document.elementFromPoint(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2));
    })).toBe(true);
    await matrixSearch.press("Escape");
    expect(await matrixDialog.isVisible()).toBe(true);
    expect(await matrixSearch.inputValue()).toBe("");
    await matrixSearch.fill("winner");
    for (const width of [1280, 390, 320]) {
      await matrix.setViewportSize({ width, height: 844 });
      await matrixDialog.screenshot({ path: path.join(screenshots, `unified-search-${width}.png`) });
      expect(await matrixDialog.evaluate((node: HTMLElement) => node.scrollWidth <= node.clientWidth)).toBe(true);
      expect((await matrixSearch.boundingBox())!.width).toBeGreaterThanOrEqual(96);
    }
    await matrix.keyboard.press("Escape");
    const searchButton = matrix.getByRole("button", { name: "Search sessions and commands", exact: true });
    await searchButton.scrollIntoViewIfNeeded();
    expect((await searchButton.boundingBox())!.width).toBeGreaterThanOrEqual(44);
    await searchButton.focus();
    await matrix.keyboard.press("Control+p");
    expect(await matrixSearch.inputValue()).toBe("");
    await matrixSearch.press("Escape");
    await matrix.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Search sessions and commands");
    await matrix.mouse.move(0, 0);
    await matrix.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await matrix.getByRole("button", { name: "Hide bottom navigation" }).click();
    expect(await searchButton.isVisible()).toBe(false);
    await matrix.keyboard.press("Meta+p");
    await matrixSearch.waitFor();
    await matrixSearch.press("Escape");
    await matrix.getByRole("button", { name: "Show bottom navigation" }).click();
    await matrix.close();

    ctrl.searchError = true;
    const failure = await browser.newPage();
    await failure.goto(origin);
    await openSessions(failure);
    const failureDialog = failure.getByRole("dialog", { name: "Go to session or command", exact: true });
    await failureDialog.getByText("Winner", { exact: true }).waitFor();
    await failureDialog.getByPlaceholder("Search sessions").fill("is:pinned absent"); // Only a filter asks the server.
    await failureDialog.getByRole("alert").filter({ hasText: "Search failed: search unavailable" }).waitFor();
    await failureDialog.getByText("Search incomplete", { exact: true }).waitFor();
    expect(await failureDialog.getByText("No matches", { exact: true }).count()).toBe(0);
    ctrl.searchError = false;
    await failureDialog.getByPlaceholder("Search sessions").press("End");
    await failureDialog.getByPlaceholder("Search sessions").pressSequentially(" "); // The next keystroke retries.
    await failureDialog.getByText("No matches", { exact: true }).waitFor();
    expect(await failureDialog.getByRole("alert").count()).toBe(0);
    await failure.close();
    ctrl.yieldBatches = complete(Array.from({ length: 96 }, (_, index) => ({ ...row(`perf-${index}`, `Search fixture ${index}`) })));
    const many = await browser.newPage();
    await many.addInitScript(() => {
      const fetch = window.fetch;
      (window as unknown as { searchFetches: number }).searchFetches = 0;
      window.fetch = new Proxy(fetch, { apply(target, thisArg, args: Parameters<typeof fetch>) {
        if (String(args[0]).endsWith("/api/SearchSessions")) (window as unknown as { searchFetches: number }).searchFetches++;
        return Reflect.apply(target, thisArg, args);
      } });
    });
    await many.goto(origin);
    await openSessions(many);
    const manyDialog = many.getByRole("dialog", { name: "Go to session or command", exact: true });
    await many.waitForFunction(() => document.querySelectorAll('[role="dialog"] li > button').length === 96);
    await manyDialog.getByPlaceholder("Search sessions").pressSequentially("is:pinned fixture 0", { delay: 20 });
    await many.waitForFunction(() => document.querySelector('[role="dialog"] ul[data-pending="true"]') !== null); // The title matches stay, dimmed, until the response.
    await many.waitForTimeout(300);
    expect(await many.evaluate(() => (window as unknown as { searchFetches: number }).searchFetches)).toBe(1); // Typing settles into one request.
    manySearchHold.resolve();
    // The shown list follows the response in a background render.
    await many.waitForFunction(() => document.querySelectorAll('[role="dialog"] li > button [data-slot="session-title"]').length === 1);
    expect(await manyDialog.getByText("Search fixture 0", { exact: true }).count()).toBe(1);
    await manyDialog.getByPlaceholder("Search sessions").fill("is:pinned not-in-any-row");
    // While the next response is held, the earlier response's rows are matched against the new text at once.
    await manyDialog.getByText("Search fixture 0", { exact: true }).waitFor({ state: "detached" });
    lastSearchHold.resolve();
    await manyDialog.getByText("No matches", { exact: true }).waitFor();
    expect(await many.evaluate(() => (window as unknown as { searchFetches: number }).searchFetches)).toBe(2);
    await many.close();
    ctrl.rows = [
      row("slack-thread:C:active", "matching active"),
      { ...row("slack-thread:C:resolved", "matching resolved"), tags: ["customer", "resolved"] },
      { ...row("slack-thread:C:other", "matching other agent"), agent: "other" },
      row("web-session:other-room", "matching other room"),
    ];
    ctrl.yieldBatches = complete(ctrl.rows);
    const pendingPage = await context.newPage();
    const catalogGate = Promise.withResolvers<void>();
    await pendingPage.route("**/api/ListAgents", async (route: { fulfill(options: { json: unknown }): Promise<void> }) => {
      await catalogGate.promise;
      await route.fulfill({ json: { agents: suggestionAgents } });
    });
    await pendingPage.goto(origin);
    await openSessions(pendingPage);
    const pendingDialog = pendingPage.getByRole("dialog", { name: "Go to session or command", exact: true });
    const pendingSearch = pendingDialog.getByPlaceholder("Search sessions");
    await shown(pendingDialog, "matching active");
    for (const prefix of ["agent:main", "room:missing"]) {
      await pendingSearch.fill(prefix);
      await pendingSearch.press("Enter"); // Sends the term; its empty response opens nothing.
      await pendingPage.waitForTimeout(300);
      expect(new URL(pendingPage.url()).pathname).toBe("/");
      expect(await pendingDialog.isVisible()).toBe(true);
      expect(await pendingSearch.inputValue()).toBe(prefix);
    }
    catalogGate.resolve();
    await pendingSearch.fill("agent:ma");
    await pendingDialog.getByRole("button", { name: "main gpt", exact: true }).waitFor();
    await pendingSearch.press("Enter");
    await pendingDialog.getByRole("button", { name: "agent:main", exact: true }).waitFor();
    await pendingPage.close();
    const navPage = await context.newPage();
    await navPage.route("**/api/ListAgents", (route: { fulfill(options: { json: unknown }): Promise<void> }) => route.fulfill({ json: { agents: suggestionAgents } }));
    await navPage.goto(origin);
    await openSessions(navPage);
    const navPalette = navPage.getByRole("dialog", { name: "Go to session or command", exact: true });
    const navSearch = navPalette.getByPlaceholder("Search sessions");
    await navSearch.fill("agent:");
    await navPalette.getByRole("button", { name: "main gpt", exact: true }).waitFor();
    await navPage.keyboard.press("Enter");
    await navPalette.getByRole("button", { name: "agent:main", exact: true }).waitFor();
    await navSearch.fill("room:ro");
    await navPalette.getByRole("button", { name: "room", exact: true }).waitFor();
    await navPage.keyboard.press("Enter");
    await navPalette.getByRole("button", { name: "room:room", exact: true }).waitFor();
    await navSearch.fill("matching");
    await shown(navPalette, "matching active");
    await shown(navPalette, "matching resolved");
    await hidden(navPalette, "matching other agent");
    await hidden(navPalette, "matching other room");
    await navPage.keyboard.press("Escape");
    const footer = navPage.locator("footer");
    const openPage = async (name: string) => {
      await navPage.keyboard.press("Control+Shift+p");
      await navPage.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: ({ Cron: "Cron: Dashboard", Agents: "List Agents", Skills: "List Skills", Config: "Settings" } as Record<string, string>)[name], exact: true }).click();
      await navPage.locator('[data-slot="dialog-overlay"]').waitFor({ state: "hidden" });
      // The top-left corner is a tab, whose tooltip would take the next Escape.
      await navPage.mouse.move(0, navPage.viewportSize()!.height / 2);
      await navPage.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    };
    const navMain = navPage.locator("main");
    const navTabs = navPage.getByRole("tablist", { name: "Open tabs" });
    // Closing a page closes its tab, and a neighbouring tab becomes active.
    const closed = async (name: string) => {
      await navPage.waitForURL((url: URL) => url.pathname !== `/${name.toLowerCase()}`);
      expect(await navTabs.getByRole("tab", { name: name === "Config" ? "Settings" : name, exact: true }).count()).toBe(0);
    };
    // Escape first closes search suggestions without leaving the page.
    await navPage.keyboard.press("Control+Shift+p");
    await navPage.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await navPage.getByRole("dialog").waitFor({ state: "detached" });
    await navPage.waitForURL("**/search");
    await navMain.getByPlaceholder("Search or agent: or room:").fill("agent:");
    for (let i = 0; i < 10; i++) await navPage.keyboard.press("ArrowDown");
    expect(await navMain.getByRole("button", { name: "agent9", exact: true }).evaluate((node: HTMLElement) => {
      const bounds = node.getBoundingClientRect();
      return node.classList.contains("bg-accent") && node.contains(document.elementFromPoint(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2));
    })).toBe(true);
    await navPage.keyboard.press("Escape");
    expect(new URL(navPage.url()).pathname).toBe("/search");
    expect(await navMain.getByPlaceholder("Search or agent: or room:").inputValue()).toBe("");
    await navPage.keyboard.press("Escape");
    await closed("Search");
    for (const name of ["Cron", "Agents", "Skills", "Config"]) {
      await openPage(name);
      await navPage.waitForURL(`${origin}/${name.toLowerCase()}`);
      if (name === "Config") {
        await shown(navPage.locator("main"), "Configured user");
        await shown(navPage.locator("main"), "connected@example.com");
      }
      await navPage.keyboard.press("Escape");
      await closed(name);
    }
    const chatPath = `/s/${Buffer.from("slack-thread:C:active").toString("base64url")}`;
    for (const width of [1280, 390]) {
      await navPage.setViewportSize({ width, height: 844 });
      await goToSession(navPage, "matching active");
      await navPage.waitForURL(`${origin}${chatPath}`);
      await navPage.getByPlaceholder("Message or $command").fill("keep this chat draft");
      for (const name of ["Cron", "Agents", "Skills", "Config"]) {
        await openPage(name);
        await navPage.waitForURL(`${origin}/${name.toLowerCase()}`);
        const heading = navMain.getByRole("heading", { name, exact: true });
        const mainBox = (await navMain.boundingBox())!;
        const headerBox = (await heading.locator("..").boundingBox())!;
        const headingBox = (await heading.boundingBox())!;
        expect(headerBox.x).toBe(mainBox.x + Math.max(0, (mainBox.width - 768) / 2) + 16);
        const close = navMain.getByRole("button", { name: "Close", exact: true });
        expect(headingBox.x).toBe(headerBox.x);
        if (width === 390) expect(await close.count()).toBe(0);
        else await close.waitFor();
        expect(headingBox.y).toBe(mainBox.y + 16);
        expect(headingBox.height).toBe(28);
        expect(await heading.evaluate((element: HTMLElement) => getComputedStyle(element).padding)).toBe("0px");
        if (name === "Cron") {
          const searchBox = (await navMain.getByRole("textbox").first().boundingBox())!;
          expect(searchBox.x).toBe(headerBox.x);
          expect(searchBox.y).toBe(headingBox.y + headingBox.height + 24);
        }
        if (width === 1280 && name === "Skills") await close.click();
        else await navPage.keyboard.press("Escape");
        await closed(name);
        await navTabs.getByRole("tab", { name: "matching active", exact: true }).click();
        await navPage.waitForURL(`${origin}${chatPath}`);
      }
      await openPage("Cron");
      await navPage.waitForURL(`${origin}/cron`);
      await openPage("Skills");
      await navPage.waitForURL(`${origin}/skills`);
      expect(await navTabs.getByRole("tab", { name: "Cron", exact: true }).count()).toBe(0); // Skills replaced the Cron preview tab.
      await navPage.keyboard.press("Escape");
      await closed("Skills");
      await navTabs.getByRole("tab", { name: "matching active", exact: true }).click();
      await navPage.waitForURL(`${origin}${chatPath}`);
      if (width === 390) await navPage.keyboard.press("Escape");
      expect(await navPage.getByPlaceholder("Message or $command").inputValue()).toBe("keep this chat draft");
      const newChat = footer.getByRole("button", { name: "New session", exact: true });
      const groupBox = await newChat.locator("..").boundingBox();
      if (width === 390) {
        const scrollBox = (await newChat.locator("../..").boundingBox())!;
        expect(Math.abs(scrollBox.x + scrollBox.width / 2 - width / 2)).toBeLessThan(1);
        expect(scrollBox.x + scrollBox.width).toBeLessThanOrEqual(width);
      } else {
        expect(Math.abs(groupBox!.x + groupBox!.width / 2 - width / 2)).toBeLessThan(1);
      }
      expect(await newChat.locator("..").getByRole("button").count()).toBe(4);
      const creations = ctrl.createdAgents.length;
      await newChat.click();
      await navPage.waitForURL(`${origin}/`);
      expect(ctrl.createdAgents.length).toBe(creations);
      if (width === 390) await navPage.keyboard.press("Escape");
      expect(await navPage.getByPlaceholder("Message a new session").inputValue()).toBe("");
      await navPage.getByRole("button", { name: "Choose agent", exact: true }).click();
      await navPage.getByRole("menuitemradio", { name: "other gpt", exact: true }).click();
      await navPage.getByPlaceholder("Message a new session").fill("fresh draft");
      await openPage("Config");
      await navPage.waitForURL(`${origin}/config`);
      await navPage.goBack();
      await navPage.waitForURL(`${origin}/`);
      if (width === 390) await navPage.keyboard.press("Escape");
      expect(await navPage.getByPlaceholder("Message a new session").inputValue()).toBe("fresh draft");
      await navPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
      await newChat.click();
      if (width === 390) await navPage.keyboard.press("Escape");
      await navPage.waitForFunction(() => document.querySelector("textarea")?.value === "" && !document.querySelector("textarea")?.closest("fieldset")?.disabled);
      expect(await navPage.getByPlaceholder("Message a new session").inputValue()).toBe("");
      await navPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "main" }).waitFor();
      expect(ctrl.createdAgents.length).toBe(creations);
    }
    await navPage.close();
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
      await expected.press("Escape");
      await cronPage.getByRole("button", { name: /^Expected daily at / }).last().hover();
      // The page scroller clips overflow, so the tooltip's far edge must be painted, not just laid out.
      expect(await cronPage.getByRole("tooltip").evaluate((node: HTMLElement) => { const box = node.getBoundingClientRect(); return node.contains(document.elementFromPoint(box.right - 1, box.top + box.height / 2)); })).toBe(true);
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
      await cronPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
      await cronPage.getByRole("button", { name: "Choose agent", exact: true }).click();
      await cronPage.getByRole("menuitemradio").first().waitFor();
      expect(await cronPage.getByRole("menuitemradio").count()).toBe(1);
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
    await cronPage.getByRole("tab", { name: "daily", exact: true }).waitFor();
    await cronPage.close();

    ctrl.history = Array.from({ length: 20 }, (_, i) => [
      { role: "user", text: `Prompt ${i + 1}` },
      { role: "thinking", text: `Trace ${i + 1}\n` + "Working through the task.\n".repeat(40) },
      { role: "assistant", text: `Reply ${i + 1}` },
      { role: "assistant", text: "Repeated reply" },
      { role: "assistant", text: "Repeated reply" },
    ].map((item, index) => ({ ...item, entryKey: `turn-${i}`, itemId: `turn-${i}:${index}`, inputId: "", turnId: "", complete: true }))).flat();
    ctrl.running = false;
    const transcriptPage = await browser.newPage();
    for (const width of [1280, 390]) {
      transcriptStreams.clear();
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
      ctrl.history = [...ctrl.history.filter((message) => message.entryKey !== "live"), { entryKey: "live", itemId: "live:0", inputId: "", role: "assistant", text: `Live reply ${width}`, turnId: "live", complete: false }];
      (await transcriptStream(`jump-${width}`).promise).enqueue(`data: ${JSON.stringify({ conversationId: `jump-${width}`, revision: "hint" })}\n\n`);
      await transcriptPage.getByText(`Live reply ${width}`, { exact: true }).waitFor({ state: "attached" });
      expect(await scroll.evaluate((el: HTMLElement) => el.scrollTop)).toBeLessThan(50);
      await transcriptPage.getByRole("button", { name: "Scroll to latest", exact: true }).click();
      await transcriptPage.waitForFunction(() => {
        const el = document.querySelector("#transcript-scroll")!;
        return el.scrollHeight - el.clientHeight - el.scrollTop < 2;
      });
      ctrl.history[ctrl.history.length - 1] = { ...ctrl.history.at(-1)!, text: `\nFollowing latest ${width}\n` + "More streamed text.\n".repeat(40) };
      (await transcriptStream(`jump-${width}`).promise).enqueue(`data: ${JSON.stringify({ conversationId: `jump-${width}`, revision: "hint" })}\n\n`);
      await transcriptPage.getByText(`Following latest ${width}`, { exact: false }).waitFor({ state: "attached" });
      await transcriptPage.waitForFunction(() => {
        const el = document.querySelector("#transcript-scroll")!;
        return el.scrollHeight - el.clientHeight - el.scrollTop < 2;
      });
      // Reaching the bottom with a wheel, or with the scrollbar after a jump, keeps following new text.
      await scroll.hover();
      for (const [step, reach] of [
        () => transcriptPage.mouse.wheel(0, 400),
        async () => {
          await rail.getByRole("button", { name: "Turn 1: Prompt 1", exact: true }).click();
          await transcriptPage.waitForFunction(() => document.querySelector("#transcript-scroll")!.scrollTop < 50);
          await scroll.evaluate((el: HTMLElement) => new Promise((resolve) => {
            el.addEventListener("scroll", resolve, { once: true });
            el.scrollTop = el.scrollHeight;
          }));
        },
      ].entries()) {
        await reach();
        ctrl.history[ctrl.history.length - 1] = { ...ctrl.history.at(-1)!, text: `\nStill following ${width} ${step}\n` + "More streamed text.\n".repeat(40 * (step + 2)) };
        (await transcriptStream(`jump-${width}`).promise).enqueue(`data: ${JSON.stringify({ conversationId: `jump-${width}`, revision: "hint" })}\n\n`);
        await transcriptPage.getByText(`Still following ${width} ${step}`, { exact: false }).waitFor({ state: "attached" });
        await transcriptPage.waitForFunction(() => {
          const el = document.querySelector("#transcript-scroll")!;
          return el.scrollHeight - el.clientHeight - el.scrollTop < 2;
        });
      }
      await rail.getByRole("button", { name: "Turn 20: Prompt 20", exact: true }).focus();
      await transcriptPage.keyboard.press("Enter");
      const last = transcriptPage.getByRole("region", { name: "Turn 20", exact: true });
      expect(await last.evaluate((el: HTMLElement) => el === document.activeElement)).toBe(true);
      await transcriptPage.mouse.move(0, 0);
      await previewBox.waitFor({ state: "hidden" });
      await last.locator('[data-slot="trace-collapsible-trigger"]').first().click();
      await rail.getByRole("button", { name: "Turn 1: Prompt 1", exact: true }).click();
      await transcriptPage.waitForFunction(() => document.querySelector("#transcript-scroll")!.scrollTop < 50);
    }
    const codeText = "  echo '<literal>'\n" + "  long output\n".repeat(100) + "x".repeat(400) + "\n";
    ctrl.history = [
      { role: "user", text: "Show the output" },
      { role: "tool", text: "bash\n{}", toolName: "bash", toolCallId: "bounded" },
      { role: "tool", text: codeText, toolCallId: "bounded" },
      { role: "assistant", text: "Before\n```sh\n" + codeText + "```\nAfter" },
    ].map((item, index) => ({ ...item, entryKey: "code", itemId: `code:${index}`, inputId: "", turnId: "", complete: true }));
    await transcriptPage.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    for (const width of [1280, 390]) {
      ctrl.history = ctrl.history.filter((message) => message.entryKey !== "fenced-stream");
      transcriptStreams.clear();
      await transcriptPage.setViewportSize({ width, height: 600 });
      await transcriptPage.goto(`${origin}/s/${Buffer.from(`code-${width}`).toString("base64url")}`);
      await transcriptPage.locator('pre[aria-label="sh"]').waitFor();
      // The default Compact level folds the call into a closed Chain of Thought row.
      await transcriptPage.getByRole("button", { name: "Used 1 tool", exact: true }).click();
      await transcriptPage.getByRole("button", { name: "bash", exact: true }).click();
      expect(await transcriptPage.locator("#transcript-scroll pre").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["bash", "sh"]);
      for (const label of ["bash", "sh"]) {
        const panel = transcriptPage.locator(`#transcript-scroll pre[aria-label="${label}"]`);
        const scroller = panel;
        await panel.waitFor();
        expect(await panel.textContent()).toBe(label === "bash" ? `Arguments\n{}\n\nResult\n${codeText}` : codeText);
        expect(await scroller.evaluate((el: HTMLElement) => el.clientHeight)).toBeLessThanOrEqual(256);
        expect(await scroller.evaluate((el: HTMLElement) => el.scrollHeight > el.clientHeight && el.scrollWidth > el.clientWidth)).toBe(true);
        expect(await scroller.getAttribute("tabindex")).toBe("0");
        await scroller.focus();
        await transcriptPage.keyboard.press("ArrowDown");
        await transcriptPage.waitForFunction((label: string) => document.querySelector(`pre[aria-label="${label}"]`)!.scrollTop > 0, label);
        await transcriptPage.getByRole("button", { name: `Expand ${label}`, exact: true }).click();
        const overlay = transcriptPage.getByRole("dialog", { name: label, exact: true });
        await overlay.waitFor();
        expect(await overlay.locator("pre").textContent()).toBe(label === "bash" ? `Arguments\n{}\n\nResult\n${codeText}` : codeText);
        expect(await overlay.locator("pre").evaluate((el: HTMLElement) => el.clientHeight)).toBeGreaterThan(256);
        expect(await scroller.evaluate((el: HTMLElement) => el.clientHeight)).toBeLessThanOrEqual(256);
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
      const tool = transcriptPage.locator('[data-slot="trace-collapsible"]').filter({ has: transcriptPage.locator('pre[aria-label="bash"]') }).last();
      await tool.getByRole("button", { name: "Collapse tool ↑", exact: true }).click();
      await tool.locator("pre").waitFor({ state: "hidden" });
      await tool.locator('[data-slot="trace-collapsible-trigger"]').first().click();
      await tool.locator("pre").waitFor();
      // A partial streamed fence is rendered as code before its closing fence arrives.
      ctrl.history = [...ctrl.history.filter((message) => message.entryKey !== "fenced-stream"), { entryKey: "fenced-stream", itemId: "fenced-stream:0", inputId: "", role: "assistant", text: "~~~py\n  streamed", turnId: "fenced-stream", complete: false }];
      (await transcriptStream(`code-${width}`).promise).enqueue(`data: ${JSON.stringify({ conversationId: `code-${width}`, revision: "hint" })}\n\n`);
      await transcriptPage.locator('pre[aria-label="py"]').waitFor();
      // Markdown output ends every code block with a line break, as a closed fence does.
      expect(await transcriptPage.locator('pre[aria-label="py"]').textContent()).toBe("  streamed\n");
      // One remembered wrap setting applies to every block, inline and expanded, long tokens included.
      const fits = (label: string) => transcriptPage.locator(`#transcript-scroll pre[aria-label="${label}"]`).evaluate((el: HTMLElement) => el.scrollWidth <= el.clientWidth);
      expect([await fits("bash"), await fits("sh")]).toEqual([false, false]);
      await transcriptPage.getByRole("button", { name: "Wrap sh", exact: true }).click();
      expect(await transcriptPage.getByRole("button", { name: "Wrap bash", exact: true }).getAttribute("aria-pressed")).toBe("true");
      expect([await fits("bash"), await fits("sh")]).toEqual([true, true]);
      transcriptStreams.clear();
      await transcriptPage.reload();
      await transcriptPage.locator('pre[aria-label="sh"]').waitFor();
      expect(await fits("sh")).toBe(true);
      expect(await transcriptPage.getByRole("button", { name: "Wrap sh", exact: true }).getAttribute("aria-pressed")).toBe("true");
      await transcriptPage.getByRole("button", { name: "Expand sh", exact: true }).click();
      const expanded = transcriptPage.getByRole("dialog", { name: "sh", exact: true });
      const expandedFits = () => expanded.locator("pre").evaluate((el: HTMLElement) => el.scrollWidth <= el.clientWidth);
      expect(await expandedFits()).toBe(true);
      await expanded.getByRole("button", { name: "Wrap sh", exact: true }).click();
      expect(await expandedFits()).toBe(false);
      await transcriptPage.keyboard.press("Escape");
      await expanded.waitFor({ state: "hidden" });
      expect(await fits("sh")).toBe(false);
      expect(await transcriptPage.evaluate(() => localStorage.getItem("code-wrap"))).toBe("false");
    }
    await transcriptPage.close();
    // Timeline detail: Compact by default, live updates keep an opened row open, and palette levels reshape and persist.
    const step = (role: string, text: string, extra: Record<string, string> = {}) => ({ role, text, ...extra });
    const detailItems = [
      step("user", "Check the build"), step("thinking", "Planning the check"),
      step("tool", `execute\n${JSON.stringify({ code: "bash(command=r'''make test''')" })}`, { toolName: "execute", toolCallId: "run" }), step("tool", "all tests passed", { toolCallId: "run" }),
    ];
    ctrl.history = detailItems.map((item, index) => ({ ...item, entryKey: "detail", itemId: `detail:${index}`, inputId: "", turnId: "", complete: true }));
    transcriptStreams.clear();
    const detailPage = await browser.newPage();
    await detailPage.goto(`${origin}/s/${Buffer.from("timeline-detail").toString("base64url")}`);
    const grouped = detailPage.locator('#transcript-scroll [data-slot="trace-collapsible-trigger"]').filter({ hasText: /^Used \d tools?$/ });
    await grouped.waitFor();
    expect(await grouped.textContent()).toBe("Used 1 tool");
    expect(await detailPage.evaluate(() => localStorage.getItem("timeline-detail"))).toBeNull();
    await grouped.click();
    ctrl.history = [...detailItems, step("tool", "skill\n{\"name\":\"release\"}", { toolName: "skill", toolCallId: "load" }), step("assistant", "Build is green")]
      .map((item, index) => ({ ...item, entryKey: "detail", itemId: `detail:${index}`, inputId: "", turnId: "", complete: true }));
    (await transcriptStream("timeline-detail").promise).enqueue(`data: ${JSON.stringify({ conversationId: "timeline-detail", revision: "hint" })}\n\n`);
    await detailPage.getByText("Build is green", { exact: true }).waitFor();
    expect(await grouped.textContent()).toBe("Used 2 tools");
    expect(await grouped.getAttribute("aria-expanded")).toBe("true");
    const palette = async (name: string) => {
      await detailPage.keyboard.press("Control+Shift+p");
      await detailPage.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: new RegExp(`^${name}`) }).click();
    };
    // At Detailed the lone thought is its own group, and opening it shows the thought without a second Thought row.
    await palette("Timeline: Detailed");
    transcriptStreams.clear();
    await detailPage.reload();
    const thought = detailPage.locator('#transcript-scroll [data-slot="trace-collapsible-trigger"]').filter({ hasText: /^Thought$/ });
    await thought.click();
    await detailPage.getByText("Planning the check", { exact: true }).waitFor();
    expect(await thought.count()).toBe(1);
    await palette("Timeline: Everything");
    await detailPage.locator('pre[aria-label="Script"]').waitFor();
    expect(JSON.parse((await detailPage.evaluate(() => localStorage.getItem("timeline-detail")))!).rows.execute).toEqual({ placement: "separate", details: "expanded" });
    // A skill row opens its skill in the right panel, which links to the skill's own page and back to the list.
    await detailPage.getByRole("link", { name: "View skill: release", exact: true }).click();
    const skillPanel = detailPage.getByRole("complementary", { name: "Skill", exact: true });
    await skillPanel.getByText("release steps", { exact: true }).waitFor();
    expect(new URL(detailPage.url()).searchParams.get("skill")).toBe("release");
    await skillPanel.getByRole("link", { name: "Open page", exact: true }).click();
    await detailPage.waitForURL(`${origin}/skills/release`);
    await detailPage.getByRole("tab", { name: "release", exact: true }).waitFor();
    await detailPage.reload();
    await detailPage.getByRole("heading", { name: "release", exact: true }).waitFor();
    await detailPage.getByText("release steps", { exact: true }).waitFor();
    await detailPage.getByRole("link", { name: "All skills", exact: true }).click();
    await detailPage.waitForURL(`${origin}/skills`);
    expect(await detailPage.getByText("release steps", { exact: true }).count()).toBe(0);
    await detailPage.getByRole("link", { name: "release", exact: true }).click();
    await detailPage.waitForURL(`${origin}/skills/release`);
    await detailPage.goto(`${origin}/s/${Buffer.from("timeline-detail").toString("base64url")}`);
    await detailPage.locator('pre[aria-label="Script"]').waitFor();
    await detailPage.keyboard.press("Control+Shift+p");
    const levels = detailPage.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: /^Timeline: / });
    expect(await levels.filter({ hasText: "Current · " }).allTextContents()).toEqual(["Timeline: EverythingCurrent · Show all activity separately and expanded."]);
    // Running a level does not move it to the top with recent commands.
    expect((await levels.allTextContents()).map((text: string) => text.match(/^Timeline: (Messages only|Quiet|Compact|Detailed|Everything)/)![1])).toEqual(["Messages only", "Quiet", "Compact", "Detailed", "Everything"]);
    await levels.filter({ hasText: "Messages only" }).click();
    await detailPage.getByText("Build is green", { exact: true }).waitFor();
    expect(await detailPage.locator('#transcript-scroll [data-slot="trace-collapsible-trigger"]').count()).toBe(0);
    // Chat messages render as Markdown by default; Output: Raw restores Slack formatting and persists across reloads.
    ctrl.history = [step("assistant", "| Name | Role |\n|---|---|\n| Alice | Developer |")].map((item, index) => ({ ...item, entryKey: "table", itemId: `table:${index}`, inputId: "", turnId: "", complete: true }));
    await detailPage.goto(`${origin}/s/${Buffer.from("markdown-output").toString("base64url")}`);
    const table = detailPage.locator("#transcript-scroll table");
    await table.waitFor();
    expect(await table.locator("td").allTextContents()).toEqual(["Alice", "Developer"]);
    await palette("Output: Raw");
    const raw = detailPage.locator('#transcript-scroll [data-slot="bubble-content"]').getByText("| Alice | Developer |");
    await raw.waitFor();
    expect(await table.count()).toBe(0);
    await detailPage.reload();
    await raw.waitFor();
    expect(await detailPage.evaluate(() => localStorage.getItem("output-mode"))).toBe("raw");
    await palette("Output: Markdown");
    await table.waitFor();
    // Pointer input reaches the Config slider: clicking its far end selects Everything.
    await detailPage.goto(`${origin}/config`);
    const sliderBox = (await detailPage.getByRole("group", { name: "Timeline detail" }).boundingBox())!;
    await detailPage.mouse.click(sliderBox.x + sliderBox.width - 1, sliderBox.y + sliderBox.height / 2);
    await detailPage.getByText("Everything:", { exact: true }).waitFor();
    expect(JSON.parse((await detailPage.evaluate(() => localStorage.getItem("timeline-detail")))!).rows).toEqual(timelineLevels.at(-1)!.rows);
    await detailPage.close();
    ctrl.history = [];
    // The agent menu lists each agent with its model and effort; the agents refetch every 2s without closing it.
    const agentPage = await browser.newPage({ viewport: { width: 1280, height: 844 } });
    await agentPage.route("**/api/ListAgents", (route: { fulfill(options: { json: unknown }): Promise<void> }) => route.fulfill({ json: { agents: [{ name: "other", model: "gpt" }, { name: "main", model: "root/model", reasoning: "high" }], currentAgent: "other" } }));
    await agentPage.goto(`${origin}/s/${Buffer.from("named").toString("base64url")}`);
    const agentPicker = agentPage.getByRole("button", { name: "Choose agent", exact: true });
    await agentPicker.filter({ hasText: "other" }).waitFor();
    await agentPicker.click();
    const agentOptions = agentPage.getByRole("menuitemradio");
    await agentOptions.nth(1).waitFor();
    expect(await agentOptions.allInnerTexts()).toEqual(["other\ngpt", "main\nroot/model · high"]);
    expect(await agentOptions.first().getAttribute("aria-checked")).toBe("true");
    await agentPage.waitForTimeout(2500);
    expect(await agentOptions.count()).toBe(2);
    await agentOptions.filter({ hasText: "main" }).click();
    await agentPicker.filter({ hasText: "main" }).waitFor();
    await agentOptions.first().waitFor({ state: "hidden" });
    await agentPage.close();
    for (const width of [1280, 390]) {
      ctrl.rows = [row("recent", "Most recent message"), row("named", "Original preview"), { ...row("pinned", "Pinned preview"), pinned: true }];
      ctrl.yieldBatches = complete(ctrl.rows);
      const detailsPage = await browser.newPage({ viewport: { width, height: 844 } });
      await detailsPage.goto(`${origin}/s/${Buffer.from("named").toString("base64url")}`);
      await detailsPage.getByRole("button", { name: "Choose agent", exact: true }).waitFor();
      expect(await detailsPage.getByRole("button", { name: "Steer", exact: true }).isDisabled()).toBe(true);
      if (width === 390) {
        await detailsPage.setViewportSize({ width: 320, height: 568 });
        const selector = detailsPage.getByRole("button", { name: "Choose agent", exact: true });
        expect((await selector.boundingBox())!.height).toBeGreaterThanOrEqual(44);
        const selectorBox = (await selector.boundingBox())!;
        const sendBox = (await detailsPage.getByRole("button", { name: "Send", exact: true }).boundingBox())!;
        expect(selectorBox.width).toBeLessThanOrEqual(144);
        expect(Math.abs(selectorBox.y - sendBox.y)).toBeLessThan(2);
        await selector.click();
        await detailsPage.getByRole("menuitemradio", { name: "main gpt", exact: true }).waitFor();
        expect((await detailsPage.locator('[data-slot="dropdown-menu-content"]').boundingBox())!.width).toBeGreaterThan(selectorBox.width);
        expect(await detailsPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        await detailsPage.screenshot({ path: path.join(screenshots, "chat-agent-320.png") });
        await detailsPage.keyboard.press("Escape");
        await detailsPage.screenshot({ path: path.join(screenshots, "chat-composer-320.png") });
        await detailsPage.setViewportSize({ width, height: 844 });
      }
      await goToSession(detailsPage, "Most recent message");
      await detailsPage.waitForURL(`**/s/${Buffer.from("recent").toString("base64url")}`);
      await goToSession(detailsPage, "Original preview");
      await detailsPage.waitForURL(`**/s/${Buffer.from("named").toString("base64url")}`);
      if (width === 390) {
        await detailsPage.close();
        continue;
      }
      const commands = detailsPage.getByRole("dialog", { name: "Run command", exact: true });
      const sessionCommand = async (label: string) => {
        await detailsPage.keyboard.press("Control+Shift+p");
        await commands.getByRole("button", { name: `Sessions: ${label}`, exact: true }).click();
        await commands.waitFor({ state: "hidden" });
      };
      await sessionCommand("Pin session");
      await detailsPage.keyboard.press("Control+p");
      const detailsPalette = detailsPage.getByRole("dialog", { name: "Go to session or command", exact: true });
      const search = detailsPalette.getByPlaceholder("Search sessions");
      const settled = () => detailsPalette.locator('ul[data-pending="false"]').waitFor();
      await search.fill("IS:PINNED");
      await settled();
      expect(await detailsPalette.locator('li > button [data-slot="session-title"]').allTextContents()).toEqual(["Original preview", "Pinned preview"]);
      await detailsPage.keyboard.press("Escape");
      await sessionCommand("Unpin session");
      await sessionCommand("Name session");
      const dialog = detailsPage.getByRole("dialog", { name: "Name session", exact: true });
      await dialog.getByLabel("Session name").fill("  Release notes  ");
      ctrl.updateError = true;
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.getByRole("alert").waitFor();
      expect(await dialog.getByLabel("Session name").inputValue()).toBe("  Release notes  ");
      ctrl.updateError = false;
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.waitFor({ state: "hidden" });
      await detailsPage.reload();
      await detailsPage.getByRole("button", { name: "Choose agent", exact: true }).waitFor();
      await detailsPage.keyboard.press("Control+p");
      await detailsPalette.getByText("Release notes", { exact: true }).waitFor();
      await search.fill("Release notes");
      await settled();
      expect(await detailsPalette.locator('li > button [data-slot="session-title"]').count()).toBe(1);
      await search.fill("Original preview"); // A named session's preview is no longer its title.
      await settled();
      expect(await detailsPalette.locator('li > button [data-slot="session-title"]').count()).toBe(0);
      await detailsPage.keyboard.press("Escape");
      expect(await detailsPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await sessionCommand("Name session");
      expect(await dialog.getByLabel("Session name").inputValue()).toBe("Release notes");
      await dialog.getByLabel("Session name").fill("");
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.waitFor({ state: "hidden" });
      await detailsPage.keyboard.press("Control+p");
      await detailsPalette.getByText("Original preview", { exact: true }).waitFor();
      await detailsPage.keyboard.press("Escape");
      await detailsPage.screenshot({ path: path.join(screenshots, `session-details-${width}.png`) });
      await detailsPage.close();
    }
    ctrl.history = [{ entryKey: "files", itemId: "files:0", inputId: "", role: "tool", text: "Delivered image", toolCallId: "result", turnId: "", complete: true, attachments: [image] }];
    transcriptStreams.clear();
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
      { entryKey: "files", itemId: "files:1", inputId: "consumed-0", role: "user", text: "queued message now consumed", turnId: "", complete: true },
      { entryKey: "files", itemId: "files:2", inputId: "", role: "assistant", text: "reply to queued message", turnId: "", complete: true },
      { entryKey: "files", itemId: "files:3", inputId: "consumed-2", role: "user", text: "steering message consumed", turnId: "", complete: true },
      { entryKey: "files", itemId: "files:4", inputId: "", role: "assistant", text: "", turnId: "", complete: true, attachments: [live] },
    );
    (await transcriptStream("visible-files").promise).enqueue(`data: ${JSON.stringify({ conversationId: "visible-files", revision: "hint" })}\n\n`);
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
    // A pasted screenshot arrives as a clipboard file, as with cmd+V or the context menu's Paste.
    await attachmentPage.locator("textarea").evaluate((element: HTMLElement) => {
      const clipboardData = new DataTransfer();
      clipboardData.items.add(new File([new Uint8Array([137, 80, 78, 71])], "image.png", { type: "image/png" }));
      element.dispatchEvent(new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData }));
    });
    await attachmentPage.getByRole("button", { name: "Remove image.png", exact: true }).click();
    await attachmentPage.locator("textarea").fill("  exact file draft\n");
    await attachmentPage.keyboard.press("Control+Shift+p");
    await attachmentPage.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Settings" }).click();
    await attachmentPage.goBack();
    await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    await goToSession(attachmentPage, "Most recent message");
    await attachmentPage.waitForURL(`**/s/${Buffer.from("recent").toString("base64url")}`);
    expect(await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).count()).toBe(0);
    await attachmentPage.goBack();
    await attachmentPage.getByRole("button", { name: "Remove keep.bin", exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    const beforeFailedUpload = ctrl.prompt.length;
    await attachmentPage.getByRole("button", { name: "Choose agent", exact: true }).click();
    await attachmentPage.getByRole("menuitemradio", { name: "other gpt" }).click();
    ctrl.uploadError = "drop.bin";
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await attachmentPage.getByText("Upload failed: drop.bin", { exact: true }).waitFor();
    expect(ctrl.prompt.length).toBe(beforeFailedUpload);
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    expect(await attachmentPage.getByRole("button", { name: /^Remove .*\.bin$/ }).count()).toBe(2);
    expect(await attachmentPage.locator("textarea").isEnabled()).toBe(true);
    expect(await attachmentPage.getByRole("button", { name: "Choose agent", exact: true }).innerText()).toBe("other");
    expect(["keep.bin", "drop.bin"].map((name) => [...uploadedFiles.findLast(({ meta }) => meta.name === name)!.data])).toEqual([[0, 255, 1, 2], [3, 4, 0, 255]]);
    ctrl.uploadError = "";
    ctrl.promptError = true;
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await attachmentPage.getByText("Send failed; retry", { exact: true }).waitFor();
    expect(await attachmentPage.locator("textarea").inputValue()).toBe("  exact file draft\n");
    expect(await attachmentPage.getByRole("button", { name: /^Remove .*\.bin$/ }).count()).toBe(2);
    expect(ctrl.attachmentPrompts.at(-1)).toMatchObject({ id: "visible-files", text: "  exact file draft\n", delivery: "STEER" });
    expect(ctrl.attachmentPrompts.at(-1)?.attachmentIds?.map((id) => uploadedFiles.find(({ meta }) => meta.id === id)?.meta.name)).toEqual(["keep.bin", "drop.bin"]);
    expect(ctrl.attachmentPrompts.at(-1)!.attachmentIds!.map((id) => [...uploadedFiles.find(({ meta }) => meta.id === id)!.data])).toEqual([[0, 255, 1, 2], [3, 4, 0, 255]]);
    ctrl.promptError = false;
    ctrl.holdUpload = true;
    ctrl.uploadStarted = Promise.withResolvers();
    await attachmentPage.getByRole("button", { name: "Send", exact: true }).click();
    await ctrl.uploadStarted.promise;
    const beforeUploadDispatch = ctrl.attachmentPrompts.length;
    await attachmentPage.locator("textarea").dispatchEvent("keydown", { key: "Enter" });
    await goToSession(attachmentPage, "Most recent message");
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
    ctrl.history.push({ entryKey: "uploaded", itemId: "uploaded:0", inputId: ctrl.attachmentPrompts.at(-1)!.messageId, role: "user", text: "  exact file draft\n", turnId: "", complete: true, attachments: sentAttachments });
    ctrl.running = true;
    await goToSession(attachmentPage, "Most recent message");
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
    // Raw output shows the sent text verbatim, so its trailing whitespace is observable.
    await photoPage.addInitScript(() => localStorage.setItem("output-mode", "raw"));
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
    ctrl.history = [{ entryKey: "photo", itemId: "photo:0", inputId: ctrl.attachmentPrompts.at(-1)!.messageId, role: "user", text: "What's this? \n", turnId: "", complete: true, attachments: [sentPhoto] }];
    await goToSession(photoPage, "Most recent message");
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
    ctrl.history.push({ ...ctrl.history[0], entryKey: "second-photo", itemId: "second-photo:0", inputId: ctrl.attachmentPrompts.at(-1)!.messageId, attachments: [secondPhoto] });
    await goToSession(photoPage, "Most recent message");
    await photoPage.goBack();
    await photoPage.waitForFunction(() => { const images = [...document.querySelectorAll<HTMLImageElement>('img[alt="photo.png"]')]; return images.length === 2 && images.every((image) => image.src.includes("/api/DownloadAttachment") && image.naturalWidth === 1); });
    expect(await photoPage.getByText("What's this?", { exact: true }).allTextContents()).toEqual(["What's this? \n", "What's this? \n"]);
    expect(await photoPage.getByRole("img", { name: "photo.png", exact: true }).count()).toBe(2);
    await photoPage.close();
    const desktopPage = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await desktopPage.goto(origin);
    const desktopSearch = desktopPage.locator("footer").getByRole("button", { name: "Search sessions and commands" });
    await desktopSearch.waitFor();
    expect((await desktopSearch.boundingBox())!.width).toBe(32);
    expect((await desktopSearch.locator("svg").boundingBox())!.width).toBe(24);
    const commandsBox = (await desktopSearch.boundingBox())!;
    await desktopSearch.hover();
    expect((await desktopSearch.boundingBox())!.x).toBe(commandsBox.x);
    expect(await desktopPage.locator("#bottom-navigation > :last-child").evaluate((el: HTMLElement) => {
      const style = getComputedStyle(el);
      return [style.overflowY, style.scrollbarWidth];
    })).toEqual(["hidden", "none"]);
    for (const name of ["New session", "Search sessions and commands"]) {
      const control = desktopPage.locator("footer").getByRole("button", { name, exact: true });
      await control.hover();
      await desktopPage.locator('[data-slot="tooltip-content"]').filter({ hasText: name }).waitFor();
      await desktopPage.mouse.move(0, desktopPage.viewportSize()!.height / 2); // The top-left corner is a tab with its own tooltip.
      await desktopPage.locator('[data-slot="tooltip-content"]').filter({ hasText: name }).waitFor({ state: "hidden" });
    }
    await desktopPage.keyboard.press("Tab");
    await desktopSearch.focus();
    await desktopPage.locator('[data-slot="tooltip-content"]').filter({ hasText: "Search sessions and commands" }).waitFor();
    await desktopPage.keyboard.press("Escape");
    await desktopPage.getByRole("button", { name: "Hide bottom navigation" }).click();
    expect(await desktopSearch.isVisible()).toBe(false);
    await desktopPage.getByRole("button", { name: "Show bottom navigation" }).click();
    await desktopPage.setViewportSize({ width: 320, height: 640 });
    await desktopSearch.click();
    await desktopPage.getByRole("dialog", { name: "Go to session or command" }).getByPlaceholder("Search sessions and commands").fill("settings");
    await desktopPage.getByRole("dialog", { name: "Go to session or command" }).getByRole("button", { name: "Settings", exact: true }).click();
    await desktopPage.waitForURL("**/config");
    expect(await desktopPage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await desktopPage.close();
    // Held work is server-backed and silent; keyboard Pop only releases to the queue.
    ctrl.history = [];
    ctrl.queue = [];
    ctrl.running = false;
    transcriptStreams.clear();
    const stashPage = await browser.newPage({ viewport: { width: 390, height: 844 } });
    await stashPage.goto(`${origin}/s/${Buffer.from("stash-session").toString("base64url")}`);
    await stashPage.locator("textarea").fill("$stop");
    await stashPage.locator('input[type="file"]').setInputFiles({ name: "held.txt", mimeType: "text/plain", buffer: Buffer.from("held contents") });
    ctrl.promptError = true;
    const stashButton = stashPage.getByRole("button", { name: "Stash", exact: true });
    // Idle: Stash stays usable but is no louder than the disabled Steer.
    expect(await stashButton.isDisabled()).toBe(false);
    expect((await stashButton.getAttribute("class"))?.split(/\s+/)).toContain("opacity-50");
    await stashButton.click();
    await shown(stashPage, "Stash failed; retry");
    expect(await stashPage.locator("textarea").inputValue()).toBe("$stop");
    expect(await stashPage.getByRole("button", { name: "Remove held.txt", exact: true }).count()).toBe(1);
    ctrl.promptError = false;
    await stashPage.locator("textarea").focus();
    await stashPage.keyboard.press("Meta+Alt+Enter");
    const heldRow = stashPage.locator("[data-queue-id]");
    await heldRow.getByRole("button", { name: "Pop", exact: true }).waitFor();
    expect(await heldRow.getByText("$stop", { exact: true }).count()).toBe(1);
    expect(await stashPage.locator("textarea").inputValue()).toBe("");
    expect(await stashPage.getByRole("button", { name: "Stop", exact: true }).count()).toBe(0);
    expect(await heldRow.getByRole("button", { name: "Steer", exact: true }).count()).toBe(0);
    expect(await heldRow.getByRole("button", { name: "Reorder", exact: true }).count()).toBe(1);
    expect(await heldRow.getByRole("button", { name: "Remove", exact: true }).count()).toBe(1);
    expect(await stashPage.getByRole("region", { name: "Messages", exact: true }).getByText("$stop", { exact: true }).count()).toBe(0);
    transcriptStreams.clear();
    await stashPage.reload();
    await heldRow.getByRole("button", { name: "Pop", exact: true }).waitFor();
    await heldRow.getByRole("link", { name: "Download held.txt", exact: true }).waitFor();
    ctrl.popError = true;
    await heldRow.getByRole("button", { name: "Pop", exact: true }).click();
    await shown(stashPage, "Pop failed; retry");
    expect(ctrl.queue[0].delivery).toBe("STASH");
    ctrl.popError = false;
    const popRelease = Promise.withResolvers<void>();
    await stashPage.route("**/api/PopQueueItem", async (route: { continue(): Promise<void> }) => {
      await popRelease.promise;
      await route.continue();
    });
    ctrl.running = true;
    ctrl.history.push({ entryKey: "other-work", itemId: "other-work:0", inputId: "other-work", role: "user", text: "Other queued work", turnId: "other-work", complete: false });
    (await transcriptStream("stash-session").promise).enqueue(`data: ${JSON.stringify({ conversationId: "stash-session", revision: "hint" })}\n\n`);
    await stashPage.getByRole("button", { name: "Stop", exact: true }).waitFor();
    await heldRow.getByRole("button", { name: "Pop", exact: true }).focus();
    await stashPage.keyboard.press("Enter");
    const popping = heldRow.getByRole("button", { name: "Popping…", exact: true });
    await popping.waitFor();
    expect(await popping.isDisabled()).toBe(true);
    popRelease.resolve();
    await heldRow.getByRole("button", { name: "Steer", exact: true }).waitFor();
    expect(await heldRow.getByText("$stop", { exact: true }).count()).toBe(1);
    expect(ctrl.queue[0].delivery).toBe("QUEUE");
    expect(await stashPage.getByRole("region", { name: "Pending steers", exact: true }).count()).toBe(0);
    const steerRelease = Promise.withResolvers<void>();
    let steers = 0;
    await stashPage.route("**/api/SteerQueueItem", async (route: { continue(): Promise<void> }) => {
      steers++;
      await steerRelease.promise;
      await route.continue();
    });
    const steer = heldRow.getByRole("button", { name: "Steer", exact: true });
    await steer.click();
    // A second click while the steer is in flight would claim the row again and fail as not recorded.
    await steer.click({ force: true });
    expect(await steer.isDisabled()).toBe(true);
    steerRelease.resolve();
    await stashPage.getByRole("region", { name: "Pending steers", exact: true }).getByText("$stop", { exact: true }).waitFor();
    expect(ctrl.queue[0].delivery).toBe("STEER");
    expect(steers).toBe(1);
    expect(await heldRow.getByRole("button", { name: "Edit", exact: true }).count()).toBe(0);
    await stashPage.locator("textarea").fill("busy stash");
    // Running: Stash lights up together with Steer.
    expect((await stashButton.getAttribute("class"))?.split(/\s+/)).not.toContain("opacity-50");
    await stashPage.locator("textarea").press("Control+Alt+Enter");
    await heldRow.getByRole("button", { name: "Pop", exact: true }).waitFor();
    expect(await heldRow.getByText("busy stash", { exact: true }).count()).toBe(1);
    expect(await stashPage.getByRole("region", { name: "Messages", exact: true }).getByText("busy stash", { exact: true }).count()).toBe(0);
    expect(await stashPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await stashPage.close();

    // Reading queued/stashed text is local, including for one very long row.
    const queueText = `First line with **literal markup**\n\n  Indented line\n${"unbroken".repeat(100)}\n${"Another complete line\n".repeat(30)}Final line`;
    for (const width of [1280, 320]) for (const count of [1, 3]) {
      ctrl.history = [];
      ctrl.running = false;
      ctrl.queue = [
        { id: "expand-target", text: queueText, delivery: count === 1 ? "STASH" : "QUEUE", principal: ctrl.principal, attachments: [uploadedFiles.find(({ meta }) => meta.name === "held.txt")!.meta] },
        ...Array.from({ length: count - 1 }, (_, index): QueueItem => ({ id: `expand-other-${index}`, text: `Other message ${index}`, delivery: "STASH", principal: ctrl.principal })),
      ];
      const originalQueue = JSON.stringify(ctrl.queue);
      const queuePage = await browser.newPage({ viewport: { width, height: 640 } });
      const mutations: string[] = [];
      queuePage.on("request", (request: { url(): string }) => {
        if (/\/api\/(Prompt|PopQueueItem|SteerQueueItem|RemoveQueueItem|EditQueueItem|ReorderQueue)$/.test(request.url())) mutations.push(request.url());
      });
      await queuePage.goto(`${origin}/s/${Buffer.from(`queue-expansion-${width}-${count}`).toString("base64url")}`);
      const target = queuePage.locator('[data-queue-id="expand-target"]');
      await target.getByRole("link", { name: "Download held.txt", exact: true }).waitFor();
      expect(await target.getByRole("button", { name: "Edit", exact: true }).count()).toBe(1);
      const attachmentHref = await target.getByRole("link").getAttribute("href");
      await queuePage.locator("textarea").fill("Draft stays here");
      const compact = await target.textContent();
      const expand = target.getByRole("button", { name: "Expand message", exact: true });
      expect(await expand.getAttribute("aria-expanded")).toBe("false");
      await expand.focus();
      await queuePage.keyboard.press("Enter");
      const collapse = target.getByRole("button", { name: "Collapse message", exact: true });
      await collapse.waitFor();
      expect(await collapse.getAttribute("aria-expanded")).toBe("true");
      const fullText = target.getByText(queueText, { exact: true });
      expect(await fullText.textContent()).toBe(queueText);
      expect(await fullText.evaluate((el: HTMLElement) => {
        const style = getComputedStyle(el);
        return style.whiteSpace === "pre-wrap" && el.scrollWidth <= el.clientWidth && el.clientHeight >= parseFloat(style.lineHeight) * 35;
      })).toBe(true);
      expect(await queuePage.locator("[data-queue-id]").evaluateAll((rows: HTMLElement[]) => rows.map((row) => row.dataset.queueId))).toEqual(ctrl.queue.map((item) => item.id));
      expect(await queuePage.getByRole("button", { name: "Collapse message", exact: true }).count()).toBe(1);
      expect(await target.getByRole("link").getAttribute("href")).toBe(attachmentHref);
      expect(await target.getByRole("link").evaluate((el: HTMLElement) => el.closest("button, summary") === null)).toBe(true);
      expect(await target.getByRole("button", { name: count === 1 ? "Pop" : "Send", exact: true }).count()).toBe(1);
      expect(await target.getByRole("button", { name: "Remove", exact: true }).count()).toBe(1);
      expect(await target.getByRole("button", { name: "Reorder", exact: true }).count()).toBe(1);
      expect(await queuePage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      const composerBox = (await queuePage.locator("textarea").boundingBox())!;
      expect(composerBox.y).toBeGreaterThanOrEqual(0);
      expect(composerBox.y + composerBox.height).toBeLessThanOrEqual(640);
      expect(await target.locator("..").evaluate((el: HTMLElement) => el.scrollHeight > el.clientHeight && getComputedStyle(el).overflowY === "auto")).toBe(true);
      await queuePage.screenshot({ path: path.join(screenshots, `queue-expanded-${width}-${count}.png`) });
      await target.locator("..").evaluate((el: HTMLElement) => { el.scrollTop = el.scrollHeight; });
      expect(await fullText.evaluate((el: HTMLElement) => el.getBoundingClientRect().bottom <= el.closest("ul")!.getBoundingClientRect().bottom)).toBe(true);
      await collapse.focus();
      await queuePage.keyboard.press("Space");
      await expand.waitFor();
      expect(await expand.getAttribute("aria-expanded")).toBe("false");
      expect(await fullText.count()).toBe(0);
      expect(await target.textContent()).toBe(compact);
      expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
      expect(JSON.stringify(ctrl.queue)).toBe(originalQueue);
      expect(mutations).toEqual([]);

      const editor = queuePage.getByRole("textbox", { name: "Edit queued message", exact: true });
      await target.getByRole("button", { name: "Edit", exact: true }).click();
      expect(await editor.inputValue()).toBe(queueText);
      expect(await target.evaluate((row: HTMLElement) => {
        const panel = row.closest("ul")!.getBoundingClientRect();
        return [row.querySelector("textarea")!, ...row.querySelectorAll("form button")].every((control) => {
          const box = control.getBoundingClientRect();
          return box.top >= panel.top && box.bottom <= panel.bottom;
        });
      })).toBe(true);
      await editor.fill("discard this edit");
      await target.getByRole("button", { name: "Cancel", exact: true }).click();
      expect(JSON.stringify(ctrl.queue)).toBe(originalQueue);
      expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
      expect(mutations).toEqual([]);

      await target.getByRole("button", { name: "Edit", exact: true }).click();
      await editor.fill("Revised");
      await editor.press("Shift+Enter");
      await queuePage.keyboard.type("  queue text");
      expect(await editor.inputValue()).toBe("Revised\n  queue text");
      await editor.dispatchEvent("keydown", { key: "Enter", isComposing: true });
      await editor.dispatchEvent("keydown", { key: "Enter", keyCode: 229 });
      expect(mutations).toEqual([]);
      await queuePage.route("**/api/EditQueueItem", (route: { fulfill(input: { status: number; json: { code: number; message: string } }): Promise<void> }) => route.fulfill({ status: 500, json: { code: 13, message: "Edit failed; retry" } }));
      await editor.press("Enter");
      await shown(queuePage, "Edit failed; retry");
      expect(await editor.inputValue()).toBe("Revised\n  queue text");
      expect(JSON.stringify(ctrl.queue)).toBe(originalQueue);
      await queuePage.unroute("**/api/EditQueueItem");
      const editRelease = Promise.withResolvers<void>();
      let edits = 0;
      await queuePage.route("**/api/EditQueueItem", async (route: { continue(): Promise<void> }) => {
        edits++;
        await editRelease.promise;
        await route.continue();
      });
      await queuePage.screenshot({ path: path.join(screenshots, `queue-editing-${width}-${count}.png`) });
      expect(await queuePage.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await editor.press("Enter");
      const saving = target.getByRole("button", { name: "Saving…", exact: true });
      await saving.waitFor();
      await saving.click({ force: true });
      expect(await saving.isDisabled()).toBe(true);
      for (const name of ["Cancel", "Remove", "Reorder", count === 1 ? "Pop" : "Send"]) expect(await target.getByRole("button", { name, exact: true }).isDisabled()).toBe(true);
      expect(await editor.isDisabled()).toBe(true);
      if (width === 1280 && count === 3) {
        const documentBefore = await queuePage.evaluateHandle(() => document);
        // Match navigation.tsx's SPA navigation; a reload would replace the mutation cache.
        await queuePage.evaluate((pathname: string) => {
          history.pushState(null, "", pathname);
          window.dispatchEvent(new PopStateEvent("popstate"));
        }, `/s/${Buffer.from("queue-navigation-other").toString("base64url")}`);
        await queuePage.waitForURL(`**/s/${Buffer.from("queue-navigation-other").toString("base64url")}`);
        await queuePage.waitForFunction(() => document.querySelector("textarea")?.value === "");
        expect(await target.getByRole("button", { name: "Edit", exact: true }).isEnabled()).toBe(true);
        transcriptStreams.delete(`queue-expansion-${width}-${count}`);
        await queuePage.goBack();
        await queuePage.waitForFunction(() => document.querySelector("textarea")?.value === "Draft stays here");
        expect(await documentBefore.evaluate((before: Document) => before === document)).toBe(true);
        for (const name of ["Edit", "Remove", "Reorder", "Send"]) {
          expect(await target.getByRole("button", { name, exact: true }).isDisabled()).toBe(true);
        }
        const other = queuePage.locator('[data-queue-id="expand-other-0"]');
        expect(await other.getByRole("button", { name: "Pop", exact: true }).isDisabled()).toBe(true);
        expect(await editor.count()).toBe(0);
        expect(edits).toBe(1);
        expect(ctrl.queue[0].text).toBe(queueText);
      }
      editRelease.resolve();
      await editor.waitFor({ state: "hidden" });
      await target.getByText("Revised queue text", { exact: true }).waitFor();
      expect(edits).toBe(1);
      expect(ctrl.queue).toEqual(JSON.parse(originalQueue).map((item: QueueItem) => item.id === "expand-target" ? { ...item, text: "Revised\n  queue text" } : item));
      expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
      expect(await target.getByRole("link").getAttribute("href")).toBe(attachmentHref);
      if (width === 1280 && count === 3) {
        for (const name of ["Edit", "Remove", "Reorder", "Send"]) {
          expect(await target.getByRole("button", { name, exact: true }).isEnabled()).toBe(true);
        }
        await target.getByRole("button", { name: "Edit", exact: true }).click();
        await editor.fill("Later edit after navigation");
        await target.getByRole("button", { name: "Save", exact: true }).click();
        await editor.waitFor({ state: "hidden" });
        await target.getByText("Later edit after navigation", { exact: true }).waitFor();
        expect(ctrl.queue[0].text).toBe("Later edit after navigation");
        expect(edits).toBe(2);
      }
      transcriptStreams.delete(`queue-expansion-${width}-${count}`);
      await queuePage.reload();
      await target.getByText(width === 1280 && count === 3 ? "Later edit after navigation" : "Revised queue text", { exact: true }).waitFor();
      await queuePage.waitForFunction(() => document.querySelector("textarea")?.value === "Draft stays here");
      expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
      if (count === 3) {
        const last = queuePage.locator('[data-queue-id="expand-other-1"]');
        await last.getByRole("button", { name: "Edit", exact: true }).click();
        await editor.fill(queueText);
        expect(await last.evaluate((row: HTMLElement) => {
          const panel = row.closest("ul")!.getBoundingClientRect();
          return [...row.querySelectorAll("form button")].every((control) => control.getBoundingClientRect().bottom <= panel.bottom);
        })).toBe(true);
        await last.getByRole("button", { name: "Cancel", exact: true }).click();
      }
      if (width === 1280 && count === 3) {
        await target.getByRole("button", { name: "Edit", exact: true }).click();
        await editor.fill("consumed while editing");
        ctrl.queue = ctrl.queue.filter((item) => item.id !== "expand-target");
        const id = `queue-expansion-${width}-${count}`;
        (await transcriptStream(id).promise).enqueue(`data: ${JSON.stringify({ conversationId: id, revision: "consumed" })}\n\n`);
        await target.waitFor({ state: "hidden" });
        const remaining = queuePage.locator('[data-queue-id="expand-other-0"]');
        expect(await remaining.getByRole("button", { name: "Edit", exact: true }).isEnabled()).toBe(true);
        await remaining.getByRole("button", { name: "Edit", exact: true }).click();
        expect(await editor.inputValue()).toBe("Other message 0");
        await remaining.getByRole("button", { name: "Cancel", exact: true }).click();
        expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
        expect(mutations.filter((url) => url.endsWith("/api/Prompt"))).toEqual([]);
        expect(mutations.every((url) => url.endsWith("/api/EditQueueItem"))).toBe(true);
      }
      if (width === 320 && count === 3) {
        await queuePage.unroute("**/api/EditQueueItem");
        await target.getByRole("button", { name: "Edit", exact: true }).click();
        await editor.fill("consumed before save");
        await queuePage.route("**/api/EditQueueItem", (route: { fulfill(input: { status: number; json: { code: number; message: string } }): Promise<void> }) => {
          ctrl.queue = ctrl.queue.filter((item) => item.id !== "expand-target");
          return route.fulfill({ status: 404, json: { code: 5, message: "queue item is not recorded" } });
        });
        await target.getByRole("button", { name: "Save", exact: true }).click();
        await target.waitFor({ state: "hidden" });
        await shown(queuePage, "This message is no longer waiting.");
        expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
        const remaining = queuePage.locator('[data-queue-id="expand-other-0"]');
        expect(await remaining.getByRole("button", { name: "Edit", exact: true }).isEnabled()).toBe(true);
        await remaining.getByRole("button", { name: "Edit", exact: true }).click();
        expect(await editor.inputValue()).toBe("Other message 0");
        await remaining.getByRole("button", { name: "Cancel", exact: true }).click();
        expect(ctrl.queue.some((item) => item.id === "expand-target")).toBe(false);
        await queuePage.unroute("**/api/EditQueueItem");
        const consumedSave = Promise.withResolvers<void>();
        await queuePage.route("**/api/EditQueueItem", async (route: { fulfill(input: { status: number; json: { code: number; message: string } }): Promise<void> }) => {
          await consumedSave.promise;
          return route.fulfill({ status: 404, json: { code: 5, message: "queue item is not recorded" } });
        });
        await remaining.getByRole("button", { name: "Edit", exact: true }).click();
        await remaining.getByRole("button", { name: "Save", exact: true }).click();
        await remaining.getByRole("button", { name: "Saving…", exact: true }).waitFor();
        ctrl.queue = ctrl.queue.filter((item) => item.id !== "expand-other-0");
        const id = `queue-expansion-${width}-${count}`;
        (await transcriptStream(id).promise).enqueue(`data: ${JSON.stringify({ conversationId: id, revision: "consumed-during-save" })}\n\n`);
        await remaining.waitFor({ state: "hidden" });
        const last = queuePage.locator('[data-queue-id="expand-other-1"]');
        expect(await last.getByRole("button", { name: "Edit", exact: true }).isDisabled()).toBe(true);
        consumedSave.resolve();
        await last.getByRole("button", { name: "Edit", exact: true }).click();
        expect(await editor.inputValue()).toBe("Other message 1");
        await last.getByRole("button", { name: "Cancel", exact: true }).click();
        expect(await queuePage.locator("textarea").inputValue()).toBe("Draft stays here");
        expect(mutations.filter((url) => url.endsWith("/api/Prompt"))).toEqual([]);
        expect(mutations.every((url) => url.endsWith("/api/EditQueueItem"))).toBe(true);
      }
      await queuePage.close();
    }

    // A session whose agent is no longer listed shows, and moves to, the first listed agent.
    type PromptResponse = { url: () => string; request: () => { postDataJSON: () => { text: string } } };
    ctrl.history = [];
    ctrl.queue = [];
    ctrl.running = false;
    ctrl.currentAgents.set("retired-session", "retired");
    transcriptStreams.clear();
    const retiredPage = await browser.newPage({ viewport: { width: 1280, height: 844 } });
    const retiredPrompts = () => ctrl.prompt.filter((text) => text.startsWith("retired-session:"));
    await retiredPage.goto(`${origin}/s/${Buffer.from("retired-session").toString("base64url")}`);
    await retiredPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
    await retiredPage.locator("textarea").fill("held hello");
    await retiredPage.getByRole("button", { name: "Stash", exact: true }).click();
    const popped = retiredPage.waitForResponse((response: PromptResponse) => response.url().endsWith("/api/PopQueueItem"));
    await retiredPage.locator("[data-queue-id]").getByRole("button", { name: "Pop", exact: true }).click();
    await popped;
    expect(retiredPrompts()).toEqual(["retired-session:held hello", "retired-session:$agent other"]);
    ctrl.queue = [];
    ctrl.currentAgents.set("retired-session", "retired");
    transcriptStreams.clear();
    await retiredPage.reload();
    await retiredPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
    await retiredPage.route("**/api/Prompt", (route: { request(): { postDataJSON(): { text: string } }; fulfill(options: object): Promise<void>; continue(): Promise<void> }) => route.request().postDataJSON().text.startsWith("$agent ")
      ? route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "agent is not currently allowed", code: 3 }) })
      : route.continue());
    await retiredPage.locator("textarea").fill("hello");
    await retiredPage.locator("textarea").press("Enter");
    await shown(retiredPage, "agent is not currently allowed");
    expect(await retiredPage.locator("textarea").inputValue()).toBe("hello");
    expect(retiredPrompts()).toEqual(["retired-session:held hello", "retired-session:$agent other"]);
    // A rejected switch keeps the stash held instead of popping it to the unlisted agent.
    ctrl.queue = [{ id: "kept-stash", text: "kept stash", delivery: "STASH", principal: ctrl.principal }];
    const keptRow = retiredPage.locator('[data-queue-id="kept-stash"]');
    await keptRow.getByRole("button", { name: "Pop", exact: true }).click();
    await keptRow.getByRole("button", { name: "Pop", exact: true }).waitFor();
    await shown(retiredPage, "agent is not currently allowed");
    expect(ctrl.queue[0].delivery).toBe("STASH");
    ctrl.queue = [];
    await retiredPage.unroute("**/api/Prompt");
    const helloSent = retiredPage.waitForResponse((response: PromptResponse) => response.url().endsWith("/api/Prompt") && response.request().postDataJSON().text === "hello");
    await retiredPage.locator("textarea").press("Enter");
    await helloSent;
    expect(retiredPrompts().slice(2)).toEqual(["retired-session:$agent other", "retired-session:hello"]);
    expect(ctrl.currentAgents.get("retired-session")).toBe("other");
    // Sending already-queued work also switches the unlisted agent first.
    ctrl.currentAgents.set("retired-session", "retired");
    ctrl.queue = [{ id: "queued-work", text: "queued work", delivery: "QUEUE", principal: ctrl.principal }];
    transcriptStreams.clear();
    await retiredPage.reload();
    await retiredPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
    const steered = retiredPage.waitForResponse((response: PromptResponse) => response.url().endsWith("/api/SteerQueueItem"));
    await retiredPage.locator('[data-queue-id="queued-work"]').getByRole("button", { name: "Send", exact: true }).click();
    await steered;
    expect(retiredPrompts().at(-1)).toBe("retired-session:$agent other");
    expect(ctrl.queue[0].delivery).toBe("STEER");
    ctrl.queue = [];
    // An unrecorded agent is not a mismatch, and a new chat without `main` starts on the first listed agent.
    ctrl.currentAgents.set("blank-session", "");
    transcriptStreams.clear();
    await retiredPage.goto(`${origin}/s/${Buffer.from("blank-session").toString("base64url")}`);
    await retiredPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
    const blankSent = retiredPage.waitForResponse((response: PromptResponse) => response.url().endsWith("/api/Prompt") && response.request().postDataJSON().text === "hi");
    await retiredPage.locator("textarea").fill("hi");
    await retiredPage.locator("textarea").press("Enter");
    await blankSent;
    expect(ctrl.prompt.filter((text) => text.startsWith("blank-session:"))).toEqual(["blank-session:hi"]);
    await retiredPage.route("**/api/ListAgents", (route: { fulfill(options: { json: unknown }): Promise<void> }) => route.fulfill({ json: { agents: [{ name: "other", model: "gpt" }], currentAgent: "" } }));
    transcriptStreams.clear();
    await retiredPage.goto(`${origin}/`);
    await retiredPage.getByRole("button", { name: "Choose agent", exact: true }).filter({ hasText: "other" }).waitFor();
    await retiredPage.close();

    // Alice reads Bob's recorded authors, never her current identity, in every delivery surface.
    const bob = 'Bob Smith Ω <img src=x onerror="alert(1)"> ' + "LongName".repeat(18);
    const rawHeader = String.raw`[Web principal=${JSON.stringify(bob)} additional_instructions="principal=\"not the author\""]`;
    ctrl.username = "alice";
    ctrl.yieldBatches = complete(ctrl.rows);
    ctrl.history = [
      { entryKey: "authors", itemId: "authors:0", inputId: "bob-history", role: "user", text: "Bob's saved message", principal: bob, header: rawHeader, turnId: "authors", complete: true },
      { entryKey: "authors", itemId: "authors:1", inputId: "legacy", role: "user", text: "[Web principal=\"not metadata\"]", turnId: "authors", complete: true },
      { entryKey: "authors", itemId: "authors:2", inputId: "", role: "assistant", text: "Assistant unchanged", principal: bob, agent: "main", model: "gpt", turnId: "authors", complete: true },
    ];
    ctrl.queue = [{ id: "bob-queue", text: "Bob's queued message", delivery: "QUEUE", principal: bob }, { id: "bob-stash", text: "Bob's stashed message", delivery: "STASH", principal: bob }];
    ctrl.running = false;
    transcriptStreams.clear();
    const authorPage = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    // A background tab connecting first must not receive this conversation's hint.
    await page.evaluate(() => {
      const stream = new EventSource("/stream?id=background-session");
      Object.assign(window, { __backgroundStream: stream });
      return new Promise<void>((resolve) => { stream.onopen = () => resolve(); });
    });
    await authorPage.goto(`${origin}/s/${Buffer.from("author-session").toString("base64url")}`);
    const authorChat = authorPage.getByRole("region", { name: "Messages", exact: true });
    const savedBubble = authorChat.locator('[data-slot="bubble-content"]').filter({ hasText: "Bob's saved message" });
    await savedBubble.locator('[data-slot="message-author"]').waitFor({ state: "visible" });
    expect(await savedBubble.locator('[data-slot="message-author"]').textContent()).toBe(bob);
    expect(await savedBubble.locator("img").count()).toBe(0);
    expect(await authorChat.locator('[data-slot="bubble-content"]').filter({ hasText: "not metadata" }).locator('[data-slot="message-author"]').count()).toBe(0);
    expect(await authorChat.locator('[data-slot="bubble-content"]').filter({ hasText: "Assistant unchanged" }).locator('[data-slot="message-author"]').count()).toBe(0);
    await savedBubble.hover();
    const headerTip = authorPage.locator('[data-slot="tooltip-content"]');
    await headerTip.waitFor();
    expect(await headerTip.textContent()).toBe(rawHeader);
    await authorPage.keyboard.press("Escape");
    await headerTip.waitFor({ state: "hidden" });
    for (const width of [1280, 320]) {
      await authorPage.setViewportSize({ width, height: 844 });
      await savedBubble.locator('[data-slot="message-author"]').scrollIntoViewIfNeeded();
      await authorPage.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      expect(await savedBubble.locator('[data-slot="message-author"]').evaluate((node: HTMLElement) => {
        const label = node.getBoundingClientRect();
        const body = node.nextElementSibling!.getBoundingClientRect();
        return label.bottom <= body.top && node.scrollWidth <= node.clientWidth && label.right <= innerWidth;
      })).toBe(true);
      await authorPage.screenshot({ path: path.join(screenshots, `human-author-${width}.png`) });
    }
    for (const id of ["bob-queue", "bob-stash"]) {
      await authorPage.locator(`[data-queue-id="${id}"] [data-slot="message-author"]`).waitFor();
      expect(await authorPage.locator(`[data-queue-id="${id}"] [data-slot="message-author"]`).textContent()).toBe(bob);
    }
    await authorPage.locator('[data-queue-id="bob-stash"]').getByRole("button", { name: "Pop", exact: true }).click();
    await authorPage.locator('[data-queue-id="bob-stash"]').getByRole("button", { name: "Send", exact: true }).click();
    const authorParking = authorPage.getByRole("region", { name: "Pending steers", exact: true });
    await authorParking.locator('[data-slot="message-author"]').waitFor();
    expect(await authorParking.locator('[data-slot="message-author"]').textContent()).toBe(bob);
    ctrl.history.push({ entryKey: "live-author", itemId: "live-author:0", inputId: "bob-stash", role: "user", text: "Bob's stashed message", principal: bob, header: rawHeader, turnId: "live-author", complete: true, attachments: [image] });
    ctrl.queue = ctrl.queue.filter((item) => item.id !== "bob-stash");
    (await transcriptStream("author-session").promise).enqueue(`data: ${JSON.stringify({ conversationId: "author-session", revision: "hint" })}\n\n`);
    await authorParking.waitFor({ state: "hidden" });
    await authorChat.getByRole("img", { name: "history.png", exact: true }).waitFor();
    expect(await authorChat.locator('[data-slot="message-author"]').allTextContents()).toEqual([bob, bob]);
    transcriptStreams.clear();
    await authorPage.reload();
    await authorChat.getByRole("img", { name: "history.png", exact: true }).waitFor();
    expect(await authorChat.locator('[data-slot="message-author"]').allTextContents()).toEqual([bob, bob]);
    expect(await authorPage.locator('[data-queue-id="bob-queue"] [data-slot="message-author"]').textContent()).toBe(bob);

    // An identity refresh cannot supply a provisional author; recorded authors remain visible.
    const refreshing = Promise.withResolvers<void>();
    const refreshStarted = Promise.withResolvers<void>();
    await authorPage.route("**/api/Identity", async (route: { continue(): Promise<void> }) => { refreshStarted.resolve(); await refreshing.promise; await route.continue(); });
    await refreshStarted.promise;
    ctrl.holdPrompt = true;
    promptHold = Promise.withResolvers();
    ctrl.promptStarted = Promise.withResolvers();
    await authorPage.locator("textarea").fill("Unlabeled while identity refreshes");
    await authorPage.getByLabel("Send", { exact: true }).click();
    await ctrl.promptStarted.promise;
    expect(await authorChat.locator('[data-slot="bubble-content"]').filter({ hasText: "Unlabeled while identity refreshes" }).locator('[data-slot="message-author"]').count()).toBe(0);
    const identityRefreshed = authorPage.waitForResponse("**/api/Identity");
    refreshing.resolve();
    await identityRefreshed;
    await authorPage.unroute("**/api/Identity");
    promptHold.resolve("");
    ctrl.holdPrompt = false;
    await authorPage.locator("textarea").fill("$handoff");
    await authorPage.getByLabel("Send", { exact: true }).click();
    const handoffDialog = authorPage.getByRole("dialog", { name: "Session handoff", exact: true });
    await handoffDialog.getByRole("button", { name: "Start new session", exact: true }).waitFor();
    ctrl.history = [];
    ctrl.queue = [];
    ctrl.holdPrompt = true;
    promptHold = Promise.withResolvers();
    await authorPage.waitForResponse("**/api/Identity");
    await handoffDialog.getByRole("button", { name: "Start new session", exact: true }).click();
    await authorPage.waitForURL("**/s/d2ViLXNlc3Npb246bmV3");
    const handoffBubble = authorPage.locator('[data-slot="bubble-content"]').filter({ hasText: "Handoff document" });
    await handoffBubble.locator('[data-slot="message-author"]').waitFor();
    expect(await handoffBubble.locator('[data-slot="message-author"]').textContent()).toBe("Alice Smith");
    promptHold.resolve("");
    ctrl.holdPrompt = false;
    await authorPage.close();
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

test("session titles and turn-rail previews format tags and markup", async () => {
  const { chromium: engine } = await import(playwright!);
  const preview = "<!subteam^S1> *Red alert*";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "alert", preview, agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "sidebar-format" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "/workspace" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }] });
      case "/api/SlackNames": return Response.json({ names: { S1: "handle" } });
      case "/api/History": return Response.json({ messages: [{ messageId: "1:0", entryKey: "1", itemId: "1:0", inputId: "", role: "user", text: preview, complete: true, turnId: "" }], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: ["1"], running: false, terminal: "" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "TOP" } });
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
