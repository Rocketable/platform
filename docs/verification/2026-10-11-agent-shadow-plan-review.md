---
title: Agent shadow plan review
date: 2026-10-11
artifact: docs/plans/2026-10-11-1632-feat-agent-shadow-comparison-plan.md
artifact_sha256: 472cb6714e578e12137af3759b42a88fe65b57656055bff1d06f684448db6a8e
status: complete
---

# Agent shadow plan review

## Outcome

The planning pipeline is complete. Planning confidence is high after targeted deepening and non-interactive document review. Three review corrections landed; no proposed fixes, unresolved decisions, or FYI observations remain. This is a planning assessment, not evidence that the feature works.

The reviewed artifact is `docs/plans/2026-10-11-1632-feat-agent-shadow-comparison-plan.md`, identified by the SHA-256 above. Its file-path membership, session-per-shadow mechanism, identical consumed inputs, and tabbed vertical comparison remain intact. All three user-directed settlement annotations and all stable requirement, decision, example, and unit IDs survived.

## Confidence check

Depth: **Deep**. Risks: durable migrations, authorization, external tool effects, concurrent input consumption, recovery, producer routing, and live Web comparison.

The risk-weighted checklist selected these sections for strengthening:

| Section | Checklist triggers | Risk bonus | Critical-section bonus | Initial score |
|---|---:|---:|---:|---:|
| System-Wide Impact | 2: failure propagation and replay lifecycle underexplored | 1 | 1 | 4 |
| Implementation Units | 1: a named existing steer test did not exist | 1 | 1 | 3 |
| Key Technical Decisions | 0; selected for the material persistence/concurrency risk | 1 | 1 | 2 |

Deepening made canonical input/context/environment replay explicit, put note claims and input capture in one transaction, specified durable closure before journal cleanup/delivery, handled terminal paths with no prepared input, and made final drains replay-aware. It also fixed the existing steer-test target and added the relevant storage/mutation targets. The second pass found no remaining checklist failure that prevents implementation. Risk bonuses remain; they do not establish implementation correctness.

Repository evidence came from the actual activation, drain, journal, finish, delivery, workflow, History, authorization, and Web rendering paths. Native dispatch discovery exposed no subagent primitive, so this research ran inline using the architecture, data-integrity, and repository-research prompts. No model elevation preference was present.

## Document review contract and coverage

Invocation: `ce-doc-review mode:non-interactive` on the artifact above. Classification: `unified-plan`; provenance: `product_contract_source:ce-plan-bootstrap`.

Edit authority: apply corrections to the draft's implementation and verification sections needed to satisfy its established Product Contract. Preserve that contract, repository constraints, and user-directed or user-reserved decisions. No implementation authority was granted.

| Reviewer | Reason selected | Execution | Original findings |
|---|---|---|---:|
| coherence | Always-on; cross-section contracts and stable IDs | Inline, complete | 0 |
| feasibility | Always-on; execution, persistence, and recovery paths | Inline, complete | 3 |
| product-lens | Assistant-selected operating assumptions and long-lived comparison sessions | Inline, complete | 0 |
| design-lens | Tabs, aligned rows, responsive layout, and accessibility | Inline, complete | 0 |
| security-lens | Child authorization, tool authority, and connector isolation | Inline, complete | 0 |
| scope-guardian | Fifteen requirements, eight units, and deferred features | Inline, complete | 0 |
| adversarial | New durable fan-out pattern, migrations, and bootstrap provenance | Inline, complete | 0 |

All lenses used the supplied persona prompts and the whole artifact when cross-section evidence was needed. These were sequential passes in one context, not independent agents. No agreement-based confidence promotion was used.

The automatic cross-model pass did not run: the host is OpenCode, but no serving-family attestation in the review protocol's accepted vocabulary was established. The declared GPT model/provider was not relabeled as a CLI family. No external peer jobs were launched. Compound Pack resolution returned zero roots, warnings, and errors.

## Original reviewer evidence

These are the completed inline reviewer responses before resolution. Empty arrays mean that the lens found no concern meeting its consequence and confidence standards, not that it proved the implementation correct.

```json
[
  {
    "reviewer": "coherence",
    "findings": [],
    "residual_risks": [],
    "deferred_questions": []
  },
  {
    "reviewer": "feasibility",
    "findings": [
      {
        "title": "Compaction erases input acknowledgements needed for replay",
        "severity": "P1",
        "section": "KTD5 — Final-input wait and replay",
        "why_it_matters": "A resumed shadow can consume an old steer twice after context-overflow compaction removes that steer's replay item. The manifest still contains the input, but scanning surviving replay IDs no longer proves it was applied. Retaining acknowledgements outside compactable content preserves exact consumed-input delivery without a second independently advanced cursor.",
        "finding_type": "error",
        "autofix_class": "gated_auto",
        "suggested_fix": "Keep applied input identities in the existing turn checkpoint, save them with the applying step, preserve them through both compaction paths, and add a compaction/restart/second-steer scenario to U3 — Consumed-input mirroring.",
        "confidence": 100,
        "evidence": [
          "Reuse replay input-ID deduplication; do not add a separately advanced in-memory cursor that can outrun journaling."
        ]
      },
      {
        "title": "Compiled workflow snapshot omits its executable program",
        "severity": "P1",
        "section": "KTD6 — Workflow and producer ownership",
        "why_it_matters": "A lagging or restarted child cannot recover the original executable workflow by JSON-saving its compiled definition. The executable program is private and omitted from JSON. Saving the validated source and recompiling it uses the existing compiler while preserving the required invocation across reload.",
        "finding_type": "error",
        "autofix_class": "gated_auto",
        "suggested_fix": "Capture validated workflow source and its path/name with the invocation arguments; recompile the saved source on recovery and add the source-preserving compiler path and reload/restart verification to U4 — Independent execution.",
        "confidence": 100,
        "evidence": [
          "Persist the prepared workflow definition and invocation name/arguments with KTD4's canonical capture, before either workflow runs; lag/reload cannot give a child a different definition for that invocation."
        ]
      },
      {
        "title": "Older binary rollback conflicts with schema version enforcement",
        "severity": "P2",
        "section": "System-Wide Impact and Risks — Recovery guidance",
        "why_it_matters": "An operator following the rollback guidance cannot start the older binary after this migration, even with all child work settled and additive tables retained. Startup rejects an unknown migration ledger entry. A forward repair is compatible with the project's existing schema policy and avoids deleting durable comparison data to force a downgrade.",
        "finding_type": "error",
        "autofix_class": "gated_auto",
        "suggested_fix": "Replace binary-swap rollback guidance with a forward repair on the upgraded schema, optionally removing shadow definitions and settling admitted work to pause execution; explicitly forbid deleting migration records or comparison data to force old-binary startup.",
        "confidence": 100,
        "evidence": [
          "Rollback must settle pending shadow work before reverting to an older binary that cannot service it; retain additive tables rather than deleting comparison data as a shortcut."
        ]
      }
    ],
    "residual_risks": [],
    "deferred_questions": []
  },
  {
    "reviewer": "product-lens",
    "findings": [],
    "residual_risks": [],
    "deferred_questions": []
  },
  {
    "reviewer": "design-lens",
    "findings": [],
    "residual_risks": [],
    "deferred_questions": []
  },
  {
    "reviewer": "security-lens",
    "findings": [],
    "residual_risks": [],
    "deferred_questions": []
  },
  {
    "reviewer": "scope-guardian",
    "findings": [],
    "residual_risks": [],
    "deferred_questions": []
  },
  {
    "reviewer": "adversarial",
    "findings": [],
    "residual_risks": [],
    "deferred_questions": []
  }
]
```

## Resolved review

| Claim | Disposition | Project evidence and reason | Actual edit and verification |
|---|---|---|---|
| Input acknowledgements lost on compaction | Applied | `internal/rocketcode/looper.go:856–882` replaces replay content during overflow recovery; `appendSteers` scans that content. The previous plan specified replay-aware waiting but did not preserve the information that decision needs. | KTD5 now owns checkpointed input identities outside compactable content. U3 names the checkpoint file and verifies compaction, restart, and a later steer without reinjection. |
| Compiled workflow cannot be persisted as proposed | Applied | `internal/rocketclaw/workflow/definition.go:30–36` holds a private Starlark program; its existing compiler constructs that program from source. The prior snapshot instruction could not reconstruct executable work after restart. | KTD6 now saves validated source/path/name/arguments and recompiles through the existing compiler. U4 names the definition/compiler tests and verifies changed/deleted source after reload or restart. Unshadowed workflows retain their current path. |
| Binary rollback unavailable after migration | Applied | `internal/rocketclaw/backend/store_schema.go:74–75` rejects unknown migration records; `README.md:153` documents this policy. Retaining tables alone does not make an older binary start. | Recovery guidance now calls for a forward repair on the upgraded schema and forbids deleting ledger/comparison data to force a downgrade. |

All three corrections are within the supplied implementation/verification edit authority and preserve the Product Contract. No peer-only finding was applied, no settlement annotation was changed, and neither session-per-shadow nor the aligned comparison mechanism was replaced.

Considered claims dismissed as already addressed: real duplicate effects are an explicit assumption with permission tests and operator warnings; child write isolation already includes backend mutations, not only UI controls; producer sync already projects source-aware correlations rather than remirroring work; CSS shared rows already resolve unequal-height alignment without a measurement subsystem. These do not return as FYIs or questions.

The final file audit also grounded History metadata in `internal/rocketclaw/backend/history.go`'s existing repeatable-read snapshot and kept documentation in the existing root/Web READMEs rather than inventing component README surfaces. These are drafting-grounding changes, not additional unresolved review findings.

## Structured completion state

```yaml
status: complete
mode: non-interactive
document_review_started: true
document_review_completed: true
fixes_applied: 3
proposed_fixes_count: 0
decisions_count: 0
fyi_count: 0
remaining_p0_count: 0
remaining_p1_count: 0
independent_review: false
cross_model_review: not_run_host_family_unattested
```

Document review complete (non-interactive mode).

Applied 3 fixes:
- Final-input coordination: input acknowledgements now survive compaction and restart (feasibility).
- Workflow execution: recovery recompiles captured validated source, not a JSON copy of a compiled program (feasibility).
- Recovery guidance: forward repair replaces unsupported older-binary rollback (feasibility).

Review complete

## Planning-only boundary and remaining execution checks

Only the plan and this review record were authored. No production code, migrations, protocol output, tests, README, or glossary was changed. No tests, lint/build targets, metric targets, commits, pushes, or deployment ran.

Static artifact checks confirmed unique/resolved IDs, all three protected settlement annotations, six design diagrams, and file targets that either exist or are explicitly planned additions. No cmux environment markers were present. Implementation still must run the plan's full Verification Contract, measure source-budget headroom, and establish real-store/provider/browser behavior; none of those results is claimed here.

README impact was considered. No README update is needed for this planning-only change. U8 requires updates to the root and Web READMEs when the feature is implemented, including the real-tool-effects warning.
