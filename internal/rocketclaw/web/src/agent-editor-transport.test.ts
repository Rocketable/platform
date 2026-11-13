import { expect, test } from "bun:test";
import type { AgentDraft, AgentDraftView } from "./types";

type Body = { op: { kind: string }; id?: string; limit?: number };
type Route = { fetch(): Promise<object>; fulfill(options: object): Promise<void>; continue(): Promise<void>; request(): { postDataJSON(): Body } };
type Routed = { route(url: RegExp, handler: (route: Route) => Promise<void>): Promise<void>; unroute(url: RegExp): Promise<void> };

// Holds the page's first count API requests url matches, of those whose body only accepts, until the returned release
// sends them; with respond, each is sent at once and only its answer is held. Release lets the held ones finish before
// it unroutes, since unrouting sends requests still held on to the server, a second time for one already sent.
async function hold(page: Routed, url: RegExp, { respond = false, count = Infinity, only = () => true }: { respond?: boolean; count?: number; only?: (body: Body) => boolean } = {}) {
  let release = () => {}, held = 0;
  const gate = new Promise<void>((resolve) => { release = resolve; }), handled: Promise<void>[] = [];
  await page.route(url, async (route) => {
    if (!only(route.request().postDataJSON()) || held++ >= count) return await route.continue().catch(() => {});
    const answered = (async () => {
      const response = respond ? await route.fetch() : undefined;
      await gate;
      await (response ? route.fulfill({ response }) : route.continue());
    })().catch(() => {});
    handled.push(answered);
    await answered;
  });
  return async () => {
    count = 0;
    release();
    await Promise.all(handled);
    await page.unroute(url);
  };
}

// The server refuses the page's draft changes, naming each one's kind, until the returned undo.
async function refuse(page: Routed) {
  const url = /\/api\/ApplyAgentDraftOp$/;
  await page.route(url, (route) => route.fulfill({ status: 400, json: { code: "invalid_argument", message: `Refused ${route.request().postDataJSON().op.kind}.` } }));
  return () => page.unroute(url);
}

// Go's TestAgentEditorRealBackendBrowser owns the backend: alice's browser at ALICE, and bob's at BOB, where bob is
// limited to Ops until /test/unlimit.
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

    // Easy or advanced comes first, then how to start.
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

    // A person limited to spaces gets a plain refusal and the way back to the read-only Agents page.
    await b.goto(`${bob}/agents/drafts/${id}`);
    await b.getByText("People limited to spaces cannot use the agent editor", { exact: false }).waitFor();
    await b.getByRole("link", { name: "Back to the Agents page", exact: true }).click();
    await b.waitForURL(`${bob}/agents`);
    await b.getByRole("heading", { name: "Agents", exact: true }).waitFor();
    await Bun.sleep(500);
    expect(await b.getByRole("link", { name: "Create New Agent" }).count()).toBe(0);
    expect(await b.getByRole("button", { name: /^Edit / }).count()).toBe(0);

    // Once unlimited, bob opens the shared link: the same draft, and both people are shown in both browsers.
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    await b.goto(`${bob}/agents/drafts/${id}`);
    await b.getByText("triage/c.md · easy", { exact: true }).waitFor();
    for (const page of [a, b]) for (const who of ["alice", "bob"]) await page.getByRole("img", { name: `${who} is here`, exact: true }).waitFor();

    // Alice switches the whole draft to advanced, and every variant follows in bob's browser.
    await a.getByRole("button", { name: "Advanced", exact: true }).click();
    for (const file of ["triage.md", "triage/b.md", "triage/c.md"]) await b.getByText(`${file} · advanced`, { exact: true }).waitFor();
    expect(await b.getByRole("button", { name: "Advanced", exact: true }).getAttribute("aria-pressed")).toBe("true");

    // The editor stays on the variant it shows when a teammate renames it.
    await a.getByRole("button", { name: "triage/b.md · advanced", exact: true }).click();
    const { draft: advanced } = await call<AgentDraftView>("GetAgentDraft", { id, since: "1" });
    await call("ApplyAgentDraftOp", { id, op: { baseRevision: advanced.revision, opId: "test-rename", kind: "rename_variant", variant: "b", value: "beta" } });
    await a.getByRole("button", { name: "triage/beta.md · advanced", exact: true, pressed: true }).waitFor();

    // The Drafts menu reopens drafts and starts a new agent.
    await b.getByRole("button", { name: "Drafts", exact: true }).click();
    await b.getByRole("menuitem", { name: /^triage\.md/ }).waitFor();
    await b.getByRole("menuitem", { name: "Start a new agent", exact: true }).click();
    await b.waitForURL(`${bob}/agents/new`);
    await b.getByRole("radio", { name: "Advanced agent" }).click();

    // A starter agent brings its own settings and mode.
    await b.getByRole("radio", { name: "Template" }).click();
    await b.getByRole("button", { name: "Use template researcher", exact: true }).dblclick();
    await b.getByText("researcher.md · easy", { exact: true }).waitFor();

    // Editing an agent reopens its draft still in editing; copying one starts a new agent.
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

    // Each double click above started one draft.
    await Bun.sleep(500);
    const names = (await call<{ drafts: AgentDraft[] }>("ListAgentDrafts", {})).drafts.map((draft) => draft.name);
    for (const name of ["researcher", "support-bot", "support-bot-copy"]) expect([name, names.filter((started) => started === name).length]).toEqual([name, 1]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 120_000);

// On a phone the panes take turns filling the screen; switching keeps what was typed, and Fix this opens the assistant.
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

    // Settings first; the test chat and the assistant wait behind their tabs.
    await instructions.waitFor();
    expect(await chat.isVisible()).toBe(false);
    expect(await assistant.isVisible()).toBe(false);
    await instructions.click();
    await instructions.press("ControlOrMeta+End");
    await instructions.pressSequentially(" Be brief.", { delay: 20 });
    await page.getByRole("tab", { name: "File", exact: true }).click();
    await page.getByRole("tab", { name: "Form", exact: true }).click();

    // The test chat fills the screen and answers; going back to Settings keeps the edit.
    await panes.getByRole("tab", { name: "Test chat", exact: true }).click();
    expect(await instructions.isVisible()).toBe(false);
    await chat.getByRole("textbox", { name: "Test message" }).fill("hello");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    const answer = chat.getByRole("region", { name: "narrow answer", exact: true });
    await answer.getByText("gpt-a answer 1", { exact: false }).waitFor();
    await panes.getByRole("tab", { name: "Settings", exact: true }).click();
    expect(await instructions.inputValue()).toContain("Be brief.");

    // Fix this on an answer switches to the assistant with that answer.
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

    // An easy agent must name a space: the Form says so in plain words under the field, and the File tab is marked.
    await a.getByRole("group", { name: "Space", exact: true }).getByText("None: an easy agent must name a space.", { exact: true }).waitFor();
    await a.getByRole("tab", { name: "File, needs fixes" }).waitFor();

    // Both type in the instructions at the same spot at once: every view ends with both texts, in the same order.
    await Promise.all([instructions(a).click(), instructions(b).click()]);
    await Promise.all([instructions(a).pressSequentially("alice ", { delay: 40 }), instructions(b).pressSequentially("bob ", { delay: 40 })]);
    let both = "";
    await until("both texts in every view", async () => {
      both = await instructions(a).inputValue();
      return both.length === 10 && both === await instructions(b).inputValue() && both === await body();
    });
    expect(both).toMatch(/a.*l.*i.*c.*e/s);
    expect(both).toMatch(/b.*o.*b/s);

    // Bob reloads while alice types: he catches up with nothing lost or doubled.
    await instructions(a).press("ControlOrMeta+End");
    const typing = instructions(a).pressSequentially("more words here", { delay: 60 });
    await Bun.sleep(300);
    await b.reload();
    await typing;
    await until("bob caught up", async () => await instructions(b).inputValue() === `${both}more words here` && await body() === `${both}more words here`);

    // Half-typed YAML in the File tab is alice's alone until it parses.
    const text = `${both}more words here`;
    await a.getByRole("tab", { name: "File, needs fixes" }).click();
    await a.getByRole("textbox", { name: "Agent file" }).fill(`---\nmode: easy\nspace: [Ops\n---\n${text}`);
    await a.getByText("Not yet valid: line", { exact: false }).waitFor();
    await Bun.sleep(500);
    expect(await b.getByRole("textbox", { name: "Space", exact: true }).inputValue()).toBe("");
    await a.getByRole("textbox", { name: "Agent file" }).fill(`---\nmode: easy\nspace: Ops\n---\n${text}`);
    await until("bob sees the space", async () => await b.getByRole("textbox", { name: "Space", exact: true }).inputValue() === "Ops");
    expect(await body()).toBe(text);

    // History lists each change by person and field; restoring one brings the old value back for everyone.
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

    // Switching to advanced drops easy-only settings, says which, and Restore brings them back.
    const { draft } = await call<AgentDraftView>("GetAgentDraft", { id, since: "1" });
    await call("ApplyAgentDraftOp", { id, op: { baseRevision: draft.revision, opId: "test-helpers", kind: "set_field", variant: "a", key: "subagents", value: "auto" } });
    await a.getByRole("textbox", { name: "Helper models", exact: true }).waitFor();
    await b.getByRole("button", { name: "Advanced", exact: true }).click();
    await a.getByText("bob switched the whole draft to advanced. Dropped: Helper models.", { exact: false }).waitFor();
    await a.getByRole("status").getByRole("button", { name: "Restore", exact: true }).click();
    await until("the helpers are back", async () => await b.getByRole("textbox", { name: "Helper models", exact: true }).inputValue() === "auto");

    // Settings the editor has no help for are shown read-only and edited in the File tab.
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

    // A value changed back while its File submit is in flight goes too.
    const setting = async (key: string) => (await call<AgentDraftView>("GetAgentDraft", { id: tuner.id, since: "1" })).draft.variants[0].settings.find((stored) => stored.key === key)?.value;
    const retype = async (from: string | RegExp, to: string) => file.fill((await file.inputValue()).replace(from, to));
    await a.getByRole("tab", { name: "File", exact: true }).click();
    let release = await hold(a, /\/api\/ApplyAgentDraftOp$/, { respond: true });
    await retype("temperature: 0.5", "temperature: 0.7");
    await until("0.7 is stored", async () => await setting("temperature") === "0.7");
    await retype("temperature: 0.7", "temperature: 0.5");
    await Bun.sleep(500);
    await release();
    await until("0.5 is back", async () => await setting("temperature") === "0.5");

    // A File edit started while alice's Form change is unconfirmed does not send that change again over bob's later one.
    await a.getByRole("tab", { name: "Form", exact: true }).click();
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

// models.dev answers from this fixture; the RocketClaw server only names its providers, openai and work.
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

    // models.dev is fetched once the picker is first used, not with the page; only this installation's providers are
    // offered.
    await picker.getByText("None: every agent must name a model.", { exact: true }).waitFor();
    expect(fetched).toBe(0);
    await picker.getByRole("combobox", { name: "Model provider" }).click();
    await page.getByRole("option", { name: "openai", exact: true }).waitFor();
    expect(await page.getByRole("option").allTextContents()).toEqual(["work", "openai"]);
    await page.getByRole("option", { name: "openai", exact: true }).click();

    // openai matches models.dev's openai: each suggestion has its price, context and efforts, and links to slopalytics.
    await picker.getByRole("combobox", { name: "Model", exact: true }).click();
    const suggestions = picker.getByRole("list", { name: "Suggestions from models.dev for openai" });
    const gpt5 = suggestions.getByRole("listitem").filter({ hasText: "GPT-5gpt-5" });
    await gpt5.getByText("$1.25 in · $10 out per 1M tokens · 400K context · effort minimal, low, medium, high", { exact: true }).waitFor();
    await suggestions.getByRole("listitem").filter({ hasText: "GPT-6.1 Sol" }).getByText("$2 in · $10 out per 1M tokens · 1.1M context · effort low, medium, high, xhigh, max", { exact: true }).waitFor();
    expect(await suggestions.getByRole("link", { name: "Compare GPT-5 on slopalytics" }).getAttribute("href")).toBe("https://slopalytics.com/");
    expect(fetched).toBe(1);

    // A suggestion fills the model, and its efforts fill the effort choice.
    await gpt5.getByRole("button").click();
    await until("the model is gpt-5", async () => await model() === "gpt-5");
    await picker.getByRole("combobox", { name: "Model effort" }).click();
    await page.getByRole("option", { name: "Effort: low", exact: true }).waitFor();
    expect(await page.getByRole("option").allTextContents()).toEqual(["Effort: default", "Effort: minimal", "Effort: low", "Effort: medium", "Effort: high"]);
    await page.getByRole("option", { name: "Effort: low", exact: true }).click();
    await until("the effort is low", async () => await model() === "gpt-5#low");

    // models.dev has no provider called work: no suggestions, any name can be typed, and Test answers in plain words.
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

// The openai provider answers each turn "<model> answer <n>", n counting the user messages its conversation was sent.
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
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("request", (request: { url(): string }) => { const url = new URL(request.url()); if (url.pathname === "/stream") streams.push(url.searchParams.get("id")!); });
    const chat = page.getByRole("region", { name: "Test chat" });
    const row = (n: number) => chat.getByRole("region", { name: `Message ${n}`, exact: true });
    const cell = (n: number, column: string) => row(n).getByRole("region", { name: `${column} answer`, exact: true });
    const message = chat.getByRole("textbox", { name: "Test message" });
    const send = async (text: string, button = "Send") => {
      await message.fill(text);
      await chat.getByRole("button", { name: button, exact: true }).click();
    };

    // A new agent: the draft column answers, and each message runs once per column with real tools.
    const echo = await call<{ id: string }>("CreateAgentDraft", { name: "echo", mode: "advanced" });
    await apply(echo.id, { kind: "edit_file", variant: "a", value: "---\nmodel: gpt-a\n---\nHelp.\n" });
    const revA = Number((await read(echo.id)).revision);
    await page.goto(`${alice}/agents/drafts/${echo.id}`);
    await chat.getByText("Test messages use real tools.", { exact: false }).waitFor();
    await send("first");
    await cell(1, "echo").getByText("gpt-a answer 1", { exact: false }).waitFor();
    await cell(1, "echo").getByText(`r${revA}`, { exact: true }).waitFor();

    // Variant b, added after message 1, has no answer in its row; its answer to message 2, which it gets with the chat
    // so far, sits in message 2's row.
    await chat.getByRole("button", { name: "Add a variant", exact: true }).click();
    await chat.getByText("echo/b", { exact: true }).waitFor();
    const revB = Number(await apply(echo.id, { kind: "set_field", variant: "b", key: "model", value: "gpt-b" }));
    await cell(1, "echo/b").getByText("Not sent: this column did not exist yet.", { exact: true }).waitFor();
    await send("second");
    await cell(2, "echo/b").getByText("gpt-b answer 2", { exact: false }).waitFor();
    await cell(2, "echo").getByText("gpt-a answer 2", { exact: false }).waitFor();
    await cell(2, "echo/b").getByText(`r${revB}`, { exact: true }).waitFor();

    // Editing b fades only b's earlier answers; the draft column keeps its label.
    const revB2 = Number(await apply(echo.id, { kind: "set_field", variant: "b", key: "model", value: "gpt-b2" }));
    await cell(2, "echo/b").getByText(`r${revB} · changed since`, { exact: true }).waitFor();
    await cell(2, "echo").getByText(`r${revA}`, { exact: true }).waitFor();
    expect(await row(2).getByText("changed since", { exact: false }).count()).toBe(1);
    await chat.getByText(`next message uses r${revB2}`, { exact: true }).waitFor();

    // Changing the draft's model mid-chat: the chat continues, the new answer carries the new version, and both
    // restarts stay offered.
    const revA2 = Number(await apply(echo.id, { kind: "set_field", variant: "a", key: "model", value: "gpt-a2" }));
    await send("third");
    await cell(3, "echo").getByText("gpt-a2 answer 3", { exact: false }).waitFor();
    await cell(3, "echo").getByText(`r${revA2}`, { exact: true }).waitFor();
    await cell(3, "echo/b").getByText("gpt-b2 answer 3", { exact: false }).waitFor();
    await cell(1, "echo").getByText(`r${revA} · changed since`, { exact: true }).waitFor();
    for (const name of ["Restart", "Restart & replay"]) await chat.getByRole("button", { name, exact: true }).waitFor();

    // A variant whose file does not load is not run, with its reason, while the draft answers.
    await apply(echo.id, { kind: "set_field", variant: "b", key: "sticky", value: "maybe" });
    await send("fourth");
    await cell(4, "echo").getByText("gpt-a2 answer 4", { exact: false }).waitFor();
    await cell(4, "echo/b").getByText("Not run. Shared chat: off, user or global.", { exact: true }).waitFor();

    // A draft whose own settings do not load is refused, naming the setting, and nothing is sent.
    await apply(echo.id, { kind: "set_field", variant: "a", key: "sticky", value: "maybe" });
    await send("fifth");
    await chat.getByRole("alert").getByText("Fix the draft's settings before testing it.", { exact: false }).waitFor();
    await chat.getByRole("alert").getByText("Shared chat: off, user or global.", { exact: false }).waitFor();
    await Bun.sleep(500);
    expect(await row(5).count()).toBe(0);
    await apply(echo.id, { kind: "set_field", variant: "a", key: "sticky", value: "" });
    await apply(echo.id, { kind: "set_field", variant: "b", key: "sticky", value: "" });

    // Restart & replay sends the earlier messages again, in order, to a new chat; Restart empties it.
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

    // Send and Restart & replay wait for the instructions typed before them, so the message runs and is labelled with
    // them; text typed into the message box while a message goes stays.
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
      const { ops } = await call<AgentDraftView>("GetAgentDraft", { id: echo.id, since: "1" });
      await cell(1, "echo").getByText(`r${ops.filter((op) => op.kind === "edit_body").at(-1)!.revision}`, { exact: true }).waitFor();
    };
    let release = await typed(" Be brief.");
    await send("brief");
    await Bun.sleep(300);
    await release();
    await labelled(" Be brief.");
    release = await hold(page, /\/api\/DraftPrompt$/);
    await send("brief again");
    await message.fill("still typing");
    await release();
    await cell(2, "echo").getByText("gpt-a2 answer 2", { exact: false }).waitFor();
    expect(await message.inputValue()).toBe("still typing");
    release = await typed(" Really.");
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await Bun.sleep(300);
    await release();
    await labelled(" Really.");

    // So does a setting changed in the File tab just before Send, before the tab sends it on its own.
    const file = page.getByRole("textbox", { name: "Agent file" });
    await page.getByRole("tab", { name: /^File/ }).click();
    await message.fill("from the file");
    await file.fill((await file.inputValue()).replace("model: gpt-a2", "model: gpt-a3"));
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await cell(3, "echo").getByText("gpt-a3 answer 3", { exact: false }).waitFor();

    // Restart & replay sends every earlier message, one still waiting its turn too, in order. A turn on gpt-held
    // answers only after /test/release.
    const queue = await call<{ id: string }>("CreateAgentDraft", { name: "queue", mode: "advanced" });
    await apply(queue.id, { kind: "edit_file", variant: "a", value: "---\nmodel: gpt-held\n---\nHelp.\n" });
    await page.goto(`${alice}/agents/drafts/${queue.id}`);
    await send("one");
    await row(1).getByText("one", { exact: true }).first().waitFor();
    await send("two");
    await chat.getByText("Waiting its turn: two", { exact: true }).waitFor();
    const held = (await read(queue.id)).testChat;
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    await until("a new chat", async () => (await read(queue.id)).testChat !== held);
    expect((await fetch(`${alice}/test/release`, { method: "POST" })).ok).toBe(true);
    for (const [n, text] of ["one", "two"].entries()) {
      await row(n + 1).getByText(text, { exact: true }).first().waitFor();
      await cell(n + 1, "queue").getByText(`gpt-held answer ${n + 1}`, { exact: false }).waitFor();
    }

    // So does a message whose turn started after the page last read the queue, before the page's stream shows it.
    const replayed = (await read(queue.id)).testChat;
    release = await hold(page, /\/api\/History$/, { only: (body) => body.id === replayed && !!body.limit });
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

    // No second restart goes while one is under way.
    const restarting = (await read(queue.id)).testChat;
    release = await hold(page, /\/api\/RestartDraftChat$/);
    await chat.getByRole("button", { name: "Restart & replay", exact: true }).click();
    for (const name of ["Restart", "Restart & replay"]) await chat.getByRole("button", { name, exact: true, disabled: true }).waitFor();
    await release();
    await until("a new chat", async () => (await read(queue.id)).testChat !== restarting);
    await cell(3, "queue").getByText("gpt-held answer 3", { exact: false }).waitFor();
    await chat.getByRole("button", { name: "Restart & replay", exact: true, disabled: false }).waitFor();
    expect(await row(4).count()).toBe(0);

    // An existing agent's live version answers beside the draft, from the file as it was when each message was sent.
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

    // Renaming the draft makes it a new agent: the original stays, and the live column goes.
    await apply(support.id, { kind: "rename", value: "support-bot-2" });
    await chat.getByText("The original stays as it is", { exact: false }).waitFor();
    await send("renamed");
    await cell(3, "support-bot-2").getByText("answer 3", { exact: false }).waitFor();
    expect(await cell(3, "Live").count()).toBe(0);

    // External MCP only serves sticky: off agents, so a sticky: user draft cannot turn it on.
    const desk = await call<{ id: string }>("CreateAgentDraft", { name: "helpdesk", copy: true });
    await page.goto(`${alice}/agents/drafts/${desk.id}`);
    const toggle = chat.getByRole("switch", { name: "Test as External MCP" });
    await chat.getByText("External MCP only serves sticky: off agents", { exact: false }).waitFor();
    expect(await toggle.isDisabled()).toBe(true);

    // With sticky off, the metadata variable the instructions read suggests External MCP; a Web test says the value
    // comes only from External MCP, and External MCP mode requires it before Send.
    await apply(desk.id, { kind: "set_field", variant: "a", key: "sticky", value: "off" });
    await until("the switch is enabled", async () => !await toggle.isDisabled());
    await chat.getByText("$ROCKETCLAW_METADATA_CUSTOMER_ID comes only from External MCP", { exact: false }).waitFor();
    // The suggestion's change and a panel change made before it is stored land in the order made, the panel's sent
    // once the suggestion's is stored.
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

    // Two quick changes land in the order made, the second sent once the first is stored.
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

    // A key added after the first message goes with that message only; changing a locked key offers a restart.
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

    // Two keys of one variable, or a key posing as the conversation id, keep Send from going, saying why.
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

    // A fixed key renamed to one that becomes the same variable, with the same value, is still that fixed key: no
    // restart, and both the panel and the message show it fixed.
    await apply(desk.id, { kind: "set_external_mcp", externalMcp: { on: true, caller: "zendesk-bridge", metadata: { "customer-id": "acme-043", ticket: "77", channel: "email" } } });
    await value("customer-id").waitFor();
    expect(await chat.getByLabel("Fixed by the chat's first message", { exact: true }).count()).toBe(4);
    await send("renamed");
    await row(2).getByText("customer-id=acme-043", { exact: true }).waitFor();
    await cell(2, "helpdesk").getByText("gpt-help answer 2", { exact: false }).waitFor();

    // A refused change or restart says why.
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

// The assistant's model, gpt-editor, fills a described billing agent, answers a pointed-at answer with new
// instructions for its variant that quote it, and answers anything else with a shared chat.
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

    // Describing a new agent creates the draft and sends the description to the assistant, whose card gives a
    // reason for each change and marks the one that changes what the agent may do.
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
    await access.getByText("All six groups.", { exact: false }).waitFor();
    await access.getByText("[sessions]", { exact: false }).waitFor();
    expect(await described.getByRole("listitem").filter({ hasText: "Space" }).getByText("Changes what this agent may do", { exact: false }).count()).toBe(0);

    // Until someone accepts, the Form shows the change as Suggested and the File tab as a diff; the draft is unchanged.
    await a.getByRole("group", { name: "Space", exact: true }).getByText("Suggested: Billing", { exact: true }).waitFor();
    await a.getByRole("tab", { name: /^File/ }).click();
    const diff = a.getByRole("region", { name: "Suggested change to the file" });
    for (const line of ["+ space: Billing", "+ rocketclaw: [sessions]", "+ You answer billing questions."]) await diff.getByText(line, { exact: true }).waitFor();
    const before = (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft;
    expect(before.variants[0].settings.map(({ key }) => key)).not.toContain("space");
    expect(before.variants[0].body).toBe("");

    // Bob, on the same draft, sees the card and accepts it: both pages get the change, and History names the
    // assistant and bob.
    await b.goto(`${bob}/agents/drafts/${id}`);
    await card(b, "A billing agent for the Billing space.").getByRole("button", { name: "Accept", exact: true }).click();
    await a.getByRole("tab", { name: "Form", exact: true }).click();
    await a.waitForFunction(() => (document.querySelector("input[aria-label=Space]") as HTMLInputElement | null)?.value === "Billing");
    await described.getByText("Accepted", { exact: true }).waitFor();
    expect(await a.getByText("Suggested: Billing", { exact: true }).count()).toBe(0);
    await b.getByRole("tab", { name: "History", exact: true }).click();
    await b.getByText("Assistant (accepted by bob)", { exact: false }).first().waitFor();

    // A request alice makes is rejected by bob: the card goes for both, and the draft stays as it was.
    await panel(a).getByRole("textbox", { name: "Ask the assistant" }).fill("Let each person keep a chat");
    await panel(a).getByRole("button", { name: "Suggest", exact: true }).click();
    await card(b, "Each person gets a chat of their own.").getByText("Every person keeps their own chat.", { exact: true }).waitFor();
    const accepted = (await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft;
    await card(b, "Each person gets a chat of their own.").getByRole("button", { name: "Reject", exact: true }).click();
    for (const page of [a, b]) await card(page, "Each person gets a chat of their own.").waitFor({ state: "detached" });
    expect((await call<AgentDraftView>("GetAgentDraft", { id, since: "1" })).draft).toEqual(accepted);

    // Fix this on variant b's test answer sends that answer, and the card changes only b.
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

// The overlay github.com/acme/team and the workspace both hold router.md, the workspace's with a comment; gh opens pull
// request #7 on the overlay and reports it open. Checking a save of an agent named hold takes three seconds.
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

    // The Agents page opens the editor. Edit starts a draft of router, which has none yet.
    const a = await open(`${alice}/agents`);
    await a.getByRole("link", { name: "Create New Agent", exact: true }).click();
    await a.waitForURL(`${alice}/agents/new`);
    await a.goto(`${alice}/agents`);
    await a.getByRole("button", { name: "Edit router", exact: true }).dblclick();
    await a.getByRole("heading", { name: "Edit router", exact: true }).waitFor();
    const id = a.url().split("/").at(-1)!;
    await a.locator(`[data-tab="/agents/drafts/${id}"]`).getByText("Edit router", { exact: true }).waitFor();
    const b = await open(`${bob}/agents/drafts/${id}`);

    // The workspace copy provides router: saving there shows the comment the rewrite drops. Saving to the overlay
    // warns that the workspace copy hides it, and asks push or pull request, with no default.
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

    // Both people see the pull request, Stop waiting, and that the next edit stops waiting.
    for (const page of [a, b]) {
      await page.getByRole("link", { name: "Pull request #7", exact: true }).first().waitFor();
      await page.getByText("Your next edit stops waiting", { exact: false }).waitFor();
    }
    expect(await a.getByRole("link", { name: "Pull request #7", exact: true }).first().getAttribute("href")).toBe("https://github.com/acme/team/pull/7");

    // Stop waiting returns both to editing; the pull request stays open and linked.
    await b.getByRole("button", { name: "Stop waiting", exact: true }).click();
    for (const page of [a, b]) {
      await page.getByText("Draft · saved on the server", { exact: true }).waitFor();
      await page.getByText("is still open on GitHub.", { exact: false }).waitFor();
      await page.getByRole("link", { name: "Pull request #7", exact: true }).waitFor();
    }

    // A second pull request waits again, and bob's edit ends the wait.
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

    // A third pull request that gh then reports closed ends the wait when a page opens the draft, and both say so.
    await choose(a, "github.com/acme/team");
    await dialog(a).getByRole("radio", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Open pull request", exact: true }).click();
    await dialog(a).getByRole("button", { name: "Done", exact: true }).click();
    await b.getByText("Your next edit stops waiting", { exact: false }).waitFor();
    await Bun.write(process.env.ROCKETCLAW_EDITOR_PR_STATE!, "CLOSED\n");
    await a.reload();
    for (const page of [a, b]) await page.getByText("The pull request was closed on GitHub.", { exact: true }).waitFor();
    expect(await b.getByText("is still open on GitHub.", { exact: false }).count()).toBe(0);

    // The workspace file changes the model and gains an instruction while the draft changes the model too: the review
    // flags Model, and keeping mine while bringing in the new line saves both, which bob's draft gets too.
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

    // A setting that does not load writes nothing: the dialog closes and the sentence stands by its field.
    await apply(id, { kind: "set_field", variant: "a", key: "sticky", value: "maybe" });
    await choose(a, "Workspace");
    await dialog(a).getByRole("button", { name: "Save to workspace", exact: true }).click();
    await a.getByRole("group", { name: "Shared chat", exact: true }).getByText("Not saved. Allowed: off, user or global.", { exact: true }).waitFor();
    expect(await dialog(a).count()).toBe(0);
    await a.getByRole("alert").getByText("Not saved. Fix the draft's settings before saving it. Shared chat: off, user or global.", { exact: true }).waitFor();

    // While alice's save of hold runs, bob's Save is refused, naming her.
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
    await dialog(a).getByText("Saved and reloaded: hold is live.", { exact: true }).waitFor();

    // Saving main over the built-in warns that the built-in's updates stop reaching the workspace.
    const main = await call<{ id: string }>("CreateAgentDraft", { name: "main", copy: true });
    await a.goto(`${alice}/agents/drafts/${main.id}`);
    await choose(a, "Workspace");
    await dialog(a).getByText("Replaces the built-in main: its future updates will no longer reach this workspace.", { exact: true }).waitFor();

    // Edit reopens router's draft, back in editing since the refused save; the double click above started only it.
    await a.goto(`${alice}/agents`);
    await a.getByRole("button", { name: "Edit router", exact: true }).click();
    await a.waitForURL(`${alice}/agents/drafts/${id}`);
    expect((await call<{ drafts: AgentDraft[] }>("ListAgentDrafts", {})).drafts.filter((draft) => draft.name === "router")).toHaveLength(1);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 240_000);
