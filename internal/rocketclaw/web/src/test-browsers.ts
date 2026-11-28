import { afterEach, setDefaultTimeout } from "bun:test";
import { readFileSync } from "node:fs";

setDefaultTimeout(30_000);

globalThis.location ??= new URL("http://localhost/") as unknown as Location;

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
if (playwright) {
  const { chromium } = await import(playwright);
  const launch = chromium.launch.bind(chromium);
  const open = new Set<{ close(): Promise<void> }>();
  chromium.launch = async (...args: unknown[]) => {
    const browser = await launch(...args);
    open.add(browser);
    const newContext = browser.newContext.bind(browser);
    browser.newContext = async (...options: unknown[]) => {
      const context = await newContext(...options);
      await context.addInitScript(() => { if (window.origin !== "null" && localStorage.getItem("sidebar-view") === null) localStorage.setItem("sidebar-view", "spaces"); });
      return context;
    };
    return browser;
  };
  afterEach(async () => {
    await Promise.all([...open].map((browser) => browser.close()));
    open.clear();
  });
}

export function unpacked(file: string) {
  return new Response(Bun.gunzipSync(readFileSync(`${file}.gz`)), { headers: { "Content-Type": Bun.file(file).type } });
}
