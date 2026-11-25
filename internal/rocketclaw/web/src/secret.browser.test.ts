import { expect, mock, test } from "bun:test";
import path from "node:path";
import { unpacked } from "./test-browsers";
import type { PendingSecret, Session } from "./types";
import { fetch as openCode, reset } from "../../oc/host/test/server";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
const ocDist = path.resolve(import.meta.dir, "../../internal/web/oc-dist");
const shots = path.resolve(import.meta.dir, "../../../../.tmp/web-screenshots");
const encode = (text: string) => btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const historyView = { origin: "", delegations: [], replacedKeys: [], removedKeys: [], terminal: "", movable: false, backgroundJobs: [], messages: [], revision: "1", reset: true, entryKeys: [], running: true, start: "0", more: false };
const value = "ghp_example-token-0123456789";

function serve(rows: Session[], secrets: Map<string, PendingSecret[]>, answer: (body: { askId: string; value?: string }) => Response | undefined) {
  const calls: { path: string; body: { [key: string]: unknown } }[] = [];
  const streams = new Set<ReadableStreamDefaultController<string>>();
  const web = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname.startsWith("/oc/box/api/")) return openCode(request);
    if (url.pathname.startsWith("/oc-ui/")) return unpacked(path.join(ocDist, url.pathname.slice("/oc-ui/".length)));
    if (url.pathname === "/stream" || url.pathname === "/typing") {
      let own: ReadableStreamDefaultController<string>;
      return new Response(new ReadableStream<string>({ start(controller) { own = controller; streams.add(controller); controller.enqueue(": connected\n\n"); }, cancel() { streams.delete(own); } }), { headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: rows, owner: "tester", upstreamSuccess: true, opencodeServers: { box: "http://box.test:4096" } })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = path.join(dist, url.pathname);
      if (await Bun.file(`${file}.gz`).exists()) return unpacked(file);
      return new Response(await unpacked(path.join(dist, "index.html")).text(), { headers: { "Content-Type": "text/html" } });
    }
    const input = await request.json() as { id?: string; conversationId?: string; askId: string; value?: string };
    calls.push({ path: url.pathname, body: input });
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "secret" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "secret" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Prompt": return Response.json({ privateText: "" });
      case "/api/History": return Response.json(historyView);
      case "/api/PendingSecrets": return Response.json({ secrets: secrets.get(input.conversationId!) ?? [] });
      case "/api/AnswerSecret": {
        const refused = answer(input);
        if (refused) return refused;
        secrets.set(input.conversationId!, secrets.get(input.conversationId!)!.filter((secret) => secret.id !== input.askId));
        return Response.json({});
      }
      case "/api/Tabs": return Response.json({ spaces: [{ id: "main", name: "Main" }], state: { tabs: rows.map((row) => ({ path: row.kind ? `/oc/${encode(row.id)}` : `/s/${encode(row.id)}`, space: "main" })), groups: [], space: "main", last: {} } });
      default: return Response.json({});
    }
  } });
  const notify = (conversationId: string) => { for (const stream of streams) stream.enqueue(`data: ${JSON.stringify({ conversationId, revision: "2" })}\n\n`); };
  return { web, calls, notify, base: `http://127.0.0.1:${web.port}` };
}

for (const [width, height] of [[1280, 900], [390, 664]]) test(`a native chat's pending secret at ${width}px`, async () => {
  const { chromium: engine } = await import(playwright!);
  const secrets = new Map([["chat", [{ id: "s1", name: "GITHUB_TOKEN", reason: "Open the pull request" }]]]);
  const { web, calls, notify, base } = serve([{ id: "chat", agent: "main", running: true }], secrets, (body) =>
    body.value === btoa("short") ? Response.json({ code: 3, message: "a secret value must be at least 8 bytes" }, { status: 400 }) : undefined);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`${base}/s/${encode("chat")}`);
    const card = page.getByRole("form", { name: "Secret" });
    await card.getByText("The agent asks for a secret: GITHUB_TOKEN").waitFor();
    await card.getByText("Open the pull request").waitFor();
    const composer = page.locator("textarea");
    expect((await card.boundingBox())!.y).toBeLessThan((await composer.boundingBox())!.y);
    const field = card.getByLabel("The agent asks for a secret: GITHUB_TOKEN");
    expect(await field.evaluate((input: HTMLInputElement) => [input.type, input.autocomplete, !!input.closest(".ph-no-capture")])).toEqual(["password", "off", true]);
    expect(await composer.getAttribute("placeholder")).toBe("Paste the secret in the form above, not here");
    await page.screenshot({ path: path.join(shots, `secret-native-${width}.png`) });

    await field.fill("short");
    await card.getByRole("button", { name: "Save" }).click();
    await card.getByText("a secret value must be at least 8 bytes").waitFor();
    expect(await field.inputValue()).toBe("");

    await field.fill(value);
    await composer.fill("a follow-up while it waits");
    await page.getByRole("button", { name: /^(Send|Queue)$/ }).click();
    await page.locator("textarea:enabled").waitFor();
    for (const started = Date.now(); !calls.some((call) => call.path === "/api/Prompt") && Date.now() - started < 5_000;) await Bun.sleep(10);
    expect(await field.inputValue()).toBe(value);
    await card.getByRole("button", { name: "Save" }).click();
    await card.waitFor({ state: "detached" });
    expect(await composer.getAttribute("placeholder")).toBe("Queue a follow-up");

    secrets.set("chat", [{ id: "s2", name: "NPM_TOKEN", reason: "Publish the package" }]);
    notify("chat");
    await card.getByText("The agent asks for a secret: NPM_TOKEN").waitFor();
    await card.getByRole("button", { name: "Decline" }).click();
    await card.waitFor({ state: "detached" });

    const stored = await page.evaluate(() => new Promise<string>((resolve) => {
      const open = indexedDB.open("rocketclaw-drafts", 1);
      open.onsuccess = () => { const read = open.result.transaction("content").objectStore("content").getAll(); read.onsuccess = () => resolve(JSON.stringify(read.result)); };
    }));
    expect(stored).not.toContain(value);
    expect(JSON.stringify(calls.filter((call) => call.path !== "/api/AnswerSecret"))).not.toContain(value);
    expect(calls.filter((call) => ["/api/AnswerSecret", "/api/Prompt"].includes(call.path))).toEqual([
      { path: "/api/AnswerSecret", body: { conversationId: "chat", askId: "s1", value: btoa("short") } },
      { path: "/api/Prompt", body: expect.objectContaining({ id: "chat", text: "a follow-up while it waits", delivery: "QUEUE" }) },
      { path: "/api/AnswerSecret", body: { conversationId: "chat", askId: "s1", value: btoa(value) } },
      { path: "/api/AnswerSecret", body: { conversationId: "chat", askId: "s2", decline: true } },
    ]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 30_000);

test("a Remote OpenCode Session's pending secret shows as a strip above the view, only while its tab shows", async () => {
  const { chromium: engine } = await import(playwright!);
  reset();
  const remote = "opencode:box:ses_a";
  const secrets = new Map([[remote, [{ id: "s1", name: "GITHUB_TOKEN", reason: "Push the branch" }]]]);
  const rows: Session[] = [{ id: remote, kind: "REMOTE_OPENCODE", remoteSessionId: "ses_a", preview: "Alpha work", server: "box", directory: "/repo", asking: true }, { id: "chat", preview: "Native chat", agent: "main" }];
  const { web, calls, base } = serve(rows, secrets, () => undefined);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    const visit = (href: string) => page.evaluate((target: string) => { history.pushState(null, "", target); window.dispatchEvent(new PopStateEvent("popstate")); }, href);
    const strip = page.getByRole("form", { name: "Secret" });
    const view = page.locator("main [data-oc-view]:not([data-oc-hidden])");

    await page.goto(`${base}/s/${encode("chat")}`);
    await page.locator("main [data-oc-view][data-oc-hidden]").getByText("answer in ses_a").waitFor({ state: "attached" });
    expect(await strip.count()).toBe(0);
    expect(calls.filter((call) => call.path === "/api/PendingSecrets" && call.body.conversationId === remote)).toEqual([]);

    await visit(`/oc/${encode(remote)}`);
    await view.getByText("answer in ses_a").waitFor();
    await strip.getByText("The agent asks for a secret: GITHUB_TOKEN").waitFor();
    expect((await strip.boundingBox())!.y + (await strip.boundingBox())!.height).toBeLessThanOrEqual((await view.boundingBox())!.y);
    await page.screenshot({ path: path.join(shots, "secret-remote-1280.png") });
    const field = strip.getByLabel("The agent asks for a secret: GITHUB_TOKEN");
    await field.fill(value);

    await visit(`/s/${encode("chat")}`);
    await strip.waitFor({ state: "detached" });
    expect(await page.locator("input[type=password]").count()).toBe(0);
    await visit(`/oc/${encode(remote)}`);
    expect(await field.inputValue()).toBe("");
    await field.fill(value);
    await strip.getByRole("button", { name: "Save" }).click();
    await strip.waitFor({ state: "detached" });
    expect(calls.filter((call) => call.path === "/api/AnswerSecret")).toEqual([{ path: "/api/AnswerSecret", body: { conversationId: remote, askId: "s1", value: btoa(value) } }]);
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
    web.stop(true);
  }
}, 60_000);

test("PostHog session replay drops AnswerSecret requests", async () => {
  let options: { session_recording: { maskCapturedNetworkRequestFn: (request: { name: string }) => unknown } } | undefined;
  mock.module("posthog-js", () => ({ default: { init: (_token: string, given: typeof options) => { options = given; }, identify: () => {} } }));
  const page = globalThis.document;
  globalThis.document = { getElementById: () => ({ textContent: JSON.stringify({ project_token: "token", host: "https://telemetry.test" }) }) } as unknown as Document;
  try {
    await import("./posthog");
  } finally {
    globalThis.document = page;
  }
  const mask = options!.session_recording.maskCapturedNetworkRequestFn;
  expect(mask({ name: "http://localhost/api/AnswerSecret" })).toBeNull();
  expect(mask({ name: "http://localhost/api/PendingSecrets" })).toEqual({ name: "http://localhost/api/PendingSecrets" });
});
