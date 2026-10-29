import { expect, test } from "bun:test";
import path from "node:path";
import type { Session } from "./types";
import { linked, fetch as openCode, reset, server as fake } from "../../oc/host/test/server";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const ocDist = path.resolve(import.meta.dir, "../../internal/web/oc-dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");
const encode = (text: string) => btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const route = (id: string) => `/oc/${encode(id)}`;
// The box server's own address, which RocketClaw only ever sends as an origin.
const box = "http://box.test:4096";

// A RocketClaw Web serving the built view at /oc-ui/ and the fake OpenCode server behind /oc/box/api/.
function start() {
  reset();
  const rows: Session[] = [
    { id: "opencode:box:ses_a", kind: "REMOTE_OPENCODE", remoteSessionId: "ses_a", preview: "Alpha work", server: "box", directory: "/repo", updatedAt: new Date().toISOString() },
    { id: "opencode:box:ses_off", kind: "REMOTE_OPENCODE", remoteSessionId: "ses_off", preview: "Old work", server: "box", directory: "/srv/old", offline: true, tags: ["billing"] },
  ];
  const calls: { path: string; body: { id?: string; [key: string]: unknown } }[] = [];
  // The view's entry waits until released, so its loading state can be seen.
  let release = () => {};
  const entry = new Promise<void>((resolve) => { release = resolve; });
  let failCreate = true;
  let failFork = true;
  // Terminal sockets the view holds open, counted where OpenCode would serve them.
  const terminals = { open: 0, input: [] as string[] };
  // A test holds session creates here to press again while one is still running.
  const creates = { hold: Promise.resolve() };
  // Loads of the view's entry left to fail, as a network blip or a deploy mid-load would.
  const entryFailures = { remaining: 0 };
  const web = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, websocket: { open() { terminals.open++; }, close() { terminals.open--; }, message(_socket, message) { terminals.input.push(message.toString()); } }, async fetch(request, server) {
    const url = new URL(request.url);
    if (/^\/oc\/box\/api\/pty\/[^/]+\/connect$/.test(url.pathname)) return server.upgrade(request) ? undefined : new Response("not a WebSocket", { status: 400 });
    if (url.pathname.startsWith("/oc/box/api/")) return openCode(request);
    if (url.pathname.startsWith("/oc-ui/")) {
      if (url.pathname === "/oc-ui/_assets/index.js") {
        await entry;
        if (entryFailures.remaining > 0) {
          entryFailures.remaining--;
          return new Response("unavailable", { status: 503 });
        }
      }
      return new Response(Bun.file(path.join(ocDist, url.pathname.slice("/oc-ui/".length))));
    }
    if (url.pathname === "/stream") {
      calls.push({ path: url.pathname, body: { id: url.searchParams.get("id") ?? "" } });
      return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, opencodeServers: { box }, agentTags: { main: { tags: ["customer"] } } })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
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
        await creates.hold;
        if (failCreate) { failCreate = false; return Response.json({ code: 14, message: "box did not answer" }, { status: 503 }); }
        return Response.json({ id: "opencode:box:ses_child" });
      case "/api/ForkSession": {
        if (failFork) { failFork = false; return Response.json({ code: 14, message: "Fork was refused" }, { status: 503 }); }
        const forked = await openCode(new Request(`${url.origin}/oc/box/api/session/${input.id?.split(":").at(-1)}/fork`, { method: "POST", body: JSON.stringify({ before: input.before }) }));
        const body: { data: { id: string } } = await forked.json();
        return Response.json({ id: `opencode:box:${body.data.id}` });
      }
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: route("opencode:box:ses_a"), preview: true, space: "main" }], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  return { web, calls, release, terminals, creates, entryFailures, base: `http://127.0.0.1:${web.port}` };
}

async function until(check: () => boolean | Promise<boolean>) {
  for (let tries = 0; tries < 250; tries++) {
    if (await check()) return;
    await Bun.sleep(20);
  }
  throw new Error("timed out");
}

test("a remote session mounts OpenCode's view in the page, with RocketClaw's keys, commands, and tabs around it", async () => {
  const { chromium: engine } = await import(playwright!);
  const { web, calls, release, terminals, base } = start();
  fake.extraText = '<script>window.__ocUnsafe=1</script><img src=x onerror="window.__ocUnsafe=1"><a href="javascript:alert(1)">unsafe link</a>\n\n```mermaid\nflowchart LR\nA["<img src=x onerror=alert(1)>"]\nclick A "javascript:alert(1)"\n```';
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, permissions: ["clipboard-read", "clipboard-write"] });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const tabs = page.getByRole("tablist", { name: "Open tabs" });
    const view = page.locator("main [data-oc-view]");
    const palette = page.locator("[data-oc-portal] .command-palette");
    const rocketPalette = (mode: "sessions" | "commands") => page.getByPlaceholder(mode === "sessions" ? "Search sessions and commands" : "Type a command");
    const clipboard = () => page.evaluate(() => navigator.clipboard.readText());
    const command = async (label: string) => {
      await page.keyboard.press("ControlOrMeta+Shift+p");
      await rocketPalette("commands").fill(label);
      await page.getByRole("button", { name: `Sessions: ${label}` }).click();
    };
    // RocketClaw's look, measured on its own page and text: fonts are page-wide, so a leaked one changes text width.
    const look = () => page.evaluate(async () => {
      await document.fonts.ready;
      const probe = document.createElement("span");
      probe.className = "font-sans";
      probe.textContent = "RocketClaw sessions Wwiil 0123";
      document.body.append(probe);
      const width = probe.getBoundingClientRect().width;
      probe.remove();
      const body = getComputedStyle(document.body);
      const strip = getComputedStyle(document.querySelector('[aria-label="Open tabs"]')!);
      return { width, background: body.backgroundColor, color: body.color, font: body.fontFamily, strip: [strip.backgroundColor, strip.fontFamily, strip.fontSize] };
    });

    await page.goto(base);
    await tabs.getByRole("tab", { name: "Alpha work", exact: true }).waitFor();
    const before = await look();

    // Opening the session shows Loading… until the view reports it, then the view itself: no frame.
    await tabs.getByRole("tab", { name: "Alpha work", exact: true }).click();
    await page.getByRole("status").filter({ hasText: "Loading…" }).waitFor();
    release();
    await view.getByText("answer in ses_a").waitFor();
    // Markdown is sanitized and approved Pele keeps diagram labels as text, not executable HTML.
    await view.locator('[data-component="markdown-mermaid"] svg').waitFor();
    expect(await view.locator('script, [onerror], a[href^="javascript:"]').count()).toBe(0);
    expect(await view.locator('[data-component="markdown-mermaid"] img, [data-component="markdown-mermaid"] image').count()).toBe(0);
    expect(await page.evaluate(() => (window as Window & { __ocUnsafe?: number }).__ocUnsafe)).toBeUndefined();
    // Extensions keep their empty states and configuration links, but no unsupported draft-prompt setup buttons.
    await view.getByRole("button", { name: "Session details", exact: true }).click();
    const details = page.locator('[data-oc-portal] [data-component="session-summary-panel"]');
    for (const [service, empty] of [["mcp", "No MCP servers configured"], ["plugins", "No plugins configured"], ["skills", "No skills configured"], ["lsp", "No LSP servers configured"]]) {
      const pane = details.locator(`[data-service="${service}"]`);
      await pane.getByText(empty, { exact: true }).waitFor({ state: "attached" });
      expect(await pane.getByRole("button", { name: "Configuration file", exact: true, includeHidden: true }).count()).toBe(1);
    }
    expect(await details.getByRole("button", { name: /with OpenCode/, includeHidden: true }).count()).toBe(0);
    await page.keyboard.press("Escape");
    expect(await page.getByRole("status").filter({ hasText: "Loading…" }).count()).toBe(0);
    expect(await page.locator("iframe").count()).toBe(0);
    expect(fake.requests.some((request) => request.startsWith("GET /oc/box/api/session/ses_a"))).toBe(true);
    expect(calls.filter(({ path, body }) => (path === "/api/History" || path === "/stream" || path === "/api/ListQueue") && body.id?.startsWith("opencode:"))).toEqual([]);
    expect(await look()).toEqual(before);
    await page.screenshot({ path: path.join(shots, "opencode-session.png") });

    // Leaving and coming back leaves one view and one event stream.
    await page.goBack();
    await until(async () => await view.count() === 0 && fake.streams === 0);
    await page.goForward();
    await view.getByText("answer in ses_a").waitFor();
    await until(() => fake.streams === 1);
    await Bun.sleep(200);
    expect([await page.locator("[data-oc-view]:not([data-oc-portal])").count(), fake.streams]).toEqual([1, 1]);

    // Escape belongs to the view: twice from the composer, and twice after clicking timeline text, the tab stays.
    const tabCount = await tabs.getByRole("tab").count();
    await view.locator("[data-component=composer] [contenteditable=true]").click();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await view.getByText("answer in ses_a").click();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    expect([page.url(), await tabs.getByRole("tab").count()]).toEqual([`${base}${route("opencode:box:ses_a")}`, tabCount]);

    // From inside the view, ⌘P is RocketClaw's palette and ⌘K OpenCode's.
    await view.locator("[data-component=composer] [contenteditable=true]").click();
    await page.keyboard.press("ControlOrMeta+p");
    await rocketPalette("sessions").waitFor();
    expect(await palette.count()).toBe(0);
    await page.keyboard.press("Escape");
    await view.locator("[data-component=composer] [contenteditable=true]").click();
    await page.keyboard.press("ControlOrMeta+k");
    await palette.waitFor();
    expect(await rocketPalette("sessions").count()).toBe(0);
    await page.keyboard.press("Escape");
    await until(async () => await palette.count() === 0);

    // The terminal keeps unbound keys, but host shortcuts must not also reach its socket.
    await view.locator("[data-component=composer] [contenteditable=true]").click();
    await page.keyboard.press("Control+Backquote");
    await view.locator('[data-component="terminal"]').waitFor();
    await view.locator('[data-pane-content="bottom"] [data-component="terminal"]').waitFor();
    expect(await view.locator('[data-pane-content="side"]').isVisible()).toBe(false);
    await until(() => terminals.open === 1);
    // Reload keys must reach the browser unchanged, not toggle review or enter the terminal.
    for (const target of [view.locator("[data-component=composer] [contenteditable=true]"), view.getByText("answer in ses_a", { exact: true }), view.locator('[data-component="terminal"]')]) {
      await target.click();
      expect(await target.evaluate((node: HTMLElement) => ["KeyR", "KeyO"].flatMap((code) => ["ctrlKey", "metaKey"].map((modifier) => {
        const event = new KeyboardEvent("keydown", { key: "R", code, shiftKey: true, [modifier]: true, bubbles: true, cancelable: true });
        node.dispatchEvent(event);
        return event.defaultPrevented;
      })))).toEqual([false, false, false, false]);
      expect(await view.locator('[data-pane-content="side"]').isVisible()).toBe(false);
    }
    expect(terminals.input).toEqual([]);
    for (const modifier of ["Control", "Meta"]) {
      for (const mode of ["sessions", "commands"] as const) {
        await view.locator('[data-component="terminal"]').click();
        await page.keyboard.press(`${modifier}+${mode === "commands" ? "Shift+" : ""}p`);
        await rocketPalette(mode).waitFor();
        expect(await palette.count()).toBe(0);
        await page.locator('[data-slot="dialog-overlay"]').click({ position: { x: 5, y: 5 } });
      }
    }
    await view.locator('[data-component="terminal"]').click();
    await page.keyboard.down("Control");
    await page.keyboard.down("p");
    await rocketPalette("sessions").waitFor();
    await page.keyboard.up("Control");
    await page.locator('[data-slot="dialog-overlay"]').click({ position: { x: 5, y: 5 } });
    await view.locator('[data-component="terminal"]').click();
    await page.keyboard.down("Control");
    await page.keyboard.down("p");
    await page.keyboard.up("p");
    await page.keyboard.up("Control");
    await page.keyboard.press("x");
    await until(() => terminals.input.join("").endsWith("x"));
    expect(await rocketPalette("sessions").count()).toBe(0);
    expect(terminals.input.join("")).toBe("x");

    // Native append carries the stored panes property when the last terminal is reopened.
    await view.locator('[data-pane-tab^="terminal:"]').getByRole("button", { name: "Close tab", exact: true }).click();
    await until(() => terminals.open === 0);
    await view.locator("[data-component=composer] [contenteditable=true]").click();
    await page.keyboard.press("Control+Backquote");
    await view.locator('[data-pane-content="bottom"] [data-component="terminal"]').waitFor();
    await until(() => terminals.open === 1);

    // Moving the terminal uses OpenCode's real drag state; the host must keep that choice after remounting.
    await view.locator('[data-slot="session-review-toggle"]').click();
    await view.locator('[data-pane-tab^="terminal:"]').dragTo(view.locator('[data-pane-content="side"]'), {
      targetPosition: { x: 40, y: 40 },
      steps: 10,
    });
    await view.locator('[data-pane-content="side"] [data-component="terminal"]').waitFor();

    // Leaving the session closes the terminal's socket with the rest of the view.
    await page.goBack();
    await until(async () => await view.count() === 0 && terminals.open === 0 && fake.streams === 0);
    await page.goForward();
    await view.getByText("answer in ses_a").waitFor();
    await view.locator('[data-pane-content="side"] [data-component="terminal"]').waitFor();
    // The restored terminal must not take focus during the remaining keyboard checks.
    await view.locator('[data-pane-tab^="terminal:"]').getByRole("button", { name: "Close tab", exact: true }).click();
    await until(() => terminals.open === 0);

    // The Side pane's own launcher is an explicit destination, not a request for the bottom default.
    await view.locator('[data-slot="session-review-toggle"]').click();
    const side = view.locator('[data-pane-content="side"]');
    await side.getByRole("button", { name: "Add tab", exact: true }).click();
    await side.getByRole("button", { name: /^Terminal/ }).click();
    await side.locator('[data-component="terminal"]').waitFor();
    await side.locator('[data-pane-tab^="terminal:"]').getByRole("button", { name: "Close tab", exact: true }).click();
    await until(() => terminals.open === 0);

    // The server link names the shown session on its own server, with no password.
    await command("Copy server link");
    await until(async () => await clipboard() === `${box}/server/${encode(box)}/session/ses_a`);
    await command("OpenCode commands");
    await palette.waitFor();
    await page.keyboard.press("Escape");
    await until(async () => await palette.count() === 0);

    // A child session opens inside the view, and the link follows it. RocketClaw's default timeline detail shows its card.
    await view.locator("[data-component=task-tool-card]").first().click();
    await view.getByText("answer in ses_child").waitFor();
    await command("Copy server link");
    await until(async () => await clipboard() === `${box}/server/${encode(box)}/session/ses_child`);
    expect(page.url()).toBe(`${base}${route("opencode:box:ses_a")}`);

    // Forking opens the fork, a session the list has not seen, in a new RocketClaw tab.
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    release();
    await view.getByText("answer in ses_a").waitFor();
    const beforeForkTabs = await tabs.getByRole("tab").count();
    await view.locator("[data-component=composer] [contenteditable=true]").fill("/fork");
    // oc/packages/app/src/composer/suggestions/machine.ts consumes Enter until a result is active.
    await view.locator('[data-suggestion-id="session.fork"][data-active]').waitFor();
    await page.keyboard.press("Enter");
    await page.locator("[data-oc-portal]").locator("[role=option], button", { hasText: "question in ses_a" }).first().click();
    await page.getByText("Fork was refused", { exact: true }).waitFor();
    expect(page.url()).toBe(`${base}${route("opencode:box:ses_a")}`);
    expect(await page.locator('[role="tablist"][aria-label="Open tabs"] [role="tab"]').count()).toBe(beforeForkTabs);
    await page.locator("[data-oc-portal]").locator("[role=option], button", { hasText: "question in ses_a" }).first().click();
    await page.waitForURL(new RegExp(`/oc/${encode("opencode:box:ses_fork_").slice(0, -2)}`));
    await view.getByText(/answer in ses_fork_/).waitFor();
    expect(calls.filter(({ path }) => path === "/api/ForkSession").map(({ body }) => body)).toEqual(Array(2).fill({ id: "opencode:box:ses_a", before: "msg_ses_a_1" }));
    expect(await view.locator("[data-component=composer] [contenteditable=true]").textContent()).toBe("question in ses_a");
    const forkTabs = await tabs.getByRole("tab").count();

    // Deleting the shown session closes its tab.
    await view.locator("[aria-label='More options']").first().click();
    await page.locator("[data-oc-portal] [role=menuitem]", { hasText: "Delete" }).click();
    await page.locator("[data-oc-portal] button", { hasText: "Delete session" }).click();
    await until(async () => await tabs.getByRole("tab").count() === forkTabs - 1);
    expect(page.url()).not.toContain(encode("opencode:box:ses_fork_").slice(0, -2));
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 60_000);

test("remote sessions list, tag, go offline, and start", async () => {
  const { chromium: engine } = await import(playwright!);
  const { web, calls, release, creates, entryFailures, base } = start();
  release();
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));

    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await page.locator("main [data-oc-view] [data-slot=session-review-toggle]").waitFor();

    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("Name session");
    await page.getByRole("button", { name: "Sessions: Name session" }).click();
    const rename = page.getByRole("dialog", { name: "Name session", exact: true });
    await rename.getByText("Shared with everyone who can see this session. Leave blank to clear the OpenCode title too.", { exact: true }).waitFor();
    await rename.getByRole("button", { name: "Cancel", exact: true }).click();

    // People type any tag on a remote session; tags already in use are suggested, agent tags are not offered.
    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("tag");
    expect(await page.getByRole("button", { name: "Sessions: Add tag customer" }).count()).toBe(0);
    await page.getByRole("button", { name: "Sessions: Add tag…" }).click();
    const tag = page.getByRole("dialog", { name: "Add tag" }).getByLabel("Tag");
    expect(await page.locator("datalist option").evaluateAll((options: Element[]) => options.map((option) => (option as HTMLOptionElement).value))).toEqual(["billing"]);
    await tag.fill("billing");
    await tag.press("Enter");
    await until(() => calls.some(({ path }) => path === "/api/UpdateSession"));
    expect(calls.find(({ path }) => path === "/api/UpdateSession")?.body).toEqual({ id: "opencode:box:ses_a", tag: "billing" });

    // An offline server's session shows a notice instead of the view; a server no longer configured, another.
    await page.goto(`${base}${route("opencode:box:ses_off")}`);
    await page.getByText("box is offline; this session reopens when it is reachable.").waitFor();
    expect(await page.locator("[data-oc-view]").count()).toBe(0);
    await page.goto(`${base}${route("opencode:gone:ses_x")}`);
    await page.getByText("This OpenCode session is no longer available.").waitFor();

    // A new session takes a typed folder; a failed create shows its error and the next try opens the session.
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await page.locator("main [data-oc-view]").getByText("answer in ses_a").waitFor();
    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("new on box");
    await page.getByRole("button", { name: "Sessions: New on box" }).click();
    const folder = page.getByPlaceholder("Folder on box, like /srv/app");
    // Folders the person picked lead the server's other folders, most recent first.
    const folders = page.getByRole("button", { name: /^\// });
    await folders.first().waitFor();
    expect(await folders.allTextContents()).toEqual(["/repo", "/srv/old"]);
    await page.keyboard.press("Escape");
    await page.evaluate(() => localStorage.setItem("command-history", JSON.stringify(["folder-/srv/old"])));
    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("new on box");
    await page.getByRole("button", { name: "Sessions: New on box" }).click();
    await folders.first().waitFor();
    expect(await folders.allTextContents()).toEqual(["/srv/old", "/repo"]);
    await folder.fill("/srv/fresh");
    await folder.press("Enter");
    await page.getByText("box did not answer").waitFor();
    await folder.press("Enter");
    await page.waitForURL(`${base}${route("opencode:box:ses_child")}`);
    await page.locator("main [data-oc-view]").getByText("answer in ses_child").waitFor();
    expect(calls.filter(({ path }) => path === "/api/CreateSession").map(({ body }) => body)).toEqual([{ opencodeServer: "box", directory: "/srv/fresh", spaceId: "main" }, { opencodeServer: "box", directory: "/srv/fresh", spaceId: "main" }]);

    // OpenCode's New session, from inside the view, goes through RocketClaw: the session's own folder, a new tab.
    const view = page.locator("main [data-oc-view]");
    const palette = page.locator("[data-oc-portal] .command-palette");
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await view.getByText("answer in ses_a").waitFor();
    await page.keyboard.press("ControlOrMeta+k");
    await palette.locator("input").fill("New session");
    await palette.locator(".command-palette-row", { hasText: "New session" }).first().click();
    await page.waitForURL(`${base}${route("opencode:box:ses_child")}`);
    await view.getByText("answer in ses_child").waitFor();
    expect(calls.filter(({ path }) => path === "/api/CreateSession").map(({ body }) => body).slice(2)).toEqual([{ opencodeServer: "box", directory: "/repo", spaceId: "main" }]);

    // Pressing New session again while the first is still being created starts one session, not two.
    const created = () => calls.filter(({ path }) => path === "/api/CreateSession").length;
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await view.getByText("answer in ses_a").waitFor();
    let finish = () => {};
    creates.hold = new Promise<void>((resolve) => { finish = resolve; });
    const before = created();
    await page.keyboard.press("ControlOrMeta+k");
    await palette.locator("input").fill("New session");
    await palette.locator(".command-palette-row", { hasText: "New session" }).first().click();
    await until(() => created() === before + 1);
    await page.keyboard.press("ControlOrMeta+k");
    await palette.locator("input").fill("New session");
    await palette.locator(".command-palette-row", { hasText: "New session" }).first().click();
    await Bun.sleep(300);
    finish();
    await page.waitForURL(`${base}${route("opencode:box:ses_child")}`);
    await Bun.sleep(300);
    expect(created()).toBe(before + 1);

    // A session the conversation mentions opens in its own RocketClaw tab.
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await view.locator(`button[data-session-id="${linked}"]`).click();
    await page.waitForURL(`${base}${route(`opencode:box:${linked}`)}`);
    await view.getByText(`answer in Session ${linked}`).waitFor();
    expect(errors).toEqual([]);

    // When the view's code fails to load, Retry loads it again without reloading the page.
    const fresh = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    entryFailures.remaining = 1;
    await fresh.goto(`${base}${route("opencode:box:ses_a")}`);
    await fresh.getByRole("alert").waitFor();
    await fresh.getByRole("button", { name: "Retry", exact: true }).click();
    await fresh.locator("main [data-oc-view]").getByText("answer in ses_a").waitFor();
    expect(await fresh.getByRole("alert").count()).toBe(0);
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 60_000);
