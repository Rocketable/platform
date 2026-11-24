# Tab attention: bounded verification

`TabAttention.tla` models one person's attention state for one tab: the server's unread mark, park and timer, and
each browser window deciding on its own whether to raise a desktop pop-up. The design, its state diagram and its
rules are in `docs/specs/2026-11-09-tab-attention-design.md` ("The state"); the build plans are
`docs/plans/2026-11-10-0947-feat-tab-attention-plan.md`, for parking a session until it is idle,
`docs/plans/2026-11-18-0900-feat-park-until-idle-plan.md`, and for Follow,
`docs/plans/2026-11-19-1133-feat-card-folding-shared-group-follow-plan.md`. **Passing checks do not prove the Go or TypeScript
implementation correct.**

## Run

From the repository root:

```sh
make -C docs/verification/tab-attention                     # translate, then every check
make -C docs/verification/tab-attention one-window          # one check
make -C docs/verification/tab-attention fail-old-turn-end   # the replaced rule must fail
make -C docs/verification/tab-attention fail-follow-ignored # a server that ignores Follow must fail
```

Requires Java 11+, `curl` and Make. The Makefile downloads `tla2tools.jar` v1.8.0 and verifies SHA-256
`fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713`. The tools, the translator's copy, Java's
temporary files, TLC's working files and each check's log go to the repository root's
`.tmp/verification/tab-attention/`. A check passes only when TLC exits 0 and reports
`Model checking completed. No error has been found.` `fail-old-turn-end` passes only when TLC exits 12 or 13 and
reports `Invariant DoneEndsOnlyWhenIdle is violated`, and `fail-follow-ignored` only when it reports
`Invariant NoMarkWhileUnfollowed is violated`.

## What each check proves

On 2026-11-21, `make all` passed with TLC `2026.11.19.180144` (rev `bc1b506`) and OpenJDK `21.0.13.1`.

Both checks run the design as built (every `CONSTANTS` switch set to the built shape) and check:

- invariants `TypeOK`, `NoPopWhileParked`, `NoPopSettled`, `NoPopWithoutTab`, `NoStaleFire`, `OneTimer`,
  `NoUntimedParkClosed`, `HoldOnlyInView`, `DoneEndsOnlyWhenIdle` (a park until it is done never ends while the
  session has work, unless a turn failed or the agent asked), `ResumeOnlyUnparked`, `NoMarkWhileUnfollowed` (the server
  writes no mark on a tab the person does not follow; only a mark they set themselves), `NoPopWhileUnfollowed` and
  `QuestionLeavesUnfollowedPark` (a question leaves the park of a tab the person does not follow alone);
- temporal properties `NoLostPop` (a pop-up owed is raised or handled), `ParkWakes` (a timed park ends),
  `TimerBringsTabBack` (a closed tab with a timer comes back), `IdleEndsDone` (a park until it is done ends once
  the session is idle after a turn ended) and `QuestionRestores` (once no question waits, the park a question ended
  comes back or is dropped);
- every state its `_POSSIBLE` line names is reached, so a check cannot pass because the model never gets to a pop-up
  or a reopened tab. TLC fails a check whose `_POSSIBLE` state is never reached.

| Check | World | Distinct states | Depth | TLC time |
|---|---|---|---|---|
| `one-window` | one window; the person may close the tab once and open it again | 6,771,964 | 33 | 1 h 02 min |
| `two-windows` | two windows; the tab stays open | 7,654,433 | 31 | 42 min |
| `fail-old-turn-end` | `one-window` with `EndsAtIdle = FALSE`, the earlier rule, and no Follow moves; checks `DoneEndsOnlyWhenIdle` only | 1,471 | 8 | under 1 s, violated as required |
| `fail-follow-ignored` | `one-window` with `FollowGates = FALSE`, a server that ignores Follow; checks `NoMarkWhileUnfollowed` only | 489 | 6 | under 1 s, violated as required |

Both share `MaxTime = 2`, `MaxEvents = 3` and `MaxFollowMoves = 2`, and the tab may start followed or not: an
unfollowed start is an Unfollow before anything happens. Both at once would take too long, so they are checked as two worlds,
not as one composed world.

## Assumptions

- The person and the outside world, one process, act when they like; the clock, the server's timer jobs, its loop
  that ends parks at idle, its loop that puts back the park a question ended, and each window's row polls, tab polls
  and reloads each keep going, fairly on their own (weak fairness per process).
- The session's work is one flag: a turn running, a message queued or a background job that will wake the agent.
  A turn end may leave more work, and may fail; whoever it reaches with a mark, it records work for the park and a
  failure ends it, as the server's per-session update does. Anyone may answer a question, also someone who shares
  the session, and a second question keeps the park the first one remembered.
- Each server step is atomic, as each is one SQL statement in one transaction.
- One person and one tab. A page that is not a session has no agent that asks, so its behaviors are a subset of a
  session's.
- Follow and Unfollow are the person's own steps, at most `MaxFollowMoves` of them. Unfollowing clears the mark and
  keeps any park, its timer and the park a question ended. While the person does not follow the tab, the server writes
  no mark for a turn end, news, a question or a park's end, and a question leaves the park alone; a failed turn, the
  idle loop and a timer still end a park, without a mark. Mark as unread still works.

## Recorded run

2026-11-18, on aarch64 (16 workers), OpenJDK 21.0.13, TLC `2026.11.06.222357 (rev: d8fb231)`, `pcal.trans` 1.12:
`one-window` and `two-windows` passed and `fail-old-turn-end` found `DoneEndsOnlyWhenIdle` violated, as required.
The release now serves a rebuilt `tla2tools.jar` under the same v1.8.0 tag, which fails the pinned checksum; the run
used a local copy of the pinned jar, whose SHA-256 matches `TLA_SHA256` and whose manifest names revision `d8fb231`.
Both new liveness properties were also checked for teeth on throwaway copies: with the idle end disabled, TLC finds
`IdleEndsDone` violated, and with the restore disabled, `QuestionRestores`.

Rerun 2026-11-18 after the `tla2tools.jar` pin moved to build `bc1b506` (one upstream commit, "Fix races in TLCSet/TLCGet with multiple workers"): TLC `2026.11.15.002146 (rev: bc1b506)`, OpenJDK 21.0.13 on linux/aarch64; `make -k` exited 0.

Rerun 2026-11-19 with Follow: TLC `2026.11.15.002146 (rev: bc1b506)`, OpenJDK 21.0.13 on linux/aarch64 (16 workers);
every check passed or failed as required, and both worlds reached `UnfollowedParkAsked` and `PopsAfterRefollow`. The
release again serves a rebuilt `tla2tools.jar` that fails the pin; the run used a local copy of the pinned jar, whose
SHA-256 matches `TLA_SHA256` and whose manifest names revision `bc1b506`.

## Limits

- Bounded: at most 3 events, a clock that runs to 2, at most two windows, one close of the tab and two Follow moves.
- The plan's success criterion also names shapes the design replaced (shapes 1 to 5 and 8) that must still fail.
  Their configs are not kept here, so this Makefile no longer shows that the properties catch those designs.
- Several people and several tabs are abstracted to the one person and one tab above; a Shared tab is checked as
  that person's ordinary tab (`SharedQuiet = FALSE`, the built rule).
- The model does not see how the server tells that a session is idle, whether a queued turn becomes visible before
  the turn ending ahead of it is closed, or what OpenCode reports; it trusts the work flag. Those are Go tests, and
  `docs/verification/session-done/` checks how an OpenCode park learns its session is done.
