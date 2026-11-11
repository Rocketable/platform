---
title: Every Tab Group Holds Its Place With a Hidden Placeholder Tab
date: 2026-11-03
last_updated: 2026-11-11
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: medium
applies_when:
  - Adding code that lists, counts, opens, or steps through a person's tabs, in the Web or in Go
  - Changing how tab groups are created, stored, ordered, merged, deleted, or moved between spaces
  - Writing a Web browser test that compares the saved tab list while a group exists
  - Giving a tab group (or another strip item) a position that must survive when it has no tabs
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/frontend/rpc
tags: [web, tabs, tab-groups, sticky-groups, delete-group, placeholder, web-tabs, tab-state, saved-tabs]
---

# Every Tab Group Holds Its Place With a Hidden Placeholder Tab

## Context

A Web tab group has no position or space of its own. The strip is one ordered list of tabs, and a group is drawn where its first member is (`arrange`, `internal/rocketclaw/web/src/ui.tsx:981`). Its space is its members' space. The saved form drops every group without members except the reserved `shared` and `parked` entries (`savedState`, `ui.tsx:1052-1054`). So, with nothing else, a group vanishes when its last tab closes or leaves it.

On 2026-11-03 "Keep group when empty" made that optional. It needed an empty group to stay exactly where it was, across reloads and devices. Two designs were weighed:

- **A marker on the group** ("draw me before tab X"), plus a space field. A group can go empty in about six ways: closing its tabs, Ungroup, Remove from group, dragging tabs out, moving tabs to another space, and a merge of another window's save. Each one has to leave the marker right, and the marker needs a fallback when tab X itself closes or moves. Worse, dragging tab X drags the empty group with it, so the group does not really stay where it was. It also adds fields to the proto, Go and SQL layers.
- **A placeholder tab in the tab list.** Chosen. The tab list already is the position model, so everything that moves tabs already keeps the placeholder in place.

On 2026-11-11 the person asked for every group to be sticky: a group no longer goes when its last tab closes or leaves it. The placeholder design took this with no new storage. Only where placeholders are created and removed changed.

## Guidance

Every group holds one placeholder entry among the person's tabs, with path `group:<id>` and `group` set to the group's id (`keeper`/`isKeeper`, `ui.tsx:978`). A group exists as long as that entry, or any member, is in the tab list:

- **Add to new group** creates the group and inserts its placeholder after the new members with the existing `join` (`ui.tsx:1388-1391`).
- `savedTabs()` adds a placeholder after the last member of any saved group that lacks one (`ui.tsx:1029-1032`). This upgrades groups saved while keeping was optional, the first time a window loads them. A group with no members and no placeholder was already dropped by `savedState`, so nothing restores it.
- **Delete group** is now the only way a group goes. It closes the group's tabs by the usual close rules, then filters out the placeholder (`ui.tsx:1407-1410`). **Ungroup** and **Close group** leave the empty group in place (`ui.tsx:1405-1406`). The old **Keep group when empty** / **Don't keep group when empty** commands are gone.
- The placeholder saves, syncs, and three-way merges like any tab, so neither change added a proto field, Go field, column, or migration.

Because the entry is not a real location, anything that treats tabs as pages must skip it. In the Web:

- `strip` holds the current space's unparked tabs including placeholders, for drawing headers and as drop targets. `shown` leaves placeholders out, and closing, stepping (Cmd/Ctrl+[ and ]), selection, the keyboard stop, and the tab menu's commands read `shown` (`ui.tsx:1251`). `spaceTabs`, which bulk closes read, leaves them out too (`ui.tsx:1253`).
- `entry()` skips placeholders when picking a space's first tab (`ui.tsx:1012-1014`).
- `savedTabs()` drops a placeholder whose group is gone (`ui.tsx:1028`). Without that filter the orphan would keep being saved, and the strip would throw reading `group!.collapsed` on a group that no longer exists.
- Close group needs no filter, because `close()` only acts on paths in `shown` or in Parked (`ui.tsx:1258`). Delete group has to remove the placeholder itself for the same reason.
- Only the strip draws the placeholder: an expanded group with no shown members shows a "New session" row that starts a session in the group (`ui.tsx:1734`). A group whose only other tab is parked keeps its header for the same reason.

**The Go side mostly treats the placeholder as an ordinary row.** `SaveTabState` stores it like any other tab, and `TabConversation("group:…")` is empty, so the row has no conversation (`internal/rocketclaw/backend/store_dao.go:595`). `TabState` returns it in `Tabs`. Two places skip it explicitly: `SaveTabState` does not record a vanished `group:` row as a recently closed tab (`store_dao.go:741`), and `changeAttention` refuses attention on `group:` paths (`internal/rocketclaw/frontend/rpc/tabs.go:178`). Server code that lists a person's open tabs as pages, or counts them, must skip paths starting with `group:` in the same way.

## Why This Matters

A new consumer of `state.tabs`, `saved.state.tabs`, `backend.TabState.Tabs` or `web_tabs` that does not skip placeholders shows a bogus tab named after `group:<id>`, navigates to it, or counts it. Nothing fails loudly. For example, `entry()` picked a space's first tab from `state.tabs`; without its filter, entering a space whose first strip item is an empty group would navigate to `group:<id>`. Now that every group has a placeholder, this hits every person who uses groups, not only those who chose to keep one.

Tests feel this too. When groups became sticky, browser tests that compared the saved tab list exactly failed with "the saved tab state never matched", because the list now held `group:<id>` rows. They were fixed either by expecting the placeholder or by filtering `group:` paths out before comparing (`internal/rocketclaw/web/src/tabs.browser.test.ts:930`). Strip-order assertions also changed, because an emptied group's header now stays.

A browser window still running a build from before placeholders shows them as odd extra tabs until it reloads.

## When to Apply

- Writing Web code that iterates `state.tabs` or `saved.state.tabs`: use `shown` when you mean the tabs a person sees, or filter with `isKeeper`.
- Adding another way to create a group: insert its placeholder with `join`, as Add to new group does, or the group goes when it empties until the next load adds one.
- Adding another way to remove a group: filter out its placeholder explicitly, as Delete group does, because the close path never sees it.
- Writing Go code over `web_tabs` or `TabState.Tabs` that treats rows as locations.
- Writing a browser test that waits for an exact saved tab list while a group exists.
- Reconsidering the "marker on the group" design: the drag-follows-marker problem and the six emptying paths are why it lost.

## Examples

Reading the current space's real tabs in the Web:

```ts
// ui.tsx:1251
const strip = state.tabs.filter((tab) => tab.space === state.space && !parked.has(tab.path)), shown = strip.filter((tab) => !isKeeper(tab));
```

Deleting a group, which must drop the placeholder itself:

```ts
// ui.tsx:1407-1410
{ key: "delete", label: "Delete group", run: () => {
  close(spaceTabs.flatMap((tab) => tab.group === id ? [tab.path] : []));
  update((tabs) => tabs.filter((tab) => tab.path !== keeper(id)));
} },
```

The browser test "an emptied group stays in place through reload, offers New session, takes dropped tabs, and goes only when deleted, with its tabs" (`tabs.browser.test.ts:1108`) covers emptying by close and by drag-out, reload, dropping a tab on the empty group, Close group, the New session row, and Delete group. "tab group editor moves New session in, ungroups, closes, and deletes" (`tabs.browser.test.ts:1043`) covers the editor's commands. The Park test at `tabs.browser.test.ts:1974` covers a group saved without a placeholder getting one on load.

The optional design shipped in PR #933. Sticky groups and Delete group were not yet committed or opened as a PR when this was updated, 2026-11-11.

## Related

- `docs/solutions/architecture-patterns/palette-preview-tab-stays-in-its-window-at-every-saved-state-boundary.md`: the palette preview, a tab that must never be saved, the opposite of this placeholder, which is saved and must be skipped by readers.
- `docs/solutions/logic-errors/web-tab-state-holds-a-placeholder-main-space-before-its-first-load.md`: a different "placeholder", the Main space `useTabs` holds before its first load.
- `internal/rocketclaw/web/README.md`, Tabs > Groups and Storage.
