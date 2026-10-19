import { expect, test } from "bun:test";
import path from "node:path";
import type { HistoryView, ShadowBinding, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const mode of ["initial", "cached", "earlier"] as const) test(`shadow refresh survives ${mode} history reads`, async () => {
  const { chromium: engine } = await import(playwright!);
  const message = (id: string, turn: number, text: string): TranscriptEvent => ({ text, role: "assistant", complete: true, turnId: `T${turn}`, entryKey: `turn:${id}:T${turn}`, itemId: `${id}-${turn}`, inputId: "", messageId: `${turn}:0`, agent: id, model: `${id}/model`, origin: "canonical" });
  const correlation = { parentSourceConversationId: "parent", parentTurnId: "T1", childTurnId: "T1", order: "1", running: true, terminal: "", revision: "initial" };
  const binding: ShadowBinding = { parentConversationId: "parent", parentAgent: "main", shadowAgent: "main/a", childConversationId: "a", turns: [correlation] };
  const inactive: ShadowBinding = { ...binding, shadowAgent: "main/b", childConversationId: "b", turns: [] };
  const view = (messages: TranscriptEvent[], shadows: ShadowBinding[] = [], extra: Partial<HistoryView> = {}): HistoryView => ({ messages, shadows, delegations: [], revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: messages.map((message) => message.entryKey), running: false, terminal: "", start: "1", more: false, movable: false, backgroundJobs: [], ...extra });
  const parent = view([message("parent", 1, "Parent oldest answer")], [binding, inactive]);
  const tail = view(Array.from({ length: 50 }, (_, i) => message("parent", i + 2, `Parent answer ${i + 2}`)), [{ ...binding, turns: [] }, inactive], { revision: "tail", start: "2", more: true });
  let child = view([message("a", 1, "Partial child answer")]);
  const reads: { id: string; before?: string; from?: string; limit?: number; revision?: string }[] = [];
  const streams: string[] = [];
  const delayed = Promise.withResolvers<Response>(), started = Promise.withResolvers<void>();
  let hold = mode === "initial", stream: ReadableStreamDefaultController<string>;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") {
      streams.push(url.searchParams.get("id")!);
      return new Response(new ReadableStream<string>({ start(controller) { stream = controller; controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "parent", agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as typeof reads[number];
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "shadow-refresh" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "shadow-refresh" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/History": {
        reads.push(input);
        if (input.id === "parent") return Response.json(mode === "earlier" && !input.before ? { ...tail, reset: !input.revision } : parent);
        if (input.id === "b") return Response.json({ code: 14, message: "Shadow history unavailable" }, { status: 503 });
        if (hold) { hold = false; started.resolve(); return delayed.promise; }
        return Response.json(child);
      }
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "TOP" } });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors: string[] = [], failedReads: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("requestfailed", (request: { url(): string }) => { if (request.url().endsWith("/api/History")) failedReads.push(request.url()); });
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("parent").replace(/=+$/, "")}`, { waitUntil: "domcontentloaded" });
    await page.getByRole("tab", { name: "main/a", exact: true }).waitFor();
    if (mode === "initial") await started.promise;
    else {
      if (mode === "earlier") {
        await page.getByText("Parent answer 51", { exact: true }).waitFor();
        await page.locator("#transcript-scroll").evaluate((node: HTMLElement) => { node.scrollTop = 0; });
        await page.getByText("Parent oldest answer", { exact: true }).waitFor();
        expect(await page.locator("[data-comparison-row]").count()).toBe(51);
      }
      await page.getByText("Partial child answer", { exact: true }).waitFor();
      if (mode === "cached") {
        hold = true;
        correlation.revision = parent.revision = "mid-read";
        stream!.enqueue(`data: ${JSON.stringify({ conversationId: "parent", revision: "mid-read" })}\n\n`);
        await started.promise;
      }
    }
    correlation.running = false;
    correlation.terminal = "complete";
    correlation.revision = parent.revision = "complete";
    child = view([message("a", 1, "Final child answer")], [], { revision: "complete" });
    const refreshed = page.waitForResponse(async (response: { url(): string; request(): { postDataJSON(): { id: string } }; json(): Promise<HistoryView> }) => response.url().endsWith("/api/History") && response.request().postDataJSON().id === "parent" && (await response.json()).shadows?.some((shadow) => shadow.turns.some((turn) => turn.revision === "complete")), { timeout: 2000 });
    stream!.enqueue(`data: ${JSON.stringify({ conversationId: "parent", revision: "complete" })}\n\n`);
    if (mode !== "earlier") {
      await refreshed;
      // Wait for the parent metadata to render before releasing the old child snapshot.
      await page.locator("[data-shadow-cell]").getByText("Completed silently.", { exact: true }).or(page.locator("[data-shadow-cell]").getByText("Completed.", { exact: true })).waitFor();
      delayed.resolve(Response.json(view([message("a", 1, "Partial child answer")])));
    } else await refreshed;
    await page.getByText("Final child answer", { exact: true }).waitFor({ timeout: 2000 });
    const row = page.locator("[data-comparison-row]").filter({ hasText: "Parent oldest answer" });
    expect(await row.getByText("Final child answer", { exact: true }).count()).toBe(1);
    expect(await row.getByText("Completed.", { exact: true }).count()).toBe(1);
    expect(streams).toEqual(["parent"]);
    expect(reads.every((read) => read.id !== "b")).toBe(true);
    expect(reads.every((read) => read.limit === 50 || read.id === "parent" && !!read.before && !!read.from)).toBe(true);
    // A fingerprint key change cancels the obsolete read; its replacement owns a fresh signal.
    expect(failedReads).toHaveLength(mode === "earlier" ? 0 : 1);
    if (mode === "cached") {
      await page.getByRole("tab", { name: "main/b", exact: true }).click();
      await page.getByRole("alert").filter({ hasText: "Shadow history unavailable" }).waitFor();
      expect(await page.getByText("Parent oldest answer", { exact: true }).count()).toBe(1);
      await page.getByRole("tab", { name: "main/a", exact: true }).click();
      await page.getByText("Final child answer", { exact: true }).waitFor();
      expect(await page.getByRole("alert").count()).toBe(0);
    }
    expect(errors).toEqual([]);
  } finally {
    delayed.resolve(Response.json(child));
    await browser.close();
    server.stop(true);
  }
}, 15000);
