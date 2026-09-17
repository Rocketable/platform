import { expect, test } from "bun:test";
import path from "node:path";
import { listSessions } from "./api";
import type { Session, TranscriptEvent } from "./types";

// Invoked by Go's TestSessionEntries with real isolated PostgreSQL storage.
test.skipIf(!process.env.ROCKETCLAW_TEST_HTTP_URL)("transcript and entry HTTP proxy reach Go and reject an unmapped connection", async () => {
  const url = process.env.ROCKETCLAW_TEST_HTTP_URL!;
  const id = process.env.ROCKETCLAW_ENTRY_TEST_ID!;
  const call = async (method: string, input: object = {}, denied = false) => {
    const target = new URL(`/api/${method}`, url);
    if (denied) target.hostname = "[::1]";
    const response = await fetch(target, { method: "POST", body: JSON.stringify(input), headers: { "content-type": "application/json", "rocketclaw-principal": "192.0.2.1", "x-forwarded-for": "192.0.2.1", "x-real-ip": "192.0.2.1" } });
    expect(response.status).toBe(denied && method !== "Protocol" ? 401 : 200);
    return response.json();
  };
  const hash = new Bun.CryptoHasher("sha256").update(await Bun.file(new URL("../proto/web.proto", import.meta.url)).arrayBuffer()).digest("hex");
  expect(await call("Protocol")).toEqual({ protoSha256: hash });
  expect(await call("Protocol", {}, true)).toEqual({ protoSha256: hash });
  expect(await call("Identity")).toEqual({ username: "alice" });
  const batches = await Array.fromAsync(listSessions(undefined, `${url}/api/ListSessions`));
  expect(batches.flatMap((batch) => batch.sessions.map((session) => session.id))).toEqual(["empty-web", id]);
  expect(batches.at(-1)).toEqual({ sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true });
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
  expect(history.messages.map(({ role, text }) => ({ role, text }))).toEqual([
    { role: "developer", text: "private instructions" }, { role: "user", text: "human one" },
    { role: "thinking", text: "**Planning the answer**" }, { role: "tool", text: "execute\n{\"code\":\"true\"}" },
    { role: "tool", text: "ok" }, { role: "assistant", text: "answer one" },
    { role: "user", text: "human two" }, { role: "assistant", text: "answer two" },
    { role: "tool", text: "rocketclaw_i_want_human_partner_to_see_this\n{\"payload\":\"Exact report\\nwith details\"}" },
    { role: "tool", text: "queued for verbatim delivery" },
    { role: "tool", text: "rocketclaw_i_want_human_partner_to_see_this\ninvalid" },
    { role: "tool", text: "invalid arguments" }, { role: "assistant", text: "Exact report\nwith details" },
  ]);
  const config = await call("ListConfig");
  expect(config.config).toEqual({ workspace: process.env.ROCKETCLAW_VIEW_TEST_WORKSPACE, overlays: ["local-overlay"], models: [{ name: "alpha", model: "gpt-5.4" }, { name: "zeta", model: "gpt-5.5" }], slackChannels: [{ channel: "#ops", agents: ["main"] }], mcpServers: ["alpha", "zeta"], loggingLevel: "info", autoApproverModel: "gpt-5.5", instrumentationEnabled: true, mcpExternal: true, webAutoSettleAfter: "1h30m0s", tailscaleUser: "" });
  expect(JSON.stringify(config)).not.toContain("secret-");
  expect(JSON.stringify(config)).not.toContain("postgres://");
  expect((await call("ListSkills")).skills).toEqual(["alpha", "zeta"].map((name) => ({ name, description: "Read-only skill", license: "MIT", compatibility: "Unix", content: "# Instructions\nKeep [literal] text.\n", origin: `${name}/SKILL.md` })));
  const listed = await call("ListSessionEntries", { id });
  expect(listed.entries).toHaveLength(1);
  expect(listed.entries[0].type).toBe("turn");
  const loaded = await call("LoadSessionEntries", { id });
  expect(loaded.entries[0].id).toBe(listed.entries[0].id);
  expect(JSON.parse(loaded.entries[0].json).type).toBe("turn");
  for (const method of ["ListSessionEntries", "LoadSessionEntries", "DeleteSessionEntries", "UpdateSession", "ListConfig", "ListSkills"]) expect(await call(method, method.startsWith("ListC") || method === "ListSkills" ? {} : { id }, true)).toMatchObject({ code: 16 });

  // Serve the compiled App while the actual Go handler supplies its data.
  const playwrightModule = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
  if (playwrightModule) {
    const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
    const web = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
      const target = new URL(request.url);
      if (target.pathname.startsWith("/api/") || target.pathname === "/stream") return fetch(new Request(new URL(target.pathname + target.search, url), request));
      const file = Bun.file(path.join(dist, target.pathname));
      return new Response(await file.exists() && target.pathname !== "/" ? file : Bun.file(path.join(dist, "index.html")));
    } });
    const { chromium } = await import(playwrightModule);
    const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM });
    try {
      const page = await browser.newPage();
      for (const [width, filename] of [[1280, "r22-web-desktop.png"], [390, "r22-web-mobile.png"]] as const) {
        await page.setViewportSize({ width, height: 844 });
        await page.goto(`http://127.0.0.1:${web.port}/s/${Buffer.from(process.env.ROCKETCLAW_HISTORY_TEST_ID!).toString("base64url")}`);
        const report = page.getByText("Exact report\nwith details", { exact: true });
        await report.waitFor();
        const trace = page.locator("section > details").filter({ hasText: "queued for verbatim delivery" });
        const tool = trace.locator("details").filter({ hasText: "Exact report" });
        const toolBody = tool.locator("pre").first();
        expect(await toolBody.isVisible()).toBe(true);
        await tool.locator("summary").click();
        expect(await toolBody.isVisible()).toBe(false);
        expect(await tool.locator("summary").innerText()).toContain("Send report");
        expect(await report.isVisible()).toBe(true);
        expect(await trace.getAttribute("open")).not.toBeNull();
        await trace.locator(":scope > summary").click();
        expect(await trace.getAttribute("open")).toBeNull();
        expect(await trace.locator("pre").filter({ hasText: "queued for verbatim delivery" }).isVisible()).toBe(false);
        expect(await report.isVisible()).toBe(true);
        expect(await report.count()).toBe(1);
        expect(await page.getByText("answer two", { exact: true }).isVisible()).toBe(true);
        expect(await page.locator("section > details[open]").count()).toBe(2);
        await trace.locator(":scope > summary").press("Enter");
        expect(await trace.getAttribute("open")).not.toBeNull();
        expect(await trace.locator("pre").filter({ hasText: "queued for verbatim delivery" }).isVisible()).toBe(false);
        expect(await toolBody.isVisible()).toBe(false);
        await tool.locator("summary").press("Enter");
        expect(await toolBody.isVisible()).toBe(true);
        expect(await tool.locator("pre").filter({ hasText: "queued for verbatim delivery" }).isVisible()).toBe(true);
        expect(await tool.locator("pre").count()).toBe(2);
        await tool.getByRole("button", { name: "Collapse tool" }).click();
        expect(await toolBody.isVisible()).toBe(false);
        await page.screenshot({ path: path.join(process.env.TMPDIR!, filename) });
      }
    } finally { await browser.close(); web.stop(true); }
  }

  expect(await call("DeleteSessionEntries", { id })).toEqual({ deleted: "1" });
  expect(await call("ListSessionEntries", { id })).toEqual({ entries: [] });
  expect(await call("LoadSessionEntries", { id })).toEqual({ entries: [] });
}, 60000);
