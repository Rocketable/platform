import { expect, test } from "bun:test";
import path from "node:path";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;

test.skipIf(!playwright || !chromium)("useAction keeps overlapping executions independent and displays the latest", async () => {
  const { chromium: engine } = await import(playwright!);
  const entry = path.join(import.meta.dir, "action-harness.tsx");
  const bundle = await Bun.build({ entrypoints: [entry], target: "browser", files: { [entry]: `
    import React from "react";
    import { createRoot } from "react-dom/client";
    import { Effect } from "effect";
    import { RegistryContext } from "@effect/atom-react";
    import { registry, useAction } from ${JSON.stringify(path.join(import.meta.dir, "state.ts"))};
    const promises = {}, successes = [], aborted = [];
    function Harness() {
      const action = useAction({
        action: (input) => Effect.tryPromise({
          try: async (signal) => {
            signal.addEventListener("abort", () => aborted.push(input));
            const response = await fetch("/" + input, { signal });
            const value = await response.text();
            if (!response.ok) throw new Error(value);
            return value;
          },
          catch: (error) => error,
        }),
        onSuccess: (value, input) => successes.push({ value, input }),
      });
      Reflect.set(window, "start", (input) => {
        promises[input] = action.execute(input).then((value) => ({ value }), (error) => ({ error: error.message }));
      });
      return <output>{JSON.stringify({ variables: action.variables, pending: action.isPending, error: action.error?.message ?? null })}</output>;
    }
    Reflect.set(window, "results", async () => ({ A: await promises.A, B: await promises.B, successes, aborted }));
    Reflect.set(window, "resultB", () => promises.B);
    createRoot(document.getElementById("root")).render(<RegistryContext.Provider value={registry}><Harness /></RegistryContext.Provider>);
  ` } });
  expect(bundle.success).toBe(true);
  const requests = new Map(["A", "B"].map((input) => [input, {
    started: Promise.withResolvers<void>(), response: Promise.withResolvers<Response>(),
  }]));
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(request) {
    const pathname = new URL(request.url).pathname;
    const pending = requests.get(pathname.slice(1));
    if (pending) { pending.started.resolve(); return pending.response.promise; }
    if (pathname === "/harness.js") return new Response(bundle.outputs[0], { headers: { "Content-Type": "text/javascript" } });
    return new Response('<div id="root"></div><script type="module" src="/harness.js"></script>', { headers: { "Content-Type": "text/html" } });
  } });
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage();
    await page.goto(`http://127.0.0.1:${server.port}/`);
    await page.locator("output").waitFor();
    await page.evaluate(() => Reflect.get(window, "start")("A"));
    await requests.get("A")!.started.promise;
    await page.evaluate(() => Reflect.get(window, "start")("B"));
    await requests.get("B")!.started.promise;
    requests.get("B")!.response.resolve(new Response("B result"));
    expect(await page.evaluate(() => Reflect.get(window, "resultB")())).toEqual({ value: "B result" });
    const latest = JSON.stringify({ variables: "B", pending: false, error: null });
    await page.waitForFunction((text: string) => document.querySelector("output")!.textContent === text, latest);
    requests.get("A")!.response.resolve(new Response("A failed", { status: 500 }));
    const results = await page.evaluate(() => Reflect.get(window, "results")());
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    expect(results).toEqual({ A: { error: "A failed" }, B: { value: "B result" }, successes: [{ value: "B result", input: "B" }], aborted: [] });
    expect(await page.locator("output").textContent()).toBe(latest);
  } finally {
    for (const request of requests.values()) request.response.resolve(new Response("cleanup"));
    await browser.close();
    server.stop(true);
  }
}, 30_000);
