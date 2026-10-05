---
title: Open delegation transcripts from the web chat
date: 2026-10-01
status: active
---

# Open delegation transcripts from the web chat

## Goal

A person reading a web chat can open what a Task subagent, a guardrail
check, or an automatic permission review did for a tool call, at any
nesting depth, in a read-only panel with breadcrumbs, after reload.

The design reuses existing concepts instead of adding parallel ones:
no new table, migration, RPC, access rule, or status model.

## Design

- **A delegation transcript is an ordinary session history.** Its
  conversation ID is `<producer conversation ID>/<tool call ID>`, and a
  delegation inside it is `<that ID>/<inner call ID>`. Everything one
  tool call starts (its permission review, the guardrail delegation and
  response checks, the Task subagent, and nested `execute` reviews) is
  appended to that one history as separate entries, in order.
- **RocketCode** replaces the func-typed `ChildRunLogger` with one small
  interface that appends a finished child entry under a child key, plus
  an inert implementation. The tool factory carries its child key
  (empty at the root), and each child loop's save appends under
  `childKey + "/" + callID`. The call ID reaches Task, guardrail, review,
  and nested `execute` review children. Children keep
  `InertCheckpointSink`; only finished child turns are saved.
- **RocketClaw** implements the interface by prefixing the producer
  conversation ID and appending through the session store, never
  through the external MCP mirroring path.
- **Lifecycle:** deleting, clearing, and pruning a conversation also
  deletes histories whose ID starts with `<id>/`. Fork copies them under
  the fork's ID in the same transaction. No other lifecycle code.
- **Access:** unchanged. A child of a cron producer starts with `cron:`
  and inherits the cron trace rule; other child IDs are unrecorded IDs,
  which History already serves.
- **History** returns one new field: the IDs of the requested history's
  direct child delegations, including those of the producers it mirrors.
- **Web:** a tool row whose call ID matches a listed child shows an
  "open" control. `?delegation=<child ID>` opens a read-only panel (right
  `<aside>` on wide screens, full-screen `Sheet` on narrow ones) that
  renders the child with the existing transcript components. Breadcrumbs
  are the conversation plus each `/` level of the child ID below the
  first-level child the chat lists. Closing or picking a crumb pushes
  history, so Back, Forward, and reload work.

## Deliberately out of scope

- Workflow agents, live updates while a delegation runs, and transcripts
  of failed or stopped delegations (their error stays on the parent row).
- Verdict, outcome, or kind metadata beyond what the transcript shows.
- Hiding delegation histories from the agent session tools.

## Verification

- RocketCode: Task, guardrail, review, and nested `execute` review
  children append under the expected keys; root saves are unchanged.
- RocketClaw: prefix delete, prune, and fork copy; History lists direct
  children, including a mirrored producer's.
- Web: browser test opens a delegation, follows a nested one through the
  breadcrumbs, and survives reload, at desktop and phone widths.
- `go test ./...`, `make lint`, `make test`, CLOC budgets unchanged.
