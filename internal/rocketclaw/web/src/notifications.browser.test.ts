import { expect, test } from "bun:test";
import path from "node:path";
import { encodeSessionId as encode } from "./session-id";
import type { Session } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Note = { title: string; body?: string; tag?: string; closed: boolean };
declare global {
  interface Window { notes: Note[]; focused: boolean; asked: boolean }
}

test("an open RocketClaw or OpenCode tab newly marked unread raises a desktop notification, and the poll reports the session in view", async () => {
  const { chromium: engine } = await import(playwright!);
  let rows: Session[] = [
    { id: "chat", agent: "main", preview: "Chat work", running: true },
    { id: "opencode:box:ses_a", kind: "REMOTE_OPENCODE", remoteSessionId: "ses_a", preview: "Alpha work", server: "box", directory: "/srv/app", running: true, reply: "All **green**:\n\n- 12 tests pass" },
    { id: "idle", agent: "main", preview: "Idle work", unreadAt: "2026-10-22T10:00:00Z" },
    { id: "closed", agent: "main", preview: "Closed work", running: true },
  ];
  const mark = (ids: string[], unreadAt: string) => { rows = rows.map((row) => ids.includes(row.id) ? { ...row, unreadAt } : row); };
  const viewing: string[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      viewing.push(url.searchParams.get("viewing") ?? "");
      return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, opencodeServers: { box: "http://box.test:4096" } })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id?: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: input.id, reset: true, entryKeys: [], running: false, start: "0", more: false });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [["/s/", "chat"], ["/oc/", "opencode:box:ses_a"], ["/s/", "idle"]].map(([route, id]) => ({ path: `${route}${encode(id)}`, space: "main" })), groups: [], space: "main", last: {} }, revision: "1" });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    // The browser's notifications and window focus are stubbed at the page boundary; a granted permission
    // outlives a reload, as the browser's does. Opaque-origin documents, such as about:blank, have no storage.
    await page.addInitScript(() => {
      if (origin === "null") return;
      window.notes = [];
      window.focused = true;
      document.hasFocus = () => window.focused;
      window.Notification = class {
        static permission = (sessionStorage.getItem("permission") ?? "default") as NotificationPermission;
        static async requestPermission() { window.asked = true; this.permission = "granted"; sessionStorage.setItem("permission", "granted"); return "granted" as const; }
        onclick: (() => void) | null = null;
        closed = false;
        constructor(public title: string, options?: NotificationOptions) {
          Object.assign(this, { body: options?.body, tag: options?.tag });
          window.notes.push(this as unknown as Note);
        }
        close() { this.closed = true; }
      } as unknown as typeof Notification;
    });
    const base = `http://127.0.0.1:${server.port}`;
    await page.goto(`${base}/s/${encode("chat")}`);
    const notes = (): Promise<Note[]> => page.evaluate(() => window.notes.map(({ title, body, tag, closed }) => ({ title, body, tag, closed })));
    // A poll answered after the change can still be in flight, so the next one proves the change was read.
    const polls = async () => {
      const seen = viewing.length;
      while (viewing.length < seen + 2) await Bun.sleep(20);
      await Bun.sleep(100);
    };
    await page.getByRole("tab", { name: "Alpha work" }).waitFor();
    await polls();
    // A focused window reports the session tab in view.
    expect(viewing.at(-1)).toBe("chat");

    // Notifications are on by default: the first click asks for the browser's permission, and nothing earlier does.
    expect(await page.evaluate(() => window.asked)).toBeUndefined();
    await page.getByRole("tab", { name: "Chat work" }).click();
    await page.waitForFunction(() => window.asked);

    // A turn ending without marking this person, such as another person's, stays quiet, and so does a row
    // already unread when the page loaded.
    rows = rows.map((row) => ({ ...row, running: false }));
    await polls();
    expect(await notes()).toEqual([]);

    // A new mark, or one moving past the last seen, notifies; the tab in view in a focused window and a row
    // without a tab stay quiet. An OpenCode notification keeps the tab title and shows the start of its reply as plain text.
    mark(["chat", "opencode:box:ses_a", "idle", "closed"], "2026-10-22T10:01:00Z");
    await polls();
    expect(await notes()).toEqual([
      { title: "Alpha work", body: "All green: - 12 tests pass", tag: "opencode:box:ses_a", closed: false },
      { title: "main", body: "Idle work", tag: "idle", closed: false },
    ]);

    // With the window in the background, the poll reports no view, so a turn ending on the active tab marks it and
    // notifies. An unnamed RocketClaw session is titled by its agent and shows its last message, cut to 200 characters.
    await page.evaluate(() => { window.focused = false; });
    rows = rows.map((row) => row.id === "chat" ? { ...row, unreadAt: "2026-10-22T10:02:00Z", preview: `Chat work\n${"x".repeat(300)}` } : row);
    await polls();
    expect(viewing.at(-1)).toBe("");
    expect((await notes()).slice(2)).toEqual([{ title: "main", body: `Chat work ${"x".repeat(189)}…`, tag: "chat", closed: false }]);
    expect(await page.getByRole("tab", { name: "Chat work" }).getAttribute("aria-description")).toBe("Space: Main, Unread");

    // Clicking a notification opens its tab and closes it; the focused window then reports the OpenCode session.
    await page.evaluate(() => (window.notes[0] as unknown as Notification).onclick!(new Event("click")));
    await page.waitForURL(`${base}/oc/${encode("opencode:box:ses_a")}`);
    expect((await notes())[0].closed).toBe(true);
    await page.evaluate(() => { window.focused = true; });
    await polls();
    expect(viewing.at(-1)).toBe("opencode:box:ses_a");
    expect(errors).toEqual([]);

    // Marks restored from the saved session list are older than the first live poll's, which raises nothing;
    // later marks notify again.
    await page.goto("about:blank");
    mark(["chat", "opencode:box:ses_a", "idle"], "2026-10-22T10:03:00Z");
    await page.goto(`${base}/s/${encode("idle")}`);
    await page.getByRole("tab", { name: "Alpha work" }).waitFor();
    await polls();
    expect(await page.evaluate(() => Notification.permission)).toBe("granted");
    expect(await notes()).toEqual([]);
    mark(["chat"], "2026-10-22T10:04:00Z");
    await polls();
    expect(await notes()).toEqual([{ title: "main", body: `Chat work ${"x".repeat(189)}…`, tag: "chat", closed: false }]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
