import { expect, test } from "bun:test";
import path from "node:path";
import { timelineLevels } from "./timeline-detail";
import type { CodeModeCall, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type History = { messages: TranscriptEvent[]; delegations: string[]; reviewRationales?: Record<string, string> };
const event = (role: string, text: string, tool: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ role, text, complete: true, entryKey: "", itemId: "", inputId: "", turnId: "", ...tool });

function serve(histories: Record<string, History>) {
  return Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", agent: "main" }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
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
      default: return Response.json({});
    }
  } });
}

for (const [width, height] of [[1280, 900], [390, 664]]) test(`delegation panel at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const histories: Record<string, History> = {
    // A task row opens the subagent its progress records, also when it continued it; an older row without one opens the listed delegation ending in its call ID.
    // A row opens its permission review only once the delegations list it; an older task row opens the one saved under its call ID.
    chat: { messages: [event("user", "Run the review", { messageId: "1:0" }), event("tool", `task {"description":"Review"}`, { toolName: "task", toolCallId: "call-task", delegation: "chat/call-task-0a1b2c3d" }), event("tool", `bash {"command":"ls"}`, { toolName: "bash", toolCallId: "call-plain", review: "chat/call-plain-review-9f9f9f9f" }), event("tool", `task {"description":"Again"}`, { toolName: "task", toolCallId: "call-again", delegation: "chat/call-task-0a1b2c3d" }),
      event("tool", `bash {"command":"rm"}`, { toolName: "bash", toolCallId: "call_0", review: "chat/call_0-review-1a2b3c4d" }), event("tool", "blocked", { toolCallId: "call_0" }), event("tool", `task {"description":"Fresh"}`, { toolName: "task", toolCallId: "call-fresh", delegation: "chat/call-fresh-2b3c4d5e", review: "chat/call-fresh-review-3c4d5e6f" }), event("tool", `bash {"command":"old"}`, { toolName: "bash", toolCallId: "call-old" }), event("assistant", "Review finished")],
      delegations: ["chat/call-task", "chat/call-task-0a1b2c3d", "chat/call_0-review-1a2b3c4d", "chat/call-fresh-2b3c4d5e", "chat/call-fresh-review-3c4d5e6f", "chat/call-old"], reviewRationales: { "chat/call_0-review-1a2b3c4d": "The requested change is outside the allowed scope.", "chat/call-fresh-review-3c4d5e6f": "The read-only check is within the requested scope." } },
    "chat/call-task": { messages: [event("assistant", "Task call allowed")], delegations: [] },
    "chat/call_0-review-1a2b3c4d": { messages: [event("assistant", "Bash call allowed")], delegations: [] },
    "chat/call-task-0a1b2c3d": { messages: [event("user", "Review this"), event("tool", `execute {"code":"print(1)"}`, { toolName: "execute", toolCallId: "call-inner" }), event("assistant", "Child done")], delegations: ["chat/call-task-0a1b2c3d/call-inner"] },
    "chat/call-task-0a1b2c3d/call-inner": { messages: [event("assistant", "Inner review allowed")], delegations: [] },
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
    for (const title of ["task", "task", "task", "bash · ls", "bash · rm", "bash · old"]) await page.locator("main").getByRole("button", { name: title, exact: true, expanded: false }).first().click();
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
    const review = page.locator("main").getByRole("link", { name: /^Open permission review/ });
    expect(await review.count()).toBe(3);
    const denied = page.locator('main [data-tool-call-id="call_0"]');
    const allowed = page.locator('main [data-tool-call-id="call-fresh"]');
    expect(await denied.locator('[data-slot="code-block"] > [data-slot="permission-review-rationale"]').textContent()).toBe("The requested change is outside the allowed scope.");
    expect(await allowed.locator('[data-slot="code-block"] > [data-slot="permission-review-rationale"]').textContent()).toBe("The read-only check is within the requested scope.");
    expect(await plain.locator('[data-slot="permission-review-rationale"]').count()).toBe(0);
    expect(await href(review.nth(2))).toBe("chat/call-fresh-review-3c4d5e6f");
    await review.and(page.getByRole("link", { name: "Open permission review: task", exact: true })).first().click();
    await panel.getByText("Task call allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task");
    await page.goBack();
    await panel.waitFor({ state: "detached" });
    await review.and(page.getByRole("link", { name: "Open permission review: bash · rm", exact: true })).click();
    await panel.getByText("Bash call allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call_0-review-1a2b3c4d");
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

    await panel.getByRole("link", { name: "Open delegation: Run · print(1)", exact: true }).click();
    await panel.getByText("Inner review allowed", { exact: true }).waitFor();
    expect(delegation()).toBe("chat/call-task-0a1b2c3d/call-inner");
    expect(await crumbs.getByRole("link").allTextContents()).toEqual(["Conversation", "task"]);
    expect(await crumbs.locator('[aria-current="page"]').textContent()).toBe("Run · print(1)");
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
  // has a reason but no row; it shows in the block's footer.
  const server = serve({ chat: { delegations: ["chat/call-a-review-1", "chat/call-c-review-2"], reviewRationales: {
    "chat/call-a-review-1": "The script only reads the diff.\nReading the diff is in scope.\nDeleting files is out of scope.\nThe late cleanup is in scope.",
    "chat/call-c-review-2": "Listing files is read-only.",
  }, messages: [
    event("user", "Check the diff", { messageId: "1:0" }),
    script("call-a", { code: 'def main():\n    print("checked")', description: "Validate PR diff" }, { review: "chat/call-a-review-1", scriptReason: "The script only reads the diff.", codeModeCalls: [
      call("bash", { command: "cat <<'EOF'\nline one\nline two\nEOF" }, { summary: "line two", outcome: "allow", reason: "Reading the diff is in scope." }),
      call("bash", { command: "rm -rf build" }, { status: "denied", summary: "denied by the approver", outcome: "deny", reason: "Deleting files is out of scope." }),
      call("bash", { command: "rm go.sum" }, { status: "denied", summary: "denied by a rule" }),
      call("grep", { pattern: "TODO" }, { status: "error", summary: "exit status 2" }),
      call("read", '{"filePath":"go.mod","limit":1…[truncated]', { summary: "(End of file - total 3 lines)" }),
    ] }),
    result("call-a", JSON.stringify(["<path>go.mod</path>\nmodule x", "checked"])),
    script("call-b", { code: 'def main():\n    bash(command="sleep 600")' }, { state: "background", codeModeCalls: [call("bash", { command: "sleep 600" }, { status: "running" })] }),
    result("call-b", "moved", { state: "background" }),
    script("call-c", { code: 'def main():\n    return bash(command="ls")' }, { review: "chat/call-c-review-2" }),
    result("call-c", "tool call failed: boom"),
    script("call-d", { code: "def main():\n    pass", description: "Emit many" }, { codeModeCalls: Array.from({ length: 200 }, () => call("emit", { text: "x" }, { summary: "x" })) }),
    result("call-d", "emitted"),
    script("call-e", { code: "def main():\n    pass", description: "Still testing" }, { state: "working", codeModeCalls: [call("bash", { command: "make test" }, { status: "running" })] }),
    event("assistant", "Done"),
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

    // Compact: the description titles the block, and its reason comes before its calls; the script is not in the page.
    expect(await trigger("call-a").getAttribute("title")).toBe("Validate PR diff");
    expect(await trigger("call-c").getAttribute("title")).toBe("Run · ls");
    const text = (await a.textContent())!;
    expect(text.indexOf("The script only reads the diff.")).toBeGreaterThanOrEqual(0);
    expect(text.indexOf("The script only reads the diff.")).toBeLessThan(text.indexOf("line two"));
    expect(text).not.toContain('print("checked")');
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
    expect(await heredoc.locator('[data-slot="trace-line"]').innerText()).toBe("bash · cat <<'EOF' line one line two EOF\nline two\nallowed");
    await heredoc.locator('> [data-slot="trace-collapsible-trigger"]').click();
    await heredoc.locator('[aria-label="bash"] span[style*="--shiki"]').first().waitFor();
    expect(await heredoc.locator('[aria-label="bash"]').innerText()).toBe("cat <<'EOF'\nline one\nline two\nEOF");
    expect(await heredoc.locator('[data-slot="permission-review-rationale"]').innerText()).toBe("Allowed: Reading the diff is in scope.");
    const denied = row("call-a", "bash · rm -rf build");
    expect(await denied.locator('[data-slot="trace-line"]').getAttribute("data-status")).toBe("error");
    expect(await denied.locator('[data-slot="trace-line"]').innerText()).toBe("bash · rm -rf build\ndenied by the approver\ndenied");
    expect(await denied.locator('[data-slot="permission-review-rationale"]').textContent()).toBe("Denied: Deleting files is out of scope.");
    const rule = row("call-a", "bash · rm go.sum");
    expect(await rule.locator('[data-slot="trace-line"]').innerText()).toBe("bash · rm go.sum\ndenied by a rule\ndenied");
    expect(await rule.locator('[data-slot="permission-review-rationale"]').count()).toBe(0);
    const failed = row("call-a", "grep · TODO");
    expect(await failed.locator('[data-slot="trace-line"]').getAttribute("data-status")).toBe("error");
    expect(await failed.locator('[data-slot="trace-line"]').innerText()).toBe("grep · TODO\nexit status 2\nfailed");
    const clipped = row("call-a", 'read · {"filePath":"go.mod","limit":1…[truncated]');
    expect(await clipped.locator('[aria-label="read"]').textContent()).toBe('{"filePath":"go.mod","limit":1…[truncated]');
    expect(await clipped.locator('[data-slot="permission-review-rationale"]').count()).toBe(0);
    // Only reasons no row or script reason shows reach the footer.
    const footer = (id: string) => block(id).locator('footer[data-slot="permission-review-rationale"]:not([data-slot="code-block"] > *)');
    expect(await footer("call-a").innerText()).toBe("The late cleanup is in scope.");
    expect(await footer("call-c").innerText()).toBe("Listing files is read-only.");
    expect(await footer("call-d").count()).toBe(0);
    expect(await block("call-b").getByText("Moved to the background. Its result arrives in a Completion Note.", { exact: true }).isVisible()).toBe(true);
    const waiting = row("call-b", "bash · sleep 600").locator('[data-slot="trace-line"]');
    expect(await waiting.innerText()).toBe("bash · sleep 600\nstill running when moved to the background");
    expect(await waiting.getAttribute("data-status")).toBe(null);
    const running = row("call-e", "bash · make test").locator('[data-slot="trace-line"]');
    expect(await running.getAttribute("data-status")).toBe("running");
    expect(await running.innerText()).toBe("bash · make test\nrunning");
    expect(await block("call-d").getByText("Showing the first 200 calls; the script may have made more.", { exact: true }).isVisible()).toBe(true);
    expect(await block("call-c").getByText("Showing the first 200 calls; the script may have made more.", { exact: true }).count()).toBe(0);
    await a.scrollIntoViewIfNeeded();
    await Bun.write(path.resolve(import.meta.dir, `../../../../.tmp/u4/code-mode-${width}.png`), await page.screenshot());

    // Everything shows the script without a toggle.
    await level("everything");
    await a.locator('[aria-label="Script"]').waitFor();
    expect(await a.getByRole("button", { name: "Show script", exact: true }).count()).toBe(0);

    // Messages hides a successful block; a failed one still shows, with the toggle.
    await level("messages");
    await block("call-c").waitFor();
    expect(await a.count()).toBe(0);
    expect(await block("call-c").locator('[aria-label="Script"]').count()).toBe(0);
    await block("call-c").getByRole("button", { name: "Show script", exact: true }).click();
    expect(await block("call-c").locator('[aria-label="Script"]').innerText()).toBe('def main():\n    return bash(command="ls")');
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
