# State Store consolidation: rehearsal and downtime runbook

This release moves the State Store to its new tables in eleven migrations, `049` to `059`. They all run at one startup of the new binary, after `048_shadow_step_limit` from the release before if an install does not have it yet. Each one commits on its own and ends with a self-check; if a check fails, the migration aborts and the binary exits.

Rehearse on copies of Wallace and Gene first, then follow the same steps in the scheduled downtime. Nothing here has been run on Wallace or Gene yet. No tool automates these comparisons yet; the steps below are manual.

Keep secrets, database addresses, and captured conversation content out of this file and out of the repository.

## How to run the SQL

Connect with `psql` as the role RocketClaw uses. Several State Stores can share one database, so first run:

```sql
SET search_path TO <the store's schema>;
```

Queries marked **old schema** run before the new binary starts. Queries marked **new schema** run after it has finished migrating.

## 1. Preconditions

Check these days before the downtime, and again on each copy.

### PostgreSQL version

```sql
SHOW server_version;
SELECT extname, extversion FROM pg_extension WHERE extname = 'pg_trgm';
```

Wallace runs 18.4. Confirm that Gene runs 18.4 or newer, and that the copies run the same major version as the server they came from. `pg_trgm` is already installed by the previous release's search. If it is missing, `055` must create it, which needs the right privilege.

Also check that nothing will cut a long startup short:

```sql
SHOW statement_timeout;
SHOW lock_timeout;
```

Both should be `0` for RocketClaw's role. Also check that the service manager does not kill a startup that takes minutes.

### Free disk

`053` rewrites every saved turn step into one row per item (about 1.1 GB on Wallace). `054` copies the steps of unfinished turns into `steps` and keeps the old rows in `legacy_turn_steps`, next to the moved copies. `055` writes search text onto the entries of listed chats and of runs that report to one, and builds two trigram indexes. Expect the database to grow by at least twice the turn-step data, plus WAL.

```sql
-- old schema
SELECT relname, pg_size_pretty(pg_total_relation_size(oid)) AS total, pg_size_pretty(pg_relation_size(oid)) AS main_table
FROM pg_class
WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r'
ORDER BY pg_total_relation_size(oid) DESC
LIMIT 10;

SELECT pg_size_pretty(pg_database_size(current_database()));
SHOW max_wal_size;
SHOW archive_mode;
SELECT slot_name, active, restart_lsn FROM pg_replication_slots;
```

Plan free disk of at least: 2 × total `turn_steps` + main table of `session_entries` + total `message_search` + `max_wal_size`. If WAL is archived, or a replication slot holds it, the WAL written during the migration (about the size of everything rewritten) stays on disk until it is archived or the slot catches up. On each copy, record `pg_database_size` before and after the migration, and use the measured growth, plus a margin, for production.

### Every visible chat is in the search index

`049` refuses to run while any visible chat lacks a `message_search_indexed` marker, since `055` moves search text from `message_search`. The refusal commits nothing, so the previous binary still starts. The previous release adds the markers in a startup backfill. This query, `049`'s exact refusal predicate, must return no rows:

```sql
-- old schema: the chats today's visibleChatsSQL lists, without a search marker
WITH visible_chats AS (
    SELECT c.conversation_id FROM managed_conversations c
    LEFT JOIN shadow_bindings b ON b.child_conversation_id = c.conversation_id
    LEFT JOIN external_mcp_sessions p ON p.private_conversation_id = c.conversation_id
    LEFT JOIN managed_conversations parent ON parent.conversation_id = c.parent_conversation_id
    WHERE c.conversation_id NOT LIKE 'cron:%' AND c.conversation_id NOT LIKE 'one-off-cron:%'
        AND b.child_conversation_id IS NULL AND p.private_conversation_id IS NULL AND parent.conversation_id IS NULL
)
SELECT v.conversation_id FROM visible_chats v
LEFT JOIN message_search_indexed i USING (conversation_id)
WHERE i.conversation_id IS NULL;
```

If it returns rows, keep the previous release running until its backfill finishes, then check again.

### Completion Notes whose destination would move

`057` aborts if a pending Completion Note's old destination differs from where its run now reports. Background Jobs now follow the run's `reports_to`. This query closely matches that check; the rehearsal is the exact test:

```sql
-- old schema
WITH runs AS (
    SELECT conversation_id, NULLIF(producer_inbound_json->>'SyncDestination', '') AS reports_to
    FROM managed_conversations WHERE producer_inbound_json IS NOT NULL
)
SELECT j.conversation_id, j.job_id, j.sync_destination, r.reports_to
FROM background_jobs j LEFT JOIN runs r USING (conversation_id)
WHERE j.note_state = 'pending' AND j.sync_destination <> COALESCE(r.reports_to, '');
```

If it returns rows, let the owning conversation take its next turn so the note is consumed before the downtime. If that is not possible, stop and decide how to handle them first. This is the open U13 decision in the release notes: these jobs follow the run's `reports_to`, or keep a redirect per job.

### Wallace's 641 MB running turn

`049` refuses to run while any turn is running. Find the large running turns:

```sql
-- old schema
WITH running AS (
    SELECT id, conversation_id, phase FROM active_turns WHERE phase IN ('running', 'delivering')
)
SELECT r.conversation_id, r.id, r.phase, pg_size_pretty(sum(pg_column_size(s.value))::bigint) AS steps
FROM running r
JOIN turn_steps s ON s.conversation_id = r.conversation_id AND (s.key = r.id OR starts_with(s.key, r.id || '/'))
GROUP BY r.conversation_id, r.id, r.phase
ORDER BY sum(pg_column_size(s.value)) DESC;
```

On a Wallace copy (set up as in section 2), start the **current** binary, open that conversation in Web, and stop the turn. It passes if the turn row becomes `phase = 'done'` with `terminal = 'stopped'`, and the binary then shuts down normally. If stopping fails, hangs, or runs the server out of memory, do not stop it in production: let it finish, and schedule the downtime after it completes.

Web cannot stop a turn that the query in section 3, step 4 lists. On the October 20 copy, the largest running turn is one of those: `turn-1791490122634033525`, with 184 MB of stored steps. Rehearse that step's script on the copy instead.

Also note the largest saved turn records. `053` splits each one in memory, so watch the server's memory while `053` runs:

```sql
-- old schema
SELECT conversation_id, key, pg_size_pretty(pg_column_size(value)::bigint) AS stored_size
FROM turn_steps ORDER BY pg_column_size(value) DESC LIMIT 5;
```

## 2. Rehearsal on a copy

Rehearse on Wallace and on Gene, one copy at a time.

1. **Make the copy.** Restore the latest backup into an isolated PostgreSQL server of the same major version. Give the copy its own copy of the workspace. Give it a config that cannot reach anything real: no Slack tokens, `mcp_external.enabled` set to false, and no cron files in its workspace. A copy runs tools for real in its workspace, and saved queued work starts again when a binary starts.
2. **Pick the sample.** Using the old schema, write down the conversation IDs to compare:
   - the 20 most recently updated listed chats;
   - every chat a hidden run reports to (`producer_inbound_json->>'SyncDestination'`), and every chat holding copies of a run's entries (`sync_source_entry_id`);
   - a Web chat that continues a cron run (`web:cron:%`);
   - a chat with a staged undo (`revert_message_id <> ''`);
   - a Slack thread, an External MCP thread, a subagent session, and a shadow's parent.

   Also pick about ten search terms: a rare word, a common word, a person's name, a Slack mention such as `<@U…>`, a word that appears in cron reports, `%`, `_`, and a word with non-ASCII letters.
3. **Capture the old binary.** Start the **current** binary on the copy and open Web. Web's calls are JSON: `POST /api/History`, `POST /api/SearchMessages`, and `GET /api/ListSessions`. In the browser's developer tools (Network), save each response for:
   - `History` of each sampled conversation: the first page, and one older page using `before`;
   - `SearchMessages` for each term;
   - `ListSessions`.

   Then, in a scratch chat, have an agent that may use `rocketclaw_sessions_get` page through three sampled conversations, with `limit` and `before_entry_id`. Save the tool outputs. Measure list and search times as in step 7.
4. **Drain.** Wait until the refusal query in section 3, step 2 returns nothing but the stuck turns of step 4. Then stop the binary, and clear those turns as in section 3, step 4.
5. **Start the new binary** on the same copy, and time it. Note the time of the log line `initializing rocketclaw state store schema`. Wait for `initialized rocketclaw state store schema`, which reports `migrations` (11) and `elapsed`. Then get each file's time:

   ```sql
   -- new schema: applied_at is when each file finished; 049's time runs from the "initializing" log line
   WITH applied AS (
       SELECT id, applied_at, applied_at - lag(applied_at) OVER (ORDER BY id) AS took FROM pg_migrations
   )
   SELECT id, applied_at, took FROM applied WHERE id >= '049' ORDER BY id;
   ```

   Expect `053` (rewrites about 1.1 GB on Wallace) and `054` (keeps old rows next to moved copies) to take longest. `049` and `059` each read every saved entry in full to compare its bytes. On a copy of Wallace taken on 2026-10-20, all eleven files took 5 min 54 s together on a 12 GB, 8-core machine, the longest being `049` (2 min 1 s), `053` and `054` (about 1 min each). Record `pg_database_size` again.
6. **Check the result.** See section 4. Run the before/after harness in `internal/rocketclaw/backend/testdata/equivalence` on the same restored copy: it drains the copy with the previous code, reads every surface people and agents use with the previous code, migrates a clone with this code, reads them again, and writes a report. Every surface must pass; the only expected differences are the ones the report lists as known. On the 2026-10-20 Wallace copy, all 20 surfaces passed (run on 2026-10-21). For a spot check by hand, capture the same responses as in step 3 with the new binary, and diff each pair:

   ```sh
   jq -S 'del(.. | .revision?)' old/History-chat.json > old.json
   jq -S 'del(.. | .revision?)' new/History-chat.json > new.json
   diff old.json new.json
   ```

   Every diff should be empty, apart from the known differences in section 6. Compare `revertEligible`, `canUndo`, and `revertMessageId` in the History responses for undo eligibility. Compare the `rocketclaw_sessions_get` outputs as text.
7. **Time the session list and search.** On the copy's server, log every statement and its plan, and reload the configuration:

   ```sql
   ALTER SYSTEM SET log_min_duration_statement = 0;
   ALTER SYSTEM SET session_preload_libraries = 'auto_explain';
   ALTER SYSTEM SET auto_explain.log_min_duration = 0;
   ALTER SYSTEM SET auto_explain.log_analyze = on;
   SELECT pg_reload_conf();
   ```

   Restart the binary so its connections load `auto_explain`. Load the session list three times, and run a rare-word search three times. The server log shows each statement's duration and its `EXPLAIN ANALYZE` plan. The session list runs the listing query and the chat-origin query. The target is about 100 ms for the list and for a search, measured with the old binary and with the new one. Afterwards, undo the settings with `ALTER SYSTEM RESET` and `SELECT pg_reload_conf();`.

   If the target is not met, first check that statistics are fresh:

   ```sql
   -- new schema
   SELECT relname, last_analyze, last_autoanalyze FROM pg_stat_user_tables
   WHERE schemaname = current_schema() AND relname IN ('conversations', 'entries', 'turns') ORDER BY relname;
   ```

   Then look in the plans for sequential scans over `entries` or `conversations`. Synthetic tests at 6,000 listed chats measured a 112–163 ms session list (main: 124–137 ms) and a 121–174 ms rare-word search (main: 176–228 ms). If the list is still slow on Wallace's copy, the planned next step is to store each chat's creating source on its conversation row (about seven write paths keep it exact). That is a follow-up change. Record the numbers, and decide whether it must land before the downtime.
8. **Record the results** for each copy: server version, database size before and after, each file's time, the total, list and search times for the old and new binaries, bucket and kind counts, and whether every diff was empty.

## 3. The downtime

1. **Stop new work.** RocketClaw has no switch that pauses cron or connectors. Choose a window in which no cron run is due:

   ```sql
   -- old schema
   SELECT relative_path, to_timestamp(next_due_unix_ns / 1e9) AS next_due FROM cron_schedules ORDER BY next_due_unix_ns LIMIT 10;
   ```

   Ask people to stop sending in Web and Slack, and pause External MCP callers. To turn off External MCP, set `mcp_external.enabled` to false and restart.
2. **Finish or stop running work.** Let running turns, Background Jobs, and shadow turns finish, or stop them from Web. Shut the binary down only when this query, `049`'s exact refusal predicate for unfinished work, returns nothing:

   ```sql
   -- old schema
   WITH unfinished AS (
       SELECT 'turn' AS what, conversation_id, id FROM active_turns WHERE phase IN ('running', 'delivering')
       UNION ALL SELECT 'background job', conversation_id, job_id FROM background_jobs WHERE status = 'running'
       UNION ALL SELECT 'shadow turn', child_conversation_id, id::text FROM shadow_turns WHERE child_turn_id = '' AND terminal = ''
   )
   SELECT * FROM unfinished;
   ```

   The only rows it may still return are the turns stuck on a deleted destination, which step 4 lists and clears. The previous binary can never finish or stop those.

   Run it again after the binary has stopped. If it now returns any other rows, start the previous binary again and repeat this step. If `049` refuses later, it commits nothing, and the previous binary still starts.
3. **Snapshot.** Take a database snapshot you know how to restore, such as `pg_dump --format=custom` of the whole database or a stopped-server volume snapshot. Keep the previous binary next to it. The migrations do not touch attachment storage, and nothing writes attachments during the downtime.
4. **Clear turns stuck on a deleted destination.** Run this with the previous binary stopped, after the snapshot.

   A hidden run's turn runs on the chat it reports to (its `SyncDestination`). When that chat has been deleted, the previous binary's startup skips the turn ("active turn worker conversation is not recorded"). No chat can stop it from Web, since only that chat's bridge finds the turn. So the turn stays `running` forever, and `049` refuses to run. On Wallace there are two: hidden cron runs from October 8 and October 16 whose Web chats were deleted.

   This query lists them:

   ```sql
   -- old schema: unfinished turns whose worker chat has no row
   WITH unfinished AS (
       SELECT id, conversation_id, phase, COALESCE(NULLIF(inbound_json->>'SyncDestination', ''), conversation_id) AS worker
       FROM active_turns WHERE phase IN ('running', 'delivering')
   )
   SELECT u.id, u.conversation_id, u.phase, u.worker FROM unfinished u
   LEFT JOIN managed_conversations w ON w.conversation_id = u.worker
   WHERE w.conversation_id IS NULL;
   ```

   Expect `turn-1791490122634033525` and `turn-1792189340153184567` on Wallace, and no rows on Gene unless a chat a hidden run reports to has been deleted there too.

   The script below writes what the previous binary's own stop would write. It runs `finishTurn` as stopped, then `closeTurn`. The turn ends `done`, `stopped`, with the same stopped final as `outbound_json`. Its inputs leave the Thread Queue. Its claimed Completion Notes are released. Its steps under `<turn>/` are deleted, except a running Background Job's. The turn's own record step stays, as it does for every stopped turn. The script refuses any stuck turn it cannot copy the binary's stop for: one that is delivering, one outside a cron run, one with human input or a goal, and one with shadow turns. If it refuses, roll back and stop: that turn needs a decision.

   ```sql
   -- old schema
   BEGIN;

   CREATE TEMP TABLE stuck_turns ON COMMIT DROP AS
   WITH unfinished AS (
       SELECT id, conversation_id, phase, inbound_json, COALESCE(NULLIF(inbound_json->>'SyncDestination', ''), conversation_id) AS worker
       FROM active_turns WHERE phase IN ('running', 'delivering')
   )
   SELECT u.* FROM unfinished u LEFT JOIN managed_conversations w ON w.conversation_id = u.worker
   WHERE w.conversation_id IS NULL;

   SELECT id, conversation_id, phase, worker FROM stuck_turns ORDER BY id;

   WITH unfinished AS (
       SELECT 1 FROM active_turns WHERE phase IN ('running', 'delivering')
       UNION ALL SELECT 1 FROM background_jobs WHERE status = 'running'
       UNION ALL SELECT 1 FROM shadow_turns WHERE child_turn_id = '' AND terminal = ''
   )
   SELECT count(*) AS unfinished_before FROM unfinished;

   DO $$
   DECLARE
       unsupported BIGINT;
   BEGIN
       WITH shadowed AS (
           SELECT DISTINCT s.id FROM stuck_turns s
           JOIN shadow_turns t ON t.parent_turn_id = s.id OR t.child_turn_id = s.id
       )
       SELECT count(*) INTO unsupported FROM stuck_turns s LEFT JOIN shadowed h USING (id)
       WHERE s.phase <> 'running' OR h.id IS NOT NULL
           OR NOT (starts_with(s.conversation_id, 'cron:') OR starts_with(s.conversation_id, 'one-off-cron:'))
           OR s.inbound_json->>'Human' = 'true' OR COALESCE(s.inbound_json->>'GoalAction', '') <> '' OR s.inbound_json->>'GoalTurn' = 'true';
       IF unsupported > 0 THEN
           RAISE EXCEPTION '% stuck turns need the binary to stop them; stop and ask', unsupported;
       END IF;
   END $$;

   WITH now_ns AS (
       SELECT (extract(epoch FROM clock_timestamp()) * 1000000)::bigint * 1000 AS ns
   ), payloads AS (
       SELECT s.id, s.conversation_id, json_build_object(
           'ConsumedID', '', 'ConsumedText', '', 'ConsumedHeader', '', 'ConsumedRawText', '', 'ConsumedSource', '',
           'Text', '', 'ConversationID', s.worker, 'TurnID', s.id,
           'ExternalConversationID', CASE WHEN s.inbound_json->>'Source' = 'external_mcp' THEN btrim(COALESCE(s.inbound_json->'Metadata'->>'external_conversation_id', '')) ELSE '' END,
           'Agent', COALESCE(c.agent, ''), 'Model', '', 'SourceConversationID', s.worker, 'ReasoningEffort', NULL,
           'Cronjob', CASE WHEN json_typeof(s.inbound_json->'Cronjob') = 'object' THEN format('{"RelativePath":%s,"Agent":%s,"RanAt":%s,"Space":%s}',
               to_json(COALESCE(s.inbound_json->'Cronjob'->>'RelativePath', '')), to_json(COALESCE(s.inbound_json->'Cronjob'->>'Agent', '')),
               to_json(COALESCE(s.inbound_json->'Cronjob'->>'RanAt', '')), to_json(COALESCE(s.inbound_json->'Cronjob'->>'Space', '')))::json END,
           'Complete', TRUE, 'SlackReply', s.inbound_json->'SlackReply', 'Attachments', NULL,
           'GoalTurn', FALSE, 'GoalComplete', FALSE, 'GoalActive', FALSE, 'GoalTurnNumber', 0, 'GoalMaxTurns', 0,
           'Source', COALESCE(s.inbound_json->>'Source', ''), 'Terminal', 'stopped') AS payload
       FROM stuck_turns s LEFT JOIN managed_conversations c ON c.conversation_id = s.conversation_id
   ), fields AS (
       SELECT p.id, f.key, f.value, f.position FROM payloads p CROSS JOIN LATERAL json_each(p.payload) WITH ORDINALITY f(key, value, position)
       UNION ALL
       SELECT p.id, 'ReplyState', r.value::json, 1000 FROM payloads p JOIN turn_steps r ON r.conversation_id = p.conversation_id AND r.key = p.id || '/reply'
   ), finals AS (
       SELECT id, json_object_agg(key, value ORDER BY position) AS outbound FROM fields GROUP BY id
   ), stopped AS (
       UPDATE active_turns a SET phase = 'done', terminal = 'stopped', updated_at_unix_ns = n.ns, outbound_json = f.outbound
       FROM finals f CROSS JOIN now_ns n WHERE a.id = f.id AND a.phase = 'running' RETURNING a.id
   ), owned_inputs AS (
       SELECT s.conversation_id, item->>'input_id' AS queue_item_id FROM stuck_turns s JOIN session_entries e USING (conversation_id)
       CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
       UNION
       SELECT s.conversation_id, applied.id FROM stuck_turns s JOIN turn_steps t ON t.conversation_id = s.conversation_id AND t.key = s.id
       CROSS JOIN LATERAL jsonb_array_elements_text(t.value::jsonb->'applied_input_ids') applied(id)
   ), released_queue AS (
       DELETE FROM thread_queue q USING owned_inputs o
       WHERE q.conversation_id = o.conversation_id AND q.queue_item_id = o.queue_item_id RETURNING q.queue_item_id
   ), released_notes AS (
       UPDATE background_jobs j SET claimed_turn_id = '', wake = hidden.run, wake_attempts = j.wake_attempts + hidden.run::integer,
           wake_after_unix_ns = CASE WHEN hidden.run THEN n.ns + (ARRAY[60000000000, 300000000000, 1800000000000]::bigint[])[LEAST(j.wake_attempts + 1, 3)] ELSE j.wake_after_unix_ns END
       FROM stuck_turns s CROSS JOIN now_ns n
       CROSS JOIN LATERAL (VALUES (COALESCE(s.inbound_json->>'SyncDestination', '') <> '' OR s.inbound_json->>'RequireOutputDecision' = 'true')) hidden(run)
       WHERE j.conversation_id = s.conversation_id AND j.claimed_turn_id = s.id AND j.note_state = 'pending' RETURNING j.job_id
   ), unowned_steps AS (
       SELECT t.conversation_id, t.key FROM stuck_turns s
       JOIN turn_steps t ON t.conversation_id = s.conversation_id AND starts_with(t.key, s.id || '/')
       LEFT JOIN background_jobs j ON j.conversation_id = t.conversation_id AND j.status = 'running'
           AND starts_with(j.job_id, s.id || '/') AND starts_with(t.key, j.job_id || '/')
       WHERE j.job_id IS NULL
   ), cleared_steps AS (
       DELETE FROM turn_steps t USING unowned_steps u WHERE t.conversation_id = u.conversation_id AND t.key = u.key RETURNING t.key
   ), cleared_search AS (
       DELETE FROM message_search m USING stuck_turns s WHERE m.turn_id = s.id RETURNING m.turn_id
   ), counts AS (
       SELECT 'stopped turns' AS what, count(*) AS n FROM stopped
       UNION ALL SELECT 'released queue items', count(*) FROM released_queue
       UNION ALL SELECT 'released Completion Notes', count(*) FROM released_notes
       UNION ALL SELECT 'deleted turn steps', count(*) FROM cleared_steps
       UNION ALL SELECT 'deleted search rows', count(*) FROM cleared_search
   )
   SELECT * FROM counts;

   WITH unfinished AS (
       SELECT 1 FROM active_turns WHERE phase IN ('running', 'delivering')
       UNION ALL SELECT 1 FROM background_jobs WHERE status = 'running'
       UNION ALL SELECT 1 FROM shadow_turns WHERE child_turn_id = '' AND terminal = ''
   )
   SELECT count(*) AS unfinished_after FROM unfinished;
   ```

   On Wallace, expect 2 stuck turns, `unfinished_before` 2, 2 stopped turns, 0 released queue items, 0 released Completion Notes, 874 deleted turn steps (as on the October 20 copy; nothing adds steps to a stuck turn), 0 deleted search rows, and `unfinished_after` 0. On a rehearsal copy taken while other turns ran, `unfinished_before` and `unfinished_after` also count those turns. On Gene with no stuck turns, every count is 0. If the counts match, type `COMMIT;`. Otherwise type `ROLLBACK;` and stop. Then run step 2's query again: it must return nothing.

   On a Wallace copy, this script was compared with the previous binary's stop code, which a test ran on a second copy through a bridge built for the deleted chat. The `active_turns`, `turn_steps`, `thread_queue`, `session_entries`, `background_jobs`, and `message_search` rows matched, apart from `updated_at_unix_ns`.
5. **Start the new binary**, and wait for `initialized rocketclaw state store schema` with `migrations=11`.
6. **Check** as in section 4. Then open a few old Web links, check that a Slack thread still routes to its conversation, and check that an existing External MCP conversation ID still reaches its thread. Then let people back in.

## 4. Checks after migrating

If the binary logged `initialized rocketclaw state store schema`, every self-check passed. Each check aborts startup on any mismatch. Confirm all eleven files are recorded:

```sql
-- new schema
SELECT id, applied_at FROM pg_migrations WHERE id >= '049' ORDER BY id;
```

Record these counts. The queries on `legacy_` tables work only on a store at `059`; a later release adopts `001_baseline.sql` at startup and drops those tables with their rows.

```sql
-- new schema
-- Hidden-run history: every original and copy falls into one bucket.
SELECT bucket, originals, copies FROM legacy_hidden_run_buckets ORDER BY bucket;

-- Conversations by kind; 051 created a row for each history that had none.
SELECT kind, listed, retention, count(*) FROM conversations GROUP BY kind, listed, retention ORDER BY kind, listed, retention;
SELECT kind, retention, count(*) FROM legacy_unrecorded_conversations GROUP BY kind, retention ORDER BY kind, retention;

-- Facts about IDs without a conversation, kept as they were.
SELECT source, count(*) FROM legacy_unmoved_facts GROUP BY source ORDER BY source;

-- Turns: waiting inputs and reminders, Background Jobs, ended turns.
SELECT phase, reason, kind, takes_lane, count(*) FROM turns GROUP BY phase, reason, kind, takes_lane ORDER BY phase, reason, kind, takes_lane;
SELECT audience, count(*) FROM entries GROUP BY audience ORDER BY audience;

-- The same entries and listed chats as 049's manifest.
WITH current_counts AS (
    SELECT 'entries' AS what, count(*) AS after_count FROM entries
    UNION ALL SELECT 'listed chats', count(*) FROM conversations WHERE listed
), baseline AS (
    SELECT 'entries' AS what, count(*) AS before_count FROM legacy_entry_md5
    UNION ALL SELECT 'listed chats', count(*) FROM legacy_listed
)
SELECT what, before_count, after_count FROM baseline JOIN current_counts USING (what);

-- 049's per-table counts, for the record. Raw counts are not expected to match:
-- 053 split turn records into item rows, and 049 applied pending schedule effects.
SELECT table_name, state, n FROM legacy_row_counts ORDER BY table_name, state;

-- What the retired tables keep.
SELECT relname, pg_size_pretty(pg_total_relation_size(oid)) FROM pg_class
WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r' AND starts_with(relname, 'legacy_')
ORDER BY pg_total_relation_size(oid) DESC;
```

## 5. If anything fails: rollback

A failure in `049`'s first step, the refusal, commits nothing; the previous binary still starts. Any other failure leaves the database partly migrated.

To roll back, restore the snapshot and start the previous binary. Never start any binary on a partly migrated database, whether the previous one or a patched new one. The previous binary refuses to start anyway: it reports an unknown migration in the database. Rolling back after people have used the new binary loses everything they did since the snapshot.

| File | Stops when | What to do |
|---|---|---|
| `049` | A turn is running or delivering, a Background Job is running, or a shadow turn is unfinished (commits nothing) | Section 3, steps 2 and 4 |
| `049` | A visible chat has no search marker (commits nothing) | Run the previous release until its backfill finishes; section 1 |
| `049` | Its own self-check (bytes, order, shown chats, buckets) | Restore; report |
| `051` | A history matches no conversation kind (the message names one) | Restore; report the ID |
| `057` | A Background Job is running, or a pending note would report elsewhere | Restore; section 1 |
| any | `% rows differ from …` | Restore; report the file and message |
| any | Out of disk or memory, or a jsonb size error | Restore; add disk or memory, or report |

## 6. Known behavior differences

These are intended. Diffs that show only these are expected.

- Search: when a cron run that reports to a Slack thread is continued in Web, its messages are still found under `web:<run>`, and now also under the Slack thread, which shows them.
- Search: entries only a run's own context holds (`sandbox`) are matched through the run, not a second time through the visible chat. This only removes duplicate hits; the results people see do not change. Search rows held under a run's own ID from before it reported to a chat stay unsearchable in the chat, as before.
- History: running turns' transcript fingerprints change format once, so open clients refetch once. The session list and search no longer report whether summaries or the search index are complete; both always are. The Web protocol changes, so open pages reload themselves after the upgrade.
- Undo: Slack threads, External MCP threads, and chats a cron run started or continues can now undo. In every chat, undo reaches only the Web messages after the newest message from a cron run, External MCP, or Slack; a Slack thread's unlabeled older messages count as Slack. A cron, External MCP, or Slack message that arrives after an undo is staged cancels it at the next Web message instead of being cut. The debug "delete session entries" call removes only what undo can, so a cron report stays in its chat and in the run.
- Delegations: each subagent and review records its parent and the call that started it; 051 matches every existing one against the rules that named it and refuses if any is left over. A history page's delegation list now also names the subagents its calls started, so a subagent panel no longer closes in a chat with a staged undo. A review made inside a subagent's guardrail run nests under that subagent instead of the top-level chat. Forks and newly attached shadows copy their delegations' records.
- Delegation links: 142 task rows saved before 2026-10-17, in 54 conversations (53 of them hidden cron or External MCP runs), saved a two-part subagent key, and their "Open delegation" link now opens nothing. They expire with their conversations, by mid-November.
- Remote OpenCode sessions open only at `/oc/<id>`; an older `/s/<id>` link to one no longer opens it.
- Background Jobs follow their run's `reports_to`. A job started before a run was bound to a chat is now listed and woken in that chat.
- Cron: runs that fall due during the downtime are not made up. Startup rebuilds each cron line from its file, and the next run is the next scheduled time. The overlap check now counts a run's running or delivering turns, including a run resumed after a restart.
- Cron: a due rule advances its next time atomically, but the run's turn is created separately. A double run would need the in-memory window to exceed one scan, about a minute.
- `CreateSession` with a cron-shaped source ID that has no row returns InvalidArgument instead of NotFound. Web only sends IDs from `ListCronJobs`.
- Requests: an ID with surrounding spaces, over 1 KiB, or holding a NUL character, and any text holding a NUL character, is refused as invalid instead of being trimmed or failing in the database. A conversation with no record answers not found on every method, and a database error no longer sends its own text to the client.
- Remote OpenCode sessions: an `opencode:` ID RocketClaw never recorded answers not found. The debug "delete session entries" call refuses a remote session, whose history stays on its server.
- The retired tables keep their rows under `legacy_` names, past the 30-day prune, until a later release's `001_baseline.sql` adoption drops them. `shadow_turns` and `shadow_steps` stay their own tables, so the store has 16 tables, plus the `legacy_` ones until then.
