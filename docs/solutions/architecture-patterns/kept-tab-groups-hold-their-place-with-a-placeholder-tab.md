---
title: Kept Tab Groups Hold Their Place With a Hidden Placeholder Tab
date: 2026-11-03
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: medium
applies_when:
  - Adding code that lists, counts, opens, or steps through a person's tabs, in the Web or in Go
  - Changing how tab groups are stored, ordered, merged, or moved between spaces
  - Giving a tab group (or another strip item) a position that must survive when it has no tabs
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/frontend/rpc
tags: [web, tabs, tab-groups, keep-group, placeholder, web-tabs, tab-state, saved-tabs]
---

# Kept Tab Groups Hold Their Place With a Hidden Placeholder Tab

## Context

A Web tab group has no position or space of its own. The strip is one ordered list of tabs, and a group is drawn where its first member is (`arrange`, `internal/rocketclaw/web/src/ui.tsx:966`). Its space is its members' space. The saved form drops every group without members except the reserved `shared` entry (`savedState`, `ui.tsx:1042-1043`). So a group used to vanish when its last tab closed or left it.

"Keep group when empty" needed an empty group to stay exactly where it was, across reloads and devices. Two designs were weighed:

- **A marker on the group** ("draw me before tab X"), plus a space field. A group can go empty in about six ways: closing its tabs, Ungroup, Remove from group, dragging tabs out, moving tabs to another space, and a merge of another window's save. Each one has to leave the marker right, and the marker needs a fallback when tab X itself closes or moves. Worse, dragging tab X drags the empty group with it, so the group does not really stay where it was. It also adds fields to the proto, Go and SQL layers.
- **A placeholder tab in the tab list.** Chosen. The tab list already is the position model, so everything that moves tabs already keeps the placeholder in place.

## Guidance

A kept group holds one placeholder entry among the person's tabs, with path `group:<id>` and `group` set to the group's id (`keeper`/`isKeeper`, `ui.tsx:963`). Whether a group is kept is just whether that entry exists:

- **Keep group when empty** inserts it after the group's last member with the existing `join` (`ui.tsx:1374`). **Don't keep group when empty** filters it out (`ui.tsx:1373`).
- It saves, syncs, and three-way merges like any tab, so no proto field, Go field, column, or migration was added.

Because the entry is not a real location, anything that treats tabs as pages must skip it. In the Web this is already done:

- `strip` holds the current space's tabs including placeholders, for drawing headers and as drop targets. `shown` leaves them out, and closing, stepping (Cmd/Ctrl+[ and ]), selection, the keyboard stop, and the tab menu's commands read `shown` (`ui.tsx:1235`).
- `entry()` skips placeholders when picking a space's first tab (`ui.tsx:1004-1005`).
- `savedTabs()` drops a placeholder whose group is gone (`ui.tsx:1020-1023`). Without that filter the orphan would keep being saved, and the strip would throw reading `group!.collapsed` on a group that no longer exists.
- Ungroup leaves the placeholder in its group (`ui.tsx:1375`). Close group needs no filter, because `close()` only acts on paths in `shown` (`ui.tsx:1240`).
- Only the strip draws the placeholder: an expanded empty kept group shows a "New session" row that starts a session in the group (`ui.tsx:1600`).

**The Go side does not know about placeholders.** `SaveTabState` stores the row like any other tab. `TabConversation("group:…")` is empty, so the row is kept with no conversation (`internal/rocketclaw/backend/store_dao.go:505`, `:531`). `TabState` returns it in `Tabs` (`store_dao.go:438`). Server code that lists a person's open tabs as pages, or counts them, must skip paths starting with `group:`.

## Why This Matters

A new consumer of `state.tabs`, `saved.state.tabs`, `backend.TabState.Tabs` or `web_tabs` that does not skip placeholders shows a bogus tab named after `group:<id>`, navigates to it, or counts it. Nothing fails loudly. For example, `entry()` picked a space's first tab from `state.tabs`; without its filter, entering a space whose first strip item is an empty kept group would navigate to `group:<id>`.

A browser window still running a build from before placeholders shows them as odd extra tabs until it reloads.

## When to Apply

- Writing Web code that iterates `state.tabs` or `saved.state.tabs`: use `shown` when you mean the tabs a person sees, or filter with `isKeeper`.
- Writing Go code over `web_tabs` or `TabState.Tabs` that treats rows as locations.
- Reconsidering the "marker on the group" design: the drag-follows-marker problem and the six emptying paths are why it lost.

## Examples

Reading the current space's real tabs in the Web:

```ts
// ui.tsx:1235
const strip = state.tabs.filter((tab) => tab.space === state.space), shown = strip.filter((tab) => !isKeeper(tab));
```

The browser test "a group kept when empty stays in place through reload, offers New session, and takes dropped tabs" (`internal/rocketclaw/web/src/tabs.browser.test.ts:1026`) covers keep, emptying by close and by drag-out, reload, dropping a tab on the empty group, Close group, the New session row, and unkeeping.

Implemented in PR #933 (open, not merged as of 2026-11-03).

## Related

- `docs/solutions/logic-errors/web-tab-state-holds-a-placeholder-main-space-before-its-first-load.md`: a different "placeholder", the Main space `useTabs` holds before its first load.
- `internal/rocketclaw/web/README.md`, Tabs > Groups and Storage.
