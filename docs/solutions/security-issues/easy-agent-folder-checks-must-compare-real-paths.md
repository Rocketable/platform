---
title: Symlinks slipped past easy-agent folder checks that compared names, not real paths
date: 2026-10-31
category: docs/solutions/security-issues/
module: internal/rocketclaw/backend
problem_type: security_issue
component: service_layer
symptoms:
  - "An easy agent's workingDirectory could hold the real target of a symlinked agents/ folder, so the agent could write its own agent files"
  - "A bash workdir inside a read-only skill or upload folder named through a symlink failed with \"is outside this agent's folders\""
  - A path check passes or fails depending on whether some folder in it happens to be a symlink
root_cause: logic_error
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/justbash
tags: [symlink, path-containment, sandbox, easy-mode, working-directory, just-bash, realpath, dangling-link]
---

# Symlinks slipped past easy-agent folder checks that compared names, not real paths

## Problem

An easy agent is confined to its `workingDirectory`, plus read-only uploads and skill folders. Its folder may not be, contain or lie inside the folders that define agents, skills, cron jobs, scripts and workflows, or the agent could rewrite its own rights. The checks that enforce this compared some paths by their real location and others by the name they were given. Wherever a symlink sat on the "name" side, the check gave the wrong answer. One change (PR #822, unmerged as of this writing) hit this three times: `resolveWorkingDirectory` compared the definitions folders by name and had no "contains" test, and the emulator's workdir check in `wrapper.ts` compared a real path with mount names.

## Symptoms

- With a workspace's `<workspace>/agents` a symlink to `/srv/shared/agents`, `workingDirectory: /srv/shared` passed the check. The agent could then write `/srv/shared/agents/new.md`, into the folder RocketClaw reads agent definitions from through that link.
- A bash `workdir` inside a read-only skill folder that was named through a symlink failed with `workdir … is outside this agent's folders`, though it was inside.
- Nothing in normal tests showed either one: test workspaces come from `filepath.EvalSymlinks(t.TempDir())`, so they contain no links.

## What Didn't Work

- **Resolving only the agent's folder and the workspace.** `resolveWorkingDirectory` already called `filepath.EvalSymlinks` on both, then compared the real folder with `filepath.Join(workspace, "agents")` and the other names. The definitions folders were never resolved, so a link out of the workspace was invisible to the check.
- **Checking only "the folder lies inside a refused folder".** Once a refused folder can live outside the workspace, a working folder can also *contain* it (`/srv/shared` contains `/srv/shared/agents`). The old check had no reason to test that direction, because a folder that contains `<workspace>/agents` must contain the workspace, which was already refused.
- **Falling back to the plain path when resolving fails.** The first idea was "resolve the folder if it exists, else use its name". A dangling link (`cron -> /srv/shared/cron`, target not yet made) fails to resolve, so the check would use `<workspace>/cron` and pass `/srv/shared`. The agent can then create `/srv/shared/cron` itself and write into the folder the `cron` link names.
- **Comparing the shell's working directory with mount names.** In `wrapper.ts`, `process.cwd()` returns the real path (the OS resolves links), but read-only folders are mounted where they are named. A `within(process.cwd(), dir)` test against the named path fails for a linked folder; using the real path instead would make just-bash start in a path that is not mounted.

## Solution

Compare in one space (real paths), fail closed when a link cannot be resolved, and hand back paths in the space the consumer uses.

Folder check, `internal/rocketclaw/backend/easy_turn.go:128-142`:

```go
// A definitions folder counts where it really is, which a link may put out of the workspace; a missing
// one, where it would be made.
refused := []string{cfg.RuntimeDirName(), "agents", "skills", "cron", "scripts", "workflows"}
for _, name := range refused {
	dir := filepath.Join(workspace, name)
	if _, errStat := os.Lstat(dir); !errors.Is(errStat, fs.ErrNotExist) {
		if dir, err = filepath.EvalSymlinks(dir); err != nil {
			return "", fmt.Errorf("workingDirectory: %w", err)
		}
	}

	if within(workspace, folder) || within(folder, dir) || within(dir, folder) {
		return "", fmt.Errorf(...)
	}
}
```

- `os.Lstat` succeeds for a dangling link, so the link is resolved, the resolution fails, and the turn fails. Only a folder that truly does not exist is checked by its name, which is where it would be created.
- `within(dir, folder)` adds the "contains" direction.

Working directory in the emulator, `internal/rocketclaw/justbash/wrapper.ts:106-113`:

```ts
// The OS reports the workdir's real path; a folder mounts where it is named.
const cwd = (() => {
  for (const dir of [folder, ...(config.readOnly ?? [])]) {
    const real = realpathSync(dir);
    if (within(process.cwd(), real)) return join(dir, relative(real, process.cwd()));
  }
  return easy ? fail(`workdir ${process.cwd()} is outside this agent's folders`) : folder;
})();
```

Tests: `TestResolveWorkingDirectory` (`internal/rocketclaw/backend/easy_turn_test.go:80-95`) covers a definitions folder linked out of the workspace (be, contain, inside) and a dangling link; `TestJustBash` covers "a workdir in a symlinked read-only folder is used" (`internal/rocketclaw/justbash/justbash_test.go:185`).

## Why This Works

A containment check is only as strong as the weaker side of the comparison. `EvalSymlinks` on one side and a joined name on the other answers "is this name under that name", not "can this agent write there". Resolving both sides makes the answer about locations on disk. A dangling link must fail rather than fall back, because the fallback answers for a folder the agent can create next. Presentation is a separate step: just-bash mounts folders under their given names, so a real path is converted back into that name only after the check.

## Prevention

- In any sandbox path check, resolve **every** path in the comparison, not just the one the caller supplies. Grep for `filepath.Join(workspace,` or `realpathSync(config.` next to a `within(`/`IsLocal` test and ask whether the other side was resolved.
- Test both directions: the folder inside a refused folder, and the folder containing one. Once links exist, "contains" is no longer implied by "contains the workspace".
- Treat "cannot resolve" as refuse, never as "use the name". Ask whether the agent could create the missing target.
- Keep the check space (real paths) and the presentation space (names the user, prompt or mount uses) apart, and convert at the boundary.
- Test fixtures should include a symlinked folder on purpose: `EvalSymlinks(t.TempDir())` removes links, so ordinary fixtures never exercise this. Make the link deliberately, as `TestResolveWorkingDirectory` does with `root.Symlink`.

## Related Issues

- `docs/solutions/best-practices/just-bash-runs-under-node-from-a-local-npm-install.md` (the wrapper this check lives in)
