---
title: A Chromium Crash Dump in internal/rocketclaw/web Changes the Built Stylesheet, and jj Records It in the Commit Under Test
date: 2026-11-08
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - A Web build or test run leaves a file named core in internal/rocketclaw/web
  - The built stylesheet name in internal/rocketclaw/internal/web/dist/index.html changes although no Web source changed
  - Running make lint, make test or a Web build with the working copy (@) sitting on a real commit, such as while checking each commit of a stack
  - Splitting, rebasing or re-verifying commits that carry the built Web bundle
tags:
  - tailwind
  - web
  - dist
  - stylesheet
  - core-dump
  - chromium
  - jj-snapshot
  - working-copy
---

# A Chromium Crash Dump in internal/rocketclaw/web Changes the Built Stylesheet, and jj Records It in the Commit Under Test

## Context

The built Web bundle in `internal/rocketclaw/internal/web/dist` is committed. Every Web build rewrites it: `build.ts` empties `dist` (`build.ts:7`), runs Tailwind over `app/globals.css` (`build.ts:27`) and names the stylesheet after a hash of its content (`build.ts:33`). `make lint` and `make test` both rebuild it (`internal/rocketclaw/Makefile:43`-`44` and `:58`; `internal/rocketclaw/web/Makefile:42`).

While re-checking each commit of the sticky-sessions PR (#999), this went wrong twice in one session:

- **Nov 8.** The working copy (@) was the middle commit itself, not a new commit on top of it. A run of the full checks there crashed a Chromium renderer, which left a 220 MB `internal/rocketclaw/web/core`. The next run's build wrote `styles-ed446c921ebd971.css` instead of the `styles-a130e7492852830a.css` the same sources had produced before. The next `jj` command snapshotted the new `dist` into that commit. jj refused only the dump, because 220.3 MiB is over the 100 MiB `snapshot.max-new-file-size` set in this checkout's jj repo config (`jj config list --repo snapshot`).
- **Nov 7.** A failed browser test run left a 27 MB `core` in the same place. The next `make lint` changed the stylesheet name from `styles-a130e7492852830a.css` to `styles-f3774c483db53a6d.css`. The dump was under jj's size limit, so jj tracked it as a new file in @: it showed up in the review's changed-file list until it was deleted. `ui.tsx` had also been edited in that window, so this stylesheet change has two possible causes.

The fix both times was to delete the dump and put the known-good `dist` back. After the rebase, a build with no dump present produced `styles-45fcf796246bf0fc.css`, which matched `main`.

## Guidance

**Tailwind reads every file under `internal/rocketclaw/web`, not just `src/`.** `internal/rocketclaw/web/app/globals.css` starts with a plain `@import "tailwindcss";` (`app/globals.css:1`) and has no `@source` lines. The build runs from `internal/rocketclaw/web`, so Tailwind's automatic source detection starts there. Asking Tailwind 4.3.5's own scanner (`@tailwindcss/oxide` `Scanner`) which files it reads in this tree lists 97: `src/` and `app/`, plus `README.md`, `Makefile`, `go.mod`, `proto/`, `build.ts`, `index.html` and the JSON configs (paths relative to `internal/rocketclaw/web`). In the tests below it also read a binary file named `core`, which has no extension.

**Chromium crash dumps land in exactly that folder.** The Web browser tests run `bun test` from `internal/rocketclaw/web`. So do the Go real-browser tests, through `runWebTest`, which sets `web.Dir = "../../web"` (`internal/rocketclaw/frontend/rpc/server_test.go:4051`). Both dumps in this session were written there.

**Measured on Tailwind 4.3.5 (macOS binding, scratch copies only):**

| Setup | Result |
|---|---|
| Minimal project, plus a small `core` with `tracking-[0.37em]` between NUL bytes | That class appears in the CSS |
| Copy of the real `app/`, `src/` and top-level files, plus an 85 MB binary named `core` | CSS grows by about 10.8 KB, and its hash changes |
| Same copy, `core` deleted | Hash goes back to exactly the earlier value |
| Same copy, `README.md` removed | No change to the current CSS |
| `@import "tailwindcss" source("../src");` with the 85 MB `core` present | Same CSS as with no `core` at all |
| A `.gitignore` listing `core`, next to it | Did not stop the scan in the scratch copy. This was not tested in the real tree, where other ignore rules apply |

That shows a stray `core` in this folder is enough to change the stylesheet and its name. It does not prove the dump caused `ed446c921ebd971` specifically: that dump was deleted before anyone rebuilt the same sources with it removed. The Linux build uses a different native binding of the same Tailwind version.

**Before you trust a rebuilt `dist`, look for dumps.**

```bash
find internal/rocketclaw/web -path '*/node_modules' -prune -o -name core -type f -print
```

If one is there, delete it and build again. A stylesheet name of 15 hex characters is fine: `build.ts:33` prints the hash with `toString(16)`, which drops leading zeros.

**Build and test on a new commit, never on the commit being checked.** jj snapshots the working copy at the start of almost every command. With @ on a real commit, whatever the run leaves behind becomes part of that commit: a rebuilt `dist`, a lint autofix, or any dump under the size limit.

```bash
jj new <commit-to-check>            # @ is now an empty child
# ... run make lint / make test / the Web build ...
jj diff --git --summary             # what the run changed
jj abandon @                        # or move only the wanted changes into the commit
```

**To repair a commit that already picked this up,** restore `dist` from a commit whose Web sources match and whose bundle you trust, then re-check the commits above it:

```bash
rm internal/rocketclaw/web/core
jj restore --from <known-good> --into <commit> internal/rocketclaw/internal/web/dist
```

## Why This Matters

Nothing fails. The tests serve whichever bundle was built, the extra CSS is harmless, and the only sign is a renamed stylesheet in a `dist` diff that people usually skip as rebuild noise. So a commit that should not touch the Web bundle ends up carrying a different one, and a stacked PR gets bundles that do not match their sources. The jj size limit caught the 220 MB dump. It does not catch a smaller one: the 27 MB dump on Nov 7 was tracked without any warning.

## When to Apply

- Checking each commit of a stack, or re-checking a commit after a rebase or split.
- Any time `internal/rocketclaw/internal/web/dist/index.html` names a different `styles-*.css` while the Web sources are unchanged.
- After a browser test run that crashed, timed out or was killed.

## Examples

Not yet done (these change product files, so this capture did not make them). Whoever owns the Web build can choose:

- **Limit Tailwind to the source tree:** `@import "tailwindcss" source("../src");` in `app/globals.css`. In the scratch copy this ignored the dump and left the CSS unchanged. Before adopting it, check that no class name lives outside `src/` (for example in `index.html` or `build.ts`), then confirm the built CSS stays the same.
- **Ignore `core` in `internal/rocketclaw/web/.gitignore`:** this stops jj from tracking a dump. It is not known whether it also stops Tailwind in the real tree.
- **Turn off core dumps for test runs:** for example, `ulimit -c 0` in the Linux test container. That removes the dump at its source but also removes crash evidence.

A check that compares `dist` against a fresh build would not help on its own, because the fresh build reads the same stray file.

## Related

- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: the same `dist`, and a different trap (a stale bundle after a failed type check).
- `docs/solutions/best-practices/dupword-fix-rewrites-repeated-test-strings.md`: another check step that edits the working copy during a run.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: how `runWebTest` runs the browser tests from `internal/rocketclaw/web`.
