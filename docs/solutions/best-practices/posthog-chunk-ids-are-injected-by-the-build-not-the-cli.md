---
title: PostHog Chunk IDs Are Injected by build.ts, Not by posthog-cli
date: 2026-11-18
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: tooling_decision
component: observability
severity: medium
applies_when:
  - Changing how internal/rocketclaw/web/build.ts writes source maps or tags bundles for PostHog error tracking
  - Replacing the hand-written chunk-ID injection with `posthog-cli sourcemap inject` or the PostHog GitHub Action
  - PostHog error stack traces stop resolving to internal/rocketclaw/web/src/
  - Upgrading posthog-cli in .github/workflows/posthog-sourcemaps.yml
retire_when: "`posthog-cli sourcemap inject` writes chunk-ID-only snippets with no credentials and no API call inside a jj/git checkout; check by running it on a built assets directory in a checkout with POSTHOG_CLI_API_KEY and POSTHOG_CLI_PROJECT_ID unset"
tags:
  - posthog
  - source-maps
  - chunk-id
  - posthog-cli
  - build
  - dist
  - deterministic-build
  - error-tracking
---

# PostHog Chunk IDs Are Injected by build.ts, Not by posthog-cli

## Context

PostHog resolves a minified browser stack frame to source only when the served JavaScript file carries a chunk ID that matches an uploaded source map. Each served chunk needs three things: a snippet that registers the chunk in `globalThis._posthogChunkIds` keyed by an `Error().stack` taken inside the file, a trailing `//# chunkId=<id>` comment, and a `chunk_id` field in the source map. posthog-js reads `_posthogChunkIds` when it builds an exception and attaches the chunk ID to each frame.

The served JavaScript is not built at deploy time. `internal/rocketclaw/internal/web/dist` is committed, gzipped, and embedded in the Go binary (`internal/rocketclaw/internal/web/assets.go:24-27`: `go generate` runs `bun run build`, then gzips every file in `dist`, then `//go:embed dist all:oc-dist`), so what runs in production is whatever a developer's local `go generate` wrote. Gzip leaves the JavaScript bytes unchanged. CI checks the committed `dist` against a fresh build (`.github/workflows/test.yml:68-69`, "Generated files are committed"). So the injection has to happen in every local build, and it has to produce byte-identical output on every machine.

PostHog documents `posthog-cli sourcemap inject` for this step. It does not work in that build, as tested with posthog-cli 0.20.1:

- With no credentials it stops before touching any file: `Couldn't find POSTHOG_CLI_API_KEY and POSTHOG_CLI_PROJECT_ID in process env`. That happens even with no release flags.
- Inside a jj/git checkout it reads release coordinates from git and creates the release through the API, so it needs a real key and network access.
- It ran offline only with a dummy `phx_` key and a working directory outside any repository. Making every local build depend on that is fragile.

Two shortcuts don't work either. Leaving the snippet out and relying on maps fetched from the `sourceMappingURL` doesn't work, because PostHog cannot reach a tailnet-only deployment. Injecting only in the release workflow doesn't work, because the files users get come from the commit, not from the workflow.

## Guidance

`build.ts` injects the chunk ID itself (`internal/rocketclaw/web/build.ts:9-21`):

- Bun builds with `sourcemap: "linked"`, which gives every chunk a `debugId` derived from its content. The same source gives the same ID on every build.
- For each `.js` output, it reuses that `debugId` as the chunk ID. It prepends the `_posthogChunkIds` snippet as its own first line and appends `//# chunkId=<id>`.
- It writes the map to `.tmp/posthog-sourcemaps/` with `chunk_id` set and `mappings` prefixed by `;`. The `;` shifts every mapping down one line to account for the prepended snippet line.
- It copies the final JavaScript next to the map and removes the map from `dist`, so RocketClaw never serves maps.

Use posthog-cli only to upload. `.github/workflows/posthog-sourcemaps.yml` rebuilds at the release tag, decompresses the committed `dist` JavaScript and compares it with the rebuilt JavaScript (`posthog-sourcemaps.yml:21-34`), and runs `posthog-cli sourcemap upload --directory .tmp/posthog-sourcemaps` (`posthog-sourcemaps.yml:41`). Upload reads `chunk_id` from each map, so the self-injected maps upload as they are.

## Why This Matters

If someone "simplifies" `build.ts` by calling `posthog-cli sourcemap inject`, CI's generate step fails with a credentials error. The obvious next move, adding a key to the build, leaks a personal API key into every developer build and makes the build depend on PostHog's API.

Breaking the injection in other ways fails silently. Errors still arrive in PostHog, but their stack traces point to minified code. Examples are a snippet without its line shift, a chunk ID that differs between the JavaScript and the map, or a map left in `dist`.

## When to Apply

- Any change to `build.ts` output naming, splitting, minification, or source map settings: re-check that a rebuild is byte-identical and that each `.js` in `.tmp/posthog-sourcemaps/` has a matching `.map` with `chunk_id`.
- Upgrading posthog-cli: confirm `sourcemap upload` still reads `chunk_id` from the map. Point the CLI at a local stub server with `POSTHOG_CLI_HOST`, and check that its `bulk_check_upload` request lists every chunk ID.
- Upgrading posthog-js: confirm it still reads `globalThis._posthogChunkIds`.

## Examples

What a served chunk looks like after the build:

```js
!function(){try{var n=(new Error).stack;n&&((globalThis._posthogChunkIds=globalThis._posthogChunkIds||{})[n]="EE9BD1A7CBC3236564756E2164756E21")}catch(e){}}();
var pZ=Object.create;...
//# debugId=EE9BD1A7CBC3236564756E2164756E21
//# sourceMappingURL=main-7q8jrqyq.js.map

//# chunkId=EE9BD1A7CBC3236564756E2164756E21
```

How this was verified when it was built:

- Two builds of the same tree produced identical `dist` directories.
- The real `dist` was served in headless Chromium with a PostHog config. An exception sent from a failed `Identity` request carried 17 frames, each tagged with the main bundle's chunk ID.
- `posthog-cli sourcemap upload` was pointed at a local stub server. It found all 503 JavaScript and map pairs, and its first request listed every chunk ID.

## Related

- `docs/solutions/best-practices/crash-dumps-in-web-change-the-built-stylesheet.md`: another way the committed `dist` stops matching a fresh build.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: how `bun run build` and `dist` relate to the browser tests.
- `docs/solutions/best-practices/posthog-needs-genai-span-attributes.md`: the server-side PostHog integration.
- `docs/solutions/performance-issues/search-page-froze-rendering-every-result.md`: records the Sentry-era removal of source maps that this change reverses.
