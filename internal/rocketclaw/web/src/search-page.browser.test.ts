import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const width of [1280, 390]) test.skipIf(!playwright || !chromium)(`saved search tabs at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  let owner = "alice";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: owner });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-page" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "" });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: [] });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ jobs: [] });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
    if (url.pathname === "/api/SearchMessages") return Response.json({ matches: [] });
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
    await search.fill("is:forked agent:ma");
    await page.getByRole("button", { name: "main" }).click();
    expect(await search.inputValue()).toBe("is:forked");
    await search.fill("edited");
    await page.reload();
    await search.waitFor();
    expect(await search.inputValue()).toBe("edited");
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
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Session: Search" }).click();
    await page.waitForURL("**/search");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

for (const width of [1280, 390]) test.skipIf(!playwright || !chromium)(`search editor groups clickable message matches at ${width}px, and Enter reloads`, async () => {
  const { chromium: engine } = await import(playwright!);
  const searches: string[] = [];
  const rows = [
    { id: "first", name: "", title: "", preview: "Ordinary", agent: "main" },
    { id: "second", name: "", title: "#notes", preview: "Needle notes\nOther text", agent: "main" },
    { id: "third", name: "Pinned conversation", preview: "Other text", agent: "main", pinned: true },
    { id: "fourth", name: "Origin chat", preview: "Other text", agent: "main", pinned: true },
    { id: "web-session:unnamed", name: "", title: "", preview: "", agent: "main" },
  ];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-results" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "" });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: [] });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ jobs: [] });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
    if (url.pathname === "/api/ListQueue") return Response.json({ items: [] });
    if (url.pathname === "/api/History") {
      const { id } = await request.json() as { id: string };
      return Response.json({ messages: [], origin: id === "fourth" ? JSON.stringify({ kind: "cron", sourcePath: "/notes/archive-key.md", stem: "review" }) : "", delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    }
    if (url.pathname === "/api/SearchMessages") {
      const { query } = await request.json() as { query: string };
      searches.push(query);
      return Response.json({ matches: query === "needle" ? [{ conversationId: "third", message: { messageId: "1:0", role: "user", text: "Context before NEEDLE then needle and after", complete: true, turnId: "", entryKey: "", itemId: "", inputId: "" } }, { conversationId: "third", message: { messageId: "1:1", role: "assistant", text: "A second needle response", complete: true, turnId: "", entryKey: "", itemId: "", inputId: "" } }, { conversationId: "first", message: { messageId: "2:0", role: "assistant", text: "Needle in excluded chat", complete: true, turnId: "", entryKey: "", itemId: "", inputId: "" } }] : [] });
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 700 } });
    await page.goto(`http://127.0.0.1:${server.port}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const results = page.getByLabel("Search results");
    await search.fill("is:pinned needle");
    const group = results.getByRole("group", { name: "Pinned conversation", exact: true });
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
    await search.fill("needle");
    await results.getByRole("link", { name: /Needle notes/ }).last().waitFor();
    await results.getByRole("link", { name: /Needle in excluded chat/ }).waitFor();
    expect(await results.getByRole("group").count()).toBe(3);
    expect(await results.getByRole("group").first().getAttribute("aria-label")).toBe("Pinned conversation");
    expect(await results.getByRole("group", { name: "Ordinary", exact: true }).getByRole("heading").textContent()).toContain("Ordinary");
    expect(await results.getByRole("link", { name: /Needle notes/ }).last().locator("mark").textContent()).toBe("Needle");
    await page.waitForTimeout(300);
    expect(searches).toEqual(["needle"]);
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
    await search.fill("is:pinned");
    await results.getByRole("heading").getByRole("link", { name: "Pinned conversation", exact: true }).click();
    await page.waitForURL("**/s/dGhpcmQ");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:tester")!).tabs[0].query)).toBe("is:pinned");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test.skipIf(!playwright || !chromium)("newer search replaces an in-flight message search and retries errors", async () => {
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
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [], currentAgent: "" });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: [] });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ jobs: [] });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
    if (url.pathname === "/api/SearchMessages") {
      const { query } = await request.json() as { query: string };
      searches.push(query);
      if (query === "old") { started.resolve(); await delayed.promise; }
      if (query === "new" && fail) return Response.json({ message: "Try again", code: 13 }, { status: 500 });
      return Response.json({ matches: [{ conversationId: "chat", message: { messageId: "1:0", role: "user", text: `Match ${query}`, complete: true, turnId: "", entryKey: "", itemId: "", inputId: "" } }] });
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

test.skipIf(!playwright || !chromium)("message matches jump after history loads and last close returns to the last visible message", async () => {
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
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "" });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: [] });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ jobs: [] });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: {} });
    if (url.pathname === "/api/ListQueue") return Response.json({ items: [] });
    if (url.pathname === "/api/SearchMessages") return Response.json({ matches: [
      { conversationId: "one", message: messages("one")[2] },
      { conversationId: "one", message: messages("one")[3] },
    ] });
    if (url.pathname === "/api/History") {
      const { id, originOnly } = await request.json() as { id: string; originOnly?: boolean };
      if (originOnly) return Response.json({ messages: [], origin: "", delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" });
      if (delayHistory && id === "one") await pending.promise;
      return Response.json({ messages: messages(id), origin: "", delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages(id).map((message) => message.entryKey))], running: false, terminal: "" });
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
