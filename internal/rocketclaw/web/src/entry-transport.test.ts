import { expect, test } from "bun:test";
import path from "node:path";
import { listSessions } from "./api";
import type { Session, TranscriptEvent } from "./types";

const screenshots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");

// Invoked by Go's TestSessionEntries with real isolated PostgreSQL storage.
test("transcript and entry HTTP proxy reach Go", async () => {
  const url = process.env.ROCKETCLAW_ENTRY_TEST_URL!;
  const id = process.env.ROCKETCLAW_ENTRY_TEST_ID!;
  const call = async (method: string, input: object = {}) => {
    const response = await fetch(new URL(`/api/${method}`, url), { method: "POST", body: JSON.stringify(input), headers: { "content-type": "application/json", "rocketclaw-principal": "192.0.2.1", "x-forwarded-for": "192.0.2.1", "x-real-ip": "192.0.2.1" } });
    expect(response.status).toBe(200);
    return response.json();
  };
  const hash = new Bun.CryptoHasher("sha256").update(await Bun.file(new URL("../proto/web.proto", import.meta.url)).arrayBuffer()).digest("hex");
  expect(await call("Protocol")).toEqual({ protoSha256: hash });
  expect(await call("Identity")).toEqual({ username: "alice", principal: "alice" });
  const batches = await Array.fromAsync(listSessions(undefined, `${url}/api/ListSessions`));
  expect(batches.flatMap((batch) => batch.sessions.map((session) => session.id))).toEqual(["empty-web", id]);
  expect(batches.at(-1)).toEqual({ sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true, opencodeServers: {}, agentTags: {} });
  const original = batches.flatMap((batch) => batch.sessions).find((session) => session.id === id)!;
  for (const details of [{ id, pinned: true }, { id, name: " Launch " }, { id, pinned: false }, { id, name: "" }]) {
    await call("UpdateSession", details);
    const updated: Session[] = (await Array.fromAsync(listSessions(undefined, `${url}/api/ListSessions`))).flatMap((batch) => batch.sessions);
    const session = updated.find((session) => session.id === id)!;
    expect(session.preview).toBe(original.preview);
    expect(session.updatedAt).toBe(original.updatedAt);
    if (details.pinned !== undefined) expect(session.pinned).toBe(details.pinned);
    if (details.name !== undefined) expect(session.name).toBe(details.name.trim());
  }
  const history: { messages: TranscriptEvent[] } = await call("History", { id: process.env.ROCKETCLAW_HISTORY_TEST_ID! });
  expect(history.messages[1]).toMatchObject({ model: "legacy-model", agent: "", origin: "canonical" });
  expect(history.messages[1].reasoningEffort).toBeUndefined();
  expect(history.messages[1].principal).toBe("");
  expect(history.messages[6].principal).toBe("alice");
  expect(history.messages.at(-1)).toMatchObject({ agent: "planner", model: "work/model-a", reasoningEffort: "", origin: "canonical" });
  const copied = await call("History", { id });
  expect(copied.messages.at(-1)).toMatchObject({ agent: "producer", reasoningEffort: "high", origin: "sandboxed" });
  expect(JSON.parse(copied.origin).pairs).toEqual([{ key: "Original-Key", value: "Value <&> Unicode Ω" }]);
  expect(await call("SearchSessions", { query: "unicode Ω" })).toMatchObject({ needle: "unicode ω", matches: [{ conversationId: id, field: "Origin", text: "external mcp external conversation: external agent: producer original-key=value <&> unicode ω" }] });
  expect(history.messages.map(({ role, text }) => ({ role, text }))).toEqual([
    { role: "developer", text: "private instructions" }, { role: "user", text: "human one" },
    { role: "thinking", text: "**Planning the answer**" }, { role: "tool", text: "execute\n{\"code\":\"true\"}" },
    { role: "tool", text: "ok" }, { role: "assistant", text: "answer one" },
    { role: "user", text: "human two" }, { role: "assistant", text: "answer two" },
    { role: "tool", text: "execute\n{\"code\":\"tag\"}" },
    { role: "tool", text: "tagged" },
  ]);
  const config = await call("ListConfig");
  expect(config.config).toEqual({ workspace: process.env.ROCKETCLAW_VIEW_TEST_WORKSPACE, overlays: ["local-overlay"], models: [{ name: "alpha", model: "gpt-5.4" }, { name: "zeta", model: "gpt-6.1-sol" }], slackChannels: [{ channel: "#ops", agents: ["main"] }], mcpServers: ["alpha", "zeta"], loggingLevel: "info", autoApproverModel: "gpt-6.1-sol", instrumentationEnabled: true, mcpExternal: true, tailscaleUser: "" });
  expect(JSON.stringify(config)).not.toContain("secret-");
  expect(JSON.stringify(config)).not.toContain("postgres://");
  expect((await call("ListSkills")).skills).toEqual(["alpha", "zeta"].map((name) => ({ name, description: "Read-only skill", license: "MIT", compatibility: "Unix", content: "# Instructions\nKeep [literal] text.\n", origin: `${name}/SKILL.md` })));
  const listed = await call("ListSessionEntries", { id });
  expect(listed.entries).toHaveLength(1);
  expect(listed.entries[0].type).toBe("turn");
  const loaded = await call("LoadSessionEntries", { id });
  expect(loaded.entries[0].id).toBe(listed.entries[0].id);
  expect(JSON.parse(loaded.entries[0].json).type).toBe("turn");

  // Serve the compiled App while the actual Go handler supplies its data.
  const playwrightModule = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!;
  const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
  const web = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const target = new URL(request.url);
    if (target.pathname.startsWith("/api/") || target.pathname === "/stream") return fetch(new Request(new URL(target.pathname + target.search, url), request), { decompress: false });
    const file = Bun.file(path.join(dist, target.pathname));
    return new Response(await file.exists() && target.pathname !== "/" ? file : Bun.file(path.join(dist, "index.html")));
  } });
  const { chromium } = await import(playwrightModule);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM });
  try {
    for (const [width, filename] of [[1280, "r22-web-desktop.png"], [390, "r22-web-mobile.png"]] as const) {
      const page = await browser.newPage({ hasTouch: width === 390, viewport: { width, height: 844 }, permissions: ["clipboard-read", "clipboard-write"] });
      await page.goto(`http://127.0.0.1:${web.port}/s/${Buffer.from(process.env.ROCKETCLAW_HISTORY_TEST_ID!).toString("base64url")}`);
      const report = page.locator("#transcript-scroll").getByText("answer two", { exact: true });
      await report.waitFor();
      const footer = page.locator('[data-slot="message-footer"]').filter({ hasText: "planner (work/model-a)" }).last();
      await footer.waitFor({ state: "attached" });
      expect(await footer.isVisible()).toBe(false);
      const reply = footer.locator('xpath=ancestor::*[@data-slot="message"]');
      if (width === 1280) {
        await page.keyboard.press("Tab");
        await reply.focus();
        expect(await footer.isVisible()).toBe(true);
        await reply.evaluate((node: HTMLElement) => node.blur());
      }
      if (width === 390) await reply.locator('[data-slot="bubble-content"]').tap();
      else await reply.hover();
      expect(await footer.isVisible()).toBe(true);
      expect(await footer.innerText()).toBe("planner (work/model-a)");
      const replyCopy = reply.getByRole("button", { name: "Copy message" });
      await replyCopy.waitFor({ state: "visible" });
      await replyCopy.click();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("answer two");
      expect(await page.locator('[data-slot="message-footer"]').filter({ hasText: "legacy-model" }).count()).toBe(1);
      expect(await page.locator('[data-slot="message"][data-align="end"] [data-slot="message-footer"]').count()).toBe(0);
      const user = page.locator('[data-slot="message"][data-align="end"]').filter({ hasText: "human two" });
      expect(await user.locator('[data-slot="bubble-content"] [data-slot="message-author"]').textContent()).toBe("alice");
      expect(await user.locator('[data-slot="message-author"]').isVisible()).toBe(true);
      const userCopy = user.getByRole("button", { name: "Copy message" });
      expect(await userCopy.isVisible()).toBe(false);
      if (width === 390) await user.locator('[data-slot="bubble-content"]').tap();
      else await user.hover();
      await userCopy.waitFor({ state: "visible" });
      expect(await user.locator('[data-slot="message-actions"] button').evaluateAll((nodes: Element[]) => nodes.map((node) => node.getAttribute("aria-label")))).toEqual(["Revert message", "Copy message"]);
      await replyCopy.waitFor({ state: "hidden" });
      await userCopy.click();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("human two");
      expect(await page.locator('[data-slot="message-footer"]').filter({ hasText: " - canonical" }).count()).toBe(0);
      expect(await page.locator('[data-slot="message-footer"] details').count()).toBe(0);
      expect(await footer.innerText()).not.toContain("Source:");
      expect(await footer.innerText()).not.toContain("Destination:");
      expect(await page.getByRole("group", { name: "Show messages from" }).count()).toBe(0);
      const trace = page.getByRole("region", { name: /^Turn \d+$/ }).filter({ hasText: "tagged" }).locator(':scope > [data-slot="trace"] > [data-slot="trace-collapsible"]');
      const traceTrigger = trace.locator(':scope > [data-slot="trace-collapsible-trigger"]');
      // The tag call ran inside the execute script; only the script's own row is shown.
      const tool = trace.locator('[data-slot="trace-collapsible"]').filter({ hasText: "tagged" });
      const toolBody = tool.locator("pre").first();
      // The default Compact level folds each turn's activity into one closed Chain of Thought row.
      expect(await page.locator('[aria-label^="Turn "] > [data-slot="trace"] > [data-slot="trace-collapsible"] > [aria-expanded="true"]').count()).toBe(0);
      expect(await traceTrigger.innerText()).toContain("Used 1 tool");
      expect(await toolBody.isVisible()).toBe(false);
      expect(await report.isVisible()).toBe(true);
      expect(await report.count()).toBe(1);
      await traceTrigger.press("Enter");
      expect(await traceTrigger.getAttribute("aria-expanded")).toBe("true");
      expect(await trace.innerText()).not.toContain("rocketclaw_set_tag");
      await tool.locator('[data-slot="trace-collapsible-trigger"]').first().press("Enter");
      await toolBody.waitFor();
      // The transcript is still scrolling to follow the opening row, so a mouse click can release on another element.
      await tool.getByRole("button", { name: "Collapse tool" }).press("Enter");
      await toolBody.waitFor({ state: "hidden" });
      await page.screenshot({ path: path.join(screenshots, filename) });
      await page.goto(`http://127.0.0.1:${web.port}/s/${Buffer.from(id).toString("base64url")}`);
      const sandboxed = page.locator('[data-slot="message-footer"]').filter({ hasText: "producer (work/a-very-long-provider-qualified-model-name-for-narrow-layout#high) - sandboxed" }).last();
      await sandboxed.waitFor({ state: "attached" });
      const sandboxedMessage = sandboxed.locator('xpath=ancestor::*[@data-slot="message"]');
      if (width === 390) await sandboxedMessage.locator('[data-slot="bubble-content"]').tap();
      else await sandboxedMessage.hover();
      expect(await sandboxed.isVisible()).toBe(true);
      const sandboxedButton = page.getByRole("button", { name: "sandboxed", exact: true });
      const canonicalButton = page.getByRole("button", { name: "canonical", exact: true });
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("true");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("true");
      await sandboxedButton.click();
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("false");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("true");
      expect(await sandboxed.isVisible()).toBe(false);
      await canonicalButton.click();
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("false");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("false");
      expect(await sandboxed.isVisible()).toBe(false);
      expect(await page.getByText("No messages for selected origins.").isVisible()).toBe(true);
      await sandboxedButton.click();
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("true");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("false");
      if (width === 390) await sandboxedMessage.locator('[data-slot="bubble-content"]').tap();
      else await sandboxedMessage.hover();
      expect(await sandboxed.isVisible()).toBe(true);
      await sandboxedButton.click();
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("false");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("false");
      await canonicalButton.press("Space");
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("false");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("true");
      await sandboxedButton.press("Space");
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("true");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("true");
      const bounds = await sandboxedButton.boundingBox();
      expect(bounds!.height).toBeLessThan(28);
      if (width === 390) await page.touchscreen.tap(bounds!.x + bounds!.width / 2, bounds!.y - 8);
      else await sandboxedButton.click();
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("false");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("true");
      await sandboxedButton.click();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.reload();
      await sandboxed.waitFor({ state: "attached" });
      expect(await sandboxedButton.getAttribute("aria-pressed")).toBe("true");
      expect(await canonicalButton.getAttribute("aria-pressed")).toBe("true");
      if (width === 390) await sandboxedMessage.locator('[data-slot="bubble-content"]').tap();
      else await sandboxedMessage.hover();
      await page.screenshot({ path: path.join(screenshots, `sandboxed-${filename}`) });
      await page.close();
    }
  } finally { await browser.close(); web.stop(true); }

  expect(await call("DeleteSessionEntries", { id })).toEqual({ deleted: "1" });
  expect(await call("ListSessionEntries", { id })).toEqual({ entries: [] });
  expect(await call("LoadSessionEntries", { id })).toEqual({ entries: [] });
}, 60000);
