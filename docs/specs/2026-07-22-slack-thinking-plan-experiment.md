# Slack Thinking Plan Experiment

## Goal

Test whether Slack's native Plan display gives one coherent thinking container with many activity tasks and stable interaction state.

## Live Variant A Result

Commit `909e7edca8e0ed536720eead99a00492f3ceec09` produced one thinking message containing one task card per activity. That layout failed acceptance because the required UX is one thinking card containing multiple activity entries.

## Variant B

Variant B keeps Variant A's bounded activity `task_update` chunks but changes their native layout:

1. `chat.startStream` creates one thinking message with `task_display_mode=plan` and a plain-text `plan_update` title of `Thinking...`, `Pursuing Goal...`, or `Pursuing Goal (n/m)...`.
2. The existing zero-width answer placeholder is posted second without modification.
3. The existing two-second debounce remains.
4. Each thinking flush appends only newly received bounded `task_update` activities; Slack groups those tasks inside one `plan` Block.
5. Existing answer delivery remains unchanged.
6. `chat.stopStream` sends `plan_update` title `Complete` after answer delivery.

Goal kickoff stores the initiating Slack recipient team and user IDs in goal state. Automatic and restart goal continuations reuse that recipient so every goal turn uses the same Plan layout. Non-streaming fallback task cards keep their existing underscore-form titles.

Recipient-less turns retain the existing non-streaming card path.

## Live Acceptance

Variant B passes only if Slack keeps exactly one thinking message and one Plan block, groups all activity tasks inside that Plan, preserves chronological literal activities and links, preserves the user's Plan interaction state across updates, keeps the answer separate and unchanged, and ends with Plan title `Complete`.

If Slack renders top-level task cards, multiple Plan blocks, loses activities, or resets interaction state, Variant B fails and the baseline non-streaming card must be restored before release.
