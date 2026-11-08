import { expect, test } from "bun:test";
import path from "node:path";

test("a person limited to Ops sees and drives only Ops, and everything once unlimited", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const url = process.env.ROCKETCLAW_PERMISSIONS_TEST_URL!, out = process.env.ROCKETCLAW_PERMISSIONS_DEMO_DIR!;
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors: string[] = [], prompts: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("request", (request: { url(): string; postData(): string }) => { if (request.url().endsWith("/api/Prompt")) prompts.push(JSON.parse(request.postData()).text); });
    let shots = 0;
    const shot = (name: string) => page.screenshot({ path: path.join(out, `${String(++shots).padStart(2, "0")}-${name}.png`) });
    const spaceMenu = async (current: string, name: string) => {
      await page.getByRole("button", { name: `Space: ${current}`, exact: true }).click();
      await page.getByRole("menuitemradio", { name: current, exact: true }).waitFor();
      const listed = [await page.getByRole("menuitemradio").allInnerTexts(), await page.getByRole("menuitem").allInnerTexts()];
      await shot(name);
      await page.keyboard.press("Escape");
      return listed;
    };
    const sessions = async (shown: string[], name: string) => {
      await page.keyboard.press("Control+p");
      const palette = page.getByRole("dialog", { name: "Go to session or command", exact: true });
      for (const title of shown) await palette.getByText(title, { exact: true }).waitFor();
      const titles = await palette.locator('[data-slot="session-title"]').allInnerTexts();
      await shot(name);
      await page.keyboard.press("Escape");
      return titles.toSorted();
    };
    const agentMenu = async (name: string) => {
      await page.getByRole("button", { name: "Choose agent", exact: true }).click();
      await page.getByRole("menuitemradio").first().waitFor();
      const listed = await page.getByRole("menuitemradio").allInnerTexts();
      await shot(name);
      await page.keyboard.press("Escape");
      return listed.map((text: string) => text.trim().split("\n")[0]);
    };
    const agentsPage = async (shown: string, name: string) => {
      await page.goto(`${url}/agents`);
      await page.locator("section h2").getByText(shown, { exact: true }).waitFor();
      const listed = await page.locator("section h2").allInnerTexts();
      await shot(name);
      return listed;
    };
    const transcript = page.locator("#transcript-scroll");

    await page.goto(url);
    await page.getByRole("button", { name: "Space: Ops", exact: true }).waitFor();
    await shot("limited-new-session");
    expect(await spaceMenu("Ops", "limited-space-menu-only-ops")).toEqual([["Ops"], []]);
    const ops = ["Ops: deploy checklist", "Ops: on-call handoff"];
    expect(await sessions(ops, "limited-sessions-only-ops")).toEqual(ops);
    expect(await agentMenu("limited-agent-picker-only-ops-agent")).toEqual(["ops-agent"]);

    await page.keyboard.press("Control+p");
    await page.getByRole("dialog", { name: "Go to session or command", exact: true }).getByText("Ops: deploy checklist", { exact: true }).click();
    await transcript.getByText("main-agent added the budget sign-off step.", { exact: true }).waitFor();
    await shot("limited-chat-handed-to-main-agent");
    expect(await agentMenu("limited-chat-agent-picker-only-ops-agent")).toEqual(["ops-agent"]);
    await page.locator("textarea").fill("Is the deploy checklist ready for Thursday?");
    await shot("limited-prompt-typed");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await transcript.getByText("ops-agent here: Thursday's deploy checklist is ready.", { exact: true }).waitFor();
    await shot("limited-reply-from-ops-agent");
    expect(prompts).toEqual(["$agent ops-agent", "Is the deploy checklist ready for Thursday?"]);
    expect(await agentsPage("ops-agent", "limited-agents-page-only-ops-agent")).toEqual(["ops-agent"]);

    expect(await page.evaluate(async () => (await fetch("/test/unlimit", { method: "POST" })).ok)).toBe(true);
    await page.goto(url);
    await page.getByRole("button", { name: "Space: Ops", exact: true }).waitFor();
    await shot("unlimited-new-session");
    expect(await spaceMenu("Ops", "unlimited-space-menu-all-spaces")).toEqual([["Main", "Ops"], ["New space", "Rename space", "Delete space"]]);
    const every = ["Main: quarterly budget", "No space: scratch notes", ...ops];
    expect(await sessions(every, "unlimited-sessions-all")).toEqual(every);
    expect(await agentMenu("unlimited-agent-picker-all-agents")).toEqual(["loose", "main", "main-agent", "ops-agent", "voice"]);
    await page.keyboard.press("Control+p");
    await page.getByRole("dialog", { name: "Go to session or command", exact: true }).getByText("Main: quarterly budget", { exact: true }).click();
    await transcript.getByText("Q4 spend is 8% under plan.", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Space: Main", exact: true }).waitFor();
    await shot("unlimited-main-chat");
    expect(await agentsPage("main-agent", "unlimited-agents-page-all-agents")).toEqual(["loose", "main", "main-agent", "ops-agent", "voice"]);
    expect(errors).toEqual([]);
  } finally { await browser.close(); }
}, 120_000);
