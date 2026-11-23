---
title: Stop the pull request scan from rereading whole session trees
date: 2026-11-22
status: built and verified
---

# Stop the pull request scan from rereading whole session trees

## Problem

On a wide screen, every tab switch runs the thread's pull request scan (`ThreadPRWatch`,
`internal/rocketclaw/web/src/thread-pr-panel.tsx`). Its results were dropped as soon as the tab was left (`gcTime: 0`),
and each run read every message of the thread and of every subagent below it, all at once, plus one empty page per list.

Measured against a live OpenCode server (2026-11-22): a thread with 142 subagents cost 459 requests and 123 MB on every
look; one with 71 subagents, 250 requests and 37 MB. While such a scan ran, a small session load on the same server took
1.4 s instead of 20 ms, so the next tab opened slowly too.

## Design

1. **Keep results across tab switches.** The `threadPRs`, `checkoutPR` and `threadPRHeads` queries keep their results
   (`staleTime: Infinity`, the default cache time). Their keys already include the thread's version, so a change still
   looks again; Refresh still forces a look.
2. **Read only what changed.** Each OpenCode session remembers the pull requests its finished messages showed, up to a
   boundary message. A later look reads messages newest first only down to the boundary, and moves the boundary past
   finished messages only, so a running reply is read again. A session other than the thread is read only when the
   updated time its parent's listing shows moved; an unchanged session lists its children again only if it has any.
   If the boundary message is gone, as after a revert, the session is read again from the start.
3. **Never flood the server.** A level of the walk reads at most four sessions at a time, and a list stops at its first
   short page instead of fetching an empty one.

The model, its checks and their evidence are in `docs/verification/thread-pr-scan`.

## Results

Live OpenCode server, `loadThreadPRs` before and after:

| Thread | Before, every look | After, first look | After, repeat look |
|---|---|---|---|
| 142 subagents | 459 requests, 123 MB | 315 requests, 123 MB | 2 requests, 0.13 MB |
| 71 subagents | 250 requests, 37 MB | 177 requests, 37 MB | 2 requests, 0.10 MB |
| 4 subagents | 16 requests, 3 MB | 10 requests, 3 MB | 2 requests, 0.17 MB |

A small session load during a first look of the 142-subagent thread: worst 1.4 s before, about 50 ms after. Before and
after listed the same pull requests and issues for the 30 newest threads on that server, first and repeat looks.

## Found while testing

With results kept, the narrow-screen pane opens with Refresh enabled, and the sheet focused it on open, popping its
tooltip, so the first Escape closed only the tooltip. `SidePanel`'s sheet now focuses itself on open, as the palette
dialogs do, so one Escape closes it again.

## Left as is

- A thread's first look per page load still reads its whole tree; the OpenCode API has no way to ask only for the parts
  the scan needs.
- Native RocketClaw threads keep their full read per look; they gain the kept results and the four-at-a-time limit.
