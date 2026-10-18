import { expect, test } from "bun:test";
import path from "node:path";
import type { Session } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");
const encode = (text: string) => btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

test("remote OpenCode sessions list, open in the relay frame, tag, and start", async () => {
  const { chromium: engine } = await import(playwright!);
  const rows: Session[] = [
    { id: "opencode:box:ses_a", preview: "Alpha work", server: "box", directory: "/srv/app", updatedAt: new Date().toISOString() },
    { id: "opencode:box:ses_off", preview: "Old work", server: "box", directory: "/srv/old", offline: true, tags: ["billing"] },
  ];
  const calls: { path: string; body: { id?: string; [key: string]: unknown } }[] = [];
  let failCreate = true;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") {
      calls.push({ path: url.pathname, body: { id: url.searchParams.get("id") ?? "" } });
      return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true, opencodeServers: { box: 4100 }, agentTags: { main: { tags: ["customer"] } } })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id?: string; [key: string]: unknown };
    calls.push({ path: url.pathname, body: input });
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "opencode" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/CreateSession":
        if (failCreate) { failCreate = false; return Response.json({ code: 14, message: "box did not answer" }, { status: 503 }); }
        return Response.json({ id: "opencode:box:ses_new" });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const base = `http://127.0.0.1:${server.port}`;

    // The frame loads OpenCode's own session page from the relay on the same host.
    await page.goto(`${base}/s/${encode("opencode:box:ses_a")}`);
    const frame = page.locator("iframe");
    await frame.waitFor({ state: "attached" });
    const relay = "http://127.0.0.1:4100";
    expect(await frame.getAttribute("src")).toBe(`${relay}/server/${encode(relay)}/session/ses_a`);
    await page.screenshot({ path: path.join(shots, "opencode-session.png") });
    expect(calls.filter(({ path, body }) => (path === "/api/History" || path === "/stream" || path === "/api/ListQueue") && body.id?.startsWith("opencode:"))).toEqual([]);

    // People type any tag on a remote session; tags already in use are suggested, agent tags are not offered.
    await page.getByRole("button", { name: "Open command palette" }).click();
    await page.getByPlaceholder("Type a command").fill("tag");
    expect(await page.getByRole("button", { name: "Sessions: Add tag customer" }).count()).toBe(0);
    await page.getByRole("button", { name: "Sessions: Add tag…" }).click();
    const tag = page.getByRole("dialog", { name: "Add tag" }).getByLabel("Tag");
    expect(await page.locator("datalist option").evaluateAll((options: Element[]) => options.map((option) => (option as HTMLOptionElement).value))).toEqual(["billing"]);
    await tag.fill("billing");
    await tag.press("Enter");
    for (let tries = 0; tries < 50 && !calls.some(({ path }) => path === "/api/UpdateSession"); tries++) await Bun.sleep(20);
    expect(calls.find(({ path }) => path === "/api/UpdateSession")?.body).toEqual({ id: "opencode:box:ses_a", tag: "billing" });

    // An offline server's session shows a notice instead of a frame, however it is opened.
    // Moving in-app from an open frame to an offline session still shows the notice.
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.getByRole("dialog", { name: "Go to session" }).getByText("Old work").click();
    await page.getByText("box is offline; this session reopens when it is reachable.").waitFor();
    expect(await frame.count()).toBe(0);
    await page.goto(`${base}/s/${encode("opencode:box:ses_gone")}`);
    await page.getByText("This OpenCode session is no longer available.").waitFor();

    // A new session takes a typed folder; a failed create shows its error and the next try opens the session.
    await page.getByRole("button", { name: "Open command palette" }).click();
    await page.getByPlaceholder("Type a command").fill("new on box");
    await page.getByRole("button", { name: "Sessions: New on box" }).click();
    const folder = page.getByPlaceholder("Folder on box, like /srv/app");
    await folder.fill("/srv/fresh");
    await folder.press("Enter");
    await page.getByText("box did not answer").waitFor();
    await folder.press("Enter");
    await page.waitForURL(`${base}/s/${encode("opencode:box:ses_new")}`);
    expect(calls.filter(({ path }) => path === "/api/CreateSession").map(({ body }) => body)).toEqual([{ opencodeServer: "box", directory: "/srv/fresh" }, { opencodeServer: "box", directory: "/srv/fresh" }]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
