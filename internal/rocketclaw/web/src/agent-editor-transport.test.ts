import { expect, test } from "bun:test";
import { isDeepStrictEqual } from "node:util";
import type { AgentDraft, AgentDraftView, DraftPromptResponse, HistoryView } from "./types";

type Body = { op: { kind: string }; id?: string; limit?: number };
type RoutedRequest = {
  url(): string;
  method(): string;
  postData(): string | null;
  postDataJSON(): Body;
  existingResponse(): { status(): number; text(): Promise<string>; json(): Promise<unknown> } | null;
  failure(): { errorText: string } | null;
  timing(): { responseEnd: number };
};
type Route = { fetch(): Promise<object>; fulfill(options: object): Promise<void>; continue(): Promise<void>; request(): RoutedRequest };
type Routed = { route(url: RegExp, handler: (route: Route) => Promise<void>): Promise<void>; unroute(url: RegExp): Promise<void> };
type HeldRequest = { request: RoutedRequest; routeError?: string };

async function hold(page: Routed, url: RegExp, { respond = false, count = Infinity, only = () => true, onHeld }: { respond?: boolean; count?: number; only?: (request: RoutedRequest) => boolean; onHeld?: (entry: HeldRequest) => void } = {}) {
  let release = () => {}, held = 0;
  const gate = new Promise<void>((resolve) => { release = resolve; }), handled: Promise<void>[] = [];
  await page.route(url, async (route) => {
    const request = route.request();
    if (!only(request) || held++ >= count) return await route.continue().catch(() => {});
    const observation: HeldRequest = { request };
    const answered = (async () => {
      const response = respond ? await route.fetch() : undefined;
      await gate;
      await (response ? route.fulfill({ response }) : route.continue());
    })().catch((error: unknown) => { observation.routeError = String(error); });
    handled.push(answered);
    onHeld?.(observation);
    await answered;
  });
  return async () => {
    count = 0;
    release();
    await Promise.all(handled);
    await page.unroute(url);
  };
}

async function refuse(page: Routed) {
  const url = /\/api\/ApplyAgentDraftOp$/;
  await page.route(url, (route) => route.fulfill({ status: 400, json: { code: "invalid_argument", message: `Refused ${route.request().postDataJSON().op.kind}.` } }));
  return () => page.unroute(url);
}

test("the agent editor starts drafts, refuses limited people, and follows a teammate live", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!, bob = process.env.ROCKETCLAW_EDITOR_BOB_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [];
    const open = async () => {
      const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
      page.on("pageerror", (error: Error) => errors.push(error.message));
      return page;
    };
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    const a = await open(), b = await open();

    await a.goto(`${alice}/agents/new`);
    await a.getByRole("radio", { name: "Easy agent" }).click();
    await a.getByRole("radio", { name: "Blank" }).click();
    await a.getByRole("textbox", { name: "Agent name" }).fill("triage");
    await a.getByRole("button", { name: "Start blank", exact: true }).click();
    await a.waitForURL(/\/agents\/drafts\/[^/]+$/);
    const id = a.url().split("/").at(-1)!;
    await a.getByRole("heading", { name: "Create New Agent", exact: true }).waitFor();
    await a.getByText("triage.md · easy", { exact: true }).waitFor();
    await a.locator(`[data-tab="/agents/drafts/${id}"]`).getByText("triage", { exact: true }).waitFor();
    for (const variant of ["b", "c"]) {
      const { draft } = await call<AgentDraftView>("GetAgentDraft", { id, since: "1" });
      await call("ApplyAgentDraftOp", { id, op: { baseRevision: draft.revision, opId: `test-${variant}`, kind: "add_variant", variant, value: "a" } });
    }
    await a.getByText("all 3 variants", { exact: true }).waitFor();

    await b.goto(`${bob}/agents/drafts/${id}`);
    await b.getByText("People limited to spaces cannot use the agent editor", { exact: false }).waitFor();
    await b.getByRole("link", { name: "Back to the Agents page", exact: true }).click();
    await b.waitForURL(`${bob}/agents`);
    await b.getByRole("heading", { name: "Agents", exact: true }).waitFor();
    await Bun.sleep(500);
    expect(await b.getByRole("link", { name: "Create New Agent" }).count()).toBe(0);
    expect(await b.getByRole("button", { name: /^Edit / }).count()).toBe(0);

    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    await b.goto(`${bob}/agents/drafts/${id}`);
    await b.getByText("triage/c.md · easy", { exact: true }).waitFor();
    for (const page of [a, b]) for (const who of ["alice", "bob"]) await page.getByRole("img", { name: `${who} is here`, exact: true }).waitFor();

    await a.getByRole("button", { name: "Advanced", exact: true }).click();
    for (const file of ["triage.md", "triage/b.md", "triage/c.md"]) await b.getByText(`${file} · advanced`, { exact: true }).waitFor();
    expect(await b.getByRole("button", { name: "Advanced", exact: true }).getAttribute("aria-pressed")).toBe("true");

    await a.getByRole("button", { name: "triage/b.md · advanced", exact: true }).click();
    const { draft: advanced } = await call<AgentDraftView>("GetAgentDraft", { id, since: "1" });
    await call("ApplyAgentDraftOp", { id, op: { baseRevision: advanced.revision, opId: "test-rename", kind: "rename_variant", variant: "b", value: "beta" } });
    await a.getByRole("button", { name: "triage/beta.md · advanced", exact: true, pressed: true }).waitFor();

    await b.getByRole("button", { name: "Drafts", exact: true }).click();
    await b.getByRole("menuitem", { name: /^triage\.md/ }).waitFor();
    await b.getByRole("menuitem", { name: "Start a new agent", exact: true }).click();
    await b.waitForURL(`${bob}/agents/new`);
    await b.getByRole("radio", { name: "Advanced agent" }).click();

    await b.getByRole("radio", { name: "Template" }).click();
    await b.getByRole("button", { name: "Use template researcher", exact: true }).dblclick();
    await b.getByText("researcher.md · easy", { exact: true }).waitFor();

    const edit = async () => {
      await b.goto(`${bob}/agents/new`);
      await b.getByRole("radio", { name: "Advanced agent" }).click();
      await b.getByRole("radio", { name: "Copy an agent" }).click();
      await b.getByRole("button", { name: "Edit support-bot", exact: true }).dblclick();
      await b.getByRole("heading", { name: "Edit support-bot", exact: true }).waitFor();
      return b.url();
    };
    const editing = await edit();
    expect(editing).toMatch(/\/agents\/drafts\/[^/]+$/);
    expect(await edit()).toBe(editing);
    await b.goto(`${bob}/agents/new`);
    await b.getByRole("radio", { name: "Advanced agent" }).click();
    await b.getByRole("radio", { name: "Copy an agent" }).click();
    await b.getByRole("button", { name: "Copy support-bot", exact: true }).dblclick();
    await b.getByText("support-bot-copy.md · advanced", { exact: true }).waitFor();
    await b.getByRole("heading", { name: "Create New Agent", exact: true }).waitFor();
    expect(b.url()).not.toBe(editing);

    await Bun.sleep(500);
    const names = (await call<{ drafts: AgentDraft[] }>("ListAgentDrafts", {})).drafts.map((draft) => draft.name);
    for (const name of ["researcher", "support-bot", "support-bot-copy"]) expect([name, names.filter((started) => started === name).length]).toEqual([name, 1]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 120_000);

test("on a phone the editor shows one pane at a time", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [];
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    const narrow = await call<{ id: string }>("CreateAgentDraft", { name: "narrow", mode: "advanced" });
    const { draft } = await call<AgentDraftView>("GetAgentDraft", { id: narrow.id, since: "1" });
    await call("ApplyAgentDraftOp", { id: narrow.id, op: { baseRevision: draft.revision, opId: crypto.randomUUID(), kind: "edit_file", variant: "a", value: "---\nmodel: gpt-a\n---\nHelp.\n" } });
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`${alice}/agents/drafts/${narrow.id}`);
    const panes = page.getByRole("tablist", { name: "Panes" }), chat = page.getByRole("region", { name: "Test chat" }), assistant = page.getByRole("region", { name: "Assistant" });
    const instructions = page.getByRole("textbox", { name: /^Instructions/ });

    await instructions.waitFor();
    expect(await chat.isVisible()).toBe(false);
    expect(await assistant.isVisible()).toBe(false);
    await instructions.click();
    await instructions.press("ControlOrMeta+End");
    await instructions.pressSequentially(" Be brief.", { delay: 20 });
    await page.getByRole("tab", { name: "File", exact: true }).click();
    await page.getByRole("tab", { name: "Form", exact: true }).click();

    await panes.getByRole("tab", { name: "Test chat", exact: true }).click();
    expect(await instructions.isVisible()).toBe(false);
    await chat.getByRole("textbox", { name: "Test message" }).fill("hello");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    const answer = chat.getByRole("region", { name: "narrow answer", exact: true });
    await answer.getByText("gpt-a answer 1", { exact: false }).waitFor();
    await panes.getByRole("tab", { name: "Settings", exact: true }).click();
    expect(await instructions.inputValue()).toContain("Be brief.");

    await panes.getByRole("tab", { name: "Test chat", exact: true }).click();
    await answer.getByRole("button", { name: "Fix this", exact: true }).click();
    await assistant.getByText("gpt-a answer 1", { exact: false }).waitFor();
    expect(await panes.getByRole("tab", { name: "Assistant", exact: true }).getAttribute("aria-selected")).toBe("true");
    expect(await chat.isVisible()).toBe(false);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 120_000);

test("two people co-edit a draft live in the Form and the File, with history and restore", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!, bob = process.env.ROCKETCLAW_EDITOR_BOB_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [];
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    const until = async (what: string, check: () => Promise<boolean>) => {
      for (const deadline = Date.now() + 15_000; !await check();) if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`); else await Bun.sleep(100);
    };
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    const { id } = await call<{ id: string }>("CreateAgentDraft", { name: "notes", mode: "easy" });
    const open = async (url: string) => {
      const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
      page.on("pageerror", (error: Error) => errors.push(error.message));
      await page.goto(`${url}/agents/drafts/${id}`);
      return page;
    };
    const a = await open(alice), b = await open(bob);
    const instructions = (page: typeof a) => page.getByRole("textbox", { name: /^Instructions/ });
    const body = async () => (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft.variants[0].body;

    await a.getByRole("group", { name: "Space", exact: true }).getByText("None: an easy agent must name a space.", { exact: true }).waitFor();
    await a.getByRole("tab", { name: "File, needs fixes" }).waitFor();

    await Promise.all([instructions(a).click(), instructions(b).click()]);
    await Promise.all([instructions(a).pressSequentially("alice ", { delay: 40 }), instructions(b).pressSequentially("bob ", { delay: 40 })]);
    let both = "";
    await until("both texts in every view", async () => {
      both = await instructions(a).inputValue();
      return both.length === 10 && both === await instructions(b).inputValue() && both === await body();
    });
    expect(both).toMatch(/a.*l.*i.*c.*e/s);
    expect(both).toMatch(/b.*o.*b/s);

    await instructions(a).press("ControlOrMeta+End");
    const typing = instructions(a).pressSequentially("more words here", { delay: 60 });
    await Bun.sleep(300);
    await b.reload();
    await typing;
    await until("bob caught up", async () => await instructions(b).inputValue() === `${both}more words here` && await body() === `${both}more words here`);

    const text = `${both}more words here`;
    await a.getByRole("tab", { name: "File, needs fixes" }).click();
    await a.getByRole("textbox", { name: "Agent file" }).fill(`---\nmode: easy\nspace: [Ops\n---\n${text}`);
    await a.getByText("Not yet valid: line", { exact: false }).waitFor();
    await Bun.sleep(500);
    expect(await b.getByRole("textbox", { name: "Space", exact: true }).inputValue()).toBe("");
    await a.getByRole("textbox", { name: "Agent file" }).fill(`---\nmode: easy\nspace: Ops\n---\n${text}`);
    await until("bob sees the space", async () => await b.getByRole("textbox", { name: "Space", exact: true }).inputValue() === "Ops");
    expect(await body()).toBe(text);

    await b.getByRole("tab", { name: "History", exact: true }).click();
    await b.getByText("bob changed Instructions", { exact: false }).first().waitFor();
    const undo = await refuse(b);
    await b.getByRole("button", { name: "Restore the draft from before alice changed Space", exact: true }).click();
    await b.getByRole("alert").getByText("Refused restore.", { exact: true }).waitFor();
    await undo();
    await b.getByRole("button", { name: "Restore the draft from before alice changed Space", exact: true }).click();
    await a.getByRole("tab", { name: "Form", exact: true }).click();
    await a.getByRole("group", { name: "Space", exact: true }).getByText("None: an easy agent must name a space.", { exact: true }).waitFor();
    await b.getByRole("tab", { name: "Form", exact: true }).click();
    await until("bob sees the restored space", async () => await b.getByRole("textbox", { name: "Space", exact: true }).inputValue() === "");

    const { draft } = await call<AgentDraftView>("GetAgentDraft", { id, since: "1" });
    await call("ApplyAgentDraftOp", { id, op: { baseRevision: draft.revision, opId: "test-helpers", kind: "set_field", variant: "a", key: "subagents", value: "auto" } });
    await a.getByRole("textbox", { name: "Helper models", exact: true }).waitFor();
    await b.getByRole("button", { name: "Advanced", exact: true }).click();
    await a.getByText("bob switched the whole draft to advanced. Dropped: Helper models.", { exact: false }).waitFor();
    await a.getByRole("status").getByRole("button", { name: "Restore", exact: true }).click();
    await until("the helpers are back", async () => await b.getByRole("textbox", { name: "Helper models", exact: true }).inputValue() === "auto");

    const tuner = await call<{ id: string; revision: string }>("CreateAgentDraft", { name: "tuner", mode: "advanced" });
    await call("ApplyAgentDraftOp", { id: tuner.id, op: { baseRevision: tuner.revision, opId: "test-tuner", kind: "edit_file", variant: "a", value: "---\nmodel: gpt-6.1-sol\ntemperature: 0.2\nhidden: true\n---\nTune.\n" } });
    await a.goto(`${alice}/agents/drafts/${tuner.id}`);
    const others = a.getByRole("region", { name: "Other settings" });
    for (const setting of ["temperature: 0.2", "hidden: true"]) await others.getByText(setting, { exact: true }).waitFor();
    await a.getByRole("tab", { name: "File", exact: true }).click();
    const file = a.getByRole("textbox", { name: "Agent file" });
    await file.fill((await file.inputValue()).replace("temperature: 0.2", "temperature: 0.5"));
    await a.getByRole("tab", { name: "Form", exact: true }).click();
    await others.getByText("temperature: 0.5", { exact: true }).waitFor();

    const setting = async (key: string) => (await call<AgentDraftView>("GetAgentDraft", { id: tuner.id, since: "1" })).draft.variants[0].settings.find((stored) => stored.key === key)?.value;
    const retype = async (from: string | RegExp, to: string) => file.fill((await file.inputValue()).replace(from, to));
    await a.getByRole("tab", { name: "File", exact: true }).click();
    let release = await hold(a, /\/api\/ApplyAgentDraftOp$/, { respond: true });
    await retype("temperature: 0.5", "temperature: 0.7");
    await until("0.7 is stored", async () => await setting("temperature") === "0.7");
    await retype("temperature: 0.7", "temperature: 0.5");
    await Bun.sleep(500);
    await a.getByRole("tab", { name: "Form", exact: true }).click();
    await others.getByText("temperature: 0.7", { exact: true }).waitFor();
    await release();
    await until("0.5 is back", async () => await setting("temperature") === "0.5");
    await others.getByText("temperature: 0.5", { exact: true }).waitFor();

    release = await hold(a, /\/api\/(ApplyAgentDraftOp|GetAgentDraft)$/, { respond: true });
    await a.getByRole("textbox", { name: "Description", exact: true }).fill("by-alice");
    await until("alice's description is stored", async () => await setting("description") === "by-alice");
    await a.getByRole("tab", { name: "File", exact: true }).click();
    await retype(/temperature: [\d.]+/, "temperature: 0.9");
    const { draft: tuned } = await call<AgentDraftView>("GetAgentDraft", { id: tuner.id, since: "1" });
    await call("ApplyAgentDraftOp", { id: tuner.id, op: { baseRevision: tuned.revision, opId: "test-bob", kind: "set_field", variant: "a", key: "description", value: "by-bob" } });
    await Bun.sleep(500);
    await release();
    await until("0.9 is stored", async () => await setting("temperature") === "0.9");
    expect(await setting("description")).toBe("by-bob");
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 180_000);

const modelsDev = { openai: { id: "openai", name: "OpenAI", models: {
  "gpt-5": { id: "gpt-5", name: "GPT-5", reasoning: true, reasoning_options: [{ type: "effort", values: ["minimal", "low", "medium", "high"] }], limit: { context: 400000, output: 128000 }, cost: { input: 1.25, output: 10 } },
  "gpt-6.1-sol": { id: "gpt-6.1-sol", name: "GPT-6.1 Sol", reasoning: true, reasoning_options: [{ type: "effort", values: ["low", "medium", "high", "xhigh", "max"] }], limit: { context: 1050000, output: 128000 }, cost: { input: 2, output: 10 } },
} } };

test("the model picker offers the server's providers, models.dev suggestions, and Test", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [];
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    const until = async (what: string, check: () => Promise<boolean>) => {
      for (const deadline = Date.now() + 15_000; !await check();) if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`); else await Bun.sleep(100);
    };
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    const { id } = await call<{ id: string }>("CreateAgentDraft", { name: "picker", mode: "easy" });
    const model = async () => (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft.variants[0].settings.find(({ key }) => key === "model")?.value;
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    page.on("pageerror", (error: Error) => errors.push(error.message));
    let fetched = 0;
    await page.route("https://models.dev/api.json", (route: { fulfill(options: object): Promise<void> }) => { fetched++; return route.fulfill({ json: modelsDev, headers: { "access-control-allow-origin": "*" } }); });
    await page.goto(`${alice}/agents/drafts/${id}`);
    const picker = page.getByRole("group", { name: "Model", exact: true });

    await picker.getByText("None: every agent must name a model.", { exact: true }).waitFor();
    expect(fetched).toBe(0);
    await picker.getByRole("combobox", { name: "Model provider" }).click();
    await page.getByRole("option", { name: "openai", exact: true }).waitFor();
    expect(await page.getByRole("option").allTextContents()).toEqual(["work", "openai"]);
    await page.getByRole("option", { name: "openai", exact: true }).click();

    await picker.getByRole("combobox", { name: "Model", exact: true }).click();
    const suggestions = picker.getByRole("list", { name: "Suggestions from models.dev for openai" });
    const gpt5 = suggestions.getByRole("listitem").filter({ hasText: "GPT-5gpt-5" });
    await gpt5.getByText("$1.25 in · $10 out per 1M tokens · 400K context · effort minimal, low, medium, high", { exact: true }).waitFor();
    await suggestions.getByRole("listitem").filter({ hasText: "GPT-6.1 Sol" }).getByText("$2 in · $10 out per 1M tokens · 1.1M context · effort low, medium, high, xhigh, max", { exact: true }).waitFor();
    expect(await suggestions.getByRole("link", { name: "Compare GPT-5 on slopalytics" }).getAttribute("href")).toBe("https://slopalytics.com/");
    expect(fetched).toBe(1);

    await gpt5.getByRole("button").click();
    await until("the model is gpt-5", async () => await model() === "gpt-5");
    await picker.getByRole("combobox", { name: "Model effort" }).click();
    await page.getByRole("option", { name: "Effort: low", exact: true }).waitFor();
    expect(await page.getByRole("option").allTextContents()).toEqual(["Effort: default", "Effort: minimal", "Effort: low", "Effort: medium", "Effort: high"]);
    await page.getByRole("option", { name: "Effort: low", exact: true }).click();
    await until("the effort is low", async () => await model() === "gpt-5#low");

    await picker.getByRole("combobox", { name: "Model provider" }).click();
    await page.getByRole("option", { name: "work", exact: true }).click();
    const typed = picker.getByRole("combobox", { name: "Model", exact: true });
    await typed.fill("llama-4-maverik");
    await picker.getByText("models.dev has no provider called “work”, so there are no suggestions. Type the exact model name it serves, then Test it.", { exact: true }).waitFor();
    expect(await picker.getByRole("listitem").count()).toBe(0);
    await until("the model is work's", async () => await model() === "work/llama-4-maverik#low");
    await picker.getByText("Test sends one small paid request, only when you click.", { exact: true }).waitFor();
    await picker.getByRole("button", { name: "Test", exact: true }).click();
    await picker.getByRole("alert").getByText("This gateway serves no such model; it serves llama-4-maverick", { exact: true }).waitFor();
    await typed.fill("llama-4-maverick");
    await picker.getByRole("button", { name: "Test", exact: true }).click();
    await picker.getByRole("status").getByText(/^Works\. work answered in \d+\.\d s\.$/).waitFor();
    await until("the model is the one that works", async () => await model() === "work/llama-4-maverick#low");
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 120_000);

test("the test chat answers in every column, labels versions, restarts, and poses as External MCP", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [], streams: string[] = [];
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    const until = async (what: string, check: () => Promise<boolean>) => {
      for (const deadline = Date.now() + 20_000; !await check();) if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`); else await Bun.sleep(100);
    };
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    const read = async (id: string) => (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft;
    const apply = async (id: string, op: object) => (await call<{ revision: string }>("ApplyAgentDraftOp", { id, op: { baseRevision: (await read(id)).revision, opId: crypto.randomUUID(), ...op } })).revision;
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    const keepInterceptionEnabled = `${alice}/test/keep-interception-enabled`;
    await page.route(keepInterceptionEnabled, (route: Route) => route.continue());
    const reads: { at: number; request: RoutedRequest }[] = [];
    const frames: { at: number; conversationId: string; revision: string }[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("request", (request: RoutedRequest) => {
      const url = new URL(request.url());
      if (url.pathname === "/stream") streams.push(url.searchParams.get("id")!);
      if (url.pathname === "/api/History") reads.push({ at: Date.now(), request });
    });
    const network = await page.context().newCDPSession(page);
    network.on("Network.eventSourceMessageReceived", ({ eventName, data }: { eventName: string; data: string }) => {
      if (eventName === "message") frames.push({ at: Date.now(), ...JSON.parse(data) as { conversationId: string; revision: string } });
    });
    await network.send("Network.enable");
    const chat = page.getByRole("region", { name: "Test chat" });
    const row = (n: number) => chat.getByRole("region", { name: `Message ${n}`, exact: true });
    const cell = (n: number, column: string) => row(n).getByRole("region", { name: `${column} answer`, exact: true });
    const message = chat.getByRole("textbox", { name: "Test message" });
    const send = async (text: string, button = "Send") => {
      await message.fill(text);
      await chat.getByRole("button", { name: button, exact: true }).click();
    };
    const checkHeld = async (rpc: "DraftPrompt" | "RestartDraftChat", expected: { id: string; text: string } | { id: string; replay: string[]; replayEarlier: boolean }, check: (release: () => Promise<void>) => Promise<string | undefined>) => {
      const url = `${alice}/api/${rpc}`, requests: RoutedRequest[] = [], intercepted: HeldRequest[] = [];
      const reached = Promise.withResolvers<RoutedRequest>();
      const matches = (request: RoutedRequest) => request.url() === url && request.method() === "POST" && isDeepStrictEqual(request.postDataJSON(), expected);
      const track = (request: RoutedRequest) => { if (request.url() === url) requests.push(request); };
      page.on("request", track);
      try {
        const release = await hold(page, new RegExp(`/api/${rpc}$`), {
          only: matches,
          onHeld: (entry) => {
            intercepted.push(entry);
            reached.resolve(entry.request);
          },
        });
        const arrived = page.waitForRequest(async (request: RoutedRequest) => matches(request) && request === await reached.promise);
        const accepted = page.waitForResponse(async (response: { url(): string; request(): RoutedRequest }) => response.url() === url && response.request() === await arrived).then(async (response: { status(): number; json(): Promise<unknown> }) => {
          expect(response.status(), `${rpc} response status`).toBe(200);
          const reply = await response.json() as DraftPromptResponse;
          expect(reply, `${rpc} reply`).toMatchObject({ refused: "", restart: false });
          return reply.testChat;
        });
        const [acknowledged, persisted] = await Promise.all([accepted, check(async () => {
          await arrived;
          await release();
        })]);
        expect(acknowledged, `${rpc} response chat ID (expected the persisted chat ID)`).toBe(persisted);
        const held: { method: string; body: unknown; routeError?: string }[] = intercepted.map(({ request, routeError }) => ({
          method: request.method(), body: request.postDataJSON(), routeError,
        }));
        expect(held).toEqual([
          { method: "POST", body: expected, routeError: undefined },
        ]);
      } catch (cause) {
        const traffic = await Promise.all([...new Set([...requests, ...intercepted.map(({ request }) => request)])].map(async (request) => {
          const match = intercepted.find((entry) => entry.request === request);
          const response = request.existingResponse();
          return {
            method: request.method(), body: request.postData(), intercepted: !!match, routeError: match?.routeError,
            status: response?.status(), response: response && request.timing().responseEnd >= 0 ? await response.text().catch(String) : null,
            failure: request.failure()?.errorText,
          };
        }));
        const dom = await chat.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => ({
          text: node.innerText,
          composer: node.querySelector("textarea")?.value,
          disabled: [...node.querySelectorAll("button:disabled")].map((button) => button.textContent),
        }))).catch(String);
        throw new Error(`${rpc}: held request and chat state: ${JSON.stringify({ expected, traffic, dom, pageErrors: errors })}`, { cause });
      } finally {
        page.off("request", track);
      }
    };

    const echo = await call<{ id: string }>("CreateAgentDraft", { name: "echo", mode: "advanced" });
    await apply(echo.id, { kind: "edit_file", variant: "a", value: "---\nmodel: gpt-a\n---\nHelp.\n" });
    const revA = Number((await read(echo.id)).revision);
    await page.goto(`${alice}/agents/drafts/${echo.id}`);
    await chat.getByText("Test messages use real tools.", { exact: false }).waitFor();
    await send("first");
    await cell(1, "echo").getByText("gpt-a answer 1", { exact: false }).waitFor();
    await cell(1, "echo").getByText(`r${revA}`, { exact: true }).waitFor();

    await chat.getByRole("button", { name: "Add a variant", exact: true }).click();
    await chat.getByText("echo/b", { exact: true }).waitFor();
    const revB = Number(await apply(echo.id, { kind: "set_field", variant: "b", key: "model", value: "gpt-b" }));
    await cell(1, "echo/b").getByText("Not sent: this column did not exist yet.", { exact: true }).waitFor();
    await send("second");
    await cell(2, "echo/b").getByText("gpt-b answer 2", { exact: false }).waitFor();
    await cell(2, "echo").getByText("gpt-a answer 2", { exact: false }).waitFor();
    await cell(2, "echo/b").getByText(`r${revB}`, { exact: true }).waitFor();

    const revB2 = Number(await apply(echo.id, { kind: "set_field", variant: "b", key: "model", value: "gpt-b2" }));
    await cell(2, "echo/b").getByText(`r${revB} · changed since`, { exact: true }).waitFor();
    await cell(2, "echo").getByText(`r${revA}`, { exact: true }).waitFor();
    expect(await row(2).getByText("changed since", { exact: false }).count()).toBe(1);
    await chat.getByText(`next message uses r${revB2}`, { exact: true }).waitFor();

    const revA2 = Number(await apply(echo.id, { kind: "set_field", variant: "a", key: "model", value: "gpt-a2" }));
    await send("third");
    await cell(3, "echo").getByText("gpt-a2 answer 3", { exact: false }).waitFor();
    await cell(3, "echo").getByText(`r${revA2}`, { exact: true }).waitFor();
    await cell(3, "echo/b").getByText("gpt-b2 answer 3", { exact: false }).waitFor();
    await cell(1, "echo").getByText(`r${revA} · changed since`, { exact: true }).waitFor();
    for (const name of ["Restart", "Restart & replay"]) await chat.getByRole("button", { name, exact: true }).waitFor();

    await apply(echo.id, { kind: "set_field", variant: "b", key: "sticky", value: "maybe" });
    await send("fourth");
    await cell(4, "echo").getByText("gpt-a2 answer 4", { exact: false }).waitFor();
    await cell(4, "echo/b").getByText("Not run. Shared chat: off, user, global or slack.", { exact: true }).waitFor();

    await apply(echo.id, { kind: "set_field", variant: "a", key: "sticky", value: "maybe" });
    await send("fifth");
    await chat.getByRole("alert").getByText("Fix the draft's settings before testing it.", { exact: false }).waitFor();
    await chat.getByRole("alert").getByText("Shared chat: off, user, global or slack.", { exact: false }).waitFor();
    await Bun.sleep(500);
    expect(await row(5).count()).toBe(0);
    await apply(echo.id, { kind: "set_field", variant: "a", key: "sticky", value: "" });
    await apply(echo.id, { kind: "set_field", variant: "b", key: "sticky", value: "" });

    const first = (await read(echo.id)).testChat;
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await until("a new chat", async () => (await read(echo.id)).testChat !== first);
    const second = (await read(echo.id)).testChat;
    for (const [n, text] of ["first", "second", "third", "fourth"].entries()) {
      await row(n + 1).getByText(text, { exact: true }).first().waitFor();
      await cell(n + 1, "echo").getByText(`gpt-a2 answer ${n + 1}`, { exact: false }).waitFor();
      await cell(n + 1, "echo/b").getByText(`gpt-b2 answer ${n + 1}`, { exact: false }).waitFor();
    }
    expect(new Set(streams.filter((id) => !id.startsWith("draft:")))).toEqual(new Set([first, second]));
    await chat.getByRole("button", { name: "Restart", exact: true }).click();
    await chat.getByText("Send a test message.", { exact: false }).waitFor();
    expect(await row(1).count()).toBe(0);

    const instructions = page.getByRole("textbox", { name: /^Instructions/ });
    const typed = async (text: string) => {
      const held = await hold(page, /\/api\/ApplyAgentDraftOp$/);
      await instructions.click();
      await instructions.press("ControlOrMeta+End");
      await instructions.pressSequentially(text);
      return held;
    };
    const labelled = async (text: string) => {
      await until("the edit is stored", async () => (await read(echo.id)).variants[0].body.endsWith(text));
      const { draft, ops } = await call<AgentDraftView>("GetAgentDraft", { id: echo.id, since: "1" });
      await cell(1, "echo").getByText(`r${ops.filter((op) => op.kind === "edit_body").at(-1)!.revision}`, { exact: true }).waitFor();
      return draft.testChat;
    };
    let release = await typed(" Be brief.");
    await send("brief");
    await Bun.sleep(300);
    await release();
    const briefChat = await labelled(" Be brief.");
    await checkHeld("DraftPrompt", { id: echo.id, text: "brief again" }, async (release) => {
      await send("brief again");
      await message.fill("still typing");
      const releasedAt = Date.now();
      await release();
      await cell(2, "echo").getByText("gpt-a2 answer 2", { exact: false }).waitFor().catch(async (cause: unknown) => {
        const history = await call<HistoryView>("History", { id: briefChat }).then(({ messages, running, terminal }) => ({
          running, terminal, messages: messages.map(({ role, text, turnId, complete }) => ({ role, text, turnId, complete })),
        })).catch(String);
        const browserHistory = await Promise.all(reads.filter(({ request }) => request.postDataJSON().id === briefChat).map(async ({ at, request }) => {
          const response = request.existingResponse(), status = response?.status();
          const reply = response && request.timing().responseEnd >= 0 ? await (status === 200
            ? response.json().then((value) => {
              const { revision, reset, entryKeys, replacedKeys, removedKeys, messages, running } = value as HistoryView;
              return { revision, reset, entryKeys, replacedKeys, removedKeys, running, messages: messages.map(({ role, text, turnId, entryKey, inputId, complete }) => ({ role, text, turnId, entryKey, inputId, complete })) };
            })
            : response.text()).catch(String) : null;
          return { msFromRelease: at - releasedAt, request: request.postDataJSON(), status, failure: request.failure()?.errorText, reply };
        }));
        const transcriptFrames = frames.filter(({ conversationId }) => conversationId === briefChat).map(({ at, revision }) => ({ msFromRelease: at - releasedAt, revision }));
        throw new Error(`Held DraftPrompt did not render Message 2 for ${briefChat}: ${JSON.stringify({ history, transcriptFrames, browserHistory })}`, { cause });
      });
      expect(await message.inputValue()).toBe("still typing");
      return briefChat;
    });
    release = await typed(" Really.");
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await Bun.sleep(300);
    await release();
    await labelled(" Really.");

    const file = page.getByRole("textbox", { name: "Agent file" });
    await page.getByRole("tab", { name: /^File/ }).click();
    await message.fill("from the file");
    await file.fill((await file.inputValue()).replace("model: gpt-a2", "model: gpt-a3"));
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await cell(3, "echo").getByText("gpt-a3 answer 3", { exact: false }).waitFor();

    const queue = await call<{ id: string }>("CreateAgentDraft", { name: "queue", mode: "advanced" });
    await apply(queue.id, { kind: "edit_file", variant: "a", value: "---\nmodel: gpt-held\n---\nHelp.\n" });
    await page.goto("about:blank");
    await page.clock.install();
    await page.goto(`${alice}/agents/drafts/${queue.id}`);
    await send("one");
    await row(1).getByText("one", { exact: true }).first().waitFor();
    await Promise.all([
      page.waitForResponse((reply: { url(): string; request(): { postDataJSON(): { id: string; text: string } } }) => {
        if (reply.url() !== `${alice}/api/DraftPrompt`) return false;
        const { id, text } = reply.request().postDataJSON();
        return id === queue.id && text === "two";
      }),
      send("two"),
    ]);
    await page.clock.runFor(2_000);
    await chat.getByText("Waiting its turn: two", { exact: true }).waitFor();
    const held = (await read(queue.id)).testChat;
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await until("a new chat", async () => (await read(queue.id)).testChat !== held);
    expect((await fetch(`${alice}/test/release`, { method: "POST" })).ok).toBe(true);
    for (const [n, text] of ["one", "two"].entries()) {
      await row(n + 1).getByText(text, { exact: true }).first().waitFor();
      await cell(n + 1, "queue").getByText(`gpt-held answer ${n + 1}`, { exact: false }).waitFor();
    }

    const replayed = (await read(queue.id)).testChat;
    release = await hold(page, /\/api\/History$/, { only: (request) => {
      const body = request.postDataJSON();
      return body.id === replayed && !!body.limit;
    } });
    await send("three");
    await until("the turn starts", async () => (await call<{ messages: { role: string; text: string }[] }>("History", { id: replayed })).messages.some((line) => line.role === "user" && line.text === "three"));
    await Bun.sleep(2500);
    await chat.getByText("Waiting its turn: three", { exact: true }).waitFor({ state: "detached" });
    expect(await row(3).count()).toBe(0);
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await until("a new chat", async () => (await read(queue.id)).testChat !== replayed);
    for (const [n, text] of ["one", "two", "three"].entries()) {
      await row(n + 1).getByText(text, { exact: true }).first().waitFor();
      await cell(n + 1, "queue").getByText(`gpt-held answer ${n + 1}`, { exact: false }).waitFor();
    }
    await release();

    const restarting = (await read(queue.id)).testChat;
    await checkHeld("RestartDraftChat", { id: queue.id, replay: [], replayEarlier: true }, async (release) => {
      await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
      for (const name of ["Restart", "Restart & replay"]) await chat.getByRole("button", { name, exact: true, disabled: true }).waitFor();
      await release();
      let observed: string | undefined;
      await until("a new persisted test chat", async () => {
        observed = (await read(queue.id)).testChat;
        return !!observed && observed !== restarting;
      }).catch((cause) => {
        throw new Error(`Restart & replay: draft ${queue.id} last reported test chat ${JSON.stringify(observed)}; expected a non-empty ID different from ${JSON.stringify(restarting)}`, { cause });
      });
      await cell(3, "queue").getByText("gpt-held answer 3", { exact: false }).waitFor();
      await chat.getByRole("button", { name: "Restart & replay", exact: true, disabled: false }).waitFor();
      expect(await row(4).count()).toBe(0);
      return observed;
    });

    const support = await call<{ id: string }>("CreateAgentDraft", { name: "support-bot", copy: true });
    await page.goto(`${alice}/agents/drafts/${support.id}`);
    await send("help me");
    await cell(1, "Live").getByText("gpt-6.1-sol answer 1", { exact: false }).waitFor();
    await cell(1, "support-bot").getByText("gpt-6.1-sol answer 1", { exact: false }).waitFor();
    const liveOne = (await cell(1, "Live").getByText(/^live [0-9a-f]{12}$/).textContent())!;
    await Bun.write(`${process.env.ROCKETCLAW_EDITOR_WORKSPACE}/agents/support-bot.md`, "---\ndescription: Answers support questions\nmodel: gpt-live-2\npermission: {}\n---\nYou answer support questions.\n");
    await call("Reload", { reason: "live agent changed" });
    await send("again");
    await cell(2, "Live").getByText("gpt-live-2 answer 2", { exact: false }).waitFor();
    const liveTwo = (await cell(2, "Live").getByText(/^live [0-9a-f]{12}$/).textContent())!;
    expect(liveTwo).not.toBe(liveOne);
    expect(await cell(1, "Live").getByText(liveOne, { exact: true }).count()).toBe(1);

    await apply(support.id, { kind: "rename", value: "support-bot-2" });
    await chat.getByText("The original stays as it is", { exact: false }).waitFor();
    await send("renamed");
    await cell(3, "support-bot-2").getByText("answer 3", { exact: false }).waitFor();
    expect(await cell(3, "Live").count()).toBe(0);

    const desk = await call<{ id: string }>("CreateAgentDraft", { name: "helpdesk", copy: true });
    await page.goto(`${alice}/agents/drafts/${desk.id}`);
    const toggle = chat.getByRole("switch", { name: "Test as External MCP" });
    await chat.getByText("External MCP only serves sticky: off agents", { exact: false }).waitFor();
    expect(await toggle.isDisabled()).toBe(true);

    await apply(desk.id, { kind: "set_field", variant: "a", key: "sticky", value: "off" });
    await until("the switch is enabled", async () => !await toggle.isDisabled());
    await chat.getByText("$ROCKETCLAW_METADATA_CUSTOMER_ID comes only from External MCP", { exact: false }).waitFor();
    const mcpChanges = async () => (await call<AgentDraftView>("GetAgentDraft", { id: desk.id, since: "1" })).ops.filter((op) => op.kind === "set_external_mcp").length;
    let made = await mcpChanges();
    release = await hold(page, /\/api\/ApplyAgentDraftOp$/, { count: 1 });
    await chat.getByRole("button", { name: "Test as External MCP", exact: true }).click();
    await toggle.click();
    await Bun.sleep(300);
    expect(await mcpChanges()).toBe(made);
    await release();
    await until("both changes are stored", async () => await mcpChanges() === made + 2);
    const value = (key: string) => chat.getByRole("textbox", { name: `Value of ${key}`, exact: true });
    await value("customer_id").waitFor();
    await message.fill("who am I");
    expect(await chat.getByRole("button", { name: "Send", exact: true }).isDisabled()).toBe(true);
    await chat.getByText("Fill customer_id first", { exact: false }).waitFor();
    await value("customer_id").fill("acme-042");
    await value("customer_id").press("Tab");
    await chat.getByRole("textbox", { name: "Caller name", exact: true }).fill("zendesk-bridge");
    await chat.getByRole("textbox", { name: "Caller name", exact: true }).press("Tab");
    await until("the pairs are stored", async () => (await read(desk.id)).externalMcp?.caller === "zendesk-bridge" && (await read(desk.id)).externalMcp?.metadata?.customer_id === "acme-042");
    await send("who am I");
    await cell(1, "helpdesk").getByText("gpt-help answer 1", { exact: false }).waitFor();
    await row(1).getByText("customer_id=acme-042", { exact: true }).waitFor();
    await row(1).getByText("from zendesk-bridge", { exact: false }).waitFor();
    await cell(1, "Live").getByText("Not run. Shared chat: External MCP only serves sticky: off agents, and the live agent is not.", { exact: true }).waitFor();

    const pair = async (key: string, text: string) => {
      await chat.getByRole("textbox", { name: "New metadata key", exact: true }).fill(key);
      await chat.getByRole("textbox", { name: "New metadata value", exact: true }).fill(text);
      await chat.getByRole("button", { name: "Add pair", exact: true }).click();
    };
    made = await mcpChanges();
    release = await hold(page, /\/api\/ApplyAgentDraftOp$/, { count: 1 });
    await pair("ticket", "77");
    await pair("channel", "email");
    await Bun.sleep(300);
    await release();
    await until("both changes are stored", async () => await mcpChanges() === made + 2);
    expect((await read(desk.id)).externalMcp?.metadata).toMatchObject({ ticket: "77", channel: "email" });

    await value("ticket").waitFor();
    await send("and the ticket?");
    await row(2).getByText("ticket=77 · this message only", { exact: true }).waitFor();
    await cell(2, "helpdesk").getByText("gpt-help answer 2", { exact: false }).waitFor();
    const before = (await read(desk.id)).testChat;
    await value("customer_id").fill("acme-043");
    await value("customer_id").press("Tab");
    await send("as the new customer", "Restart with new metadata");
    await until("a new chat", async () => (await read(desk.id)).testChat !== before);
    await row(1).getByText("customer_id=acme-043", { exact: true }).waitFor();
    await cell(1, "helpdesk").getByText("gpt-help answer 1", { exact: false }).waitFor();
    expect(await row(2).count()).toBe(0);

    for (const [key, refusal] of [
      ["customer-id", "Metadata keys customer-id and customer_id both become $ROCKETCLAW_METADATA_CUSTOMER_ID: keep one of them."],
      ["external-conversation-id", "External MCP sets $ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID to the conversation's id: remove the metadata key external-conversation-id."],
    ]) {
      await pair(key, "acme-043");
      await chat.getByText(refusal, { exact: true }).waitFor();
      await message.fill("refused");
      expect(await chat.getByRole("button", { name: "Send", exact: true }).isDisabled()).toBe(true);
      await chat.getByRole("button", { name: `Remove ${key}`, exact: true }).click();
      await chat.getByText(refusal, { exact: true }).waitFor({ state: "detached" });
    }

    await apply(desk.id, { kind: "set_external_mcp", externalMcp: { on: true, caller: "zendesk-bridge", metadata: { "customer-id": "acme-043", ticket: "77", channel: "email" } } });
    await value("customer-id").waitFor();
    expect(await chat.getByLabel("Fixed by the chat's first message", { exact: true }).count()).toBe(4);
    await send("renamed");
    await row(2).getByText("customer-id=acme-043", { exact: true }).waitFor();
    await cell(2, "helpdesk").getByText("gpt-help answer 2", { exact: false }).waitFor();

    const undo = await refuse(page);
    await chat.getByRole("button", { name: "Add a variant", exact: true }).click();
    await chat.getByRole("alert").getByText("Refused add_variant.", { exact: true }).waitFor();
    await chat.getByRole("textbox", { name: "Caller name", exact: true }).fill("other-bridge");
    await chat.getByRole("textbox", { name: "Caller name", exact: true }).press("Tab");
    await chat.getByRole("alert").getByText("Refused set_external_mcp.", { exact: true }).waitFor();
    await undo();
    await apply(desk.id, { kind: "set_field", variant: "a", key: "sticky", value: "user" });
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await chat.getByRole("alert").getByText("External MCP only serves sticky: off agents: set sticky to off in every variant", { exact: false }).waitFor();
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 240_000);

test("the assistant suggests changes as cards that anyone on the draft accepts or rejects", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!, bob = process.env.ROCKETCLAW_EDITOR_BOB_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [];
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    const open = async () => {
      const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
      page.on("pageerror", (error: Error) => errors.push(error.message));
      return page;
    };
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    const a = await open(), b = await open();
    const panel = (page: typeof a) => page.getByRole("region", { name: "Assistant" });
    const card = (page: typeof a, text: string) => panel(page).getByRole("article").filter({ hasText: text });

    await a.goto(`${alice}/agents/new`);
    await a.getByRole("radio", { name: "Easy agent" }).click();
    await a.getByRole("radio", { name: "Describe it" }).click();
    await a.getByRole("textbox", { name: "Agent name" }).fill("billing");
    await a.getByRole("textbox", { name: "What should it do?" }).fill("An agent that answers billing questions");
    await a.getByRole("button", { name: "Start and describe", exact: true }).click();
    await a.waitForURL(/\/agents\/drafts\/[^/]+$/);
    const id = a.url().split("/").at(-1)!;
    const described = card(a, "A billing agent for the Billing space.");
    await described.getByText("It serves the billing team.", { exact: true }).waitFor();
    await described.getByText("From your description.", { exact: true }).waitFor();
    const access = described.getByRole("listitem").filter({ hasText: "RocketClaw tools" });
    await access.getByText("Changes what this agent may do", { exact: false }).waitFor();
    await access.getByText("All five groups.", { exact: false }).waitFor();
    await access.getByText("[sessions]", { exact: false }).waitFor();
    expect(await described.getByRole("listitem").filter({ hasText: "Space" }).getByText("Changes what this agent may do", { exact: false }).count()).toBe(0);

    await a.getByRole("group", { name: "Space", exact: true }).getByText("Suggested: Billing", { exact: true }).waitFor();
    await a.getByRole("tab", { name: /^File/ }).click();
    const diff = a.getByRole("region", { name: "Suggested change to the file" });
    for (const line of ["+ space: Billing", "+ rocketclaw: [sessions]", "+ You answer billing questions."]) await diff.getByText(line, { exact: true }).waitFor();
    const before = (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft;
    expect(before.variants[0].settings.map(({ key }) => key)).not.toContain("space");
    expect(before.variants[0].body).toBe("");

    await b.goto(`${bob}/agents/drafts/${id}`);
    await card(b, "A billing agent for the Billing space.").getByRole("button", { name: "Accept", exact: true }).click();
    await a.getByRole("tab", { name: "Form", exact: true }).click();
    await a.waitForFunction(() => (document.querySelector("input[aria-label=Space]") as HTMLInputElement | null)?.value === "Billing");
    await described.getByText("Accepted", { exact: true }).waitFor();
    expect(await a.getByText("Suggested: Billing", { exact: true }).count()).toBe(0);
    await b.getByRole("tab", { name: "History", exact: true }).click();
    await b.getByText("Assistant (accepted by bob)", { exact: false }).first().waitFor();

    await panel(a).getByRole("textbox", { name: "Ask the assistant" }).fill("Let each person keep a chat");
    await panel(a).getByRole("button", { name: "Suggest", exact: true }).click();
    await card(b, "Each person gets a chat of their own.").getByText("Every person keeps their own chat.", { exact: true }).waitFor();
    const accepted = (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft;
    await card(b, "Each person gets a chat of their own.").getByRole("button", { name: "Reject", exact: true }).click();
    for (const page of [a, b]) await card(page, "Each person gets a chat of their own.").waitFor({ state: "detached" });
    expect((await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft).toEqual(accepted);

    const fixer = await call<{ id: string; revision: string }>("CreateAgentDraft", { name: "fixer", mode: "advanced" });
    const apply = async (op: object) => call("ApplyAgentDraftOp", { id: fixer.id, op: { baseRevision: (await call<AgentDraftView>("GetAgentDraft", { id: fixer.id, since: "1" })).draft.revision, opId: crypto.randomUUID(), ...op } });
    await apply({ kind: "edit_file", variant: "a", value: "---\nmodel: gpt-a\n---\nHelp.\n" });
    await apply({ kind: "add_variant", variant: "b", value: "a" });
    await apply({ kind: "set_field", variant: "b", key: "model", value: "gpt-b" });
    await a.goto(`${alice}/agents/drafts/${fixer.id}`);
    const chat = a.getByRole("region", { name: "Test chat" });
    await chat.getByRole("textbox", { name: "Test message" }).fill("hello");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    const answer = chat.getByRole("region", { name: "fixer/b answer", exact: true });
    await answer.getByText("gpt-b answer 1", { exact: false }).waitFor();
    await answer.getByRole("button", { name: "Fix this", exact: true }).click();
    await panel(a).getByText("gpt-b answer 1", { exact: false }).waitFor();
    await panel(a).getByRole("button", { name: "Suggest", exact: true }).click();
    const fixed = card(a, "It said gpt-b answer 1.");
    await fixed.getByText("Instructions · fixer/b", { exact: true }).waitFor();
    await fixed.getByText("The answer skipped the greeting.", { exact: true }).waitFor();
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 180_000);

test("saving picks a variant and a layer, warns, reviews live changes, and follows pull requests", async () => {
  const alice = process.env.ROCKETCLAW_EDITOR_ALICE_URL!, bob = process.env.ROCKETCLAW_EDITOR_BOB_URL!, workspace = process.env.ROCKETCLAW_EDITOR_WORKSPACE!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const errors: string[] = [];
    const call = async <T>(method: string, data: object) => {
      const response = await fetch(`${alice}/api/${method}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data) });
      expect(response.status).toBe(200);
      return await response.json() as T;
    };
    const until = async (what: string, check: () => Promise<boolean>) => {
      for (const deadline = Date.now() + 20_000; !await check();) if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`); else await Bun.sleep(100);
    };
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    const read = async (id: string) => (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft;
    const apply = async (id: string, op: object) => call("ApplyAgentDraftOp", { id, op: { baseRevision: (await read(id)).revision, opId: crypto.randomUUID(), ...op } });
    const open = async (url: string) => {
      const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
      page.on("pageerror", (error: Error) => errors.push(error.message));
      await page.goto(url);
      return page;
    };
    const dialog = (page: Awaited<ReturnType<typeof open>>) => page.getByRole("dialog");
    const choose = async (page: Awaited<ReturnType<typeof open>>, destination: string) => {
      if (!await dialog(page).count()) await page.getByRole("button", { name: "Save…", exact: true }).click();
      await dialog(page).getByRole("radio", { name: `Destination ${destination}`, exact: true }).click();
    };
    const instructions = (page: Awaited<ReturnType<typeof open>>) => page.getByRole("textbox", { name: /^Instructions/ });

    const a = await open(`${alice}/agents`);
    await a.getByRole("link", { name: "Create New Agent", exact: true }).click();
    await a.waitForURL(`${alice}/agents/new`);
    await a.goto(`${alice}/agents`);
    await a.getByRole("button", { name: "Edit router", exact: true }).dblclick();
    await a.getByRole("heading", { name: "Edit router", exact: true }).waitFor();
    const id = a.url().split("/").at(-1)!;
    await a.locator(`[data-tab="/agents/drafts/${id}"]`).getByText("Edit router", { exact: true }).waitFor();
    const b = await open(`${bob}/agents/drafts/${id}`);

    await choose(a, "Workspace");
    await dialog(a).getByText("- # Routes every request.", { exact: true }).waitFor();
    await dialog(a).getByText("provides it now", { exact: true }).waitFor();
    await apply(id, { kind: "set_field", variant: "a", key: "description", value: "Routes every request" });
    await dialog(a).getByText("+ description: Routes every request", { exact: true }).waitFor();
    await choose(a, "github.com/acme/team");
    await dialog(a).getByText("Hidden by Workspace: its router.md wins, so this save will not be the router that runs.", { exact: true }).waitFor();
    expect(await dialog(a).getByRole("button", { name: "Choose push or pull request", exact: true }).isDisabled()).toBe(true);
    await dialog(a).getByRole("radio", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByText("Waiting for merge:", { exact: false }).waitFor();
    await dialog(a).getByRole("button", { name: "Done", exact: true }).click();

    for (const page of [a, b]) {
      await page.getByRole("link", { name: "Pull request #7", exact: true }).first().waitFor();
      await page.getByText("Your next edit stops waiting", { exact: false }).waitFor();
    }
    expect(await a.getByRole("link", { name: "Pull request #7", exact: true }).first().getAttribute("href")).toBe("https://github.com/acme/team/pull/7");

    await b.getByRole("button", { name: "Stop waiting", exact: true }).click();
    for (const page of [a, b]) {
      await page.getByText("Draft · saved on the server", { exact: true }).waitFor();
      await page.getByText("is still open on GitHub.", { exact: false }).waitFor();
      await page.getByRole("link", { name: "Pull request #7", exact: true }).waitFor();
    }

    await choose(a, "github.com/acme/team");
    await dialog(a).getByRole("radio", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Done", exact: true }).click();
    await b.getByText("Your next edit stops waiting", { exact: false }).waitFor();
    await instructions(b).click();
    await instructions(b).press("ControlOrMeta+End");
    await instructions(b).pressSequentially("Thanks.\n", { delay: 20 });
    await until("the wait ended", async () => (await read(id)).saveState === "editing");
    for (const page of [a, b]) await page.getByText("is still open on GitHub.", { exact: false }).waitFor();

    await choose(a, "github.com/acme/team");
    await dialog(a).getByRole("radio", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Done", exact: true }).click();
    await b.getByText("Your next edit stops waiting", { exact: false }).waitFor();
    await Bun.write(process.env.ROCKETCLAW_EDITOR_PR_STATE!, "CLOSED\n");
    await a.reload();
    for (const page of [a, b]) await page.getByText("The pull request was closed on GitHub.", { exact: true }).waitFor();
    expect(await b.getByText("is still open on GitHub.", { exact: false }).count()).toBe(0);

    await until("bob's line is saved", async () => (await read(id)).variants[0].body.includes("Thanks."));
    await apply(id, { kind: "set_field", variant: "a", key: "model", value: "gpt-mine" });
    await Bun.write(`${workspace}/agents/router.md`, "---\n# Routes every request.\ndescription: Routes requests\nmodel: gpt-theirs\npermission: {}\n---\nRoute each request.\nEscalate billing to Ops.\n");
    await choose(a, "Workspace");
    await dialog(a).getByRole("button", { name: "Save to workspace", exact: true }).click();
    await dialog(a).getByText("Changed in both", { exact: false }).waitFor();
    expect(await dialog(a).getByRole("button", { name: "Bring in and save", exact: true }).isDisabled()).toBe(true);
    await dialog(a).getByRole("button", { name: "Keep mine for Model", exact: true }).click();
    expect(await dialog(a).getByRole("button", { name: "Take theirs for Instructions", exact: true }).getAttribute("aria-pressed")).toBe("true");
    await dialog(a).getByRole("button", { name: "Bring in and save", exact: true }).click();
    await dialog(a).getByText("Saved and reloaded: router is live.", { exact: true }).waitFor();
    const saved = await Bun.file(`${workspace}/agents/router.md`).text();
    expect(saved).toContain("model: gpt-mine");
    expect(saved).toContain("Escalate billing to Ops.");
    expect(saved).toContain("Thanks.");
    expect(saved).not.toContain("#");
    await until("bob has the line", async () => (await instructions(b).inputValue()).includes("Escalate billing to Ops."));
    await dialog(a).getByRole("button", { name: "Done", exact: true }).click();

    await apply(id, { kind: "set_field", variant: "a", key: "sticky", value: "maybe" });
    await choose(a, "Workspace");
    await dialog(a).getByRole("button", { name: "Save to workspace", exact: true }).click();
    await a.getByRole("group", { name: "Shared chat", exact: true }).getByText("Not saved. Allowed: off, user, global or slack.", { exact: true }).waitFor();
    expect(await dialog(a).count()).toBe(0);
    await a.getByRole("alert").getByText("Not saved. Fix the draft's settings before saving it. Shared chat: off, user, global or slack.", { exact: true }).waitFor();

    const hold = await call<{ id: string }>("CreateAgentDraft", { name: "hold", mode: "advanced" });
    await apply(hold.id, { kind: "edit_file", variant: "a", value: "---\nmodel: gpt-hold\n---\nHold.\n" });
    await a.goto(`${alice}/agents/drafts/${hold.id}`);
    await b.goto(`${bob}/agents/drafts/${hold.id}`);
    await choose(a, "Workspace");
    await choose(b, "Workspace");
    await dialog(a).getByRole("button", { name: "Save to workspace", exact: true }).click();
    await until("alice's save runs", async () => (await read(hold.id)).saveState === "saving");
    await dialog(b).getByRole("button", { name: "Save to workspace", exact: true }).click();
    await dialog(b).getByText("alice is saving this draft already.", { exact: true }).waitFor();
    expect((await fetch(`${alice}/test/release-save`, { method: "POST" })).ok).toBe(true);
    await dialog(a).getByText("Saved and reloaded: hold is live.", { exact: true }).waitFor();

    const main = await call<{ id: string }>("CreateAgentDraft", { name: "main", copy: true });
    await a.goto(`${alice}/agents/drafts/${main.id}`);
    await choose(a, "Workspace");
    await dialog(a).getByText("Replaces the built-in main: its future updates will no longer reach this workspace.", { exact: true }).waitFor();

    await a.goto(`${alice}/agents`);
    await a.getByRole("button", { name: "Edit router", exact: true }).click();
    await a.waitForURL(`${alice}/agents/drafts/${id}`);
    expect((await call<{ drafts: AgentDraft[] }>("ListAgentDrafts", {})).drafts.filter((draft) => draft.name === "router")).toHaveLength(1);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 240_000);
