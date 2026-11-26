import { expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";
import { mkdir } from "node:fs/promises";
import type { PendingQuestion, QueueItem, TranscriptEvent } from "./types";

const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

// OpenCode references: packages/app/src/composer/editor/editor.tsx (including our
// embedded touch bar) and packages/app/src/session/composer/queue.ts.
for (const [width, touch] of [[390, true], [320, true], [820, true], [1280, false], [390, false]] as const) {
  test(`native composer at ${width}px with ${touch ? "touch" : "desktop"} input`, async () => {
    const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
    const calls: { text: string; delivery?: string }[] = [];
    const queue: QueueItem[] = [];
    let running = false;
    let stream: ReadableStreamDefaultController<string>;
    const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
      const url = new URL(request.url);
      if (url.pathname === "/stream") return new Response(new ReadableStream<string>({ start(controller) { stream = controller; controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
      if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "composer", name: "Native session layout", agent: "Build", running }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
      if (!url.pathname.startsWith("/api/")) {
        const file = path.join(dist, url.pathname);
        return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
      }
      switch (url.pathname) {
        case "/api/Protocol": return Response.json({ protoSha256: "composer" });
        case "/api/Identity": return Response.json({ username: "tester" });
        case "/api/ListConfig": return Response.json({ config: { workspace: "composer" } });
        case "/api/ListAgents": return Response.json({ agents: [{ name: "Build", model: "OpenAI / GPT-6.1 Sol", reasoning: "high" }], currentAgent: "Build" });
        case "/api/ListSkills": return Response.json({ skills: [] });
        case "/api/SlackNames": {
          const { query } = await request.json() as { query?: string };
          return Response.json({ names: query === "Ri" ? { U42: "Riley", U43: "Rika" } : { U42: "Riley" } });
        }
        case "/api/ListQueue": return Response.json({ items: queue });
        case "/api/Prompt": {
          const prompt = await request.json() as { text: string; delivery: QueueItem["delivery"]; messageId: string };
          calls.push(prompt);
          if (prompt.delivery === "QUEUE" || prompt.delivery === "STASH") queue.push({ id: prompt.messageId, text: prompt.text, delivery: prompt.delivery });
          return Response.json({});
        }
        case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], pendingQuestions: [], messages: [{ entryKey: "turn", itemId: "reply", inputId: "", turnId: "turn", role: "assistant", text: "The session is ready. You can send a new request, queue a follow-up, or steer the work already in progress.", complete: true }], running, revision: String(running), reset: true, entryKeys: ["turn"], start: "0", more: false });
        case "/api/Tabs": case "/api/ChangeTabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
        default: return Response.json({});
      }
    } });
    const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
    try {
      const page = await browser.newPage({ viewport: { width, height: 844 }, hasTouch: touch, isMobile: touch && width < 640 });
      const errors: string[] = [];
      page.on("pageerror", (error: Error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${server.port}/s/${Buffer.from("composer").toString("base64url")}`);
      const input = page.locator("textarea");
      await page.getByRole("button", { name: "Choose agent", exact: true }).waitFor();
      await page.locator("textarea:enabled").waitFor();
      await page.evaluate(() => document.fonts.ready);
      expect(await page.getByRole("button", { name: "Steer", exact: true }).count()).toBe(0);
      expect(await page.getByRole("button", { name: "Send", exact: true }).isDisabled()).toBe(true);
      await page.getByText("OpenAI / GPT-6.1 Sol", { exact: true }).waitFor();
      await mkdir(shots, { recursive: true });
      await page.screenshot({ path: path.join(shots, `native-composer-idle-${width}-${touch ? "touch" : "desktop"}.png`) });

      await input.fill("First line");
      // Clearing is optimistic; the fixture records each prompt before its response.
      const firstPrompt = page.waitForResponse("**/api/Prompt");
      await input.press("Enter");
      if (touch) {
        expect(await input.inputValue()).toBe("First line\n");
        expect(calls).toEqual([]);
        await page.getByRole("button", { name: "Send", exact: true }).click();
      }
      await firstPrompt;
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls[0]).toMatchObject({ text: touch ? "First line\n" : "First line", delivery: "STEER" });

      running = true;
      stream!.enqueue(`data: ${JSON.stringify({ conversationId: "composer", revision: "running" })}\n\n`);
      await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
      await input.fill("Please focus on the composer layout.");
      const steer = page.getByRole("button", { name: "Steer", exact: true }).last();
      await steer.waitFor();
      const primary = page.getByRole("button", { name: touch ? "Queue" : "Send", exact: true });
      const box = (await primary.boundingBox())!;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.y + box.height).toBeLessThanOrEqual(844);
      if (touch) expect(box.height).toBeGreaterThanOrEqual(44);
      await page.mouse.move(0, 0);
      await page.getByRole("tooltip").waitFor({ state: "hidden" });
      await page.screenshot({ path: path.join(shots, `native-composer-working-${width}-${touch ? "touch" : "desktop"}.png`) });
      await Promise.all([page.waitForResponse("**/api/Prompt"), primary.click()]);
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls.at(-1)).toMatchObject({ text: "Please focus on the composer layout.", delivery: "QUEUE" });
      await page.locator("[data-queue-id]").waitFor();
      await input.fill("Keep the steer control.");
      await page.mouse.move(0, 0);
      await page.getByRole("tooltip").waitFor({ state: "hidden" });
      await page.screenshot({ path: path.join(shots, `native-composer-queued-${width}-${touch ? "touch" : "desktop"}.png`) });
      await Promise.all([page.waitForResponse("**/api/Prompt"), steer.click()]);
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls.at(-1)).toMatchObject({ text: "Keep the steer control.", delivery: "STEER" });
      expect(await input.evaluate((element: HTMLTextAreaElement) => document.activeElement === element)).toBe(true);
      await input.fill("Save this for later");
      await page.getByRole("button", { name: "Composer actions", exact: true }).click();
      await Promise.all([page.waitForResponse("**/api/Prompt"), page.getByRole("menuitem", { name: "Stash", exact: true }).click()]);
      await page.waitForFunction(() => document.querySelector("textarea")?.value === "");
      expect(calls.at(-1)).toMatchObject({ text: "Save this for later", delivery: "STASH" });
      if (width === 1280 && !touch) {
        await input.fill("Ask @R\nTomorrow");
        await input.press("ArrowUp");
        await input.press("End");
        const accepted = await page.getByRole("button", { name: "@Riley", exact: true }).evaluate(async (button: HTMLButtonElement) => {
          button.click();
          await Promise.resolve();
          const field = document.querySelector("textarea")!;
          const caret = { text: field.value, start: field.selectionStart, end: field.selectionEnd };
          field.select();
          await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
          return { caret, selection: [field.selectionStart, field.selectionEnd] };
        });
        const acceptedText = "Ask @Riley \nTomorrow";
        expect(accepted).toEqual({ caret: { text: acceptedText, start: 11, end: 11 }, selection: [0, acceptedText.length] });
        for (const replacement of ["Ask @Riley please review", "Ask @Riley "]) {
          await input.fill("Ask @Ril");
          await page.getByRole("button", { name: "@Riley", exact: true }).click();
          await input.fill(replacement);
          await Promise.all([page.waitForResponse("**/api/Prompt"), page.getByRole("button", { name: "Send", exact: true }).click()]);
          expect(calls.at(-1)).toMatchObject({ text: replacement });
          expect(calls.at(-1)).not.toHaveProperty("mentionIds");
        }
        await input.fill("Ask @");
        await input.pressSequentially("Ri");
        await page.getByRole("button", { name: "@Riley", exact: true }).waitFor();
        await input.press("ArrowUp");
        await input.press("Tab");
        expect(await input.inputValue()).toBe("Ask @Riley ");
        await input.press("Home");
        await input.pressSequentially("Please ");
        await input.press("End");
        await input.pressSequentially("caf");
        const ime = await page.context().newCDPSession(page);
        await ime.send("Input.imeSetComposition", { text: "e", selectionStart: 1, selectionEnd: 1 });
        await ime.send("Input.imeSetComposition", { text: "é", selectionStart: 1, selectionEnd: 1 });
        await page.keyboard.insertText("é");
        await ime.detach();
        expect(await input.inputValue()).toBe("Please Ask @Riley café");
        await page.keyboard.press("Control+Shift+p");
        const palette = page.getByRole("dialog", { name: "Run command", exact: true });
        await palette.getByPlaceholder("Type a command").fill("Enqueue work");
        await palette.getByRole("button", { name: "Command: Enqueue work (/enqueue)", exact: true }).click();
        await palette.waitFor({ state: "hidden" });
        expect(await input.inputValue()).toBe("/enqueue Please Ask @Riley café");
        expect(await page.getByRole("button", { name: "@Riley", exact: true }).count()).toBe(0);
        await Promise.all([page.waitForResponse("**/api/Prompt"), input.press("Enter")]);
        expect(calls.at(-1)).toMatchObject({ text: "/enqueue Please Ask @Riley café", mentionIds: ["U42"] });
        await input.fill("Ask @Riley and <@U42|Riley>");
        await Promise.all([page.waitForResponse("**/api/Prompt"), input.press("Enter")]);
        expect(calls.at(-1)).not.toHaveProperty("mentionIds");
        await input.fill("Ask @Ril");
        await page.getByRole("button", { name: "@Riley", exact: true }).waitFor();
        await input.press("Tab");
        await input.press("Backspace");
        await input.press("Backspace");
        await input.pressSequentially("y tomorrow");
        await Promise.all([page.waitForResponse("**/api/Prompt"), input.press("Enter")]);
        expect(calls.at(-1)).toMatchObject({ text: "Ask @Riley tomorrow" });
        expect(calls.at(-1)).not.toHaveProperty("mentionIds");
        await input.fill("Ask @Ril");
        await page.getByRole("button", { name: "@Riley", exact: true }).click();
        await input.press("End");
        await input.press("ArrowLeft");
        await input.pressSequentially("x");
        await input.press("End");
        await Promise.all([page.waitForResponse("**/api/Prompt"), input.press("Enter")]);
        expect(calls.at(-1)).toMatchObject({ text: "Ask @Rileyx " });
        expect(calls.at(-1)).not.toHaveProperty("mentionIds");
      }
      expect(errors).toEqual([]);
    } finally {
      await browser.close();
      server.stop(true);
    }
  }, 30_000);
}

// While the agent is paused, Send posts to the people in the chat at once, also during a finishing reply, and
// starts no turn. A switch the server refuses says why, and a person's name shows as text, never as markup.
for (const [width, touch] of [[1280, false], [390, true]] as const) test(`paused composer at ${width}px`, async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const name = "<img src=x onerror=alert(1)>";
  const prompts: { text: string; delivery?: string; messageId: string }[] = [], updates: object[] = [], answers: object[] = [];
  const messages: TranscriptEvent[] = [{ entryKey: "1", itemId: "hello", inputId: "", turnId: "1", role: "assistant", text: "Hello, everyone.", complete: true }];
  const queue: QueueItem[] = [];
  let questions: PendingQuestion[] = [];
  let paused = false, running = false, refused = false, revision = 0;
  let stream: ReadableStreamDefaultController<string>;
  const add = (event: Omit<TranscriptEvent, "entryKey" | "turnId" | "complete">) => messages.push({ entryKey: String(messages.length + 1), turnId: "", complete: true, ...event });
  const changed = () => { revision++; stream.enqueue(`data: ${JSON.stringify({ conversationId: "paused", revision: String(revision) })}\n\n`); };
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream<string>({ start(controller) { stream = controller; controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "paused", name: "Team chat", agent: "Build", running, paused: paused || undefined, pausable: true }], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "paused" });
      case "/api/Identity": return Response.json({ username: "tester", principal: name });
      case "/api/ListConfig": return Response.json({ config: { workspace: "paused" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "Build" }], currentAgent: "Build" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: queue });
      case "/api/UpdateSession": {
        // The first switch is refused, as the server refuses a session that cannot pause.
        if (!refused) {
          refused = true;
          return Response.json({ code: 3, message: "this session's agent cannot pause" }, { status: 400 });
        }
        const input = await request.json() as { paused: boolean };
        updates.push(input);
        paused = input.paused;
        add({ itemId: `line-${messages.length}`, inputId: "", role: "developer", text: `${JSON.stringify(name)} ${paused ? "paused the agent. Until it is resumed, messages here are people talking to each other, not requests to you." : "resumed the agent."}` });
        changed();
        return Response.json({});
      }
      case "/api/AnswerQuestion": answers.push(await request.json()); return Response.json({});
      case "/api/Prompt": {
        const prompt = await request.json() as typeof prompts[number];
        prompts.push(prompt);
        add({ itemId: prompt.messageId, inputId: prompt.messageId, role: "user", text: prompt.text, principal: name });
        changed();
        return Response.json({});
      }
      case "/api/History": return Response.json({ origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], pendingQuestions: questions, messages, running, revision: String(revision), reset: true, entryKeys: messages.map((message) => message.entryKey), start: "0", more: false });
      case "/api/Tabs": case "/api/ChangeTabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height: 844 }, hasTouch: touch, isMobile: touch });
    const errors: string[] = [], dialogs: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("dialog", (dialog: { message: () => string; dismiss: () => Promise<void> }) => { dialogs.push(dialog.message()); void dialog.dismiss(); });
    await page.goto(`http://127.0.0.1:${server.port}/s/${Buffer.from("paused").toString("base64url")}`);
    const input = page.locator("textarea"), transcript = page.locator("#transcript-scroll"), send = page.getByRole("button", { name: "Send", exact: true });
    const waitForPrompt = (text: string) => page.waitForResponse((response: { url(): string; request(): { postDataJSON(): { text: string } } }) =>
      response.url().endsWith("/api/Prompt") && response.request().postDataJSON().text === text);
    // The pause toggle sits with Send, as a button that stays pressed while the agent is paused.
    const toggle = page.locator('[data-slot="composer-delivery"]').getByRole("button", { name: "Pause agent", exact: true });
    await page.locator("textarea:enabled").waitFor();
    expect(await toggle.getAttribute("aria-pressed")).toBe("false");
    await toggle.click();
    await page.getByRole("alert").filter({ hasText: "this session's agent cannot pause" }).waitFor();
    expect(updates).toEqual([]);
    expect(await toggle.getAttribute("aria-pressed")).toBe("false");
    await toggle.click();
    await page.locator('textarea[placeholder="Message the people here"]').waitFor();
    expect(updates).toEqual([{ id: "paused", paused: true }]);
    expect(await toggle.getAttribute("aria-pressed")).toBe("true");
    expect(await page.getByRole("alert").filter({ hasText: "this session's agent cannot pause" }).count()).toBe(0);
    expect(await input.getAttribute("placeholder")).toBe("Message the people here");
    if (!touch) expect(await page.locator('[role="tab"][aria-label="Team chat"]').getAttribute("aria-description")).toContain("Agent paused");
    const box = (await toggle.boundingBox())!;
    expect(box.x + box.width).toBeLessThanOrEqual(width);

    // On an idle chat the message shows at once and nothing works on it.
    await input.fill("Lunch at noon?");
    const lunch = waitForPrompt("Lunch at noon?");
    await send.click();
    await transcript.getByText("Lunch at noon?", { exact: true }).waitFor();
    await lunch;
    expect(prompts.at(-1)).toMatchObject({ text: "Lunch at noon?", delivery: "STEER" });
    expect(await page.getByText("Working…", { exact: true }).count()).toBe(0);
    await mkdir(shots, { recursive: true });
    await page.screenshot({ path: path.join(shots, `paused-composer-idle-${width}.png`) });

    // During a finishing reply a message shows at once too, and "Working…" stays with the reply.
    running = true;
    add({ itemId: "reply", inputId: "", role: "assistant", text: "Still answering the earlier request" });
    messages.at(-1)!.complete = false;
    changed();
    const working = page.getByText("Working…", { exact: true });
    await working.waitFor();
    await input.fill("While the reply runs");
    // Paused, Steer would only post as Send does, so the row shows the pressed toggle and Send.
    const steer = page.locator('[data-slot="composer-delivery"]').getByRole("button", { name: "Steer", exact: true });
    expect(await steer.count()).toBe(0);
    expect(await toggle.getAttribute("aria-pressed")).toBe("true");
    const followUp = waitForPrompt("While the reply runs");
    await send.click();
    await transcript.getByText("While the reply runs", { exact: true }).waitFor();
    expect(await page.getByRole("region", { name: "Pending steers" }).count()).toBe(0);
    await followUp;
    expect(prompts.at(-1)).toMatchObject({ text: "While the reply runs", delivery: "STEER" });
    expect(await working.count()).toBe(1);

    // Send does not answer the agent's question; its card still can.
    questions = [{ id: "ask", question: "Which venue?", details: "", multiple: false, options: [{ label: "Cafe", value: "cafe", description: "" }] }];
    changed();
    const card = page.getByRole("form", { name: "Question" });
    await card.getByText("Which venue?").waitFor();
    expect(await input.getAttribute("placeholder")).toBe("Message the people here");
    await input.fill("Cafe works for me");
    const cafe = waitForPrompt("Cafe works for me");
    await send.click();
    await transcript.getByText("Cafe works for me", { exact: true }).waitFor();
    await cafe;
    expect(prompts.at(-1)).toMatchObject({ text: "Cafe works for me", delivery: "STEER" });
    expect(answers).toEqual([]);
    expect(await card.isVisible()).toBe(true);

    // A queued item waits for resume.
    queue.push({ id: "later", text: "Summarize the plan", delivery: "QUEUE" });
    const queued = page.locator('[data-queue-id="later"]').getByRole("button", { name: "Steer", exact: true });
    await queued.waitFor();
    expect(await queued.isDisabled()).toBe(true);
    expect(await queued.getAttribute("title")).toBe("Runs after the agent is resumed");
    await page.screenshot({ path: path.join(shots, `paused-composer-finishing-${width}.png`) });

    running = false;
    questions = [];
    messages.find((message) => message.itemId === "reply")!.complete = true;
    changed();
    await working.waitFor({ state: "hidden" });

    // The person's name shows as text: as the author of their messages and in the line saying they paused the agent.
    expect(await transcript.locator('[data-slot="message-author"]').first().textContent()).toBe(name);
    const line = transcript.locator('[data-slot="code-block-code"]').filter({ hasText: "paused the agent." }).first();
    await line.waitFor({ state: "attached" });
    expect(await line.textContent()).toStartWith(`${JSON.stringify(name)} paused the agent.`);
    expect(await page.locator('img[src="x"]').count()).toBe(0);

    await toggle.click();
    await page.locator('textarea[placeholder="Message or /command"]').waitFor();
    expect(updates.at(-1)).toEqual({ id: "paused", paused: false });
    expect(await input.getAttribute("placeholder")).toBe("Message or /command");

    // Live, a reply running and a typed message bring Steer, and the toggle steps aside so Send stays on screen.
    running = true;
    changed();
    await working.waitFor();
    await input.fill("Steer this reply");
    await steer.waitFor();
    expect(await toggle.count()).toBe(0);
    const submit = (await page.locator("[data-composer-submit]").boundingBox())!;
    expect(submit.x + submit.width).toBeLessThanOrEqual(width);
    await input.fill("");
    await toggle.waitFor();
    expect(errors).toEqual([]);
    expect(dialogs).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
