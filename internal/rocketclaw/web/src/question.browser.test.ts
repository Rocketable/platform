import { expect, test } from "bun:test";
import path from "node:path";
import type { PendingQuestion } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

for (const [width, height] of [[1280, 900], [390, 664]]) test(`pending Web question at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const question = (id: string, multiple: boolean, ...labels: string[]): PendingQuestion => ({ id, question: `Question ${id}?`, details: `Details of ${id}`, multiple, options: labels.map((label) => ({ label, value: label.toLowerCase(), description: label === "Yes" ? "Ship it now" : "" })) });
  // Each answer moves the turn on to its next question; the last one already ended when it is answered.
  const next = [question("q2", true, "Alpha", "Beta", "Gamma"), question("q3", false, "Red", "Blue"), question("q4", false, "Late")];
  let questions = [question("q1", false, "Yes", "No")];
  const calls: { path: string; body: object }[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", agent: "main", running: true }], owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; askId?: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "question" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "question" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Prompt": calls.push({ path: url.pathname, body: input }); return Response.json({ privateText: "" });
      case "/api/AnswerQuestion": {
        calls.push({ path: url.pathname, body: input });
        questions = next.splice(0, 1);
        if (input.askId === "q4") return Response.json({ code: 5, message: "question is no longer pending" }, { status: 404 });
        return Response.json({});
      }
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], pendingQuestions: input.id === "chat" ? questions : [], messages: [], revision: questions.map((asked) => asked.id).join(), reset: true, entryKeys: [], running: input.id === "chat", start: "0", more: false });
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat").replace(/=+$/, "")}`);
    const card = page.getByRole("group", { name: "Question" });
    await card.getByText("Question q1?").waitFor();
    await page.screenshot({ path: path.join(shots, `question-single-${width}.png`) });

    // A closed and reopened tab lists the question again from History.
    await page.reload();
    await card.getByText("Question q1?").waitFor();
    await card.getByRole("button", { name: /^Yes/ }).click();
    await card.getByText("Question q2?").waitFor();

    // A multiple choice sends every checked option, in option order.
    await card.getByLabel("Gamma").check();
    await card.getByLabel("Alpha").check();
    await page.screenshot({ path: path.join(shots, `question-multiple-${width}.png`) });
    await card.getByRole("button", { name: "Submit" }).click();
    await card.getByText("Question q3?").waitFor();

    // Send answers with the composer's text instead of queueing it.
    const composer = page.locator("textarea");
    expect(await composer.getAttribute("placeholder")).toBe("Answer the question · ⌘⏎ steers");
    await composer.fill("my own answer");
    await composer.press("Enter");
    await card.getByText("Question q4?").waitFor();
    expect(await composer.inputValue()).toBe("");

    await card.getByRole("button", { name: "Late" }).click();
    await page.getByText("This question was already answered or ended").waitFor();
    await card.waitFor({ state: "detached" });
    expect(await composer.getAttribute("placeholder")).toBe("Queue a follow-up · ⌘⏎ steers");
    expect(calls).toEqual([
      { path: "/api/AnswerQuestion", body: { conversationId: "chat", askId: "q1", selected: ["yes"] } },
      { path: "/api/AnswerQuestion", body: { conversationId: "chat", askId: "q2", selected: ["alpha", "gamma"] } },
      { path: "/api/AnswerQuestion", body: { conversationId: "chat", askId: "q3", custom: "my own answer" } },
      { path: "/api/AnswerQuestion", body: { conversationId: "chat", askId: "q4", selected: ["late"] } },
    ]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
