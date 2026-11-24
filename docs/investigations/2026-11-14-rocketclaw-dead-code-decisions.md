# RocketClaw dead-code candidates: findings for decision

Date: 2026-11-14. Base revision: `355cbdb3` (`main@origin`). Read-only investigation; no code changed. Follows `2026-11-14-rocketclaw-product-surface.md`, section 4.

Each item: what it is, whether the "dead" claim held, how it got here, what removal costs, risks, options. The owner decides.

**Outcome (2026-11-15):** the owner decided every item; all were carried out as described in `docs/plans/2026-11-14-rocketclaw-dead-code-removal-plan.md` (Outcome section), except that items 1-2 converted the old copies instead of deleting them, and item 6's `Model`, `ReasoningEffort` and `CompactThreshold` became named constants rather than deletions. Item 10a kept `StartSessionPromptServer` but made it take a `net.Listener`; item 14's tests keep a fake Slack by swapping `http.DefaultTransport`.

## Summary

| # | Item | Claim held? | Removal size (prod / tests) | Main risk |
| --- | --- | --- | --- | --- |
| 1 | `entries.audience` | **No.** Go never writes it, but migration 049 (v0.2.0) did; upgraded databases hold `sandbox`/`visible` rows | ~42 / ~100, plus a migration | Removing the filters shows pre-v0.2.0 hidden-run messages twice |
| 2 | `sync_source_entry_id` reads | **Partly.** Not written since v0.2.0, but old copied rows remain, and the reads feed undo, hiding, panels and origin | ~20 across 3 packages / ~100-150; the index alone is 1 migration line | Visible behavior changes for old copied rows |
| 3 | 4 dead `conversations` columns | **Yes** | 1 migration (~10 lines) | Rollback to older binaries refused (true of any migration) |
| 4 | `CompactionSteering` | **Yes** | ~15 / ~41 | None found |
| 5 | Runtime diagnostics | **Understated.** Print is ~µs; the flag also gates diagnostic events nobody reads since 2026-10-02 | A: ~21 + 3 call args / ~53 mechanical. B: ~100-200 / many tests | B changes the 30 s "no first output" warning |
| 6 | `ExperimentalStrongerSkills`, `AutoApprovePermissions`, other constants | **Partly.** Two flags and `InputPrompts` are dead; `Model`, `ReasoningEffort`, `CompactThreshold` feed live defaults | ~20 + small / ~30 + some unknown | Tests using `auto` rules with the default may start calling the reviewer |
| 7 | `filename` / `patch` aliases | **Yes** | ~6 / ~43 | None found |
| 8 | `harness.Looper` | **Yes** | 4 / 0 | None |
| 9 | `guest_env` wire field | **Partly.** Production never uses it; tests do, including a hostile-guest security test | ~1 proto + regenerate / ~4 | Must `reserve` field 5 so it is never reused |
| 10 | Test-only exported methods | **Yes, but** three are thin wrappers or a needed test seam | 2-21 each / 6-54 test edits each | Test churn outweighs savings for 10a-10c |
| 11 | `harness/opencode.json` | **Yes** | 1 file (40 lines) | None |
| 12 | `environment` config key | **Yes.** Never applied in this repo's whole history | ~23 / ~31, example 3 | Malformed entries stop failing startup |
| 13 | `task.command` | **Yes, and worse:** the schema makes it required on every `task` call | 2 / 2 | One prompt-cache miss |
| 14 | `ROCKETCLAW_SLACK_API_URL` | **Yes** (only tests set it) | 4 / replacement needed for 2 tests | Removing it alone costs fake-Slack coverage |

---

## 1. `entries.audience`

**What it is.** A tag on each saved message: `both`, `sandbox` (only the hidden cron/External MCP run's own agent sees it) or `visible` (only the chat the run reports to shows it). It let v0.2.0 keep both originals and old copies without showing anything twice.

**Claim corrected.** Go code never writes a non-default value (only insert: `backend/store.go:1862`; `fork.go:206` copies without it). But old migration `049_hidden_run_entries.sql`, shipped in `zvyvxzwnvvvk` (v0.2.0, 2026-10-19), set `visible` and `sandbox` on existing rows (old file lines 159-163). Migrations were squashed into `001_baseline.sql` by `pyprsknurqwz` (v0.2.5). Any database upgraded through v0.2.0 that had hidden runs holds such rows. The plan names two production deployments upgraded that way.

**Read sites** (more than first reported): `revert.go:60-70`, `session_tools.go:416-419`, `store_dao.go:171`, `message_search.go:165,261`, `store_summaries.go:133`, `store.go:1238,1240,1992,2013`, prune kind `store.go:635,641,659-665,718-724,766-768,1557-1569`.

**Do old rows survive retention?** Yes. v0.2.0 is 23 days old. The cron-run prune drops `sandbox` rows 90 days after a run's last message (first around January 2027) and never touches `visible` rows. Active chats keep old rows indefinitely.

**If the filters were removed:** each pre-v0.2.0 hidden-run message would appear twice in the chat it reports to, an External MCP run would replay its history twice to the agent, and search could return duplicates.

**Removal cost.** ~12 one-line filter edits, ~30 lines of prune code, `DROP COLUMN` migration, delete `TestPruneStateDropsFinishedCronRunContext` and `TestMessageSearchSkipsSandboxEntries`, rework `revert_test.go:835-836,1667` (they use the column for failure injection). A data migration first, because copies and originals have different IDs that undo points and links refer to; 049 itself needed a rehearsal.

**Options.**
- Keep (the filters cost ~12 lines).
- Run the data check below; remove only if no non-`both` rows exist on every deployment.
- Write and rehearse a data migration, then remove.

## 2. `sync_source_entry_id` / `sync_source_conversation_id`

**What it is.** Keys in a saved message's JSON that marked it as a copy of a hidden run's message and named the source.

**Claim corrected.** The writer was removed in v0.2.0 (`zvyvxzwnvvvk`); nothing writes them since. But migration 049 kept every copy unchanged, so old rows hold the keys. The two read sites (`revert.go:72-74`, `store_summaries.go:128-136`) produce `synced` and `source_conversation_id`, which feed: the undo floor (`revert.go:182`), hiding copies whose source was deleted (`frontend/rpc/server.go:444,2310`, `voice.go:661`, `fork.go:41-43`), opening subagent panels on copied turns (`store.go:1031-1035`), and a chat's origin (`conversations.first_source`). `docs/solutions/logic-errors/external-mcp-thread-lost-metadata-after-copy-free-hidden-runs.md:71` records keeping the reads on purpose.

**The index** `session_entries_sync_source_id` (`001_baseline.sql:458`) serves no production query since `lqryvskkmtkz` (2026-11-01). Only `TestTranscriptChangeUsesSyncSourceIndex` checks it.

**If the reads were removed, for old copied rows:** deleted-source copies reappear in History, voice and forks; compaction summaries inside copies become visible; undo can cross old cron/MCP messages; some subagent panels stop opening; a chat's origin label can change. Precedent: `lqryvskkmtkz` accepted a similar loss for the Cron page.

**Removal cost.** ~8 lines of SQL plus dead branches in 3 packages, `DROP INDEX`, ~100-150 test lines, 3 passages of `frontend/rpc/README.md`.

**Options.**
- Drop only the unused index now (1 migration line, 1 test).
- Count rows with the keys; remove the reads if zero, or accept the changes above.
- Keep.

## 3. `conversations.settled`, `reopened_at`, `snoozed_until`, `producer_effects_through_id`

**Claim confirmed.** No Go, trigger, view, index, proto or web code uses them. Every Go `settled` match is a CTE name or local variable.

**History.** The first three were the old "settle/snooze" sidebar state, last used before `kqyzutvnrypx` (2026-10-07, "remove conversation settle, snooze, and auto-settle"); they were kept so tab variants could share one database, and `docs/plans/2026-10-07-1521-feat-web-tabs-replace-sidebar-plan.md:72,89` says to drop them once tabs shipped (they have). `producer_effects_through_id` was used one last time by migration 049 to convert pending follow-ups; its last Go use went in v0.2.0. CONCEPTS.md:400 says retired data stays until a later release drops it.

**Removal cost.** One migration with four `DROP COLUMN`s (pattern: `073_drop_spawned_by.sql`), add it to `store_schema_test.go:643,648`. No behavior change.

**Risks.** As for any migration: older binaries refuse the upgraded database; dropped columns cannot be restored; outside SQL readers are unknowable.

**Options.** Remove now, or keep (no runtime cost).

## 4. `CompactionSteering`

**Claim confirmed.** An optional developer message added after a compaction (`harness/looper.go:1149-1157`). It came from the retired standalone `cmd/rocketcode` (`ROCKETCODE_COMPACTION_STEERING`, retired `orqqrnxonxxw` 2026-08-11, loader removed `sumwxxnlsmxv` 2026-10-18). RocketClaw always passed `""`. `docs/plans/2026-08-24-1324-feat-slack-steer-or-enqueue-plan.md:385` says not to reuse it.

**Removal cost.** ~15 lines in 4 files; delete `TestLooperInjectsCompactionSteering` (~41 lines).

**Options.** Delete, or keep as an unused hook.

## 5. Runtime diagnostics

**Claim understated.** Two parts:

- **The print.** `printRuntimeDiagnostics` runs once per Web/Slack turn (bridge only; workflow and handoff runs have it off) into `io.Discard`. Benchmarked at ~3 µs for a 20 KB prompt, ~20 µs for 100 KB. Negligible. The writer existed for the standalone tool's `ROCKETCODE_DIAG`.
- **The events.** The same flag gates diagnostic events on the turn's output channel: tool, search and provider events (`looper.go:2189,2201,2209`), nested `execute` calls (`mcp_tools.go:877`, which waits when the channel is full), permission review results (`permission_review.go:167,174,205`), reviewer sub-events (`tasks.go:416`). The bridge keeps only assistant messages (`bridge.go:2540-2557`); it stopped forwarding the rest in `vyqwwzuwzskp` (2026-10-02). So these are built and dropped too. Cost is small.

**One visible effect today:** a diagnostic event can be a turn's first output, which logs `first_response_item` earlier and suppresses the 30-second "no first output yet" warning (`bridge.go:2535-2546`).

**Options.**
- A: remove the writer and print only. ~21 production lines, 3 call arguments; ~53 mechanical test edits; delete `TestPrintRuntimeDiagnosticsIncludesSystemPrompt`; 3 tests switch to reading `loop.SystemPrompt`.
- B: remove the flag and all diagnostic events. Estimated ~100-200 production lines (not measured); many tests observe behavior through the event stream; long tool-only turns could start logging the 30 s warning.
- C: keep.

## 6. Constant settings in `harness.Config`

| Setting | Value in production | Verdict |
| --- | --- | --- |
| `ExperimentalStrongerSkills` | true wherever tools exist (false only in handoff, which has no tools) | **Dead false path.** The false path is OpenCode's style (skill content as the tool result). Collapse: ~8 lines; tests `TestLooperInjectsSteersInSendOrderAsUserRole` and probably `TestLooperMixedSkillSteersDoNotSilentlyLoseAcceptedWork` need new expectations |
| `AutoApprovePermissions` | true wherever tools exist; always-on since `rmrymtkkxzqr` (2026-06-24) removed its config key | **Dead false path.** In production the false path only makes handoff skip reviewer-name validation. Collapse: ~12 lines; delete ~4 false-path tests. Unknown: tests with `auto` rules on the default may start calling the reviewer. `docs/plans/2026-10-29-1118-feat-agent-sudo-mode-plan.md:56` mentions the handoff rule |
| `ExpandPromptShellCommands.InputPrompts` | false everywhere | **Dead.** Replaced by per-message `ExpandShellCommands` (live for cron) in `zpuutpqywksv` (2026-09-03) |
| `ParallelToolCalls` | 16 | **Live** (sets the concurrency limit; tests use 1 or 3) |
| `CompactThreshold` | 0, normalized to 200000 | **Live default** (fallback for providers without `autocompaction_threshold`). Could become a constant |
| `Model` | `""`, normalized to `gpt-6-luna` | **Live default.** It is the guardian model when `auto_approver_model` is unset. Could become a constant |
| `ReasoningEffort` | `""`, normalized to `high` | **Live default** for agents without their own effort. Could become a constant |

**Options.** Collapse the two flags and delete `InputPrompts`; separately decide whether to turn the three live defaults into named constants (no behavior change); or keep.

## 7. `read` `filename` and `apply_patch` `patch` aliases

**Claim confirmed.** Since `zwzowntzzslp` (2026-08-09) both tools exist only inside `execute`, whose arguments are validated against a schema with `additionalProperties: false` (`harness/mcp_tools.go:893-911`, `codemode/codemode.go:255-277`). Every path checked: top-level calls (tool not found), `execute`, resume after restart (results replay only when the re-run script's validated arguments hash-match), decision images, easy agents, workflow workers, reviewers, shadows, backend tool calls. None reaches the aliases. OpenCode itself uses `patchText`. The comment at `mcp_tools.go:929` has been wrong since written; the `case` must stay (it keeps `offset` optional).

**Removal cost.** 2 struct fields, 4 `cmp.Or` simplifications, fix one comment; delete `TestSandboxedToolParameterAliases` (~43 lines). The model sees no change.

**Options.** Delete, or keep.

## 8. `harness.Looper` interface

**Claim confirmed.** `harness/looper.go:288-291`, no users, not in `.mockery.yml`. Its last user, `cmd/rocketloop`, was deleted in `lzyturxuxorv` (2026-08-07, "platform: retire unused tools").

**Removal cost.** 4 lines. Nothing else changes.

**Options.** Delete, or keep.

## 9. `guest_env` in the sandbox wire protocol

**Claim corrected.** Production never sets or reads it (`sandbox/guest_linux.go:107-110`, `broker_linux.go:129-260`). Tests do: `sandbox/wire/wire_test.go:26,99` and the hostile-guest program in `harness/emulated_linux_test.go:310`, whose G05 case (line 286) checks that forged guest environment never reaches host commands. Added in `oqnyzqplspok` (2026-11-01); no production code ever used it.

The docs already say there is no environment field: `docs/explainers/2026-11-01-emulated-shell-sandbox.md:100`, `docs/verification/2026-11-01-emulated-shell-acceptance.md:129`.

No version-skew concern: the guest relay is the host binary bind-mounted read-only (`sandbox/runtime_linux.go:48-53`, `launcher_linux.go:238`).

**Behavior change if removed.** `wire.Read` rejects unknown fields (`wire.go:51-57`), so a frame carrying field 5 becomes malformed (exit 126) instead of accepted and ignored. `reserved 5; reserved "guest_env";` prevents later reuse of the number, which matters for security.

**Removal cost.** 1 proto line plus `reserved`, regenerate `wire.pb.go` (~10 lines; needs `protoc-gen-go v1.36.12` installed), ~4 test edits, verification doc rows F04/G05.

**Options.** Remove, reserve, and update the forge test so G05 still proves forged environment is refused; or keep as harmless metadata.

## 10. Exported methods only tests call

All are in `internal/`, so nothing outside this module can import them.

| Method | Production logic it owns | Test use | Removal |
| --- | --- | --- | --- |
| 10a `externalmcp.Server.URL` | None; it is how tests learn the random port | 7 calls in other packages | 2-4 lines; tests would need a `net.Listener` parameter or racy pre-picked ports. Investigator: keep |
| 10b `SessionService.ObserveTranscript` | Trim + empty check; the query still runs in production via `ObserveHistory` | 54 calls in 11 files | ~18 lines (mostly a doc comment); ~54 test edits to `ObserveHistory` |
| 10c `SessionService.TranscriptPage` | One `QueryRowContext`; the SQL still runs via `ObserveHistory` | `TestTranscriptPages`, `TestHistoryVisibilityUsesRevertedParentIndex` | 11 lines; ~6 test edits |
| 10d `SessionService.UpsertExternalMCPSession` | **Its own SQL** (moves a binding between conversations); production uses `RegisterExternalMCPConversation` | 20 calls in 11 files; some set up states production cannot create | 21 lines; ~20 call sites, some need raw SQL from a package that cannot reach the DB handle |

Last production use: 10b and 10c ended in `mxswstvmmnly` (2026-10-06); 10d ended in `rlpskomwtxwr` (2026-07-25, "platform: remove cross-version migration logic").

**Options.** Keep 10a. For 10b/10c, ~29 production lines vs ~60 test edits. 10d is the only one that removes logic production does not have.

## 11. `harness/opencode.json`

**Claim confirmed.** A 40-line personal OpenCode config: `allow` read/external-directory permissions for two personal source checkouts under `$HOME/go/src` and `~/go/pkg/mod`, and `parallelToolCalls: false` for OpenAI. In the first import (`mysnnpqqnsry`, 2026-06-02), moved unchanged in `oxutlymlnmql` (2026-10-29). Nothing refers to it. OpenCode loads it only when started inside `harness/`, where it widens permissions. No secrets, but it reveals a username and folder layout.

**Options.** Delete, or move its contents to a personal global OpenCode config.

## 12. `environment` config key

**Claim confirmed.** `Config.Environment` is validated (`config/config.go:445,805-821`) and cloned (`config/locked.go:55`) and nothing else. Added with the first import (`mysnnpqqnsry`); no commit in this repo ever applied it. Not in README, CHEATSHEET, CONCEPTS, skel skills, or the Web Config page. Advertised in `rocketclaw.example.json:8-10`.

**Deployments.** One deployment's config has no `environment` key. Another deployment's live config and any merged Secrets Manager secret were not visible.

**Decoding is not strict** (`config/config.go:359`, plain `json.Unmarshal`; one deployment's config already carries unknown keys and loads), so files that keep the key still load. A malformed entry, which fails startup today, would be ignored.

**Removal cost.** ~23 production lines, ~31 test lines (`TestValidateEnvironmentEntries`, `TestLockedConfig` lines, one `server_test.go` line), 3 example lines, optionally `frontend/rpc/README.md:224` wording.

**Options.** Delete (removes a false promise); wire it up as a real feature (new security surface); keep.

## 13. `task` tool `command` parameter

**Claim confirmed, and stronger.** `functionTool` makes every property required (`harness/tools.go:720-731`), so the model must send `command` on every `task` call (`background_test.go:6` asserts the schema). `taskParams.Command` (`tasks.go:23`) is never read. It copies an optional field from OpenCode V1 that OpenCode's own `execute` never read; OpenCode V2's subagent tool, which `background.go:268` cites, has no `command`. The web UI's generic `toolTitle` (`web/src/ui.tsx:3794`) would show a non-empty value as the row title. Old saved calls still decode after removal.

**Stale description** copied from OpenCode V1 `task.txt`: `tasks.go:94` ("/check-file" slash commands), `:97-99` ("Read or Glob tool"), `:120-121` ("Write tool", which does not exist). No test checks this text.

**Removal cost.** 2 production lines, 2 test edits; optional description rewrite. One prompt-cache miss.

**Options.** Delete the field (and optionally fix the description), or keep.

## 14. `ROCKETCLAW_SLACK_API_URL`

**Claim confirmed.** `frontend/slack/connector.go:152-154` sends all Slack Web API calls (including Socket Mode setup) to that URL. Added in `zpuutpqywksv` (2026-09-03) with the assemble test; `pyzkzvtq` (2026-10-30) made the serve test use it. Only `cmd/rocketclaw/assemble_test.go:58` and `serve_test.go:126` set it. No CI, deploy skill or doc mentions it. One deployment's environment does not set it; another's was not visible. slack-go documents `OptionAPIURL` as "only useful for testing".

**A legitimate use exists in principle:** GovSlack (`slack-gov.com`) or an egress proxy.

**If removed,** the two tests build the connector deep inside `processAssembler`/`runServe` and have no other handle to substitute a fake Slack. Choices: a documented `slack.api_url` config key; a `processAssembler` field (covers only one test); swapping `http.DefaultTransport` in tests; or letting the tests hit real Slack.

**Options.**
- Keep (3 lines, hidden knob).
- Replace with a documented `slack.api_url` config key (tests use config; GovSlack/proxy becomes supported).
- Delete and reshape or drop the two tests.
- Keep and document it.

## Data check before deciding items 1-3

Read-only; the first query scans all of `entries`, so run it off-peak or on a copy, on every deployment:

```sql
SELECT c.kind, e.audience, count(*), max(e.entry_timestamp) FROM entries e JOIN conversations c USING (conversation_id) GROUP BY 1, 2;
SELECT count(*), max(entry_timestamp) FROM entries WHERE (entry_json::jsonb ->> 'sync_source_entry_id') IS NOT NULL;
SELECT count(*) FILTER (WHERE settled), count(*) FILTER (WHERE reopened_at > '0001-01-01'), count(*) FILTER (WHERE snoozed_until IS NOT NULL), count(*) FILTER (WHERE producer_effects_through_id <> 0) FROM conversations;
```
