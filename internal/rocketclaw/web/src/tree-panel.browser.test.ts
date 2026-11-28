import { expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";
import type { TreeNode } from "./types";

type Node = { id: string; parent: string; role: "user" | "assistant"; text: string; branch?: string };

const floorReason = "Revert can't go back past a message from a routine run, External MCP, or Slack";
const outside = (node: Partial<TreeNode> & Pick<TreeNode, "id" | "text">): TreeNode => ({ parentId: "", role: "user", outside: false, pickable: false, through: false, onPath: true, current: false, branchId: "", deletable: false, ...node });
const reported = [
  outside({ id: "1:0", text: "Before report" }),
  outside({ id: "1:1", parentId: "1:0", role: "assistant", text: "Earlier answer", through: true }),
  outside({ id: "2:0", parentId: "1:1", text: "Abandoned try", through: true, onPath: false, branchId: "2:0", deletable: true }),
  outside({ id: "3:0", parentId: "1:1", text: "Kept try", branchId: "3:0" }),
  outside({ id: "4:0", parentId: "3:0", text: "Cron report", outside: true }),
  outside({ id: "4:1", parentId: "4:0", role: "assistant", text: "Report done", outside: true, pickable: true, through: true, current: true }),
];
const branched = [
  outside({ id: "b0", text: "Plan a **trip**" }),
  outside({ id: "b1", parentId: "b0", role: "assistant", text: "## Options: `a` and [b](https://b.test) *now*" }),
  outside({ id: "b8", parentId: "b1", text: "Try **B**", onPath: false, branchId: "b8", deletable: true }),
  outside({ id: "b9", parentId: "b8", role: "assistant", text: "For B", onPath: false }),
  outside({ id: "b2", parentId: "b1", text: "Try A", branchId: "b2", deletable: true }),
  outside({ id: "b3", parentId: "b2", role: "assistant", text: "For A" }),
  outside({ id: "b6", parentId: "b3", text: "A two", onPath: false, branchId: "b6", deletable: true }),
  outside({ id: "b7", parentId: "b6", role: "assistant", text: "A two answer", onPath: false }),
  outside({ id: "b4", parentId: "b3", text: "A one", branchId: "b4", deletable: true }),
  outside({ id: "b5", parentId: "b4", role: "assistant", text: "A one answer", current: true }),
];

for (const [width, height] of [[1280, 900], [390, 664], [320, 568]]) test(`message tree panel at ${width}px`, async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
  let nodes: Node[] = [
    { id: "1:0", parent: "", role: "user", text: "First request" },
    { id: "1:1", parent: "1:0", role: "assistant", text: "First answer" },
    { id: "2:0", parent: "1:1", role: "user", text: "Old try", branch: "2:0" },
    { id: "3:0", parent: "1:1", role: "user", text: "New try", branch: "3:0" },
    { id: "3:1", parent: "3:0", role: "assistant", text: "New answer" },
  ];
  let head = "3:1", busy = false, held = Promise.resolve();
  const moves: { messageId?: string; boundary?: string }[] = [], deletes: { messageId: string; dryRun?: boolean }[] = [], summaries: { branchId: string; instructions: string }[] = [];
  let nextSummary = Promise.withResolvers<() => void>();
  const onPath = () => {
    const ids = new Set<string>();
    for (let at = nodes.find((node) => node.id === head); at; at = nodes.find((node) => node.id === at!.parent)) ids.add(at.id);
    return ids;
  };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", name: "Chat", agent: "main" }, { id: "cron", name: "Cron", agent: "main" }, { id: "branches", name: "Branches", agent: "main" }], owner: "alice", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    const input = await request.json() as { id: string; messageId?: string; boundary?: string; dryRun?: boolean; branchId: string; instructions: string };
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "alice", principal: "Alice" });
      case "/api/Protocol": return Response.json({ protoSha256: "tree" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "first" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/History": {
        const path = onPath(), shown = input.id === "chat" ? nodes.filter((node) => path.has(node.id)) : [];
        const messages = shown.map((node) => ({ messageId: node.id, entryKey: node.id.split(":")[0], itemId: node.id, inputId: "", turnId: node.id.split(":")[0], role: node.role, text: node.text, complete: true }));
        return Response.json({ messages, origin: "", delegations: [], revision: head, reset: true, replacedKeys: [], removedKeys: [], entryKeys: [...new Set(messages.map((message) => message.entryKey))], running: false, terminal: "", start: "0", more: false, revertEligible: true, revertMessageId: head === "3:1" ? "" : head, canUndo: true });
      }
      case "/api/MessageTree": {
        if (input.id === "cron") return Response.json({ nodes: reported });
        if (input.id === "branches") return Response.json({ nodes: branched });
        const path = onPath();
        return Response.json({ nodes: nodes.map((node): TreeNode => ({ id: node.id, parentId: node.parent, role: node.role, text: node.text, outside: false, pickable: true, onPath: path.has(node.id), current: node.id === head,
          through: node.role === "assistant" || !path.has(node.id) && !nodes.some((child) => child.parent === node.id), branchId: node.branch ?? "", deletable: !!node.branch })) });
      }
      case "/api/StageRevert": {
        if (busy) return Response.json({ message: "History is busy", code: 10 }, { status: 409 });
        moves.push({ messageId: input.messageId, boundary: input.boundary });
        await held;
        const node = nodes.find((item) => item.id === input.messageId)!;
        head = input.boundary === "THROUGH" ? node.id : node.parent;
        return Response.json({ revertMessageId: head, prompt: input.boundary === "THROUGH" ? undefined : { role: "user", text: node.text, complete: true, turnId: "", entryKey: "", itemId: "", inputId: "" } });
      }
      case "/api/DeleteBranch": {
        deletes.push({ messageId: input.messageId!, dryRun: input.dryRun });
        const current = onPath().has(input.messageId!);
        if (!input.dryRun) {
          nodes = nodes.filter((node) => node.id !== input.messageId);
          if (current) head = "1:1";
        }
        return Response.json({ messages: 1, nestedBranches: 0, subagentSessions: 1, queuedMessages: 2, current });
      }
      case "/api/SummarizeBranch": {
        summaries.push({ branchId: input.branchId, instructions: input.instructions });
        const heldSummary = Promise.withResolvers<void>();
        nextSummary.resolve(heldSummary.resolve);
        nextSummary = Promise.withResolvers<() => void>();
        await heldSummary.promise;
        if (input.instructions === "fail") return Response.json({ message: "Summary model unavailable", code: 14 }, { status: 503 });
        if (onPath().has(input.branchId)) return Response.json({ appended: false });
        nodes.push({ id: "9:0", parent: head, role: "user", text: "Summary of New try" });
        head = "9:0";
        return Response.json({ appended: true });
      }
      case "/api/Tabs": case "/api/ChangeTabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const narrow = width < 1024;
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replaceAll("=", "")}`);
    const input = page.locator("textarea");
    const tree = page.getByRole("list", { name: "Messages and replies" });
    const lines = () => tree.locator("li > button:not([aria-label])").allTextContents();
    const row = (name: string) => tree.getByRole("button", { name, exact: true });
    const shape = () => tree.getByRole("listitem").evaluateAll((items: Element[]) => items.map((item) => {
      const connectors = item.querySelector("[data-connectors]")!;
      return [connectors.getAttribute("aria-hidden"), connectors.textContent, connectors.getAttribute("data-connectors"), item.querySelector(":scope > button")!.textContent];
    }));
    const drawn = (want: [string, string][]) => want.map(([connectors, line]) => ["true", "", connectors, line]);
    const left = async (name: string) => (await row(name).boundingBox())!.x;
    const open = (chat = "chat") => page.evaluate((id: string) => { history.pushState({}, "", `/s/${btoa(id).replaceAll("=", "")}?tree=1`); dispatchEvent(new PopStateEvent("popstate")); }, chat);
    const opened = () => new URL(page.url()).searchParams.has("tree");
    const closed = () => page.waitForFunction(() => !new URLSearchParams(location.search).has("tree"));
    await page.getByText("New answer", { exact: true }).waitFor();
    await input.waitFor({ state: "visible" });
    await page.waitForFunction(() => !document.querySelector("textarea")?.closest("fieldset")?.disabled);

    const chat = new URL(page.url()).pathname;
    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("message tree");
    const entries = () => page.getByRole("dialog").getByRole("button", { name: /(Show|Hide) message tree/ }).allTextContents();
    await page.getByRole("button", { name: "Sessions: Show message tree" }).waitFor();
    expect(await entries()).toEqual(["Sessions: Show message tree"]);
    await page.keyboard.press("Enter");
    await row("Agent New answer").waitFor();
    expect(opened()).toBe(true);
    await page.keyboard.press("Control+p");
    await page.getByPlaceholder("Search sessions and commands").fill("message tree");
    await page.getByRole("button", { name: "Sessions: Hide message tree" }).waitFor();
    expect(await entries()).toEqual(["Sessions: Hide message tree"]);
    await page.keyboard.press("Enter");
    await closed();
    expect(new URL(page.url()).pathname).toBe(chat);
    await row("Agent New answer").waitFor({ state: "detached" });
    await page.getByRole("dialog").waitFor({ state: "detached" });

    await input.fill("/tree");
    if (width < 640) await page.getByRole("button", { name: "Send", exact: true }).click();
    else await input.press("Enter");
    await row("Agent New answer").waitFor();
    expect(opened()).toBe(true);
    expect(await input.inputValue()).toBe("");
    await page.waitForFunction(() => document.activeElement?.getAttribute("aria-current") === "true");
    expect(await page.evaluate(() => document.activeElement?.textContent)).toBe("Agent New answer");
    const shown = (await row("Agent New answer").boundingBox())!;
    expect(shown.y >= 0 && shown.y + shown.height <= height).toBe(true);
    expect(await lines()).toEqual(["You First request", "Agent First answer", "You New try", "Agent New answer", "You Old try"]);
    expect(await shape()).toEqual(drawn([["", "You First request"], ["", "Agent First answer"], ["├", "You New try"], ["│", "Agent New answer"], ["└", "You Old try"]]));
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: path.resolve(import.meta.dir, `../../../../.tmp/rocketclaw/message-tree/tree-${width}.png`) });

    const fold = tree.getByRole("button", { name: "Other branches", exact: true });
    expect(await fold.getAttribute("aria-expanded")).toBe("true");
    await fold.focus();
    await page.keyboard.press("Enter");
    expect(await fold.getAttribute("aria-expanded")).toBe("false");
    expect(await lines()).toEqual(["You First request", "Agent First answer", "You New try", "Agent New answer"]);
    await page.keyboard.press("Enter");
    expect(await lines()).toHaveLength(5);

    const mine = page.getByRole("switch", { name: "Your messages only" });
    await mine.click();
    expect(await lines()).toEqual(["You First request", "You New try", "You Old try"]);
    await mine.click();
    expect(await lines()).toHaveLength(5);

    busy = true;
    await row("You First request").click();
    await tree.locator("xpath=..").getByRole("alert").filter({ hasText: "History is busy" }).waitFor();
    expect(opened()).toBe(true);
    expect(moves).toHaveLength(0);
    busy = false;

    let release = () => {};
    held = new Promise<void>((resolve) => { release = resolve; });
    await row("Agent New answer").click();
    await page.waitForFunction(() => document.querySelector('[aria-label="Messages and replies"] li > button:not([aria-label])')?.getAttribute("aria-disabled") === "true");
    await row("You First request").click({ force: true });
    expect(moves).toEqual([{ messageId: "3:1", boundary: "THROUGH" }]);
    release();
    if (narrow) {
      await closed();
      await open();
    }
    await page.waitForFunction(() => !document.querySelector('[aria-label="Messages and replies"] li > button[aria-disabled]:not([aria-label])'));
    await tree.locator("xpath=..").getByRole("alert").waitFor({ state: "detached" });
    expect(moves).toHaveLength(1);

    if (narrow) {
      await page.getByRole("button", { name: "Close message tree", exact: true }).click();
      await closed();
    }
    await input.fill("Typed draft");
    if (narrow) await open();
    await row("Agent First answer").focus();
    await page.keyboard.press("Enter");
    await page.locator('[data-message-id="3:1"]').first().waitFor({ state: "detached" });
    expect(moves.at(-1)).toEqual({ messageId: "1:1", boundary: "THROUGH" });
    expect(await input.inputValue()).toBe("Typed draft");
    expect(await page.locator('[data-message-id="1:1"]').count()).toBeGreaterThan(0);
    if (narrow) {
      await closed();
      await open();
    }
    await page.waitForFunction(() => document.querySelector('[aria-label="Messages and replies"] [aria-current="true"]')?.textContent === "Agent First answer");

    await row("You New try").click();
    await page.waitForFunction(() => document.querySelector("textarea")?.value === "New try");
    expect(moves.at(-1)).toEqual({ messageId: "3:0", boundary: undefined });
    if (narrow) {
      await closed();
      expect(await input.isVisible()).toBe(true);
      expect(await input.inputValue()).toBe("New try");
      await open();
    }

    await row("You Old try").click();
    await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
    expect(moves.at(-1)).toEqual({ messageId: "2:0", boundary: "THROUGH" });
    await page.locator('[data-message-id="2:0"]').first().waitFor();
    if (narrow) {
      await closed();
      await open();
    }
    await page.waitForFunction(() => document.querySelector('[aria-label="Messages and replies"] [aria-current="true"]')?.textContent === "You Old try");

    const buttons = (name: string, count: number) => page.waitForFunction(([name, count]: [string, number]) => document.querySelectorAll(`[aria-label="Messages and replies"] [aria-label="${name}"]`).length === count, [name, count] as [string, number]);
    await buttons("Delete branch", 2);
    await tree.getByRole("listitem").filter({ hasText: "Old try" }).getByRole("button", { name: "Delete branch", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Delete branch" });
    await dialog.getByText("It is the current branch, so the chat moves back to where it splits off, and 2 queued messages will run from there.", { exact: true }).waitFor();
    expect(await dialog.getByRole("listitem").allTextContents()).toEqual(["1 message", "0 nested branches", "1 subagent session", "It is the current branch, so the chat moves back to where it splits off, and 2 queued messages will run from there."]);
    await dialog.getByRole("button", { name: "Delete branch", exact: true }).click();
    await dialog.waitFor({ state: "detached" });
    await row("You Old try").waitFor({ state: "detached" });
    expect(deletes).toEqual([{ messageId: "2:0", dryRun: true }, { messageId: "2:0", dryRun: undefined }]);
    await page.waitForFunction(() => document.querySelector('[aria-label="Messages and replies"] [aria-current="true"]')?.textContent === "Agent First answer");

    const summarize = tree.getByRole("listitem").filter({ hasText: "New try" }).getByRole("button", { name: "Summarize branch", exact: true });
    const requestSummary = async (instructions: string) => {
      const received = nextSummary.promise;
      await summarize.click();
      const dialog = page.getByRole("dialog", { name: "Summarize branch" });
      await dialog.getByRole("textbox", { name: "Instructions (optional)" }).fill(instructions);
      await dialog.getByRole("button", { name: "Summarize", exact: true }).click();
      await dialog.waitFor({ state: "detached" });
      await page.waitForFunction(() => document.querySelector('[aria-label="Messages and replies"] [aria-label="Summarize branch"]')?.hasAttribute("disabled"));
      return received;
    };
    const pickAndReopen = async (name: string) => {
      await row(name).click();
      if (narrow) {
        await closed();
        await open();
      }
      await page.waitForFunction((text: string) => document.querySelector('[aria-label="Messages and replies"] [aria-current="true"]')?.textContent === text, name);
    };
    await buttons("Summarize branch", 1);
    const finishCurrentBranchSummary = await requestSummary(" Keep the numbers ");
    expect(summaries).toEqual([{ branchId: "3:0", instructions: "Keep the numbers" }]);
    await pickAndReopen("Agent New answer");
    expect(moves.at(-1)).toEqual({ messageId: "3:1", boundary: "THROUGH" });
    finishCurrentBranchSummary();
    await page.getByRole("status").filter({ hasText: "Nothing was added: the current path already holds the branch that starts at “New try”." }).waitFor();
    expect(await row("You Summary of New try").count()).toBe(0);

    await pickAndReopen("Agent First answer");
    const finishAppendedSummary = await requestSummary("");
    finishAppendedSummary();
    await page.waitForFunction(() => document.querySelector('[aria-label="Messages and replies"] [aria-current="true"]')?.textContent === "You Summary of New try");
    await page.locator('[data-message-id="9:0"]').first().waitFor();
    expect(await page.getByRole("status").filter({ hasText: "Nothing was added" }).count()).toBe(0);

    const finishFailedSummary = await requestSummary("fail");
    finishFailedSummary();
    await tree.locator("xpath=..").getByRole("alert").filter({ hasText: "Summary model unavailable" }).waitFor();
    expect(await summarize.isEnabled()).toBe(true);
    expect(summaries.map((summary) => summary.instructions)).toEqual(["Keep the numbers", "", "fail"]);

    await open("branches");
    await row("Agent A one answer").waitFor();
    const everything = drawn([["", "You Plan a trip"], ["", "Agent Options: a and b now"], ["├", "You Try A"], ["│", "Agent For A"], ["│├", "You A one"], ["││", "Agent A one answer"], ["│└", "You A two"], ["│ ", "Agent A two answer"], ["└", "You Try B"], [" ", "Agent For B"]]);
    expect(await shape()).toEqual(everything);
    expect(await left("Agent Options: a and b now")).toBe(await left("You Plan a trip"));
    expect(await left("Agent For A")).toBe(await left("You Try A"));
    expect(await left("Agent A two answer")).toBe(await left("You A two"));
    expect(await left("You A one")).toBeGreaterThan(await left("Agent For A"));
    expect(await left("You Try A")).toBeGreaterThan(await left("Agent Options: a and b now"));
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const branchRow = tree.getByRole("listitem").filter({ has: page.getByRole("button", { name: "You A two", exact: true }) });
    const middle = (box: { y: number; height: number }) => box.y + box.height / 2;
    const text = middle((await row("You A two").locator("span").last().boundingBox())!);
    for (const action of ["Summarize branch", "Delete branch"]) expect(Math.abs(middle((await branchRow.getByRole("button", { name: action, exact: true }).boundingBox())!) - text)).toBeLessThanOrEqual(1);
    const foldOptions = tree.getByRole("listitem").filter({ hasText: "Options" }).getByRole("button", { name: "Other branches", exact: true });
    await foldOptions.click();
    const folded = drawn([["", "You Plan a trip"], ["", "Agent Options: a and b now"], ["", "You Try A"], ["", "Agent For A"], ["├", "You A one"], ["│", "Agent A one answer"], ["└", "You A two"], [" ", "Agent A two answer"]]);
    expect(await shape()).toEqual(folded);
    expect(await left("You Try A")).toBe(await left("Agent Options: a and b now"));
    await mine.click();
    expect(await shape()).toEqual(drawn([["", "You Plan a trip"], ["", "You Try A"], ["├", "You A one"], ["└", "You A two"]]));
    await mine.click();
    expect(await shape()).toEqual(folded);
    await foldOptions.click();
    expect(await shape()).toEqual(everything);
    await mine.click();
    expect(await shape()).toEqual(drawn([["", "You Plan a trip"], ["├", "You Try A"], ["│├", "You A one"], ["│└", "You A two"], ["└", "You Try B"]]));
    await mine.click();
    expect(await shape()).toEqual(everything);

    await open("cron");
    await row("Outside Report done").waitFor();
    for (const name of ["You Before report", "Agent Earlier answer", "You Abandoned try", "You Kept try", "Outside Cron report"]) {
      expect(await row(name).getAttribute("aria-disabled")).toBe("true");
      expect(await row(name).getAttribute("title")).toBe(floorReason);
    }
    const before = moves.length;
    await row("You Before report").click({ force: true });
    expect(moves).toHaveLength(before);
    expect(await tree.getByRole("listitem").filter({ hasText: "Abandoned try" }).getByRole("button", { name: "Delete branch" }).count()).toBe(1);
    expect(await tree.getByRole("listitem").filter({ hasText: "Kept try" }).getByRole("button", { name: "Delete branch" }).count()).toBe(0);
    await page.getByRole("button", { name: "Close message tree", exact: true }).click();
    await closed();
    expect(errors).toEqual([]);
  } finally { await browser.close(); server.stop(true); }
}, 60_000);
