import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const pages = [{ agent: "main", path: "index.html" }, { agent: "ops-bot", space: "Ops", path: "index.html" }, { agent: "reporter", space: "Billing", path: "index.html" }, { agent: "reporter", space: "Billing", path: "q3/report.html" }];
// The page keeps every message it receives, and its fragment token, for the test to read.
const script = `<script>window.replies = []; addEventListener("message", (event) => replies.push(event.data)); window.token = new URLSearchParams(location.hash.slice(1)).get("rc");</script>`;

async function withPage(spaces: string[], run: (page: any, origin: string, reads: object[]) => Promise<void>, options = {}) {
  const { chromium: engine } = await import(playwright!);
  const reads: object[] = [];
  // The shared spaces, and the tab state the page saves.
  const saved = { spaces: spaces.map((name, index) => ({ id: `s${index}`, name })), state: { tabs: [], groups: [], space: "s0", last: {}, placement: "TOP" } };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname.startsWith("/pages-raw/")) return new Response(script, { headers: { "Content-Type": "text/html", "Content-Security-Policy": "sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json().catch(() => ({})) as { agent: string; path: string; data: boolean; state?: typeof saved.state };
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "pages" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "pages" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListPages": return Response.json({ pages });
      case "/api/Tabs": if (input.state) saved.state = input.state; return Response.json(saved);
      case "/api/GetPage":
        reads.push(input);
        return input.agent === "reporter" && input.path === "revenue" && input.data ? Response.json({ space: "Billing", contentType: "application/json", body: Buffer.from(`{"total":3}`).toString("base64") }) : Response.json({ code: 5, message: "page not found" }, { status: 404 });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, ...options });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const origin = `http://127.0.0.1:${server.port}`;
    await page.goto(`${origin}/`);
    await strip(page).getByRole("tab", { name: "New session", exact: true }).waitFor();
    await run(page, origin, reads);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}

const strip = (page: any) => page.getByRole("tablist", { name: "Open tabs" });
const names = (page: any) => strip(page).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
const space = (page: any) => page.getByRole("button", { name: /^Space: / }).getAttribute("aria-label");
const go = (page: any, link: string) => page.evaluate((link: string) => { history.pushState(null, "", link); dispatchEvent(new PopStateEvent("popstate")); }, link);
async function chooseSpace(page: any, name: string) {
  await page.getByRole("button", { name: /^Space: / }).click();
  await page.getByRole("menuitemradio", { name, exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
}

test("a page link opens in the space named by its segment, ignoring case, on a fresh load and in the app", () => withPage(["Main", "billing"], async (page, origin) => {
  await page.goto(`${origin}/pages/Billing/reporter/index.html`);
  // The strip draws before the spaces load; the tab then moves to the space its link names.
  await page.getByRole("button", { name: "Space: billing" }).waitFor();
  await page.waitForURL(`${origin}/pages/billing/reporter/index.html`);

  await chooseSpace(page, "Main");
  await go(page, "/pages/Billing/reporter/q3/report.html");
  await strip(page).getByRole("tab", { name: "reporter · q3/report.html", exact: true }).waitFor();
  expect(await space(page)).toBe("Space: billing");
  await page.waitForURL(`${origin}/pages/billing/reporter/q3/report.html`);
  expect((await names(page)).toSorted()).toEqual(["reporter · index.html", "reporter · q3/report.html"]);

  // Without a matching space the page opens in the current space, and no space is added.
  await chooseSpace(page, "Main");
  await go(page, "/pages/Finance/reporter/index.html");
  await strip(page).getByRole("tab", { name: "reporter · index.html", exact: true }).waitFor();
  expect(await space(page)).toBe("Space: Main");
  expect(page.url()).toBe(`${origin}/pages/Finance/reporter/index.html`);
  await page.getByRole("button", { name: /^Space: / }).click();
  await page.getByRole("menuitem", { name: "New space", exact: true }).waitFor();
  expect(await page.getByRole("menuitemradio").allInnerTexts()).toEqual(["Main", "billing"]);
  await page.keyboard.press("Escape");
}), 60_000);

test("a page's shared link and its Page Index link share one tab, and a - link opens in each space", () => withPage(["Main", "billing", "Space 2"], async (page, origin) => {
  await chooseSpace(page, "billing");
  await go(page, "/pages");
  await page.getByRole("link", { name: "reporter · index.html", exact: true }).click();
  await page.waitForURL(`${origin}/pages/billing/reporter/index.html`);
  await go(page, "/pages/Billing/reporter/index.html");
  await page.waitForURL(`${origin}/pages/billing/reporter/index.html`);
  expect(await names(page)).toEqual(["New session", "reporter · index.html"]);

  await go(page, "/pages/-/main/index.html");
  await page.waitForURL(`${origin}/pages/billing/main/index.html`);
  await chooseSpace(page, "Main");
  await go(page, "/pages/-/main/index.html");
  await page.waitForURL(`${origin}/pages/Main/main/index.html`);
  expect(await space(page)).toBe("Space: Main");
  expect(await names(page)).toContain("main · index.html");

  await go(page, "/pages/Space%202/main/index.html");
  await page.waitForURL(`${origin}/pages/Space%202/main/index.html`);
  expect(await space(page)).toBe("Space: Space 2");
  expect(await names(page)).toEqual(["main · index.html"]);
}), 60_000);

test("a sandboxed page reads its agent's Page Data only with its token and from its own frame", () => withPage(["Main"], async (page, origin, reads) => {
  await page.goto(`${origin}/pages/-/reporter/index.html`);
  const iframe = page.locator("iframe");
  expect(await iframe.getAttribute("sandbox")).toBe("allow-scripts allow-popups allow-forms allow-modals allow-downloads");
  expect(await iframe.getAttribute("title")).toBe("reporter · index.html");
  const src = new URL(await iframe.getAttribute("src"), origin);
  expect(src.pathname).toBe("/pages-raw/reporter/index.html");
  const token = new URLSearchParams(src.hash.slice(1)).get("rc")!;
  expect(token).toMatch(/^\d+-\d+-\d+-\d+$/);
  const frame = await (await iframe.elementHandle()).contentFrame();
  await frame.waitForFunction(() => (window as any).token);
  // The top window answers nothing it posts itself, even with the token.
  await page.evaluate((token: string) => {
    (window as any).replies = [];
    addEventListener("message", (event) => (window as any).replies.push(event.data));
    postMessage({ type: "rocketclaw.read", id: 1, key: "revenue", token }, "*");
  }, token);
  await frame.evaluate(() => {
    const token = (window as any).token;
    parent.postMessage({ type: "rocketclaw.read", id: 2, key: "revenue" }, "*");
    parent.postMessage({ type: "rocketclaw.read", id: 3, key: "revenue", token: "wrong" }, "*");
    parent.postMessage({ type: "rocketclaw.read", id: 4, key: "revenue", token }, "*");
    parent.postMessage({ type: "rocketclaw.read", id: 5, key: "missing", token }, "*");
  });
  await frame.waitForFunction(() => (window as any).replies.length === 2);
  // Independent reads can finish in either order; each reply belongs to its request ID.
  expect(await frame.evaluate(() => (window as any).replies.toSorted((a: { id: number }, b: { id: number }) => a.id - b.id))).toEqual([{ type: "rocketclaw.read", id: 4, value: { total: 3 } }, { type: "rocketclaw.read", id: 5, error: "page not found" }]);
  expect(reads).toHaveLength(2);
  expect(reads).toEqual(expect.arrayContaining([{ agent: "reporter", path: "revenue", data: true }, { agent: "reporter", path: "missing", data: true }]));
  expect(await page.evaluate(() => (window as any).replies.filter((reply: object) => "value" in reply || "error" in reply))).toEqual([]);
}), 60_000);

for (const width of [1280, 320]) test(`the Page Index lists this space's and spaceless agents' pages and opens them at ${width}px`, () => withPage(["Billing"], async (page, origin) => {
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Pages: Index", exact: true }).click();
  await page.waitForURL(`${origin}/pages`);
  await strip(page).getByRole("tab", { name: "Pages", exact: true }).waitFor();
  const links = page.locator("main").getByRole("link");
  await links.first().waitFor();
  expect(await links.allInnerTexts()).toEqual(["main · index.html", "reporter · index.html", "reporter · q3/report.html"]);
  for (const box of await links.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getBoundingClientRect().right))) expect(box).toBeLessThanOrEqual(width);
  await links.filter({ hasText: "reporter · q3/report.html" }).click();
  await page.waitForURL(`${origin}/pages/Billing/reporter/q3/report.html`);
  await strip(page).getByRole("tab", { name: "reporter · q3/report.html", exact: true }).waitFor();
  expect(await page.locator("iframe").getAttribute("src")).toStartWith("/pages-raw/reporter/q3/report.html#rc=");
}, { viewport: { width, height: 800 } }), 60_000);
