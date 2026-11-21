import { expect, onTestFinished, test } from "bun:test";
import { href, withPage, names, italic, behind, paletteReady, animation, order, until, openSaved, openFromPalette, follow, menu, chooseSpace, shared, sharedNames, focus, strip } from "./tabs-browser_test";

test("each opened location keeps a tab; close commands, middle-click, and drag survive reload", () => withPage({}, async (page, origin, prompts, saved) => {
  await page.goto(`${origin}/`);
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await tab("New session").waitFor();
  await page.getByPlaceholder("Message a new session").fill("hello");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL(`**${href("fresh")}`);
  expect(await names(page)).toEqual(["hello"]);
  expect(prompts.map((prompt) => prompt.id)).toEqual(["fresh"]);

  await follow(page, "bravo");
  await tab("hello").click();
  await page.waitForURL(`**${href("fresh")}`);
  await follow(page, "alpha");
  await follow(page, "bravo");
  expect(await names(page)).toEqual(["hello", "Alpha", "Bravo"]);
  expect(await italic(tab("Alpha"))).toBe(false);
  await follow(page, "charlie");
  await follow(page, "delta");
  expect(await names(page)).toEqual(["hello", "Alpha", "Bravo", "Charlie", "Delta"]);
  await menu(page, "Bravo", "Close to the right");
  expect(await names(page)).toEqual(["hello", "Alpha", "Bravo"]);
  await page.waitForURL(`**${href("bravo")}`);
  await openFromPalette(page, "Charlie");
  await menu(page, "Bravo", "Close others");
  expect(await names(page)).toEqual(["Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");

  await follow(page, "charlie");
  await follow(page, "delta");
  await tab("Charlie").click();
  await page.waitForURL(`**${href("charlie")}`);
  await tab("Charlie").click({ button: "middle" });
  await page.waitForURL(`**${href("delta")}`);
  expect(await names(page)).toEqual(["Bravo", "Delta"]);
  await tab("Delta").click({ button: "middle" });
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Bravo"]);

  await follow(page, "charlie");
  expect(await names(page)).toEqual(["Bravo", "Charlie"]);
  await tab("Charlie").dragTo(tab("Bravo"));
  expect(await names(page)).toEqual(["Charlie", "Bravo"]);
  await until(() => saved.state.tabs.map((tab) => tab.path).join() === [href("charlie"), href("bravo")].join());
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await names(page)).toEqual(["Charlie", "Bravo"]);
  expect(await tab("Charlie").getAttribute("aria-selected")).toBe("true");

  await menu(page, "Charlie", "Close all");
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["New session"]);
  await strip(page).getByRole("button", { name: "Close New session" }).click();
  expect(await names(page)).toEqual(["New session"]);
  expect(page.url()).toBe(`${origin}/`);
}), 60_000);

test("arrowing through the palette previews sessions in this space; Escape returns and closes the preview, Enter keeps", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const palette = page.getByRole("dialog", { name: "Go to session" });
  const step = async (key: string, id: string) => { await page.keyboard.press(key); await page.waitForURL(`**${href(id)}`); };
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Alpha").waitFor();
  const depth = await page.evaluate(() => history.length);
  await page.keyboard.press("Control+p");
  await paletteReady(page);
  await step("ArrowDown", "bravo");
  await step("ArrowDown", "charlie");
  expect(await behind(page)).toEqual(["Alpha", "Charlie*"]);
  await step("ArrowDown", "delta");
  expect(await behind(page)).toEqual(["Alpha", "Delta*"]);
  // Echo was born in another space, so its row shows where the palette opened.
  await step("ArrowDown", "alpha");
  await step("ArrowUp", "delta");
  await step("Escape", "alpha");
  await palette.waitFor({ state: "hidden" });
  expect(await page.evaluate(() => history.length)).toBe(depth);
  expect(await names(page)).toEqual(["Alpha"]);

  await page.keyboard.press("Control+p");
  await paletteReady(page);
  for (const id of ["bravo", "charlie", "delta"]) await step("ArrowDown", id);
  await page.keyboard.press("Enter");
  await palette.waitFor({ state: "hidden" });
  expect(page.url()).toEndWith(href("delta"));
  expect(await names(page)).toEqual(["Alpha", "Delta"]);
  expect(await italic(tab("Delta"))).toBe(false);
  await page.goBack();
  await page.waitForURL(`**${href("alpha")}`);

  await page.keyboard.press("Control+p");
  await paletteReady(page);
  for (const id of ["bravo", "charlie", "delta"]) await step("ArrowDown", id);
  expect(await behind(page)).toEqual(["Alpha", "Delta"]);
  await step("Escape", "alpha");
  await palette.waitFor({ state: "hidden" });

  await page.keyboard.press("Control+p");
  await paletteReady(page);
  await step("ArrowDown", "bravo");
  await page.keyboard.type("Tabs: Close");
  await page.waitForURL(`**${href("alpha")}`);
  await step("Enter", "delta");
  expect(await names(page)).toEqual(["Delta"]);
}), 30_000);

test("a palette preview stays in its window: never saved, and kept while other windows' changes arrive", () => withPage({}, async (page, origin, _prompts, saved) => {
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  const before = saved.revision;
  await page.keyboard.press("Control+p");
  await paletteReady(page);
  await page.keyboard.press("ArrowDown");
  await page.waitForURL(`**${href("bravo")}`);
  // A save would follow the preview's render at once; none comes.
  await page.waitForTimeout(300);
  expect(saved.revision).toBe(before);
  expect(saved.state).toMatchObject({ tabs: [{ path: href("alpha") }], last: { main: href("alpha") } });

  [saved.state.tabs, saved.revision] = [[...saved.state.tabs, { path: href("charlie"), space: "main" }], saved.revision + 1];
  await focus(page);
  await page.locator('[aria-label="Main tabs"] [role="tab"][aria-label="Charlie"]').waitFor({ state: "attached" });
  expect(await behind(page)).toEqual(["Alpha", "Bravo*", "Charlie"]);
  expect(page.url()).toEndWith(href("bravo"));
  await page.keyboard.press("Escape");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);
}), 30_000);

for (const width of [1280, 320]) test(`the bottom bar's buttons fit without overlap at ${width}px`, () => withPage({ viewport: { width, height: 800 }, hasTouch: width === 320 }, async (page, origin) => {
  await page.goto(`${origin}${href("alpha")}`);
  const buttons = page.locator("#bottom-navigation").getByRole("button");
  await buttons.first().waitFor();
  const boxes = await buttons.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => {
    const { x, y, width, height } = node.getBoundingClientRect();
    return { x, y, width, height };
  }));
  // The sidebar toggle, the four middle buttons, and the theme toggle, left to right without overlap.
  expect(boxes).toHaveLength(6);
  expect(await page.locator("#bottom-navigation").getByRole("button", { name: "Pull requests and issues", exact: true }).count()).toBe(0);
  expect(boxes[0].x).toBeLessThan(16);
  expect(boxes[5].x + boxes[5].width).toBeGreaterThan(width - 16);
  for (const [index, box] of boxes.entries()) {
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    expect(Math.round(box.width)).toBe(width === 320 ? 48 : 32);
    expect(Math.round(box.height)).toBe(width === 320 ? 48 : 32);
    if (index) expect(boxes[index - 1].x + boxes[index - 1].width).toBeLessThanOrEqual(box.x);
  }
}), 30_000);

test("a tab menu's Copy link copies the tab in view's whole address, and another tab's own", () => withPage({ permissions: ["clipboard-read", "clipboard-write"] }, async (page, origin, _prompts, saved) => {
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  const url = `${origin}${href("alpha")}?view=one%20%26%20two#result%2Fthree`;
  await page.goto(url);
  await strip(page).getByRole("button", { name: "Tab actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Copy link", exact: true }).click();
  const copied = async (link: string) => { while (await page.evaluate(() => navigator.clipboard.readText()) !== link) await Bun.sleep(10); };
  await copied(url);
  await menu(page, "Bravo", "Copy link");
  await copied(`${origin}${href("bravo")}`);
}), 30_000);

test("Copy link on plain HTTP without Clipboard API", () => withPage({ permissions: ["clipboard-read", "clipboard-write"] }, async (page, origin) => {
  await page.route("http://copy.test/**", async (route: any) => {
    if (new URL(route.request().url()).pathname === "/stream") return route.fulfill({ contentType: "text/event-stream", body: ": connected\n\n" });
    await route.fulfill({ response: await route.fetch({ url: route.request().url().replace("http://copy.test", origin) }) });
  });
  const url = `http://copy.test${href("alpha")}?view=one%20%26%20two#result%2Fthree`;
  await page.goto(url);
  expect(await page.evaluate(() => ({ secure: isSecureContext, clipboard: typeof navigator.clipboard }))).toEqual({ secure: false, clipboard: "undefined" });
  await strip(page).getByRole("button", { name: "Tab actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Copy link", exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
  const reader = await page.context().browser().newPage({ permissions: ["clipboard-read", "clipboard-write"] });
  await reader.goto(origin);
  expect(await reader.evaluate(() => navigator.clipboard.readText())).toBe(url);
  await reader.close();
}), 30_000);

test("saved tabs, deep links, keyboard, page close, palette commands, and the sidebar", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await names(page)).toEqual(["Alpha"]);
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");
  await page.reload();
  await tab("Alpha").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-selected")).toBe("true");

  await tab("Bravo").focus();
  await page.keyboard.press("Home");
  expect(await tab("Alpha").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("End");
  expect(await tab("Bravo").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("ArrowLeft");
  expect(await tab("Alpha").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  expect(page.url()).toEndWith(href("bravo"));
  await page.keyboard.press("Enter");
  await page.waitForURL(`**${href("alpha")}`);
  await page.keyboard.press("Shift+ArrowRight");
  expect(await names(page)).toEqual(["Bravo", "Alpha"]);
  expect(await tab("Alpha").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Shift+F10");
  // Alpha is the last tab, so Close to the right closes nothing.
  await page.getByRole("menuitem", { name: "Close to the right", exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
  expect(await names(page)).toEqual(["Bravo", "Alpha"]);

  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Cron: Dashboard" }).click();
  await page.waitForURL("**/cron");
  await tab("Cron").dblclick();
  await page.getByRole("heading", { name: "Cron" }).waitFor();
  await page.keyboard.press("Escape");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await names(page)).toEqual(["Bravo", "Alpha"]);
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Settings" }).click();
  await page.waitForURL("**/config");
  await page.getByRole("main").getByRole("button", { name: "Close", exact: true }).click();
  await page.waitForURL(`**${href("alpha")}`);
  expect(await names(page)).toEqual(["Bravo", "Alpha"]);
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Close", exact: true }).click();
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Bravo"]);

  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  const mainBox = await page.getByRole("main").boundingBox();
  expect((await strip(page).boundingBox()).x + (await strip(page).boundingBox()).width).toBeLessThanOrEqual(mainBox.x);
  expect((await page.getByRole("banner").boundingBox()).x).toBe(mainBox.x);
  await page.locator("footer").getByRole("button", { name: "Hide sidebar", exact: true }).click();
  await strip(page).waitFor({ state: "hidden" });
  expect(await page.getByRole("banner").textContent()).toBe("Bravo");
  await page.keyboard.press("Control+b");
  await strip(page).waitFor();
  expect(await page.locator("footer").getByRole("button", { name: "Hide sidebar", exact: true }).getAttribute("aria-expanded")).toBe("true");
  for (const state of ["hidden", "visible"] as const) { // The palette runs the same toggle as Cmd/Ctrl+B.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Tabs: Toggle sidebar" }).click();
    await strip(page).waitFor({ state });
  }
  const width = (await strip(page).boundingBox()).width;
  await page.getByRole("separator", { name: "Resize tabs" }).focus();
  await page.keyboard.press("ArrowRight");
  expect((await strip(page).boundingBox()).width).toBe(width + 16);
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  expect((await strip(page).boundingBox()).width).toBe(width + 16);
  await page.setViewportSize({ width: 375, height: 700 });
  await strip(page).waitFor({ state: "detached" });
}), 60_000);

test("tab menu renames sessions in place, bracket shortcuts cycle tabs, and double-clicking empty strip opens a new session", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) {
    await openSaved(page, origin, href(id), name, saved).catch(async (error: unknown) => {
      console.error("Rename test could not open saved tab", JSON.stringify({ expectedPath: href(id), expectedName: name, actualURL: page.url(), saved }), error);
      await page.evaluate(() => ({
        url: location.href, title: document.title, readyState: document.readyState, focused: document.hasFocus(), activeElement: document.activeElement?.outerHTML,
        tabs: [...document.querySelectorAll('[data-tab] [role="tab"]')].map((node) => ({
          path: node.closest("[data-tab]")!.getAttribute("data-tab"), label: node.getAttribute("aria-label"), selected: node.getAttribute("aria-selected"), description: node.getAttribute("aria-description"), text: node.textContent,
        })),
      })).then((state: unknown) => console.error("Rename test browser state", JSON.stringify(state)), (errSnapshot: unknown) => console.error("Rename test snapshot failed", errSnapshot));
      throw error;
    });
  }
  await page.getByPlaceholder("Message or /command").focus();
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`**${href("alpha")}`);
  await page.keyboard.press("Meta+BracketLeft");
  await page.waitForURL(`**${href("charlie")}`);
  await page.keyboard.press("Meta+BracketLeft");
  await page.waitForURL(`**${href("bravo")}`);
  for (const [command, id] of [["Tabs: Next", "charlie"], ["Tabs: Previous", "bravo"]]) { // The palette closes, then steps like the brackets.
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: command }).click();
    await page.waitForURL(`**${href(id)}`);
  }

  const field = strip(page).getByRole("textbox", { name: "Session name" });
  await menu(page, "Alpha", "Rename session");
  expect(await field.evaluate((node: HTMLInputElement) => document.activeElement === node && node.selectionEnd! - node.selectionStart! === node.value.length)).toBe(true);
  await field.fill("Discarded");
  await field.press("Escape");
  await field.waitFor({ state: "detached" });
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  const long = "A renamed session whose name is much longer than twelve rem";
  await menu(page, "Alpha", "Rename session");
  await field.fill(long);
  await field.press("Enter");
  await tab(long).waitFor();
  await field.waitFor({ state: "detached" });
  expect(page.url()).toEndWith(href("bravo"));
  expect(await tab(long).evaluate((node: HTMLElement) => node.getBoundingClientRect().right <= node.closest('[role="tablist"]')!.getBoundingClientRect().right)).toBe(true);
  await tab(long).hover();
  expect(await page.locator('[data-slot="tooltip-content"][data-open]').innerText()).toBe(long);
  await page.mouse.move(640, 700);
  await page.locator('[data-slot="tooltip-content"][data-open]').waitFor({ state: "hidden" });
  await tab("Bravo").focus();
  await page.keyboard.press("ArrowLeft");
  await page.locator('[data-slot="tooltip-content"]').filter({ hasText: long }).waitFor();
  expect(await tab(long).evaluate((node: HTMLElement) => document.activeElement === node && getComputedStyle(node).outlineStyle)).toBe("solid");
  await menu(page, long, "Rename session");
  await field.fill("Renamed");
  await page.getByPlaceholder("Message or /command").click();
  await tab("Renamed").waitFor();
  const { promise: titleGate, resolve: releaseTitle } = Promise.withResolvers();
  saved.titleGate = titleGate;
  await menu(page, "Renamed", "Regenerate title");
  await tab("Renamed").and(page.locator('[aria-description*="Regenerating title"]')).waitFor();
  releaseTitle(undefined);
  await tab("Regenerated").waitFor();
  expect(await tab("Regenerated").getAttribute("aria-description")).not.toContain("Regenerating title");

  const bar = page.getByRole("banner"), barMenu = bar.getByRole("button", { name: "Page actions", exact: true });
  const barTitle = await bar.getByText("Bravo", { exact: true }).boundingBox();
  expect((await barMenu.boundingBox()).x - (barTitle.x + barTitle.width)).toBeLessThanOrEqual(8);
  const items = async (button: any) => {
    await button.click();
    const texts = await page.getByRole("menuitem").allInnerTexts();
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
    return texts;
  };
  expect(await items(barMenu)).toEqual(await items(strip(page).getByRole("button", { name: "Tab actions", exact: true })));
  await barMenu.click();
  await page.getByRole("menuitem", { name: "Rename session", exact: true }).click();
  await bar.getByRole("textbox", { name: "Session name" }).fill("Renamed from the bar");
  await bar.getByRole("textbox", { name: "Session name" }).press("Enter");
  await tab("Renamed from the bar").waitFor();
  expect(await bar.textContent()).toBe("Renamed from the bar");

  const box = await strip(page).boundingBox();
  await strip(page).dblclick({ position: { x: box.width / 2, y: box.height - 2 } });
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["Regenerated", "Renamed from the bar", "New session", "Charlie"]);
  await tab("New session").click({ button: "right" });
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain("Rename session");
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain("Regenerate title");
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain("Pull requests and issues");
}), 60_000);

test("a RocketClaw session's tab menu and palette delete it after asking, showing a refusal and closing its tab", async () => {
  const started = performance.now();
  const teardown = { pageErrorsChecked: false, browserCloseStarted: undefined as number | undefined, browserClosed: false };
  let waitingFor = "open Chromium page", observed: (() => object) | undefined;
  let lastVisibleTabs: (string | null)[] | undefined;
  let firstDelete: object | undefined, refusalStatus: number | undefined, refusalTransport = "not requested", lastRefusalRegion = "not observed";
  onTestFinished(() => {
    if (waitingFor) {
      const refusal = waitingFor === "show the running-turn refusal" || waitingFor === "confirm deletion from the tab menu"
        ? { refusalStatus, refusalTransport, lastRefusalRegion } : {};
      const cleanup = waitingFor === "check page errors";
      const { pageErrorsChecked, browserCloseStarted, browserClosed } = teardown;
      const stage = cleanup && pageErrorsChecked ? (browserClosed ? "stop the test server" : "close Chromium") : waitingFor;
      const browserCloseStartedMs = browserCloseStarted === undefined ? undefined : Math.round(browserCloseStarted - started);
      expect({ waitingFor: stage, elapsedMs: Math.round(performance.now() - started), lastVisibleTabs, ...observed?.(), ...refusal,
        ...(cleanup ? { pageErrorsChecked, browserCloseStartedMs, browserClosed } : {}) },
        `Expected to finish: ${stage}`).toMatchObject({ waitingFor: "" });
    }
  });
  await withPage({}, async (page, origin, _prompts, saved) => {
    saved.state = { ...saved.state, space: "main", tabs: ["alpha", "bravo"].map((id) => ({ path: href(id), space: "main" })) };
    observed = () => ({ url: page.url(), listSessionsCalls: saved.polls, deleteRequests: saved.deletes, savedTabs: saved.state.tabs.map((tab) => tab.path), savedRevision: saved.revision });
    waitingFor = "navigate to Bravo";
    await page.goto(`${origin}${href("bravo")}`);
    waitingFor = "show the saved Alpha tab";
    await strip(page).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
    const confirm = page.getByRole("dialog", { name: "Delete session", exact: true });
    waitingFor = "select Delete session in Bravo's tab menu";
    await menu(page, "Bravo", "Delete session");
    waitingFor = "show the confirmation naming Bravo";
    await confirm.getByText('Delete session "Bravo"?', { exact: true }).waitFor();
    page.on("request", (request: { url(): string }) => {
      if (firstDelete || request.url() !== `${origin}/api/DeleteSession`) return;
      firstDelete = request;
      refusalTransport = "requested";
    });
    page.on("response", (response: { request(): object; status(): number }) => {
      if (response.request() === firstDelete) refusalStatus = response.status();
    });
    page.on("requestfinished", (request: object) => {
      if (request === firstDelete) refusalTransport = "body downloaded";
    });
    page.on("requestfailed", (request: { failure(): { errorText: string } | null }) => {
      if (request === firstDelete) refusalTransport = request.failure()?.errorText ?? "request failed";
    });
    const refusalSnapshot = "delete-refusal ";
    page.on("console", (message: { text(): string }) => {
      const text = message.text();
      if (text.startsWith(refusalSnapshot)) lastRefusalRegion = text.slice(refusalSnapshot.length);
    });
    await page.evaluate((prefix: string) => {
      const dialog = Array.from(document.querySelectorAll('[role="dialog"]')).find((element) =>
        document.getElementById(element.getAttribute("aria-labelledby") ?? "")?.textContent === "Delete session");
      let previous = "";
      const snapshot = () => {
        const state = JSON.stringify(dialog?.isConnected ? {
          alerts: Array.from(dialog.querySelectorAll('[role="alert"]'), (alert) => alert.textContent),
          disabledButtons: Array.from(dialog.querySelectorAll("button:disabled"), (button) => button.textContent),
        } : "dialog not mounted");
        if (state === previous) return;
        previous = state;
        console.debug(prefix + state);
      };
      const observer = new MutationObserver(() => {
        snapshot();
        if (!dialog?.isConnected) observer.disconnect();
      });
      if (dialog) observer.observe(document, { childList: true, characterData: true, subtree: true, attributes: true, attributeFilter: ["disabled", "role"] });
      snapshot();
    }, refusalSnapshot);
    waitingFor = "confirm deletion from the tab menu";
    await confirm.getByRole("button", { name: "Delete session", exact: true }).click();
    waitingFor = "show the running-turn refusal";
    await confirm.getByRole("alert").getByText("this session has a running turn; stop it first", { exact: true }).waitFor();
    waitingFor = "cancel the refused deletion";
    await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
    waitingFor = "hide the confirmation after cancelling";
    await confirm.waitFor({ state: "hidden" });
    waitingFor = "keep both tabs after cancelling";
    lastVisibleTabs = await names(page);
    expect(lastVisibleTabs).toEqual(["Alpha", "Bravo"]);

    waitingFor = "open the command palette";
    await page.keyboard.press("Control+Shift+p");
    waitingFor = "type Delete session in the command palette";
    await page.getByPlaceholder("Type a command").fill("Delete session");
    waitingFor = "select Sessions: Delete session in the command palette";
    await page.getByRole("button", { name: "Sessions: Delete session" }).click();
    waitingFor = "confirm deletion from the command palette";
    await confirm.getByRole("button", { name: "Delete session", exact: true }).click();
    waitingFor = "save the tabs without Bravo";
    await until(() => !saved.state.tabs.some((tab) => tab.path === href("bravo")));
    waitingFor = "verify the two Bravo delete requests and the remaining Alpha tab";
    expect(saved.deletes).toEqual(["bravo", "bravo"]);
    lastVisibleTabs = await names(page);
    expect(lastVisibleTabs).toEqual(["Alpha"]);
    waitingFor = "check page errors";
  }, teardown);
  waitingFor = "";
});

test("running tabs keep a reachable close control, and closing hands focus to the next tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"], [href("delta"), "Delta"], ["/search", "Search"]]) {
    await page.goto(`${origin}${link}`);
    await tab(name).waitFor();
    // The strip draws before the saved tabs load, but only loaded tabs are saved; leaving earlier drops this tab.
    await until(() => saved.state.tabs.some((tab) => tab.path === link));
  }
  await tab("Delta").waitFor();
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo", "Charlie", "Delta", "Search"]);
  await page.mouse.move(640, 400);
  await tab("Delta").and(page.locator('[aria-description="Space: Main, Turn running, Background work running"]')).waitFor();
  const close = strip(page).getByRole("button", { name: "Close Delta" });
  expect(await close.evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  await close.focus();
  expect(await close.evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Enter");
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo", "Charlie", "Search"]);
  expect(page.url()).toEndWith("/search");
  expect(await tab("Search").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Shift+F10");
  await page.getByRole("menuitem", { name: "Close", exact: true }).press("Enter");
  await page.waitForURL(`**${href("charlie")}`);
  // The menu hands focus back as it finishes closing, which can land after the URL changes.
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Main tabs"] [role="tab"][aria-label="Charlie"]'));
}), 60_000);

test("narrow screens show the tabs in a drawer opened by the footer's sidebar button or a right swipe and closed by a pick, Escape, or a left swipe, and head the page with the tab's name", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const title = page.getByRole("banner");
  for (const id of ["alpha", "bravo"]) {
    await page.goto(`${origin}${href(id)}`);
    await until(() => saved.state.tabs.some((tab) => tab.path === href(id)));
  }
  const toggle = page.locator("footer").getByRole("button", { name: "Show sidebar", exact: true });
  expect(await strip(page).count()).toBe(0);
  await title.getByText("Bravo", { exact: true }).waitFor();

  await toggle.tap();
  expect(await strip(page).getAttribute("aria-orientation")).toBe("vertical");
  await tab("Alpha").tap();
  await page.waitForURL(`**${href("alpha")}`);
  await strip(page).waitFor({ state: "detached" });
  expect(await title.textContent()).toBe("Alpha");
  // Picking the tab already shown closes the drawer too, by touch or mouse.
  for (const pick of ["tap", "click"]) {
    await toggle[pick]();
    await tab("Alpha")[pick]();
    await strip(page).waitFor({ state: "detached" });
  }
  expect(page.url()).toEndWith(href("alpha"));

  const cdp = await page.context().newCDPSession(page);
  // Moves come at a finger's 60 Hz pace: Chrome on Linux turns an instant burst into a fling that navigates back.
  const swipe = async (x: number, y: number, dx: number) => {
    await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
    for (let step = 1; step <= 12; step++) {
      await page.waitForTimeout(16);
      await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: x + dx * step / 12, y }] });
    }
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  };
  // A swipe that starts in a text field is left to the field.
  const field = (await page.getByPlaceholder("Message or /command").boundingBox())!;
  await swipe(field.x + 20, field.y + 20, 200);
  await page.waitForTimeout(300);
  expect(await strip(page).count()).toBe(0);
  await swipe(100, 300, 200);
  await strip(page).waitFor();
  await swipe(250, 300, -230);
  await strip(page).waitFor({ state: "detached" });

  // Escape closes only the drawer, not the non-chat tab behind it.
  await page.goto(`${origin}/config`);
  await title.getByText("Settings", { exact: true }).waitFor();
  await toggle.tap();
  expect(await names(page)).toContain("Settings");
  await page.keyboard.press("Escape");
  await strip(page).waitFor({ state: "detached" });
  expect(new URL(page.url()).pathname).toBe("/config");
}), 60_000);

test("tab status icons and running titles keep their layout and shimmer in both themes", () => withPage({}, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const title = (name: string) => tab(name).getByText(name, { exact: true });
  const indent = (name: string) => title(name).evaluate((node: HTMLElement) => node.getBoundingClientRect().left - node.parentElement!.getBoundingClientRect().left);
  await page.goto(`${origin}${href("delta")}`);
  await tab("Delta").waitFor();
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Delta").and(page.locator('[aria-description="Space: Main, Turn running, Background work running"]')).waitFor();
  await page.mouse.move(640, 600);
  expect(await animation(title("Delta"))).not.toBe("none");
  expect(await animation(title("Alpha"))).toBe("none");
  expect(await strip(page).getByRole("button", { name: "Close Delta" }).evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("1");
  await tab("Delta").click();
  await page.waitForURL(`**${href("delta")}`);
  expect(await animation(title("Delta"))).not.toBe("none");
  expect(await indent("Delta")).toBe(await indent("Alpha"));
  expect(await strip(page).getByRole("img", { name: "Turn running" }).count()).toBe(0);
  expect(await animation(tab("Delta").locator("svg"))).toBe("none");

  const sweep = async (name: string) => {
    const title = tab(name).getByText(name, { exact: true });
    const levels: number[] = [];
    for (const progress of [0, 0.2, 0.35, 0.5, 0.65, 0.8]) {
      await title.evaluate((node: Element, progress: number) => { const run = node.getAnimations()[0]!; run.pause(); run.currentTime = Number(run.effect!.getTiming().duration) * progress; }, progress);
      const png = (await title.screenshot()).toString("base64");
      levels.push(await page.evaluate(async (png: string) => {
        const image = new Image();
        image.src = `data:image/png;base64,${png}`;
        await image.decode();
        const canvas = Object.assign(document.createElement("canvas"), { width: image.width, height: image.height });
        const context = canvas.getContext("2d")!;
        context.drawImage(image, 0, 0);
        const { data } = context.getImageData(0, 0, image.width, image.height);
        let sum = 0;
        for (let i = 0; i < data.length; i += 4) sum += (data[i]! + data[i + 1]! + data[i + 2]!) / 3;
        return sum / (data.length / 4);
      }, png));
    }
    return Math.max(...levels) - Math.min(...levels);
  };
  // Only a selected title in dark mode dims under the band; each state and each scheme is swept once.
  await page.emulateMedia({ colorScheme: "dark" });
  expect(await sweep("Delta")).toBeGreaterThan(8);
  await tab("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  await page.emulateMedia({ colorScheme: "light" });
  expect(await sweep("Delta")).toBeGreaterThan(8);

  await page.goto(`${origin}${href("echo")}`);
  await tab("Echo").and(page.locator('[aria-description$="Background work running"]')).waitFor();
  expect(await tab("Echo").getAttribute("aria-description")).toEndWith("Background work running");
  expect(await tab("Echo").locator("svg").getAttribute("class")).toContain("lucide-hourglass");
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running, Background work running");
  expect(await tab("Delta").locator("svg").getAttribute("class")).toContain("lucide-hourglass");

  await page.goto(`${origin}${href("hotel")}`);
  await tab("Hotel").and(page.locator('[aria-description$="Forked session"]')).waitFor();
  expect(await tab("Hotel").getAttribute("aria-description")).toBe("Space: Main, Forked session");
  expect(await tab("Hotel").locator("svg").getAttribute("class")).toContain("lucide-git-fork");
}), 60_000);

test("with reduced motion a running tab's title shows a still gradient", () => withPage({ reducedMotion: "reduce" }, async (page, origin) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const gradient = (name: string) => tab(name).getByText(name, { exact: true }).evaluate((node: Element) => getComputedStyle(node).backgroundImage);
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Alpha").waitFor();
  await page.goto(`${origin}${href("delta")}`);
  const title = tab("Delta").getByText("Delta", { exact: true });
  await tab("Delta").and(page.locator('[aria-description$="Background work running"]')).waitFor();
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running, Background work running");
  expect(await animation(title)).toBe("none");
  expect(await gradient("Delta")).toStartWith("linear-gradient(");
  expect(await gradient("Alpha")).toBe("none");
}), 60_000);

test("collapsed group badges do not rescan sessions for each hidden tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  const sessions = Array.from({ length: 200 }, (_, index) => ({ id: `group-row-${index}`, name: `Row ${index}`, agent: "main", unreadAt: index === 79 ? "2026-01-01T00:00:00Z" : "", asking: index === 79 }));
  const tabs = sessions.slice(0, 80).map(({ id }) => ({ path: href(id), group: "work", space: "main" }));
  saved.state = { ...saved.state, space: "main", tabs: [...tabs, { path: "group:work", group: "work", space: "main" }], groups: [{ id: "work", name: "Work", color: "Blue" }] };
  await page.route("**/api/ListSessions*", (route: any) => route.fulfill({ contentType: "text/event-stream", body: `data: ${JSON.stringify({ sessions, owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n` }));
  await page.goto(origin + tabs[0].path);
  await strip(page).getByRole("tab", { name: "Row 79", exact: true }).waitFor();
  await until(() => saved.revision > 0);
  await page.evaluate(() => {
    const original = window.atob, counts = window as unknown as { groupReads: number };
    counts.groupReads = 0;
    window.atob = (value) => {
      const decoded = original(value);
      if (decoded.startsWith("group-row-")) counts.groupReads++;
      return decoded;
    };
  });
  await strip(page).getByRole("button", { name: "Work", exact: true }).click();
  const header = strip(page).locator('[data-group="work"]');
  await header.getByText("Needs you", { exact: true }).waitFor();
  expect(await names(page)).toEqual(["Row 0"]);
  expect(await header.locator("button span").first().evaluate((node: HTMLElement) => getComputedStyle(node).fontWeight)).toBe("700");
  // Count decoding work, not elapsed time; allow several renders, but not a scan per hidden tab.
  expect(await page.evaluate(() => (window as unknown as { groupReads: number }).groupReads)).toBeLessThan(sessions.length * tabs.length * 8);
}), 30_000);

test("an unread tab shows a bold title", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const weight = (name: string) => tab(name).getByText(name, { exact: true }).evaluate((title: HTMLElement) => getComputedStyle(title).fontWeight);
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openSaved(page, origin, href("foxtrot"), "Foxtrot", saved);
  const description = "Space: Main, Turn running, Unread";
  await tab("Foxtrot").and(page.locator(`[aria-description="${description}"]`)).waitFor();
  expect(await weight("Foxtrot")).toBe("700");
  expect(await weight("Alpha")).toBe("400");
  expect(await tab("Foxtrot").getAttribute("aria-description")).toBe(description);

  // A collapsed group holding an unread tab shows a bold name.
  const header = strip(page).getByRole("button", { name: "Work", exact: true });
  const groupWeight = () => header.locator("span").evaluate((name: HTMLElement) => getComputedStyle(name).fontWeight);
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  const field = page.getByRole("textbox", { name: "Group name" });
  await menu(page, "Alpha", "Add to group", "New group");
  await field.waitFor();
  await page.keyboard.type("Work");
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "Foxtrot", "Add to group", "Work");
  expect(await groupWeight()).toBe("500");
  await header.click();
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  expect(await groupWeight()).toBe("700");
}), 60_000);

test("a tab waiting on this person's answer stops shimmering and needs them, as does its collapsed group, which stays regular while the tab is read", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await page.title()).toBe("lab - RocketClaw");
  await openSaved(page, origin, href("golf"), "Golf", saved);
  expect(await tab("Golf").getAttribute("aria-description")).toBe("Space: Main, Needs your answer");
  expect(await animation(tab("Golf").getByText("Golf", { exact: true }))).toBe("none");
  expect(await tab("Golf").getByText("Needs you", { exact: true }).isVisible()).toBe(true);
  expect(await tab("Alpha").getByText("Needs you").count()).toBe(0);
  // Read, it does not count in the window title.
  expect(await page.title()).toBe("lab - RocketClaw");

  const field = page.getByRole("textbox", { name: "Group name" });
  await menu(page, "Golf", "Add to group", "New group");
  await field.waitFor();
  await page.keyboard.type("Work");
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await tab("Alpha").click();
  const header = strip(page).locator("[data-group]");
  expect(await header.getByText("Needs you").count()).toBe(0);
  await header.getByRole("button", { name: "Work", exact: true }).click();
  await tab("Golf").waitFor({ state: "detached" });
  expect(await header.getByText("Needs you", { exact: true }).isVisible()).toBe(true);
  expect(await header.getByRole("button", { name: "Work", exact: true }).locator("span").evaluate((name: HTMLElement) => getComputedStyle(name).fontWeight)).toBe("500");
}), 60_000);

for (const [platform, modifier] of [["MacIntel", "⌘"], ["Linux x86_64", "Ctrl"]]) test(`Cmd/Ctrl+Shift+A, its command and its bottom bar button open the next waiting tab, sessions asking a question first, longest waiting first, and the title counts them on ${platform}`, () => withPage({}, async (page, origin, _prompts, saved) => {
  await page.addInitScript((value: string) => Object.defineProperty(navigator, "platform", { value }), platform);
  const key = modifier === "⌘" ? "Meta" : "Control";
  const at = (minute: number) => `2026-10-22T10:0${minute}:00Z`;
  const sessions = [
    { id: "calm", name: "Calm", agent: "main" },
    { id: "unread-new", name: "Unread new", agent: "main", unreadAt: at(6) },
    { id: "asked-new", name: "Asked new", agent: "main", running: true, asking: true, stateSince: at(3), unreadAt: at(5) },
    { id: "unread-old", name: "Unread old", agent: "main", unreadAt: at(2) },
    { id: "asked-old", name: "Asked old", agent: "main", running: true, asking: true, stateSince: at(1), unreadAt: at(0) },
    // Read, parked, or without a tab, a session does not wait for this person, even while it asks.
    { id: "asked-read", name: "Asked read", agent: "main", running: true, asking: true, stateSince: at(0) },
    { id: "parked", name: "Parked", agent: "main", unreadAt: at(0), parked: true },
    { id: "untabbed", name: "Untabbed", agent: "main", running: true, asking: true, stateSince: at(0), unreadAt: at(0) },
  ];
  const ops = { name: "Ops", searches: [{ id: "0", name: "Needs you", query: "is:waiting" }], active: "0" };
  // Unread old is only in Shared, and the Ops Search page waits through its tab row.
  saved.state = { ...saved.state, space: "main", tabs: [...sessions.flatMap(({ id }) => ["untabbed", "unread-old"].includes(id) ? [] : [{ path: href(id), space: "main" }]), { path: "/search/dash", space: "main", search: ops }] };
  saved.shared = [{ space: "main", path: href("unread-old") }];
  const done = { upstreamSuccess: true, tabs: [{ path: "/search/dash", unreadAt: at(3) }] };
  await page.route("**/api/ListSessions*", (route: any) => route.fulfill({ contentType: "text/event-stream", body: `data: ${JSON.stringify({ sessions, owner: "tester" })}\n\ndata: ${JSON.stringify(done)}\n\nevent: complete\ndata: {}\n\n` }));
  await page.goto(origin + href("calm"));
  await strip(page).getByRole("tab", { name: "Asked old", exact: true }).waitFor();
  await page.waitForFunction(() => document.title === "(5) lab - RocketClaw");
  for (const path of [href("asked-old"), href("asked-new"), href("unread-old"), "/search/dash", href("unread-new"), href("asked-old")]) {
    await page.keyboard.press(`${key}+Shift+a`);
    await page.waitForURL((url: URL) => url.pathname === path);
  }
  // A Shared row it visits gains no tab of this person's.
  expect(await names(page)).not.toContain("Unread old");
  await page.keyboard.press(`${key}+Shift+p`);
  const command = page.getByRole("dialog", { name: "Run command", exact: true }).getByRole("button", { name: "Sessions: Next needing you", exact: true });
  expect(await command.locator("kbd").allTextContents()).toEqual([modifier, "Shift", "A"]);
  await command.click();
  await page.waitForURL(origin + href("asked-new"));
  await page.getByRole("button", { name: "Next session needing you", exact: true }).click();
  await page.waitForURL(origin + href("unread-old"));
  await page.keyboard.press(`${key}+Shift+a`);
  await page.getByRole("textbox", { name: "Search sessions", exact: true }).waitFor();
  await until(() => saved.state.last.main === "/search/dash");
  saved.state.tabs.find((tab) => tab.path === "/search/dash")!.search!.searches[0].query = "changed elsewhere";
  saved.revision++;
  // Leave as a refreshed query reaches the DOM, before passive effects can write the old page's URL.
  const leaving = await page.evaluate((modifier: string) => new Promise<string>((resolve) => {
    const input = document.querySelector<HTMLInputElement>('input[aria-label="Search sessions"]')!;
    const observer = new MutationObserver(() => {
      if (input.value !== "changed elsewhere") return;
      observer.disconnect();
      dispatchEvent(new KeyboardEvent("keydown", { key: "A", code: "KeyA", shiftKey: true, [modifier]: true, bubbles: true, cancelable: true }));
      resolve(location.pathname);
    });
    observer.observe(input, { attributes: true, attributeFilter: ["value"] });
    dispatchEvent(new Event("focus"));
  }), key === "Meta" ? "metaKey" : "ctrlKey");
  expect(leaving).toBe(href("unread-new"));
}), 60_000);

test("a space's Shared tabs head its card, which counts them, under a Shared pill that collapses them to the one in view, bold for a hidden unread row and needing this person for a hidden asking one", () => withPage({}, async (page, origin, _prompts, saved) => {
  const pill = page.getByRole("button", { name: "Shared in Main", exact: true }), title = page.getByRole("button", { name: "Main", exact: true }), card = page.locator('[data-card-key="/main"]');
  const weight = () => pill.locator("span").evaluate((name: HTMLElement) => getComputedStyle(name).fontWeight);
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, tabs: [{ path: href("charlie"), space: "ops" }] };
  saved.shared = ["alpha", "foxtrot", "golf"].map((id) => ({ space: "main", path: href(id) }));
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  await shared(page).getByRole("tab", { name: "Golf", exact: true }).waitFor();
  expect(await card.getByRole("tablist").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["Main Shared tabs", "Main tabs"]);
  expect(await shared(page, "Ops").count()).toBe(0);
  expect([await title.getAttribute("aria-description"), await weight(), await pill.locator("..").getByText("Needs you").count()]).toEqual(["1 waiting, Turn running", "500", 0]);
  await pill.click();
  await pill.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect(await sharedNames(page)).toEqual([]);
  await until(() => saved.layout?.folded?.["#shared/main"] === true);
  expect([await weight(), await pill.locator("..").getByText("Needs you", { exact: true }).isVisible()]).toEqual(["700", true]);
  await pill.click();
  await shared(page).getByRole("tab", { name: "Alpha", exact: true }).click();
  await page.waitForURL(`**${href("alpha")}`);
  await pill.click();
  await pill.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect(await sharedNames(page)).toEqual(["Alpha"]);
  await title.click();
  await title.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect([await sharedNames(page), await names(page)]).toEqual([["Alpha"], []]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await shared(page).count()).toBe(0);
}), 60_000);

test("a full sidebar scrolls Shared and the person's tabs as one while Parked stays put", () => withPage({ viewport: { width: 1280, height: 400 } }, async (page, origin, _prompts, saved) => {
  saved.shared = ["alpha", "bravo"].map((id) => ({ space: "main", path: href(id) }));
  saved.state = { ...saved.state, space: "main", groups: [{ id: "work", name: "Work", color: "Blue" }], tabs: [href("charlie"), href("delta"), "/cron", href("foxtrot"), href("golf"), "/shortcuts", href("hotel"), "group:work"].map((path, i) => ({ path, space: "main", group: i > 2 ? "work" : undefined })) };
  saved.attention = [{ path: href("hotel"), parked: true }];
  await page.goto(`${origin}${href("charlie")}`);
  const top = (locator: any) => locator.evaluate((node: HTMLElement) => node.getBoundingClientRect().top), parked = page.getByRole("button", { name: "Parked", exact: true });
  await parked.waitFor();
  const [sharedAt, parkedAt] = [await top(shared(page)), await top(parked)];
  await strip(page).getByRole("tab", { name: "Golf", exact: true }).hover();
  await page.mouse.wheel(0, 1000);
  await page.waitForFunction((at: number) => document.querySelector('[aria-label="Main Shared tabs"]')!.getBoundingClientRect().top < at, sharedAt);
  expect(await top(parked)).toBe(parkedAt);
}), 30_000);

test("tab groups take a name and color, collapse, adopt opened tabs, follow drags, and survive reload", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const header = strip(page).getByRole("button", { name: "Work", exact: true });
  const field = page.getByRole("textbox", { name: "Group name" });
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) await openSaved(page, origin, href(id), name, saved);
  await menu(page, "Alpha", "Add to group", "New group");
  await field.waitFor();
  expect(await field.evaluate((node: HTMLInputElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.type("Work");
  expect(await field.inputValue()).toBe("Work");
  await page.getByRole("radio", { name: "Green" }).check();
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "Bravo", "Add to group", "Work");
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Work, Space: Main");
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");

  // The new group holds its place with a placeholder among its tabs.
  await until(() => saved.state.groups.some((group) => group.name === "Work") && saved.state.tabs.filter((tab) => tab.group).map((tab) => tab.path).toSorted().join() === [href("alpha"), href("bravo"), `group:${saved.state.groups[0].id}`].toSorted().join());
  await page.reload();
  await tab("Alpha").waitFor();
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  await strip(page).getByRole("button", { name: "Edit Work", exact: true }).click();
  expect(await field.inputValue()).toBe("Work");
  expect(await page.getByRole("radio", { name: "Green" }).isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Main tabs"] [aria-label="Work"]'));

  await header.click();
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  expect(await order(page)).toEqual(["Work", "Charlie"]);
  await page.getByPlaceholder("Message or /command").focus();
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await header.getAttribute("aria-expanded")).toBe("true");
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);

  await tab("Bravo").click();
  await page.waitForURL(`**${href("bravo")}`);
  await openFromPalette(page, "Delta");
  await page.waitForURL(`**${href("delta")}`);
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Delta", "Charlie"]);
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Group Work, Space: Main, Turn running, Background work running");
  await tab("Charlie").dragTo(tab("Alpha"));
  expect(await order(page)).toEqual(["Work", "Charlie", "Alpha", "Bravo", "Delta"]);
  await menu(page, "Delta", "Remove from group");
  expect(await order(page)).toEqual(["Work", "Charlie", "Alpha", "Bravo", "Delta"]);
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Space: Main, Turn running, Background work running");
  await tab("Alpha").dragTo(tab("Delta"));
  expect(await order(page)).toEqual(["Work", "Charlie", "Bravo", "Delta", "Alpha"]);
  expect(await tab("Alpha").getAttribute("aria-description")).toBe("Space: Main");
}), 60_000);

test("Cmd/Ctrl+click and Shift+click select tabs that the tab menu groups and ungroups together", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const items = async (name: string) => {
    await tab(name).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Close", exact: true }).waitFor();
    const listed = await page.getByRole("menuitem").allInnerTexts();
    await tab(name).dispatchEvent("keydown", { key: "Escape" });
    await page.getByRole("menu").waitFor({ state: "hidden" });
    return listed;
  };
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"], ["delta", "Delta"]]) await openSaved(page, origin, href(id), name, saved);
  await tab("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  await tab("Charlie").click({ modifiers: ["ControlOrMeta"] });
  expect(page.url()).toEndWith(href("alpha"));
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("In selection, Space: Main");
  // An unselected tab's menu acts on it alone, and a selected one's menu has no Rename session.
  expect(await items("Bravo")).toContain("Rename session");
  expect(await items("Bravo")).toContain("Pull requests and issues");
  expect(await items("Charlie")).not.toContain("Rename session");
  expect(await items("Charlie")).not.toContain("Pull requests and issues");
  await menu(page, "Charlie", "Add to group", "New group");
  await page.keyboard.type("Work");
  await page.keyboard.press("Escape");
  expect(await order(page)).toEqual(["Work", "Alpha", "Charlie", "Bravo", "Delta"]);

  // Shift+click selects the shown tabs from the active one; the selection joins a group together.
  await menu(page, "Delta", "Add to group", "New group");
  await page.keyboard.press("Escape");
  await tab("Bravo").click({ modifiers: ["Shift"] });
  expect(await page.evaluate(() => getSelection()!.toString())).toBe("");
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("In selection, Space: Main");
  expect(await tab("Delta").getAttribute("aria-description")).toBe("Group Blue group, Space: Main, Turn running, Background work running");
  // Work, emptied, stays where it was.
  await menu(page, "Bravo", "Add to group", "Blue group");
  expect(await order(page)).toEqual(["Work", "Blue group", "Delta", "Alpha", "Charlie", "Bravo"]);
  await menu(page, "Alpha", "Remove from group");
  expect(await order(page)).toEqual(["Work", "Blue group", "Delta", "Alpha", "Charlie", "Bravo"]);
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("In selection, Space: Main");

  // Escape on a tab drops the selection; a plain click on the active tab does too.
  await tab("Alpha").focus();
  await page.keyboard.press("Escape");
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Space: Main");
  await tab("Bravo").click({ modifiers: ["ControlOrMeta"] });
  await tab("Alpha").click();
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Space: Main");
  expect(page.url()).toEndWith(href("alpha"));
}), 60_000);

test("a group moves whole by dragging its header onto a tab or a collapsed group's header, or with Shift+Arrow", () => withPage({}, async (page, origin, _prompts, saved) => {
  const header = (name: string) => strip(page).getByRole("button", { name, exact: true });
  const group = async (tab: string, name: string) => {
    await menu(page, tab, "Add to group", "New group");
    await page.keyboard.type(name);
    await page.keyboard.press("Escape");
    await page.getByRole("textbox", { name: "Group name" }).waitFor({ state: "detached" });
  };
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"], ["delta", "Delta"]]) await openSaved(page, origin, href(id), name, saved);
  await group("Alpha", "Work");
  await menu(page, "Bravo", "Add to group", "Work");
  await header("Work").dragTo(strip(page).getByRole("tab", { name: "Delta", exact: true }));
  expect(await order(page)).toEqual(["Charlie", "Delta", "Work", "Alpha", "Bravo"]);

  await group("Charlie", "Ops");
  await header("Ops").click();
  expect(await order(page)).toEqual(["Ops", "Delta", "Work", "Alpha", "Bravo"]);
  await header("Work").dragTo(header("Ops"));
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Ops", "Delta"]);

  await header("Work").focus();
  await page.keyboard.press("Shift+ArrowRight");
  expect(await order(page)).toEqual(["Ops", "Work", "Alpha", "Bravo", "Delta"]);
  expect(await header("Work").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await page.keyboard.press("Shift+ArrowLeft");
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Ops", "Delta"]);
  await until(() => saved.state.tabs.flatMap((tab) => tab.path.startsWith("group:") ? [] : [tab.path]).join() === ["alpha", "bravo", "charlie", "delta"].map(href).join());
}), 60_000);

test("tab group editor moves New session in, ungroups, closes, and deletes; collapsed groups skip arrow keys; a lost group is dropped", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const field = page.getByRole("textbox", { name: "Group name" });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name, saved);
  await menu(page, "Alpha", "Add to group", "New group");
  await page.keyboard.type("Work");
  await page.keyboard.press("Enter");
  await field.waitFor({ state: "detached" });
  await menu(page, "Bravo", "Add to group", "Work");
  expect(await order(page)).toEqual(["New session", "Work", "Alpha", "Bravo", "Charlie"]);

  await strip(page).getByRole("button", { name: "Edit Work", exact: true }).click();
  await page.getByRole("button", { name: "New tab in group", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "New session", "Charlie"]);
  expect(await tab("New session").getAttribute("aria-selected")).toBe("true");
  await menu(page, "Alpha", "Remove from group");
  expect(await order(page)).toEqual(["Alpha", "Work", "Bravo", "New session", "Charlie"]);
  expect(await tab("Alpha").getAttribute("aria-description")).toBe("Space: Main");

  await tab("Charlie").click();
  await page.waitForURL(`**${href("charlie")}`);
  await strip(page).getByRole("button", { name: "Work", exact: true }).click();
  await until(() => saved.state.groups.some((group) => group.collapsed));
  await page.reload();
  // Charlie comes from the URL before saved tabs load; Alpha waits for the saved group too.
  await tab("Alpha").waitFor();
  expect(await order(page)).toEqual(["Alpha", "Work", "Charlie"]);
  await tab("Alpha").focus();
  await page.keyboard.press("ArrowDown");
  expect(await tab("Charlie").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);

  await strip(page).getByRole("button", { name: "Work", exact: true }).focus();
  await page.keyboard.press("Shift+F10");
  await page.getByRole("button", { name: "Ungroup", exact: true }).click();
  expect(await order(page)).toEqual(["Alpha", "Work", "Bravo", "New session", "Charlie"]);
  // The editor hands focus back to the emptied group's header as it finishes closing; Delete group removes the group.
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Main tabs"] [aria-label="Work"]'));
  await strip(page).getByRole("button", { name: "Edit Work", exact: true }).click();
  await page.getByRole("button", { name: "Delete group", exact: true }).click();
  expect(await order(page)).toEqual(["Alpha", "Bravo", "New session", "Charlie"]);

  await tab("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  await menu(page, "Alpha", "Add to group", "New group");
  await page.keyboard.press("Escape");
  await field.waitFor({ state: "detached" });
  await menu(page, "New session", "Add to group", "Grey group");
  expect(await order(page)).toEqual(["Grey group", "Alpha", "New session", "Bravo", "Charlie"]);
  await strip(page).getByRole("button", { name: "Edit Grey group", exact: true }).click();
  await page.getByRole("button", { name: "Close group", exact: true }).click();
  await page.waitForURL(`**${href("bravo")}`);
  expect(await order(page)).toEqual(["Grey group", "Bravo", "Charlie"]);

  await menu(page, "Charlie", "Add to group", "New group");
  await page.keyboard.press("Escape");
  // A tab whose group the saved state no longer has leaves the group.
  await until(() => saved.state.groups.some((group) => group.color === "Grey"));
  saved.state.groups = [];
  await page.reload();
  await tab("Charlie").waitFor();
  expect(await order(page)).toEqual(["Bravo", "Charlie"]);
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");
}), 60_000);

test("an emptied group stays in place through reload, offers New session, takes dropped tabs, and goes only when deleted, with its tabs", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const fresh = strip(page).getByRole("button", { name: "New session in Work", exact: true });
  const command = (name: string) => strip(page).getByRole("button", { name: "Edit Work", exact: true }).click().then(() => page.getByRole("button", { name, exact: true }).click());
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"], ["delta", "Delta"]]) await openSaved(page, origin, href(id), name, saved);
  await menu(page, "Bravo", "Add to group", "New group");
  await page.keyboard.type("Work");
  await page.keyboard.press("Escape");
  await menu(page, "Charlie", "Add to group", "Work");
  expect(await order(page)).toEqual(["Alpha", "Work", "Bravo", "Charlie", "Delta"]);
  expect(await fresh.count()).toBe(0);

  // Emptied by a close and a drag out, it stays where it was, and so it reloads.
  await menu(page, "Bravo", "Close");
  await tab("Charlie").dragTo(tab("Alpha"));
  expect(await order(page)).toEqual(["Charlie", "Alpha", "Work", "Delta"]);
  await until(() => saved.state.tabs.length === 4 && !saved.state.tabs.some((item) => item.path === href("charlie") && item.group));
  await page.reload();
  await fresh.waitFor();
  expect(await order(page)).toEqual(["Charlie", "Alpha", "Work", "Delta"]);

  // A tab dropped on it joins it; closing the group's tabs keeps it, and its New session starts one inside it.
  await tab("Delta").dragTo(fresh);
  expect(await tab("Delta").getAttribute("aria-description")).toStartWith("Group Work, ");
  expect(await fresh.count()).toBe(0);
  await command("Close group");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await order(page)).toEqual(["Charlie", "Alpha", "Work"]);
  await fresh.click();
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Charlie", "Alpha", "Work", "New session"]);
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Main");

  // Delete group closes its tabs and removes it.
  await command("Delete group");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await order(page)).toEqual(["Charlie", "Alpha"]);
  await until(() => !saved.state.groups.some((group) => group.name === "Work") && !saved.state.tabs.some((item) => item.path.startsWith("group:")));
}), 60_000);
