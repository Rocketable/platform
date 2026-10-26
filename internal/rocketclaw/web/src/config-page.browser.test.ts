import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Behavior = { auto_approver_model: string; logging: { level: string }; slack: { channels: unknown[] } };
// `future` stands for a field this screen does not know; saves must keep it.
const initial = { auto_approver_model: "old-model", slack: { channels: [{ channel: "#general", agents: ["main"], allowed_user_ids: ["U0"] }] }, server_display_name: "", logging: { level: "info" }, instrumentation: { hide_inputs: false, hide_outputs: false }, web: { sentry: { environment: "", traces_sample_rate: null } }, overlays: null, future: 1 };
const view = (behavior: Behavior, canEdit: boolean) => ({ workspace: "config", autoApproverModel: behavior.auto_approver_model, loggingLevel: behavior.logging.level, canEdit, behaviorJson: canEdit ? JSON.stringify(behavior) : undefined });

test("only admins edit the behavior settings, and the view changes only with the server's answer", async () => {
  const { chromium: engine } = await import(playwright!);
  let config = view(initial, false);
  const saves: unknown[] = [];
  const release = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "config" });
      case "/api/ListConfig": return Response.json({ config });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }, { name: "helper" }], currentAgent: "main" });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      case "/api/SaveBehaviorSettings": {
        const { documentJson } = await request.json() as { documentJson: string };
        const behavior = JSON.parse(documentJson) as Behavior;
        saves.push(behavior);
        if (behavior.auto_approver_model === "bad") return Response.json({ code: 3, message: "auto_approver_model is not a known model" }, { status: 400 });
        await release.promise;
        config = view(behavior, true);
        return Response.json({ config });
      }
      default: return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const edit = page.getByRole("button", { name: "Edit", exact: true });
    const model = page.getByRole("textbox", { name: "Auto-approver model", exact: true });
    const rate = page.getByRole("spinbutton", { name: "Sentry sample rate", exact: true });
    const save = page.getByRole("button", { name: /^Sav/ });
    const cancel = page.getByRole("button", { name: "Cancel", exact: true });
    const pick = async (control: string, option: string) => {
      await page.getByRole("combobox", { name: control, exact: true }).click();
      await page.getByRole("option", { name: option, exact: true }).click();
    };

    // Covers AE3: a non-admin sees the settings and has no way to edit them.
    await page.goto(`http://127.0.0.1:${server.port}/config`);
    await page.getByText("old-model", { exact: true }).waitFor();
    expect(await edit.count()).toBe(0);
    expect(await model.count()).toBe(0);

    config = view(initial, true);
    await page.reload();
    await edit.click();
    expect(await model.inputValue()).toBe("old-model");

    // A rejected save shows the server's reason and keeps the edits.
    await model.fill("bad");
    await save.click();
    await page.getByText("auto_approver_model is not a known model", { exact: true }).waitFor();
    expect(await model.inputValue()).toBe("bad");

    // While a save is pending the controls are locked and the page still shows the saved settings.
    await model.fill("new-model");
    await pick("Log level", "warn");
    await save.click();
    await page.getByRole("button", { name: "Saving…", exact: true }).waitFor();
    expect(await save.isDisabled()).toBe(true);
    expect(await cancel.isDisabled()).toBe(true);
    expect(await model.isDisabled()).toBe(true);
    expect(await page.getByText("old-model", { exact: true }).count()).toBe(1);
    expect(await page.getByText("info", { exact: true }).filter({ visible: true }).count()).toBe(1);

    release.resolve();
    await page.getByText("new-model", { exact: true }).waitFor();
    await page.getByText("warn", { exact: true }).waitFor();
    await edit.waitFor();
    expect(await model.count()).toBe(0);
    expect(await page.getByText("old-model", { exact: true }).count()).toBe(0);
    const saved = { ...initial, auto_approver_model: "new-model", logging: { level: "warn" } };
    expect(saves).toEqual([{ ...initial, auto_approver_model: "bad" }, saved]);

    // A sample rate outside 0 to 1 blocks Save until it is fixed.
    await edit.click();
    await rate.fill("2");
    await page.getByText("Between 0 and 1", { exact: true }).waitFor();
    expect(await save.isDisabled()).toBe(true);
    await rate.fill("0.5");
    expect(await page.getByText("Between 0 and 1", { exact: true }).count()).toBe(0);
    expect(await save.isDisabled()).toBe(false);

    // Cancel drops the edit, and Edit starts again from the saved settings.
    await cancel.click();
    await edit.click();
    expect(await rate.inputValue()).toBe("");
    expect(await model.inputValue()).toBe("new-model");

    // A new Slack channel takes its agents from the agent list, in the order picked.
    await page.getByRole("button", { name: "Add channel", exact: true }).click();
    await page.getByRole("textbox", { name: "Channel 2", exact: true }).fill("#ops");
    await pick("Add agent to channel 2", "helper");
    await pick("Add agent to channel 2", "main");
    await page.getByRole("textbox", { name: "Add user ID to channel 2", exact: true }).fill("U1");
    await page.keyboard.press("Enter");
    await save.click();
    await edit.waitFor();
    expect(saves.at(-1)).toEqual({ ...saved, slack: { channels: [...initial.slack.channels, { channel: "#ops", agents: ["helper", "main"], allowed_user_ids: ["U1"] }] } });
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
