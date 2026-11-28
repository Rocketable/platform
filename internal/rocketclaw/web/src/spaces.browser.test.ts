import { expect, onTestFinished, test } from "bun:test";
import { listed, href, withPage, current, names, order, until, openSaved, openFromPalette, menu, chooseSpace, newSpace, shared, sharedNames, focus, menuLines, sharedMenu, strip, type SharedTab, type Change, type Saved } from "./tabs-browser_test";
import type { SearchPage } from "./types";

test("spaces show only their own tabs, return to their last tab, follow opened tabs, and survive reload", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const space = page.locator(`button${current}`);
  const choose = (item: string, role?: string) => chooseSpace(page, item, role);
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name, saved);
  expect(await space.getAttribute("aria-label")).toBe("Main");
  expect(await tab("Charlie").getAttribute("aria-description")).toBe("Space: Main");

  await newSpace(page, "Ops");
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  await page.keyboard.press("Control+Shift+p");
  await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Spaces: New space", exact: true }).click();
  await page.getByRole("textbox", { name: "Space name" }).fill("OPS");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await page.getByRole("dialog", { name: "New space" }).getByRole("alert").waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("dialog", { name: "New space" }).waitFor({ state: "hidden" });
  expect(saved.spaces.map((item) => item.name)).toEqual(["Main", "Ops"]);
  expect(await names(page)).toEqual(["New session"]);

  await choose("Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  await menu(page, "Bravo", "Move to space", "Ops");
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);
  await tab("Alpha").focus();
  await page.keyboard.press("ArrowRight");
  expect(await tab("Charlie").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["New session", "Bravo"]);
  await tab("Bravo").click();
  await choose("Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);

  await openFromPalette(page, "Bravo");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await space.getAttribute("aria-label")).toBe("Ops");
  await until(() => saved.state.space !== "main" && Object.values(saved.state.last).includes(href("bravo")));
  await page.reload();
  await tab("New session").waitFor();
  expect(await space.getAttribute("aria-label")).toBe("Ops");
  expect(await names(page)).toEqual(["New session", "Bravo"]);
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`${origin}/`);

  await menu(page, "Bravo", "Close all");
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["New session"]);
  expect(await space.getAttribute("aria-label")).toBe("Ops");
  await openFromPalette(page, "Bravo");
  await choose("Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session"]);
  expect(await space.getAttribute("aria-label")).toBe("Main");

  await choose("Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Bravo"]);
  await choose("Main", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  await menu(page, "Alpha", "Move to space", "Ops");
  expect(await names(page)).toEqual(["Charlie", "New session"]);
  await choose("Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);

  await choose("Delete space");
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session", "Bravo"]);
  expect(page.url()).toEndWith(href("bravo"));
  await page.getByRole("button", { name: "Main space actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Rename space" }).waitFor();
  expect(await page.getByRole("menuitem").allInnerTexts()).toEqual(["Go to space", "Rename space"]);
  await page.keyboard.press("Escape");

  const settled = () => until(() => saved.spaces.length === 1 && saved.state.tabs.every((tab) => tab.space === "main"));
  await settled();
  saved.state.tabs = saved.state.tabs.map((tab) => ({ ...tab, space: tab.path === href("alpha") ? "" : tab.space }));
  await page.reload();
  await tab("Bravo").waitFor();
  await newSpace(page, "Space 2");
  await choose("Space 2", "menuitemradio");
  expect(await names(page)).toEqual(["New session"]);
  await choose("Delete space");

  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  await settled();
  saved.state.tabs = saved.state.tabs.map((tab) => tab.path === href("bravo") ? { ...tab, space: "gone", group: "kept" } : tab);
  saved.state.groups = [{ id: "kept", name: "Kept", color: "Blue" }];
  await page.reload();
  await tab("Bravo").waitFor();
  expect(await names(page)).toEqual(["Alpha", "Charlie", "New session", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Kept, Space: Main");
  const [button, list] = [await space.boundingBox(), await strip(page).boundingBox()];
  expect(button.y + button.height).toBeLessThanOrEqual(list.y);
}), 60_000);

test("New session offers every agent but sticky: slack ones, and a space shows its own and spaceless cron jobs, ignoring case", () => withPage({}, async (page, origin, _prompts, saved) => {
  const agents = async () => {
    await page.getByRole("button", { name: "Choose agent", exact: true }).click();
    await page.getByRole("menuitemradio", { name: "main", exact: true }).waitFor();
    const listed = await page.getByRole("menuitemradio").allInnerTexts();
    await page.keyboard.press("Escape");
    return listed.map((text: string) => text.trim());
  };
  const runnable = async () => {
    await page.keyboard.press("Control+Shift+p");
    await page.getByRole("button", { name: "Routines: Run", exact: true }).click();
    const palette = page.getByRole("dialog", { name: "Run routine", exact: true });
    await palette.getByRole("button", { name: "daily", exact: true }).waitFor();
    const listed = await palette.getByRole("button").allTextContents();
    await page.keyboard.press("Escape");
    await palette.waitFor({ state: "hidden" });
    return listed;
  };
  const dashboard = async () => {
    await page.getByRole("button", { name: "Preview latest run of daily", exact: true }).waitFor();
    return (await page.getByRole("button", { name: /^Preview latest run of / }).evaluateAll((nodes: HTMLElement[]) => nodes.filter((node) => node.checkVisibility()).map((node) => node.getAttribute("aria-label")))).map((label: string) => label.replace("Preview latest run of ", ""));
  };
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await agents()).toEqual(["main", "opsbot", "channel"]);
  await openSaved(page, origin, "/routines", "Routines", saved);
  expect(await dashboard()).toEqual(["daily", "retired"]);
  await openSaved(page, origin, "/", "New session", saved);
  expect(await agents()).toEqual(["main", "opsbot"]);
  expect(await runnable()).toEqual(["daily"]);
  await newSpace(page, "ops");
  await chooseSpace(page, "ops", "menuitemradio");
  expect(await agents()).toEqual(["main", "opsbot"]);
  expect(await runnable()).toEqual(["daily", "opsreport"]);
  await chooseSpace(page, "Main", "menuitemradio");
  await menu(page, "Routines", "Move to space", "ops");
  await chooseSpace(page, "ops", "menuitemradio");
  await strip(page).getByRole("tab", { name: "Routines", exact: true }).click();
  expect(await dashboard()).toEqual(["daily", "opsreport", "retired"]);
}), 60_000);

test("Search pages are tabs of their own: renamed in place, shared as one page, grouped and moved with their searches", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const query = page.getByRole("main").getByRole("textbox", { name: "Search sessions", exact: true });
  await openSaved(page, origin, "/search/one", "Search", saved);
  await query.fill("deploys");
  await tab("deploys").waitFor();

  await menu(page, "deploys", "Rename search");
  const field = page.getByRole("textbox", { name: "Search page name", exact: true });
  await field.fill("Discarded");
  await field.press("Escape");
  await tab("deploys").waitFor();
  await menu(page, "deploys", "Rename search");
  await field.fill("Deploys");
  await field.press("Enter");

  await menu(page, "Deploys", "Add to Shared");
  const row = (name: string) => shared(page).getByRole("tab", { name, exact: true });
  await row("Deploys").waitFor();
  expect(saved.shared).toMatchObject([{ space: "main", path: "/search/one", search: { name: "Deploys", searches: [{ query: "deploys" }] } }]);
  await row("Deploys").click();
  await query.fill("deploys prod");
  await until(() => saved.shared[0].search?.searches[0].query === "deploys prod");
  await row("Deploys").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Rename search", exact: true }).click();
  await field.fill("Prod deploys");
  await field.press("Enter");
  await row("Prod deploys").waitFor();
  await until(() => saved.shared[0].search?.name === "Prod deploys" && saved.shared[0].search.searches[0].query === "deploys prod");
  saved.shared[0].search = { name: "Prod deploys", searches: [{ id: "x", query: "rollback" }], active: "x" };
  await focus(page);
  await page.waitForFunction(() => (document.querySelector('main input[aria-label="Search sessions"]') as HTMLInputElement | null)?.value === "rollback");
  saved.shared[0].search = { name: "Prod deploys", searches: [{ id: "x", query: "rollback" }, { id: "y", query: "other person" }], active: "x" };
  await page.getByRole("button", { name: "New search", exact: true }).click();
  await query.fill("mine");
  await until(() => JSON.stringify(saved.shared[0].search?.searches.map((item) => item.query)) === JSON.stringify(["rollback", "other person", "mine"]));
  expect(await sharedMenu(page, "Prod deploys")).toEqual(["Rename search", "—", "Close", "Close others", "Close to the right", "Close all", "—", "Copy link", "Remove from Shared", "Duplicate", "—", "Mark as unread", "Park", "Unfollow", "—", "Add to group"]);
  await page.getByRole("menuitem", { name: "Add to group", exact: true }).click();
  await page.getByRole("menuitem", { name: "New group", exact: true }).click();
  await page.getByRole("textbox", { name: "Group name" }).waitFor();
  await page.keyboard.press("Escape");
  await sharedMenu(page, "Prod deploys");
  await page.getByRole("menuitem", { name: "Remove from Shared", exact: true }).click();
  await row("Prod deploys").waitFor({ state: "detached" });
  await tab("Prod deploys").waitFor();
  await until(() => JSON.stringify(saved.state.tabs.find((item) => item.path === "/search/one")?.search?.searches.map((item) => item.query)) === JSON.stringify(["rollback", "other person", "mine", "deploys"]));
  await page.waitForFunction(() => (document.querySelector('main input[aria-label="Search sessions"]') as HTMLInputElement | null)?.value === "mine");

  expect(await tab("Prod deploys").getAttribute("aria-description")).toMatch(/^Group /);
  await newSpace(page, "Ops");
  await chooseSpace(page, "Main", "menuitemradio");
  await menu(page, "Prod deploys", "Move to space", "Ops");
  await until(() => saved.state.tabs.some((item) => item.path === "/search/one" && item.space !== "main" && item.search?.name === "Prod deploys" && item.search.searches[0].query === "rollback"));
  await page.reload();
  await chooseSpace(page, "Ops", "menuitemradio");
  await tab("Prod deploys").click();
  await page.waitForURL((url: URL) => url.pathname === "/search/one");
  expect(await query.inputValue()).toBe("mine");
}), 30_000);

test("a Search page keeps its page through a group moved to another space and that space deleted, and taking someone else's Shared page out keeps it", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const kept = (space?: string) => saved.state.tabs.some((item) => item.path === "/search/g" && (!space || item.space === space) && item.search?.searches[0]?.query === "grouped");
  await openSaved(page, origin, "/search/g", "Search", saved);
  await page.getByRole("main").getByRole("textbox", { name: "Search sessions", exact: true }).fill("grouped");
  await tab("grouped").waitFor();
  await menu(page, "grouped", "Add to group", "New group");
  await page.getByRole("textbox", { name: "Group name" }).waitFor();
  await page.keyboard.type("Work");
  await page.keyboard.press("Escape");
  await newSpace(page, "Ops");
  await chooseSpace(page, "Main", "menuitemradio");
  await strip(page).getByRole("button", { name: "Edit Work", exact: true }).click();
  await page.getByRole("button", { name: 'Move group to "Ops"', exact: true }).click();
  await until(() => kept() && saved.state.tabs.find((item) => item.path === "/search/g")!.space !== "main");
  await chooseSpace(page, "Ops", "menuitemradio");
  await chooseSpace(page, "Delete space");
  await until(() => kept("main"));

  saved.shared.push({ space: "main", path: "/search/team", search: { name: "Team", searches: [{ id: "t", query: "team" }], active: "t" } });
  await focus(page);
  await shared(page).getByRole("tab", { name: "Team", exact: true }).waitFor();
  await sharedMenu(page, "Team");
  await page.getByRole("menuitem", { name: "Remove from Shared", exact: true }).click();
  await tab("Team").waitFor();
  await until(() => saved.state.tabs.some((item) => item.path === "/search/team" && item.space === "main" && item.search?.searches[0]?.query === "team"));
}), 30_000);

for (const width of [1280, 390]) test(`palette search handoffs stay in their opening space at ${width}px`, () => withPage({ viewport: { width, height: 800 } }, async (page, origin, _prompts, saved) => {
  const name = 'Blue "garden"\\shed', scoped = `space:${JSON.stringify(name)} needle`;
  const foreign: SearchPage = { searches: [{ id: "old", query: "needle" }], active: "old" };
  saved.spaces.push({ id: "blue", name }, { id: "empty", name: "Empty" });
  saved.state.space = "blue";
  saved.state.tabs = [{ path: href("alpha"), space: "blue" }, { path: "/search/foreign", space: "main", search: foreign }];
  await page.goto(origin + href("alpha"));
  await until(() => saved.revision > 0);
  const palette = page.getByRole("dialog", { name: "Go to session" });
  const input = palette.getByPlaceholder("Search sessions");
  const open = async () => { await page.keyboard.press("Control+p"); await input.fill("needle"); };
  const at = (query: string) => page.waitForURL((url: URL) => url.pathname.startsWith("/search/") && url.searchParams.get("q") === query);
  await open();
  await input.fill("space:");
  for (const choice of ["Main", name, "Empty"]) await palette.getByRole("button", { name: choice, exact: true }).waitFor();
  for (const fragment of ['space:"Blue \\"gar', 'space:"Blue \\"garden\\"\\\\sh']) {
    await input.fill(fragment);
    await palette.getByRole("button", { name, exact: true }).waitFor({ timeout: 3000 });
  }
  await palette.getByRole("button", { name, exact: true }).click();
  expect(await input.inputValue()).toBe(`space:${JSON.stringify(name)} `);
  await input.fill("needle");
  await palette.getByRole("button", { name: "Search everywhere for 'needle'", exact: true }).waitFor();
  expect((await palette.locator("li > button").allTextContents()).slice(-2)).toEqual(["Search in this space for 'needle'", "Search everywhere for 'needle'"]);
  await palette.getByRole("button", { name: "Search everywhere for 'needle'", exact: true }).click();
  await at("needle");
  const broad = new URL(page.url()).pathname;
  expect(broad).not.toBe("/search/foreign");
  await until(() => saved.state.tabs.some((tab) => tab.path === broad && tab.space === "blue"));
  await open();
  await palette.getByRole("button", { name: "Search everywhere for 'needle'", exact: true }).click();
  await at("needle");
  expect(new URL(page.url()).pathname).toBe(broad);
  saved.shared.push({ space: "blue", path: "/search/team", search: { searches: [{ id: "other", query: "other" }, { id: "local", query: scoped }], active: "other" } });
  await focus(page);
  await open();
  await palette.getByRole("button", { name: /^Search: other/ }).waitFor();
  const held = Promise.withResolvers<void>(), release = Promise.withResolvers<void>();
  let blocked = false;
  await page.route("**/api/ChangeTabs", async (route: any) => {
    const input = route.request().postDataJSON();
    if (input.editShared && !saved.shared.some((tab) => tab.path === "/search/ack")) saved.shared.push({ space: "blue", path: "/search/ack", search: { name: "Acknowledged", searches: [], active: "" } });
    const response = await route.fetch(), reply = await response.json();
    if (!blocked && input.state && reply.shared.some((tab: SharedTab) => tab.path === "/search/team" && tab.search?.active === "other")) {
      blocked = true;
      held.resolve();
      await release.promise;
    }
    await route.fulfill({ response, json: reply });
  });
  await page.keyboard.press("Escape");
  await palette.waitFor({ state: "hidden" });
  if (width === 390) await page.getByRole("button", { name: "Show sidebar", exact: true }).click();
  await strip(page).getByRole("tab", { name: "Alpha", exact: true }).press("Shift+ArrowDown");
  await held.promise;
  if (width === 390) await page.keyboard.press("Escape");
  await open();
  await page.waitForURL((url: URL) => url.pathname === broad);
  await input.press("End");
  await input.press("ArrowUp");
  await input.press("ArrowUp");
  expect(await palette.locator("li > button.bg-accent").textContent()).toBe("Search in this space for 'needle'");
  await input.press("Enter");
  await at(scoped);
  const local = new URL(page.url()).pathname;
  expect(local).toBe("/search/team");
  await until(() => saved.shared.find((tab) => tab.path === local)?.search?.active === "local");
  await open();
  await input.fill("Acknowledged");
  await palette.getByRole("button", { name: /^Search: Acknowledged/ }).waitFor();
  await page.keyboard.press("Escape");
  const queries = await page.evaluateHandle(() => {
    const queries: string[] = [];
    const replace = history.replaceState.bind(history);
    history.replaceState = (...args: Parameters<History["replaceState"]>) => {
      queries.push(new URL(String(args[2]), location.origin).searchParams.get("q") ?? "");
      replace(...args);
    };
    return queries;
  });
  const resumed = page.waitForResponse((response: any) => response.url().endsWith("/api/ChangeTabs") && !!response.request().postDataJSON().state);
  const nextSave = page.waitForRequest((request: any) => request.url().endsWith("/api/ChangeTabs") && !!request.postDataJSON().state);
  release.resolve();
  await (await resumed).finished();
  await nextSave;
  await open();
  await input.fill("Acknowledged");
  await palette.getByRole("button", { name: /^Search: Acknowledged/ }).waitFor();
  await page.keyboard.press("Escape");
  expect(await queries.jsonValue()).not.toContain("other");
  await page.unroute("**/api/ChangeTabs");
  await page.reload();
  await page.getByRole("main").getByRole("textbox", { name: "Search sessions", exact: true }).waitFor();
  await at(scoped);
  await open();
  await palette.getByRole("button", { name: "Search in this space for 'needle'", exact: true }).click();
  await at(scoped);
  expect(new URL(page.url()).pathname).toBe(local);
  await open();
  await palette.getByRole("button", { name: /^Search: needle\s*Main$/ }).click();
  await page.waitForURL((url: URL) => url.pathname === "/search/foreign");
  await until(() => saved.state.space === "main");
  await open();
  await input.fill("unmatched");
  await palette.getByText("No matches", { exact: true }).waitFor();
  expect(await palette.locator("li > button.bg-accent").textContent()).toBe("Search everywhere for 'unmatched'");
  await input.press("ArrowUp");
  expect(await palette.locator("li > button.bg-accent").textContent()).toBe("Search in this space for 'unmatched'");
  await input.press("ArrowDown");
  await input.press("Enter");
  await at("unmatched");
}), 30_000);

test("a group lives in one space: menus offer only this space's groups, groups move whole, and New tab in group brings New session", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const header = strip(page).getByRole("button", { name: "Work", exact: true }), edit = strip(page).getByRole("button", { name: "Edit Work", exact: true });
  const editor = (item: string) => edit.click().then(() => page.getByRole("button", { name: item, exact: true }).click());
  const green = page.getByRole("radio", { name: "Green" });
  for (const [link, name] of [["/", "New session"], [href("alpha"), "Alpha"], [href("bravo"), "Bravo"], [href("charlie"), "Charlie"]]) await openSaved(page, origin, link, name, saved);
  await menu(page, "Alpha", "Add to group", "New group");
  await page.keyboard.type("Work");
  await green.check();
  await page.keyboard.press("Escape");
  await menu(page, "Bravo", "Add to group", "Work");

  await newSpace(page, "Ops");
  await chooseSpace(page, "Ops", "menuitemradio");
  await tab("New session").click({ button: "right" });
  await page.getByRole("menuitem").first().waitFor();
  expect(await menuLines(page)).toEqual(["Close", "Close others", "Close to the right", "Close all", "—", "Copy link", "—", "Add to group", "Move to space"]);
  await page.getByRole("menuitem", { name: "Move to space", exact: true }).click();
  const spaces = page.getByRole("menu").nth(1);
  await spaces.waitFor();
  expect(await spaces.getByRole("menuitem").allInnerTexts()).toEqual(["Main"]);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");

  await chooseSpace(page, "Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await order(page)).toEqual(["Work", "Alpha", "Bravo", "Charlie"]);
  await editor('Move group to "Ops"');
  expect(await order(page)).toEqual(["Charlie"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["New session", "Work", "Alpha", "Bravo"]);
  await edit.click();
  expect(await green.isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await menu(page, "Alpha", "Move to space", "Main");
  expect(await order(page)).toEqual(["New session", "Work", "Bravo"]);
  await header.click();
  await chooseSpace(page, "Main", "menuitemradio");
  await page.waitForURL(`**${href("charlie")}`);
  expect(await order(page)).toEqual(["Alpha", "Charlie"]);
  expect(await tab("Alpha").getAttribute("aria-description")).toBe("Space: Main");

  await until(() => saved.state.groups.some((group) => group.collapsed));
  await page.reload();
  await tab("Alpha").waitFor();
  expect(await order(page)).toEqual(["Alpha", "Charlie"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["New session", "Work"]);
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  await edit.click();
  expect(await green.isChecked()).toBe(true);
  await page.keyboard.press("Escape");
  await header.click();
  expect(await order(page)).toEqual(["New session", "Work", "Bravo"]);
  expect(await tab("Bravo").getAttribute("aria-description")).toBe("Group Work, Space: Ops");

  await chooseSpace(page, "Main", "menuitemradio");
  await page.getByRole("button", { name: "New session", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["New session", "Alpha", "Charlie"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  await editor("New tab in group");
  await page.waitForURL(`${origin}/`);
  expect(await order(page)).toEqual(["Work", "Bravo", "New session"]);
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Ops");

  await header.click();
  await chooseSpace(page, "Delete space");
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  expect(await order(page)).toEqual(["Alpha", "Work", "New session", "Charlie"]);
  expect(await header.getAttribute("aria-expanded")).toBe("false");
  expect(await tab("New session").getAttribute("aria-description")).toBe("Group Work, Space: Main");
}), 60_000);

test("a first message to an agent with a space creates the space and takes the session there", () => withPage({}, async (page, origin) => {
  await page.goto(`${origin}/`);
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  await page.getByRole("button", { name: "Choose agent", exact: true }).click();
  await page.getByRole("menuitemradio", { name: "opsbot", exact: true }).click();
  await page.getByPlaceholder("Message a new session").fill("hello");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL(`**${href("fresh")}`);
  await page.locator(`button${current}[aria-label="OPS"]`).waitFor();
  expect(await names(page)).toEqual(["Ops chat"]);
}), 30_000);

test("a session opens in the space it was born in, and a moved one can be restored there", () => withPage({}, async (page, origin, _prompts, saved) => {
  saved.spaces.push({ id: "ops", name: "OPS" });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openFromPalette(page, "Echo");
  await page.waitForURL(`**${href("echo")}`);
  await page.locator(`button${current}[aria-label="OPS"]`).waitFor();
  expect(await names(page)).toEqual(["Echo"]);
  await menu(page, "Echo", "Move to space", "Main");
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  expect(await names(page)).toEqual(["Alpha", "Echo"]);
  await menu(page, "Echo", 'Restore to "OPS"');
  await page.locator(`button${current}[aria-label="OPS"]`).waitFor();
  await strip(page).getByRole("tab", { name: "Echo", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close", exact: true }).waitFor();
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain('Restore to "OPS"');
  await page.keyboard.press("Escape");
}), 30_000);

test("Recently closed lists closed tabs newest first and reopens one where it was, as Cmd/Ctrl+Alt+T reopens the last, pressed twice the last two", () => withPage({}, async (page, origin, _prompts, saved) => {
  saved.spaces.push({ id: "ops", name: "OPS" });
  saved.closed = [{ path: href("echo"), space: "main" }];
  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"], ["charlie", "Charlie"]]) await openSaved(page, origin, href(id), name, saved);
  await menu(page, "Bravo", "Close");
  await menu(page, "Charlie", "Close");
  await page.keyboard.press("Control+Shift+p");
  const palette = page.getByRole("dialog", { name: "Run command", exact: true });
  await palette.getByPlaceholder("Type a command", { exact: true }).fill("recently closed");
  await palette.getByPlaceholder("Type a command", { exact: true }).press("Enter");
  await page.waitForURL("**/closed");
  const list = page.getByRole("list", { name: "Recently closed tabs" }), rows = () => list.getByRole("button").allTextContents();
  await list.getByRole("button").filter({ hasText: "Charlie" }).waitFor();
  expect(await rows()).toEqual(["CharlieMain", "BravoMain", "EchoMain"]);

  await list.getByRole("button").filter({ hasText: "Echo" }).click();
  await page.waitForURL(`**${href("echo")}`);
  await strip(page).getByRole("tab", { name: "Recently closed", exact: true }).click();
  await list.getByRole("button").filter({ hasText: "Echo" }).waitFor({ state: "detached" });
  expect(await rows()).toEqual(["CharlieMain", "BravoMain"]);

  await page.route("**/api/ChangeTabs", async (route: any) => { await Bun.sleep(500); await route.continue(); });
  await page.keyboard.press("Control+Alt+t");
  await page.waitForURL(`**${href("charlie")}`);
  await page.keyboard.press("Control+Alt+t");
  await page.waitForURL(`**${href("bravo")}`);
  await until(() => saved.closed.length === 0);
  expect(await names(page)).toEqual(["Alpha", "Recently closed", "Charlie", "Bravo", "Echo"]);
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  expect(saved.state.tabs.find((tab) => tab.path === href("echo"))?.space).toBe("main");
}), 30_000);

test("tabs follow a person across windows: a window takes in changes on focus but stays on its own tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  const other = await page.context().browser().newPage();
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  await openSaved(other, origin, href("charlie"), "Charlie", saved);
  expect(await names(other)).toEqual(["Alpha", "Bravo", "Charlie"]);

  await menu(other, "Bravo", "Close");
  await until(() => !saved.state.tabs.some((tab) => tab.path === href("bravo")));
  await page.evaluate(() => { dispatchEvent(new Event("visibilitychange")); });
  await page.waitForURL(`**${href("charlie")}`);
  expect(await names(page)).toEqual(["Alpha", "Charlie"]);

  await openSaved(other, origin, href("delta"), "Delta", saved);
  await page.evaluate(() => { dispatchEvent(new Event("focus")); });
  await strip(page).getByRole("tab", { name: "Delta", exact: true }).waitFor();
  expect(page.url()).toEndWith(href("charlie"));
}), 60_000);

test("a change made from tabs older than another window's save keeps both", () => withPage({}, async (page, origin, _prompts, saved) => {
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  [saved.state.tabs, saved.revision] = [[...saved.state.tabs, { path: href("charlie"), space: "main" }], saved.revision + 1];
  await openFromPalette(page, "Bravo");
  await strip(page).getByRole("tab", { name: "Charlie", exact: true }).waitFor();
  await until(() => ["alpha", "bravo", "charlie"].every((id) => saved.state.tabs.some((tab) => tab.path === href(id))));
  expect(await names(page)).toEqual(["Alpha", "Bravo", "Charlie"]);
  expect(saved.refused).toBe(1);
}), 30_000);

test("a session can be renamed in Shared without opening a personal tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  const path = href("bravo");
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.shared = [{ space: "main", path }, { space: "ops", path }];
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await sharedMenu(page, "Bravo")).toContain("Rename session");
  await page.getByRole("menuitem", { name: "Rename session", exact: true }).click();
  const input = shared(page).getByRole("textbox", { name: "Session name" });
  await input.waitFor();
  expect(await page.getByRole("textbox", { name: "Session name" }).count()).toBe(1);
  await input.fill("Renamed session");
  await input.press("Enter");
  for (const space of ["Main", "Ops"]) await shared(page, space).getByRole("tab", { name: "Renamed session", exact: true }).waitFor();
  expect(await names(page)).toEqual(["Alpha"]);
  expect(saved.shared).toEqual([{ space: "main", path }, { space: "ops", path }]);
  expect(new URL(page.url()).pathname).toBe(href("alpha"));
}), 30_000);

test("Shared hides while empty, takes tabs from the tab menu, highlights both copies, and changes only through its menu", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true }), row = (name: string) => shared(page).getByRole("tab", { name, exact: true });
  const header = page.getByRole("button", { name: "Shared in Main", exact: true }), commands = page.getByRole("dialog", { name: "Run command" });
  await openSaved(page, origin, "/", "New session", saved);
  expect(await header.count()).toBe(0);
  await tab("New session").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close", exact: true }).waitFor();
  expect(await page.getByRole("menuitem").allInnerTexts()).not.toContain("Add to Shared");
  await page.keyboard.press("Escape");
  await page.getByRole("menu").waitFor({ state: "hidden" });
  await page.keyboard.press("Control+Shift+p");
  await commands.getByRole("button", { name: "Tabs: Close", exact: true }).waitFor();
  expect(await commands.getByRole("button", { name: "Tabs: Add to Shared" }).count()).toBe(0);
  await page.waitForFunction(() => document.activeElement?.getAttribute("placeholder") === "Type a command");
  await page.keyboard.press("Escape");
  await commands.waitFor({ state: "hidden" });

  for (const [id, name] of [["alpha", "Alpha"], ["bravo", "Bravo"]]) await openSaved(page, origin, href(id), name, saved);
  await menu(page, "Alpha", "Add to Shared");
  await row("Alpha").waitFor();
  expect(saved.shared).toEqual([{ space: "main", path: href("alpha") }]);
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo"]);
  const [list, space] = [await shared(page).boundingBox(), await strip(page).boundingBox()];
  expect([list.y + list.height <= space.y, await header.getAttribute("aria-expanded")]).toEqual([true, "true"]);
  expect(await row("Alpha").getAttribute("aria-description")).toBe("Shared, Space: Main");
  await row("Alpha").click();
  await page.waitForURL(`**${href("alpha")}`);
  expect([await row("Alpha").getAttribute("aria-selected"), await tab("Alpha").getAttribute("aria-selected")]).toEqual(["true", "true"]);

  expect(await shared(page).getByRole("button", { name: "Close Alpha" }).count()).toBe(0);
  const lines = async (open: () => Promise<void>) => {
    await open();
    await page.getByRole("menuitem").first().waitFor();
    const got = await menuLines(page);
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
    return got;
  };
  const fromShared = await lines(() => row("Alpha").click({ button: "right" }));
  expect(fromShared).toEqual(["Pull requests and issues", "Hosted Pages", "—", "Rename session", "Regenerate title", "—", "Close", "Close others", "Close to the right", "Close all", "—", "Copy link", "Remove from Shared", "—", "Mark as unread", "Park", "Auto-park when I send", "Unfollow", "—", "Add to group", "—", "Delete session"]);
  expect([await lines(() => tab("Alpha").click({ button: "right" })), await lines(() => page.getByRole("banner").getByRole("button", { name: "Page actions", exact: true }).click())]).toEqual([fromShared, fromShared]);
  await row("Alpha").click({ button: "middle" });
  await tab("Bravo").dragTo(row("Alpha"));
  expect(await names(page)).toEqual(["New session", "Alpha", "Bravo"]);
  expect(await sharedNames(page)).toEqual(["Alpha"]);
  for (const [name, item] of [["Alpha", "Remove from Shared"], ["Bravo", "Add to Shared"]]) {
    await tab(name).click({ button: "right" });
    await page.getByRole("menuitem", { name: item, exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
  }

  await menu(page, "Bravo", "Add to Shared");
  await row("Bravo").waitFor();
  await menu(page, "Bravo", "Close");
  await row("Bravo").click();
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["New session", "Alpha"]);
  await until(() => saved.state.last.main === href("bravo") && saved.state.tabs.length === 2);
  await page.reload();
  await tab("Alpha").waitFor();
  expect(await names(page)).toEqual(["New session", "Alpha"]);
  expect(await row("Bravo").getAttribute("aria-selected")).toBe("true");
  await shared(page).getByRole("button", { name: "Tab actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Pull requests and issues", exact: true }).click();
  await page.getByRole("complementary", { name: "Pull requests and issues", exact: true }).getByText("No pull requests or issues found for this thread.", { exact: true }).waitFor();
  expect(new URL(page.url()).pathname).toBe(href("bravo"));
  expect(await names(page)).toEqual(["New session", "Alpha"]);
  await page.getByRole("button", { name: "Close pull requests and issues", exact: true }).click();
  await page.keyboard.press("Control+Shift+p");
  await commands.getByRole("button", { name: "Tabs: Remove from Shared", exact: true }).waitFor();
  expect(await commands.getByRole("button", { name: "Tabs: Close", exact: true }).count()).toBe(0);
  await page.keyboard.press("Escape");
  await commands.waitFor({ state: "hidden" });

  await sharedMenu(page, "Bravo");
  await page.getByRole("menuitem", { name: "Remove from Shared", exact: true }).click();
  await page.waitForURL(`${origin}/`);
  expect(await sharedNames(page)).toEqual(["Alpha"]);
  await sharedMenu(page, "Alpha");
  await page.getByRole("menuitem", { name: "Remove from Shared", exact: true }).click();
  await header.waitFor({ state: "detached" });
  expect(await names(page)).toEqual(["New session", "Alpha"]);
  expect(saved.shared).toEqual([]);

  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  await menu(page, "Bravo", "Add to Shared");
  await row("Bravo").waitFor();
  await menu(page, "Bravo", "Close");
  for (const [key, url] of [["Control+BracketLeft", `**${href("alpha")}`], ["Control+BracketRight", `${origin}/`]]) {
    await row("Bravo").click();
    await page.waitForURL(`**${href("bravo")}`);
    await page.keyboard.press(key);
    await page.waitForURL(url);
  }
  await row("Bravo").click();
  await page.waitForURL(`**${href("bravo")}`);
  await strip(page).getByRole("button", { name: "Close Alpha" }).click();
  expect(await names(page)).toEqual(["New session"]);
  expect(await tab("New session").evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
}), 60_000);

test("other people's Shared changes arrive on focus, and a window on a Shared tab stays on it", () => withPage({}, async (page, origin, _prompts, saved) => {
  const row = (name: string) => shared(page).getByRole("tab", { name, exact: true }), space = page.locator(`button${current}`);
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state.tabs = [{ path: href("charlie"), space: "ops" }];
  saved.shared = [{ space: "main", path: href("charlie") }];
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await row("Charlie").click();
  await page.waitForURL(`**${href("charlie")}`);
  expect([await space.getAttribute("aria-label"), await row("Charlie").getAttribute("aria-selected"), await names(page)]).toEqual(["Main", "true", ["Alpha"]]);
  await until(() => saved.state.last.main === href("charlie"));
  [saved.state.tabs, saved.revision] = [[...saved.state.tabs, { path: href("delta"), space: "main" }], saved.revision + 1];
  await focus(page);
  await strip(page).getByRole("tab", { name: "Delta", exact: true }).waitFor();
  expect([page.url().endsWith(href("charlie")), await space.getAttribute("aria-label")]).toEqual([true, "Main"]);
  saved.shared.push({ space: "main", path: href("bravo") });
  await focus(page);
  await row("Bravo").waitFor();
  expect(page.url()).toEndWith(href("charlie"));
  saved.shared = saved.shared.filter((tab) => tab.path !== href("charlie"));
  await focus(page);
  await page.waitForURL(`**${href("alpha")}`);
  expect(await space.getAttribute("aria-label")).toBe("Main");
}), 30_000);

test("a space another person deletes is left as if this window deleted it", () => withPage({}, async (page, origin, _prompts, saved) => {
  const row = (name: string, space?: string) => shared(page, space).getByRole("tab", { name, exact: true });
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, tabs: [{ path: href("alpha"), space: "main" }, { path: href("bravo"), space: "ops" }], space: "ops" };
  saved.shared = [{ space: "ops", path: href("charlie") }];
  await page.goto(`${origin}${href("charlie")}`);
  await page.locator(`button${current}[aria-label="Ops"]`).waitFor();
  expect([await row("Charlie", "Ops").getAttribute("aria-selected"), await names(page)]).toEqual(["true", ["Bravo"]]);
  saved.spaces = saved.spaces.filter((item) => item.id !== "ops");
  saved.shared = [{ space: "main", path: href("charlie") }];
  await focus(page);
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  expect([page.url().endsWith(href("charlie")), await row("Charlie").getAttribute("aria-selected"), await names(page)]).toEqual([true, "true", ["Alpha", "Bravo"]]);
  expect(await page.locator("[data-tab-lists] button[aria-description][aria-expanded]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["Main"]);
}), 30_000);

test("each space's card lists its own Shared tabs under a Shared pill that folds them for this person on every page, a space returns to its last Shared tab, and a deleted space's Shared tabs join the first space's", () => withPage({}, async (page, origin, _prompts, saved) => {
  const main = page.getByRole("button", { name: "Shared in Main", exact: true }), opsNames = () => shared(page, "Ops").getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.shared = [{ space: "main", path: href("alpha") }, { space: "ops", path: href("delta") }, { space: "main", path: href("bravo") }, { space: "ops", path: href("alpha") }];
  await openSaved(page, origin, href("charlie"), "Charlie", saved);
  await shared(page, "Ops").getByRole("tab", { name: "Delta", exact: true }).waitFor();
  expect([await sharedNames(page), await opsNames()]).toEqual([["Alpha", "Bravo"], ["Delta", "Alpha"]]);
  await main.click();
  await main.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect([await sharedNames(page), await opsNames()]).toEqual([[], ["Delta", "Alpha"]]);
  await until(() => saved.layout?.folded?.["#shared/main"] === true);
  const foldSaved = (value: boolean) => page.waitForResponse((response: any) => response.ok() && response.url().endsWith("/api/ChangeTabs") && response.request().postDataJSON().layout?.folded?.["#shared/main"] === value);
  await Promise.all([foldSaved(false), main.click()]);
  await main.and(page.locator('[aria-expanded="true"]')).waitFor();
  await shared(page).getByRole("tab", { name: "Alpha", exact: true }).click();
  await page.waitForURL(`**${href("alpha")}`);
  await Promise.all([foldSaved(true), main.click()]);
  await main.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect(await sharedNames(page)).toEqual(["Alpha"]);
  await page.reload();
  await main.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect(await sharedNames(page)).toEqual(["Alpha"]);
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  expect([await main.getAttribute("aria-expanded"), await sharedNames(page), await opsNames()]).toEqual(["false", [], ["Delta", "Alpha"]]);
  await chooseSpace(page, "Main", "menuitemradio");
  await page.waitForURL(`**${href("alpha")}`);
  expect(await names(page)).toEqual(["Charlie"]);
  await main.click();
  await main.and(page.locator('[aria-expanded="true"]')).waitFor();
  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`${origin}/`);
  await chooseSpace(page, "Delete space");
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  await shared(page).getByRole("tab", { name: "Delta", exact: true }).waitFor();
  expect(await sharedNames(page)).toEqual(["Alpha", "Delta", "Bravo"]);
}), 60_000);

test("Shared shows in the narrow screen's tab drawer once it has tabs", () => withPage({ viewport: { width: 375, height: 700 }, hasTouch: true }, async (page, origin, _prompts, saved) => {
  const toggle = page.getByRole("button", { name: "Show sidebar", exact: true }), drawer = page.locator("#tab-sidebar");
  await page.goto(`${origin}${href("bravo")}`);
  await until(() => saved.state.tabs.some((tab) => tab.path === href("bravo")));
  await toggle.tap();
  await strip(page).getByRole("tab", { name: "Bravo", exact: true }).waitFor();
  expect(await drawer.getByRole("button", { name: "Shared in Main", exact: true }).count()).toBe(0);
  await page.keyboard.press("Escape");
  await strip(page).waitFor({ state: "detached" });
  saved.shared.push({ space: "main", path: href("alpha") });
  await focus(page);
  await toggle.tap();
  await drawer.getByRole("tablist", { name: "Main Shared tabs" }).getByRole("tab", { name: "Alpha", exact: true }).tap();
  await page.waitForURL(`**${href("alpha")}`);
  await strip(page).waitFor({ state: "detached" });
}), 30_000);

test("A mouse click opens a tab in the narrow screen's tab drawer", () => withPage({ viewport: { width: 375, height: 700 } }, async (page, origin, _prompts, saved) => {
  saved.state = { ...saved.state, tabs: [{ path: href("alpha"), space: "main" }, { path: href("bravo"), space: "main" }], space: "main" };
  await page.goto(`${origin}${href("alpha")}`);
  await page.getByRole("button", { name: "Show sidebar", exact: true }).click();
  await strip(page).getByRole("tab", { name: "Bravo", exact: true }).click();
  await page.waitForURL(`**${href("bravo")}`);
  await strip(page).waitFor({ state: "detached" });
}), 30_000);

test("Shared rows step, arrow, and reorder like this person's tabs, and Escape leaves a Shared-only page open", () => withPage({}, async (page, origin, _prompts, saved) => {
  const row = (name: string) => shared(page).getByRole("tab", { name, exact: true }), tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const focused = (locator: any) => locator.evaluate((node: HTMLElement) => document.activeElement === node);
  saved.state = { ...saved.state, tabs: [{ path: href("alpha"), space: "main" }, { path: href("bravo"), space: "main" }], space: "main" };
  saved.shared = [{ space: "main", path: "/shortcuts" }, { space: "main", path: href("bravo") }];
  await page.goto(`${origin}${href("alpha")}`);
  await row("Bravo").waitFor();
  for (const link of ["/shortcuts", href("bravo"), href("alpha"), "/shortcuts"]) {
    await page.keyboard.press("Control+BracketRight");
    await page.waitForURL(`${origin}${link}`);
  }
  expect(await page.getByRole("button", { name: "Close", exact: true }).count()).toBe(0);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Control+BracketRight");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await names(page)).toEqual(["Alpha", "Bravo"]);
  await row("Bravo").focus();
  await page.keyboard.press("ArrowDown");
  expect(await focused(tab("Alpha"))).toBe(true);
  await page.keyboard.press("ArrowUp");
  expect(await focused(row("Bravo"))).toBe(true);
  const order = (...paths: string[]) => until(() => JSON.stringify(saved.state.sharedOrder) === JSON.stringify(paths));
  await row("Shortcuts").focus();
  await page.keyboard.press("Shift+ArrowDown");
  await shared(page).getByRole("tab").last().filter({ hasText: "Shortcuts" }).waitFor();
  expect([await sharedNames(page), await focused(row("Shortcuts"))]).toEqual([["Bravo", "Shortcuts"], true]);
  await order(href("bravo"), "/shortcuts");
  expect(saved.shared.map((item) => item.path)).toEqual(["/shortcuts", href("bravo")]);
  await row("Shortcuts").dragTo(tab("Alpha"));
  expect([await names(page), await sharedNames(page)]).toEqual([["Alpha", "Bravo"], ["Bravo", "Shortcuts"]]);
  saved.shared.push({ space: "main", path: href("charlie") });
  await page.reload();
  await row("Charlie").waitFor();
  expect(await sharedNames(page)).toEqual(["Bravo", "Shortcuts", "Charlie"]);
  await row("Charlie").dragTo(row("Bravo"));
  await order(href("charlie"), href("bravo"), "/shortcuts");
  expect(await sharedNames(page)).toEqual(["Charlie", "Bravo", "Shortcuts"]);
}), 30_000);

test("Park moves tabs of any kind to Parked, which lists every space's, stepping skips it and bulk closes take it, Unpark returns tabs to their group and place, and a closed tab comes back parked only while its park has a time", () => withPage({ timezoneId: "Asia/Kolkata" }, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true }), parked = page.getByRole("tablist", { name: "Parked tabs" }), row = (name: string) => parked.getByRole("tab", { name, exact: true });
  const show = (view: string) => page.getByRole("group", { name: "Sidebar view" }).getByRole("button", { name: view, exact: true }).click(), sent = (count: number) => until(() => saved.sent.length === count);
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, space: "ops", groups: [{ id: "work", name: "Work", color: "Blue" }], tabs: [...[href("alpha"), href("bravo"), href("charlie"), href("delta"), "/routines"].map((path, i) => ({ path, space: "ops", group: i === 1 || i === 2 ? "work" : undefined })), { path: "group:work", space: "ops", group: "work" }, { path: href("hotel"), space: "main" }] };
  saved.attention = [{ path: href("hotel"), parked: true }];
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Alpha").waitFor();
  expect(await parked.getByRole("tab").count()).toBe(0);
  await show("Parked");
  expect(await row("Hotel").getAttribute("aria-description")).toBe("Space: Main, Forked session, Parked");

  await show("Spaces");
  await menu(page, "Bravo", "Park", "Until I unpark");
  await tab("Bravo").waitFor({ state: "detached" });
  await sent(1);
  expect(saved.sent).toEqual([{ paths: [href("bravo")], action: "PARK" }]);
  expect(await order(page)).toEqual(["Alpha", "Work", "Charlie", "Delta", "Routines"]);
  await menu(page, "Routines", "Park", "Tomorrow 9:00");
  await sent(2);
  const nine = await page.evaluate(() => { const at = new Date(); at.setDate(at.getDate() + 1); at.setHours(9, 0, 0, 0); return at.toISOString(); });
  expect([saved.sent[1], nine.slice(10)]).toEqual([{ paths: ["/routines"], action: "PARK", until: nine }, "T03:30:00.000Z"]);
  await tab("Charlie").focus();
  await page.keyboard.press("Shift+F10");
  await page.getByRole("menuitem", { name: "Park", exact: true }).press("ArrowRight");
  await page.getByRole("menuitem", { name: "Until I unpark", exact: true }).press("Enter");
  await tab("Charlie").waitFor({ state: "detached" });
  await page.waitForFunction(() => document.activeElement === document.querySelector('[aria-label="Ops tabs"] [role="tab"][aria-label="Alpha"]'));
  expect(await order(page)).toEqual(["Alpha", "Work", "Delta"]);
  await show("Parked");
  await parked.locator('[role="tab"][aria-label="Routines"][aria-description^="Space: Ops, Parked until "]').waitFor();
  await row("Charlie").waitFor();

  for (const id of ["delta", "alpha"]) {
    await page.keyboard.press("Control+BracketRight");
    await page.waitForURL(`**${href(id)}`);
  }
  for (const name of ["Charlie", "Bravo"]) {
    await row(name).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Unpark", exact: true }).click();
    await row(name).waitFor({ state: "detached" });
  }
  await sent(5);
  expect(saved.sent.slice(3)).toEqual([{ paths: [href("charlie")], action: "UNPARK" }, { paths: [href("bravo")], action: "UNPARK" }]);
  await show("Spaces");
  expect(await order(page)).toEqual(["Alpha", "Work", "Bravo", "Charlie", "Delta"]);

  await show("Parked");
  for (const [name, path] of [["Routines", "/routines"], ["Hotel", href("hotel")]]) {
    await row(name).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Close", exact: true }).click();
    await row(name).waitFor({ state: "detached" });
    await until(() => saved.closed[0]?.path === path);
  }
  await page.getByText("No parked tabs.").waitFor();
  expect(saved.attention.find((item) => item.path === href("hotel"))?.parked).toBe(false);
  for (const polled = saved.polls + 2; saved.polls < polled;) await Bun.sleep(20);
  await page.keyboard.press("Control+Alt+t");
  await page.waitForURL(`**${href("hotel")}`);
  await show("Spaces");
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
  await tab("Hotel").waitFor();
  await page.keyboard.press("Control+Alt+t");
  await page.waitForURL("**/routines");
  await tab("Routines").waitFor();
  expect(await strip(page).locator('[data-tab="/routines"]').evaluate((node: HTMLElement) => getComputedStyle(node).opacity)).toBe("0.6");
  await show("Parked");
  await row("Routines").waitFor();

  await show("Spaces");
  await menu(page, "Alpha", "Close others");
  await until(() => saved.state.tabs.map((item) => item.path).join() === [href("alpha"), href("hotel"), "group:work"].join());
  await show("Parked");
  await page.getByText("No parked tabs.").waitFor();
}), 60_000);

test("a parked tab with something new is the one the Parked count counts, marked in its row without turning bold", () => withPage({}, async (page, origin, _prompts, saved) => {
  const views = page.getByRole("group", { name: "Sidebar view" }), parked = page.getByRole("tablist", { name: "Parked tabs" });
  const row = (id: string) => parked.locator(`[data-tab="${href(id)}"]`);
  saved.state = { ...saved.state, space: "main", tabs: ["alpha", "bravo", "charlie"].map((id) => ({ path: href(id), space: "main" })) };
  saved.attention = [{ path: href("bravo"), parked: true, unreadAt: "2026-10-22T10:00:00Z" }, { path: href("charlie"), parked: true }];
  await page.goto(`${origin}${href("alpha")}`);
  await strip(page).getByRole("tab", { name: "Alpha", exact: true }).waitFor();
  await views.getByRole("button", { name: "Parked", exact: true }).click();
  await row("charlie").waitFor();
  expect(await views.getByRole("button", { name: "Parked", exact: true }).locator("span[aria-hidden]").innerText()).toBe("1");
  expect(await row("bravo").getByRole("tab").getAttribute("aria-description")).toBe("Space: Main, Unread, Parked");
  expect(await row("charlie").getByRole("tab").getAttribute("aria-description")).toBe("Space: Main, Parked");
  expect([await row("bravo").locator("[data-parked-unread]").isVisible(), await row("charlie").locator("[data-parked-unread]").count()]).toEqual([true, 0]);
  expect([await row("bravo").getAttribute("data-unread"), await row("charlie").getAttribute("data-unread")]).toEqual(["false", "false"]);
}));

test("Park until it is done is offered for a session, not a page, and parks the tab, which Parked says waits for its turn", () => withPage({}, async (page, origin, _prompts, saved) => {
  const parked = page.getByRole("tablist", { name: "Parked tabs" });
  const parkMenu = async (name: string) => {
    await strip(page).getByRole("tab", { name, exact: true }).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Park", exact: true }).hover();
    const submenu = page.getByRole("menu").nth(1);
    await submenu.getByRole("menuitem", { name: "Until I unpark", exact: true }).waitFor();
    const items = await submenu.locator('[role="menuitem"], [role="separator"]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("role") === "separator" ? "—" : node.innerText));
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await page.getByRole("menu").first().waitFor({ state: "hidden" });
    return items;
  };
  await openSaved(page, origin, "/routines", "Routines", saved);
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  const times = ["1 hour", "4 hours", "Tomorrow 9:00", "Next Monday 9:00", "Pick a time…"];
  expect(await parkMenu("Routines")).toEqual([...times, "—", "Until I unpark"]);
  expect(await parkMenu("Alpha")).toEqual(["Until it is done", "—", ...times, "—", "Until I unpark"]);

  await menu(page, "Alpha", "Park", "Until it is done");
  await until(() => saved.sent.length === 1);
  expect(saved.sent[0]).toEqual({ paths: [href("alpha")], action: "PARK_UNTIL_DONE" });
  await strip(page).locator(`[data-tab="${href("alpha")}"]`).and(page.locator(".opacity-60")).waitFor();
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  await parked.locator('[role="tab"][aria-label="Alpha"][aria-description$="Parked until it is done"]').waitFor();
  expect(await parked.getByRole("tab", { name: "Alpha", exact: true }).locator("svg").getAttribute("class")).toContain("lucide-message-square");

  await page.getByRole("button", { name: "Spaces", exact: true }).click();
  await strip(page).getByRole("tab", { name: "Routines", exact: true }).click();
  await page.waitForURL(`${origin}/routines`);
  await page.keyboard.press("Control+Shift+s");
  await openSaved(page, origin, href("bravo"), "Bravo", saved);
  await page.keyboard.press("Control+Shift+s");
  await until(() => saved.sent.length === 2);
  expect(saved.sent[1]).toEqual({ paths: [href("bravo")], action: "PARK_UNTIL_DONE" });
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  await parked.locator('[role="tab"][aria-label="Bravo"][aria-description$="Parked until it is done"]').waitFor();
}), 30_000);

test("Auto-park is offered for a session's tab, not a page's, and turns on and off for that session; Config turns it on for every session", () => withPage({}, async (page, origin, _prompts, saved) => {
  const items = async (name: string) => {
    await strip(page).getByRole("tab", { name, exact: true }).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Close", exact: true }).waitFor();
    const listed = await page.getByRole("menuitem").allInnerTexts();
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
    return listed;
  };
  await openSaved(page, origin, "/routines", "Routines", saved);
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  expect(await items("Routines")).not.toContain("Auto-park when I send");

  await menu(page, "Alpha", "Auto-park when I send");
  await until(() => saved.sent.length === 1);
  expect(saved.sent[0]).toEqual({ paths: [href("alpha")], action: "AUTO_PARK" });
  while (!(await items("Alpha")).includes("Stop auto-parking")) await Bun.sleep(50);
  await menu(page, "Alpha", "Stop auto-parking");
  await until(() => saved.sent.length === 2);
  expect(saved.sent[1]).toEqual({ paths: [href("alpha")], action: "STOP_AUTO_PARK" });

  await page.goto(`${origin}/config`);
  const toggle = page.getByRole("switch", { name: "Auto-park sessions" });
  await toggle.and(page.locator('[aria-checked="false"]:not([data-disabled])')).waitFor();
  await toggle.click();
  await until(() => saved.autoPark === true);
  await toggle.and(page.locator('[aria-checked="true"]')).waitFor();
}), 30_000);

test("the palette offers the tab in view the same attention items, without Mute or Snooze, and Park until a picked time starts at the next whole hour, which the parked tab states", () => withPage({}, async (page, origin, _prompts, saved) => {
  const parked = page.getByRole("tablist", { name: "Parked tabs" }), commands = page.getByRole("dialog", { name: "Run command" }), dialog = page.getByRole("dialog", { name: "Park until" });
  await openSaved(page, origin, href("alpha"), "Alpha", saved);
  await page.keyboard.press("Control+Shift+p");
  const filter = async (query: string) => {
    await page.getByPlaceholder("Type a command").fill(query);
    return (await commands.getByRole("button").allInnerTexts()).map((text: string) => text.split("\n")[0]).filter((label: string) => label.startsWith("Tabs: ") && label.toLowerCase().includes(query.toLowerCase())).toSorted();
  };
  expect(await filter("Tabs: Park")).toEqual(["Tabs: Park for 1 hour", "Tabs: Park for 4 hours", "Tabs: Park until I unpark", "Tabs: Park until a picked time…", "Tabs: Park until it is done", "Tabs: Park until next Monday at 9:00", "Tabs: Park until tomorrow at 9:00"]);
  expect(await commands.getByRole("button", { name: "Tabs: Park until it is done", exact: true }).locator("kbd").allTextContents()).toEqual(["Ctrl", "Shift", "S"]);
  expect([await filter("Mute"), await filter("Snooze"), await filter("Tabs: Mark as")]).toEqual([[], [], ["Tabs: Mark as unread"]]);
  await page.keyboard.press("Escape");
  await commands.waitFor({ state: "hidden" });

  const opensAt = async (now: string) => {
    await page.clock.setFixedTime(new Date(await page.evaluate((at: string) => new Date(at).toISOString(), now)));
    await menu(page, "Alpha", "Park", "Pick a time…");
    return [await dialog.getByLabel("Date").inputValue(), await dialog.getByLabel("Time").inputValue()];
  };
  expect(await opensAt("2030-01-01T23:30")).toEqual(["2030-01-02", "00:00"]);
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  expect(await opensAt("2030-01-02T08:23")).toEqual(["2030-01-02", "09:00"]);
  await dialog.getByLabel("Date").fill("2030-01-02");
  await dialog.getByLabel("Time").fill("10:30");
  await dialog.getByRole("button", { name: "Park", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  await until(() => saved.sent.length === 1);
  expect(saved.sent[0]).toEqual({ paths: [href("alpha")], action: "PARK", until: await page.evaluate(() => new Date("2030-01-02T10:30").toISOString()) });
  const stated = await page.evaluate(() => `Parked until ${new Date("2030-01-02T10:30").toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}`);
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  await parked.locator(`[role="tab"][aria-label="Alpha"][aria-description$="${stated}"]`).waitFor();
  expect(await parked.getByRole("tab", { name: "Alpha", exact: true }).locator("svg").getAttribute("class")).toContain("lucide-clock");
}), 30_000);

test("a Shared row offers the attention items as a tab of the person's own does, and stays in place when parked, dimmed only for the person who parked it", () => withPage({}, async (page, origin, _prompts, saved) => {
  const row = (name: string) => shared(page).getByRole("tab", { name, exact: true }), opacity = (path: string) => shared(page).locator(`[data-tab="${path}"]`).evaluate((node: HTMLElement) => getComputedStyle(node).opacity);
  const close = async () => { await page.keyboard.press("Escape"); await page.getByRole("menu").waitFor({ state: "hidden" }); };
  saved.state = { ...saved.state, tabs: [{ path: href("alpha"), space: "main" }], space: "main" };
  saved.shared = [{ space: "main", path: href("bravo") }, { space: "main", path: "/search/d", search: { name: "Ops", searches: [{ id: "0", name: "Needs you", query: "is:waiting" }], active: "0" } }];
  await page.goto(`${origin}${href("alpha")}`);
  expect(await sharedMenu(page, "Bravo")).toEqual(["Pull requests and issues", "Hosted Pages", "—", "Rename session", "Regenerate title", "—", "Copy link", "Remove from Shared", "—", "Mark as unread", "Park", "Auto-park when I send", "Unfollow", "—", "Add to group", "—", "Delete session"]);
  await close();
  expect(await sharedMenu(page, "Ops")).toEqual(["Rename search", "—", "Copy link", "Remove from Shared", "Duplicate", "—", "Mark as unread", "Park", "Unfollow", "—", "Add to group"]);
  await close();

  await shared(page).getByRole("tab", { name: "Bravo", exact: true }).click({ button: "right" });
  for (const item of ["Park", "Until I unpark"]) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await row("Bravo").and(page.locator('[aria-description="Shared, Space: Main, Parked"]')).waitFor();
  expect([await sharedNames(page), await opacity(href("bravo")), await names(page)]).toEqual([["Bravo", "Ops"], "0.6", ["Alpha"]]);
  expect(await sharedMenu(page, "Bravo")).toEqual(["Pull requests and issues", "Hosted Pages", "—", "Rename session", "Regenerate title", "—", "Copy link", "Remove from Shared", "—", "Unpark", "Auto-park when I send", "Unfollow", "—", "Add to group", "—", "Delete session"]);
  await close();
  saved.attention = [];
  await row("Bravo").and(page.locator('[aria-description="Shared, Space: Main"]')).waitFor();
  expect(await opacity(href("bravo"))).toBe("1");

  await sharedMenu(page, "Ops");
  for (const item of ["Add to group", "New group"]) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await page.getByRole("textbox", { name: "Group name" }).waitFor();
  await page.keyboard.press("Escape");
  await sharedMenu(page, "Bravo");
  for (const item of ["Add to group", "Grey group"]) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await strip(page).getByRole("tab", { name: "Bravo", exact: true }).waitFor();
  expect([await names(page), await sharedNames(page)]).toEqual([["Alpha", "Ops", "Bravo"], ["Bravo", "Ops"]]);
  await until(() => {
    const [ops, bravo] = ["/search/d", href("bravo")].map((path) => saved.state.tabs.find((item) => item.path === path));
    return !!ops?.group && bravo?.group === ops.group && ops.search?.name === "Ops";
  });
}), 30_000);

test("a Shared row moves to another space as a tab of the person's own, staying in Shared", () => withPage({}, async (page, origin, _prompts, saved) => {
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, tabs: [{ path: href("alpha"), space: "main" }], space: "main" };
  saved.shared = [{ space: "main", path: href("bravo") }];
  await page.goto(`${origin}${href("alpha")}`);
  expect(await sharedMenu(page, "Bravo")).toEqual(expect.arrayContaining(["Add to group", "Move to space"]));
  for (const item of ["Move to space", "Ops"]) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await until(() => saved.state.tabs.some((item) => item.path === href("bravo") && item.space === "ops"));
  expect([await names(page), await sharedNames(page)]).toEqual([["Alpha"], ["Bravo"]]);
}), 30_000);

test("a new chat created while another page shows opens when New session shows again", () => withPage({}, async (page, origin, prompts) => {
  const held = Promise.withResolvers<void>(), requested = Promise.withResolvers<void>();
  await page.route("**/api/CreateSession", async (route: { continue(): Promise<void> }) => { requested.resolve(); await held.promise; await route.continue(); });
  await page.goto(`${origin}/`);
  const box = page.getByPlaceholder("Message a new session");
  await box.fill("hello");
  await box.press("Enter");
  await requested.promise;
  await page.evaluate(() => { history.pushState(null, "", "/routines"); dispatchEvent(new PopStateEvent("popstate")); });
  await page.waitForURL("**/routines");
  held.resolve();
  await until(() => prompts.some((prompt) => prompt.id === "fresh"));
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(new URL(page.url()).pathname).toBe("/routines");
  await page.evaluate(() => { history.pushState(null, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
  await page.waitForURL(`**${href("fresh")}`);
}), 30_000);

test("every tab offers Unfollow, which then offers Follow, and the two act on their own tabs of a selection", () => withPage({}, async (page, origin, _prompts, saved) => {
  const tab = (name: string) => strip(page).getByRole("tab", { name, exact: true });
  const lines = async (name: string) => {
    await tab(name).click({ button: "right" });
    await page.getByRole("menuitem").first().waitFor();
    const items = await menuLines(page);
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "hidden" });
    return items;
  };
  saved.state = { ...saved.state, space: "main", tabs: [href("alpha"), href("charlie"), "/routines"].map((path) => ({ path, space: "main" })) };
  saved.shared = [{ space: "main", path: href("bravo") }];
  await page.goto(`${origin}${href("alpha")}`);
  await tab("Routines").waitFor();
  const after = (items: string[], item: string) => items[items.indexOf(item) + 1];
  expect([after(await lines("Alpha"), "Auto-park when I send"), after(await lines("Routines"), "Park"), after(await sharedMenu(page, "Bravo"), "Auto-park when I send")]).toEqual(["Unfollow", "Unfollow", "Unfollow"]);
  await page.keyboard.press("Escape");
  await page.getByRole("menu").waitFor({ state: "hidden" });

  await menu(page, "Routines", "Unfollow");
  await until(() => saved.sent.length === 1);
  await focus(page);
  await tab("Routines").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Follow", exact: true }).click();
  await page.getByRole("menu").waitFor({ state: "hidden" });
  await until(() => saved.sent.length === 2);
  expect(saved.sent).toEqual([{ paths: ["/routines"], action: "UNFOLLOW" }, { paths: ["/routines"], action: "FOLLOW" }]);

  await menu(page, "Alpha", "Unfollow");
  await focus(page);
  await tab("Alpha").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Follow", exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("menu").waitFor({ state: "hidden" });
  await tab("Charlie").click({ modifiers: ["ControlOrMeta"] });
  await menu(page, "Charlie", "Unfollow");
  await until(() => saved.sent.length === 4);
  expect(saved.sent[3]).toEqual({ paths: [href("charlie")], action: "UNFOLLOW" });
}), 60_000);

function opsCard(page: any, saved: Saved, sessionIDs: string[]) {
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, space: "main", groups: [{ id: "work", name: "Work", color: "Blue" }], tabs: [{ path: href("alpha"), space: "main" }, ...sessionIDs.map((id) => ({ path: href(id), space: "ops", group: id === "charlie" || id === "delta" ? undefined : "work" }))] };
  const ops = page.getByRole("tablist", { name: "Ops tabs" });
  return {
    ops,
    fold: page.getByRole("button", { name: "Ops", exact: true }),
    layout: () => ops.locator('[role="tab"], [aria-expanded]').evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label"))),
  };
}

const opsGolfMenu = ["Pull requests and issues", "Hosted Pages", "—", "Rename session", "Regenerate title", "—", "Close", "Close others", "Close to the right", "Close all", "—", "Copy link", "Add to Shared", "—", "Mark as unread", "Park", "Auto-park when I send", "Unfollow", "—", "Add to group", "Remove from group", "Move to space", "—", "Delete session"];

async function readOpsGolfMenu(page: any, ops: any) {
  await ops.getByRole("tab", { name: "Golf", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem").first().waitFor();
  const items = await menuLines(page);
  await page.keyboard.press("Escape");
  await page.getByRole("menu").waitFor({ state: "hidden" });
  return items;
}

test("Ops space card closes an inactive tab without changing the current space", () => withPage({}, async (page, origin, _prompts, saved) => {
  const { ops, fold, layout } = opsCard(page, saved, ["bravo", "foxtrot", "golf", "charlie", "delta"]);
  const bravo = ops.getByRole("tab", { name: "Bravo", exact: true });
  await page.goto(`${origin}${href("alpha")}`);
  await ops.getByRole("tab", { name: "Golf", exact: true }).waitFor();
  expect(await layout()).toEqual(["Work", "Bravo", "Foxtrot", "Golf", "Charlie", "Delta"]);
  expect(await bravo.getAttribute("aria-description")).toBe("Group Work, Space: Ops");
  expect(await fold.getAttribute("aria-description")).toBe("1 waiting, Turn running");
  expect(await page.locator('[data-card-key="/ops"]').getByText("Needs you", { exact: true }).first().isVisible()).toBe(true);
  expect(await readOpsGolfMenu(page, ops)).toEqual(opsGolfMenu);

  await ops.getByRole("tab", { name: "Delta", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close", exact: true }).click();
  await until(() => !saved.state.tabs.some((tab) => tab.path === href("delta")));
  expect(new URL(page.url()).pathname).toBe(href("alpha"));
  expect(await page.locator(`button${current}`).getAttribute("aria-label")).toBe("Main");
  expect(await fold.getAttribute("aria-expanded")).toBe("true");

  await bravo.click();
  await page.waitForURL(`**${href("bravo")}`);
  await fold.and(page.locator(current)).waitFor();
  await page.getByRole("tablist", { name: "Main tabs" }).getByRole("tab", { name: "Alpha", exact: true }).click();
  await page.waitForURL(`**${href("alpha")}`);
  await page.locator(`button${current}[aria-label="Main"]`).waitFor();
}));

test("folded Ops space card shows only the tab in view and marks hidden work on its group", () => withPage({}, async (page, origin, _prompts, saved) => {
  const { ops, fold, layout } = opsCard(page, saved, ["bravo", "foxtrot", "golf", "charlie"]);
  const header = ops.locator('[data-group="work"]');
  await page.goto(`${origin}${href("alpha")}`);
  await ops.getByRole("tab", { name: "Golf", exact: true }).waitFor();
  await fold.and(page.locator('[aria-expanded="true"]')).click();
  await fold.and(page.locator('[aria-expanded="false"]')).waitFor();
  expect(await layout()).toEqual([]);
  expect(await fold.getAttribute("aria-description")).toBe("1 waiting, Turn running");
  await until(() => saved.layout?.folded?.["/ops"] === true);

  await chooseSpace(page, "Ops", "menuitemradio");
  await page.waitForURL(`**${href("bravo")}`);
  expect(await layout()).toEqual(["Work", "Bravo"]);
  await header.getByText("Needs you", { exact: true }).waitFor();
  expect(await header.getByRole("button", { name: "Work", exact: true }).locator("span").evaluate((name: HTMLElement) => getComputedStyle(name).fontWeight)).toBe("700");
}));

test("unfolding the current Ops space card restores its tabs and full menu", () => withPage({}, async (page, origin, _prompts, saved) => {
  const { ops, fold, layout } = opsCard(page, saved, ["bravo", "foxtrot", "golf", "charlie"]);
  saved.state.space = "ops";
  saved.layout = { folded: { "/ops": true } };
  const header = ops.locator('[data-group="work"]');
  await page.goto(`${origin}${href("bravo")}`);
  await fold.and(page.locator(`${current}[aria-expanded="false"]`)).click();
  await fold.and(page.locator('[aria-expanded="true"]')).waitFor();
  expect(await layout()).toEqual(["Work", "Bravo", "Foxtrot", "Golf", "Charlie"]);
  expect(await ops.getByRole("tab", { name: "Bravo", exact: true }).getAttribute("aria-description")).toBe("Group Work, Space: Ops");
  expect(await readOpsGolfMenu(page, ops)).toEqual(opsGolfMenu);
  expect(await header.getByText("Needs you").count()).toBe(0);
  expect(await header.getByRole("button", { name: "Work", exact: true }).locator("span").evaluate((name: HTMLElement) => getComputedStyle(name).fontWeight)).toBe("500");
}));

test("Parked keeps one list of every space's parked tabs in the person's order: a newly parked tab is born on top, rows move by dragging or Shift+Arrow, and the X unparks a tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, space: "main", tabs: [...["alpha", "bravo", "delta"].map((id) => ({ path: href(id), space: "main" })), ...["charlie", "echo"].map((id) => ({ path: href(id), space: "ops" }))] };
  saved.attention = [{ path: href("bravo"), parked: true }, { path: href("charlie"), parked: true }];
  const parked = page.getByRole("tablist", { name: "Parked tabs" }), row = (name: string) => parked.getByRole("tab", { name, exact: true });
  const rows = () => parked.locator("[data-recent]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => [node.querySelector('[role="tab"]')!.getAttribute("aria-label"), node.querySelector('[role="tab"] span[aria-hidden="true"]')!.textContent!.split(" · ")[0]]));
  const stored = () => JSON.stringify(listed(saved, "parked").map((tab) => tab.path));
  await page.goto(`${origin}${href("alpha")}`);
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  await row("Charlie").waitFor();
  expect(await rows()).toEqual([["Bravo", "Main"], ["Charlie", "Ops"]]);
  expect(await parked.locator("[aria-expanded]").count()).toBe(0);
  await until(() => stored() === JSON.stringify([href("bravo"), href("charlie")]));

  saved.attention.push({ path: href("delta"), parked: true });
  await row("Delta").waitFor();
  await until(() => stored() === JSON.stringify([href("delta"), href("bravo"), href("charlie")]));
  expect(await rows()).toEqual([["Delta", "Main"], ["Bravo", "Main"], ["Charlie", "Ops"]]);

  await row("Charlie").dragTo(row("Delta"));
  await until(() => stored() === JSON.stringify([href("charlie"), href("delta"), href("bravo")]));
  await row("Charlie").press("Shift+ArrowDown");
  await until(() => stored() === JSON.stringify([href("delta"), href("charlie"), href("bravo")]));
  expect(await page.evaluate(() => document.activeElement?.getAttribute("aria-label"))).toBe("Charlie");
  await page.reload();
  await row("Bravo").waitFor();
  expect(await rows()).toEqual([["Delta", "Main"], ["Charlie", "Ops"], ["Bravo", "Main"]]);

  await parked.getByRole("button", { name: "Unpark Bravo", exact: true }).click();
  await row("Bravo").waitFor({ state: "detached" });
  expect(saved.sent.at(-1)).toEqual({ paths: [href("bravo")], action: "UNPARK" });
  await until(() => stored() === JSON.stringify([href("delta"), href("charlie")]));
  expect(saved.state.tabs.some((tab) => tab.path === href("bravo"))).toBe(true);

  saved.attention.find((item) => item.path === href("bravo"))!.parked = true;
  await row("Bravo").waitFor();
  await until(() => stored() === JSON.stringify([href("bravo"), href("delta"), href("charlie")]));
}), 30_000);

function parkSharedHotel(saved: Saved) {
  saved.spaces.push({ id: "ops", name: "Ops" });
  saved.state = { ...saved.state, space: "main", tabs: [{ path: href("alpha"), space: "main" }] };
  saved.shared = [{ space: "ops", path: href("hotel") }];
  saved.attention = [{ path: href("hotel"), parked: true }];
}

test("Parked Shared rows list their space and can be renamed", () => withPage({}, async (page, origin, _prompts, saved) => {
  parkSharedHotel(saved);
  const parked = page.getByRole("tablist", { name: "Parked tabs" });
  const row = parked.getByRole("tab", { name: "Hotel", exact: true });
  await page.goto(`${origin}${href("alpha")}`);
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  expect(await row.getAttribute("aria-description")).toBe("Shared, Space: Ops, Forked session, Parked");
  expect(await parked.getByRole("button", { name: "Close Hotel" }).count()).toBe(0);

  await row.click({ button: "right" });
  await page.getByRole("menuitem").first().waitFor();
  expect(await menuLines(page)).toEqual(["Pull requests and issues", "Hosted Pages", "—", "Rename session", "Regenerate title", "—", "Copy link", "Remove from Shared", "—", "Unpark", "Auto-park when I send", "Unfollow", "—", "Add to group", "Move to space", "—", "Delete session"]);
  await page.getByRole("menuitem", { name: "Rename session", exact: true }).click();
  const input = parked.getByRole("textbox", { name: "Session name" });
  await input.fill("Renamed hotel");
  await input.press("Enter");
  await parked.getByRole("tab", { name: "Renamed hotel", exact: true }).waitFor();
}));

test("Parked Shared rows open in their space without a personal tab", () => withPage({}, async (page, origin, _prompts, saved) => {
  parkSharedHotel(saved);
  const row = page.getByRole("tablist", { name: "Parked tabs" }).getByRole("tab", { name: "Hotel", exact: true });
  await page.goto(`${origin}${href("alpha")}`);
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  await row.click();
  await page.waitForURL(`**${href("hotel")}`);
  await until(() => saved.state.space === "ops");
  expect(saved.state.tabs.map((tab) => tab.path)).toEqual([href("alpha")]);
}));

test("Parked Shared rows can join a group in their space", () => withPage({}, async (page, origin, _prompts, saved) => {
  parkSharedHotel(saved);
  saved.state.space = "ops";
  const row = page.getByRole("tablist", { name: "Parked tabs" }).getByRole("tab", { name: "Hotel", exact: true });
  await page.goto(`${origin}${href("hotel")}`);
  await page.getByRole("button", { name: "Ops", exact: true }).and(page.locator(current)).waitFor();
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  expect(saved.state.tabs.some((tab) => tab.path === href("hotel"))).toBe(false);
  await row.click({ button: "right" });
  for (const item of ["Add to group", "New group"]) await page.getByRole("menuitem", { name: item, exact: true }).click();
  await page.getByRole("textbox", { name: "Group name" }).waitFor();
  await page.keyboard.press("Escape");
  await until(() => saved.state.tabs.some((tab) => tab.path === href("hotel") && tab.space === "ops" && !!tab.group));
}));

test("a parked Shared row offers the same menu from Parked as from its space's Shared group", () => withPage({}, async (page, origin, _prompts, saved) => {
  saved.state = { ...saved.state, space: "main", tabs: [{ path: href("alpha"), space: "main" }] };
  saved.shared = [{ space: "main", path: href("bravo") }];
  saved.attention = [{ path: href("bravo"), parked: true }];
  await page.goto(`${origin}${href("alpha")}`);
  const fromShared = await sharedMenu(page, "Bravo");
  await page.keyboard.press("Escape");
  await page.getByRole("menu").waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "Parked", exact: true }).click();
  await page.getByRole("tablist", { name: "Parked tabs" }).getByRole("tab", { name: "Bravo", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem").first().waitFor();
  expect(await menuLines(page)).toEqual(fromShared);
}));

type RemoteTabs = { spaces: { id: string; name: string }[]; state: Saved["state"]; shared: SharedTab[] };
function serveRemote(tabs: RemoteTabs, sessions: (sent: Change[]) => object[]) {
  const cors = { "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Headers": "*", "Access-Control-Allow-Methods": "*" }, sent: Change[] = [], saved = { ...tabs, revision: 1, closed: [] };
  const stream = { ...cors, "Content-Type": "text/event-stream" };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (request.method === "OPTIONS") return new Response(null, { headers: cors });
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: stream });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: sessions(sent), owner: "tester", upstreamSuccess: true, tabs: [], opencodeServers: { far: "http://far.test" } })}\n\nevent: complete\ndata: {}\n\n`, { headers: stream });
    const input = await request.json().catch(() => ({})) as { attention?: Change; state?: Saved["state"]; revision?: number };
    if (input.attention) sent.push(input.attention);
    const stale = !!input.state && input.revision !== saved.revision;
    if (input.state && !stale) [saved.state, saved.revision] = [input.state, saved.revision + 1];
    const replies: Record<string, unknown> = { "/api/Identity": { username: "tester" }, "/api/Protocol": { protoSha256: "tabs" }, "/api/Tabs": stale ? { ...saved, stale } : saved, "/api/ChangeTabs": stale ? { ...saved, stale } : saved, "/api/ListConfig": { config: { workspace: "tabs" } }, "/api/ListAgents": { agents: [{ name: "main" }], currentAgent: "main" }, "/api/ListRoutines": { jobs: [] }, "/api/ListQueue": { items: [] }, "/api/CreateSession": { id: "opencode:far:ses_far" },
      "/api/History": { messages: [], origin: "", revision: "1", reset: true, replacedKeys: [], removedKeys: [], entryKeys: [], running: false, terminal: "" } };
    return Response.json(replies[url.pathname] ?? {}, { headers: cors });
  } });
  return { server, sent, saved, url: `http://127.0.0.1:${server.port}` };
}
const addRemote = (page: any, url: string) => page.addInitScript((remote: string) => document.addEventListener("readystatechange", () => {
  if (document.getElementById("rocketclaws")) return;
  document.head.append(Object.assign(document.createElement("script"), { id: "rocketclaws", type: "application/json", textContent: JSON.stringify([{ name: "work", url: remote }]) }));
}), url);

const workSessions = () => [
  { id: "india", name: "India", agent: "main", spaceId: "lab", unreadAt: "2026-10-22T11:00:00Z" },
  { id: "kilo", name: "Kilo", agent: "main", spaceId: "lab" },
  { id: "juliet", name: "Juliet", agent: "main", spaceId: "home", running: true, asking: true },
  { id: "lima", name: "Lima", agent: "main", spaceId: "home" },
];
const workTabs = (): RemoteTabs => ({
  spaces: [{ id: "lab", name: "Lab" }, { id: "home", name: "Home" }],
  state: { tabs: [{ path: href("india"), space: "lab" }, { path: href("kilo"), space: "lab" }, { path: href("juliet"), space: "home" }], groups: [], space: "lab", last: {}, sharedOrder: [] },
  shared: [{ space: "home", path: href("lima") }],
});
async function withWork(page: any, saved: Saved, run: (remote: ReturnType<typeof serveRemote>) => Promise<void>) {
  const remote = serveRemote(workTabs(), workSessions);
  try {
    await addRemote(page, remote.url);
    saved.spaces.push({ id: "ops", name: "Ops" });
    saved.state = { ...saved.state, space: "main", tabs: [{ path: href("alpha"), space: "main" }, { path: href("bravo"), space: "ops" }] };
    await run(remote);
  } finally {
    remote.server.stop(true);
  }
}
const cardTitles = (page: any) => page.locator("[data-card-key] > div > button[aria-expanded][aria-description]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));
const cardTitle = (page: any, name: string) => page.getByRole("button", { name, exact: true });
const tabList = (page: any, name: string) => page.getByRole("tablist", { name, exact: true });
const tabNames = (page: any, name: string) => tabList(page, name).getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")));

test("with other RocketClaws the strip lists every server's space cards, titled with their server, each with its tabs and Shared tabs", () => withPage({}, (page, origin, _prompts, saved) => withWork(page, saved, async () => {
  await page.goto(`${origin}${href("alpha")}`);
  await tabList(page, "Lab tabs on work").getByRole("tab", { name: "Kilo", exact: true }).waitFor();
  expect(await cardTitles(page)).toEqual(["Main · 127.0.0.1", "Ops · 127.0.0.1", "Lab · work", "Home · work"]);
  expect(await page.locator(`button${current}`).getAttribute("aria-label")).toBe("Main · 127.0.0.1");
  expect(await tabNames(page, "Ops tabs on 127.0.0.1")).toEqual(["Bravo"]);
  expect(await tabNames(page, "Lab tabs on work")).toEqual(["India", "Kilo"]);
  expect(await tabNames(page, "Home tabs on work")).toEqual(["Juliet"]);
  expect(await tabNames(page, "Home Shared tabs on work")).toEqual(["Lima"]);
})), 30_000);

test("another RocketClaw's tab takes the whole tab menu, and its changes and closes act on that server", () => withPage({}, (page, origin, _prompts, saved) => withWork(page, saved, async (remote) => {
  await page.goto(`${origin}${href("alpha")}`);
  const lab = tabList(page, "Lab tabs on work");
  await lab.getByRole("tab", { name: "India", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem").first().waitFor();
  expect(await menuLines(page)).toEqual(expect.arrayContaining(["Close", "Close others", "Add to Shared", "Mark as read", "Add to group", "Move to space"]));
  await page.getByRole("menuitem", { name: "Mark as read", exact: true }).click();
  await until(() => remote.sent.length === 1);
  expect(remote.sent).toEqual([{ paths: [href("india")], action: "MARK_READ" }]);

  await lab.getByRole("button", { name: "Close Kilo", exact: true }).click();
  await until(() => !remote.saved.state.tabs.some((tab) => tab.path === href("kilo")));
  expect(remote.saved.state.tabs.map((tab) => tab.path)).toEqual([href("india"), href("juliet")]);
  expect(new URL(page.url()).pathname).toBe(href("alpha"));
})), 30_000);

test("space cards from every RocketClaw move by dragging and Shift+Arrow, a card not shown keeps its place, and a new space comes last", () => withPage({}, (page, origin, _prompts, saved) => withWork(page, saved, async () => {
  saved.layout = { order: ["away/ops"] };
  await page.goto(`${origin}${href("alpha")}`);
  await cardTitle(page, "Lab · work").waitFor();

  await cardTitle(page, "Lab · work").dragTo(cardTitle(page, "Main · 127.0.0.1"), { targetPosition: { x: 20, y: 2 } });
  await until(() => JSON.stringify(saved.layout?.order) === JSON.stringify(["away/ops", "work/lab", "/main", "/ops", "work/home"]));
  expect(await cardTitles(page)).toEqual(["Lab · work", "Main · 127.0.0.1", "Ops · 127.0.0.1", "Home · work"]);

  await cardTitle(page, "Ops · 127.0.0.1").focus();
  await page.keyboard.press("Shift+ArrowUp");
  await until(() => JSON.stringify(saved.layout?.order) === JSON.stringify(["away/ops", "work/lab", "/ops", "/main", "work/home"]));
  expect(await page.evaluate(() => document.activeElement?.getAttribute("aria-label"))).toBe("Ops · 127.0.0.1");

  await newSpace(page, "Docs", "Spaces: New space on 127.0.0.1");
  await cardTitle(page, "Docs · 127.0.0.1").waitFor();
  expect(await cardTitles(page)).toEqual(["Lab · work", "Ops · 127.0.0.1", "Main · 127.0.0.1", "Home · work", "Docs · 127.0.0.1"]);
  expect(saved.layout?.order).toEqual(["away/ops", "work/lab", "/ops", "/main", "work/home"]);
})), 30_000);

test("a folded space card and the card order follow this person to another RocketClaw's page and to a browser with nothing saved", () => withPage({}, (page, origin, _prompts, saved) => withWork(page, saved, async () => {
  const order = ["Lab · work", "Ops · 127.0.0.1", "Main · 127.0.0.1", "Home · work"];
  saved.layout = { order: ["work/lab", "/ops", "/main", "work/home"] };
  await page.goto(`${origin}${href("alpha")}`);
  await tabList(page, "Ops tabs on 127.0.0.1").getByRole("tab", { name: "Bravo", exact: true }).waitFor();
  expect(await cardTitles(page)).toEqual(order);

  await cardTitle(page, "Ops · 127.0.0.1").click();
  await page.locator('[aria-label="Ops · 127.0.0.1"][aria-expanded="false"]').waitFor();
  await until(() => saved.layout?.folded?.["/ops"] === true);
  expect(await tabNames(page, "Ops tabs on 127.0.0.1")).toEqual([]);

  await tabList(page, "Home tabs on work").getByRole("tab", { name: "Juliet", exact: true }).click();
  await page.waitForURL(`**/r/work${href("juliet")}`);
  await page.locator(`button${current}[aria-label="Home · work"]`).waitFor();
  expect(await cardTitles(page)).toEqual(order);
  expect(await cardTitle(page, "Ops · 127.0.0.1").getAttribute("aria-expanded")).toBe("false");

  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await tabList(page, "Home tabs on work").getByRole("tab", { name: "Juliet", exact: true }).waitFor();
  expect(await cardTitles(page)).toEqual(order);
  expect(await cardTitle(page, "Ops · 127.0.0.1").getAttribute("aria-expanded")).toBe("false");
  expect(await tabNames(page, "Ops tabs on 127.0.0.1")).toEqual([]);
})), 30_000);

test("with parked tabs on more than one RocketClaw, Parked lists them in one list whose rows name their server and act on their own server", () => withPage({}, async (page, origin, _prompts, saved) => {
  const remote = serveRemote({ spaces: [{ id: "lab", name: "Lab" }], state: { tabs: [{ path: href("india"), space: "lab" }], groups: [], space: "lab", last: {}, sharedOrder: [] }, shared: [] },
    (sent) => [{ id: "india", name: "India", agent: "main", spaceId: "lab", parked: !sent.length }]);
  try {
    await addRemote(page, remote.url);
    saved.state = { ...saved.state, space: "main", tabs: [{ path: href("alpha"), space: "main" }, { path: href("bravo"), space: "main" }] };
    saved.attention = [{ path: href("bravo"), parked: true }];
    const parked = page.getByRole("tablist", { name: "Parked tabs" }), india = parked.getByRole("tab", { name: "India", exact: true });
    await page.goto(`${origin}${href("alpha")}`);
    await page.getByRole("button", { name: "Parked", exact: true }).click();
    await india.waitFor();
    await parked.getByRole("tab", { name: "Bravo", exact: true }).waitFor();
    expect((await parked.getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toSorted()).toEqual(["Bravo", "India"]);
    expect(await parked.locator("[aria-expanded]").count()).toBe(0);
    expect([await india.getAttribute("aria-description"), await india.locator('span[aria-hidden="true"]').last().textContent()]).toEqual(["Space: Lab, work, Parked", "Lab · work · Parked"]);
    expect(await parked.getByRole("button", { name: "Close India" }).count()).toBe(0);

    await india.click({ button: "right" });
    await page.getByRole("menuitem").first().waitFor();
    expect(await menuLines(page)).toEqual(["Pull requests and issues", "Hosted Pages", "—", "Unpark", "Auto-park when I send", "Unfollow"]);
    await page.getByRole("menuitem", { name: "Unpark", exact: true }).click();
    await until(() => remote.sent.length === 1);
    expect(remote.sent).toEqual([{ paths: [href("india")], action: "UNPARK" }]);
    await india.waitFor({ state: "detached" });
    expect(await parked.getByRole("tab").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["Bravo"]);
  } finally {
    remote.server.stop(true);
  }
}));

test("New session and the palette offer every RocketClaw's OpenCode servers from any page, and start the session on the server that has it", () => withPage({}, async (page, origin, _prompts, saved) => {
  const remote = serveRemote({ spaces: [{ id: "lab", name: "Lab" }], state: { tabs: [], groups: [], space: "lab", last: {}, sharedOrder: [] }, shared: [] }, () => []);
  const createURL = `${remote.url}/api/CreateSession`;
  let pending = "configure remote RocketClaw";
  type CreateRequest = { url(): string; postData(): string | null; failure(): { errorText: string } | null };
  let lastCreate: CreateRequest | undefined, status: number | undefined, bodyFinished = false;
  let lastPageError: { pending: string; message: string } | null = null;
  page.on("pageerror", (error: Error) => { lastPageError = { pending, message: error.message }; });
  page.on("response", (response: { request(): CreateRequest; status(): number }) => {
    if (response.request() === lastCreate) status = response.status();
  });
  page.on("requestfinished", (request: CreateRequest) => {
    if (request === lastCreate) bodyFinished = true;
  });
  onTestFinished(() => {
    if (pending) {
      expect({
        pending, pageURL: page.url(), createURL, lastPageError,
        lastCreate: lastCreate ? { url: lastCreate.url(), body: lastCreate.postData(), status: status ?? null, bodyFinished, failure: lastCreate.failure()?.errorText ?? null } : null,
      }, `Expected to finish: ${pending}`).toMatchObject({ pending: "" });
    }
  });
  try {
    await addRemote(page, remote.url);
    saved.state = { ...saved.state, space: "main", tabs: [{ path: href("alpha"), space: "main" }] };
    pending = "open unknown server page";
    await page.goto(`${origin}/r/nowhere/`);
    pending = "open New session menu on unknown server page";
    await page.getByRole("button", { name: "New session", exact: true }).click();
    pending = "wait for far in New session menu";
    await page.getByRole("menuitem", { name: "far", exact: true }).waitFor();
    pending = "open command palette";
    await page.keyboard.press("Escape");
    await page.keyboard.press("Control+Shift+p");
    pending = "search command palette for far";
    await page.getByPlaceholder("Type a command").fill("new on far");
    pending = "wait for far in command palette";
    await page.getByRole("button", { name: "Sessions: New on far (work)", exact: true }).waitFor();
    pending = "open Alpha";
    await page.keyboard.press("Escape");
    await page.getByRole("tab", { name: "Alpha", exact: true }).click();
    await page.waitForURL(`${origin}${href("alpha")}`);
    const created = page.waitForRequest((request: CreateRequest) => {
      const url = request.url();
      if (new URL(url).pathname === "/api/CreateSession") {
        lastCreate = request;
        status = undefined;
        bodyFinished = false;
      }
      return url === createURL;
    });
    pending = "open New session menu on Alpha";
    await page.getByRole("button", { name: "New session", exact: true }).click();
    pending = "choose far in New session menu";
    await page.getByRole("menuitem", { name: "far", exact: true }).click();
    const folder = page.getByPlaceholder("Folder on far, like /srv/app");
    pending = "fill remote folder";
    await folder.fill("/srv/far");
    pending = "submit remote folder";
    await folder.press("Enter");
    pending = "check CreateSession request";
    expect(JSON.parse((await created).postData())).toEqual({ opencodeServer: "far", directory: "/srv/far", spaceId: "lab" });
    pending = "wait for remote session URL";
    const outcome = new AbortController();
    try {
      await Promise.race([
        page.waitForURL(`**/r/work/oc/${Buffer.from("opencode:far:ses_far").toString("base64url")}`, { signal: outcome.signal }),
        page.getByRole("dialog", { name: "New session on far", exact: true }).getByRole("alert").textContent({ signal: outcome.signal }).then((message: string | null) => {
          throw new Error(`New session on far: ${message}`);
        }),
      ]);
    } finally {
      outcome.abort();
    }
    pending = "";
  } finally {
    remote.server.stop(true);
  }
}));
