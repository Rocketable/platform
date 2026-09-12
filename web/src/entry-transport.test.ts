import { expect, test } from "bun:test";
import { Layer } from "effect";
import http from "node:http";
import path from "node:path";
import { createTRPCClient, httpBatchStreamLink } from "@trpc/client";
import type { AppRouter } from "./router";
import { RocketclawLive, protoSHA256 } from "./grpc";
import { createRPCHandler } from "./transport";
import { WhoisLive } from "./whois";

// Invoked by Go's TestSessionEntries with real isolated PostgreSQL storage.
test.skipIf(!process.env.ROCKETCLAW_ENTRY_TEST_ID)("transcript and entry HTTP proxy reach Go and reject an unmapped connection", async () => {
  const id = process.env.ROCKETCLAW_ENTRY_TEST_ID!;
  const server = http.createServer(createRPCHandler(Layer.merge(RocketclawLive, WhoisLive)));
  await new Promise<void>((resolve) => server.listen(0, "::", resolve));
  const address = server.address() as import("node:net").AddressInfo;
  const call = (method: string, mutation = false, localAddress = "127.0.0.1", conversationId = id) => new Promise<{ status: number; body: any }>((resolve, reject) => {
    const input = JSON.stringify({ id: conversationId });
    const req = http.request({
      host: localAddress, port: address.port, localAddress,
      path: `/trpc/${method}${mutation ? "" : `?input=${encodeURIComponent(input)}`}`,
      method: mutation ? "POST" : "GET",
      // Neither a forged principal nor forwarding metadata overrides the socket.
      headers: { "content-type": "application/json", "rocketclaw-principal": "192.0.2.1", "x-forwarded-for": "192.0.2.1", "x-real-ip": "192.0.2.1" },
    }, (res) => {
      let body = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => { body += chunk; });
      res.on("end", () => resolve({ status: res.statusCode!, body: JSON.parse(body) }));
    });
    req.on("error", reject);
    req.end(mutation ? input : undefined);
  });
  try {
    const protocol = await call("protocol");
    expect(protocol.body.result.data).toBe(protoSHA256());
    const client = createTRPCClient<AppRouter>({ links: [httpBatchStreamLink({ url: `http://127.0.0.1:${address.port}/trpc` })] });
    const sessions = [];
    for await (const batch of await client.sessions.query()) sessions.push(batch);
    expect(sessions.flatMap((batch) => batch.sessions.map((session) => session.id))).toEqual(["empty-web", id]);
    expect(sessions.at(-1)).toEqual({ sessions: [], owner: "alice", upstreamSuccess: true, summariesComplete: true });
    expect((await call("identity")).body.result.data).toBe("alice");
    expect((await call("protocol", false, "::1")).body.result.data).toBe(protoSHA256());
    const history = await call("history", false, "127.0.0.1", process.env.ROCKETCLAW_HISTORY_TEST_ID!);
    expect(history.status).toBe(200);
    expect(history.body.result.data.map(({ role, text }: { role: string; text: string }) => ({ role, text }))).toEqual([
      { role: "developer", text: "private instructions" },
      { role: "user", text: "human one" },
      { role: "thinking", text: "**Planning the answer**" },
      { role: "tool", text: "execute\n{\"code\":\"true\"}" },
      { role: "tool", text: "ok" },
      { role: "assistant", text: "answer one" },
      { role: "user", text: "human two" },
      { role: "assistant", text: "answer two" },
      { role: "tool", text: "rocketclaw_i_want_human_partner_to_see_this\n{\"payload\":\"Exact report\\nwith details\"}" },
      { role: "tool", text: "queued for verbatim delivery" },
      { role: "tool", text: "rocketclaw_i_want_human_partner_to_see_this\ninvalid" },
      { role: "tool", text: "invalid arguments" },
      { role: "assistant", text: "Exact report\nwith details" },
    ]);
    const config = await call("config");
    expect(config.status).toBe(200);
    expect(config.body.result.data).toEqual({
      workspace: process.env.ROCKETCLAW_VIEW_TEST_WORKSPACE, overlays: ["local-overlay"],
      models: [{ name: "alpha", model: "gpt-5.4" }, { name: "zeta", model: "gpt-5.5" }],
      slackChannels: [{ channel: "#ops", agents: ["main"] }], mcpServers: ["alpha", "zeta"],
      loggingLevel: "info", autoApproverModel: "gpt-5.5", instrumentationEnabled: true, mcpExternal: true,
    });
    expect(JSON.stringify(config.body)).not.toContain("secret-");
    expect(JSON.stringify(config.body)).not.toContain("postgres://");
    const skills = await call("skills");
    expect(skills.status).toBe(200);
    expect(skills.body.result.data).toEqual(["alpha", "zeta"].map((name) => ({
      name, description: "Read-only skill", license: "MIT", compatibility: "Unix",
      content: "# Instructions\nKeep [literal] text.\n", origin: `${name}/SKILL.md`,
    })));
    const listed = await call("listSessionEntries");
    expect(listed.status).toBe(200);
    expect(listed.body.result.data).toHaveLength(1);
    expect(listed.body.result.data[0].type).toBe("turn");
    const loaded = await call("loadSessionEntries");
    expect(loaded.status).toBe(200);
    expect(loaded.body.result.data[0].id).toBe(listed.body.result.data[0].id);
    expect(JSON.parse(loaded.body.result.data[0].json).type).toBe("turn");
    for (const method of ["listSessionEntries", "loadSessionEntries", "deleteSessionEntries", "config", "skills"]) {
      const denied = await call(method, method === "deleteSessionEntries", "::1");
      expect(denied.status).toBe(401);
      expect(denied.body.error.data.code).toBe("UNAUTHORIZED");
    }

    // Opt-in browser evidence uses the retained UI and actual Next server, not
    // an imitation page. Playwright is supplied by the operator's browser tools.
    const playwrightModule = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
    if (playwrightModule) {
      const { chromium } = await import(playwrightModule);
      const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM });
      const web = Bun.spawn(["bun", "src/server.ts"], {
        env: { ...process.env, PORT: "0", NODE_ENV: "production" },
        stdout: "pipe", stderr: "inherit",
      });
      try {
        let output = "";
        let port = "";
        const reader = web.stdout.getReader();
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          output += new TextDecoder().decode(value);
          const ready = output.match(/web home http:\/\/0\.0\.0\.0:(\d+)/);
          if (ready) { port = ready[1]; break; }
        }
        reader.releaseLock();
        expect(port, output).not.toBe("");
        const page = await browser.newPage();
        for (const [width, filename] of [[1280, "r22-web-desktop.png"], [390, "r22-web-mobile.png"]] as const) {
          await page.setViewportSize({ width, height: 844 });
          await page.goto(`http://127.0.0.1:${port}/s/${Buffer.from(process.env.ROCKETCLAW_HISTORY_TEST_ID!).toString("base64url")}`);
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
      } finally {
        await browser.close();
        web.kill("SIGKILL");
        await web.exited;
      }
    }
    const removed = await call("deleteSessionEntries", true);
    expect(removed.status).toBe(200);
    expect(removed.body.result.data).toBe("1");
    expect((await call("listSessionEntries")).body.result.data).toEqual([]);
    expect((await call("loadSessionEntries")).body.result.data).toEqual([]);
  } finally {
    await new Promise<void>((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
  }
}, 60000);
