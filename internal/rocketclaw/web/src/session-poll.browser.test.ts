import { expect, test } from "bun:test";
import { once } from "node:events";
import path from "node:path";
import { unpacked } from "./test-browsers";
import type { SavedListing } from "./session-list";
import type { Session, SessionBatch } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
type SnapshotCommits = { snapshotCommits: Record<string, PromiseWithResolvers<void>[]> };

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
  const requested = new EventTarget();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") {
      const held = url.searchParams.get("since") ?? "";
      polls.push({ since: held, elapsedMs: Math.round(performance.now() - started) });
      requested.dispatchEvent(new Event(String(polls.length)));
      const { sessions, version, removed } = listings[held] ?? listings.v2;
      const done: SessionBatch = { upstreamSuccess: true, version, ...held && { since: held, removed } };
      return new Response([{ owner: "tester" }, ...sessions.map((session) => ({ sessions: [session] })), done].map((batch) => `data: ${JSON.stringify(batch)}\n\n`).join("") + "event: complete\ndata: {}\n\n", { headers: { "Content-Type": "text/event-stream" } });
    }
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "poll" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/Tabs": case "/api/ChangeTabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
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
    await page.addInitScript(() => {
      const commits = Object.fromEntries(["v1", "v2", "v3"].map((version) => [version, [
        Promise.withResolvers<void>(), Promise.withResolvers<void>(),
      ]]));
      const counts: Record<string, number> = {};
      (window as unknown as SnapshotCommits).snapshotCommits = commits;
      const put = IDBObjectStore.prototype.put;
      IDBObjectStore.prototype.put = function (value: SavedListing, key?: IDBValidKey) {
        if (this.transaction.db.name === "rocketclaw-session-list-v2" && this.name === "snapshots") {
          this.transaction.addEventListener("complete", () => {
            const count = counts[value.version] ?? 0;
            counts[value.version] = count + 1;
            commits[value.version]?.[count]?.resolve();
          }, { once: true });
        }
        return put.call(this, value, key);
      };
    });
    const pollState = () => `Session poll state: ${JSON.stringify({ polls, finished, failed, pageErrors: errors })}`;
    const waitForPolls = async (count: number, signal: AbortSignal) => {
      if (polls.length < count) await once(requested, String(count), { signal }).catch((error: Error) => {
        throw new Error(pollState(), { cause: error });
      });
    };
    const savedListing = (version: string, timeout: number, index = 0) => page.waitForFunction(
      async ({ version, index }: { version: string; index: number }) => {
        await (window as unknown as SnapshotCommits).snapshotCommits[version][index].promise;
        return true;
      },
      { version, index }, { timeout },
    ).catch((error: Error) => { throw new Error(`Waiting for saved listing ${version}. ${pollState()}`, { cause: error }); });
    await page.clock.install();
    await page.goto(`http://127.0.0.1:${server.port}`);
    const initialDeadline = performance.now() + 8_000;
    await savedListing("v1", Math.max(1, initialDeadline - performance.now()));
    await page.clock.fastForward(2_000);
    await savedListing("v2", Math.max(1, initialDeadline - performance.now()));
    await Promise.all([
      waitForPolls(3, AbortSignal.timeout(Math.max(1, initialDeadline - performance.now()))),
      page.clock.fastForward(2_000),
    ]);
    expect(polls.slice(0, 3).map(({ since }) => since), pollState()).toEqual(["", "v1", "v2"]);

    await page.keyboard.press("ControlOrMeta+p");
    const search = page.getByPlaceholder("Search sessions and commands");
    await search.waitFor();
    const sessions = page.getByRole("button", { name: /^(Alpha|Beta|Gamma)/ });
    await page.getByRole("button", { name: /^Beta again/ }).waitFor();
    expect(await sessions.allTextContents()).toEqual([expect.stringMatching(/^Beta again/), expect.stringMatching(/^Alpha/)]);

    await savedListing("v3", 0);
    const polled = polls.length;
    await page.reload();
    const reloadDeadline = performance.now() + 8_000;
    await savedListing("v3", Math.max(1, reloadDeadline - performance.now()));
    await page.clock.pauseAt(await page.evaluate(() => Date.now() + 2_000));
    await savedListing("v3", Math.max(1, reloadDeadline - performance.now()), 1);
    const reloaded = polls.slice(polled).map(({ since }) => since);
    expect(reloaded.length, pollState()).toBeGreaterThanOrEqual(2);
    expect(reloaded.every((since) => since === "v3"), pollState()).toBe(true);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
