---
title: The Pull Requests Pane Missed PRs in jj Workspaces and in Subagent Sessions
date: 2026-11-13
category: docs/solutions/integration-issues/
module: internal/rocketclaw/web
problem_type: integration_issue
component: frontend
symptoms:
  - "On a Remote OpenCode Session, Pull requests showed: Could not read the current checkout's pull request using this OpenCode server's gh login."
  - "Running gh pr view with no branch in the session folder printed: could not determine current branch: failed to run git: not on any branch"
  - "Created in this thread listed none of the seven stacked PRs the thread had opened"
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - internal/rocketclaw/oc
tags:
  - jj
  - detached-head
  - gh
  - pull-requests
  - remote-opencode-session
  - subagent-session
  - child-sessions
  - opencode
---

# The Pull Requests Pane Missed PRs in jj Workspaces and in Subagent Sessions

## Problem

The Web **⋯ → Pull requests** pane, added in PR #1071, found nothing for a Remote OpenCode Session that had opened a stack of seven PRs (#1097 to #1103). It had two independent causes. **Current checkout** asked `gh` for the current branch's PR, and a colocated jj workspace has no current Git branch. **Created in this thread** read only the thread's own session, while a subagent had opened the PRs in its own child session, using a multi-line script that the pane rejected on purpose.

The fix changes `internal/rocketclaw/web/src/opencode-pr.ts` and `internal/rocketclaw/web/src/thread-pr.ts`. It was uncommitted and unmerged as of this writing.

## Symptoms

- The pane showed the "gh login" error from `internal/rocketclaw/web/src/thread-pr-panel.tsx:40`. The login was fine.
- `opencode-pr.ts` counts only `no pull requests found for branch` as a normal "no PR" answer (`opencode-pr.ts:48`). Every other failure becomes the same login message (`opencode-pr.ts:3`, `:49`), so the real `gh` error stayed hidden.
- In any colocated jj workspace, including the one where this doc was written, `GH_PROMPT_DISABLED=1 gh pr view --json url` exits 1 with `could not determine current branch: failed to run git: not on any branch`. This happens even when a jj bookmark points at `@`.
- **Created in this thread** was empty although the thread's history showed the PR links.

## What Didn't Work

- **Trusting the error text.** The message blamed the `gh` login. Running the command by hand in the session folder showed the real error.
- **Trusting the browser test.** The fake OpenCode server in `opencode.browser.test.ts` uses `could not determine current branch` as its "checkout unavailable" output (`opencode.browser.test.ts:24`, `:45`). So the test suite treated the normal state of every jj workspace as a rare failure.
- **A bare jj bookmark lookup.** The first candidate passed `$(jj log ... bookmarks() ...)` to `gh pr view` every time. Review showed that jj looks for its repo by walking up the directory tree. In a plain Git checkout nested inside a jj workspace, such as a clone under `<repo-root>/.tmp/` or a submodule, the lookup returned the outer workspace's bookmark instead of nothing. `gh` then looked for the wrong branch. The fix asks jj only when `git symbolic-ref -q HEAD` finds no branch. That nested case was checked by reasoning and a jj-only probe, not end to end with a real nested Git repo.
- **Pairing quotes across the whole script.** `createdPRs` blanks quoted strings before it looks for `gh pr create`. Once multi-line scripts were accepted, an apostrophe in a comment (`# don't forget`) paired with a quote several lines later and blanked the `gh pr create` between them. The fix pairs quotes within each line (`thread-pr.ts:11`).
- **Checking for `--dry-run` anywhere.** That check was written for one-line commands. In a script, a `jj git push --dry-run` before the real push discarded every PR the script created. The fix matches `--dry-run` only on the `gh pr create` invocation (`thread-pr.ts:12`).
- **Requiring exactly one URL.** The old rule accepted one simple command that printed exactly one PR URL, so a stack script could never count. The fix accepts every PR URL that a `gh pr create` command or script prints alone on a line. Per this session's testing, `gh pr create` prints its URL that way.
- **Declined as unmeasured:** caching subagent sessions by `time.updated`, a cap on parallel requests, and cancelling other requests after one fails. Each refresh rereads the whole session tree, which was about 180 requests for this 90-subagent thread. A `ponytail:` comment marks this limit (`thread-pr.ts:43`).

## Solution

**Current checkout** names the branch explicitly when Git has none (`opencode-pr.ts:46`):

```ts
// before
await run(source, "gh pr view --json url,headRefName,closedAt,mergedAt", signal);
// after
await run(source, `gh pr view $(git symbolic-ref -q HEAD >/dev/null || jj log --ignore-working-copy --no-graph -r 'latest(heads(::@ & bookmarks()))' -T 'local_bookmarks.map(|b| b.name()).join("\\n")' 2>/dev/null | head -n1) --json url,headRefName,closedAt,mergedAt`, signal);
```

- On a Git branch, `git symbolic-ref` succeeds, the substitution is empty, and `gh` keeps its old behavior.
- In a jj workspace, the substitution is the nearest bookmark at or below `@`.
- Without jj, the substitution is empty and `gh` fails as before.
- `--ignore-working-copy` keeps the lookup from snapshotting another thread's working copy.

**Created in this thread** walks the session tree one level at a time. For each session it reads that session's messages and lists its child sessions with `GET <api>/session?parentID=<id>`, paged by cursor (`thread-pr.ts:40-64`). The listing follows the upstream client's `listChildren` (`internal/rocketclaw/oc/packages/client/src/solid/data.ts:604-612`). `createdPRs` now accepts scripts, with the per-line quote pairing and the scoped `--dry-run` check described above.

The tests pin both halves:

- `opencode-pr.test.ts` checks the exact command.
- `thread-pr.test.ts` covers a stack script, the apostrophe and dry-run cases, a nested subagent, and a subagent that appears only on the second page of children.

On the real data, the lookup found #1103 under `sh` and `zsh`, and `createdPRs` returned all seven PRs from the recorded script.

## Why This Works

- **Current checkout:** jj keeps Git on a detached HEAD, so any tool that asks Git for the current branch fails in a jj workspace. The branch name lives in jj's bookmarks, so the command has to read it there and pass it to `gh` explicitly.
- **Created in this thread:** in OpenCode, a subagent is a separate session that records its parent. Its tool calls never appear in the parent's message history. Work done "in this thread" is therefore the whole session tree, not the root session.

## Prevention

- **jj workspaces:** for any command run in a folder that may be a jj workspace, never rely on Git's current branch. That includes `gh pr view`, `gh pr status` and `gh pr checks` without a branch argument, `gh pr create` without `--head`, `git branch --show-current`, and `git rev-parse --abbrev-ref HEAD`. Pass the branch explicitly from jj bookmarks. Guard the jj lookup so a plain Git checkout keeps its own branch.
- **Session history:** anything that scans an OpenCode session's history for what the thread did must also walk its child sessions through `parentID`, reading every page. The same applies to commands run, files changed, and PRs opened.
- **Test fixtures:** do not use a real environment's normal state as a fixture's error case. If the fake server's "unavailable" output had come from a real failure, the jj case would have needed its own fixture.
- **Error messages (proposal, not done):** show the tool's own error text instead of one fixed explanation. Here, `not on any branch` would have pointed straight at the cause. The pane still shows the login message for any failure.
- **Known limit:** the bookmark lookup finds only the top PR of a stack (#1103 here). The rest of the stack appears only under **Created in this thread**.
- **Verification:**
  - A browser test that runs while `internal/rocketclaw/web/src/` is being edited can test an older bundle. See `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`.
  - The `frontend/rpc` Go tests need a test database. See `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`.
  - Several browser test files timed out under load from other workspaces' builds. They passed when run one file at a time.

## Related Issues

- PR #1071 added the pane and both defects.
- `docs/solutions/architecture-patterns/web-acts-on-remote-opencode-sessions-through-the-api-proxy.md` covers the `/oc/` API proxy that the pane calls.
- `docs/verification/thread-pr/README.md` covers the bounded model of thread PR discovery. The model covers one session and does not include the walk through subagent sessions.
