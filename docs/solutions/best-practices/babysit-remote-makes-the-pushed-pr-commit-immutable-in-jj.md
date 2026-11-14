---
title: A babysit remote makes the pushed PR commit immutable in jj, so later fixes miss the PR
date: 2026-11-13
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - Fixing or rebasing a PR after ce-babysit-pr started watching it from the same jj workspace
  - jj prints "The working-copy commit became immutable; a new commit has been created on top of it." after a fetch
  - jj git push --bookmark reports nothing to push although the PR's fix was just made
  - jj squash, jj rebase, or jj describe refuses with "would rewrite 1 immutable commits" on a PR commit
retire_when: "The ce-babysit-pr intake-overlap script stops leaving an untracked babysit-pr-N-head remote bookmark on the PR head; check its scripts/intake-overlap for the remote add and fetch, and check jj git remote list after a babysit run"
tags:
  - jj
  - immutable
  - babysit
  - remote-bookmark
  - squash
  - rebase
  - pull-request
---

# A babysit remote makes the pushed PR commit immutable in jj, so later fixes miss the PR

## Context

When `ce-babysit-pr` starts on a PR, its intake check (`scripts/intake-overlap` in the skill, not in this repository) adds a remote named `babysit-pr-<N>-head` for the PR's head repository and fetches the PR branch through it:

```
jj git remote add babysit-pr-1126-head https://github.com/Rocketable/platform.git
jj git fetch --remote babysit-pr-1126-head --branch web-right-pane-shortcut
```

The fetch creates an untracked remote bookmark, `web-right-pane-shortcut@babysit-pr-1126-head`, on the pushed PR commit. This repository sets no `revset-aliases`, so jj uses its built-in `immutable_heads()`, which is `trunk() | tags() | untracked_remote_bookmarks() | untracked_remote_tags()` (`jj config list --include-defaults`, jj 0.47.1). The PR commit becomes immutable.

When the workspace's `@` is that PR commit, the fetch also moves `@`. The operation that did it (`jj op show` on PR #1126's fetch) shows `@` leaving the PR commit for a new empty change, and jj prints:

```
Warning: The working-copy commit became immutable; a new commit has been created on top of it.
```

Babysit prints its own output, and this warning is easy to miss. Every edit after it lands in the new child change, while the PR bookmark stays on the old commit. So `jj git push --bookmark <pr-branch>` pushes nothing new, and the fix never reaches the PR. On PR #1126 a documentation fix was made this way and was caught only because the bookmark sat on a different commit from the edited one.

Babysit never removes the remote; it stays until someone runs `jj git remote remove`. On 2026-11-13 this repository had leftover remotes for six PRs (`babysit-pr-{985,1010,1076,1097,1117,1126}-head`), so a branch name fetched through one of them stays immutable until something changes that.

## Guidance

**After babysit starts, check where `@` is before editing a PR.** If `jj log -r '@ | @-'` shows an empty `@` whose parent carries the PR bookmark, edits will go to the child, not to the PR commit.

**To fold a fix into the PR commit, squash with `--ignore-immutable`.** The commit is immutable only because of the babysit remote bookmark, not because it is on `main`:

```
jj squash --ignore-immutable --from @ --into <pr-change> --use-destination-message
jj git push --remote origin --bookmark <pr-branch>
```

The local bookmark follows the rewritten commit, and the push moves the branch sideways (a force push in Git terms). `jj rebase -b @ -d main@origin` needs the same flag when `main` moves under the PR.

**Or make the commit mutable again first.** Both of these clear immutability. They were checked in a throwaway repository under `.tmp/` with jj 0.47.1:

- `jj bookmark track <pr-branch> --remote babysit-pr-<N>-head`: a tracked remote bookmark is not in `untracked_remote_bookmarks()`.
- `jj git remote remove babysit-pr-<N>-head`: removes the remote's bookmarks with it. The next intake check, from a fresh babysit run or a mid-watch update request, adds the remote back and fetches again, which makes the commit immutable again.

Neither moves `@` back. Run `jj edit <pr-change>` or squash as above.

**Before pushing, compare the bookmark with the change you edited.** `jj diff --stat --from <pr-branch>@origin --to <pr-branch>` must show the fix. An empty diff means the edits are still in the child change.

## Why This Matters

The failure is silent. The push succeeds or reports nothing to do, CI goes green on the old commit, and the merged PR is missing the fix. Describing the child change and moving the PR bookmark onto it also works, but it adds a second commit, which breaks this repository's habit of one commit per PR scope. A plain `jj squash`, `jj rebase`, or `jj describe` on the PR commit fails with `Error: Commit <id> is immutable` and the hint "This operation would rewrite 1 immutable commits." That looks like a protected `main` commit and invites the wrong fix, such as editing `immutable_heads()` or abandoning work.

## When to Apply

- Any edit, rebase, or conflict fix on a PR while or after `ce-babysit-pr` watched it from this workspace.
- Any PR branch whose name was ever fetched through a `babysit-pr-*` remote. Check with `jj bookmark list --all-remotes <pr-branch>`.

## Examples

PR #1126, after babysit's intake fetch (historical commit IDs; the PR was later rebased and merged, so they are not on `main`):

```
@  lorqronx 42f0e758            (child; held the doc fix)
◆  mlmnosvr 10d10074 web-right-pane-shortcut web-right-pane-shortcut@babysit-pr-1126-head
```

`◆` marks the immutable PR commit. Squashing the child into it with `--ignore-immutable` produced one commit, and `jj diff --stat --from web-right-pane-shortcut@origin --to web-right-pane-shortcut` then showed only the doc fix before the push.

A throwaway repository reproduces it: `jj edit feat`, then `jj git remote add extra <same URL>` and `jj git fetch --remote extra --branch feat`. `feat` turns immutable and `@` moves to a new empty child. `jj bookmark track feat --remote extra` makes it mutable, `jj bookmark untrack` makes it immutable again, and `jj git remote remove extra` makes it mutable.

## Related

- `docs/solutions/best-practices/crash-dumps-in-web-change-the-built-stylesheet.md`: another case where the position of `@` decides which commit gets changes. Its advice to move only the wanted changes into a commit needs `--ignore-immutable` when that commit is a babysat PR head.
- `docs/solutions/best-practices/rebasing-a-package-move-in-jj.md`: how bookmarks follow `jj rebase` onto `main@origin`.
- `docs/solutions/best-practices/finding-merged-commits-that-silently-undid-earlier-ones.md`: the operation log is shared by every workspace, so undo with `jj abandon` rather than `jj op restore`.
