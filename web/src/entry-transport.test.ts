import { expect, test } from "bun:test";
import { Layer } from "effect";
import http from "node:http";
import path from "node:path";
import { RocketclawLive, protoSHA256 } from "./grpc";
import { createRPCHandler } from "./transport";
import { WhoisLive } from "./whois";

// Invoked by Go's TestSessionEntries with real isolated PostgreSQL storage.
test.skipIf(!process.env.ROCKETCLAW_ENTRY_TEST_ID)("entry panel HTTP proxy reaches Go and rejects an unmapped connection", async () => {
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
    const sessions = await call("sessions");
    expect(sessions.status).toBe(200);
    expect(sessions.body.result.data.map((session: { id: string }) => session.id)).toEqual(["empty-web", id]);
    const history = await call("history", false, "127.0.0.1", process.env.ROCKETCLAW_HISTORY_TEST_ID!);
    expect(history.status).toBe(200);
    expect(history.body.result.data.map(({ role, text }: { role: string; text: string }) => ({ role, text }))).toEqual([
      { role: "user", text: "human one" }, { role: "assistant", text: "answer one" },
      { role: "user", text: "human two" }, { role: "assistant", text: "answer two" },
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
        await page.goto(`http://127.0.0.1:${port}/s/${Buffer.from(id).toString("base64url")}`);
        await page.getByText("Session entries", { exact: true }).click();
        const panel = page.locator("details").filter({ hasText: "Session entries" });
        await panel.getByRole("button", { name: "List entries", exact: true }).click();
        await panel.locator("pre").filter({ hasText: listed.body.result.data[0].id }).waitFor();
        await panel.getByRole("button", { name: "Load entries", exact: true }).click();
        await panel.locator("pre").filter({ hasText: "\\\"version\\\"" }).waitFor();
        await page.screenshot({ path: path.join(process.env.TMPDIR!, "r22-web-desktop.png") });
        await page.setViewportSize({ width: 390, height: 844 });
        expect(await panel.getByRole("button", { name: "Delete entries", exact: true }).isVisible()).toBe(true);
        await page.screenshot({ path: path.join(process.env.TMPDIR!, "r22-web-mobile.png") });
        page.once("dialog", (dialog: { accept: () => Promise<void> }) => dialog.accept());
        await panel.getByRole("button", { name: "Delete entries", exact: true }).click();
        await panel.getByText("Deleted 1 entries.", { exact: true }).waitFor();
      } finally {
        await browser.close();
        web.kill("SIGKILL");
        await web.exited;
      }
    }
    const removed = await call("deleteSessionEntries", true);
    expect(removed.status).toBe(200);
    expect(removed.body.result.data).toBe(playwrightModule ? "0" : "1");
    expect((await call("listSessionEntries")).body.result.data).toEqual([]);
    expect((await call("loadSessionEntries")).body.result.data).toEqual([]);
  } finally {
    await new Promise<void>((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));
  }
}, 60000);
