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
  text: query, needle: query.toLowerCase(), matches: matches.map((match) => ({ field: "Preview", text: "", ...match })), messages: [], mentionIds: [], indexComplete: true, summariesComplete: true, ...extra,
});

// serve answers the App's requests with rows and a SearchSessions fixture, and records every API path.
function serve(rows: object[], search: (input: { query: string; messages: boolean }) => Promise<object> | object, agents = [{ name: "main" }]) {
  const paths = new Set<string>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    paths.add(url.pathname);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
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
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  return { server, paths, origin: `http://127.0.0.1:${server.port}` };
}

for (const width of [1280, 390]) test(`saved search tabs at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  let owner = "alice";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: async (request) => {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: owner });
    if (url.pathname === "/api/ListConfig") return Response.json({ config: { workspace: "search" } });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-page" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      return Response.json(response(query, [], ({ 'tag:"Needs review" is:forked': ['tag:"Needs review"', "is:forked"], "is:forked": ["is:forked"] } as Record<string, string[]>)[query]));
    }
    if (url.pathname.startsWith("/api/")) return Response.json({});
    const file = Bun.file(path.join(dist, url.pathname));
    return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 800 } });
    const searches = page.getByRole("tablist", { name: "Searches" });
    const tabs = searches.getByRole("tab");
    await page.goto(`http://127.0.0.1:${server.port}/`);
    // The footer Search button is Cmd/Ctrl+P; the Search page is the "Sessions: Search" command.
    await page.getByRole("button", { name: "Search sessions" }).click();
    await page.getByRole("dialog", { name: "Go to session" }).getByPlaceholder("Search sessions").waitFor();
    expect(new URL(page.url()).pathname).toBe("/");
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.waitForURL("**/search");
    const search = page.getByRole("textbox", { name: "Search sessions" });
    await search.waitFor();
    expect(await tabs.count()).toBe(1);
    expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key.startsWith("search-tabs:")))).toEqual([]);
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    expect(await tabs.count()).toBe(1);
    await search.fill("same");
    await page.getByRole("button", { name: "New search" }).click();
    await search.fill("same");
    const saved = await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:alice")!));
    expect(saved.tabs).toHaveLength(2);
    expect(saved.tabs[0].id).not.toBe(saved.tabs[1].id);
    await page.reload();
    await search.waitFor();
    expect(await tabs.last().getAttribute("aria-selected")).toBe("true");
    expect(await search.inputValue()).toBe("same");
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
    await search.fill(""); // An owner switch imports the URL search; an empty one keeps Bob's tab a draft.
    await page.waitForURL("**/search");
    await tabs.first().click();
    await name.fill("Alice");
    await name.press("Enter");
    await page.evaluate(() => localStorage.setItem("last-seen:alice", "/s/b25l?message=private"));
    owner = "bob";
    await searches.getByRole("tab", { name: "Search 1" }).waitFor({ timeout: 10_000 });
    expect(await search.inputValue()).toBe("");
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:bob"))).toBeNull();
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:alice")!).tabs)).toHaveLength(1);
    await search.press("Enter");
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:bob")!).tabs)).toHaveLength(1);
    await page.getByRole("button", { name: "Close search 1" }).click();
    await page.waitForURL(`http://127.0.0.1:${server.port}/`);
    expect(await page.evaluate(() => localStorage.getItem("search-tabs:bob"))).toBeNull();
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
    await page.waitForURL("**/search");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("search URL follows the active tab, folds legacy filters, and reopens a matching tab", async () => {
  const { chromium: engine } = await import(playwright!);
  const folded = "agent:alitu-cs-support outage";
  const { server, paths, origin } = serve([{ id: "chat", name: "Outage chat", preview: "", agent: "alitu-cs-support" }], ({ query }) => query === folded ? response(query, [{ conversationId: "chat", field: "Name", text: "Outage chat" }], ["agent:alitu-cs-support"]) : response(query));
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    const link = "/search?q=outage&agent=alitu-cs-support";
    const canonical = "/search?q=agent%3Aalitu-cs-support+outage";
    const at = (target: string) => page.waitForURL((url: URL) => url.pathname + url.search === target);
    const search = page.getByRole("textbox", { name: "Search sessions" });
    const tabs = page.getByRole("tablist", { name: "Searches" }).getByRole("tab");
    const opened = async (count: number) => {
      await page.getByRole("button", { name: "agent:alitu-cs-support" }).waitFor();
      expect(await tabs.count()).toBe(count);
      expect(await tabs.first().getAttribute("aria-selected")).toBe("true");
      expect(await search.inputValue()).toBe("outage");
    };
    await page.goto(`${origin}/`);
    await page.evaluate(() => localStorage.setItem("search-tabs:tester", JSON.stringify({ tabs: [{ id: "legacy", query: "outage", agentFilter: "alitu-cs-support", roomFilter: "" }], active: "legacy" })));
    await page.goto(origin + link);
    await opened(1); // The legacy tab, folded once into its query, matches the folded link.
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:tester")!).tabs)).toEqual([{ id: "legacy", query: folded }]);
    await at(canonical);
    await page.reload();
    await opened(1);
    await page.getByRole("button", { name: "New search" }).click();
    await at("/search");
    await search.fill("other room");
    await at("/search?q=other+room");
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

test("Cmd+P asks the server as you type, shows its pills, and Enter opens the first row for the typed text", async () => {
  const { chromium: engine } = await import(playwright!);
  const requests: { query: string; messages: boolean }[] = [];
  const held = Promise.withResolvers<void>();
  const started = Promise.withResolvers<void>();
  let failing = true;
  const { server, paths, origin } = serve([{ id: "a", name: "Alpha", agent: "main", tags: ["bug"] }, { id: "b", name: "Bravo", agent: "main" }, { id: "c", name: "Charlie", agent: "main" }], async (input) => {
    requests.push(input);
    const { query } = input;
    if (query === "tag:bug x") { started.resolve(); await held.promise; }
    if (query === "boom" && failing) throw new Error("search unavailable");
    const ids = ({ foo: ["c", "a"], fas: ["a"], fast: ["b"], boom: ["c"], "tag:bug x": ["a"] } as Record<string, string[]>)[query] ?? [];
    return response(query, ids.map((conversationId) => ({ conversationId })), query.startsWith("tag:") ? [query.split(" ")[0]] : []);
  });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(origin);
    await page.getByRole("button", { name: "Search sessions", exact: true }).waitFor();
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
    await page.waitForTimeout(400);
    expect(requests).toEqual([]); // Unfinished operators are not sent.
    await input.fill("foo");
    await titles(["Charlie", "Alpha"]);
    expect(requests).toEqual([{ query: "foo", messages: false }]);

    const pills = palette.getByRole("button", { name: /^tag:/ });
    await input.fill("tag:bu");
    await titles([]);
    expect(requests.at(-1)!.query).toBe("tag:bu");
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

    await input.fill("fas");
    await titles(["Alpha"]);
    await input.pressSequentially("t");
    await input.press("Enter"); // Before the pause ends: Enter sends "fast" and opens its first row, not Alpha.
    await page.waitForURL(`**/s/${Buffer.from("b").toString("base64url")}`);
    expect(requests.filter(({ query }) => query === "fast")).toHaveLength(1);

    await page.keyboard.press("Control+p");
    await input.fill("nothing");
    await input.press("Enter");
    await page.waitForTimeout(300);
    expect(requests.at(-1)!.query).toBe("nothing");
    expect(await palette.isVisible()).toBe(true); // An empty response opens nothing.
    await input.fill("boom");
    await palette.getByRole("alert").filter({ hasText: "Search failed" }).waitFor();
    await palette.getByText("Search incomplete", { exact: true }).waitFor();
    await input.press("Enter");
    await page.waitForTimeout(100);
    expect(await palette.isVisible()).toBe(true); // A failed response opens nothing.
    failing = false;
    await input.press("End");
    await input.pressSequentially(" "); // The next keystroke retries the same text.
    await titles(["Charlie"]);
    expect(await palette.getByRole("alert").count()).toBe(0);
    expect(paths.has("/api/SearchOrigins")).toBe(false);
  } finally {
    held.resolve();
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("/search renders the server's groups, notices and pills", async () => {
  const { chromium: engine } = await import(playwright!);
  const requests: { query: string; messages: boolean }[] = [];
  const hold = Promise.withResolvers<void>();
  const rows = [
    { id: "third", name: "Pinned conversation", preview: "Other text", agent: "main", pinned: true },
    { id: "second", name: "", title: "", preview: "Needle notes\nOther text", agent: "main" },
    { id: "fourth", name: "Origin chat", preview: "Other text", agent: "main" },
  ];
  const message = (messageId: string, role: string, text: string) => ({ conversationId: "third", message: { messageId, role, text, complete: true } });
  const { server, paths, origin } = serve(rows, async (input) => {
    requests.push(input);
    const { query } = input;
    if (query === "agent:main" && requests.filter((request) => request.query === query).length === 1) await hold.promise;
    switch (query) {
      case "needle": return response(query, [{ conversationId: "third", field: "" }, { conversationId: "second", text: "Needle notes\nOther text" }, { conversationId: "fourth", field: "Origin", text: "cron source: /notes/needle.md" }], [], { messages: [message("1:0", "user", "Context before NEEDLE then needle"), message("1:1", "assistant", "A second needle response")] });
      case "partial": return response(query, [], [], { indexComplete: false, summariesComplete: false });
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
    const stored = () => page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:tester")!).tabs[0].query);
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

    await search.fill("partial");
    await results.getByText("Message results may be incomplete while chats are still being indexed.").waitFor();
    await results.getByText("Results may be incomplete while chat summaries are still loading.").waitFor();
    expect(await results.getByText("No matches").count()).toBe(0);

    await search.fill("chat is:pinned"); // A saved query ending in a filter reopens with that filter.
    await results.getByRole("group", { name: "Pinned conversation" }).waitFor();
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
    expect(await stored()).toBe("tag:bug foobar");
    await sent("tag:bug foobar");
    await tagPill.waitFor();
    expect(await search.inputValue()).toBe("foobar");
    expect(await search.evaluate((node: HTMLInputElement) => node.selectionStart)).toBe(3);
    await tagPill.click();
    await search.fill("foo tag:bug bar");
    await tagPill.waitFor();
    await search.fill("outage"); // Select-all and type.
    expect(await stored()).toBe("tag:bug outage");
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
    expect(await agentPill("main").count()).toBe(0);
    expect(await stored()).toBe("agent:other ");
    hold.resolve();
    await search.pressSequentially("agent:");
    await page.getByRole("button", { name: "main", exact: true }).click();
    await agentPill("main").waitFor();
    expect(await agentPill("other").count()).toBe(0);
    expect(await stored()).toBe("agent:main ");
    await agentPill("main").click();

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

test("newer search replaces an in-flight search and retries errors", async () => {
  const { chromium: engine } = await import(playwright!);
  const started = Promise.withResolvers<void>();
  const delayed = Promise.withResolvers<void>();
  const searches: string[] = [];
  let fail = true;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", name: "A chat", preview: "" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
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

test("message matches jump after history loads and last close returns to the last visible message", async () => {
  const { chromium: engine } = await import(playwright!);
  const messages = (prefix: string) => Array.from({ length: 18 }, (_, i) => [
    { role: "user", messageId: `${prefix}-u${i}`, entryKey: `${prefix}-t${i}`, itemId: `${prefix}-item-u${i}`, inputId: `${prefix}-input${i}`, turnId: `${prefix}-t${i}`, text: `${prefix} user ${i}`, complete: true, origin: prefix === "one" && i === 1 ? "sandboxed" : "canonical" },
    { role: "assistant", messageId: `${prefix}-a${i}`, entryKey: `${prefix}-t${i}`, itemId: `${prefix}-item-a${i}`, inputId: "", turnId: `${prefix}-t${i}`, text: `${prefix} assistant ${i}`, complete: true, origin: prefix === "one" && i === 1 ? "sandboxed" : "canonical" },
  ]).flat();
  const pending = Promise.withResolvers<void>();
  let delayHistory = true;
  let owner = "tester";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "one", name: "First chat" }, { id: "two", name: "Second chat" }], owner, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
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
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
    await page.getByRole("link", { name: /one user 1/ }).click();
    await page.waitForFunction(() => document.querySelector('[data-message-id="one-u1"]')?.getBoundingClientRect().height !== 0);
    expect(await page.getByRole("button", { name: "sandboxed" }).getAttribute("aria-pressed")).toBe("true");
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Sessions: Search" }).click();
    await page.getByRole("dialog").waitFor({ state: "detached" });
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
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem("search-tabs:tester")!).tabs)).toHaveLength(1);
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
    await page.getByRole("button", { name: "Open command palette", exact: true }).click();
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
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "formatted", name: "", preview, tags: ["x"], agent: "main" }, { id: "long", name: "Long chat", preview: "", agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/Identity") return Response.json({ username: "tester" });
    if (url.pathname === "/api/Protocol") return Response.json({ protoSha256: "search-format" });
    if (url.pathname === "/api/ListAgents") return Response.json({ agents: [{ name: "main" }] });
    if (url.pathname === "/api/SlackNames") return Response.json({ names: { S0BA868QQ90: "cs-operators" } });
    if (url.pathname === "/api/SearchSessions") {
      const { query } = await request.json() as { query: string };
      if (query === "tag:x") return Response.json(response(query, [{ conversationId: "formatted", text: preview }], ["tag:x"], { needle: "" }));
      if (query === "windows") return Response.json(response(query, [{ conversationId: "long", field: "" }], [], { messages: [{ conversationId: "long", message: { messageId: "1:0", role: "assistant", text: "*found windows here*", complete: true } }, { conversationId: "long", message: { messageId: "1:1", role: "assistant", text: long, complete: true } }] }));
      if (query === "cs-operators") return Response.json(response(query, [{ conversationId: "long", field: "" }], [], { messages: [{ conversationId: "long", message: { messageId: "2:0", role: "assistant", text: tagged, complete: true } }], mentionIds: ["S0BA868QQ90"] }));
      return Response.json(response(query));
    }
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
    await page.goto(`${root}/search?q=tag:x`);
    await results.locator("pre").waitFor();
    await results.locator("pre").click();
    await page.waitForURL("**/s/Zm9ybWF0dGVk");
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
