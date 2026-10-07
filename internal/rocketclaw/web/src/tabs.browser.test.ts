import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Message = { role: string; messageId: string; entryKey: string; itemId: string; inputId: string; turnId: string; text: string; complete: boolean };

function chat(id: string, turns: number): Message[] {
  return Array.from({ length: turns }, (_, i) => [
    { role: "user", messageId: `${i + 1}:0`, entryKey: String(i + 1), itemId: `${id}-${i}:0`, inputId: `${id}-input-${i}`, turnId: "", text: `${id} user ${i + 1}`, complete: true },
    { role: "assistant", messageId: `${i + 1}:1`, entryKey: String(i + 1), itemId: `${id}-${i}:1`, inputId: "", turnId: "", text: `${id} assistant ${i + 1}\n\nline\n\nline`, complete: true },
  ]).flat();
}

async function start(width = 1100, height = 650) {
  const histories: Record<string, Message[]> = { alpha: chat("alpha", 30), beta: chat("beta", 30), gamma: chat("gamma", 3), delta: chat("delta", 3), epsilon: chat("epsilon", 3), zeta: chat("zeta", 3) };
  const running = new Set<string>();
  const reads: { id: string; revision?: string }[] = [];
  const identity = { username: "tester", ready: Promise.resolve() };
  const create = { ready: Promise.resolve() };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: Object.keys(histories).map((id) => ({ id, name: `${id} chat`, running: running.has(id) })), owner: identity.username, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; revision?: string; text: string; messageId?: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "tabs" });
      case "/api/Identity": await identity.ready; return Response.json({ username: identity.username });
      case "/api/ListConfig": return Response.json({ config: { workspace: "tabs" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/CreateSession": await create.ready; histories.fresh = []; return Response.json({ id: "fresh" });
      case "/api/Prompt": {
        const n = histories[input.id].length / 2 + 1;
        histories[input.id].push({ role: "user", messageId: `${n}:0`, entryKey: String(n), itemId: `${input.id}-${n}:0`, inputId: input.messageId ?? "", turnId: "", text: input.text, complete: true });
        return Response.json({ privateText: "" });
      }
      case "/api/History": {
        reads.push({ id: input.id, revision: input.revision });
        const messages = histories[input.id] ?? [];
        const revision = String(messages.length);
        const view = { origin: "", revision, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages.map((message) => message.entryKey))], running: running.has(input.id), terminal: "", start: "1", more: false };
        return Response.json(input.revision === revision ? { ...view, messages: [], reset: false } : { ...view, messages, reset: true });
      }
      default: return Response.json({});
    }
  } });
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
  // Count client EventSources: the transport opens one per mounted live transcript.
  await page.addInitScript(() => {
    const streams = { open: 0, max: 0 };
    Object.assign(window, { streams });
    window.EventSource = class extends EventSource {
      constructor(url: string | URL, init?: EventSourceInit) {
        super(url, init);
        streams.max = Math.max(streams.max, ++streams.open);
      }
      close() {
        if (this.readyState !== EventSource.CLOSED) streams.open--;
        super.close();
      }
    };
  });
  const errors: string[] = [];
  page.on("pageerror", (error: Error) => errors.push(error.message));
  const base = `http://127.0.0.1:${server.port}`;
  const close = async () => {
    await browser.close();
    server.stop(true);
  };
  return { page, base, histories, running, reads, identity, create, errors, close };
}

type Page = Awaited<ReturnType<typeof start>>["page"];

async function go(page: Page, to: string) {
  await page.evaluate((target: string) => {
    history.pushState(null, "", target);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, to);
}

const session = (id: string) => `/s/${btoa(id).replace(/=+$/, "")}`;
const tabNames = (page: Page) => page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab").allTextContents();
const waitTabs = (page: Page, names: string[]) => page.waitForFunction((expected: string) => [...document.querySelectorAll('[aria-label="Open tabs"] [role="tab"]')].map((tab) => tab.textContent).join("|") === expected, names.join("|"));
const selectedTab = (page: Page) => page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { selected: true }).textContent();
const streams = (page: Page) => page.evaluate(() => (window as unknown as { streams: { open: number; max: number } }).streams);
const frames = (page: Page) => page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));

test("switching session tabs keeps scroll, draft, and DOM without reloading history", async () => {
  const { page, base, reads, errors, close } = await start();
  try {
    await page.goto(base + session("alpha"));
    await page.getByText("alpha user 30", { exact: true }).waitFor();
    const viewport = page.locator("#transcript-scroll");
    await viewport.hover();
    await page.mouse.wheel(0, -2000);
    await page.waitForFunction(() => document.querySelector("#transcript-scroll")!.scrollTop < 7000);
    const offset = await viewport.evaluate((element: HTMLElement) => { element.dataset.kept = "alpha"; return element.scrollTop; });
    await page.locator("textarea:visible").fill("unsent alpha draft");

    await go(page, session("beta"));
    await page.getByText("beta user 30", { exact: true }).waitFor();
    expect(await page.locator("textarea:visible").inputValue()).toBe("");
    const before = reads.length;

    await page.getByRole("tab", { name: "alpha chat" }).click();
    await page.getByText("alpha user 30", { exact: true }).waitFor();
    // The scroller re-measures a frame after showing; wait past it.
    await frames(page);
    expect(await viewport.evaluate((element: HTMLElement) => element.scrollTop)).toBe(offset);
    expect(await viewport.evaluate((element: HTMLElement) => element.dataset.kept)).toBe("alpha");
    expect(await page.locator("textarea:visible").inputValue()).toBe("unsent alpha draft");
    // Activation catches up through a revision delta, never a full read from the start.
    expect(reads.slice(before).filter((read) => read.id === "alpha")).not.toEqual([]);
    expect(reads.slice(before).filter((read) => read.id === "alpha" && read.revision === undefined)).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await close();
  }
}, 30_000);

test("warm tabs stream one at a time, route commands to the visible tab, catch up, and evict past the cap", async () => {
  const { page, base, histories, running, reads, errors, close } = await start();
  try {
    await page.goto(base + session("alpha"));
    await page.getByText("alpha user 30", { exact: true }).waitFor();
    for (const id of ["beta", "gamma"]) {
      await go(page, session(id));
      await page.getByText(`${id} user 3`, { exact: true }).waitFor();
    }
    await page.getByRole("tab", { name: "beta chat" }).click();
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat", "gamma chat"]);
    expect(await page.locator('[data-slot="message-scroller-viewport"]').count()).toBe(3);
    expect(await streams(page)).toEqual({ open: 1, max: 1 });
    await go(page, "/cron");
    await page.getByRole("heading", { name: "Cron" }).waitFor();
    expect((await streams(page)).open).toBe(0);
    await page.getByRole("tab", { name: "beta chat" }).click();

    // Palette $ commands and the Quote action act on the visible tab only.
    await page.keyboard.press("Meta+Shift+p");
    await page.getByPlaceholder("Type a command").fill("goal");
    await page.getByRole("button", { name: "Command: Start goal ($goal)" }).click();
    expect(await page.locator("textarea:visible").inputValue()).toBe("$goal ");
    await page.locator("textarea:visible").fill("");
    await page.locator("#transcript-scroll").getByText("beta user 30", { exact: true }).evaluate((node: HTMLElement) => {
      const range = document.createRange();
      range.selectNodeContents(node);
      getSelection()!.removeAllRanges();
      getSelection()!.addRange(range);
    });
    await page.getByRole("button", { name: "Quote", exact: true }).click();
    expect(await page.locator("textarea:visible").inputValue()).toBe("> beta user 30\n\n");
    expect(await page.locator("textarea").evaluateAll((nodes: HTMLTextAreaElement[]) => nodes.map((node) => node.value).filter(Boolean))).toEqual(["> beta user 30\n\n"]);

    // A hidden tab shows its running turn from the session list and catches up when shown.
    running.add("alpha");
    histories.alpha.push({ role: "assistant", messageId: "31:1", entryKey: "31", itemId: "alpha-31:1", inputId: "", turnId: "", text: "alpha caught up", complete: true });
    await page.getByRole("tab", { name: "alpha chat" }).getByRole("img", { name: "Turn running" }).waitFor();
    expect(await page.getByText("alpha caught up", { exact: true }).count()).toBe(0);
    running.delete("alpha");
    await page.getByRole("tab", { name: "alpha chat" }).click();
    await page.getByText("alpha caught up", { exact: true }).waitFor();

    // Past the cap the least recently used view unmounts; activating it remounts and reads it again.
    for (const id of ["delta", "epsilon", "zeta"]) {
      await go(page, session(id));
      await page.getByText(`${id} user 3`, { exact: true }).waitFor();
    }
    await page.getByRole("tab", { name: "gamma chat" }).click();
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat", "gamma chat", "Cron", "delta chat", "epsilon chat", "zeta chat"]);
    expect(await page.locator('[data-slot="message-scroller-viewport"]').count()).toBe(5);
    expect(await page.getByText("beta user 1", { exact: true }).count()).toBe(0);
    const before = reads.length;
    await page.getByRole("tab", { name: "beta chat" }).click();
    await page.getByText("beta user 30", { exact: true }).waitFor();
    expect(reads.slice(before).some((read) => read.id === "beta")).toBe(true);
    expect(await page.getByText("alpha user 1", { exact: true }).count()).toBe(0);
    expect((await streams(page)).max).toBe(1);
    expect(errors).toEqual([]);
  } finally {
    await close();
  }
}, 30_000);

test("creating a session turns the composer tab into its tab without remounting", async () => {
  const { page, base, errors, close } = await start();
  try {
    await page.goto(base + session("alpha"));
    await page.getByText("alpha user 30", { exact: true }).waitFor();
    await page.getByRole("button", { name: "New session" }).click();
    await page.waitForURL(base + "/");
    expect(await tabNames(page)).toEqual(["alpha chat", "New session"]);
    await page.locator("#transcript-scroll").evaluate((element: HTMLElement) => { element.dataset.kept = "composer"; });
    await page.locator("textarea:visible").fill("hello fresh");
    await page.locator("textarea:visible").press("Enter");
    await page.waitForURL(base + session("fresh"));
    await page.getByText("hello fresh", { exact: true }).waitFor();
    expect(await page.locator("#transcript-scroll").evaluate((element: HTMLElement) => element.dataset.kept)).toBe("composer");
    await page.getByRole("tab", { name: "fresh chat" }).waitFor();
    expect(await tabNames(page)).toEqual(["alpha chat", "fresh chat"]);
    expect(await selectedTab(page)).toBe("fresh chat");
    expect(errors).toEqual([]);
  } finally {
    await close();
  }
}, 30_000);

test("a hidden composer tab finishes creating its session only when shown again", async () => {
  const { page, base, create, errors, close } = await start();
  try {
    await page.goto(base + session("alpha"));
    await page.getByText("alpha user 30", { exact: true }).waitFor();
    await page.getByRole("button", { name: "New session" }).click();
    await page.waitForURL(base + "/");
    const held = Promise.withResolvers<void>();
    create.ready = held.promise;
    await page.locator("textarea:visible").fill("hello fresh");
    await page.locator("textarea:visible").press("Enter");
    await page.getByRole("tab", { name: "alpha chat" }).click();
    await page.waitForURL(base + session("alpha"));
    const prompted = page.waitForResponse((response: { url(): string }) => response.url().endsWith("/api/Prompt"));
    held.resolve();
    await prompted;
    await frames(page);
    expect(page.url()).toBe(base + session("alpha"));
    await page.getByRole("tab", { name: "New session" }).click();
    await page.waitForURL(base + session("fresh"));
    await page.getByRole("tab", { name: "fresh chat" }).waitFor();
    expect(await tabNames(page)).toEqual(["alpha chat", "fresh chat"]);
    expect(errors).toEqual([]);
  } finally {
    await close();
  }
}, 30_000);

test("tabs persist per owner, close to a neighbour, and move between top and left", async () => {
  const { page, base, identity, errors, close } = await start(1280, 800);
  try {
    await page.goto(base + session("alpha"));
    await page.getByText("alpha user 30", { exact: true }).waitFor();
    await go(page, session("beta"));
    await go(page, "/cron");
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat", "Cron"]);
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("tabs-warm:tester")!))).toEqual([session("alpha"), session("beta"), "/cron"]);

    // Reload keeps the tabs and the URL keeps the active one; a deep link adds a tab.
    await page.reload();
    await page.getByRole("heading", { name: "Cron" }).waitFor();
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat", "Cron"]);
    expect(await selectedTab(page)).toBe("Cron");
    await page.goto(base + session("gamma"));
    await page.getByText("gamma user 3", { exact: true }).waitFor();
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat", "Cron", "gamma chat"]);
    expect(await selectedTab(page)).toBe("gamma chat");

    // Tabs are scoped to the signed-in owner.
    identity.username = "other";
    await waitTabs(page, ["gamma chat"]);
    identity.username = "tester";
    await waitTabs(page, ["alpha chat", "beta chat", "Cron", "gamma chat"]);

    // Keyboard: arrows, Home, and End move focus and activate.
    await page.getByRole("tab", { name: "gamma chat" }).focus();
    await page.keyboard.press("ArrowLeft");
    await page.waitForURL(base + "/cron");
    expect(await page.evaluate(() => document.activeElement?.textContent)).toBe("Cron");
    await page.keyboard.press("Home");
    await page.waitForURL(base + session("alpha"));
    await page.keyboard.press("ArrowLeft");
    await page.waitForURL(base + session("gamma"));
    expect(await page.getByRole("tab", { name: "gamma chat" }).getAttribute("tabindex")).toBe("0");
    expect(await page.getByRole("tab", { name: "alpha chat" }).getAttribute("tabindex")).toBe("-1");

    // Closing the active last tab activates its left neighbour; page close controls close the page tab.
    await page.getByRole("button", { name: "Close gamma chat" }).click();
    await page.waitForURL(base + "/cron");
    await page.locator("main").getByRole("button", { name: "Close", exact: true }).click();
    await page.waitForURL(base + session("beta"));
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat"]);
    await go(page, "/agents");
    await page.getByRole("heading", { name: "Agents" }).waitFor();
    await page.keyboard.press("Escape");
    await page.waitForURL(base + session("beta"));
    expect(await tabNames(page)).toEqual(["alpha chat", "beta chat"]);
    await page.getByRole("button", { name: "Close alpha chat" }).click();
    expect(page.url()).toBe(base + session("beta"));
    await page.getByRole("button", { name: "Close beta chat" }).click();
    await page.waitForURL(base + "/");
    expect(await tabNames(page)).toEqual(["New session"]);
    expect(await page.getByRole("button", { name: "Close New session" }).count()).toBe(0);

    // Placement toggles from the strip and the palette and survives reload.
    const tablist = page.getByRole("tablist", { name: "Open tabs" });
    expect(await tablist.getAttribute("aria-orientation")).toBe("horizontal");
    await page.getByRole("button", { name: "Move tabs to left" }).click();
    expect(await tablist.getAttribute("aria-orientation")).toBe("vertical");
    await page.reload();
    await tablist.waitFor();
    expect(await tablist.getAttribute("aria-orientation")).toBe("vertical");
    const strip = (await page.getByRole("navigation", { name: "Tabs" }).boundingBox())!;
    const main = (await page.locator("main").boundingBox())!;
    expect(strip.x + strip.width).toBeLessThanOrEqual(main.x);
    expect(strip.height).toBeGreaterThan(400);
    await page.keyboard.press("Meta+Shift+p");
    await page.getByPlaceholder("Type a command").fill("tabs");
    await page.getByRole("button", { name: "Tabs: Move to top" }).click();
    expect(await tablist.getAttribute("aria-orientation")).toBe("horizontal");
    expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("top");

    // Corrupt or mismatched storage resets to the current location.
    for (const stored of ["{not json", JSON.stringify({ tabs: [session("alpha")] }), JSON.stringify([1]), JSON.stringify(["/nope"]), JSON.stringify(["/cron", "/cron"])]) {
      await page.evaluate((value: string) => localStorage.setItem("tabs-warm:tester", value), stored);
      await page.goto(base + session("beta"));
      await page.getByText("beta user 30", { exact: true }).waitFor();
      expect(await tabNames(page)).toEqual(["beta chat"]);
    }
    expect(errors).toEqual([]);
  } finally {
    await close();
  }
}, 60_000);

test("narrow screens scroll the top strip and keep left placement on top", async () => {
  const { page, base, identity, errors, close } = await start(375, 700);
  try {
    // Tabs opened before identity resolves are kept once the owner's stored tabs load.
    const held = Promise.withResolvers<void>();
    identity.ready = held.promise;
    await page.goto(base + session("alpha"));
    for (const id of ["beta", "gamma", "delta", "epsilon"]) await go(page, session(id));
    held.resolve();
    await waitTabs(page, ["alpha chat", "beta chat", "gamma chat", "delta chat", "epsilon chat"]);
    await page.evaluate(() => localStorage.setItem("tab-placement", "left"));
    await page.reload();
    await page.getByText("epsilon user 3", { exact: true }).waitFor();
    const tablist = page.getByRole("tablist", { name: "Open tabs" });
    expect(await tablist.getAttribute("aria-orientation")).toBe("horizontal");
    const strip = (await page.getByRole("navigation", { name: "Tabs" }).boundingBox())!;
    const main = (await page.locator("main").boundingBox())!;
    expect(strip.y + strip.height).toBeLessThanOrEqual(main.y);
    expect(await tablist.evaluate((element: HTMLElement) => element.scrollWidth > element.clientWidth)).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
    const closer = page.getByRole("button", { name: "Close epsilon chat" });
    expect(await closer.isVisible()).toBe(true);
    expect((await closer.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    expect(errors).toEqual([]);
  } finally {
    await close();
  }
}, 30_000);
