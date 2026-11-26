---
title: Tests That Start backend.Run Must Put Fixture Agents in the Workspace's agents Directory
date: 2026-10-31
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/rpc
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - A Go test starts a real RocketClaw with backend.Run and needs an agent of its own
  - A test fixture writes agent files under cfg.RuntimeDirName()
  - ListAgents, an agent picker, or an agent filter in a test never shows an agent the fixture wrote
symptoms:
  - ListAgents returns only the built-in main agent, never the agent the fixture wrote
  - A browser test waiting for a fixture agent's name in the Web page times out
  - "A fixture's own main.md (description: ... fixture) never takes effect, while the test still passes"
root_cause: test_isolation
resolution_type: test_fix
tags: [test-fixtures, agents, runtime-dir, skel, backend-run, browser-tests, listagents]
---

# Tests That Start backend.Run Must Put Fixture Agents in the Workspace's agents Directory

## Context

`TestRemoteRocketClawRealBrowser` needed an agent, `scout`, that only the remote RocketClaw has, to check that Cmd+P suggests remote agents. Its fixture already wrote `main.md` into the runtime directory, `<workspace>/<cfg.RuntimeDirName()>/agents/` (default `.rocketclaw`). The `scout.md` written beside it never appeared: the browser test timed out waiting for `scout`, with or without the Web change it was testing.

What did not explain it:

- **Agents are read once and cached.** They are not. `ListAgents` reloads them from disk on every call (`internal/rocketclaw/frontend/rpc/server.go:1314`, through `backend.LoadRuntimeDefinitions`), from the runtime directory's `agents` folder (`internal/rocketclaw/backend/bridge.go:2567`).
- **The fixture's `main.md` works, so the folder is right.** It never worked. A debug print of the remote's `ListAgents` reply showed one agent, the built-in `main`, with the built-in description, not "Remote browser fixture". The test had passed only because the built-in `main` exists.

## Guidance

`backend.Run` rebuilds the runtime directory at startup, so anything a test writes there first is gone by the time the server answers. Startup calls `skel.SyncInWithOverlays` (`internal/rocketclaw/backend/app.go:419`), which:

1. empties the runtime directory except a short keep list (`.rocketcode`, `attachments`, `sandbox`, `uploads`, `auth.json`, `auth.json.lock`, `.auth.json-*` files, and, at startup only, the overlay clones; `resetRuntimeDirectory`, called from `SyncInWithOverlays` in `internal/rocketclaw/skel/skel.go`);
2. rebuilds it from the built-in assets, then configured overlays, then last the workspace's `agents`, `skills`, `cron` and `scripts` directories, each overwriting what came before (`SyncEffectiveRuntimeAssets`, `overlayWorkspaceIn`).

A reload (`skel.ReplaceRuntimeAssetsAfterValidation`, `internal/rocketclaw/backend/runtime.go:64`) runs both steps through a validated staging copy, so it wipes runtime-only fixtures too.

So when a test runs `backend.Run`, write fixture agents where startup reads them from: `<workspace>/agents/<name>.md`. Startup only creates the workspace's `agents/` when it is missing, and step 2 copies it last.

Never name a fixture agent `main`. That is a built-in agent that every load takes from the binary (`readRocketCodeDefinitionsIn`, `internal/rocketclaw/backend/bridge.go:2838`), and step 2 skips `agents/main.md` and `agents/main/` in overlays and the workspace with a warning (`builtinAgentFilter` in `skel.go`). Such a fixture is ignored wherever it is written, and the test runs against the built-in agent. Give the fixture another name and use that name in the test. To change a built-in agent's model or turn it off, set `cfg.EmbeddedAgents`.

Tests that build the RPC `Server` directly, without `backend.Run`, can keep writing the runtime directory: nothing rebuilds it there. `reads_test.go`, `opencode_test.go` and `shadow_upload_test.go` in `internal/rocketclaw/frontend/rpc` do this correctly.

## Why This Matters

The failure is silent. A wiped fixture agent leaves the built-in agents in place, so a test that only needs some agent keeps passing and nobody notices its fixture does nothing. The cost shows up later, when someone needs the fixture to matter and spends time on caching, request paths and the Web code before suspecting the fixture.

The "Owned browser fixture" and "Revert fixture" agents in `internal/rocketclaw/frontend/rpc/live_test.go` show the fix: they used to write a `main.md` into the runtime directory, where the rebuild wiped them and their tests quietly ran on the built-in `main`. They now write `agents/primary.md` in the workspace, so the fixture is the agent the test runs.

## When to Apply

- Adding or changing an agent fixture in any test that calls `backend.Run`, such as the real-backend browser tests in `internal/rocketclaw/frontend/rpc`.
- A test that runs `backend.Run` can't see an agent, skill, cron job or script its fixture wrote: the same rebuild applies to `skills`, `cron` and `scripts`.

## Examples

Before (wiped at startup):

```go
require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), fixture, 0o600))
```

After (`internal/rocketclaw/frontend/rpc/remote_test.go:64`–`68`, abridged):

```go
require.NoError(t, root.MkdirAll("agents", 0o700))
for _, agent := range agents {
	require.NoError(t, root.WriteFile(filepath.Join("agents", agent+".md"), fixture, 0o600))
}
```

The remote is started with `serve("Remote reply", "scout")` and the local server with `serve("Local reply")`, so only the remote lists `scout`.

## Related

- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: the setup these same `backend.Run` browser tests need in a fresh workspace.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: another way a browser test checks something other than what its author thinks.
- `docs/solutions/best-practices/rocketcode-strings-that-must-survive-the-harness-rename.md`: another entry on the runtime directory's keep list.
