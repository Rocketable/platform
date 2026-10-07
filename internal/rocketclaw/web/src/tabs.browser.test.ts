import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const href = (id: string) => `/s/${btoa(id).replace(/=+$/, "")}`;

async function app(width: number, seed: Record<string, string> = {}) {
  const { chromium: engine } = await import(playwright!);
  const sessions = [
    { id: "alpha", name: "Alpha chat", preview: "Alpha preview", agent: "main", updatedAt: ago(2 * 3_600_000) },
    { id: "beta", preview: "Beta question", agent: "helper", running: true, updatedAt: ago(0) },
    { id: "gamma", preview: "Gamma notes", agent: "main", updatedAt: ago(3 * 86_400_000) },
  ];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "tabs" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "tabs-grouped" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/CreateSession") return Response.json({ id: "delta" });
    if (url.pathname === "/api/Prompt") return Response.json({ privateText: "" });
    if (url.pathname === "/api/SearchMessages" || url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  const page = await browser.newPage({ viewport: { width, height: 800 } });
  // Seed storage once; reloads must observe what the app wrote.
  await page.addInitScript((entries: Record<string, string>) => {
    if (sessionStorage.getItem("seeded")) return;
    sessionStorage.setItem("seeded", "1");
    for (const [key, value] of Object.entries(entries)) localStorage.setItem(key, value);
  }, seed);
  const nav = page.getByRole("navigation", { name: "Tabs" });
  const palette = async (mode: "sessions" | "commands", name: string | RegExp) => {
    await page.keyboard.press(mode === "sessions" ? "Control+p" : "Control+Shift+p");
    const dialog = page.getByRole("dialog", { name: mode === "sessions" ? "Go to session" : "Run command" });
    await dialog.getByRole("button", { name }).click();
    await page.locator('[role="dialog"]').waitFor({ state: "detached" });
  };
  const stored = () => page.evaluate(() => JSON.parse(localStorage.getItem("tabs-grouped:tester")!));
  return { page, nav, palette, stored, origin: `http://127.0.0.1:${server.port}`, done: async () => { await browser.close(); server.stop(true); } };
}

test("left placement groups sessions and pages with rich rows, collapse, and pins", async () => {
  const { page, nav, palette, origin, done } = await app(1280);
  try {
    await page.goto(origin + href("alpha"));
    await nav.getByRole("tab", { name: "Alpha chat" }).waitFor();
    expect(await nav.getByRole("tablist").first().getAttribute("aria-orientation")).toBe("horizontal");
    await nav.getByRole("button", { name: "Move tabs to left" }).click();
    expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("left");
    await palette("sessions", /Beta question/);
    await page.waitForURL(`**${href("beta")}`);
    await palette("commands", "Cron: Dashboard");
    await page.waitForURL("**/cron");
    const sessions = nav.getByRole("tablist", { name: "Sessions" });
    const pages = nav.getByRole("tablist", { name: "Pages" });
    expect(await sessions.getAttribute("aria-orientation")).toBe("vertical");
    expect(await sessions.getByRole("tab").count()).toBe(2);
    expect(await pages.getByRole("tab").count()).toBe(1);
    expect(await pages.getByRole("tab", { name: "Cron" }).getAttribute("aria-selected")).toBe("true");
    const alpha = sessions.getByRole("tab", { name: "Alpha chat" });
    for (const text of ["main", "2h", "Alpha preview"]) expect(await alpha.textContent()).toContain(text);
    const beta = sessions.getByRole("tab", { name: "Beta question, Turn running" });
    expect(await beta.textContent()).toContain("helper");
    expect(await beta.locator('svg[aria-label="Turn running"]').count()).toBe(1);

    const pagesHeader = nav.getByRole("button", { name: /^Pages/ });
    await pagesHeader.click();
    expect(await pagesHeader.getAttribute("aria-expanded")).toBe("false");
    expect(await pages.count()).toBe(0);
    await nav.getByRole("button", { name: "Pin Alpha chat" }).click();
    const pinned = nav.getByRole("tablist", { name: "Pinned" });
    expect(await pinned.getByRole("tab", { name: "Alpha chat" }).count()).toBe(1);
    expect(await sessions.getByRole("tab").count()).toBe(1);

    await page.reload();
    await pinned.getByRole("tab", { name: "Alpha chat" }).waitFor();
    expect(await pinned.getAttribute("aria-orientation")).toBe("vertical");
    expect(await sessions.getByRole("tab").count()).toBe(1);
    expect(await pages.count()).toBe(0);
    expect(await pagesHeader.getAttribute("aria-expanded")).toBe("false");

    await pinned.getByRole("tab", { name: "Alpha chat" }).focus();
    await page.keyboard.press("ArrowDown");
    expect(await beta.evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
    expect(page.url()).toEndWith("/cron");
    await page.keyboard.press("Enter");
    await page.waitForURL(`**${href("beta")}`);
    expect(await nav.getByRole("button", { name: "Close Beta question" }).count()).toBe(1);
  } finally {
    await done();
  }
}, 30_000);

test("top placement numbers, Cmd/Ctrl+Alt+digit, and palette switches", async () => {
  const { page, nav, palette, origin, stored, done } = await app(1280);
  try {
    await page.goto(`${origin}/`);
    await nav.getByRole("tab", { name: "New session" }).waitFor();
    await palette("sessions", /Alpha chat/);
    await palette("sessions", /Beta question/);
    await page.waitForURL(`**${href("beta")}`);
    expect(await nav.locator('[data-slot="tab-number"]').count()).toBe(0);
    await nav.getByRole("button", { name: "Show tab numbers" }).click();
    expect(await nav.locator('[data-slot="tab-number"]').allTextContents()).toEqual(["1", "2", "3"]);
    expect(await nav.getByRole("tab").nth(1).getAttribute("aria-label")).toBe("Beta question, Turn running");
    await page.keyboard.press("Control+Alt+1");
    await page.waitForURL(`**${href("alpha")}`);
    await page.keyboard.press("Control+Alt+3");
    await page.waitForURL(`${origin}/`);
    await page.keyboard.press("Control+Alt+2");
    await page.waitForURL(`**${href("beta")}`);
    expect((await stored()).indicators).toBe("numbers");

    await palette("commands", "Tabs: Show status indicators");
    expect(await nav.locator('[data-slot="tab-number"]').count()).toBe(0);
    await palette("commands", "Tabs: Pin tab");
    expect(await nav.getByRole("tablist", { name: "Pinned" }).getByRole("tab").getAttribute("aria-label")).toBe("Beta question, Turn running");
    await palette("commands", "Tabs: Move to left");
    expect(await nav.getByRole("tablist", { name: "Pinned" }).getAttribute("aria-orientation")).toBe("vertical");
    await page.keyboard.press("Control+Alt+2");
    await page.waitForURL(`**${href("alpha")}`);
    await nav.getByRole("button", { name: /^Sessions/ }).click();
    await page.keyboard.press("Control+Alt+2");
    await page.waitForURL(`${origin}/`);
    expect(await stored()).toEqual({ tabs: ["/", href("alpha"), href("beta")], active: "/", pinned: [href("beta")], collapsed: ["Sessions"], indicators: "status" });
  } finally {
    await done();
  }
}, 30_000);

test("closing tabs activates a neighbour, pages close their tab, and the composer stays", async () => {
  const { page, nav, palette, origin, stored, done } = await app(1280);
  try {
    await page.goto(origin + href("alpha"));
    await nav.getByRole("tab", { name: "Alpha chat" }).waitFor();
    await palette("sessions", /Beta question/);
    await palette("sessions", /Gamma notes/);
    await page.waitForURL(`**${href("gamma")}`);
    await nav.getByRole("tab", { name: /^Beta question/ }).click();
    await page.waitForURL(`**${href("beta")}`);
    await nav.getByRole("button", { name: "Close Beta question" }).click();
    await page.waitForURL(`**${href("gamma")}`);
    await nav.getByRole("button", { name: "Close Gamma notes" }).click();
    await page.waitForURL(`**${href("alpha")}`);
    await nav.getByRole("button", { name: "Close Alpha chat" }).click();
    await page.waitForURL(`${origin}/`);
    expect(await nav.getByRole("tab").count()).toBe(1);
    expect(await nav.getByRole("tab", { name: "New session" }).getAttribute("aria-selected")).toBe("true");
    expect(await nav.getByRole("button", { name: "Close New session" }).count()).toBe(0);

    await palette("commands", "Cron: Dashboard");
    await page.waitForURL("**/cron");
    await page.locator("main").getByRole("button", { name: "Close", exact: true }).click();
    await page.waitForURL(`${origin}/`);
    await palette("commands", "List Agents");
    await nav.getByRole("tab", { name: "Agents", selected: true }).waitFor();
    await page.keyboard.press("Escape");
    await page.waitForURL(`${origin}/`);
    expect(await nav.getByRole("tab").count()).toBe(1);

    await page.locator("textarea").fill("hello");
    await page.locator("textarea").press("Enter");
    await page.waitForURL(`**${href("delta")}`);
    expect(await nav.getByRole("tab").count()).toBe(1);
    expect(await nav.getByRole("tab", { name: "delta" }).getAttribute("aria-selected")).toBe("true");
    expect(await stored()).toMatchObject({ tabs: [href("delta")], active: href("delta") });
  } finally {
    await done();
  }
}, 30_000);

test("left placement keeps hidden tabs out of close neighbours and the Tab order", async () => {
  const saved = { tabs: [href("alpha"), href("beta"), "/agents"], active: href("beta"), pinned: [href("beta")], collapsed: ["Sessions"], indicators: "status" };
  const { page, nav, origin, done } = await app(1280, { "tab-placement": "left", "tabs-grouped:tester": JSON.stringify(saved) });
  try {
    await page.goto(origin + href("beta"));
    await nav.getByRole("button", { name: "Close Beta question" }).click();
    await page.waitForURL("**/agents");
    await nav.getByRole("button", { name: /^Pages/ }).click();
    await nav.getByRole("button", { name: /^Sessions/ }).click();
    await page.keyboard.press("Tab");
    expect(await nav.getByRole("tab", { name: "Alpha chat" }).evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
  } finally {
    await done();
  }
}, 30_000);

test("reload restores tabs and the active tab, a deep link adds a tab, and bad storage resets", async () => {
  const saved = { tabs: [href("alpha"), "/cron"], active: "/cron", pinned: [], collapsed: [], indicators: "status" };
  const restore = await app(1280, { "tabs-grouped:tester": JSON.stringify(saved) });
  try {
    await restore.page.goto(`${restore.origin}/cron`);
    await restore.nav.getByRole("tab", { name: "Cron", selected: true }).waitFor();
    expect(await restore.nav.getByRole("tab").count()).toBe(2);
    await restore.page.reload();
    await restore.nav.getByRole("tab", { name: "Cron", selected: true }).waitFor();
    expect(await restore.nav.getByRole("tab").count()).toBe(2);
    await restore.page.goto(restore.origin + href("beta"));
    await restore.nav.getByRole("tab", { name: /^Beta question/ }).waitFor();
    expect(await restore.nav.getByRole("tab", { name: /^Beta question/ }).getAttribute("aria-selected")).toBe("true");
    expect(await restore.stored()).toEqual({ ...saved, tabs: [...saved.tabs, href("beta")], active: href("beta") });
  } finally {
    await restore.done();
  }
  for (const value of ["{bad", '{"tabs":"x"}', '{"tabs":["/nowhere"],"active":"/nowhere","pinned":[],"collapsed":[],"indicators":"status"}']) {
    const { page, nav, origin, stored, done } = await app(1280, { "tabs-grouped:tester": value });
    try {
      await page.goto(origin + href("alpha"));
      await nav.getByRole("tab", { name: "Alpha chat" }).waitFor();
      expect(await nav.getByRole("tab").count()).toBe(1);
      await page.waitForFunction(() => localStorage.getItem("tabs-grouped:tester")?.startsWith('{"tabs":["/s/'));
      expect((await stored()).tabs).toEqual([href("alpha")]);
    } finally {
      await done();
    }
  }
}, 30_000);

test("narrow screens use a scrolling top strip even when left is stored", async () => {
  const saved = { tabs: [href("alpha"), href("beta"), href("gamma"), "/search", "/cron", "/agents"], active: href("alpha"), pinned: [], collapsed: [], indicators: "status" };
  const { page, nav, origin, done } = await app(375, { "tab-placement": "left", "tabs-grouped:tester": JSON.stringify(saved) });
  try {
    await page.goto(origin + href("alpha"));
    await nav.getByRole("tab", { name: "Alpha chat" }).waitFor();
    expect(await nav.getByRole("tablist", { name: "Sessions" }).getAttribute("aria-orientation")).toBe("horizontal");
    expect(await nav.getByRole("button", { name: /^Pages/ }).count()).toBe(0);
    expect(await nav.evaluate((node: HTMLElement) => {
      const strip = node.firstElementChild as HTMLElement;
      return [getComputedStyle(strip).overflowX, strip.scrollWidth > strip.clientWidth, document.documentElement.scrollWidth <= innerWidth];
    })).toEqual(["auto", true, true]);
    expect(await nav.getByRole("button", { name: "Close Alpha chat" }).isVisible()).toBe(true);
    await nav.getByRole("tab", { name: "Agents" }).click();
    await page.waitForURL("**/agents");
    expect(await nav.getByRole("tab", { name: "Agents" }).isVisible()).toBe(true);
  } finally {
    await done();
  }
}, 30_000);
