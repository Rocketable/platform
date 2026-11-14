import { expect, test } from "bun:test";
import path from "node:path";
import { mkdir } from "node:fs/promises";
import type { QueueItem } from "./types";

const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

// OpenCode references: packages/app/src/composer/editor/editor.tsx (including our
// embedded touch bar) and packages/app/src/session/composer/queue.ts.
for (const [width, touch] of [[390, true], [320, true], [820, true], [1280, false], [390, false]] as const) {
  test(`native composer at ${width}px with ${touch ? "touch" : "desktop"} input`, async () => {
    const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
    const calls: { text: string; delivery?: string }[] = [];
    const queue: QueueItem[] = [];
    let running = false;
    let stream: ReadableStreamDefaultController<string>;
    const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
      const url = new URL(request.url);
      if (url.pathname === "/stream") return new Response(new ReadableStream<string>({ start(controller) { stream = controller; controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
      if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "composer", name: "Native session layout", agent: "Build", running }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
      if (!url.pathname.startsWith("/api/")) {
        const file = Bun.file(path.join(dist, url.pathname));
        return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
      }
      switch (url.pathname) {
        case "/api/Protocol": return Response.json({ protoSha256: "composer" });
        case "/api/Identity": return Response.json({ username: "tester" });
        case "/api/ListConfig": return Response.json({ config: { workspace: "composer" } });
        case "/api/ListAgents": return Response.json({ agents: [{ name: "Build", model: "OpenAI / GPT-6.1 Sol", reasoning: "high" }], currentAgent: "Build" });
        case "/api/ListSkills": return Response.json({ skills: [] });
        case "/api/ListQueue": return Response.json({ items: queue });
        case "/api/Prompt": {
          const prompt = await request.json() as { text: string; delivery: QueueItem["delivery"]; messageId: string };
          calls.push(prompt);
          if (prompt.delivery === "QUEUE" || prompt.delivery === "STASH") queue.push({ id: prompt.messageId, text: prompt.text, delivery: prompt.delivery });
          return Response.json({});
        }
        case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], pendingQuestions: [], messages: [{ entryKey: "turn", itemId: "reply", inputId: "", turnId: "turn", role: "assistant", text: "The session is ready. You can send a new request, queue a follow-up, or steer the work already in progress.", complete: true }], running, revision: String(running), reset: true, entryKeys: ["turn"], start: "0", more: false });
        case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
        default: return Response.json({});
      }
    } });
    const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
    try {
      const page = await browser.newPage({ viewport: { width, height: 844 }, hasTouch: touch, isMobile: touch && width < 640 });
      const errors: string[] = [];
      page.on("pageerror", (error: Error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${server.port}/s/${Buffer.from("composer").toString("base64url")}`);
      const input = page.locator("textarea");
      await page.getByRole("button", { name: "Choose agent", exact: true }).waitFor();
      await page.locator("textarea:enabled").waitFor();
      await page.evaluate(() => document.fonts.ready);
      expect(await page.getByRole("button", { name: "Steer", exact: true }).count()).toBe(0);
      expect(await page.getByRole("button", { name: "Send", exact: true }).isDisabled()).toBe(true);
      await page.getByText("OpenAI / GPT-6.1 Sol", { exact: true }).waitFor();
      await page.getByRole("button", { name: "Voice call", exact: true }).waitFor();
      await mkdir(shots, { recursive: true });
      await page.screenshot({ path: path.join(shots, `native-composer-idle-${width}-${touch ? "touch" : "desktop"}.png`) });

      await input.fill("First line");
      // Clearing is optimistic; the fixture records each prompt before its response.
      const firstPrompt = page.waitForResponse("**/api/Prompt");
      await input.press("Enter");
      if (touch) {
        expect(await input.inputValue()).toBe("First line\n");
        expect(calls).toEqual([]);
        await page.getByRole("button", { name: "Send", exact: true }).click();
      }
      await firstPrompt;
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls[0]).toMatchObject({ text: touch ? "First line\n" : "First line", delivery: "STEER" });

      running = true;
      stream!.enqueue(`data: ${JSON.stringify({ conversationId: "composer", revision: "running" })}\n\n`);
      await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
      await input.fill("Please focus on the composer layout.");
      const steer = page.getByRole("button", { name: "Steer", exact: true }).last();
      await steer.waitFor();
      const primary = page.getByRole("button", { name: touch ? "Queue" : "Send", exact: true });
      const box = (await primary.boundingBox())!;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.y + box.height).toBeLessThanOrEqual(844);
      if (touch) expect(box.height).toBeGreaterThanOrEqual(44);
      await page.mouse.move(0, 0);
      await page.getByRole("tooltip").waitFor({ state: "hidden" });
      await page.screenshot({ path: path.join(shots, `native-composer-working-${width}-${touch ? "touch" : "desktop"}.png`) });
      await Promise.all([page.waitForResponse("**/api/Prompt"), primary.click()]);
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls.at(-1)).toMatchObject({ text: "Please focus on the composer layout.", delivery: "QUEUE" });
      await page.locator("[data-queue-id]").waitFor();
      await input.fill("Keep the voice control.");
      await page.mouse.move(0, 0);
      await page.getByRole("tooltip").waitFor({ state: "hidden" });
      await page.screenshot({ path: path.join(shots, `native-composer-queued-${width}-${touch ? "touch" : "desktop"}.png`) });
      await Promise.all([page.waitForResponse("**/api/Prompt"), steer.click()]);
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls.at(-1)).toMatchObject({ text: "Keep the voice control.", delivery: "STEER" });
      expect(await input.evaluate((element: HTMLTextAreaElement) => document.activeElement === element)).toBe(true);
      await input.fill("Save this for later");
      await page.getByRole("button", { name: "Composer actions", exact: true }).click();
      await Promise.all([page.waitForResponse("**/api/Prompt"), page.getByRole("menuitem", { name: "Stash", exact: true }).click()]);
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls.at(-1)).toMatchObject({ text: "Save this for later", delivery: "STASH" });
      expect(errors).toEqual([]);
    } finally {
      await browser.close();
      server.stop(true);
    }
  }, 30_000);
}
