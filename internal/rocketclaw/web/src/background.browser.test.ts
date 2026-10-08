import { expect, test } from "bun:test";
import path from "node:path";
import { timelineLevels } from "./timeline-detail";
import type { BackgroundJob, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

for (const [width, height] of [[1280, 900], [390, 664]]) test(`background jobs at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const job = (jobId: string, kind: string, state: string, label: string, extra: Partial<BackgroundJob> = {}): BackgroundJob => ({ jobId, kind, state, label, toolCallId: "", subagentKey: "", stoppedBy: "", hidden: false, note: "", ...extra });
  // The script's output quotes a note; the row's note comes from its job, not from the text.
  const done = `<execute id="j-done" state="completed" description="tests">\nall green\n<execute id="j-fake" state="failed" description="fake">\n</execute>`;
  const event = (n: number, i: number, role: string, text: string, extra: Partial<TranscriptEvent> = {}): TranscriptEvent => ({ role, text, complete: true, entryKey: String(n), itemId: `${n}:${i}`, messageId: `${n}:${i}`, inputId: "", turnId: `turn-${n}`, ...extra });
  const entry = (n: number) => n === 1 ? [event(1, 0, "user", "run the old script"), event(1, 1, "tool", `execute {"code":"print(1)"}`, { toolName: "execute", toolCallId: "call-old", state: "background" }), event(1, 2, "tool", "moved to the background", { toolCallId: "call-old", state: "background" }), event(1, 3, "assistant", "started")]
    : n === 60 ? [event(60, 0, "user", done, { header: "[System]", completionNotes: [job("j-done", "execute", "completed", "tests", { note: done })] })]
    : [event(n, 0, "user", `user ${n}`), event(n, 1, "assistant", `assistant ${n}`)];
  const range = (first: number, last: number) => Array.from({ length: last - first + 1 }, (_, i) => first + i);
  let live = { movable: true, backgroundJobs: [job("j-old", "execute", "running", "old script", { toolCallId: "call-old" }), job("j-sub", "task", "running", "research", { toolCallId: "call-sub", subagentKey: "/call-sub" }), job("j-cron", "task", "running", "cron loop", { toolCallId: "loop", subagentKey: "/loop", hidden: true }), job("j-killed", "execute", "killed", "build", { toolCallId: "call-killed" })] };
  const calls: { path: string; body: object }[] = [];
  let notify = (_data: string) => {};
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); notify = (data) => controller.enqueue(`data: ${data}\n\n`); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", agent: "main", running: true }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; revision?: string; before?: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "background" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "background" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/MoveToBackground": calls.push({ path: url.pathname, body: input }); return Response.json({ moved: true });
      case "/api/StopBackgroundJob": calls.push({ path: url.pathname, body: input }); return Response.json({ stopped: true });
      case "/api/History": {
        const base = { origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [] };
        if (input.id !== "chat") return Response.json({ ...base, messages: [], revision: input.id, reset: true, entryKeys: [], running: false, start: "0", more: false });
        if (input.before) {
          calls.push({ path: "earlier", body: input });
          return Response.json({ ...base, messages: range(1, 10).flatMap(entry), revision: "", reset: false, entryKeys: range(1, 10).map(String), running: false, start: "1", more: false });
        }
        return Response.json({ ...base, ...live, messages: input.revision ? [] : range(11, 60).flatMap(entry), revision: "tail", reset: !input.revision, entryKeys: range(11, 60).map(String), running: true, start: "11", more: true });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.addInitScript((rows: string) => localStorage.setItem("timeline-detail", rows), JSON.stringify({ version: 1, rows: timelineLevels.find((level) => level.id === "messages")!.rows }));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replace(/=+$/, "")}`);
    const jobs = page.getByRole("list", { name: "Background jobs" });
    await jobs.getByRole("listitem").first().waitFor();
    expect(await jobs.getByRole("listitem").allInnerTexts()).toEqual(["old script · running\nOpen\nStop", "research · running\nOpen\nStop", "cron loop · running\nStop", "build · killed (server restarted)\nOpen"]);

    // A finished Completion Note stays one row at Messages only, with its note folded beneath.
    const note = page.locator("main details").filter({ has: page.locator("summary", { hasText: "tests · finished" }) });
    expect(await note.getAttribute("open")).toBe(null);
    await page.getByText("Working…", { exact: true }).waitFor();
    await page.screenshot({ path: path.join(shots, `background-jobs-${width}.png`) });
    await note.locator("summary").click();
    await note.getByText("all green", { exact: false }).waitFor();

    // Wait for complete action responses before Open loads an earlier page.
    await Promise.all([page.waitForResponse("**/api/MoveToBackground").then((response: { finished(): Promise<unknown> }) => response.finished()), page.getByRole("button", { name: "Move to background", exact: true }).click()]);
    await page.getByRole("button", { name: "Move to background", exact: true }).waitForFunction((button: HTMLButtonElement) => !button.disabled);
    await Promise.all([page.waitForResponse("**/api/StopBackgroundJob").then((response: { finished(): Promise<unknown> }) => response.finished()), page.getByRole("button", { name: "Stop old script", exact: true }).click()]);
    await page.getByRole("button", { name: "Stop old script", exact: true }).waitForFunction((button: HTMLButtonElement) => !button.disabled);
    expect(calls).toEqual([{ path: "/api/MoveToBackground", body: { conversationId: "chat" } }, { path: "/api/StopBackgroundJob", body: { conversationId: "chat", jobId: "j-old" } }]);

    // A script's call lives on an earlier page; Open loads it and scrolls to the call.
    await page.getByRole("button", { name: "Open old script", exact: true }).click();
    await page.waitForFunction(() => {
      const viewport = document.querySelector("#transcript-scroll")?.getBoundingClientRect();
      const call = document.querySelector('#transcript-scroll [data-tool-call-id="call-old"]')?.getBoundingClientRect();
      return !!viewport && !!call && call.bottom > viewport.top && call.top < viewport.bottom;
    });
    expect(calls[2]).toEqual({ path: "earlier", body: { id: "chat", before: "11", limit: 50 } });
    await page.screenshot({ path: path.join(shots, `background-open-call-${width}.png`) });

    await page.getByRole("button", { name: "Open research", exact: true }).click();
    await page.waitForFunction(() => new URLSearchParams(location.search).get("delegation") === "chat/call-sub");
    await page.goBack();
    await page.waitForFunction(() => !new URLSearchParams(location.search).has("delegation"));

    // The stream's hint alone updates the list and the move button, with an unchanged revision.
    live = { movable: false, backgroundJobs: [live.backgroundJobs[3]] };
    notify(JSON.stringify({ conversationId: "chat", revision: "tail" }));
    await page.getByRole("button", { name: "Move to background" }).waitFor({ state: "detached" });
    expect(await jobs.getByRole("listitem").allInnerTexts()).toEqual(["build · killed (server restarted)\nOpen"]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
