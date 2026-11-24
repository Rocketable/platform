---
title: Moving a Remote OpenCode Session's Page Tab Must Move the Session's Birth Space
date: 2026-11-20
category: docs/solutions/logic-errors/
module: internal/rocketclaw/frontend/rpc
problem_type: logic_error
component: service_layer
symptoms:
  - "A Remote OpenCode Session's Hosted Pages were published under /pages/<old space>/... although the session's tab sat in another space."
  - "Move to space on a page tab moved only that person's tab; the page link, the new space's Page Index and Space Limit access stayed on the old space."
  - "The agent could not fix it: no bridge tool, and no Web action, changed where its pages lived."
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - frontend
  - database
tags:
  - hosted-pages
  - birth-space
  - space-limit
  - move-to-space
  - remote-opencode-session
  - mcp-bridge
  - web
---

# Moving a Remote OpenCode Session's Page Tab Must Move the Session's Birth Space

## Replaced

Hosted Pages now belong to Web spaces, not to sessions or agents (`docs/plans/2026-11-24-1018-feat-space-owned-hosted-pages-plan.md`). A page's space is stored with the page, so a session's Birth Space no longer decides where its existing pages live, and moving a page tab moves only that person's tab again. `UpdateSessionRequest.space_id` and the Web's `movePages` are gone. The objection below to a per-page space column held while a session owned its pages; with no owner left, the page's own space is the only source of truth.

What still holds: a Hosted Page's space is its access boundary, and any future path that moves pages between spaces must check the mover's Space Limit for both spaces.

## Problem

A Remote OpenCode Session created while its owner was in the space Ops published reports that landed in Ops, although the owner had moved the session's tab to Acme. "Move to space" on the page tabs did not move the pages either. A page's space comes from its session's Birth Space, and nothing in the Web changed that.

## Symptoms

- Page links read `/pages/Ops/opencode%3A.../index.html` while the session's tab was in Acme.
- "Move to space" → "Acme" on a page tab moved the tab only. The link still named Ops, Acme's Page Index did not list the page, and a person limited to Acme still could not open it.
- The agent guessed at tags and reconnecting the bridge, then asked the person to move the session, which only moves a tab.

## What Didn't Work

Each of these was proposed or built in the session and rejected by the owner, in this order:

- **Pages with no space, placed per person like tabs.** The owner first wanted this, then reversed it. A page's space is the Space Limit gate. A Remote OpenCode Session is never within a Space Limit (`CONCEPTS.md`, Space Limit), so for a limited person the page's space is the only thing that admits them. Per-person placement would let anyone widen who can read a page.
- **A separate "Set home space" Web action, plus new bridge instructions.** Rejected as "splitting signals": the owner wanted the existing "Move to space" to work, and the bridge left as it is.
- **A bridge tool that lets the agent move its own session.** It would let an agent change who can read its pages.
- **Moving only the one page whose tab was moved.** The report loaded a stylesheet, a script and Page Data from the same session. For a person limited to the new space, half the files would stay unreadable. The owner chose to move everything the session published.
- **A new table holding a per-page space override.** Built, tested, and rejected as table sprawl.
- **A `space_id` column on `hosted_pages` (migration `106_hosted_page_space.sql`) overriding the session's space.** Built and pushed to the PR, then rejected as far too large for so targeted a change. It also had a race: a page stored while the move ran could stay in the old space.

## Solution

Moving a page tab moves the session that owns the page. Pages already take their space from the session, so they all follow, and there is no database change. The fix is in PR #1530 (unmerged as of this writing).

- `UpdateSessionRequest` gains `optional string space_id = 8` (`internal/rocketclaw/web/proto/web.proto`).
- `updateSession` checks the caller's Space Limit, then moves the conversation (`internal/rocketclaw/frontend/rpc/server.go:1187`):

  ```go
  if request.SpaceId != nil {
  	_, limit, err := s.spaceLimit(ctx)
  	if err == nil && !limit.AllowsSpaceID(*request.SpaceId) {
  		err = status.Error(codes.PermissionDenied, "this space is outside your spaces")
  	}
  	if err == nil {
  		err = s.sessions.MoveConversation(ctx, request.Id, *request.SpaceId)
  	}
  	...
  }
  ```

  `MoveConversation` is one `UPDATE conversations SET space_id = $2 WHERE conversation_id = $1` (`internal/rocketclaw/backend/store.go:426`).
- In the Web, "Move to space" and "Move group to" still call `moveTo` for the person's tabs, then `movePages` (`internal/rocketclaw/web/src/ui.tsx:1270`). For each distinct page owner among the moved tabs, `movePages` calls `updateSession({ id: agent, spaceId })`. When that succeeds, it rewrites that owner's page tab paths to the new space name, refreshes the page list and navigates if the page is in view.

## Why This Works

- A page's space is read through its owning conversation: `pageOwnerSQL` joins `hosted_pages` → `conversations` → `web_spaces` on `c.space_id` (`internal/rocketclaw/backend/pages.go:32`). One write to the session moves every page and every Page Data key it owns.
- The same space decides access: `GetPage` admits a limited person only when `limit.AllowsSpaceName(cmp.Or(agent.Space, page.Space))` holds (`internal/rocketclaw/frontend/rpc/pages.go:55`). Access follows the move without new code.
- Old links keep working. A `/pages/<space>/...` request whose space differs from the page's gets a permanent redirect to the current one (`internal/rocketclaw/frontend/rpc/http.go:209`).
- The OpenCode sync does not undo the move. `recordOpenCodeSession` upserts with `space_id = COALESCE(conversations.space_id, excluded.space_id)` (`internal/rocketclaw/backend/opencode.go:600`), so an existing Birth Space wins over the sync's value.

Accepted trade-offs, which the owner agreed to:

- Moving a page tab moves the session for everyone. Its "Restore to …" target and `space:` search follow the new space.
- Moving the session's own chat tab still moves only that person's tab, so its pages stay put until one of the page tabs is moved.
- Pages of a configured agent take their space from the agent's `space:` setting (`cmp.Or(agent.Space, page.Space)` above). This change does not move them; change the agent's `space:` instead.

## Prevention

- Treat a Hosted Page's space as an access boundary, not a placement. Do not make it per person or drop it.
- To change where a session's pages live, change the session's Birth Space. Do not add a per-page space table or column. The owner rejected both, and either one adds a second source of truth for a page's space.
- Any path that changes a session's space must check the mover's Space Limit first, so a limited person cannot move a session into a space they cannot see.
- Tests that hold this:
  - `TestPages` in `internal/rocketclaw/frontend/rpc/pages_test.go`: after `UpdateSession` with `SpaceId`, the page reports the new space and the old link redirects.
  - `TestTabsSpaceLimit` in `internal/rocketclaw/frontend/rpc/tabs_test.go`: a limited person moving a session outside their spaces gets `PermissionDenied`.
  - The "moving a page's tab moves its session's pages to the space" test in `internal/rocketclaw/web/src/pages.browser.test.ts`.
- If a session lands in the wrong space again, one unconfirmed lead from this session: a Remote OpenCode Session created on another RocketClaw from the Web takes the space last used on that server, not the one on screen (`spaceId: rocketclaw === here ? tabs.state.space : view.tabs?.state.space` in `internal/rocketclaw/web/src/ui.tsx:2863`).

## Related Issues

- `docs/solutions/integration-issues/opencode-sessions-lacked-rocketclaw-tools-until-rocketclaw-added-the-bridge.md`: how Remote OpenCode Sessions get the MCP Bridge that publishes these pages.
- `docs/solutions/logic-errors/remote-opencode-fork-marker-dropped-by-sidebar-remote-branch.md`: quotes the same `recordOpenCodeSession` upsert.
- `CONCEPTS.md`: Birth Space, Space Limit, Hosted Page.
