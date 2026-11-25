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

**Work.** That unit gates the stateful behavior: building it starts only after TLC passes the plan's checks. Translate with `pcal.trans`, then run `tlc2.TLC` on each `.cfg`; translation alone checks nothing. Fix a counterexample in the design; weakening a property or shrinking a bound is recorded in the evidence. Rerun the model whenever transitions or assumptions change. When changing an existing model, run each check the change can affect (`make <check>`) before building, and run the full `make -k` once before the change merges; record both in the evidence. To stop a run, end only its own TLC process, for example by matching its work directory under `.tmp/`; never kill every `tlc2.TLC` process, because other workspaces on this machine check models too. When Java or the tools are missing, a check does not finish, or TLC fails, the gate is BLOCKED / NOT VERIFIED; only the owner can waive it, in writing, in the plan.

**Where models live.** `docs/verification/<feature>/`, shaped like `docs/verification/thread-pr/`:

- one `.tla` holding the PlusCal algorithm, its properties and the generated translation; edit only outside the translation markers;
- one `.cfg` per check;
- a `Makefile` that downloads `tla2tools.jar` v1.8.0 and verifies SHA-256 `fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713`, keeps every tool, metadata and log file under the repository root's `.tmp/`, requires `Model checking completed. No error has been found.` from a passing check, and requires TLC exit status 12 or 13 plus the named violation from a check that must fail, so a parse error never counts as an expected failure;
- a `README.md` evidence record: the command, the checked properties, bounds, assumptions, TLC and Java versions, outcome, state counts, run time, and what the model leaves out. The Mermaid diagram lives there or in the plan.

**When the pinned jar no longer matches.** TLA+'s v1.8.0 is a pre-release that the project rebuilds from `master` and publishes again under the same tag, so its single `tla2tools.jar` (the same file on every operating system) changes with no new version number. A checksum failure on a fresh download therefore usually means a newer build, not a damaged or wrong file. It still fails the gate until the new jar is checked as follows, with the jar the old pin matches (any copy under a `.tmp/` that passes the old checksum) as `old.jar` and the fresh download as `new.jar`:

1. The fresh download is what the release publishes: `gh api repos/tlaplus/tlaplus/releases/tags/v1.8.0 -q '.assets[] | select(.name == "tla2tools.jar") | .digest'` prints `sha256:` and the SHA-256 of `new.jar`.
2. Both jars come from the project's own `master` builds: `unzip -p <jar> META-INF/MANIFEST.MF` shows `Built-By: runner`, `X-Git-Branch: master`, and an `X-Git-Revision` for each.
3. The new revision follows or matches the old one on `master`: `gh api repos/tlaplus/tlaplus/compare/<old revision>...<new revision> -q '.status, .ahead_by'` prints `ahead` and the number of commits, or `identical` and zero for a rebuild from the same revision. For each new commit, `gh api repos/tlaplus/tlaplus/commits/<revision> -q '.commit.message, .files[].filename'` shows what changed.
4. The jars differ only where those commits do: unzip each into its own folder, hash every file (`find . -type f -print0 | sort -z | xargs -0 sha256sum`), and compare. For `identical`, only `META-INF/MANIFEST.MF` may differ. Otherwise, every other differing file must be one those commits change: a standard module `.tla` byte for byte equal to the upstream file at the new revision (`gh api 'repos/tlaplus/tlaplus/contents/tlatools/org.lamport.tlatools/src/tla2sany/StandardModules/<name>.tla?ref=<new revision>' -q .content | base64 -d`), or a `.class` compiled from a changed `.java`. No file may appear or disappear unless a commit adds or removes its source.

When all four hold, update `TLA_SHA256` in one change in every model's `Makefile` that downloads the release jar, rerun each model, and record the new TLC version in each evidence record. A model that extracts `tla2tools.jar` from a fixed build of something else, such as a pinned VS Code extension, keeps its own pin, because that build's jar does not change. When any fails, or the old jar is gone so step 4 cannot run, stop: the gate is BLOCKED / NOT VERIFIED.

**Review.** Report a missing diagram, model or evidence record; a model whose steps or properties disagree with the diagram, the plan or the code; a model not rerun after the behavior changed; and an evidence record that claims more than the finite model shows.

**Limits.** A pass means the properties hold for the explored finite model under its assumptions, not that the code is correct. CI does not run these models, because TLC can run for a long time, so the gate holds only when the agent and the reviewer run `make -C docs/verification/<feature>`.
