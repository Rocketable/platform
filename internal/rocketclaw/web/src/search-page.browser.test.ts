import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const width of [1280, 390]) test(`saved search tabs at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  let owner = "alice";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: owner });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-page" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchMessages" || url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 800 } });
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.waitForURL("**/search");
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.waitFor();
    expect(await page.getByRole("tab").count()).toBe(1);
    expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key.startsWith("search-tabs:")))).toEqual([]);
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await page.getByRole("button", { name: "Search sessions" }).click();
    expect(await page.getByRole("tab").count()).toBe(1);
    await search.fill("same");
    await page.getByRole("button", { name: "New search" }).click();
    await search.fill("same");
    const saved = await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:alice")!));
    expect(saved.tabs).toHaveLength(2);
    expect(saved.tabs[0].id).not.toBe(saved.tabs[1].id);
    await page.reload();
    await search.waitFor();
    expect(await page.getByRole("tab").last().getAttribute("aria-selected")).toBe("true");
    expect(await search.inputValue()).toBe("same");
    await page.getByRole("tab").first().click();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    await page.getByRole("tab").first().click();
    const name = page.getByRole("textbox", { name: "Search name", exact: true });
    await name.waitFor();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    expect(await name.evaluate((node: HTMLElement) => !!node.closest('[role="tablist"]'))).toBe(true);
    expect(await name.textContent()).toBe("same");
    expect(await name.getAttribute("contenteditable")).toBe("plaintext-only");
    await page.waitForFunction((node: HTMLElement) => document.activeElement === node, await name.elementHandle());
    expect(await name.evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    await name.fill("Discarded name");
    await name.press("Escape");
    expect(await page.getByRole("tab").first().textContent()).toBe("same");
    await page.getByRole("tab").first().click();
    await name.fill("");
    await name.press("Enter");
    expect(await page.getByRole("tab").first().textContent()).toBe("same");
    await page.getByRole("tab").first().click();
    expect(await name.textContent()).toBe("same");
    await name.pressSequentially("Saved query");
    expect(await name.textContent()).toBe("Saved query");
    await name.press("Enter");
    await page.getByRole("tab", { name: "Saved query", exact: true }).waitFor();
    await page.waitForFunction(() => document.activeElement === document.querySelector('[role="tab"][aria-selected="true"]'));
    expect(await page.getByRole("tab").first().evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    expect(await search.inputValue()).toBe("same");
    await page.getByRole("tab").last().click();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    await page.getByRole("tab", { name: "Saved query", exact: true }).click();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    await page.getByRole("tab", { name: "Saved query", exact: true }).click();
    await name.fill("  Saved query  ");
    await name.press("Enter");
    expect(await page.getByRole("tab").first().textContent()).toBe("Saved query");
    await page.getByRole("tab", { name: "Saved query", exact: true }).click();
    await name.fill("Blur saved");
    await page.getByRole("tab").last().click();
    expect(await page.getByRole("tab").first().textContent()).toBe("Blur saved");
    expect(await page.getByRole("tab").last().getAttribute("aria-selected")).toBe("true");
    await page.getByRole("tab", { name: "Blur saved", exact: true }).click();
    await page.getByRole("tab", { name: "Blur saved", exact: true }).click();
    await name.fill("Saved query");
    await name.press("Enter");
    await search.fill("edited");
    await search.fill('is:forked tag:"Needs review" tag:customer agent:ma');
    await page.getByRole("button", { name: "main" }).click();
    const pills = page.getByRole("button", { name: /^(?:is|tag):/ });
    expect(await search.inputValue()).toBe("");
    expect(await pills.allTextContents()).toEqual(["is:forked", 'tag:"Needs review"', "tag:customer"]);
    await page.reload();
    await search.waitFor();
    expect(await search.inputValue()).toBe("");
    expect(await pills.allTextContents()).toEqual(["is:forked", 'tag:"Needs review"', "tag:customer"]);
    while (await pills.count()) await pills.first().click();
    await search.fill("edited");
    expect(await page.getByRole("tab").first().textContent()).toBe("Saved query");
    expect(await page.getByRole("button", { name: "agent:main" }).count()).toBe(1);
    await page.getByRole("tab", { name: "Saved query", exact: true }).click();
    expect(await name.textContent()).toBe("Saved query");
    await name.fill("Discard again");
    await page.keyboard.press("Escape");
    expect(await page.getByRole("tab").first().textContent()).toBe("Saved query");
    await page.getByRole("tab", { name: "Saved query", exact: true }).click();
    await name.fill("");
    await search.click();
    await page.getByRole("tab", { name: "edited", exact: true }).waitFor();
    expect(await search.inputValue()).toBe("edited");
    await page.getByRole("tab").last().focus();
    await page.keyboard.press("ArrowLeft");
    expect(await page.getByRole("tab").first().getAttribute("aria-selected")).toBe("true");
    await page.getByRole("button", { name: "Close search 1" }).click();
    expect(await page.getByRole("tab").count()).toBe(1);
    expect(await page.getByRole("tab").first().evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    expect(await search.inputValue()).toBe("same");
    expect(await page.getByRole("button", { name: "agent:main" }).count()).toBe(0);
    await search.fill(""); // An owner switch imports the URL search; an empty one keeps Bob's tab a draft.
    await page.waitForURL("**/search");
    await page.getByRole("tab").first().click();
    await name.fill("Alice");
    await name.press("Enter");
    await page.evaluate(() => localStorage.setItem("last-seen:alice", "/s/b25l?message=private"));
    owner = "bob";
    await page.getByRole("tab", { name: "Search 1" }).waitFor({ timeout: 10_000 });
    expect(await search.inputValue()).toBe("");
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:bob"))).toBeNull();
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:alice")!).tabs)).toHaveLength(1);
    await search.press("Enter");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:bob")!).tabs)).toHaveLength(1);
    await page.getByRole("button", { name: "Close search 1" }).click();
    await page.waitForURL(`http://127.0.0.1:${server.port}/`);
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:bob"))).toBeNull();
    await page.keyboard.press("Control+p");
    const dialog = page.getByRole("dialog", { name: "Go to session" });
    await dialog.getByPlaceholder("Search sessions").fill("temporary");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Control+p");
    expect(await dialog.getByPlaceholder("Search sessions").inputValue()).toBe("");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.waitForURL("**/search");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("search URL follows the active tab and reopens a matching tab", async () => {
  const { chromium: engine } = await import(playwright!);
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", name: "Outage chat", preview: "", agent: "alitu-cs-support" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-links" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "alitu-cs-support" }] });
    if (url.pathname === "/api/SearchMessages" || url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const link = "/search?q=outage&agent=alitu-cs-support";
    const at = (target: string) => page.waitForURL((url: URL) => url.pathname + url.search === target);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const tabs = page.getByRole("tab");
    const opened = async (count: number) => {
      await page.getByRole("button", { name: "agent:alitu-cs-support" }).waitFor();
      expect(await tabs.count()).toBe(count);
      expect(await tabs.first().getAttribute("aria-selected")).toBe("true");
      expect(await search.inputValue()).toBe("outage");
    };
    await page.goto(`http://127.0.0.1:${server.port}${link}`);
    await opened(1);
    await page.reload();
    await opened(1);
    await page.getByRole("button", { name: "New search" }).click();
    await at("/search");
    await search.fill("other room");
    await at("/search?q=other+room");
    await page.goto(`http://127.0.0.1:${server.port}${link}`);
    await opened(2);
    await page.getByLabel("Search results").getByRole("heading").getByRole("link", { name: "Outage chat" }).click();
    await page.waitForURL("**/s/Y2hhdA");
    await page.goBack();
    await at(link);
    await opened(2);
    const entries = await page.evaluate(() => history.length);
    await tabs.last().click();
    await at("/search?q=other+room");
    await tabs.first().click();
    await at(link);
    await page.getByRole("button", { name: "Close search 1" }).click();
    await at("/search?q=other+room");
    await page.getByRole("button", { name: "New search" }).click();
    await at("/search");
    expect(await page.evaluate(() => history.length)).toBe(entries);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

for (const width of [1280, 390]) test(`search editor groups clickable message matches at ${width}px, and Enter reloads`, async () => {
  const { chromium: engine } = await import(playwright!);
  const searches: string[] = [];
  const originSearches: string[] = [];
  let summariesComplete = true;
  const rows = [
    { id: "first", name: "", title: "", preview: "Ordinary", agent: "main", cron: true, cronName: "daily", updatedAt: "2026-01-03T00:00:00Z" },
    { id: "second", name: "", title: "#notes", preview: "Needle notes\nOther text", agent: "main", updatedAt: "2026-01-02T00:00:00Z" },
    { id: "third", name: "Pinned conversation", preview: "Other text", agent: "main", pinned: true, tags: ["customer", "Needs review", 'say "hello"'], updatedAt: "2026-01-01T00:00:00Z" },
    { id: "fourth", name: "Origin chat", preview: "Other text", agent: "main", pinned: true, cron: true, cronName: "Weekly report" },
    { id: "web-session:unnamed", name: "", title: "", preview: "", agent: "main" },
  ];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-results" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchOrigins") {
      const { query } = await request.json() as { query: string };
      originSearches.push(query);
      const text = "cron source: /notes/archive-key.md stem: review";
      return Response.json({ matches: text.includes(query) ? [{ conversationId: "fourth", text }] : [] });
    }
    if (url.pathname === "/api/SearchMessages") {
      const { query } = await request.json() as { query: string };
      searches.push(query);
      return Response.json({ matches: query === "needle" ? [{ conversationId: "third", message: { messageId: "1:0", role: "user", text: "Context before NEEDLE then needle and after", complete: true } }, { conversationId: "third", message: { messageId: "1:1", role: "assistant", text: "A second needle response", complete: true } }, { conversationId: "first", message: { messageId: "2:0", role: "assistant", text: "Needle in excluded chat", complete: true } }] : [] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 700 } });
    await page.goto(`http://127.0.0.1:${server.port}/`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const results = page.getByLabel("Search results");
    const group = results.getByRole("group", { name: "Pinned conversation", exact: true });
    if (width < 768) await page.getByRole("button", { name: "Sessions", exact: true }).click();
    const sidebar = width < 768 ? page.getByRole("dialog", { name: "Sessions", exact: true }) : page.locator("#session-sidebar");
    await sidebar.getByRole("link", { name: /Needle notes/ }).waitFor();
    expect(await sidebar.getByRole("link", { name: /Ordinary|Origin chat/ }).count()).toBe(0);
    if (width < 768) { await page.keyboard.press("Escape"); await sidebar.waitFor({ state: "detached" }); }
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session" });
    await palette.getByRole("button").filter({ hasText: "Ordinary" }).waitFor();
    await palette.getByPlaceholder("Search sessions").fill("is:cr");
    await palette.getByRole("button", { name: "cron", exact: true }).click();
    expect(await palette.locator('[data-slot="session-title"]').allTextContents()).toEqual(["Origin chat", "Ordinary"]);
    await palette.getByPlaceholder("Search sessions").fill("cron:DAILY");
    await palette.getByRole("button", { name: "daily", exact: true }).click();
    expect(await palette.locator('[data-slot="session-title"]').allTextContents()).toEqual(["Ordinary"]);
    await palette.getByRole("button", { name: "cron:daily", exact: true }).click();
    await palette.getByPlaceholder("Search sessions").fill("cron:Week");
    await palette.getByRole("button", { name: "Weekly report", exact: true }).click();
    expect(await palette.locator('[data-slot="session-title"]').allTextContents()).toEqual(["Origin chat"]);
    await page.keyboard.press("Escape");
    await palette.waitFor({ state: "detached" });
    await page.getByRole("button", { name: "Search sessions", exact: true }).click();
    await search.fill("is:cr");
    await page.getByRole("button", { name: "cron", exact: true }).click();
    await results.getByRole("group", { name: "Origin chat", exact: true }).waitFor();
    expect(await results.getByRole("group").allTextContents()).toHaveLength(2);
    await search.fill("cron:DAILY");
    await page.getByRole("button", { name: "daily", exact: true }).click();
    await results.getByRole("group", { name: "Ordinary", exact: true }).waitFor();
    expect(await results.getByRole("group").count()).toBe(1);
    await page.getByRole("button", { name: "is:cron", exact: true }).click();
    expect(await results.getByRole("group").count()).toBe(1);
    await page.getByRole("button", { name: "cron:daily", exact: true }).click();
    for (const query of ["tag:customer", 'tag:"Needs review"', 'tag:"say \\"hello\\""', "tag:customer tag:customer is:pinned"]) {
      await search.fill(query);
      await group.waitFor();
      expect(await results.getByRole("group").count()).toBe(1);
    }
    rows[2].tags = ["internal"];
    await results.getByText("No matches").waitFor();
    rows[2].tags = ["customer", "Needs review", 'say "hello"'];
    await group.waitFor();
    for (const query of ["tag:Customer", "tag:unknown", "tag:customer tag:internal"]) {
      await search.fill(query);
      await results.getByText("No matches").waitFor();
      expect(await results.getByRole("group").count()).toBe(0);
    }
    summariesComplete = false;
    await search.fill("tag:unknown");
    await results.getByText("Session search is still loading.").waitFor();
    expect(await results.getByText("No matches").count()).toBe(0);
    summariesComplete = true;
    await results.getByText("No matches").waitFor();
    await page.waitForTimeout(300);
    expect(searches).toEqual([]);
    expect(originSearches).toEqual([]); // Status-only queries search neither messages nor origins.
    await search.fill("tag:customer is:pinned needle");
    await group.waitFor({ timeout: 5000 });
    expect(await results.getByRole("group").count()).toBe(1);
    expect(await group.getByRole("heading").textContent()).toBe("Pinned conversation(2)");
    expect(await group.locator('a[href*="?message="]').count()).toBe(2);
    const hit = group.getByRole("link", { name: /Context before NEEDLE/ });
    expect(await hit.locator("mark").allTextContents()).toEqual(["NEEDLE", "needle"]);
    expect(await hit.getAttribute("href")).toBe("/s/dGhpcmQ?message=1%3A0");
    const bounds = await hit.boundingBox();
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    expect(bounds!.y + bounds!.height).toBeLessThan(700);
    expect(await results.getByText("Chat", { exact: true }).count()).toBe(0);
    expect(searches).toEqual(["needle"]);
    const pills = page.getByRole("button", { name: /^(?:is|tag):/ });
    expect(await pills.allTextContents()).toEqual(["tag:customer", "is:pinned"]);
    expect(await search.inputValue()).toBe("needle");
    while (await pills.count()) await pills.first().click();
    await search.fill("needle");
    await results.getByRole("link", { name: /Needle notes/ }).last().waitFor();
    await results.getByRole("link", { name: /Needle in excluded chat/ }).waitFor();
    expect(await results.getByRole("group").count()).toBe(3);
    expect(await results.getByRole("group").first().getAttribute("aria-label")).toBe("Pinned conversation");
    expect(await results.getByRole("group", { name: "Ordinary", exact: true }).getByRole("heading").textContent()).toContain("Ordinary");
    expect(await results.getByRole("link", { name: /Needle notes/ }).last().locator("mark").textContent()).toBe("Needle");
    await page.waitForTimeout(300);
    expect(searches).toEqual(["needle"]);
    await search.fill("is:cron cron:daily needle");
    await results.getByRole("group", { name: "Ordinary", exact: true }).waitFor();
    expect(await results.getByRole("group").count()).toBe(1);
    expect(await results.getByRole("link", { name: /Needle in excluded chat/ }).count()).toBe(1);
    await page.getByRole("button", { name: "cron:daily", exact: true }).click();
    await page.getByRole("button", { name: "is:cron", exact: true }).click();
    await group.waitFor();
    await search.press("Enter");
    await page.waitForTimeout(100);
    expect(searches).toEqual(["needle", "needle"]);
    expect(await results.getByRole("button", { name: /Refresh/ }).count()).toBe(0);
    await search.fill("archive-key");
    await results.getByRole("link", { name: /Origin chat/ }).waitFor();
    expect(await results.getByRole("link", { name: /archive-key/ }).locator("mark").textContent()).toBe("archive-key");
    await search.fill("no-such-match");
    await results.getByText("No matches").waitFor();
    await search.fill("");
    await results.getByText("Type to search messages and conversations.").waitFor();
    expect(await results.getByRole("link").count()).toBe(0);
    expect(searches).toEqual(["needle", "needle", "archive-key", "no-such-match"]);
    const order = (labels: string[]) => page.waitForFunction((labels: string[]) => [...document.querySelectorAll('[aria-label="Search results"] [role="group"]')].map((node) => node.getAttribute("aria-label")).join("|") === labels.join("|"), labels, { timeout: 5000 });
    await search.fill("sort:oldest needle");
    await order(["Pinned conversation", "Needle notes", "Ordinary"]); // sort: overrides message-hit grouping.
    await search.fill("sort:newest needle");
    await order(["Ordinary", "Needle notes", "Pinned conversation"]);
    for (const term of ["sort:oldest", "sort:newest"]) await page.getByRole("button", { name: term, exact: true }).click();
    await search.fill("");
    await search.fill("tag:needs");
    await page.getByRole("button", { name: "Needs review", exact: true }).click();
    expect(await pills.allTextContents()).toEqual(['tag:"Needs review"']);
    expect(await search.inputValue()).toBe("");
    await group.waitFor();
    await search.pressSequentially("is:pinned ");
    expect(await pills.allTextContents()).toEqual(['tag:"Needs review"', "is:pinned"]);
    while (await pills.count()) await pills.first().click();
    await search.fill("is:pinned");
    await results.getByRole("heading").getByRole("link", { name: "Pinned conversation", exact: true }).click();
    await page.waitForURL("**/s/dGhpcmQ");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:tester")!).tabs[0].query)).toBe("is:pinned");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("newer search replaces an in-flight message search and retries errors", async () => {
  const { chromium: engine } = await import(playwright!);
  const started = Promise.withResolvers<void>();
  const delayed = Promise.withResolvers<void>();
  const searches: string[] = [];
  let fail = true;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", name: "A chat", preview: "" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-race" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [] });
    if (url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname === "/api/SearchMessages") {
      const { query } = await request.json() as { query: string };
      searches.push(query);
      if (query === "old") { started.resolve(); await delayed.promise; }
      if (query === "new" && fail) return Response.json({ message: "Try again", code: 13 }, { status: 500 });
      return Response.json({ matches: [{ conversationId: "chat", message: { messageId: "1:0", role: "user", text: `Match ${query}`, complete: true } }] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.addInitScript(() => {
      const original = window.fetch;
      const abortedSearches: string[] = [];
      Object.assign(window, { abortedSearches });
      Object.defineProperty(window, "fetch", { value: (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).includes("/api/SearchMessages")) init?.signal?.addEventListener("abort", () => abortedSearches.push(JSON.parse(init.body as string).query));
        return original(input, init);
      } });
    });
    await page.goto(`http://127.0.0.1:${server.port}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("old");
    await started.promise;
    expect(await page.getByRole("status").filter({ hasText: "Searching…" }).count()).toBe(1);
    await search.fill("new");
    await search.press("Enter");
    await page.getByRole("alert").getByRole("button", { name: "Retry" }).waitFor();
    expect(await page.evaluate(() => Reflect.get(window, "abortedSearches") as string[])).toContain("old");
    expect(await page.getByLabel("Search results").getByRole("link").count()).toBe(0);
    delayed.resolve();
    fail = false;
    await page.getByRole("button", { name: "Retry" }).click();
    await page.getByRole("link", { name: /Match new/ }).waitFor();
    expect(searches).toEqual(["old", "new", "new"]);
    expect(await page.getByLabel("Search results").getByText("Match old").count()).toBe(0);
    for (let i = 1; i <= 7; i++) {
      await search.fill("x".repeat(i));
      await page.waitForTimeout(180);
    }
    expect(searches.slice(3).some((query) => query.length < 7)).toBe(true);
    await page.waitForTimeout(300);
    expect(searches.at(-1)).toBe("xxxxxxx");
  } finally {
    delayed.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("one SearchOrigins request per needle feeds Cmd+P and /search", async () => {
  const { chromium: engine } = await import(playwright!);
  const rows = Array.from({ length: 30 }, (_, i) => ({ id: `row-${String(i).padStart(2, "0")}`, name: `Row ${i}`, preview: "", agent: "main" }));
  const originSearches: string[] = [];
  let failing = true;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-origins" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchMessages") return Response.json({ matches: [] });
    if (url.pathname === "/api/SearchOrigins") {
      const { query } = await request.json() as { query: string };
      originSearches.push(query);
      if (failing) return Response.json({ message: "origin unavailable", code: 13 }, { status: 500 });
      const text = "external mcp external conversation: deep-origin agent: main ";
      return Response.json({ matches: text.includes(query) ? [{ conversationId: "row-29", text }] : [] });
    }
    if (url.pathname === "/api/History") return Response.json({ message: "origin search must not read histories", code: 13 }, { status: 500 });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.port}/search`);
    await page.locator("#session-sidebar").getByText("Row 29", { exact: true }).waitFor();
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const results = page.getByLabel("Search results");
    await search.fill("deep-origin");
    await results.getByRole("alert").filter({ hasText: "Some chat origins could not be searched." }).waitFor();
    expect(await results.getByRole("status").filter({ hasText: "Searching…" }).count()).toBe(0);
    failing = false;
    await search.fill("deep");
    await results.getByRole("group", { name: "Row 29", exact: true }).waitFor({ timeout: 5000 });
    await results.getByRole("alert").waitFor({ state: "detached" });
    expect(await results.getByRole("group").count()).toBe(1);
    await search.pressSequentially("-ori", { delay: 30 }); // Typing settles into one request.
    await page.waitForTimeout(400);
    expect(originSearches).toEqual(["deep-origin", "deep", "deep-ori"]);
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session", exact: true });
    await palette.getByPlaceholder("Search sessions").fill("DEEP-ORIGIN");
    await palette.getByText("Row 29", { exact: true }).waitFor({ timeout: 5000 });
    expect(await palette.locator("li > button").count()).toBe(1);
    expect(originSearches.slice(3)).toEqual(["deep-origin"]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("search shows message and row matches while origins are still loading", async () => {
  const { chromium: engine } = await import(playwright!);
  const hold = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "named", name: "Needle row", preview: "" }, { id: "messaged", name: "Plain chat", preview: "" }, { id: "origin", name: "Origin only", preview: "" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-partial" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [] });
    if (url.pathname === "/api/SearchMessages") {
      const { query } = await request.json() as { query: string };
      return Response.json({ matches: query === "needle" ? [{ conversationId: "messaged", message: { messageId: "1:0", role: "user", text: "A needle message", complete: true } }] : [] });
    }
    if (url.pathname === "/api/SearchOrigins") {
      const { query } = await request.json() as { query: string };
      await hold.promise;
      const text = "external mcp external conversation: needle-origin agent: main ";
      return Response.json({ matches: text.includes(query) ? [{ conversationId: "origin", text }] : [] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.port}/search`);
    await page.locator("#session-sidebar").getByText("Origin only", { exact: true }).waitFor();
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const results = page.getByLabel("Search results");
    const checking = results.getByRole("status").filter({ hasText: "Still checking chat origins…" });
    await search.fill("needle");
    await results.getByRole("group", { name: "Needle row", exact: true }).waitFor({ timeout: 5000 });
    await results.getByRole("link", { name: /A needle message/ }).waitFor();
    await checking.waitFor();
    expect(await results.getByRole("group", { name: "Origin only", exact: true }).count()).toBe(0);
    await search.fill("absent");
    await results.getByText("Session search is still loading.").waitFor();
    expect(await results.getByText("No matches").count()).toBe(0);
    await search.fill("needle");
    await results.getByRole("group", { name: "Needle row", exact: true }).waitFor();
    hold.resolve();
    await results.getByRole("group", { name: "Origin only", exact: true }).waitFor({ timeout: 5000 });
    await checking.waitFor({ state: "detached" });
    expect(await results.getByRole("group").count()).toBe(3);
  } finally {
    hold.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("message matches jump after history loads and last close returns to the last visible message", async () => {
  const { chromium: engine } = await import(playwright!);
  const messages = (prefix: string) => Array.from({ length: 18 }, (_, i) => [
    { role: "user", messageId: `${prefix}-u${i}`, entryKey: `${prefix}-t${i}`, itemId: `${prefix}-item-u${i}`, inputId: `${prefix}-input${i}`, turnId: `${prefix}-t${i}`, text: `${prefix} user ${i}`, complete: true, origin: prefix === "one" && i === 1 ? "sandboxed" : "canonical" },
    { role: "assistant", messageId: `${prefix}-a${i}`, entryKey: `${prefix}-t${i}`, itemId: `${prefix}-item-a${i}`, inputId: "", turnId: `${prefix}-t${i}`, text: `${prefix} assistant ${i}`, complete: true, origin: prefix === "one" && i === 1 ? "sandboxed" : "canonical" },
  ]).flat();
  const pending = Promise.withResolvers<void>();
  let delayHistory = true;
  let owner = "tester";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "one", name: "First chat" }, { id: "two", name: "Second chat" }], owner, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: owner });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "jump" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname === "/api/SearchMessages") return Response.json({ matches: [
      { conversationId: "one", message: messages("one")[2] },
      { conversationId: "one", message: messages("one")[3] },
    ] });
    if (url.pathname === "/api/History") {
      const { id } = await request.json() as { id: string };
      if (delayHistory && id === "one") await pending.promise;
      return Response.json({ messages: messages(id), origin: "", revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages(id).map((message) => message.entryKey))], running: false, terminal: "" });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 650 } });
    const root = `http://127.0.0.1:${server.port}`;
    await page.goto(`${root}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("one");
    await page.getByRole("link", { name: /one user 1/ }).click();
    await page.waitForURL("**/s/b25l?message=one-u1");
    expect(await page.locator('[data-message-id="one-u1"]').count()).toBe(0);
    delayHistory = false;
    pending.resolve();
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")!.getBoundingClientRect();
      const target = [...document.querySelectorAll('[data-message-id="one-u1"]')].find((node) => node.getBoundingClientRect().bottom > viewport.top && node.getBoundingClientRect().top < viewport.bottom);
      return !!target;
    });
    await page.reload();
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="one-u1"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    });
    await page.getByRole("button", { name: "sandboxed" }).click();
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.getByRole("link", { name: /one user 1/ }).click();
    await page.waitForFunction(() => document.querySelector('[data-message-id="one-u1"]')?.getBoundingClientRect().height !== 0);
    expect(await page.getByRole("button", { name: "sandboxed" }).getAttribute("aria-pressed")).toBe("true");
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.getByRole("link", { name: /one assistant 1/ }).click();
    await page.waitForURL("**/s/b25l?message=one-a1");
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="one-a1"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    });
    await page.evaluate(() => {
      history.pushState(null, "", "/s/b25l?message=one-u14");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="one-u14"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    }, undefined, { timeout: 5000 });
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:tester")!).tabs)).toHaveLength(1);
    await page.evaluate(() => {
      history.pushState(null, "", "/s/dHdv");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await page.getByText("two assistant 17").waitFor();
    await page.locator("#transcript-scroll").hover();
    await page.mouse.wheel(0, -10000);
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")!;
      const bounds = viewport.getBoundingClientRect();
      const visible = [...viewport.querySelectorAll<HTMLElement>('[data-slot="message"][data-message-id]')].filter((node) => {
        const rect = node.getBoundingClientRect();
        return rect.bottom > bounds.top && rect.top < bounds.bottom;
      });
      return viewport.scrollTop === 0 && localStorage.getItem("last-seen:tester") === `/s/dHdv?message=${visible.at(-1)?.dataset.messageId}`;
    });
    const lastSeen = await page.evaluate(() => localStorage.getItem("last-seen:tester"));
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.getByRole("button", { name: "Close search 1" }).click();
    await page.waitForURL(root + lastSeen);
    expect(await page.getByRole("textbox", { name: "Search sessions" }).count()).toBe(0);
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:tester"))).toBeNull();
    owner = "bob";
    await page.waitForResponse(async (response: { url: () => string; json: () => Promise<{ username: string }> }) => response.url().endsWith("/api/Identity") && (await response.json()).username === "bob");
    await page.waitForTimeout(150);
    expect(await page.evaluate(() => localStorage.getItem("last-seen:bob"))).toBeNull();
  } finally {
    pending.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("search formats rows, windows around matches, and resolves tag names", async () => {
  const { chromium: engine } = await import(playwright!);
  const preview = "<!subteam^S0BA868QQ90> *Allen now says he would buy a Windows computer if memory is the cause.*\n\nsee [docs](https://example.com/a)\n```\ncode line\n```";
  const long = ["alpha", "bravo", "charlie", "delta", "echo", "the windows line", "foxtrot", "golf", "hotel", "india"].join("\n");
  const tagged = ["keep-out-before", "pad-a", "pad-b", "<!subteam^S0BA868QQ90> tagged here", "pad-c", "pad-d", "keep-out-after"].join("\n");
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "formatted", name: "", preview, tags: ["x"], agent: "main" }, { id: "long", name: "Long chat", preview: "", agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-format" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchOrigins") return Response.json({ matches: [] });
    if (url.pathname === "/api/SlackNames") return Response.json({ names: { S0BA868QQ90: "cs-operators" } });
    if (url.pathname === "/api/SearchMessages") {
      const { query } = await request.json() as { query: string };
      if (query === "windows") return Response.json({ matches: [{ conversationId: "long", message: { messageId: "1:0", role: "assistant", text: "*found windows here*", complete: true } }, { conversationId: "long", message: { messageId: "1:1", role: "assistant", text: long, complete: true } }] });
      if (query === "cs-operators") return Response.json({ matches: [{ conversationId: "long", message: { messageId: "2:0", role: "assistant", text: tagged, complete: true } }], tagIds: ["S0BA868QQ90"] });
      return Response.json({ matches: [] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const root = `http://127.0.0.1:${server.port}`;
    const results = page.getByLabel("Search results");
    await page.goto(`${root}/search?q=tag:x`);
    const group = results.getByRole("group", { name: "@cs-operators Allen now says he would buy a Windows computer if memory is the cause." });
    await group.waitFor();
    expect(await group.getAttribute("aria-label")).toBe("@cs-operators Allen now says he would buy a Windows computer if memory is the cause.");
    const heading = group.getByRole("heading").getByRole("link");
    expect(await heading.getAttribute("title")).toBe("@cs-operators Allen now says he would buy a Windows computer if memory is the cause.");
    expect(await heading.locator("strong").textContent()).toBe("Allen now says he would buy a Windows computer if memory is the cause.");
    expect(await results.locator("pre").textContent()).toContain("code line");
    const row = results.locator("a").filter({ has: page.locator("pre") });
    expect(await row.locator("a, button").count()).toBe(0);
    await page.goto(`${root}/search?q=windows`);
    const marked = results.getByRole("link", { name: /found windows/ });
    await marked.waitFor();
    expect(await marked.locator("strong mark").textContent()).toBe("windows");
    expect(await results.getByText("delta").count()).toBe(1);
    expect(await results.getByText("the windows line").count()).toBe(1);
    expect(await results.getByText("golf").count()).toBe(1);
    expect(await results.getByText("alpha").count()).toBe(0);
    expect(await results.getByText("india").count()).toBe(0);
    await page.goto(`${root}/search?q=cs-operators`);
    await results.getByText("@cs-operators").waitFor();
    expect(await results.locator("mark").textContent()).toBe("cs-operators");
    expect(await results.getByText("pad-a").count()).toBe(1);
    expect(await results.getByText("pad-d").count()).toBe(1);
    expect(await results.getByText("keep-out-before").count()).toBe(0);
    expect(await results.getByText("keep-out-after").count()).toBe(0);
    await page.goto(`${root}/search?q=tag:x`);
    await results.locator("pre").waitFor();
    await results.locator("pre").click();
    await page.waitForURL("**/s/Zm9ybWF0dGVk");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
