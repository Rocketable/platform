# Wallace: stranded external-MCP follow-up messages

## Status and purpose

Investigation date: 2026-09-20.

**Diagnosis only. No fix, queue deletion, replay, configuration change, or service restart was performed.**

This document preserves evidence from Wallace's production deployment and a subsequent audit of `main`.

**Follow-up result:** at `main` commit `549038341ed3da3c91e338bf6270e3c7b84720c6`, the original live queue mismatch has been removed, but startup still does not recover ordinary saved queue rows. See [Current-main audit](#current-main-audit) for source references and local test evidence.

The original deployed-version diagnosis rests on production database observations, retained service logs, and inspection of the deployed source. No controlled reproduction of that old revision was run. The follow-up tests below exercise the pinned `main` revision with synthetic data, not Wallace's production database.

## Bottom line

Six external-MCP follow-ups remained in Wallace's durable `thread_queue` table. Every row belonged to a managed Slack conversation paired with a private external-MCP conversation.

The deployed code can return an MCP response while the previous turn still holds the conversation-pair lock. A follow-up arriving in that interval is saved on the managed Slack conversation's queue. When the private MCP turn finishes, its later-work picker looks up the private conversation's queue instead. It does not find the managed conversation's saved message.

Startup does not generally enumerate ordinary queued messages, so subsequent restarts do not by themselves recover these rows.

Confidence in this explanation is **high**, with the evidence and remaining limits below. It explains persistence of these queue entries, not whether customers ultimately received support through other activity.

## Environment and revision boundaries

| Item | Observed value |
| --- | --- |
| SSH aliases | `wallace`, `gene`, and administrative `bizclaws` reach the same host |
| Tailnet address | `100.125.81.26` |
| Hostname | `ip-10-40-1-44` |
| Service investigated | `wallace.service` |
| Working directory | `/home/wallace` |
| Running application at inspection | `github.com/Rocketable/platform/cmd/rocketclaw`, module version `v0.0.52` |
| Deployed tag commit | `31d8ab13eac0e962deda8a870bbd19dfd2b1ad76` |
| Go version in running binary | `go1.27.1` |
| Service command | `go run github.com/Rocketable/platform/cmd/rocketclaw@latest` |
| Database | `rocketclaw_wallace`, PostgreSQL 18.4 on RDS |
| Applied migrations | `001_init.sql` through `005_drop_store_bootstrap.sql` |
| Candidate `main` inspected during release assessment | `8c2268b37325b36785f4036acf1f13ba21e8c95a` |

The running binary was identified using `go version -m /proc/392292/exe`. That PID was valid during inspection; rediscover it before repeating the check.

The retained startup logs included:

| UTC startup | Version |
| --- | --- |
| 2026-09-10 06:59:28.450 | v0.0.50 |
| 2026-09-10 07:00:27.691 | v0.0.50 |
| 2026-09-16 19:44:30.663 | v0.0.51 |
| 2026-09-16 20:09:44.646 | v0.0.52 |
| 2026-09-19 02:32:47.691 | v0.0.52 |

This does not establish every earlier startup. However, a source comparison found **no differences** between `v0.0.50` and `v0.0.52` in `backend/bridge.go`, `backend/thread_bridges.go`, or `backend/app.go`, the files containing the queue routing, completion, and startup paths discussed here.

## Production observations

Queries used PostgreSQL sessions with `default_transaction_read_only=on` and `statement_timeout=15000`. Credentials were read remotely from `/home/wallace/femtoclaw.json`, passed through environment variables, and not included in this document.

At inspection:

- There were six queue rows, each on a different managed Slack thread.
- All six had a corresponding `managed_conversations` row and an `external_mcp_sessions` binding.
- All six were at position `0` in their respective queues.
- All six had nonempty message text.
- All six had empty `principal`, `slack_channel`, `slack_ts`, and `park_after` fields.
- None of the six managed conversations had a goal row in the inspected join.
- The database had no active-turn checkpoints and no scheduled-message rows at the earlier release inspection.
- Retained logs contained one original private-MCP turn for each pair, finishing successfully, and no matching managed-Slack turn-start/dequeue records for these six threads in the searched period.

Counts and absence checks are observations of a live system, not a single frozen snapshot. They should be repeated before drawing conclusions about the current state.

### The six rows

Customer text is summarized here rather than copied in full. IDs allow an authorized operator to recover the exact rows if needed.

All managed IDs have the prefix `slack-thread:C0B8CQT7P2N:`. All private IDs have the prefix `external_mcp:alitu-cs-customer-triage:`.

| Queue item ID | Managed ID suffix | Private ID suffix | External conversation ID | Message subject |
| --- | --- | --- | --- | --- |
| `H4LSUFNMROIKC42PGMG2OMVQPJ` | `1788701544.878719` | `J7LBOY52CI67KH4UNUOGVS33NG` | `intercom:215475813660616` | Publishing an existing episode |
| `CM7GEE4DTHYLEBLTLEI3SLPWYK` | `1788939561.324029` | `X7JPL54DWII4QBFT2F6M4TJVJ7` | `intercom:215475856429236` | Unexpected additional-show charge |
| `CAYPMEVWVAYLJMHQ2K6R4BND6V` | `1789160862.869749` | `JJPK72KVFUDQQKZHYDC5XWKWIX` | `intercom:215475903363189` | Choppy 20-minute recording |
| `QZ3N3TADEGUSR3ZKCT76WLSDGV` | `1789356985.923149` | `2I73PX7MRGTDFMVWPXCP7AX3PT` | `intercom:215475923677657` | Publishing a second episode to Apple |
| `KQMTTBRTNEAHVQQXXHGIML7DTG` | `1789449324.457619` | `5SSQJ2JF6AXFJCZXG7JGXC4LTF` | `intercom:215475943204713` | Video files may be too large |
| `YTWVZYHRHIDS5BYI7FV3QU5ZET` | `1789828665.900679` | `NIHFWN4S3OOKKYIGOKHH25ZPKL` | `intercom:215476008713898` | Storage and whether to delete/archive 136+ episodes |

### Timing correlation

Times below are UTC. Queue timestamps are the SQL rendering of `stash_at_unix_ns`; log timestamps have millisecond precision. The final column is consequently approximate.

| Date | Original private turn started | Model loop returned | Follow-up stashed | Original turn finished | Stashed before finish |
| --- | --- | --- | --- | --- | ---: |
| September 6 | 13:32:25.770 | 13:59:21.085 | 13:59:21.745535 | 13:59:21.874 | 128 ms |
| September 9 | 07:39:22.288 | 07:51:10.675 | 07:51:11.401974 | 07:51:11.539 | 137 ms |
| September 11 | 21:07:43.507 | 22:13:43.502 | 22:13:43.503829 | 22:13:44.137 | 633 ms |
| September 14 | 03:36:26.808 | 03:48:49.042 | 03:48:49.733489 | 03:48:49.885 | 151 ms |
| September 15 | 05:15:25.093 | 05:28:40.012 | 05:28:40.676929 | 05:28:40.733 | 56 ms |
| September 19 | 14:37:46.770 | 14:44:59.208 | 14:44:59.876873 | 14:44:59.910 | 33 ms |

All six original turns logged `error=<nil>` on return and finish. All six queue inserts fall after the model loop returned and before the original turn's final completion log.

The timestamps do **not** directly record the caller's receipt of the response or the pair-lock release. Those parts of the explanation come from source inspection. In particular, the September 11 insert was almost simultaneous with model-loop return; do not claim that every caller demonstrably waited for the first response before submitting its next request.

### History timestamps

For each pair, the maximum stored `entry_timestamp` was identical on the private and managed sides:

| Date | Maximum stored entry timestamp on both sides |
| --- | --- |
| September 6 | `2026-09-06T13:32:44.560482713Z` |
| September 9 | `2026-09-09T07:39:41.139171149Z` |
| September 11 | `2026-09-11T21:08:02.15793984Z` |
| September 14 | `2026-09-14T03:36:45.527883145Z` |
| September 15 | `2026-09-15T05:15:43.630725753Z` |
| September 19 | `2026-09-19T14:38:05.733646005Z` |

These are stored entry timestamps, **not completion times**. Do not infer that the original turns stopped at those timestamps; the service logs show their later completion.

## Causal chain in the deployed source

All references in this section refer to tag `v0.0.52`, not the working tree. Prefix paths with `internal/rocketclaw/` unless specified otherwise.

### 1. A result can be returned before the pair becomes idle

[`backend/bridge.go:1042–1069`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/bridge.go#L1042-L1069), `publishFinal`:

1. Publishes the outbound response.
2. Calls `msg.CompleteResponseWithAttachments(...)` at line 1063.
3. Waits for outbound delivery at line 1065.

Meanwhile, `Bridge.loop` acquired the conversation-pair lock before handling the request and defers its release until after request handling and later-work selection. Returning a response to the caller does not itself release that lock.

[`backend/store.go:1050`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/store.go#L1050), `PairBusyFor`, reports the pair busy while its lock token is held, including when another request addresses the same private conversation.

This establishes an interval where the caller can receive a result but a new request still takes the busy path. The logs establish that all six inserts occurred while their preceding turns were still finishing; they do not directly expose each request's caller-side timing.

### 2. The busy path saves to the managed Slack ID

[`backend/thread_bridges.go:586–615`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/thread_bridges.go#L586-L615), `SubmitExternalMCP`:

- Resolves the private conversation's external-MCP binding.
- Uses `session.ManagedConversationID` as the pair ID.
- If `PairBusyFor(managedID, conversationID)` is true, calls `stashBusyExternalMCP`.

[`backend/thread_bridges.go:638–659`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/thread_bridges.go#L638-L659), `stashBusyExternalMCP`:

- Converts the managed ID into a Slack target.
- Saves a `ThreadQueueItem` via `StashThreadQueueItem`.
- Keeps an in-memory MCP waiter keyed by queue item ID.

The stored rows' managed IDs and empty Slack message fields match this path. The original payload's response waiter is in memory; the database row is durable.

### 3. Completion checks the private ID instead

[`backend/bridge.go:493–550`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/bridge.go#L493-L550), `Bridge.loop`, calls `b.PickLaterWork(ctx)` after successful request handling.

[`backend/bridge.go:569–615`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/bridge.go#L569-L615), `pickLaterWork`, reads goals, queue rows, and scheduled messages using `b.config.ConversationID`.

For these completing jobs, the logs show that ID is `external_mcp:alitu-cs-customer-triage:...`. The saved row instead belongs to `slack-thread:C0B8CQT7P2N:...`.

The finishing bridge therefore does not select the saved follow-up. Releasing the pair lock does not itself trigger a pick on the paired managed conversation.

### 4. No activation means no removal

[`backend/bridge.go:624–654`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/bridge.go#L624-L654), `submitEnqueuedItem`, would submit the selected item with its queue ID and an activation hook.

[`backend/bridge.go:496–504`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/bridge.go#L496-L504) deletes that queue row after successful activation, before handling the queued turn.

Here, the item is never selected by the private bridge's completion path. This is a failure to start the saved work, rather than evidence that it ran and merely failed to delete its row.

### 5. Restart does not sweep ordinary queues

[`backend/app.go:310–315`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/app.go#L310-L315) restores scheduled-message bridges and active goals. The later startup block restores active turns and handles their pending-steer recovery.

[`backend/bridge.go:269–283`](https://github.com/Rocketable/platform/blob/v0.0.52/internal/rocketclaw/backend/bridge.go#L269-L283), `Start`, arms scheduled messages and starts the request loop. It does not pick ordinary durable queue rows.

There is no general startup enumeration of `thread_queue` in these inspected startup paths. With no other event causing these managed threads to pick later work, their queue rows survive restarts.

## Alternative explanations and limits

| Explanation | Evidence / disposition |
| --- | --- |
| Previous model job failed | All six preceding private turns logged successful completion. Not supported for these cases. |
| An active goal or future schedule is intentionally blocking the queue | No goal rows for these six in the inspected join; no scheduled rows at the earlier snapshot; `park_after` empty. Not supported by observed state. |
| Work ran but queue-row deletion failed | No corresponding managed-turn start/dequeue logs were found, and the source exposes the private/managed lookup mismatch. Less consistent with the evidence. |
| Restarts alone should recover saved work | The inspected startup code does not sweep ordinary queues. Restarts were observed while older rows remained. |
| Customers necessarily went unanswered | Not established. September 19 logs show the original job had already noticed and investigated the storage question. A saved duplicate/follow-up can coexist with work done in the original turn. Customer-resolution status was not audited. |
| Current `main` has exactly the same defect | The follow-up audit below found the live mechanism removed, while the startup recovery gap persists. This was not established by the original production investigation alone. |

Historical logs were searched from September 6 onward. Their retention and completeness were not independently certified. Absence of a matching line is supporting evidence, not proof of all possible historical activity.

## Existing tests and prior work

In the deployed revision, `backend/thread_bridges_test.go:891`, `TestThreadBridgeManagerBusyExternalMCPStashesOnManagedQueue`, checks that busy MCP input is saved on the managed queue and not immediately submitted. It then **manually deletes the queued item** and verifies the waiter receives a removal error.

That test does not exercise a real private bridge completing, releasing its pair lock, and causing the managed queue to run. The deployed `backend/bridge_test.go` contains later-work ordering and goal/schedule tests, but the inspected tests do not cover this cross-conversation completion sequence.

GitHub issue search was unavailable because repository issues are disabled. A targeted PR search found [PR #8, “Steer a live Slack turn or enqueue the next one”](https://github.com/Rocketable/platform/pull/8). Its description establishes that queued work is durable and should be selected after a turn ends. It does not establish that this particular MCP pairing bug was fixed. No same-bug fix was confirmed in the searched PR material.

## How to resume the investigation

### First establish the current state

1. Identify the running binary version again; do not assume Wallace is still on v0.0.52.
2. Pin the exact candidate or `main` commit under investigation.
3. Re-query the six IDs read-only. Record whether each still exists, and any current active turn, goal, or scheduled-message blocker for its conversation.
4. Read the current equivalents of MCP submission, durable queue ownership, turn completion, pair release, and startup queue restoration.
5. Trace any current `PickLaterWork` or equivalent calls across both sides of an external-MCP pair. A renamed or unified implementation may have removed the old mismatch.

### Controlled reproduction to build

Use an isolated test database and synthetic messages. Do not use production Slack/MCP credentials or replay these customer messages.

The smallest useful behavioral test should:

1. Create a private external-MCP conversation linked to a managed Slack conversation.
2. Start the first private turn.
3. Hold its final outbound delivery open after its response has become available, so the pair remains busy deterministically.
4. Submit a second MCP message during that interval and verify it is durably queued on the expected conversation.
5. Allow the first turn to finish, without sending another Slack message or manually calling the queue picker.
6. Require the second message to start exactly once and its durable row to be removed at the intended point.
7. Separately assert prompt framing/principal, completion of the MCP waiter, and outbound Slack routing. Starting a turn alone is not enough to prove correct delivery.

An additional restart case should seed a queued item with no active checkpoint, goal, or schedule, then test the intended startup behavior. Establish the intended contract before changing startup behavior; do not silently turn a diagnosis into automatic replay of old customer work.

Prefer extending the tests that already own MCP busy-queue and bridge completion behavior. Use existing test dependencies and synchronization patterns. Do not introduce real sleeps to hit a millisecond race.

### Interpretation of results

- If the old revision reproduces the failure and the candidate passes the same behavioral scenario, identify the exact change that fixed it and separately decide what happens to already-stranded rows.
- If both fail, the defect persists in the candidate.
- If neither fails, inspect whether the test actually holds the pair busy after the response and exercises the real queue picker, rather than a mock that skips the relevant path.
- Schema migration preserving the six rows is a separate claim from the runtime automatically processing them.
- Do not automatically pop or replay these six entries: first determine whether their customer work has already been handled.

## Read-only evidence queries

Run these only in a read-only session. Obtain connection credentials remotely without printing them. Do not place DSNs or passwords in shell arguments, saved reports, or logs.

```sql
BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';

SELECT q.queue_item_id, q.conversation_id,
       to_timestamp(q.stash_at_unix_ns / 1000000000.0) AS queued_at,
       q.position, q.principal, q.slack_channel, q.slack_ts, q.park_after,
       m.private_conversation_id, m.external_conversation_id,
       g.status AS goal_status
FROM thread_queue q
LEFT JOIN external_mcp_sessions m
  ON m.managed_conversation_id = q.conversation_id
LEFT JOIN conversation_goals g
  ON g.conversation_id = q.conversation_id
WHERE q.queue_item_id IN (
  'H4LSUFNMROIKC42PGMG2OMVQPJ', 'CM7GEE4DTHYLEBLTLEI3SLPWYK',
  'CAYPMEVWVAYLJMHQ2K6R4BND6V', 'QZ3N3TADEGUSR3ZKCT76WLSDGV',
  'KQMTTBRTNEAHVQQXXHGIML7DTG', 'YTWVZYHRHIDS5BYI7FV3QU5ZET'
)
ORDER BY q.stash_at_unix_ns;

SELECT count(*) AS active_turns FROM active_turns;
SELECT count(*) AS scheduled_messages FROM scheduled_messages;
SELECT id, applied_at FROM pg_migrations ORDER BY id;
ROLLBACK;
```

For logs, use `sudo journalctl -u wallace.service` through `ssh bizclaws`, bounded to the dates above, and filter by the private/managed IDs and these message names:

- `bridge dequeued request`
- `starting rocketcode turn`
- `rocketcode looper returned`
- `finished rocketcode turn`
- `pick later work`
- `delete started enqueue item`

Avoid dumping unfiltered debug logs: they can contain customer content and tool arguments. Keep any future scratch files, isolated workspaces, or diagnostic artifacts under the repository's `.tmp/` directory.

## Current-main audit

Follow-up date: 2026-09-20.

Pinned revision: **`549038341ed3da3c91e338bf6270e3c7b84720c6`**, `internal/rocketclaw: share Slack channel-by-name lookup`. GitHub's `main` and the local `main` bookmark both pointed to this commit when checked, including after the diagnostic checks. It is newer than the release-assessment commit recorded above.

### Answer

| Question | Finding |
| --- | --- |
| Can the exact old private/managed queue mismatch strand a newly arriving MCP follow-up? | **No, that mechanism has been removed.** MCP execution now runs through the destination conversation's work loop, and the caller waits through synchronization and delivery. |
| Does startup automatically process an ordinary saved queue row with no active checkpoint, goal, or schedule? | **No. This part remains true.** The local startup probe preserved its queued row and instantiated no conversation bridge for it. |
| Will upgrading alone process Wallace's six existing entries? | **Do not expect it to.** Their observed state matches the missing-startup-consumer case. This audit did not replay the actual rows or re-query production. |

The conclusion concerns this exact failure mechanism. It is not a claim that every queue, delivery-error, cancellation, or crash-recovery path is defect-free.

### What changed in the live path

References in this subsection are pinned to the audited commit:

1. [`cmd/rocketclaw/mcp.go:78–79`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/cmd/rocketclaw/mcp.go#L78-L79) serializes calls for the same external conversation ID until the handler returns.
2. [`cmd/rocketclaw/mcp.go:241–279`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/cmd/rocketclaw/mcp.go#L241-L279) records the private conversation ID and the managed Slack ID as `SyncDestination`. It calls `submitAgent` synchronously before reading the response channel.
3. [`cmd/rocketclaw/assemble.go:87–95`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/cmd/rocketclaw/assemble.go#L87-L95) implements that submission as `CreateConversation`, then `RunTurn`, then `SyncConversation`. An internally available response is not sufficient to return the MCP call.
4. [`backend/conversations.go:291–321`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/conversations.go#L291-L321) submits producer work to the **destination's** bridge, keeping the private bridge as the handler. This replaces the old busy-MCP stash path; `SubmitExternalMCP` and `stashBusyExternalMCP` no longer exist in production Go source.
5. [`backend/bridge.go:538–588`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/bridge.go#L538-L588) holds the destination's work loop until the producer reservation is released. It subsequently calls `PickLaterWork` on that destination bridge, so it reads the managed conversation's queue.
6. [`backend/conversations.go:193–212`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/conversations.go#L193-L212) publishes the synchronized output on the managed ID and releases the reservation afterward. [`backend/runtime.go:82–115`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/runtime.go#L82-L115) waits for live consumers' delivery acknowledgements.
7. [`backend/bridge.go:1146–1156`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/bridge.go#L1146-L1156) also waits for final delivery before completing the inbound response, reversing the old ordering.

The core architectural change is commit [`0c738a7747281999543082c55fa67ae83eebcf56`](https://github.com/Rocketable/platform/commit/0c738a7747281999543082c55fa67ae83eebcf56), `internal/rocketclaw: compose Slack, MCP, and Cron on one conversation Backend`. Its diff removes the busy-MCP stash implementation and introduces `RunTurn`/`SyncConversation` routing. GitHub associates that commit with merged [PR #42](https://github.com/Rocketable/platform/pull/42), “Conversation Backend for Slack and Web,” which documents the shared backend and private-producer-to-managed-conversation behavior. No separate targeted fix for Wallace's six rows was found.

### Why saved rows still remain idle on startup

The remaining chain is straightforward:

1. The database contains a managed conversation and a durable queue row.
2. There is no active-turn checkpoint, active goal, or scheduled message for that conversation.
3. [`backend/app.go:233–258`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/app.go#L233-L258) discovers active-turn recovery; [`backend/app.go:336–381`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/app.go#L336-L381) starts schedules, active goals, and recovery work. None enumerates ordinary queue-only conversations.
4. [`backend/startup_recovery.go:125–138`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/startup_recovery.go#L125-L138) picks later work only for specific unresumable checkpoints, not every conversation with queued work.
5. [`backend/bridge.go:280–294`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/bridge.go#L280-L294) arms scheduled messages and starts a request loop when a bridge is instantiated; it does not pick ordinary queue rows on its own.
6. No consumer is created for the queue-only conversation. Its saved row remains until some further event activates later-work processing or another operation removes it.

A source-wide search of `thread_queue`, `ThreadQueueForConversation`, and queue-reading call sites found no other general startup sweep. Data persistence and automatic execution remain different guarantees.

### Recurrence after deleting the existing rows

Starting with an empty queue removes the historical backlog, but does not make all newly persisted work restart-safe.

For new external-MCP requests, the original busy-stash/mismatched-picker mechanism is gone. These calls use the serialized `RunTurn`/`SyncConversation` path described above.

For new explicit Slack or Web enqueues, a concrete restart window remains:

1. [`backend/thread_bridges.go:620–647`](https://github.com/Rocketable/platform/blob/549038341ed3da3c91e338bf6270e3c7b84720c6/internal/rocketclaw/backend/thread_bridges.go#L620-L647) commits the new queue row before loading/starting its bridge and submitting the in-memory request.
2. If the process stops after that commit but before the row is claimed, an otherwise idle conversation can be left with a durable row and no active checkpoint, goal, or schedule. The database write and execution handoff are separate operations.
3. The next startup encounters the same queue-only state reproduced below and does not start a consumer. The new row can remain idle until another event activates processing.

The callers include Slack's `handleEnqueueCommand` at `frontend/slack/connector.go:3860–3886` and Web queue delivery at `frontend/rpc/server.go:889–898`. The save-to-start window is established by source inspection; the resulting startup state was exercised by the local diagnostic. No process-crash injection was run.

Therefore: **the specific production MCP race has been removed, but deleting the six old rows alone does not eliminate every way a new queue entry can become stranded.** This distinction is about future work, not a requirement to replay the historical messages.

### Local verification

The checks ran in an isolated Jujutsu workspace at the pinned revision, using a dedicated native PostgreSQL **18.6** instance and Go **1.27.1** on macOS. Database files, workspace, test temporary directories, and logs were kept under `.tmp/wallace-queue-main-audit/`. Production runs PostgreSQL 18.4; this was a runtime-path investigation, not a production-copy migration rehearsal.

Existing tests passed before the diagnostic additions:

- `TestRuntimeProducerKeepsDestinationUntilSync`
- `TestRuntimePersistedEnqueueAndProducerArrivalOrder` — all four same/different-producer and enqueue-first/producer-first cases.
- `TestRuntimeSteersWaitForTheirTurnDelivery`
- `TestRunInitializesRuntimeAndCleansUpOnCancellation`
- `TestSubmitExternalMCPInputPreservesPublicConversationMetadata`
- `TestSubmitExternalMCPInputWaitsForOwnQueuedTurnResult`
- `TestSubmitExternalMCPInputReturnsAfterSubmitAgent`
- `TestExternalMCPDuplicateSuppliedIDCreatesOneSlackRoot`

Two temporary diagnostic tests then exercised the disputed boundaries:

**`TestAuditMCPFollowUpDuringFinalDelivery` — PASS with `-race`.**

- Registered a synthetic external-MCP/private/managed binding and used the real `Runtime`, bridges, store, publication, and synchronization code.
- Held the first private final event's acknowledgement open, submitted a second external-MCP-origin request, and confirmed neither submission returned.
- Released acknowledgements one at a time. Observed exactly `private first → managed first → private second → managed second`, with the correct Slack channel, thread, and message target for each.
- Confirmed each submission waited for its managed delivery, both response waiters completed successfully, and the managed durable queue was empty afterward.
- Used the existing attachment-fallback response to avoid a model-service dependency. It tests scheduling and delivery, not model reasoning or prompt framing. The separate existing metadata tests cover framing.
- Called the backend directly, deliberately allowing overlap that the frontend's per-external-ID lock would serialize. It did not run a complete live MCP-to-Slack network exchange.

**`TestAuditStartupStartsPersistedQueueWithoutOtherWork` — FAIL at the expected consumer-start assertion, with `-race`.**

- Created a synthetic binding, recent conversation history, and one saved follow-up with blank principal and Slack-message fields.
- Closed the seeding store, ran the actual backend `Run` startup with the existing generated frontend mocks, then reopened the store.
- Provided a completed frontend-lifecycle channel so `Run` would return after its synchronous startup/recovery sequence. No model call, production connector, manual queue pick, or new incoming message was involved.
- Confirmed the same queue ID survived startup. Observed **one queue row and zero instantiated conversation bridges**.
- The assertion requiring a consumer for that conversation failed with `startup never started a consumer for the saved queue`.

The first draft of this startup probe had no history and was retention-pruned. It was corrected to include recent history, matching the production evidence, before accepting the result. A fixture with no history would not demonstrate the reported stranded-row symptom. The live probe's initial expected fallback text was also corrected to match its attachment flags; its delivery-order assertions already passed.

Additional contract checks passed with `-race`: `TestRunTurnSendsExternalMCPMetadataAsDeveloperMessage`, `TestInitializeSessionDBUpgradesMainSchema` (both five/eight-migration starting points and both migration-ledger names), the four runtime enqueue/producer-ordering cases, and the MCP metadata/submit-completion tests. The migration test uses synthetic fixtures and does not substitute for the outstanding Wallace database-copy rehearsal. `gopls check` reported no diagnostics for the temporary test additions.

Diagnostic command:

```sh
# With ROCKETCLAW_TEST_DATABASE_URL targeting only an isolated local database,
# and TMPDIR/GOTMPDIR set beneath the repository's .tmp directory:
go test ./internal/rocketclaw/backend -run '^TestAudit' -race -count=1 -timeout=120s -v
```

The diagnostic additions were confined to the disposable workspace's existing `backend/app_test.go` and `backend/runtime_test.go`. Their patch and output were captured locally as `.tmp/wallace-queue-main-audit/diagnostic-tests.patch` and `.tmp/wallace-queue-main-audit/audit-tests-corrected.log`; the additional passing run is in `contract-tests.log` in the same directory. These are scratch artifacts, not committed regression tests; the command needs that patch applied to the pinned revision. The local database was stopped and the disposable workspace was forgotten and removed afterward. Verification was targeted to this investigation; no full-suite, lint, or release-certification claim is made.

### Recommended follow-up

If automatic restart recovery is desired, the smallest relevant test belongs with backend startup tests: persist a queue-only conversation, restart the real backend, and require its saved work to activate without another message or an explicit picker call. Keep queue ordering, goal/schedule precedence, prompt provenance, and outbound delivery assertions separate from merely proving that a row disappeared.

For the live path, retain coverage of overlapping producer work and delivery acknowledgement boundaries. The existing runtime ordering tests are useful coverage; the temporary diagnostic adds the specific held-final-delivery sequence.

Handling the six historical customer follow-ups still needs a separate decision about whether their work has already been completed. Removing the old live race does not itself recover those rows, recreate their in-memory MCP waiters, or validate their eventual outbound delivery.

## Scope of this record

The original investigation preserved the production evidence without changing application code or production state. The follow-up exercised the pinned `main` revision with synthetic local diagnostics. No production fix, replay, deletion, migration, configuration change, or restart was performed. Customer-resolution status and full production-copy migration safety remain unverified.

README impact was considered. This is an internal investigation record; no README update is needed.
