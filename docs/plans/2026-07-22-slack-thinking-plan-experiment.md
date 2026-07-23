# Slack Thinking Plan Experiment Plan

## Goal

Produce one pre-release `main` commit that groups Variant A activity task updates inside Slack's native Plan display.

## Constraints

- Change only thinking stream display mode and Plan title chunks.
- Preserve the existing answer implementation and exact answer requests.
- Preserve thinking-first and answer-second message order.
- Preserve the two-second debounce.
- Reuse Variant A's bounded, ordered, once-only activity task updates and sources.
- Preserve recipient-less behavior.
- Use plain Plan titles without underscore emphasis markers while retaining existing fallback task-card titles.
- Persist the goal starter's Slack recipient team/user and reuse it for automatic and restart goal continuations.
- Use no `markdown_text`.
- Do not release without live acceptance and an explicit final product decision.

## Steps

1. Add a failing test requiring `chat.startStream` to use `task_display_mode=plan` with plain `plan_update` title `Thinking...` or the plain goal-progress title, while retaining the existing answer request second.
2. Keep Variant A activity queue, splitting, IDs, sources, failure retention, and `task_update` append payloads unchanged.
3. Remove the Variant A overall status task; use `plan_update` for the running and completed Plan title.
4. Keep the existing in-flight flush synchronization so completion and cleanup cannot race an append.
5. Assert answer-placeholder posting and final answer requests are unchanged from the baseline.
6. Assert recipient-less turns still use `chat.update` with the same card.
7. Add `GoalState.SlackRecipientTeamID` and `GoalState.SlackRecipientUserID`, migrate `conversation_goals`, capture the kickoff recipient, and propagate it to automatic and restart continuations.
8. Run focused tests, full package tests, `gofmt`, `gopls`, `go test ./...`, `make lint`, and `make test`.
9. Review the Variant A-to-B diff, commit Variant B to `main`, and hand off the exact commit for testing-environment deployment.
