import { expect, test } from "bun:test";
import path from "node:path";
import type { TranscriptEvent } from "./types";

// OpenCode V2 packages/app/src/session/revert.ts and
// packages/session-ui/src/message/message-content.tsx: message action, Undo/Redo.
for (const [width, height] of [[1280, 900], [390, 664], [320, 568]]) test(`revert and owned local drafts at ${width}px`, async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
  const messages: TranscriptEvent[] = ["First request", "First answer", "Second request", "Second answer"].map((text, index) => ({ messageId: `9007199254741001:${index}`, entryKey: "saved", itemId: `item:${index}`, inputId: "", turnId: "turn", role: index % 2 ? "assistant" : "user", text, complete: true, header: index % 2 ? "" : "[Web principal=\"original\"]" }));
  let marker = "", username = "alice", workspace = "first", rejected = false, rejectedConfig = false;
  const prompts: object[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: "chat", name: "Chat", agent: "main" }], owner: username, upstreamSuccess: true, summariesComplete: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = await request.json() as { id: string; messageId?: string; text?: string };
    switch (url.pathname) {
      case "/api/Identity": return rejected ? Response.json({ message: "Identity rejected", code: 16 }, { status: 401 }) : Response.json({ username, principal: "Shared display name" });
      case "/api/Protocol": return Response.json({ protoSha256: "revert" });
      case "/api/ListConfig": return rejectedConfig ? Response.json({ message: "Config rejected", code: 16 }, { status: 401 }) : Response.json({ config: { workspace } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListWorkflows": return Response.json({ workflows: [] });
      case "/api/ListQueue": return Response.json({ items: marker ? [] : [{ id: "pending", text: "Waiting request", delivery: "STASH" }] });
      case "/api/History": {
        const index = marker ? messages.findIndex((item) => item.messageId === marker) : messages.length;
        const visible = messages.slice(0, index);
        return Response.json({ messages: visible, origin: "", delegations: [], revision: marker || "normal", reset: true, replacedKeys: [], removedKeys: [], entryKeys: visible.length ? ["saved"] : [], running: false, terminal: "", start: "0", more: false, revertEligible: true, revertMessageId: marker, canUndo: index > 0 });
      }
      case "/api/StageRevert": {
        const index = input.messageId ? messages.findIndex((item) => item.messageId === input.messageId) : (marker ? messages.findIndex((item) => item.messageId === marker) : messages.length) - 2;
        if (index < 0) return Response.json({ revertMessageId: marker });
        marker = messages[index].messageId!;
        return Response.json({ revertMessageId: marker, prompt: messages[index] });
      }
      case "/api/ClearRevert": marker = ""; return Response.json({});
      case "/api/Prompt": prompts.push(input); return Response.json({ privateText: "" });
      default: return Response.json({});
    }
  } });
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width, height }, isMobile: width < 640, hasTouch: width < 640 });
    const errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat")}`);
    const input = page.locator("textarea:visible");
    await input.fill("Existing draft");
    const selected = page.locator('[data-message-id="9007199254741001:2"]');
    if (width < 640) await selected.getByText("Second request", { exact: true }).tap();
    else await selected.hover();
    const action = selected.getByRole("button", { name: "Revert message", exact: true });
    if (width >= 640) { await selected.focus(); await action.focus(); await action.press("Enter"); }
    else await action.click();
    await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "Second request");
    expect(await input.evaluate((node: HTMLTextAreaElement) => node === document.activeElement && node.selectionStart === node.value.length && node.selectionEnd === node.value.length)).toBe(true);
    // Composer restoration precedes the queue and history refreshes.
    await page.getByText("Second answer", { exact: true }).waitFor({ state: "detached" });
    await page.getByText("Waiting request", { exact: true }).waitFor({ state: "detached" });
    expect(await page.getByText("Second answer", { exact: true }).count()).toBe(0);
    expect(await page.getByText("Waiting request", { exact: true }).count()).toBe(0);
    expect(prompts).toHaveLength(0);
    await input.fill("Edited replacement");
    await page.getByRole("button", { name: "Add files", exact: true }).click();
    await page.locator('main > :visible input[type="file"]').setInputFiles({ name: "draft.txt", mimeType: "text/plain", buffer: Buffer.from("private bytes") });
    // Read the persisted bytes, not a timer; the next reload must restore File objects.
    await page.waitForFunction(async () => {
      const db = await new Promise<IDBDatabase>((resolve) => { const open = indexedDB.open("rocketclaw-drafts", 1); open.onsuccess = () => resolve(open.result); });
      const rows = await new Promise<{ text: string; files: { file: File }[] }[]>((resolve) => { const read = db.transaction("content").objectStore("content").getAll(); read.onsuccess = () => resolve(read.result); });
      db.close();
      return rows.some((row) => row.text === "Edited replacement" && row.files[0]?.file instanceof File);
    });
    await page.reload();
    await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "Edited replacement");
    await page.getByText("draft.txt", { exact: true }).waitFor();
    await page.getByRole("button", { name: "Redo", exact: true }).click();
    await page.getByText("Second answer", { exact: true }).waitFor();
    expect(await input.inputValue()).toBe("Edited replacement");
    await input.fill("$undo"); await input.press("Enter");
    await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "Second request");
    expect(await page.getByText("draft.txt", { exact: true }).count()).toBe(0);
    await input.fill("$undo"); await input.press("Enter");
    await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "First request");
    await page.waitForFunction(() => !document.querySelector("[data-message-id]"));
    expect(await page.locator("[data-message-id]").count()).toBe(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: path.resolve(import.meta.dir, `../../../../.tmp/rocketclaw/revert-message/revert-${width}.png`) });
    // The previous display-principal key format cannot establish login ownership.
    await page.evaluate(async () => {
      const db = await new Promise<IDBDatabase>((resolve) => { const open = indexedDB.open("rocketclaw-drafts", 1); open.onsuccess = () => resolve(open.result); });
      await new Promise<void>((resolve) => {
        const tx = db.transaction("content", "readwrite");
        tx.objectStore("content").put({ text: "Another person's legacy draft", files: [{ id: "legacy", file: new File(["legacy private bytes"], "legacy.txt") }], agent: "main" }, JSON.stringify([JSON.stringify([location.origin, "bob", "first"]), "chat"]));
        tx.oncomplete = () => resolve();
      });
      db.close();
    });
    username = "bob";
    await page.reload(); await input.waitFor();
    await page.waitForFunction(() => ![...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.disabled && ![...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.closest("fieldset")?.disabled);
    expect(await input.inputValue()).toBe("");
    expect(await page.getByText("legacy.txt", { exact: true }).count()).toBe(0);
    username = "alice";
    await page.reload(); await input.waitFor();
    await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "First request");
    // A failed polling refresh retains Query data, but must revoke local draft scope.
    rejected = true;
    await page.getByText("Identity rejected", { exact: true }).first().waitFor();
    expect(await input.inputValue()).toBe("");
    expect(await input.isEditable()).toBe(false);
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("other")}`);
    expect(await input.inputValue()).toBe("");
    expect(await input.isEditable()).toBe(false);
    rejected = false;
    await page.goto(`http://127.0.0.1:${server.port}/s/${btoa("chat")}`);
    await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "First request");
    rejectedConfig = true;
    await page.reload();
    await page.getByRole("alert").filter({ hasText: "Config rejected" }).first().waitFor();
    expect(await input.inputValue()).toBe("");
    expect(await input.isEditable()).toBe(false);
    rejectedConfig = false; workspace = "second";
    await page.reload(); await input.waitFor();
    await page.waitForFunction(() => ![...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.closest("fieldset")?.disabled);
    expect(await input.inputValue()).toBe("");
    await input.fill("Keep named draft");
    // New session must clear only the persisted empty-session composer before remount.
    await page.goto(`http://127.0.0.1:${server.port}/`);
    for (const outcome of ["reset", "failure", "navigate", "scope"]) {
      const storageFailure = outcome === "failure" || outcome === "scope";
      if (outcome === "navigate") {
        await page.evaluate(() => { history.pushState({}, "", `/s/${btoa("chat")}`); dispatchEvent(new PopStateEvent("popstate")); });
        await input.fill("Keep named draft");
        await page.evaluate(() => { history.pushState({}, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
      }
      const scratch = outcome === "reset" ? "$go" : "Discarded scratch";
      await input.fill(scratch);
      await page.getByRole("button", { name: "Add files", exact: true }).click();
      await page.locator('main > :visible input[type="file"]').setInputFiles({ name: "scratch.txt", mimeType: "text/plain", buffer: Buffer.from("scratch bytes") });
      await page.waitForFunction(async (text: string) => {
        const db = await new Promise<IDBDatabase>((resolve) => { const open = indexedDB.open("rocketclaw-drafts", 1); open.onsuccess = () => resolve(open.result); });
        const rows = await new Promise<{ text: string; files: { file: File }[] }[]>((resolve) => { const read = db.transaction("content").objectStore("content").getAll(); read.onsuccess = () => resolve(read.result); });
        db.close();
        return rows.some((row) => row.text === text && row.files[0]?.file.name === "scratch.txt");
      }, scratch);
      await page.evaluate((fail: boolean) => {
        const control = window as unknown as { releaseClear?: () => void; clearWrites: number };
        control.clearWrites = 0;
        const open = indexedDB.open.bind(indexedDB), put = IDBObjectStore.prototype.put;
        indexedDB.open = (...args) => {
          indexedDB.open = open;
          const request = open(...args);
          // Delay the application's open callback, then use the actual IndexedDB transaction.
          Object.defineProperty(request, "onsuccess", { set(callback) {
            request.addEventListener("success", (event) => { control.releaseClear = () => callback.call(request, event); });
          } });
          return request;
        };
        IDBObjectStore.prototype.put = function(value, key) {
          if (value.text === "" && !value.files.length) {
            control.clearWrites++;
            if (fail) { IDBObjectStore.prototype.put = put; throw new DOMException("storage unavailable", "QuotaExceededError"); }
          }
          return put.call(this, value, key);
        };
      }, storageFailure);
      await page.keyboard.press("Control+Alt+n");
      await page.waitForFunction(() => !!(window as unknown as { releaseClear?: () => void }).releaseClear);
      await page.keyboard.press("Control+Alt+n"); // A duplicate request must not enqueue another reset.
      await input.focus();
      await page.keyboard.type(" typed while clearing");
      // Use actual pointer clicks: disabled controls must not dispatch draft edits.
      const buttons = ["Add files", "Remove scratch.txt", "Stash", "Steer", "Send"];
      if (outcome === "reset") buttons.unshift("$goal <objective> Start a goal loop");
      for (const name of buttons) {
        const button = page.getByRole("button", { name, exact: true });
        expect(await button.isDisabled()).toBe(true);
        const bounds = (await button.boundingBox())!;
        await page.mouse.click(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
      }
      const agent = page.getByRole("combobox", { name: "Choose agent" });
      expect(await agent.isDisabled()).toBe(true);
      const bounds = (await agent.boundingBox())!;
      await page.mouse.click(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
      expect(await page.getByRole("listbox").count()).toBe(0);
      expect(await page.locator("main > :visible").getByLabel("Attach files").isDisabled()).toBe(true);
      await input.evaluate((node: HTMLTextAreaElement) => {
        const dataTransfer = new DataTransfer();
        dataTransfer.items.add(new File(["late bytes"], "late.txt"));
        node.dispatchEvent(new DragEvent("drop", { bubbles: true, dataTransfer }));
      });
      expect(await page.getByText("late.txt", { exact: true }).count()).toBe(0);
      expect(await page.getByText("scratch.txt", { exact: true }).count()).toBe(1);
      expect(await input.inputValue()).toBe(scratch);
      expect(await input.isEditable()).toBe(false);
      if (outcome === "navigate") {
        await page.evaluate(() => { history.pushState({}, "", `/s/${btoa("chat")}`); dispatchEvent(new PopStateEvent("popstate")); });
        await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "Keep named draft");
        await input.focus();
        await input.evaluate((node: HTMLTextAreaElement) => node.setSelectionRange(node.value.length, node.value.length));
        await page.keyboard.type(" + current edit");
      } else if (outcome === "scope") {
        username = "bob";
        await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "");
      }
      await page.evaluate(() => (window as unknown as { releaseClear: () => void }).releaseClear());
      if (outcome === "navigate") {
        expect(new URL(page.url()).pathname).toBe(`/s/${btoa("chat")}`);
        expect(await input.inputValue()).toBe("Keep named draft + current edit");
        await page.evaluate(() => { history.pushState({}, "", "/"); dispatchEvent(new PopStateEvent("popstate")); });
      } else if (outcome === "scope") {
        await page.waitForFunction(() => ![...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.closest("fieldset")?.disabled);
        expect(await input.inputValue()).toBe("");
        expect(await page.getByRole("alert").filter({ hasText: "Local draft could not be cleared" }).count()).toBe(0);
        username = "alice";
        await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "Discarded scratch");
        expect(await input.isEditable()).toBe(true);
        await page.keyboard.press("Control+Alt+n");
      } else if (storageFailure) {
        await page.getByRole("alert").filter({ hasText: "Local draft could not be cleared" }).waitFor();
        expect(await input.inputValue()).toBe("Discarded scratch");
        expect(await input.isEditable()).toBe(true);
        await page.getByText("scratch.txt", { exact: true }).waitFor();
        for (const name of ["Add files", "Remove scratch.txt", "Stash", "Send"]) expect(await page.getByRole("button", { name, exact: true }).isEnabled()).toBe(true);
        expect(await agent.isEnabled()).toBe(true);
        expect(await page.locator("main > :visible").getByLabel("Attach files").isEnabled()).toBe(true);
        await input.fill("Recovered scratch");
        await page.keyboard.press("Control+Alt+n");
      }
      await page.waitForFunction(() => [...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.value === "" && ![...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.closest("fieldset")?.disabled);
      // One reset plus the new composer's ordinary hydration save, not two resets.
      if (outcome === "reset") {
        await page.waitForFunction(() => (window as unknown as { clearWrites: number }).clearWrites >= 2);
        expect(await page.evaluate(() => (window as unknown as { clearWrites: number }).clearWrites)).toBe(2);
      }
      expect(await page.getByText("scratch.txt", { exact: true }).count()).toBe(0);
      await page.reload();
      await page.waitForFunction(() => ![...document.querySelectorAll("textarea")].find((node) => node.checkVisibility())?.closest("fieldset")?.disabled);
      expect(await input.inputValue()).toBe("");
      expect(await page.getByText("scratch.txt", { exact: true }).count()).toBe(0);
    }
    expect(errors).toEqual([]);
  } finally { await browser.close(); server.stop(true); }
}, 60_000);
