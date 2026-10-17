---
title: Permission Review Reasons Were Read From a Transport-Specific Trace
date: 2026-10-17
category: docs/solutions/logic-errors/
module: internal/rocketcode
problem_type: logic_error
component: execute
symptoms:
  - Web chats never showed the automatic approver's reasons on deployments that reach models over HTTPS.
  - The same chats showed reasons when the model connection used the Responses WebSocket transport.
  - Every saved review entry held a valid allow or deny answer, yet the reason reader returned an empty string.
root_cause: logic_error
resolution_type: code_fix
severity: medium
tags:
  - permission-review
  - approver
  - output-trace
  - replay-input
  - transport
  - history
---

# Permission Review Reasons Were Read From a Transport-Specific Trace

## Problem
The Web showed no approver reason for any permission review on wallace, which reaches models over HTTPS. The old reader (`PermissionReviewRationale`) only accepted a reason backed by a `rocketcode_public_progress` text record in the review entry's `OutputTrace`. Only the Responses WebSocket transport writes those records (`responseObservations` in `internal/rocketcode/responses_websocket.go`), so HTTP reviews never had one.

## Symptoms
- All 110 saved reviews in wallace chat `AHWNE3GO6MMDBJ3V7OGHZDRH2S` had a final answer such as `{"outcome":"allow","rationale":"..."}` in `ReplayInput`, and none had a progress record.
- Tests passed because they built reviews with the WebSocket trace shape.

## What Didn't Work
- Reading the reason from progress records. Their presence depends on how the model was reached, not on whether a review happened.

## Solution
Read the decision the way `reviewPermission` makes it. `(*SessionEntry).PermissionReviewDecision` in `internal/rocketcode/permission_review.go` scans `ReplayInput` from the end. It takes the last assistant message outside the commentary phase whose content decodes as an allow or deny decision. It never runs the SDK replay decode.

Reading the reviewed action for older chats has a second trap. The review prompt's first message can be hundreds of kilobytes of transcript, followed by the planned action that `permissionReviewPrompt` writes between `Planned action JSON:\n` and `\n>>> APPROVAL REQUEST END`. `(PermissionReview).CodeModeCall` finds the last JSON-escaped `Planned action JSON:\n` in the raw item with `bytes.LastIndex`. It decodes only the tail from there, so History does not pay for decoding the whole transcript. A transcript line that tries to forge the delimiter is escaped once more by JSON, so it never matches the raw search.

## Why This Works
`ReplayInput` is saved the same way for every transport, and it holds the exact message the review decided from. The trace only holds what a given transport chose to stream.

## Prevention
- Build reader fixtures for saved entries from the HTTP shape (replay only, no trace) as well as the WebSocket shape. `TestPermissionReviewDecisionUsesFinalAnswer` covers both.
- Before relying on an `OutputTrace` record type, check which transports write it.

## Related Issues
- Plan: `docs/plans/2026-10-17-0938-feat-code-mode-block-visibility-plan.md` (U1, KTD1)
- `docs/solutions/runtime-errors/execute-uncapped-results-exceeded-model-context.md`
