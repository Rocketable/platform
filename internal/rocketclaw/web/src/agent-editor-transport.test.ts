import { expect, test } from "bun:test";
import type { AgentDraftView } from "./types";

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

    // Once unlimited, bob opens the shared link: the same draft, and both people are shown in both browsers.
    expect((await fetch(`${alice}/test/unlimit`, { method: "POST" })).ok).toBe(true);
    await b.goto(`${bob}/agents/drafts/${id}`);
    await b.getByText("triage/c.md · easy", { exact: true }).waitFor();
    for (const page of [a, b]) for (const who of ["alice", "bob"]) await page.getByRole("img", { name: `${who} is here`, exact: true }).waitFor();

    // Alice switches the whole draft to advanced, and every variant follows in bob's browser.
    await a.getByRole("button", { name: "Advanced", exact: true }).click();
    for (const file of ["triage.md", "triage/b.md", "triage/c.md"]) await b.getByText(`${file} · advanced`, { exact: true }).waitFor();
    expect(await b.getByRole("button", { name: "Advanced", exact: true }).getAttribute("aria-pressed")).toBe("true");

    // The Drafts menu reopens drafts and starts a new agent.
    await b.getByRole("button", { name: "Drafts", exact: true }).click();
    await b.getByRole("menuitem", { name: /^triage\.md/ }).waitFor();
    await b.getByRole("menuitem", { name: "Start a new agent", exact: true }).click();
    await b.waitForURL(`${bob}/agents/new`);
    await b.getByRole("radio", { name: "Advanced agent" }).click();

    // A starter agent brings its own settings and mode.
    await b.getByRole("radio", { name: "Template" }).click();
    await b.getByRole("button", { name: "Use template researcher", exact: true }).click();
    await b.getByText("researcher.md · easy", { exact: true }).waitFor();

    // Editing an agent reopens its draft still in editing; copying one starts a new agent.
    const edit = async () => {
      await b.goto(`${bob}/agents/new`);
      await b.getByRole("radio", { name: "Advanced agent" }).click();
      await b.getByRole("radio", { name: "Copy an agent" }).click();
      await b.getByRole("button", { name: "Edit support-bot", exact: true }).click();
      await b.getByRole("heading", { name: "Edit support-bot", exact: true }).waitFor();
      return b.url();
    };
    const editing = await edit();
    expect(editing).toMatch(/\/agents\/drafts\/[^/]+$/);
    expect(await edit()).toBe(editing);
    await b.goto(`${bob}/agents/new`);
    await b.getByRole("radio", { name: "Advanced agent" }).click();
    await b.getByRole("radio", { name: "Copy an agent" }).click();
    await b.getByRole("button", { name: "Copy support-bot", exact: true }).click();
    await b.getByText("support-bot-copy.md · advanced", { exact: true }).waitFor();
    await b.getByRole("heading", { name: "Create New Agent", exact: true }).waitFor();
    expect(b.url()).not.toBe(editing);
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
    expect(new RegExp("a.*l.*i.*c.*e", "s").test(both) && new RegExp("b.*o.*b", "s").test(both)).toBe(true);

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
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 180_000);
