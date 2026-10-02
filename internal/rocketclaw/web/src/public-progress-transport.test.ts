import { expect, test } from "bun:test";

const url = process.env.ROCKETCLAW_TEST_HTTP_URL;
const id = process.env.ROCKETCLAW_PUBLIC_TEST_ID;
const final = process.env.ROCKETCLAW_PUBLIC_TEST_FINAL;
const playwright = process.env.ROCKETCLAW_PLAYWRIGHT_MODULE;
const chromium = process.env.ROCKETCLAW_CHROMIUM;

test.skipIf(!url || !id || !final || !playwright || !chromium)("real Go HTTP App shows persisted progress before provider completion", async () => {
  const { chromium: engine } = await import(playwright!);
  const browser = await engine.launch({ executablePath: chromium, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 375, height: 700 } });
    await page.goto(`${url}/s/${Buffer.from(id!).toString("base64url")}`);
    if (final === "calls") {
      const log = page.locator("#transcript-scroll");
      const summaries = log.locator("summary").filter({ hasText: /^task/ });
      await summaries.nth(2).waitFor();
      expect(await summaries.allTextContents()).toEqual(["taskworking", "taskworking", "taskworking"]);
      for (const call of ["B", "C"]) {
        expect((await fetch(`${url}/test/calls/${call}`, { method: "POST" })).ok).toBe(true);
        await summaries.nth(call === "B" ? 1 : 2).filter({ hasText: call === "B" ? "completed" : "blocked" }).waitFor();
        expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
      }
      expect(await summaries.allTextContents()).toEqual(["taskworking", "taskcompleted", "taskblocked"]);
      await page.reload();
      await summaries.nth(2).filter({ hasText: "blocked" }).waitFor();
      expect(await summaries.allTextContents()).toEqual(["taskworking", "taskcompleted", "taskblocked"]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      expect((await fetch(`${url}/test/calls/A`, { method: "POST" })).ok).toBe(true);
      await page.getByRole("button", { name: "Send", exact: true }).waitFor();
      expect(await summaries.allTextContents()).toEqual(["taskcompleted", "taskcompleted", "taskblocked"]);
      expect(await log.innerText()).not.toContain("PRIVATE_SENTINEL");
      await page.reload();
      await summaries.nth(2).filter({ hasText: "blocked" }).waitFor();
      expect(await log.innerText()).not.toContain("PRIVATE_SENTINEL");
      return;
    }
    await page.getByRole("textbox").fill("Owned public progress question");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    const log = page.locator("#transcript-scroll");
    await log.getByText("Held public partial suffix", { exact: true }).waitFor();
    expect(await page.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
    expect(await log.locator('[data-slot="message"][data-message-id]').count()).toBe(0);
    expect(await log.innerText()).not.toContain("PRIVATE_SENTINEL");
    expect(await log.locator("summary").count()).toBe(0);
    // Both reload and a new browser page fetch the same committed row, with no
    // terminal event and no test-built History responses or content-bearing hint.
    await page.reload();
    await log.getByText("Held public partial suffix", { exact: true }).waitFor();
    const reopened = await browser.newPage();
    await reopened.goto(page.url());
    await reopened.locator("#transcript-scroll").getByText("Held public partial suffix", { exact: true }).waitFor();
    expect(await reopened.getByRole("button", { name: "Stop", exact: true }).isEnabled()).toBe(true);
    if (final === "stop") {
      await page.getByRole("button", { name: "Stop", exact: true }).click();
    } else {
      const response = await fetch(`${url}/test/release`, { method: "POST" });
      expect(response.ok).toBe(true);
    }
    await page.getByRole("button", { name: "Send", exact: true }).waitFor();
    await reopened.getByRole("button", { name: "Send", exact: true }).waitFor();
    for (const view of [page, reopened]) {
      const transcript = view.locator("#transcript-scroll");
      if (final === "stop") {
        expect(await transcript.getByText("Held public partial suffix", { exact: true }).count()).toBe(1);
      } else {
        await transcript.getByText("Held public partial suffix", { exact: true }).waitFor({ state: "detached" });
        expect(await transcript.getByText("Short", { exact: true }).count()).toBe(final === "short" ? 1 : 0);
      }
      expect(await transcript.innerText()).not.toContain("PRIVATE_SENTINEL");
      expect(await view.getByRole("button", { name: "Stop", exact: true }).count()).toBe(0);
      await view.reload();
      await view.getByRole("button", { name: "Send", exact: true }).waitFor();
      if (final === "short") await transcript.getByText("Short", { exact: true }).waitFor();
      if (final === "stop") await transcript.getByText("Held public partial suffix", { exact: true }).waitFor();
      if (final === "empty") expect(await transcript.locator('[data-slot="bubble-content"]').filter({ hasText: "Held public partial suffix" }).count()).toBe(0);
    }
    await reopened.close();
  } finally {
    await fetch(`${url}/test/release`, { method: "POST" });
    await browser.close();
  }
}, 120_000);
