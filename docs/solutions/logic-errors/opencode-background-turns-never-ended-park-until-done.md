---
title: A Remote OpenCode Turn Started by Background Work Never Ended a Park Until It Is Done
date: 2026-11-11
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: service_layer
symptoms:
  - "A Remote OpenCode Session's tab parked until it is done stayed in Parked after the session finished a turn."
  - "The stuck sessions had background work: a subagent or shell command had finished and OpenCode had started a turn on its own."
  - "web_tab_attention kept until = 'infinity' while conversations.read_through had already reached the turn's idle marker."
  - "People with the session in their tabs were not marked unread for those turns either."
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - database
tags:
  - opencode
  - remote-opencode-session
  - park
  - attention
  - wake-turn
  - idle-marker
  - tla-plus
---

# A Remote OpenCode Turn Started by Background Work Never Ended a Park Until It Is Done

## Problem

A park until it is done ends at the session's next turn end. For a Remote OpenCode Session, a turn that OpenCode
started itself, after a background subagent or shell command finished, reached no one when it ended. The park never
ended, and nobody with the session open saw it go unread.

## Symptoms

- The park ended normally after a turn a person prompted from the Web, and stayed after a turn background work started.
- The session's attention row kept `until = 'infinity'`, and its `read_through` cursor sat on the newest idle marker.
  The sync had seen that turn end and still marked no one.
- RocketClaw's own sessions did not show it.

## What Didn't Work

- The tab-attention model passed `DoneEndsAtTurnEnd`. It assumed every turn end reaches the person, and its
  invariant checked the effect, the person's `happened` flag, rather than the turn end itself. The spec also put
  "who an event reaches" outside the model, so a reach bug could not fail any check.
- Looking only at the parked rows suggested a timing or locking problem in the marking SQL. That SQL was fine: it never
  ran, because the sync produced no event row for those turns.

## Solution

The sync turns a new OpenCode idle marker into attention events in `syncOpenCode`
(`internal/rocketclaw/backend/opencode.go:336-340`). It used to make events only from `openCodeSenders`, which reads
`/message?type=user` (`opencode.go:442`). OpenCode starts a turn after background work with a message of type
`synthetic`, not `user` (`Session.Message.Synthetic`, `internal/rocketclaw/oc/packages/schema/src/session-message.ts:98`),
so that list was empty and no event was made. The cursor still moved, so the miss was permanent.

Every new idle marker now also adds an event with an empty sender, which reaches the session's readers:

```go
if ... && len(idle.Data) > 0 && idle.Data[0].Time.Created > last.through {
	if senders, err := openCodeSenders(...); err == nil {
		cursor = &idle.Data[0].Time.Created

		for _, sender := range append(senders, "") {
```

The `> last.through` guard matters. Without it, a session whose `time.updated` moves with no new turn end, as on a
rename, would mark its readers again for an old idle marker.

## Why This Works

RocketClaw's own listed sessions always emit the turn itself with an empty username, whoever sent it (`turnEventsSQL`,
`internal/rocketclaw/backend/attention.go:98`, called from `markTurnEnd`, `store_dao.go:1803`). The OpenCode sync now
does the same. `sessionReadersSQL` sends any event row to the people with the session's tab and to everyone who
parked it until it is done (`attention.go:79-84`), so a single row with an empty sender is enough.

## Prevention

- When a behavior keys on "a turn ended", check both producers: `markTurnEnd` for RocketClaw's sessions and
  `syncOpenCode` for Remote OpenCode Sessions. The remote path derives turns from OpenCode's message stream, and a
  turn there need not start with a `user` message.
- `TestSyncOpenCodeMarksTurnSenders` (`opencode_test.go:656-669`) covers both cases: a turn with no prompt ends a
  park until it is done, and a moved `time.updated` without a new idle marker does not.
- In the model, a ghost variable tracks the cause (`endedDone`, set on every turn end, `TabAttention.tla:86`), not its
  effect, and `WakeReaches` chooses whether a background turn end reaches anyone (`TabAttention.tla:46`, `:124`).
  With `WakeReaches = FALSE`, `DoneEndsAtTurnEnd` (`TabAttention.tla:641`) fails in a three-state trace: park until
  done, then a background turn end. An invariant over a variable only the reach path sets cannot see a reach bug.
- To diagnose a stuck park on a remote session, list the session's messages from the OpenCode server,
  `/api/session/<id>/message?order=desc`. Idle markers with only `synthetic` messages since the previous one are
  turns background work started.

## Related Issues

- `docs/specs/2026-11-09-tab-attention-design.md`, "Fixed (2026-11-11): a turn no prompt started also ends it".
- `docs/solutions/logic-errors/remote-opencode-fork-marker-dropped-by-sidebar-remote-branch.md`: another fact that held
  for RocketClaw's sessions and went missing on the separate Remote OpenCode Session path.
