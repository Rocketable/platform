import { expect, test } from "bun:test";
import path from "node:path";
import type { Session, SessionBatch } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

// Polls after the first send the version they hold and get only changed rows; the list keeps the rest in the
// server's order and drops what the order no longer names.
test("session list polls ask for changes since the version they hold", async () => {
  const { chromium: engine } = await import(playwright!);
  const row = (id: string, preview: string): Session => ({ id, preview, title: "", agent: "main", updatedAt: new Date().toISOString() });
  const listings: Record<string, { sessions: Session[]; version: string; order: string[] }> = {
    "": { sessions: [row("a", "Alpha"), row("b", "Beta"), row("c", "Gamma")], version: "v1", order: ["a", "b", "c"] },
    v1: { sessions: [row("b", "Beta again")], version: "v2", order: ["a", "b"] },
    v2: { sessions: [], version: "v3", order: ["a", "b"] },
  };
  const since: string[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") {
      const held = url.searchParams.get("since") ?? "";
      since.push(held);
      const { sessions, version, order } = listings[held] ?? listings.v2;
      const done: SessionBatch = { sessions: [], owner: "tester", upstreamSuccess: true, opencodeServers: {}, version, order };
      return new Response([...sessions.map((session) => ({ sessions: [session], owner: "tester", upstreamSuccess: false })), done].map((batch) => `data: ${JSON.stringify(batch)}\n\n`).join("") + "event: complete\ndata: {}\n\n", { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "poll" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, placement: "TOP" } });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}`);
    for (let tries = 0; tries < 400 && !since.includes("v2"); tries++) await Bun.sleep(20);
    expect(since.slice(0, 3)).toEqual(["", "v1", "v2"]);

    await page.keyboard.press("ControlOrMeta+p");
    const search = page.getByPlaceholder("Search sessions and commands");
    await search.waitFor();
    const sessions = page.getByRole("button", { name: /^(Alpha|Beta|Gamma)/ });
    await page.getByRole("button", { name: /^Beta again/ }).waitFor();
    expect(await sessions.allTextContents()).toEqual([expect.stringMatching(/^Alpha(?!.*again)/), expect.stringMatching(/^Beta again/)]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
