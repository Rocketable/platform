---
title: A Stale OpenCode Sync Ended a New Park Until It Is Done Before Its Prompt Ran
date: 2026-11-24
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: service_layer
symptoms:
  - "An auto-park on a Remote OpenCode Session's tab ended a second or two after a message was sent, while the agent was still working on it."
  - "It happened only when the message was sent a few seconds after the previous turn ended, so it looked random."
  - "Park until it is done, set by hand on an OpenCode tab shortly after a turn ended, could end at once in the same way."
root_cause: concurrency
resolution_type: code_fix
severity: medium
framework_version: opencode 2.4.1
related_components:
  - internal/rocketclaw/frontend/rpc
  - database
tags: [opencode, remote-opencode-session, park, auto-park, idle-marker, stale-read, clock, tla-plus]
---

# A Stale OpenCode Sync Ended a New Park Until It Is Done Before Its Prompt Ran

## Problem

RocketClaw learns about a Remote OpenCode Session by polling. One sync pass reads OpenCode in several requests and
then commits what it saw. A pass that read OpenCode before a new prompt arrived could commit after the park for that
prompt was saved. It then recorded the previous turn's end as the end of the new park's work, and the park ended while
the new turn was still running.

## Symptoms

- Sending a message from the OpenCode view parked the tab, and the tab came back on its own about two seconds later
  with the agent still busy.
- It only happened when the message went out within about one sync interval (5 seconds at the time) after the
  previous turn ended. Messages sent later parked correctly.
- The attention row looked normal afterwards, because a person who parks the tab again by hand overwrites it. Server
  logs from the moment were gone. The proof came from a TLA+ model and a Go test against real Postgres.

## What Didn't Work

All of these were checked with TLC before any code was written. The kept model is
`docs/verification/session-done/Doorbell.tla`; the first two attempts were checked in a smaller debugging model that
was not kept.

- **Stamping the session at send time and having older syncs skip it.** This reuses the guard that keeps a rename
  from being overwritten: a rename stamps `conversations.synced_at`, and a sync that started earlier keeps the old
  title. It fails because what matters is when a sync *read OpenCode*, not when it started. A sync can start after
  the stamp and still read OpenCode before the relay delivers the prompt.
- **The same stamp, written after OpenCode admits the prompt.** It fails because a stale sync can commit in the gap
  between the park write and the stamp.
- **An idle marker newer than the prompt's admission, alone.** This was the first fix that passed. Once the model
  split OpenCode's turn end into "decide to settle, write the idle marker, settle", TLC found a gap (check
  `a-no-recheck`). A prompt admitted while the previous turn is settling joins no busy period yet. The settling turn
  then writes its idle marker *after* the prompt's admission time, so the marker passes the threshold while the
  prompt has not run at all.
- **Following OpenCode's ordered session log instead of polling** (`/api/experimental/session/:id/log`). It would
  remove the race by design, and `OrderedLog.tla` passes when the log replays missed entries after a reconnect. It
  fails without replay, and `opencode serve` v2.4.1 has none. Replay needs the `events.persist` server option, which
  only OpenCode's hosted runtime sets, and `serve` has no flag or config for it. Reads with `after=` returned only the
  `log.synced` marker. A `follow=true` reader also received nothing in a minute in which the session's log grew by
  161 entries.
- **Making the wake-ups faster without the race fix.** Waking the sync from OpenCode's `session.status` events and the
  park-ending step right after a sync (check `a-no-racefix`) only made the early end happen sooner.

## Solution

An OpenCode park now has a threshold on OpenCode's own clock, stored in `web_tab_attention.after_idle` (migration
`111_web_park_after_idle.sql`). The park ends only when the sync has recorded a newer idle marker *and* a read of
OpenCode's active sessions taken after that marker shows the session idle.

1. **The relay parks after admission, using OpenCode's time** (`handleOpenCode` and `openCodeProxy` in
   `internal/rocketclaw/frontend/rpc/relay.go`). It forwards the prompt first. On a 2xx answer it reads the admitted
   message's `data.time.created` and only then records the `SENT` park with `AttentionChange.admitted`. A refused
   prompt parks nothing. The proxy drops the browser's `Accept-Encoding` so the answer arrives uncompressed and can be
   read. The park write uses `context.WithoutCancel`, so a browser that disconnects right after sending still gets
   its park.
2. **Park until it is done takes the newest idle marker as its threshold** (`ChangeAttention` in
   `internal/rocketclaw/backend/attention.go`). It reads `/api/session/<id>/message?type=idle&order=desc&limit=1`, the
   same request the sync uses. When OpenCode does not answer, the SQL falls back to the stored `read_through`, so
   one unreachable server does not fail the whole click.
3. **The park-ending step compares on one clock** (`parkWorkDoneSQL` in `attention.go`, used by `tickParks`). For a
   remote session it requires `c.read_through > a.after_idle`, where `read_through` is the newest idle marker the sync
   stored. RocketClaw's own sessions still use `worked`.
4. **A failed turn ends a park at once only when it is newer than the threshold** (`endTurnParksSQL` in
   `attention.go`): the sync now passes each ended session's idle-marker time.
5. **The sync reads active sessions again after a new idle marker** (`storeOpenCodeSync` in
   `internal/rocketclaw/backend/opencode.go`). When any session ended in this pass, it calls `/api/session/active`
   once more before committing. A session that is still listed is stored as running, and its failure does not count:

   ```go
   for i, id := range ended {
   	if _, busy := again.Data[strings.TrimPrefix(id, "opencode:"+name+":")]; busy {
   		running[slices.Index(ids, id)], failures[i] = true, false
   	}
   }
   ```

6. **Existing parks were backfilled.** Migration 111 sets `after_idle` from the session's `read_through` for remote
   parks until done and for remote parks a question paused (`resumes AND resume_until = 'infinity'`). It subtracts one
   when the park had already seen its work end, so those parks still end. `TestParkAfterIdleMigration` checks both.

## Why This Works

The old park kept one boolean, `worked`. Any recorded turn end set it (`endTurnParksSQL`), and `tickParks` ended a park
until done once `worked` was set and the session had no work left, such as a running turn, background work
or a queued message. A boolean cannot say *which* turn ended, so a stale commit about the turn before
the prompt looked exactly like the prompt's own turn ending.

The threshold makes "which turn" explicit. Both numbers come from OpenCode's server clock: the threshold is the
admitted message's `time.created` or an idle marker's `time.created`, and `read_through` is an idle marker's
`time.created`. Clock skew between RocketClaw and OpenCode cannot reorder them. A stale sync can only write an idle
marker it actually saw, and every marker from before the prompt is at or below the threshold.

The second read covers the settling gap. In OpenCode v2.4.1, `/api/session/active` lists a session from the start of
a busy period until it settles. A prompt admitted while the session settles starts its successor turn in the same
step that settles, so the session never leaves the active list in between (`run-coordinator.ts` `active` and `settle`,
as recorded in the plan and the verification README). A settling turn's marker can be newer than the threshold, but a
read of active sessions taken *after* the marker was read still shows the session busy. The first read of active
sessions cannot show this, because it happens before the marker is read.

TLC checks the design as built (`a-recovery`, 8.1 million distinct states) with lost feed events, lost browser rings
and periodic recovery. It also shows that removing either the threshold (`a-no-racefix`) or the second read
(`a-no-recheck`) brings back the early end.

## Prevention

- `TestOpenCodeParkWaitsForItsOwnPrompt` in `internal/rocketclaw/backend/opencode_test.go` drives the real
  `syncOpenCode`, `ChangeAttention` and `tickParks` against Postgres. It covers a turn that ended before the prompt
  was admitted, a prompt admitted while a turn settles (`fakeOpenCode.activeAt` answers the two reads of active
  sessions differently), failed turns before and after the threshold, a failed turn OpenCode moves straight past,
  Park until it is done before the last marker is synced, and an unreachable OpenCode server. Run
  `make -C docs/verification/session-done` after any change to how a park learns its session is done.
- **Do not "simplify" away the second read of active sessions or how it reaches the database.** Its override
  reaches the commit only because `running` and `synced[5]` share one backing array in `storeOpenCodeSync`.
  Rebuilding either slice silently brings back the settling race. The test above fails if that happens.
- **OpenCode parks track done-ness in two places.** `worked` still gates `endTurnParksSQL`, and `after_idle` decides
  `tickParks` for remote sessions. Any statement that resets `worked` must reset `after_idle` the same way. Any new
  path that creates a remote park until done must set `after_idle`: with it NULL, `read_through > after_idle` is never
  true and the park never ends.
- The newest-idle-marker request is written twice, in `ChangeAttention` and `syncOpenCode`. The threshold and the
  sync's turn-end detection must read the same marker, so change both together.
- **General rule for any poller of a remote system:** if a pass reads the remote in several requests and then
  commits, assume it can commit after a local write that happened between its reads. Do not order things with a
  local timestamp. Compare positions on the remote's own clock or sequence, and re-read any "still busy" fact after
  reading the fact that would end a wait.
- The fix relies on OpenCode behavior that RocketClaw's tests only fake: the prompt answer carrying `data.time.created`
  in milliseconds, and a session staying in `/api/session/active` into its successor. Rerun the model and check these
  against a real `opencode serve` when upgrading OpenCode. An OpenCode version whose session log replays and streams
  would make the ordered-log design worth reconsidering.

## Related Issues

- `docs/plans/2026-11-24-1409-feat-instant-session-done-plan.md`, KTD1: the mechanism and the OpenCode source behind
  the assumptions above.
- `docs/verification/session-done/README.md`: the park state diagram, the model-to-code map, and every TLC result.
- `docs/solutions/logic-errors/opencode-background-turns-never-ended-park-until-done.md`: the earlier remote-park bug
  on the same sync path, where turns OpenCode started on its own reached no one.
