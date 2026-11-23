# Shared-session teammate mentions

**Implementation draft.** Delivery, identity linking, and access changes still need verification before merging.

## What people can do

In a Shared session, type `@`, select a teammate by their existing directory name, and send. The message shows the mention. The teammate gets a direct message containing the session link and an indication on the session's tab. Both native and OpenCode sessions work this way. OpenCode keeps its existing file and agent suggestions. Agent messages do not notify people.

The picker offers directory names only in a Shared session. A mention does not grant access. A recipient must have signed in to the Web before, have access to the session, and share a space that contains it. Queued or stashed text does not notify at admission. Editing or cancelling a pending message does not send a notification for the old selection.

Use the existing tab attention surface alongside the direct message. The direct message contains no prompt text, session title, or link preview. An indication clears when read or cleared and does not unpark a tab. No new queue protocol, guaranteed delivery after restart, or exactly-once delivery across queue rewrites.

## Implementation

1. **Resolve teammates with the existing directory.** Reuse its people search and display labels, not its groups. Resolve a selected user by directory ID, not by display name. Use only a confirmed directory email that exactly matches a known Web login to address the tab mark. Do not present ownership of an arbitrary directory profile as proof of a Web login. Use a direct conversation with one recipient and disable previews for its link.
2. **Carry explicit selections in both composers.** Add a people result to each `@` picker and carry the selected ID alongside the message. The transcript shows the name; the prompt uses the existing `<@ID>` spelling for selected mentions. Editing a native queue item clears its old selections. Typed, pasted, quoted, or replayed text alone is not a selection. The server checks selections on the authenticated send path. An unavailable recipient does not prevent the ordinary message from sending. Keep scheduling, attachments, and routing unchanged.
3. **Mark targeted attention after visibility.** On the native path, use the committed paused entry or the consumed-input journal, not queue admission. On the OpenCode path, sign the selection at the authenticated relay, binding it to the authenticated sender, server, session and exact message ID. Check the signature in the projected user-message history the existing sync reads at turn end. A copied signature does not authorize a different message or session. No durable receipt store or recovery worker. The draft can skip notifications on restart or when the queue replaces an ID; the OpenCode path waits for the turn to end.
4. **Keep access checks on both sides of delivery.** Check both the current conversation permissions and that the sender and recipient have a Shared space in common for that session. These checks are distinct; some accounts cannot open OpenCode sessions at all. Apply the recipient's checks to every surface that could expose a mark, including counts. Check the write and read paths against unshare, space deletion, and configuration reload. A fresh read alone does not prove the write and access check are atomic. An external message cannot be rolled back when sharing changes.

## Verification before merging

The [bounded delivery model](../verification/shared-session-mentions/README.md) covers a native and an OpenCode message, visibility, cancellation, and the indication. It treats identity, access, and message ownership as inputs, not as properties of the real implementation.

The current model does not cover direct delivery or account linking. Verify that configured identity providers agree on ownership of matching logins, that the exact visible message is distinguishable from a queued item or a copy in a fork, and that neither a share change nor a concurrent read exposes a mark for a session the recipient cannot open. The model's abstract authorization step is not proof that the current access checks and attention write share a safe transaction boundary. Extend the model to distinguish selecting, checking access, marking, and sending the link; re-run its positive and fault cases before declaring those transitions verified.

Exercise both composers with separate authenticated sender and recipient accounts. Confirm selection by keyboard, a visible tag, a targeted mark, no mark for an unauthorized recipient, no mark before visibility or after cancellation, and unchanged ordinary sends. Exercise paused and steered native input and queued OpenCode input; verify OpenCode file and agent suggestions still work. Rebuild both bundles before the browser tests and run the repository's Go and Web checks. Document the feature when it works.
