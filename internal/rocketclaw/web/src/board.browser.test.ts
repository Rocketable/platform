import { expect, test } from "bun:test";
import path from "node:path";
import { decodeSessionId, encodeSessionId } from "./session-id";
import type { SearchPage, Session } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

const styles = ["strips", "dispatch", "triage", "kanban", "radar"];
const labels = ["Strip Bay", "Dispatch Console", "Triage Census", "Kanban", "Radar Scope"];
const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
// A row in state for age minutes that arrived arrived minutes ago, in the space "main" unless extra says otherwise.
const row = (id: string, workState: NonNullable<Session["workState"]>, age: number, arrived: number, extra: Partial<Session> = {}): Session =>
  ({ id, title: id, agent: "main", spaceId: "main", workState, stateSince: ago(age), arrivedAt: ago(arrived), updatedAt: ago(arrived), ...extra });
const fourLanes = [["Needs you", "is:waiting"], ["Running", "is:running"], ["Queued", "is:queued"], ["External", "is:external"]];
const matchers: Record<string, (session: Session) => boolean> = {
  "is:waiting": (session) => session.workState === "NEEDS_YOU", "is:running": (session) => session.workState === "RUNNING",
  "is:queued": (session) => session.workState === "QUEUED", "is:external": (session) => !!session.external,
};

// serve keeps a Dashboard at /search/d with the given style and lanes, answers ListSessions with only the rows changed
// since the version a poll holds, and sorts the space's rows into lanes for Board as the server does: first lane wins.
// "is:many" stands for a lane that matched more sessions than it lists.
function serve(board: string, lanes = fourLanes) {
  const rows = new Map([
    row("w1", "NEEDS_YOU", 5, 5), row("r1", "RUNNING", 30, 40), row("r2", "RUNNING", 2, 3),
    row("q1", "QUEUED", 20, 20), row("q2", "QUEUED", 70, 90), row("q3", "QUEUED", 1, 50),
    row("x1", "IDLE", 90, 120, { external: true }), row("o1", "RUNNING", 1, 1, { spaceId: "other" }),
  ].map((session) => [session.id, session]));
  const changed = new Set<string>(), requests: { spaceId: string; dashboards: { lanes: string[] }[] }[] = [];
  const control = { polls: 0, failing: "", gate: undefined as Promise<void> | undefined };
  let state = { tabs: [{ path: "/search/d", space: "main", search: { name: "Ops", board, searches: lanes.map(([name, query], i) => ({ id: String(i), name, query })), active: "0" } as SearchPage }], groups: [], space: "main", last: { main: "/search/d" } };
  let revision = 1;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    switch (url.pathname) {
      case "/api/ListSessions": {
        const sessions = url.searchParams.get("since") ? [...changed].map((id) => rows.get(id)!) : [...rows.values()];
        changed.clear();
        control.polls++;
        const done = { sessions: [], owner: "tester", upstreamSuccess: true, version: `v${control.polls}`, order: [...rows.keys()] };
        return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: false })}\n\ndata: ${JSON.stringify(done)}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
      }
      case "/api/Board": {
        const input = await request.json() as typeof requests[number];
        requests.push(input);
        await control.gate;
        if (control.failing) return Response.json({ message: control.failing, code: 13 }, { status: 500 });
        return Response.json({ dashboards: input.dashboards.map(({ lanes }) => {
          const taken = new Set<string>();
          return { lanes: lanes.map((query) => {
            if (query === "is:many") return { sessions: [rows.get("w1")], total: 250 };
            const sessions = [...rows.values()].filter((session) => (session.spaceId === input.spaceId || !session.spaceId) && !taken.has(session.id) && (matchers[query] ?? ((other) => other.title === query))(session));
            sessions.forEach((session) => taken.add(session.id));
            return { sessions, total: sessions.length };
          }) };
        }) });
      }
      case "/api/Tabs": {
        const body = await request.json() as { state?: typeof state };
        if (body.state) [state, revision] = [body.state, revision + 1];
        return Response.json({ spaces: [{ id: "main", name: "Main" }, { id: "other", name: "Other" }], state, revision: String(revision), shared: [], mutes: [] });
      }
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "board" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: [], messages: [], mentionIds: [] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  return {
    server, requests, control, origin: `http://127.0.0.1:${server.port}/search/d`, saved: () => state.tabs[0].search,
    update: (id: string, change: Partial<Session>) => { rows.set(id, { ...rows.get(id)!, ...change }); changed.add(id); },
  };
}

async function until(check: () => boolean | Promise<boolean>, what: string) {
  for (let tries = 0; !await check(); tries++) {
    if (tries > 200) throw new Error(`timed out waiting for ${what}`);
    await Bun.sleep(50);
  }
}

async function open(fixture: ReturnType<typeof serve>, body: (page: any) => Promise<void>, media: object = {}) {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.emulateMedia(media);
    await page.goto(fixture.origin);
    await body(page);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    fixture.server.stop(true);
  }
}

const lane = (page: any, name: string) => page.locator(`[data-board] section[aria-label="${name}"]`);
const cards = (page: any, name: string) => lane(page, name).locator("li").allTextContents();
// ids are the sessions a lane's cards open, in drawn order.
const ids = (page: any, name: string): Promise<string[]> => lane(page, name).locator("li a").evaluateAll((links: HTMLAnchorElement[]) => links.map((link) => link.pathname.split("/").at(-1))).then((paths: string[]) => paths.map(decodeSessionId));
const boardAnimations = (page: any): Promise<number> => page.locator("[data-board]").evaluate((board: HTMLElement) => board.getAnimations({ subtree: true }).length);
const choose = async (page: any, label: string) => {
  await page.getByRole("combobox", { name: "Board style" }).click();
  await page.getByRole("option", { name: label, exact: true }).click();
};

test("each board style draws the lanes as cards in its order, with age cues and the lane total", async () => {
  const fixture = serve("strips", [...fourLanes, ["Many", "is:many"]]);
  await open(fixture, async (page) => {
    await page.locator('[data-board="strips"]').waitFor();
    // Strip Bay shows arrival newest first, Kanban arrival oldest first, Dispatch Console the longest in its state first.
    const orders: Record<string, string[]> = { strips: ["q1", "q3", "q2"], kanban: ["q2", "q3", "q1"], dispatch: ["q2", "q1", "q3"] };
    for (const [i, style] of styles.entries()) {
      if (i) await choose(page, labels[i]);
      await page.locator(`[data-board="${style}"]`).waitFor();
      await until(() => fixture.saved()?.board === style, `${style} to be saved`);
      expect(await page.locator("[data-board] section").count()).toBe(5);
      expect(await Promise.all(["Needs you", "Running", "Queued", "External"].map(async (name) => (await cards(page, name)).length))).toEqual([1, 2, 3, 1]);
      if (orders[style]) expect(await ids(page, "Queued")).toEqual(orders[style]);
    }
    // Keyboard focus walks the cards in the order they are drawn.
    await choose(page, "Kanban");
    const links = page.locator("[data-board] li a");
    await links.first().focus();
    const walked = [];
    for (let i = 0; i < 7; i++) {
      walked.push(await page.evaluate(() => document.activeElement!.getAttribute("href")));
      await page.keyboard.press("Tab");
    }
    expect(walked).toEqual(await links.evaluateAll((nodes: HTMLElement[]) => nodes.slice(0, 7).map((link) => link.getAttribute("href"))));
    // Age cues only for work that is not idle: amber from 15 minutes, red from 60.
    await choose(page, "Dispatch Console");
    const chip = (id: string) => page.locator(`[data-board] li:has(a[href$="/${encodeSessionId(id)}"]) em`).evaluate((node: HTMLElement) => getComputedStyle(node).boxShadow);
    const [fresh, amber, red, idle] = await Promise.all(["r2", "q1", "q2", "x1"].map(chip));
    expect(amber).not.toBe(fresh);
    expect(red).not.toBe(fresh);
    expect(red).not.toBe(amber);
    expect(idle).toBe(fresh);
    // Radar Scope: a lane's blips sit wholly inside its sector, the one its name labels, older ones farther from the centre.
    await choose(page, "Radar Scope");
    const blips: { id: string; inside: boolean; distance: number }[][] = await page.locator('[data-board="radar"]').evaluate((board: HTMLElement) => {
      const scope = board.getBoundingClientRect(), sections = [...board.querySelectorAll("section")], at = (node: Element) => (({ x, y, width, height }) => [x + width / 2 - scope.x - scope.width / 2, y + height / 2 - scope.y - scope.height / 2])(node.getBoundingClientRect());
      const sector = ([dx, dy]: number[]) => ((Math.atan2(dy, dx) / (2 * Math.PI) + 1) % 1) * sections.length;
      return sections.map((section, i) => [...section.querySelectorAll("li")].map((li) => {
        const [dx, dy] = at(li), turn = sector([dx, dy]) - i, distance = Math.hypot(dx, dy);
        return { id: li.querySelector("b")!.textContent!, inside: Math.floor(sector(at(section.querySelector("h3")!))) === i && turn > 0 && turn < 1 && distance * Math.sin(Math.min(turn, 1 - turn) * 2 * Math.PI / sections.length) >= li.getBoundingClientRect().width / 2, distance };
      }));
    });
    expect(blips.flat().filter((blip) => !blip.inside)).toEqual([]);
    const [q2, q1, q3] = blips[2].map((blip) => blip.distance);
    expect(q2).toBeGreaterThan(q1);
    expect(q1).toBeGreaterThan(q3);
    expect(await lane(page, "Many").locator("h3").allTextContents()).toEqual([expect.stringContaining("250")]);
    // List is an ordinary Search Page again.
    await choose(page, "List");
    await page.getByLabel("Search results").waitFor();
    expect(await page.locator("[data-board]").count()).toBe(0);
    await until(() => !fixture.saved()?.board, "List to be saved");
  });
}, 60_000);

test("the board follows session list changes in its space, one Board request at a time, marking new cards once", async () => {
  const fixture = serve("kanban");
  await open(fixture, async (page) => {
    await page.locator('[data-board="kanban"]').waitFor();
    expect(await page.locator('[data-board] li[data-new="true"]').count()).toBe(0);
    // The first listing arriving after the first request asks once more.
    await until(() => fixture.control.polls >= 2, "a poll");
    const first = fixture.requests.length;
    expect(fixture.requests[0]).toEqual({ spaceId: "main", dashboards: [{ lanes: ["is:waiting", "is:running", "is:queued", "is:external"] }] });
    // A changed row in another space asks for nothing.
    fixture.update("o1", { title: "other" });
    const polled = fixture.control.polls;
    await until(() => fixture.control.polls >= polled + 2, "two polls");
    expect(fixture.requests.length).toBe(first);
    // A queued session that starts running moves lanes without a reload, marked new once.
    fixture.update("q1", { workState: "RUNNING", stateSince: ago(0) });
    await until(async () => (await ids(page, "Running")).includes("q1"), "q1 to run");
    expect(await ids(page, "Queued")).toEqual(["q2", "q3"]);
    expect(await page.locator('[data-board] li[data-new="true"]').allTextContents()).toEqual([expect.stringContaining("q1")]);
    expect(await boardAnimations(page)).toBeGreaterThan(0);
    // Changes while a request is in flight ask once more after it, not once each.
    let release = () => {};
    fixture.control.gate = new Promise((resolve) => { release = resolve; });
    const before = fixture.requests.length;
    fixture.update("w1", { title: "w1 again" });
    await until(() => fixture.requests.length === before + 1, "a Board request");
    for (const id of ["r1", "r2"]) {
      const at = fixture.control.polls;
      fixture.update(id, { title: `${id} again` });
      await until(() => fixture.control.polls >= at + 1, "a poll");
    }
    expect(fixture.requests.length).toBe(before + 1);
    fixture.control.gate = undefined;
    release();
    await until(() => fixture.requests.length === before + 2, "the follow-up request");
    const settled = fixture.control.polls;
    await until(() => fixture.control.polls >= settled + 2, "two polls");
    expect(fixture.requests.length).toBe(before + 2);
    await until(async () => await page.locator('[data-board] li[data-new="true"]').count() === 0, "the new mark to settle");
    // An edited lane starts from what it holds: the Running lane's new query takes the queued cards, none of them new.
    await page.getByRole("tab", { name: "Running" }).click();
    await page.getByRole("textbox", { name: "Search sessions" }).fill("is:queued");
    await until(async () => (await ids(page, "Running")).includes("q2"), "the edited lane");
    expect(await page.locator('[data-board] li[data-new="true"]').count()).toBe(0);
  });
}, 60_000);

test("a failed Board request shows the search error with Retry instead of a board", async () => {
  const fixture = serve("radar");
  fixture.control.failing = "board broke";
  await open(fixture, async (page) => {
    // The first listing may arrive after the first request and ask again; both fail before Retry.
    await until(() => fixture.control.polls >= 2, "a poll");
    await page.getByRole("alert").filter({ hasText: "board broke" }).waitFor();
    expect(await page.locator("[data-board]").count()).toBe(0);
    fixture.control.failing = "";
    await page.getByRole("button", { name: "Retry" }).click();
    await page.locator('[data-board="radar"] li').first().waitFor();
    expect(await page.getByRole("alert").count()).toBe(0);
  });
}, 60_000);

test("under reduced motion no board style animates", async () => {
  const fixture = serve("strips");
  await open(fixture, async (page) => {
    await page.locator('[data-board="strips"]').waitFor();
    fixture.update("q1", { workState: "NEEDS_YOU", stateSince: ago(0) });
    await page.locator('[data-board] li[data-new="true"]').waitFor();
    for (const [i, style] of styles.entries()) {
      if (i) await choose(page, labels[i]);
      await page.locator(`[data-board="${style}"] li[data-new="true"]`).waitFor();
      expect(await boardAnimations(page)).toBe(0);
    }
  }, { reducedMotion: "reduce" });
}, 60_000);
