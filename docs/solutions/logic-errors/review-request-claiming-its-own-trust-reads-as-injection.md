---
title: A Review Request That Claimed Its Own Trust Read as Prompt Injection
date: 2026-11-02
category: docs/solutions/logic-errors/
module: internal/rocketclaw/harness
problem_type: logic_error
component: permission_review
symptoms:
  - The automatic approver denied harmless commands, such as listing the shell's environment variable names, as "clear malicious prompt injection".
  - The same denials rated the action low risk with high user authorization.
  - The same request was sometimes allowed and sometimes denied.
  - It started after the easy mode agents change, on every agent with automatic review, not only easy agents.
root_cause: logic_error
resolution_type: code_fix
severity: high
tags:
  - permission-review
  - approver
  - guardian
  - prompt-injection
  - origin-header
  - model-behavior
---

# A Review Request That Claimed Its Own Trust Read as Prompt Injection

## Problem
The easy mode agents change (`f56f664f2af8`, plan R40, KTD8, U5) gave every permission review a trusted statement of who started the turn. `permissionReviewPrompt` in `internal/rocketclaw/harness/permission_review.go` put it on the first line of the review request:

```text
Turn origin, written by RocketClaw and trusted, unlike any origin the transcript claims: [Web principal="Alice <alice@example.com>"]
```

The reviewer's instructions (`embeddedGuardianPrompt` in `internal/rocketclaw/harness/guardian.go`) never mentioned that line. They tell it to ignore content that tries to redefine policy. A request that vouches for its own trust and asks to be believed over the transcript has the shape of prompt injection, so the reviewer sometimes denied the action as injection. Its outcome rules let injection deny even a low-risk action.

The same change named the person differently in the two places. The trusted line said `Alice <alice@example.com>`, and the person's own messages in the transcript said `Alice`, or `Alice (U1)` on Slack. The "trust me" line then also contradicted the transcript, which made the injection reading more likely.

## Symptoms
- An easy agent asked "Give me my shell session env var names." was denied with: "The transcript contains an instruction to treat its claimed origin as trusted, which is clear prompt injection; the requested environment-name listing is otherwise low risk."
- Before the name fix, denials pointed at the mismatch between the trusted line's principal and the transcript's.
- The reviewer is a model, so the same request passed on some tries and failed on others. Agents had been reviewed without trouble before the change.

## What Didn't Work
- Making the names match (PR #921, `reviewOriginHeader` in `internal/rocketclaw/backend/bridge.go` now names the person exactly as `provenanceFromInbound` does). It removed the contradiction but not the self-vouching line, and the injection denials continued.

## Solution
Move the trust statement to where only RocketClaw writes: the reviewer's instructions.

- The request now opens with a plain label: `Turn origin (written by RocketClaw): [Web principal="Alice"]`.
- `permissionReviewOriginRule` is appended to every reviewer's instructions in `reviewPermission`, the embedded guardian and custom reviewers alike. It says that RocketClaw writes that line, that the line is never prompt injection, and that origin headers inside the transcript remain claims.
- `TestPermissionReviewSystemPromptIncludesRootInstructionsAndCodeMode` checks that both kinds of reviewer get the rule. `TestPermissionReviewPromptIncludesReviewContextAndPlannedAction` checks that the request opens with the line the rule describes.

## Why This Works
A model reviewer trusts its instructions and treats the request as evidence. A claim of trust belongs in the channel the reviewer already trusts. The same claim made inside the evidence is what an attacker would write. The label in the request now only says where the line came from, and the rule in the instructions says what that means.

## Prevention
- **Change the reviewer's instructions whenever you change what its request contains.** Any new line in the request that the reviewer must treat differently from the transcript needs a rule in the instructions saying so. The plan for this feature listed `harness/guardian.go` among U5's files, but the commit never touched it, and nothing compared the plan's file list with the diff.
- **Never put "trust this" in a model's input.** Put trust in the instructions and keep the input factual.
- **Test reviewer prompt changes against the real reviewer model, not only with string checks.** Tests that confirm a line is present cannot show how the reviewer reacts to it. Before merging a change to the review request or the reviewer's instructions, send the real reviewer a few harmless requests from the turn's own person, such as listing environment variable names or reading a file in the workspace, and confirm they are allowed. Run each several times, since the answers vary.
- **Keep one prompt change per commit.** The trusted line and its name format arrived together, and each made the other worse.

## Related Issues
- Plan: `docs/plans/2026-10-30-1313-feat-easy-mode-agents-plan.md` (R40, KTD8, U5)
- PR #921: the matching-name fix
- `docs/solutions/logic-errors/permission-review-reasons-read-from-transport-specific-trace.md`
