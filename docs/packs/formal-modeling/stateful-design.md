---
title: Stateful behavior is drawn as a state diagram, modeled in PlusCal and checked with TLC before it is built
applies_when:
  - designing or changing a background job, queue, worker, retry, lease, claim or acknowledgment
  - adding behavior where concurrent actors, crashes, timeouts or reordered messages can interleave
  - changing a multi-step lifecycle, a recovery path or a step that cannot be undone
  - reviewing a plan or diff that changes such behavior or a model under docs/verification/
tags: [state-machine, concurrency, pluscal, tla, tlc, model-checking]
---

**Scope.** This applies when correctness depends on the order of events between actors, or on surviving crashes, timeouts or retries. It does not apply to documentation, styling or changes without meaningful state, such as single-request CRUD. When applicability is borderline, the plan records the decision and why.

**Brainstorm.** Draw a Mermaid `stateDiagram-v2` with named states, events, guards, terminal states, and the timeout, retry, cancel and recovery paths. List the assumptions: the actors, what persists and what lives only in a process, message delivery (lost, duplicated, reordered), which steps are atomic, and where a crash can happen. State at least one safety property (something bad never happens) and, when meaningful, one liveness property (something good eventually happens). The diagram explains the design; it proves nothing.

**Plan.** Specify the PlusCal model: variables, processes, initial state, atomic steps (labels), invariants, temporal properties, and each fairness assumption with its reason. Give every TLC check a finite world (constants and sizes) and say which checks must pass and which must find a named violation. Map each model step to the code that will implement it. The first implementation unit is "write, translate and check the model, fix the design for each counterexample, record the evidence", marked pending. A plan never reports a TLC result nobody ran.

**Work.** That unit gates the stateful behavior: building it starts only after TLC passes the plan's checks. Translate with `pcal.trans`, then run `tlc2.TLC` on each `.cfg`; translation alone checks nothing. Fix a counterexample in the design; weakening a property or shrinking a bound is recorded in the evidence. Rerun the model whenever transitions or assumptions change. When Java or the tools are missing, a check does not finish, or TLC fails, the gate is BLOCKED / NOT VERIFIED; only the owner can waive it, in writing, in the plan.

**Where models live.** `docs/verification/<feature>/`, shaped like `docs/verification/thread-pr/`:

- one `.tla` holding the PlusCal algorithm, its properties and the generated translation; edit only outside the translation markers;
- one `.cfg` per check;
- a `Makefile` that downloads `tla2tools.jar` v1.8.0 and verifies SHA-256 `e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f`, keeps every tool, metadata and log file under the repository root's `.tmp/`, requires `Model checking completed. No error has been found.` from a passing check, and requires TLC exit status 12 or 13 plus the named violation from a check that must fail, so a parse error never counts as an expected failure;
- a `README.md` evidence record: the command, the checked properties, bounds, assumptions, TLC and Java versions, outcome, state counts, run time, and what the model leaves out. The Mermaid diagram lives there or in the plan.

**Review.** Report a missing diagram, model or evidence record; a model whose steps or properties disagree with the diagram, the plan or the code; a model not rerun after the behavior changed; and an evidence record that claims more than the finite model shows.

**Limits.** A pass means the properties hold for the explored finite model under its assumptions, not that the code is correct. CI does not run these models, because TLC can run for a long time, so the gate holds only when the agent and the reviewer run `make -C docs/verification/<feature>`.
