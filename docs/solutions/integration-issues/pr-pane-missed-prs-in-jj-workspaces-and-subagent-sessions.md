---
title: The Pull Requests Pane Missed PRs in jj Workspaces and in Subagent Sessions
date: 2026-11-13
last_updated: 2026-11-23
category: docs/solutions/integration-issues/
module: internal/rocketclaw/web
problem_type: integration_issue
component: frontend
symptoms:
  - "On a Remote OpenCode Session, Pull requests showed: Could not read the current checkout's pull request using this OpenCode server's gh login, although gh pr view had failed only because the folder had no repository or no current branch"
  - "Created in this thread listed none of the seven stacked PRs the thread had opened"
  - "In native threads the pane missed PRs that subagents or shadows opened, and any PR link printed before a command's last output line"
  - "The Go classifier recorded nothing for gh pr create --body \"$(cat <<'EOF' ... EOF)\" when the body held double-quoted text such as \"gh api\""
  - "A PR opened by GIT_DIR=$(jj git root) gh pr list --head b ...; GIT_DIR=$(jj git root) gh pr create --head b ... never appeared, because any gh pr list in the script discarded the whole output"
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - internal/rocketclaw/harness
  - internal/rocketclaw/frontend/rpc
  - internal/rocketclaw/oc
tags:
  - jj
  - gh
  - pull-requests
  - subagent-session
  - opencode
  - native-history
  - heredoc
  - shell-parser
  - mvdan-sh
  - gh-pr-view
  - checkout-banner
---

# The Pull Requests Pane Missed PRs in jj Workspaces and in Subagent Sessions

## Problem

The Web **⋯ → Pull requests** pane (added in PR #1071) missed pull requests in rounds 1, 2 and 4, and showed a false error in round 3.

1. **2026-11-13, Remote OpenCode Sessions.** A session that opened a stack of seven PRs (#1097 to #1103) showed none. **Current checkout** asked `gh` for the current branch's PR, and a colocated jj workspace has no current Git branch. The thread's list read only the thread's own session, while a subagent had opened the PRs in its own child session.
2. **2026-11-18, native threads.** The pane read only the thread's own `/api/History`, never its subagents or shadows. Worse, a native History record cannot hold the evidence: it keeps a bash call's arguments clipped at 4 KiB and only the last output line, cut to 200 bytes. So the classification moved into the harness, which left the same shell classifier in two languages. The two ports then disagreed on the most common way agents open PRs.
3. **2026-11-20, Remote OpenCode Sessions outside a repository.** A session running in `$HOME`, a home folder that is not a repository, showed "Could not read the current checkout's pull request using this OpenCode server's gh login." Right below it, the pane showed PR #1548, which it had read with that same login. **Current checkout** counted only `no pull requests found for branch` as a normal "no PR" answer. In a folder with no repository, or in a jj repository with no bookmark at or below `@`, `gh pr view` fails before it reaches GitHub, and the pane blamed the login.
4. **2026-11-23, the classifier itself.** A long OpenCode thread did not list PR #1767, which one of its subagents had opened. The subagent ran one command that checked for an open PR and then created one: `GIT_DIR=$(jj git root) gh pr list --head access-log …; GIT_DIR=$(jj git root) gh pr create --head access-log …`. Both regular-expression classifiers, Go and TypeScript, threw away the whole output of any script that ran `gh pr list`. Neither saw `gh pr create` as a command after `GIT_DIR=$(…)`, because the `$(…)` holds spaces. This was the fourth round of patching one shell shape at a time, in two copies.

The second round merged in PR #1323 (plan `docs/plans/2026-11-18-0825-feat-pr-pane-subagents-markdown-quoting-plan.md`). The third round's evidence is in `docs/verification/pr-pane-checkout/README.md`. The fourth round merged in PR #1768.

## Symptoms

- The pane showed the "gh login" message from `internal/rocketclaw/web/src/thread-pr-panel.tsx:69`. The login was fine. Before round 3, `opencode-pr.ts` counted only `no pull requests found for branch` as a normal "no PR" answer. Every other failure throws (`opencode-pr.ts:49`) and becomes the same fixed message (`:57`), so the real `gh` error stayed hidden.
- Round 3: in a folder that is not a repository, `gh pr view` printed `failed to run git: fatal: not a git repository (or any of the parent directories): .git` and exited 1. In the same folder, `gh api --method GET repos/Rocketable/platform/pulls/1548` exited 0. So the pane showed the banner and the PR it had found in the session history together. A colocated jj repository with no bookmark at or below `@` printed `could not determine current branch: failed to run git: not on any branch` and showed the same banner. Both outputs were checked by hand with gh 2.104.0 and jj 0.47.1.
- In any colocated jj workspace, `GH_PROMPT_DISABLED=1 gh pr view --json url` exits 1 with `could not determine current branch: failed to run git: not on any branch`, even when a jj bookmark points at `@`.
- A Remote OpenCode thread's list was empty although its history showed the PR links.
- In a native thread, a PR a subagent or shadow opened never appeared. A PR the thread itself opened appeared only when its URL was the last output line. `gh pr create` often prints a warning after the URL, such as `Warning: 1 uncommitted change`.
- After the classifier moved into Go, `gh pr create --title "Add x" --body "$(cat <<'EOF' … Use "gh api" here … EOF)"` recorded no PR at all in Go, while the TypeScript port marked it created.
- Round 4: fed the exact command and output for #1767, the browser's `pullRequestsIn` returned nothing. Without the `gh pr list` step it returned #1767 only as mentioned. PR #1745 was missing from the same thread for the same reason.

## What Didn't Work

Round 1 (OpenCode):

- **Trusting the error text.** The message blamed the `gh` login. Running the command by hand in the session folder showed the real error.
- **Trusting the browser test.** The fake OpenCode server in `opencode.browser.test.ts` used `could not determine current branch` as its "checkout unavailable" output. So the suite treated the normal state of every jj workspace as a rare failure. Round 3 replaced it with `gh`'s real not-logged-in text (`opencode.browser.test.ts:50`), so the banner check now runs on a real login failure.
- **A bare jj bookmark lookup.** jj finds its repo by walking up the directory tree. In a plain Git checkout nested inside a jj workspace, such as a clone under `<repo-root>/.tmp/`, the lookup returned the outer workspace's bookmark. The fix asks jj only when `git symbolic-ref -q HEAD` finds no branch. That nested case was checked by reasoning and a jj-only probe, not end to end.
- **Pairing quotes across the whole script, and `--dry-run` anywhere.** Once multi-line scripts were accepted, an apostrophe in a comment (`# don't forget`) paired with a quote lines later and blanked the `gh pr create` between them, and a `jj git push --dry-run` before the real push discarded every created PR. Both are cases in the case table ("an apostrophe in a comment", "an earlier dry-run push").
- **Declined as unmeasured:** caching subagent sessions by `time.updated`, capping parallel requests, and cancelling other requests after one fails. Each refresh reread the whole tree, about 180 requests for a 90-subagent thread. PR #1746 later added the cache and the cap with measurements.

Round 2 (native threads and the two ports):

- **Reading the History record in the browser.** A record's `Summary` is the last non-empty output line cut to 200 bytes (`internal/rocketclaw/harness/public_progress.go:221-226`). Its `Arguments` are clipped to `maxCodeModeCallArguments = 4 << 10` and end in `…[truncated]` (`:95`, `:187-193`), so `JSON.parse` fails on a long `gh pr create --body …` and the browser skipped the call. A plan review proposed recovering the command prefix from the clipped JSON. An endpoint returning full outputs was rejected because it would ship whole outputs to the browser to find a few URLs (plan, "Alternatives Considered"). Keeping the unclipped command in the harness made the prefix recovery unnecessary.
- **Keeping the old "created" rule unchanged.** It counted every PR URL alone on a line when any `gh pr create` ran. A script that runs `gh pr list` and then `gh pr create` would then count listed PRs as created, and the pane opens itself on created PRs. Review replaced it with t3code's `changeRequestsCreatedBy` guards. Those guards included "a listing command records nothing", which round 4 had to relax.
- **Quotes before heredocs in Go.** The first Go `shellCode` blanked quoted strings first. In `--body "$(cat <<'EOF' … "gh api" … EOF)"` the opening `"` paired with the quote before `gh api`, which left `gh api` looking like a command. The listing rule then dropped everything. TypeScript removed heredoc bodies first and got it right.
- **Separate case lists per port.** Nine quoting, comment and dry-run cases lived only in `thread-pr.test.ts`, so Go's branches for them were untested. Review also reproduced a second divergence: TypeScript stripped only the first of two heredocs opened on one line (`cat <<'A' <<'B'`), while Go queued both. Round 2's answer was one shared case file both ports ran. That kept them in step but left two hand-written scanners to patch.
- **Considered, not built:** a "checked" flag on every successful bash call, so the browser could tell an old record from a new one that found nothing. The record fields use `omitempty`, so the two look alike. Old records show no links (see the known limits).

Round 3 (the checkout banner):

- **Assuming round 1 had fixed every jj case.** Round 1 passed the nearest bookmark. A jj repository with no bookmark at or below `@` still has no branch to name, and a session can also run in a folder that is not a repository at all.
- **Suspecting a race between the lookups.** The checkout lookup and the history walk run at once, and the PR read waits for both. The PlusCal model `docs/verification/pr-pane-checkout/CheckoutAlert.tla` explores every order of the three. Its finding is that the order never decides the banner: with a working login, only the session folder does. The older model `docs/verification/thread-pr/` had missed this because it treats every checkout failure as one generic outcome.
- **Counting every `gh pr view` failure as "no PR".** That would remove the false banner, but it would also hide a real login failure in a folder with a named branch. The model's `too-broad` check fails on this.
- **Matching only the start of each message.** The first fix matched `failed to run git: fatal: not a git repository` and `could not determine current branch: `. Review found that the second prefix covers any local `git symbolic-ref` failure, not just a missing branch. Review also noted that the first matches Git's `fatal: not a git repository: <gitdir>`, which Git prints for a linked worktree whose entry was pruned. No one ran that case. The rule now matches the two full texts that were checked by hand.

Round 4 (the classifier):

- **Another pair of patches.** The first proposal split the script into steps so a `gh pr list` step dropped only its own output, and widened the create pattern to accept `VAR=$(…)`. The owner rejected it as whack-a-mole: each earlier round had added one more shell shape to two regex scanners (quotes, comments, heredocs, here-strings, escaped newlines, dry runs), and each new shape broke them again.
- **A shell parser in the browser.** The web package has no shell parser. The only one in the tree is `web-tree-sitter`, a dependency of the copied OpenCode UI, not of the web app. Porting the rules onto it would have kept two implementations.
- **A parser alone.** The parser fixes the `GIT_DIR=$(…)` case, but #1767 also needed a rule change: under the old "a listing records nothing" rule, a correct parse still drops the PR.

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

### A folder with no repository or no branch has no checkout PR

`opencode-pr.ts:48` (round 3):

```ts
// before
if (/^no pull requests found for branch /m.test(result.output)) return { ...source, url: null, branch: "" };
// after
if (/^(?:no pull requests found for branch |failed to run git: fatal: not a git repository \(or any of the parent directories\)|could not determine current branch: failed to run git: not on any branch)/m.test(result.output)) return { ...source, url: null, branch: "" };
```

Any other failure still throws and shows the banner. A bad token printed `HTTP 401: Bad credentials (https://api.github.com/graphql)`, which does not match. The second test in `opencode-pr.test.ts` runs both new outputs, copied from the real runs, as "no PR" cases (`opencode-pr.test.ts:44`). It runs a login failure as an error (`:45`). In the model, the `fixed` check passes all three properties in all four folder kinds. `screenshot` reproduces the report step by step under the old rule.

### Native bash calls are classified in the harness

When a bash call finishes, the harness classifies the full command and the full output, before the summary is cut (`public_progress.go:217-219`). The full command is kept in a private field taken before the arguments are clipped (`:179-184`) and cleared in `finish` (`:196-197`). The result is stored on the call as `created_pull_requests` and `mentioned_pull_requests` (`:109-110`), each capped at `maxPullRequestLinks = 100` (`:239`, `:299`, `:305`) because the record is stored.

### One classifier, on the shell parser, on the server (round 4)

`PullRequestsIn` (`public_progress.go:248-311`) is the only classifier. It parses the command with `mvdan.cc/sh/v3/syntax`, the parser the harness already uses for permission rules, and walks the syntax tree (`:255-283`). `ghCommand` (`:313-340`) expands each call's literal words, those made only of plain, single-quoted or double-quoted text, reads any word with an expansion as empty, skips `env`, `nohup`, `timeout <d>` and `NAME=value` prefixes, and accepts `gh` by its base name. Quoted text, comments and heredoc bodies are never calls, so the hand-written quote, comment and heredoc scanners were deleted. A `gh` call counts wherever it runs: after `GIT_DIR=$(…)`, inside `$(…)`, in a pipe, a function or an `if`.

The rule, still after t3code's `changeRequestsCreatedBy`, plus mentions:

- Created: PR URLs alone on an output line, when a `gh pr create` or `gh pr new` call runs and none of them has `--dry-run` (`:259-265`, `:279-291`). A URL whose number appears in a literal outside the create calls is not created (`:269-272`, `:282`). When there are more such URLs than create calls, none is created (`:286-288`).
- A call to `gh pr list`, `gh pr status`, `gh search prs` or `gh api` (`:266-267`) no longer discards everything. The script keeps its created PRs, and none of its other links are mentioned, because a listing can print unrelated PRs (`:294-296`). If the listing printed URLs alone on lines, the count rule still drops the created ones (case "a script that lists and then creates records nothing").
- Mentioned: every other PR URL in the output (`:298-302`).

Each caller uses this one function:

- **Native calls**, when they finish (above).
- **Remote OpenCode Sessions.** The browser still reads their history through the `/oc/` proxy, because only the browser can reach it. It sends each successful call's command, with only the output lines that hold `https://github.com/`, to the new `FindPullRequests` RPC (`internal/rocketclaw/web/src/thread-pr.ts:84-100`). Batches stay under 512 KiB (`:69-82`), well below the server's 4 MiB request limit (`internal/rocketclaw/frontend/rpc/http.go:489`). The handler requires a signed-in caller and runs `PullRequestsIn` on each run (`internal/rocketclaw/frontend/rpc/session_commands.go:335`). It is dispatched from `pageCall` (`internal/rocketclaw/frontend/rpc/pages.go:25`), because one more case in `webCall` failed `gocyclo`. The RPC is kept out of session recordings beside `/api/History` (`internal/rocketclaw/web/src/posthog.ts:19`).

`internal/rocketclaw/harness/testdata/pr-discovery-cases.json` is now Go-only. `TestCodeModeCallPullRequests` (`public_progress_test.go:14-59`) runs every row, and `TestHTTPFindPullRequests` in `internal/rocketclaw/frontend/rpc/http_test.go` covers the RPC with a command shaped like #1767's. `thread-pr.test.ts` tests only the browser's plumbing: which runs it sends and how it merges the answers. Round 4 added three cases: "a check for an open PR before the create records the created PR", "a create after an assignment from a command", and "a create inside a command substitution". The earlier case "a quoted heredoc marker is read as a heredoc" is now "a heredoc marker inside quotes is text", and its PR is created. The parser removed the cost that round 2 had accepted.

### Validated by replaying real commands

Unit cases show that the intended rules hold, not that real commands improve. So all 179 successful shell calls with a GitHub link in the affected thread, from 147 sessions and 10,150 calls in total, went through the old TypeScript `pullRequestsIn` from `main` and the new Go `PullRequestsIn`. Nine results changed. All nine were PRs the thread had created that the old rules missed: #1767, #1745, #1226, #1238, #1242, #1243, #1244, #1249 and #1336. For each one, the PR's head branch (`gh pr view <n> --json headRefName`) matched the `--head` in its command. No PR found before was lost. The replay ran from throwaway files under `.tmp/` and was not kept.

### The browser walks the whole tree

`loadThreadPRs` walks level by level with a visited set, reading at most four sessions at a time (`thread-pr.ts:122-129`, `:152-177`). For OpenCode it lists child sessions with `GET <api>/session?parentID=<id>`, paged by cursor (`:45-53`), as the upstream client's `listChildren` does (`internal/rocketclaw/oc/packages/client/src/solid/data.ts:645-650`). For native threads it follows each message's `delegation` (subagents) and each page's `shadows` (`:140`, `:146`). Only created PRs feed the automatic-open watch (`thread-pr-panel.tsx:40-41`). Mentioned PRs are listed and marked "mentioned" in the picker (`:89`).

## Why This Works

- **Current checkout:** jj keeps Git on a detached HEAD, so anything that asks Git for the current branch fails in a jj workspace. The branch name lives in jj's bookmarks.
- **Session tree:** subagents and shadows run as their own sessions, so a thread's work is the whole tree (`CONCEPTS.md`: Subagent Session, Shadow Session, Remote OpenCode Session).
- **Native records:** the harness is the last place that sees the full command and output. A History record is a display summary, so facts computed from it later are limited to what it kept.
- **The parser:** which words are commands is a grammar question: quotes, heredocs, substitutions, assignments and comments nest. Regular expressions approximate it one shape at a time, and every classifier fix before round 4 added a shape. A real parser answers it for every shape at once. One implementation also means there is no second copy to drift.
- **The listing rule:** a listing's links are the risk, not the script's create. Dropping only the mentions keeps the protection against listed PRs counted as created, and the count rule still guards a listing that prints bare URLs.
- **Checkout banner:** `gh pr view` asks Git for the branch before it calls GitHub. When there is no repository or no branch, it fails without testing the login. Those failures describe the folder, so "no checkout PR" is the true answer. The PR read calls `gh api` with an explicit `repos/<owner>/<repo>/pulls/<n>` path (`thread-pr-panel.tsx:43`, `github-pr.ts:44`). That call needs no repository: it exited 0 in the same folder.

## Prevention

- **Native History is a display record.** Do not derive facts from a code-mode call's `Summary` or `Arguments`. Compute them in the harness when the call finishes and store them as fields.
- **Shell commands are read with the parser.** To decide what a command runs, walk the `mvdan.cc/sh` syntax tree, as `PullRequestsIn` and the permission rules do. Do not scan shell text with regular expressions or hand-written quote and heredoc scanners. Code that cannot import Go sends the command to the server, as `FindPullRequests` does. Walking the tree is safe; `mvdan.cc/sh`'s `expand` is not, because it panics on dynamic words, so expand only words `quotedLiteralWord` accepts (`docs/solutions/runtime-errors/mvdan-expand-panicked-on-dynamic-shell-words.md`).
- **One owner per rule.** The classifier lives in Go only. Add new cases to `pr-discovery-cases.json`. Do not add classification back to the browser: send the runs to `FindPullRequests`.
- **Check a classifier change on real history, not only on cases.** Replay the recorded successful calls of a long thread through the old and new rule, then check each changed result by hand, for example against the PR's head branch.
- **jj workspaces:** never rely on Git's current branch in a folder that may be a jj workspace. That includes `gh pr view`, `gh pr status` and `gh pr checks` without a branch argument, `gh pr create` without `--head`, `git branch --show-current`, and `git rev-parse --abbrev-ref HEAD`. Pass the branch from jj bookmarks, and guard the lookup so a plain Git checkout keeps its own branch.
- **Session history:** anything that scans a thread's history for what it did must walk subagent and shadow sessions, reading every page.
- **Test fixtures:** do not use a real environment's normal state as a fixture's error case. A browser fixture that fakes native History must carry the stored `createdPullRequests` and `mentionedPullRequests` the server sends. Round 4 broke a `github-pr.browser.test.ts` fixture that had relied on the removed browser fallback.
- **Classifying tool failures:** a session folder may be any folder, not only a repository. When a message blames one cause, such as the login, it should appear only for failures that cause can produce. Count environment states as normal answers only by their exact, hand-checked text. A broad prefix or a catch-all hides real failures. When a new `gh` output turns up, run the command in that folder by hand, then add the copied output to the second test in `opencode-pr.test.ts`.
- **Changed behavior to know about:** stacked PRs opened by one `gh pr create` inside a shell function or loop count as mentioned, not created, because the output has more URLs than create calls. They are listed, but the pane does not open itself for them. The 2026-11-13 fix had counted them as created. The change follows the chosen t3code parity and was left for the owner to confirm at merge.
- **Known limits:**
  - Native records saved before round 2 lack the stored lists, and History no longer reads their summary line, so their PRs are not listed.
  - A command the parser rejects yields no create and no listing, so its PR links count only as mentioned.
  - `gh` run through a wrapper the walk does not unwrap, such as `sh -c '…'`, `xargs gh …` or `sudo gh …`, is not seen as a `gh` call.
  - A word with any expansion reads as empty, so `"$HOME/bin/gh" pr create` and `env A=$B gh pr create` are not seen as `gh` calls.
  - One failed child read, shadow read or `FindPullRequests` call makes `loadThreadPRs` fail for the whole thread (`thread-pr.ts:173-176`). No test covers it.
  - The bookmark lookup finds only the top PR of a stack. The tree walk finds the rest.
  - Error messages (proposal, not done): for any checkout failure other than the three "no PR" outputs, the pane still shows one fixed login message instead of the tool's own error. Round 3 proposed putting `gh`'s first error line in the banner's tooltip. It was not built.
  - The round-3 rule matches English text from gh 2.104.0 and Git. If either rewords its message, the false banner comes back, and no test catches it, because the unit test holds copied text.
  - With a broken login in a folder with no repository or no branch, `gh` fails on the Git step first, so no banner shows. The PR read still reports its own failure for a PR found in the history (`github-pr-preview.tsx:185`). When the history has no PR, nothing signals the broken login.
  - Other local Git failures, such as a linked worktree whose entry was pruned, still show the login banner. Their `gh` output was not checked.
- **Verification:**
  - A browser test that runs while `internal/rocketclaw/web/src/` is being edited can test an older bundle. See `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`.
  - The `frontend/rpc` Go tests need a test database. See `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`.
  - Browser test files can time out under load from other workspaces' builds. Rerun them one file at a time.

## Related Issues

- PR #1071 added the pane and the round-1 defects.
- PR #1768 moved the classifier onto the shell parser and the server (round 4).
- `docs/solutions/architecture-patterns/web-acts-on-remote-opencode-sessions-through-the-api-proxy.md` covers the `/oc/` API proxy the OpenCode walk calls.
- `docs/verification/thread-pr/README.md` covers the bounded model of thread PR discovery across a session tree of a thread, a subagent, a shadow and a nested subagent, and the created-only automatic opening. It assumes the classifier is sound and points to the Go case table as the evidence.
- `docs/verification/pr-pane-checkout/README.md` covers the round-3 model: when the checkout banner shows, across a Git branch, a jj bookmark, a folder with no nameable branch and a folder that is not a repository. Its checks are `repo-only`, `fixed`, `screenshot`, `current-claim` and `too-broad`.
