import { afterEach, expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
let reportFailure: (() => void) | undefined;

afterEach(() => {
  reportFailure?.();
  reportFailure = undefined;
});

test("long chats open at the newest turns, load earlier ones near the top, and load linked older messages", async () => {
  const startedAt = performance.now();
  const elapsed = () => Math.round(performance.now() - startedAt);
  const { chromium: engine } = await import(playwright!);
  const turns = 120;
  const entry = (n: number) => [
    { role: "user", messageId: `${n}:0`, entryKey: String(n), itemId: `${n}:0`, inputId: `input-${n}`, turnId: `turn-${n}`, text: `user ${n}`, complete: true },
    { role: "assistant", messageId: `${n}:1`, entryKey: String(n), itemId: `${n}:1`, inputId: "", turnId: `turn-${n}`, text: `assistant ${n}`, complete: true },
  ];
  const view = (first: number, last: number, extra: object) => ({ messages: Array.from({ length: last - first + 1 }, (_, i) => entry(first + i)).flat(), origin: "", delegations: [], revision: "", reset: true, replacedKeys: [], removedKeys: [], entryKeys: Array.from({ length: last - first + 1 }, (_, i) => String(first + i)), running: false, terminal: "", start: String(first), more: first > 1, ...extra });
  const pages: { before?: string; from?: string; limit?: number }[] = [];
  const opened = Promise.withResolvers<void>();
  let fullReads = 0, streamRequests = 0, firstReadArrived = false;
  const streamOpened = Promise.withResolvers<void>();
  const steps = [{ name: "Chromium launch", atMs: 0 }];
  const mark = (name: string) => { steps.push({ name, atMs: elapsed() }); };
  let closeStartedAtMs: number | null = null;
  let closeFinishedAtMs: number | null = null;
  let serverStoppedAtMs: number | null = null;
  let browserDisconnectedAtMs: number | null = null;
  let pageClosedAtMs: number | null = null;
  let chromiumPID: number | undefined;
  const streamAbortedAtMs: number[] = [];
  const scroll: string[] = [], historyAlert: string[] = [], pageErrors: string[] = [], failedReads: string[] = [];
  const historyResponses = new Map<object, { body: string | null; status: number; atMs: number; finishedAtMs: number | null }>();
  const transcriptRead = (request: { url(): string }) => {
    const url = new URL(request.url());
    return url.pathname === "/api/History" && !url.searchParams.has("pull-request-discovery");
  };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") {
      streamRequests++;
      request.signal.addEventListener("abort", () => {
        streamAbortedAtMs.push(elapsed());
      }, { once: true });
      setTimeout(streamOpened.resolve, 200);
      return new Response(new ReadableStream({ start(controller) { controller.enqueue(`data: ${JSON.stringify({ conversationId: "long", revision: "" })}\n\n`); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "long", name: "Long chat" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "long-chat" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "long" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    // The desktop pull request watch reads history too; these reads are the transcript's.
    if (url.pathname === "/api/History" && url.searchParams.has("pull-request-discovery")) return Response.json({ messages: [], more: false });
    if (url.pathname === "/api/History") {
      const input = await request.json() as { revision?: string; limit?: number; before?: string; from?: string };
      if (!input.before && !input.revision) firstReadArrived = true;
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
    const file = path.join(dist, url.pathname);
    return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
  } });
  reportFailure = () => {
    let chromiumProcess = "not observed";
    if (chromiumPID !== undefined) {
      try {
        process.kill(chromiumPID, 0);
        chromiumProcess = "PID present";
      } catch (error) {
        chromiumProcess = (error as NodeJS.ErrnoException).code === "ESRCH" ? "PID gone" : String(error);
      }
    }
    console.error("Long chat test state", JSON.stringify({
      steps, elapsedMs: elapsed(), firstReadArrived, streamRequests, streamAbortedAtMs, fullReads, pages, historyResponses: [...historyResponses.values()], scroll, historyAlert, pageErrors, failedReads,
      cleanup: { closeStartedAtMs, closeFinishedAtMs, pageClosedAtMs, browserDisconnectedAtMs, serverStoppedAtMs, pendingRequests: server.pendingRequests, chromiumProcess },
    }));
  };
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  browser.once("disconnected", () => {
    browserDisconnectedAtMs = elapsed();
  });
  void browser.newBrowserCDPSession().then(async (session: {
    send(method: "SystemInfo.getProcessInfo"): Promise<{ processInfo: { type: string; id: number }[] }>;
    detach(): Promise<void>;
  }) => {
    try {
      const { processInfo } = await session.send("SystemInfo.getProcessInfo");
      chromiumPID = processInfo.find(({ type }) => type === "browser")?.id;
    } finally {
      await session.detach();
    }
  }).catch(() => {});
  try {
    mark("create page");
    const page = await browser.newPage({ viewport: { width: 1100, height: 650 } });
    page.once("close", () => {
      pageClosedAtMs = elapsed();
    });
    page.on("pageerror", (error: Error) => pageErrors.push(error.message));
    page.on("requestfailed", (request: { url(): string; postData(): string | null; failure(): { errorText: string } }) => {
      if (transcriptRead(request)) failedReads.push(`${request.postData()}: ${request.failure().errorText}`);
    });
    page.on("response", (response: { request(): { url(): string; postData(): string | null }; status(): number }) => {
      const request = response.request();
      if (transcriptRead(request)) historyResponses.set(request, { body: request.postData(), status: response.status(), atMs: elapsed(), finishedAtMs: null });
    });
    page.on("requestfinished", (request: object) => {
      const response = historyResponses.get(request);
      if (response) response.finishedAtMs = elapsed();
    });
    page.on("console", (message: { text(): string }) => {
      const text = message.text();
      if (text.startsWith("long-chat-scroll ")) scroll.push(`${elapsed()} ms: ${text}`);
      if (text.startsWith("long-chat-history-alert ")) historyAlert.push(`${elapsed()} ms: ${text}`);
    });
    mark("navigate to long chat");
    await page.goto(`http://127.0.0.1:${server.port}/s/bG9uZw`);
    // A slow first read shows that the chat is loading, not an empty chat.
    mark("show loading placeholder, not empty chat");
    await page.getByText("Loading conversation…").waitFor();
    expect(await page.getByText("Send a message to start the conversation.").count()).toBe(0);
    mark("render assistant 120 from the initial read");
    opened.resolve();
    await page.getByText(`assistant ${turns}`).waitFor();
    mark("show only the newest page after one full read");
    expect(await page.locator('[data-slot="message"][data-message-id]').count()).toBe(100);
    expect(pages).toEqual([]);
    expect(fullReads).toBe(1);

    // Typing redraws only the composer; redrawing a long transcript per keystroke made input lag.
    // Background polls may still redraw it a few times, so this bounds the count well below one per key.
    mark("type in composer and check redraw count");
    await page.evaluate(() => {
      const probe = window as unknown as { transcriptRenders: number };
      probe.transcriptRenders = 0;
      window.URLSearchParams = class extends URLSearchParams {
        constructor(init?: ConstructorParameters<typeof URLSearchParams>[0]) {
          super(init);
          probe.transcriptRenders++;
        }
      };
      const viewport = document.querySelector("#transcript-scroll")!;
      document.addEventListener("wheel", (event) => {
        console.debug("long-chat-scroll wheel", viewport.contains(event.target as Node), viewport.scrollTop);
        viewport.addEventListener("scroll", () => {
          console.debug("long-chat-scroll position", viewport.scrollTop, viewport.clientHeight, viewport.scrollHeight);
        }, { once: true });
      }, { capture: true, once: true, passive: true });
    });
    const typed = "typing in a long chat redraws only the composer";
    await page.locator("textarea").pressSequentially(typed);
    expect(await page.locator("textarea").inputValue()).toBe(typed);
    expect(await page.evaluate(() => (window as unknown as { transcriptRenders: number }).transcriptRenders)).toBeLessThan(typed.length / 2);
    // Naming a tab group redraws only its field too; tab state belongs to the whole app.
    mark("open tab group menu");
    await page.getByRole("tab", { name: "Long chat", exact: true }).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Add to group" }).click();
    await page.getByRole("menuitem", { name: "New group" }).click();
    mark("type group name and check redraw count");
    const group = page.getByRole("textbox", { name: "Group name" });
    await group.waitFor();
    await page.evaluate(() => { (window as unknown as { transcriptRenders: number }).transcriptRenders = 0; });
    const named = "Incident Oct 12th";
    await group.pressSequentially(named);
    expect(await group.inputValue()).toBe(named);
    expect(await page.evaluate(() => (window as unknown as { transcriptRenders: number }).transcriptRenders)).toBeLessThan(named.length / 2);
    await page.keyboard.press("Escape");
    mark("show new tab group name");
    await page.getByRole("button", { name: named, exact: true }).waitFor();

    mark("enable CPU throttling for history paging");
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 8 });
    mark("hover over transcript");
    await page.locator("#transcript-scroll").hover();
    mark("wheel to the top");
    await page.mouse.wheel(0, -100_000);
    mark("attach user 21 after scrolling");
    await page.getByText("user 21", { exact: true }).waitFor({ state: "attached" });
    mark("check earlier page and retained user 71 anchor");
    expect(pages).toEqual([{ before: "71", limit: 50 }]);
    // The turns already on screen stay put; the page grows above them.
    expect(await page.evaluate(() => {
      const element = document.querySelector("#transcript-scroll")!;
      const viewport = element.getBoundingClientRect();
      const turn = document.querySelector('[data-message-id="71:0"]')!.getBoundingClientRect();
      console.debug("long-chat-scroll prepend anchor", JSON.stringify({ scrollTop: element.scrollTop, viewport: [viewport.top, viewport.bottom], anchor: [turn.top, turn.bottom] }));
      return turn.bottom > viewport.top && turn.top < viewport.bottom;
    })).toBe(true);
    expect(await page.getByText("user 20", { exact: true }).count()).toBe(0);

    mark("navigate to linked message 5:0");
    const linkedTurnInViewport = await page.evaluateHandle(() => {
      let alertRendered = false;
      const alertObserver = new MutationObserver(() => {
        const rendered = [...document.querySelectorAll('[role="alert"]')].some((alert) => alert.textContent === "Earlier history unavailable");
        if (rendered === alertRendered) return;
        alertRendered = rendered;
        console.debug(`long-chat-history-alert ${rendered ? "rendered" : "cleared"}`);
        if (!rendered) alertObserver.disconnect();
      });
      alertObserver.observe(document.body, { childList: true, subtree: true, characterData: true });
      history.pushState(null, "", "/s/bG9uZw?message=5%3A0");
      window.dispatchEvent(new PopStateEvent("popstate"));
      let lastPosition: string | undefined;
      return () => {
        const element = document.querySelector("#transcript-scroll");
        const viewport = element?.getBoundingClientRect();
        const target = document.querySelector('[data-message-id="5:0"]')?.getBoundingClientRect();
        const inViewport = !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
        const position = !viewport ? "no viewport" : !target ? "not rendered" : inViewport ? "in viewport" : "outside viewport";
        if (position !== lastPosition) {
          const loadingEarlier = [...element?.querySelectorAll('[role="status"]') ?? []].some((node) => node.textContent === "Loading earlier messages…");
          console.debug("long-chat-scroll linked turn", JSON.stringify({ position, loadingEarlier, scrollTop: element?.scrollTop, viewport: [viewport?.top, viewport?.bottom], anchor: [target?.top, target?.bottom] }));
          lastPosition = position;
        }
        return inViewport;
      };
    });
    mark("wait for linked message 5:0 in viewport");
    await page.waitForFunction((inViewport: () => boolean) => inViewport(), linkedTurnInViewport);
    expect(pages[1]).toEqual({ before: "21", from: "5", limit: undefined });
    mark("show the earlier-history error");
    await page.getByText("Earlier history unavailable", { exact: true }).waitFor();
    mark("check loaded turns and composer after the history error");
    expect(pages[2]).toEqual({ before: "5", from: undefined, limit: 50 });
    expect(await page.getByText("user 5", { exact: true }).count()).toBe(1);
    expect(await page.getByText(`assistant ${turns}`, { exact: true }).count()).toBe(1);
    expect(await page.locator("textarea").inputValue()).toBe(typed);
    mark("assert Chromium is connected before cleanup");
    expect(browserDisconnectedAtMs, "Chromium disconnected before cleanup").toBeNull();
    mark("assertions complete");
  } finally {
    closeStartedAtMs = elapsed();
    await browser.close();
    closeFinishedAtMs = elapsed();
    server.stop(true);
    serverStoppedAtMs = elapsed();
  }
  reportFailure = undefined;
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
    const file = path.join(dist, url.pathname);
    return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.port}/s/bm9uZQ`);
    await page.getByText("This chat doesn't exist.").waitFor();
    expect(await page.getByRole("link", { name: "Start a new chat" }).getAttribute("href")).toBe("/");
    expect(await page.getByText("Loading conversation…").count()).toBe(0);
    expect(await page.getByText("conversation is not recorded").count()).toBe(0);
    expect(await page.getByRole("textbox", { name: "Message or /command" }).count()).toBe(0);
  } finally {
    await browser.close();
    server.stop(true);
  }
});
