import { expect, test } from "bun:test";
import path from "node:path";
import type { TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const [width, height] of [[1280, 900], [390, 664]]) test.skipIf(!playwright || !chromium)(`delegation panel at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const event = (role: string, text: string, tool: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ role, text, complete: true, entryKey: "", itemId: "", inputId: "", turnId: "", ...tool });
  const histories: Record<string, { messages: TranscriptEvent[]; delegations: string[] }> = {
    chat: { messages: [event("user", "Run the review", { messageId: "1:0" }), event("tool", `task {"description":"Review"}`, { toolName: "task", toolCallId: "call-task" }), event("tool", `bash {"command":"ls"}`, { toolName: "bash", toolCallId: "call-plain" }), event("assistant", "Review finished")], delegations: ["chat/call-task"] },
    "chat/call-task": { messages: [event("user", "Review this"), event("tool", `execute {"code":"print(1)"}`, { toolName: "execute", toolCallId: "call-inner" }), event("assistant", "Child done")], delegations: ["chat/call-task/call-inner"] },
    "chat/call-task/call-inner": { messages: [event("assistant", "Inner review allowed")], delegations: [] },
  };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "delegations" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListCronJobs": return Response.json({ jobs: [] });
      case "/api/ListConfig": return Response.json({ config: {} });
      case "/api/History": {
        const history = histories[input.id] ?? { messages: [], delegations: [] };
        const messages = history.messages.map((message, index) => ({ ...message, entryKey: input.id, itemId: `${input.id}:${index}` }));
        return Response.json({ ...history, messages, origin: "", revision: input.id, reset: true, replacedKeys: [], removedKeys: [], entryKeys: [input.id], running: false, terminal: "" });
      }
      case "/api/ListQueue": return Response.json({ items: [] });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replace(/=+$/, "")}?message=1%3A0`);
    const open = page.locator("main").getByRole("link", { name: /^Open delegation/ });
    await page.locator("main").getByText("Review finished", { exact: true }).waitFor();
    expect(await open.count()).toBe(1);
    const plain = page.locator('main details:has(> summary[title="bash · ls"])');
    await plain.waitFor();
    expect(await plain.getByRole("link").count()).toBe(0);
    const panel = width >= 1024 ? page.getByRole("complementary", { name: "Delegation", exact: true }) : page.getByRole("dialog", { name: "Delegation", exact: true });
    const crumbs = panel.getByRole("navigation", { name: "Delegation breadcrumbs" });
    const delegation = () => new URL(page.url()).searchParams.get("delegation");

    await open.and(page.getByRole("link", { name: "Open delegation: task", exact: true })).click();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task");
    expect(new URL(page.url()).searchParams.get("message")).toBe("1:0");
    expect(await page.getByRole("dialog").count()).toBe(width >= 1024 ? 0 : 1);
    await page.goBack();
    await panel.waitFor({ state: "detached" });
    expect(delegation()).toBe(null);
    await page.goForward();
    await panel.getByText("Child done", { exact: true }).waitFor();

    await panel.getByRole("link", { name: "Open delegation: Run · print(1)", exact: true }).click();
    await panel.getByText("Inner review allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task/call-inner");
    expect(await crumbs.getByRole("link").allTextContents()).toEqual(["Conversation", "task"]);
    expect(await crumbs.locator('[aria-current="page"]').textContent()).toBe("Run · print(1)");
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/delegation-panel-${width}.png`), await page.screenshot());
    await panel.getByRole("link", { name: "Back to task", exact: true }).click();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task");
    await page.goBack();
    await panel.getByText("Inner review allowed", { exact: true }).waitFor();

    await crumbs.getByRole("link", { name: "task", exact: true }).click();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task");
    expect(await crumbs.locator('[aria-current="page"]').textContent()).toBe("task");
    await page.reload();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(await crumbs.getByRole("link").allTextContents()).toEqual(["Conversation"]);
    await panel.getByRole("button", { name: "Close delegation", exact: true }).click();
    await panel.waitFor({ state: "detached" });
    expect(delegation()).toBe(null);
    expect(new URL(page.url()).searchParams.get("message")).toBe("1:0");
    await page.goBack();
    await panel.getByText("Child done", { exact: true }).waitFor();
    if (width >= 1024) for (const [aside, name, sign, initial, max] of [[panel, "Resize delegation panel", -1, 384, 768], [page.locator("#session-sidebar"), "Resize sidebar", 1, 288, 512]] as const) {
      const handle = page.getByRole("separator", { name, exact: true });
      const size = async () => Math.round((await aside.boundingBox())!.width);
      const drag = async (by: number) => {
        const box = (await handle.boundingBox())!;
        await page.mouse.move(box.x + box.width / 2, box.y + 50);
        await page.mouse.down();
        await page.mouse.move(Math.min(Math.max(box.x + box.width / 2 + by, 0), width - 1), box.y + 50, { steps: 4 });
        await page.mouse.up();
      };
      expect(await size()).toBe(initial);
      await drag(sign * 100);
      const dragged = await size();
      expect(Math.abs(dragged - initial - 100)).toBeLessThan(2);
      await handle.press("ArrowLeft");
      expect(await size()).toBe(dragged - sign * 16);
      await drag(sign * 2000);
      const widest = await size();
      const chat = Math.round((await page.locator("main").boundingBox())!.width);
      expect(chat).toBeGreaterThanOrEqual(416);
      expect(widest === max || chat === 416).toBe(true);
      expect(await handle.getAttribute("aria-valuenow")).toBe(String(widest));
      await page.reload();
      await panel.getByText("Child done", { exact: true }).waitFor();
      expect(await size()).toBe(widest);
      await drag(-sign * 2000);
    }
    await panel.getByRole("link", { name: "Back to conversation", exact: true }).click();
    await panel.waitFor({ state: "detached" });
    expect(delegation()).toBe(null);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
