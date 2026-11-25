---
title: Headless Chromium Blocks a Page's Calls to Another RocketClaw over the Tailnet
date: 2026-11-24
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: frontend
severity: medium
applies_when:
  - Driving a live RocketClaw Web page in headless Chromium (playwright-core) to check a change against real remote RocketClaws
  - A headless probe shows remote calls failing with "Permission was denied for this request to access the 'unknown' address space"
  - Deciding whether a remote RocketClaw's CORS handling is broken after an automated browser check fails
tags:
  - remote-rocketclaws
  - tailnet
  - chromium
  - local-network-access
  - playwright
  - live-verification
---

# Headless Chromium Blocks a Page's Calls to Another RocketClaw over the Tailnet

## Context

A Web page can list and drive other RocketClaws: it reads their names and origins from the page's `rocketclaws` JSON and calls each remote's tailnet origin directly (`rocketclaws` and `serverURL` in `internal/rocketclaw/web/src/navigation.tsx`, `rpc` in `internal/rocketclaw/web/src/api.ts`). To check a Web change against real data, an agent drove a live page in headless Chromium through `playwright-core`, the same `ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM` setup the browser tests use (Chrome for Testing 159.0.8154.6).

Every call from the page to the remote failed, while the page's own server answered. The console showed, for each remote RPC:

```text
Access to fetch at 'https://rc-b.example.ts.net/api/ListPages' from origin 'https://rc-a.example.ts.net'
has been blocked by CORS policy: Permission was denied for this request to access the 'unknown' address space.
Failed to load resource: net::ERR_FAILED
```

It reads like a CORS bug in the remote, but it is not. A `curl` preflight from the same machine (`OPTIONS` with the page's `Origin`) got `204` with `access-control-allow-origin` naming the page origin, so `NewTailnetHandler` was answering correctly. The block comes from Chrome's Local Network Access checks: the remote's tailnet address (a `100.x` CGNAT address) is classed as a private or unknown address space, and an automated browser with no one to answer the permission prompt is denied.

The repository's own remote test never meets this. `remote-transport.test.ts` maps its remote host names to `127.0.0.1` with Chromium's `--host-resolver-rules`, so page and remote share the loopback address space. That is why the open risk in `docs/plans/2026-10-30-1328-feat-remote-rocketclaws-plan.md` (Chrome's Local Network Access on tailnet-to-tailnet calls) stayed unanswered by the test suite.

## Guidance

For a headless probe against live remotes, turn the checks off at launch. These three features together let the remote calls through:

```ts
const browser = await chromium.launch({
  executablePath: process.env.ROCKETCLAW_CHROMIUM,
  headless: true,
  args: ["--disable-features=LocalNetworkAccessChecks,BlockInsecurePrivateNetworkRequests,PrivateNetworkAccessSendPreflights"],
});
```

This session did not isolate which one of the three is required; drop one at a time if the list needs to be minimal.

Treat the flag as a property of the probe, not of the product. A failure that names an "address space" is the browser's network policy, so do not change `NewTailnetHandler` or the page's CORS handling to answer it. Whether a person's own Chrome prompts for, allows, or blocks these calls depends on that browser's Local Network Access state; this session did not check a real browser.

Chromium in this setup also aborts at launch when `TMPDIR` is long, because its `SingletonSocket` path passes the Unix socket limit. That trap and its fix are in `frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`. From a nested workspace, a short `TMPDIR` under the main checkout's `.tmp/` keeps the repository's temporary-directory rule and fits.

## Why This Matters

Without the flag, a live probe shows only the local server's data, and every remote looks down or misconfigured. The natural next step is to debug CORS in `NewTailnetHandler`, which is already correct, or to conclude the change under test does not work across servers when it does. With the flag, the same probe showed every remote's data and the links that open on each remote.

## When to Apply

- Any automated Chromium run whose page origin and target origin are on different non-loopback addresses, such as two tailnet hosts.
- Not the repository's browser tests: they run every remote on `127.0.0.1` and need no flag.

## Examples

Checking an unreleased Web bundle against live servers: load the live page, swap in the locally built bundle with `page.route`, and launch with the flag above. The live server's page keeps its own `rocketclaws` list, so the new code calls the real remotes. Here `site` is the live page's origin and `gunzip` reads a file's `.gz` copy from `internal/rocketclaw/internal/web/dist`.

```ts
await page.route(`${site}/pages`, async (route) => {
  const live = await (await route.fetch()).text();
  const remotes = live.match(/<script id="rocketclaws"[^>]*>[^<]*<\/script>/)![0];
  const html = gunzip("dist/index.html").replace("</head>", `${remotes}</head>`);
  await route.fulfill({ contentType: "text/html", body: html });
});
await page.route(`${site}/assets/**`, (route) => route.fulfill({ body: gunzip(`dist${new URL(route.request().url()).pathname}`) }));
```

Before the flag, the probe showed the local server's empty list and the console errors above. After it, the same probe listed the remote's pages under their space and server, each linking to `/r/<name>/pages/...`.

## Related

- `docs/solutions/architecture-patterns/tailnet-pages-calling-another-rocketclaw.md`: how the page calls a remote, and how the remote decides which page origins it trusts.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: the `ROCKETCLAW_CHROMIUM` setup and the long-`TMPDIR` socket-path abort.
- `docs/solutions/best-practices/web-shortcuts-may-claim-any-browser-key-chromium-does-not-reserve.md`: another case where headless Chromium differs from a person's browser.
- `docs/plans/2026-10-30-1328-feat-remote-rocketclaws-plan.md`: the plan's open risk about Local Network Access on tailnet-to-tailnet calls.
