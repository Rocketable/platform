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
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json().catch(() => ({})) as { id: string; text: string };
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "tabs" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "tabs" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/History": return Response.json({ messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
      case "/api/CreateSession": return Response.json({ id: "fresh" });
      case "/api/Prompt": prompts.push(input); return Response.json({ privateText: "" });
      case "/api/SearchMessages": case "/api/SearchOrigins": return Response.json({ matches: [] });
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
async function openFromPalette(page: any, name: string) {
  await page.keyboard.press("Control+p");
  const palette = page.getByRole("dialog", { name: "Go to session" });
  await palette.getByRole("button").filter({ hasText: name }).waitFor();
  await palette.getByPlaceholder("Search sessions").fill(name);
  await palette.getByPlaceholder("Search sessions").press("Enter");
  await palette.waitFor({ state: "hidden" });
}
async function menu(page: any, name: string, item: string) {
  await strip(page).getByRole("tab", { name, exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: item, exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
}

test("preview tabs, promotion, pins, close commands, middle-click, and drag survive reload", () => withPage({}, async (page, origin, prompts) => {
  await page.goto(`${origin}/`);
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await tab("New session").waitFor();
  await page.getByPlaceholder("Message a new session").fill("hello");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL(`**${href("fresh")}`);
  expect(await names(page)).toEqual(["fresh"]);
  expect(prompts.map((prompt) => prompt.id)).toEqual(["fresh"]);

  await openFromPalette(page, "Alpha");
  await page.waitForURL(`**${href("alpha")}`);
  await openFromPalette(page, "Bravo");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["fresh", "Bravo"]);
  expect(await italic(tab("Bravo"))).toBe(true);
  expect(await italic(tab("fresh"))).toBe(false);
  await tab("Bravo").dblclick();
  expect(await italic(tab("Bravo"))).toBe(false);
  await openFromPalette(page, "Alpha");
  expect(await names(page)).toEqual(["fresh", "Bravo", "Alpha"]);
  expect(await italic(tab("Alpha"))).toBe(true);
  await page.getByPlaceholder("Message or $command").fill("keep me");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForFunction(() => getComputedStyle(document.querySelector('[role="tab"][aria-selected="true"]')!).fontStyle !== "italic");

  await openFromPalette(page, "Charlie");
  expect(await italic(tab("Charlie"))).toBe(true);
  await page.getByPlaceholder("Message or $command").fill("unsent draft");
  expect(await italic(tab("Charlie"))).toBe(false);
  await openFromPalette(page, "Delta");
  expect(await names(page)).toEqual(["fresh", "Bravo", "Alpha", "Charlie", "Delta"]);
  await menu(page, "Alpha", "Pin");
  expect(await names(page)).toEqual(["Alpha", "fresh", "Bravo", "Charlie", "Delta"]);
  expect(await tab("Alpha").innerText()).toBe("");
  await menu(page, "Bravo", "Close to the right");
  expect(await names(page)).toEqual(["Alpha", "fresh", "Bravo"]);
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
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  await page.setViewportSize({ width: 375, height: 700 });
  await page.waitForFunction(() => document.querySelector('[role="tablist"][aria-label="Open tabs"]')!.getAttribute("aria-orientation") === "horizontal");
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

test("narrow top strip scrolls and running tabs keep a reachable close control", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"], [href("delta"), "Delta"], ["/search", "Search"]]) {
    await page.goto(`${origin}${link}`);
    await tab(name).waitFor();
  }
  await tab("Delta").waitFor();
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo", "Charlie", "Delta", "Search"]);
  expect(await strip(page).evaluate((node: HTMLElement) => node.scrollWidth > node.clientWidth && getComputedStyle(node).overflowX === "auto")).toBe(true);
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
  expect(await tab("Charlie").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
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
