---
title: Dropping only the broken agent made missing-agent lookups fail open
date: 2026-11-25
category: docs/solutions/security-issues/
module: internal/rocketclaw/harness
problem_type: security_issue
component: assistant
symptoms:
  - "A non-sudo agent that names a helper agent could list and read that helper's sessions inside a sudo agent's chat once the sudo agent's file failed to load or was disabled"
  - "A helper session whose top-level agent was not loaded got no maxRecursion limit and could start helpers at any depth"
root_cause: logic_error
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/backend
tags:
  - agent-loading
  - fail-closed
  - sudo
  - delegation
  - max-recursion
  - zero-value
---

# Dropping only the broken agent made missing-agent lookups fail open

## Problem

The one-agent-record change (plan `docs/plans/2026-11-25-1231-refactor-one-agent-record-plan.md`, KTD4) made agent loading drop only the file that fails, with an `Agent X was not loaded` warning. Before, many agent-file errors kept every agent from loading. Two lookups in the loaded agent set read a missing agent's zero value as the permissive case. A missing top-level agent opened the sudo seal in `Agents.delegates` and removed the helper depth limit in `Bridge` turn setup.

## Symptoms

- A non-sudo caller whose `task` rules name helper B could list and read B's helper sessions inside a sudo agent's chat after that sudo agent's file failed to load, was renamed or deleted, or `main` was turned off.
- A helper session started under a top-level agent that later failed to load had no `maxRecursion` limit, so it could keep delegating at any depth.

## What Didn't Work

- **The TLA+ model.** `docs/verification/one-agent-record/` assumes every session's top-level agent is loaded, so its checks could not see the gap. Its README lists "sessions of agents that are not loaded" under what it leaves out. The model's starting states have the same blind spot described in `docs/solutions/best-practices/a-tlc-models-initial-state-must-include-what-a-migration-leaves-behind.md`, with "an agent that is not loaded" in place of migrated rows.
- **Judging the finding by "did this change cause it?".** Three code reviewers (correctness, adversarial, past learnings) reported the sudo gap on their own. The review's validator rejected it because the base release let every agent list and read every session, so the change narrowed access instead of opening it. That answers the wrong question. The plan promises that only sudo agents read a sudo agent's sessions (R19), and the new check did not keep that promise. It was fixed anyway, and the code review report kept it as a residual risk.
- **Considered, not applied: turning `MaxRecursion` into a plain `int`.** A maintainability reviewer suggested this, because "unset means unlimited" no longer exists for loaded agents. The narrow fix below closes the hole and keeps the pointer, so `nil` still reads as "no limit" wherever a budget is built without starting from zero.

## Solution

Both lookups now treat "not in the loaded set" as the most restrictive case.

The sudo check in `Agents.delegates` (`internal/rocketclaw/harness/delegation.go`). Before, the seal applied only when the owner was found:

```go
if top, ok := a.Items[owner]; ok && top.Permission.Sudo && owner != caller.Name && (op != DelegationRead || !caller.Permission.Sudo) {
	return false
}
if caller.Permission.Sudo {
	return true
}
_, loaded := a.Items[target]
return loaded && caller.Permission.evaluate("task", target).Action != permissionDeny
```

After, a non-sudo caller needs the owner to be loaded and not sudo. Sudo callers keep their earlier reach:

```go
top, ok := a.Items[owner]
if caller.Permission.Sudo {
	return !ok || !top.Permission.Sudo || owner == caller.Name || op == DelegationRead
}
_, loaded := a.Items[target]
return ok && !top.Permission.Sudo && loaded && caller.Permission.evaluate("task", target).Action != permissionDeny
```

The helper budget in the subagent branch of `Bridge` turn setup (`internal/rocketclaw/backend/bridge.go`, where `owner.agentSnapshot()` sets `rootAgent`). It started from `nil`, which `toolFactory.runTask` and the tool factory read as "no limit", and only a found root agent replaced it. It now starts from `new(0)`:

```go
rootAgent, agent.MaxRecursion = owner.agentSnapshot(), new(0)
if limit := agents.Items[rootAgent].MaxRecursion; limit != nil {
	agent.MaxRecursion = new(max(*limit-len(jobs), 0))
}
```

`LoadAgents` always sets `MaxRecursion` for a loaded agent (it defaults to 1 in `internal/rocketclaw/harness/agents.go`), so `nil` here now means only that the root agent is missing.

Tests that failed before the fix:

- `TestSessionToolsReachOnlyTargets` (`internal/rocketclaw/backend/session_tools_test.go`) refuses non-sudo reads of `web:main/history` (inside a sudo chat) and `web:retired/helper` (inside a chat whose agent is not loaded) with "a sudo agent's chats and agents that are not loaded are out of its reach", and checks that a sudo caller still reads them.
- `TestTaskHelperOfUnloadedTopLevelAgentCannotDelegate` (`internal/rocketclaw/backend/active_turn_test.go`) breaks the top-level agent's file while its turn runs and checks the helper is offered neither `task` nor `task_clone`.

A table of 234 delegation cases checked the fixed rule against the previous one. It found exactly 12 differences, and all 12 were this hole.

## Why This Works

`Agents.Items` is a map, so `a.Items[name]` on a missing agent returns a zero `Agent`. Its `Permission.Sudo` is `false`, its `MaxRecursion` is `nil`, and its `Space` is `""`. Each zero value is the permissive reading of its field: not sealed, no limit, not pinned to a space. When a load error stopped every agent, a missing agent came mostly from a rename, a deletion or a turned-off built-in, so code rarely met one. Once one bad edit to a file drops just that agent, any typo in a sudo agent's file is enough to reach the zero value. Checking `ok` and refusing on a miss makes every missing-agent case take the restrictive path, whatever the cause.

## Prevention

- When a change makes a partial set of loaded items normal (drop the broken one, skip the disabled one, filter by permission), grep every lookup into that set and ask what the missing key's zero value means. For agents: `rg -n '\.Items\[' internal/rocketclaw --glob '!*_test.go'`. A lookup that guards access or a limit must check `ok` and refuse on a miss. A lookup that only feeds display, such as a space name or a model label, can use the zero value.
- Watch for checks written as "deny when found and restricted" (`ok && top.Permission.Sudo`). That form allows by default. Write the allow condition instead (`ok && !top.Permission.Sudo`), so a missing entry is refused.
- Treat a `nil` pointer that means "unlimited" or "not set" as a fail-open default. Start a budget from the restrictive value and widen it only when a lookup succeeds.
- Also list the lookups whose missing-key branch deletes or skips data. In the same change, a routine whose agent is not loaded forgets its next due time, and a follow-up it scheduled is dropped when it comes due. The plan accepts this, and the README release note warns about it, but it is the same audit question.
- A model or evidence record for delegation or reach should say whether every agent is assumed loaded. If it is, the code needs a test with a missing owner, because the model cannot provide one.
- In review, treat this as a rule for permission and limit code: a lookup into a loaded set that guards access or a limit refuses on a missing key.

## Related Issues

- `docs/solutions/best-practices/a-tlc-models-initial-state-must-include-what-a-migration-leaves-behind.md`: the same blind spot in a model's starting states.
- `docs/solutions/best-practices/agent-file-format-changes-need-a-deployment-load-check.md`: the deploy-time check for agent-file format changes. Its Context still says one failing agent file stops RocketClaw from starting. After this change that file is dropped with a warning instead.
- `docs/verification/one-agent-record/README.md`: the delegation model, the differential harness, and the limits that leave unloaded agents out of the model.
- `CONCEPTS.md`, Delegation Target: the rule that sessions whose top-level agent is not loaded are no non-sudo agent's target.
