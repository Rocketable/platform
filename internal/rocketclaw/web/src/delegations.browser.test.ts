import { expect, test } from "bun:test";
import path from "node:path";
import { timelineLevels } from "./timeline-detail";
import type { CodeModeCall, HistoryView, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type History = { messages: TranscriptEvent[]; delegations: string[]; reviewRationales?: Record<string, string>; lineage?: HistoryView["lineage"] };
const event = (role: string, text: string, tool: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ role, text, complete: true, entryKey: "", itemId: "", inputId: "", turnId: "", ...tool });

function serve(histories: Record<string, History>) {
  return Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", agent: "main" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "delegations" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "delegations" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/History": {
        const history = histories[input.id] ?? { messages: [], delegations: [] };
        const messages = history.messages.map((message, index) => ({ ...message, entryKey: input.id, itemId: `${input.id}:${index}` }));
        return Response.json({ ...history, messages, revision: input.id, reset: true, replacedKeys: [], removedKeys: [], entryKeys: [input.id], running: false, terminal: "", backgroundJobs: [] });
      }
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Tabs": return Response.json({ revision: "1", spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
}

for (const [width, height] of [[1280, 900], [390, 664]]) test(`delegation panel at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const histories: Record<string, History> = {
    // A task row opens the subagent its progress records, also when it continued it, or the one the server linked to an older row.
    // A row's reasons open its permission review only once the delegations list it, or the one the server linked to an older task row.
    chat: { messages: [event("user", "Run the review", { messageId: "1:0" }), event("tool", `task {"description":"Review"}`, { toolName: "task", toolCallId: "call-task", delegation: "chat/call-task-0a1b2c3d", review: "chat/call-task" }), event("tool", `bash {"command":"ls"}`, { toolName: "bash", toolCallId: "call-plain", review: "chat/call-plain-review-9f9f9f9f" }), event("tool", `task {"description":"Again"}`, { toolName: "task", toolCallId: "call-again", delegation: "chat/call-task-0a1b2c3d" }),
      event("tool", `bash {"command":"rm"}`, { toolName: "bash", toolCallId: "call_0", review: "chat/call_0-review-1a2b3c4d" }), event("tool", "blocked", { toolCallId: "call_0" }), event("tool", `task {"description":"Fresh"}`, { toolName: "task", toolCallId: "call-fresh", delegation: "chat/call-fresh-2b3c4d5e", review: "chat/call-fresh-review-3c4d5e6f" }), event("tool", `bash {"command":"old"}`, { toolName: "bash", toolCallId: "call-old", delegation: "chat/call-old" }), event("assistant", "Review finished")],
      delegations: ["chat/call-task", "chat/call-task-0a1b2c3d", "chat/call_0-review-1a2b3c4d", "chat/call-fresh-2b3c4d5e", "chat/call-fresh-review-3c4d5e6f", "chat/call-old"], reviewRationales: { "chat/call-task": "The task is in scope.", "chat/call_0-review-1a2b3c4d": "The requested change is outside the allowed scope.", "chat/call-fresh-review-3c4d5e6f": "The read-only check is within the requested scope." } },
    "chat/call-task": { messages: [event("assistant", "Task call allowed")], delegations: [], lineage: [{ id: "chat" }, { id: "chat/call-task", kind: "SUBAGENT" }] },
    "chat/call_0-review-1a2b3c4d": { messages: [event("assistant", "Bash call allowed")], delegations: [], lineage: [{ id: "chat" }, { id: "chat/call_0-review-1a2b3c4d", kind: "REVIEW" }] },
    "chat/call-task-0a1b2c3d": { messages: [event("user", "Review this"), event("tool", `execute {"code":"print('checking every configured region for configuration drift')"}`, { toolName: "execute", toolCallId: "call-inner", delegation: "chat/call-task-0a1b2c3d/call-inner" }), event("assistant", "Child done")], delegations: ["chat/call-task-0a1b2c3d/call-inner"],
      lineage: [{ id: "chat" }, { id: "chat/call-task-0a1b2c3d", kind: "SUBAGENT", agent: "reviewer" }] },
    "chat/call-task-0a1b2c3d/call-inner": { messages: [event("assistant", "Inner review allowed")], delegations: [], lineage: [{ id: "chat" }, { id: "chat/call-task-0a1b2c3d", kind: "SUBAGENT", agent: "reviewer" }, { id: "chat/call-task-0a1b2c3d/call-inner", kind: "SUBAGENT" }] },
  };
  const server = serve(histories);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replace(/=+$/, "")}?message=1%3A0`);
    const open = page.locator("main").getByRole("link", { name: /^Open delegation/ });
    await page.locator("main").getByText("Review finished", { exact: true }).waitFor();
    // The default Compact level folds both calls into one closed Chain of Thought row.
    await page.locator("main").getByRole("button", { name: "Used 6 tools", exact: true }).click();
    // Auto-scroll can move the next click target while a panel is still growing.
    await page.waitForFunction(() => [...document.querySelectorAll<HTMLElement>('main [data-slot="trace-collapsible-content"][data-open]')].every((panel) => panel.style.getPropertyValue("--collapsible-panel-height") === "auto"));
    for (const title of ["task", "task", "task", "bash · ls", "bash · rm", "bash · old"]) {
      await page.locator("main").getByRole("button", { name: title, exact: true, expanded: false }).first().click();
      await page.waitForFunction(() => [...document.querySelectorAll<HTMLElement>('main [data-slot="trace-collapsible-content"][data-open]')].every((panel) => panel.style.getPropertyValue("--collapsible-panel-height") === "auto"));
    }
    const href = async (link: { getAttribute(name: string): Promise<string | null> }) => new URL(await link.getAttribute("href") ?? "", page.url()).searchParams.get("delegation");
    expect(await open.count()).toBe(4);
    expect(await href(open.nth(1))).toBe("chat/call-task-0a1b2c3d");
    expect(await href(open.nth(2))).toBe("chat/call-fresh-2b3c4d5e");
    expect(await href(page.getByRole("link", { name: "Open delegation: bash · old", exact: true }))).toBe("chat/call-old");
    const plain = page.locator('main [data-slot="trace-collapsible"]:has(> [data-slot="trace-collapsible-trigger"][title="bash · ls"])');
    await plain.waitFor();
    expect(await plain.getByRole("link").count()).toBe(0);
    const panel = width >= 1024 ? page.getByRole("complementary", { name: "Delegation", exact: true }) : page.getByRole("dialog", { name: "Delegation", exact: true });
    const crumbs = panel.getByRole("navigation", { name: "Delegation breadcrumbs" });
    const delegation = () => new URL(page.url()).searchParams.get("delegation");
    // A review opens from the reason under its call's box, not from a row of its own.
    const review = page.locator("main").getByRole("link", { name: "Open permission review", exact: true });
    expect(await review.count()).toBe(3);
    const denied = page.locator('main [data-tool-call-id="call_0"]');
    const allowed = page.locator('main [data-tool-call-id="call-fresh"]');
    const reason = '[data-slot="code-block"] + footer[data-slot="permission-review-rationale"]';
    expect(await denied.locator(reason).textContent()).toBe("The requested change is outside the allowed scope.");
    expect(await allowed.locator(reason).textContent()).toBe("The read-only check is within the requested scope.");
    expect(await page.locator('main [data-slot="code-block"] [data-slot="permission-review-rationale"]').count()).toBe(0);
    expect(await plain.locator('[data-slot="permission-review-rationale"]').count()).toBe(0);
    expect(await href(allowed.getByRole("link", { name: "Open permission review", exact: true }))).toBe("chat/call-fresh-review-3c4d5e6f");
    await page.locator('main [data-tool-call-id="call-task"]').first().getByRole("link", { name: "Open permission review", exact: true }).click();
    await panel.getByText("Task call allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task");
    await page.goBack();
    await panel.waitFor({ state: "detached" });
    await denied.getByRole("link", { name: "Open permission review", exact: true }).click();
    await panel.getByText("Bash call allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call_0-review-1a2b3c4d");
    expect(new URL(page.url()).searchParams.get("reason")).toBe("The requested change is outside the allowed scope.");
    expect(await crumbs.locator('[aria-current="page"]').textContent()).toBe("bash · rm");
    await page.goBack();
    await panel.waitFor({ state: "detached" });

    await open.and(page.getByRole("link", { name: "Open delegation: task", exact: true })).first().click();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task-0a1b2c3d");
    expect(new URL(page.url()).searchParams.get("message")).toBe("1:0");
    expect(await page.getByRole("dialog").count()).toBe(width >= 1024 ? 0 : 1);
    await page.goBack();
    await panel.waitFor({ state: "detached" });
    expect(delegation()).toBe(null);
    await page.goForward();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(await panel.getByRole("link", { name: /^Open permission review/ }).count()).toBe(0);

    await panel.getByRole("link", { name: "Open delegation: Run · print('checking every configured region for configuration drift')", exact: true }).click();
    await panel.getByText("Inner review allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task-0a1b2c3d/call-inner");
    expect(await crumbs.getByRole("link").allTextContents()).toEqual(["Conversation", "task"]);
    expect(await crumbs.locator('[aria-current="page"]').textContent()).toBe("Run · print('checking every configured region for configuration drift')");
    // A long last step shortens first; the steps before it stay whole.
    expect(await crumbs.getByRole("link", { name: "task", exact: true }).evaluate((link: HTMLElement) => link.scrollWidth <= link.clientWidth && link.clientWidth > 0)).toBe(true);
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/delegation-panel-${width}.png`), await page.screenshot());
    await panel.getByRole("link", { name: "Back to task", exact: true }).click();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task-0a1b2c3d");
    await page.goBack();
    await panel.getByText("Inner review allowed", { exact: true }).waitFor();

    await crumbs.getByRole("link", { name: "task", exact: true }).click();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task-0a1b2c3d");
    expect(await crumbs.locator('[aria-current="page"]').textContent()).toBe("task");
    await page.reload();
    await panel.getByText("Child done", { exact: true }).waitFor();
    expect(await crumbs.getByRole("link").allTextContents()).toEqual(["Conversation"]);
    await panel.getByRole("button", { name: "Close delegation", exact: true }).click();
    await panel.waitFor({ state: "detached" });
    expect(delegation()).toBe(null);
    expect(new URL(page.url()).searchParams.get("message")).toBe("1:0");
    await page.goBack();
    await panel.getByText("Child done", { exact: true }).waitFor();
    if (width >= 1024) {
      const handle = page.getByRole("separator", { name: "Resize delegation panel", exact: true });
      const size = async () => Math.round((await panel.boundingBox())!.width);
      const drag = async (by: number) => {
        const box = (await handle.boundingBox())!;
        await page.mouse.move(box.x + box.width / 2, box.y + 50);
        await page.mouse.down();
        await page.mouse.move(Math.min(Math.max(box.x + box.width / 2 + by, 0), width - 1), box.y + 50, { steps: 4 });
        await page.mouse.up();
      };
      expect(await size()).toBe(384);
      await drag(-100);
      const dragged = await size();
      expect(Math.abs(dragged - 384 - 100)).toBeLessThan(2);
      await handle.press("ArrowLeft");
      expect(await size()).toBe(dragged + 16);
      await handle.press("ArrowRight");
      expect(await size()).toBe(dragged);
      await drag(-2000);
      const widest = await size();
      const chat = Math.round((await page.locator("main").boundingBox())!.width);
      expect(chat).toBeGreaterThanOrEqual(416);
      expect(widest === 768 || chat === 416).toBe(true);
      expect(await handle.getAttribute("aria-valuenow")).toBe(String(widest));
      await page.reload();
      await panel.getByText("Child done", { exact: true }).waitFor();
      expect(await size()).toBe(widest);
      await drag(2000);
    }
    await panel.getByRole("link", { name: "Back to conversation", exact: true }).click();
    await panel.waitFor({ state: "detached" });
    expect(delegation()).toBe(null);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

for (const [width, height] of [[1280, 900], [390, 664]]) test(`code mode blocks at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const call = (name: string, input: object | string, extra: Partial<CodeModeCall> = {}): CodeModeCall => ({ name, arguments: typeof input === "string" ? input : JSON.stringify(input), status: "ok", ...extra });
  const script = (id: string, input: object, extra: Partial<TranscriptEvent> = {}) => event("tool", `execute ${JSON.stringify(input)}`, { toolName: "execute", toolCallId: id, ...extra });
  const result = (id: string, text: string, extra: Partial<TranscriptEvent> = {}) => event("tool", text, { toolCallId: id, ...extra });
  // A review whose action cannot be read back, or one a background script saved after its turn ended,
  // has a reason but no row; it follows the block's result.
  // The script's review saves one answer per check under the check's turn, each after a long prompt.
  const answer = (id: number, rationale: string, turnId: string) => [event("user", `Planned action ${id}\n${"Transcript line.\n".repeat(60)}`, { messageId: `${id}:0`, turnId }), event("assistant", JSON.stringify({ risk_level: "low", user_authorization: "high", outcome: "allow", rationale }), { messageId: `${id}:1`, turnId })];
  const server = serve({ "chat/call-a-review-1": { delegations: [], messages: [
    ...answer(20, "The script only reads the diff.", "t-script"), ...answer(21, "Reading the diff is in scope.", "t-read"), ...answer(22, "Deleting files is out of scope.", "t-rm"),
    ...answer(23, "The late cleanup is in scope.", "t-late"), ...answer(24, "Reading the diff is in scope.", "t-diff"),
  ] }, chat: { delegations: ["chat/call-a-review-1", "chat/call-c-review-2"], reviewRationales: {
    "chat/call-a-review-1": "The script only reads the diff.\nReading the diff is in scope.\nDeleting files is out of scope.\nThe late cleanup is in scope.\nReading the diff is in scope.",
    "chat/call-c-review-2": "Listing files is read-only.",
  }, messages: [
    event("user", "Check the diff", { messageId: "1:0" }),
    script("call-a", { code: 'def main():\n    print("checked")', description: "Validate PR diff" }, { review: "chat/call-a-review-1", reviewTurn: "t-script", scriptReason: "The script only reads the diff.", scriptOutcome: "allow", codeModeCalls: [
      call("bash", { command: "cat <<'EOF'\nline one\nline two\nEOF" }, { outcome: "allow", reason: "Reading the diff is in scope.", reviewTurn: "t-read" }),
      call("bash", { command: "git diff" }, { outcome: "allow", reason: "Reading the diff is in scope.", reviewTurn: "t-diff" }),
      call("bash", { command: "rm -rf build" }, { status: "denied", outcome: "deny", reason: "Deleting files is out of scope.", reviewTurn: "t-rm" }),
      call("bash", { command: "rm go.sum" }, { status: "denied" }),
      call("grep", { pattern: "TODO" }, { status: "error" }),
      call("read", '{"filePath":"go.mod","limit":1…[truncated]'),
    ] }),
    result("call-a", JSON.stringify(["<path>go.mod</path>\nmodule x", "checked"])),
    script("call-b", { code: 'def main():\n    bash(command="sleep 600")' }, { state: "background", codeModeCalls: [call("bash", { command: "sleep 600" }, { status: "running" })] }),
    result("call-b", "moved", { state: "background" }),
    script("call-c", { code: 'def main():\n    return bash(command="ls")' }, { review: "chat/call-c-review-2" }),
    result("call-c", "tool call failed: boom"),
    script("call-d", { code: "def main():\n    pass", description: "Emit many" }, { codeModeCalls: Array.from({ length: 200 }, () => call("emit", { text: "x" })) }),
    result("call-d", "emitted"),
    script("call-e", { code: "def main():\n    pass", description: "Still testing" }, { state: "working", scriptReason: "Running tests is in scope.", codeModeCalls: [call("bash", { command: "make test" }, { status: "running" })] }),
    event("assistant", "Done"),
    event("user", "Check another action"),
    script("call-blocked", { code: "blocked_action()", description: "Blocked action" }),
    result("call-blocked", "blocked", { state: "blocked" }),
    event("assistant", "Action denied"),
  ] } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const level = async (id: string) => {
      await page.evaluate((rows: string) => localStorage.setItem("timeline-detail", rows), JSON.stringify({ version: 1, rows: timelineLevels.find((level) => level.id === id)!.rows }));
      await page.reload();
      await page.locator("main").getByText("Done", { exact: true }).waitFor();
    };
    const block = (id: string) => page.locator(`main [data-tool-call-id="${id}"]`);
    const trigger = (id: string) => block(id).locator(':scope > [data-slot="trace-collapsible-trigger"]');
    const row = (id: string, title: string) => block(id).locator(`[data-slot="trace-collapsible"]:has(> [data-slot="trace-collapsible-trigger"][title="${title.replaceAll('"', '\\"')}"])`);
    const a = block("call-a");
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replace(/=+$/, "")}`);
    await page.locator("main").getByText("Done", { exact: true }).waitFor();

    // Compact: the description titles the block, and its reasons follow its result; the script is not in the page.
    expect(await trigger("call-a").getAttribute("title")).toBe("Validate PR diff");
    expect(await trigger("call-c").getAttribute("title")).toBe("Run · ls");
    // Only the script reason and reasons no row shows follow the result, one footer each.
    const reasons = (id: string) => block(id).locator('[data-slot="code-block"]:has(> [aria-label="Result"]) ~ footer[data-slot="permission-review-rationale"]');
    expect(await reasons("call-a").allTextContents()).toEqual(["Allowed: The script only reads the diff.", "The late cleanup is in scope."]);
    expect(await reasons("call-c").allTextContents()).toEqual(["Listing files is read-only."]);
    expect(await reasons("call-d").count()).toBe(0);
    // A script still running has no result yet; its reason stands alone.
    expect(await block("call-e").locator('footer[data-slot="permission-review-rationale"]').allTextContents()).toEqual(["Running tests is in scope."]);
    expect(await a.textContent()).not.toContain('print("checked")');
    expect(await a.locator('[aria-label="Script"]').count()).toBe(0);

    // Detailed: each call is one row; the script waits behind its toggle.
    await level("detailed");
    // A returned list of strings shows as its text, not as escaped JSON.
    expect(await a.locator('[aria-label="Result"]').innerText()).toBe("<path>go.mod</path>\nmodule x\n\nchecked");
    expect(await a.locator('[aria-label="Script"]').count()).toBe(0);
    await a.getByRole("button", { name: "Show script", exact: true }).click();
    await a.locator('[aria-label="Script"] span[style*="--shiki"]').first().waitFor();
    expect(await a.locator('[aria-label="Script"]').innerText()).toBe('def main():\n    print("checked")');
    const heredoc = row("call-a", "bash · cat <<'EOF' line one line two EOF");
    expect(await heredoc.locator('[data-slot="trace-line"]').innerText()).toBe("bash · cat <<'EOF' line one line two EOF");
    await heredoc.locator('> [data-slot="trace-collapsible-trigger"]').click();
    await heredoc.locator('[aria-label="bash"] span[style*="--shiki"]').first().waitFor();
    expect(await heredoc.locator('[aria-label="bash"]').innerText()).toBe("cat <<'EOF'\nline one\nline two\nEOF");
    expect(await heredoc.locator('[data-slot="code-block"] + [data-slot="permission-review-rationale"]').innerText()).toBe("Allowed: Reading the diff is in scope.");
    const denied = row("call-a", "bash · rm -rf build");
    expect(await denied.locator('[data-slot="trace-line"]').getAttribute("data-status")).toBe("error");
    expect(await denied.locator('[data-slot="trace-line"]').innerText()).toBe("bash · rm -rf build\ndenied");
    expect(await denied.locator('[data-slot="permission-review-rationale"]').textContent()).toBe("Denied: Deleting files is out of scope.");
    const rule = row("call-a", "bash · rm go.sum");
    expect(await rule.locator('[data-slot="trace-line"]').innerText()).toBe("bash · rm go.sum\ndenied");
    expect(await rule.locator('[data-slot="permission-review-rationale"]').count()).toBe(0);
    const failed = row("call-a", "grep · TODO");
    expect(await failed.locator('[data-slot="trace-line"]').getAttribute("data-status")).toBe("error");
    expect(await failed.locator('[data-slot="trace-line"]').innerText()).toBe("grep · TODO\nfailed");
    const clipped = row("call-a", 'read · {"filePath":"go.mod","limit":1…[truncated]');
    expect(await clipped.locator('[aria-label="read"]').textContent()).toBe('{"filePath":"go.mod","limit":1…[truncated]');
    expect(await clipped.locator('[data-slot="permission-review-rationale"]').count()).toBe(0);
    expect(await block("call-b").getByText("Moved to the background. Its result arrives in a Completion Note.", { exact: true }).isVisible()).toBe(true);
    const waiting = row("call-b", "bash · sleep 600").locator('[data-slot="trace-line"]');
    expect(await waiting.innerText()).toBe("bash · sleep 600");
    expect(await waiting.getAttribute("data-status")).toBe(null);
    const running = row("call-e", "bash · make test").locator('[data-slot="trace-line"]');
    expect(await running.getAttribute("data-status")).toBe("running");
    expect(await running.innerText()).toBe("bash · make test");
    expect(await block("call-d").getByText("Showing the first 200 calls; the script may have made more.", { exact: true }).isVisible()).toBe(true);
    expect(await block("call-c").getByText("Showing the first 200 calls; the script may have made more.", { exact: true }).count()).toBe(0);
    await a.scrollIntoViewIfNeeded();
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/u4/code-mode-${width}.png`), await page.screenshot());

    // A reason is one line until clicked.
    const late = reasons("call-a").last().getByRole("button");
    expect(await late.evaluate((node: Element) => getComputedStyle(node).whiteSpace)).toBe("nowrap");
    await late.click();
    expect(await late.getAttribute("aria-expanded")).toBe("true");
    expect(await late.evaluate((node: Element) => getComputedStyle(node).whiteSpace)).toBe("pre-wrap");

    // A call's reason opens the script's review at the approver's answer on that call, with the arrow to its end.
    const panel = width >= 1024 ? page.getByRole("complementary", { name: "Delegation", exact: true }) : page.getByRole("dialog", { name: "Delegation", exact: true });
    const viewport = '[data-delegation-transcript]';
    const atEnd = (end: boolean) => page.waitForFunction(([selector, end]: [string, boolean]) => {
      const view = document.querySelector(selector)?.closest<HTMLElement>('[data-slot="message-scroller-viewport"]');
      return !!view && view.scrollHeight > view.clientHeight && (view.scrollHeight - view.clientHeight - view.scrollTop <= 1) === end;
    }, [viewport, end]);
    const shown = (id: string) => page.waitForFunction(([selector, id]: [string, string]) => {
      const node = document.querySelector(`${selector} [data-message-id="${id}"]`), view = node?.closest('[data-slot="message-scroller-viewport"]');
      if (!node || !view) return false;
      const box = node.getBoundingClientRect(), bounds = view.getBoundingClientRect();
      return box.top >= bounds.top && box.bottom <= bounds.bottom;
    }, [viewport, id]);
    const params = () => Object.fromEntries(["delegation", "reason", "answer"].map((name) => [name, new URL(page.url()).searchParams.get(name)]));
    // Two calls with the same reason each open their own answer.
    await heredoc.getByRole("link", { name: "Open permission review", exact: true }).click();
    expect(params()).toEqual({ delegation: "chat/call-a-review-1", reason: "Reading the diff is in scope.", answer: "t-read" });
    await shown("21:1");
    await page.goBack();
    await panel.waitFor({ state: "detached" });
    const diff = row("call-a", "bash · git diff");
    await diff.locator('> [data-slot="trace-collapsible-trigger"]').click();
    await diff.getByRole("link", { name: "Open permission review", exact: true }).click();
    expect(params()).toEqual({ delegation: "chat/call-a-review-1", reason: "Reading the diff is in scope.", answer: "t-diff" });
    await shown("24:1");
    await page.goBack();
    await panel.waitFor({ state: "detached" });
    // A reason with no review turn, as saved before turns were named, opens at the first answer giving it.
    await reasons("call-a").last().getByRole("link", { name: "Open permission review", exact: true }).click();
    expect(params()).toEqual({ delegation: "chat/call-a-review-1", reason: "The late cleanup is in scope.", answer: null });
    await shown("23:1");
    await atEnd(false);
    const latest = panel.getByRole("button", { name: "Scroll to latest", exact: true });
    await latest.and(panel.locator('[data-active="true"]')).waitFor();
    await latest.click();
    await atEnd(true);
    // Opened without an answer, it starts at its end.
    const plainReview = new URL(page.url());
    plainReview.searchParams.delete("reason");
    plainReview.searchParams.delete("answer");
    await page.goto(plainReview.href);
    await panel.getByText("Planned action 23", { exact: false }).waitFor();
    await atEnd(true);
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replace(/=+$/, "")}`);

    // Everything shows the script without a toggle.
    await level("everything");
    await a.locator('[aria-label="Script"]').waitFor();
    expect(await a.getByRole("button", { name: "Show script", exact: true }).count()).toBe(0);

    // Quiet hides a successful block; a failed one still shows, with the toggle.
    await level("quiet");
    await block("call-c").waitFor();
    expect(await a.count()).toBe(0);
    expect(await block("call-c").locator('[aria-label="Script"]').count()).toBe(0);
    await block("call-c").getByRole("button", { name: "Show script", exact: true }).click();
    expect(await block("call-c").locator('[aria-label="Script"]').innerText()).toBe('def main():\n    return bash(command="ls")');
    await level("messages");
    await block("call-c").waitFor({ state: "detached" });
    await block("call-blocked").waitFor({ state: "detached" });
    const failure = page.locator("main").getByRole("status").filter({ hasText: "A tool failed" });
    expect(await failure.count()).toBe(2);
    expect(await failure.first().getAttribute("title")).toBe("Increase timeline detail in the command palette to see tool details.");
    expect(await page.locator("main").getByText("tool call failed: boom", { exact: true }).count()).toBe(0);
    expect((await failure.first().boundingBox())!.height).toBeLessThan(32);
    // Raising the level through the palette restores the failure's full panel.
    await page.keyboard.press("Control+Shift+p");
    await page.getByPlaceholder("Type a command").fill("Timeline:");
    await page.getByRole("button", { name: /^Timeline: Detailed/ }).click();
    await block("call-c").getByText("tool call failed: boom", { exact: true }).waitFor();
    await block("call-blocked").locator('[aria-label="Result"]').getByText("blocked", { exact: true }).waitFor();
    expect(await failure.count()).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
