import { expect, test } from "bun:test";
import path from "node:path";
import { encodeSessionId as encode } from "./session-id";
import type { SearchPage, Session } from "./types";

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

test("an open RocketClaw or OpenCode tab newly marked unread raises a desktop notification, and the poll reports the session in view", async () => {
  const { chromium: engine } = await import(playwright!);
  let rows: Session[] = [
    { id: "chat", agent: "main", preview: "Chat work", running: true },
    { id: "opencode:box:ses_a", kind: "REMOTE_OPENCODE", remoteSessionId: "ses_a", preview: "Alpha work", server: "box", directory: "/srv/app", running: true, reply: "All **green**:\n\n- 12 tests pass" },
    { id: "idle", agent: "main", preview: "Idle work", unreadAt: "2026-10-22T10:00:00Z" },
    { id: "closed", agent: "main", preview: "Closed work", running: true },
    // Team is open only in Shared; Quiet is muted, Later snoozed for years, and Over's snooze has ended.
    ...["team", "quiet", "later", "over"].map((id) => ({ id, agent: "main", preview: `${id} work`, running: true })),
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
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [["/s/", "chat"], ["/oc/", "opencode:box:ses_a"], ["/s/", "idle"], ["/s/", "quiet"], ["/s/", "later"], ["/s/", "over"]].map(([route, id]) => ({ path: `${route}${encode(id)}`, space: "main" })), groups: [], space: "main", last: {} }, revision: "1",
        shared: [{ space: "main", path: `/s/${encode("team")}` }], mutes: [["quiet", ""], ["later", "2999-01-01T00:00:00Z"], ["over", "2001-01-01T00:00:00Z"]].map(([id, until]) => ({ path: `/s/${encode(id)}`, until })) });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript(stubNotifications);
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

    // A new mark, or one moving past the last seen, notifies, a tab only in Shared too; the tab in view in a focused
    // window, a row without a tab, and a muted or snoozed tab stay quiet. An OpenCode notification keeps the tab
    // title and shows the start of its reply as plain text.
    mark(["chat", "opencode:box:ses_a", "idle", "closed", "team", "quiet", "later", "over"], "2026-10-22T10:01:00Z");
    await polls();
    expect(await notes()).toEqual([
      { title: "Alpha work", body: "All green: - 12 tests pass", tag: "opencode:box:ses_a", closed: false },
      { title: "main", body: "Idle work", tag: "idle", closed: false },
      { title: "main", body: "team work", tag: "team", closed: false },
      { title: "main", body: "over work", tag: "over", closed: false },
    ]);

    // With the window in the background, the poll reports no view, so a turn ending on the active tab marks it and
    // notifies. An unnamed RocketClaw session is titled by its agent and shows its last message, cut to 200 characters.
    await page.evaluate(() => { window.focused = false; });
    rows = rows.map((row) => row.id === "chat" ? { ...row, unreadAt: "2026-10-22T10:02:00Z", preview: `Chat work\n${"x".repeat(300)}` } : row);
    await polls();
    expect(viewing.at(-1)).toBe("");
    expect((await notes()).slice(4)).toEqual([{ title: "main", body: `Chat work ${"x".repeat(189)}…`, tag: "chat", closed: false }]);
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

test("an open Dashboard in this space notifies when a session enters a lane marked for notification", async () => {
  const { chromium: engine } = await import(playwright!);
  const rows = new Map(([["w1", "NEEDS_YOU"], ["r1", "RUNNING"], ["q1", "QUEUED", "Deploy **api**"], ["q2", "QUEUED"], ["q3", "QUEUED"], ["o1", "RUNNING", "", "other"]] as const)
    .map(([id, workState, name = "", spaceId = "main"]): [string, Session] => [id, { id, agent: "main", name: name || id, spaceId, workState }]));
  const has: Record<string, NonNullable<Session["workState"]>> = { "is:waiting": "NEEDS_YOU", "is:running": "RUNNING", "is:queued": "QUEUED" };
  const changed = new Set<string>(), requests: { spaceId: string; dashboards: { lanes: string[] }[] }[] = [];
  const update = (id: string, workState: NonNullable<Session["workState"]>) => { rows.set(id, { ...rows.get(id)!, workState }); changed.add(id); };
  let polls = 0;
  // Ops is this person's Dashboard in Main; Elsewhere is Shared in another space, with a notify lane matching o1.
  const lanes = [["Needs you", "is:waiting", true], ["Running", "is:running", false], ["Queued", "is:queued", false]] as const;
  const ops: SearchPage = { name: "Ops", board: "kanban", searches: lanes.map(([name, query, notify], i) => ({ id: String(i), name, query, notify })), active: "0" };
  const elsewhere: SearchPage = { name: "Elsewhere", board: "strips", searches: [{ id: "0", name: "Busy", query: "is:running", notify: true }], active: "0" };
  let state = { tabs: [{ path: "/search/d", space: "main", search: ops }], groups: [], space: "main", last: { main: "/search/d" } };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") {
      const sessions = url.searchParams.get("since") ? [...changed].map((id) => rows.get(id)!) : [...rows.values()];
      changed.clear();
      polls++;
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, order: [...rows.keys()] };
      return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: false })}\n\ndata: ${JSON.stringify(done)}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id?: string; state?: typeof state } & typeof requests[number];
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "notifications" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: input.id, reset: true, entryKeys: [], running: false, start: "0", more: false });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
      case "/api/Tabs":
        if (input.state) state = input.state;
        return Response.json({ spaces: [{ id: "main", name: "Main" }, { id: "other", name: "Other" }], state, revision: String(polls), shared: [{ space: "other", path: "/search/e", search: elsewhere }], mutes: [] });
      case "/api/Board": {
        // Lanes take the space's sessions in order, the first matching lane winning, as the server does.
        requests.push(input);
        const taken = new Set<string>();
        return Response.json({ dashboards: input.dashboards.map(({ lanes: queries }) => ({ lanes: queries.map((query) => {
          const sessions = [...rows.values()].filter((row) => row.spaceId === input.spaceId && !taken.has(row.id) && row.workState === has[query]);
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
    const base = `http://127.0.0.1:${server.port}`;
    const notes = (): Promise<Note[]> => page.evaluate(() => window.notes.map(({ title, body, tag, renotify, closed }) => ({ title, body, tag, renotify, closed })));
    const lane = (name: string): Promise<string[]> => page.locator(`[data-board] section[aria-label="${name}"] li a`).allTextContents();
    // Two more polls, and the Board requests they cause, settle every change made before.
    const settle = async () => {
      for (const at = polls; polls < at + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    const note = (id: string, body = id) => ({ title: "Ops: Needs you", body, tag: `/search/d:${id}`, renotify: true, closed: false });
    await page.goto(`${base}/search/d`);
    await page.locator('[data-board] section[aria-label="Needs you"] li').waitFor();
    await settle();
    // The first result after opening a Dashboard notifies nothing.
    expect(await notes()).toEqual([]);

    // A session entering a notify lane notifies, even in a focused window showing the Dashboard; one entering a lane
    // without the mark does not. The page and the watcher ask Board once for the change.
    const before = requests.length;
    update("q1", "NEEDS_YOU");
    update("q2", "RUNNING");
    await settle();
    expect(await notes()).toEqual([note("q1", "Deploy api")]);
    expect(requests.length).toBe(before + 1);

    // A session leaving and re-entering the lane notifies again under the same tag, alerting while the first still shows.
    update("q1", "QUEUED");
    await settle();
    update("q1", "NEEDS_YOU");
    await settle();
    expect(await notes()).toEqual([note("q1", "Deploy api"), note("q1", "Deploy api")]);

    // Turning a lane's mark off and on, or editing its query, notifies nothing for sessions already in it.
    for (const pressed of ["false", "true"]) {
      await page.getByRole("button", { name: "Notify", exact: true }).click();
      await page.waitForFunction((value: string) => document.querySelector('button[aria-label="Notify"]')?.getAttribute("aria-pressed") === value, pressed);
      await settle();
    }
    await page.getByRole("textbox", { name: "Search sessions" }).fill("is:queued");
    await settle();
    expect(await lane("Needs you")).toEqual([expect.stringContaining("q3")]);
    expect(await notes()).toHaveLength(2);
    // Later arrivals in the edited lane notify.
    update("r1", "QUEUED");
    await settle();
    expect((await notes())[2]).toEqual(note("r1"));

    // Clicking a notification opens its session; the Dashboard keeps notifying while out of view.
    await page.evaluate(() => (window.notes[0] as unknown as Notification).onclick!(new Event("click")));
    await page.waitForURL(`${base}/s/${encode("q1")}`);
    expect((await notes())[0].closed).toBe(true);
    update("w1", "QUEUED");
    await settle();
    expect((await notes())[3]).toEqual(note("w1"));

    // A Dashboard Shared in another space is never asked for, so it notifies nothing.
    update("o1", "QUEUED");
    await settle();
    update("o1", "RUNNING");
    await settle();
    expect(requests.every((sent) => sent.spaceId === "main" && sent.dashboards.length === 1)).toBe(true);
    expect(await notes()).toHaveLength(4);

    // The first result after a reload notifies nothing either.
    await page.reload();
    await settle();
    expect(await notes()).toEqual([]);
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
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, order: [...rows.keys()] };
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
        return Response.json({ spaces: [{ id: "main", name: "Main" }], state, revision: String(polls), shared: [], mutes: [] });
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
    const notes = (): Promise<Note[]> => page.evaluate(() => window.notes.map(({ title, body, tag, renotify, closed }) => ({ title, body, tag, renotify, closed })));
    const lane = (name: string): Promise<string[]> => page.locator(`[data-board] section[aria-label="${name}"] li b`).allTextContents();
    const marked = (): Promise<string[]> => page.locator('[data-board] li[data-new="true"] b').allTextContents();
    const settle = async () => {
      for (const seen = polls; polls < seen + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    await page.goto(`http://127.0.0.1:${server.port}/search/d`);
    await page.locator('[data-board] section[aria-label="Running"] li').waitFor();
    await settle();
    expect(await notes()).toEqual([]);

    // Closing the Running lane moves e1 into External, a notify lane: it did not arrive, so nothing notifies.
    await page.getByRole("button", { name: "Close search 2" }).click();
    await page.locator('[data-board] section[aria-label="Running"]').waitFor({ state: "detached" });
    await settle();
    expect(await lane("External")).toEqual(["e1"]);
    expect(await notes()).toEqual([]);
    expect(await marked()).toEqual([]);

    // A session newly entering External notifies once and shows new.
    set({ id: "e2", agent: "main", name: "e2", spaceId: "main", workState: "IDLE", external: true });
    await settle();
    expect(await notes()).toEqual([{ title: "Ops: External", body: "e2", tag: "/search/d:e2", renotify: true, closed: false }]);
    expect(await marked()).toEqual(["e2"]);

    // Editing Needs you's query moves w1 into External: nothing notifies or shows new.
    await page.getByRole("textbox", { name: "Search sessions" }).fill("is:running");
    await settle();
    expect(await lane("Needs you")).toEqual(["e1"]);
    expect(await lane("External")).toEqual(["w1", "e2"]);
    expect(await notes()).toHaveLength(1);
    expect(await marked()).toEqual([]);

    // Turning Needs you's mark on keeps its cards in view, even with Board slow to answer, and e1, already in it,
    // never notifies. The next result is a silent baseline; a session entering after it notifies once.
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
    expect((await notes()).slice(1)).toEqual([{ title: "Ops: Needs you", body: "q1", tag: "/search/d:q1", renotify: true, closed: false }]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("a lane listing only its newest 200 sessions notifies those arriving newer, not ones sliding in from beyond", async () => {
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
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, order: [...rows.keys()] };
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
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: "/search/d", space: "main", search: ops }], groups: [], space: "main", last: { main: "/search/d" } }, revision: "1", shared: [], mutes: [] });
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
    const notes = (): Promise<string[]> => page.evaluate(() => window.notes.map(({ body }) => body!));
    const marked = (): Promise<string[]> => page.locator('[data-board] li[data-new="true"] b').allTextContents();
    const settle = async () => {
      for (const seen = polls; polls < seen + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    await page.goto(`http://127.0.0.1:${server.port}/search/d`);
    await page.locator('[data-board] section[aria-label="Needs you"] li').first().waitFor();
    await settle();
    expect(await notes()).toEqual([]);

    // The newest session leaving lets the 201st into the list: it was in the lane already, so it is not new.
    set({ ...rows.get("c000")!, workState: "IDLE" });
    await settle();
    expect(await page.locator('[data-board] section[aria-label="Needs you"] h3').textContent()).toBe("Needs you 201");
    expect(await notes()).toEqual([]);
    expect(await marked()).toEqual([]);

    // A session arriving newer than the listed ones still notifies and is marked new.
    set({ id: "new", agent: "main", name: "new", spaceId: "main", workState: "NEEDS_YOU", arrivedAt: at(-1) });
    await settle();
    expect(await notes()).toEqual(["new"]);
    expect(await marked()).toEqual(["new"]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("a session the Board lists before the session list has it notifies once, from the Board's own card", async () => {
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
      const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${polls}`, order: [...listed] };
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
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: "/search/d", space: "main", search: ops }], groups: [], space: "main", last: { main: "/search/d" } }, revision: "1", shared: [], mutes: [] });
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
    const notes = (): Promise<Note[]> => page.evaluate(() => window.notes.map(({ title, body, tag, renotify, closed }) => ({ title, body, tag, renotify, closed })));
    const marked = (): Promise<string[]> => page.locator('[data-board] li[data-new="true"] b').allTextContents();
    const settle = async () => {
      for (const seen = polls; polls < seen + 2;) await Bun.sleep(20);
      await Bun.sleep(300);
    };
    await page.goto(`http://127.0.0.1:${server.port}/search/d`);
    await page.locator('[data-board] section[aria-label="Needs you"] li').waitFor();
    await settle();
    expect(await notes()).toEqual([]);

    // The Board lists late before the session list has it: another row's change asks Board, whose card notifies and shows new.
    sessions.set("late", { id: "late", agent: "main", name: "Late **arrival**", spaceId: "main", workState: "NEEDS_YOU", arrivedAt: "2026-10-22T10:05:00Z" });
    list({ ...sessions.get("w1")!, preview: "nudge" });
    await settle();
    const late = { title: "Ops: Needs you", body: "Late arrival", tag: "/search/d:late", renotify: true, closed: false };
    expect(await notes()).toEqual([late]);
    expect(await marked()).toEqual(["Late arrival"]);

    // The session list catching up notifies nothing more.
    list(sessions.get("late")!);
    await settle();
    expect(await notes()).toEqual([late]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
