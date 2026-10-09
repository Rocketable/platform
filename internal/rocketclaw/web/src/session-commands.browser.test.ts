import { expect, test } from "bun:test";
import path from "node:path";
import type { PromptDelivery, Session, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const [width, height] of [[1280, 900], [390, 664], [320, 568]]) test(`fork and handoff at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const message = (messageId: string, role: string, text: string): TranscriptEvent => ({ messageId, entryKey: messageId.split(":")[0], itemId: `item:${messageId}`, inputId: "", role, text, complete: true, turnId: "" });
  const histories: Record<string, TranscriptEvent[]> = {
    source: [message("1:0", "user", "First request"), message("1:1", "assistant", "First answer"), message("2:0", "user", "Choose this prompt")],
    destination: [message("3:0", "user", "Destination search needle"), message("3:1", "assistant", "You are looking at the destination")],
  };
  const header = "[ exact <header>\n" + "long-unbroken-header-value".repeat(40) + " ]";
  histories.source[0].header = header;
  const prompts: { id: string; text: string; delivery?: PromptDelivery }[] = [];
  const forks: { id: string; before?: string }[] = [];
  const forkParents: Record<string, string> = {};
  const details: Record<string, Partial<Session>> = { destination: { agent: "a-very-long-agent-name-that-must-not-hide-the-session-age", updatedAt: "2026-09-09T00:00:00.123456Z", tags: ["customer", "a-very-long-tag-name-that-must-not-hide-the-session-age", "<b>literal</b>"] } };
  let failPin = true;
  let agentChoices = true;
  const handoffs: string[] = [];
  const searches: string[] = [];
  const handoffDocument = "# Handoff from source\n" + "Continue the verified work.\n".repeat(80);
  let handoffReady = Promise.withResolvers<void>();
  let handoffSeen = Promise.withResolvers<void>();
  const stashReady = Promise.withResolvers<void>();
  const newSessionReady = Promise.withResolvers<void>();
  const firstTurnReady = Promise.withResolvers<void>();
  const stoppedTurn = Promise.withResolvers<void>();
  const running = new Set<string>();
  let failHandoff = true;
  let failFirstTurn = false;
  const queues: Record<string, { id: string; text: string; delivery: PromptDelivery }[]> = {};
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/UploadAttachment") {
      expect(await request.text()).toBe("keep attachment");
      return Response.json({ id: "uploaded-draft", name: "draft.txt", mimeType: "text/plain", size: "15", conversationId: url.searchParams.get("conversationId") });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: Object.keys(histories).map((id) => ({ id, name: id === "destination" ? "Podcast editing notes with a very long conversation title" : id, agent: "main", preview: histories[id].at(-1)?.text, forkedFrom: forkParents[id], ...details[id] })), owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; before?: string; text: string; messageId?: string; delivery?: PromptDelivery; query: string } & Partial<Session>;
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "session-commands" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "session-commands" } });
      case "/api/ListAgents": return Response.json({ agents: agentChoices ? [{ name: "main", model: "test" }] : [], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/History": {
        const messages = histories[input.id] ?? [];
        return Response.json({ messages, origin: "", revision: JSON.stringify({ messages, running: running.has(input.id) }), reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages.map((message) => message.entryKey))], running: running.has(input.id), terminal: "" });
      }
      case "/api/ListQueue": return Response.json({ items: queues[input.id] ?? [] });
      case "/api/UpdateSession":
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
        handoffSeen.resolve();
        if (failHandoff) return Response.json({ message: "Handoff provider failed", code: 13 }, { status: 500 });
        await handoffReady.promise;
        return Response.json({ document: handoffDocument });
      // Fixed fixture: the server reads IS:FORKED as the fork filter.
      case "/api/SearchSessions": return Response.json({ terms: [], text: "", needle: "", matches: input.query === "IS:FORKED" ? [{ conversationId: "forked", field: "Preview", text: "" }] : [], messages: [], mentionIds: [], indexComplete: true, summariesComplete: true });
      case "/api/SearchMessages": searches.push(input.query); return Response.json({ matches: Object.entries(histories).flatMap(([conversationId, messages]) => messages.filter((item) => item.text.toLowerCase().includes(input.query.toLowerCase())).map((message) => ({ conversationId, message }))) });
      case "/api/CreateSession": await newSessionReady.promise; histories.created = []; return Response.json({ id: "created" });
      case "/api/Prompt": {
        prompts.push(input);
        if (input.delivery === "STASH" || input.delivery === "QUEUE") {
          if (input.delivery === "STASH") await stashReady.promise;
          (queues[input.id] ??= []).push({ id: "stashed", text: input.text, delivery: input.delivery });
          return Response.json({ privateText: "" });
        }
        if (input.text === "$stop") {
          running.delete(input.id);
          stoppedTurn.resolve();
          return Response.json({ privateText: "" });
        }
        running.add(input.id);
        const accepted = { ...message(`${histories[input.id].length + 1}:0`, "user", input.text), inputId: input.messageId ?? "", complete: false };
        histories[input.id].push(accepted);
        if (input.id === "created") await firstTurnReady.promise;
        if (input.text === "start a turn") await stoppedTurn.promise;
        running.delete(input.id);
        accepted.complete = true;
        if (input.id === "created" && failFirstTurn) return Response.json({ message: "First turn failed", code: 13 }, { status: 500 });
        return Response.json({ privateText: "" });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    const headerMessage = page.locator('[data-message-id="1:0"]');
    const headerButton = headerMessage.getByRole("button", { name: "Show message header", exact: true, includeHidden: true });
    const copyButton = headerMessage.getByRole("button", { name: "Copy message", exact: true, includeHidden: true });
    await headerButton.waitFor({ state: "attached" });
    expect(await headerButton.isVisible()).toBe(false);
    expect(await copyButton.isVisible()).toBe(false);
    if (width >= 640) await headerMessage.hover();
    else await headerMessage.getByText("First request", { exact: true }).tap();
    await headerButton.waitFor();
    expect(await copyButton.isVisible()).toBe(true);
    expect(await page.getByRole("button", { name: "Show message header", exact: true, includeHidden: true }).count()).toBe(1);
    if (width >= 640) {
      await headerButton.hover();
      const tooltip = page.locator('[data-slot="tooltip-content"]');
      await tooltip.waitFor();
      expect(await tooltip.textContent()).toBe(header);
      await page.keyboard.press("Escape");
      await tooltip.waitFor({ state: "hidden" });
    }
    await headerButton.click();
    const headerDialog = page.getByRole("dialog", { name: "Message header", exact: true });
    await headerDialog.waitFor();
    expect(await headerDialog.locator('[data-slot="dialog-description"]').textContent()).toBe(header);
    expect(await headerDialog.evaluate((node: HTMLElement) => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.keyboard.press("Escape");
    await headerDialog.waitFor({ state: "hidden" });
    await page.mouse.move(0, 0);
    await page.locator("textarea").focus();
    await headerButton.waitFor({ state: "hidden" });
    expect(await copyButton.isVisible()).toBe(false);
    await headerMessage.focus();
    await headerButton.waitFor();
    await page.keyboard.press("Tab");
    expect(await headerButton.evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
    await page.keyboard.press("Enter");
    await headerDialog.waitFor();
    await page.keyboard.press("Escape");
    await headerDialog.waitFor({ state: "hidden" });
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
    for (const name of ["Sessions: Fork session", "Sessions: Handoff session", "Sessions: Name session", "Sessions: Pin session", "Sessions: Choose agent", "Command: Start goal ($goal)", "Command: Run workflow ($workflow)", "Command: Invoke skill ($skill)", "Command: Enqueue work ($enqueue)", "Command: Stash work ($stash)", "Command: Steer turn ($steer)"]) {
      const action = commands.getByRole("button", { name, exact: true });
      await action.waitFor();
      expect(await action.textContent()).toBe(name);
      expect(await action.evaluate((element: HTMLElement) => element.offsetHeight)).toBeGreaterThanOrEqual(width < 640 ? 44 : 36);
      expect(await action.evaluate((element: HTMLElement) => element.scrollWidth <= element.clientWidth)).toBe(true);
    }
    expect(await commands.getByRole("button", { name: "Sessions: Stop turn" }).count()).toBe(0);
    const commandBounds = await commands.boundingBox();
    expect(commandBounds.x).toBeGreaterThanOrEqual(0);
    expect(commandBounds.x + commandBounds.width).toBeLessThanOrEqual(width);
    expect(commandBounds.y + commandBounds.height).toBeLessThanOrEqual(height);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/command-palette-${width}.png`), await page.screenshot());
    const pinIndex = (await commands.getByRole("button").allTextContents()).indexOf("Sessions: Pin session");
    for (let index = 0; index < pinIndex; index++) await commandSearch.press("ArrowDown");
    await commandSearch.press("Enter");
    await commands.getByRole("alert").waitFor();
    expect(await commands.getByRole("alert").textContent()).toBe("Pin failed");
    const pin = commands.getByRole("button", { name: "Sessions: Pin session", exact: true });
    expect(await pin.evaluate((element: HTMLElement) => element.classList.contains("bg-accent"))).toBe(true);
    failPin = false;
    await pin.waitFor({ state: "visible" });
    await commandSearch.press("Enter");
    await commands.waitFor({ state: "hidden" });
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Unpin session");
    await commands.getByRole("button", { name: "Sessions: Unpin session", exact: true }).click();
    await commands.waitFor({ state: "hidden" });
    expect(details.source.pinned).toBe(false);
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Name session");
    await commands.getByRole("button", { name: "Sessions: Name session" }).click();
    const rename = page.getByRole("dialog", { name: "Name session", exact: true });
    await rename.getByLabel("Session name").fill("Named source");
    await rename.getByRole("button", { name: "Save", exact: true }).click();
    await rename.waitFor({ state: "hidden" });
    expect(details.source.name).toBe("Named source");
    expect(prompts).toHaveLength(0);
    await page.getByLabel("Attach files", { exact: true }).setInputFiles({ name: "draft.txt", mimeType: "text/plain", buffer: Buffer.from("keep attachment") });
    for (const [label, invocation] of [["Start goal", "$goal"], ["Run workflow", "$workflow"], ["Invoke skill", "$skill"], ["Enqueue work", "$enqueue"], ["Stash work", "$stash"], ["Steer turn", "$steer"]]) {
      await composer.fill("keep this draft");
      await page.keyboard.press("Meta+Shift+p");
      await commandSearch.fill(label);
      await commands.getByRole("button", { name: `Command: ${label} (${invocation})`, exact: true }).click();
      await commands.waitFor({ state: "hidden" });
      expect(await composer.inputValue()).toBe(`${invocation} keep this draft`);
      expect(await composer.evaluate((element: HTMLElement) => document.activeElement === element)).toBe(true);
      expect(await page.getByRole("button", { name: "Remove draft.txt", exact: true }).count()).toBe(1);
      expect(prompts).toHaveLength(0);
    }
    // MRU follows command IDs, not sessions or labels, without section dividers.
    await page.keyboard.press("Meta+Shift+p");
    expect(await commands.getByRole("button").first().textContent()).toBe("Command: Steer turn ($steer)");
    const history = await page.evaluate(() => localStorage.getItem("command-history"));
    await commandSearch.fill("no-such-command");
    await commands.getByText("No matches", { exact: true }).waitFor();
    await page.keyboard.press("Escape");
    expect(await page.evaluate(() => localStorage.getItem("command-history"))).toBe(history);
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("$goal");
    await commandSearch.press("Enter");
    await commands.waitFor({ state: "hidden" });
    const savedComposer = await composer.inputValue();
    await page.waitForFunction(async (text: string) => {
      const db = await new Promise<IDBDatabase>((resolve) => { const open = indexedDB.open("rocketclaw-drafts", 1); open.onsuccess = () => resolve(open.result); });
      const rows = await new Promise<{ text: string }[]>((resolve) => { const request = db.transaction("content").objectStore("content").getAll(); request.onsuccess = () => resolve(request.result); });
      db.close(); return rows.some((row) => row.text === text);
    }, savedComposer);
    await page.reload();
    await composer.waitFor();
    await page.waitForFunction((text: string) => document.querySelector("textarea")?.value === text, savedComposer);
    await page.keyboard.press("Meta+Shift+p");
    expect(await commands.getByRole("button").first().textContent()).toBe("Command: Start goal ($goal)");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("command-history")!).filter((key: string) => key === "goal"))).toEqual(["goal"]);
    await commands.evaluate((element: HTMLElement) => Promise.all(element.getAnimations({ subtree: true }).map((animation) => animation.finished)));
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/command-recency-${width}.png`), await page.screenshot());
    await commandSearch.press("ArrowDown");
    await commandSearch.press("Enter");
    await commands.waitFor({ state: "hidden" });
    expect(await composer.inputValue()).toBe(`$steer ${savedComposer}`);
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Choose agent");
    await commands.getByRole("button", { name: "Sessions: Choose agent" }).click();
    await page.getByRole("option", { name: /main/ }).waitFor();
    // Desktop types straight into the search; touch keeps the keyboard closed.
    await page.waitForTimeout(300);
    expect(await page.getByRole("option", { name: /main/ }).isVisible()).toBe(true);
    expect(await page.evaluate(() => document.activeElement?.getAttribute("aria-label") === "Search agents")).toBe(width >= 640);
    await page.keyboard.press("Escape");
    await page.getByRole("option", { name: /main/ }).waitFor({ state: "hidden" });
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Fork session");
    await commands.getByRole("button", { name: "Sessions: Fork session" }).click();
    await dialog.getByRole("button", { name: "Choose this prompt Continue before this message" }).waitFor();
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("forked").replace(/=+$/, "")}`);
    await composer.waitFor();
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session", exact: true });
    const search = palette.getByPlaceholder("Search sessions");
    const destinationRow = palette.locator("li").filter({ hasText: "Podcast editing notes" });
    await destinationRow.waitFor();
    expect(await palette.getByRole("img", { name: "Forked session", exact: true }).count()).toBe(1);
    const age = destinationRow.locator("time");
    expect(await destinationRow.locator("span[title]:not([data-slot])").getAttribute("title")).toBe([details.destination.agent, ...details.destination.tags!].join(" · "));
    expect(await destinationRow.locator("b").count()).toBe(0);
    expect(await age.count()).toBe(1);
    expect(await age.getAttribute("datetime")).toBe(details.destination.updatedAt);
    const ageText = await age.textContent();
    expect(ageText).toMatch(/^\d+d$/);
    expect(await age.evaluate((element: HTMLElement) => element.scrollWidth <= element.clientWidth && element.getBoundingClientRect().right <= element.closest("li")!.getBoundingClientRect().right)).toBe(true);
    if (width >= 768) {
      await age.hover();
      const date = await page.evaluate((iso: string) => new Date(iso).toLocaleString(undefined, { timeZoneName: "short" }), details.destination.updatedAt!);
      const tooltip = page.locator('[data-slot="tooltip-content"]');
      await tooltip.waitFor();
      expect(await tooltip.textContent()).toBe(`Updated ${date}`);
      expect(await age.getAttribute("aria-label")).toBe(`Updated ${date}`);
      await page.mouse.move(0, 0);
      await tooltip.waitFor({ state: "hidden" });
    }
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/palette-rows-${width}.png`), await page.screenshot());
    await search.fill("IS:FORKED");
    await palette.locator('ul[data-pending="false"]').waitFor();
    expect(await palette.getByRole("img", { name: "Forked session", exact: true }).count()).toBe(1);
    expect(await palette.locator("li > button").count()).toBe(1);
    await page.keyboard.press("Escape");
    if (width >= 768) {
      await page.locator("main").getByRole("button", { name: "Open original conversation", exact: true }).click();
      await page.waitForURL("**/s/" + btoa("source").replace(/=+$/, ""));
    }
    forkParents.destination = "source";

    // Handoff originates from an ordinary session, independent of the fork above.
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    await composer.waitFor();
    // Plain HTTP on a LAN address does not expose crypto.randomUUID.
    await page.evaluate(() => Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true }));
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Handoff session");
    await commands.getByRole("button", { name: "Sessions: Handoff session" }).click();
    const destinationSearch = dialog.getByRole("textbox", { name: "Search messages" });
    await destinationSearch.waitFor({ timeout: 5000 });
    expect(await destinationSearch.count()).toBe(1);
    await dialog.getByRole("alert").waitFor();
    expect(await dialog.getByRole("alert").textContent()).toBe("Handoff provider failed");
    expect(handoffs).toEqual(["source"]);
    expect(searches).toEqual([]);
    for (const label of ["Copy handoff", "Start new session"]) expect(await dialog.getByRole("button", { name: label, exact: true }).count()).toBe(1);
    expect(await dialog.getByRole("button", { name: /Destination search needle/ }).count()).toBe(0);
    expect(prompts).toHaveLength(0);
    failHandoff = false;
    handoffSeen = Promise.withResolvers();
    await dialog.getByRole("button", { name: "Retry handoff" }).click();
    await handoffSeen.promise;
    expect(handoffs).toEqual(["source", "source"]);
    const aborted = page.waitForEvent("requestfailed", (request: { url: () => string }) => request.url().endsWith("/api/Handoff"));
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await aborted;
    await composer.fill("$handoff");
    await composer.press("Enter");
    await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true }));
    await dialog.getByRole("button", { name: "Copy handoff", exact: true }).click();
    await dialog.getByRole("button", { name: "Copying handoff…", exact: true }).waitFor({ timeout: 2000 });
    expect(await dialog.getByRole("button", { name: "Copying handoff…" }).locator("[aria-live]").getAttribute("aria-live")).toBe("polite");
    expect(await dialog.locator('p[role="status"]').count()).toBe(0);
    expect(prompts).toHaveLength(0);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/handoff-loading-${width}.png`), await page.screenshot());
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    handoffReady.resolve();
    await dialog.waitFor({ state: "hidden" });
    await page.evaluate(() => Reflect.deleteProperty(navigator, "clipboard"));
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(handoffDocument);
    expect(handoffs).toEqual(["source", "source", "source"]);
    expect(prompts).toHaveLength(0);

    await composer.fill("$handoff");
    handoffSeen = Promise.withResolvers();
    await composer.press("Enter");
    await destinationSearch.fill("destination search");
    const destinationMatch = dialog.getByRole("button", { name: /Destination search needle/ });
    await destinationMatch.getByRole("img", { name: "Forked session", exact: true }).waitFor();
    expect(await destinationMatch.getByText("Podcast editing notes with a very long conversation title", { exact: true }).count()).toBe(1);
    expect(await destinationMatch.locator("span[title]:not([data-slot])").getAttribute("title")).toBe([details.destination.agent, ...details.destination.tags!].join(" · "));
    expect(searches).toEqual(["destination search"]);
    await destinationMatch.click();
    await page.waitForURL("**/s/" + btoa("destination").replace(/=+$/, ""));
    expect(await dialog.getByRole("heading").textContent()).toBe("Session handoff");
    await dialog.getByText("Podcast editing notes with a very long conversation title", { exact: true }).waitFor();
    await page.locator("main").getByText("You are looking at the destination", { exact: true }).waitFor();
    await page.waitForFunction(() => !document.querySelector<HTMLButtonElement>('button[aria-label="Stash handoff here"]')?.disabled);
    await handoffSeen.promise;
    expect(handoffs).toEqual(["source", "source", "source", "source"]);
    expect(await composer.isVisible()).toBe(false);
    // The dialog's entrance scale also changes its buttons' measured bounds.
    await dialog.evaluate((element: HTMLElement) => Promise.all(element.getAnimations().map((animation) => animation.finished)));
    const bounds = await dialog.boundingBox();
    expect(bounds.height).toBeLessThan(height * 0.75);
    expect(await dialog.evaluate((element: HTMLElement) => element.scrollWidth <= element.clientWidth)).toBe(true);
    for (const name of ["Copy handoff", "Start new session", "Copy Handoff", "Expand Handoff", "Stash handoff here", "Change"]) {
      const button = await dialog.getByRole("button", { name, exact: true }).boundingBox();
      expect(button.height).toBeGreaterThanOrEqual(44);
      expect(button.x).toBeGreaterThanOrEqual(bounds.x);
      expect(button.x + button.width).toBeLessThanOrEqual(bounds.x + bounds.width);
      expect(button.y + button.height).toBeLessThanOrEqual(bounds.y + bounds.height);
    }
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
    expect(handoffs).toEqual(["source", "source", "source", "source"]);
    await dialog.getByRole("button", { name: "Stash handoff here" }).click();
    await dialog.getByRole("button", { name: "Stash handoff here" }).getByText("Stashing…", { exact: true }).waitFor();
    expect(await dialog.getByRole("button", { name: "Stash handoff here" }).isDisabled()).toBe(true);
    expect(await dialog.getByText("Stashing handoff…", { exact: true }).isVisible()).toBe(false);
    stashReady.resolve();
    await dialog.waitFor({ state: "hidden" });
    expect(prompts).toEqual([{ id: "destination", text: handoffDocument, delivery: "STASH" }]);
    expect(forks).toHaveLength(1);
    const stashed = page.locator('[data-queue-id="stashed"]');
    await stashed.getByText(handoffDocument, { exact: true }).waitFor();
    expect(await page.locator("[data-queue-id]").count()).toBe(1);
    await stashed.getByRole("button", { name: "Pop", exact: true }).waitFor();
    expect(prompts).toHaveLength(1);

    await composer.fill("$handoff");
    await composer.press("Enter");
    expect(prompts).toHaveLength(1);
    await destinationSearch.fill("First request");
    if (width < 640) {
      // Exercise the reduced viewport available while a mobile keyboard is open.
      await page.setViewportSize({ width, height: 360 });
      await page.waitForFunction(() => document.querySelector('[role="dialog"]')!.getBoundingClientRect().bottom <= window.innerHeight);
      const keyboardBounds = await dialog.boundingBox();
      expect(keyboardBounds.y + keyboardBounds.height).toBeLessThanOrEqual(360);
      await page.setViewportSize({ width, height });
    }
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/handoff-search-${width}.png`), await page.screenshot());
    await dialog.getByRole("button", { name: /First request/ }).first().click();
    await dialog.getByRole("button", { name: "Copy Handoff", exact: true }).waitFor();
    await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true }));
    await dialog.getByRole("button", { name: "Copy Handoff", exact: true }).click();
    await page.evaluate(() => Reflect.deleteProperty(navigator, "clipboard"));
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(handoffDocument);
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    expect(prompts).toHaveLength(1);
    await composer.fill("$handoff");
    await composer.press("Enter");
    await dialog.getByRole("button", { name: "Start new session", exact: true }).click();
    await dialog.getByRole("button", { name: "Starting session…", exact: true }).waitFor({ timeout: 2000 });
    expect(await dialog.locator('p[role="status"]').count()).toBe(0);
    newSessionReady.resolve();
    await page.waitForURL("**/s/" + btoa("created").replace(/=+$/, ""));
    await dialog.waitFor({ state: "hidden" });
    expect(await page.locator("main").getByText("# Handoff from source", { exact: false }).count()).toBeGreaterThan(0);
    await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
    const turn = page.waitForResponse((response: { url: () => string }) => response.url().endsWith("/api/Prompt"));
    firstTurnReady.resolve();
    await turn;
    expect(prompts.at(-1)).toMatchObject({ id: "created", text: handoffDocument });
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    await composer.waitFor();
    failFirstTurn = true;
    await composer.fill("$handoff");
    await composer.press("Enter");
    await dialog.getByRole("button", { name: "Start new session", exact: true }).click();
    await page.waitForURL("**/s/" + btoa("created").replace(/=+$/, ""));
    await dialog.waitFor({ state: "hidden" });
    await page.locator("main").getByText("First turn failed", { exact: true }).waitFor();
    expect(await composer.inputValue()).toBe(handoffDocument);
    failFirstTurn = false;
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    await composer.waitFor();
    await composer.fill("start a turn");
    await composer.press("Enter");
    await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
    await composer.fill("draft during turn");
    await page.keyboard.press("Meta+Shift+p");
    await commandSearch.fill("Stop turn");
    await commands.getByRole("button", { name: "Sessions: Stop turn" }).click();
    await commands.waitFor({ state: "hidden" });
    expect(prompts.at(-1)?.text).toBe("$stop");
    expect(await composer.inputValue()).toBe("draft during turn");
    handoffReady = Promise.withResolvers<void>();
    await composer.fill("$handoff");
    await composer.press("Enter");
    await destinationSearch.fill("destination search");
    await dialog.getByRole("button", { name: /Destination search needle/ }).click();
    const stashButton = dialog.getByRole("button", { name: "Stash handoff here" });
    await page.waitForFunction(() => !document.querySelector<HTMLButtonElement>('button[aria-label="Stash handoff here"]')?.disabled);
    await stashButton.click();
    await stashButton.getByText("Stashing…", { exact: true }).waitFor();
    const footerBounds = await dialog.locator('[data-slot="dialog-footer"]').boundingBox();
    const buttonBounds = await stashButton.boundingBox();
    expect(footerBounds.x + footerBounds.width - buttonBounds.x - buttonBounds.width).toBeLessThanOrEqual(24);
    expect(await dialog.getByText("Preparing handoff…", { exact: true }).isVisible()).toBe(false);
    handoffReady.resolve();
    await dialog.waitFor({ state: "hidden" });
    agentChoices = false;
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    await composer.waitFor();
    await page.keyboard.press("Meta+Shift+p");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("command-history")!))).toEqual(expect.arrayContaining(["agent", "stop"]));
    expect(await commands.getByRole("button", { name: /^Sessions: (Choose agent|Stop turn|Open original conversation)/ }).count()).toBe(0);
    await page.keyboard.press("Escape");
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await composer.waitFor();
    await page.keyboard.press("Meta+Shift+p");
    expect(await commands.getByRole("button", { name: /^(Command:|Sessions: (Open original conversation|Fork session|Handoff session|Name session|Pin session|Unpin session|Stop turn|Choose agent))/ }).count()).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("handoff hits and queued items format tags and markup", async () => {
  const { chromium: engine } = await import(playwright!);
  const marked = "*bold* <!subteam^S1>";
  const message = (messageId: string, role: string, text: string): TranscriptEvent => ({ messageId, entryKey: messageId.split(":")[0], itemId: `item:${messageId}`, inputId: "", role, text, complete: true, turnId: "" });
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "source", agent: "main", name: "Source" }, { id: "dest", agent: "main", name: "Dest" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; query: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "format-handoff" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "/workspace" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }] });
      case "/api/SlackNames": return Response.json({ names: { S1: "handle" } });
      case "/api/History": return Response.json({ messages: [message("1:0", "user", "hello")], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: ["1"], running: false, terminal: "" });
      case "/api/ListQueue": return Response.json({ items: input.id === "dest" ? [{ id: "q1", text: marked, delivery: "QUEUE" }] : [] });
      case "/api/Handoff": return Response.json({ document: "doc" });
      case "/api/SearchMessages": return Response.json({ matches: [{ conversationId: "dest", message: message("2:0", "user", marked) }] });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("dest")}`);
    const queued = page.locator('[data-queue-id="q1"]');
    await queued.getByText("@handle").waitFor();
    expect(await queued.locator("strong").textContent()).toBe("bold");
    expect(await queued.getByText("@handle").count()).toBe(1);
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source")}`);
    await page.locator("textarea").waitFor();
    await page.locator("textarea").fill("$handoff");
    await page.locator("textarea").press("Enter");
    const dialog = page.getByRole("dialog", { name: "Session handoff" });
    await dialog.getByRole("textbox", { name: "Search messages" }).fill("bold");
    const hit = dialog.getByRole("button").filter({ has: page.locator("strong") });
    await hit.getByText("@handle").waitFor();
    expect(await hit.locator("strong").textContent()).toBe("bold");
    expect(await hit.getByText("@handle").count()).toBe(1);
    await hit.click();
    const excerpt = dialog.locator("p.truncate");
    await excerpt.getByText("@handle").waitFor();
    expect(await excerpt.locator("strong").textContent()).toBe("bold");
    expect(await excerpt.getByText("@handle").count()).toBe(1);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
