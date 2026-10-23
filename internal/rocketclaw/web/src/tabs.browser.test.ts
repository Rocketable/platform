import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const rows: { id: string; name: string; agent: string; running?: boolean; background?: boolean; spaceId?: string; unreadAt?: string }[] = [
  { id: "alpha", name: "Alpha", agent: "main" },
  { id: "bravo", name: "Bravo", agent: "main" },
  { id: "charlie", name: "Charlie", agent: "main" },
  { id: "delta", name: "Delta", agent: "main", running: true, background: true },
  // Echo was born in the ops space, and its background work runs while no turn does.
  { id: "echo", name: "Echo", agent: "main", spaceId: "ops", background: true },
  // A turn of the person's ended unseen, and another runs now.
  { id: "foxtrot", name: "Foxtrot", agent: "main", running: true, unreadAt: "2026-10-22T10:00:00Z" },
];
const href = (id: string) => `/s/${Buffer.from(id).toString("base64url")}`;
type Saved = { spaces: { id: string; name: string }[]; state: { tabs: { path: string; pinned?: boolean; group?: string; space?: string }[]; groups: { id: string; name: string; color?: string; collapsed?: boolean }[]; space: string; last: Record<string, string>; placement: string }; revision: number; refused: number };

// Mirrors the server's Tabs call: it saves the state or changes one space, then answers with every space and the saved state.
// A taken name, ignoring case, and deleting the last space are refused, and a save made from an older revision is stale.
function tabsCall(saved: Saved, input: { state?: Saved["state"]; revision?: number; createSpace?: string; renameSpace?: { id: string; name: string }; deleteSpace?: string }) {
  const taken = (name: string, id?: string) => saved.spaces.some((space) => space.id !== id && space.name.toLowerCase() === name.toLowerCase());
  if ([input.createSpace && taken(input.createSpace), input.renameSpace && taken(input.renameSpace.name, input.renameSpace.id), input.deleteSpace && saved.spaces.length === 1].some(Boolean)) {
    return Response.json({ message: "another space has that name, or it is the last space", code: 3 }, { status: 400 });
  }
  if (input.state && (input.revision ?? 0) !== saved.revision) return Response.json({ ...saved, stale: !!++saved.refused });
  if (input.state) [saved.state, saved.revision] = [input.state, saved.revision + 1];
  if (input.createSpace) saved.spaces.push({ id: `space-${saved.spaces.length}-${input.createSpace}`, name: input.createSpace });
  if (input.renameSpace) saved.spaces.find((space) => space.id === input.renameSpace!.id)!.name = input.renameSpace.name;
  if (input.deleteSpace) saved.spaces = saved.spaces.filter((space) => space.id !== input.deleteSpace);
  return Response.json(saved);
}

async function withPage(options: object, run: (page: any, origin: string, prompts: { id: string; text: string }[], saved: Saved) => Promise<void>) {
  const { chromium: engine } = await import(playwright!);
  const prompts: { id: string; text: string }[] = [];
  const sessions = structuredClone(rows);
  const saved: Saved = { spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "", last: {}, placement: "TOP" }, revision: 0, refused: 0 };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json().catch(() => ({})) as { id: string; text: string; name: string; query: string; agent?: string } & Parameters<typeof tabsCall>[1];
    if (url.pathname === "/api/UpdateSession") sessions.find((row) => row.id === input.id)!.name = input.name;
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "tabs" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "tabs" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }, { name: "opsbot", space: "OPS" }], currentAgent: "main" });
      case "/api/ListCronJobs": return Response.json({ jobs: [{ stem: "daily", status: "" }, { stem: "opsreport", status: "", space: "Ops" },
        { stem: "opsreport", status: "ran", lastRun: new Date().toISOString() }, { stem: "retired", status: "ran", lastRun: new Date().toISOString() }] });
      case "/api/History": return Response.json({ messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
      case "/api/CreateSession":
        // opsbot names the OPS space, which its first session creates, as the server does.
        if (input.agent === "opsbot") {
          saved.spaces.push({ id: "ops", name: "OPS" });
          sessions.push({ id: "fresh", name: "Ops chat", agent: "opsbot", spaceId: "ops" });
        }
        return Response.json({ id: "fresh" });
      case "/api/Prompt": prompts.push(input); return Response.json({ privateText: "" });
      case "/api/Tabs": return tabsCall(saved, input);
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
  } finally {
    await browser.close();
    server.stop(true);
  }
}

const strip = (page: any) => page.getByRole("tablist", { name: "Open tabs" });
const names = (page: any) => strip(page).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
const italic = (tab: any) => tab.evaluate((node: HTMLElement) => getComputedStyle(node).fontStyle === "italic");
const animation = (locator: any) => locator.evaluate((node: Element) => getComputedStyle(node).animationName);
const mask = (locator: any) => locator.evaluate((node: Element) => getComputedStyle(node).maskImage);
// Group headers are the strip's only controls that report aria-expanded.
const order = (page: any) => strip(page).locator('[role="tab"], [aria-expanded]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
// Saves reach the server after the page changes, so a test waits for them before reloading.
async function until(check: () => boolean) {
  for (const started = Date.now(); !check(); await Bun.sleep(10)) if (Date.now() - started > 5_000) throw new Error("the saved tab state never matched");
}
async function openSaved(page: any, origin: string, link: string, name: string, saved: Saved) {
  await page.goto(`${origin}${link}`);
  await strip(page).getByRole("tab", { name, exact: true }).waitFor();
  await until(() => saved.state.tabs.some((tab) => tab.path === link));
}
async function openFromPalette(page: any, name: string) {
  await page.keyboard.press("Control+p");
  const palette = page.getByRole("dialog", { name: "Go to session" });
  await palette.getByRole("button").filter({ hasText: name }).waitFor();
  await palette.getByPlaceholder("Search sessions").fill(name);
  await palette.getByPlaceholder("Search sessions").press("Enter");
  await palette.waitFor({ state: "hidden" });
}
// An in-app link, like a Search result, opens a session without the palette.
async function follow(page: any, id: string) {
  await page.evaluate((link: string) => { history.pushState(null, "", link); dispatchEvent(new PopStateEvent("popstate")); }, href(id));
  await page.waitForURL(`**${href(id)}`);
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

test("preview tabs, promotion, pins, close commands, middle-click, and drag survive reload", () => withPage({}, async (page, origin, prompts, saved) => {
  await page.goto(`${origin}/`);
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await tab("New session").waitFor();
  await page.getByPlaceholder("Message a new session").fill("hello");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL(`**${href("fresh")}`);
  expect(await names(page)).toEqual(["hello"]);
  expect(prompts.map((prompt) => prompt.id)).toEqual(["fresh"]);

  await follow(page, "alpha");
  await follow(page, "bravo");
  expect(await names(page)).toEqual(["hello", "Bravo"]);
  expect(await italic(tab("Bravo"))).toBe(true);
  expect(await italic(tab("hello"))).toBe(false);
  await tab("Bravo").dblclick();
  expect(await italic(tab("Bravo"))).toBe(false);
  expect(await tab("Bravo").evaluate((node: HTMLElement) => document.activeElement === node && getComputedStyle(node).outlineStyle)).toBe("none");
  await follow(page, "alpha");
  expect(await names(page)).toEqual(["hello", "Bravo", "Alpha"]);
  expect(await italic(tab("Alpha"))).toBe(true);
  await page.getByPlaceholder("Message or $command").fill("keep me");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForFunction(() => getComputedStyle(document.querySelector('[role="tab"][aria-selected="true"]')!).fontStyle !== "italic");

  await follow(page, "charlie");
  expect(await italic(tab("Charlie"))).toBe(true);
  await page.getByPlaceholder("Message or $command").fill("unsent draft");
  expect(await italic(tab("Charlie"))).toBe(false);
  await follow(page, "delta");
  expect(await names(page)).toEqual(["hello", "Bravo", "Alpha", "Charlie", "Delta"]);
  await menu(page, "Alpha", "Pin");
  expect(await names(page)).toEqual(["Alpha", "hello", "Bravo", "Charlie", "Delta"]);
  expect(await tab("Alpha").innerText()).toBe("");
  expect(await strip(page).locator('[aria-label="Pinned"]').count()).toBe(1);
  await menu(page, "Bravo", "Close to the right");
  expect(await names(page)).toEqual(["Alpha", "hello", "Bravo"]);
  await page.waitForURL(`**${href("bravo")}`);
  await openFromPalette(page, "Charlie");
  await menu(page, "Bravo", "Close others");
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");

  // The palette opens a permanent tab, beside the preview tab, and makes an open preview tab permanent.
  await follow(page, "charlie");
  await openFromPalette(page, "Delta");
  await page.waitForURL(`**${href("delta")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie", "Delta"]);
  expect(await italic(tab("Delta"))).toBe(false);
  expect(await italic(tab("Charlie"))).toBe(true);
  await page.keyboard.press("Control+p");
  await page.getByRole("dialog", { name: "Go to session" }).getByRole("button").filter({ hasText: "Charlie" }).click();
  await page.waitForURL(`**${href("charlie")}`);
  expect(await italic(tab("Charlie"))).toBe(false);
  await tab("Charlie").click({ button: "middle" });
  await page.waitForURL(`**${href("delta")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Delta"]);
  await tab("Delta").click({ button: "middle" });
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);

  await follow(page, "charlie");
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  await tab("Charlie").dragTo(tab("Bravo"));
  expect(await names(page)).toEqual(["Alpha", "Charlie", "Bravo"]);
  await until(() => saved.state.tabs.map((tab) => tab.path).join() === [href("alpha"), href("charlie"), href("bravo")].join());
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

test("arrowing through the palette previews sessions in this space; Escape returns, Enter keeps", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const palette = page.getByRole("dialog", { name: "Go to session" });
  const step = async (key: string, id: string) => { await page.keyboard.press(key); await page.waitForURL(`**${href(id)}`); };
  // The palette focuses its input after it shows its rows; arrows pressed before then are lost.
  const focused = async () => {
    await palette.getByRole("button").filter({ hasText: "Echo" }).waitFor();
    await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Search sessions and commands");
  };
  // The open palette hides the strip from role queries, so tabs are read as names, an italic one marked "*".
  const behind = () => page.locator('[aria-label="Open tabs"] [role="tab"]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => `${node.getAttribute("aria-label")}${getComputedStyle(node).fontStyle === "italic" ? "*" : ""}`));
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Alpha").waitFor();
  const depth = await page.evaluate(() => history.length);
  await page.keyboard.press("Control+p");
  await focused();
  await step("ArrowDown", "bravo");
  await step("ArrowDown", "charlie");
  expect(await behind()).toEqual(["Alpha", "Charlie*"]);
  await step("ArrowDown", "delta");
  expect(await behind()).toEqual(["Alpha", "Delta*"]);
  // Echo was born in another space, so its row shows where the palette opened.
  await step("ArrowDown", "alpha");
  await step("ArrowUp", "delta");
  await step("Escape", "alpha");
  await palette.waitFor({ state: "hidden" });
  expect(await page.evaluate(() => history.length)).toBe(depth);

  await page.keyboard.press("Control+p");
  await focused();
  for (const id of ["bravo", "charlie", "delta"]) await step("ArrowDown", id);
  await page.keyboard.press("Enter");
  await palette.waitFor({ state: "hidden" });
  expect(page.url()).toEndWith(href("delta"));
  expect(await names(page)).toEqual(["Alpha", "Delta"]);
  expect(await italic(tab("Delta"))).toBe(false);
  await page.goBack();
  await page.waitForURL(`**${href("alpha")}`);

  // Typing returns to where the palette opened, so a command acts on that tab.
  await page.keyboard.press("Control+p");
  await focused();
  await step("ArrowDown", "bravo");
  await page.keyboard.type("Tabs: Close");
  await page.waitForURL(`**${href("alpha")}`);
  await step("Enter", "bravo");
  expect(await names(page)).toEqual(["Bravo", "Delta"]);
}), 30_000);

for (const width of [1280, 320]) test(`copy page link across pages at ${width}px`, () => withPage({ viewport: { width, height: 800 }, hasTouch: width === 320, permissions: ["clipboard-read", "clipboard-write"] }, async (page, origin) => {
  const copy = page.getByRole("button", { name: "Copy page link", exact: true });
  const status = page.locator("footer").getByRole("status");
  for (const pathname of ["/", `${href("alpha")}?view=one%20%26%20two#result%2Fthree`, "/search?q=tag%3A%22two%20words%22%20%26%20%2B%25#result%2Ftwo", "/cron", "/agents", "/skills", "/skills/example", "/config", "/voice", "/shortcuts"]) {
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
    expect(boxes).toHaveLength(4);
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
  await page.getByRole("button", { name: "Search sessions and commands", exact: true }).click();
  await page.getByRole("dialog", { name: "Go to session" }).waitFor();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "detached" });
  await page.waitForURL((url: URL) => url.pathname === "/search");
  await page.getByRole("main").getByRole("textbox", { name: "Search sessions", exact: true }).fill("two words & +%");
  await page.waitForURL((url: URL) => url.searchParams.get("q") === "two words & +%");
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

test("page-link feedback expires and clears when switching sessions", () => withPage({ permissions: ["clipboard-read", "clipboard-write"] }, async (page, origin) => {
  await page.clock.install();
  await page.goto(`${origin}${href("alpha")}`);
  const copy = page.getByRole("button", { name: "Copy page link", exact: true });
  const status = page.locator("footer").getByRole("status");
  await copy.click();
  await status.filter({ hasText: "Page link copied" }).waitFor();
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 100));
  await page.clock.runFor(1_000);
  await copy.click();
  await status.filter({ hasText: "Page link copied" }).waitFor();
  await page.clock.runFor(1_100);
  expect(await copy.locator("svg.lucide-check").isVisible()).toBe(true);
  await page.clock.runFor(1_000);
  await copy.locator("svg.lucide-share").waitFor({ timeout: 2_000 });
  expect(await status.textContent()).toBe("");

  await copy.click();
  await status.filter({ hasText: "Page link copied" }).waitFor();
  const button = await copy.elementHandle();
  await follow(page, "bravo");
  await copy.locator("svg.lucide-share").waitFor({ timeout: 2_000 });
  expect(await status.textContent()).toBe("");
  expect(await button.evaluate((node: HTMLElement) => node.isConnected)).toBe(true);
  await follow(page, "alpha");
  expect(await copy.locator("svg.lucide-check").count()).toBe(0);
  for (const failed of [false, true]) {
    await page.evaluate((failed: boolean) => {
      const pending = Promise.withResolvers<void>();
      window.addEventListener("settle-copy", () => failed ? pending.reject(new Error("Copy denied")) : pending.resolve(), { once: true });
      let first = true;
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: () => {
        if (first) { first = false; return pending.promise; }
        return Promise.resolve();
      } } });
    }, failed);
    await copy.click();
    await follow(page, "bravo");
    await copy.click();
    await status.filter({ hasText: "Page link copied" }).waitFor();
    await page.evaluate(() => dispatchEvent(new Event("settle-copy")));
    await page.clock.runFor(100);
    expect(await copy.locator("svg.lucide-check").isVisible()).toBe(true);
    await follow(page, "alpha");
  }
  await page.clock.resume();
}), 30_000);

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

test("saved tabs, deep links, keyboard, page close, palette commands, and placement", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await names(page)).toEqual(["Alpha"]);
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
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
  await until(() => saved.state.placement === "LEFT");
  expect(await tab("Alpha").innerText()).toBe("Alpha");
  const mainBox = await page.getByRole("main").boundingBox();
  expect((await strip(page).boundingBox()).x + (await strip(page).boundingBox()).width).toBeLessThanOrEqual(mainBox.x);
  await page.locator("footer").getByRole("button", { name: "Hide sidebar", exact: true }).click();
  await strip(page).waitFor({ state: "hidden" });
  await page.keyboard.press("Control+b");
  await strip(page).waitFor();
  expect(await page.locator("footer").getByRole("button", { name: "Hide sidebar", exact: true }).getAttribute("aria-expanded")).toBe("true");
  for (const state of ["hidden", "visible"] as const) { // The palette runs the same toggle as Cmd/Ctrl+B.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Toggle sidebar" }).click();
    await strip(page).waitFor({ state });
  }
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
  await until(() => saved.state.placement === "TOP");

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
  await until(() => saved.state.placement === "LEFT");
  expect(await placement.innerText()).toBe("Left sidebar");
  await placement.click();
  await page.getByRole("option", { name: "Top" }).click();
  await orientation("horizontal");
  await until(() => saved.state.placement === "TOP");
}), 60_000);

test("tab menu renames sessions in place, bracket shortcuts cycle tabs, and double-clicking empty strip opens a new session", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) {
    await openSaved(page, origin, href(id), name, saved);
  }
  await page.getByPlaceholder("Message or $command").focus();
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`**${href("alpha")}`);
  await page.keyboard.press("Meta+BracketLeft");
  await page.waitForURL(`**${href("charlie")}`);
  await page.keyboard.press("Meta+BracketLeft");
  await page.waitForURL(`**${href("bravo")}`);
  for (const [command, id] of [["Tabs: Next", "charlie"], ["Tabs: Previous", "bravo"]]) { // The palette closes, then steps like the brackets.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: command }).click();
    await page.waitForURL(`**${href(id)}`);
  }

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

test("narrow top strip scrolls and running tabs keep a reachable close control", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"], [href("delta"), "Delta"], ["/search", "Search"]]) {
    await page.goto(`${origin}${link}`);
    await tab(name).waitFor();
    // The strip draws before the saved tabs load, but only loaded tabs are saved; leaving earlier drops this tab.
    await until(() => saved.state.tabs.some((tab) => tab.path === link));
  }
  await tab("Delta").waitFor();
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo", "Charlie", "Delta", "Search"]);
  expect(await strip(page).evaluate((node: HTMLElement) => node.scrollWidth > node.clientWidth && getComputedStyle(node).overflowX === "auto")).toBe(true);
  await strip(page).evaluate((node: HTMLElement) => { node.scrollLeft = 0; });
  await strip(page).hover();
  await page.mouse.wheel(0, 100);
  await page.waitForFunction(() => document.querySelector('[role="tablist"][aria-label="Open tabs"]')!.scrollLeft === 100);
  await page.mouse.move(0, 400);
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running");
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

test("narrow left placement is a drawer opened by the top-left button or a right swipe and closed by a pick, Escape, or a left swipe", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  await menu(page, "Bravo", "Move tabs to left");
  await until(() => saved.state.placement === "LEFT");
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

test("running tab title shimmers without shifting, and a pinned running tab shimmers its icon", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const title = (name: string) => tab(name).getByText(name, { exact: true });
  const indent = (name: string) => title(name).evaluate((node: HTMLElement) => node.getBoundingClientRect().left - node.parentElement!.getBoundingClientRect().left);
  await page.goto(`${origin}${href("delta")}`);
  await tab("Delta").waitFor();
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Delta").waitFor();
  await page.mouse.move(640, 600);
  expect(await animation(title("Delta"))).not.toBe("none");
  expect(await animation(title("Alpha"))).toBe("none");
  expect(await strip(page).getByRole("button", { name: "Close Delta" }).evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  await tab("Delta").click();
  await page.waitForURL(`**${href("delta")}`);
  expect(await animation(title("Delta"))).not.toBe("none");
  expect(await indent("Delta")).toBe(await indent("Alpha"));
  expect(await strip(page).getByRole("img", { name: "Turn running" }).count()).toBe(0);
  expect(await animation(tab("Delta").locator("svg"))).toBe("none");
  await menu(page, "Delta", "Pin");
  await menu(page, "Alpha", "Pin");
  expect(await animation(tab("Delta").locator("svg"))).not.toBe("none");
  expect(await mask(tab("Delta").locator("svg"))).toStartWith("linear-gradient(");
  expect(await animation(tab("Alpha").locator("svg"))).toBe("none");
  expect(await mask(tab("Alpha").locator("svg"))).toBe("none");
  await menu(page, "Delta", "Move tabs to left");
  expect(await animation(title("Delta"))).not.toBe("none");
  expect(await animation(tab("Delta").locator("svg"))).toBe("none");
}), 60_000);

test("an idle tab with background work shows an hourglass; a running turn keeps the chat icon", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await page.goto(`${origin}${href("delta")}`);
  await tab("Delta").waitFor();
  await page.goto(`${origin}${href("echo")}`);
  await tab("Echo").and(page.locator('[aria-description$="Background work running"]')).waitFor();
  expect(await tab("Echo").getAttribute("aria-description")).toEndWith("Background work running");
  expect(await tab("Echo").locator("svg").getAttribute("class")).toContain("lucide-hourglass");
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running");
  expect(await tab("Delta").locator("svg").getAttribute("class")).toContain("lucide-message-square");
}), 60_000);

test("the shimmer visibly sweeps the selected and other running titles in both themes", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const sweep = async (name: string) => {
    const title = tab(name).getByText(name, { exact: true });
    const levels: number[] = [];
    for (const progress of [0, 0.2, 0.35, 0.5, 0.65, 0.8]) {
      await title.evaluate((node: Element, progress: number) => { const run = node.getAnimations()[0]!; run.pause(); run.currentTime = Number(run.effect!.getTiming().duration) * progress; }, progress);
      const png = (await title.screenshot()).toString("base64");
      levels.push(await page.evaluate(async (png: string) => {
        const image = new Image();
        image.src = `data:image/png;base64,${png}`;
        await image.decode();
        const canvas = Object.assign(document.createElement("canvas"), { width: image.width, height: image.height });
        const context = canvas.getContext("2d")!;
        context.drawImage(image, 0, 0);
        const { data } = context.getImageData(0, 0, image.width, image.height);
        let sum = 0;
        for (let i = 0; i < data.length; i += 4) sum += (data[i]! + data[i + 1]! + data[i + 2]!) / 3;
        return sum / (data.length / 4);
      }, png));
    }
    return Math.max(...levels) - Math.min(...levels);
  };
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Alpha").waitFor();
  await page.goto(`${origin}${href("delta")}`);
  await tab("Alpha").waitFor();
  await page.mouse.move(640, 600);
  // Only a selected title in dark mode dims under the band; each state and each scheme is swept once.
  await page.emulateMedia({ colorScheme: "dark" });
  expect(await sweep("Delta")).toBeGreaterThan(8);
  await tab("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  await page.emulateMedia({ colorScheme: "light" });
  expect(await sweep("Delta")).toBeGreaterThan(8);
}), 60_000);

test("with reduced motion a running tab's title and pinned icon show a still gradient", () => withPage({ reducedMotion: "reduce" }, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const gradient = (name: string) => tab(name).getByText(name, { exact: true }).evaluate((node: Element) => getComputedStyle(node).backgroundImage);
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Alpha").waitFor();
  await page.goto(`${origin}${href("delta")}`);
  const title = tab("Delta").getByText("Delta", { exact: true });
  await tab("Delta").and(page.locator('[aria-description$="Turn running"]')).waitFor();
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running");
  expect(await animation(title)).toBe("none");
  expect(await gradient("Delta")).toStartWith("linear-gradient(");
  expect(await gradient("Alpha")).toBe("none");
  await menu(page, "Delta", "Pin");
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Turn running");
  expect(await animation(tab("Delta").locator("svg"))).toBe("none");
  expect(await mask(tab("Delta").locator("svg"))).toStartWith("linear-gradient(");
}), 60_000);

test("an unread tab shows a bold title and a primary bar on its top edge, pinned and in the sidebar too", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  // The bar is the tab's 2px absolutely placed child flush with its top edge; the title is the tab's text span.
  const cue = (id: string) => strip(page).locator(`[data-tab="${href(id)}"]`).evaluate((root: HTMLElement) => {
    const probe = root.appendChild(Object.assign(document.createElement("i"), { style: "background-color: var(--primary)" }));
    const primary = getComputedStyle(probe).backgroundColor;
    probe.remove();
    const bar = [...root.children].find((node) => getComputedStyle(node).position === "absolute" && node.getBoundingClientRect().top === root.getBoundingClientRect().top && node.getBoundingClientRect().height === 2);
    const title = root.querySelector('[role="tab"] > span');
    return { bar: bar ? getComputedStyle(bar).backgroundColor === primary : false, weight: title ? getComputedStyle(title).fontWeight : "" };
  });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openSaved(page, origin, href("foxtrot"), "Foxtrot", saved);
  expect(await cue("foxtrot")).toEqual({ bar: true, weight: "700" });
  expect(await cue("alpha")).toEqual({ bar: false, weight: "400" });
  expect(await tab("Foxtrot").getAttribute("aria-description")).toBe("Space: Main, Turn running, Unread");
  // A pinned tab in the top strip has no title, so the bar alone shows it.
  await menu(page, "Foxtrot", "Pin");
  expect(await cue("foxtrot")).toEqual({ bar: true, weight: "" });
  expect(await tab("Foxtrot").getAttribute("aria-description")).toBe("Turn running, Unread");
  await menu(page, "Foxtrot", "Move tabs to left");
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  expect(await cue("foxtrot")).toEqual({ bar: true, weight: "700" });
  expect(await cue("alpha")).toEqual({ bar: false, weight: "400" });
}), 60_000);

test("tab groups take a name and color, collapse, adopt opened tabs, follow drags, and survive reload", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const header = strip(page).getByRole("button", { name: "Work", exact: true });
  const field = page.getByRole("textbox", { name: "Group name" });
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) await openSaved(page, origin, href(id), name, saved);
  await menu(page, "Alpha", "Add to group", "New group");
  await field.waitFor();
  expect(await field.evaluate((node: HTMLInputElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.type("Work");
  expect(await field.inputValue()).toBe("Work");
  await page.getByRole("radio", { name: "Green" }).check();
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "Bravo", "Add to group", "Work");
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Work, Space: Main");
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");

  await until(() => saved.state.groups.some((group) => group.name === "Work") && saved.state.tabs.filter((tab) => tab.group).length === 2);
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
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Group Work, Space: Main, Turn running");
  await tab("Charlie").dragTo(tab("Alpha"));
  expect(await order(page)).toEqual(["Work", "Charlie", "Alpha", "Bravo", "Delta"]);
  await menu(page, "Delta", "Remove from group");
  expect(await order(page)).toEqual(["Work", "Charlie", "Alpha", "Bravo", "Delta"]);
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running");
  await tab("Alpha").dragTo(tab("Delta"));
  expect(await order(page)).toEqual(["Work", "Charlie", "Bravo", "Delta", "Alpha"]);
  expect(await tab("Alpha").getAttribute("aria-description")).toBe("Space: Main");
}), 60_000);

test("Cmd/Ctrl+click and Shift+click select tabs that the tab menu groups and ungroups together", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const items = async (name: string) => {
    await tab(name).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Close", exact: true }).waitFor();
    const listed = await page.getByRole("menuitem").allInnerTexts();
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
    return listed;
  };
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"], ["delta", "Delta"]]) await openSaved(page, origin, href(id), name, saved);
  await tab("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  await tab("Charlie").click({ modifiers: ["ControlOrMeta"] });
  expect(page.url()).toEndWith(href("alpha"));
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("In selection, Space: Main");
  // An unselected tab's menu acts on it alone, and a selected one's menu has no Rename session.
  expect(await items("Bravo")).toContain("Rename session");
  expect(await items("Charlie")).not.toContain("Rename session");
  await menu(page, "Charlie", "Add to group", "New group");
  await page.keyboard.type("Work");
  await page.keyboard.press("Escape");
  expect(await order(page)).toEqual(["Work", "Alpha", "Charlie", "Bravo", "Delta"]);

  // Shift+click selects the shown tabs from the active one; the selection joins a group together.
  await menu(page, "Delta", "Add to group", "New group");
  await page.keyboard.press("Escape");
  await tab("Bravo").click({ modifiers: ["Shift"] });
  expect(await page.evaluate(() => getSelection()!.toString())).toBe("");
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("In selection, Space: Main");
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Group Blue group, Space: Main, Turn running");
  await menu(page, "Bravo", "Add to group", "Blue group");
  expect(await order(page)).toEqual(["Blue group", "Delta", "Alpha", "Charlie", "Bravo"]);
  await menu(page, "Alpha", "Remove from group");
  expect(await order(page)).toEqual(["Blue group", "Delta", "Alpha", "Charlie", "Bravo"]);
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("In selection, Space: Main");

  // Escape on a tab drops the selection; a plain click on the active tab does too.
  await tab("Alpha").focus();
  await page.keyboard.press("Escape");
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Space: Main");
  await tab("Bravo").click({ modifiers: ["ControlOrMeta"] });
  await tab("Alpha").click();
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Space: Main");
  expect(page.url()).toEndWith(href("alpha"));
}), 60_000);

test("a group moves whole by dragging its header onto a tab or a collapsed group's header, or with Shift+Arrow", () => withPage({}, async (page, origin, _prompts, saved) => {
  const header = (name: string) => strip(page).getByRole("button", { name, exact: true });
  const group = async (tab: string, name: string) => {
    await menu(page, tab, "Add to group", "New group");
    await page.keyboard.type(name);
    await page.keyboard.press("Escape");
    await page.getByRole("textbox", { name: "Group name" }).waitFor({ state: "detached" });
  };
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"], ["delta", "Delta"]]) await openSaved(page, origin, href(id), name, saved);
  await group("Alpha", "Work");
  await menu(page, "Bravo", "Add to group", "Work");
  await header("Work").dragTo(strip(page).getByRole("tab", { name: "Delta", exact: true }));
  expect(await order(page)).toEqual(["Charlie", "Delta", "Work", "Alpha", "Bravo"]);

  await group("Charlie", "Ops");
  await header("Ops").click();
  expect(await order(page)).toEqual(["Ops", "Delta", "Work", "Alpha", "Bravo"]);
  await header("Work").dragTo(header("Ops"));
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Ops", "Delta"]);

  await header("Work").focus();
  await page.keyboard.press("Shift+ArrowRight");
  expect(await order(page)).toEqual(["Ops", "Work", "Alpha", "Bravo", "Delta"]);
  expect(await header("Work").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Shift+ArrowLeft");
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Ops", "Delta"]);
  await until(() => saved.state.tabs.map((tab) => tab.path).join() === ["alpha", "bravo", "charlie", "delta"].map(href).join());
}), 60_000);

test("tab group editor moves New session in, ungroups, and closes; pins leave groups; collapsed groups skip arrow keys; a lost group is dropped", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const field = page.getByRole("textbox", { name: "Group name" });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name, saved);
  await menu(page, "Alpha", "Add to group", "New group");
  await page.keyboard.type("Work");
  await page.keyboard.press("Enter");
  await field.waitFor({ state: "detached" });
  await menu(page, "Bravo", "Add to group", "Work");
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
  await until(() => saved.state.placement === "LEFT" && saved.state.groups.some((group) => group.collapsed));
  await page.reload();
  // Charlie comes from the URL before saved tabs load; Bravo waits for the saved placement and group too.
  await tab("Bravo").waitFor();
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
  await menu(page, "Alpha", "Add to group", "New group");
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "New session", "Add to group", "Grey group");
  expect(await order(page)).toEqual(["Bravo", "Grey group", "Alpha", "New session", "Charlie"]);
  await strip(page).getByRole("button", { name: "Edit Grey group", exact: true }).click();
  await page.getByRole("button", { name: "Close group", exact: true }).click();
  await page.waitForURL(`**${href("charlie")}`);
  expect(await order(page)).toEqual(["Bravo", "Charlie"]);

  await menu(page, "Charlie", "Add to group", "New group");
  await page.keyboard.press("Escape");
  // A tab whose group the saved state no longer has leaves the group.
  await until(() => saved.state.groups.some((group) => group.color === "Grey"));
  saved.state.groups = [];
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await order(page)).toEqual(["Bravo", "Charlie"]);
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");
}), 60_000);

test("spaces show only their own tabs, return to their last tab, follow opened tabs, and survive reload", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const space = page.getByRole("button", { name: /^Space: / });
  const choose = (item: string, role?: string) => chooseSpace(page, item, role);
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name, saved);
  expect(await space.getAttribute("aria-label")).toBe("Space: Main");
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");

  // A new space takes New session along and is named in place.
  await choose("New space");
  await page.waitForURL(`${origin}/`);
  await page.getByRole("textbox", { name: "Space name" }).waitFor();
  await page.keyboard.type("Ops");
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Space: Ops" }).waitFor();
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
  await until(() => saved.state.space !== "main" && Object.values(saved.state.last).includes(href("bravo")));
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

  // A pinned tab stays in its space and moves, still pinned, like any other tab.
  await menu(page, "Alpha", "Pin");
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Bravo"]);
  await choose("Main", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  await menu(page, "Alpha", "Move to space", "Ops");
  expect(await names(page)).toEqual(["Charlie", "New session"]);
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  await tab("Alpha").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Unpin", exact: true }).waitFor();
  await page.keyboard.press("Escape");

  // Deleting a space moves its tabs, pinned ones still pinned, to the first space, and the last space stays.
  await choose("Delete space");
  await page.getByRole("button", { name: "Space: Main" }).waitFor();
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session", "Bravo"]);
  expect(page.url()).toEndWith(href("bravo"));
  await space.click();
  await page.getByRole("menuitem", { name: "Rename space" }).waitFor();
  expect(await page.getByRole("menuitem").allInnerTexts()).toEqual(["New space", "Rename space"]);
  await page.keyboard.press("Escape");

  // A pinned tab saved without a space shows only in the first space.
  const settled = () => until(() => saved.spaces.length === 1 && saved.state.tabs.every((tab) => tab.space === "main"));
  await settled();
  saved.state.tabs = saved.state.tabs.map((tab) => ({ ...tab, space: tab.pinned ? "" : tab.space }));
  await page.reload();
  await tab("Bravo").waitFor();
  await choose("New space");
  await page.getByRole("textbox", { name: "Space name" }).waitFor();
  await page.keyboard.press("Escape");
  expect(await names(page)).toEqual(["New session"]);
  await choose("Delete space");

  // A space deleted elsewhere hands its tabs, groups kept, to the first space; the menu also sits atop left tabs.
  await page.getByRole("button", { name: "Space: Main" }).waitFor();
  await settled();
  saved.state.tabs = saved.state.tabs.map((tab) => tab.path === href("bravo") ? { ...tab, space: "gone", group: "kept" } : tab);
  saved.state.groups = [{ id: "kept", name: "Kept", color: "Blue" }];
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Kept, Space: Main");
  await menu(page, "Bravo", "Move tabs to left");
  const [button, list] = [await space.boundingBox(), await strip(page).boundingBox()];
  expect(button.y + button.height).toBeLessThanOrEqual(list.y);
}), 60_000);

test("New session offers every agent, and a space shows its own and spaceless cron jobs, ignoring case", () => withPage({}, async (page, origin, _prompts, saved) => {
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
  // Main shows only spaceless cron jobs: opsreport hides with its run. Every session lists the agents the server offers.
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await agents()).toEqual(["main", "opsbot"]);
  await openSaved(page, origin, "/cron", "Cron", saved);
  expect(await dashboard()).toEqual(["daily", "retired"]);
  await openSaved(page, origin, "/", "New session", saved);
  expect(await agents()).toEqual(["main", "opsbot"]);
  expect(await runnable()).toEqual(["daily"]);
  // The OPS agent and the Ops job belong to a space named ops.
  await chooseSpace(page, "New space");
  await page.getByRole("textbox", { name: "Space name" }).waitFor();
  await page.keyboard.type("ops");
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Space: ops" }).waitFor();
  expect(await agents()).toEqual(["main", "opsbot"]);
  expect(await runnable()).toEqual(["daily", "opsreport"]);
  await chooseSpace(page, "Main", "menuitemradio");
  await menu(page, "Cron", "Move to space", "ops");
  await chooseSpace(page, "ops", "menuitemradio");
  await strip(page).getByRole("tab", { name: "Cron", exact: true }).click();
  expect(await dashboard()).toEqual(["daily", "opsreport", "retired"]);
}), 60_000);

test("a group lives in one space: menus offer only this space's groups, groups move whole, and New tab in group brings New session", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const header = strip(page).getByRole("button", { name: "Work", exact: true }), edit = strip(page).getByRole("button", { name: "Edit Work", exact: true });
  const editor = (item: string) => edit.click().then(() => page.getByRole("button", { name: item, exact: true }).click());
  const green = page.getByRole("radio", { name: "Green" });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name, saved);
  await menu(page, "Alpha", "Add to group", "New group");
  await page.keyboard.type("Work");
  await green.check();
  await page.keyboard.press("Escape");
  await menu(page, "Bravo", "Add to group", "Work");

  // A new space takes New session, and its tab menu offers no group from Main.
  await chooseSpace(page, "New space");
  await page.getByRole("textbox", { name: "Space name" }).fill("Ops");
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Space: Ops" }).waitFor();
  await tab("New session").click({ button: "right" });
  expect(await page.getByRole("menuitem").allInnerTexts()).toEqual(["Close", "Close others", "Close to the right", "Close all", "Pin", "Add to group", "Move to space", "Move tabs to left"]);
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

  // Reload keeps spaces, groups, colors, collapsed state, and memberships; the pinned tab stays in Main in no group.
  await until(() => saved.state.tabs.some((tab) => tab.path === href("charlie") && tab.pinned) && saved.state.groups.some((group) => group.collapsed));
  await page.reload();
  await tab("Charlie").waitFor();
  expect(await order(page)).toEqual(["Charlie", "Alpha"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["New session", "Work"]);
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  await edit.click();
  expect(await green.isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await header.click();
  expect(await order(page)).toEqual(["New session", "Work", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Work, Space: Ops");

  // New tab in group brings New session over from Main.
  await chooseSpace(page, "Main", "menuitemradio");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Charlie", "New session", "Alpha"]);
  expect(await tab("Charlie").getAttribute("aria-description")).toBeNull();
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  await editor("New tab in group");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Work", "Bravo", "New session"]);
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Ops");

  // Deleting Ops moves the collapsed group, intact, to Main.
  await header.click();
  await chooseSpace(page, "Delete space");
  await page.getByRole("button", { name: "Space: Main" }).waitFor();
  expect(await order(page)).toEqual(["Charlie", "Alpha", "Work", "New session"]);
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Main");
}), 60_000);

test("a first message to an agent with a space creates the space and takes the session there", () => withPage({}, async (page, origin) => {
  await page.goto(`${origin}/`);
  await page.getByRole("button", { name: "Space: Main" }).waitFor();
  await page.getByRole("button", { name: "Choose agent", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "opsbot", exact: true }).click();
  await page.getByPlaceholder("Message a new session").fill("hello");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL(`**${href("fresh")}`);
  await page.getByRole("button", { name: "Space: OPS" }).waitFor();
  expect(await names(page)).toEqual(["Ops chat"]);
}), 30_000);

test("a session opens in the space it was born in, and a moved one can be restored there", () => withPage({}, async (page, origin, _prompts, saved) => {
  saved.spaces.push({ id: "ops", name: "OPS" });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openFromPalette(page, "Echo");
  await page.waitForURL(`**${href("echo")}`);
  await page.getByRole("button", { name: "Space: OPS" }).waitFor();
  expect(await names(page)).toEqual(["Echo"]);
  await menu(page, "Echo", "Move to space", "Main");
  await page.getByRole("button", { name: "Space: Main" }).waitFor();
  expect(await names(page)).toEqual(["Alpha", "Echo"]);
  await menu(page, "Echo", 'Restore to "OPS"');
  await page.getByRole("button", { name: "Space: OPS" }).waitFor();
  await strip(page).getByRole("tab", { name: "Echo", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close", exact: true }).waitFor();
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain('Restore to "OPS"');
  await page.keyboard.press("Escape");
}), 30_000);

test("tabs follow a person across windows: a window takes in changes on focus but stays on its own tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  const other = await page.context().browser().newPage();
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  await openSaved(other, origin, href("charlie"), "Charlie", saved);
  expect(await names(other)).toEqual(["Alpha", "Bravo", "Charlie"]);

  // The other window closes the tab this one shows, and opens another: this window moves off the closed tab.
  await menu(other, "Bravo", "Close");
  await until(() => !saved.state.tabs.some((tab) => tab.path === href("bravo")));
  await page.evaluate(() => { dispatchEvent(new Event("visibilitychange")); });
  await page.waitForURL(`**${href("charlie")}`);
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);

  // A tab opened elsewhere shows up on focus alone, and this window stays where it is.
  await openSaved(other, origin, href("delta"), "Delta", saved);
  await page.evaluate(() => { dispatchEvent(new Event("focus")); });
  await strip(page).getByRole("tab", { name: "Delta", exact: true }).waitFor();
  expect(page.url()).toEndWith(href("charlie"));
}), 60_000);

test("a change made from tabs older than another window's save keeps both", () => withPage({}, async (page, origin, _prompts, saved) => {
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  // Another window opens Charlie, and this one, without reloading, opens Bravo: its save is refused and merged.
  [saved.state.tabs, saved.revision] = [[...saved.state.tabs, { path: href("charlie"), space: "main" }], saved.revision + 1];
  await openFromPalette(page, "Bravo");
  await strip(page).getByRole("tab", { name: "Charlie", exact: true }).waitFor();
  await until(() => ["alpha", "bravo", "charlie"].every((id) => saved.state.tabs.some((tab) => tab.path === href(id))));
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  expect(saved.refused).toBe(1);
}), 30_000);
