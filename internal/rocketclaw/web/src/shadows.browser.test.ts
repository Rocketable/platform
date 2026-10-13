import { expect, test } from "bun:test";
import path from "node:path";
import type { HistoryView, ShadowBinding, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const width of [1440, 390, 320]) test(`shadow comparison at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const message = (id: string, turn: string, role: string, text: string, index: number): TranscriptEvent => ({ text, role, complete: true, turnId: turn, entryKey: `turn:${id}:${turn}`, itemId: `${id}-${turn}-${index}`, inputId: "", agent: id, model: `${id}/model`, origin: "canonical" });
  const messages = (id: string, heights: number[]) => heights.flatMap((height, index) => [message(id, `T${index + 1}`, "user", "Repeated prompt", 0), message(id, `T${index + 1}`, "assistant", Array.from({ length: height }, (_, row) => `${id} turn ${index + 1} paragraph ${row + 1}.`).join("\n\n"), 1)]);
  const binding = (id: string): ShadowBinding => ({ parentConversationId: "parent", parentAgent: "main", shadowAgent: `main/${id}`, childConversationId: id, turns: [1, 2, 3, 4, 5, 6, 7].map((turn) => ({ parentSourceConversationId: "parent", parentTurnId: `T${turn}`, childTurnId: turn > 3 ? "" : `T${turn}`, order: String(turn), running: false, terminal: ["complete", "complete", "complete", "failed", "", "complete", "stopped"][turn - 1], revision: "initial" })) });
  const a = binding("a"), b = binding("b");
  const view = (events: TranscriptEvent[], shadows: ShadowBinding[] = []): HistoryView => ({ messages: events, delegations: [], shadows, revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(events.map((event) => event.entryKey))], running: false, terminal: "", start: "0", more: false, movable: false, backgroundJobs: [] });
  const histories = new Map<string, HistoryView>([["parent", view(messages("parent", [12, 1, 1]), [a, b])], ["a", view(messages("a", [1, 15, 1]))], ["b", view(messages("b", [4, 1, 8]))]]);
  if (width === 320) histories.get("parent")!.messages.filter((event) => event.turnId === "T2").forEach((event) => { event.origin = "sandboxed"; });
  histories.get("parent")!.messages.push(message("parent", "legacy", "assistant", "Before shadows were attached.", 0));
  histories.get("parent")!.entryKeys.push("turn:parent:legacy");
  histories.get("parent")!.messages[0].attachments = [{ id: "image", name: "loaded-later.png", mimeType: "image/png", conversationId: "parent" }];
  const streams: string[] = [], reads: string[] = [];
  let controller: ReadableStreamDefaultController<string>;
  let image: ReadableStreamDefaultController<Uint8Array>;
  const imageRequested = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") {
      streams.push(url.searchParams.get("id")!);
      return new Response(new ReadableStream<string>({ start(stream) { controller = stream; stream.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "parent", agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/DownloadAttachment") return new Response(new ReadableStream<Uint8Array>({ start(stream) { image = stream; imageRequested.resolve(); } }), { headers: { "Content-Type": "image/svg+xml" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; revision?: string; limit?: number };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "shadows" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "shadows" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/History": {
        reads.push(input.id);
        if (input.id !== "parent") expect(input.limit).toBe(50);
        return Response.json(histories.get(input.id)!);
      }
      case "/api/ListQueue": return Response.json({ items: [] });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 900 }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("parent").replace(/=+$/, "")}?shadow=not-a-binding`, { waitUntil: "domcontentloaded" });
    await page.getByText("parent turn 3 paragraph 1.", { exact: true }).waitFor();
    if (width < 1024) {
      const controls = page.getByRole("group", { name: "Conversation view", exact: true });
      const shadows = controls.getByRole("button", { name: "Shadows", exact: true });
      await shadows.waitFor();
      const position = await controls.evaluate((node: HTMLElement) => ({ top: node.getBoundingClientRect().top, bottom: node.getBoundingClientRect().bottom, transcriptBottom: document.querySelector("#transcript-scroll")!.getBoundingClientRect().bottom, composerTop: document.querySelector("textarea")!.getBoundingClientRect().top }));
      expect(position.top).toBeGreaterThanOrEqual(position.transcriptBottom);
      expect(position.bottom).toBeLessThanOrEqual(position.composerTop);
      expect(await controls.getByRole("group", { name: "Show messages from" }).count()).toBe(width === 320 ? 1 : 0);
      if (width === 320) {
        const sandboxed = controls.getByRole("button", { name: "sandboxed", exact: true });
        await sandboxed.click();
        expect(await sandboxed.getAttribute("aria-pressed")).toBe("false");
        expect(await page.getByText("parent turn 2 paragraph 1.", { exact: true }).count()).toBe(0);
        await sandboxed.click();
        await page.getByText("parent turn 2 paragraph 1.", { exact: true }).waitFor();
        const canonical = controls.getByRole("button", { name: "canonical", exact: true });
        await canonical.click();
        expect(await canonical.getAttribute("aria-pressed")).toBe("false");
        expect(await page.getByText("parent turn 3 paragraph 1.", { exact: true }).count()).toBe(0);
        expect(await page.getByText("parent turn 2 paragraph 1.", { exact: true }).count()).toBe(1);
        await canonical.click();
        await page.getByText("parent turn 3 paragraph 1.", { exact: true }).waitFor();
      }
      await shadows.click();
      await page.getByRole("heading", { name: "Shadow comparison · main", exact: true }).waitFor();
    } else expect(await page.getByRole("button", { name: "Shadows", exact: true }).count()).toBe(0);
    await page.getByRole("tab", { name: "main/a", exact: true }).waitFor();
    expect(await page.getByRole("tab", { name: "main/a", exact: true }).innerText()).toBe("a");
    await page.getByText("a turn 3 paragraph 1.", { exact: true }).waitFor();
    expect(await page.locator("[data-shadow-cell] > p:first-child").allTextContents()).toEqual(Array.from({ length: 8 }, (_, index) => `Turn ${index + 1}`));
    const alignment = async () => {
      const tops = await page.locator('#transcript-scroll [data-comparison-row]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => {
        const cells = [...node.children] as HTMLElement[];
        return Math.abs(cells[0].getBoundingClientRect().top - cells[1].getBoundingClientRect().top);
      }));
      expect(tops).toHaveLength(8);
      expect(tops.every((difference: number) => difference < 1)).toBe(true);
    };
    if (width >= 1024) await alignment();
    await page.locator('img[alt="loaded-later.png"]').waitFor({ state: "attached" });
    await page.locator('img[alt="loaded-later.png"]').evaluate((node: HTMLImageElement) => node.scrollIntoView());
    await imageRequested.promise;
    image!.enqueue(new TextEncoder().encode('<svg xmlns="http://www.w3.org/2000/svg" width="400" height="400"><rect width="400" height="400" fill="gray"/></svg>'));
    image!.close();
    await page.locator('img[alt="loaded-later.png"]').evaluate((node: HTMLImageElement) => node.decode());
    if (width >= 1024) await alignment();
    const tabs = page.getByRole("tab", { name: "main/a", exact: true });
    await tabs.focus();
    await tabs.press("ArrowRight");
    expect(new URL(page.url()).searchParams.get("shadow")).toBe("b");
    await page.getByText(/b turn 3 paragraph 8\./).waitFor();
    expect(await page.getByRole("tab", { name: "main/b", exact: true }).getAttribute("aria-selected")).toBe("true");
    expect(new URL(page.url()).searchParams.get("shadow")).toBe("b");
    if (width >= 1024) {
      await alignment();
      const handle = page.getByRole("separator", { name: "Resize shadow panel" });
      await handle.focus();
      const previous = Number(await handle.getAttribute("aria-valuenow"));
      await handle.press("ArrowLeft");
      expect(Number(await handle.getAttribute("aria-valuenow"))).toBe(previous + 16);
      await alignment();
      await page.setViewportSize({ width: 1100, height: 900 });
      await alignment();
    }
    expect(streams).toEqual(["parent"]);
    expect(reads).not.toContain("not-a-binding");
    expect(await page.locator("[data-shadow-cell]").getByRole("button", { name: "Revert message" }).count()).toBe(0);
    expect(await page.getByText("Turn failed.", { exact: true }).count()).toBe(1);
    for (const label of ["Waiting for parent input…", "Completed silently.", "Turn stopped.", "Not attached for this turn or inactive agent segment."]) expect(await page.getByText(label, { exact: true }).count()).toBe(1);
    const updated = histories.get("b")!;
    updated.messages = messages("b", [4, 1, 10]);
    updated.revision = "streamed";
    b.turns[2].revision = "streamed";
    histories.get("parent")!.revision = "child-only";
    controller!.enqueue(`data: ${JSON.stringify({ conversationId: "parent", revision: "child-only" })}\n\n`);
    await page.getByText(/b turn 3 paragraph 10\./).waitFor();
    if (width >= 1024) await alignment();
    expect(streams).toEqual(["parent"]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/rocketclaw/shadow-comparison-${width}.png`), await page.screenshot({ animations: "disabled" }));
    if (width < 1024) {
      await page.getByRole("button", { name: "Go to parent turn 3", exact: true }).click();
      await page.getByRole("dialog").waitFor({ state: "hidden" });
      const parent = await page.getByRole("region", { name: "Turn 3", exact: true }).boundingBox();
      const viewport = await page.locator("#transcript-scroll").boundingBox();
      expect(parent!.y).toBeGreaterThanOrEqual(viewport!.y);
      expect(parent!.y).toBeLessThan(viewport!.y + viewport!.height);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/rocketclaw/shadow-controls-${width}.png`), await page.screenshot({ animations: "disabled" }));
      if (width === 320) {
        b.parentAgent = "other";
        b.shadowAgent = "other/a";
        histories.get("parent")!.revision = "other-parent";
        controller!.enqueue(`data: ${JSON.stringify({ conversationId: "parent", revision: "other-parent" })}\n\n`);
        await page.getByRole("button", { name: "Shadows", exact: true }).click();
        await page.getByRole("heading", { name: "Shadow comparison · other", exact: true }).waitFor();
        expect(await page.getByRole("tab", { name: "main/a", exact: true }).innerText()).toBe("main/a");
        expect(await page.getByRole("tab", { name: "other/a", exact: true }).innerText()).toBe("other/a");
        await page.getByRole("button", { name: "Go to parent turn 3", exact: true }).click();
        histories.get("parent")!.shadows = [];
        histories.get("parent")!.revision = "no-shadows";
        controller!.enqueue(`data: ${JSON.stringify({ conversationId: "parent", revision: "no-shadows" })}\n\n`);
        await page.getByRole("button", { name: "Shadows", exact: true }).waitFor({ state: "hidden" });
        expect(await page.getByRole("group", { name: "Show messages from", exact: true }).count()).toBe(1);
      }
    }
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30000);
