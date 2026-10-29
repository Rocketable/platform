import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

for (const [width, height] of [[1280, 900], [390, 664]]) test(`web app update bar at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const current = await Bun.file(path.join(dist, "index.html")).text();
  const script = current.match(/src="(\/assets\/main-[^"]+)"/)![1];
  const styles = current.match(/href="(\/assets\/styles-[^"]+)"/)![1];
  // The index page the server answers with; undefined answers HTTP 500.
  let served: string | undefined = current;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname.startsWith("/assets/")) return new Response(Bun.file(path.join(dist, url.pathname)));
    if (!url.pathname.startsWith("/api/")) return served === undefined ? new Response("down", { status: 500 }) : new Response(served, { headers: { "Content-Type": "text/html" } });
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "web-update" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "web-update" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    let loads = 0;
    page.on("load", () => loads++);
    // A tab that becomes visible checks the index page again; wait for that answer to render.
    const recheck = async () => {
      const response = page.waitForResponse((r: { url(): string; request(): { resourceType(): string } }) => new URL(r.url()).pathname === "/" && r.request().resourceType() === "fetch");
      await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange", { bubbles: true })));
      await (await response).finished();
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    };
    const notice = page.getByText("A new version of RocketClaw is available.", { exact: true });
    const refresh = page.getByRole("button", { name: "Refresh", exact: true });
    // Narrow screens keep the tabs in a drawer behind the footer's Show sidebar button.
    const tabs = width < 768 ? page.getByRole("button", { name: "Show sidebar", exact: true }) : page.getByRole("tablist", { name: "Open tabs" }).locator("..");
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await tabs.waitFor();

    await recheck();
    expect(await notice.count()).toBe(0);

    served = current.replace(script, "/assets/main-next.js");
    await recheck();
    await notice.waitFor();
    expect(await page.getByRole("status").filter({ hasText: "A new version of RocketClaw is available." }).count()).toBe(1);
    // Nothing on the page may hide behind the bar, and the bar may not hide the theme toggle.
    const unobstructed = async (locator: { evaluate<T>(fn: (el: Element) => T): Promise<T> }) => locator.evaluate((el: Element) => {
      const box = el.getBoundingClientRect();
      return box.width > 0 && box.top >= 0 && box.left >= 0 && box.bottom <= innerHeight && box.right <= innerWidth && el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
    });
    for (const target of [refresh, page.getByRole("button", { name: /^Theme / }), tabs, page.locator("footer").getByRole("button", { name: "New session", exact: true })]) expect(await unobstructed(target)).toBe(true);
    await page.screenshot({ path: path.join(shots, `web-update-${width}.png`) });

    // A failed check keeps what the last successful check showed.
    served = undefined;
    await recheck();
    expect(await notice.count()).toBe(1);

    // Rolling back to this tab's build removes the bar.
    served = current;
    await recheck();
    expect(await notice.count()).toBe(0);

    served = current.replace(styles, "/assets/styles-next.css");
    await recheck();
    await notice.waitFor();
    await recheck();
    expect(loads).toBe(1);

    served = current;
    const reloaded = page.waitForEvent("load");
    await refresh.click();
    await reloaded;
    await tabs.waitFor();
    await recheck();
    expect(await notice.count()).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
