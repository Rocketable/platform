import { expect, test } from "bun:test";
import path from "node:path";
import { PALETTES } from "./components/theme";
import type { AgentChoices, TranscriptEvent } from "./types";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;
const dist = path.resolve(import.meta.dir, "../../internal/web/dist");

test.skipIf(!playwright || !chromium)("agent risk follows selection locally across themes and mobile", async () => {
  const { chromium: engine } = await import(playwright!);
  const agents: AgentChoices["agents"] = [
    { name: "main", model: "test" },
    ...(["primary", "warning", "danger"] as const).map((riskLevel) => ({ name: riskLevel, model: "test", riskLevel })),
  ];
  const current: Record<string, string> = { source: "warning", other: "main" };
  const mutations: { id: string; text: string }[] = [];
  const history: TranscriptEvent[] = Array.from({ length: 50 }, (_, i) => ({ messageId: `${i}:0`, entryKey: `${i}`, itemId: `${i}`, inputId: "", role: "assistant", text: `Saved answer ${i}\n\nConversation text remains readable.`, complete: true, turnId: `${i}` }));
  let holdCatalog = false;
  const release = Promise.withResolvers<void>();
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: Object.keys(current).map((id) => ({ id, name: id, agent: current[id] })), owner: "tester", upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; conversationId: string; text: string };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "agent-risk" });
      case "/api/Identity": return Response.json({ username: "tester" });
      case "/api/ListAgents": {
        if (holdCatalog) await release.promise;
        const catalog: AgentChoices = { agents, currentAgent: current[input.conversationId] ?? "main" };
        return Response.json(catalog);
      }
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListConfig": return Response.json({ config: {} });
      case "/api/ListCronJobs": return Response.json({ jobs: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/History": return Response.json({ messages: history, origin: "", revision: "saved", reset: true, replacedKeys: [], removedKeys: [], entryKeys: history.map((item) => item.entryKey), running: false, terminal: "completed" });
      case "/api/Prompt":
        mutations.push({ id: input.id, text: input.text });
        if (input.text.startsWith("$agent ")) current[input.id] = input.text.slice(7);
        return Response.json({ privateText: "" });
      default: mutations.push({ id: input.id, text: url.pathname }); return Response.json({});
    }
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, colorScheme: "light" });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("source").replace(/=+$/, "")}`);
    const pane = page.locator(".conversation-pane");
    const composer = page.locator("textarea");
    await page.getByRole("combobox", { name: "Choose agent" }).waitFor();
    await page.waitForFunction(() => document.querySelector(".conversation-pane")?.getAttribute("data-agent-risk") === "warning");
    expect(await pane.getAttribute("data-agent-risk")).toBe("warning");
    holdCatalog = true;
    const choose = async (name: string) => {
      await page.getByRole("combobox", { name: "Choose agent" }).click();
      await page.getByRole("option", { name: new RegExp(`^${name}\\s+test$`) }).click();
      expect(await pane.getAttribute("data-agent-risk")).toBe(name === "main" ? null : name);
      await page.getByRole("listbox").waitFor({ state: "hidden" });
    };
    const styles = () => pane.evaluate((node: HTMLElement) => {
      const card = node.querySelector<HTMLElement>(".composer-surface")!;
      const canvas = document.createElement("canvas").getContext("2d")!;
      const rgb = (color: string) => { canvas.clearRect(0, 0, 1, 1); canvas.fillStyle = color; canvas.fillRect(0, 0, 1, 1); return [...canvas.getImageData(0, 0, 1, 1).data].slice(0, 3); };
      const contrast = (a: string, b: string) => {
        const luminance = (color: string) => rgb(color).map((value) => { const v = value / 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }).reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0);
        const x = luminance(a), y = luminance(b);
        return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
      };
      const p = getComputedStyle(node), c = getComputedStyle(card);
      const text = getComputedStyle(node.querySelector("textarea")!);
      const placeholder = getComputedStyle(node.querySelector("textarea")!, "::placeholder");
      const muted = getComputedStyle(node.querySelector('#transcript-scroll p[role="status"]')!);
      const risk = node.getAttribute("data-agent-risk") as "primary" | "warning" | "danger" | null;
      const surface = p.backgroundColor === "rgba(0, 0, 0, 0)" ? p.getPropertyValue("--background") : p.backgroundColor;
      return { pane: p.backgroundColor, card: c.backgroundColor, border: c.borderColor, outline: c.outlineColor, outlineStyle: c.outlineStyle, outlineWidth: parseFloat(c.outlineWidth), riskColor: risk ? rgb(p.getPropertyValue("--agent-risk-color")) : null, expectedRiskColor: risk ? rgb(p.getPropertyValue({ primary: "--primary", warning: "--warning", danger: "--destructive" }[risk])) : null, mutedText: contrast(muted.color, surface), paneText: contrast(p.color, surface), cardText: contrast(text.color, c.backgroundColor), placeholderText: contrast(placeholder.color, c.backgroundColor), focus: contrast(c.outlineColor, surface), body: getComputedStyle(document.body).backgroundColor, sidebar: getComputedStyle(document.querySelector("#session-sidebar")!).backgroundColor, theme: localStorage.getItem("theme"), palette: localStorage.getItem("palette") };
    });
    await choose("main");
    const ordinary = await styles();
    for (const name of ["primary", "warning", "danger"]) {
      await choose(name);
      const tinted = await styles();
      expect(tinted.pane).not.toBe(ordinary.pane);
      expect(tinted.card).not.toBe(ordinary.card);
      expect(tinted.border).not.toBe(ordinary.border);
      expect([tinted.body, tinted.sidebar, tinted.theme, tinted.palette]).toEqual([ordinary.body, ordinary.sidebar, ordinary.theme, ordinary.palette]);
    }
    await choose("main");
    expect(await styles()).toEqual(ordinary);
    await choose("danger");
    await composer.fill("retained draft");
    holdCatalog = false;
    release.resolve();
    await page.locator("#session-sidebar").getByRole("link", { name: /other/ }).click();
    expect(await pane.getAttribute("data-agent-risk")).toBe(null);
    await page.locator("#session-sidebar").getByRole("link", { name: /source/ }).click();
    expect(await composer.inputValue()).toBe("retained draft");
    expect(await pane.getAttribute("data-agent-risk")).toBe("danger");
    expect(mutations).toEqual([]);
    await composer.press("Enter");
    await page.waitForFunction(() => (document.querySelector("textarea") as HTMLTextAreaElement).value === "");
    expect(mutations).toEqual([{ id: "source", text: "$agent danger" }, { id: "source", text: "retained draft" }]);

    await page.keyboard.press("Meta+Shift+p");
    await page.getByRole("dialog", { name: "Run command" }).getByRole("button", { name: "Settings", exact: true }).click();
    await page.getByRole("combobox", { name: "Color theme" }).click();
    await page.getByRole("option", { name: "Lagoon", exact: true }).click();
    await page.getByRole("button", { name: "Theme system", exact: true }).click();
    await page.locator("#session-sidebar").getByRole("link", { name: /source/ }).click();
    expect(await pane.getAttribute("data-agent-risk")).toBe("danger");
    expect(await page.locator("html").getAttribute("data-palette")).toBe("lagoon");
    expect(await page.evaluate(() => [localStorage.getItem("palette"), localStorage.getItem("theme")])).toEqual(["lagoon", "light"]);
    await page.getByRole("button", { name: "Theme light", exact: true }).click();
    await page.getByRole("button", { name: "Theme dark", exact: true }).click();

    const matrix: object[] = [];
    for (const palette of PALETTES) for (const mode of ["light", "dark"]) {
      await page.evaluate(({ palette, mode }: { palette: string; mode: string }) => { document.documentElement.dataset.palette = palette; document.documentElement.classList.toggle("dark", mode === "dark"); }, { palette: palette.id, mode });
      expect(await page.locator("html").getAttribute("data-palette")).toBe(palette.id);
      await choose("main");
      const base = await styles();
      for (const name of ["primary", "warning", "danger", "main"]) {
        await choose(name);
        await composer.focus();
        const value = await styles();
        expect(value.riskColor).toEqual(value.expectedRiskColor);
        expect(value.mutedText).toBeGreaterThanOrEqual(4.5);
        expect(value.paneText).toBeGreaterThanOrEqual(4.5);
        expect(value.cardText).toBeGreaterThanOrEqual(4.5);
        expect(value.placeholderText).toBeGreaterThanOrEqual(4.5);
        expect([value.body, value.sidebar, value.theme, value.palette]).toEqual([base.body, base.sidebar, base.theme, base.palette]);
        if (name === "main") expect([value.pane, value.card, value.border]).toEqual([base.pane, base.card, base.border]);
        else {
          expect(value.pane).not.toBe(base.pane);
          expect(value.card).not.toBe(base.card);
          expect(value.focus).toBeGreaterThanOrEqual(3);
          expect(value.outlineStyle).not.toBe("none");
          expect(value.outlineWidth).toBeGreaterThan(0);
        }
        matrix.push({ palette: palette.id, mode, risk: name, computed: value });
      }
    }
    await page.evaluate(() => { document.documentElement.dataset.palette = "neutral"; document.documentElement.classList.remove("dark"); });
    await choose("danger");
    const systemLight = await styles();
    await page.emulateMedia({ colorScheme: "dark" });
    await page.waitForFunction(() => document.documentElement.classList.contains("dark"));
    expect((await styles()).pane).not.toBe(systemLight.pane);
    expect(await pane.getAttribute("data-agent-risk")).toBe("danger");
    expect(await page.evaluate(() => localStorage.getItem("theme"))).toBe("system");
    await page.waitForFunction(() => getComputedStyle(document.querySelector('[aria-label="Choose agent"]')!).color === getComputedStyle(document.querySelector(".conversation-pane")!).color);
    await Bun.write(path.resolve(import.meta.dir, "../../../../.tmp/rocketclaw/agent-risk/desktop.png"), await page.screenshot());
    await page.setViewportSize({ width: 390, height: 664 });
    await choose("warning");
    await composer.focus();
    const mobile = await styles();
    expect(mobile.outlineStyle).not.toBe("none");
    expect(mobile.outlineWidth).toBeGreaterThan(0);
    expect(mobile.focus).toBeGreaterThanOrEqual(3);
    expect(await composer.evaluate((node: HTMLElement) => document.activeElement === node)).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect(await pane.evaluate((node: HTMLElement) => node.getBoundingClientRect().bottom <= innerHeight)).toBe(true);
    const scroller = page.locator('[data-slot="message-scroller-viewport"]');
    expect(await scroller.evaluate((node: HTMLElement) => node.scrollHeight > node.clientHeight)).toBe(true);
    await scroller.evaluate((node: HTMLElement) => { node.scrollTop = 0; });
    expect(await scroller.evaluate((node: HTMLElement) => node.scrollTop)).toBe(0);
    expect(errors).toEqual([]);
    await Bun.write(path.resolve(import.meta.dir, "../../../../.tmp/rocketclaw/agent-risk/theme-matrix.json"), JSON.stringify(matrix, null, 2));
    await Bun.write(path.resolve(import.meta.dir, "../../../../.tmp/rocketclaw/agent-risk/mobile.png"), await page.screenshot());
  } finally {
    release.resolve();
    await browser.close();
    server.stop(true);
  }
}, 180_000);
