---
title: Send teammate tags when a session prompt is sent
type: feat
status: active
date: 2026-11-24
---

# Send teammate tags when a session prompt is sent

## Problem Frame

Typing someone's name in a session doesn't tell them their input is needed. Both composers need a way to select a person and notify them without waiting for the agent to finish.

## Product Contract

- R1. Both composers offer people from the existing directory. Selecting a person binds the tag to their user ID, not their display name. The composers reject more than five distinct people before sending. The native endpoint and the OpenCode notification endpoint also enforce the cap; invalid OpenCode tags do not change the upstream prompt's result.
- R2. A human send attempts a direct notification containing only the session URL, with no preview, for each selected person. Web identity and session visibility never gate the direct notification. A delivery failure is shown to the sender.
- R3. If there is a case-insensitive match to a Web login that can see the session, use the existing mark-as-unread action, which leaves parked tabs alone. The link never grants access.
- R4. Keep delivery on the native Web send path and in the OpenCode relay. Held drafts, command-only actions, agent input, history replay, and synchronization paths do not send these notifications. Do not add a delivery worker, database state, or queue replay guarantee.

## Planning Contract

- Extend `SlackNames` to search the existing directory for people, not groups. Use `conversations.open` then `chat.postMessage` so the link goes to the app's direct conversation, not a channel or a message from the system account. The configured Web origin supplies the link; the sender never supplies a URL.
- Match the directory email against known `web_tab_states` logins and use the stored spelling. Skip the tab mark for no match or an ambiguous match. Check session visibility for the recipient, not the sender; a space-limited login cannot open an OpenCode session. Reuse the attention record for a quiet mark without changing a park. No lookup or mark failure can prevent direct delivery.
- Native: validate the selection before mutating state. Attempt notifications after queue or goal submission succeeds, after a paused message commits, or after a regular turn enters the bridge but before waiting on its completion. Keep the admission signal scoped to the Web request. Return a distinct warning field so a delivery failure does not restore the composer or retry an accepted prompt.
- OpenCode: existing authentication identifies the sender. Read and remove selected recipient metadata before forwarding `/prompt`. On a successful upstream admission, deliver through an internal RPC that has no browser route. Return a warning header read by the host UI without altering upstream status or body.

## Scope Limits

A held draft is not a send. A transport retry after a lost response may send another notification. The draft does not add an acknowledgment store, retry worker, or proof of crash recovery, and does not change how either queue admits, orders, or advances prompts.

## Implementation Units

1. Add the directory picker and transport fields. Unit-check that removing a tag removes its recipient, and that unselected plain text and repeated targets do not create extra deliveries.
2. Add synchronous delivery to the native send and OpenCode relay. Use a fake HTTP service to assert the direct recipient, exact URL, disabled previews, access-independent delivery, and visible errors. Check that a slow notification does not run before admission, and that the relay strips metadata and does not deliver on a rejected prompt.
3. Rebuild both Web bundles and update the operator docs for the required app scope. Check unchanged command handling, queue order, and prompt text separately from delivery.

## Verification Contract

Run focused Go and browser checks, then the repository Go, lint, coverage, Web, and source budget checks. Use synthetic recipients only. No real service sends are part of draft verification.

## Definition of Done

Both composers send the same capped recipient selection; the native path and the relay attempt delivery without waiting for a turn to finish. Access affects only the tab mark. Keep the change as a draft until approved.
