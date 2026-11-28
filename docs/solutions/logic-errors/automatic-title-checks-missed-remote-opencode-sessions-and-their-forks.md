---
title: Automatic Title Checks Missed Remote OpenCode Sessions, Then Missed Their Forks
date: 2026-11-27
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: service_layer
symptoms:
  - "With title_decision_model set, a Remote OpenCode Session kept its first title however far the conversation moved; no title check ever ran for it."
  - "After the check moved to the OpenCode sync, sessions created or forked from the RocketClaw Web were still never checked, while sessions the sync found on its own were."
  - "A forked session's row had name equal to its title and generated_name NULL, so the generated-name filter never matched it."
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - database
tags:
  - opencode
  - remote-opencode-session
  - session-title
  - turn-end
  - fork
  - generated-name
  - opencode-sync
---

# Automatic Title Checks Missed Remote OpenCode Sessions, Then Missed Their Forks

## Problem

Every fourth message a person sends, RocketClaw asks a decision model whether the session title still fits and
names the session again when it does not. That never happened for Remote OpenCode Sessions, because the check ran
only inside `Bridge.runTurn` and their turns never reach a `Bridge`. Moving the check into the OpenCode sync fixed
sessions the sync found, but sessions created or forked from RocketClaw are inserted by a second statement that did
not set the `generated_name` mark the check depends on.

## Symptoms

- A Remote OpenCode Session kept the title OpenCode gave it from the first message, even with `title_decision_model` set.
- After the first fix, a session the sync discovered was checked on its fourth prompt; the same session recorded
  through **New session** or **Fork** never was.
- That row showed `name = title` and `generated_name IS NULL`.

## What Didn't Work

- **Wiring the check where the Web turn starts.** The original check lives in `Bridge.runTurn`
  (`internal/rocketclaw/backend/bridge.go`), guarded by `msg.Human && msg.Source == protocol.SourceWeb`. A prompt typed
  in an OpenCode tab goes from the browser through the `/oc/{server}/api/...` proxy (`handleOpenCode` in
  `internal/rocketclaw/frontend/rpc/relay.go`) straight to the OpenCode server. RocketClaw never runs a turn for it, so
  nothing in `runTurn` applies. Its tests (`TestEveryFourthWebMessageChecksTheSessionTitle` in `bridge_test.go`) only
  drive `Bridge`, so they could not notice.
- **Marking only the sync's own insert.** The first move to the sync set `generated_name` in the sync upsert
  (`storeOpenCodeSync` in `internal/rocketclaw/backend/opencode.go`) and added migration `121_remote_title_origin.sql`
  for existing rows. The new test started from a session the sync found, and passed. A data-migration review found
  the gap: `CreateOpenCodeSession` and `ForkOpenCodeSession` both call `recordOpenCodeSession`, which inserts the row
  first and did not set `generated_name`. The sync then takes its `ON CONFLICT` branch, which copies the title into
  `name` but changes `generated_name` only when the title differs from the stored one. A fork keeps its source's title,
  so its mark stayed `NULL` and `retitle`'s filter, `name = '' OR name = generated_name`, never matched.

## Solution

1. **Check at the sync's turn end.** `syncOpenCode` already detects a finished remote turn: a new idle marker newer
   than the stored `read_through`. It now also records the previous cursor (`throughs`), and after the sync commits,
   `storeOpenCodeSync` starts `retitleOpenCode` for each ended session on `s.titles`. `retitleOpenCode` counts the
   session's `type=user` prompts up to the turn's idle marker and runs the check when that count crosses a multiple of
   `titleCheckEvery`. With `through == 0` it does nothing, so counting starts at the first turn RocketClaw sees end and
   a newly added server's old sessions are left alone.
2. **One retitle implementation.** `Bridge.retitle` became `SessionService.retitle` (`bridge.go`), shared by both paths.
3. **Send the title to OpenCode first.** `NameGenerated` (`internal/rocketclaw/backend/store.go`) locks the row,
   sends a remote session's new title to OpenCode with `renameOpenCode`, then stores `name`, `generated_name`, `title`
   and `synced_at` together, so a sync pass already in flight cannot write the old title back. A single `UPDATE`
   without the lock was rejected for that reason. It renames only while the name is still the one the check judged,
   so two overlapping checks, or a rename made in OpenCode during a check, never get a title nobody judged replaced.
   `docs/verification/opencode-title-check/` model-checks these writers together and found that gap.
4. **Mark every insert of a remote row.** Both inserts now write the title as `generated_name`:

   ```sql
   -- recordOpenCodeSession, before
   INSERT INTO conversations (..., server, title, directory, ...)
   SELECT ..., $2, $3, $4, ...
   -- after
   INSERT INTO conversations (..., server, title, generated_name, directory, ...)
   SELECT ..., $2, $3, $3, $4, ...
   ```

   The sync upsert inserts `title` as both `name` and `generated_name`, and on conflict moves `generated_name` to the
   new title only when OpenCode's title changed and no local write is newer than the pass.
5. **Keep a typed name typed when RocketClaw's save fails.** "OpenCode's title changed" is how the sync recognizes
   a title from OpenCode, so a typed name OpenCode accepted just before RocketClaw's commit failed would come back
   looking like one. `UpdateConversationDetails` records the name in `typed_titles`, in its own commit, before sending
   it, and removes it when the rename commits; the sync never marks a recorded title generated and drops it once
   OpenCode lists it. `pushOpenCodeNames` now locks the row, re-checks that the name still waits to be sent, and writes
   the title under that lock, and the sync keeps a name that still waits (`name NOT IN ('', title)`), so a refused push
   no longer loses it and a pushed name needs no record. `TestSyncOpenCodeKeepsATypedTitleTyped` fails the commit with
   a deferred trigger; `TestOpenCodeMovesOldNamesToTheServer` checks the lock and the re-check.

`TestSyncOpenCodeChecksTheTitleEveryFourthPrompt` (`internal/rocketclaw/backend/opencode_test.go`) runs its whole
scenario twice: `recorded=false` lets the sync find the session, `recorded=true` first inserts it through
`recordOpenCodeSession` as create and fork do. Removing `generated_name` from `recordOpenCodeSession`'s insert fails
`recorded=true` and leaves `recorded=false` passing, which is the shape of this bug.

## Why This Works

A Remote OpenCode Session's agent runs on its OpenCode server, and RocketClaw notices that one of its turns ended
when the sync sees a new idle marker. Anything RocketClaw does per turn for its own sessions has to be hung there as
well to reach remote ones.

The title check acts only on names RocketClaw considers generated. For remote rows the rule is that a title from
OpenCode, including a rename made there, counts as generated, and only a name typed in RocketClaw does not:
`UpdateConversationDetails` sends the typed name to OpenCode and stores it as `name` and `title` without touching
`generated_name`, so a typed name stops matching, and the next sync leaves the mark alone because the title did not
change. That
rule holds only if every row's mark starts out equal to the title it was inserted with. Remote rows have two
inserts, the sync upsert and `recordOpenCodeSession`, and whichever runs first decides the inserted columns; the other
reaches only its `ON CONFLICT` clause. For create and fork, `recordOpenCodeSession` usually runs first, so a column
that only the sync's `INSERT` sets is never set for those rows unless the sync's conflict clause happens to write it.

## Prevention

- When a feature acts on a person's Web message or turn in `Bridge.runTurn`, decide explicitly what happens for Remote
  OpenCode Sessions. Their prompts bypass `Bridge`; the remote equivalent is the turn-end detection in `syncOpenCode`.
  Test that path with the fake OpenCode server in `opencode_test.go`, not only with `Bridge`.
- When the sync upsert starts setting a column on insert, set it in `recordOpenCodeSession`'s insert too, or make the
  sync's `ON CONFLICT` clause write it unconditionally. Check what the row looks like when `recordOpenCodeSession`
  inserted first, since a conditional conflict clause is skipped exactly when nothing on OpenCode's side changed.
- Run a remote-row test from both starting points, a sync-found session and a recorded one, as
  `TestSyncOpenCodeChecksTheTitleEveryFourthPrompt` does with its `recorded` subtests.
- Owner follow-up (not done here, it changes product code): the two inserts of a remote `conversations` row list
  their columns separately. Having one statement or one shared column list for both would remove this class of gap.

## Related Issues

- [A Remote OpenCode Turn Started by Background Work Never Ended a Park Until It Is Done](opencode-background-turns-never-ended-park-until-done.md):
  another behavior keyed on a turn end that was missing on the remote path.
- [A Remote OpenCode Fork Lost Its Marker in the Sidebar List's Separate Remote Branch](remote-opencode-fork-marker-dropped-by-sidebar-remote-branch.md):
  the opposite direction across the same two inserts, a fact `recordOpenCodeSession` sets that the sync must not undo.
- [A New Model Call in Web Turns Reaches Every Test Fake Provider](../best-practices/new-model-calls-in-web-turns-reach-every-test-fake-provider.md):
  the title model calls `Bridge.runTurn` starts in the background.
