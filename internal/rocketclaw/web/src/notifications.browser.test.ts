import { expect, test } from "bun:test";
import path from "node:path";
import { decodeSessionId, encodeSessionId as encode } from "./session-id";
import type { SearchPage, Session, TabAttention } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Note = { title: string; body?: string; tag?: string; renotify?: boolean; closed: boolean };
declare global {
  interface Window { notes: Note[]; focused: boolean; asked: boolean }
}

// The browser's notifications and window focus are stubbed at the page boundary; a granted permission
// outlives a reload, as the browser's does. Opaque-origin documents, such as about:blank, have no storage.
function stubNotifications() {
  if (origin === "null") return;
  window.notes = [];
  window.focused = true;
  document.hasFocus = () => window.focused;
  window.Notification = class {
    static permission = (sessionStorage.getItem("permission") ?? "default") as NotificationPermission;
    static async requestPermission() { window.asked = true; this.permission = "granted"; sessionStorage.setItem("permission", "granted"); return "granted" as const; }
    onclick: (() => void) | null = null;
    closed = false;
    constructor(public title: string, options?: NotificationOptions & { renotify?: boolean }) {
      Object.assign(this, { body: options?.body, tag: options?.tag, renotify: options?.renotify });
      window.notes.push(this as unknown as Note);
    }
    close() { this.closed = true; }
  } as unknown as typeof Notification;
}

test("a loud mark the server sets raises one desktop notification per window, a quiet or parked one none, and the poll reports the tab in view", async () => {
  const { chromium: engine } = await import(playwright!);
  let rows: Session[] = [
    { id: "chat", agent: "main", preview: "Chat work", running: true },
    { id: "opencode:box:ses_a", kind: "REMOTE_OPENCODE", remoteSessionId: "ses_a", preview: "Alpha work", server: "box", directory: "/srv/app", running: true, reply: "All **green**:\n\n- 12 tests pass" },
    { id: "idle", agent: "main", preview: "Idle work", unreadAt: "2026-10-22T10:00:00Z" },
    // Closed has no tab any more, Team is open only in Shared; this person's marks on Quiet are quiet and Parked is parked.
    { id: "closed", agent: "main", preview: "Closed work", running: true },
    ...["team", "quiet", "parked"].map((id) => ({ id, agent: "main", preview: `${id} work`, running: true, markedUnread: id === "quiet", parked: id === "parked" })),
  ];
  const mark = (ids: string[], unreadAt: string, note?: string) => { rows = rows.map((row) => ids.includes(row.id) ? { ...row, unreadAt, note } : row); };
  const viewing: string[] = [], tabs: { path: string; space: string }[] = [["/s/", "chat"], ["/oc/", "opencode:box:ses_a"], ["/s/", "idle"], ["/s/", "quiet"], ["/s/", "parked"]].map(([route, id]) => ({ path: `${route}${encode(id)}`, space: "main" }));
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
    const input = await request.json() as { id?: string; state?: { tabs: typeof tabs } };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: input.id, reset: true, entryKeys: [], running: false, start: "0", more: false });
      case "/api/Tabs":
        if (input.state) tabs.splice(0, tabs.length, ...input.state.tabs);
        return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs, groups: [], space: "main", last: {} }, revision: "1", shared: [{ space: "main", path: `/s/${encode("team")}` }] });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(stubNotifications);
    const base = `http://127.0.0.1:${server.port}`, at = (id: string) => `/s/${encode(id)}`, opencode = `/oc/${encode("opencode:box:ses_a")}`;
    await page.goto(`${base}${at("chat")}`);
    const notes = (): Promise<Note[]> => page.evaluate(() => window.notes.map(({ title, body, tag, closed }) => ({ title, body, tag, closed })));
    // A poll answered after the change can still be in flight, so the next one proves the change was read.
    const polls = async () => {
      const seen = viewing.length;
      while (viewing.length < seen + 2) await Bun.sleep(20);
      await Bun.sleep(100);
    };
    await page.getByRole("tab", { name: "Alpha work" }).waitFor();
    await polls();
    // A focused window reports the tab in view by its location.
    expect(viewing.at(-1)).toBe(at("chat"));

    // Notifications are on by default: the first click asks for the browser's permission, and nothing earlier does.
    expect(await page.evaluate(() => window.asked)).toBeUndefined();
    await page.getByRole("tab", { name: "Chat work" }).click();
    await page.waitForFunction(() => window.asked);

    // A turn ending without marking this person stays quiet, and so does a row already marked when the page loaded.
    rows = rows.map((row) => ({ ...row, running: false }));
    await polls();
    expect(await notes()).toEqual([]);

    // A new mark, or one moving past the last seen, notifies once, whether the tab is this person's or Shared; the tab in
    // view in a focused window, a quiet mark, a parked tab and a session they have no tab for stay quiet. An OpenCode
    // notification keeps the tab title and shows the start of its reply as plain text.
    mark(["chat", "opencode:box:ses_a", "idle", "closed", "team", "quiet", "parked"], "2026-10-22T10:01:00Z");
    await polls();
    await polls();
    expect(await notes()).toEqual([
      { title: "Alpha work", body: "All green: - 12 tests pass", tag: opencode, closed: false },
      { title: "main", body: "Idle work", tag: at("idle"), closed: false },
      { title: "main", body: "team work", tag: at("team"), closed: false },
    ]);
    // The window title counts every mark on a tab this person has, quiet ones too, but not a parked tab's or a closed one's.
    expect(await page.title()).toStartWith("(5) ");

    // With the window in the background, the poll reports no view, so a turn ending on the active tab marks it and
    // notifies. An unnamed RocketClaw session is titled by its agent and shows its last message, cut to 200 characters.
    await page.evaluate(() => { window.focused = false; });
    rows = rows.map((row) => row.id === "chat" ? { ...row, unreadAt: "2026-10-22T10:02:00Z", preview: `Chat work\n${"x".repeat(300)}` } : row);
    await polls();
    expect(viewing.at(-1)).toBe("");
    expect((await notes()).slice(3)).toEqual([{ title: "main", body: `Chat work ${"x".repeat(189)}…`, tag: at("chat"), closed: false }]);
    expect(await page.getByRole("tab", { name: "Chat work" }).getAttribute("aria-description")).toBe("Space: Main, Unread");

    // A park that ended on a session shows its note, not the reply.
    mark(["idle"], "2026-10-22T10:03:00Z", "Park ended");
    await polls();
    expect((await notes()).slice(4)).toEqual([{ title: "main", body: "Park ended", tag: at("idle"), closed: false }]);

    // Clicking a notification opens its tab and closes it; the focused window then reports the OpenCode session.
    await page.evaluate(() => (window.notes[0] as unknown as Notification).onclick!(new Event("click")));
    await page.waitForURL(`${base}${opencode}`);
    expect((await notes())[0].closed).toBe(true);
    await page.evaluate(() => { window.focused = true; });
    await polls();
    expect(viewing.at(-1)).toBe(opencode);
    expect(errors).toEqual([]);

    // Marks restored from the saved session list are older than the first live poll's, which raises nothing;
    // later marks notify again.
    await page.goto("about:blank");
    mark(["chat", "opencode:box:ses_a", "idle"], "2026-10-22T10:04:00Z");
    await page.goto(`${base}${at("idle")}`);
    await page.getByRole("tab", { name: "Alpha work" }).waitFor();
    await polls();
    expect(await page.evaluate(() => Notification.permission)).toBe("granted");
    expect(await notes()).toEqual([]);
    mark(["chat"], "2026-10-22T10:05:00Z");
    await polls();
    expect(await notes()).toEqual([{ title: "main", body: `Chat work ${"x".repeat(189)}…`, tag: at("chat"), closed: false }]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("a person marks tabs read or unread from the tab menu or the palette, each item acting on the selected tabs it fits, silently, and the tab in view keeps its mark until they come back", async () => {
  const { chromium: engine } = await import(playwright!);
  let rows: Session[] = ["chat", "first", "second", "seen"].map((id) => ({ id, agent: "main", preview: `${id} work`, ...id === "seen" ? { unreadAt: "2026-10-22T10:00:00Z" } : {} }));
  const listed: string[] = [], marked: string[][] = [], read: string[][] = [];
  const set = (id: string, change: Partial<Session>) => { rows = rows.map((row) => row.id === id ? { ...row, ...change } : row); };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      const viewing = decodeSessionId((url.searchParams.get("viewing") ?? "").slice(3)), hold = url.searchParams.has("hold");
      listed.push(`${viewing}${hold ? " held" : ""}`);
      if (viewing && !hold) set(viewing, { unreadAt: undefined, markedUnread: undefined });
      return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id?: string; attention?: { paths: string[]; action: string } };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: input.id, reset: true, entryKeys: [], running: false, start: "0", more: false });
      case "/api/Tabs": {
        const ids = input.attention?.action === "MARK_UNREAD" ? input.attention.paths.map((item) => decodeSessionId(item.slice(3))) : [];
        if (ids.length) marked.push(ids);
        for (const id of ids) if (!rows.find((row) => row.id === id)!.unreadAt) set(id, { unreadAt: new Date().toISOString(), markedUnread: true });
        const cleared = input.attention?.action === "MARK_READ" ? input.attention.paths.map((item) => decodeSessionId(item.slice(3))) : [];
        if (cleared.length) read.push(cleared);
        for (const id of cleared) set(id, { unreadAt: undefined, markedUnread: undefined });
        return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: rows.map((row) => ({ path: `/s/${encode(row.id)}`, space: "main" })), groups: [], space: "main", last: {} }, revision: "1", shared: [] });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(() => { if (origin !== "null") sessionStorage.setItem("permission", "granted"); });
    await page.addInitScript(stubNotifications);
    const base = `http://127.0.0.1:${server.port}`;
    const tab = (id: string) => page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { name: `${id} work`, exact: true });
    const unread = async (id: string) => (await tab(id).getAttribute("aria-description"))!.includes("Unread");
    const polls = async () => {
      const seen = listed.length;
      while (listed.length < seen + 2) await Bun.sleep(20);
      await Bun.sleep(100);
    };
    await page.goto(`${base}/s/${encode("chat")}`);
    await tab("seen").waitFor();
    await polls();
    expect(listed.at(-1)).toBe("chat");

    // A read session's menu offers Mark as unread, and choosing it marks the session without a notification; an unread
    // one's menu offers Mark as read instead.
    await tab("seen").click({ button: "right" });
    await page.getByRole("menuitem").first().waitFor();
    expect((await page.getByRole("menuitem").allInnerTexts()).filter((item: string) => item.startsWith("Mark as"))).toEqual(["Mark as read"]);
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
    await tab("first").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Mark as unread", exact: true }).click();
    await polls();
    expect(marked).toEqual([["first"]]);
    expect(await unread("first")).toBe(true);
    expect(await page.evaluate(() => window.notes.length)).toBe(0);

    // Three selected tabs, two read and one bold, offer both items, and each acts in one call on the tabs it fits: Mark as
    // read on the bold one; Mark as unread, offered while any selected tab is read, on the read ones, here leaving out
    // the bold first, and taking the tab in view, which polls then report as held.
    await tab("second").click({ modifiers: ["ControlOrMeta"] });
    await tab("seen").click({ modifiers: ["ControlOrMeta"] });
    await tab("seen").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Mark as unread", exact: true }).waitFor();
    await page.getByRole("menuitem", { name: "Mark as read", exact: true }).click();
    await polls();
    expect(read).toEqual([["seen"]]);
    expect(await unread("seen")).toBe(false);
    await tab("first").click({ modifiers: ["ControlOrMeta"] });
    await tab("seen").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Mark as unread", exact: true }).click();
    await polls();
    expect(marked.length).toBe(2);
    expect(marked[1].toSorted()).toEqual(["chat", "second", "seen"]);
    expect(listed.at(-1)).toBe("chat held");
    expect(await unread("chat")).toBe(true);
    expect(await page.evaluate(() => window.notes.length)).toBe(0);

    // The hold outlives a reload, and leaving the tab and coming back ends it, which clears the mark.
    await page.reload();
    await tab("seen").waitFor();
    await polls();
    expect(listed.at(-1)).toBe("chat held");
    await tab("second").click();
    await tab("chat").click();
    await polls();
    expect(listed.at(-1)).toBe("chat");
    expect(await unread("chat")).toBe(false);

    // The palette marks the tab in view and holds it too.
    await page.keyboard.press("Control+Shift+p");
    const commands = page.getByRole("dialog", { name: "Run command", exact: true });
    await commands.getByRole("button", { name: "Tabs: Mark as unread", exact: true }).click();
    await commands.waitFor({ state: "hidden" });
    await polls();
    expect(marked.at(-1)).toEqual(["chat"]);
    expect(listed.at(-1)).toBe("chat held");
    expect(await unread("chat")).toBe(true);

    // A turn of the person's ending later on a session they marked notifies as usual.
    set("first", { unreadAt: new Date(Date.now() + 60_000).toISOString(), markedUnread: undefined });
    await polls();
    expect(await page.evaluate(() => window.notes.map(({ tag }) => tag))).toEqual([`/s/${encode("first")}`]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("a Cron tab's loud mark turns it bold and notifies with its note, a park that ends brings it back after it closed, a tab closed elsewhere never notifies, and a Shared-only row's notification opens it in its space", async () => {
  const { chromium: engine } = await import(playwright!);
  const cron = `/cron/${encode("daily")}`, chat = `/s/${encode("chat")}`;
  const ops: SearchPage = { name: "Ops", board: "kanban", searches: [{ id: "0", name: "Needs you", query: "is:waiting" }], active: "0" };
  let state = { tabs: [{ path: chat, space: "main" }, { path: cron, space: "main" }], groups: [], space: "main", last: {} }, revision = 1, marks: TabAttention[] = [];
  const viewing: string[] = [];
  let reloads = 0;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      // As the server does, the tab in view in a focused window is read.
      viewing.push(url.searchParams.get("viewing") ?? "");
      marks = marks.filter((mark) => mark.path !== viewing.at(-1));
      const first = { sessions: [{ id: "chat", agent: "main", preview: "Chat work" }], owner: "tester", tabRevision: String(revision) };
      return new Response(`data: ${JSON.stringify(first)}\n\ndata: ${JSON.stringify({ upstreamSuccess: true, tabs: marks })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id?: string; state?: typeof state; dashboards?: { lanes: string[] }[] };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/ListCronJobs": return Response.json({ jobs: [] });
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: input.id, reset: true, entryKeys: [], running: false, start: "0", more: false });
      case "/api/Board": return Response.json({ dashboards: input.dashboards!.map(({ lanes }) => ({ lanes: lanes.map(() => ({ sessions: [], total: 0 })) })) });
      case "/api/Tabs":
        if (input.state) [state, revision] = [input.state, revision + 1];
        else reloads++;
        return Response.json({ spaces: [{ id: "main", name: "Main" }, { id: "team", name: "Team" }], state, revision: String(revision), shared: [{ space: "team", path: "/search/d", search: ops }] });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(() => { if (origin !== "null") sessionStorage.setItem("permission", "granted"); });
    await page.addInitScript(stubNotifications);
    const base = `http://127.0.0.1:${server.port}`, strip = page.getByRole("tablist", { name: "Open tabs" }), daily = strip.getByRole("tab", { name: "daily", exact: true });
    const notes = (): Promise<Note[]> => page.evaluate(() => window.notes.map(({ title, body, tag, closed }) => ({ title, body, tag, closed })));
    const polls = async () => {
      const seen = viewing.length;
      while (viewing.length < seen + 2) await Bun.sleep(20);
      await Bun.sleep(100);
    };
    await page.goto(`${base}${chat}`);
    await daily.waitFor();
    await polls();

    // A run that finished marks the Cron tab: it goes bold, and the notification takes the tab's title and the note.
    marks = [{ path: cron, unreadAt: "2026-10-22T10:01:00Z", note: "Run finished" }];
    await polls();
    expect(await notes()).toEqual([{ title: "daily", body: "Run finished", tag: cron, closed: false }]);
    expect(await daily.getAttribute("aria-description")).toBe("Space: Main, Unread");
    expect(await daily.getByText("daily", { exact: true }).evaluate((title: HTMLElement) => getComputedStyle(title).fontWeight)).toBe("700");

    // A focused window names the Cron tab in view, which reads it; without focus it names none.
    await daily.click();
    await polls();
    expect(viewing.at(-1)).toBe(cron);
    expect(await daily.getAttribute("aria-description")).toBe("Space: Main");
    await page.evaluate(() => { window.focused = false; });
    await polls();
    expect(viewing.at(-1)).toBe("");

    // A park of the Cron tab that ends after it closed brings it back, bold, with a notification, in this window, which stayed open.
    await page.evaluate(() => { window.focused = true; });
    await strip.getByRole("tab", { name: "Chat work", exact: true }).click();
    await strip.getByRole("button", { name: "Close daily", exact: true }).click();
    await daily.waitFor({ state: "detached" });
    for (const started = Date.now(); state.tabs.some((tab) => tab.path === cron); await Bun.sleep(20)) if (Date.now() - started > 5_000) throw new Error("the closed tab was never saved");
    const before = reloads;
    await polls();
    expect(reloads).toBe(before); // This window's own save raised the revision; it reloads nothing for it.
    [state, revision, marks] = [{ ...state, tabs: [...state.tabs, { path: cron, space: "main" }] }, revision + 1, [{ path: cron, unreadAt: "2026-10-22T10:02:00Z", note: "Park ended" }]];
    await daily.waitFor();
    await polls();
    expect(await daily.getAttribute("aria-description")).toBe("Space: Main, Unread");
    expect((await notes()).slice(1)).toEqual([{ title: "daily", body: "Park ended", tag: cron, closed: false }]);

    // Another window closes the Cron tab just as a run finishes on it: this window, holding older tabs than the listing
    // names, notifies nothing for it, and drops the tab once it reloads.
    [state, revision, marks] = [{ ...state, tabs: state.tabs.filter((tab) => tab.path !== cron) }, revision + 1, [{ path: cron, unreadAt: "2026-10-22T10:02:30Z", note: "Run finished" }]];
    await daily.waitFor({ state: "detached" });
    await polls();
    expect((await notes()).slice(2)).toEqual([]);

    // A Dashboard only in another space's Shared group notifies under its name, and its click enters that space, where
    // it shows as the Shared row it is, without a tab of this person's.
    marks = [...marks, { path: "/search/d", unreadAt: "2026-10-22T10:03:00Z", note: "Needs you: q1" }];
    await polls();
    expect((await notes()).slice(2)).toEqual([{ title: "Ops", body: "Needs you: q1", tag: "/search/d", closed: false }]);
    await page.evaluate(() => (window.notes[2] as unknown as Notification).onclick!(new Event("click")));
    await page.waitForURL((url: URL) => url.pathname === "/search/d");
    await page.getByRole("button", { name: "Space: Team" }).waitFor();
    expect(await page.getByRole("tablist", { name: "Shared tabs" }).getByRole("tab", { name: "Ops", exact: true }).getAttribute("aria-selected")).toBe("true");
    await polls();
    expect(state.tabs.map((tab) => tab.path)).not.toContain("/search/d");
    expect(await strip.locator('[data-tab="/search/d"]').count()).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("sessions moving between a Dashboard's lanes because its lanes changed are not new", async () => {
  const { chromium: engine } = await import(playwright!);
  // e1 is an External MCP session running, so it sits in Running ahead of External; w1 waits on this person.
  const rows = new Map(([["w1", "NEEDS_YOU", true], ["e1", "RUNNING", true], ["q1", "QUEUED", false]] as const)
    .map(([id, workState, external]): [string, Session] => [id, { id, agent: "main", name: id, spaceId: "main", workState, external }]));
  const has: Record<string, (row: Session) => boolean> = { "is:waiting": (row) => row.workState === "NEEDS_YOU", "is:running": (row) => row.workState === "RUNNING", "is:queued": (row) => row.workState === "QUEUED", "is:external": (row) => !!row.external };
  const changed = new Set<string>(), set = (row: Session) => { rows.set(row.id, row); changed.add(row.id); };
  let polls = 0, slow = 0;
  const lanes = [["Needs you", "is:waiting", false], ["Running", "is:running", false], ["Queued", "is:queued", true], ["External", "is:external", true]] as const;
  let state = { tabs: [{ path: "/search/d", space: "main", search: { name: "Ops", board: "kanban", searches: lanes.map(([name, query, notify], i) => ({ id: String(i), name, query, notify })), active: "0" } as SearchPage }], groups: [], space: "main", last: { main: "/search/d" } };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      const sessions = url.searchParams.get("since") ? [...changed].map((id) => rows.get(id)!) : [...rows.values()];
      changed.clear();
      polls++;
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, since: url.searchParams.get("since") || undefined };
      return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: false })}\n\ndata: ${JSON.stringify(done)}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { state?: typeof state; dashboards: { lanes: string[] }[] };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
      case "/api/Tabs":
        if (input.state) state = input.state;
        return Response.json({ spaces: [{ id: "main", name: "Main" }], state, revision: String(polls), shared: [] });
      case "/api/Board": {
        // Each session sits only in the first lane it matches, as the server does.
        await Bun.sleep(slow);
        const taken = new Set<string>();
        return Response.json({ dashboards: input.dashboards.map(({ lanes: queries }) => ({ lanes: queries.map((query) => {
          const sessions = [...rows.values()].filter((row) => !taken.has(row.id) && has[query](row));
          sessions.forEach((row) => taken.add(row.id));
          return { sessions, total: sessions.length };
        }) })) });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(() => { if (origin !== "null") sessionStorage.setItem("permission", "granted"); });
    await page.addInitScript(stubNotifications);
    const lane = (name: string): Promise<string[]> => page.locator(`[data-board] section[aria-label="${name}"] li b`).allTextContents();
    const marked = (): Promise<string[]> => page.locator('[data-board] li[data-new="true"] b').allTextContents();
    const settle = async () => {
      for (const seen = polls; polls < seen + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    await page.goto(`http://127.0.0.1:${server.port}/search/d`);
    await page.locator('[data-board] section[aria-label="Running"] li').waitFor();
    await settle();

    // Closing the Running lane moves e1 into External: it did not arrive, so it is not new.
    await page.getByRole("button", { name: "Close search 2" }).click();
    await page.locator('[data-board] section[aria-label="Running"]').waitFor({ state: "detached" });
    await settle();
    expect(await lane("External")).toEqual(["e1"]);
    expect(await marked()).toEqual([]);

    // A session newly entering External shows new.
    set({ id: "e2", agent: "main", name: "e2", spaceId: "main", workState: "IDLE", external: true });
    await settle();
    expect(await marked()).toEqual(["e2"]);

    // Editing Needs you's query moves w1 into External: nothing shows new.
    await page.getByRole("textbox", { name: "Search sessions" }).fill("is:running");
    await settle();
    expect(await lane("Needs you")).toEqual(["e1"]);
    expect(await lane("External")).toEqual(["w1", "e2"]);
    expect(await marked()).toEqual([]);

    // Turning Needs you's mark on keeps its cards in view, even with Board slow to answer, and e1, already in it, is not
    // new. A session entering after the next result shows new; the browser notifies nothing, as the server watches lanes.
    slow = 1000;
    await page.getByRole("button", { name: "Notify", exact: true }).click();
    await page.waitForFunction(() => document.querySelector('button[aria-label="Notify"]')?.getAttribute("aria-pressed") === "true");
    expect(await lane("Needs you")).toEqual(["e1"]);
    expect(await page.locator('[data-board] section[aria-label="Needs you"] h3 span').textContent()).toBe("1");
    slow = 0;
    set({ ...rows.get("q1")!, preview: "q1 again" });
    await settle();
    set({ ...rows.get("q1")!, workState: "RUNNING" });
    await settle();
    expect(await lane("Needs you")).toEqual(["e1", "q1"]);
    expect(await marked()).toEqual(["q1"]);
    expect(await page.evaluate(() => window.notes)).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("a lane listing only its newest 200 sessions shows new those arriving newer, not ones sliding in from beyond", async () => {
  const { chromium: engine } = await import(playwright!);
  // c000 arrived last; 202 sessions wait, so the lane lists 200 of them, as the server does.
  const at = (minutes: number) => new Date(Date.UTC(2026, 9, 22, 10) - minutes * 60_000).toISOString();
  const rows = new Map(Array.from({ length: 202 }, (_, i): [string, Session] => {
    const id = `c${String(i).padStart(3, "0")}`;
    return [id, { id, agent: "main", name: id, spaceId: "main", workState: "NEEDS_YOU", arrivedAt: at(i) }];
  }));
  const ops: SearchPage = { name: "Ops", board: "kanban", searches: [{ id: "0", name: "Needs you", query: "is:waiting", notify: true }], active: "0" };
  const changed = new Set<string>(), set = (row: Session) => { rows.set(row.id, row); changed.add(row.id); };
  let polls = 0;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      const sessions = url.searchParams.get("since") ? [...changed].map((id) => rows.get(id)!) : [...rows.values()];
      changed.clear();
      polls++;
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, since: url.searchParams.get("since") || undefined };
      return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: false })}\n\ndata: ${JSON.stringify(done)}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: "/search/d", space: "main", search: ops }], groups: [], space: "main", last: { main: "/search/d" } }, revision: "1", shared: [] });
      case "/api/Board": {
        const sessions = [...rows.values()].filter((row) => row.workState === "NEEDS_YOU").toSorted((a, b) => b.arrivedAt!.localeCompare(a.arrivedAt!));
        return Response.json({ dashboards: [{ lanes: [{ sessions: sessions.slice(0, 200), total: sessions.length }] }] });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(() => { if (origin !== "null") sessionStorage.setItem("permission", "granted"); });
    await page.addInitScript(stubNotifications);
    const marked = (): Promise<string[]> => page.locator('[data-board] li[data-new="true"] b').allTextContents();
    const settle = async () => {
      for (const seen = polls; polls < seen + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    await page.goto(`http://127.0.0.1:${server.port}/search/d`);
    await page.locator('[data-board] section[aria-label="Needs you"] li').first().waitFor();
    await settle();

    // The newest session leaving lets the 201st into the list: it was in the lane already, so it is not new.
    set({ ...rows.get("c000")!, workState: "IDLE" });
    await settle();
    expect(await page.locator('[data-board] section[aria-label="Needs you"] h3').textContent()).toBe("Needs you 201");
    expect(await marked()).toEqual([]);

    // A session arriving newer than the listed ones is marked new.
    set({ id: "new", agent: "main", name: "new", spaceId: "main", workState: "NEEDS_YOU", arrivedAt: at(-1) });
    await settle();
    expect(await marked()).toEqual(["new"]);
    expect(await page.evaluate(() => window.notes)).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("a session the Board lists before the session list has it shows new once, from the Board's own card", async () => {
  const { chromium: engine } = await import(playwright!);
  // Board answers from every session; the session list has only those listed, sending the changed ones since a version.
  const sessions = new Map<string, Session>([["w1", { id: "w1", agent: "main", name: "w1", spaceId: "main", workState: "NEEDS_YOU", arrivedAt: "2026-10-22T10:00:00Z" }]]);
  const listed = new Set(["w1"]), changed = new Set<string>(), list = (row: Session) => { sessions.set(row.id, row); listed.add(row.id); changed.add(row.id); };
  const ops: SearchPage = { name: "Ops", board: "kanban", searches: [{ id: "0", name: "Needs you", query: "is:waiting", notify: true }], active: "0" };
  let polls = 0;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      const rows = [...(url.searchParams.get("since") ? changed : listed)].map((id) => sessions.get(id)!);
      changed.clear();
      polls++;
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, since: url.searchParams.get("since") || undefined };
      return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: false })}\n\ndata: ${JSON.stringify(done)}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: "/search/d", space: "main", search: ops }], groups: [], space: "main", last: { main: "/search/d" } }, revision: "1", shared: [] });
      case "/api/Board": {
        const lane = [...sessions.values()].filter((row) => row.workState === "NEEDS_YOU").toSorted((a, b) => b.arrivedAt!.localeCompare(a.arrivedAt!));
        return Response.json({ dashboards: [{ lanes: [{ sessions: lane, total: lane.length }] }] });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(() => { if (origin !== "null") sessionStorage.setItem("permission", "granted"); });
    await page.addInitScript(stubNotifications);
    const marked = (): Promise<string[]> => page.locator('[data-board] li[data-new="true"] b').allTextContents();
    const settle = async () => {
      for (const seen = polls; polls < seen + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    await page.goto(`http://127.0.0.1:${server.port}/search/d`);
    await page.locator('[data-board] section[aria-label="Needs you"] li').waitFor();
    await settle();

    // The Board lists late before the session list has it: another row's change asks Board, whose card shows new.
    sessions.set("late", { id: "late", agent: "main", name: "Late **arrival**", spaceId: "main", workState: "NEEDS_YOU", arrivedAt: "2026-10-22T10:05:00Z" });
    list({ ...sessions.get("w1")!, preview: "nudge" });
    await settle();
    expect(await marked()).toEqual(["Late arrival"]);

    // The session list catching up marks nothing more, and the browser notifies nothing.
    list(sessions.get("late")!);
    await settle();
    expect(await marked()).toEqual([]);
    expect(await page.evaluate(() => window.notes)).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
