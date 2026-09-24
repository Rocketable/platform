import { expect, test } from "bun:test";
import path from "node:path";
import type { PromptDelivery, Session, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const [width, height] of [[1280, 900], [390, 664], [320, 568]]) test.skipIf(!playwright || !chromium)(`fork and handoff at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const message = (messageId: string, role: string, text: string): TranscriptEvent => ({ messageId, role, text, complete: true, snapshot: false, turnId: "" });
  const histories: Record<string, TranscriptEvent[]> = {
    source: [message("1:0", "user", "First request"), message("1:1", "assistant", "First answer"), message("2:0", "user", "Choose this prompt")],
    destination: [message("3:0", "user", "Destination search needle"), message("3:1", "assistant", "You are looking at the destination")],
  };
  const prompts: { id: string; text: string; delivery?: PromptDelivery }[] = [];
  const forks: { id: string; before?: string }[] = [];
  const forkParents: Record<string, string> = {};
  const details: Record<string, Partial<Session>> = {};
  let failPin = true;
  let settledFork = false;
  const handoffs: string[] = [];
  const handoffDocument = "# Handoff from source\n" + "Continue the verified work.\n".repeat(80);
  const handoffReady = Promise.withResolvers<void>();
  let failHandoff = true;
  const queues: Record<string, { id: string; text: string; delivery: PromptDelivery }[]> = {};
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: Object.keys(histories).map((id) => ({ id, name: id === "destination" ? "Podcast editing notes with a very long conversation title" : id, agent: "main", preview: histories[id].at(-1)?.text, forkedFrom: forkParents[id], settled: id === "forked" && settledFork, ...details[id] })), owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; before?: string; text: string; delivery?: PromptDelivery; query: string } & Partial<Session>;
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "session-commands" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main", model: "test" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/History": return Response.json({ messages: histories[input.id] ?? [] });
      case "/api/ListQueue": return Response.json({ items: queues[input.id] ?? [] });
      case "/api/UpdateSession":
      case "/api/SettleSession":
        if (input.pinned && failPin) return Response.json({ message: "Pin failed", code: 13 }, { status: 500 });
        details[input.id] = { ...details[input.id], ...input };
        return Response.json({});
      case "/api/ForkSession": {
        forks.push(input);
        const index = histories[input.id].findIndex((item) => item.messageId === input.before);
        histories.forked = input.before ? histories[input.id].slice(0, index) : [...histories[input.id]];
        forkParents.forked = input.id;
        return Response.json({ id: "forked", prompt: input.before ? histories[input.id][index] : message("", "user", "") });
      }
      case "/api/Handoff":
        handoffs.push(input.id);
        if (failHandoff) return Response.json({ message: "Handoff provider failed", code: 13 }, { status: 500 });
        await handoffReady.promise;
        return Response.json({ document: handoffDocument });
      case "/api/SearchMessages": return Response.json({ matches: Object.entries(histories).flatMap(([conversationId, messages]) => messages.filter((item) => item.text.toLowerCase().includes(input.query.toLowerCase())).map((message) => ({ conversationId, message }))) });
      case "/api/Prompt":
        prompts.push(input);
        (queues[input.id] ??= []).push({ id: "stashed", text: input.text, delivery: input.delivery! });
        return Response.json({ privateText: "" });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    const composer = page.locator("textarea");
    await composer.fill("$fork");
    await composer.press("Enter");
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Choose this prompt Continue before this message" }).click();
    await page.waitForURL("**/s/" + btoa("forked").replace(/=+$/, ""));
    expect(forks).toHaveLength(1);
    expect(forks[0].id).toBe("source");
    expect(forks[0].before).toBe("2:0");
    expect(await composer.inputValue()).toBe("Choose this prompt");
    await page.locator("main").getByText("First answer", { exact: true }).waitFor();
    expect(prompts).toHaveLength(0);

    await page.reload();
    await composer.waitFor();
    await page.keyboard.press("Meta+Shift+p");
    const commands = page.getByRole("dialog", { name: "Run command", exact: true });
    const commandSearch = commands.getByPlaceholder("Type a command");
    await commandSearch.fill("original");
    await commands.getByRole("button", { name: /Open original conversation/ }).waitFor();
    expect(await commands.getByRole("button", { name: /Open original conversation/ }).count()).toBe(1);
    await commands.getByRole("button", { name: /Open original conversation/ }).click();
    await page.waitForURL("**/s/" + btoa("source").replace(/=+$/, ""));
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("original");
    expect(await commands.getByRole("button", { name: /Open original conversation/ }).count()).toBe(0);
    await commandSearch.fill("");
    for (const name of ["Fork session", "Handoff session", "Name session", "Snooze session", "Pin session", "Settle", "Choose agent", "Start goal", "Run workflow", "Invoke skill", "Stash work", "Show queue"]) {
      const action = commands.getByRole("button", { name, exact: true });
      await action.waitFor();
      expect(await action.textContent()).toBe(name);
      expect(await action.evaluate((element: HTMLElement) => element.offsetHeight)).toBe(width < 640 ? 44 : 36);
    }
    expect(await commands.getByRole("button", { name: /^Stop turn/ }).count()).toBe(0);
    const commandBounds = await commands.boundingBox();
    expect(commandBounds.x).toBeGreaterThanOrEqual(0);
    expect(commandBounds.x + commandBounds.width).toBeLessThanOrEqual(width);
    expect(commandBounds.y + commandBounds.height).toBeLessThanOrEqual(height);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/command-palette-${width}.png`), await page.screenshot());
    await commandSearch.fill("Pin session");
    await commands.getByRole("button", { name: /^Pin session/ }).click();
    await commands.getByRole("alert").waitFor();
    expect(await commands.getByRole("alert").textContent()).toBe("Pin failed");
    failPin = false;
    await commands.getByRole("button", { name: /^Pin session/ }).click();
    await commands.waitFor({ state: "hidden" });
    for (const label of ["Unpin session", "Settle", "Unsettle"]) {
      await page.keyboard.press("Meta+Shift+p");
      await commandSearch.fill(label);
      await commands.getByRole("button", { name: label, exact: true }).click();
      await commands.waitFor({ state: "hidden" });
    }
    expect(details.source.pinned).toBe(false);
    expect(details.source.settled).toBe(false);
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Name session");
    await commands.getByRole("button", { name: /^Name session/ }).click();
    const rename = page.getByRole("dialog", { name: "Name session", exact: true });
    await rename.getByLabel("Session name").fill("Named source");
    await rename.getByRole("button", { name: "Save", exact: true }).click();
    await rename.waitFor({ state: "hidden" });
    expect(details.source.name).toBe("Named source");
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Snooze session");
    await commands.getByRole("button", { name: "Snooze session", exact: true }).click();
    const snooze = page.getByRole("dialog", { name: "Snooze session", exact: true });
    await snooze.getByLabel("Return at (local time)").fill("2027-01-02T09:30");
    await snooze.getByRole("button", { name: "Snooze", exact: true }).click();
    await snooze.waitFor({ state: "hidden" });
    expect(details.source.snoozedUntil).toBe(await page.evaluate(() => new Date("2027-01-02T09:30").toISOString()));
    expect(prompts).toHaveLength(0);
    await page.getByLabel("Attach files", { exact: true }).setInputFiles({ name: "draft.txt", mimeType: "text/plain", buffer: Buffer.from("keep attachment") });
    for (const [label, invocation] of [["Start goal", "$goal"], ["Run workflow", "$workflow"], ["Invoke skill", "$skill"], ["Stash work", "$enqueue"]]) {
      await composer.fill("keep this draft");
      await page.keyboard.press("Meta+Shift+p");
      await commandSearch.fill(label);
      await commands.getByRole("button", { name: label, exact: true }).click();
      await commands.waitFor({ state: "hidden" });
      expect(await composer.inputValue()).toBe(`${invocation} keep this draft`);
      expect(await composer.evaluate((element: HTMLElement) => document.activeElement === element)).toBe(true);
      expect(await page.getByRole("button", { name: "Remove draft.txt", exact: true }).count()).toBe(1);
      expect(prompts).toHaveLength(0);
    }
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Choose agent");
    await commands.getByRole("button", { name: /^Choose agent/ }).click();
    await page.getByRole("option", { name: /main/ }).waitFor();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Show queue");
    await commands.getByRole("button", { name: /^Show queue/ }).click();
    const queue = page.getByRole("dialog", { name: "Session queue", exact: true });
    await queue.getByText("No pending work", { exact: true }).waitFor();
    await queue.getByRole("button", { name: "Close", exact: true }).click();
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Fork session");
    await commands.getByRole("button", { name: /^Fork session/ }).click();
    await dialog.getByRole("button", { name: "Choose this prompt Continue before this message" }).waitFor();
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("forked").replace(/=+$/, "")}`);
    if (width < 768) await page.getByRole("button", { name: "Sessions", exact: true }).click();
    const sidebar = width < 768 ? page.getByRole("dialog", { name: "Sessions", exact: true }) : page.locator("#session-sidebar");
    const forkRow = sidebar.locator("li").filter({ has: page.getByRole("img", { name: "Forked session", exact: true }) });
    await forkRow.waitFor();
    expect(await sidebar.getByRole("img", { name: "Forked session", exact: true }).count()).toBe(1);
    await forkRow.hover();
    await forkRow.getByRole("button", { name: "Open original conversation", exact: true }).click();
    await page.waitForURL("**/s/" + btoa("source").replace(/=+$/, ""));
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session", exact: true });
    const search = palette.getByPlaceholder("Search sessions");
    await search.fill("IS:FORKED");
    await palette.getByRole("button", { name: /forked/ }).waitFor();
    expect(await palette.getByRole("img", { name: "Forked session", exact: true }).count()).toBe(1);
    expect(await palette.locator("li > button").count()).toBe(1);
    await search.fill("is:forked forked");
    expect(await palette.locator("li > button").count()).toBe(1);
    await search.fill("is:forked source");
    expect(await palette.locator("li > button").count()).toBe(0);
    await page.keyboard.press("Escape");
    settledFork = true;
    await page.goto(`http://127.0.0.1:${server.port}/settled`);
    const settled = page.locator("main");
    await settled.getByRole("textbox").fill("is:forked");
    await settled.getByRole("img", { name: "Forked session", exact: true }).waitFor();
    expect(await settled.locator("li").count()).toBe(1);
    settledFork = false;
    forkParents.destination = "source";

    // Handoff originates from an ordinary session, independent of the fork above.
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    await composer.waitFor();
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Handoff session");
    await commands.getByRole("button", { name: /^Handoff session/ }).click();
    await dialog.getByRole("alert").waitFor();
    expect(await dialog.getByRole("alert").textContent()).toBe("Handoff provider failed");
    expect(await dialog.getByRole("textbox", { name: "Search messages" }).count()).toBe(0);
    expect(prompts).toHaveLength(0);
    failHandoff = false;
    await dialog.getByRole("button", { name: "Retry handoff" }).click();
    await dialog.getByText("Preparing handoff…", { exact: true }).waitFor();
    expect(await dialog.getByRole("textbox", { name: "Search messages" }).count()).toBe(0);
    expect(await dialog.getByText("Enter text to find a destination message").count()).toBe(0);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/handoff-loading-${width}.png`), await page.screenshot());
    handoffReady.resolve();
    await dialog.getByRole("textbox", { name: "Search messages" }).fill("destination search");
    const destinationMatch = dialog.getByRole("button", { name: /Destination search needle/ });
    await destinationMatch.getByRole("img", { name: "Forked session", exact: true }).waitFor();
    expect(await destinationMatch.getByText("Podcast editing notes with a very long conversation title", { exact: true }).count()).toBe(1);
    expect(await destinationMatch.getByText("main", { exact: true }).count()).toBe(1);
    await destinationMatch.click();
    await page.waitForURL("**/s/" + btoa("destination").replace(/=+$/, ""));
    expect(await dialog.getByRole("heading").textContent()).toBe("Session handoff");
    await dialog.getByText("Podcast editing notes with a very long conversation title", { exact: true }).waitFor();
    await page.locator("main").getByText("You are looking at the destination", { exact: true }).waitFor();
    await page.waitForFunction(() => !document.querySelector<HTMLButtonElement>('button[aria-label="Stash handoff here"]')?.disabled);
    expect(handoffs).toEqual(["source", "source"]);
    expect(await composer.isVisible()).toBe(false);
    const bounds = await dialog.boundingBox();
    expect(bounds.height).toBeLessThan(height / 2);
    expect(await dialog.evaluate((element: HTMLElement) => element.scrollWidth <= element.clientWidth)).toBe(true);
    for (const name of ["Copy Handoff", "Expand Handoff", "Stash handoff here", "Change"]) {
      const button = await dialog.getByRole("button", { name, exact: true }).boundingBox();
      expect(button.height).toBeGreaterThanOrEqual(44);
      expect(button.x).toBeGreaterThanOrEqual(bounds.x);
      expect(button.x + button.width).toBeLessThanOrEqual(bounds.x + bounds.width);
      expect(button.y + button.height).toBeLessThanOrEqual(bounds.y + bounds.height);
    }
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await dialog.getByRole("button", { name: "Copy Handoff", exact: true }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(handoffDocument);
    expect(prompts).toHaveLength(0);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/handoff-${width}.png`), await page.screenshot());
    await dialog.getByRole("button", { name: "Expand Handoff", exact: true }).click();
    const expanded = page.getByRole("dialog", { name: "Handoff", exact: true });
    await expanded.waitFor();
    expect(await expanded.locator("pre").textContent()).toBe(handoffDocument);
    await expanded.getByRole("button", { name: "Close", exact: true }).click();
    await expanded.waitFor({ state: "hidden" });
    expect(handoffs).toEqual(["source", "source"]);
    await dialog.getByRole("button", { name: "Stash handoff here" }).click();
    await dialog.waitFor({ state: "hidden" });
    expect(prompts).toEqual([{ id: "destination", text: handoffDocument, delivery: "STASH" }]);
    expect(forks).toHaveLength(1);
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Show queue");
    await commands.getByRole("button", { name: /^Show queue/ }).click();
    await queue.getByText("Stashed", { exact: true }).waitFor();
    expect(await queue.locator("li").count()).toBe(1);
    expect(await queue.locator("li").textContent()).toBe(`Stashed${handoffDocument}`);
    await queue.getByRole("button", { name: "Close", exact: true }).click();
    expect(prompts).toHaveLength(1);

    await composer.fill("$handoff");
    await composer.press("Enter");
    await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true }));
    await dialog.getByRole("button", { name: "Copy Handoff", exact: true }).click();
    await page.evaluate(() => Reflect.deleteProperty(navigator, "clipboard"));
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(handoffDocument);
    expect(prompts).toHaveLength(1);
    await dialog.getByRole("textbox").fill("First request");
    if (width < 640) {
      // Exercise the reduced viewport available while a mobile keyboard is open.
      await page.setViewportSize({ width, height: 360 });
      await page.waitForFunction(() => document.querySelector('[role="dialog"]')!.getBoundingClientRect().bottom <= window.innerHeight);
      const keyboardBounds = await dialog.boundingBox();
      expect(keyboardBounds.y + keyboardBounds.height).toBeLessThanOrEqual(360);
      await dialog.getByRole("button", { name: "Copy Handoff", exact: true }).click();
      await page.setViewportSize({ width, height });
    }
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/handoff-search-${width}.png`), await page.screenshot());
    await dialog.getByRole("button", { name: /First request/ }).first().click();
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    expect(prompts).toHaveLength(1);
    await composer.fill("start a turn");
    await composer.press("Enter");
    await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
    await composer.fill("draft during turn");
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Stop turn");
    await commands.getByRole("button", { name: /^Stop turn/ }).click();
    await commands.waitFor({ state: "hidden" });
    expect(prompts.at(-1)?.text).toBe("$stop");
    expect(await composer.inputValue()).toBe("draft during turn");
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await composer.waitFor();
    await page.keyboard.press("Meta+Shift+p");
    expect(await commands.getByRole("button", { name: /^(Open original conversation|Fork session|Handoff session|Name session|Stop turn)/ }).count()).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
