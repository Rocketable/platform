import { expect, test } from "bun:test";
import { once } from "node:events";

// TestSlackExecutionLiveReplay supplies the real bridge, provider, store and HTTP
// server. Stdout barriers let Go submit Slack inputs only after Web observes each
// required boundary; no requests in this test synthesize transcript events.
test.skipIf(process.env.ROCKETCLAW_SLACK_BROWSER !== "1")("real Slack execution is readable before reload and survives reconnect", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true, args: ["--no-sandbox"] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  page.setDefaultTimeout(10000);
  const errors: string[] = [];
  page.on("pageerror", (error: Error) => errors.push(error.message));
  try {
    const url = `${process.env.ROCKETCLAW_TEST_HTTP_URL}/s/${Buffer.from(process.env.ROCKETCLAW_LIVE_TEST_ID!).toString("base64url")}`;
    const opening = page.waitForResponse((response: { url(): string; status(): number }) => response.url().includes("/stream?") && response.status() === 200);
    await page.goto(url);
    await page.locator("textarea").waitFor();
    await opening;
    console.log("browser-ready");

    await page.locator("main").getByText("Before tools", { exact: true }).waitFor();
    await page.locator("main").getByText("Checking files", { exact: true }).waitFor();
    await page.locator("main").getByText("Available thinking", { exact: true }).waitFor();
    await page.locator("main pre").filter({ hasText: "/first" }).waitFor();
    await page.locator("main pre").filter({ hasText: "/second" }).waitFor();
    expect(await page.locator("main pre").filter({ hasText: "def main()" }).count()).toBe(2);
    let text = await page.locator("main").innerText();
    expect(text.indexOf("Slack idle input")).toBeLessThan(text.indexOf("Before tools"));
    expect(text.indexOf("Checking files")).toBeLessThan(text.indexOf("Before tools"));
    expect(text.indexOf("Before tools")).toBeLessThan(text.indexOf("def main()"));
    expect(text).not.toContain("same Slack reply");

    await page.reload();
    await page.locator("main").getByText("Before tools", { exact: true }).waitFor();
    await page.locator("main pre").filter({ hasText: "/first" }).waitFor();
    await page.locator("main pre").filter({ hasText: "/second" }).waitFor();
    expect(await page.locator("main pre").filter({ hasText: "def main()" }).count()).toBe(2);
    console.log("calls-restored");

    await page.locator("main").getByText("After consumed Slack replies", { exact: true }).waitFor();
    await page.waitForFunction(() => (document.querySelector("main")?.innerText.match(/same Slack reply/g) ?? []).length === 2);
    await page.getByText("Thinking…", { exact: true }).waitFor({ state: "hidden" });
    expect(await page.locator("main").getByText("Checking files", { exact: true }).count()).toBe(1);
    expect(await page.locator("main").getByText("Before tools\nAfter consumed Slack replies", { exact: true }).count()).toBe(0);
    text = await page.locator("main").innerText();
    for (const detail of ["complete result /first", "complete result /second", "Full loaded skill instructions."]) expect(text).toContain(detail);
    expect(text.indexOf("complete result /second")).toBeLessThan(text.indexOf("same Slack reply"));
    expect(text.lastIndexOf("same Slack reply")).toBeLessThan(text.indexOf("After consumed Slack replies"));
    const live = await page.getByRole("region", { name: /^Turn \d+$/ }).allTextContents();
    await page.reload();
    await page.locator("main").getByText("After consumed Slack replies", { exact: true }).waitFor();
    expect(await page.getByRole("region", { name: /^Turn \d+$/ }).allTextContents()).toEqual(live);
    console.log("settled-equals-reload");

    await page.locator("main").getByText("Silent Slack input", { exact: true }).waitFor();
    await page.getByText("Thinking…", { exact: true }).waitFor({ state: "hidden" });
    expect(await page.locator("main").getByText("After consumed Slack replies", { exact: true }).count()).toBe(1);
    console.log("silent-idle");
    await page.locator("main").getByText("Interrupt Slack input", { exact: true }).waitFor();
    await page.getByText("Thinking…", { exact: true }).waitFor({ state: "hidden" });
    console.log("interrupt-idle");
    await page.locator("main").getByText("Offline Slack input", { exact: true }).waitFor();
    await page.getByText("Thinking…", { exact: true }).waitFor();
    // Leaving the chat closes its native EventSource but keeps its draft in
    // App. Browser offline mode alone does not close an existing SSE socket.
    await page.getByRole("button", { name: "New session", exact: true }).click();
    await page.waitForURL(`${process.env.ROCKETCLAW_TEST_HTTP_URL}/`);
    const reconnect = once(process.stdin, "data");
    console.log("disconnected");
    await reconnect;
    expect(await page.locator("main").getByText("Completed while offline", { exact: true }).count()).toBe(0);
    await page.goBack();
    await page.locator("main").getByText("Completed while offline", { exact: true }).waitFor();
    await page.getByText("Thinking…", { exact: true }).waitFor({ state: "hidden" });
    const privateFinished = once(process.stdin, "data");
    console.log("reconnected");
    await privateFinished;
    expect(await page.locator("main").getByText("Private readable thinking", { exact: true }).count()).toBe(0);
    expect(await page.locator("main").getByText("Private producer input", { exact: false }).count()).toBe(0);
    console.log("private-hidden");
    await page.locator("main").getByText("Private readable thinking", { exact: true }).waitFor();
    const sources = page.getByRole("group", { name: "Show messages from" });
    await sources.getByRole("button", { name: "sandboxed", exact: true }).click();
    expect(await page.locator("main").getByText("Private readable thinking", { exact: true }).count()).toBe(0);
    expect(await page.locator("main").getByText("Completed while offline", { exact: true }).count()).toBe(1);
    await sources.getByRole("button", { name: "sandboxed", exact: true }).click();
    await sources.getByRole("button", { name: "canonical", exact: true }).click();
    expect(await page.locator("main").getByText("Private readable thinking", { exact: true }).count()).toBe(1);
    expect(await page.locator("main").getByText("Completed while offline", { exact: true }).count()).toBe(0);
    expect(await page.locator("main").innerText()).toContain("Full loaded skill instructions.");
    await sources.getByRole("button", { name: "canonical", exact: true }).click();
    const syncedLive = await page.getByRole("region", { name: /^Turn \d+$/ }).allTextContents();
    await page.screenshot({ path: `${process.env.TMPDIR}/slack-live-synced.png`, fullPage: true });
    await page.reload();
    await page.locator("main").getByText("Private readable thinking", { exact: true }).waitFor();
    expect(await page.getByRole("region", { name: /^Turn \d+$/ }).allTextContents()).toEqual(syncedLive);
    expect(errors).toEqual([]);
  } catch (error) {
    console.error(await page.locator("main").innerText());
    await page.screenshot({ path: `${process.env.TMPDIR}/slack-live-failure.png`, fullPage: true });
    throw error;
  } finally {
    await browser.close();
  }
}, 30000);
