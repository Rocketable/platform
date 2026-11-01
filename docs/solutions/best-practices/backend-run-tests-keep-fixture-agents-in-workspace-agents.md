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

- **Agents are read once and cached.** They are not. `ListAgents` reloads them from disk on every call (`internal/rocketclaw/frontend/rpc/server.go:1204`), from the runtime directory's `agents` folder (`internal/rocketclaw/backend/bridge.go:2789`).
- **The fixture's `main.md` works, so the folder is right.** It never worked. A debug print of the remote's `ListAgents` reply showed one agent, the built-in `main`, with the built-in description, not "Remote browser fixture". The test had passed only because the built-in `main` exists.

## Guidance

`backend.Run` rebuilds the runtime directory at startup, so anything a test writes there first is gone by the time the server answers. Startup calls `skel.SyncInWithOverlays` (`internal/rocketclaw/backend/app.go:183`), which:

1. unpacks the built-in agents and cron into the workspace's own `agents/` and `cron/` without overwriting existing files (`internal/rocketclaw/skel/skel.go:78`, `overwrite` is `false`);
2. empties the runtime directory except a short keep list (`.rocketcode`, `attachments`, `sandbox`, `uploads`, `auth.json`, `auth.json.lock`, `.auth.json-*` files, and, at startup only, the overlay clones; `resetRuntimeDirectory`, `skel.go:595`, called at `skel.go:88`);
3. rebuilds it from the built-in assets, then configured overlays, then last the workspace's `agents`, `skills`, `cron`, `scripts` and `workflows` directories, each overwriting what came before (`SyncEffectiveRuntimeAssets` at `skel.go:96`, `overlayWorkspaceIn` at `skel.go:401`).

A reload (`skel.ReplaceRuntimeAssetsAfterValidation`, `internal/rocketclaw/backend/runtime.go:63`) runs steps 2 and 3 through a validated staging copy, so it wipes runtime-only fixtures too.

So when a test runs `backend.Run`, write fixture agents where startup reads them from: `<workspace>/agents/<name>.md`. Because step 1 keeps an existing workspace file and step 3 copies the workspace's `agents/` last, a fixture `agents/main.md` also replaces the built-in `main`. `pages_test.go` already writes its `main.md` there.

Tests that build the RPC `Server` directly, without `backend.Run`, can keep writing the runtime directory: nothing rebuilds it there. `reads_test.go`, `opencode_test.go` and `shadow_upload_test.go` in `internal/rocketclaw/frontend/rpc` do this correctly.

## Why This Matters

The failure is silent. A wiped fixture agent leaves the built-in agents in place, so a test that only needs some agent keeps passing and nobody notices its fixture does nothing. The cost shows up later, when someone needs the fixture to matter and spends time on caching, request paths and the Web code before suspecting the fixture.

Two `backend.Run` fixtures still write the runtime directory and are wiped the same way: `internal/rocketclaw/frontend/rpc/live_test.go:98`–`99` ("Owned browser fixture") and `live_test.go:526`–`527` ("Revert fixture"). Their tests pass on the built-in `main`. Move them to `agents/` if either test ever depends on what its `main.md` says.

## When to Apply

- Adding or changing an agent fixture in any test that calls `backend.Run`, such as the real-backend browser tests in `internal/rocketclaw/frontend/rpc`.
- A test that runs `backend.Run` can't see an agent, skill, cron job or script its fixture wrote: the same rebuild applies to `skills`, `cron`, `scripts` and `workflows`.

## Examples

Before (wiped at startup):

```go
require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), fixture, 0o600))
```

After (`internal/rocketclaw/frontend/rpc/remote_test.go:66`–`71`, abridged):

```go
// Startup builds the runtime agents from the workspace's agents directory.
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
