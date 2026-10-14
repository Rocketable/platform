import { expect, test } from "bun:test";

// Go's TestReloadRealBackendBrowser owns the runtime and PostgreSQL fixture.
test("authenticated browser reloads runtime assets without a conversation", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage();
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(process.env.ROCKETCLAW_RELOAD_TEST_URL!);
    await page.getByRole("textbox").waitFor();
    const result = await page.evaluate(async () => {
      const response = await fetch("/api/Reload", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: "updated assets" }),
      });
      return { status: response.status, body: await response.json() };
    });
    expect(result).toEqual({ status: 200, body: { message: "rocketclaw runtime assets reloaded" } });
    await page.reload();
    await page.getByRole("textbox").waitFor();
    expect(await page.getByRole("button", { name: "Reload", exact: true }).count()).toBe(0);
    expect(errors).toEqual([]);
  } finally { await browser.close(); }
}, 60_000);
