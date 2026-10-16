// Every bun test run preloads this (bunfig.toml). It closes each browser a test launched once the test
// ends, timed out or not: bun skips a timed-out test's finally blocks, and a browser left open outlives bun.
import { afterEach } from "bun:test";

const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
if (playwright) {
  const { chromium } = await import(playwright);
  const launch = chromium.launch.bind(chromium);
  const open = new Set<{ close(): Promise<void> }>();
  chromium.launch = async (...args: unknown[]) => {
    const browser = await launch(...args);
    open.add(browser);
    return browser;
  };
  afterEach(async () => {
    await Promise.all([...open].map((browser) => browser.close()));
    open.clear();
  });
}
