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
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "long", name: "Long chat" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "long" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/History") {
      const input = await request.json() as { revision?: string; limit?: number; before?: string; from?: string };
      if (!input.before) return Response.json(view(turns - input.limit! + 1, turns, { revision: "tail", reset: !input.revision, ...(input.revision ? { messages: [], replacedKeys: [] } : {}) }));
      pages.push({ before: input.before, from: input.from, limit: input.limit });
      const before = Number(input.before);
      return Response.json(view(input.from ? Number(input.from) : Math.max(1, before - input.limit!), before - 1, { reset: false }));
    }
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
    await page.getByText(`assistant ${turns}`).waitFor();
    expect(await page.locator('[data-slot="message"][data-message-id]').count()).toBe(100);
    expect(pages).toEqual([]);

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
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
