---
title: Preview GitHub pull requests in chat
type: feature
date: 2026-11-11
artifact_contract: unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# Preview GitHub pull requests in chat

## Problem and scope

People need to read linked pull requests without leaving their conversation.
Provide four working presentation variations: side panel, modal, inline expansion,
and full-screen. They share retrieval and rendering, not four credential paths.
The initial presentation is a side panel. Keep the existing external link and add
an explicit Preview action; opening the ordinary link must retain its behavior.

Show title, description, draft/open/closed/merged status, file names and available patches,
with a link back to GitHub. This does not add comments, reviews, checkout, merge,
CI aggregation, background polling, Enterprise support or agent tools.

## Access decision

This is a multi-user application. A server's GitHub CLI login does not establish
that every browser user may read that login's private repositories. Use direct
browser GETs to the fixed `https://api.github.com` origin instead. Public PRs need
no credential. A person may explicitly supply their own fine-grained read token
for a private PR. Keep that token and results only in the open preview's memory;
closing or navigating away discards them. Never persist or forward them through
application RPCs or telemetry. The alternative was server-held credentials and a
new per-user authorization service; that is a substantially larger integration.
Private token entry requires a secure browser context and a browser-trusted
certificate; memory-only handling does not protect a compromised page. Use a
password input with autocomplete off, outside a submitted form. The application
does not store tokens; browser extensions and clipboard managers are outside its
control. An anonymous 404 must say "not found or private" and offer token entry.

## Requirements

- R1: Preview is explicit. Rendering, hover and focus cause no GitHub request.
- R2: All four layouts display real retrieved content and reuse the same result
  when switching presentations. Existing conversation drafts and panels remain.
- R3: Only HTTPS GitHub PR root and recognized files/commits/checks links qualify.
  Reject foreign hosts, userinfo, ports, traversal and encoded separators before
  constructing API URLs. Never follow response-provided links or redirects.
- R4: Fetch metadata and file pages with omitted ambient credentials, no referrer,
  and no browser cache. Only GitHub receives an explicitly supplied token.
- R5: Render description and patches as escaped text, never raw HTML, images or
  remote embeds. No executable content or extra fetches from the response.
- R6: Loading, access errors, rate limits, network failures and missing patches
  have honest messages. File-page errors preserve previously loaded content.
- R7: Page files explicitly, preserve ordering and show loaded versus reported
  counts. Do not claim completeness at GitHub's 3,000-file limit or when pages end
  early. A revision change while paging requires refresh rather than mixed data.
- R8: Abort on cancel, close, replacement, navigation and unmount. Stale results
  cannot reappear. Reopening starts a fresh anonymous preview.
- R9: All four layouts work on narrow screens with keyboard close/focus behavior.
- R10: Exclude preview DOM, inputs, GitHub request data and private identifiers
  from configured Sentry and PostHog collection. Catch failures locally.

## Source evidence

Existing rendering seams are the interactive raw-link renderer and Markdown
anchor component in `internal/rocketclaw/web/src/transcript-text.tsx`.
Existing Sheet and Dialog components provide accessible overlays. Application
navigation lives in `internal/rocketclaw/web/src/navigation.tsx`.

The external reference at `pingdotgg/t3code` revision
`0318eadc3202c64f3da9aad08ea5aa868a2f0d7c` separates PR layout, credentials and
request policy. Relevant files are
`apps/web/src/components/pullRequest/PullRequestDetailLayout.tsx`,
`packages/source-control-github/src/server/GitHubCredentials.ts`, and
`packages/source-control-github/src/server/GitHubRequestPolicy.ts`;
`packages/client-runtime/src/connection/pullRequestRoutingPermissions.ts`.
Its settings/app/environment/CLI credential chain is not a multi-user browser
authorization contract for this application. Borrow content organization only.

GitHub REST documentation confirms read-only pull-request access, diff media
types and the fine-grained Pull requests: read permission. React documentation
requires cancellation/cleanup so stale fetch results cannot replace newer state.

## Implementation

1. Add a small typed adapter in
   `internal/rocketclaw/web/src/github-pr.ts`, with boundary tests in
   `github-pr.test.ts`. Validate references, construct fixed-origin requests,
   sanitize errors, and retrieve metadata and ordered file pages. No shared cache.
2. Add a link-local viewer in `github-pr-preview.tsx`. Keep retrieval state above
   four presentation shells; reuse the existing Sheet, Dialog, Input and Button.
   Inline content must use paragraph-compatible markup. Mount only the selected
   patch. Clear memory on close/navigation, including hidden-but-mounted tabs.
   Every header has one four-option layout selector, initially Side panel. Do
   not put preview state or private repository names into application URLs. The
   panel deliberately uses a modal right Sheet to leave existing docked panels
   untouched; the inline variation permits reading and typing beside the result.
   Reset inherited typography in inline content and constrain it to its container.
3. Use it at both interactive transcript link sites. Leave non-interactive
   snippets, code, unrelated links and the original anchors unchanged.
4. Add narrow telemetry exclusions in `sentry.ts` and `posthog.ts`, verified with
   telemetry enabled. Private previews must not expose credentials or response
   contents through network recordings, DOM recording or interaction labels.
   Mark each portal's actual content root, not just its parent link. Tests must
   enable the recorder and network capture with positive-control content outside
   the preview so an inactive recorder cannot produce a false pass.
5. Document the feature and token lifetime in
   `internal/rocketclaw/web/README.md`. Build and commit the tracked Web bundle.

## Checks and acceptance

Use synthetic data only. `github-pr.test.ts` covers valid/invalid references,
anonymous/token headers, redirects, errors, paging and omitted patches.
`github-pr.browser.test.ts` runs against the built bundle and proves all four
presentations, no fetch before Preview, switching without refetch, cancellation
and late results, partial-page failure/retry, private access isolation, no unsafe
response fetches, normal external links, unchanged drafts, and mobile/keyboard
operation. Verify telemetry payloads and browser storage with unique synthetic
secret/content markers; none may escape. Existing transcript tests protect raw
and Markdown discovery, including no preview inside code or passive snippets.
Additionally smoke-test a public PR against real GitHub from Chromium, including
an Authorization preflight with a synthetic credential (never a real user token).
This verifies GitHub's CORS behavior separately from synthetic response tests.

Run Web lint and tests, rebuild before browser checks, and run repository checks
through the Linux make wrapper where Go is involved. The measured Web source
count is 8,467 against a failing threshold of 9,300; aim for at most 550 added
source lines without raising budgets or hiding code. No Go changes are planned.

## Assumptions and confidence

The requested variations are four usable presentations of the same feature, not
four independent authentication systems. Description formatting is plain text.
Private access is per open preview, not a saved account setting. Status describes
PR lifecycle, not CI. API file patches may be unavailable or incomplete.

Confidence is moderate-high. Browser CORS and SDK privacy exclusions require
runtime proof before shipping. The design is not complete until those tests,
all four layouts and independent review pass. README impact requires an update.
