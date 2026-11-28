import { expect, test } from "bun:test";

test("a page loads its own files but its script cannot read the API as the viewer, opened directly or inside its tab", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  const origin = process.env.ROCKETCLAW_PAGES_TEST_URL!;
  try {
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`${origin}/pages-raw/Main/probe.html`);
    await page.waitForFunction(() => (window as any).result);
    expect(await page.evaluate(() => (window as any).result)).toBe("TypeError");
    expect(await page.evaluate(() => (window as any).asset)).toBe("loaded");

    await page.goto(`${origin}/pages/Main/probe.html`);
    const frame = await (await page.locator("iframe").elementHandle()).contentFrame();
    await frame.waitForFunction(() => (window as any).result);
    expect(await frame.evaluate(() => (window as any).result)).toBe("TypeError");
    expect(await frame.evaluate(() => (window as any).asset)).toBe("loaded");
    expect(await page.evaluate(() => fetch("/api/ListSessions").then((response) => response.ok))).toBe(true);
    expect(errors).toEqual([]);
  } finally { await browser.close(); }
}, 60_000);
