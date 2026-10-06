import { expect, test } from "bun:test";
import path from "node:path";
import { SourceMap } from "node:module";

const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

for (const rate of [undefined, 0, 1]) test(`Sentry errors and tracing at sample rate ${rate ?? "disabled"}`, async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const envelopes: string[] = [];
  const spans: { name: string; attributes: { "sentry.op"?: { value: string }; "url.path"?: { value: string } } }[] = [];
  const tracing = Promise.withResolvers<void>();
  const interaction = Promise.withResolvers<void>();
  const reported = Promise.withResolvers<void>();
  const searchStarted = Promise.withResolvers<void>();
  const searchCancelled = Promise.withResolvers<void>();
  const events: { exception?: { values: { type?: string; value: string; stacktrace?: { frames: { filename: string; lineno: number; colno: number }[] }; mechanism?: { type: string } }[] }; logentry?: { formatted: string }; debug_meta?: { images: { debug_id: string }[] } }[] = [];
  const headers: Headers[] = [];
  let breakReact = false;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/api/1/envelope/") {
      envelopes.push(await request.text());
      for (const line of envelopes.at(-1)!.split("\n")) {
        const item: typeof events[number] & { items?: typeof spans } = JSON.parse(line);
        if (item.items) spans.push(...item.items);
        if (item.exception || item.logentry) events.push(item);
      }
      if (["pageload", "navigation", "http.client"].every((op) => spans.some((span) => span.attributes["sentry.op"]?.value === op))) tracing.resolve();
      if (["ui.action.click", "ui.long_task"].every((op) => spans.some((span) => span.attributes["sentry.op"]?.value === op))) interaction.resolve();
      if (["browser exception", "promise rejection", "console failure", "request failure", "clipboard failure"].every((message) => events.some((event) => event.logentry?.formatted === message || event.exception?.values.some((exception) => exception.value === message))) && events.some((event) => event.exception?.values.some((exception) => exception.type === "React ErrorBoundary TypeError"))) reported.resolve();
      return Response.json({});
    }
    if (url.pathname.startsWith("/api/")) headers.push(request.headers);
    if (url.pathname === "/api/SearchMessages") {
      const input = await request.json();
      if (input.query === "cancelled request") {
        request.signal.addEventListener("abort", () => searchCancelled.resolve(), { once: true });
        searchStarted.resolve();
        await searchCancelled.promise;
      }
      return Response.json({ matches: [] });
    }
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "sentry-test" });
    if (url.pathname === "/api/ListCronJobs") return Response.json({ message: "request failure", code: 13 }, { status: 500 });
    if (url.pathname === "/api/ListSkills") return Response.json({ skills: breakReact ? "invalid rows" : [] }); // Force a real React render failure.
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
    if (url.pathname === "/api/History") return Response.json({ messages: [{ role: "assistant", text: "private-chat-text", entryKey: "entry", itemId: "item", inputId: "", turnId: "turn", complete: true }], entryKeys: ["entry"], replacedKeys: [], removedKeys: [], revision: "one", reset: true, running: false, delegations: [] });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    if (await file.exists()) return new Response(file);
    let html = await Bun.file(path.join(dist, "index.html")).text();
    if (rate !== undefined) html = html.replace("</head>", `<script id="sentry-config" type="application/json">${JSON.stringify({ dsn: `http://public@127.0.0.1:${server.port}/1`, environment: "browser-test", traces_sample_rate: rate })}</script></head>`);
    return new Response(html, { headers: { "Content-Type": "text/html" } });
  } });
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/?q=private-search`);
    await page.evaluate(() => { document.cookie = "private-cookie=secret"; });
    await page.getByRole("button", { name: "Search sessions" }).waitFor();
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.waitForURL("**/search");
    await page.getByRole("textbox", { name: "Search sessions" }).waitFor();
    await page.getByRole("textbox", { name: "Search sessions" }).fill("cancelled request");
    await searchStarted.promise;
    await page.getByRole("textbox", { name: "Search sessions" }).fill("next search");
    await searchCancelled.promise;
    if (rate === 1) {
      await tracing.promise;
      // Exercise a click after pageload/navigation have ended, not just startup stalls.
      const search = page.getByRole("textbox", { name: "Search sessions" });
      await search.evaluate((node: HTMLElement) => node.addEventListener("click", () => {
        const until = performance.now() + 150;
        while (performance.now() < until) {}
      }, { once: true }));
      await search.click();
      await interaction.promise;
      const data = envelopes.join("\n");
      expect(spans.some((span) => span.attributes["url.path"]?.value === "/search" && span.attributes["sentry.op"]?.value === "navigation")).toBe(true);
      expect(data).toContain("browser-test");
      expect(data).not.toContain("private-search");
      expect(data).not.toContain('"type":"replay_event"');
      expect(data).not.toContain('"type":"replay_recording"');
    }
    await page.addScriptTag({ content: `
      console.log("private-console-log");
      console.warn("private-console-warning");
      console.error("console failure");
      setTimeout(() => { throw new Error("browser exception"); }, 0);
      void Promise.reject(new Error("promise rejection"));
      history.pushState({}, "", "/cron?q=private-search");
      dispatchEvent(new PopStateEvent("popstate"));
    ` });
    await page.getByText("request failure", { exact: true }).waitFor();
    await page.evaluate(() => { history.pushState({}, "", "/s/c2Vzc2lvbg"); dispatchEvent(new PopStateEvent("popstate")); });
    await page.getByText("private-chat-text", { exact: true }).waitFor();
    await page.evaluate(() => { Object.defineProperty(navigator, "clipboard", { value: { writeText: async () => { throw new Error("clipboard failure"); } } }); });
    const copy = page.getByRole("button", { name: "Copy message", exact: true });
    await page.getByText("private-chat-text", { exact: true }).hover();
    await copy.click();
    breakReact = true;
    await page.goto(`http://127.0.0.1:${server.port}/skills?q=private-search`);
    if (rate !== undefined) {
      await reported.promise;
      expect(events.filter((event) => event.exception?.values.some((exception) => exception.value === "browser exception"))).toHaveLength(1);
      expect(events.some((event) => event.exception?.values.some((exception) => exception.type === "React ErrorBoundary TypeError" && exception.stacktrace?.frames.length))).toBe(true);
      const react = events.find((event) => event.exception?.values.some((exception) => exception.mechanism?.type === "auto.function.react.error_handler"))!;
      const frames = react.exception!.values.find((exception) => exception.type === "TypeError")!.stacktrace!.frames;
      const bundle = path.basename(new URL(frames.at(-1)!.filename).pathname);
      const map = await Bun.file(path.resolve(import.meta.dir, "../../../../.tmp/sentry-sourcemaps", bundle + ".map")).json();
      expect(react.debug_meta!.images.some((image) => image.debug_id === map.debugId)).toBe(true);
      const sourceMap = new SourceMap(map);
      expect(frames.some((frame) => {
        const entry = sourceMap.findEntry(frame.lineno - 1, frame.colno - 1);
        return "originalSource" in entry && entry.originalSource.endsWith("/ui.tsx") && map.sourcesContent[map.sources.indexOf(entry.originalSource)].split("\n")[entry.originalLine].includes("rows.map");
      })).toBe(true);
      expect(await Bun.file(path.join(dist, "assets", bundle + ".map")).exists()).toBe(false);
      const data = envelopes.join("\n");
      for (const privateValue of ["private-search", "private-cookie", "private-chat-text", "private-console-log", "private-console-warning", '"type":"log"', '"type":"replay_event"', '"type":"replay_recording"']) expect(data).not.toContain(privateValue);
      expect(events.every((event) => !("user" in event) && !("breadcrumbs" in event))).toBe(true);
      expect(events.some((event) => event.exception?.values.some((exception) => exception.type === "AbortError"))).toBe(false);
    }
    await page.goto("about:blank"); // Flush the SDK's pending browser spans.
    if (rate !== 1) expect(envelopes.join("\n")).not.toContain('"type":"span"');
    if (rate === undefined) expect(envelopes).toHaveLength(0);
    expect(headers.length).toBeGreaterThan(0);
    expect(headers.every((header) => !header.has("sentry-trace") && !header.has("baggage"))).toBe(true);
    expect(errors).toContain("browser exception");
    expect(errors).toContain("promise rejection");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
