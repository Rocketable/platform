---
title: Faster OpenCode tab switching on slow links
date: 2026-11-02
status: accepted, step 1 only
---

# Faster OpenCode tab switching on slow links

## The problem

Opening an OpenCode tab builds the embedded OpenCode view from scratch, which downloads everything it shows.
Measured on Hally (2026-11-02, one OpenCode session, page assets already cached), each build costs:

| | Requests | Bytes |
|---|---|---|
| The session itself (messages, permissions, children…) | 15 | ~72 KB |
| Lists for the session's project folder (skills 154 KB, models, agents, commands, file search) | 24 | ~201 KB |
| The same for the whole server (projects, providers…) | 13 | ~22 KB |
| **Total per switch** | **~54** | **~305 KB**, plus a new live event stream |

On a relayed link moving ~43 KB/s that is about 7 seconds per switch, during which every other request waits
behind it on the shared connection.

## Why OpenCode on its own feels fast

OpenCode's server sends no caching headers on any API answer (checked: skills, models, projects, a session, its
messages), so the browser never reuses its answers. OpenCode avoids repeat downloads by keeping data in memory,
inside its one long-lived app:

- **Each folder's lists stay loaded.** Skills, models, agents, commands, integrations, config, providers and the
  rest are kept per folder (`oc/packages/client/src/solid/data.ts:145-149`), each "loaded once per invalidation"
  (`data.ts:1474-1486`). Only a change event from the server (such as `skill.updated`) or a reconnect reloads one
  (`data.ts:1355-1370`, `2085-2112`). Nothing evicts a folder.
- **Recent transcripts stay loaded**: those of open tabs and the 3 most recently viewed sessions
  (`oc/packages/app/src/runtime/server/runtime.tsx:244-262`).

RocketClaw loses all of it, because it disposes the view on every tab switch (`RemoteSession` in
`web/src/ui.tsx` is keyed by tab, `key={route.remote}`).

## Proposal, step 1: one view per server, moved between sessions

Keep exactly one OpenCode view on the page, as today. When you switch from one OpenCode tab to another tab **of
the same server**, move the existing view to the other session instead of rebuilding it, so OpenCode's memory
carries over. In every other case (switching to a RocketClaw tab, to another server, or closing the tab) the view
is disposed exactly as today.

This is how OpenCode itself works: one app that moves between sessions. RocketClaw already does it inside the
view for child sessions (`oc/host/navigation.tsx`, tested by "navigating into a child session stays in the view and
reports the child" in `oc/host/mount.test.tsx`). Step 1 reuses that path for tab switches.

### Why it's safe from cross-session contamination

- **Never two views at once, nothing hidden.** Every risk found in the keep-alive review (below) needs either a
  second view or a hidden one.
- **It uses OpenCode the way it was built.** The composer's draft and attachments are looked up and cached per
  server, folder and session from the view's address (`oc/packages/app/src/composer/persistence.tsx:55-110`); each
  send carries the session it was typed in (`session/composer/adapter.ts`); folder data is kept per folder.
- **No copied OpenCode file changes.** Only RocketClaw's Web UI and RocketClaw's glue (`oc/host/`).

### What changes

1. `web/src/ui.tsx`: key the OpenCode view by server instead of by tab; a switch between OpenCode tabs of one server
   calls the view's new `show(sessionID)` instead of remounting. RocketClaw's "Loading…" state, "Copy server link"
   and the OpenCode commands entry follow the shown session.
2. `oc/host/api.ts`, `oc/host/mount.tsx`: add `show(sessionID)`. It closes any open dialog, moves the view's address
   to that session, and clears the view's Back history so Back can never return to the previous tab's session (the
   history object is RocketClaw's own, created in `mount.tsx`).
3. `oc/host/session-tab.tsx`: follow the shown session, and drop entries for sessions no longer open in RocketClaw,
   so OpenCode's "keep" list (open tabs plus 3 recent) does not grow with every session visited.

### Expected gain (to be measured)

| Switching between OpenCode tabs of one server, one folder per session | Today | Step 1 |
|---|---|---|
| First visit to a session in this page | ~305 KB, ~7 s at 43 KB/s | ~270 KB (no new event stream, no server-wide lists) |
| Back to a session already visited | ~305 KB, ~7 s | folder lists from memory; transcript from memory if recent, else ~72 KB |

Not yet verified at runtime: whether moving back to a session revalidates anything OpenCode keeps in memory. The
probe below will show it.

### Risks of step 1

| Risk | Handling | Proved by |
|---|---|---|
| Back returns to the previous tab's session | `show()` clears the Back history | Test: switch A→B, press Back, still B |
| A dialog, palette or menu opened for A stays open on B and acts on A | `show()` closes open dialogs first | Test: open the palette in A, switch to B, palette closed |
| A draft typed in A shows in B, or is lost | Drafts are kept per session | Test: type in A, switch to B (empty), back to A (draft back) |
| Memory grows with every session visited | Drop entries for sessions no longer open, so OpenCode's own limit applies | Test: visit 5 sessions, at most open tabs + 3 transcripts kept |
| A send in progress on A lands in B | Each send carries its own session ID | Test: start a send in A, switch to B, request names A |
| RocketClaw shows the wrong link or title | They read the shown session, which the view reports | Browser test: switch, "Copy server link" names B |
| Something in OpenCode's session screen assumes a fresh start | OpenCode's own app moves between sessions this way | The tests above, plus a check on Hally |

## Optional step 2: keep the one view while a RocketClaw tab shows

Step 1's memory is lost whenever a RocketClaw tab opens, because the view is disposed. Keeping that single view
mounted but hidden would keep it. With only one view, the multi-view problems (overwritten settings, duplicated
toasts, files dropped into every view, the shared image loader, RocketClaw's single `remoteView` slot) do not arise,
but these remain and each needs a fix in RocketClaw's glue, one possibly in copied code:

- A shortcut such as ⌘K pressed with nothing focused reaches the hidden view (`oc/host/fences.ts:59-63`).
- The hidden view's dialogs and toasts stay on screen (its portal sits on `document.body`, `oc/host/mount.tsx:33-39`).
- Hiding with `display:none` makes OpenCode read width 0 and switch to its mobile layout (`oc/host/mount.tsx:41-44`).
- The hidden session is marked read on the server whenever the window has focus
  (`oc/packages/app/src/session/route.tsx:218-233`; a fix here edits copied code).
- Its live event stream and 10-second health check keep running while hidden.

Recommended only if step 1 proves itself and hops through RocketClaw tabs still feel slow.

## Complement: answer "unchanged" when the view is rebuilt anyway

Whenever the view is rebuilt (first visit, a RocketClaw tab hop without step 2, a reload), RocketClaw's proxy
(`openCodeProxy` in `frontend/rpc/relay.go`) could label each list answer with a fingerprint (`ETag`, plus
`Cache-Control: private, no-cache`) and answer `304 Not Modified` when the browser already has it. The proxy still
asks OpenCode every time, so nothing is ever stale. Limited to the lists (skills, models, agents, commands,
integrations, config, projects, providers, file search) and not session data, this matches OpenCode's own line:
it saves your drafts and last 100 sent prompts in the browser, but never the assistant's replies or the transcript.
Rebuilding an unchanged view would drop from ~305 KB to ~80 KB.

## Ruled out: keeping several views alive

Reviewed by three models from different families (Claude Sonnet, GPT-6.1, DeepSeek); all found it unsafe, because
the copied OpenCode UI assumes one view per page: shortcuts could act on a hidden session, hidden dialogs and toasts
stay on screen, hidden sessions are marked read, saved model choices overwrite each other, dropped files attach to
every view, and each hidden view keeps its own event stream and polls.

## How it would be verified

- `oc/host/mount.test.tsx`: the step 1 tests above, all in one mounted view.
- A browser test in `web/`: switching between two OpenCode tabs of one server creates the view once and opens one
  event stream.
- On Hally: requests and bytes per switch, first visit and revisit, before and after, with the probe used above.

## Decision

The owner chose to do only what OpenCode does: step 1, one view per server moved between sessions, so OpenCode's own
memory (each folder's lists, open tabs plus the 3 most recent transcripts) carries over. Not adopted: the
"unchanged" answers from the proxy, and keeping the view alive behind RocketClaw tabs (step 2).
