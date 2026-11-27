import { expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const pages = [{ space: "Gone", path: "index.html" }, { space: "Ops", path: "index.html" }, { space: "Billing", path: "index.html" }, { space: "Billing", path: "q3/report.html" }];
const script = `<script>window.replies = []; addEventListener("message", (event) => replies.push(event.data)); window.token = new URLSearchParams(location.hash.slice(1)).get("rc");</script>`;

async function withPage(spaces: string[], run: (page: any, origin: string, reads: object[], moves: object[]) => Promise<void>, options = {}) {
  const { chromium: engine } = await import(playwright!);
  const reads: object[] = [], moves: object[] = [];
  const saved = { spaces: spaces.map((name, index) => ({ id: `s${index}`, name })), state: { tabs: [], groups: [], space: "s0", last: {} } };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname.startsWith("/pages-raw/")) return new Response(url.pathname.endsWith(".pdf") ? "%PDF-1.4" : script, { headers: { "Content-Type": url.pathname.endsWith(".pdf") ? "application/pdf" : "text/html", "Content-Security-Policy": "sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    const input = await request.json().catch(() => ({})) as { space: string; path: string; data: boolean; state?: typeof saved.state };
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "pages" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "pages" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/History": return Response.json({ messages: [], delegations: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, backgroundJobs: [], pendingQuestions: [], start: "0", more: false });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/ListPages": return Response.json({ pages });
      case "/api/UpdateSession": moves.push(input); return Response.json({});
      case "/api/Tabs": case "/api/ChangeTabs": if (input.state) saved.state = input.state; return Response.json(saved);
      case "/api/GetPage":
        reads.push(input);
        return input.space === "Billing" && input.path === "revenue" && input.data ? Response.json({ space: "Billing", contentType: "application/json", body: Buffer.from(`{"total":3}`).toString("base64") }) : Response.json({ code: 5, message: "page not found" }, { status: 404 });
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
    await page.getByPlaceholder("Message a new session").waitFor();
    await run(page, origin, reads, moves);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}

const strip = (page: any) => page.locator('div:has(> div > button[aria-current="true"]) > [role="tablist"]');
const names = (page: any) => strip(page).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
const space = (page: any) => page.locator('button[aria-current="true"]').getAttribute("aria-label");
const go = (page: any, link: string) => page.evaluate((link: string) => { history.pushState(null, "", link); dispatchEvent(new PopStateEvent("popstate")); }, link);
async function chooseSpace(page: any, name: string) {
  await page.getByRole("button", { name, exact: true }).hover();
  await page.getByRole("button", { name: `${name} space actions`, exact: true }).click();
  await page.getByRole("menuitem", { name: "Go to space", exact: true }).click();
  await page.locator(`button[aria-current="true"][aria-label="${name}"]`).waitFor();
}

test("a page link opens in the space named by its segment, ignoring case, on a fresh load and in the app", () => withPage(["Main", "billing"], async (page, origin) => {
  await page.goto(`${origin}/pages/Billing/index.html`);
  await page.locator('button[aria-current="true"][aria-label="billing"]').waitFor();
  await page.waitForURL(`${origin}/pages/billing/index.html`);

  await chooseSpace(page, "Main");
  await go(page, "/pages/Billing/q3/report.html");
  await strip(page).getByRole("tab", { name: "billing · q3/report.html", exact: true }).waitFor();
  expect(await space(page)).toBe("billing");
  await page.waitForURL(`${origin}/pages/billing/q3/report.html`);
  expect((await names(page)).toSorted()).toEqual(["billing · index.html", "billing · q3/report.html"]);

  await chooseSpace(page, "Main");
  await go(page, "/pages/Finance/index.html");
  await strip(page).getByRole("tab", { name: "Finance · index.html", exact: true }).waitFor();
  expect(await space(page)).toBe("Main");
  expect(page.url()).toBe(`${origin}/pages/Finance/index.html`);
  expect(await page.locator("[data-tab-lists] button[aria-description][aria-expanded]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["Main", "billing"]);
}), 60_000);

test("a hosted page updates beside a session without remounting its draft", () => withPage(["Main"], async (page, origin) => {
  const route = `/s/${btoa("sample")}`;
  const example = { space: "Billing", path: "q3/read me.html" };
  const raw = "/pages-raw/Billing/q3/read%20me.html";
  let revision = "first", missing = false, offline = false, mounts = 0;
  let hold = false;
  const requested = Promise.withResolvers<void>(), release = Promise.withResolvers<void>();
  await page.route(`**${raw}`, (route: any) => {
    mounts++;
    return route.fulfill({ contentType: "text/html", headers: { "Cache-Control": "no-store" }, body: `<h1>${revision}</h1>` });
  });
  await page.route("**/api/ListPages", async (route: any) => {
    const { space, path } = route.request().postDataJSON();
    if (!path) return route.fulfill({ json: { pages: [example] } });
    expect({ space, path }).toEqual(example);
    if (hold) { requested.resolve(); await release.promise; }
    if (offline) return route.fulfill({ status: 503, json: { message: "Unavailable" } });
    return route.fulfill({ json: { pages: missing ? [] : [{ ...example, updatedAt: revision }] } });
  });
  await page.goto(`${origin}${route}`);
  const composer = page.locator("main textarea").first();
  await composer.fill("Unsent draft");
  const draft = await composer.elementHandle();
  await page.getByRole("button", { name: "Tab actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Hosted Pages", exact: true }).click({ timeout: 2500 });
  const pane = page.getByRole("complementary", { name: "Hosted Pages", exact: true });
  await pane.getByRole("link", { name: "q3/read me.html", exact: true }).click();
  const frame = pane.frameLocator("iframe");
  await frame.getByRole("heading", { name: "first" }).waitFor();
  expect(await pane.locator("iframe").getAttribute("sandbox")).toBe("allow-scripts allow-popups allow-forms allow-modals allow-downloads");
  const checksPage = (request: any) => request.url().endsWith("/api/ListPages") && request.postDataJSON().path === example.path;
  await page.waitForResponse((response: any) => checksPage(response.request()));
  await page.waitForRequest(checksPage);
  expect(mounts).toBe(1);
  revision = "second";
  await frame.getByRole("heading", { name: "second" }).waitFor();
  offline = true;
  await pane.getByRole("alert").waitFor();
  expect(await frame.getByRole("heading", { name: "second" }).isVisible()).toBe(true);
  offline = false;
  missing = true;
  await pane.getByText("Page unavailable", { exact: true }).waitFor();
  expect(await pane.locator("iframe").count()).toBe(0);
  await page.setViewportSize({ width: 390, height: 844 });
  hold = true;
  try {
    await requested.promise;
    const canceled = page.waitForEvent("requestfailed", { predicate: (request: any) => request.url().endsWith("/api/ListPages") });
    await page.getByRole("dialog", { name: "Hosted Pages", exact: true }).getByRole("button", { name: "Close hosted pages", exact: true }).click();
    await canceled;
  } finally { release.resolve(); }
  expect(new URL(page.url()).pathname).toBe(route);
  expect(new URL(page.url()).search).toBe("");
  expect(await page.locator("iframe").count()).toBe(0);
  expect(await draft.evaluate((node: Element) => node.isConnected)).toBe(true);
  expect(await composer.inputValue()).toBe("Unsent draft");
}), 30_000);

test("a page's shared link and its Page Index link share one tab", () => withPage(["Main", "billing"], async (page, origin) => {
  await chooseSpace(page, "billing");
  await go(page, "/pages");
  await page.locator("main section").filter({ hasText: "Billing" }).getByRole("link", { name: "index.html", exact: true }).click();
  await page.waitForURL(`${origin}/pages/billing/index.html`);
  await go(page, "/pages/Billing/index.html");
  await page.waitForURL(`${origin}/pages/billing/index.html`);
  expect(await names(page)).toEqual(["New session", "Pages", "billing · index.html"]);
}), 60_000);

test("moving a page's tab moves only the tab, and its title keeps naming the page's space", () => withPage(["Main", "billing"], async (page, origin, _reads, moves) => {
  await page.goto(`${origin}/pages/Billing/index.html`);
  await page.locator('button[aria-current="true"][aria-label="billing"]').waitFor();
  await page.waitForURL(`${origin}/pages/billing/index.html`);

  await strip(page).getByRole("tab", { name: "billing · index.html", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Move to space", exact: true }).click();
  await page.getByRole("menuitem", { name: "Main", exact: true }).click();
  await chooseSpace(page, "Main");
  await go(page, "/pages/Main/index.html");
  await strip(page).getByRole("tab", { name: "Main · index.html", exact: true }).waitFor();
  expect((await names(page)).filter((name: string) => name.endsWith("index.html")).toSorted()).toEqual(["Main · index.html", "billing · index.html"]);
  expect(moves).toEqual([]);
}), 60_000);

test("a sandboxed page reads its space's Page Data only with its token and from its own frame", () => withPage(["Main"], async (page, origin, reads) => {
  await page.goto(`${origin}/pages/Billing/index.html`);
  const iframe = page.locator("iframe");
  expect(await iframe.getAttribute("sandbox")).toBe("allow-scripts allow-popups allow-forms allow-modals allow-downloads");
  expect(await iframe.getAttribute("title")).toBe("Billing · index.html");
  const src = new URL(await iframe.getAttribute("src"), origin);
  expect(src.pathname).toBe("/pages-raw/Billing/index.html");
  const token = new URLSearchParams(src.hash.slice(1)).get("rc")!;
  expect(token).toMatch(/^\d+-\d+-\d+-\d+$/);
  const frame = await (await iframe.elementHandle()).contentFrame();
  await frame.waitForFunction(() => (window as any).token);
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
  expect(await frame.evaluate(() => (window as any).replies.toSorted((a: { id: number }, b: { id: number }) => a.id - b.id))).toEqual([{ type: "rocketclaw.read", id: 4, value: { total: 3 } }, { type: "rocketclaw.read", id: 5, error: "page not found" }]);
  expect(reads).toHaveLength(2);
  expect(reads).toEqual(expect.arrayContaining([{ space: "Billing", path: "revenue", data: true }, { space: "Billing", path: "missing", data: true }]));
  expect(await page.evaluate(() => (window as any).replies.filter((reply: object) => "value" in reply || "error" in reply))).toEqual([]);
}), 60_000);

test("a PDF page, which Chrome refuses to show in a sandboxed frame, opens in its own tab", () => withPage(["Main"], async (page, origin) => {
  await page.goto(`${origin}/pages/Billing/q3/report.pdf`);
  const link = page.getByRole("link", { name: "Open PDF", exact: true });
  expect(await link.getAttribute("href")).toBe("/pages-raw/Billing/q3/report.pdf");
  expect(await page.locator("iframe").count()).toBe(0);
  const [opened] = await Promise.all([page.waitForEvent("popup"), link.click()]);
  const isPDFViewer = (frame: { url(): string }) => frame.url().startsWith("chrome-extension://");
  if (!opened.frames().some(isPDFViewer)) await opened.waitForEvent("framenavigated", { predicate: isPDFViewer, timeout: 5_000 });
  expect(opened.url()).toBe(`${origin}/pages-raw/Billing/q3/report.pdf`);
}), 60_000);

for (const width of [1280, 320]) test(`the Page Index lists every space's pages under their space and opens them at ${width}px`, () => withPage(["Billing"], async (page, origin) => {
  const opened = async (name: string) => {
    if (width === 320) await page.getByRole("button", { name: "Show sidebar", exact: true }).click();
    await strip(page).getByRole("tab", { name, exact: true }).waitFor();
    if (width === 320) { await page.keyboard.press("Escape"); await strip(page).waitFor({ state: "detached" }); }
  };
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Pages: Index", exact: true }).click();
  await page.waitForURL(`${origin}/pages`);
  await opened("Pages");
  const links = page.locator("main").getByRole("link");
  await links.first().waitFor();
  expect(await page.locator("main").getByRole("heading", { level: 2 }).allInnerTexts()).toEqual(["Billing", "Gone", "Ops"]);
  expect(await links.allInnerTexts()).toEqual(["index.html", "q3/report.html", "index.html", "index.html"]);
  expect(await page.locator("main section").filter({ hasText: "Ops" }).getByRole("link").getAttribute("href")).toBe("/pages/Ops/index.html");
  for (const box of await links.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getBoundingClientRect().right))) expect(box).toBeLessThanOrEqual(width);
  await links.filter({ hasText: "q3/report.html" }).click();
  await page.waitForURL(`${origin}/pages/Billing/q3/report.html`);
  await opened("Billing · q3/report.html");
  expect(await page.locator("iframe").getAttribute("src")).toStartWith("/pages-raw/Billing/q3/report.html#rc=");
}, { viewport: { width, height: 800 } }), 60_000);

for (const down of [false, true]) test(`the Page Index lists every RocketClaw's pages under their space and server, each opening on its own server, ${down ? "beside a failing remote's error" : "with every remote up"}`, () => withPage(["Billing"], async (page, origin) => {
  const cors = { "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Headers": "*", "Access-Control-Allow-Methods": "*" };
  const remote = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, fetch(request) {
    const url = new URL(request.url);
    if (request.method === "OPTIONS") return new Response(null, { headers: cors });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { ...cors, "Content-Type": "text/event-stream" } });
    if (down && url.pathname === "/api/ListPages") return Response.json({ code: 14, message: "Unavailable" }, { status: 503, headers: cors });
    const replies: Record<string, unknown> = { "/api/Identity": { username: "tester" }, "/api/Tabs": { spaces: [{ id: "lab", name: "Lab" }], state: { tabs: [], groups: [], space: "lab", last: {} }, shared: [] }, "/api/ChangeTabs": { spaces: [{ id: "lab", name: "Lab" }], state: { tabs: [], groups: [], space: "lab", last: {} }, shared: [] }, "/api/ListPages": { pages: [{ space: "Lab", path: "index.html" }] } };
    return Response.json(replies[url.pathname] ?? {}, { headers: cors });
  } });
  try {
    await page.addInitScript((url: string) => document.addEventListener("readystatechange", () => {
      if (!document.getElementById("rocketclaws")) document.head.append(Object.assign(document.createElement("script"), { id: "rocketclaws", type: "application/json", textContent: JSON.stringify([{ name: "work", url }]) }));
    }), `http://127.0.0.1:${remote.port}`);
    await page.goto(`${origin}/pages`);
    const headings = page.locator("main").getByRole("heading", { level: 2 });
    await headings.filter({ hasText: "Ops · 127.0.0.1" }).waitFor();
    await (down ? page.locator("main").getByText("work: Unavailable", { exact: true }) : headings.filter({ hasText: "Lab · work" })).waitFor();
    expect(await headings.allInnerTexts()).toEqual(["Billing · 127.0.0.1", "Gone · 127.0.0.1", "Ops · 127.0.0.1", ...down ? [] : ["Lab · work"]]);
    expect(await page.locator("main section").filter({ hasText: "Ops" }).getByRole("link").getAttribute("href")).toBe("/pages/Ops/index.html");
    if (!down) expect(await page.locator("main section").filter({ hasText: "Lab · work" }).getByRole("link").getAttribute("href")).toBe("/r/work/pages/Lab/index.html");
    expect(await page.locator("main").getByText("No pages", { exact: true }).count()).toBe(0);
  } finally {
    remote.stop(true);
  }
}), 60_000);
