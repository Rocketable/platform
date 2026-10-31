---
title: The Emulated Shell Runs just-bash Under Node From a Local npm Install, Not Bun, npx, or a Bundle
date: 2026-10-29
category: docs/solutions/best-practices/
module: internal/rocketclaw/justbash
problem_type: tooling_decision
component: service_layer
severity: high
applies_when:
  - Changing how RocketClaw runs or installs just-bash for easy agents or agents that set shell: emulated
  - Proposing bunx, bun, npx, or a bun build bundle to run the just-bash wrapper
  - Moving wrapper.ts, renaming it to .mjs, or embedding just-bash in the binary
  - Upgrading the pinned just-bash version or the Node version CI installs
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/harness
  - .github/workflows/test.yml
tags:
  - just-bash
  - emulated-shell
  - node
  - bun
  - npx
  - type-stripping
  - defense-in-depth
retire_when: "Bun ships node:module registerHooks and just-bash reports level full under Bun, or just-bash publishes a self-contained build that keeps python3 and sqlite3 workers working when re-bundled; check just-bash's defense-in-depth status output under the newest Bun and the just-bash release notes"
---

# The Emulated Shell Runs just-bash Under Node From a Local npm Install, Not Bun, npx, or a Bundle

## Context

Easy agents, and agents that set `shell: emulated`, run their shell commands in [just-bash](https://github.com/vercel-labs/just-bash), a bash emulator written in TypeScript. RocketClaw embeds a small TypeScript wrapper (`internal/rocketclaw/justbash/wrapper.ts`) and a `package.json` that pins `just-bash` to `3.6.0`. At startup and on each reload, when some agent uses the emulated shell, the server writes both into `<runtime dir>/justbash/` and runs `npm install` there (`internal/rocketclaw/justbash/justbash.go:77-115`, called from `internal/rocketclaw/backend/justbash.go:15-36`). Each shell command then runs `<runtime dir>/justbash/wrapper.ts` directly, and its `#!/usr/bin/env node` first line hands it to Node; it gets the setup and the command on stdin.

The code and README say *what* happens. They do not say why the shorter-looking routes were dropped. The repo already uses Bun for the Web app, and the first request in this work was to "use bunx to run the embedded TypeScript wrapper". So the next person will reach for Bun, `npx`, or a bundle again. Each one was tried in the session that built PR #755 (merged), and each one failed or lost something.

## Guidance

Keep all three of these:

1. **Run the wrapper with Node, not Bun.**
2. **Install just-bash with `npm install` into the runtime directory, and keep `wrapper.ts` outside `node_modules`.**
3. **Use just-bash's published package as is. Do not re-bundle it.**

What was tried and why it was dropped:

| Route | What happened | Why it was dropped |
|---|---|---|
| `bun wrapper.ts` / `bunx` (Bun 1.4.4) | Everything ran, including `python3`, `js-exec`, `sqlite3` and custom commands. Bun also fetched `just-bash@3.6.0` by itself. | just-bash's defense-in-depth layer reported `"level":"best-effort"` instead of `"full"`, with reason "context-aware ESM loader hooks are unavailable". The check is `typeof nodeModuleApi?.registerHooks === "function"` (just-bash 3.6.0, `packages/just-bash/src/security/defense-in-depth-box.ts:199`). Bun has no `node:module` `registerHooks`, so the block on sandboxed code loading new code through dynamic `import()` is skipped. just-bash also declares only `"engines": {"node": ">=20.19"}`. The user picked Node so this layer stays complete. |
| `npx -y -p just-bash@3.6.0 node wrapper.ts` | `Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'just-bash' imported from …/wrapper.ts` | `npx` installs into its own hashed cache (`~/.npm/_npx/<hash>/node_modules`) and only puts the package's commands on `PATH`. Node resolves `import` from `node_modules` next to the importing file and ignores `NODE_PATH` for ESM. Importing from the npx cache path would depend on npm's private layout. |
| Wrapper packaged as its own npm package (depends on just-bash), run with `npx -y <tarball>` | With `wrapper.ts`: `ERR_UNSUPPORTED_NODE_MODULES_TYPE_STRIPPING`. With the wrapper renamed to `wrapper.mjs` it worked, about 0.55 s per call once cached. | Node will not strip TypeScript types from files under `node_modules`, and npx installs the wrapper package there. The working version needs plain JavaScript. Offered as an option, the user chose `npm install` with `wrapper.ts` instead (route below). |
| `bun build` of the wrapper plus just-bash into one bundle (an early attempt, later dropped) | It needed `--external @mongodb-js/zstd --external node-liblzma` to build. Then `python3` hung until the 30 s limit with `Cannot find module '<path>'`, and `sqlite3` failed with `sqlite3 worker not found. Run 'pnpm build' to compile the worker.` | just-bash finds its worker files relative to its own module, for example `new URL("./worker.js", import.meta.url)` (just-bash 3.6.0, `packages/just-bash/src/commands/python3/python3.ts:197`). A bundle moves `import.meta.url`, so the workers are not found. The same probe run from a plain `node_modules` install passed all four tools. |
| **Chosen:** server runs `npm install` into `<runtime dir>/justbash/`; each call runs `<runtime dir>/justbash/wrapper.ts` directly, and its `#!/usr/bin/env node` first line hands it to Node | Works under Node 22.18+ (CI uses Node 24, `.github/workflows/test.yml:119-122`). | The wrapper sits beside `node_modules`, not inside it, so Node runs the `.ts` file with no build step and resolves `just-bash` normally. Only the server installs, so two installs never race, and the server holds the result for the Web alert ("just-bash install failed: …"). A reload keeps the folder (`internal/rocketclaw/skel/skel.go:599`). |

Two related limits are already written down in the tree. Leave them alone unless you are fixing them on purpose:

- **Binary written straight to stdout comes back garbled.** just-bash's `exec()` turns stdout into text, so after that `café` and raw bytes look alike. Binary that goes to files or through pipes keeps its exact bytes, because the wrapper hands host-command output to just-bash as bytes (`bytesOutput`). The session tried four workarounds, and none of them worked or kept text correct: a group redirect, `exec >file`, `ctx.exec()`, and guessing the encoding. Exact stdout bytes need an upstream change (`wrapper.ts:130-131`, README "Emulated shell").
- **`Emulated` fails closed.** An empty `CustomShell` means host bash. So `shell: emulated`, and every easy agent, sets `Emulated: true`, and a code path that never received the wrapper fails ("shell command path is required", `internal/rocketclaw/harness/shell.go:148`) rather than quietly running on the host (`internal/rocketclaw/harness/rocketcode.go:37-39` and `withCustomShell` at `:55-58`).

## Why This Matters

- **Switching to Bun is a quiet security downgrade.** Every test still passes under Bun. The only sign is a status field nobody reads. The lost protection matters only if just-bash has an escape bug. That is exactly the case a defense-in-depth layer is there for, and just-bash runs in the same JavaScript process as the wrapper, with no VM.
- **Bundling breaks things only at run time.** The build succeeds and plain bash commands work. `python3` and `sqlite3` fail only when an agent calls them, and `python3` fails by hanging until its time limit.
- **The npx routes look like one-line wins.** One fails at import time. The other only works if the wrapper is rewritten as JavaScript and shipped as a package tarball. Without this note, someone will spend time finding that out again.

## When to Apply

- Before changing how `internal/rocketclaw/justbash` starts or installs just-bash.
- Before embedding just-bash in the binary, which is deferred work. An embedded copy must keep just-bash's published file layout, so its workers still load from `import.meta.url`.
- When upgrading Bun or just-bash. If Bun gains `registerHooks`, check that just-bash reports `"level":"full"` under Bun before you reconsider Bun (see `retire_when`).

## Examples

To check the defense-in-depth level for a runtime, run the same file with `bun status.ts` and `node status.ts`:

```ts
import { Bash, DefenseInDepthBox } from "just-bash";
await new Bash().exec("echo x");
console.log(JSON.stringify(DefenseInDepthBox.getInstance?.()?.getStatus?.()));
```

With just-bash 3.6.0, Bun 1.4.4 printed `"level":"best-effort","contextualDynamicImportProtection":false` and Node 26 printed `"level":"full","contextualDynamicImportProtection":true`.

Smoke test for any new install or packaging route. Run it from the installed location, not from a source checkout:

```ts
import { Bash } from "just-bash";
const b = new Bash({ python: true, javascript: true });
for (const c of ['python3 -c "print(1+1)"', 'js-exec -c "console.log(2*3)"', 'sqlite3 :memory: "select 7"']) {
  const r = await b.exec(c);
  console.log(c, r.exitCode, JSON.stringify(r.stdout), JSON.stringify(r.stderr));
}
```

On a plain `npm install` all three exit 0 in roughly 30-120 ms. If `python3` exits 124 after the 30 s limit, or `sqlite3` reports `worker not found`, the route broke just-bash's worker paths.

## Related

- PR #755: Rocketable/platform "internal/rocketclaw: run shell: emulated agents in just-bash" (merged)
- README "Emulated shell" section and `cmd/rocketclaw/CHEATSHEET.md` (`shell` frontmatter key)
