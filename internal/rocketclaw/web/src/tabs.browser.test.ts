import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const rows = [
  { id: "alpha", name: "Alpha", agent: "main" },
  { id: "bravo", name: "Bravo", agent: "main" },
  { id: "charlie", name: "Charlie", agent: "main" },
  { id: "delta", name: "Delta", agent: "main", running: true },
];
const href = (id: string) => `/s/${Buffer.from(id).toString("base64url")}`;

async function withPage(options: object, run: (page: any, origin: string, prompts: { id: string; text: string }[]) => Promise<void>) {
  const { chromium: engine } = await import(playwright!);
  const prompts: { id: string; text: string }[] = [];
  const sessions = structuredClone(rows);
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json().catch(() => ({})) as { id: string; text: string; name: string; query: string };
    if (url.pathname === "/api/UpdateSession") sessions.find((row) => row.id === input.id)!.name = input.name;
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "tabs" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "tabs" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }, { name: "opsbot", space: "OPS" }], currentAgent: "main" });
      case "/api/ListCronJobs": return Response.json({ jobs: [{ stem: "daily", status: "" }, { stem: "opsreport", status: "", space: "Ops" },
        { stem: "opsreport", status: "ran", lastRun: new Date().toISOString() }, { stem: "retired", status: "ran", lastRun: new Date().toISOString() }] });
      case "/api/History": return Response.json({ messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
      case "/api/CreateSession": return Response.json({ id: "fresh" });
      case "/api/Prompt": prompts.push(input); return Response.json({ privateText: "" });
      // Fixed fixture: searching a row's exact name returns that row.
      case "/api/SearchSessions": return Response.json({ terms: [], text: input.query, needle: input.query.toLowerCase(), matches: rows.filter((row) => row.name === input.query).map((row) => ({ conversationId: row.id, field: "Name", text: row.name })), messages: [], mentionIds: [], indexComplete: true, summariesComplete: true });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, ...options });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await run(page, `http://127.0.0.1:${server.port}`, prompts);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}

const strip = (page: any) => page.getByRole("tablist", { name: "Open tabs" });
const names = (page: any) => strip(page).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
const italic = (tab: any) => tab.evaluate((node: HTMLElement) => getComputedStyle(node).fontStyle === "italic");
// Group headers are the strip's only controls that report aria-expanded.
const order = (page: any) => strip(page).locator('[role="tab"], [aria-expanded]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
async function openSaved(page: any, origin: string, link: string, name: string) {
  await page.goto(`${origin}${link}`);
  await strip(page).getByRole("tab", { name, exact: true }).waitFor();
  await page.waitForFunction((path: string) => JSON.parse(localStorage.getItem("tabs:tester") ?? "[]").some((saved: { path: string }) => saved.path === path), link);
}
async function openFromPalette(page: any, name: string) {
  await page.keyboard.press("Control+p");
  const palette = page.getByRole("dialog", { name: "Go to session" });
  await palette.getByRole("button").filter({ hasText: name }).waitFor();
  await palette.getByPlaceholder("Search sessions").fill(name);
  await palette.getByPlaceholder("Search sessions").press("Enter");
  await palette.waitFor({ state: "hidden" });
}
async function menu(page: any, name: string, ...items: string[]) {
  await strip(page).getByRole("tab", { name, exact: true }).click({ button: "right" });
  for (const item of items) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
}
async function chooseSpace(page: any, item: string, role = "menuitem") {
  await page.getByRole("button", { name: /^Space: / }).click();
  await page.getByRole(role, { name: item, exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
}

test("preview tabs, promotion, pins, close commands, middle-click, and drag survive reload", () => withPage({}, async (page, origin, prompts) => {
  await page.goto(`${origin}/`);
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await tab("New session").waitFor();
  await page.getByPlaceholder("Message a new session").fill("hello");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL(`**${href("fresh")}`);
  expect(await names(page)).toEqual(["hello"]);
  expect(prompts.map((prompt) => prompt.id)).toEqual(["fresh"]);

  await openFromPalette(page, "Alpha");
  await page.waitForURL(`**${href("alpha")}`);
  await openFromPalette(page, "Bravo");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["hello", "Bravo"]);
  expect(await italic(tab("Bravo"))).toBe(true);
  expect(await italic(tab("hello"))).toBe(false);
  await tab("Bravo").dblclick();
  expect(await italic(tab("Bravo"))).toBe(false);
  expect(await tab("Bravo").evaluate((node: HTMLElement) => document.activeElement === node && getComputedStyle(node).outlineStyle)).toBe("none");
  await openFromPalette(page, "Alpha");
  expect(await names(page)).toEqual(["hello", "Bravo", "Alpha"]);
  expect(await italic(tab("Alpha"))).toBe(true);
  await page.getByPlaceholder("Message or $command").fill("keep me");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForFunction(() => getComputedStyle(document.querySelector('[role="tab"][aria-selected="true"]')!).fontStyle !== "italic");

  await openFromPalette(page, "Charlie");
  expect(await italic(tab("Charlie"))).toBe(true);
  await page.getByPlaceholder("Message or $command").fill("unsent draft");
  expect(await italic(tab("Charlie"))).toBe(false);
  await openFromPalette(page, "Delta");
  expect(await names(page)).toEqual(["hello", "Bravo", "Alpha", "Charlie", "Delta"]);
  await menu(page, "Alpha", "Pin");
  expect(await names(page)).toEqual(["Alpha", "hello", "Bravo", "Charlie", "Delta"]);
  expect(await tab("Alpha").innerText()).toBe("");
  await menu(page, "Bravo", "Close to the right");
  expect(await names(page)).toEqual(["Alpha", "hello", "Bravo"]);
  await page.waitForURL(`**${href("bravo")}`);
  await openFromPalette(page, "Charlie");
  await menu(page, "Bravo", "Close others");
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");

  await openFromPalette(page, "Charlie");
  await tab("Charlie").dblclick();
  await openFromPalette(page, "Delta");
  await tab("Charlie").click();
  await page.waitForURL(`**${href("charlie")}`);
  await tab("Charlie").click({ button: "middle" });
  await page.waitForURL(`**${href("delta")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Delta"]);
  await tab("Delta").click({ button: "middle" });
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);

  await openFromPalette(page, "Charlie");
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  await tab("Charlie").dragTo(tab("Bravo"));
  expect(await names(page)).toEqual(["Alpha", "Charlie", "Bravo"]);
  await page.reload();
  await tab("Charlie").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Charlie", "Bravo"]);
  expect(await tab("Alpha").innerText()).toBe("");
  expect(await italic(tab("Charlie"))).toBe(true);
  expect(await tab("Charlie").getAttribute("aria-selected")).toBe("true");

  await menu(page, "Charlie", "Close all");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await names(page)).toEqual(["Alpha"]);
  await menu(page, "Alpha", "Close");
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["New session"]);
  await strip(page).getByRole("button", { name: "Close New session" }).click();
  expect(await names(page)).toEqual(["New session"]);
  expect(page.url()).toBe(`${origin}/`);
}), 60_000);

for (const width of [1280, 320]) test(`copy page link across pages at ${width}px`, () => withPage({ viewport: { width, height: 800 }, hasTouch: width === 320, permissions: ["clipboard-read", "clipboard-write"] }, async (page, origin) => {
  const copy = page.getByRole("button", { name: "Copy page link", exact: true });
  const status = page.locator("footer").getByRole("status");
  for (const pathname of ["/", `${href("alpha")}?view=one%20%26%20two#result%2Fthree`, "/search?q=tag%3A%22two%20words%22%20%26%20%2B%25#result%2Ftwo", "/cron", "/agents", "/skills", "/skills/example", "/config", "/voice"]) {
    await page.goto(`${origin}${pathname}`);
    // Search normalizes its address after identity loads; compare the clipboard with that settled URL.
    if (pathname.startsWith("/search")) await page.waitForURL(`${origin}/search?q=tag%3A%22two+words%22+%26+%2B%25`);
    if (pathname.startsWith("/s/")) expect(page.url()).toBe(`${origin}${pathname}`);
    await copy.waitFor();
    if (width === 320) await copy.tap();
    else { await copy.focus(); await page.keyboard.press("Enter"); }
    await status.filter({ hasText: "Page link copied" }).waitFor();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(page.url());
    expect(await copy.locator("svg.lucide-check").count()).toBe(1);
    const buttons = page.locator("#bottom-navigation").getByRole("button");
    const boxes = await buttons.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => {
      const { x, y, width, height } = node.getBoundingClientRect();
      return { x, y, width, height };
    }));
    expect(boxes).toHaveLength(5);
    for (const [index, box] of boxes.entries()) {
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(Math.round(box.width)).toBe(width === 320 ? 48 : 32);
      expect(Math.round(box.height)).toBe(width === 320 ? 48 : 32);
      if (index) expect(boxes[index - 1].x + boxes[index - 1].width).toBeLessThanOrEqual(box.x);
    }
  }
  if (width === 1280) {
    // Config can finish after Search canonicalizes its URL; that must not replace a focused copy control.
    const config = Promise.withResolvers<void>();
    await page.route("**/api/ListConfig", async (route: any) => { await config.promise; await route.continue(); });
    await page.goto(`${origin}/search?q=two%20words`);
    await page.waitForURL(`${origin}/search?q=two+words`);
    const focusedCopy = await copy.elementHandle();
    await copy.focus();
    config.resolve();
    await page.waitForFunction(() => !document.querySelector("main fieldset")!.hasAttribute("disabled"));
    expect(await focusedCopy.evaluate((node: HTMLElement) => ({ connected: node.isConnected, focused: document.activeElement === node }))).toEqual({ connected: true, focused: true });
    await page.keyboard.press("Enter");
    await status.filter({ hasText: "Page link copied" }).waitFor();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}/search?q=two+words`);
    await page.unroute("**/api/ListConfig");
    for (const name of ["Cron", "Agents", "Cron"]) {
      await page.getByRole("tab", { name, exact: true }).click();
      await page.waitForURL(`${origin}/${name.toLowerCase()}`);
    }
    const tabCopy = await copy.elementHandle();
    await copy.focus();
    await page.keyboard.press("Control+BracketRight");
    await page.waitForURL(`${origin}/agents`);
    expect(await tabCopy.evaluate((node: HTMLElement) => ({ connected: node.isConnected, focused: document.activeElement === node }))).toEqual({ connected: true, focused: true });
    await page.keyboard.press("Enter");
    await status.filter({ hasText: "Page link copied" }).waitFor();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}/agents`);
  }
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  await copy.click();
  await status.filter({ hasText: "Page link copied" }).waitFor();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}/`);
  await page.getByRole("button", { name: "Search sessions", exact: true }).click();
  await page.getByRole("dialog", { name: "Go to session" }).waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Open command palette", exact: true }).click();
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "detached" });
  await page.waitForURL((url: URL) => url.pathname === "/search");
  const probeBox = page.getByRole("main").getByRole("textbox", { name: "Search sessions", exact: true }); // PROBE
  const probeBefore = JSON.stringify({ url: page.url(), value: await probeBox.inputValue(), main: (await page.getByRole("main").innerText()).slice(0, 300) });
  await probeBox.fill("two words & +%");
  await page.waitForURL((url: URL) => url.searchParams.get("q") === "two words & +%", { timeout: 10_000 }).catch(async (error: unknown) => {
    console.error("PROBE before", probeBefore, "after", JSON.stringify({ url: page.url(), value: await probeBox.inputValue(), main: (await page.getByRole("main").innerText()).slice(0, 300) }));
    throw error;
  });
  await page.evaluate(() => { location.hash = "match%2Ftwo"; });
  await copy.click();
  await status.filter({ hasText: "Page link copied" }).waitFor();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(page.url());
  for (const pathname of ["/search", href("alpha")]) {
    await page.goto(`${origin}${pathname}`);
    await copy.click();
    await status.filter({ hasText: "Page link copied" }).waitFor();
    const input = pathname === "/search" ? page.getByRole("main").getByRole("textbox", { name: "Search sessions", exact: true }) : page.getByPlaceholder("Message or $command");
    await input.focus();
    await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("Copy denied"); } } }));
    await copy.click();
    await status.filter({ hasText: "Could not copy page link" }).waitFor();
    await input.focus();
    expect(await copy.locator("svg.lucide-circle-alert").isVisible()).toBe(true);
    expect(await copy.locator("svg.lucide-check").count()).toBe(0);
    expect(await status.textContent()).not.toContain("Page link copied");
    expect(await page.getByRole("tooltip").count()).toBe(0);
    if (pathname.startsWith("/s/")) {
      await input.fill("next message");
      expect(await page.getByRole("button", { name: "Send", exact: true }).evaluate((node: HTMLElement) => {
        const rect = node.getBoundingClientRect();
        const status = document.querySelector('footer [role="status"]')!.getBoundingClientRect();
        return {
          tappable: node.contains(document.elementFromPoint((rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2)),
          unobscured: status.right <= rect.left || status.left >= rect.right || status.bottom <= rect.top || status.top >= rect.bottom,
        };
      })).toEqual({ tappable: true, unobscured: true });
    }
  }
}), 60_000);

test("copy page link on plain HTTP without Clipboard API", () => withPage({ permissions: ["clipboard-read", "clipboard-write"] }, async (page, origin) => {
  await page.route("http://copy.test/**", async (route: any) => {
    if (new URL(route.request().url()).pathname === "/stream") return route.fulfill({ contentType: "text/event-stream", body: ": connected\n\n" });
    await route.fulfill({ response: await route.fetch({ url: route.request().url().replace("http://copy.test", origin) }) });
  });
  const url = `http://copy.test${href("alpha")}?view=one%20%26%20two#result%2Fthree`;
  await page.goto(url);
  expect(await page.evaluate(() => ({ secure: isSecureContext, clipboard: typeof navigator.clipboard }))).toEqual({ secure: false, clipboard: "undefined" });
  const copy = page.getByRole("button", { name: "Copy page link", exact: true });
  await copy.click();
  const status = page.locator("footer").getByRole("status");
  await status.filter({ hasText: "Page link copied" }).waitFor();
  expect(await copy.evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
  const reader = await page.context().browser().newPage({ permissions: ["clipboard-read", "clipboard-write"] });
  await reader.goto(origin);
  expect(await reader.evaluate(() => navigator.clipboard.readText())).toBe(url);
  await reader.close();
  await page.evaluate(() => { document.execCommand = () => false; });
  await copy.click();
  await status.filter({ hasText: "Could not copy page link" }).waitFor();
  expect(await status.isVisible()).toBe(true);
  expect(await copy.locator("svg.lucide-check").count()).toBe(0);
}), 30_000);

test("storage reset, deep links, keyboard, page close, palette commands, and placement", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await page.goto(`${origin}/`);
  await page.waitForFunction(() => localStorage.getItem("tabs:tester") !== null);
  for (const stored of ["{not json", JSON.stringify([{ path: href("charlie"), preview: "false" }, { path: href("charlie") }])]) {
    await page.evaluate((value: string) => localStorage.setItem("tabs:tester", value), stored);
    await page.goto(`${origin}${href("alpha")}`);
    await tab("Alpha").waitFor();
    expect(await names(page)).toEqual(["Alpha"]);
  }
  await page.goto(`${origin}${href("bravo")}`);
  await tab("Bravo").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");
  await page.reload();
  await tab("Alpha").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");

  await tab("Bravo").focus();
  await page.keyboard.press("Home");
  expect(await tab("Alpha").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("End");
  expect(await tab("Bravo").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("ArrowLeft");
  expect(await tab("Alpha").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  expect(page.url()).toEndWith(href("bravo"));
  await page.keyboard.press("Enter");
  await page.waitForURL(`**${href("alpha")}`);
  await page.keyboard.press("Shift+ArrowRight");
  expect(await names(page)).toEqual(["Bravo", "Alpha"]);
  expect(await tab("Alpha").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Shift+F10");
  await page.getByRole("menuitem", { name: "Pin", exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Alpha").innerText()).toBe("");
  expect(await strip(page).getByRole("button", { name: "Close Bravo" }).count()).toBe(1);

  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Cron: Dashboard" }).click();
  await page.waitForURL("**/cron");
  await tab("Cron").dblclick();
  await page.getByRole("heading", { name: "Cron" }).waitFor();
  await page.keyboard.press("Escape");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Settings" }).click();
  await page.waitForURL("**/config");
  await page.getByRole("main").getByRole("button", { name: "Close", exact: true }).click();
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Pin", exact: true }).click();
  expect(await tab("Bravo").innerText()).toBe("");

  expect(await strip(page).getAttribute("aria-orientation")).toBe("horizontal");
  await strip(page).getByRole("button", { name: "Tab actions" }).click();
  await page.getByRole("menuitem", { name: "Move tabs to left" }).click();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("left");
  expect(await tab("Alpha").innerText()).toBe("Alpha");
  const mainBox = await page.getByRole("main").boundingBox();
  expect((await strip(page).boundingBox()).x + (await strip(page).boundingBox()).width).toBeLessThanOrEqual(mainBox.x);
  await page.locator("footer").getByRole("button", { name: "Hide sidebar", exact: true }).click();
  await strip(page).waitFor({ state: "hidden" });
  await page.keyboard.press("Control+b");
  await strip(page).waitFor();
  expect(await page.locator("footer").getByRole("button", { name: "Hide sidebar", exact: true }).getAttribute("aria-expanded")).toBe("true");
  const width = (await strip(page).boundingBox()).width;
  await page.getByRole("separator", { name: "Resize tabs" }).focus();
  await page.keyboard.press("ArrowRight");
  expect((await strip(page).boundingBox()).width).toBe(width + 16);
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  expect((await strip(page).boundingBox()).width).toBe(width + 16);
  await page.setViewportSize({ width: 375, height: 700 });
  await strip(page).waitFor({ state: "detached" });
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Move tabs to top" }).click();
  expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("top");

  // Settings offers the same placement switch.
  const orientation = (value: string) => page.waitForFunction((expected: string) => document.querySelector('[role="tablist"][aria-label="Open tabs"]')!.getAttribute("aria-orientation") === expected, value);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByRole("dialog").waitFor({ state: "detached" });
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Settings" }).click();
  await page.waitForURL("**/config");
  const placement = page.getByRole("combobox", { name: "Tab placement" });
  expect(await placement.innerText()).toBe("Top");
  await placement.click();
  await page.getByRole("option", { name: "Left sidebar" }).click();
  await orientation("vertical");
  expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("left");
  expect(await placement.innerText()).toBe("Left sidebar");
  await placement.click();
  await page.getByRole("option", { name: "Top" }).click();
  await orientation("horizontal");
  expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("top");
}), 60_000);

test("tab menu renames sessions in place, bracket shortcuts cycle tabs, and double-clicking empty strip opens a new session", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) {
    await page.goto(`${origin}${href(id)}`);
    await tab(name).waitFor();
  }
  await page.getByPlaceholder("Message or $command").focus();
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`**${href("alpha")}`);
  await page.keyboard.press("Meta+BracketLeft");
  await page.waitForURL(`**${href("charlie")}`);
  await page.keyboard.press("Meta+BracketLeft");
  await page.waitForURL(`**${href("bravo")}`);

  const field = strip(page).getByRole("textbox", { name: "Session name" });
  await menu(page, "Alpha", "Rename session");
  expect(await field.evaluate((node: HTMLInputElement) => document.activeElement === node && node.selectionEnd! - node.selectionStart! === node.value.length)).toBe(true);
  await field.fill("Discarded");
  await field.press("Escape");
  await field.waitFor({ state: "detached" });
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  const long = "A renamed session whose name is much longer than twelve rem";
  await menu(page, "Alpha", "Rename session");
  await field.fill(long);
  await field.press("Enter");
  await tab(long).waitFor();
  await field.waitFor({ state: "detached" });
  expect(page.url()).toEndWith(href("bravo"));
  const width = await tab(long).evaluate((node: HTMLElement) => node.getBoundingClientRect().width);
  expect(width).toBeGreaterThan(12 * 16);
  expect(width).toBeLessThanOrEqual(20 * 16);
  await tab(long).hover();
  expect(await page.locator('[data-slot="tooltip-content"]').innerText()).toBe(long);
  await page.mouse.move(640, 700);
  await page.locator('[data-slot="tooltip-content"]').waitFor({ state: "hidden" });
  await tab("Bravo").focus();
  await page.keyboard.press("ArrowLeft");
  await page.locator('[data-slot="tooltip-content"]').filter({ hasText: long }).waitFor();
  expect(await tab(long).evaluate((node: HTMLElement) => document.activeElement === node && getComputedStyle(node).outlineStyle)).toBe("solid");
  await menu(page, long, "Rename session");
  await field.fill("Renamed");
  await page.getByPlaceholder("Message or $command").click();
  await tab("Renamed").waitFor();

  const box = await strip(page).boundingBox();
  await strip(page).dblclick({ position: { x: box.width - 4, y: box.height / 2 } });
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["Renamed", "Bravo", "New session", "Charlie"]);
  await tab("New session").click({ button: "right" });
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain("Rename session");
}), 60_000);

test("narrow top strip scrolls and running tabs keep a reachable close control", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"], [href("delta"), "Delta"], ["/search", "Search"]]) {
    await page.goto(`${origin}${link}`);
    await tab(name).waitFor();
    // The strip draws before Identity loads, but only an owner's tabs are saved; leaving earlier drops this tab.
    await page.waitForFunction((path: string) => JSON.parse(localStorage.getItem("tabs:tester") ?? "[]").some((saved: { path: string }) => saved.path === path), link);
  }
  await tab("Delta").waitFor();
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo", "Charlie", "Delta", "Search"]);
  expect(await strip(page).evaluate((node: HTMLElement) => node.scrollWidth > node.clientWidth && getComputedStyle(node).overflowX === "auto")).toBe(true);
  await strip(page).evaluate((node: HTMLElement) => { node.scrollLeft = 0; });
  await strip(page).hover();
  await page.mouse.wheel(0, 100);
  await page.waitForFunction(() => document.querySelector('[role="tablist"][aria-label="Open tabs"]')!.scrollLeft === 100);
  await page.mouse.move(0, 400);
  expect(await strip(page).getByRole("img", { name: "Turn running" }).evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  const close = strip(page).getByRole("button", { name: "Close Delta" });
  expect(await close.evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  await close.focus();
  expect(await close.evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Enter");
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo", "Charlie", "Search"]);
  expect(page.url()).toEndWith("/search");
  expect(await tab("Search").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Shift+F10");
  await page.getByRole("menuitem", { name: "Close", exact: true }).press("Enter");
  await page.waitForURL(`**${href("charlie")}`);
  // The menu hands focus back as it finishes closing, which can land after the URL changes.
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Open tabs"] [role="tab"][aria-label="Charlie"]'));
}), 60_000);

test("narrow left placement is a drawer opened by the top-left button or a right swipe and closed by a pick, Escape, or a left swipe", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await openSaved(page, origin, href("alpha"), "Alpha");
  await openSaved(page, origin, href("bravo"), "Bravo");
  await page.evaluate(() => localStorage.setItem("tab-placement", "left"));
  await page.reload();
  const toggle = page.getByRole("button", { name: "Show sidebar", exact: true });
  const box = (await toggle.boundingBox())!;
  expect(box.x + box.y).toBeLessThan(24);
  expect(await page.locator("footer").getByRole("button", { name: "Show sidebar" }).count()).toBe(0);
  expect(await strip(page).count()).toBe(0);

  await toggle.tap();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  await tab("Alpha").tap();
  await page.waitForURL(`**${href("alpha")}`);
  await strip(page).waitFor({ state: "detached" });

  const cdp = await page.context().newCDPSession(page);
  // Moves come at a finger's 60 Hz pace: Chrome on Linux turns an instant burst into a fling that navigates back.
  const swipe = async (x: number, y: number, dx: number) => {
    await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
    for (let step = 1; step <= 12; step++) {
      await page.waitForTimeout(16);
      await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: x + dx * step / 12, y }] });
    }
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  };
  // A swipe that starts in a text field is left to the field.
  const field = (await page.getByPlaceholder("Message or $command").boundingBox())!;
  await swipe(field.x + 20, field.y + 20, 200);
  await page.waitForTimeout(300);
  expect(await strip(page).count()).toBe(0);
  await swipe(100, 300, 200);
  await strip(page).waitFor();
  await swipe(250, 300, -230);
  await strip(page).waitFor({ state: "detached" });

  // Escape closes only the drawer, not the non-chat tab behind it.
  await page.goto(`${origin}/config`);
  await toggle.tap();
  expect(await names(page)).toContain("Settings");
  await page.keyboard.press("Escape");
  await strip(page).waitFor({ state: "detached" });
  expect(new URL(page.url()).pathname).toBe("/config");
}), 60_000);

test("running tab shows a dot instead of close until hover or focus at desktop width", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await page.goto(`${origin}${href("delta")}`);
  await tab("Delta").waitFor();
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Delta").waitFor();
  await page.mouse.move(640, 600);
  const close = strip(page).getByRole("button", { name: "Close Delta" });
  const dot = strip(page).getByRole("img", { name: "Turn running" });
  expect(await close.evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("0");
  expect(await dot.evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  await tab("Delta").focus();
  expect(await close.evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  await tab("Delta").click();
  await page.waitForURL(`**${href("delta")}`);
  expect(await dot.evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
}), 60_000);

test("tab groups take a name and color, collapse, adopt opened tabs, follow drags, and survive reload", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const header = strip(page).getByRole("button", { name: "Work", exact: true });
  const field = page.getByRole("textbox", { name: "Group name" });
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) await openSaved(page, origin, href(id), name);
  await menu(page, "Alpha", "Add to new group");
  await field.waitFor();
  expect(await field.evaluate((node: HTMLInputElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.type("Work");
  expect(await field.inputValue()).toBe("Work");
  await page.getByRole("radio", { name: "Green" }).check();
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "Bravo", 'Add to group "Work"');
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Work, Space: Main");
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");

  await page.reload();
  await tab("Charlie").waitFor();
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  await strip(page).getByRole("button", { name: "Edit Work", exact: true }).click();
  expect(await field.inputValue()).toBe("Work");
  expect(await page.getByRole("radio", { name: "Green" }).isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Open tabs"] [aria-label="Work"]'));

  await header.click();
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  expect(await order(page)).toEqual(["Work", "Charlie"]);
  await page.getByPlaceholder("Message or $command").focus();
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await header.getAttribute("aria-expanded")).toBe("true");
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);

  await tab("Bravo").click();
  await page.waitForURL(`**${href("bravo")}`);
  await openFromPalette(page, "Delta");
  await page.waitForURL(`**${href("delta")}`);
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Delta", "Charlie"]);
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Group Work, Space: Main");
  await tab("Charlie").dragTo(tab("Alpha"));
  expect(await order(page)).toEqual(["Work", "Charlie", "Alpha", "Bravo", "Delta"]);
  await menu(page, "Delta", "Remove from group");
  expect(await order(page)).toEqual(["Work", "Charlie", "Alpha", "Bravo", "Delta"]);
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main");
  await tab("Alpha").dragTo(tab("Delta"));
  expect(await order(page)).toEqual(["Work", "Charlie", "Bravo", "Delta", "Alpha"]);
  expect(await tab("Alpha").getAttribute("aria-description")).toBe("Space: Main");
}), 60_000);

test("tab group editor moves New session in, ungroups, and closes; pins leave groups; collapsed groups skip arrow keys; bad group storage resets", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const field = page.getByRole("textbox", { name: "Group name" });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name);
  await menu(page, "Alpha", "Add to new group");
  await page.keyboard.type("Work");
  await page.keyboard.press("Enter");
  await field.waitFor({ state: "detached" });
  await menu(page, "Bravo", 'Add to group "Work"');
  expect(await order(page)).toEqual(["New session", "Work", "Alpha", "Bravo", "Charlie"]);

  await strip(page).getByRole("button", { name: "Edit Work", exact: true }).click();
  await page.getByRole("button", { name: "New tab in group", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "New session", "Charlie"]);
  expect(await tab("New session").getAttribute("aria-selected")).toBe("true");
  await menu(page, "Bravo", "Pin");
  expect(await order(page)).toEqual(["Bravo", "Work", "Alpha", "New session", "Charlie"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBeNull();

  await menu(page, "Charlie", "Move tabs to left");
  await tab("Charlie").click();
  await page.waitForURL(`**${href("charlie")}`);
  await strip(page).getByRole("button", { name: "Work", exact: true }).click();
  await page.reload();
  await tab("Charlie").waitFor();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  expect(await order(page)).toEqual(["Bravo", "Work", "Charlie"]);
  await tab("Bravo").focus();
  await page.keyboard.press("ArrowDown");
  expect(await tab("Charlie").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);

  await strip(page).getByRole("button", { name: "Work", exact: true }).focus();
  await page.keyboard.press("Shift+F10");
  await page.getByRole("button", { name: "Ungroup", exact: true }).click();
  expect(await order(page)).toEqual(["Bravo", "Alpha", "New session", "Charlie"]);
  // The editor hands focus back as it finishes closing.
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Open tabs"] [role="tab"][aria-label="Charlie"]'));

  await tab("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  await menu(page, "Alpha", "Add to new group");
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "New session", 'Add to group "Grey group"');
  expect(await order(page)).toEqual(["Bravo", "Grey group", "Alpha", "New session", "Charlie"]);
  await strip(page).getByRole("button", { name: "Edit Grey group", exact: true }).click();
  await page.getByRole("button", { name: "Close group", exact: true }).click();
  await page.waitForURL(`**${href("charlie")}`);
  expect(await order(page)).toEqual(["Bravo", "Charlie"]);

  await menu(page, "Charlie", "Add to new group");
  await page.keyboard.press("Escape");
  await page.waitForFunction(() => localStorage.getItem("tab-groups-chrome-spaces:tester")!.includes("Grey"));
  await page.evaluate(() => localStorage.setItem("tab-groups-chrome-spaces:tester", "{not json"));
  await page.reload();
  await tab("Charlie").waitFor();
  expect(await order(page)).toEqual(["Bravo", "Charlie"]);
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");
}), 60_000);

test("spaces show only their own tabs, return to their last tab, follow opened tabs, and survive reload", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const space = page.getByRole("button", { name: /^Space: / });
  const choose = (item: string, role?: string) => chooseSpace(page, item, role);
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name);
  expect(await space.getAttribute("aria-label")).toBe("Space: Main");
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");

  // A new space takes New session along and is named in place.
  await choose("New space");
  await page.waitForURL(`${origin}/`);
  await page.getByRole("textbox", { name: "Space name" }).waitFor();
  await page.keyboard.type("Ops");
  await page.keyboard.press("Enter");
  expect(await space.getAttribute("aria-label")).toBe("Space: Ops");
  expect(await names(page)).toEqual(["New session"]);
  expect(page.url()).toBe(`${origin}/`);

  await choose("Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  await menu(page, "Bravo", "Move to space", "Ops");
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);
  await tab("Alpha").focus();
  await page.keyboard.press("ArrowRight");
  expect(await tab("Charlie").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["New session", "Bravo"]);
  await tab("Bravo").click();
  await choose("Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);

  // Opening a tab that lives in another space switches to it, and reload keeps both.
  await openFromPalette(page, "Bravo");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await space.getAttribute("aria-label")).toBe("Space: Ops");
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await space.getAttribute("aria-label")).toBe("Space: Ops");
  expect(await names(page)).toEqual(["New session", "Bravo"]);
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`${origin}/`);

  await menu(page, "Bravo", "Close all");
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["New session"]);
  expect(await space.getAttribute("aria-label")).toBe("Space: Ops");
  await openFromPalette(page, "Bravo");
  await choose("Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);
  // New session is never duplicated: opening it brings its tab into this space.
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session"]);
  expect(await space.getAttribute("aria-label")).toBe("Space: Main");

  await menu(page, "Alpha", "Pin");
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);

  // Deleting a space moves its tabs to the first space, and the last space stays.
  await choose("Delete space");
  expect(await space.getAttribute("aria-label")).toBe("Space: Main");
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session", "Bravo"]);
  expect(page.url()).toEndWith(href("bravo"));
  await space.click();
  await page.getByRole("menuitem", { name: "Rename space" }).waitFor();
  expect(await page.getByRole("menuitem").allInnerTexts()).toEqual(["New space", "Rename space"]);
  await page.keyboard.press("Escape");

  // Unreadable spaces reset to Main and keep every tab; the menu also sits atop left tabs.
  await page.evaluate(() => localStorage.setItem("tab-groups-chrome-spaces:tester", "{not json"));
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session", "Bravo"]);
  await menu(page, "Bravo", "Move tabs to left");
  const [button, list] = [await space.boundingBox(), await strip(page).boundingBox()];
  expect(button.y + button.height).toBeLessThanOrEqual(list.y);
}), 60_000);

test("a space offers New session its own and spaceless agents, and shows its own and spaceless cron jobs, ignoring case", () => withPage({}, async (page, origin) => {
  const agents = async () => {
    await page.getByRole("button", { name: "Choose agent", exact: true }).click();
    await page.getByRole("menuitemradio", { name: "main", exact: true }).waitFor();
    const listed = await page.getByRole("menuitemradio").allInnerTexts();
    await page.keyboard.press("Escape");
    return listed.map((text: string) => text.trim());
  };
  const runnable = async () => {
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("button", { name: "Cron: Run", exact: true }).click();
    const palette = page.getByRole("dialog", { name: "Run cron", exact: true });
    await palette.getByRole("button", { name: "daily", exact: true }).waitFor();
    const listed = await palette.getByRole("button").allTextContents();
    await page.keyboard.press("Escape");
    await palette.waitFor({ state: "hidden" });
    return listed;
  };
  const dashboard = async () => {
    await page.getByRole("button", { name: "Preview latest run of daily", exact: true }).waitFor();
    return (await page.getByRole("button", { name: /^Preview latest run of / }).evaluateAll((nodes: HTMLElement[]) => nodes.filter((node) => node.checkVisibility()).map((node) => node.getAttribute("aria-label")))).map((label: string) => label.replace("Preview latest run of ", ""));
  };
  // Main is spaceless for both: an existing session keeps every agent, New session drops opsbot, and opsreport hides with its run.
  await openSaved(page, origin, href("alpha"), "Alpha");
  expect(await agents()).toEqual(["main", "opsbot"]);
  await openSaved(page, origin, "/cron", "Cron");
  expect(await dashboard()).toEqual(["daily", "retired"]);
  await openSaved(page, origin, "/", "New session");
  expect(await agents()).toEqual(["main"]);
  expect(await runnable()).toEqual(["daily"]);
  // The OPS agent and the Ops job belong to a space named ops.
  await chooseSpace(page, "New space");
  await page.getByRole("textbox", { name: "Space name" }).waitFor();
  await page.keyboard.type("ops");
  await page.keyboard.press("Enter");
  expect(await agents()).toEqual(["main", "opsbot"]);
  expect(await runnable()).toEqual(["daily", "opsreport"]);
  await chooseSpace(page, "Main", "menuitemradio");
  await menu(page, "Cron", "Move to space", "ops");
  await chooseSpace(page, "ops", "menuitemradio");
  await strip(page).getByRole("tab", { name: "Cron", exact: true }).click();
  expect(await dashboard()).toEqual(["daily", "opsreport", "retired"]);
}), 60_000);

test("a group lives in one space: menus offer only this space's groups, groups move whole, and New tab in group brings New session", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const header = strip(page).getByRole("button", { name: "Work", exact: true }), edit = strip(page).getByRole("button", { name: "Edit Work", exact: true });
  const editor = (item: string) => edit.click().then(() => page.getByRole("button", { name: item, exact: true }).click());
  const green = page.getByRole("radio", { name: "Green" });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name);
  await menu(page, "Alpha", "Add to new group");
  await page.keyboard.type("Work");
  await green.check();
  await page.keyboard.press("Escape");
  await menu(page, "Bravo", 'Add to group "Work"');

  // A new space takes New session, and its tab menu offers no group from Main.
  await chooseSpace(page, "New space");
  await page.getByRole("textbox", { name: "Space name" }).fill("Ops");
  await page.keyboard.press("Enter");
  await tab("New session").click({ button: "right" });
  expect(await page.getByRole("menuitem").allInnerTexts()).toEqual(["Close", "Close others", "Close to the right", "Close all", "Pin", "Add to new group", "Move to space", "Move tabs to left"]);
  await page.getByRole("menuitem", { name: "Move to space", exact: true }).click();
  const spaces = page.getByRole("menu").nth(1);
  await spaces.waitFor();
  expect(await spaces.getByRole("menuitem").allInnerTexts()).toEqual(["Main"]);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");

  // Moving the group keeps its name and color; moving one member out of it leaves the group.
  await chooseSpace(page, "Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  await editor('Move group to "Ops"');
  expect(await order(page)).toEqual(["Charlie"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["New session", "Work", "Alpha", "Bravo"]);
  await edit.click();
  expect(await green.isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await menu(page, "Alpha", "Move to space", "Main");
  expect(await order(page)).toEqual(["New session", "Work", "Bravo"]);
  await header.click();
  await chooseSpace(page, "Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await order(page)).toEqual(["Alpha", "Charlie"]);
  expect(await tab("Alpha").getAttribute("aria-description")).toBe("Space: Main");
  await menu(page, "Charlie", "Pin");

  // Reload keeps spaces, groups, colors, collapsed state, and memberships; the pinned tab shows everywhere in no group.
  await page.reload();
  await tab("Charlie").waitFor();
  expect(await order(page)).toEqual(["Charlie", "Alpha"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Charlie", "New session", "Work"]);
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  await edit.click();
  expect(await green.isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await header.click();
  expect(await order(page)).toEqual(["Charlie", "New session", "Work", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Work, Space: Ops");
  expect(await tab("Charlie").getAttribute("aria-description")).toBeNull();

  // New tab in group brings New session over from Main.
  await chooseSpace(page, "Main", "menuitemradio");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Charlie", "New session", "Alpha"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  await editor("New tab in group");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Charlie", "Work", "Bravo", "New session"]);
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Ops");

  // Deleting Ops moves the collapsed group, intact, to Main.
  await header.click();
  await chooseSpace(page, "Delete space");
  expect(await page.getByRole("button", { name: /^Space: / }).getAttribute("aria-label")).toBe("Space: Main");
  expect(await order(page)).toEqual(["Charlie", "Alpha", "Work", "New session"]);
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Main");
}), 60_000);
