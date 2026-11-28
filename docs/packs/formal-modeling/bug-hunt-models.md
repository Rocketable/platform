---
title: A model that hunts a bug covers the whole path from the code, states what the user sees, and is written before the cause is known
applies_when:
  - someone asks for a PlusCal- or TLC-driven investigation of a bug in existing code
  - a bug depends on outages, sleep, reconnects, timeouts or retries across more than one component
  - a model is written after the cause is suspected, or only around the suspected component
  - a fix changes when a view, worker or connection is torn down or rebuilt
tags: [pluscal, tla, tlc, model-checking, debugging, liveness]
---

**Scope.** This applies when a model is used to find the cause of a bug in code that already exists, not only to gate a new design.

**Write the model before settling on a cause.** List, from the code, every state that decides what the user sees: both sides of any host/embedded boundary, every network segment, every shared load or cache, every timeout and retry. Model the environment nondeterministically (outages at any step on each segment, every request result the client distinguishes), with outages bounded so liveness checks finish.

**State the property as the outcome the user needs**, not as the reported symptom being gone. "The transcript ends up on screen" catches a stuck error page that "the tab is not blank forever" passes.

**Let TLC list the failures.** Reproduce each counterexample in a harness or browser before believing it. Never add a switch or assumption only so a check comes out as expected; cite the code behind every assumption change in the evidence record.

**Give each fix its own switch and a check that must fail without it**, and treat a property that holds as a finding too: it rules out a suspected cause or an unneeded fix, and the evidence record's limits say what the model leaves out.

**Model a lifecycle-changing fix in the same model before shipping it.**

See `docs/solutions/best-practices/pluscal-bug-hunt-models-the-whole-path-before-the-cause-is-known.md` and `docs/verification/oc-tab-recovery/`.
