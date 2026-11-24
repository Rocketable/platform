import { afterEach, expect, test } from "bun:test";

const local = process.env.ROCKETCLAW_REMOTE_TEST_LOCAL!, remote = process.env.ROCKETCLAW_REMOTE_TEST_REMOTE!;
const observed = { stage: "launch", url: "", completed: false, errors: [] as string[], requests: [] as string[] };

// Bun's test deadline is uncatchable and closes Chromium, so report already-observed state without querying the page.
afterEach(() => {
  if (!observed.completed) console.error("Remote transport test failed", JSON.stringify(observed));
});

// Go's TestRemoteRocketClawRealBrowser serves a local RocketClaw and a remote "work" behind its tailnet handler, at
// names under one tailnet suffix that the browser maps to this machine. "gone" is declared but never answers.
test("a local page works, lists, searches and follows a chat on a remote RocketClaw", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true, args: [`--host-resolver-rules=MAP *.${process.env.ROCKETCLAW_REMOTE_TEST_SUFFIX} 127.0.0.1`] });
  const context = await browser.newContext({ ignoreHTTPSErrors: true });
  await context.addInitScript(() => {
    const shown: string[] = (window as unknown as { shown: string[] }).shown = [];
    Object.assign(window, { Notification: class { static permission = "granted"; onclick = null; constructor(title: string) { shown.push(title); } close() {} } });
  });
  const page = await context.newPage();
  page.on("pageerror", (error: Error) => observed.errors.push(error.message));
  page.on("framenavigated", () => { observed.url = page.url(); });
  page.on("request", (request: { url(): string }) => {
    if (/\/api\/(Protocol|Identity)$/.test(request.url())) observed.requests.push(`request ${request.url()}`);
  });
  page.on("response", (response: { url(): string; status(): number }) => {
    if (/\/api\/(Protocol|Identity)$/.test(response.url())) observed.requests.push(`response ${response.status()} ${response.url()}`);
  });
  page.on("requestfailed", (request: { url(): string; failure(): { errorText: string } | null }) => {
    if (/\/api\/(Protocol|Identity)$/.test(request.url())) observed.requests.push(`failed ${request.failure()?.errorText} ${request.url()}`);
  });
  // Advance the page's polling and debounce timers; the database clock remains real.
  observed.stage = "remote reply";
  await page.clock.install();
  await page.goto(`${local}/r/work/`);
  await page.getByRole("textbox").fill("Remote question");
  await page.getByLabel("Attach files", { exact: true }).setInputFiles({ name: "dot.png", mimeType: "image/png", buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64") });
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.locator("#transcript-scroll").getByText("Remote reply", { exact: true }).waitFor();
  const chat = new URL(page.url()).pathname;
  expect(chat).toStartWith("/r/work/s/");
  const id = Buffer.from(chat.slice("/r/work/s/".length), "base64url").toString();
  // The chat's tab is saved on the remote, where the local page reads it for notifications.
  while (!await page.evaluate(async (url: string) => (await (await fetch(`${url}/api/Tabs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })).json()).state.tabs.some((tab: { path: string }) => tab.path.startsWith("/s/")), remote)) await Bun.sleep(100);

  // On the local server, the palette lists the remote chat under its server's name, and a filter finds it there.
  observed.stage = "remote palette";
  await page.goto(`${local}/`);
  await page.getByRole("textbox").waitFor();
  // The footer's New session menu starts one on a remote, where the remote is the highlighted choice, and from there on
  // the local server, named by the first label of the page's host.
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.getByRole("menuitem", { name: "work", exact: true }).click();
  await page.waitForURL((url: URL) => url.pathname === "/r/work/");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.getByRole("menuitem", { name: "work", exact: true }).and(page.locator(":focus")).waitFor();
  await page.getByRole("menuitem", { name: "local", exact: true }).click();
  await page.waitForURL((url: URL) => url.pathname === "/");
  await page.getByRole("textbox").waitFor();
  await page.keyboard.press("Control+p");
  const palette = page.getByRole("dialog", { name: "Go to session" });
  // The fake provider also names the chat, after its reply.
  const row = palette.getByRole("button").filter({ hasText: "work · " });
  await row.waitFor();
  // A filter asks every server; until its answer, the rows shown are the last answer's.
  await palette.getByPlaceholder("Search sessions").fill("agent:other");
  await page.clock.fastForward(500);
  await row.waitFor({ state: "hidden" });
  await palette.getByPlaceholder("Search sessions").fill("agent:main");
  await page.clock.fastForward(500);
  await row.waitFor();
  // Agent choices offer the remote's own agents too, under its name.
  await palette.getByPlaceholder("Search sessions").fill("agent:sc");
  await page.clock.fastForward(500);
  await palette.getByRole("button").filter({ hasText: "scout" }).filter({ hasText: "work · gpt-6.1-sol" }).waitFor();
  await palette.getByPlaceholder("Search sessions").fill("");
  await page.keyboard.press("Escape");

  // A new reply on the remote, while a local page shows, notifies: the chat is in one of the remote's tabs.
  observed.stage = "remote notification";
  // The remote counts a chat viewed in the last 10 seconds as read (internal/rocketclaw/backend/store_dao.go).
  expect(await page.evaluate(async () => (await fetch("/test/view-expired", { method: "POST" })).ok)).toBe(true);
  await page.evaluate(async ([url, conversation]: string[]) => { await fetch(`${url}/api/Prompt`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: conversation, text: "Again" }) }); }, [remote, id]);
  await page.clock.fastForward(2_000);
  await page.waitForFunction(() => (window as unknown as { shown: string[] }).shown.length > 0);

  // A remote that stops answering keeps its rows, offline, until it answers again.
  observed.stage = "offline palette row";
  expect(await page.evaluate(async () => (await fetch("/test/remote/down", { method: "POST" })).ok)).toBe(true);
  await page.keyboard.press("Control+p");
  await page.clock.fastForward(2_000);
  await row.filter({ hasText: "offline" }).waitFor();
  expect(await page.evaluate(async () => (await fetch("/test/remote/up", { method: "POST" })).ok)).toBe(true);
  observed.stage = "online palette row";
  await page.clock.fastForward(2_000);
  await row.filter({ hasNotText: "offline" }).waitFor();
  await page.keyboard.press("Escape");

  // Opening the remote chat from the local page lands on its server.
  observed.stage = "open remote chat";
  await page.keyboard.press("Control+p");
  await row.click();
  await page.waitForURL((url: URL) => url.pathname === chat);

  // On a local page the remote chat is in the remote's Main card, whose items act on the remote: Park moves it to
  // Parked, whose Unpark puts it back, and opening it lands on its server, in its space.
  observed.stage = "remote card";
  await page.goto(`${local}/`);
  const chatTab = page.getByRole("tablist", { name: "Main tabs on work" }).locator('[data-tab^="/s/"]');
  const change = (action: string) => page.waitForRequest((request: { url(): string; postData(): string | null }) => request.url() === `${remote}/api/Tabs` && JSON.parse(request.postData()!).attention?.action === action);
  const parking = change("PARK");
  await chatTab.getByRole("tab").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Park", exact: true }).click();
  await page.getByRole("menuitem", { name: "Until I unpark", exact: true }).click();
  expect(JSON.parse((await parking).postData()).attention.paths).toEqual([chat.slice("/r/work".length)]);
  await chatTab.waitFor({ state: "detached" });
  const parked = page.getByRole("tablist", { name: "Parked tabs" });
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  const unparking = change("UNPARK");
  await parked.getByRole("tab").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Unpark", exact: true }).click();
  await unparking;
  await chatTab.getByRole("tab").click();
  await page.waitForURL((url: URL) => url.pathname === chat);
  await page.locator("#transcript-scroll").getByText("Remote reply", { exact: true }).first().waitFor();
  await chatTab.getByRole("tab", { selected: true }).waitFor();
  await page.waitForFunction(() => (document.querySelector('#transcript-scroll img[alt="dot.png"]') as HTMLImageElement | null)?.naturalWidth === 1);
  const attachment = (await page.locator('#transcript-scroll img[alt="dot.png"]').getAttribute("src"))!;

  observed.stage = "remote hosted page";
  await page.goto(`${local}/r/work/pages/scout/index.html`);
  await page.locator("main iframe").contentFrame().getByText("Remote reply page", { exact: true }).waitFor();

  observed.stage = "another site";
  const other = await context.newPage();
  await other.goto(`${local.replace("//local.", "//other.")}/test/other-site`);
  const framed = [`${remote}/pages-raw/scout/index.html`, `${remote}/`];
  await other.evaluate((sources: string[]) => Promise.all(sources.map((src) => new Promise((resolve) => {
    const frame = document.createElement("iframe");
    frame.onload = resolve;
    frame.src = src;
    document.body.append(frame);
  }))), framed);
  const frameURLs = await Promise.all(other.frames().slice(1).map(
    (frame: { evaluate(readURL: () => string): Promise<string> }) => frame.evaluate(() => location.href),
  ));
  expect(frameURLs).toEqual(framed.map(() => "chrome-error://chromewebdata/"));
  expect(await other.evaluate((src: string) => new Promise((resolve) => {
    const image = new Image();
    image.onload = image.onerror = () => resolve(image.naturalWidth);
    image.src = src;
  }), attachment)).toBe(0);
  await other.close();

  // A space's card enters it, saving it as the current space: on this server, and on a remote, whose page opens even in
  // an empty space, which it enters on arrival.
  observed.stage = "enter spaces";
  const enter = async (space: string, server: string) => {
    const name = `${space} space actions on ${server}`;
    await page.getByRole("button", { name, exact: true }).click();
    const menu = page.getByRole("menu", { name, exact: true });
    await menu.getByRole("menuitem", { name: "Go to space", exact: true }).click();
    await menu.waitFor({ state: "hidden" });
  };
  await page.evaluate(async () => { await fetch("/api/Tabs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ createSpace: "Side" }) }); });
  await page.evaluate(async (url: string) => { await fetch(`${url}/api/Tabs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ createSpace: "Billing" }) }); }, remote);
  await page.goto(`${local}/`);
  const current = (url: string, name: string) => page.evaluate(async ([url, name]: string[]) => {
    const view = await (await fetch(`${url}/api/Tabs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })).json();
    return view.spaces.some((space: { id: string; name: string }) => space.name === name && space.id === view.state.space);
  }, [url, name]);
  await enter("Side", "local");
  while (!await current(local, "Side")) await Bun.sleep(100);
  await enter("Billing", "work");
  await page.waitForURL((url: URL) => url.pathname.startsWith("/r/work/"));
  while (!await current(remote, "Billing")) await Bun.sleep(100);
  const online = page.waitForRequest((request: { url(): string; frame(): { url(): string } }) => request.url() === `${remote}/api/Protocol` && request.frame().url() === `${local}/r/work/config`);
  await page.goto(`${local}/r/work/config`);
  await page.getByText(process.env.ROCKETCLAW_REMOTE_TEST_WORKSPACE!, { exact: true }).waitFor();
  // Config can render before Protocol answers; establish online before taking the remote offline.
  observed.stage = "online remote config protocol";
  const protocol = await (await online).response();
  expect(protocol?.ok()).toBe(true);
  await protocol!.finished();

  // With the remote off, its page says so and a local space is still a choice away.
  observed.stage = "offline remote config page";
  expect(await page.evaluate(async () => (await fetch("/test/remote/down", { method: "POST" })).ok)).toBe(true);
  await page.clock.fastForward(2_000);
  await page.getByText("work is offline; this page reopens when it is reachable.").waitFor();
  await enter("Main", "local");
  await page.waitForURL((url: URL) => !url.pathname.startsWith("/r/work"));
  expect(await page.evaluate(async () => (await fetch("/test/remote/up", { method: "POST" })).ok)).toBe(true);

  // A remote on another protocol keeps working with a notice and never reloads the page; a local one reloads it.
  observed.stage = "remote version notice";
  await page.goto(`${local}/r/work/agents`);
  await page.evaluate(() => Object.assign(window, { kept: true }));
  expect(await page.evaluate(async () => (await fetch("/test/version/work", { method: "POST" })).ok)).toBe(true);
  await page.clock.fastForward(2_000);
  await page.getByText("work runs a different RocketClaw version; some actions may fail.").waitFor();
  await page.getByRole("heading", { name: "Agents" }).waitFor();
  expect(await page.evaluate(() => "kept" in window)).toBe(true);
  observed.stage = "local version reload";
  expect(await page.evaluate(async () => (await fetch("/test/version/local", { method: "POST" })).ok)).toBe(true);
  await page.clock.fastForward(2_000);
  await page.waitForFunction(() => !("kept" in window));

  observed.stage = "unreachable remote notice";
  await page.goto(`${local}/r/gone/agents`);
  await page.getByText(/^gone is unavailable: .*Address: https:\/\/gone\./).waitFor();
  observed.completed = true;
}, 90_000);
