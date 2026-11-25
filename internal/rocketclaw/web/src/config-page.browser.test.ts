import { expect, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

type Behavior = { auto_approver_model: string; logging: { level: string }; slack: { channels: unknown[] } };
const initial = { auto_approver_model: "old-model", slack: { channels: [{ channel: "#general", agents: ["main"], allowed_user_ids: ["U0"] }] }, logging: { level: "info" }, overlays: null, embedded_agents: { main: { model: "anthropic/opus#high" } }, future: 1 };
const view = (behavior: Behavior, canEdit: boolean) => ({ workspace: "config", autoApproverModel: behavior.auto_approver_model, loggingLevel: behavior.logging.level, canEdit, decisionProviders: ["typesafe"], behaviorJson: canEdit ? JSON.stringify(behavior) : undefined, mcpServerFacets: [{ server: "alpha", facets: ["read", "write"] }, { server: "zeta", facets: ["sudo"] }] });

test("only admins edit the behavior settings, and the view changes only with the server's answer", async () => {
  const { chromium: engine } = await import(playwright!);
  let config = view(initial, false);
  const saves: unknown[] = [];
  const tested: unknown[] = [];
  const release = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [], owner: "tester", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      return unpacked(await Bun.file(`${file}.gz`).exists() ? file : path.join(dist, "index.html"));
    }
    switch (url.pathname) {
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/Protocol": return Response.json({ protoSha256: "config" });
      case "/api/ListConfig": return Response.json({ config });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main", sticky: "global" }, { name: "helper", sticky: "slack" }, { name: "support", sticky: "slack" }], currentAgent: "main" });
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {} } });
      case "/api/TestModel": tested.push(await request.json()); return Response.json({ elapsedMs: "100" });
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
    const save = page.getByRole("button", { name: /^Sav/ });
    const cancel = page.getByRole("button", { name: "Cancel", exact: true });
    const pick = async (control: string, option: string) => {
      await page.getByRole("combobox", { name: control, exact: true }).click();
      await page.getByRole("option", { name: option, exact: true }).click();
    };

    await page.goto(`http://127.0.0.1:${server.port}/config`);
    await page.getByText("old-model", { exact: true }).waitFor();
    const servers = page.locator("section").filter({ has: page.getByRole("heading", { name: "MCP", exact: true }) }).getByRole("listitem");
    expect(await servers.evaluateAll((items: HTMLElement[]) => items.map((item) => [...item.children].map((chip) => chip.textContent)))).toEqual([["alpha", "read", "write"], ["zeta", "sudo"]]);
    expect(await edit.count()).toBe(0);
    expect(await model.count()).toBe(0);

    config = view(initial, true);
    await page.reload();
    await edit.click();
    expect(await model.inputValue()).toBe("old-model");

    await model.fill("bad");
    await save.click();
    await page.getByText("auto_approver_model is not a known model", { exact: true }).waitFor();
    expect(await model.inputValue()).toBe("bad");

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

    await edit.click();
    await model.fill("discarded");

    await cancel.click();
    await edit.click();
    expect(await model.inputValue()).toBe("new-model");

    await page.getByRole("button", { name: "Add channel", exact: true }).click();
    await page.getByRole("textbox", { name: "Channel 2", exact: true }).fill("#ops");
    await pick("Add agent to channel 2", "helper");
    await page.getByRole("combobox", { name: "Add agent to channel 2", exact: true }).click();
    await page.getByRole("option", { name: "support", exact: true }).waitFor();
    expect(await page.getByRole("option").allInnerTexts()).toEqual(["support"]);
    await page.getByRole("option", { name: "support", exact: true }).click();
    await page.getByRole("textbox", { name: "Add user ID to channel 2", exact: true }).fill("U1");
    await page.keyboard.press("Enter");
    await page.getByRole("textbox", { name: "Wake when for channel 1", exact: true }).fill("someone asks how to deploy");
    await page.getByRole("textbox", { name: "Engagement for channel 1", exact: true }).fill("answer briefly");
    await save.click();
    await edit.waitFor();
    const withChannels = { ...saved, slack: { channels: [{ ...initial.slack.channels[0], wake_when: "someone asks how to deploy", engagement: "answer briefly" }, { channel: "#ops", agents: ["helper", "support"], allowed_user_ids: ["U1"] }] } };
    expect(saves.at(-1)).toEqual(withChannels);

    await page.route("https://models.dev/api.json", (route: { fulfill(options: object): Promise<void> }) => route.fulfill({ json: {}, headers: { "access-control-allow-origin": "*" } }));
    await edit.click();
    const builtIn = (name: string) => page.getByRole("group", { name: `${name} agent`, exact: true });
    const voiceModel = builtIn("voice").getByRole("combobox", { name: "voice model", exact: true });
    expect(await builtIn("main").getByRole("combobox", { name: "main model", exact: true }).inputValue()).toBe("opus");
    expect(await voiceModel.inputValue()).toBe("");
    expect(await builtIn("voice").getByRole("switch", { name: "voice", exact: true }).isChecked()).toBe(true);
    await builtIn("main").getByRole("switch", { name: "main", exact: true }).click();
    await voiceModel.fill("gpt-realtime");
    await builtIn("voice").getByRole("button", { name: "Test", exact: true }).click();
    await builtIn("voice").getByRole("status").getByText(/^Works\./).waitFor();
    expect({ tested, saves: saves.length, editing: await save.isVisible() }).toEqual({ tested: [{ model: "gpt-realtime" }], saves: 3, editing: true });
    await save.click();
    await edit.waitFor();
    const withAgents = { ...withChannels, embedded_agents: { main: { model: "anthropic/opus#high", disabled: true }, voice: { model: "gpt-realtime" } } };
    expect(saves.at(-1)).toEqual(withAgents);

    await edit.click();
    const titles = (name: string) => page.getByRole("group", { name, exact: true });
    await titles("Title model").getByRole("combobox", { name: "Title model", exact: true }).fill("gpt-6-luna");
    const judge = titles("Title decision model").getByRole("textbox", { name: "Title decision model", exact: true });
    expect(await judge.getAttribute("placeholder")).toBe("Off");
    await judge.fill("jev-latest");
    await titles("Title decision model").getByRole("button", { name: "Test", exact: true }).click();
    await titles("Title decision model").getByRole("status").getByText("Works. typesafe answered in 0.1 s.", { exact: true }).waitFor();
    expect(tested.at(-1)).toEqual({ model: "typesafe/jev-latest", decision: true });
    await save.click();
    await edit.waitFor();
    const withTitles = { ...withAgents, title_model: "gpt-6-luna", title_decision_model: "typesafe/jev-latest" };
    expect(saves.at(-1)).toEqual(withTitles);

    const listener = page.getByRole("group", { name: "Ambient decision model", exact: true }).getByRole("textbox", { name: "Ambient decision model", exact: true });
    for (const name of ["jev-latest", ""]) {
      await edit.click();
      expect(await listener.getAttribute("placeholder")).toBe("Off");
      await listener.fill(name);
      await save.click();
      await edit.waitFor();
      expect(saves.at(-1)).toEqual({ ...withTitles, slack: { ...withTitles.slack, ambient_decision_model: name && `typesafe/${name}` } });
    }
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    server.stop(true);
  }
}, 30_000);
