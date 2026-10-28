import { expect, test } from "bun:test";
import path from "node:path";
import type { SavedTabs, SearchPage } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type State = SavedTabs["state"];
type Shared = SavedTabs["shared"];

// serve keeps each person's tabs, named by the "who" cookie, and the Shared tabs everyone sees; Board answers empty lanes.
function serve(tabs: Record<string, State["tabs"]>, shared: Shared) {
  const people: Record<string, { state: State; revision: number }> = {};
  const person = (who: string) => people[who] ??= { state: { tabs: tabs[who] ?? [], groups: [], space: "main", last: {}, sharedOrder: [] }, revision: 1 };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url), who = /who=(\w+)/.exec(request.headers.get("cookie") ?? "")?.[1] ?? "alice";
    switch (url.pathname) {
      case "/api/ListSessions": return new Response(`data: ${JSON.stringify({ sessions: [], owner: who, upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
      case "/api/Board": {
        const input = await request.json() as { dashboards: { lanes: string[] }[] };
        return Response.json({ dashboards: input.dashboards.map(({ lanes }) => ({ lanes: lanes.map(() => ({ sessions: [], total: 0 })) })) });
      }
      case "/api/Tabs": {
        const input = await request.json() as { state?: State; share?: Shared[number]; editShared?: Shared[number] }, mine = person(who);
        if (input.state) [mine.state, mine.revision] = [input.state, mine.revision + 1];
        if (input.share) shared.push(input.share);
        const edited = input.editShared && shared.find((tab) => tab.space === input.editShared!.space && tab.path === input.editShared!.path);
        if (edited) edited.search = input.editShared!.search;
        return Response.json({ spaces: [{ id: "main", name: "Main" }], state: mine.state, revision: String(mine.revision), shared, mutes: [] });
      }
      case "/api/Identity": return Response.json({ username: who });
      case "/api/Protocol": return Response.json({ protoSha256: "dashboards" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  return { server, origin: `http://127.0.0.1:${server.port}`, shared, own: (who: string) => person(who).state.tabs };
}

async function until(check: () => boolean | Promise<boolean>, what: string) {
  for (let tries = 0; !await check(); tries++) {
    if (tries > 200) throw new Error(`timed out waiting for ${what}`);
    await Bun.sleep(25);
  }
}

// open shows link to each named person, each in a browser context of their own.
async function open(fixture: ReturnType<typeof serve>, link: string, body: (person: (who: string) => Promise<any>) => Promise<void>) {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  const errors: string[] = [];
  try {
    await body(async (who) => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      await context.addCookies([{ name: "who", value: who, url: fixture.origin }]);
      const page = await context.newPage();
      page.on("pageerror", (error: Error) => errors.push(error.message));
      await page.goto(fixture.origin + link);
      return page;
    });
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    fixture.server.stop(true);
  }
}

const strip = (page: any) => page.getByRole("tablist", { name: "Open tabs" });
const sharedRows = (page: any) => page.getByRole("tablist", { name: "Shared tabs" });
const lanes = (page: any) => page.locator("[data-board] section").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
const runCommand = async (page: any, label: string) => {
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: label, exact: true }).click();
};
// A page as configured: its name, board style, and lanes' names, queries, and notify marks, with the shown one's place.
const config = (page?: SearchPage) => page && { name: page.name, board: page.board, lanes: page.searches.map(({ name, query, notify }) => [name ?? "", query, !!notify]), shown: page.searches.findIndex((item) => item.id === page.active) };
const ids = (page?: SearchPage) => page?.searches.map((item) => item.id) ?? [];
const searchPath = (page: any) => new URL(page.url()).pathname;

test("New dashboard opens a shared Strip Bay board with the default lanes and notify marks, which a second person sees", async () => {
  const fixture = serve({}, []);
  await open(fixture, "/", async (person) => {
    const alice = await person("alice");
    await strip(alice).getByRole("tab", { name: "New session", exact: true }).waitFor();
    await runCommand(alice, "New dashboard");
    await alice.locator('[data-board="strips"]').waitFor();
    const at = searchPath(alice);
    expect(at).toMatch(/^\/search\/./);
    expect(await lanes(alice)).toEqual(["Needs you", "Running", "Queued", "External"]);
    expect(await alice.getByRole("combobox", { name: "Board style" }).innerText()).toMatch(/^Strip Bay\b/);
    // It is in the space's Shared Group, not one of Alice's own tabs.
    expect(fixture.shared.map((tab) => [tab.space, tab.path])).toEqual([["main", at]]);
    expect(config(fixture.shared[0].search)).toMatchObject({ board: "strips", lanes: [["Needs you", "is:waiting", true], ["Running", "is:running", false], ["Queued", "is:queued", true], ["External", "is:external", true]], shown: 0 });
    await sharedRows(alice).getByRole("tab").first().waitFor();
    expect(await strip(alice).locator(`[data-tab="${at}"]`).count()).toBe(0);
    expect(fixture.own("alice").some((tab) => tab.path === at)).toBe(false);

    // Bob looks before Alice clicks through the lanes: her click-through saves the shown lane for everyone, renaming the tab.
    const bob = await person("bob");
    await sharedRows(bob).getByRole("tab", { name: "is:waiting", exact: true }).click();
    await bob.waitForURL((url: URL) => url.pathname === at);
    await bob.locator('[data-board="strips"]').waitFor();
    expect(await lanes(bob)).toEqual(["Needs you", "Running", "Queued", "External"]);

    const marks = [];
    for (const name of ["Needs you", "Running", "Queued", "External"]) {
      await alice.getByRole("tablist", { name: "Searches" }).getByRole("tab", { name, exact: true }).click();
      marks.push(await alice.getByRole("button", { name: "Notify", exact: true }).getAttribute("aria-pressed"));
    }
    expect(marks).toEqual(["true", "false", "true", "true"]);
  });
}, 60_000);

test("Duplicate makes an own Search page's copy an own tab named '<name> copy' with the same lanes at a new address", async () => {
  const source: SearchPage = { name: "Ops", searches: [{ id: "a", name: "Run", query: "is:running" }, { id: "b", query: "is:queued", notify: true }], active: "b" };
  const fixture = serve({ alice: [{ path: "/search/own", space: "main", search: source }] }, []);
  await open(fixture, "/search/own", async (person) => {
    const alice = await person("alice");
    await strip(alice).getByRole("tab", { name: "Ops", exact: true }).click({ button: "right" });
    await alice.getByRole("menuitem", { name: "Duplicate", exact: true }).click();
    await strip(alice).getByRole("tab", { name: "Ops copy", exact: true }).waitFor();
    const at = searchPath(alice);
    expect(at).not.toBe("/search/own");
    await until(() => fixture.own("alice").some((tab) => tab.path === at), "the copy to be saved");
    const copy = fixture.own("alice").find((tab) => tab.path === at)!.search;
    expect(config(copy)).toEqual({ ...config(source)!, name: "Ops copy" });
    expect(ids(copy).some((id) => ids(source).includes(id))).toBe(false);
    expect(fixture.own("alice").find((tab) => tab.path === "/search/own")!.search).toEqual(source);
    expect(fixture.shared).toEqual([]);
  });
}, 60_000);

test("Duplicate makes a shared Dashboard's copy a second shared Dashboard, and editing the copy leaves the source alone", async () => {
  const source: SearchPage = { name: "Team", board: "kanban", searches: [{ id: "w", name: "Waiting", query: "is:waiting", notify: true }, { id: "r", name: "Running", query: "is:running" }], active: "w" };
  const fixture = serve({}, [{ space: "main", path: "/search/team", search: structuredClone(source) }]);
  await open(fixture, "/search/team", async (person) => {
    const alice = await person("alice");
    await alice.locator('[data-board="kanban"]').waitFor();
    await sharedRows(alice).getByRole("tab", { name: "Team", exact: true }).click({ button: "right" });
    await alice.getByRole("menuitem", { name: "Duplicate", exact: true }).click();
    await sharedRows(alice).getByRole("tab", { name: "Team copy", exact: true }).waitFor();
    const at = searchPath(alice);
    expect(fixture.shared.map((tab) => tab.path)).toEqual(["/search/team", at]);
    expect(config(fixture.shared[1].search)).toEqual({ ...config(source)!, name: "Team copy" });
    expect(ids(fixture.shared[1].search).some((id) => ids(source).includes(id))).toBe(false);
    expect(fixture.own("alice").some((tab) => tab.path === at)).toBe(false);
    await alice.locator('[data-board="kanban"]').waitFor();
    await alice.getByRole("textbox", { name: "Search sessions" }).fill("is:queued");
    await until(() => fixture.shared[1].search?.searches[0].query === "is:queued", "the copy's edit to be saved");
    expect(fixture.shared[0].search).toEqual(source);
  });
}, 60_000);

test("Duplicate from Cmd+Shift+P names an unnamed Search page's copy after its shown query", async () => {
  const fixture = serve({ alice: [{ path: "/search/u", space: "main", search: { searches: [{ id: "a", query: "deploys" }, { id: "b", query: "rollbacks" }], active: "b" } }] }, []);
  await open(fixture, "/search/u", async (person) => {
    const alice = await person("alice");
    await strip(alice).getByRole("tab", { name: "rollbacks", exact: true }).waitFor();
    await runCommand(alice, "Tabs: Duplicate");
    await strip(alice).getByRole("tab", { name: "rollbacks copy", exact: true }).waitFor();
    await until(() => fixture.own("alice").some((tab) => tab.search?.name === "rollbacks copy"), "the copy to be saved");
    expect(searchPath(alice)).not.toBe("/search/u");
  });
}, 60_000);
