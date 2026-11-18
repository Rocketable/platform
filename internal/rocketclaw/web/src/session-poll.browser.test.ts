import { expect, test } from "bun:test";
import path from "node:path";
import type { Session, SessionBatch } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

// Polls after the first send the version they hold and get only changed rows and the IDs removed since; the list
// keeps the rest in the server's order. A reload holds the saved version, so it is not sent every row again.
test("session list polls ask for changes since the version they hold", async () => {
  const { chromium: engine } = await import(playwright!);
  const row = (id: string, preview: string, updatedAt: string): Session => ({ id, preview, title: "", agent: "main", updatedAt });
  const listings: Record<string, { sessions: Session[]; version: string; removed?: string[] }> = {
    "": { sessions: [row("a", "Alpha", "2026-11-01T10:00:00Z"), row("b", "Beta", "2026-11-01T09:00:00Z"), row("c", "Gamma", "2026-11-01T08:00:00Z")], version: "v1" },
    v1: { sessions: [row("b", "Beta again", "2026-11-01T10:00:00.5Z")], version: "v2", removed: ["c"] },
    v2: { sessions: [], version: "v3" },
  };
  const started = performance.now();
  const polls: { since: string; elapsedMs: number }[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") {
      const held = url.searchParams.get("since") ?? "";
      polls.push({ since: held, elapsedMs: Math.round(performance.now() - started) });
      const { sessions, version, removed } = listings[held] ?? listings.v2;
      const done: SessionBatch = { upstreamSuccess: true, version, ...held && { since: held, removed } };
      return new Response([{ owner: "tester" }, ...sessions.map((session) => ({ sessions: [session] })), done].map((batch) => `data: ${JSON.stringify(batch)}\n\n`).join("") + "event: complete\ndata: {}\n\n", { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "poll" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const errors: string[] = [];
    const finished: string[] = [];
    const failed: { since: string; error: string }[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("requestfinished", (request: { url(): string }) => {
      const url = new URL(request.url());
      if (url.pathname === "/api/ListSessions") finished.push(url.searchParams.get("since") ?? "");
    });
    page.on("requestfailed", (request: { url(): string; failure(): { errorText: string } }) => {
      const url = new URL(request.url());
      if (url.pathname === "/api/ListSessions") failed.push({ since: url.searchParams.get("since") ?? "", error: request.failure().errorText });
    });
    await page.goto(`http://127.0.0.1:${server.port}`);
    for (let tries = 0; tries < 400 && !polls.some(({ since }) => since === "v2"); tries++) await Bun.sleep(20);
    expect(polls.slice(0, 3).map(({ since }) => since), `Session poll state: ${JSON.stringify({ polls, finished, failed, pageErrors: errors })}`).toEqual(["", "v1", "v2"]);

    await page.keyboard.press("ControlOrMeta+p");
    const search = page.getByPlaceholder("Search sessions and commands");
    await search.waitFor();
    const sessions = page.getByRole("button", { name: /^(Alpha|Beta|Gamma)/ });
    await page.getByRole("button", { name: /^Beta again/ }).waitFor();
    expect(await sessions.allTextContents()).toEqual([expect.stringMatching(/^Beta again/), expect.stringMatching(/^Alpha/)]);

    const polled = polls.length;
    await page.reload();
    for (let tries = 0; tries < 400 && polls.length < polled + 2; tries++) await Bun.sleep(20);
    expect(polls.slice(polled).map(({ since }) => since).filter((version) => version === "")).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
