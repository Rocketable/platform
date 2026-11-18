---
title: The Pull Requests Pane Missed PRs in jj Workspaces and in Subagent Sessions
date: 2026-11-13
last_updated: 2026-11-18
category: docs/solutions/integration-issues/
module: internal/rocketclaw/web
problem_type: integration_issue
component: frontend
symptoms:
  - "On a Remote OpenCode Session, Pull requests showed: Could not read the current checkout's pull request using this OpenCode server's gh login."
  - "Running gh pr view with no branch in the session folder printed: could not determine current branch: failed to run git: not on any branch"
  - "Created in this thread listed none of the seven stacked PRs the thread had opened"
  - "In native threads the pane missed PRs that subagents or shadows opened, and any PR link printed before a command's last output line"
  - "The Go classifier recorded nothing for gh pr create --body \"$(cat <<'EOF' ... EOF)\" when the body held double-quoted text such as \"gh api\""
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - internal/rocketclaw/harness
  - internal/rocketclaw/oc
tags:
  - jj
  - gh
  - pull-requests
  - subagent-session
  - opencode
  - native-history
  - heredoc
  - shared-test-cases
---

# The Pull Requests Pane Missed PRs in jj Workspaces and in Subagent Sessions

## Problem

The Web **⋯ → Pull requests** pane (added in PR #1071) missed pull requests in two rounds.

1. **2026-11-13, Remote OpenCode Sessions.** A session that opened a stack of seven PRs (#1097 to #1103) showed none. **Current checkout** asked `gh` for the current branch's PR, and a colocated jj workspace has no current Git branch. The thread's list read only the thread's own session, while a subagent had opened the PRs in its own child session.
2. **2026-11-18, native threads.** The pane read only the thread's own `/api/History`, never its subagents or shadows. Worse, a native History record cannot hold the evidence: it keeps a bash call's arguments clipped at 4 KiB and only the last output line, cut to 200 bytes. So the classification moved into the harness, which left the same shell classifier in two languages. The two ports then disagreed on the most common way agents open PRs.

The second round is on the bookmark `pr-pane-subagents-markdown-quoting` (plan `docs/plans/2026-11-18-0825-feat-pr-pane-subagents-markdown-quoting-plan.md`). As of this writing it has no pull request open.

## Symptoms

- The pane showed the "gh login" message from `internal/rocketclaw/web/src/thread-pr-panel.tsx:71`. The login was fine. `opencode-pr.ts` counts only `no pull requests found for branch` as a normal "no PR" answer (`opencode-pr.ts:48`); every other failure throws (`:49`), and the pane shows the same fixed message, so the real `gh` error stayed hidden.
- In any colocated jj workspace, `GH_PROMPT_DISABLED=1 gh pr view --json url` exits 1 with `could not determine current branch: failed to run git: not on any branch`, even when a jj bookmark points at `@`.
- A Remote OpenCode thread's list was empty although its history showed the PR links.
- In a native thread, a PR a subagent or shadow opened never appeared. A PR the thread itself opened appeared only when its URL was the last output line. `gh pr create` often prints a warning after the URL, such as `Warning: 1 uncommitted change`.
- After the classifier moved into Go, `gh pr create --title "Add x" --body "$(cat <<'EOF' … Use "gh api" here … EOF)"` recorded no PR at all in Go, while the TypeScript port marked it created.

## What Didn't Work

Round 1 (OpenCode):

- **Trusting the error text.** The message blamed the `gh` login. Running the command by hand in the session folder showed the real error.
- **Trusting the browser test.** The fake OpenCode server in `opencode.browser.test.ts` uses `could not determine current branch` as its "checkout unavailable" output (`opencode.browser.test.ts:24`, `:45`). So the suite treated the normal state of every jj workspace as a rare failure.
- **A bare jj bookmark lookup.** jj finds its repo by walking up the directory tree. In a plain Git checkout nested inside a jj workspace, such as a clone under `<repo-root>/.tmp/`, the lookup returned the outer workspace's bookmark. The fix asks jj only when `git symbolic-ref -q HEAD` finds no branch. That nested case was checked by reasoning and a jj-only probe, not end to end.
- **Pairing quotes across the whole script, and `--dry-run` anywhere.** Once multi-line scripts were accepted, an apostrophe in a comment (`# don't forget`) paired with a quote lines later and blanked the `gh pr create` between them, and a `jj git push --dry-run` before the real push discarded every created PR. Both rules are now cases in the shared table below ("an apostrophe in a comment", "an earlier dry-run push").
- **Declined as unmeasured:** caching subagent sessions by `time.updated`, capping parallel requests, and cancelling other requests after one fails. Each refresh rereads the whole tree, about 180 requests for a 90-subagent thread.

Round 2 (native threads and the two ports):

- **Reading the History record in the browser.** A record's `Summary` is the last non-empty output line cut to 200 bytes (`internal/rocketclaw/harness/public_progress.go:182-193`). Its `Arguments` are clipped to `maxCodeModeCallArguments = 4 << 10` and end in `…[truncated]` (`:88`, `:148-153`), so `JSON.parse` fails on a long `gh pr create --body …` and the browser skips the call. A plan review proposed recovering the command prefix from the clipped JSON. An endpoint returning full outputs was rejected because it would ship whole outputs to the browser to find a few URLs (plan, "Alternatives Considered"). Keeping the unclipped command in the harness made the prefix recovery unnecessary.
- **Keeping the old "created" rule unchanged.** It counted every PR URL alone on a line when any `gh pr create` ran. A script that runs `gh pr list` and then `gh pr create` would then count listed PRs as created, and the pane opens itself on created PRs. Review replaced it with t3code's `changeRequestsCreatedBy` guards (below).
- **Quotes before heredocs in Go.** The first Go `shellCode` blanked quoted strings first. In `--body "$(cat <<'EOF' … "gh api" … EOF)"` the opening `"` paired with the quote before `gh api`, which left `gh api` looking like a command. The listing rule then dropped everything. TypeScript removed heredoc bodies first and got it right.
- **Separate case lists per port.** Nine quoting, comment and dry-run cases lived only in `thread-pr.test.ts`, so Go's branches for them were untested. Review also reproduced a second divergence: TypeScript stripped only the first of two heredocs opened on one line (`cat <<'A' <<'B'`), while Go queued both.
- **Considered, not built:** a "checked" flag on every successful bash call, so the browser could tell an old record from a new one that found nothing. The record fields use `omitempty`, so the two look alike, and the browser runs its last-line fallback on both. It was left as a known limit.

## Solution

### Current checkout names the branch when Git has none

`opencode-pr.ts:46`:

```ts
// before
await run(source, "gh pr view --json url,headRefName,closedAt,mergedAt", signal);
// after
await run(source, `gh pr view $(git symbolic-ref -q HEAD >/dev/null || jj log --ignore-working-copy --no-graph -r 'latest(heads(::@ & bookmarks()))' -T 'local_bookmarks.map(|b| b.name()).join("\\n")' 2>/dev/null | head -n1) --json url,headRefName,closedAt,mergedAt`, signal);
```

On a Git branch the substitution is empty and `gh` behaves as before. In a jj workspace it is the nearest bookmark at or below `@`. `--ignore-working-copy` keeps the lookup from snapshotting another thread's working copy.

### Native bash calls are classified in the harness

When a bash call finishes, the harness classifies the full command and the full output, before the summary is cut (`public_progress.go:178-180`). The full command is kept in a private field taken before the arguments are clipped (`:137-146`) and cleared in `finish` (`:157-158`). The result is stored on the call as `created_pull_requests` and `mentioned_pull_requests` (`:102-103`), each capped at 100 because the record is stored (`:200`, `:253`, `:258`).

### One rule, two ports, one case table

`pullRequestsIn` exists in Go (`public_progress.go:214-259`) and TypeScript (`internal/rocketclaw/web/src/thread-pr.ts:18-35`). TypeScript is still needed for Remote OpenCode Sessions, whose full output the browser reads through the `/oc/` proxy (`thread-pr.ts:68-83`), and for native records saved before this change, which fall back to the summary line (`thread-pr.ts:96-99`). The rule follows t3code's `changeRequestsCreatedBy`, plus mentions:

- A command that runs `gh pr list`, `gh pr status`, `gh search prs` or `gh api` records nothing.
- Created: a `gh pr create` that runs as a command and is not a dry run, and PR URLs alone on an output line. A URL whose number another command in the script names is not created. When there are more such URLs than create commands, none is created.
- Mentioned: every other PR URL in the output, normalized to `https://github.com/<owner>/<repo>/pull/<n>`.

Both ports run every row of `internal/rocketclaw/harness/testdata/pr-discovery-cases.json`: `TestCodeModeCallPullRequests` (`public_progress_test.go:13-58`) and the first test in `thread-pr.test.ts` (`:8-11`). Only cases that depend on harness-only behavior stay in Go: a command over 4 KiB, and more than 100 links.

### Heredoc bodies go before quotes, in both ports

`shellCode` first drops every heredoc body (Go `public_progress.go:279-314`, which queues each pending terminator; TypeScript `thread-pr.ts:13`, which repeats the replacement until nothing changes). Only then does it blank quoted strings and comments (Go `:316-345`, TypeScript `:15`). The shared cases "a heredoc body inside a quoted command substitution" and "two heredocs on one line" pin this.

The cost is accepted and pinned too. A `<<WORD` inside quotes is read as a heredoc, so `echo "see <<EOF usage"` followed by `gh pr create --fill` counts the PR only as mentioned (case "a quoted heredoc marker is read as a heredoc"). A review suggested detecting heredocs only outside quotes. That would break `--body "$(cat <<'EOF' …)"`, where the heredoc sits inside double quotes.

### The browser walks the whole tree

`loadThreadPRs` walks level by level with a visited set (`thread-pr.ts:108-127`). For OpenCode it lists child sessions with `GET <api>/session?parentID=<id>`, paged by cursor (`:54-72`), as the upstream client's `listChildren` does (`internal/rocketclaw/oc/packages/client/src/solid/data.ts:606-612`). For native threads it follows each message's `delegation` (subagents) and each page's `shadows` (`:93`, `:102`). Only created PRs feed the automatic-open watch (`thread-pr-panel.tsx:36-37`). Mentioned PRs are listed and marked "mentioned" in the picker (`:62`).

## Why This Works

- **Current checkout:** jj keeps Git on a detached HEAD, so anything that asks Git for the current branch fails in a jj workspace. The branch name lives in jj's bookmarks.
- **Session tree:** subagents and shadows run as their own sessions, so a thread's work is the whole tree (`CONCEPTS.md`: Subagent Session, Shadow Session, Remote OpenCode Session).
- **Native records:** the harness is the last place that sees the full command and output. A History record is a display summary, so facts computed from it later are limited to what it kept.
- **Heredoc order:** a heredoc body is data, and it may contain quotes. Quote pairing that runs over it pairs quotes that the shell never sees as code.

## Prevention

- **Native History is a display record.** Do not derive facts from a code-mode call's `Summary` or `Arguments`. Compute them in the harness when the call finishes and store them as fields.
- **A rule ported to two languages shares one case table.** Add any classifier case to `pr-discovery-cases.json` first, so both ports run it. Keep port-only cases only for behavior that exists in one port. Two ports exist because OpenCode output reaches only the browser. One owner would mean the server classifying OpenCode sessions too, which is a product change, not done.
- **Shell scanning:** remove heredoc bodies before pairing quotes, and pair quotes within the remaining code only.
- **jj workspaces:** never rely on Git's current branch in a folder that may be a jj workspace. That includes `gh pr view`, `gh pr status` and `gh pr checks` without a branch argument, `gh pr create` without `--head`, `git branch --show-current`, and `git rev-parse --abbrev-ref HEAD`. Pass the branch from jj bookmarks, and guard the lookup so a plain Git checkout keeps its own branch.
- **Session history:** anything that scans a thread's history for what it did must walk subagent and shadow sessions, reading every page.
- **Test fixtures:** do not use a real environment's normal state as a fixture's error case.
- **Changed behavior to know about:** stacked PRs opened by one `gh pr create` inside a shell function or loop now count as mentioned, not created, because the output has more URLs than create commands. They are listed, but the pane does not open itself for them. The 2026-11-13 fix had counted them as created. The change follows the chosen t3code parity and was left for the owner to confirm at merge.
- **Known limits:**
  - Older native records still give only their last output line.
  - One failed child or shadow read makes `loadThreadPRs` fail for the whole thread (`thread-pr.ts:114`, `:128-131`). No test covers it.
  - The bookmark lookup finds only the top PR of a stack. The tree walk finds the rest.
  - Error messages (proposal, not done): the pane still shows one fixed login message for any checkout failure, instead of the tool's own error.
- **Verification:**
  - A browser test that runs while `internal/rocketclaw/web/src/` is being edited can test an older bundle. See `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`.
  - The `frontend/rpc` Go tests need a test database. See `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`.
  - Browser test files can time out under load from other workspaces' builds. Rerun them one file at a time.

## Related Issues

- PR #1071 added the pane and the round-1 defects.
- `docs/solutions/architecture-patterns/web-acts-on-remote-opencode-sessions-through-the-api-proxy.md` covers the `/oc/` API proxy the OpenCode walk calls.
- `docs/verification/thread-pr/README.md` covers the bounded model of thread PR discovery across a session tree of a thread, a subagent, a shadow and a nested subagent, and the created-only automatic opening.
