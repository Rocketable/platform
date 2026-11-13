import { expect, test } from "bun:test";
import path from "node:path";
import type { Session, TopShelf } from "./types";
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

test("thread PR pane reads with the OpenCode host's gh login beside the real embedded conversation", async () => {
  const { chromium: engine } = await import(playwright!);
  const { web, release, base } = start();
  release();
  fake.sessions.get("ses_child")!.location = { directory: "/repo/child" };
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  const commands: { command: string; cwd: string }[] = [], removed: string[] = [], prompts: string[] = [];
  const outputs = new Map<string, string>();
  let checkoutUnavailable = true;
  let directory = "/repo";
  let holdMetadata = false;
  const pendingMetadata = Promise.withResolvers<void>(), releaseMetadata = Promise.withResolvers<void>();
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("request", (request: any) => { if (request.method() === "POST" && /\/session\/[^/]+\/(?:prompt|message|shell)(?:\?|$)/.test(request.url())) prompts.push(request.url()); });
    await page.route("**/oc/box/api/**", async (request: any) => {
      const url = new URL(request.request().url());
      if (!url.searchParams.has("pull-request-discovery")) return request.continue();
      if (url.pathname.endsWith("/message")) return request.fulfill({ json: { data: url.pathname.includes("/ses_a/") ? [{ type: "assistant", content: [
        { type: "text", text: "https://github.com/sample/fixture-repository-marker/pull/99" },
        { type: "tool", name: "shell", state: { status: "completed", input: { command: "gh pr create --title 'Sample' --body 'Sample'" }, metadata: { exit: 0 }, content: [{ type: "text", text: "https://github.com/sample/fixture-repository-marker/pull/7" }] } },
      ] }] : [], cursor: {} } });
      if (url.pathname.endsWith("/api/session")) return request.fulfill({ json: { data: [], cursor: {} } });
      if (url.pathname.includes("/session/")) return request.fulfill({ json: { data: { location: { directory: url.pathname.endsWith("/ses_child") ? "/repo/child" : directory }, time: { created: 1 } } } });
      if (request.request().method() === "POST") {
        const body = request.request().postDataJSON(); commands.push(body);
        const command = body.command as string;
        const output = command.includes("gh pr view") ? checkoutUnavailable ? "could not determine current branch" : JSON.stringify({ url: `https://github.com/sample/fixture-repository-marker/pull/${body.cwd === "/repo/child" ? 8 : 7}`, headRefName: "feature/pr-pane", mergedAt: null, closedAt: null })
          : command.includes("/files?") ? JSON.stringify([{ filename: "private-file-marker.ts", status: "modified", additions: 1, deletions: 0, patch: "@@ -0,0 +1 @@\n+private-patch-marker <script>text only</script>" }])
          : JSON.stringify({ title: command.includes("/pulls/8") ? "Second thread PR marker" : "Remote private PR marker", body: "Private description marker", state: "open", draft: true, merged: false, changed_files: 1, user: { login: "demo-author" }, updated_at: "2026-11-12T12:00:00Z", additions: 1, deletions: 0, head: { sha: "head", ref: "feature/pr-reader" }, base: { sha: "base", ref: "main" } });
        const id = `sh_pr_${outputs.size}`; outputs.set(id, output);
        if (holdMetadata && body.cwd === "/repo/moved" && command.endsWith("/pulls/7'")) { pendingMetadata.resolve(); await releaseMetadata.promise; }
        return request.fulfill({ json: { data: { id, status: "exited", exit: command.includes("gh pr view") && checkoutUnavailable ? 1 : 0 } } });
      }
      const id = url.pathname.split("/").find((part) => part.startsWith("sh_pr_"))!;
      if (request.request().method() === "DELETE") { removed.push(id); return request.fulfill({ status: 204 }); }
      const output = outputs.get(id)!;
      return request.fulfill({ json: { data: { output, cursor: output.length, size: output.length, truncated: false } } });
    });
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    const view = page.locator("main [data-oc-view]");
    await view.getByText("answer in ses_a", { exact: true }).waitFor();
    const draft = view.locator('[contenteditable="true"]').first();
    await draft.fill("OpenCode draft before reading a PR");
    expect(await page.locator("#bottom-navigation").getByRole("button").count()).toBe(6);
    await page.getByRole("button", { name: "Tab actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).click();
    const pane = page.getByRole("complementary", { name: "Pull requests", exact: true });
    await pane.getByText("Remote private PR marker", { exact: true }).waitFor();
    await pane.getByText("Created in this thread", { exact: true }).waitFor();
    await pane.getByRole("alert").getByText("Could not read the current checkout's pull request using this OpenCode server's gh login.", { exact: true }).waitFor();
    expect(await pane.getByLabel("Personal read token").count()).toBe(0);
    expect(await pane.getByRole("combobox", { name: "Thread pull request" }).textContent()).toContain("#7");
    await pane.getByRole("combobox", { name: "Thread pull request" }).click();
    const option = page.getByRole("option", { name: "sample/fixture-repository-marker #7", exact: true });
    expect(await option.evaluate((node: HTMLElement) => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.keyboard.press("Escape");
    expect(await pane.textContent()).not.toContain("#99");
    await draft.fill("OpenCode draft edited beside the PR");
    await pane.getByRole("button", { name: "Load files", exact: true }).click();
    await pane.getByRole("treeitem", { name: "private-file-marker.ts", exact: true }).click();
    await pane.getByText("private-patch-marker <script>text only</script>", { exact: true }).waitFor();
    const resize = pane.getByRole("separator", { name: "Resize delegation panel", exact: true });
    await resize.focus();
    for (let step = 0; step < 16; step++) await resize.press("ArrowLeft");
    const patchBox = (await pane.getByRole("region", { name: "File patch", exact: true }).boundingBox())!;
    const treeBox = (await pane.getByRole("region", { name: "Changed files", exact: true }).boundingBox())!;
    expect(treeBox.x).toBeGreaterThanOrEqual(patchBox.x + patchBox.width - 1);
    checkoutUnavailable = false;
    directory = "/repo/moved";
    holdMetadata = true;
    await pane.getByRole("button", { name: "Find again", exact: true }).click();
    await pendingMetadata.promise;
    expect(await pane.getByText("Remote private PR marker", { exact: true }).count()).toBe(0);
    expect(await pane.getByRole("treeitem").count()).toBe(0);
    releaseMetadata.resolve();
    await pane.getByText("0 of 1 files loaded", { exact: true }).waitFor();
    expect(await pane.getByRole("region", { name: "File patch", exact: true }).count()).toBe(0);
    // Preserve the open pane across navigation: it must use the new thread's query and reader.
    await page.evaluate((href: string) => { history.pushState(null, "", href); window.dispatchEvent(new PopStateEvent("popstate")); }, `${route("opencode:box:ses_child")}?pullRequests=1`);
    await pane.getByText("Second thread PR marker", { exact: true }).waitFor();
    expect(await pane.getByText("Remote private PR marker", { exact: true }).count()).toBe(0);
    expect(await pane.getByRole("region", { name: "File patch", exact: true }).count()).toBe(0);
    await pane.getByText("Current checkout · feature/pr-pane", { exact: true }).waitFor();
    await page.evaluate((href: string) => { history.pushState(null, "", href); window.dispatchEvent(new PopStateEvent("popstate")); }, `${route(`opencode:box:${linked}`)}?pullRequests=1`);
    await pane.getByText("Remote private PR marker", { exact: true }).waitFor();
    await pane.getByText("Current checkout · feature/pr-pane", { exact: true }).waitFor();
    expect(await pane.getByText("Created in this thread", { exact: true }).count()).toBe(0);
    await page.getByRole("tab", { name: "Alpha work", exact: true }).click();
    await view.getByText("answer in ses_a", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Tab actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).click();
    await pane.getByText("Created in this thread", { exact: true }).waitFor();
    await pane.getByRole("button", { name: "Close pull requests", exact: true }).click();
    expect(await draft.textContent()).toBe("OpenCode draft edited beside the PR");
    expect(commands.every(({ cwd, command }) => ["/repo", "/repo/child", "/repo/moved"].includes(cwd) && /^GH_PROMPT_DISABLED=1 gh (?:pr view \$\(git symbolic-ref -q HEAD >\/dev\/null \|\| jj log .+ \| head -n1\) --json|api --hostname github.com --method GET)/.test(command))).toBe(true);
    expect(removed).toHaveLength(commands.length);
    expect(prompts).toEqual([]);
    expect(errors).toEqual([]);
  } finally { releaseMetadata.resolve(); await browser.close(); web.stop(true); }
}, 30_000);

// A RocketClaw Web serving the built view at /oc-ui/ and the fake OpenCode server behind /oc/box/api/.
function start(shelf?: TopShelf) {
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
  // Typing watches the page holds open, by the chat each watches; a test pushes frames into them.
  const typing = new Map<ReadableStreamDefaultController<string>, string>();
  const web = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, websocket: { open(socket) {
    terminals.open++;
    socket.send(JSON.stringify({ type: "attached", inputProtocol: 1, info: { size: { cols: 80, rows: 24 } }, role: "controller" }));
    socket.send(JSON.stringify({ type: "replay_complete" }));
  }, close() { terminals.open--; }, message(_socket, message) {
    // v2.2.4 sends binary frames: kind, two 16-bit dimensions, then input bytes; size-only frames aren't keystrokes.
    if (typeof message !== "string" && message[0] === 1) terminals.input.push(message.subarray(5).toString());
  } }, async fetch(request, server) {
    const url = new URL(request.url);
    if (/^\/oc\/box\/api\/experimental\/persistent-pty\/[^/]+\/connect$/.test(url.pathname)) return server.upgrade(request) ? undefined : new Response("not a WebSocket", { status: 400 });
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
    if (url.pathname === "/typing") {
      const id = url.searchParams.get("id")!;
      calls.push({ path: url.pathname, body: { id } });
      let own: ReadableStreamDefaultController<string>;
      return new Response(new ReadableStream<string>({
        start(controller) { own = controller; typing.set(controller, id); controller.enqueue(`data: ${JSON.stringify({ principal: "", typing: false })}\n\n`); },
        cancel() { typing.delete(own); },
      }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, opencodeServers: { box }, agentTags: { main: { tags: ["customer"] } } })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      if (await file.exists()) return new Response(file);
      const index = await Bun.file(path.join(dist, "index.html")).text();
      return new Response(shelf ? index.replace("</head>", '<script id="rocketclaws" type="application/json">[{"name":"work","url":"http://127.0.0.1:9"}]</script></head>') : index, { headers: { "Content-Type": "text/html" } });
    }
    const input = await request.json() as { id?: string; [key: string]: unknown };
    calls.push({ path: url.pathname, body: input });
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "opencode" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      // New session needs the config to scope its draft.
      case "/api/ListConfig": return Response.json({ config: {} });
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
      case "/api/Tabs":
        if (input.topShelf) shelf = input.topShelf as TopShelf;
        return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [{ path: route("opencode:box:ses_a"), space: "main" }], groups: [], space: "main", last: {} }, topShelf: shelf });
      default: return Response.json({});
    }
  } });
  return { web, calls, release, terminals, creates, entryFailures, typing, shelved: () => shelf, base: `http://127.0.0.1:${web.port}` };
}

async function until(check: () => boolean | Promise<boolean>) {
  for (let tries = 0; tries < 250; tries++) {
    if (await check()) return;
    await Bun.sleep(20);
  }
  throw new Error("timed out");
}

test("a remote session mounts OpenCode's view in the page, with RocketClaw's keys, commands, and tabs around it", async () => {
  const started = performance.now();
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
    for (const [service, empty] of [["mcp", "No MCP servers configured"], ["plugins", "No plugins configured"], ["skills", "No skills configured"]]) {
      const pane = details.locator(`[data-service="${service}"]`);
      await pane.getByText(empty, { exact: true }).waitFor({ state: "attached" });
      expect(await pane.getByRole("button", { name: "Copy configuration file path", exact: true, includeHidden: true }).count()).toBe(1);
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
    // mod+Shift+A (next tab needing you) and mod+Shift+S (park until done) are RocketClaw's: they never reach OpenCode's
    // keys on the document.
    expect(await view.locator("[data-component=composer] [contenteditable=true]").evaluate((node: HTMLElement) => ["A", "S"].flatMap((key) => ["ctrlKey", "metaKey"].map((modifier) => {
      let reached = false;
      const reach = () => { reached = true; };
      document.addEventListener("keydown", reach, true);
      const event = new KeyboardEvent("keydown", { key, code: `Key${key}`, shiftKey: true, [modifier]: true, bubbles: true, cancelable: true });
      node.dispatchEvent(event);
      document.removeEventListener("keydown", reach, true);
      return [event.defaultPrevented, reached];
    })))).toEqual(Array(4).fill([true, false]));
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
    for (const close of ["command", "tab"]) {
      const terminalList = Promise.withResolvers<void>();
      const releaseList = Promise.withResolvers<void>();
      await page.route("**/oc/box/api/experimental/session/ses_a/terminal", async (route: { request(): { method(): string }; continue(): Promise<void> }) => {
        if (route.request().method() === "GET") {
          terminalList.resolve();
          await releaseList.promise;
        }
        await route.continue();
      }, { times: 1 });
      if (close === "command") {
        // While Mod+W's close is pending, toggle still hides a visible terminal without waiting.
        const removing = Promise.withResolvers<void>();
        const releaseRemove = Promise.withResolvers<void>();
        await page.route("**/oc/box/api/experimental/persistent-pty/pty_1", async (route: { continue(): Promise<void> }) => {
          removing.resolve();
          await releaseRemove.promise;
          await route.continue();
        }, { times: 1 });
        await view.locator('[data-component="terminal"]').click();
        await page.keyboard.press("ControlOrMeta+w");
        await removing.promise;
        expect(await view.locator('[data-component="terminal"]').isVisible()).toBe(true);
        await page.keyboard.press("Control+Backquote");
        await until(async () => !await view.locator('[data-component="terminal"]').isVisible());
        releaseRemove.resolve();
      } else {
        await view.locator('[data-pane-tab^="terminal:"]').getByRole("button", { name: "Close tab", exact: true }).click();
      }
      await until(() => terminals.open === 0);
      await terminalList.promise;
      await view.locator("[data-component=composer] [contenteditable=true]").click();
      await page.keyboard.press("Control+Backquote");
      releaseList.resolve();
      await view.locator('[data-pane-content="bottom"] [data-component="terminal"]').waitFor();
      await until(() => terminals.open === 1).catch(async (error: unknown) => {
        console.error("Terminal reopen socket wait failed", { close, expectedOpenSockets: 1, observedOpenSockets: terminals.open, requests: fake.requests, errors });
        try {
          const dom = await view.locator('[data-component="terminal"], [data-pane-tab^="terminal:"]').evaluateAll((nodes: Element[]) => nodes.map((node) => ({
            component: node.getAttribute("data-component"),
            tab: node.getAttribute("data-pane-tab"),
            pane: node.closest("[data-pane-content]")?.getAttribute("data-pane-content"),
            wrapper: node.closest('[id^="terminal-wrapper-"]')?.id,
            visible: node.checkVisibility(),
            text: node.textContent,
          })));
          console.error("Terminal reopen DOM after socket wait", dom);
        } catch (snapshotError: unknown) {
          console.error("Terminal reopen DOM snapshot failed", snapshotError);
        }
        throw error;
      });
    }

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
    const forkRequests = fake.requests.length;
    await page.locator("[data-oc-portal]").locator("[role=option], button", { hasText: "question in ses_a" }).first().click();
    await page.waitForURL(new RegExp(`/oc/${encode("opencode:box:ses_fork_").slice(0, -2)}`));
    console.info("Waiting for visible fork answer", {
      expected: "/answer in ses_fork_/",
      elapsedMs: performance.now() - started,
      url: page.url(),
      tabs: await tabs.allTextContents(),
      rendered: await page.locator("main").allTextContents(),
      errors,
      requests: fake.requests.slice(forkRequests),
    });
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

    // RocketClaw's palette and the tab's menu delete the shown session on its server after asking; Cancel keeps it.
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await view.getByText("answer in ses_a").waitFor();
    const confirm = page.getByRole("dialog", { name: "Delete session", exact: true });
    await command("Delete session");
    await confirm.getByText('Delete session "Alpha work"?', { exact: true }).waitFor();
    await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
    await confirm.waitFor({ state: "hidden" });
    expect(fake.sessions.has("ses_a")).toBe(true);
    await tabs.getByRole("button", { name: "Tab actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Delete session", exact: true }).click();
    await confirm.getByRole("button", { name: "Cancel", exact: true }).and(page.locator(":focus")).waitFor();
    await confirm.getByRole("button", { name: "Delete session", exact: true }).click();
    await until(async () => !fake.sessions.has("ses_a") && !page.url().includes(route("opencode:box:ses_a")) && await tabs.getByRole("tab", { name: "Alpha work", exact: true }).count() === 0);
    expect(calls.filter(({ path, body }) => path === "/api/Tabs" && body.topShelf)).toEqual([]);
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
    const openTabs = () => page.locator('[aria-label="Open tabs"] [data-tab]').evaluateAll((nodes: Element[]) => nodes.map((node) => node.getAttribute("data-tab")));

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
    // With an OpenCode server, a click on the footer's New session opens a menu with this server highlighted, named by
    // the page's host, here a whole IP address: Enter starts a session here, and the next item, box, opens the same picker.
    const newSession = page.getByRole("button", { name: "New session", exact: true });
    const highlighted = (name: string) => page.getByRole("menuitem", { name, exact: true }).and(page.locator(":focus")).waitFor();
    await newSession.click();
    await highlighted("127.0.0.1");
    await page.keyboard.press("Enter");
    await page.waitForURL(`${base}/`);
    await newSession.click();
    await highlighted("127.0.0.1");
    await page.keyboard.press("ArrowDown");
    await highlighted("box");
    await page.keyboard.press("Enter");
    await folders.first().waitFor();
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
    expect(await openTabs()).toEqual(expect.arrayContaining([route("opencode:box:ses_a"), route("opencode:box:ses_child")]));

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
    expect(await openTabs()).toEqual(expect.arrayContaining([route("opencode:box:ses_a"), route("opencode:box:ses_child")]));

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

    // A session the conversation mentions opens in its own RocketClaw tab. The view moves there instead of being
    // built again, as OpenCode's own app moves between sessions, and moves back when the first tab is chosen.
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await view.getByText("answer in ses_a").waitFor();
    await view.evaluate((node: HTMLElement & { built?: number }) => { node.built = 1; });
    await view.locator(`button[data-session-id="${linked}"]`).click();
    await page.waitForURL(`${base}${route(`opencode:box:${linked}`)}`);
    // v2.3.0 leaves references to the shown session as plain text (packages/app/src/shell/routes/session-ui-provider.tsx).
    await view.getByText(`answer in ${linked}`).waitFor();
    await page.getByRole("tablist", { name: "Open tabs" }).getByRole("tab", { name: "Alpha work", exact: true }).click();
    await view.getByText("answer in ses_a").waitFor();
    await Bun.sleep(200);
    expect(await view.evaluate((node: HTMLElement & { built?: number }) => node.built)).toBe(1);
    expect([await page.locator("[data-oc-view]:not([data-oc-portal])").count(), fake.streams]).toEqual([1, 1]);
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

test("mobile keyboards keep the composer, model controls and bottom bar above the keyboard with short and long drafts", async () => {
  const { chromium: engine } = await import(playwright!);
  const { web, release, base } = start();
  release();
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    // A keyboard can shrink only the visual viewport (Safari/Chrome), or both viewports.
    // Simulate the browser metrics, not a real OS keyboard; device keyboard transitions still need a phone check.
    for (const width of [393, 820, 1280]) {
      const page = await browser.newPage({ viewport: { width, height: 844 }, isMobile: width < 1280, hasTouch: width < 1280 });
      await page.goto(`${base}${route("opencode:box:ses_a")}`);
      const editor = page.locator("[data-component=composer-editor]");
      const composer = page.locator("[data-component=composer]");
      await editor.waitFor();
      const visible = async (height: number, top: number, scale = 1) => {
        await page.evaluate(({ height, top, scale }: { height: number; top: number; scale: number }) => {
          const viewport = window.visualViewport!;
          for (const [key, value] of Object.entries({ height, offsetTop: top, scale })) Object.defineProperty(viewport, key, { configurable: true, value });
          viewport.dispatchEvent(new Event("resize"));
          viewport.dispatchEvent(new Event("scroll"));
        }, { height, top, scale });
      };
      const bounds = () => page.evaluate(() => ({
        composer: document.querySelector("[data-component=composer]")!.getBoundingClientRect().toJSON(),
        bar: document.querySelector("footer")!.getBoundingClientRect().toJSON(),
        view: document.querySelector("main [data-oc-view]")!.getBoundingClientRect().toJSON(),
      }));
      for (const text of ["short draft", "A sufficiently long draft should scroll inside its editor.\n".repeat(40)]) {
        await editor.fill(text);
        await visible(410, 0);
        expect((await bounds()).bar.bottom).toBeLessThanOrEqual(410);
        expect((await bounds()).composer.bottom).toBeLessThanOrEqual(410);
        expect((await bounds()).composer.top).toBeGreaterThanOrEqual((await bounds()).view.top);
        expect(await editor.evaluate((node: HTMLElement) => node === document.activeElement)).toBe(true);
        expect(await composer.locator("[data-slot=composer-controls]").count()).toBe(1);
        if (text.length > 100) expect(await page.locator("[data-component=composer-scroll]").evaluate((node: HTMLElement) => node.scrollHeight > node.clientHeight)).toBe(true);
        // Focusing the editor can also pan the visible viewport; a scroll event alone must reposition the shell.
        await page.evaluate(() => {
          Object.defineProperty(window.visualViewport!, "offsetTop", { configurable: true, value: 52 });
          window.visualViewport!.dispatchEvent(new Event("scroll"));
        });
        await until(async () => Math.abs((await bounds()).bar.bottom - 462) < 1);
        expect((await bounds()).composer.top).toBeGreaterThanOrEqual(52);
        // Dismissing the keyboard restores the full page, with the unsent draft and focus intact.
        await visible(844, 0);
        await until(async () => Math.abs((await bounds()).bar.bottom - 844) < 1);
        expect(await editor.innerText()).toContain(text.trim());
      }
      await page.setViewportSize({ width, height: 410 });
      await visible(410, 0);
      await until(async () => (await bounds()).bar.bottom <= 410);
      expect((await bounds()).composer.bottom).toBeLessThanOrEqual(410);
      await page.setViewportSize({ width, height: 844 });
      await visible(844, 0);
      await until(async () => Math.abs((await bounds()).bar.bottom - 844) < 1);
      // Pinch zoom must keep the layout's full size, rather than reflowing it into the zoomed-in window.
      await visible(422, 100, 2);
      expect((await bounds()).bar.bottom).toBe(844);
      await visible(844, 0);
      // RocketClaw's own composer shares the same shell, not just the embedded OpenCode view.
      await page.goto(base);
      const native = page.locator("textarea:enabled");
      await native.fill("A long RocketClaw draft\n".repeat(40));
      await visible(410, 0);
      const nativeBounds = await native.boundingBox();
      expect(nativeBounds!.y).toBeGreaterThanOrEqual(0);
      expect(nativeBounds!.y + nativeBounds!.height).toBeLessThanOrEqual(410);
      expect((await page.getByRole("button", { name: "Send", exact: true }).boundingBox())!.y).toBeLessThan(410);
      await page.close();
    }
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 60_000);

test("a shelf row offers its server's attention items, one of an offline server is greyed and offers only Remove, and a deleted remote session leaves the Top Shelf too", async () => {
  const { chromium: engine } = await import(playwright!);
  // work never answers, so its Cron tab's shelf row is offline.
  const { web, release, shelved, base } = start({ tabs: [{ path: route("opencode:box:ses_a"), space: "main" }, { server: "work", path: "/cron", space: "main" }] });
  release();
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await page.locator("main [data-oc-view]").getByText("answer in ses_a").waitFor();
    const shelf = page.getByRole("tablist", { name: "Top Shelf" }), offline = shelf.getByRole("tab", { name: "Cron", exact: true });
    const menu = async (row: any) => {
      await row.click({ button: "right" });
      await page.getByRole("menuitem").first().waitFor();
      const items = await page.getByRole("menuitem").allInnerTexts();
      await page.keyboard.press("Escape");
      await page.getByRole("menu").waitFor({ state: "hidden" });
      return items;
    };
    await shelf.getByRole("tab", { name: "Alpha work", exact: true }).waitFor();
    expect(await menu(shelf.getByRole("tab", { name: "Alpha work", exact: true }))).toEqual(["Pull requests", "Mark as unread", "Park", "Auto-park when I send", "Remove from Top Shelf"]);
    await shelf.getByRole("button", { name: "Tab actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).click();
    await page.getByRole("complementary", { name: "Pull requests", exact: true }).waitFor();
    expect(new URL(page.url()).pathname).toBe(route("opencode:box:ses_a"));
    await page.getByRole("button", { name: "Close pull requests", exact: true }).click();
    await shelf.locator('[role="tab"][aria-label="Cron"][aria-description="Top Shelf, work, Server offline"]').waitFor();
    expect(await shelf.locator('[data-tab="/cron"]').evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("0.6");
    await offline.hover();
    await page.locator('[data-slot="tooltip-content"]').filter({ hasText: "Cron · Server offline" }).waitFor();
    expect(await menu(offline)).toEqual(["Remove from Top Shelf"]);
    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("Delete session");
    await page.getByRole("button", { name: "Sessions: Delete session" }).click();
    await page.getByRole("dialog", { name: "Delete session", exact: true }).getByRole("button", { name: "Delete session", exact: true }).click();
    await until(() => !fake.sessions.has("ses_a"));
    await until(() => shelved()?.tabs?.map((tab) => tab.path).join() === "/cron");
    expect(await shelf.locator("[data-tab]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.dataset.tab))).toEqual(["/cron"]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 60_000);

test("OpenCode's composer reports typing for the session the view shows, and the pill sits below its header", async () => {
  const { chromium: engine } = await import(playwright!);
  const { web, calls, release, terminals, typing, base } = start();
  release();
  // A server on another release shows RocketClaw's version pill over OpenCode's header, which the pill must clear.
  fake.version = "1.0.0";
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const view = page.locator("main [data-oc-view]");
    const composer = view.locator("[data-component=composer] [contenteditable=true]");
    // The pill's live region, beside the view rather than inside OpenCode's DOM.
    const status = page.locator('main :has(> * > [data-oc-view]) > [role="status"][aria-live="polite"]');
    // The reports in the order the tab numbered them, which they may reach the server out of.
    const reports = () => calls.filter(({ path }) => path === "/api/ReportTyping").map(({ body }) => body).sort((a, b) => Number(a.seq) - Number(b.seq)).map(({ id, typing }) => ({ id, typing }));
    const sent: string[] = [];
    // The fake server has no models and answers sends with the wrong status; these answers let OpenCode's own send
    // paths run to the end (packages/client/src/promise/generated/client.ts).
    const catalog: Record<string, unknown> = {
      agent: [{ id: "build", name: "build", request: { settings: {}, headers: {}, body: {} }, mode: "primary", hidden: false, permissions: [] }],
      model: [{ id: "m", modelID: "m", providerID: "p", name: "Model", capabilities: { tools: true, input: ["text"], output: ["text"] }, variants: [], time: { released: 0 }, cost: [], status: "active", enabled: true, limit: { context: 1000, output: 100 } }],
      provider: [{ id: "p", name: "Provider", activation: "enabled", package: "p" }],
    };
    await page.route(/\/oc\/box\/api\/(agent|model|provider)(\?|$)/, (request: any) => request.fulfill({ json: { location: { directory: "/repo" }, data: catalog[new URL(request.request().url()).pathname.split("/").at(-1)!] } }));
    await page.route(/\/oc\/box\/api\/session\/ses_a\/(agent|model|shell|prompt|generate)$/, (request: any) => {
      const kind = new URL(request.request().url()).pathname.split("/").at(-1)!;
      sent.push(kind);
      if (kind === "prompt") return request.fulfill({ json: { data: { id: request.request().postDataJSON().id, sessionID: "ses_a", time: { created: 1 }, type: "user", payload: { text: "hello" }, delivery: "steer" } } });
      if (kind === "generate") return request.fulfill({ json: { data: { text: "by the way", tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } }, cost: 0 } } });
      return request.fulfill({ status: 204 });
    });
    // The reports after the first `from` are exactly `expected`.
    const reported = async (from: number, ...expected: { id: string; typing: boolean }[]) => {
      await until(() => reports().length >= from + expected.length);
      await Bun.sleep(200);
      expect(reports().slice(from)).toEqual(expected);
    };
    const ses = (id: string, typing: boolean) => ({ id: `opencode:box:${id}`, typing });
    await page.goto(`${base}${route("opencode:box:ses_a")}`);
    await view.getByText("answer in ses_a", { exact: true }).waitFor();

    // Typing reports, and deleting the whole draft, line break included, reports not typing.
    await composer.pressSequentially("hel");
    await reported(0, ses("ses_a", true));
    for (let count = 0; count < 3; count++) await composer.press("Backspace");
    await reported(1, ses("ses_a", false));
    await composer.pressSequentially("a");
    await composer.press("Shift+Enter");
    await composer.pressSequentially("b");
    await reported(2, ses("ses_a", true));
    await composer.press("ControlOrMeta+a");
    await composer.press("Backspace");
    await reported(3, ses("ses_a", false));

    // Every send empties the composer in OpenCode's own code: a picked slash command, /btw, and a shell command.
    await composer.pressSequentially("/fork");
    await view.locator('[data-suggestion-id="session.fork"][data-active]').waitFor();
    await page.keyboard.press("Enter");
    await page.locator("[data-oc-portal]").getByText("Choose a message to fork from").waitFor();
    await reported(4, ses("ses_a", true), ses("ses_a", false));
    await page.keyboard.press("Escape");
    await composer.pressSequentially("/btw hi");
    await composer.press("Enter");
    await until(() => sent.includes("generate"));
    await reported(6, ses("ses_a", true), ses("ses_a", false));
    // The answer opens beside the conversation; closing it gives the conversation the whole view again.
    await view.locator('[data-pane-tab^="btw:"]').getByRole("button", { name: "Close tab", exact: true }).click();
    // "!" turns the composer to shell mode and leaves it empty.
    await composer.pressSequentially("!ls");
    await composer.press("Enter");
    await until(() => sent.includes("shell"));
    await reported(8, ses("ses_a", true), ses("ses_a", false), ses("ses_a", true), ses("ses_a", false));
    expect(await composer.textContent()).toBe("");

    // OpenCode's terminal and its settings search are not its composer.
    await page.keyboard.press("Control+Backquote");
    await view.locator('[data-component="terminal"]').click();
    await page.keyboard.press("x");
    await until(() => terminals.input.join("").endsWith("x"));
    await composer.click();
    await page.keyboard.press("Control+Backquote");
    await page.keyboard.press("ControlOrMeta+Comma");
    const search = page.locator("[data-oc-view] input[type=search]");
    await search.fill("theme");
    await search.pressSequentially("s");
    await Bun.sleep(300);
    expect(reports()).toHaveLength(12);
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");

    // A send while the last report hangs reports not typing at once, numbered after it.
    const held = Promise.withResolvers<void>();
    let requested = 0;
    await page.route("**/api/ReportTyping", async (request: any) => { if (++requested === 1) await held.promise; await request.continue(); });
    await composer.pressSequentially("hello");
    await until(() => requested === 1);
    await composer.press("Enter");
    await until(() => sent.includes("prompt"));
    await reported(12, ses("ses_a", false));
    expect(await composer.textContent()).toBe("");
    held.resolve();
    await reported(12, ses("ses_a", true), ses("ses_a", false));
    await page.unroute("**/api/ReportTyping");

    // The pill shows another person's typing below OpenCode's header and RocketClaw's version pill, clear of the
    // composer and what sits above it, and lets clicks through to the conversation.
    const push = (id: string, principal: string) => { for (const [stream, watched] of typing) if (watched === `opencode:box:${id}`) stream.enqueue(`data: ${JSON.stringify({ principal, typing: true })}\n\n`); };
    const bounds = (selector: string) => page.locator(selector).first().evaluate((node: HTMLElement) => node.getBoundingClientRect().toJSON());
    // OpenCode's header: its title bar, or on a narrow view its tabs and the location bar below them.
    const placed = async (id: string, shot: string, header = "[data-session-title] > div", text = "Ana is typing") => {
      push(id, "Ana");
      await status.getByText(text, { exact: true }).waitFor();
      const pill = await status.locator("> *").evaluate((node: HTMLElement) => node.getBoundingClientRect().toJSON());
      const top = await bounds(`[data-oc-view] ${header}`);
      const dock = await bounds("[data-oc-view] [data-component=session-composer-dock]");
      const version = await bounds("[data-oc-view] [data-component=rocketclaw-version-pill]");
      expect(pill.top).toBeGreaterThanOrEqual(top.bottom);
      expect(pill.top - top.bottom).toBeLessThan(12);
      expect(pill.top).toBeGreaterThanOrEqual(version.bottom);
      expect(pill.bottom).toBeLessThanOrEqual(dock.top);
      // Nothing of the view paints over it: taking the pointer for a moment, it is what the pointer finds.
      expect(await status.evaluate((node: HTMLElement, { x, y }: { x: number; y: number }) => {
        node.style.pointerEvents = "auto";
        const found = node.contains(document.elementFromPoint(x, y));
        node.style.pointerEvents = "";
        return found;
      }, { x: pill.left + pill.width / 2, y: pill.top + pill.height / 2 })).toBe(true);
      await page.screenshot({ path: path.join(shots, `opencode-typing-pill-${shot}.png`) });
      return pill;
    };
    const pill = await placed("ses_a", "1280");
    await view.evaluate((node: HTMLElement & { clicked?: string }) => node.addEventListener("click", (event) => { node.clicked = (event.target as HTMLElement).closest("[data-component=user-message]")?.textContent ?? ""; }, { once: true }));
    await page.mouse.click(pill.left + pill.width / 2, pill.top + pill.height / 2);
    expect(await view.evaluate((node: HTMLElement & { clicked?: string }) => node.clicked)).toContain("question in ses_a");

    // A subagent session the view opens takes no prompts, so it has no composer to type in: the pill goes on watching
    // the tab's own session, and says so.
    await view.locator("[data-component=task-tool-card]").first().click();
    await view.getByText("answer in ses_child").waitFor();
    expect(await composer.count()).toBe(0);
    await placed("ses_a", "child", undefined, "Ana is typing in the main session");
    expect([...typing.values()]).toEqual(["opencode:box:ses_a"]);

    // The view moves to another tab of its server; typing there reports that session.
    await page.evaluate((href: string) => { history.pushState(null, "", href); window.dispatchEvent(new PopStateEvent("popstate")); }, route(`opencode:box:${linked}`));
    await view.getByText(`answer in ${linked}`).waitFor();
    await composer.pressSequentially("hi");
    await reported(14, ses(linked, true));
    await page.setViewportSize({ width: 390, height: 664 });
    await placed(linked, "390", "[data-slot=session-location-bar]");
    expect(calls.filter(({ path, body }) => (path === "/api/History" || path === "/stream" || path === "/api/ListQueue") && body.id?.startsWith("opencode:"))).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 60_000);
