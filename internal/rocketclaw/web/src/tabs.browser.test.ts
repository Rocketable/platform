import { expect, test } from "bun:test";
import path from "node:path";
import { encodeSessionId } from "./session-id";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const sessions = [{ id: "alpha", name: "Alpha" }, { id: "beta", name: "Beta", running: true }, { id: "gamma", name: "Gamma" }];
const path64 = (id: string) => `/s/${encodeSessionId(id)}`;

function serve() {
  const created: string[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [...sessions, ...created.map((id) => ({ id, name: "Created" }))], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "tabs" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "tabs-browser" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/CreateSession") { created.push("created"); return Response.json({ id: "created" }); }
    if (url.pathname === "/api/Prompt") return Response.json({ privateText: "" });
    if (url.pathname === "/api/SearchMessages" || url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  return { server, origin: `http://127.0.0.1:${server.port}` };
}

type Page = Awaited<ReturnType<Awaited<ReturnType<typeof launch>>["newPage"]>>;
async function launch() {
  const { chromium: engine } = await import(playwright!);
  return engine.launch({ executablePath: chromium, headless: true });
}

const strip = (page: Page) => page.getByRole("tablist", { name: "Open tabs" });
const names = (page: Page) => strip(page).getByRole("tab").allTextContents();
const selected = (page: Page) => strip(page).locator('[role="tab"][aria-selected="true"]').textContent();
const at = (page: Page, origin: string, pathname: string) => page.waitForURL((url: URL) => url.origin === origin && url.pathname === pathname);

test("browser tabs keep per-tab history, background tabs, reopen and placement", async () => {
  const { server, origin } = serve();
  const browser = await launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(origin + path64("alpha"));
    await strip(page).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
    expect(await names(page)).toEqual(["Alpha"]);
    expect(await strip(page).getAttribute("aria-orientation")).toBe("horizontal");

    // In-tab navigation replaces the tab's location and keeps its own back/forward stack.
    await page.locator("footer").getByRole("button", { name: "Search sessions" }).click();
    await at(page, origin, "/search");
    expect(await names(page)).toEqual(["Search"]);
    // Browser Back and Forward step along the same stack.
    await page.goBack();
    await at(page, origin, path64("alpha"));
    expect(await page.getByRole("button", { name: "Tab forward" }).isEnabled()).toBe(true);
    await page.goForward();
    await at(page, origin, "/search");
    await page.getByRole("button", { name: "Tab back" }).click();
    await at(page, origin, path64("alpha"));
    expect(await names(page)).toEqual(["Alpha"]);
    await page.getByRole("button", { name: "Tab forward" }).click();
    await at(page, origin, "/search");
    // A link back to the previous location is a new step, not Tab back.
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("alpha");
    await page.getByLabel("Search results").getByRole("link", { name: "Alpha", exact: true }).click();
    await at(page, origin, path64("alpha"));
    expect(await page.getByRole("button", { name: "Tab back" }).isEnabled()).toBe(true);
    await page.getByRole("button", { name: "Tab back" }).click();
    await at(page, origin, "/search");
    await page.getByRole("button", { name: "Tab back" }).click();
    await at(page, origin, path64("alpha"));

    // "+" opens the composer in a new tab.
    await page.getByRole("button", { name: "New tab" }).click();
    await at(page, origin, "/");
    expect(await names(page)).toEqual(["Alpha", "New session"]);
    expect(await selected(page)).toBe("New session");
    await page.locator("footer").getByRole("button", { name: "Search sessions" }).click();
    await at(page, origin, "/search");
    expect(await names(page)).toEqual(["Alpha", "Search"]);

    // A location open in another tab activates that tab instead of duplicating it.
    await strip(page).getByRole("tab", { name: "Alpha", exact: true }).click();
    await at(page, origin, path64("alpha"));
    await page.locator("footer").getByRole("button", { name: "Search sessions" }).click();
    await at(page, origin, "/search");
    expect(await names(page)).toEqual(["Alpha", "Search"]);
    expect(await selected(page)).toBe("Search");
    // Browser Back follows the URL history and activates the tab that owns it.
    await page.goBack();
    await at(page, origin, path64("alpha"));
    expect(await names(page)).toEqual(["Alpha", "Search"]);
    expect(await selected(page)).toBe("Alpha");
    await page.goForward();
    await at(page, origin, "/search");

    // Modifier-click and middle-click open background tabs.
    await search.fill("gamma");
    await page.getByLabel("Search results").getByRole("link", { name: "Gamma", exact: true }).click({ modifiers: ["ControlOrMeta"] });
    await strip(page).getByRole("tab", { name: "Gamma", exact: true }).waitFor();
    expect(new URL(page.url()).pathname).toBe("/search");
    expect(await selected(page)).toBe("Search");
    await search.fill("beta");
    await page.getByLabel("Search results").getByRole("link", { name: "Beta", exact: true }).click({ button: "middle" });
    await strip(page).getByRole("tab", { name: "Beta" }).waitFor();
    expect(await names(page)).toEqual(["Alpha", "Search", "Beta", "Gamma"]);
    expect(new URL(page.url()).pathname).toBe("/search");
    expect(page.context().pages()).toHaveLength(1);
    // A running session's tab shows the running indicator.
    await strip(page).getByRole("tab", { name: "Beta" }).getByRole("img", { name: "Turn running" }).waitFor();

    // Closing a tab keeps its history for "Reopen closed tab".
    await strip(page).getByRole("button", { name: "Close Search" }).click();
    expect(await names(page)).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(await selected(page)).toBe("Beta");
    await at(page, origin, path64("beta"));
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Reopen closed tab" }).click();
    await at(page, origin, "/search");
    expect(await selected(page)).toBe("Search");
    await page.getByRole("button", { name: "Tab back" }).click();
    await at(page, origin, "/");
    expect(await selected(page)).toBe("New session");

    // Reload restores tabs, each tab's location and the active tab.
    const before = await names(page);
    await page.reload();
    await strip(page).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
    expect(await names(page)).toEqual(before);
    expect(await selected(page)).toBe("New session");
    expect(new URL(page.url()).pathname).toBe("/");
    await page.getByRole("button", { name: "Tab forward" }).click();
    await at(page, origin, "/search");

    // Sending from the composer tab turns that same tab into the new session.
    await page.getByRole("button", { name: "New tab" }).click();
    await at(page, origin, "/");
    const count = (await names(page)).length;
    const index = (await names(page)).indexOf("New session");
    await page.getByPlaceholder("Message a new session").fill("hello");
    await page.getByRole("button", { name: "Send" }).click();
    await at(page, origin, path64("created"));
    expect((await names(page)).length).toBe(count);
    await strip(page).getByRole("tab", { name: "Created", exact: true }).waitFor();
    expect((await names(page))[index]).toBe("Created");

    // Keyboard: arrows move focus, Enter activates, Delete closes; close buttons are labelled.
    await strip(page).getByRole("tab", { name: "Alpha", exact: true }).click();
    await at(page, origin, path64("alpha"));
    await strip(page).getByRole("tab", { name: "Alpha", exact: true }).focus();
    await page.keyboard.press("ArrowRight");
    expect(await page.evaluate(() => document.activeElement?.textContent)).toBe((await names(page))[1]);
    await page.keyboard.press("End");
    await page.keyboard.press("Enter");
    await at(page, origin, path64("gamma"));
    await page.keyboard.press("Home");
    expect(await page.evaluate(() => document.activeElement?.textContent)).toBe("Alpha");
    expect(await strip(page).getByRole("tab", { name: "Alpha", exact: true }).getAttribute("tabindex")).toBe("-1");
    expect(await strip(page).getByRole("tab", { name: "Gamma", exact: true }).getAttribute("tabindex")).toBe("0");
    expect(await strip(page).getByRole("button", { name: "Close Gamma" }).getAttribute("tabindex")).toBe("0");

    // Escape on a page closes the page's tab.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "List Agents" }).click();
    await at(page, origin, "/agents");
    expect(await names(page)).toContain("Agents");
    expect(await names(page)).not.toContain("Gamma");
    await page.locator('[data-slot="dialog-overlay"]').waitFor({ state: "hidden" });
    await page.mouse.move(0, 0);
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => ![...document.querySelectorAll('[role="tablist"][aria-label="Open tabs"] [role="tab"]')].some((tab) => tab.textContent === "Agents"));

    // Placement toggles from the strip and the palette, and survives reload.
    await page.getByRole("button", { name: "Move tabs to left" }).click();
    expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
    expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("left");
    await page.reload();
    await strip(page).waitFor();
    expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
    const stripBox = (await strip(page).boundingBox())!;
    const mainBox = (await page.locator("main").boundingBox())!;
    expect(stripBox.x + stripBox.width).toBeLessThanOrEqual(mainBox.x + 1);
    // Below md, left placement falls back to a horizontal strip on top that scrolls.
    await page.setViewportSize({ width: 375, height: 700 });
    await page.waitForFunction(() => document.querySelector('[role="tablist"][aria-label="Open tabs"]')?.getAttribute("aria-orientation") === "horizontal");
    expect(await strip(page).evaluate((node: HTMLElement) => getComputedStyle(node).overflowX)).toBe("auto");
    expect(await strip(page).evaluate((node: HTMLElement) => node.scrollWidth > node.clientWidth)).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Move to top" }).click();
    expect(await strip(page).getAttribute("aria-orientation")).toBe("horizontal");
    expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("top");
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("browser tabs reset on corrupt storage, honor deep links and close to the composer", async () => {
  const { server, origin } = serve();
  const browser = await launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    for (const corrupt of ["{not json", JSON.stringify({ tabs: [{ id: 1 }], closed: [], active: "x" })]) {
      await page.goto(origin + "/cron");
      await strip(page).waitFor();
      await page.evaluate((value: string) => localStorage.setItem("tabs-browser:tester", value), corrupt);
      await page.goto(origin + path64("alpha"));
      await strip(page).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
      expect(await names(page)).toEqual(["Alpha"]);
    }
    // A deep link to a location that is not open adds a tab and activates it.
    await page.goto(origin + path64("gamma"));
    await strip(page).getByRole("tab", { name: "Gamma", exact: true }).waitFor();
    expect(await names(page)).toEqual(["Alpha", "Gamma"]);
    expect(await selected(page)).toBe("Gamma");
    // Closing the active tab activates its neighbour; closing the last lands on the composer.
    await strip(page).getByRole("tab", { name: "Gamma", exact: true }).press("Delete");
    await at(page, origin, path64("alpha"));
    await page.waitForFunction(() => document.activeElement?.getAttribute("aria-selected") === "true" && document.activeElement.textContent === "Alpha");
    await strip(page).getByRole("button", { name: "Close Alpha" }).click();
    await at(page, origin, "/");
    expect(await names(page)).toEqual(["New session"]);
    // The lone composer tab cannot be closed.
    expect(await strip(page).getByRole("button", { name: "Close New session" }).count()).toBe(0);
    // A page's Close button closes its tab.
    await page.goto(origin + "/skills");
    await page.locator("main").getByRole("button", { name: "Close", exact: true }).click();
    await at(page, origin, "/");
    expect(await names(page)).toEqual(["New session"]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);

test("browser tab lists sync across windows while each window keeps its active tab", async () => {
  const { server, origin } = serve();
  const browser = await launch();
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const first = await context.newPage();
    await first.goto(origin + path64("alpha"));
    await strip(first).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
    const second = await context.newPage();
    await second.goto(origin + path64("alpha"));
    await strip(second).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
    await second.getByRole("button", { name: "New tab" }).click();
    await at(second, origin, "/");
    await strip(first).getByRole("tab", { name: "New session" }).waitFor();
    expect(await names(first)).toEqual(["Alpha", "New session"]);
    expect(await selected(first)).toBe("Alpha");
    expect(new URL(first.url()).pathname).toBe(path64("alpha"));
    await strip(second).getByRole("tab", { name: "Alpha", exact: true }).click();
    await at(second, origin, path64("alpha"));
    await strip(first).getByRole("tab", { name: "New session" }).click();
    await at(first, origin, "/");
    // Storage events arrive in order, so once the second window shows a later list change from the first,
    // it has handled anything the activation could have sent.
    await first.locator("footer").getByRole("button", { name: "Search sessions" }).click();
    await strip(second).getByRole("tab", { name: "Search" }).waitFor();
    expect(new URL(second.url()).pathname).toBe(path64("alpha"));
    expect(await selected(second)).toBe("Alpha");
    // When both windows show one tab, navigating it in one window moves the other along without echoing back.
    await strip(first).getByRole("tab", { name: "Alpha", exact: true }).click();
    await at(first, origin, path64("alpha"));
    await second.keyboard.press("Control+Shift+p");
    await second.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "List Agents" }).click();
    await at(first, origin, "/agents");
    const locations = () => first.evaluate(() => (JSON.parse(localStorage.getItem("tabs-browser:tester")!) as { tabs: { location: string }[] }).tabs.map((tab) => tab.location));
    expect(await locations()).toEqual(["/agents", "/search"]);
    // A later change from the first window reaches the second after any echo would have.
    await first.getByRole("button", { name: "New tab" }).click();
    await strip(second).getByRole("tab", { name: "New session" }).waitFor();
    expect(await locations()).toEqual(["/agents", "/", "/search"]);
    expect(new URL(second.url()).pathname).toBe("/agents");
    expect(await names(second)).toEqual(["Agents", "New session", "Search"]);
    // A storage event can arrive after a newer write; every window adopts the stored list, not the event's value.
    await first.evaluate(() => {
      const key = "tabs-browser:tester";
      const current = JSON.parse(localStorage.getItem(key)!) as { tabs: unknown[] };
      const withTab = (location: string) => JSON.stringify({ ...current, tabs: [...current.tabs, { location, back: [], forward: [] }] });
      localStorage.setItem(key, withTab("/cron"));
      dispatchEvent(new StorageEvent("storage", { key, newValue: withTab("/skills") }));
    });
    await strip(first).getByRole("tab").nth(3).waitFor();
    await strip(second).getByRole("tab").nth(3).waitFor();
    expect(await names(first)).toEqual(["Agents", "New session", "Search", "Cron"]);
    expect(await names(second)).toEqual(await names(first));
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
