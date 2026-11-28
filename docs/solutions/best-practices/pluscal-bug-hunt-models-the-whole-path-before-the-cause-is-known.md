---
title: A PlusCal Bug Hunt Models the Whole Path From the Code Before the Cause Is Known
date: 2026-11-27
category: docs/solutions/best-practices/
module: docs/verification
problem_type: best_practice
component: development_workflow
severity: high
applies_when:
  - "Someone asks for a PlusCal- or TLC-driven investigation of a bug in existing code"
  - "A bug depends on network outages, sleep, reconnects, timeouts or retries across more than one component"
  - "A model under docs/verification/ is written after the cause is already suspected, or only around the suspected component"
  - "A fix changes when a view, worker or connection is torn down or rebuilt, and it ships without being modeled"
related_components:
  - internal/rocketclaw/oc
  - internal/rocketclaw/web
  - docs/verification/oc-tab-recovery
tags:
  - pluscal
  - tla
  - tlc
  - model-checking
  - debugging
  - model-scope
  - liveness
  - opencode
---

# A PlusCal Bug Hunt Models the Whole Path From the Code Before the Cause Is Known

## Context

An OpenCode tab in RocketClaw's Web could go blank after a network problem (a laptop waking, Wi-Fi dropping, a
server restarting) and stay that way until the page was reloaded. The request was a PlusCal-driven investigation.
`docs/packs/formal-modeling/stateful-design.md` describes a model as a design gate: draw it, model it and check it
before building. It says nothing about using a model to find a bug in code that already exists, and the first
attempt went wrong in a way the gate does not catch.

**First attempt: diagnose, then model one path.** The cause was found by reading code and running browser probes:
a failed transcript load of the shown session was never tried again. Only then was a model written, and it covered
that one path: one session, its transcript load, the event stream and the timeline's 30-second timeout. It was
`docs/verification/oc-transcript-load/`, which the broad model below replaced and which no longer exists. In
practice:

- Each check was written to what it "should find". Several early counterexamples were mistakes in the model, such as
  missing fairness or a step slipping between two actions. They were fixed until every check matched what was
  expected. A switch that opens the session only while the network is healthy was added so one check would pass
  and "explain" why the bug came and went. Each change could be justified from the code, but the known answer
  chose the changes.
- It found one real thing the probes had missed: a reconnect joins a stalled load that started while the event
  stream was down, instead of starting a new one.
- It could not show the fully empty view the user reported, the session's metadata load, or RocketClaw's own
  `Identity` check, which tore the whole OpenCode view down and rebuilt it. The `Identity` change shipped without a
  model.
- Its passing `retry` check gated a fix that still left two stuck error pages.

**Second attempt: model first, from the code.** Before deciding where the bug was, every state that decides what an
OpenCode tab shows after a network event was listed from the code. All of it went into
`docs/verification/oc-tab-recovery/OcTabRecovery.tla`:

- the `Identity` teardown and rebuild (`ServerSessions` and `useRemoteView` in `internal/rocketclaw/web/src/ui.tsx`);
- two separate network segments, browser to RocketClaw (`rcDown`) and RocketClaw to OpenCode (`ocDown`);
- the event stream and the shared loads (`createSync` in `internal/rocketclaw/oc/packages/client/src/solid/data.ts`);
- session resolution, the timeline and the health probe;
- every kind of request result: it loads, fails at once, stalls until the 60-second cutoff, or gets a server error.

Outages could start and end at any step, up to two of them. The property was what the person needs to see:
`Recovers == Settled ~> Good`, where `Good` means the view is mounted, the session is resolved, its transcript has
loaded, and the timeline is syncing or showing it rather than its error page. Each TLC counterexample was then
reproduced in the host harness or a real browser before anyone believed it. This model found three things the first one could not:

1. **Two more stuck shapes, both on OpenCode's error page.** With the transcript retry in place, a single failure to
   reach the server during the transcript load or the metadata load still left "Something went wrong" up until the
   person moved to another session or reloaded (`no-timeline` and `no-metadata` in
   `docs/verification/oc-tab-recovery/README.md`). Both were reproduced in the host harness before the fix, and both
   recover in a real browser after it.
2. **The fully empty tab never sticks.** `before-empty` passes against the code before the fix: within the 30-second
   resolution timeout, an empty tab turns into "Can't reach this server" or loads. So a tab that stays empty for
   longer needs a cause outside the model, and the README's Limits says so.
3. **The `Identity` change is not needed for recovery.** `without-identity` passes: rebuilding the view recovers on
   its own. The change only shortens the empty spells and keeps the draft: a separate improvement, not part of the
   recovery fix.

## Guidance

When a model is meant to find a bug, write it before you settle on a cause, and judge it by what it finds that you
did not already know.

1. **List the states from the code first, across every boundary.** Before naming a suspect, list every piece of state
   that decides what the user sees: both sides of a host/embedded boundary, every network segment, every cache or
   shared load, every timeout and every retry. Model the environment with nondeterministic choices: outages at any
   step on each segment, and every request result the client can tell apart. Keep outages bounded (`MaxOutages`) so
   liveness checks finish.
2. **State the property as the outcome the user needs, not as the reported symptom being gone.** In
   `OcTabRecovery.tla`, `NoStuckBlank` (the tab is not blank forever) passed with the transcript retry alone, while
   `Recovers` (the transcript ends up on screen) failed. An error page is not blank, so a property shaped like the
   symptom passes code that is still broken.
3. **Let TLC list the failures, then reproduce each one.** A counterexample is a hypothesis until a harness or browser
   shows it. Do not add a switch or assumption just so a check comes out the expected way. When you change an
   assumption to remove a counterexample, cite the code that justifies it in the README's Assumptions.
4. **Give each fix its own switch and a check that must fail without it.** `no-retry`, `no-timeline` and `no-metadata`
   each turn one fix off and must break `Recovers`, so no fix stays in without a reason.
5. **Use passing checks to drop work, too.** A property that holds (`before-empty`, `without-identity`) is a finding:
   it removes a suspected cause or an unneeded fix, and it shows where the model's Limits must say what is left out.
6. **Model a fix that changes a lifecycle before shipping it.** The `Identity` change decides when the view is torn
   down. It belongs in the same model as the loads it affects, not in a separate model of the view alone.

## Why This Matters

The narrow model looked like the formal-modeling gate was met: a `.tla`, `.cfg` files, a Makefile, and every check
matching its expected outcome. Yet it was built to match an answer already found, so it mostly restated that
answer. It left two "broken until reload" shapes unfixed, gave no answer about the empty tab the user reported, and
could not tell whether the `Identity` change mattered. Fixing that took a second model and a second round of fixes,
tests and review. The broad model decided both what to fix and what to leave out.

## When to Apply

- A bug report involves timing, sleep, reconnects or retries, and someone asks for formal modeling to find the
  cause.
- A model's checks are being edited until the expected outcomes appear, especially by adding assumptions that turn a
  counterexample into a pass.
- Reviewing a `docs/verification/<feature>/` record for a bug fix: check that the model covers the whole path to what
  the user sees, that its property is about that outcome, and that each fix has a check that must fail without it.

## Examples

Narrow, written after diagnosis (the replaced model): the property follows the suspected component, and the model
holds nothing else.

```tla
Settled  == opened /\ Healthy /\ stream = "connected"
Recovers == Settled ~> (loaded /\ timeline \in {"syncing", "ready"})
```

Broad, written from the code first: the property says what the person on the tab sees, and the model includes the
teardown, both network segments and the metadata load around it.

```tla
Settled  == opened /\ ~Outage /\ outages = MaxOutages
Good     == list = "present" /\ mounted /\ res = "ready" /\ loaded /\ timeline \in {"syncing", "ready"}
Recovers == Settled ~> Good
```

## Related

- `docs/packs/formal-modeling/stateful-design.md`: the design gate. It covers modeling a design before building it,
  not using a model to hunt a bug.
- `docs/verification/oc-tab-recovery/README.md`: the broad model, its checks, counterexamples, assumptions and limits.
- `docs/solutions/best-practices/a-tlc-models-initial-state-must-include-what-a-migration-leaves-behind.md`: another
  way a model's scope hides cases, through its starting states.
- `docs/solutions/best-practices/fake-opencode-event-stream-without-keepalives-hides-stuck-loads.md`: the other
  lesson from the same investigation, about the test fake.
