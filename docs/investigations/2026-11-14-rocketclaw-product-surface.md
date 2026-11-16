# RocketClaw product surface: what it can do, and what to cut

Date: 2026-11-14. Base revision: `355cbdb3` (`main@origin`). Read-only inventory of `cmd/rocketclaw` and everything it wires in. No code changed.

Purpose: list every interface, feature and capability so we can decide what to delete. Each section ends with cut candidates. The last section ranks them.

## 1. Size today

| Area | Production lines | Test lines | Notes |
| --- | --- | --- | --- |
| `cmd/rocketclaw` | 1,601 | 4,992 | CLI, wiring, Web server start, Tailscale, External MCP glue |
| `backend` | 21,272 | 39,180 | Conversation engine, Postgres store, 22 tables |
| `frontend/rpc` | ~5,900 handwritten (+9,000 generated `web.pb.go`) | 15,579 | Web API: 44 RPCs + plain HTTP routes |
| `harness` | 14,663 | 19,009 | The agent engine ("rocketcode") |
| `frontend/slack` | 1,916 | 5,961 | Slack mentions and more |
| `sandbox` | 2,008 | 521 | Emulated shell (bubblewrap + Ubuntu image) |
| `oai`, `workflow`, `config`, `codemode`, `skel`, `frontend/cron`, `agentlint`, `protocol`, `mcpclient`, `externalmcp` | ~7,800 | ~9,700 | |
| `web` (React UI) | ~10,800 (`ui.tsx` alone 6,329) | 16,445 | Plus 1,045 CSS, 999 proto |
| `oc/packages` (copied OpenCode UI source) | ~310,000 TS, 2,351 files, 26 MB | | Not counted in budgets |
| `internal/web/oc-dist` (built OpenCode UI) | 87 MB embedded in the binary | | Our own `dist` is 14 MB |

Budgets: Go is at about 41.7k of 42.75k lines; TypeScript is at 9,547 of 10,050. There is little room left, so cuts pay for any new work.

Docs that describe this surface: `README.md` (569 lines), `cmd/rocketclaw/CHEATSHEET.md` (474 lines, several single lines over 2,000 characters), `internal/rocketclaw/web/README.md` (1,564 lines, ~950 of them on tabs). The size of the docs is itself a measure of how much behavior exists.

## 2. Ways in and out (interfaces)

| Interface | What it is | Who uses it |
| --- | --- | --- |
| CLI | `run`, `lint`, `agent-graph`, `oai login/list/logout`, `ts login`, `help`. Bare `rocketclaw` runs the server when a config exists. | Operators |
| Web page | HTTPS on `web.listen_address` (default `0.0.0.0:3000`, self-signed unless certs set), optionally also a Tailscale node (`web.tsnet`). Identity from Tailscale. | Primary user surface |
| Web API | `POST /api/<38 methods>`, SSE streams (`ListSessions`, `/stream`, `/typing`, `Voice`), uploads/downloads. Internally re-dispatched through an in-process gRPC server over `bufconn` (`cmd/rocketclaw/web.go:66-79`). | Web page only |
| Hosted Pages | `/pages/{space}/{agent}/{path}`, `/pages-raw/{agent}/{path}` (sandboxed) | People opening agent-made pages |
| OpenCode proxy | `/oc/{server}/api/...` reverse proxy with the server password added, plus `/oc-ui/*` (embedded OpenCode UI) | Remote OpenCode servers |
| OpenCode MCP bridge | `/opencode/{name}/mcp` (Basic auth with the server password) | Agents running inside remote OpenCode |
| Remote RocketClaws | The page calls other RocketClaw servers directly (CORS from `trusted_rocketclaws`) | One page, many servers |
| Slack | Socket Mode. Five events: `app_mention`, `agent_session_stopped`, `agent_session_title_changed`, `channel_rename`, `group_rename`. **Required**: config fails without Slack tokens and channels (`config/config.go:575-600`), startup fails if Slack auth fails (`cmd/rocketclaw/assemble.go:24-26`). | Allowlisted mentioners |
| External MCP | Separate HTTP listener, one tool `session_prompt`. Basic auth from `rocketclaw.users.json`; **no auth at all when that file is missing** (`frontend/externalmcp/server.go:141-144`). | Other AI tools |
| Cron | `cron/*.md` files and easy-agent `routines`; 1-minute ticker | Scheduled jobs |
| Outbound MCP | `mcp_servers` (stdio or HTTP), reconnected for every `execute` call | Agents |
| pprof | `127.0.0.1:6060`, on by default | Operators |
| Telemetry out | Sentry (browser), PostHog (logs, errors, traces, browser replays), OpenTelemetry (traces) | Operators |

## 3. Features by area

### 3.1 Agents and how they are defined

- **Two agent file formats.** `mode: advanced` (explicit `permission` map) and `mode: easy` (sections compiled into `auto` rules, always emulated shell, `workingDirectory`, `commands`, `secretsARN`, `routines`, `delegatedFrom`, `allowedNetworkTargets`). Each has its own rules for approvers and decision models (`harness/easy.go`, `easy_files.go`, branches in `tools.go`, `tasks.go`, `permission_review.go`, `agents.go`).
- **Agent options:** `model#effort`, deprecated `reasoningEffort`, `verbosity`, `maxRecursion`, `shell` (program path or `emulated`), `space`, `sticky` (`off`/`user`/`global`), `additionalInstructions`, `schema.output`, `sudo`, `approver`.
- **Shadow agents:** `agents/<parent>/<shadow>.md` run alongside the parent with the same inputs, real tools, a 1.5× step budget, and Web comparison columns. ~1,100 backend lines, ~150 UI lines, 2 tables, 6 triggers, ~3,700 test lines.
- **Sticky agents:** one session per person (`user`) or for everyone (`global`). Rules about which surfaces may use which kind.
- **Skel:** scaffolds `AGENTS.md`, `agents/main.md`, `agents/voice.md` (an easy agent), examples, 9 `main-*` skills. Rebuilds `.rocketclaw/` on every start.
- **agentlint** (`rocketclaw lint`): 10 checks (RC000-RC011). It only counts `allow` rules, so it barely covers easy agents, which compile to `auto` (`agentlint.go:309-327`).
- **agent-graph:** prints the delegation graph as DOT.

### 3.2 The agent engine (harness)

- **One runtime.** The Go harness runs every session. `oc/` is not a second runtime; it is a front end for someone else's (see 3.9).
- **Tools the model sees:** `execute` (Code Mode Starlark), `load_execute_result`, `task`, `skill`, `find_skills`, hosted `websearch`.
- **Inside `execute` only:** `read`, `glob`, `grep`, `apply_patch`, `bash`, `webfetch`, `decision`, MCP tools, `search`, `gather`/`map`/`race`/`race_first`, and ~20 `rocketclaw_*` platform tools plus `ask_user_question` and `opencode_api`.
- **Permissions:** `allow`/`deny`/`auto`, last match wins, buckets `read`, `glob`, `grep`, `webfetch`, `websearch`, `edit`, `bash` (every parsed operation), `skill`, `task`, `workflow`, `mcp`, `decision`, `rocketclaw`. Per-person rules via `${ROCKETCLAW_PRINCIPAL_*}`.
- **Approval stack (4-5 layers):** decision-model pre-gate (two vendors: OpenAI Decisions API and TypeSafe) → guardian (built-in reviewer) or custom reviewer agent `auto(name)` → whole-script review `code_mode_approve` → easy `approver.model/prompt/decision` → per-reviewer `approver.decision`. The cheatsheet needs a table just to say who checks what.
- **Background jobs** (`allow_background`), Completion Notes, wake retries, `task` continue/steer/enqueue/stop.
- **Goals** (`$goal`, `maxTurns`), steering, queue, stash, interrupts, crash-resume checkpoints.
- **Compaction:** server-side `context_management`, a client chunked fallback (`looper.go:1419-1508`), and a third emulation in `oai/` for ChatGPT login (~300 lines, `oauth.go:860-1073`).
- **Model transports:** plain HTTP, WebSocket (only when a base URL is `ws://`/`wss://`), ChatGPT/Codex client. History is converted when a session switches provider (`provider_replay.go`, 400 lines). Optional Anthropic `cache_control` markers.
- **Workflows:** Starlark `workflows/*.star` with `worker`, `agent`, `parallel`, `pipeline`, `phase`. Two entry points: Web `$workflow` and the `rocketclaw_dynamic_workflow` tool.

### 3.3 Sessions, queue, history (backend)

- Conversation engine: one worker per conversation, Thread Queue, steer/enqueue/stash/pop/promote, delivery, crash recovery under a Postgres lock.
- Subagent sessions and the five `rocketclaw_sessions_*` tools, session writes with `<session_reply>` notes, `rocketclaw_sessions_recipient`.
- Revert/undo/redo (`revert.go`, 1,051 lines), fork, handoff summary, agent pause, Web questions.
- Scheduled messages (`rocketclaw_schedule_message`), stored two ways: recurring in `schedules`, one-time in `turns`.
- Hidden runs for cron and External MCP, which hand off to a Web chat through `reports_to`, `continues`, or legacy synced copies. Every history query unions all three.
- Retention: ordinary chats deleted 90 days after the last message; sticky chats keep the row.
- Attachments on filesystem or S3; inbound image resizing (~240 lines).
- Hosted Pages and Page Data (`rocketclaw_store`, `_read`, `_delete`, `_list`, `_kv_store`, `_kv_read`).
- Tags (`rocketclaw_set_tag`, `rocketclaw_get_tags`).

### 3.4 Web page

- **Routes:** `/`, `/s/<id>`, `/oc/<id>`, `/search/<id>`, `/cron`, `/cron/<stem>`, `/agents`, `/skills`, `/skills/<name>`, `/config`, `/voice`, `/pages`, `/pages/...`, `/shortcuts`, `/quick-view`, `/closed`, `/r/<server>/...`, plus side panes from `?delegation=`, `?skill=`, `?pullRequests=`, `?shadow=`.
- **Conversation:** transcript with five timeline detail levels plus an Advanced table; two renderers (Raw Slack-style and Markdown); shadow comparison columns; delegation/review panel; turn navigation rail; origin cards.
- **Composer:** `$` commands (fork, handoff, undo, redo, goal, stop, cron, workflow, agent, enqueue, stash, steer, queue, skill); Send/Queue/Steer/Stash; queue panel; background jobs; question card; pause; attachments; drafts in IndexedDB.
- **Tabs and spaces:** spaces, tab groups (9 colors), Shared tabs, Parked tabs, Top Shelf (multi-server), preview tabs, multi-select, recently closed, a three-way merge across browser windows. ~955 UI lines, ~1,260 backend lines, 9 tables.
- **Attention:** unread marks, 7 park options, per-session and global auto-park, desktop notifications, "next needing you".
- **Finding things:** command palette (4 modes), Search pages with saved searches and filter suggestions, Dashboards/Board (5 visual styles), Quick view (10 switchable layouts), cross-server search.
- **Cron page:** 36-hour timeline and per-job journal. The same actions also exist in the palette and as `$cron`.
- **Voice:** `/voice` page and composer button; WebRTC to OpenAI GPT-Live over two routes (public API `gpt-live-1` and unofficial Codex route `gpt-live-1-codex`), reconnect tones, captions, 25 voices.
- **GitHub PR panes:** two paths. Inline preview with 4 layouts calling `api.github.com` from the browser with a personal token, and a right pane that discovers PRs from recorded `gh pr create` calls or by running `gh`/`jj` through OpenCode's shell.
- **Typing indicator**, Hosted Pages viewer, Agents page, Skills (3 surfaces), Settings (14 color palettes, timeline detail, auto-park, a read-only config dump), web-update banner.
- **Telemetry:** Sentry and PostHog both capture errors and web vitals. PostHog replays record typed text and request bodies unmasked (`web/src/posthog.ts:15`); only GitHub, `/api/History` and PR-discovery requests are excluded. The README says this, so it is deliberate, but it means prompts leave the machine when PostHog is on.

### 3.5 Web API (44 RPCs)

Sessions (ListSessions, CreateSession, UpdateSession, ForkSession, Handoff, SlackNames), transcript (History, Join, StageRevert, ClearRevert), prompting and queue (Prompt, ListQueue, Steer, Pop, Remove, EditQueueItem, ReorderQueue, AnswerQuestion), background (MoveToBackground, StopBackgroundJob), attachments (Upload, Download), typing (ReportTyping, WatchTyping), voice (Voice, VoiceSettings), search (SearchSessions, SearchMessages, Board), Tabs (one RPC, 10 actions), cron (ListCronJobs, RunCronJob), pages (GetPage, ListPages), catalogs (ListAgents, ListSkills, ListWorkflows, ListConfig), meta (Protocol, Identity, Reload), raw entries (ListSessionEntries, LoadSessionEntries, DeleteSessionEntries).

- **Used by callers outside the Web page:** `Reload`, `ListSessionEntries`, `LoadSessionEntries`, `DeleteSessionEntries`. The page itself never calls them, but they are documented API (`web/README.md:1523`, `frontend/rpc/README.md:3`, `:475`) and are in use. Keep them.
- `LoadSessionEntries` returns each stored entry as raw JSON (`frontend/rpc/server.go:150-170`). It checks the caller may read the conversation and, by design, does not apply the History view's filtering.
- **Near duplicates:** `SearchMessages` vs `SearchSessions{messages:true}`; bare `$agent`/`$workflow` vs `ListAgents`/`ListWorkflows`; two in-flight search coordinators.
- **Access:** no user allowlist. Anyone Tailscale admits sees everything unless `permissions` limits them to spaces. Everyone not limited gets full control of each OpenCode server, shell included.

### 3.6 Slack

Project rule: Slack only answers @mentions from allowlisted users. What it actually does beyond that:

1. Mirrors Web messages into the Slack thread ("📡 web") (`frontend/slack/events.go:31-58`).
2. Expands forwarded messages and their files (~210 lines, `connector.go:1420-1631`).
3. Adopts unknown threads and reads 50 earlier messages.
4. Slack agent sessions: working/active/suspended status, 3-second progress polling, renewal every 50 minutes, a step checklist (`connector.go:470-613`).
5. Stop button interrupts the turn and its goal (`connector.go:1057-1092`).
6. Two-way title sync between Slack and Web.
7. Goal headers and ✅ reactions.
8. "Open in Web" footer.
9. Channel-name cache (3-minute refresh plus rename events) and user directory (8-hour reload) to show names in the Web page.
10. Legacy "report thread" detection for old cron/MCP threads.

Also: Slack is mandatory to start at all, even though the docs call it a convenience surface.

### 3.7 Cron and scheduling

Three ways to schedule: `cron/*.md` files, easy-agent `routines`, and the `rocketclaw_schedule_message` tool. Cron files also accept a one-off RFC 3339 time that deletes its own file after running, while routines reject that and point to `rocketclaw_schedule_message`. Reports open a Web chat or reuse a global sticky chat.

### 3.8 Emulated shell (sandbox)

Real Bash in bubblewrap on a pinned Ubuntu 26.04 image built with `mmdebstrap`, per-command cgroups (256 MiB, 64 processes, 1 CPU), no network except a brokered `curl`, host `commands` brokered over a protobuf wire protocol. Needs bwrap, user namespaces, delegated cgroup v2, mmdebstrap, subuid. Because the scaffolded `agents/voice.md` is `mode: easy`, every install tries to build this image.

### 3.9 Remote OpenCode

`opencode` config lists remote OpenCode servers. RocketClaw lists their sessions, proxies their API, embeds a copy of OpenCode v2.3.0's web UI (source in `oc/packages`, 26 MB; built output 87 MB in the binary), gives them an MCP bridge for tags and pages, and offers an `opencode_api` tool. Backend ~1,000 lines (`opencode.go`, `opencode_mcp.go`) plus a 5-second poll and an event stream per server, ~9 columns, a `remote` conversation kind, `relay.go`, and UI glue.

So the product holds both a home-grown OpenCode-like agent (with hand-copied parity code) and a front end for real OpenCode.

### 3.10 Remote RocketClaws

One Web page drives several RocketClaw servers: per-server polling, cross-server search, server tree in the space picker, Top Shelf, `/r/<name>/` routes, `rocketclaws` and `trusted_rocketclaws` config, CORS on the tsnet listener.

### 3.11 Models, providers, credentials

`openai` default provider plus `providers.<name>` (same struct, two code paths). `auth: api_key` or `chatgpt` (and legacy alias `rocketcode_auth`). `rocketclaw oai login` does browser PKCE or device code. Decision models under `decision` (OpenAI and TypeSafe) with separate keys. `auto_approver_model` and `auto_approver_decision` both configure the guardian. Secrets come in three ways: `--aws-secrets-manager-arn` merges a whole secret into config, inline `{"aws":{...}}` references, and easy-agent `secretsARN`.

### 3.12 Config keys

`workspace`, `database_url`, `attachments.*`, `overlays[]`, `environment[]`, `logging.level`, `server_display_name`, `web.listen_address`, `web.tls_cert_file`, `web.tls_key_file`, `web.sentry.*`, `web.tsnet.*`, `permissions`, `rocketclaws`, `trusted_rocketclaws`, `opencode`, `mcp_external.*`, `mcp_servers.*` (incl. `tools.read/write`), `slack.*`, `openai.*`, `providers.*`, `auto_approver_model`, `decision.*`, `auto_approver_decision.*`, `instrumentation.*`, `posthog.*`. Side files: `rocketclaw.users.json`, `femtoclaw.json`.

- `environment[]` is validated and cloned but never applied (`config/config.go:445`, `locked.go:55`). Dead key.

## 4. Dead code and leftovers found

Every item was investigated in depth; full findings and options are in `2026-11-14-rocketclaw-dead-code-decisions.md`. A search of this repository cannot see callers outside it, so items are split by whether anything outside the process can reach them.

**Not dead after all (corrected):**

| Item | Evidence |
| --- | --- |
| `entries.audience` | Go never writes it, but migration 049 (v0.2.0, `zvyvxzwnvvvk`) set `sandbox`/`visible` on existing rows. The filters stop old hidden-run messages showing twice. See the decisions doc, item 1 |
| `sync_source_entry_id` reads | Not written since v0.2.0, but old copied rows keep it and the reads feed undo, hiding, panels and origin. Only its index is unused. See the decisions doc, item 2 |

**Only reachable from inside the program:**

| Item | Evidence |
| --- | --- |
| `conversations` columns no Go code uses: `settled`, `reopened_at`, `snoozed_until`, `producer_effects_through_id` | `migrations/001_baseline.sql:160-168`. Every `settled` match in Go is a CTE name or a local variable |
| `CompactionSteering` is always `""`, so the branch that uses it never runs | Set at `backend/bridge.go:2810`, unset elsewhere; branch at `harness/looper.go:1149-1157` |
| Runtime diagnostics are turned on and written to `io.Discard` | `Diagnostics: true` at `backend/bridge.go:2810`; every caller passes `io.Discard` (`bridge.go:2406`, `raw_run.go:156`, `handoff.go:42`); printing at `harness/rocketcode.go:433-437,548` |
| `ExperimentalStrongerSkills` and `AutoApprovePermissions` are true in every run that has tools | `backend/bridge.go:2810`, `raw_run.go:152`; only `handoff.go:40` leaves them false, and it disables all tools |
| `read`'s `filename` and `apply_patch`'s `patch` aliases | `harness/tools.go:67,73,303,311,325,348`. The tool schemas omit them and Code Mode validates with `additionalProperties: false` (`harness/mcp_tools.go:913`, `codemode/codemode.go:255-277`), so they are rejected before they arrive |
| `harness.Looper` interface has no users | `harness/looper.go:288-291` |
| `guest_env` field of the sandbox wire protocol is never set or read in production (both ends are this binary); tests, including a hostile-guest security test, set it | `sandbox/wire/wire.proto:30`, `harness/emulated_linux_test.go:310` |
| Exported methods only tests call: `externalmcp.Server.URL`, `SessionService.ObserveTranscript`, `TranscriptPage`, `UpsertExternalMCPSession` | `frontend/externalmcp/server.go:128`, `backend/transcript.go:95,193`, `backend/store.go:315` |
| `harness/opencode.json`, a developer's local OpenCode config with `/Users/ucirello` paths | Nothing refers to it |

**Reachable from outside (dead as far as this repository shows; confirm before removing):**

| Item | Evidence |
| --- | --- |
| `environment[]` config key is validated but never applied | `config/config.go:445,805-821`, `config/locked.go:55`; advertised in `rocketclaw.example.json:8`. Config decoding is not strict, so old files that keep the key would still load |
| `task` tool's `command` parameter: the model can send it and it is ignored | In the schema at `harness/tasks.go:37`; `taskParams.Command` (`tasks.go:23`) is never read |
| `ROCKETCLAW_SLACK_API_URL` environment variable | `frontend/slack/connector.go:152`; only tests set it here, but a deployment could |

Not dead, so left out of this list: the four Web API methods with no caller in the page (used by outside callers), `harness/scripts/sync-opencode-testdata.sh` (run by `make sync-opencode-testdata`), `looper.sh` (a dev tool), `mcpclient.ServerConfig` (a duplicate of `config.MCPServerConfig`, see section 6), compatibility shims (section 5).

Other problems found along the way, not dead code: per-conversation workers are never cleaned up (`backend/thread_bridges.go:107,763`); stale text in `cmd/rocketclaw/main.go:1`, `config/config.go:118`, `skel` `main.md:45` (names a skill that does not exist) and `HEARTBEAT.example.md` (BSD `date -v-1d`); `Makefile:88` builds `../../bin/rocketclaw` while `Procfile:2` runs `./bin/rocketclaw` (not run).

## 5. Compatibility shims (each is a decision to stop supporting old data or configs)

- `femtoclaw.json` / `.femtoclaw` config fallback (`cmd/rocketclaw/main.go:19-20`, `skel.go:460`).
- `rocketcode_auth` alias (`config/config.go:251-252`, `cmd/rocketclaw/oai.go:213-218`), old single-token `auth.json` (`oai/oauth.go:158-174`).
- `reasoningEffort` field, old per-tool session keys that load but grant nothing, agentlint RC006 (plural `permissions`).
- `slack_channel` ignored on External MCP; Slack "report thread" detection.
- Legacy delegation rows before 2026-10-22, old Execute rows, old checkpoint input IDs, legacy URI-encoded tab paths, empty `goal_status`, pre-baseline store adoption, legacy search-tab migration in the UI.
- Release notes embedded in `README.md` (four blocks).

## 6. Parallel paths that could become one

| Two or more ways to do one thing | Where |
| --- | --- |
| Agent formats: easy and advanced | `harness/easy.go` + branches |
| Approval: guardian, custom reviewer, two decision vendors, easy approver, per-reviewer approver | `harness/permission_review.go`, `decision.go`, `guardian.go` |
| Compaction: server, client fallback, oai emulation | `harness/looper.go:1419-1573`, `oai/oauth.go:860-1073` |
| Scheduling: cron files, routines, schedule_message; one-off cron files | `frontend/cron`, `config/cron.go:77-92` |
| Scheduled-message storage: `schedules` vs `turns` | `backend/store_dao.go:1130-1173` |
| Hidden-run handoff: `reports_to`, `continues`, synced copies | `backend/conversations.go:102`, history SQL |
| Live updates: in-process bus and Postgres NOTIFY triggers | `backend/runtime.go:130`, 11 triggers |
| Telemetry: Sentry, PostHog, OpenTelemetry | `config/config.go`, `web/src/sentry.ts`, `posthog.ts` |
| Web API dispatch: method list in 3 places + gRPC hop | `frontend/rpc/transport.go:26,173`, `http.go:53,451`, `pages.go:15` |
| Search: two RPCs, two in-flight coordinators | `frontend/rpc/session_commands.go:148-391`, `search_runs.go` |
| Agent choices computed in two pipelines | `frontend/rpc/server.go:1349-1496` |
| Voice: public API and Codex routes | `frontend/rpc/voice_*.go` (~300 lines + 10-method interface) |
| GitHub PRs: inline preview and right pane | `web/src/github-pr*.tsx`, `thread-pr*.ts`, `opencode-pr.ts` |
| Overview screens: Quick view, Board, Search, palette | `web/src/ui.tsx:1577-1689`, `3512-3553` |
| Default provider vs named providers | `config/config.go:39-40,200-203` |
| Harness config built by hand three times | `backend/bridge.go:2810`, `raw_run.go:152`, `handoff.go:40` |
| Code Mode tool paths: MCP vs host tools | `codemode/codemode.go:20-115`, `harness/mcp_tools.go:295-348` |

## 7. Ranked cut list

Ranked by size removed and concepts removed, with risk noted. "Product call" means only the owner can say whether anyone uses it.

| # | Cut | Rough size | Risk / note |
| --- | --- | --- | --- |
| 1 | **Remote OpenCode integration** (proxy, embedded UI, sync, MCP bridge, `opencode_api`, `remote` kind) | 26 MB source + 87 MB binary assets, ~1,500 Go lines, UI glue, tests | Product call. Biggest single deletion. Also removes the "full shell control for everyone admitted" exposure |
| 2 | **Shadow agents** | ~1,250 production lines, ~3,700 test lines, 2 tables, 6 triggers | Product call. Hooks run through the main turn path |
| 3 | **Slack down to mentions only** (items 1-8 in 3.6, maybe 9) | ~600-700 of 1,448 lines | The project rule already says so; the README documents these features, so it is shipped behavior. Also make Slack optional |
| 4 | **One agent format** (drop easy or fold it into advanced) | `easy.go` + `easy_files.go` + branches; halves the agent docs | Product call. Easy agents carry the sandbox, routines, secrets |
| 5 | **One approval path** (e.g. guardian + one decision vendor) | Large part of `permission_review.go`, `decision.go`, approver rules | Product call |
| 6 | **Web tab system trim** (Top Shelf, Closed tabs, auto-park, preview tabs, cross-window merge, 5 board styles, Quick view, 14 palettes) | ~1,000+ UI lines, ~1,260 backend lines, several tables, ~300 CSS lines per cut style | Each piece is separable. Quick view and board styles are the easiest |
| 7 | **Remote RocketClaws** | Branches across UI routing, polling, search; config; CORS | Product call |
| 8 | **Dead storage paths** (four unused `conversations` columns, the unused `sync_source` index; `audience` and synced-copy reads only after a data migration) | One small migration; the rest needs a data check and a rehearsed migration | `audience` and synced copies are still load-bearing for pre-v0.2.0 data (decisions doc, items 1-2) |
| 9 | **Dead and test-only code** (section 4) and **compat shims** (section 5) | Hundreds of lines; README release notes | Low risk |
| 10 | **Drop the in-process gRPC hop**, single method table | Hand-built service desc, metadata identity, 3 method lists | Biggest internal win in `frontend/rpc`; tests are built on gRPC |
| 11 | **One telemetry stack** and stop unmasked PostHog replays | `sentry.ts`, PostHog mirrors, config | Product call |
| 12 | **One scheduling model** (drop one-off cron files at least) | `config/cron.go:77-92`, `manager.go:289-351` | Low |
| 13 | **Voice: one route**, or voice entirely | ~300 lines for one route; ~1,800 Go+TS for all | Product call |
| 14 | **GitHub PR: one path** | ~590 UI lines, ~690 test lines | Low |
| 15 | **Merge `openai` into `providers`**, drop WebSocket transport, drop client compaction fallback | Config + harness branches | Check ChatGPT compaction first |

## 8. Product questions to answer first

1. Does anyone use remote OpenCode servers, Remote RocketClaws, shadows, voice, Hosted Pages, or Dashboards? These decide the biggest cuts.
2. Should Slack stay mandatory? If it is a convenience surface, it should be optional.
3. Keep both agent formats? If easy agents are the future, can advanced be the escape hatch only, or the reverse?
4. Should External MCP refuse to start without a users file instead of running with no auth?
5. Is unmasked PostHog session replay intended for every deployment?
