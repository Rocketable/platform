import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const at = (id: string) => `/s/${btoa(id).replace(/=+$/, "")}`;

async function serve() {
  const sessions = [{ id: "alpha", name: "Alpha chat", running: true }, { id: "beta", name: "Beta chat" }];
  return Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "tabs" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "tabs" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
    if (url.pathname === "/api/History") return Response.json({ messages: [], origin: "", revision: "r", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "", delegations: [] });
    if (url.pathname === "/api/CreateSession") {
      sessions.push({ id: "created", name: "Created chat" });
      return Response.json({ id: "created" });
    }
    if (url.pathname === "/api/Prompt") return Response.json({ privateText: "" });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
}

for (const width of [1280, 375]) test(`minimal tabs at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const server = await serve();
  const base = `http://127.0.0.1:${server.port}`;
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 800 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const strip = page.getByRole("tablist", { name: "Open tabs" });
    const tabs = strip.getByRole("tab");
    const names = async () => (await tabs.allTextContents() as string[]).map((text) => text.trim());
    const selected = () => strip.locator('[role="tab"][aria-selected="true"]').textContent();
    const pick = async (dialog: string, name: RegExp) => {
      await page.getByRole("dialog", { name: dialog }).getByRole("button", { name }).click();
      await page.getByRole("dialog").waitFor({ state: "detached" });
    };
    const goSession = async (name: string) => {
      await page.keyboard.press("Control+p");
      await pick("Go to session", new RegExp(name));
    };
    const command = async (name: string) => {
      await page.getByRole("button", { name: "Open command palette" }).click();
      await pick("Run command", new RegExp(name));
    };

    // A deep link opens a tab; every later navigation opens or focuses one.
    await page.goto(`${base}${at("alpha")}`);
    await page.getByRole("tab", { name: /Alpha chat/ }).waitFor();
    expect(await names()).toEqual(["Alpha chat"]);
    expect(await strip.getByRole("img", { name: "Turn running" }).count()).toBe(1);
    await command("Cron: Dashboard");
    await page.waitForURL("**/cron");
    await goSession("Beta chat");
    await page.waitForURL(`**${at("beta")}`);
    expect(await names()).toEqual(["Alpha chat", "Cron", "Beta chat"]);
    await goSession("Alpha chat");
    await page.waitForURL(`**${at("alpha")}`);
    expect(await names()).toEqual(["Alpha chat", "Cron", "Beta chat"]);
    expect(await selected()).toBe("Alpha chat");

    // Keyboard: arrows move focus, Enter activates, Escape on a page closes its tab.
    await tabs.first().focus();
    await page.keyboard.press("ArrowRight");
    expect(await tabs.nth(1).evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    await page.keyboard.press("ArrowLeft");
    await page.keyboard.press("End");
    expect(await tabs.nth(2).evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    await page.keyboard.press("Home");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("Enter");
    await page.waitForURL("**/cron");
    expect(await strip.getByRole("button", { name: "Close Cron" }).count()).toBe(1);
    await page.locator("body").press("Escape");
    await page.waitForURL(`**${at("beta")}`);
    expect(await names()).toEqual(["Alpha chat", "Beta chat"]);
    await command("List Agents");
    await page.waitForURL("**/agents");
    await (width >= 768 ? page.getByRole("button", { name: "Close", exact: true }) : strip.getByRole("button", { name: "Close Agents" })).click();
    await page.waitForURL(`**${at("beta")}`);
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.waitForURL("**/search");
    expect(await selected()).toBe("Search");
    await strip.getByRole("button", { name: "Close Search" }).click();
    await page.waitForURL(`**${at("beta")}`);
    expect(await names()).toEqual(["Alpha chat", "Beta chat"]);

    // Reload restores the tabs and the active tab; the URL always names the active tab.
    await page.reload();
    await page.getByRole("tab", { name: /Beta chat/ }).waitFor();
    expect(await names()).toEqual(["Alpha chat", "Beta chat"]);
    expect(await selected()).toBe("Beta chat");

    // Placement toggles from the strip and the palette and survives reload; narrow screens stay on top.
    await page.getByRole("button", { name: "Move tabs to left" }).click();
    expect(await strip.getAttribute("aria-orientation")).toBe(width >= 768 ? "vertical" : "horizontal");
    await page.reload();
    await page.getByRole("tab", { name: /Beta chat/ }).waitFor();
    expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("left");
    expect(await strip.getAttribute("aria-orientation")).toBe(width >= 768 ? "vertical" : "horizontal");
    const box = (await strip.boundingBox())!;
    if (width >= 768) expect(box.height).toBeGreaterThan(box.width);
    else expect(box.y).toBeLessThan(80);
    await command("Tabs: Toggle top/left placement");
    expect(await strip.getAttribute("aria-orientation")).toBe("horizontal");
    expect(await page.evaluate(() => localStorage.getItem("tab-placement"))).toBe("top");
    expect(await strip.evaluate((node: HTMLElement) => getComputedStyle(node).overflowX)).toBe("auto");

    // Closing the active tab activates its neighbour; closing the last lands on the composer.
    await strip.getByRole("button", { name: "Close Beta chat" }).press("Enter");
    await page.waitForURL(`**${at("alpha")}`);
    await page.waitForFunction(() => document.activeElement!.matches('[role="tab"][aria-selected="true"]'), undefined, { timeout: 2_000 });
    await strip.getByRole("button", { name: "Close Alpha chat" }).click();
    await page.waitForURL(`${base}/`);
    expect(await names()).toEqual(["New session"]);
    await strip.getByRole("button", { name: "Close New session" }).click();
    expect(await names()).toEqual(["New session"]);
    expect(page.url()).toBe(`${base}/`);

    // Creating a session turns the composer tab into the session's tab.
    await goSession("Beta chat");
    expect(await page.evaluate(() => document.activeElement!.getAttribute("role"))).not.toBe("tab");
    await page.getByRole("tab", { name: "New session" }).click();
    await page.waitForURL(`${base}/`);
    await page.locator("textarea").fill("hello");
    await page.locator("textarea").press("Enter");
    await page.waitForURL(`**${at("created")}`);
    await page.getByRole("tab", { name: "Created chat" }).waitFor();
    expect(await names()).toEqual(["Created chat", "Beta chat"]);

    // Corrupt or mismatched storage resets to the current location.
    for (const stored of ["{bad", '{"tabs":["/cron"]}', "[1]"]) {
      await page.evaluate((value: string) => localStorage.setItem("tabs-minimal:tester", value), stored);
      await page.goto(`${base}${at("alpha")}`);
      await page.getByRole("tab", { name: /Alpha chat/ }).waitFor();
      expect(await names()).toEqual(["Alpha chat"]);
    }

    // The active tab scrolls into view when the strip overflows.
    await page.evaluate(() => localStorage.setItem("tabs-minimal:tester", JSON.stringify(["/", "/search", "/cron", "/agents", "/skills", "/config"])));
    await page.goto(`${base}/config`);
    await page.getByRole("tab", { name: "Settings" }).waitFor();
    await page.waitForFunction(() => document.querySelector('[aria-label="Open tabs"] [aria-selected="true"]')!.getBoundingClientRect().right <= innerWidth, undefined, { timeout: 5_000 });
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 60_000);
