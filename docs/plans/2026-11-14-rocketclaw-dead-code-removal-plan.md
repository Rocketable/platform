# RocketClaw dead-code removal: execution plan

Date: 2026-11-14. Workspace: `.tmp/workspaces/20261114-rocketclaw-product-deslop`, base `355cbdb3` (`main@origin`).
Decisions: owner's answers to questions 1-14, recorded in `.tmp/notes/20261114-rocketclaw-dead-code-decisions.md`. Findings: `docs/investigations/2026-11-14-rocketclaw-dead-code-decisions.md`.

Nothing runs until the owner approves this plan and answers the open questions below.

## Outcome (2026-11-15)

All phases are done. Each was verified on its own with `make -C internal/rocketclaw lint` and `make -C internal/rocketclaw test` (exit 0, coverage at or above 91.8%, CLOC budget passed) before the stack was rebased onto a newer main, where main had already removed compaction steering (item 4) and the harness default overrides (item 6b) itself. The work ships as two commits: the Slack `Stop` fix, which the item 14 test transport swap exposed and which is a leak on main too, then everything else. The migrations are `095` (unused columns), `096` (hidden-run copies) and `097` (`entries.audience`).

Phase 6 changed course during design: the owner first chose to delete the copy data, then, once rehearsal showed that would delete 25 External MCP support sessions still in use, chose to convert it instead. `096` merges each pair into one row and binds Web-chat copies' runs with `reports_to`; nothing is deleted. Rehearsed on copies of Hally and Wallace: views are identical except the accepted differences (old cron runs link to their existing chat, one Hally chat loses Pause, `agent:` search finds those chats by their run's agent, six Wallace chats show duplicated messages once). Before shipping: back up, and rerun the rehearsal on fresh copies, because `096` refuses any shape it did not expect.

Before deploying to Hally, a fresh database backup was taken; the rebased code was compiled and vetted, but the full local suite and the rehearsal were not rerun on it.

## Open questions

1. **Item 14 tests. Decided: C.** Both `TestRunServeReportsSlackStartupErrorWithCurrentConfig` and `TestAssembleFrontendsWiresSlackCronAndMCP` keep their fake Slack by swapping `http.DefaultTransport` inside the test for a RoundTripper that sends `slack.com` requests to the `httptest` server and passes everything else through, restored in `t.Cleanup`. slack-go's default client is `&http.Client{}` (`vendor/github.com/slack-go/slack/slack.go:195`), so it uses that transport. These tests must never call `t.Parallel` (no test in `cmd/rocketclaw` does today).
2. **Items 1-2 data access. Decided: A.** Run the three read-only queries on Wallace and Hally (the `entries` scan off-peak where possible), and make database copies under `.tmp/` for the rehearsal. The migration is only ever run against the copies until the owner approves shipping it.
3. **Delivery. Decided: A.** Local commits only, one per phase, in this workspace. No bookmarks pushed and no PRs; the owner reviews here and decides later about pushing.

## Phases

Each phase is its own commit (or PR), smallest risk first. Every phase ends with: `gofmt` on touched files, `make -C internal/rocketclaw lint`, `make -C internal/rocketclaw test`, web `make lint` when TS changes, a re-read of the diff against the Go standards in `AGENTS.md`, and a re-check that the invariants below still hold.

### Phase 1: plain deletions (no behavior change)

| Item | Change |
| --- | --- |
| 8 | Delete `harness.Looper` (`harness/looper.go:288-291`) |
| 11 | Delete `harness/opencode.json` |
| 4 | Delete `CompactionSteering` from `harness.Config`/looper and plumbing (`rocketcode.go`, `tools.go`, `looper.go:1149-1157`), the `bridge.go:2810` literal, `TestLooperInjectsCompactionSteering`, the `main_test.go:848` literal |
| 7 | Delete `readToolParams.Filename`, `applyPatchToolParams.Patch`; simplify the 4 `cmp.Or` calls; delete `TestSandboxedToolParameterAliases`; fix the `mcp_tools.go:929` comment (keep the `case`) |
| 12 | Delete `Config.Environment`, `validateEnvironment`, the clone in `locked.go:55`, `TestValidateEnvironmentEntries`, the `TestLockedConfig` and `server_test.go:2054` lines, `rocketclaw.example.json:8-10`; review `frontend/rpc/README.md:224` wording |
| 13 | Delete `taskParams.Command` and the schema property; update `background_test.go` schema expectations and fixtures; rewrite the stale `task` description lines (slash commands, Read/Glob, Write) to match RocketClaw's execute-only tools |
| 14 | Delete `connector.go:152-154` and the `os` import; the two tests swap `http.DefaultTransport` to keep their fake Slack (open question 1) |

Invariant: model-visible schemas change only for `task` (no `command`); config files with `environment` still load.

### Phase 2: harness settings

| Item | Change |
| --- | --- |
| 6a | Remove `ExperimentalStrongerSkills` (keep the "on" path), `AutoApprovePermissions` (keep the "on" path; handoff now also validates reviewer names), `ExpandPromptShellCommands.InputPrompts`; update tests that expected the "off" paths; watch for tests with `auto` rules that start calling the reviewer |
| 6b | Remove `Config.Model`, `ReasoningEffort`, `CompactThreshold` and their `normalizeConfig` filling; use named constants (`gpt-6-luna` reviewer default, `high`, 200000) directly; adjust tests; update `docs/solutions/architecture-patterns/per-provider-autocompaction-threshold.md` |
| 5 | Remove `Config.Diagnostics`, the `diagnosticsWriter` parameter and its 3 `io.Discard` arguments, `printRuntimeDiagnostics`, and every diagnostic event emission (`looper.go:2189-2209`, `mcp_tools.go:877`, `permission_review.go:167-205`, `tasks.go:416`) plus any `ChatResponse` kinds only they use; rewrite or delete tests that observe them |

Invariants: guardian model, default effort and compaction threshold unchanged; skill loading output unchanged; auto review unchanged for every turn with tools; accepted change: long tool-only turns may log "no first output yet".

### Phase 3: test-only methods

| Item | Change |
| --- | --- |
| 10a | Delete `externalmcp.Server.URL` and the `url` field; `StartSessionPromptServer` takes a `net.Listener`; `cmd/rocketclaw/mcp.go` listens on `mcp_external.listen_addr`; tests listen on `127.0.0.1:0` and use `Addr()` |
| 10b | Delete `ObserveTranscript`; move its doc comment to `observeTranscriptDB`; fix comments at `store.go:1001,1127`; ~54 test calls switch to `ObserveHistory(...).Entries`; delete the blank-ID test |
| 10c | Delete `TranscriptPage` (keep `transcriptPageSQL`); its 2 tests read `Inventory.From`/`Oldest` from `ObserveHistory` |
| 10d | Delete `UpsertExternalMCPSession` and its SQL; setup-only tests use `RegisterExternalMCPConversation`; old-version and moved-binding states via raw SQL in backend tests (keep prune coverage); reshape `rpc` tests |

Invariant: External MCP listens where configured, and its error on a busy port is unchanged.

### Phase 4: sandbox wire field

Item 9: drop `guest_env` from `sandbox/wire/wire.proto`, add `reserved 5; reserved "guest_env";`, install `protoc-gen-go v1.36.12` and run `go generate ./internal/rocketclaw/sandbox/wire`, update `wire_test.go:26,99`, rework the forge program and G05 in `harness/emulated_linux_test.go` so it proves a forged environment field is refused and host secrets stay untouched, update rows F04/G05 in `docs/verification/2026-11-01-emulated-shell-acceptance.md`. Also run `make sandbox-test`.

### Phase 5: drop unused columns

Item 3: new migration `095_drop_retired_conversation_columns.sql` (renumbered after `main` added `093` and `094`) dropping `settled`, `reopened_at`, `snoozed_until`, `producer_effects_through_id`, following `073_drop_spawned_by.sql`; add it to the lists in `store_schema_test.go:643,648`. Ships on its own.

### Phase 6: hidden-run copies migration (items 1 and 2)

Gated by open question 2. Steps:

1. Run the three read-only queries on Wallace and Hally; report counts.
2. Design the migration from the real data: merge each old copy into a single row (or delete the copy and keep the original), carry over what the reads provided (`first_source` origin, undo floor and revert markers, subagent panel links, hiding of deleted-source copies), and repoint any saved IDs that refer to copies. **Show the design to the owner before writing it.**
3. Write the migration; then remove the `audience` filters (about 12 sites), the run-context prune kind, the `sync_source_*` reads and the dead branches in `frontend/rpc/server.go`, `voice.go`, `fork.go`, `store.go`; drop `entries.audience` and the `session_entries_sync_source_id` index; update `revert.go:40-45` comments and the 3 `frontend/rpc/README.md` passages; delete or rework the dedicated tests.
4. Rehearse on copies of the Wallace and Hally databases under `.tmp/`: compare visible History, search and undo floors for affected chats before and after.

Invariant: every chat shows the same messages, once each, before and after; undo floors and origin labels unchanged.

## Afterwards

- Update `docs/investigations/2026-11-14-rocketclaw-dead-code-decisions.md` and the product-surface report to mark items done.
- Report README impact (expected: `frontend/rpc/README.md` for item 2, possibly `:224` for item 12; no top-level README change).
- The workspace stays until the owner authorizes retiring it.
