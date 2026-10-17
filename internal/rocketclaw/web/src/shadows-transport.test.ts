import { expect, test } from "bun:test";
import type { HistoryView } from "./types";

// Go's TestShadowsRealBackendBrowser owns the real backend, provider and store.
test("real child-only changes refresh paired shadow turns through the parent stream", async () => {
  const url = process.env.ROCKETCLAW_SHADOW_TEST_URL!;
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors: string[] = [], streams: string[] = [], reads: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    page.on("request", (request: { url(): string; postDataJSON(): { id: string } }) => {
      const requestURL = new URL(request.url());
      if (requestURL.pathname === "/stream") streams.push(requestURL.searchParams.get("id")!);
      if (requestURL.pathname === "/api/History") reads.push(request.postDataJSON().id);
    });
    await page.goto(`${url}/s/${btoa("parent")}`);
    const a = page.getByRole("tab", { name: "main/a", exact: true });
    const b = page.getByRole("tab", { name: "main/b", exact: true });
    for (const turn of [1, 2]) {
      await page.locator("textarea").fill("Repeated prompt");
      await page.getByRole("button", { name: "Send", exact: true }).click();
      await page.locator("#transcript-scroll").getByText(`gpt-parent answer ${turn}`, { exact: false }).first().waitFor();
      await page.getByRole("button", { name: "Send", exact: true }).waitFor();
    }
    await a.waitFor(); await b.waitFor();
    expect(await a.getAttribute("aria-selected")).toBe("true");
    await page.locator('[data-shadow-cell] [role="status"]').filter({ hasText: "Working…" }).waitFor();
    const rows = page.locator("#transcript-scroll [data-comparison-row]");
    expect(await rows.count()).toBe(2);
    const before = await (await page.request.post(`${url}/api/History`, { data: { id: "parent" } })).json() as HistoryView;
    expect(before.shadows).toHaveLength(2);
    const bindingA = before.shadows!.find((binding) => binding.shadowAgent === "main/a")!;
    const bindingB = before.shadows!.find((binding) => binding.shadowAgent === "main/b")!;
    expect(bindingA.childConversationId).not.toBe(bindingB.childConversationId);
    const turnIDs = bindingA.turns.map((turn) => turn.parentTurnId);
    expect(new Set(turnIDs).size).toBe(2);
    expect(bindingB.turns.map((turn) => turn.parentTurnId)).toEqual(turnIDs);
    const rowKeys = await page.locator("#transcript-scroll [data-turn-key]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.dataset.turnKey));
    expect(rowKeys).toEqual(turnIDs.map((id) => before.messages.find((message) => message.turnId === id && message.role === "user")!.inputId));
    const parentText = await rows.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => (node.children[0] as HTMLElement).innerText));
    expect(parentText.every((text: string) => !text.includes("gpt-shadow"))).toBe(true);
    expect((await page.locator("[data-shadow-cell]").allTextContents()).join("\n")).not.toContain("answer");
    // Inactive B has no browser reads. A held child does not create a polling
    // loop during this bounded observation window either.
    const heldReads = reads.length;
    await page.waitForTimeout(300);
    expect(reads.length).toBe(heldReads);
    expect(reads).not.toContain(bindingB.childConversationId);
    expect((await fetch(`${url}/test/release/a`, { method: "POST" })).ok).toBe(true);
    await page.locator("[data-shadow-cell]").getByText("gpt-shadow-a answer 2", { exact: false }).first().waitFor();
    await page.waitForFunction(() => [...document.querySelectorAll('[data-shadow-cell] [role="status"]')].filter((node) => node.textContent === "Completed.").length === 2);
    const after = await (await page.request.post(`${url}/api/History`, { data: { id: "parent" } })).json() as HistoryView;
    expect(after.revision).not.toBe(before.revision);
    expect(await rows.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => (node.children[0] as HTMLElement).innerText))).toEqual(parentText);
    expect(after.shadows!.find((binding) => binding.shadowAgent === "main/a")!.turns.every((turn) => turn.terminal === "complete" && !turn.running && !!turn.childTurnId)).toBe(true);
    const alignment = async () => {
      const differences = await rows.evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => {
        const parentHeader = node.children[0].querySelector("p")!;
        const childHeader = node.children[1].querySelector("p")!;
        return Math.abs(parentHeader.getBoundingClientRect().top - childHeader.getBoundingClientRect().top);
      }));
      expect(differences).toHaveLength(2);
      expect(differences.every((difference: number) => difference <= 1)).toBe(true);
    };
    await alignment();
    await a.focus(); await a.press("ArrowRight");
    expect(await b.getAttribute("aria-selected")).toBe("true");
    expect(new URL(page.url()).searchParams.get("shadow")).toBe(bindingB.childConversationId);
    await page.locator('[data-shadow-cell] [role="status"]').filter({ hasText: "Working…" }).waitFor();
    expect((await page.locator("[data-shadow-cell]").allTextContents()).join("\n")).not.toContain("gpt-shadow-a");
    expect((await fetch(`${url}/test/release/b`, { method: "POST" })).ok).toBe(true);
    await page.locator("[data-shadow-cell]").getByText("gpt-shadow-b answer 2", { exact: false }).first().waitFor();
    await page.waitForFunction(() => [...document.querySelectorAll('[data-shadow-cell] [role="status"]')].filter((node) => node.textContent === "Completed.").length === 2);
    expect(await page.locator("#transcript-scroll [data-turn-key]").evaluateAll((nodes: HTMLElement[]) => nodes.map((node) => node.dataset.turnKey))).toEqual(rowKeys);
    await alignment();
    const denied = await page.request.post(`${url}/api/Prompt`, { data: { id: bindingB.childConversationId, text: "Direct child prompt", messageId: "forbidden" } });
    expect(denied.status()).toBe(403);
    expect(await page.locator("[data-shadow-cell]").getByRole("button", { name: "Revert message", includeHidden: true }).evaluateAll((nodes: Element[]) => nodes.length > 0 && nodes.every((node) => (node as HTMLButtonElement).disabled))).toBe(true);
    expect(streams).toEqual(["parent"]);
    expect(errors).toEqual([]);
    console.log("Witnessed two completed shadow histories, durable paired rows, child-only refresh, one parent stream, and HTTP child-write denial.");
  } finally {
    await fetch(`${url}/test/release/a`, { method: "POST" });
    await fetch(`${url}/test/release/b`, { method: "POST" });
    await browser.close();
  }
}, 60_000);
