import { expect, test } from "bun:test";
import path from "node:path";
import type { PendingQuestion } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");
const chat = `/s/${Buffer.from("chat").toString("base64url")}`;
const event = (n: number, role: string) => ({ role, text: `${role} ${n}`, complete: true, entryKey: String(n), itemId: `${n}:${role}`, messageId: `${n}:${role}`, inputId: "", turnId: `turn-${n}` });
const view = (turns: number, questions: PendingQuestion[] = []) => ({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], pendingQuestions: questions,
  messages: Array.from({ length: turns }, (_, i) => [event(i + 1, "user"), event(i + 1, "assistant")]).flat(), revision: String(questions.length), reset: true, entryKeys: Array.from({ length: turns }, (_, i) => String(i + 1)), running: questions.length > 0, start: "1", more: false });

// The mocked server answers the page's calls; `typing` answers GET /typing and `api` the calls the test watches.
function serve(typing: (request: Request) => Response, api: (pathname: string, input: unknown) => Promise<Response | undefined> | Response | undefined) {
  return Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/typing") return typing(request);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", agent: "main" }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json();
    const answer = await api(url.pathname, input);
    if (answer) return answer;
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "typing" });
      case "/api/Identity": return Response.json({ username: "tester", principal: "Tess" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "typing" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
}

for (const [width, height] of [[1280, 900], [390, 664]]) test(`the pill shows who else types at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const streams = new Set<ReadableStreamDefaultController<string>>();
  const push = (principal: string, typing: boolean) => { for (const stream of streams) stream.enqueue(`data: ${JSON.stringify({ principal, typing })}\n\n`); };
  const server = serve(() => {
    let own: ReadableStreamDefaultController<string>;
    return new Response(new ReadableStream<string>({
      start(controller) { own = controller; streams.add(controller); controller.enqueue(`data: ${JSON.stringify({ principal: "", typing: false })}\n\n`); },
      cancel() { streams.delete(own); },
    }), { headers: { "Content-Type": "text/event-stream" } });
  }, (pathname) => pathname === "/api/History" ? Response.json(view(30)) : undefined);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.clock.install();
    await page.goto(`http://127.0.0.1:${server.port}${chat}`);
    await page.getByText("assistant 30", { exact: true }).waitFor();
    const status = page.locator('[role="status"][aria-live="polite"]');
    expect(await status.count()).toBe(1);
    expect(await status.textContent()).toBe("");
    for (const started = Date.now(); streams.size === 0 && Date.now() - started < 5_000;) await Bun.sleep(10);
    expect(streams.size).toBe(1);

    const box = (locator: { boundingBox(): Promise<{ x: number; y: number; width: number; height: number } | null> }) => locator.boundingBox().then((value) => value!);
    const pane = await box(page.locator("#transcript-scroll"));
    const firstTurn = page.locator("[data-turn-key]").first();
    const before = await box(firstTurn);
    // The empty event a watch opens with shows nothing, and so does an unknown frame's empty name.
    push("", true);
    push("Ana", true);
    await status.getByText("Ana is typing", { exact: true }).waitFor();
    expect(await status.ariaSnapshot()).toBe("- status: Ana is typing");
    const pill = status.locator("> *");
    const shown = await box(pill);
    // It floats at the top of the conversation, centered on it, and pushes no turn down.
    expect(shown.y - pane.y).toBeGreaterThanOrEqual(0);
    expect(shown.y - pane.y).toBeLessThan(24);
    expect(Math.abs(shown.x + shown.width / 2 - (pane.x + pane.width / 2))).toBeLessThan(2);
    expect(await box(firstTurn)).toEqual(before);
    expect(await pill.evaluate((element: HTMLElement) => getComputedStyle(element).pointerEvents)).toBe("none");
    await page.screenshot({ path: path.join(shots, `typing-pill-${width}.png`) });
    // Scrolling the conversation leaves the pill where it is.
    await page.locator("#transcript-scroll").hover();
    await page.mouse.wheel(0, -100_000);
    await page.waitForFunction(() => document.querySelector("#transcript-scroll")!.scrollTop === 0);
    expect(await box(pill)).toEqual(shown);

    push("Cy", true);
    await status.getByText("Ana and Cy are typing", { exact: true }).waitFor();
    push("Bea", true);
    await status.getByText("Ana, Cy and Bea are typing", { exact: true }).waitFor();
    push("Dee", true);
    await status.getByText("Ana, Cy and 2 others are typing", { exact: true }).waitFor();
    // A false hides that typist at once.
    push("Dee", false);
    await status.getByText("Ana, Cy and Bea are typing", { exact: true }).waitFor();
    const top = await box(firstTurn);
    for (const name of ["Ana", "Cy", "Bea"]) push(name, false);
    await page.waitForFunction(() => document.querySelector('[role="status"][aria-live="polite"]')!.textContent === "");
    expect(await box(firstTurn)).toEqual(top);

    // Long names are cut so the pill stays inside the conversation.
    const long = ["Maximiliana Wolkenstein-Featherstonehaugh", "Bartholomew Montgomery-Fitzwilliam"];
    for (const name of long) push(name, true);
    await status.getByText(`${long[0]} and ${long[1]} are typing`, { exact: true }).waitFor();
    const wide = await box(pill);
    expect(wide.x).toBeGreaterThanOrEqual(pane.x);
    expect(wide.x + wide.width).toBeLessThanOrEqual(pane.x + pane.width);
    if (width < 640) for (const name of long) expect(await status.getByText(name, { exact: true }).evaluate((element: HTMLElement) => element.scrollWidth > element.clientWidth)).toBe(true);
    await page.screenshot({ path: path.join(shots, `typing-pill-long-${width}.png`) });
    for (const name of long) push(name, false);
    await page.waitForFunction(() => document.querySelector('[role="status"][aria-live="polite"]')!.textContent === "");

    // A typist with no new report for 5 s disappears.
    push("Ana", true);
    await status.getByText("Ana is typing", { exact: true }).waitFor();
    await page.clock.fastForward(4_000);
    expect(await status.getByText("Ana is typing", { exact: true }).count()).toBe(1);
    await page.clock.fastForward(1_000);
    await page.waitForFunction(() => document.querySelector('[role="status"][aria-live="polite"]')!.textContent === "");
    // Back after disappearing, she comes after whoever started meanwhile.
    push("Cy", true);
    await status.getByText("Cy is typing", { exact: true }).waitFor();
    push("Ana", true);
    await status.getByText("Cy and Ana are typing", { exact: true }).waitFor();
    for (const name of ["Ana", "Cy"]) push(name, false);
    await page.waitForFunction(() => document.querySelector('[role="status"][aria-live="polite"]')!.textContent === "");

    // Leaving the chat closes its watch, though its hidden transcript stays; coming back opens it again.
    await page.evaluate(() => { history.pushState(null, "", "/shortcuts"); dispatchEvent(new PopStateEvent("popstate")); });
    for (const started = Date.now(); streams.size > 0 && Date.now() - started < 5_000;) await Bun.sleep(10);
    expect(streams.size).toBe(0);
    await page.goBack();
    for (const started = Date.now(); streams.size === 0 && Date.now() - started < 5_000;) await Bun.sleep(10);
    expect(streams.size).toBe(1);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);

test("the composer reports typing, not text, numbered and quietly", async () => {
  const { chromium: engine } = await import(playwright!);
  const reports: { id: string; typing: boolean; tab: string; seq: number }[] = [], prompts: string[] = [], watches: string[] = [];
  const sent = (from = 0) => reports.slice(from).map(({ id, typing }) => ({ id, typing }));
  let questions: PendingQuestion[] = [];
  let hold: Promise<void> | undefined;
  const arrived = (count: number) => (async () => { for (const started = Date.now(); reports.length < count && Date.now() - started < 5_000;) await Bun.sleep(10); })();
  // A refused watch and an older server that lacks ReportTyping both stay silent.
  const server = serve((request) => { watches.push(new URL(request.url).searchParams.get("id")!); return Response.json({ code: 7, message: "permission denied" }, { status: 403 }); }, async (pathname, input) => {
    if (pathname === "/api/ReportTyping") {
      reports.push(input as (typeof reports)[number]);
      await hold;
      return new Response("404 page not found\n", { status: 404 });
    }
    if (pathname === "/api/Prompt") prompts.push((input as { text: string }).text);
    if (pathname === "/api/AnswerQuestion") questions = [];
    if (pathname === "/api/History") return Response.json(view(2, questions));
    return undefined;
  });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.clock.install();
    await page.goto(`http://127.0.0.1:${server.port}${chat}`);
    const composer = page.locator("textarea");
    await page.locator("textarea:enabled").waitFor();
    await page.getByText("assistant 2", { exact: true }).waitFor();

    // The first keystroke reports, and further typing at most once per 2 s.
    await composer.pressSequentially("hel");
    await arrived(1);
    await composer.pressSequentially("lo");
    await Bun.sleep(300);
    expect(sent()).toEqual([{ id: "chat", typing: true }]);
    expect(Object.keys(reports[0]!).sort()).toEqual(["id", "seq", "tab", "typing"]);
    await page.clock.fastForward(2_000);
    await composer.pressSequentially("!");
    await arrived(2);

    // Sending reports false at once, though the true before it hangs, numbered after it.
    const release = Promise.withResolvers<void>();
    hold = release.promise;
    await page.clock.fastForward(2_000);
    await composer.pressSequentially(" there");
    await arrived(3);
    await composer.press("Enter");
    for (const started = Date.now(); prompts.length === 0 && Date.now() - started < 5_000;) await Bun.sleep(10);
    expect(prompts).toEqual(["hello! there"]);
    await arrived(4);
    expect(sent()).toEqual([{ id: "chat", typing: true }, { id: "chat", typing: true }, { id: "chat", typing: true }, { id: "chat", typing: false }]);
    hold = undefined;
    release.resolve();

    // $undo empties the draft without sending, and so does answering a question.
    await composer.fill("$undo");
    await arrived(5);
    await composer.press("Enter");
    await arrived(6);
    expect(sent(4)).toEqual([{ id: "chat", typing: true }, { id: "chat", typing: false }]);
    questions = [{ id: "q1", question: "Ship it?", details: "", multiple: false, options: [{ label: "Yes", value: "yes", description: "" }] }];
    await page.reload();
    await page.getByRole("form", { name: "Question" }).waitFor();
    await page.locator("textarea:enabled").waitFor();
    await composer.fill("my answer");
    await arrived(7);
    await composer.press("Enter");
    await page.getByRole("form", { name: "Question" }).waitFor({ state: "detached" });
    await arrived(8);
    expect(sent(6)).toEqual([{ id: "chat", typing: true }, { id: "chat", typing: false }]);

    // A saved draft that a page opens with is not typing.
    await composer.fill("kept for later");
    await arrived(9);
    await Bun.sleep(300);
    await page.reload();
    await page.waitForFunction(() => document.querySelector("textarea")?.value === "kept for later");
    // The new-chat page has no chat to report to.
    await page.evaluate(() => { history.pushState(null, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
    await page.getByPlaceholder("Message a new session").waitFor();
    await composer.fill("a new idea");
    await Bun.sleep(500);
    expect(reports).toHaveLength(9);
    // Each page load is a new tab, which numbers its reports from 1.
    expect(reports.map(({ seq }) => seq)).toEqual([1, 2, 3, 4, 5, 6, 1, 2, 3]);
    expect(new Set(reports.slice(0, 6).map(({ tab }) => tab)).size).toBe(1);
    expect(new Set(reports.slice(6).map(({ tab }) => tab)).size).toBe(1);
    expect(reports[6]!.tab).not.toBe(reports[0]!.tab);
    expect(prompts).toEqual(["hello! there"]);
    // Each chat page load watched once: a refused watch is not retried.
    expect(watches).toEqual(["chat", "chat", "chat"]);
    expect(await page.getByRole("alert").count()).toBe(0);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
