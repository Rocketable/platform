import { expect, test } from "bun:test";

const local = process.env.ROCKETCLAW_REMOTE_TEST_LOCAL!, remote = process.env.ROCKETCLAW_REMOTE_TEST_REMOTE!;

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
  await page.goto(`${local}/r/work/`);
  await page.getByRole("textbox").fill("Remote question");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.locator("#transcript-scroll").getByText("Remote reply", { exact: true }).waitFor();
  const chat = new URL(page.url()).pathname;
  expect(chat).toStartWith("/r/work/s/");
  const id = Buffer.from(chat.slice("/r/work/s/".length), "base64url").toString();
  // The chat's tab is saved on the remote, where the local page reads it for notifications.
  for (let saved = false; !saved; await Bun.sleep(100)) {
    saved = await page.evaluate(async (url: string) => (await (await fetch(`${url}/api/Tabs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })).json()).state.tabs.some((tab: { path: string }) => tab.path.startsWith("/s/")), remote);
  }

  // On the local server, the palette lists the remote chat under its server's name, and a filter finds it there.
  await page.goto(`${local}/`);
  await page.getByRole("textbox").waitFor();
  await page.keyboard.press("Control+p");
  const palette = page.getByRole("dialog", { name: "Go to session" });
  // The fake provider also names the chat, after its reply.
  const row = palette.getByRole("button").filter({ hasText: "work · " });
  await row.waitFor();
  // A filter asks every server; until its answer, the rows shown are the last answer's.
  await palette.getByPlaceholder("Search sessions").fill("agent:other");
  await row.waitFor({ state: "hidden" });
  await palette.getByPlaceholder("Search sessions").fill("agent:main");
  await row.waitFor();
  // Agent choices offer the remote's own agents too, under its name.
  await palette.getByPlaceholder("Search sessions").fill("agent:sc");
  await palette.getByRole("button").filter({ hasText: "scout" }).filter({ hasText: "work · gpt-6.1-sol" }).waitFor();
  await palette.getByPlaceholder("Search sessions").fill("");
  await page.keyboard.press("Escape");

  // A new reply on the remote, while a local page shows, notifies: the chat is in one of the remote's tabs.
  // The remote counts a chat viewed in the last 10 seconds as read (internal/rocketclaw/backend/store_dao.go).
  await Bun.sleep(10_000);
  await page.evaluate(async ([url, conversation]: string[]) => { await fetch(`${url}/api/Prompt`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: conversation, text: "Again" }) }); }, [remote, id]);
  await page.waitForFunction(() => (window as unknown as { shown: string[] }).shown.length > 0);

  // A remote that stops answering keeps its rows, offline, until it answers again.
  expect(await page.evaluate(async () => (await fetch("/test/remote/down", { method: "POST" })).ok)).toBe(true);
  await page.keyboard.press("Control+p");
  await row.filter({ hasText: "offline" }).waitFor();
  expect(await page.evaluate(async () => (await fetch("/test/remote/up", { method: "POST" })).ok)).toBe(true);
  await row.filter({ hasNotText: "offline" }).waitFor();
  await page.keyboard.press("Escape");

  // Opening the remote chat from the local page lands on its server.
  await page.keyboard.press("Control+p");
  await row.click();
  await page.waitForURL((url: URL) => url.pathname === chat);

  // The remote chat goes on the Top Shelf, which names where it lives and keeps it after its tab closes.
  const shelf = page.getByRole("tablist", { name: "Top Shelf" }), strip = page.getByRole("tablist", { name: "Open tabs" });
  const chatTab = strip.locator('[data-tab^="/s/"]');
  await chatTab.getByRole("button", { name: "Tab actions" }).click();
  await page.getByRole("menuitem", { name: "Add to Top Shelf" }).click();
  await shelf.getByText("work · Main").waitFor();
  await chatTab.getByRole("button", { name: /^Close / }).click();
  await chatTab.waitFor({ state: "detached" });
  for (let saved = true; saved; await Bun.sleep(100)) {
    saved = await page.evaluate(async (url: string) => (await (await fetch(`${url}/api/Tabs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })).json()).state.tabs.some((tab: { path: string }) => tab.path.startsWith("/s/")), remote);
  }
  // From a local space it opens on the remote, in its space, without a tab.
  await page.goto(`${local}/`);
  await shelf.getByRole("tab").click();
  await page.waitForURL((url: URL) => url.pathname === chat);
  await page.locator("#transcript-scroll").getByText("Remote reply", { exact: true }).first().waitFor();
  await shelf.getByRole("tab", { selected: true }).waitFor();
  await page.getByRole("button", { name: "Space: work / Main" }).waitFor();
  expect(await chatTab.count()).toBe(0);

  // A shelved Search page opens without a tab too, also from a space other than Main, and its edits save into the shelf.
  await page.evaluate(async () => { await fetch("/api/Tabs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ createSpace: "Side" }) }); });
  await page.goto(`${local}/`);
  await page.getByRole("button", { name: "Space: Main" }).click();
  await page.getByRole("menuitemradio", { name: "Side" }).click();
  await page.getByRole("button", { name: "Space: Side" }).waitFor();
  for (let entered = false; !entered; await Bun.sleep(100)) {
    entered = await page.evaluate(async () => {
      const view = await (await fetch("/api/Tabs", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })).json();
      return view.spaces.some((space: { id: string; name: string }) => space.name === "Side" && space.id === view.state.space);
    });
  }
  await page.goto(`${local}/search/shelved?q=Shelved`);
  const searchTab = strip.locator('[data-tab="/search/shelved"]');
  await searchTab.getByRole("button", { name: "Tab actions" }).click();
  await page.getByRole("menuitem", { name: "Add to Top Shelf" }).click();
  await shelf.getByText("Local · Side").waitFor();
  await searchTab.getByRole("button", { name: /^Close / }).click();
  await searchTab.waitFor({ state: "detached" });
  await shelf.getByRole("tab", { name: "Shelved", exact: true }).click();
  const query = page.getByRole("textbox", { name: "Search sessions" });
  expect(await query.inputValue()).toBe("Shelved");
  await query.fill("Shelved edit");
  for (let saved = false; !saved; await Bun.sleep(100)) {
    saved = await page.evaluate(async () => JSON.stringify((await (await fetch("/api/Tabs", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })).json()).topShelf).includes("Shelved edit"));
  }
  await page.reload();
  await page.getByRole("textbox", { name: "Search sessions" }).waitFor();
  expect(await page.getByRole("textbox", { name: "Search sessions" }).inputValue()).toBe("Shelved edit");
  expect(await searchTab.count()).toBe(0);
  // Removing a row takes it off the shelf.
  await shelf.getByRole("tab", { name: "Shelved edit", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Remove from Top Shelf" }).click();
  await shelf.getByRole("tab", { name: "Shelved edit", exact: true }).waitFor({ state: "detached" });

  // The space picker is a tree of servers; a remote's space opens on that server, whose settings it shows.
  // An empty space has no tab to open, so the tree must enter it on arrival.
  await page.goto(`${local}/`);
  await page.evaluate(async (url: string) => { await fetch(`${url}/api/Tabs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ createSpace: "Billing" }) }); }, remote);
  await page.getByRole("button", { name: /^Space: / }).click();
  await page.getByRole("group", { name: "work" }).getByRole("menuitem", { name: "Billing" }).click();
  await page.getByRole("button", { name: "Space: work / Billing" }).waitFor();
  expect(new URL(page.url()).pathname).toStartWith("/r/work/");
  await page.goto(`${local}/r/work/config`);
  await page.getByText(process.env.ROCKETCLAW_REMOTE_TEST_WORKSPACE!, { exact: true }).waitFor();
  // Config can render before Protocol answers; establish online before taking the remote offline.
  await (await page.waitForResponse((response: { url(): string; ok(): boolean }) => response.url() === `${remote}/api/Protocol` && response.ok())).finished();

  // With the remote off, its page says so and a local space is still a choice away.
  expect(await page.evaluate(async () => (await fetch("/test/remote/down", { method: "POST" })).ok)).toBe(true);
  await page.getByText("work is offline; this page reopens when it is reachable.").waitFor();
  await page.getByRole("button", { name: /^Space: work \/ / }).click();
  await page.getByRole("group", { name: "Local" }).getByRole("menuitem", { name: "Main" }).click();
  await page.getByRole("button", { name: "Space: Main" }).waitFor();
  expect(await page.evaluate(async () => (await fetch("/test/remote/up", { method: "POST" })).ok)).toBe(true);

  // A remote on another protocol keeps working with a notice and never reloads the page; a local one reloads it.
  await page.goto(`${local}/r/work/agents`);
  await page.evaluate(() => Object.assign(window, { kept: true }));
  expect(await page.evaluate(async () => (await fetch("/test/version/work", { method: "POST" })).ok)).toBe(true);
  await page.getByText("work runs a different RocketClaw version; some actions may fail.").waitFor();
  await page.getByRole("heading", { name: "Agents" }).waitFor();
  expect(await page.evaluate(() => "kept" in window)).toBe(true);
  expect(await page.evaluate(async () => (await fetch("/test/version/local", { method: "POST" })).ok)).toBe(true);
  await page.waitForFunction(() => !("kept" in window));

  await page.goto(`${local}/r/gone/agents`);
  await page.getByText(/^gone is unavailable: .*Address: https:\/\/gone\./).waitFor();
}, 90_000);
