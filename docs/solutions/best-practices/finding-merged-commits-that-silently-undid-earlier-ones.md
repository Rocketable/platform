---
title: Finding merged commits that silently undid earlier ones on main
date: 2026-11-04
category: best-practices
module: internal/rocketclaw
problem_type: workflow_issue
component: development_workflow
severity: high
applies_when:
  - "main moved under a PR and a newly merged commit deletes a migration or removes code that recently landed"
  - A large squash-merged PR touches files that other recently merged PRs also changed
  - Checking whether every recent commit on main is still present after a suspicious merge
  - Restoring merged work that a later merge dropped
symptoms:
  - "A migration that once existed is missing from main (the migrations directory ends one number early)"
  - A recently merged feature's code, tests, README text and CONCEPTS.md entry are gone, and the commit that removed them does not mention it
  - The tests cannot catch it, because the same commit also dropped the feature's tests and its entry in the migration list test
root_cause: missing_workflow_step
resolution_type: code_fix
tags: [jj, jj-duplicate, silent-revert, squash-merge, migrations, regression, main]
---

# Finding merged commits that silently undid earlier ones on main

## Context

PR #944 ("limit listed people to their spaces in the Web") was squash-merged to `main` as `fc3cfb70`. Its commit message describes only the permissions feature. It also undid both commits of PR #939, which had merged earlier the same day:

- `19ae3e34` "list and reopen recently closed tabs", including its migration `internal/rocketclaw/backend/migrations/082_web_closed_tabs.sql`;
- `30e69b6a` "take deleted sessions off the Top Shelf".

On `main` after #944, the migrations directory ends at `081_drop_web_tab_preview.sql`, and `internal/rocketclaw/backend/store_dao.go` has no `web_closed_tabs`, `ClosedTabs` or `ExistingConversations`. `jj diff --summary -r fc3cfb70` lists `D internal/rocketclaw/backend/migrations/082_web_closed_tabs.sql`. The session that found it concluded that the PR had probably carried older copies of the files these commits touched. That was not proven.

The tests had no way to notice, because the same commit removed the safety nets too. It deleted the tests of both undone commits from `store_test.go`, `tabs_test.go`, `tabs.browser.test.ts` and `opencode.browser.test.ts`. It also removed `"082_web_closed_tabs.sql"` from the migration list that `TestInitializeSessionDBAdoptsReleasedStore` checks (`internal/rocketclaw/backend/store_schema_test.go:677-682`). A test in the tree cannot catch a revert when the reverting commit edits that test too.

Deleting an applied migration does real damage. At startup, the State Store refuses any migration recorded in its ledger that the binary does not embed (`internal/rocketclaw/backend/store_schema.go:93-95`, `unknown migration in database: …`). So a database that ran 082 under PR #939's build will not start on `main` after #944. `docs/solutions/runtime-errors/deployment-binary-missing-live-migrations.md` describes that failure.

It was found by chance. PR #956 (mark a tab unread) was being rebased onto the new `main` to claim migration 083, and the directory listing ended at 081.

## Guidance

### 1. Replay each recent commit onto main and look for leftovers

`jj duplicate <commit> --onto main@origin` re-applies one commit's changes on top of current `main`. If `main` still has everything the commit added, the duplicate is empty. If anything is left over, `main` has lost or changed that part of the commit. Abandon each duplicate right away:

```bash
# From the JJ workspace root. Never use git.
for c in $(jj log --no-graph -r 'ancestors(main@origin-, 20)' -T 'commit_id.short(12) ++ "\n"'); do
  new=$(jj duplicate "$c" --onto main@origin 2>&1 | awk '/^Duplicated/ {print $4}')
  files=$(jj diff --name-only -r "$new" | grep -v -e '/dist/' -e '/oc-dist/' | tr '\n' ' ')
  conf=$(jj resolve --list -r "$new" 2>/dev/null | grep -v -e '/dist/' -e '/oc-dist/' | wc -l | tr -d ' ')
  [ -n "$files" ] && printf '%s %-55s conflicts:%s | %s\n' "$c" \
    "$(jj log --no-graph -r "$c" -T 'description.first_line()' | cut -c1-55)" "$conf" "$files"
  jj abandon "$new" >/dev/null 2>&1
done
```

Use `jj abandon` for cleanup, not `jj op restore`. The operation log is shared by every workspace of the repo, so restoring to an earlier operation also undoes anything another workspace did in the meantime.

### 2. Sort what is left over

A non-empty duplicate does not always mean a revert. In this sweep, 5 of the 16 commits before `fc3cfb70` left something over, and only two were real reverts:

| What the duplicate shows | Meaning | Example from this sweep |
|---|---|---|
| Empty | `main` still has the whole commit | 11 of the 16 commits |
| Only built bundles (`internal/rocketclaw/internal/web/dist`, `oc-dist`) | The bundle was rebuilt since. Ignore it | `117428cd` (navigate without subscribing to the route) |
| A conflict whose added lines are mostly conflict markers, in a file a later commit rewrote | Later work replaced the change. Ignore it | `c0e70321` in `internal/rocketclaw/harness/filesystem.go`, `6b81c3b9` in `internal/rocketclaw/sandbox/rootfs_linux.go` and `internal/rocketclaw/Makefile` |
| Many real added lines in source files, tests, docs or a migration | `main` lost this work | `19ae3e34` (29 files, +717) and `30e69b6a` (10 files, +394) |

A conflict is not the dividing line. Both real reverts conflicted too: `19ae3e34` in `protocol.gen.go` and `ui.tsx`, `30e69b6a` in `tabs_test.go`, and both in the bundle. What matters is the non-generated lines that are not conflict markers. Before concluding, check one distinctive symbol from each suspect commit on the original commit and on `main`:

```bash
for spec in internal/rocketclaw/backend/store_dao.go:ClosedTabs internal/rocketclaw/frontend/rpc/tabs.go:liveShelfTabs; do
  f=${spec%%:*}; p=${spec##*:}
  echo "$f $p original=$(jj file show -r 19ae3e34 "$f" | grep -c "$p") main=$(jj file show -r main@origin "$f" | grep -c "$p")"
done
```

Here `ClosedTabs` was 3 to 0 and `liveShelfTabs` was 2 to 0, so both were real reverts. For `6b81c3b9`, `mirror` appeared in `internal/rocketclaw/sandbox/rootfs_linux.go` 4 times before PR #944 and 12 times on `main`, so it was a false alarm. `jj diff --summary -r <suspect merge> | grep '^D'` is the quickest check for deleted migrations.

### 3. Restore by replaying, not by hand

1. Duplicate the lost commits onto `main@origin` in their original order (`30e69b6a`, then `19ae3e34` on top of it), and squash them into one restore commit. Its message names the merge that undid them.
2. Resolve by hand only the conflicts in source files. Here there was one: the import list of `internal/rocketclaw/frontend/rpc/tabs_test.go`, where `main` had added `"os"` and the restore needed `"slices"`. Keep both.
3. Do not hand-merge generated files. Run `go generate ./internal/rocketclaw/frontend/rpc` for `protocol.gen.go`, and `bun run build` in `internal/rocketclaw/web` for the bundle. The merged `web.pb.go` already matched what `protoc` writes.
4. Rerun the symbol counts. They should match the original commit again. Also confirm that the migration directory and `store_schema_test.go` list `082_web_closed_tabs.sql` again.
5. Rebase the PR's own commits on top. Only their generated files conflicted, so regenerate them the same way.
6. Run the full `make test` and the whole web test suite on the exact commits you push.

The restore is the first commit of PR #956, "internal/rocketclaw: restore closed tabs and Top Shelf cleanup". The full `make test` and all 33 web test files passed on the PR's commits.

## Why This Matters

A squash-merge carries the PR's final file contents and nothing more. If a PR branch kept older copies of files that `main` has since changed, merging it quietly undoes those changes. Nothing marks this as a revert: the commit message does not mention it, review looks at the PR's own feature, and the tests still pass when the old copies come with the old tests. Here it deleted a migration, which stops any database that already ran it from starting.

Without the replay sweep, someone has to notice a missing file and then work out by hand which commits were affected. The sweep checks every recent commit in one pass, and it finds lost work beyond the migration: the Top Shelf cleanup (`30e69b6a`) dropped no migration and would have been missed.

## When to Apply

- When `main` moves under your PR and a new merge deletes a migration, or removes code, docs or tests that landed recently.
- After a large squash-merge that touches the same files as other PRs merged the same day.
- Before giving a migration a number: check that the directory on `main` still holds every migration that was ever merged.

## Prevention

- **Recommended check (not added):** a CI step that fails a PR which deletes or renames anything under `internal/rocketclaw/backend/migrations/` compared to its merge base. For example, run `jj diff --summary --from <merge base> --to <PR head> -- internal/rocketclaw/backend/migrations` and fail on any line starting with `D` or `R`. Deleting a migration is rare and needs special handling, as when migrations 001 to 059 were folded into `001_baseline.sql` (`internal/rocketclaw/backend/store_schema.go:45-64`). So the rare legitimate case can explain itself in the PR. The check is not written yet because it needs a new CI step. In-tree tests cannot do this job, because the reverting commit can edit them (as it did here).
- **In review:** treat a diff that deletes a migration, or removes entries from the migration list in `store_schema_test.go`, as a sign of a stale branch, unless the PR says why.
- **Before squash-merging a long-lived branch:** rebase it onto the current `main` and look at the per-file diff of files that `main` changed since the branch was cut. `docs/solutions/best-practices/rebasing-a-package-move-in-jj.md` describes the same kind of quiet loss in a single rebase.

## Related

- `docs/solutions/best-practices/rebasing-a-package-move-in-jj.md`: `main`'s edits are lost when a rebase keeps a stale side.
- `docs/solutions/runtime-errors/deployment-binary-missing-live-migrations.md`: what `unknown migration in database` means at startup.
- PR #939 (the undone commits), PR #944 (the merge that undid them), PR #956 (the restore).
