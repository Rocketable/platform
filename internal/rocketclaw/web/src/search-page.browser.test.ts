import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Match = { conversationId: string; field?: string; text?: string };
// A SearchSessions response from fixed fixtures; each term is its text at its first, or the given, position in query.
const response = (query: string, matches: Match[] = [], terms: (string | [string, number])[] = [], extra: object = {}) => ({
  terms: terms.map((term) => {
    const [text, start] = typeof term === "string" ? [term, query.indexOf(term)] : term;
    return { key: text.slice(0, text.indexOf(":")).toLowerCase(), text, start, end: start + text.length };
  }),
  text: query, needle: query.toLowerCase(), matches: matches.map((match) => ({ field: "Preview", text: "", ...match })), messages: [], mentionIds: [], ...extra,
});

type SavedTab = { path: string; search?: { name?: string; searches: { id: string; name?: string; query: string }[]; active: string } };
// tabsApi keeps each person's saved tabs as the server does, so a reload reopens Search pages with their searches.
function tabsApi(owner: () => string) {
  const saved = new Map<string, { tabs: SavedTab[] }>();
  let revision = 0;
  return {
    tabs: (who = owner()) => saved.get(who)?.tabs ?? [],
    reply: async (request: Request) => {
      const { state } = await request.json() as { state?: { tabs: SavedTab[] } };
      if (state) {
        saved.set(owner(), state);
        revision++;
      }
      return Response.json({ spaces: [{ id: "main", name: "Main" }], state: saved.get(owner()) ?? { tabs: [], groups: [], space: "main", last: {} }, revision: String(revision) });
    },
  };
}

// until polls check, as saves follow typing after a pause.
async function until(check: () => boolean) {
  for (let tries = 0; !check(); tries++) {
    if (tries > 100) throw new Error("timed out waiting for a save");
    await Bun.sleep(50);
  }
}

// serve answers the App's requests with rows and a SearchSessions fixture, and records every API path.
function serve(rows: object[], search: (input: { query: string; filters?: boolean; messages?: boolean }) => Promise<object> | object, agents: object[] = [{ name: "main" }]) {
  const paths = new Set<string>(), tabs = tabsApi(() => "tester");
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    paths.add(url.pathname);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents });
    if (url.pathname === "/api/SearchSessions") {
      try {
        return Response.json(await search(await request.json()));
      } catch (error) {
        return Response.json({ message: (error as Error).message, code: 13 }, { status: 500 });
      }
    }
    if (url.pathname === "/api/Tabs") return tabs.reply(request);
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  return { server, paths, tabs, origin: `http://127.0.0.1:${server.port}` };
}

for (const width of [1280, 390]) test(`saved search tabs at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  let owner = "alice";
  const api = tabsApi(() => owner);
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner, upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: owner });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-page" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      return Response.json(response(query, [], ({ 'tag:"Needs review" is:forked': ['tag:"Needs review"', "is:forked"], "is:forked": ["is:forked"] } as Record<string, string[]>)[query]));
    }
    if (url.pathname === "/api/Tabs") return api.reply(request);
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 800 } });
    const searches = page.getByRole("tablist", { name: "Searches" });
    const tabs = searches.getByRole("tab");
    const shown = (who = owner) => api.tabs(who).find((tab) => tab.path === new URL(page.url()).pathname)?.search;
    const opened = (url: URL) => url.pathname.startsWith("/search/");
    await page.goto(`http://127.0.0.1:${server.port}/`);
    // The footer Search button is Cmd/Ctrl+P; the Search page is the "Sessions: Search" command, also under Cmd/Ctrl+Shift+P.
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.getByRole("dialog", { name: "Go to session" }).getByPlaceholder("Search sessions").waitFor();
    expect(new URL(page.url()).pathname).toBe("/");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.waitForURL(opened);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.waitFor();
    expect(await tabs.count()).toBe(1);
    expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key.startsWith("search-tabs:")))).toEqual([]);
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await page.getByRole("button", { name: "Search sessions and commands" }).waitFor(); // The shortcut listens once the app renders.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    expect(await tabs.count()).toBe(1);
    await search.fill("same");
    await page.getByRole("button", { name: "New search" }).click();
    await search.fill("same");
    // Each "Sessions: Search" opens a page of its own, saved with this person's tabs.
    expect(api.tabs().filter((tab) => opened(new URL(tab.path, page.url())))).toHaveLength(2);
    await until(() => shown()?.searches.at(-1)?.query === "same");
    const saved = shown()!;
    expect(saved.searches).toHaveLength(2);
    expect(saved.searches[0].id).not.toBe(saved.searches[1].id);
    await page.reload();
    await search.waitFor();
    expect(await tabs.last().getAttribute("aria-selected")).toBe("true");
    expect(await search.inputValue()).toBe("same");
    const box = await search.elementHandle();
    await page.route("**/api/Identity", (route: { abort(): Promise<void> }) => route.abort());
    await page.waitForEvent("requestfailed", (request: { url(): string }) => request.url().endsWith("/api/Identity"));
    await page.unroute("**/api/Identity");
    await page.waitForResponse((response: { url(): string; ok(): boolean }) => response.url().endsWith("/api/Identity") && response.ok());
    expect(await box.evaluate((node: HTMLElement) => node.isConnected)).toBe(true);
    await tabs.first().click();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    await tabs.first().click();
    const name = page.getByRole("textbox", { name: "Search name", exact: true });
    await name.waitFor();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    expect(await name.evaluate((node: HTMLElement) => !!node.closest('[role="tablist"]'))).toBe(true);
    expect(await name.textContent()).toBe("same");
    expect(await name.getAttribute("contenteditable")).toBe("plaintext-only");
    await page.waitForFunction((node: HTMLElement) => document.activeElement === node, await name.elementHandle());
    expect(await name.evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    await name.fill("Discarded name");
    await name.press("Escape");
    expect(await tabs.first().textContent()).toBe("same");
    await tabs.first().click();
    await name.fill("");
    await name.press("Enter");
    expect(await tabs.first().textContent()).toBe("same");
    await tabs.first().click();
    expect(await name.textContent()).toBe("same");
    await name.pressSequentially("Saved query");
    expect(await name.textContent()).toBe("Saved query");
    await name.press("Enter");
    await searches.getByRole("tab", { name: "Saved query", exact: true }).waitFor();
    await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Searches"] [role="tab"][aria-selected="true"]'));
    expect(await tabs.first().evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    expect(await search.inputValue()).toBe("same");
    await tabs.last().click();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    await searches.getByRole("tab", { name: "Saved query", exact: true }).click();
    expect(await page.getByRole("dialog", { name: "Rename search" }).count()).toBe(0);
    await searches.getByRole("tab", { name: "Saved query", exact: true }).click();
    await name.fill("  Saved query  ");
    await name.press("Enter");
    expect(await tabs.first().textContent()).toBe("Saved query");
    await searches.getByRole("tab", { name: "Saved query", exact: true }).click();
    await name.fill("Blur saved");
    await tabs.last().click();
    expect(await tabs.first().textContent()).toBe("Blur saved");
    expect(await tabs.last().getAttribute("aria-selected")).toBe("true");
    await searches.getByRole("tab", { name: "Blur saved", exact: true }).click();
    await searches.getByRole("tab", { name: "Blur saved", exact: true }).click();
    await name.fill("Saved query");
    await name.press("Enter");
    await search.fill("edited");
    await search.fill('tag:"Needs review" is:forked ');
    const pills = page.getByRole("button", { name: /^(?:is|tag):/ });
    await pills.nth(1).waitFor();
    expect(await search.inputValue()).toBe("");
    expect(await pills.allTextContents()).toEqual(['tag:"Needs review"', "is:forked"]);
    await until(() => !!shown()?.searches.some((item) => item.query === 'tag:"Needs review" is:forked '));
    await page.reload();
    await pills.nth(1).waitFor();
    expect(await search.inputValue()).toBe("");
    while (await pills.count()) await pills.first().click();
    await search.fill("edited");
    expect(await tabs.first().textContent()).toBe("Saved query");
    await searches.getByRole("tab", { name: "Saved query", exact: true }).click();
    expect(await name.textContent()).toBe("Saved query");
    await name.fill("Discard again");
    await page.keyboard.press("Escape");
    expect(await tabs.first().textContent()).toBe("Saved query");
    await searches.getByRole("tab", { name: "Saved query", exact: true }).click();
    await name.fill("");
    await search.click();
    await searches.getByRole("tab", { name: "edited", exact: true }).waitFor();
    expect(await search.inputValue()).toBe("edited");
    await tabs.last().focus();
    await page.keyboard.press("ArrowLeft");
    expect(await tabs.first().getAttribute("aria-selected")).toBe("true");
    await page.getByRole("button", { name: "Close search 1" }).click();
    expect(await tabs.count()).toBe(1);
    expect(await tabs.first().evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    expect(await search.inputValue()).toBe("same");
    await search.fill(""); // An owner switch opens the URL search for the new owner, from an empty one.
    await page.waitForURL((url: URL) => opened(url) && !url.search);
    await tabs.first().click();
    await name.fill("Alice");
    await name.press("Enter");
    await page.evaluate(() => localStorage.setItem("last-seen:alice", "/s/b25l?message=private"));
    owner = "bob";
    await searches.getByRole("tab", { name: "Search 1" }).waitFor({ timeout: 10_000 });
    expect(await search.inputValue()).toBe("");
    expect(shown("alice")!.searches.map((item) => item.name)).toEqual(["Alice"]);
    await until(() => shown("bob")?.searches.length === 1);
    // Closing Bob's only search closes his page, not to Alice's last message.
    await page.getByRole("button", { name: "Close search 1" }).click();
    await page.waitForURL(`http://127.0.0.1:${server.port}/`);
    expect(api.tabs("bob").filter((tab) => tab.search)).toEqual([]);
    await page.keyboard.press("Control+p");
    const dialog = page.getByRole("dialog", { name: "Go to session" });
    await dialog.getByPlaceholder("Search sessions").fill("temporary");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Control+p");
    expect(await dialog.getByPlaceholder("Search sessions").inputValue()).toBe("");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.waitForURL(opened);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("searches this browser kept stay when a page that already has searches is edited", async () => {
  const { chromium: engine } = await import(playwright!);
  const { server, tabs: api, origin } = serve([], ({ query }) => response(query));
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const first = () => api.tabs().find((tab) => tab.path === "/search/a")?.search?.searches[0].query;
    await page.goto(`${origin}/search/a`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("server");
    await until(() => first() === "server");
    await page.evaluate(() => localStorage.setItem("search-tabs:tester", JSON.stringify({ tabs: [{ id: "old", query: "kept" }], active: "old" })));
    await page.reload();
    await search.fill("edited");
    await until(() => first() === "edited");
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:tester"))).not.toBeNull();
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("principal filters retain links, pills and quoted suggestions", async () => {
  const { chromium: engine } = await import(playwright!);
  const principal = 'Writer "One"', term = `principal:${JSON.stringify(principal)}`;
  const { server, origin } = serve([{ id: "written", name: "Written conversation", tags: ["x"] }], ({ query }) => response(query,
    query.includes(term) ? [{ conversationId: "written", text: "recorded prompt" }] : [],
    [...(query.includes(term) ? [term] : []), ...(query.includes("tag:x") ? ["tag:x"] : [])].sort((a, b) => query.indexOf(a) - query.indexOf(b)), { principals: [principal], text: "", needle: "" }));
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const query = `${term} tag:x `;
    await page.goto(`${origin}/search?${new URLSearchParams({ q: query })}`);
    const pill = page.getByRole("button", { name: term, exact: true });
    await pill.waitFor();
    await page.reload();
    await pill.waitFor();
    await page.getByLabel("Search results").getByRole("link", { name: "Written conversation" }).waitFor();
    await pill.click();
    await page.getByRole("button", { name: "tag:x", exact: true }).waitFor();
    expect(new URL(page.url()).searchParams.get("q")).toBe("tag:x ");
    const input = page.getByRole("textbox", { name: "Search sessions" });
    await input.fill("principal:");
    const suggestion = page.getByRole("button", { name: principal, exact: true });
    await suggestion.waitFor();
    await suggestion.click();
    await pill.waitFor();
    expect(new URL(page.url()).searchParams.get("q")).toBe(`tag:x ${term} `);
    expect(await input.inputValue()).toBe("");
    await page.getByRole("button", { name: "New search", exact: true }).click();
    await input.fill("principal:");
    await suggestion.waitFor();
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("search URL follows the active tab, folds legacy filters, and reopens a matching tab", async () => {
  const { chromium: engine } = await import(playwright!);
  const folded = "agent:alitu-cs-support outage";
  const { server, paths, tabs: api, origin } = serve([{ id: "chat", name: "Outage chat", preview: "", agent: "alitu-cs-support" }], ({ query }) => query === folded ? response(query, [{ conversationId: "chat", field: "Name", text: "Outage chat" }], ["agent:alitu-cs-support"]) : response(query));
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const link = "/search?q=outage&agent=alitu-cs-support";
    const canonical = "/search?q=agent%3Aalitu-cs-support+outage";
    const at = (target: string) => page.waitForURL((url: URL) => url.pathname + url.search === target);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const tabs = page.getByRole("tablist", { name: "Searches" }).getByRole("tab");
    const opened = async (count: number) => {
      await page.getByRole("button", { name: "agent:alitu-cs-support", exact: true }).waitFor();
      expect(await tabs.count()).toBe(count);
      expect(await tabs.first().getAttribute("aria-selected")).toBe("true");
      expect(await search.inputValue()).toBe("outage");
    };
    await page.goto(`${origin}/`);
    await page.evaluate(() => localStorage.setItem("search-tabs:tester", JSON.stringify({ tabs: [{ id: "legacy", query: "outage", agentFilter: "alitu-cs-support", roomFilter: "" }], active: "legacy" })));
    await page.goto(origin + link);
    await opened(1); // The legacy tab, folded once into its query, matches the folded link, and moves into the page's tab.
    const saved = () => api.tabs().find((tab) => tab.path === "/search")?.search?.searches;
    await until(() => !!saved());
    expect(saved()).toEqual([{ id: "legacy", query: folded }]);
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:tester"))).toBeNull();
    await at(canonical);
    await page.reload();
    await opened(1);
    await page.getByRole("button", { name: "New search" }).click();
    await at("/search");
    await search.fill("other room");
    await at("/search?q=other+room");
    await until(() => saved()?.at(-1)?.query === "other room");
    await page.goto(origin + link);
    await opened(2);
    await page.getByLabel("Search results").getByRole("heading").getByRole("link", { name: "Outage chat" }).click();
    await page.waitForURL("**/s/Y2hhdA");
    await page.goBack();
    await at(canonical);
    await opened(2);
    const entries = await page.evaluate(() => history.length);
    await tabs.last().click();
    await at("/search?q=other+room");
    await tabs.first().click();
    await at(canonical);
    await page.getByRole("button", { name: "Close search 1" }).click();
    await at("/search?q=other+room");
    await page.getByRole("button", { name: "New search" }).click();
    await at("/search");
    expect(await page.evaluate(() => history.length)).toBe(entries);
    expect(paths.has("/api/SearchOrigins")).toBe(false);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("Cmd+P matches titles at once, asks the server only for filters, and Enter opens the first row", async () => {
  const { chromium: engine } = await import(playwright!);
  const requests: { query: string; filters?: boolean; messages?: boolean }[] = [];
  const held = Promise.withResolvers<void>();
  const started = Promise.withResolvers<void>();
  const filterHeld = Promise.withResolvers<void>();
  let failing = true;
  const { server, paths, origin } = serve([{ id: "a", name: "Alpha", agent: "main", tags: ["bug"] }, { id: "b", name: "Bravo", agent: "main", preview: "charlie in the body" }, { id: "c", name: "Charlie", agent: "main" }], async (input) => {
    requests.push(input);
    const { query } = input;
    if (query === "tag:bug x") { started.resolve(); await held.promise; }
    if (query === "CHAR is:pinned") await filterHeld.promise;
    if (query === "is:pinned brav" && failing) throw new Error("search unavailable");
    // As the server does for Cmd+P, the fixture applies only the filters: Alpha and Bravo are pinned, Alpha is tagged bug.
    const ids = query.startsWith("tag:bug ") ? ["a"] : query.includes("is:pinned") ? ["a", "b"] : [];
    return response(query, ids.map((conversationId) => ({ conversationId })), query.startsWith("tag:") ? [query.split(" ")[0]] : []);
  });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions and commands", exact: true }).waitFor();
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session" });
    const input = palette.getByPlaceholder("Search sessions");
    const titles = (expected: string[]) => page.waitForFunction((expected: string[]) => [...document.querySelectorAll('[role="dialog"] li > button [data-slot="session-title"]')].map((node) => node.textContent).join("|") === expected.join("|"), expected, { timeout: 5000 });
    await titles(["Alpha", "Bravo", "Charlie"]); // Empty Cmd+P lists the sidebar without a request.
    await input.fill("tag:");
    await palette.getByRole("button", { name: "bug", exact: true }).waitFor();
    await input.fill("is:pi");
    await palette.getByRole("button", { name: "pinned", exact: true }).waitFor();
    for (const typing of ["sort:ne", "agent:", "   "]) await input.fill(typing);
    await input.fill("CHAR"); // A title match, never the body: Bravo only says charlie in its preview.
    await titles(["Charlie"]);
    expect(await palette.getByRole("button", { name: "Search everywhere for 'CHAR'", exact: true }).count()).toBe(1);
    await page.waitForTimeout(400);
    expect(requests).toEqual([]); // Unfinished operators and plain text are not sent.
    await input.fill("CHAR is:pinned"); // Adding a filter keeps the title matches, dimmed, until the server answers.
    await page.waitForFunction(() => document.querySelector('[role="dialog"] ul[data-pending="true"]') !== null);
    await titles(["Charlie"]);
    filterHeld.resolve();
    await titles([]);

    const pills = palette.getByRole("button", { name: /^tag:/ });
    await input.fill("tag:bu");
    await titles([]);
    while (requests.at(-1)?.query !== "tag:bu") await page.waitForTimeout(50);
    expect(requests.at(-1)).toEqual({ query: "tag:bu", filters: true });
    expect(await pills.count()).toBe(0); // A term still being typed stays in the input.
    await input.pressSequentially("g x");
    await started.promise;
    expect(await pills.count()).toBe(0); // Pills wait for the server's reading.
    expect(await input.inputValue()).toBe("tag:bug x");
    held.resolve();
    await pills.first().waitFor();
    expect(await pills.allTextContents()).toEqual(["tag:bug"]);
    expect(await input.inputValue()).toBe("x");
    expect(await input.evaluate((node: HTMLInputElement) => node.selectionStart)).toBe(1);
    await input.pressSequentially("y");
    expect(await input.inputValue()).toBe("xy");
    await pills.first().click();
    expect(await input.inputValue()).toBe("xy");

    await input.fill("is:pinned a"); // The server's pinned rows, matched by title here.
    await titles(["Alpha", "Bravo"]);
    await input.pressSequentially("v");
    await input.press("Enter"); // Before the pause ends: Enter sends the text and opens the first row whose title holds it, not Alpha.
    await page.waitForURL(`**/s/${Buffer.from("b").toString("base64url")}`);
    expect(requests.filter(({ query }) => query === "is:pinned av")).toHaveLength(1);

    await page.keyboard.press("Control+p");
    const opened = page.url();
    await input.fill("is:pinned nothing");
    await palette.getByText("No matches", { exact: true }).waitFor();
    await input.press("Enter"); // A filtered query with no result opens nothing, not even Search everywhere.
    await page.waitForTimeout(100);
    expect(await palette.isVisible()).toBe(true);
    await input.fill("is:pinned brav");
    await palette.getByRole("alert").filter({ hasText: "Search failed" }).waitFor();
    await palette.getByText("Search incomplete", { exact: true }).waitFor();
    await input.press("Enter"); // A failed search opens nothing either.
    await page.waitForTimeout(100);
    expect(await palette.isVisible()).toBe(true);
    expect(page.url()).toBe(opened);
    failing = false;
    await input.press("End");
    await input.pressSequentially(" "); // The next keystroke retries the same text.
    await titles(["Bravo"]);
    expect(await palette.getByRole("alert").count()).toBe(0);
    expect(paths.has("/api/SearchOrigins")).toBe(false);
  } finally {
    held.resolve();
    filterHeld.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("/search renders the server's groups, notices and pills", async () => {
  const { chromium: engine } = await import(playwright!);
  const requests: { query: string; filters?: boolean; messages?: boolean }[] = [];
  const hold = Promise.withResolvers<void>();
  const rows = [
    { id: "third", name: "Pinned conversation", preview: "Other text", agent: "main", pinned: true },
    { id: "second", name: "", title: "", preview: "Needle notes\nOther text", agent: "main" },
    { id: "fourth", name: "Origin chat", preview: "Other text", agent: "main" },
  ];
  const message = (messageId: string, role: string, text: string) => ({ conversationId: "third", message: { messageId, role, text, complete: true } });
  const { server, paths, tabs: api, origin } = serve(rows, async (input) => {
    requests.push(input);
    const { query } = input;
    if (query === "agent:main" && requests.filter((request) => request.query === query).length === 1) await hold.promise;
    switch (query) {
      case "needle": return response(query, [{ conversationId: "third", field: "" }, { conversationId: "second", text: "Needle notes\nOther text" }, { conversationId: "fourth", field: "Origin", text: "cron source: /notes/needle.md" }], [], { messages: [message("1:0", "user", "Context before NEEDLE then needle"), message("1:1", "assistant", "A second needle response")] });
      case "chat is:pinned": return response(query, [{ conversationId: "third", field: "Name", text: "Pinned conversation" }], ["is:pinned"]);
      case "outage tag:": return response(query, [{ conversationId: "fourth", field: "Name", text: "Origin chat" }]);
      case "prefix-agent:main agent:main": return response(query, [], [["agent:main", 18]]);
      case "foo tag:bug bar": case "tag:bug foobar": case "tag:bug outage": return response(query, [], ["tag:bug"]);
      case "agent:main agent:other": return response(query, [], [["agent:main", 0], ["agent:other", 11]]);
      case "agent:main": case "agent:other": return response(query, [], [query]);
      case "late": return response(query, [{ conversationId: "late-chat", field: "Name", text: "Late chat" }]);
      default: return response(query);
    }
  }, [{ name: "main" }, { name: "other" }]);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`${origin}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const results = page.getByLabel("Search results");
    const stored = (query: string) => until(() => api.tabs().find((tab) => tab.path === "/search")?.search?.searches[0].query === query);
    const sent = async (query: string) => { while (requests.at(-1)?.query !== query) await page.waitForTimeout(50); };
    await search.fill("needle");
    await page.waitForFunction(() => [...document.querySelectorAll('[aria-label="Search results"] [role="group"]')].map((node) => node.getAttribute("aria-label")).join("|") === "Pinned conversation|Needle notes|Origin chat", undefined, { timeout: 5000 });
    const hits = results.getByRole("group", { name: "Pinned conversation" });
    expect(await hits.getByRole("heading").textContent()).toBe("Pinned conversation(2)"); // Only message text matched: no field row.
    expect(await hits.locator('a[href*="?message="]').count()).toBe(2);
    expect(await hits.getByRole("link", { name: /Context before NEEDLE/ }).locator("mark").allTextContents()).toEqual(["NEEDLE", "needle"]);
    const preview = results.getByRole("group", { name: "Needle notes" });
    expect(await preview.getByText("Preview", { exact: true }).count()).toBe(1);
    expect(await preview.locator("mark").textContent()).toBe("Needle");
    const field = results.getByRole("group", { name: "Origin chat" });
    expect(await field.getByText("Origin", { exact: true }).count()).toBe(1);
    expect(await field.locator("mark").textContent()).toBe("needle");
    expect(requests).toEqual([{ query: "needle", messages: true }]);

    await search.fill("chat is:pinned"); // A saved query ending in a filter reopens with that filter.
    await results.getByRole("group", { name: "Pinned conversation" }).waitFor();
    await stored("chat is:pinned");
    requests.length = 0;
    await page.reload();
    await results.getByRole("group", { name: "Pinned conversation" }).waitFor();
    expect(requests[0]).toEqual({ query: "chat is:pinned", messages: true });
    expect(await search.inputValue()).toBe("chat is:pinned");

    await search.fill("outage tag:"); // Typing leaves the bare operator out; Enter and reopening send it.
    await sent("outage");
    await search.press("Enter");
    await results.getByRole("group", { name: "Origin chat" }).waitFor();
    expect(requests.at(-1)!.query).toBe("outage tag:");
    await stored("outage tag:");
    requests.length = 0;
    await page.reload();
    await results.getByRole("group", { name: "Origin chat" }).waitFor();
    expect(requests[0].query).toBe("outage tag:");

    await search.fill("prefix-agent:main agent:main ");
    const agentPill = (name: string) => page.getByRole("button", { name: `agent:${name}`, exact: true });
    await agentPill("main").waitFor();
    expect(await search.inputValue()).toBe("prefix-agent:main ");
    await agentPill("main").click(); // Removal cuts the server's offsets, not the first matching text.
    expect(await search.inputValue()).toBe("prefix-agent:main ");
    await sent("prefix-agent:main");

    await search.fill("foo tag:bug bar"); // Editing text never deletes a middle pill or glues text to it.
    const tagPill = page.getByRole("button", { name: "tag:bug", exact: true });
    await tagPill.waitFor();
    expect(await search.inputValue()).toBe("foo bar");
    for (const key of ["ArrowLeft", "ArrowLeft", "ArrowLeft", "Backspace"]) await search.press(key);
    await stored("tag:bug foobar");
    await sent("tag:bug foobar");
    await tagPill.waitFor();
    expect(await search.inputValue()).toBe("foobar");
    expect(await search.evaluate((node: HTMLInputElement) => node.selectionStart)).toBe(3);
    await tagPill.click();
    await search.fill("foo tag:bug bar");
    await tagPill.waitFor();
    await search.fill("outage"); // Select-all and type.
    await stored("tag:bug outage");
    await sent("tag:bug outage");
    await tagPill.waitFor();
    expect(await search.inputValue()).toBe("outage");
    await tagPill.click();

    await search.fill("");
    await search.pressSequentially("agent:");
    await page.getByRole("button", { name: "main", exact: true }).click();
    await sent("agent:main"); // Held: the second pick comes before this response.
    expect(await search.inputValue()).toBe("agent:main ");
    await search.press("End");
    await search.pressSequentially("agent:");
    await page.getByRole("button", { name: "other", exact: true }).click();
    await agentPill("other").waitFor();
    expect(await search.inputValue()).toBe("");
    expect(await agentPill("main").count()).toBe(1);
    await stored("agent:main agent:other ");
    hold.resolve();
    await page.reload();
    await agentPill("main").waitFor();
    await agentPill("other").waitFor();
    await agentPill("main").click();
    await sent("agent:other");
    expect(await agentPill("other").count()).toBe(1);
    await stored("agent:other ");
    await agentPill("other").click();

    await search.fill("late"); // A match the sidebar has not loaded is pending, not "No matches".
    await results.getByText("Session search is still loading.").waitFor();
    expect(await results.getByText("No matches").count()).toBe(0);
    rows.push({ id: "late-chat", name: "Late chat", preview: "", agent: "main" });
    await results.getByRole("group", { name: "Late chat" }).waitFor({ timeout: 10_000 });
    expect(paths.has("/api/SearchOrigins")).toBe(false);
  } finally {
    hold.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("Cmd+P shows one row per session and hands its text to /search, which finds message hits", async () => {
  const { chromium: engine } = await import(playwright!);
  const requests: { query: string; filters?: boolean; messages?: boolean }[] = [];
  const rows = [
    { id: "third", name: "Pinned conversation", preview: "Other text", agent: "main", pinned: true },
    { id: "second", name: "", title: "", preview: "Needle notes\nOther text", agent: "main" },
    { id: "opencode:box:ses", kind: "REMOTE_OPENCODE", remoteSessionId: "ses", name: "Remote", preview: "Other text", server: "box", agent: "build", model: "anthropic/claude-opus-4-7" },
    { id: "md", name: "", preview: "**Done.** Updated the runbook", agent: "main" },
    { id: "long", name: "Weekly report about many unrelated things and then the deploy checklist", agent: "main" },
  ];
  const message = (messageId: string, role: string, text: string) => ({ conversationId: "third", message: { messageId, role, text, complete: true } });
  const { server, tabs: api, origin } = serve(rows, (input) => {
    requests.push(input);
    return response(input.query, [{ conversationId: "third", field: "" }, { conversationId: "second", text: "Needle notes\nOther text" }], [], { messages: [message("1:0", "user", "Context before NEEDLE then needle")] });
  }, [{ name: "main", model: "gpt-6.1-sol" }]);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions and commands", exact: true }).waitFor();
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session" });
    const input = palette.getByPlaceholder("Search sessions");
    await input.fill("needle");
    const handoff = palette.getByRole("button", { name: "Search everywhere for 'needle'", exact: true });
    await handoff.waitFor();
    await palette.getByRole("button").filter({ hasText: "Needle notes" }).waitFor();
    // The title match is one row, with no message or field rows, and the hand-off comes last.
    expect(await palette.locator("li > button").evaluateAll((buttons: Element[]) => buttons.map((button) => button.querySelector('[data-slot="session-title"]')?.textContent ?? button.textContent))).toEqual(["Needle notes", "Search everywhere for 'needle'"]);
    expect(requests).toEqual([]);

    // Remote sessions suggest their servers, agents and models; RocketClaw agents their configured models.
    for (const [typed, offered] of [["server:", ["box"]], ["agent:", ["main", "build"]], ["model:", ["gpt-6.1-sol", "anthropic/claude-opus-4-7"]], ["model:OPUS", ["anthropic/claude-opus-4-7"]]] as const) {
      await input.fill(typed);
      const choices = palette.locator("ul.bg-popover li span.font-medium");
      await choices.first().waitFor();
      expect(await choices.allTextContents()).toEqual([...offered]);
    }

    await input.fill("done. updated"); // Markdown reads as the row shows it.
    await palette.getByRole("button").filter({ hasText: "Done. Updated the runbook" }).waitFor();
    await input.fill("CHECKLIST"); // A match deep in a long title shows from just before it, highlighted.
    const long = palette.locator('[data-slot="session-title"]').filter({ hasText: "checklist" });
    await long.locator("mark").waitFor();
    expect(await long.textContent()).toBe("…then the deploy checklist");
    expect(await long.locator("mark").textContent()).toBe("checklist");
    await input.fill("zzz"); // No title matches: the empty state shows above the hand-off.
    await palette.getByText("No matches", { exact: true }).waitFor();
    await input.fill("needle");
    await handoff.click();
    const at = (query: string) => page.waitForURL((url: URL) => url.pathname.startsWith("/search/") && url.searchParams.get("q") === query);
    await at("needle");
    const first = new URL(page.url()).pathname;
    const results = page.getByLabel("Search results");
    await results.getByRole("link", { name: /Context before NEEDLE/ }).waitFor();
    expect(requests).toEqual([{ query: "needle", messages: true }]);

    // New text opens a Search page of its own, even from a Search page.
    await page.keyboard.press("Control+p");
    await input.fill("pinned");
    await palette.getByRole("button", { name: "Search everywhere for 'pinned'", exact: true }).click();
    await at("pinned");
    await palette.waitFor({ state: "hidden" });
    expect(new URL(page.url()).pathname).not.toBe(first);
    expect(await page.getByRole("textbox", { name: "Search sessions", exact: true }).inputValue()).toBe("pinned");
    expect(await page.getByRole("tablist", { name: "Searches" }).getByRole("tab").allTextContents()).toEqual(["pinned"]);
    const strip = page.getByRole("tablist", { name: "Open tabs" });
    for (const name of ["needle", "pinned"]) await strip.getByRole("tab", { name, exact: true }).waitFor();
    // Back and Forward between the two entries switch the open search with the address.
    const searchInput = page.getByRole("textbox", { name: "Search sessions", exact: true });
    await page.goBack();
    await at("needle");
    await page.waitForFunction(() => (document.querySelector('input[aria-label="Search sessions"]') as HTMLInputElement | null)?.value === "needle");
    await page.goForward();
    await at("pinned");
    await page.waitForFunction(() => (document.querySelector('input[aria-label="Search sessions"]') as HTMLInputElement | null)?.value === "pinned");
    expect(await searchInput.inputValue()).toBe("pinned");
    // Typed text in Cmd+P lists the Search pages by title or query, never an empty query, whose Enter opens the first
    // session; text one already searches for reopens it.
    await page.keyboard.press("Control+p");
    const pages = palette.getByRole("button", { name: /^Search: / });
    await palette.getByRole("button").filter({ hasText: "Needle notes" }).waitFor();
    expect(await pages.count()).toBe(0);
    await input.fill("e");
    await pages.nth(1).waitFor();
    expect(await pages.allTextContents()).toEqual(["Search: needleMain", "Search: pinnedMain"]);
    await input.fill("need");
    await palette.getByRole("button", { name: /^Search: pinned/ }).waitFor({ state: "detached" });
    expect(await pages.allTextContents()).toEqual(["Search: needleMain"]);
    await input.fill("needle");
    await palette.getByRole("button", { name: "Search everywhere for 'needle'", exact: true }).click();
    await at("needle");
    expect(new URL(page.url()).pathname).toBe(first);
    expect(api.tabs().filter((tab) => tab.path.startsWith("/search/"))).toHaveLength(2);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("Cmd+P draws large session lists only as scrolling and keyboard navigation need them", async () => {
  const { chromium: engine } = await import(playwright!);
  const rows = Array.from({ length: 600 }, (_, i) => ({ id: `report-${i}`, name: `Report ${String(i).padStart(4, "0")}`, agent: "main" }));
  const { server, origin, paths } = serve(rows, ({ query }) => response(query));
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions and commands", exact: true }).waitFor();
    await page.keyboard.press("Control+p");
    const palette = page.getByRole("dialog", { name: "Go to session" });
    const input = palette.getByPlaceholder("Search sessions");
    await input.fill("Report");
    await palette.getByText("Report 0000", { exact: true }).waitFor();
    // Let deferred work finish too: an initial-value-only limit is not paging.
    await page.evaluate(() => new Promise((resolve) => requestIdleCallback(() => requestAnimationFrame(() => requestAnimationFrame(resolve)))));
    const buttons = palette.locator("li > button");
    expect(await buttons.count()).toBe(50);
    expect(await palette.locator('[data-slot="session-title"]').allTextContents()).toEqual(rows.slice(0, 50).map(({ name }) => name));
    await input.press("ArrowUp"); // Wrap past the hand-off to the last session, without dropping choices.
    await page.keyboard.press("ArrowUp");
    await page.waitForURL(`**/s/${btoa("report-599").replaceAll("=", "")}`);
    expect(await palette.getByText("Report 0599", { exact: true }).evaluate((node: HTMLElement) => {
      const row = node.closest("button")!.getBoundingClientRect(), list = node.closest("ul")!.getBoundingClientRect();
      return row.top >= list.top && row.bottom <= list.bottom && document.activeElement === document.querySelector('[role="dialog"] input');
    })).toBe(true);
    await input.fill("Report 0000");
    await input.fill("Report");
    await palette.getByText("Report 0049", { exact: true }).waitFor();
    expect(await buttons.count()).toBe(50);
    await palette.locator("ul[data-pending]").evaluate((node: HTMLElement) => { node.scrollTop = node.scrollHeight; });
    await page.waitForFunction(() => document.querySelectorAll('[role="dialog"] li > button').length > 50);
    expect(await palette.getByText("Report 0050", { exact: true }).count()).toBe(1);
    while (await buttons.count() < rows.length + 1) {
      const drawn = await buttons.count();
      await palette.locator("ul[data-pending]").evaluate((node: HTMLElement) => { node.scrollTop = node.scrollHeight; });
      await page.waitForFunction((count: number) => document.querySelectorAll('[role="dialog"] li > button').length > count, drawn);
    }
    expect(await palette.locator('[data-slot="session-title"]').allTextContents()).toEqual(rows.map(({ name }) => name));
    await palette.getByRole("button", { name: "Search everywhere for 'Report'", exact: true }).scrollIntoViewIfNeeded();
    await input.fill("Report 0599");
    await palette.getByText("Report 0599", { exact: true }).waitFor();
    expect(await palette.locator('[data-slot="session-title"]').allTextContents()).toEqual(["Report 0599"]);
    await input.fill("Report");
    await palette.getByText("Report 0000", { exact: true }).waitFor();
    expect(await buttons.count()).toBe(50);
    for (let i = 0; i < 51; i++) await page.keyboard.press("ArrowDown");
    await page.waitForURL(`**/s/${btoa("report-51")}`);
    expect(await palette.getByText("Report 0051", { exact: true }).evaluate((node: HTMLElement) => {
      const row = node.closest("button")!.getBoundingClientRect(), list = node.closest("ul")!.getBoundingClientRect();
      return row.top >= list.top && row.bottom <= list.bottom && document.activeElement === document.querySelector('[role="dialog"] input');
    })).toBe(true);
    await input.fill("Report 0599");
    await palette.getByText("Report 0599", { exact: true }).waitFor();
    await input.press("Enter");
    await palette.waitFor({ state: "hidden" });
    await page.waitForURL(`**/s/${btoa("report-599").replaceAll("=", "")}`);
    expect(paths.has("/api/SearchSessions")).toBe(false);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("newer search replaces an in-flight search and retries errors", async () => {
  const { chromium: engine } = await import(playwright!);
  const started = Promise.withResolvers<void>();
  const delayed = Promise.withResolvers<void>();
  const searches: string[] = [];
  let fail = true;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", name: "A chat", preview: "" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-race" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [] });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      searches.push(query);
      if (query === "old") { started.resolve(); await delayed.promise; }
      if (query === "new" && fail) return Response.json({ message: "Try again", code: 13 }, { status: 500 });
      return Response.json(response(query, [{ conversationId: "chat", field: "" }], [], { messages: [{ conversationId: "chat", message: { messageId: "1:0", role: "user", text: `Match ${query}`, complete: true } }] }));
    }
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.addInitScript(() => {
      const original = window.fetch;
      const abortedSearches: string[] = [];
      Object.assign(window, { abortedSearches });
      Object.defineProperty(window, "fetch", { value: (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).includes("/api/SearchSessions")) init?.signal?.addEventListener("abort", () => abortedSearches.push(JSON.parse(init.body as string).query));
        return original(input, init);
      } });
    });
    await page.goto(`http://127.0.0.1:${server.port}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("old");
    await started.promise;
    expect(await page.getByRole("status").filter({ hasText: "Searching…" }).count()).toBe(1);
    await search.fill("new");
    await search.press("Enter");
    await page.getByRole("alert").getByRole("button", { name: "Retry" }).waitFor();
    expect(await page.evaluate(() => Reflect.get(window, "abortedSearches") as string[])).toContain("old");
    expect(await page.getByLabel("Search results").getByRole("link").count()).toBe(0);
    delayed.resolve();
    fail = false;
    await page.getByRole("button", { name: "Retry" }).click();
    await page.getByRole("link", { name: /Match new/ }).waitFor();
    expect(searches).toEqual(["old", "new", "new"]);
    expect(await page.getByLabel("Search results").getByText("Match old").count()).toBe(0);
    for (let i = 1; i <= 7; i++) {
      await search.fill("x".repeat(i));
      await page.waitForTimeout(180);
    }
    expect(searches.slice(3).some((query) => query.length < 7)).toBe(true);
    await page.waitForTimeout(300);
    expect(searches.at(-1)).toBe("xxxxxxx");
  } finally {
    delayed.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("typing reuses unchanged message results and new replies replace them", async () => {
  const { chromium: engine } = await import(playwright!);
  const text = "needle " + "fixture text ".repeat(2048);
  const rows = Array.from({ length: 64 }, (_, i) => ({ id: `result-${i}`, name: `Result ${i}`, agent: "main" }));
  const requests: { query: string; messages?: boolean }[] = [];
  const held = Promise.withResolvers<void>();
  const started = Promise.withResolvers<void>();
  const polling = Promise.withResolvers<void>();
  const { server, origin } = serve(rows, async (input) => {
    requests.push(input);
    if (input.query === "needle xyz") { started.resolve(); await held.promise; }
    const matches = input.query === "needle" ? rows : rows.slice(0, 1);
    return response(input.query, matches.map(({ id }) => ({ conversationId: id, field: "" })), [], {
      messages: matches.map(({ id }) => ({ conversationId: id, message: { messageId: "1:0", role: "assistant", text: input.query === "needle" ? text : "needle xyz replacement", complete: true } })),
    });
  });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    let listings = 0;
    await page.route((url: URL) => url.pathname === "/api/ListSessions", (route: { continue(): Promise<void> }) => {
      if (++listings === 1) return route.continue();
      // Hold refreshes until page close: new sidebar rows legitimately rebuild results.
      polling.resolve();
    });
    await page.addInitScript((text: string) => {
      Object.assign(window, { messageSplits: 0 });
      const split = String.prototype.split;
      Object.defineProperty(String.prototype, "split", { value: function(this: string, separator: string | RegExp, limit?: number) {
        if (String(this) === text) Reflect.set(window, "messageSplits", Reflect.get(window, "messageSplits") + 1);
        return Reflect.apply(split, this, [separator, limit]);
      } });
    }, text);
    await page.goto(`${origin}/search?q=needle`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const results = page.getByLabel("Search results");
    await results.getByRole("group", { name: "Result 63", exact: true }).waitFor();
    await polling.promise;
    expect(listings).toBeGreaterThan(1);
    expect(await results.getByRole("group").count()).toBe(64);
    expect(await results.locator('a[href*="?message="]').count()).toBe(64);
    expect(await results.locator("mark").count()).toBe(64);
    expect(await page.evaluate(() => Reflect.get(window, "messageSplits"))).toBeGreaterThan(0);
    await page.evaluate(() => Reflect.set(window, "messageSplits", 0));
    await search.press("End");
    await search.pressSequentially(" xyz", { delay: 140 });
    expect(await search.inputValue()).toBe("needle xyz");
    expect(await page.evaluate(() => Reflect.get(window, "messageSplits"))).toBe(0);
    expect(requests).toEqual([{ query: "needle", messages: true }]);
    await search.press("Enter");
    await started.promise;
    expect(await results.getByRole("group").count()).toBe(64);
    held.resolve();
    await results.getByRole("link", { name: "Assistant needle xyz replacement", exact: true }).waitFor();
    expect(await results.getByRole("group").count()).toBe(1);
    expect(await results.locator("mark").allTextContents()).toEqual(["needle xyz"]);
    expect(requests).toEqual([{ query: "needle", messages: true }, { query: "needle xyz", messages: true }]);
  } finally {
    held.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("a broad search draws a page of rows and scrolling draws the rest", async () => {
  const { chromium: engine } = await import(playwright!);
  const rows = Array.from({ length: 1000 }, (_, i) => ({ id: `wide-${i}`, name: `Wide ${i}`, agent: "main" }));
  const { server, origin } = serve(rows, (input) => response(input.query, rows.map(({ id }) => ({ conversationId: id, field: "" })), [], {
    messages: rows.flatMap(({ id }) => [0, 1, 2].map((turn) => ({ conversationId: id, message: { messageId: `${turn}:0`, role: "assistant", text: `wide match ${turn}`, complete: true } }))),
  }));
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`${origin}/search?q=wide`);
    const results = page.getByLabel("Search results");
    const hits = results.locator('a[href*="?message="]');
    await results.getByRole("group", { name: "Wide 0", exact: true }).waitFor();
    expect(await hits.count()).toBeLessThan(1000);
    expect(await results.getByRole("group", { name: "Wide 999", exact: true }).count()).toBe(0);
    while (!await results.getByRole("group", { name: "Wide 999", exact: true }).count()) await results.evaluate((node: HTMLElement) => { node.scrollTop = node.scrollHeight; });
    await page.waitForFunction(() => document.querySelectorAll('[aria-label="Search results"] a[href*="?message="]').length === 3000);
    expect(await results.getByRole("group").count()).toBe(1000);
    expect(await results.getByRole("group", { name: "Wide 999", exact: true }).getByRole("heading").textContent()).toBe("Wide 999(3)");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("a search answer arriving keeps the selected search text, so typing replaces it", async () => {
  const { chromium: engine } = await import(playwright!);
  const started = Promise.withResolvers<void>();
  const held = Promise.withResolvers<void>();
  const { server, origin } = serve([], async ({ query }) => {
    if (query === "old words") { started.resolve(); await held.promise; }
    return response(query);
  });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`${origin}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("old words");
    await started.promise;
    await search.selectText();
    held.resolve();
    await page.getByRole("status").filter({ hasText: "Searching…" }).waitFor({ state: "hidden" });
    await page.keyboard.type("new");
    expect(await search.inputValue()).toBe("new");
  } finally {
    held.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("message matches jump after history loads and last close returns to the last visible message", async () => {
  const { chromium: engine } = await import(playwright!);
  const messages = (prefix: string) => Array.from({ length: 18 }, (_, i) => [
    { role: "user", messageId: `${prefix}-u${i}`, entryKey: `${prefix}-t${i}`, itemId: `${prefix}-item-u${i}`, inputId: `${prefix}-input${i}`, turnId: `${prefix}-t${i}`, text: `${prefix} user ${i}`, complete: true, origin: prefix === "one" && i === 1 ? "sandboxed" : "canonical" },
    { role: "assistant", messageId: `${prefix}-a${i}`, entryKey: `${prefix}-t${i}`, itemId: `${prefix}-item-a${i}`, inputId: "", turnId: `${prefix}-t${i}`, text: `${prefix} assistant ${i}`, complete: true, origin: prefix === "one" && i === 1 ? "sandboxed" : "canonical" },
  ]).flat();
  const pending = Promise.withResolvers<void>();
  let delayHistory = true;
  let owner = "tester";
  const api = tabsApi(() => owner);
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "one", name: "First chat" }, { id: "two", name: "Second chat" }], owner, upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: owner });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "jump" });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchSessions") return Response.json(response("one", [{ conversationId: "one", field: "" }], [], { messages: [
      { conversationId: "one", message: messages("one")[2] },
      { conversationId: "one", message: messages("one")[3] },
    ] }));
    if (url.pathname === "/api/History") {
      const { id } = await request.json() as { id: string };
      if (delayHistory && id === "one") await pending.promise;
      return Response.json({ messages: messages(id), origin: "", revision: "initial", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages(id).map((message) => message.entryKey))], running: false, terminal: "" });
    }
    if (url.pathname === "/api/Tabs") return api.reply(request);
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 650 } });
    const root = `http://127.0.0.1:${server.port}`;
    await page.goto(`${root}/search`);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.fill("one");
    await page.getByRole("link", { name: /one user 1/ }).click();
    await page.waitForURL("**/s/b25l?message=one-u1");
    expect(await page.locator('[data-message-id="one-u1"]').count()).toBe(0);
    delayHistory = false;
    pending.resolve();
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")!.getBoundingClientRect();
      const target = [...document.querySelectorAll('[data-message-id="one-u1"]')].find((node) => node.getBoundingClientRect().bottom > viewport.top && node.getBoundingClientRect().top < viewport.bottom);
      return !!target;
    });
    await page.reload();
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="one-u1"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    });
    await page.getByRole("button", { name: "sandboxed" }).click();
    // The Search page's tab, titled by its query, returns to it.
    const searchTab = page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { name: "one", exact: true });
    await searchTab.click();
    await page.getByRole("link", { name: /one user 1/ }).click();
    await page.waitForFunction(() => document.querySelector('[data-message-id="one-u1"]')?.getBoundingClientRect().height !== 0);
    expect(await page.getByRole("button", { name: "sandboxed" }).getAttribute("aria-pressed")).toBe("true");
    await searchTab.click();
    await page.getByRole("link", { name: /one assistant 1/ }).click();
    await page.waitForURL("**/s/b25l?message=one-a1");
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="one-a1"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    });
    await page.evaluate(() => {
      history.pushState(null, "", "/s/b25l?message=one-u14");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const target = document.querySelector('[data-message-id="one-u14"]')?.getBoundingClientRect();
      return !!viewport && !!target && target.bottom > viewport.top && target.top < viewport.bottom;
    }, undefined, { timeout: 5000 });
    expect(api.tabs().find((tab) => tab.path === "/search")!.search!.searches).toHaveLength(1);
    await page.evaluate(() => {
      history.pushState(null, "", "/s/dHdv");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await page.getByText("two assistant 17").waitFor();
    await page.locator("#transcript-scroll").hover();
    await page.mouse.wheel(0, -10000);
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")!;
      const bounds = viewport.getBoundingClientRect();
      const visible = [...viewport.querySelectorAll<HTMLElement>('[data-slot="message"][data-message-id]')].filter((node) => {
        const rect = node.getBoundingClientRect();
        return rect.bottom > bounds.top && rect.top < bounds.bottom;
      });
      return viewport.scrollTop === 0 && localStorage.getItem("last-seen:tester") === `/s/dHdv?message=${visible.at(-1)?.dataset.messageId}`;
    });
    const lastSeen = await page.evaluate(() => localStorage.getItem("last-seen:tester"));
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.getByRole("button", { name: "Close search 1" }).click();
    await page.waitForURL(root + lastSeen);
    expect(await page.getByRole("textbox", { name: "Search sessions" }).count()).toBe(0);
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:tester"))).toBeNull();
    owner = "bob";
    await page.waitForResponse(async (response: { url: () => string; json: () => Promise<{ username: string }> }) => response.url().endsWith("/api/Identity") && (await response.json()).username === "bob");
    await page.waitForTimeout(150);
    expect(await page.evaluate(() => localStorage.getItem("last-seen:bob"))).toBeNull();
  } finally {
    pending.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("search formats rows, windows around matches, and resolves tag names", async () => {
  const { chromium: engine } = await import(playwright!);
  const preview = "<!subteam^S0BA868QQ90> *Allen now says he would buy a Windows computer if memory is the cause.*\n\nsee [docs](https://example.com/a)\n```\ncode line\n```";
  const long = ["alpha", "bravo", "charlie", "delta", "echo", "the windows line", "foxtrot", "golf", "hotel", "india"].join("\n");
  const tagged = ["keep-out-before", "pad-a", "pad-b", "<!subteam^S0BA868QQ90> tagged here", "pad-c", "pad-d", "keep-out-after"].join("\n");
  const code = ["before 0", "before 1", "before 2", "MATCH", "after 0", "after 1", "after 2"];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "formatted", name: "", preview, tags: ["x"], agent: "main" }, { id: "long", name: "Long chat", preview: "", agent: "main" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-format" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SlackNames") return Response.json({ names: { S0BA868QQ90: "cs-operators" } });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      if (query === "tag:x") return Response.json(response(query, [{ conversationId: "formatted", text: preview }], ["tag:x"], { needle: "" }));
      if (query === "windows") return Response.json(response(query, [{ conversationId: "long", field: "" }], [], { messages: [{ conversationId: "long", message: { messageId: "1:0", role: "assistant", text: "*found windows here*", complete: true } }, { conversationId: "long", message: { messageId: "1:1", role: "assistant", text: long, complete: true } }] }));
      if (query === "cs-operators") return Response.json(response(query, [{ conversationId: "long", field: "" }], [], { messages: [{ conversationId: "long", message: { messageId: "2:0", role: "assistant", text: tagged, complete: true } }], mentionIds: ["S0BA868QQ90"] }));
      if (query === "fenced" || query === "streaming") return Response.json(response(query, [{ conversationId: "long", field: "" }], [], { messages: [{ conversationId: "long", message: { messageId: "3:0", role: "assistant", text: ["keep-out-before", "padding", "```text", ...code.map((line) => line.replace("MATCH", query)), ...(query === "fenced" ? ["```", "padding", "keep-out-after"] : [])].join("\n"), complete: query === "fenced" } }] }));
      return Response.json(response(query));
    }
    if (url.pathname === "/api/Tabs") return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const root = `http://127.0.0.1:${server.port}`;
    const results = page.getByLabel("Search results");
    await page.goto(`${root}/search?q=tag:x`);
    const group = results.getByRole("group", { name: "@cs-operators Allen now says he would buy a Windows computer if memory is the cause." });
    await group.waitFor();
    expect(await group.getAttribute("aria-label")).toBe("@cs-operators Allen now says he would buy a Windows computer if memory is the cause.");
    const heading = group.getByRole("heading").getByRole("link");
    expect(await heading.getAttribute("title")).toBe("@cs-operators Allen now says he would buy a Windows computer if memory is the cause.");
    expect(await heading.locator("strong").textContent()).toBe("Allen now says he would buy a Windows computer if memory is the cause.");
    expect(await results.locator("pre").textContent()).toContain("code line");
    const row = results.locator("a").filter({ has: page.locator("pre") });
    expect(await row.locator("a, button").count()).toBe(0);
    await page.goto(`${root}/search?q=windows`);
    const marked = results.getByRole("link", { name: /found windows/ });
    await marked.waitFor();
    expect(await marked.locator("strong mark").textContent()).toBe("windows");
    expect(await results.getByText("delta").count()).toBe(1);
    expect(await results.getByText("the windows line").count()).toBe(1);
    expect(await results.getByText("golf").count()).toBe(1);
    expect(await results.getByText("alpha").count()).toBe(0);
    expect(await results.getByText("india").count()).toBe(0);
    await page.goto(`${root}/search?q=cs-operators`);
    await results.getByText("@cs-operators").waitFor();
    expect(await results.locator("mark").textContent()).toBe("cs-operators");
    expect(await results.getByText("pad-a").count()).toBe(1);
    expect(await results.getByText("pad-d").count()).toBe(1);
    expect(await results.getByText("keep-out-before").count()).toBe(0);
    expect(await results.getByText("keep-out-after").count()).toBe(0);
    for (const query of ["fenced", "streaming"]) {
      await page.getByRole("textbox", { name: "Search sessions" }).fill(query);
      await results.locator("pre").filter({ hasText: query }).waitFor();
      expect(await results.locator("pre code").textContent()).toBe(code.join("\n").replace("MATCH", query) + (query === "fenced" ? "\n" : ""));
      expect(await results.locator("pre mark").textContent()).toBe(query);
      expect(await results.getByText("keep-out-before").count()).toBe(0);
      expect(await results.getByText("keep-out-after").count()).toBe(0);
    }
    await page.goto(`${root}/search?q=tag:x`);
    await results.locator("pre").waitFor();
    await results.locator("pre").click();
    await page.waitForURL("**/s/Zm9ybWF0dGVk");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
