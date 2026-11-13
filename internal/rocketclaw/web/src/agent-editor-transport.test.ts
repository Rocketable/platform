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
