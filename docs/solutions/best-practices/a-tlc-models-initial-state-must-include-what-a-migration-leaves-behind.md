---
title: A TLC Model's Initial State Must Include What a Migration Leaves Behind
date: 2026-11-23
category: docs/solutions/best-practices/
module: docs/verification
problem_type: best_practice
component: development_workflow
severity: high
applies_when:
  - "A PlusCal model under docs/verification/ gates a feature that ships with a data migration"
  - "A plan or evidence record uses a TLC result to drop a runtime check, as in 'the model shows X needs no separate check'"
  - "Rows written by an older release can be in states the new code never creates"
related_components:
  - assistant
tags:
  - pluscal
  - tla
  - tlc
  - model-checking
  - migration
  - initial-state
  - message-tree
---

# A TLC Model's Initial State Must Include What a Migration Leaves Behind

## Context

The Message Tree plan (`docs/plans/2026-11-23-0931-feat-message-tree-plan.md`) was gated by a PlusCal model in `docs/verification/message-tree/`, as the formal-modeling pack asks (`docs/packs/formal-modeling/stateful-design.md`). The model's evidence record drew a design decision from the `delete-outside` check:

> An outside message can only sit on the current path, because the floor stops every move before it. A branch holding one is therefore the current branch, and deleting it must first move to the branch point, which the floor refuses. The branch-delete transaction needs no separate outside-message check: `delete-outside` shows that the floor check in that move is what protects outside messages.

Plan KTD9 step 3 used that to leave the check out of branch delete. TLC was right about what it explored. But the model starts from an empty chat: `created = {}`, `outside = {}`, `floor = Root`, `head = Empty` (the `variables` block, `docs/verification/message-tree/MessageTree.tla:18-28`). Every state it reaches is built by the new code's own steps. Migration 108 starts from a different place: real chats whose old staged Undo marker could have a cron, External MCP or Slack message after it. Those rows broke `HeadAfterFloor` from the first state. The outside message sat on a side branch, and the "no separate check" reasoning did not hold for them. The deployment-verification and adversarial reviewers found this (P1 D1). TLC never could have, because no behavior in the model starts there.

## Guidance

A model checks only the behaviors that start from its `Init`. When the feature ships with a migration, the migrated rows are starting states too, so cover them in one of two ways:

1. **The migration maps every legacy state into a state the model can reach, and a test proves it.** This is what the fix did. Migration 108 clears the markers the old settle would have cancelled. `TestHistoryLinksMigration` (`internal/rocketclaw/backend/store_schema_test.go:613`) asserts the result for each legacy shape. The full lesson is in `docs/solutions/logic-errors/migrating-staged-undo-markers-must-settle-what-the-old-code-settled-later.md`.
2. **Or `Init` includes the legacy states**, for example a check whose initial state is a staged marker with an outside entry after it. TLC then shows whether the new code's invariants survive them.

Either way, write the assumption down. The evidence record's assumptions, or its "What the model leaves out" list, should say which pre-existing data the model assumes and what makes that assumption true. A conclusion of the form "TLC shows X needs no runtime check" should name the starting states it depends on.

## Why This Matters

A TLC pass is often used to justify removing a check, which is the point of modeling first. But "the invariant protects this" holds only from states the invariant already holds in. Data from before the release is exactly where it may not. Removing the check and leaving the migration unexamined lets the old rows reach code that assumes they can't exist. Here that meant an outside message that branch delete could remove, which the design promises never happens. Neither the pass nor the must-fail checks (`delete-outside` fails as expected) say anything about states outside `Init`.

## When to Apply

- A model gates a feature that reinterprets stored state: a column whose meaning changes, a pending state that becomes lasting, a table backfilled from older rows.
- A plan cites a model result to drop a guard, a re-check or a validation.
- Reviewing a `docs/verification/<feature>/` evidence record for a feature with a migration. Check that its assumptions mention the migrated data.

## Examples

Before: the claim rests on the model alone.

> The branch-delete transaction needs no separate outside-message check: `delete-outside` shows that the floor check in that move is what protects outside messages.

After: the claim names the starting states it depends on and what makes them hold.

> The branch-delete transaction needs no separate outside-message check: `delete-outside` shows that the floor check in that move is what protects outside messages. This holds for states the model starts from: an empty chat, or one migration 108 settled. The migration clears every staged marker with an outside message after it, and `TestHistoryLinksMigration` checks this.

## Related

- `docs/packs/formal-modeling/stateful-design.md`: the gate this model satisfied. Its Plan paragraph asks for the model's initial state but does not mention migrated data.
- `docs/solutions/logic-errors/migrating-staged-undo-markers-must-settle-what-the-old-code-settled-later.md`: the migration fix this lesson came from.
- `docs/verification/message-tree/README.md`: the evidence record and its "What the runs changed in the design" section.
