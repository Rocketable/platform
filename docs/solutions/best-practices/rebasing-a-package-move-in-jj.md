---
title: Rebasing a package move in JJ drops main's edits to the moved files
date: 2026-10-29
category: docs/solutions/best-practices/
module: internal/rocketclaw/harness
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - Rebasing a change that moves or renames a Go package while main keeps editing files in the old location
  - Resolving JJ conflicts by keeping the destination side and re-running a mechanical rewrite
  - Restoring a local recovery bookmark after `jj rebase`
tags:
  - jj
  - rebase
  - package-move
  - rename
  - conflicts
  - modify-delete
  - bookmark
---

# Rebasing a package move in JJ drops main's edits to the moved files

## Context

The RocketCode dissolution moved `internal/rocketcode` to `internal/rocketclaw/harness` and rewrote every import of it. While that change waited for review, `main` changed files under `internal/rocketcode` twice: 5 files in one commit and 10 in another. Each `jj rebase -s <change> -d main@origin` reported those files as `2-sided conflict including 1 deletion`. JJ's rebase does not follow renames (`jj diff` may still display some as `R`), so the move merges as a deletion plus an addition and `main`'s edits to the old path have nowhere to land: the conflict sits on the deleted path, and the moved copy under the new path keeps the old content.

The same rebases also conflicted in files that only changed imports or wording (backend Go files, `CONCEPTS.md`, `cmd/rocketclaw/CHEATSHEET.md`, two `docs/solutions` learnings per rebase, and the built web bundle). Resolving those by keeping `main`'s side and re-running the rewrite looked clean, built, and passed tests. It still left stale content: after one rebase, two learnings were back on line-number citations that `main` had already corrected (`rocketcode.go:439` instead of `477`, `bridge.go:2159-2164` instead of `2131-2136`). Nothing failed. A per-file comparison against `main` found them.

## Guidance

**1. Keep the mechanical rename as a script, and replay it on every rebase.** Hand edits made during the first pass are lost the next time you take `main`'s side. The script must be idempotent so it can run over the whole tree:

```bash
files=$(grep -rlE --include='*.go' 'internal/rocketcode|\brocketcode\.[A-Z]|^package rocketcode$' cmd internal || true)
[ -n "$files" ] || exit 0   # nothing left to rewrite; without this, perl reads stdin
perl -pi -e '
  s#internal/rocketcode/codemode"#internal/rocketclaw/codemode"#;
  s#internal/rocketcode/mcpclient"#internal/rocketclaw/mcpclient"#;
  s#internal/rocketcode"#internal/rocketclaw/harness"#;
  s/^package rocketcode$/package harness/;
' $files
for f in $(grep -l '"github.com/Rocketable/platform/internal/rocketclaw/harness"' $files); do
  gofmt -r 'rocketcode.a -> harness.a' -w "$f"   # code only, not strings or comments; also hits a local named rocketcode
done
```

Keep it to import paths, package clauses, and qualified identifiers: a broader `rocketcode` replace would hit the stored and external strings listed in the related learning below. Keep a separate list of the few non-mechanical edits (reworded comments, prose) so they can be replayed too.

**2. Port each moved file from `main`, after proving the move was mechanical.** For every `including 1 deletion` conflict on the old path:

```bash
# The moved file must differ from its old base only by the rename and your non-mechanical-edit list.
diff <(jj file show -r <old-base> internal/rocketcode/X.go) \
     <(jj file show -r <pr-commit> internal/rocketclaw/harness/X.go)
# Then take main's version, put it at the new path, and drop the old one.
jj file show -r main@origin internal/rocketcode/X.go > internal/rocketclaw/harness/X.go
rm internal/rocketcode/X.go
```

Then re-run the script and replay the non-mechanical edits for that file, because `main`'s copy does not have them. Check that `main` added no new files under the old path (`jj diff --summary --from <old-base> --to main@origin internal/rocketcode | grep '^A'`), and that nothing is left there afterwards (`jj file list internal/rocketcode` is empty); then delete the empty directories. Run `jj file show` from the workspace root, or use a `root:"path"` fileset: plain paths resolve against the current directory, and redirecting a failed `jj file show` truncates the target file.

**3. Do not trust a clean resolution of text files: diff every Markdown file the change touches against `main` with the rewrite applied.** After the rebase, the only differences left should be the ones the change intends:

```bash
for p in $(jj diff --summary -r <pr-commit> | awk '$1 == "M" && /\.md$/ {print $2}'); do
  diff <(jj file show -r main@origin "$p" | perl -pe '
    s#internal/rocketcode/codemode#internal/rocketclaw/codemode#g;
    s#internal/rocketcode/mcpclient#internal/rocketclaw/mcpclient#g;
    s#internal/rocketcode/docs/plans#docs/plans#g;
    s#internal/rocketcode\b#internal/rocketclaw/harness#g') "$p"
done
```

Every remaining hunk should be one you can name: a deliberate rewording, a line shift the change itself causes, a moved file's new path. Anything else is stale text from the old version of the branch.

**4. Put back recovery bookmarks after the rebase.** `jj rebase` moves every local bookmark that points at a rewritten commit, including one created only to keep the pre-rebase head. Set it back with `jj bookmark set <name> -r <old-commit-id> --allow-backwards`. The change then shows as divergent while both versions are visible, so refer to its commits by commit ID until the bookmark is deleted.

## Why This Matters

A move PR that sits while `main` keeps changing the old package can lose `main`'s work quietly. The build catches lost API additions that other packages use: in this PR, `main`'s callers of a new `ReviewTask` method and a new `Emulated` field would not have compiled against the stale moved copies. Changes inside existing function bodies compile fine and vanish. The stale-doc case is quieter still: citations point at the wrong lines and no check reads them; only the per-file comparison found it.

## When to Apply

- Rebasing any change that moves files or renames a package, when `main` has edited the old paths since the branch was cut.
- Resolving conflicts by keeping one side and replaying a rewrite over it.
- Any long-lived mechanical rename, before each push.

## Examples

What the move looks like to `jj rebase` when `main` edited one of the moved files:

```text
internal/rocketcode/tasks.go        2-sided conflict including 1 deletion
```

The fix for that file: confirm `diff` shows only `package rocketcode` -> `package harness` (and import paths), copy `main`'s `internal/rocketcode/tasks.go` to `internal/rocketclaw/harness/tasks.go`, delete the old path, and re-run the script.

## Related

- `docs/solutions/best-practices/rocketcode-strings-that-must-survive-the-harness-rename.md`: which `rocketcode` strings the rewrite must not touch.
