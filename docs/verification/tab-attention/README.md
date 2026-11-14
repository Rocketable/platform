# Tab attention: bounded verification

`TabAttention.tla` models one person's attention state for one tab: the server's unread mark, park and timer, and
each browser window deciding on its own whether to raise a desktop pop-up. The design, its state diagram and its
rules are in `docs/specs/2026-11-09-tab-attention-design.md` ("The state"); the build plan is
`docs/plans/2026-11-10-0947-feat-tab-attention-plan.md`. **Passing checks do not prove the Go or TypeScript
implementation correct.**

## Run

From the repository root:

```sh
make -C docs/verification/tab-attention              # translate, then every check
make -C docs/verification/tab-attention one-window   # one check
```

Requires Java 11+, `curl` and Make. The Makefile downloads `tla2tools.jar` v1.8.0 and verifies SHA-256
`e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f`. The tools, the translator's copy, Java's
temporary files, TLC's working files and each check's log go to the repository root's
`.tmp/verification/tab-attention/`. A check passes only when TLC exits 0 and reports
`Model checking completed. No error has been found.`

## What each check proves

Both checks run the design as built (every `CONSTANTS` switch set to the built shape) and check:

- invariants `TypeOK`, `NoPopWhileParked`, `NoPopSettled`, `NoPopWithoutTab`, `NoStaleFire`, `OneTimer`,
  `NoUntimedParkClosed`, `HoldOnlyInView`, `DoneEndsAtTurnEnd`;
- temporal properties `NoLostPop` (a pop-up owed is raised or handled), `ParkWakes` (a timed park ends) and
  `TimerBringsTabBack` (a closed tab with a timer comes back);
- every state its `_POSSIBLE` line names is reached, so a check cannot pass because the model never gets to a pop-up
  or a reopened tab. TLC fails a check whose `_POSSIBLE` state is never reached.

| Check | World | Distinct states | Depth | TLC time |
|---|---|---|---|---|
| `one-window` | one window; the person may close the tab once and open it again | 351,172 | 25 | 33 s |
| `two-windows` | two windows; the tab stays open | 412,014 | 21 | 42 s |

Both share `MaxTime = 2` and `MaxEvents = 3`. Both at once would take too long, so they are checked as two worlds,
not as one composed world.

## Assumptions

- The person and the outside world, one process, act when they like; the clock, the server's timer jobs and each
  window's row polls, tab polls and reloads each keep going, fairly on their own (weak fairness per process).
- One person and one tab. A page that is not a session has no agent that asks, so its behaviors are a subset of a
  session's.

## Recorded run

2026-11-13, on macOS (Apple silicon, 12 workers), OpenJDK 21.0.13, TLC `2026.11.06.222357 (rev: d8fb231)`,
`pcal.trans` 1.12: both checks passed; `make` took 78 s including the tools download. Translating changed nothing
in `TabAttention.tla`.

## Limits

- Bounded: at most 3 events, a clock that runs to 2, at most two windows and one close of the tab.
- The plan's success criterion also names shapes the design replaced (shapes 1 to 5 and 8) that must still fail.
  Their configs are not kept here, so this Makefile no longer shows that the properties catch those designs.
- Several people and several tabs are abstracted to the one person and one tab above; a Shared tab is checked as
  that person's ordinary tab (`SharedQuiet = FALSE`, the built rule).
