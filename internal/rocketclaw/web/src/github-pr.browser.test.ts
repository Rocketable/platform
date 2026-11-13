import { expect, test } from "bun:test";
import path from "node:path";
import postcss from "postcss";
import tailwind from "@tailwindcss/postcss";
import type { SavedTabs, TranscriptEvent } from "./types";

const href = "https://github.com/sample/fixture-repository-marker/pull/7/files";
const metadata = { title: "Synthetic title marker", body: "description-marker <img src=https://untrusted.invalid/image> ![image](https://untrusted.invalid/image)", state: "open", draft: true, merged: false, changed_files: 101, user: { login: "demo-author" }, updated_at: "2026-11-12T12:00:00Z", additions: 12, deletions: 3, head: { sha: "head", ref: "feature/pr-reader" }, base: { sha: "base", ref: "main" } };
const file = (n: number) => ({ filename: n === 100 ? "file-marker-z-last.txt" : `file-marker-${n}.txt`, status: n === 100 ? "renamed" : "modified", additions: 1, deletions: 0, ...(n === 100 ? { previous_filename: "previous-file-marker.txt", patch: "@@ -0,0 +1 @@\n+last-page-patch-marker" } : n ? {} : { patch: "@@ -0,0 +1 @@\n+patch-marker <script>example</script>" }) });

// Compile a focused browser entry in memory; never replace the caller-owned tracked bundle.
async function assets() {
  const entry = path.join(import.meta.dir, "github-pr-browser-entry.tsx");
  const result = await Bun.build({ entrypoints: [entry], target: "browser", minify: true, define: { "process.env.NODE_ENV": '"production"' }, plugins: [{ name: "preview-test-entry", setup(build) {
    build.onResolve({ filter: /github-pr-browser-entry\.tsx$/ }, () => ({ path: entry, namespace: "preview-test" }));
    build.onLoad({ filter: /.*/, namespace: "preview-test" }, () => ({ loader: "tsx", resolveDir: import.meta.dir, contents: `
      import React from 'react'; import {createRoot} from 'react-dom/client';
      import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
      import {TranscriptText, MarkdownText} from './transcript-text';
      import './sentry'; import './posthog'; import posthog from 'posthog-js';
      import {flush, getActiveSpan, getRootSpan} from '@sentry/react';
      window.telemetryEvents=[]; window.posthog=posthog;
      window.flushTelemetry=async()=>{const span=getActiveSpan();if(span)getRootSpan(span).end();await flush(5000)};
      posthog.set_config({opt_out_useragent_filter:true, disable_compression:true, request_batching:false, before_send:event=>{window.telemetryEvents.push(event);return event}});
      const root=createRoot(document.getElementById('root'));
      root.render(<QueryClientProvider client={new QueryClient()}><MarkdownText text={'[change](${href})'}/><TranscriptText text={'${href}'}/><textarea aria-label="Conversation draft" defaultValue="draft survives"/><button>recordable-positive-control</button></QueryClientProvider>);
      window.unmountPreview=()=>root.unmount();
    ` }));
  } }] });
  expect(result.success).toBe(true);
  const cssPath = path.resolve(import.meta.dir, "../app/globals.css");
  const css = await postcss([tailwind({ optimize: true })]).process(await Bun.file(cssPath).text(), { from: cssPath });
  return { js: await result.outputs[0].text(), css: css.css };
}

test("four real presentations, paging, cancellation and telemetry privacy with an active recorder", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const built = await assets();
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  const github: { url: string; headers: Record<string, string> }[] = [];
  const envelopes: string[] = [];
  const sentrySent = Promise.withResolvers<void>();
  const posthogPayloads: string[] = [];
  let privatePR = false, failPage = false, changed = false;
  let hold: "metadata" | "files" | "" = "";
  let release = Promise.withResolvers<void>();
  let pending = Promise.withResolvers<void>();
  try {
    const page = await browser.newPage();
    // Headless focus emulation keeps real tabs visible. Exercise the page-lifecycle
    // event directly so this regression does not depend on window-manager focus.
    const visibility = async (state: DocumentVisibilityState) => page.evaluate(async (state: DocumentVisibilityState) => {
      Object.defineProperty(document, "visibilityState", { configurable: true, value: state });
      Object.defineProperty(document, "hidden", { configurable: true, value: state === "hidden" });
      document.dispatchEvent(new Event("visibilitychange"));
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }, state);
    await page.setViewportSize({ width: 1280, height: 800 });
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Emulation.setUserAgentOverride", { userAgent: "Mozilla/5.0 AppleWebKit/537.36 Chrome/145.0.0.0 Safari/537.36" });
    const errors: string[] = [], unexpected: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.route("**/*", async (route: any) => {
      const request = route.request(), url = new URL(request.url());
      if (url.hostname === "api.github.com") {
        github.push({ url: request.url(), headers: request.headers() });
        const responseMetadata = { ...metadata, ...(hold === "metadata" ? { title: "Late cancelled response" } : {}) };
        if (hold === "metadata" || hold === "files" && url.pathname.endsWith("/files")) { pending.resolve(); await release.promise; }
        if (privatePR && !request.headers().authorization) return route.fulfill({ status: 404, json: { message: "not public" } });
        if (url.pathname.endsWith("/files")) {
          if (failPage) return route.fulfill({ status: 429, json: { message: "secret-error-marker" } });
          return route.fulfill({ json: url.searchParams.get("page") === "1" ? Array.from({ length: 100 }, (_, n) => file(n)) : [file(100)] });
        }
        return route.fulfill({ json: { ...responseMetadata, head: { sha: changed ? "new" : "head" }, html_url: "https://untrusted.invalid/metadata" } });
      }
      if (url.hostname === "public-control.test") return route.fulfill({ json: { message: "network-positive-control" }, headers: { "Access-Control-Allow-Origin": "*" } });
      if (url.hostname === "telemetry.test") {
        if (url.pathname.includes("config") || url.pathname.includes("flags")) {
          const config = { sessionRecording: { sampleRate: 1, minimumDurationMilliseconds: 0, networkPayloadCapture: { recordBody: true, recordHeaders: true, capturePerformance: true } }, capturePerformance: true, autocapture_opt_out: false, supportedCompression: [] };
          return url.pathname.endsWith(".js") ? route.fulfill({ contentType: "text/javascript", body: `window._POSTHOG_REMOTE_CONFIG={"synthetic-project":{config:${JSON.stringify(config)}}};` }) : route.fulfill({ json: config });
        }
        if (url.pathname.endsWith(".js")) {
          const name = url.pathname.split("/").at(-1)!;
          const recorder = Bun.file(path.resolve(import.meta.dir, "../node_modules/posthog-js/dist", name));
          if (await recorder.exists()) return route.fulfill({ body: await recorder.text(), contentType: "text/javascript" });
          return route.fulfill({ body: "", contentType: "text/javascript" });
        }
        posthogPayloads.push(request.postData() ?? "");
        return route.fulfill({ json: { status: 1 } });
      }
      if (url.pathname.includes("envelope")) { envelopes.push(request.postData() ?? ""); sentrySent.resolve(); return route.fulfill({ json: {} }); }
      if (url.pathname === "/entry.js") return route.fulfill({ body: built.js, contentType: "text/javascript" });
      if (url.pathname === "/styles.css") return route.fulfill({ body: built.css, contentType: "text/css" });
      if (url.pathname === "/api/Identity") return route.fulfill({ json: { username: "synthetic-viewer" } });
      if (url.pathname.endsWith("/api/History")) return route.fulfill({ json: { messages: [{ codeModeCalls: [{ summary: "private-history-summary-marker" }] }] } });
      if (url.pathname === "/oc/box/api/shell/sh_private/output" && url.searchParams.has("pull-request-discovery")) return route.fulfill({ json: { data: { output: "private-lookup-network-marker" } } });
      if (url.hostname === "preview.test" && url.pathname === "/") return route.fulfill({ contentType: "text/html", body: `<html><head><link rel="stylesheet" href="/styles.css"><script id="posthog-config" type="application/json">{"project_token":"synthetic-project","host":"https://telemetry.test"}</script><script id="sentry-config" type="application/json">{"dsn":"https://public@preview.test/1","traces_sample_rate":1}</script></head><body><div id="root"></div><script type="module" src="/entry.js"></script></body></html>` });
      unexpected.push(url.hostname);
      return route.abort();
    });
    await page.goto("https://preview.test/");
    const preview = page.getByRole("button", { name: "Preview pull request", exact: true }).first();
    await preview.waitFor();
    await preview.hover(); await preview.focus();
    expect(github).toHaveLength(0);
    expect(await page.getByRole("link", { name: "change", exact: true }).getAttribute("href")).toBe(href);
    expect(await page.getByRole("link", { name: "change", exact: true }).getAttribute("target")).toBe("_blank");
    await page.waitForFunction(() => (window as any).posthog.sessionRecording?.started === true);
    // Observe cancellation only after both SDKs have installed their native fetch instrumentation.
    await page.evaluate(() => {
      (window as any).requestAborts = 0;
      const fetchRequest = window.fetch;
      window.fetch = Object.assign((input: RequestInfo | URL, options?: RequestInit) => {
        if (String(input).startsWith("https://api.github.com/")) options?.signal?.addEventListener("abort", () => { (window as any).requestAborts++; }, { once: true });
        return fetchRequest(input, options);
      }, fetchRequest);
    });
    await page.getByRole("button", { name: "recordable-positive-control" }).click();
    await page.evaluate(async () => { await fetch("https://public-control.test/control", { headers: { "x-public-control": "visible-header" } }).then((response) => response.json()); });
    await page.evaluate(async () => { await fetch("/oc/box/api/shell/sh_private/output?pull-request-discovery=1&location[directory]=/private-lookup-folder-marker", { headers: { "x-private-lookup": "private-lookup-header-marker" } }).then((response) => response.json()); });
    await page.evaluate(async () => { for (const url of ["/api/History", "/r/remote/api/History"]) await fetch(url, { method: "POST", body: "{}" }).then((response) => response.json()); });
    await preview.click();
    const viewer = () => page.getByRole("dialog", { name: "Pull request preview", exact: true });
    await viewer().getByText(metadata.title, { exact: true }).waitFor();
    expect(await viewer().getAttribute("data-slot")).toBe("sheet-content");
    expect(github).toHaveLength(1);
    expect(await viewer().getByText("Draft", { exact: true }).count()).toBe(1);
    await viewer().getByRole("tab", { name: "Summary", exact: true }).waitFor();
    await viewer().getByRole("tab", { name: "Code", exact: true }).waitFor();
    await viewer().getByRole("button", { name: "Load files", exact: true }).click();
    await viewer().getByText("100 of 101 files loaded", { exact: true }).waitFor();
    await viewer().getByRole("treeitem", { name: "file-marker-0.txt", exact: true }).click();
    await viewer().getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
    expect(await viewer().locator('[data-change-icon="change"]').count()).toBeGreaterThan(0);
    await viewer().getByRole("combobox", { name: "Diff layout", exact: true }).click();
    await page.getByRole("option", { name: "Split", exact: true }).click();
    await viewer().locator('[data-diff-type="split"]').waitFor();
    await viewer().getByRole("tab", { name: "Summary", exact: true }).click();
    await viewer().getByText(metadata.body, { exact: true }).waitFor();
    await viewer().getByRole("tab", { name: "Code", exact: true }).click();
    await viewer().getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
    expect(await page.locator('img[src*="untrusted"]').count()).toBe(0);
    await visibility("hidden");
    expect(await page.evaluate(() => document.visibilityState)).toBe("hidden");
    expect(await viewer().count()).toBe(1);
    await visibility("visible");
    expect(await page.evaluate(() => document.visibilityState)).toBe("visible");
    expect(await viewer().count()).toBe(1);
    expect(await viewer().getByText("100 of 101 files loaded", { exact: true }).count()).toBe(1);
    expect(await viewer().getByText("patch-marker <script>example</script>", { exact: true }).count()).toBe(1);
    const beforeSwitch = github.length;
    for (const layout of ["Dialog", "Full-screen", "Inline", "Side panel"]) {
      await page.getByRole("combobox", { name: "Preview layout" }).click();
      await page.getByRole("option", { name: layout, exact: true }).click();
      const root = layout === "Inline" ? page.getByRole("region", { name: "Pull request preview" }) : viewer();
      await root.getByText(metadata.title, { exact: true }).waitFor();
      await root.getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
      expect(await root.evaluate((node: Element) => new Promise<boolean>((resolve) => requestAnimationFrame(() => resolve(node.contains(document.activeElement))))), `Focus remains inside ${layout}`).toBe(true);
      expect(await root.evaluate((node: Element) => node.classList.contains("ph-no-capture"))).toBe(true);
      expect(github).toHaveLength(beforeSwitch);
      if (layout === "Inline") {
        expect(await page.locator('p span[role=region][aria-label="Pull request preview"]').count()).toBe(1);
        expect(await root.locator("p, h1, h2, ul, ol, form").count()).toBe(0);
        await page.getByRole("textbox", { name: "Conversation draft" }).fill("unchanged draft marker");
      }
    }
    failPage = true;
    await viewer().getByRole("button", { name: "Load more files", exact: true }).click();
    await viewer().getByText("GitHub rate limit reached. Try again later.", { exact: true }).waitFor();
    expect(await viewer().getByText("100 of 101 files loaded", { exact: true }).count()).toBe(1);
    expect(await viewer().getByLabel("Personal read token", { exact: true }).count()).toBe(1);
    await viewer().getByLabel("Personal read token", { exact: true }).fill("quota-token-marker");
    await viewer().getByRole("button", { name: "Use token", exact: true }).click();
    await viewer().getByText(metadata.title, { exact: true }).waitFor();
    expect(github.at(-1)!.headers.authorization).toBe("Bearer quota-token-marker");
    expect(await viewer().getByText("0 of 101 files loaded", { exact: true }).count()).toBe(1);
    failPage = false;
    await viewer().getByRole("button", { name: "Load files", exact: true }).click();
    await viewer().getByText("100 of 101 files loaded", { exact: true }).waitFor();
    await viewer().getByRole("treeitem", { name: "file-marker-0.txt", exact: true }).click();
    hold = "files";
    const beforePageCancel = await page.evaluate(() => (window as any).requestAborts);
    await viewer().getByRole("button", { name: "Load more files", exact: true }).click();
    await pending.promise;
    await viewer().getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await viewer().count()).toBe(1);
    await page.waitForFunction((before: number) => (window as any).requestAborts > before, beforePageCancel);
    expect(await viewer().getByRole("status").count()).toBe(0);
    expect(await viewer().getByText(metadata.title, { exact: true }).count()).toBe(1);
    expect(await viewer().getByText("100 of 101 files loaded", { exact: true }).count()).toBe(1);
    expect(await viewer().getByText("patch-marker <script>example</script>", { exact: true }).count()).toBe(1);
    hold = ""; release.resolve();
    await viewer().getByRole("button", { name: "Load more files", exact: true }).click();
    await viewer().getByText("101 of 101 files loaded", { exact: true }).waitFor();
    expect(github.slice(-3).every(({ headers }) => headers.authorization === "Bearer quota-token-marker")).toBe(true);
    await viewer().getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
    expect(await viewer().getByText("patch-marker <script>example</script>", { exact: true }).count()).toBe(1);
    const tree = viewer().getByRole("region", { name: "Changed files", exact: true });
    await tree.getByRole("treeitem").first().press("End");
    const last = tree.getByRole("treeitem", { name: "file-marker-z-last.txt", exact: true });
    await last.click();
    const lastLine = viewer().getByText("last-page-patch-marker", { exact: true });
    await lastLine.waitFor();
    await page.waitForFunction(({ line, viewport }: { line: HTMLElement; viewport: HTMLElement }) => {
      const box = line.getBoundingClientRect(), bounds = viewport.getBoundingClientRect();
      return box.top >= bounds.top && box.bottom <= bounds.bottom;
    }, { line: await lastLine.elementHandle(), viewport: await viewer().getByRole("region", { name: "File patch", exact: true }).elementHandle() });
    expect(await viewer().locator('[data-change-icon="rename-changed"]').count()).toBeGreaterThan(0);
    await last.press("Home");
    await tree.getByRole("treeitem", { name: "file-marker-0.txt", exact: true }).click();
    await viewer().getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
    await viewer().getByRole("treeitem", { name: "file-marker-1.txt", exact: true }).click();
    await viewer().getByText("Patch unavailable (binary, too large, or omitted by GitHub).", { exact: true }).first().waitFor();
    await page.keyboard.press("Escape");
    await viewer().waitFor({ state: "detached" });
    expect(await page.getByRole("textbox", { name: "Conversation draft" }).inputValue()).toBe("unchanged draft marker");
    await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label") === "Preview pull request");

    privatePR = true;
    await preview.click();
    await viewer().getByText("Not found or private. You can use your own read token.", { exact: true }).waitFor();
    const token = viewer().getByLabel("Personal read token", { exact: true });
    expect(await token.getAttribute("type")).toBe("password");
    expect(await token.getAttribute("autocomplete")).toBe("off");
    expect(await token.evaluate((node: HTMLInputElement) => node.form)).toBeNull();
    await token.fill("synthetic-token-marker");
    await visibility("hidden");
    expect(await page.evaluate(() => document.visibilityState)).toBe("hidden");
    expect(await token.inputValue()).toBe("synthetic-token-marker");
    await visibility("visible");
    expect(await token.inputValue()).toBe("synthetic-token-marker");
    const beforeTokenLayout = github.length;
    await page.getByRole("combobox", { name: "Preview layout" }).click();
    await page.getByRole("option", { name: "Dialog", exact: true }).click();
    expect(await token.inputValue()).toBe("synthetic-token-marker");
    expect(github).toHaveLength(beforeTokenLayout);
    await viewer().getByRole("button", { name: "Use token", exact: true }).click();
    await viewer().getByText(metadata.title, { exact: true }).waitFor();
    await viewer().getByRole("button", { name: "Load files", exact: true }).click();
    await viewer().getByText("100 of 101 files loaded", { exact: true }).waitFor();
    changed = true;
    await viewer().getByRole("button", { name: "Load more files", exact: true }).click();
    await viewer().getByText("Pull request changed. Refresh before loading more files.", { exact: true }).waitFor();
    expect(await viewer().getByText("100 of 101 files loaded", { exact: true }).count()).toBe(1);
    changed = false;
    await page.evaluate(() => { history.pushState(null, "", "/other"); dispatchEvent(new PopStateEvent("popstate")); });
    await viewer().waitFor({ state: "detached" });
    await preview.click();
    await viewer().getByText("Not found or private. You can use your own read token.", { exact: true }).waitFor();
    expect(await viewer().getByLabel("Personal read token").inputValue()).toBe("");
    expect(github.at(-1)!.headers.authorization).toBeUndefined();
    await page.keyboard.press("Escape");

    await preview.click();
    await viewer().getByLabel("Personal read token").fill("hidden-token-marker");
    await viewer().getByRole("button", { name: "Use token", exact: true }).click();
    await viewer().getByText(metadata.title, { exact: true }).waitFor();
    await page.evaluate(() => { document.getElementById("root")!.style.display = "none"; });
    await viewer().waitFor({ state: "detached" });
    await page.evaluate(() => { document.getElementById("root")!.style.display = ""; });
    await preview.click();
    await viewer().getByText("Not found or private. You can use your own read token.", { exact: true }).waitFor();
    expect(await viewer().getByLabel("Personal read token").inputValue()).toBe("");
    await page.keyboard.press("Escape");

    release = Promise.withResolvers<void>(); pending = Promise.withResolvers<void>();
    privatePR = false; hold = "metadata";
    await preview.click(); await pending.promise;
    await viewer().getByRole("status").waitFor();
    expect(await viewer().getByRole("status").textContent()).toContain("Loading pull request…");
    const aborted = await page.evaluate(() => (window as any).requestAborts);
    await viewer().getByRole("button", { name: "Cancel", exact: true }).click();
    await viewer().waitFor({ state: "detached" });
    await page.waitForFunction((before: number) => (window as any).requestAborts > before, aborted);
    hold = "";
    await preview.click();
    await viewer().getByText(metadata.title, { exact: true }).waitFor();
    release.resolve();
    expect(await page.getByText("Late cancelled response", { exact: true }).count()).toBe(0);
    await page.setViewportSize({ width: 360, height: 740 });
    for (const layout of ["Dialog", "Inline", "Full-screen", "Side panel"]) {
      await page.getByRole("combobox", { name: "Preview layout" }).click();
      await page.getByRole("option", { name: layout, exact: true }).click();
      const root = layout === "Inline" ? page.getByRole("region", { name: "Pull request preview" }) : viewer();
      await root.getByText(metadata.title, { exact: true }).waitFor();
      const bounds = await root.boundingBox();
      expect(bounds.x).toBeGreaterThanOrEqual(-1); expect(bounds.x + bounds.width).toBeLessThanOrEqual(361);
      await root.getByRole("combobox", { name: "Preview layout" }).focus();
    }
    await page.keyboard.press("Escape");
    await viewer().waitFor({ state: "detached" });
    await preview.click();
    await viewer().getByText(metadata.title, { exact: true }).waitFor();
    const beforeUnmount = await page.evaluate(() => (window as any).requestAborts);
    await page.evaluate(() => (window as any).unmountPreview());
    await viewer().waitFor({ state: "detached" });
    await page.waitForFunction((before: number) => (window as any).requestAborts > before, beforeUnmount);
    // Recorder output is buffered; positive controls must reach its emitted snapshot, not just the DOM.
    await page.waitForFunction(() => {
      const data = JSON.stringify((window as any).telemetryEvents);
      return data.includes('"$snapshot"') && data.includes("recordable-positive-control") && data.includes("network-positive-control") && data.includes("public-control.test/control");
    }, undefined, { timeout: 20_000 });
    await page.evaluate(() => { (window as any).posthog.stopSessionRecording(); });
    const data = await page.evaluate(() => JSON.stringify((window as any).telemetryEvents));
    expect(data).toContain("visible-header");
    await page.evaluate(() => (window as any).flushTelemetry());
    await sentrySent.promise;
    const storage = await page.evaluate(async () => {
      const databases = await Promise.all((await indexedDB.databases()).map(async ({ name }) => {
        const db = await new Promise<IDBDatabase>((resolve) => { const request = indexedDB.open(name!); request.onsuccess = () => resolve(request.result); });
        const stores = await Promise.all(Array.from(db.objectStoreNames).map((store) => new Promise<unknown>((resolve) => {
          const request = db.transaction(store).objectStore(store).getAll(); request.onsuccess = () => resolve(request.result);
        })));
        db.close(); return stores;
      }));
      return JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, cookies: document.cookie, databases });
    });
    privatePR = true;
    await page.goto("http://preview.test/");
    await preview.click();
    await viewer().getByText("Not found or private. You can use your own read token.", { exact: true }).waitFor();
    expect(await viewer().getByLabel("Personal read token").count()).toBe(0);
    await viewer().getByText("Token entry requires a secure browser context with a trusted certificate. Open this app over HTTPS.", { exact: true }).waitFor();
    await page.goto("about:blank");
    expect(envelopes.join("\n")).toContain('"type":"span"');
    expect(posthogPayloads.length).toBeGreaterThan(0);
    expect(posthogPayloads.join("\n")).toContain("network-positive-control");
    expect(posthogPayloads.join("\n")).toContain("recordable-positive-control");
    expect(envelopes.join("\n")).toContain("public-control.test/control");
    for (const secret of ["synthetic-token-marker", "hidden-token-marker", "quota-token-marker", "Synthetic title marker", "description-marker", "file-marker-", "patch-marker", "fixture-repository-marker", "secret-error-marker", "private-lookup-network-marker", "private-lookup-folder-marker", "private-lookup-header-marker", "private-history-summary-marker"]) {
      expect(data).not.toContain(secret);
      expect(envelopes.join("\n")).not.toContain(secret);
      expect(posthogPayloads.join("\n")).not.toContain(secret);
      expect(storage).not.toContain(secret);
    }
    expect(github.every(({ url, headers }) => url.startsWith("https://api.github.com/repos/") && !headers.cookie && !headers.referer && !headers["sentry-trace"] && !headers.baggage)).toBe(true);
    expect(github.some(({ headers }) => headers.authorization === "Bearer synthetic-token-marker")).toBe(true);
    expect(unexpected).toEqual([]);
    expect(errors).toEqual([]);
  } finally { release.resolve(); await browser.close(); }
}, 60_000);

test("built App PR layouts preserve the composer draft and existing delegation panel", async () => {
  const { chromium } = await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!);
  const dist = path.resolve(import.meta.dir, "../../internal/web/dist");
  const session = "preview-fixture", child = `${session}/task`, other = "other-fixture";
  const sessionPath = (id: string) => `/s/${Buffer.from(id).toString("base64url")}`;
  const tabs: SavedTabs = { spaces: [{ id: "main", name: "Main" }], state: { tabs: [session, other].map((id) => ({ path: sessionPath(id), space: "main" })), groups: [], space: "main", last: {}, sharedOrder: [] }, revision: "0", shared: [] };
  const message = (id: string, text: string): TranscriptEvent => ({ role: "assistant", text, complete: true, turnId: id, entryKey: id, itemId: id, inputId: "" });
  let updatedAt = "2026-01-01T00:00:00Z";
  let prompts = 0;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, idleTimeout: 0, async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/stream") return new Response(new ReadableStream({ start(controller) { controller.enqueue(": connected\n\n"); } }), { headers: { "Content-Type": "text/event-stream" } });
    if (url.pathname === "/api/ListSessions") return new Response(`data: ${JSON.stringify({ sessions: [{ id: session, name: "Preview fixture", agent: "main", updatedAt }, { id: other, name: "Other fixture", agent: "main" }], owner: "synthetic-viewer", upstreamSuccess: true })}\n\nevent: complete\ndata: {}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
    if (!url.pathname.startsWith("/api/")) {
      const file = Bun.file(path.join(dist, url.pathname));
      return new Response(await file.exists() ? file : Bun.file(path.join(dist, "index.html")));
    }
    const input = request.method === "POST" ? await request.json() as { id: string; state?: SavedTabs["state"] } : { id: "" };
    switch (url.pathname) {
      case "/api/Protocol": return Response.json({ protoSha256: "pr-app-fixture" });
      case "/api/Identity": return Response.json({ username: "synthetic-viewer" });
      case "/api/ListConfig": return Response.json({ config: { workspace: "pr-app-fixture" } });
      case "/api/ListAgents": return Response.json({ agents: [{ name: "main" }], currentAgent: "main" });
      case "/api/ListSkills": return Response.json({ skills: [] });
      case "/api/ListQueue": return Response.json({ items: [] });
      case "/api/Prompt": prompts++; return Response.json({});
      case "/api/Tabs":
        if (input.state) { tabs.state = input.state; tabs.revision = String(Number(tabs.revision) + 1); }
        return Response.json(tabs);
      case "/api/History": {
        const text = input.id === session ? `[Synthetic change](${href})` : input.id === child ? "Existing delegation fixture" : "Other conversation fixture";
        const created: TranscriptEvent[] = input.id === session ? [{ ...message(input.id, "execute\n{}"), role: "tool", toolName: "execute", toolCallId: "pr-create", itemId: "pr-create", codeModeCalls: [{ name: "bash", arguments: JSON.stringify({ command: "gh pr create --title 'Sample change' --body 'Sample'" }), status: "ok", summary: href.replace(/\/files$/, "") }] }] : [];
        return Response.json({ messages: [message(input.id, text), ...created], delegations: input.id === session ? [child] : [], lineage: input.id === child ? [{ id: session }, { id: child, kind: "SUBAGENT" }] : undefined, origin: "", revision: input.id, reset: true, replacedKeys: [], removedKeys: [], entryKeys: [input.id], running: false, terminal: "", backgroundJobs: [], pendingQuestions: [], start: "0", more: false });
      }
      default: return Response.json({});
    }
  } });
  const browser = await chromium.launch({ executablePath: process.env.ROCKETCLAW_CHROMIUM, headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const origin = `http://127.0.0.1:${server.port}`;
    const github: string[] = [], unexpected: string[] = [], errors: string[] = [];
    page.on("pageerror", (error: Error) => errors.push(error.message));
    await page.route("**/*", async (route: any) => {
      const url = new URL(route.request().url());
      if (url.hostname === "api.github.com") {
        github.push(url.pathname);
        return route.fulfill({ json: url.pathname.endsWith("/files") ? [file(0)] : { ...metadata, changed_files: 1 }, headers: { "Access-Control-Allow-Origin": "*" } });
      }
      if (url.origin === origin) return route.continue();
      unexpected.push(url.hostname);
      return route.abort();
    });
    const chat = `${sessionPath(session)}?delegation=${encodeURIComponent(child)}`;
    await page.goto(`${origin}${chat}`);
    const composer = page.locator("main textarea");
    const panel = page.getByRole("complementary", { name: "Delegation", exact: true, includeHidden: true });
    await panel.getByText("Existing delegation fixture", { exact: true }).waitFor();
    await page.locator("main textarea:enabled").waitFor();
    await composer.fill("App composer draft before preview");
    const preview = page.locator("main").getByRole("button", { name: "Preview pull request", exact: true });
    await preview.hover(); await preview.focus();
    expect(github).toHaveLength(0);
    await preview.click();
    const dialog = page.getByRole("dialog", { name: "Pull request preview", exact: true });
    await dialog.getByText(metadata.title, { exact: true }).waitFor();
    expect(await dialog.getAttribute("data-slot")).toBe("sheet-content");
    expect(await dialog.getAttribute("data-side")).toBe("right");
    await dialog.getByRole("button", { name: "Load files", exact: true }).click();
    await dialog.getByText("1 of 1 files loaded", { exact: true }).waitFor();
    await dialog.getByRole("treeitem", { name: "file-marker-0.txt", exact: true }).click();
    const loaded = github.length;
    for (const layout of ["Dialog", "Inline", "Full-screen", "Side panel"]) {
      await page.getByRole("combobox", { name: "Preview layout" }).click();
      await page.getByRole("option", { name: layout, exact: true }).click();
      const root = layout === "Inline" ? page.getByRole("region", { name: "Pull request preview", exact: true }) : dialog;
      await root.getByText(metadata.title, { exact: true }).waitFor();
      await root.getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
      expect(github).toHaveLength(loaded);
      expect(await panel.textContent()).toContain("Existing delegation fixture");
      expect(new URL(page.url()).searchParams.get("delegation")).toBe(child);
      if (layout === "Inline") await composer.fill("App composer draft edited beside preview");
    }
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "detached" });
    expect(await composer.inputValue()).toBe("App composer draft edited beside preview");
    expect(await panel.isVisible()).toBe(true);
    expect(new URL(page.url()).pathname).toBe(sessionPath(session));
    await preview.click();
    await dialog.getByText(metadata.title, { exact: true }).waitFor();
    expect(github).toHaveLength(loaded + 1);
    await page.getByRole("combobox", { name: "Preview layout" }).click();
    await page.getByRole("option", { name: "Inline", exact: true }).click();
    const inline = page.getByRole("region", { name: "Pull request preview", exact: true });
    await inline.getByText(metadata.title, { exact: true }).waitFor();
    await page.getByRole("tab", { name: "Other fixture", exact: true }).click();
    await page.getByText("Other conversation fixture", { exact: true }).waitFor();
    await inline.waitFor({ state: "detached" });
    await page.getByRole("tab", { name: "Preview fixture", exact: true }).click();
    await preview.waitFor();
    await page.waitForFunction(() => document.querySelector("main textarea") instanceof HTMLTextAreaElement && (document.querySelector("main textarea") as HTMLTextAreaElement).value === "App composer draft edited beside preview");
    expect(await inline.count()).toBe(0);
    expect(github).toHaveLength(loaded + 1);
    expect(await page.locator("#bottom-navigation").getByRole("button").count()).toBe(6);
    await page.getByRole("button", { name: "Tab actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).click();
    const prs = page.getByRole("complementary", { name: "Pull requests", exact: true });
    await prs.getByText(metadata.title, { exact: true }).waitFor();
    await prs.getByText("Created in this thread", { exact: true }).waitFor();
    expect(await page.getByRole("complementary", { name: "Delegation", exact: true }).count()).toBe(0);
    expect(await composer.inputValue()).toBe("App composer draft edited beside preview");
    await composer.fill("Draft still editable beside the PR pane");
    expect(await composer.isEnabled()).toBe(true);
    await prs.getByRole("button", { name: "Load files", exact: true }).click();
    await prs.getByRole("treeitem", { name: "file-marker-0.txt", exact: true }).click();
    await prs.getByText("patch-marker <script>example</script>", { exact: true }).waitFor();
    const resize = prs.getByRole("separator", { name: "Resize delegation panel", exact: true });
    await resize.focus();
    for (let step = 0; step < 16; step++) await resize.press("ArrowLeft");
    const patchBox = (await prs.getByRole("region", { name: "File patch", exact: true }).boundingBox())!;
    const treeBox = (await prs.getByRole("region", { name: "Changed files", exact: true }).boundingBox())!;
    expect(treeBox.x).toBeGreaterThanOrEqual(patchBox.x + patchBox.width - 1);
    const rediscovery = page.waitForRequest((request: any) => request.url().includes("/api/History?pull-request-discovery"));
    updatedAt = "2026-01-02T00:00:00Z";
    await rediscovery;
    await prs.getByText("Finding pull requests…", { exact: true }).waitFor({ state: "hidden" });
    await prs.getByText(metadata.title, { exact: true }).waitFor();
    expect(await prs.getByText("patch-marker <script>example</script>", { exact: true }).count()).toBe(1);
    await page.getByRole("tab", { name: "Other fixture", exact: true }).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).click();
    await page.getByText("Other conversation fixture", { exact: true }).waitFor();
    await prs.getByText("No pull requests found for this thread.", { exact: true }).waitFor();
    expect(await prs.getByText(metadata.title, { exact: true }).count()).toBe(0);
    await page.getByRole("tab", { name: "Preview fixture", exact: true }).click();
    await preview.waitFor();
    await page.getByRole("button", { name: "Tab actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).click();
    await prs.getByText(metadata.title, { exact: true }).waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    const mobile = page.getByRole("dialog", { name: "Pull requests", exact: true });
    await mobile.getByText(metadata.title, { exact: true }).waitFor();
    await mobile.getByRole("button", { name: "Close pull requests", exact: true }).click();
    await page.getByRole("button", { name: "Show sidebar", exact: true }).click();
    const actions = page.getByRole("button", { name: "Tab actions", exact: true });
    await actions.press("Enter");
    await page.getByRole("menuitem", { name: "Pull requests", exact: true }).press("Enter");
    await mobile.getByText(metadata.title, { exact: true }).waitFor();
    await page.waitForFunction((node: HTMLElement) => node.contains(document.activeElement), await mobile.elementHandle());
    await page.keyboard.press("Escape");
    await mobile.waitFor({ state: "hidden" });
    expect(await page.getByRole("dialog", { name: "Tabs", exact: true }).isVisible()).toBe(true);
    await page.waitForFunction((node: HTMLElement) => document.activeElement === node, await actions.elementHandle());
    expect(await composer.inputValue()).toBe("Draft still editable beside the PR pane");
    expect(prompts).toBe(0);
    expect(unexpected).toEqual([]);
    expect(errors).toEqual([]);
  } finally { await browser.close(); server.stop(true); }
}, 30_000);
