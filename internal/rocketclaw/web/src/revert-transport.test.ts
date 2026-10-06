import { expect, test } from "bun:test";

// Go's TestRevertRealBackendBrowser owns the live PostgreSQL/provider fixture.
test("real HTTP Revert commits only retained context and converges across viewers", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const first = await context.newPage(), second = await context.newPage();
    const url = process.env.ROCKETCLAW_REVERT_TEST_URL!;
    const errors: string[] = [];
    for (const page of [first, second]) page.on("pageerror", (error: Error) => errors.push(error.message));
    await Promise.all([first.goto(`${url}/s/${btoa("chat")}`), second.goto(`${url}/s/${btoa("chat")}`)]);
    await second.locator("textarea").fill("Other viewer draft");
    await first.getByRole("button", { name: "Add files", exact: true }).click();
    await first.locator('input[type="file"]').setInputFiles({ name: "old-draft.txt", mimeType: "text/plain", buffer: Buffer.from("replace me") });
    const selected = first.locator('[data-slot="message"][data-message-id]').filter({ has: first.getByText("abandoned request", { exact: true }) });
    await selected.hover();
    const staged = first.waitForResponse((response: { url: () => string }) => response.url().endsWith("/api/StageRevert"));
    await selected.getByRole("button", { name: "Revert message", exact: true }).click();
    const result = await staged;
    expect(await result.json()).toMatchObject({ prompt: { text: "abandoned request" } });
    await first.waitForFunction(() => document.querySelector("textarea")?.value === "abandoned request");
    await first.getByText("boundary-upload.txt", { exact: true }).last().waitFor();
    expect(await first.getByText("old-draft.txt", { exact: true }).count()).toBe(0);
    // Download through the real authenticated route after staging, not an inline File stub.
    const boundary = await context.request.get(`${url}/api/DownloadAttachment?conversationId=chat&id=boundary-upload`);
    expect(boundary.ok()).toBe(true);
    expect(await boundary.text()).toBe("boundary-upload bytes");
    const hidden = await context.request.get(`${url}/api/DownloadAttachment?conversationId=chat&id=hidden-upload`);
    expect(hidden.status()).toBe(404);
    await second.getByRole("button", { name: "Redo", exact: true }).waitFor();
    expect(await second.locator("textarea").inputValue()).toBe("Other viewer draft");
    expect(await second.locator('[data-slot="message"]').getByText("abandoned answer", { exact: true }).count()).toBe(0);
    await first.locator("textarea").fill("$undo"); await first.locator("textarea").press("Enter");
    await first.waitForFunction(() => document.querySelector("textarea")?.value === "retained request");
    await first.getByText("retained-upload.txt", { exact: true }).waitFor();
    expect(await first.getByText("boundary-upload.txt", { exact: true }).count()).toBe(0);
    const undone = await context.request.get(`${url}/api/DownloadAttachment?conversationId=chat&id=retained-upload`);
    expect(undone.ok()).toBe(true);
    expect(await undone.text()).toBe("retained-upload bytes");
    expect((await context.request.get(`${url}/api/DownloadAttachment?conversationId=chat&id=boundary-upload`)).status()).toBe(404);
    await first.getByRole("button", { name: "Redo", exact: true }).click();
    await selected.hover(); await selected.getByRole("button", { name: "Revert message", exact: true }).click();
    await first.waitForFunction(() => document.querySelector("textarea")?.value === "abandoned request");
    await first.locator("textarea").fill("edited replacement");
    await first.getByRole("button", { name: "Send", exact: true }).click();
    await first.locator('[data-slot="message"]').getByText("branch reply", { exact: true }).waitFor();
    await second.locator('[data-slot="message"]').getByText("branch reply", { exact: true }).waitFor();
    expect(await first.getByRole("button", { name: "Redo", exact: true }).count()).toBe(0);
    await first.reload();
    await first.locator('[data-slot="message"]').getByText("branch reply", { exact: true }).waitFor();
    expect(await first.locator("textarea").inputValue()).toBe("");
    expect(await second.locator("textarea").inputValue()).toBe("Other viewer draft");
    expect(errors).toEqual([]);
  } finally { await browser.close(); }
}, 60_000);
