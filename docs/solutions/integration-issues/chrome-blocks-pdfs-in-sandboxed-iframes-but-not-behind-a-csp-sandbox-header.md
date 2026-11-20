---
title: Chrome Blocks PDFs in Sandboxed Iframes, but Not Behind a CSP Sandbox Header
date: 2026-11-20
category: docs/solutions/integration-issues/
module: internal/rocketclaw/web
problem_type: integration_issue
component: frontend
symptoms:
  - "A Hosted Page ending in .pdf showed \"This page has been blocked by Chrome\" inside its tab"
  - "The frame's request for /pages-raw/<page>.pdf failed with net::ERR_BLOCKED_BY_CLIENT"
  - "The same /pages-raw/ link opened in its own browser tab showed the PDF normally"
root_cause: wrong_api
resolution_type: code_fix
severity: medium
related_components:
  - internal/rocketclaw/frontend/rpc
tags:
  - hosted-pages
  - pdf
  - iframe-sandbox
  - content-security-policy
  - chromium
  - pages-raw
retire_when: "Chromium shows its PDF viewer inside an iframe that has a sandbox attribute; check by loading an application/pdf response in a sandbox-attribute frame on a current Chromium (the probe under Prevention)"
---

# Chrome Blocks PDFs in Sandboxed Iframes, but Not Behind a CSP Sandbox Header

## Problem

The Web showed every Hosted Page inside an `<iframe sandbox="...">`. Chrome refuses to start its built-in PDF viewer in a frame that has a `sandbox` attribute, so every PDF page showed Chrome's block page instead of the document. The server's `Content-Security-Policy: sandbox ...` header on `/pages-raw/` was not the cause, although it looks like it.

## Symptoms

- The page tab showed "This page has been blocked by Chrome".
- The frame's request failed with `net::ERR_BLOCKED_BY_CLIENT`.
- `curl` on the real `/pages-raw/...pdf` URL returned a valid `application/pdf` (28 pages) with `content-security-policy: sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads`. The same file, opened as a top-level tab in Chromium, showed in the PDF viewer.

## What Didn't Work

- **Suspecting the CSP sandbox header.** It was the first suspect, and it is wrong. A local Chromium probe served the same PDF six ways (the `sandbox` flags were the same as production's):

  | How the PDF is loaded | Result |
  |---|---|
  | Iframe `sandbox` attribute + CSP sandbox header (production before the fix) | Blocked, `ERR_BLOCKED_BY_CLIENT` |
  | Iframe `sandbox` attribute only | Blocked, `ERR_BLOCKED_BY_CLIENT` |
  | Iframe with CSP sandbox header only | PDF viewer loads |
  | Plain iframe, no sandbox anywhere | PDF viewer loads |
  | Top-level tab, with the header | PDF viewer loads |
  | Top-level tab, no header | PDF viewer loads |

  Only the attribute blocks the viewer. Loosening or removing the header would not have fixed anything and would have weakened every page.
- **Adding more `allow-*` keywords to the attribute.** This is not a way out. This session tested only production's flags. An [outside report on Chromium 153](https://code.yangyq.net/laoyang/FamilyBook/issues/14) tried `allow-same-origin`, `allow-popups-to-escape-sandbox`, `allow-forms` and `allow-popups` as well. Every combination still blocked the viewer, and only removing the attribute helped.
- **The existing browser tests.** `pages.browser.test.ts` served only HTML pages, so the frame always worked there.

## Solution

The user weighed three options:

1. Drop the `sandbox` attribute for `.pdf` pages only and rely on the header. This keeps the PDF inline. The cost: the header covers only that one response, while the attribute also sandboxes whatever the frame navigates to next.
2. Drop the attribute for every page. This was rejected because every HTML page would lose that navigation coverage.
3. Do not frame PDFs at all. **This was chosen** as the safest option.

`HostedPage` in `internal/rocketclaw/web/src/ui.tsx:3284` now returns a link instead of the frame for a page whose path ends in `.pdf`, ignoring case:

```tsx
if (/\.pdf$/i.test(page)) return <p className="m-auto p-4 text-sm"><a href={`${serverURL()}/pages-raw/${page}`} target="_blank" rel="noopener noreferrer" className="underline">Open PDF</a></p>;
return <iframe ref={frame} title={pageLabel(page)} sandbox="allow-scripts allow-popups allow-forms allow-modals allow-downloads" ... />;
```

HTML pages still use the sandboxed frame (`ui.tsx:3285`). The root `README.md` says that a PDF page's tab offers an Open PDF link.

## Why This Works

The `/pages-raw/` handler sets the same sandbox flags as a response header on every file (`internal/rocketclaw/frontend/rpc/http.go:204`), and `TestPages` asserts that header (`internal/rocketclaw/frontend/rpc/pages_test.go:140`). A top-level tab that opens the PDF therefore still runs in an opaque origin with the same flags the iframe attribute had. Chrome's viewer accepts that, because only a frame's `sandbox` attribute stops it.

The link is a top-level navigation, so a remote RocketClaw admits it just as it admitted the frame. `serverURL()` points it at the remote's `/pages-raw/` (see [A Web Page That Calls Another RocketClaw](../architecture-patterns/tailnet-pages-calling-another-rocketclaw.md): navigations need nothing).

## Prevention

- **Regression test:** `a PDF page, which Chrome refuses to show in a sandboxed frame, opens in its own tab` (`internal/rocketclaw/web/src/pages.browser.test.ts:144`). It checks that a `.pdf` page renders no iframe, that the link points at `/pages-raw/<page>`, and that the opened tab hosts Chrome's PDF viewer (a `chrome-extension://` frame). It failed before the fix and passes after.
- **That test needs full Chromium.** According to this fix's code review, `chromium-headless-shell` has no PDF viewer, so the last assertion would fail there even though the app works. This session did not run headless-shell. CI installs full Chromium with `playwright-core install --no-shell chromium` (`.github/workflows/test.yml:144`). Point `ROCKETCLAW_CHROMIUM` at that build locally. Full Chromium in Playwright's default headless mode does load the viewer.
- **The PDF tab now depends on the header alone.** If the `/pages-raw/` CSP sandbox header is ever loosened or limited to some content types, a `text/html` file stored at a `.pdf` path and opened through the link would run with the RocketClaw origin. Keep the header on every `/pages-raw/` response.
- **Known gaps:**
  - Detection is by path suffix. A PDF stored at a path without `.pdf` is still framed and still blocked.
  - A link inside a sandboxed HTML page that opens a PDF, whether in the frame or in a popup, probably hits the same block. The frame keeps the attribute's flags, and the frame does not grant `allow-popups-to-escape-sandbox`, so popups inherit the sandbox. This session did not test that case.
- **Probe to rerun before changing how PDFs are shown** (Bun + `playwright-core`, any PDF as `raw.pdf`). Compare `/f?attr=1&src=/sandboxed.pdf`, `/f?src=/sandboxed.pdf`, and `/sandboxed.pdf`, then look for a `chrome-extension://` frame and `ERR_BLOCKED_BY_CLIENT`:

  ```ts
  const flags = "allow-scripts allow-popups allow-forms allow-modals allow-downloads";
  Bun.serve({ port: 0, fetch(request) {
    const url = new URL(request.url);
    const pdf = { "Content-Type": "application/pdf", "X-Content-Type-Options": "nosniff" };
    if (url.pathname === "/sandboxed.pdf") return new Response(Bun.file("raw.pdf"), { headers: { ...pdf, "Content-Security-Policy": `sandbox ${flags}` } });
    if (url.pathname === "/plain.pdf") return new Response(Bun.file("raw.pdf"), { headers: pdf });
    const attribute = url.searchParams.get("attr") === "1" ? `sandbox="${flags}"` : "";
    return new Response(`<iframe ${attribute} src="${url.searchParams.get("src")}"></iframe>`, { headers: { "Content-Type": "text/html" } });
  } });
  ```

## Related Issues

- [A Web Page That Calls Another RocketClaw Must Read Its Refusals and Pin Its Server](../architecture-patterns/tailnet-pages-calling-another-rocketclaw.md): why the link works for a remote space's pages.
- [Frontend RPC Browser Tests Fail Without Their Setup](../best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md): the short `TMPDIR`, rebuilt `dist`, and test database that `TestPagesRealBrowser` needed while verifying this fix.
