---
title: A Remote OpenCode Fork Lost Its Marker in the Sidebar List's Separate Remote Branch
date: 2026-10-30
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: database
symptoms:
  - "A session forked from a Remote OpenCode Session showed no fork glyph in the Web, and is:forked did not find it."
  - "Open original conversation was missing for remote forks, although the fork's conversation row stored its source."
  - "A backend test that read forked_from from the conversations row passed while the Web list still sent an empty forkedFrom."
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - frontend
tags:
  - opencode
  - remote-opencode-session
  - fork
  - sidebar
  - list-sessions
  - union-all
  - state-store
---

# A Remote OpenCode Fork Lost Its Marker in the Sidebar List's Separate Remote Branch

## Problem

Forks of Remote OpenCode Sessions carried no fork marker in the Web: no fork glyph, no `is:forked` match, no
**Open original conversation**. Recording the source in the database was not enough, because the sidebar list
reads remote rows through a second SQL branch that filled the column with a constant.

## Symptoms

- A remote fork's tab and session row looked like any other remote session.
- After the write path was fixed, the fork's `conversations` row held the right `forked_from`, yet `ListSessions`
  still sent `forkedFrom: ""` for it.
- The first regression test, which read `forked_from` straight from the row, passed against that broken list.

## What Didn't Work

- **Fixing only the write.** `ForkOpenCodeSession` (`internal/rocketclaw/backend/opencode.go:570`) records the
  fork through `recordOpenCodeSession` (`opencode.go:588`), which did not store `forked_from` at all. Storing it
  there fixed the row but not the Web.
- **Testing the row instead of the list.** `SELECT forked_from FROM conversations` proved the write and nothing a
  person sees. The gap surfaced only when a browser demo needed the mocked `ListSessions` to send `forkedFrom`
  for a remote fork, which prompted reading what the real list sends.
- **The Web browser tests could not catch it.** They mock `ListSessions`, so they test the Web's handling of
  `forkedFrom`, never whether the backend sends it.

## Solution

1. **Read the column in the remote branch.** `sidebarSessions` builds the list as `local UNION ALL remote`
   (`internal/rocketclaw/backend/store.go:1295`, `store.go:1328`, `store.go:1337`). The `local` branch reads
   `c.forked_from` (`store.go:1307`); the `remote` branch had a `''` in that position. It now reads it
   (`store.go:1329`):

   ```sql
   -- before
   SELECT o.conversation_id, o.agent, '', ..., o.pinned, o.name, '', COALESCE(o.tags, '[]'::jsonb), ...
   -- after
   SELECT o.conversation_id, o.agent, '', ..., o.pinned, o.name, o.forked_from, COALESCE(o.tags, '[]'::jsonb), ...
   ```

2. **Store the source, even when a sync got there first.** `recordOpenCodeSession` inserts `forked_from` from
   the source it is given, and its conflict clause sets it too (`opencode.go:597`), because the background
   OpenCode sync may list the new session before the fork call records it:

   ```sql
   ON CONFLICT (conversation_id) DO UPDATE SET space_id = COALESCE(conversations.space_id, excluded.space_id), forked_from = excluded.forked_from
   ```

   The sync's own upsert (`opencode.go:401`) does not set `forked_from`, so later syncs keep it.

3. **Open a remote source at `/oc/`.** **Open original conversation** navigated with `sessionPath`, which always
   builds `/s/`. It now uses `rowPath({ id: session.forkedFrom!, kind: session.kind })`
   (`internal/rocketclaw/web/src/ui.tsx:2512`, `rowPath` at `ui.tsx:70`). A fork's source has the fork's kind.

4. **Show it on the tab.** `tabActivity` (`ui.tsx:1568`) picks the fork glyph in place of the chat icon when
   `forkedFrom` is set (`ui.tsx:1569`) and adds "Forked session" to the tab's description.

The regression test now asserts through the RPC the sidebar uses, `listSessions`, and fails without step 1
(`internal/rocketclaw/frontend/rpc/opencode_test.go:158`, `opencode_test.go:167`):

```go
assert.Equal(t, map[string]string{created.Id: "", forked.Id: created.Id}, forkedFrom, "the Web lists a fork with its source")
```

## Why This Works

Local and remote conversations live in one `conversations` table but reach the Web through two hand-written
`SELECT` lists joined by `UNION ALL`. `UNION ALL` matches columns by position, so the remote branch must put a
value in every slot; it uses constants for facts remote rows do not have (creator, Slack channel, cron path, the
origin columns). `forked_from` had been one of those constants from before remote rows could be forks. Once a remote
fork had a real value, the constant silently hid it: no error, no type mismatch, just an empty string.

## Prevention

- When a conversation fact starts to matter for Remote OpenCode Sessions, check its slot in the `remote` branch
  of `sidebarSessions`. Every remaining `''`, `FALSE`, or `NULL` there is a claim that remote rows never have
  that fact.
- Test a Web-visible fact through `listSessions` (or the RPC the Web calls), not by reading the row. A row test
  proves the write; only the list proves the Web gets it.
- A new write path for remote rows has to cope with the sync having inserted the row first: put the fact in the
  `ON CONFLICT` update as well, and keep the sync's upsert from overwriting it.
- Navigating to a session by ID needs its kind: use `rowPath`, not `sessionPath`, unless the session cannot be a
  remote one.
- Only forks made through RocketClaw's fork call record a source. The sync reads no fork source from OpenCode's
  session data (`openCodeSession`, `opencode.go:37`), and remote forks made before this fix stay unmarked.

## Related Issues

- [An External MCP Slack Thread Lost Its Metadata When Hidden Runs Stopped Copying History](external-mcp-thread-lost-metadata-after-copy-free-hidden-runs.md): the same shape, a value stored correctly but missed by one reader in `store.go`, hidden by a test that skipped the production path.
- [Tests That Count PostgreSQL Reads Must Turn Autovacuum Off for Their Tables](../best-practices/read-count-tests-need-autovacuum-off.md): the read-count tests around the same listing SQL.
