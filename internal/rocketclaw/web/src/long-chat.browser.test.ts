import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

test("long chats open at the newest turns, load earlier ones near the top, and load linked older messages", async () => {
  const { chromium: engine } = await import(playwright!);
  const turns = 120;
  const entry = (n: number) => [
    { role: "user", messageId: `${n}:0`, entryKey: String(n), itemId: `${n}:0`, inputId: `input-${n}`, turnId: `turn-${n}`, text: `user ${n}`, complete: true },
    { role: "assistant", messageId: `${n}:1`, entryKey: String(n), itemId: `${n}:1`, inputId: "", turnId: `turn-${n}`, text: `assistant ${n}`, complete: true },
  ];
  const view = (first: number, last: number, extra: object) => ({ messages: Array.from({ length: last - first + 1 }, (_, i) => entry(first + i)).flat(), origin: "", delegations: [], revision: "", reset: true, replacedKeys: [], removedKeys: [], entryKeys: Array.from({ length: last - first + 1 }, (_, i) => String(first + i)), running: false, terminal: "", start: String(first), more: first > 1, ...extra });
  const pages: { before?: string; from?: string; limit?: number }[] = [];
  const opened = Promise.withResolvers<void>();
  let fullReads = 0;
  const streamOpened = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") {
      setTimeout(streamOpened.resolve, 200);
      return new Response(new ReadableStream({ start(controller) { controller.enqueue(`data: ${JSON.stringify({ conversationId: "long", revision: "" })}\n\n`); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "long", name: "Long chat" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "long-chat" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "long" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/History") {
      const input = await request.json() as { revision?: string; limit?: number; before?: string; from?: string };
      await opened.promise;
      if (!input.before && !input.revision) {
        fullReads++;
        await streamOpened.promise;
      }
      if (!input.before) return Response.json(view(turns - input.limit! + 1, turns, { revision: "tail", reset: !input.revision, ...(input.revision ? { messages: [], replacedKeys: [] } : {}) }));
      pages.push({ before: input.before, from: input.from, limit: input.limit });
      if (input.before === "5") return Response.json({ message: "Earlier history unavailable", code: 13 }, { status: 503 });
      const before = Number(input.before);
      return Response.json(view(input.from ? Number(input.from) : Math.max(1, before - input.limit!), before - 1, { reset: false }));
    }
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 650 } });
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 8 });
    await page.goto(`http://127.0.0.1:${server.port}/s/bG9uZw`);
    // A slow first read shows that the chat is loading, not an empty chat.
    await page.getByText("Loading conversation…").waitFor();
    expect(await page.getByText("Send a message to start the conversation.").count()).toBe(0);
    opened.resolve();
    await page.getByText(`assistant ${turns}`).waitFor();
    expect(await page.locator('[data-slot="message"][data-message-id]').count()).toBe(100);
    expect(pages).toEqual([]);
    expect(fullReads).toBe(1);

    // Typing redraws only the composer; redrawing a long transcript per keystroke made input lag.
    // Background polls may still redraw it a few times, so this bounds the count well below one per key.
    await page.evaluate(() => {
      const probe = window as unknown as { transcriptRenders: number };
      probe.transcriptRenders = 0;
      window.URLSearchParams = class extends URLSearchParams {
        constructor(init?: ConstructorParameters<typeof URLSearchParams>[0]) {
          super(init);
          probe.transcriptRenders++;
        }
      };
    });
    const typed = "typing in a long chat redraws only the composer";
    await page.locator("textarea").pressSequentially(typed);
    expect(await page.locator("textarea").inputValue()).toBe(typed);
    expect(await page.evaluate(() => (window as unknown as { transcriptRenders: number }).transcriptRenders)).toBeLessThan(typed.length / 2);
    // Naming a tab group redraws only its field too; tab state belongs to the whole app.
    await page.getByRole("tab", { name: "Long chat", exact: true }).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Add to group" }).click();
    await page.getByRole("menuitem", { name: "New group" }).click();
    const group = page.getByRole("textbox", { name: "Group name" });
    await group.waitFor();
    await page.evaluate(() => { (window as unknown as { transcriptRenders: number }).transcriptRenders = 0; });
    const named = "Incident Oct 12th";
    await group.pressSequentially(named);
    expect(await group.inputValue()).toBe(named);
    expect(await page.evaluate(() => (window as unknown as { transcriptRenders: number }).transcriptRenders)).toBeLessThan(named.length / 2);
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: named, exact: true }).waitFor();

    await page.locator("#transcript-scroll").hover();
    await page.mouse.wheel(0, -100_000);
    await page.getByText("user 21", { exact: true }).waitFor({ state: "attached" });
    expect(pages).toEqual([{ before: "71", limit: 50 }]);
    // The turns already on screen stay put; the page grows above them.
    expect(await page.evaluate(() => {
      const viewport = document.querySelector("#transcript-scroll")!.getBoundingClientRect();
      const turn = document.querySelector('[data-message-id="71:0"]')!.getBoundingClientRect();
      return turn.bottom > viewport.top && turn.top < viewport.bottom;
    })).toBe(true);
    expect(await page.getByText("user 20", { exact: true }).count()).toBe(0);

    await page.evaluate(() => {
      history.pushState(null, "", "/s/bG9uZw?message=5%3A0");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="5:0"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    });
    expect(pages[1]).toEqual({ before: "21", from: "5", limit: undefined });
    await page.getByText("Earlier history unavailable", { exact: true }).waitFor();
    expect(pages[2]).toEqual({ before: "5", from: undefined, limit: 50 });
    expect(await page.getByText("user 5", { exact: true }).count()).toBe(1);
    expect(await page.getByText(`assistant ${turns}`, { exact: true }).count()).toBe(1);
    expect(await page.locator("textarea").inputValue()).toBe(typed);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("a chat that doesn't exist says so instead of loading forever", async () => {
  const { chromium: engine } = await import(playwright!);
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start() {} }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "missing" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "missing" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/History") return Response.json({ code: 5, message: "web conversation: conversation is not recorded" }, { status: 404 });
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.port}/s/bm9uZQ`);
    await page.getByText("This chat doesn't exist.").waitFor();
    expect(await page.getByRole("link", { name: "Start a new chat" }).getAttribute("href")).toBe("/");
    expect(await page.getByText("Loading conversation…").count()).toBe(0);
    expect(await page.getByText("conversation is not recorded").count()).toBe(0);
    expect(await page.getByRole("textbox", { name: "Message or $command" }).count()).toBe(0);
  } finally {
    await browser.close();
    server.stop(true);
  }
});
