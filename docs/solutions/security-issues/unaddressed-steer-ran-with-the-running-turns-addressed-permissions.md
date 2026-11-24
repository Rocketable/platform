---
title: An unaddressed steer ran with the running turn's addressed permissions
date: 2026-11-24
category: docs/solutions/security-issues/
module: internal/rocketclaw/backend
problem_type: security_issue
component: assistant
symptoms:
  - "An Ambient Turn could call task continue with mode steer on a Task subagent that a mention turn had started, and its text ran inside that subagent's addressed turn"
  - "Calls the subagent made for the steered text ran under its allow rules without Permission Review and were not held to low risk"
  - "On every Ambient Turn, slack_no_reply itself was sent to Permission Review, so staying quiet cost a review call and could be denied"
root_cause: logic_error
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/harness
tags: [ambient-turn, unaddressed, steer, task-subagent, permission-review, privilege-latch, slack-no-reply, allow-rule]
---

# An unaddressed steer ran with the running turn's addressed permissions

## Problem

Slack ambient listening marks a turn started by an unmentioned message as unaddressed. An unaddressed turn may take only low-risk actions, and its Task subagents, shadows and background wakes are unaddressed too. Code review found a way around this: an unaddressed turn could steer text into a Task subagent turn that was already running from an addressed origin, and that text then ran with the addressed turn's permissions. The same review found that the new rule sending allowed calls to review on unaddressed turns also sent the stay-quiet tool, `slack_no_reply`, to review.

The fixes are in the commit "internal/rocketclaw: tighten Slack ambient turns after review" on the Slack ambient listening branch, unmerged as of this writing.

## Symptoms

- A mention turn starts a background Task subagent that has `bash: allow`. While it runs, an unmentioned reply in the same thread passes the screen and starts an Ambient Turn. That turn calls `task` with `continue` and `mode: steer` on the running subagent. Its prompt joins the subagent's running turn, and the subagent's bash calls for it run without review.
- The only check left was the review of the `task` call itself. That review judges whether sending a message is risky, not what the subagent will do with it.
- Every silent Ambient Turn paid for a reviewer call on `slack_no_reply`. If that review failed or denied the call, the agent had no clean way left to stay quiet: the guidance tells it that any text it writes is posted.

## What Didn't Work

- **Marking the write was not enough.** `Bridge.WriteSubagent` (`internal/rocketclaw/backend/bridge.go`) already put the `AmbientDelegated` mark on an unaddressed turn's write. The mark only matters when a turn starts. A steer does not start a turn, so the mark arrived too late to change anything.
- **The plan already said "an ambient turn never steers" (R10).** That rule was built for Slack intake, where ambient messages go into the Thread Queue like mentions. Nobody applied it to the other way an ambient turn sends input: an agent's write into a subagent.
- **Turning every matched allow rule into a review on unaddressed turns** (`looper.permissionDecision`, `internal/rocketclaw/harness/looper.go`) covered the gap where an agent's own allow rules skip review. But RocketClaw also grants its own platform tools, `slack_no_reply` among them, through an allow rule it sets when it loads agents (`agent.Permission.Set("rocketclaw", tool, harness.PermissionAllow)` in `readRocketCodeDefinitionsIn`, `internal/rocketclaw/backend/bridge.go`). That rule counts as matched, so the tool an Ambient Turn needs in order to obey its guidance went to review as well.

## Solution

**A turn takes input of one standing only.** The first fix turned an unaddressed turn's steer into a queued write inside `Bridge.WriteSubagent`. That covered one sender. The rule now sits where input joins a running turn, so it covers every sender in both directions:

- `Bridge.openInputWindow` records the running turn's standing, `activeUnaddressed`, from the ambient mark of the input that started it.
- `Bridge.joinsTurnLocked` admits a steer only while input is open and the steer's ambient mark gives the same standing. `Bridge.enqueue` (Web steers and queue promotion), `Bridge.admitAgentWrite` (agent writes, Task `continue`) and `Bridge.slipSteer` (steers sent during shutdown, also checked against a saved running turn) all call it. A steer that does not join runs as its own turn with its own standing.
- `claimBackgroundNotes` claims only Completion Notes whose job origin has the given standing. `Bridge.Drain` passes the running turn's standing. A wake takes the standing of the note `pickLaterWork` woke it for, and `claimWakeNotes` claims only notes of that standing; the rest stay for a later turn of theirs.

`WriteSubagent` now only marks an unaddressed turn's write; the shared rule queues it. `WriteSubagent` returns `false` for "steered", so the `task` call handles it as a queued continue.

**The stay-quiet tool skips the allow-to-review upgrade.** In `looper.permissionDecision`, the upgrade now runs only when the call is not a read and is not `slack_no_reply`:

```go
if l.unaddressed && !l.InPermissionReview && decision.Action == permissionAllow && decision.Matched && permission != "read" && (permission != "rocketclaw" || subject != NoReplyToolName) {
	decision.Action = permissionAuto
}
```

The exemption comes after the deny check, so an agent rule that denies `slack_no_reply` still wins. The same change keys the read exemption on the permission rather than the rule bucket (`permission != "read"` replaced `decision.Bucket != "read"`). A read that an agent's `edit` rules allow is evaluated in the `edit` bucket, so the bucket test sent those reads to review. `rocketclaw_attach_files_to_response` is still reviewed on unaddressed turns, and the test asserts that.

Tests: `TestTaskRunsSubagentAsSession` (`internal/rocketclaw/backend/active_turn_test.go`) checks that an unaddressed steer does not join the running turn and later runs as its own turn with the unaddressed origin. `TestBridgeSteersJoinOpenTurn` and `TestStoppedSteerJoinsOnlyOpenTurns` (`bridge_test.go`) check both directions at admission, and `TestSlackAmbientTurnFraming` checks that a wake with an ambient note and a mention's note runs two turns, the mention's one addressed and not deferred. `TestUnaddressedTurnReviewsCallsAnAllowRuleRuns` (`internal/rocketclaw/harness/permission_review_test.go`) checks the read exemption under `edit: allow` and that `slack_no_reply` alone skips review.

## Why This Works

Whether a turn is unaddressed is decided once, when the turn starts. `Bridge.runTurn` computes `unaddressed` from the ambient mark of the input that started the turn (a subagent's origin takes that mark too) and passes it to the harness as `Unaddressed`. The looper keeps it for the whole turn. Steers come in later through `Bridge.Drain`, which the looper calls between steps (`looper.appendSteers`), and nothing there looks at the steer's own origin. So any input that joins a running turn takes on that turn's standing. A running turn's input path is a privilege boundary: input from a weaker origin must not cross it.

Queuing works because a queued input starts its own turn, and a new turn's standing comes from its own input. The reverse direction matters too: addressed input that joined an unaddressed turn was held to its limits and could be silenced, so it also waits for a turn of its own. A `stop` from an unaddressed turn was left unchanged: it ends the running subagent turn, and its prompt runs as a new turn with the unaddressed mark.

For the stay-quiet tool: an origin limit that adds review must not block the action the limited turn is told to take in order to comply. Otherwise the cheapest and most common Ambient Turn outcome, saying nothing, depends on a reviewer call and fails badly when that call is denied.

## Prevention

- When a permission or standing is fixed at turn start, look at every way input can reach an already-running turn: steers from Web, agent session writes, Task `continue`, queue promotion. Each one either carries the same standing or must become a turn of its own.
- A person promoting a queued item from Web (`Runtime.PromoteQueueItem`) submits it as a steer through `Bridge.enqueue`, so the same rule applies: a promoted ambient item does not join a running addressed turn; it runs as its own unaddressed turn. Promotion does not address an item; only a Web edit does.
- Enforce such a rule where input joins a running turn, not in each sender, so a new input source cannot forget it.
- When adding an origin-based rule that sends allowed calls to review, list the tools the limited turn needs in order to follow its own guidance, and exempt only those, after the deny check. Test them through `permissionDecision`, not by calling the tool directly. The first test called `noReplyTool(...).Call` directly and never went through the review gate.

## Related Issues

- [A Review Request That Claimed Its Own Trust Read as Prompt Injection](../logic-errors/review-request-claiming-its-own-trust-reads-as-injection.md): how Permission Review reads the trusted turn origin line that carries the `unaddressed` token.
- `CONCEPTS.md`: Ambient Turn, Steer, Thread Queue, Permission Review, Subagent Session.
- `docs/plans/2026-11-24-1520-feat-slack-ambient-listening-plan.md`: R10 (an ambient turn never steers) and the unaddressed rules.
