import { expect, test } from "bun:test";
import path from "node:path";

type Captured = { event: string; properties: Record<string, unknown> };

async function entry() {
  const file = path.join(import.meta.dir, "posthog-browser-entry.ts");
  const result = await Bun.build({ entrypoints: [file], target: "browser", define: { "process.env.NODE_ENV": '"production"' }, plugins: [{ name: "posthog-test-entry", setup(build) {
    build.onResolve({ filter: /posthog-browser-entry\.ts$/ }, () => ({ path: file, namespace: "posthog-test" }));
    build.onLoad({ filter: /.*/, namespace: "posthog-test" }, () => ({ loader: "ts", resolveDir: import.meta.dir, contents: `
      import posthog from "posthog-js";
      import "./posthog";
      import { rpc, listSessions } from "./api";
      import { navigate } from "./navigation";
      const ours = posthog.config.before_send;
      window.captured = [];
      posthog.set_config({ opt_out_useragent_filter: true, disable_compression: true, request_batching: false, before_send: (event) => { const sent = ours(event); window.captured.push(sent); return sent; } });
      Object.assign(window, { rpc, listSessions, navigate });
      const button = Object.assign(document.createElement("button"), { id: "private-element-id", textContent: "slow" });
      button.addEventListener("click", () => { const until = performance.now() + 150; while (performance.now() < until) {} });
      const input = Object.assign(document.createElement("input"), { ariaLabel: "Slow input" });
      input.addEventListener("keydown", () => { const until = performance.now() + 100; while (performance.now() < until) {} });
      document.body.append(button, input);
    ` }));
  } }] });
  expect(result.success).toBe(true);
  return result.outputs[0].text();
}

for (const rate of [0, 1]) test(`PostHog reports caught errors always and timings at sample rate ${rate}`, async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const js = await entry();
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.route("**/*", async (route: any) => {
      const url = new URL(route.request().url());
      if (url.hostname === "telemetry.test") {
        if (url.pathname.endsWith(".js")) {
          const extension = Bun.file(path.resolve(import.meta.dir, "../node_modules/posthog-js/dist", url.pathname.split("/").at(-1)!));
          return route.fulfill({ body: await extension.exists() ? await extension.text() : "", contentType: "text/javascript" });
        }
        return route.fulfill({ json: {} });
      }
      if (url.pathname === "/") return route.fulfill({ contentType: "text/html", body: `<html><head><script id="posthog-config" type="application/json">{"project_token":"synthetic-project","host":"https://telemetry.test","timing_sample_rate":${rate}}</script></head><body><script type="module" src="/entry.js"></script></body></html>` });
      if (url.pathname === "/entry.js") return route.fulfill({ body: js, contentType: "text/javascript" });
      if (url.pathname === "/api/Identity") return route.fulfill({ json: { username: "synthetic-viewer" } });
      if (url.pathname === "/api/Protocol" || url.pathname === "/api/History") return route.fulfill({ json: {} });
      if (url.pathname === "/api/Slow") return setTimeout(() => route.fulfill({ json: {} }).catch(() => {}), 500);
      if (url.pathname === "/api/Fail") return route.fulfill({ status: 500, json: { message: "request failure", code: 13 } });
      if (url.pathname === "/api/ListSessions") return route.fulfill({ status: 500, json: { message: "stream failure", code: 13 } });
      return route.abort();
    });
    await page.goto("https://preview.test/");
    await page.waitForFunction(() => (window as any).captured !== undefined);
    await page.evaluate(async () => {
      const { rpc, listSessions } = window as any;
      await rpc("History");
      await rpc("Protocol");
      await rpc("Fail").catch(() => {});
      await rpc("Slow", {}, AbortSignal.timeout(50)).catch(() => {});
      await listSessions().next().catch(() => {});
    });
    await page.evaluate(() => (window as any).navigate("/s/private-conversation-id"));
    await page.getByRole("button", { name: "slow" }).click();
    await page.getByRole("textbox", { name: "Slow input" }).press("a");
    const captured = async () => page.evaluate(() => (window as any).captured.filter(Boolean)) as Promise<Captured[]>;
    const exceptions = async () => (await captured()).filter((event) => event.event === "$exception").map((event) => event.properties.rpc);
    await page.waitForFunction(() => (window as any).captured.filter((event: Captured) => event?.event === "$exception").length >= 2);
    if (rate === 1) await page.waitForFunction(() => ["web_navigation", "web_long_frame", "web_interaction"].every((name) => (window as any).captured.some((event: Captured) => event?.event === name)));
    else await page.waitForTimeout(500);
    expect((await exceptions()).toSorted()).toEqual(["Fail", "ListSessions"]);
    expect(new Set((await captured()).map((event) => event.properties.timing_sampled))).toEqual(new Set([rate === 1]));
    const timings = (await captured()).filter((event) => event.event.startsWith("web_"));
    if (rate === 0) {
      expect(timings).toEqual([]);
      return;
    }
    const requests = timings.filter((event) => event.event === "web_request").map(({ properties }) => [properties.rpc, properties.outcome, properties.remote, properties.page_type, typeof properties.duration_ms]);
    expect(requests).toEqual([["History", "done", false, "/", "number"], ["Fail", "failed", false, "/", "number"], ["Slow", "aborted", false, "/", "number"]]);
    expect(timings.find((event) => event.event === "web_navigation")!.properties).toMatchObject({ page_type: "/s/:id" });
    const frame = timings.find((event) => event.event === "web_long_frame" && event.properties.script_invoker_type === "event-listener")!.properties;
    expect(frame).toMatchObject({ page_type: "/s/:id", script_invoker: "BUTTON.onclick" });
    expect(frame.duration_ms).toBeGreaterThanOrEqual(150);
    await page.waitForFunction(() => (window as any).captured.filter((event: Captured) => event?.event === "web_interaction").length >= 2);
    const interactions = (await captured()).filter((event) => event.event === "web_interaction").map(({ properties }) => [properties.interaction, properties.target_tag, properties.target_label ?? null, properties.page_type]);
    expect(interactions).toEqual([["click", "BUTTON", null, "/s/:id"], ["keydown", "INPUT", "Slow input", "/s/:id"]]);
    expect(JSON.stringify(await captured())).not.toContain("private-element-id");
    expect(errors).toEqual([]);
  } finally {
    await browser.close();
  }
}, 30_000);
