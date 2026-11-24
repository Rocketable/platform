import { expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";
import { encodeSessionId as encode } from "./session-id";
import type { Session } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const href = (id: string) => `/s/${encode(id)}`;

function serveRingable(options: { cors?: boolean; rows: () => Session[]; tabs: () => object }) {
  const cors: Record<string, string> = options.cors ? { "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Headers": "*", "Access-Control-Allow-Methods": "*" } : {};
  const stream = { ...cors, "Content-Type": "text/event-stream" };
  const watchers = new Set<ReadableStreamDefaultController>();
  const counts = { listings: 0, tabs: 0 };
  let held: PromiseWithResolvers<void> | undefined, refusing = false;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (request.method === "OPTIONS") return new Response(null, { headers: cors });
    if (url.pathname === "/listing") {
      if (refusing) return new Response(null, { status: 503, headers: cors });
      let watcher: ReadableStreamDefaultController;
      return new Response(new ReadableStream({
        start(controller) { watcher = controller; watchers.add(controller); controller.enqueue("data: {}\n\n"); },
        cancel() { watchers.delete(watcher); },
      }), { headers: stream });
    }
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: stream });
    if (url.pathname === "/api/ListSessions") {
      counts.listings++;
      await held?.promise;
      return new Response(`data: ${JSON.stringify({ sessions: options.rows(), owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: stream });
    }
    if (!options.cors && !url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    if (url.pathname === "/api/Tabs") counts.tabs++;
    const replies: Record<string, unknown> = {
      "/api/Protocol": { protoSha256: "listing-ring" }, "/api/Identity": { username: "tester" }, "/api/ListAgents": { agents: [{ name: "main" }], currentAgent: "main" },
      "/api/ListQueue": { items: [] }, "/api/Tabs": options.tabs(), "/api/ListConfig": { config: { workspace: "ring" } }, "/api/ListCronJobs": { jobs: [] },
      "/api/History": { origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: "1", reset: true, entryKeys: [], running: false, start: "0", more: false },
    };
    return Response.json(replies[url.pathname] ?? {}, { headers: cors });
  } });
  return {
    server, counts, url: `http://127.0.0.1:${server.port}`,
    ring: () => { for (const watcher of watchers) watcher.enqueue("data: {}\n\n"); },
    hold: () => { held = Promise.withResolvers<void>(); },
    release: () => { held?.resolve(); held = undefined; },
    refuse: () => { refusing = true; for (const watcher of watchers) watcher.close(); watchers.clear(); },
  };
}

async function settled(read: () => number) {
  for (let last = read(); ; last = read()) {
    await Bun.sleep(400);
    if (read() === last) return last;
  }
}

async function advance(page: { clock: { runFor(ms: number): Promise<void> } }, done: () => boolean, ms: number) {
  for (let spent = 0; spent < ms && !done(); spent += 100) {
    await page.clock.runFor(100);
    await Bun.sleep(50);
  }
  return done();
}

async function until(check: () => boolean, ms = 3_000) {
  for (const started = Date.now(); !check();) {
    if (Date.now() - started > ms) throw new Error("timed out");
    await Bun.sleep(10);
  }
}

test("a ring fetches the listing at once, rings during a fetch become one more, and the poll still runs when the stream is down", async () => {
  const { chromium: engine } = await import(playwright!);
  let rows: Session[] = [{ id: "chat", agent: "main", preview: "Chat work", running: true }];
  const tabs = { spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: href("chat"), space: "main" }], groups: [], space: "main", last: {} }, revision: "1", shared: [] };
  const local = serveRingable({ rows: () => rows, tabs: () => tabs });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.clock.install();
    await page.goto(`${local.url}${href("chat")}`);
    await page.getByRole("tab", { name: "Chat work" }).waitFor();
    await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1_000));
    const loaded = local.counts.listings;
    await advance(page, () => local.counts.listings > loaded, 2_000);
    const before = await settled(() => local.counts.listings);

    local.ring();
    expect(await advance(page, () => local.counts.listings === before + 1, 1_500)).toBe(true);

    await settled(() => local.counts.listings);
    local.hold();
    local.ring();
    expect(await advance(page, () => local.counts.listings === before + 2, 1_500)).toBe(true);
    local.ring();
    local.ring();
    local.ring();
    await Bun.sleep(200);
    local.release();
    expect(await advance(page, () => local.counts.listings === before + 3, 1_500)).toBe(true);
    expect(await settled(() => local.counts.listings)).toBe(before + 3);

    local.refuse();
    rows = rows.map((row) => ({ ...row, running: false }));
    expect(await advance(page, () => local.counts.listings === before + 4, 3_000)).toBe(true);
  } finally {
    await browser.close();
    local.server.stop(true);
  }
}, 30_000);

test("a ring from another RocketClaw fetches that server's listing and tabs", async () => {
  const { chromium: engine } = await import(playwright!);
  const tabs = { spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: href("chat"), space: "main" }], groups: [], space: "main", last: {} }, revision: "1", shared: [] };
  const local = serveRingable({ rows: () => [{ id: "chat", agent: "main", preview: "Chat work" }], tabs: () => tabs });
  const remote = serveRingable({ cors: true, rows: () => [{ id: "far", agent: "main", preview: "Far work" }], tabs: () => ({ spaces: [{ id: "lab", name: "Lab" }], state: { tabs: [{ path: href("far"), space: "lab" }], groups: [], space: "lab", last: {}, sharedOrder: [] }, shared: [], revision: 1, closed: [] }) });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.addInitScript((url: string) => document.addEventListener("readystatechange", () => {
      if (document.getElementById("rocketclaws")) return;
      document.head.append(Object.assign(document.createElement("script"), { id: "rocketclaws", type: "application/json", textContent: JSON.stringify([{ name: "work", url }]) }));
    }), remote.url);
    await page.clock.install();
    await page.goto(`${local.url}${href("chat")}`);
    await page.getByRole("tab", { name: "Far work" }).waitFor();
    await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1_000));
    const loaded = remote.counts.listings;
    await advance(page, () => remote.counts.listings > loaded, 2_000);
    const listings = await settled(() => remote.counts.listings), saved = await settled(() => remote.counts.tabs);

    remote.ring();
    await until(() => remote.counts.tabs === saved + 1);
    expect(await advance(page, () => remote.counts.listings === listings + 1, 1_500)).toBe(true);
  } finally {
    await browser.close();
    local.server.stop(true);
    remote.server.stop(true);
  }
}, 30_000);
