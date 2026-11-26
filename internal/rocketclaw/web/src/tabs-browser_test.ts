import { expect } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";
import type { Agent, ListTab, SearchPage, StripLayout, TabAttention, TabListName } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const rows: { id: string; name: string; agent: string; running?: boolean; background?: boolean; spaceId?: string; unreadAt?: string; asking?: boolean; forkedFrom?: string; automation?: boolean; goal?: boolean }[] = [
  { id: "alpha", name: "Alpha", agent: "main" },
  { id: "bravo", name: "Bravo", agent: "main" },
  { id: "charlie", name: "Charlie", agent: "main" },
  { id: "delta", name: "Delta", agent: "main", running: true, background: true },
  // Echo was born in the ops space, and its background work runs while no turn does.
  { id: "echo", name: "Echo", agent: "main", spaceId: "ops", background: true },
  // A turn of the person's ended unseen, and another runs now.
  { id: "foxtrot", name: "Foxtrot", agent: "main", running: true, unreadAt: "2026-10-22T10:00:00Z" },
  // Golf's running turn waits on the person's answer.
  { id: "golf", name: "Golf", agent: "main", running: true, asking: true },
  { id: "hotel", name: "Hotel", agent: "main", forkedFrom: "alpha" },
  { id: "india", name: "India", agent: "main", automation: true },
  { id: "juliet", name: "Juliet", agent: "main", forkedFrom: "alpha", goal: true },
];
const at = "2026-11-26T10:00:00Z";
const julietGoal = {
  goal: { objective: "Fix the flaky auth tests", status: "active", maxTurns: 3, turnsUsed: 1, check: "make test", force: true, principalName: "Ann", createdAt: at },
  events: [
    { id: "1", kind: "GOAL_EVENT_KIND_UPDATE", turn: 1, status: "progress", note: "Fixed the token refresh race", at },
    { id: "2", kind: "GOAL_EVENT_KIND_TURN", turn: 1, status: "active", messageId: "41", at },
    { id: "3", kind: "GOAL_EVENT_KIND_UPDATE", turn: 2, status: "complete", check: "GOAL_CHECK_RESULT_FAILED", checkOutput: "FAIL auth/session_test.go:88", at },
    { id: "4", kind: "GOAL_EVENT_KIND_TURN", turn: 2, status: "active", messageId: "42", at },
    { id: "5", kind: "GOAL_EVENT_KIND_TURN", turn: 3, status: "budget_exhausted", at },
    { id: "6", kind: "GOAL_EVENT_KIND_RESUME", principal: "Ann", at },
    { id: "7", kind: "GOAL_EVENT_KIND_UPDATE", turn: 1, status: "progress", note: "Rerunning the suite", at },
    { id: "8", kind: "GOAL_EVENT_KIND_TURN", turn: 1, status: "active", messageId: "43", at },
  ],
};
export const href = (id: string) => `/s/${Buffer.from(id).toString("base64url")}`;
export type SharedTab = { space: string; path: string; search?: SearchPage };
// A Search page as the server compares it: field order and empty names do not matter.
const plain = (page?: SearchPage) => JSON.stringify(page ? [page.name ?? "", page.searches.map(({ id, name, query }) => [id, name ?? "", query]), page.active] : null);
export type Change = { paths: string[]; action: string; until?: string };
// attention is this person's attention on each tab location, sent is every attention change, and polls counts session lists.
export type Saved = { spaces: { id: string; name: string }[]; state: { tabs: { path: string; group?: string; space?: string; search?: SearchPage }[]; groups: { id: string; name: string; color?: string; collapsed?: boolean }[]; space: string; last: Record<string, string>; sharedOrder: string[] }; revision: number; refused: number; shared: SharedTab[]; closed: { path: string; space?: string; search?: SearchPage }[]; layout?: StripLayout; lists: ListTab[]; visits: ListTab[]; attention: TabAttention[]; sent: Change[]; polls: number; autoPark?: boolean; deletes: string[]; titleGate?: Promise<unknown>; agents: Agent[]; updated: Record<string, string> };

// Mirrors the server's Tabs call: it saves the state or the layout, changes one space, adds, removes, or edits Shared tabs, or changes
// attention, then answers with every space, the saved state, every space's Shared tabs in order, and the layout.
// A taken name, ignoring case, deleting the last space, and sharing New session are refused, and a save made from an
// older revision is stale. A deleted space's Shared tabs join the first remaining space's, once each, keeping their order.
// Attention changes act as the server's do, and a save closing a tab parked without a time unparks it.
export const listed = (saved: Saved, list: TabListName) => saved.lists.filter((tab) => tab.list === list);

function tabsCall(saved: Saved, input: { state?: Saved["state"]; revision?: number; createSpace?: string; renameSpace?: { id: string; name: string }; deleteSpace?: string; share?: SharedTab; unshare?: SharedTab; editShared?: SharedTab & { base?: SearchPage }; attention?: Change; autoPark?: boolean; layout?: StripLayout; listAdd?: ListTab; listForget?: ListTab; listOrder?: ListTab[] }) {
  if (input.attention) saved.sent.push(input.attention);
  const same = (a: ListTab) => (b: ListTab) => a.list === b.list && a.server === b.server && a.path === b.path;
  if (input.listAdd) {
    const tab = input.listAdd;
    if (tab.list === "recent") saved.visits.push(tab);
    saved.lists = saved.lists.some(same(tab)) ? saved.lists.map((item) => same(tab)(item) ? tab : item) : [tab, ...saved.lists];
    saved.lists = saved.lists.filter((item) => item.list !== "recent" || listed(saved, "recent").indexOf(item) < 20);
  }
  if (input.listForget) saved.lists = saved.lists.filter((tab) => !same(input.listForget!)(tab));
  if (input.listOrder) {
    const order = input.listOrder, list = order[0].list;
    saved.lists = [...saved.lists.filter((tab) => tab.list !== list), ...listed(saved, list).toSorted((a, b) => order.findIndex(same(a)) - order.findIndex(same(b)))];
  }
  if (input.listAdd || input.listForget || input.listOrder) return Response.json({ lists: saved.lists });
  if (input.autoPark !== undefined) saved.autoPark = input.autoPark;
  if (input.layout) saved.layout = input.layout;
  for (const path of input.attention?.paths ?? []) {
    const { action, until } = input.attention!, row = saved.attention.find((item) => item.path === path) ?? saved.attention[saved.attention.push({ path }) - 1];
    if (action === "MARK_READ") row.unreadAt = undefined;
    if (action === "MARK_UNREAD" && !row.unreadAt && !row.parked) row.unreadAt = new Date().toISOString();
    if (action === "PARK" || (action === "UNPARK" && row.parked)) Object.assign(row, { parked: action === "PARK", until, untilDone: false });
    if (action === "PARK_UNTIL_DONE" && path.startsWith("/s/")) Object.assign(row, { parked: true, until: undefined, untilDone: true });
    if ((action === "AUTO_PARK" || action === "STOP_AUTO_PARK") && path.startsWith("/s/")) row.autoPark = action === "AUTO_PARK";
    if (action === "UNFOLLOW") Object.assign(row, { unfollowed: true, unreadAt: undefined, note: undefined });
    if (action === "FOLLOW") row.unfollowed = false;
  }
  const taken = (name: string, id?: string) => saved.spaces.some((space) => space.id !== id && space.name.toLowerCase() === name.toLowerCase());
  const has = (space: string, path: string) => saved.shared.some((tab) => tab.space === space && tab.path === path);
  if ([input.createSpace && taken(input.createSpace), input.renameSpace && taken(input.renameSpace.name, input.renameSpace.id), input.deleteSpace && saved.spaces.length === 1, input.share?.path === "/"].some(Boolean)) {
    return Response.json({ message: "another space has that name, it is the last space, or it is New session", code: 3 }, { status: 400 });
  }
  if (input.state && (input.revision ?? 0) !== saved.revision) return Response.json({ ...saved, stale: !!++saved.refused });
  if (input.state) {
    const open = new Set(input.state.tabs.map((tab) => tab.path));
    const closing = saved.state.tabs.flatMap(({ path, space, search }) => open.has(path) || path === "/" || path.startsWith("group:") ? [] : [{ path, space, search }]);
    saved.closed = [...closing, ...saved.closed.filter((tab) => !open.has(tab.path) && !closing.some((item) => item.path === tab.path))].slice(0, 50);
    for (const row of saved.attention) if (row.parked && !row.until && closing.some((tab) => tab.path === row.path)) row.parked = false;
  }
  if (input.state) [saved.state, saved.revision] = [input.state, saved.revision + 1];
  if (input.createSpace) saved.spaces.push({ id: `space-${saved.spaces.length}-${input.createSpace}`, name: input.createSpace });
  if (input.renameSpace) saved.spaces.find((space) => space.id === input.renameSpace!.id)!.name = input.renameSpace.name;
  if (input.deleteSpace) {
    saved.spaces = saved.spaces.filter((space) => space.id !== input.deleteSpace);
    const first = saved.spaces[0].id;
    saved.shared = saved.shared.filter((tab) => tab.space !== input.deleteSpace || !has(first, tab.path)).map((tab) => tab.space === input.deleteSpace ? { ...tab, space: first } : tab);
  }
  if (input.share && !has(input.share.space, input.share.path)) saved.shared.push(input.share);
  // An edit replaces a Shared Search page's one page when made from the page as saved; one made from a page that has
  // since changed, or that is no longer shared, is refused as stale. Taking a page out of Shared returns it.
  const edited = input.editShared && saved.shared.find((tab) => tab.space === input.editShared!.space && tab.path === input.editShared!.path);
  if (input.editShared && (!edited || plain(edited.search) !== plain(input.editShared.base))) return Response.json({ ...saved, stale: true });
  if (edited) edited.search = input.editShared!.search;
  const unshared = input.unshare && saved.shared.find((tab) => tab.space === input.unshare!.space && tab.path === input.unshare!.path)?.search;
  if (input.unshare) saved.shared = saved.shared.filter((tab) => tab.space !== input.unshare!.space || tab.path !== input.unshare!.path);
  if (unshared) return Response.json({ ...saved, unshared });
  return Response.json(saved);
}

export async function withPage(options: object, run: (page: any, origin: string, prompts: { id: string; text: string }[], saved: Saved) => Promise<void>, teardown?: { pageErrorsChecked: boolean; browserCloseStarted?: number; browserClosed: boolean }) {
  const { chromium: engine } = await import(playwright!);
  const prompts: { id: string; text: string }[] = [];
  const sessions = structuredClone(rows);
  const saved: Saved = { spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "", last: {}, sharedOrder: [] }, revision: 0, refused: 0, shared: [], closed: [], lists: [], visits: [], attention: [], sent: [], polls: 0, deletes: [], agents: [{ name: "main" }, { name: "opsbot", space: "OPS" }, { name: "channel", sticky: "slack" }], updated: {} };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    // A session's attention rides its row; the other tabs' come as tab rows.
    if (url.pathname === "/api/ListSessions") {
      saved.polls++;
      const listed = sessions.map((row) => ({ ...row, updatedAt: saved.updated[row.id], ...saved.attention.find((item) => item.path === href(row.id)) })), tabs = saved.attention.filter((item) => !item.path.startsWith("/s/"));
      return new Response(`data: ${JSON.stringify({ sessions: listed, owner: "tester", upstreamSuccess: true, tabs })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      if (await Bun.file(`${file}.gz`).exists()) return unpacked(file);
      // Named like the Go handler names a server's page.
      const index = (await unpacked(path.join(dist, "index.html")).text()).replace("<title>RocketClaw</title>", "<title>lab - RocketClaw</title>");
      return new Response(index, { headers: { "Content-Type": "text/html" } });
    }
    const input = await request.json().catch(() => ({})) as { id: string; text: string; name: string; query: string; agent?: string } & Parameters<typeof tabsCall>[1];
    if (url.pathname === "/api/UpdateSession" && !input.name) await saved.titleGate;
    if (url.pathname === "/api/UpdateSession") sessions.find((row) => row.id === input.id)!.name = input.name || "Regenerated";
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "tabs" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "tabs" } });
      case "/api/ListAgents": return Response.json({ agents: saved.agents, currentAgent: "main" });
      case "/api/ListRoutines": return Response.json({ jobs: [{ stem: "daily", status: "" }, { stem: "opsreport", status: "", space: "Ops" },
        { stem: "opsreport", status: "ran", lastRun: new Date().toISOString() }, { stem: "retired", status: "ran", lastRun: new Date().toISOString() }] });
      case "/api/History": return Response.json({ messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
      case "/api/CreateSession":
        // opsbot names the OPS space, which its first session creates, as the server does.
        if (input.agent === "opsbot") {
          saved.spaces.push({ id: "ops", name: "OPS" });
          sessions.push({ id: "fresh", name: "Ops chat", agent: "opsbot", spaceId: "ops" });
        }
        return Response.json({ id: "fresh" });
      case "/api/Prompt": prompts.push(input); return Response.json({ privateText: input.text === "/goal stop" ? "Goal stopped." : "" });
      case "/api/GetGoal": return Response.json(input.id === "juliet" ? julietGoal : {});
      case "/api/Tabs": case "/api/ChangeTabs": return tabsCall(saved, input);
      case "/api/DeleteSession":
        if (saved.deletes.push(input.id) === 1) return Response.json({ message: "this session has a running turn; stop it first", code: 9 }, { status: 400 });
        sessions.splice(sessions.findIndex((row) => row.id === input.id), 1);
        return Response.json({});
      // Fixed fixture: searching a row's exact name returns that row.
      case "/api/SearchSessions": return Response.json({ terms: [], text: input.query, needle: input.query.toLowerCase(), matches: rows.filter((row) => row.name === input.query).map((row) => ({ conversationId: row.id, field: "Name", text: row.name })), messages: [], mentionIds: [] });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, ...options });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await run(page, `http://127.0.0.1:${server.port}`, prompts, saved);
    expect(errors).toEqual([]);
    if (teardown) teardown.pageErrorsChecked = true;
  } finally {
    if (teardown) teardown.browserCloseStarted = performance.now();
    await browser.close();
    if (teardown) teardown.browserClosed = true;
    server.stop(true);
  }
}

// The current space's card is the one whose name is marked current.
export const current = '[aria-current="true"]', strip = (page: any) => page.locator(`div:has(> div > button${current}) > [role="tablist"][data-card]`);
export const names = (page: any) => strip(page).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
export const italic = (tab: any) => tab.evaluate((node: HTMLElement) => getComputedStyle(node).fontStyle === "italic");
// The open palette hides the strip from role queries, so tabs are read as names, an italic one marked "*".
export const behind = (page: any) => page.locator(`div:has(> div > button${current}) > [role="tablist"] [role="tab"]`).evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => `${node.getAttribute("aria-label")}${getComputedStyle(node).fontStyle === "italic" ? "*" : ""}`));
// The palette focuses its input after it shows its rows; arrows pressed before then are lost.
export async function paletteReady(page: any) {
  await page.getByRole("dialog", { name: "Go to session" }).getByRole("button").filter({ hasText: "Echo" }).waitFor();
  await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Search sessions and commands");
}
export const animation = (locator: any) => locator.evaluate((node: Element) => getComputedStyle(node).animationName);
// Group headers are the strip's only controls that report aria-expanded.
export const order = (page: any) => strip(page).locator('[role="tab"], [aria-expanded]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
// Saves reach the server after the page changes, so a test waits for them before reloading.
export async function until(check: () => boolean) {
  for (const started = Date.now(); !check(); await Bun.sleep(10)) if (Date.now() - started > 5_000) throw new Error("the saved tab state never matched");
}
export async function openSaved(page: any, origin: string, link: string, name: string, saved: Saved) {
  await page.goto(`${origin}${link}`);
  await strip(page).getByRole("tab", { name, exact: true }).waitFor();
  await until(() => saved.state.tabs.some((tab) => tab.path === link));
}
export async function openFromPalette(page: any, name: string) {
  await page.keyboard.press("Control+p");
  const palette = page.getByRole("dialog", { name: "Go to session" });
  await palette.getByRole("button").filter({ hasText: name }).waitFor();
  await palette.getByPlaceholder("Search sessions").fill(name);
  await palette.getByPlaceholder("Search sessions").press("Enter");
  await palette.waitFor({ state: "hidden" });
}
// An in-app link, like a Search result, opens a session without the palette.
export async function follow(page: any, id: string) {
  await page.evaluate((link: string) => { history.pushState(null, "", link); dispatchEvent(new PopStateEvent("popstate")); }, href(id));
  await page.waitForURL(`**${href(id)}`);
}
export async function menu(page: any, name: string, ...items: string[]) {
  await strip(page).getByRole("tab", { name, exact: true }).click({ button: "right" });
  for (const item of items) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
}
// A space card's actions, beside its title: Go to space enters it; Rename and Delete space change it.
async function spaceAction(page: any, space: string, item: string) {
  await page.getByRole("button", { name: `${space} space actions`, exact: true }).click();
  await page.getByRole("menuitem", { name: item, exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
}
// Entering a space is its card's Go to space; Rename and Delete space are the current card's actions.
export async function chooseSpace(page: any, item: string, role = "menuitem") {
  if (role === "menuitemradio") {
    await spaceAction(page, item, "Go to space");
    return page.locator(`button${current}[aria-label="${item}"]`).waitFor();
  }
  await spaceAction(page, await page.locator(`button${current}`).getAttribute("aria-label"), item);
}
// New space is a palette command, one per server with several, that asks for the space's name.
export async function newSpace(page: any, name: string, command = "Spaces: New space") {
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: command, exact: true }).click();
  await page.getByRole("textbox", { name: "Space name" }).fill(name);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("dialog", { name: "New space" }).waitFor({ state: "hidden" });
}
export const shared = (page: any, space = "Main") => page.getByRole("tablist", { name: `${space} Shared tabs` });
export const sharedNames = (page: any) => shared(page).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
export const focus = (page: any) => page.evaluate(() => { dispatchEvent(new Event("focus")); });
// The open menu's items in order, with "—" for the line between two sections.
export const menuLines = (page: any): Promise<string[]> => page.getByRole("menu").first().locator('[role="menuitem"], [role="separator"]')
  .evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("role") === "separator" ? "—" : node.innerText));
// Opens a Shared row's menu and lists its items and section lines, leaving it open.
export async function sharedMenu(page: any, name: string) {
  await shared(page).getByRole("tab", { name, exact: true }).click({ button: "right" });
  await page.getByRole("menuitem").first().waitFor();
  return menuLines(page);
}
