---
title: "Quickbench BAR - Plan"
date: 2026-08-11
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-brainstorm
execution: code
---

# Quickbench BAR - Plan

## Goal Capsule

- **Objective:** Ship quickbench as one CLI/product surface around BAR: pack/unpack, dump, Slack/session capture, variation×model runs, ELO-only ranking.
- **Product authority:** Product Contract below is source of WHAT. Planning Contract is HOW. Principal is day-one user. YAML is not first-class.
- **Execution profile:** Greenfield rewrite of `internal/quickbench` is allowed; treat current YAML runner as disposable.
- **Stop conditions:** AE1–AE5 pass with unit tests; one sample BAR runs end-to-end with mocked judge in tests; live-model run is optional manual smoke.
- **Tail ownership:** Implementer owns docs/README/examples/skill migration with the code units.
- **Open blockers:** None.

---

## Product Contract

### Summary

Replace unfinished YAML quickbench with BAR: a txtar-based `.bar` (or equivalent unpacked directory) that holds turns, tool mocks, separate variation prompts, and an ELO scorer. One quickbench surface packs, unpacks, dumps, captures from Slack/session state, runs the matrix, and prints ELO rankings.

### Problem Frame

Quickbench exists but does not close the loop. The principal cannot systematically re-run real RocketClaw failures or compare models/prompts. Hand YAML and ad hoc re-chats do not produce a durable, shareable, re-runnable bench unit.

### Key Decisions

- KD1. **One quickbench surface** — pack/unpack/dump/run/ELO/capture share one product identity. (session-settled: user-approved — chosen over archive-first or capture-first) Governs R1, R12–R15.
- KD2. **BAR replaces YAML** — `.bar`/unpacked dir is the only benchmark unit. (session-settled: user-directed) Governs R2, R16.
- KD3. **Dir ≡ `.bar`** — packed archive and unpacked tree are fully equivalent. (session-settled: user-directed) Governs R3, R4.
- KD4. **Variations are separate prompt files** — not template parameter sets in v1. (session-settled: user-directed) Governs R7.
- KD5. **Templates/dictionary deferred** — no text/template or prompt dictionary in v1. (session-settled: user-directed) Governs Scope.
- KD6. **ELO-only scoring in v1** — ELO scorer = criteria prompt + judge model+variant. No script/scalar primary scores. (session-settled: user-directed) Governs R9–R11.
- KD7. **Visualizer is CLI dump** — not a rich UI. (session-settled: user-directed) Governs R5.
- KD8. **Capture is full-fidelity re-run** within session_entries limits. (session-settled: user-directed) Governs R13–R14.
- KD9. **Principal-first** — multi-operator later. (session-settled: user-directed) Governs R14, Scope.
- KD10. **Current quickbench implementation is disposable** — no requirement to preserve YAML types, assertion path, or multi-file dir scan shape. (session-settled: user-directed — chosen over incremental migrate)

### Actors

- A1. **Principal** — authors, packs, runs, dumps, and ranks BARs.
- A2. **Quickbench CLI** — pack/unpack, dump, run matrix, ELO rank, capture helper.
- A3. **Capture skill/subagent** — RocketClaw-compatible; Slack thread + state → BAR.
- A4. **Subject models** — selected at run time via CLI.
- A5. **Judge model+variant** — from BAR ELO scorer (optional CLI override).

### Requirements

**Archive unit**

- R1. Quickbench is the single product surface for BAR lifecycle: pack, unpack, dump, capture-assisted authoring, run, and ELO rank.
- R2. The only first-class benchmark input is a BAR: a `.bar` file or an unpacked directory with the same member layout.
- R3. A `.bar` file is a txtar archive (`golang.org/x/tools/txtar` semantics): named text file members that together define one re-runnable benchmark.
- R4. Pack and unpack behave like `tar cv` / `tar xv`: directory ↔ `.bar` without loss of members needed to run.
- R5. Dump lists and shows BAR contents in the CLI in human-readable form (the v1 “visualizer”).
- R6. A BAR includes turn definitions and tool-call mock definitions sufficient to drive a RocketCode-backed run.
- R7. A BAR may include multiple variations as wholly separate prompt/transcript members (not template-parameter maps in v1).
- R8. A BAR includes exactly one ELO scorer definition: a crisp criteria prompt plus a judge model+variant.

**Run and rank**

- R9. Running a BAR executes every combination of variation × subject model selected for the run.
- R10. After runs complete, quickbench ranks outputs with pairwise ELO using only the BAR’s ELO scorer (criteria prompt + judge model+variant). Pairwise comparisons decide better/worse solely from that criteria prompt.
- R11. V1 primary result output is the ELO ranking (and enough pairwise/trace detail to understand placements). Script scorers, structured scalar LLM-judge scores, and pass/fail assertion tables are not v1 primary scoring.

**Capture**

- R12. A RocketClaw-compatible skill and subagent accept a Slack thread reference and produce a BAR.
- R13. Capture reads session payload from RocketClaw state (e.g. `state.sqlite3` session entries) and emits a BAR that is reconstructible enough to re-run the same harness setup.
- R14. Capture fidelity is intentional for principal use: prefer completeness for re-run over aggressive redaction in v1.

**Compatibility**

- R15. Subject models remain selected at run time (CLI), not as the sole identity of the BAR; the BAR defines behavior and the ELO judge.
- R16. Existing YAML benchmark input path is removed; examples and the YAML authoring skill are replaced with BAR equivalents.

### Key Flows

- F1. Pack / unpack — CLI directory ↔ `.bar`; round-trip preserves runnability. Covers R3, R4.
- F2. Dump — CLI prints members for inspection. Covers R5.
- F3. Capture from Slack — skill/subagent + optional CLI builds BAR from session state. Covers R12–R14.
- F4. Run matrix + ELO — load BAR; run every variation×subject model; pairwise ELO; print ladder. Covers R6–R11, R15.

### Acceptance Examples

- AE1. Round-trip archive — pack then unpack yields same runnable variation set and ELO scorer. Covers R3, R4.
- AE2. Dump is enough to inspect — shows turns, mocks, variations, ELO scorer without hand-unpack. Covers R5.
- AE3. Matrix then ELO only — two variations × two models → four outputs + ELO ladder, no scalar primary table. Covers R9–R11.
- AE4. Capture re-runs — session with tool calls → BAR → run uses reconstructed turns/mocks. Covers R12–R14.
- AE5. YAML is gone — bare `.yaml` is not accepted as a BAR unit. Covers R2, R16.

### Success Criteria

- Principal: Slack thread → BAR → run → ELO ranking without YAML.
- `.bar` shareable via pack/unpack/dump.
- V1 scoring is only ELO (criteria prompt + judge model+variant).

### Scope Boundaries

**Deferred for later:** text/template + dictionary; script/scalar scorers; rich UI; multi-operator redaction; non-static tool backends beyond capture mocks.

**Outside identity:** separate archive brand; permanent YAML dual path; general eval platform unrelated to RocketCode/RocketClaw.

### Dependencies / Assumptions

- RocketCode remains subject-run harness.
- Capture is bounded by portable `SessionEntry` / ReplayInput content (no tool JSON Schema or agent markdown in sqlite alone).
- txtar text members only; binary payloads need text encoding if ever required.
- Principal-only: BARs may hold sensitive session material.

### Outstanding Questions

None blocking. Planning resolved former deferred items in KTDs below.

### Sources / Research

- Product origin: this file (ce-brainstorm → ce-plan enrichment).
- Disposable prior surface: `cmd/quickbench/`, `internal/quickbench/` (YAML scan, static tools, assertion report).
- Session APIs: `internal/rocketclaw/harnessbridge/store.go` (`ObserveSessionEntries`, `ListSessionsInOptions`, `SessionDBPathIn`); CLI mirror `cmd/rocketclaw/fc.go`.
- RocketCode replay: `internal/rocketcode` SessionEntry / ReplayInput.
- txtar: https://pkg.go.dev/golang.org/x/tools/txtar
- Scratch: `.tmp/rocketclaw/ce-brainstorm/quickbench-bar/grounding.md`, `.tmp/rocketclaw/ce-plan/quickbench-bar/repo-research.md`

**Product Contract preservation:** restructured, no scope change: KD10 added from session (disposable rewrite); former Outstanding Questions resolved into Planning Contract KTDs; R9 wording “variation × prompt path × subject model” clarified as variation × subject model (prompt path = variation members).

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Greenfield rewrite of `internal/quickbench`** — delete YAML loader, multi-YAML dir scan, and assertion-primary report path. Keep thin `cmd/quickbench/main.go` and provider config idea (`quickbench.json` + env interpolate). (session-settled: user-directed — disposable prior impl) Governs U1–U6.
- KTD2. **CLI is subcommand-shaped** — `quickbench pack|unpack|dump|run|capture` under one binary. Default/help documents the BAR loop. Governs R1, F1–F4.
- KTD3. **Canonical BAR member layout** (txtar paths; same on disk for unpacked dirs):

  ```text
  meta.txt                 # name, description, optional tags (simple key: value lines)
  variations/<id>/system.txt   # optional; empty/missing = no system
  variations/<id>/transcript.json  # ordered prior turns + final user (see KTD4)
  mocks/tools.json         # static tool defs: name, description, parameters, response
  elo/criteria.txt         # judge criteria prompt (required)
  elo/judge.txt            # model selector string, same grammar as --model (required)
  ```

  Variation id = directory name under `variations/`. At least one variation required. Exactly one ELO scorer pair. Governs R3, R6–R8.
- KTD4. **`transcript.json` shape** — JSON array of `{role, text}` with roles `user`|`assistant` only for history; optional leading system is only from `system.txt`. Final message must be `user`. Capture writes portable message text extracted from ReplayInput; tool rounds become assistant/tool mock seeds, not free-form multi-role soup beyond what the runner can replay. Governs R6, R13.
- KTD5. **Tool mocks** — JSON list of static tools: `name`, `description`, `parameters` (JSON Schema object; capture may use permissive `{type:object}`), `response` (string body returned on every call). Runner exposes them as RocketCode custom tools and records calls for optional dump in results. No assertion matching in v1. Governs R6, R11.
- KTD6. **Load path** — `openBAR(path)`: if path is `.bar` file, `txtar.Parse` into memory FS; if directory, read members by layout. Same validation either way. Reject bare `.yaml`/`.yml`. Governs R2, R3, AE5.
- KTD7. **Pack/unpack** — pack walks directory (only BAR members; ignore junk with clear rules: only known roots `meta.txt`, `variations/`, `mocks/`, `elo/`), writes txtar `.bar`. Unpack extracts all archive files to target dir. Round-trip byte-stable on member set. Governs R4, AE1.
- KTD8. **Dump** — print member paths then contents (or `--names` list-only). No GUI. Governs R5, AE2.
- KTD9. **Run matrix** — cells = each variation id × each `--model` subject. One RocketCode Loop per cell (reuse prior wiring idea: temp workspace under process temp or `.tmp` when cwd is a repo; empty skills; custom tools from mocks; system + transcript → replay + final user). Collect final assistant text (+ tool call log + latency) per cell. No pass/fail. Governs R9, R15.
- KTD10. **ELO protocol** — after all cells finish: players = cells (label `variation@model`). Round-robin all unordered pairs once. Each pair: one judge call with `elo/criteria.txt` + both outputs (order randomized per pair; judge must pick A/B/tie in a strict parseable form, e.g. first line `WINNER: A|B|TIE`). Update ratings (start 1000, K=32, tie = 0.5). Print ladder sorted by rating and list pairwise results. Optional `--judge` overrides `elo/judge.txt`. Governs R10, R11, AE3.
- KTD11. **Judge invocation** — separate RocketCode (or minimal provider) completion using judge model selector from BAR; no tools; criteria + two labeled outputs only. Fakeable in tests via injected judge function. Governs R8, R10.
- KTD12. **Capture library in `internal/quickbench`** — `Capture(ctx, opts)` reads `ObserveSessionEntries` on resolved `state.sqlite3`, maps conversation id (`slack-thread:channel:ts` or raw id), builds one variation `captured` (or `--name`), extracts messages/tool I/O into transcript + mocks, writes default stub `elo/criteria.txt` and `elo/judge.txt` the principal must edit before meaningful rank. CLI `quickbench capture` wraps the library; skill shells to it or calls same instructions. Governs R12–R14, F3.
- KTD13. **Capture fidelity contract** — best effort from portable session entries: message text, function_call name/args, function_call_output bodies → static mocks keyed by tool name (last output wins, or per-call queue if multiple). Does not require agent markdown or original tool schemas from disk. Document gap: live builtins/MCP not re-hosted; mocks stand in. AE4 means “runs without missing-member errors and exercises captured mocks,” not bit-identical to live RocketClaw. Governs R13, AE4.
- KTD14. **YAML removal** — delete `cmd/quickbench/examples/*.yaml`, rewrite `cmd/quickbench/skills/quickbench-benchmarks` into BAR authoring + capture skill(s), rewrite README. No dual loader. Governs R16.
- KTD15. **Dependency** — add direct `golang.org/x/tools/txtar` require. Governs R3.
- KTD16. **Providers** — keep cwd `quickbench.json` env-interpolated provider load for subject and judge models (OpenAI path as today unless multi-provider already trivial to keep). Governs R15.

### High-Level Technical Design

```text
                    ┌─────────────────┐
  Slack thread ───► │ capture         │──► BAR dir or .bar
  state.sqlite3     │ (lib + CLI +    │
                    │  skill/agent)   │
                    └────────┬────────┘
                             │
         pack/unpack/dump ◄──┼──► principal edits elo/* / variations/*
                             │
                    ┌────────▼────────┐
  --model… ───────► │ run             │──► cell outputs
                    │ variation×model │
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │ elo rank        │──► ladder + pairs
                    │ (judge model)   │
                    └─────────────────┘
```

Single package `internal/quickbench` owns format, I/O, run, elo, capture. `cmd/quickbench` stays a few lines.

### Assumptions

- Session entry ReplayInput is enough to seed one-shot re-runs for principal debugging.
- Round-robin ELO cost is acceptable for principal-scale matrices (small n).
- Stub ELO files on capture are OK if dump/run refuse rank without non-empty criteria/judge (or run warns and skips rank).

### Implementation Constraints

- Follow AGENTS.md Go standards on all new code; no defensive nil ctx; temp under `.tmp` when creating repo-local scratch.
- Do not expand RocketClaw store schema for v1 capture.
- No `//nolint` without approval.
- CLOC: prefer deleting old quickbench lines so net growth stays honest.

### Sequencing

1. U1 format + pack/unpack/dump + tests  
2. U2 run matrix (no ELO) + sample BAR  
3. U3 ELO rank + injected judge tests  
4. U4 capture library + CLI  
5. U5 skill + subagent docs  
6. U6 delete YAML, README, root README blurb  

U2 can start after U1 load API exists. U3 after U2 cell results type exists. U4 independent of U3 after U1 write API. U5 after U4. U6 last or folded into each unit’s cleanup.

---

## Implementation Units

### U1. BAR format, pack, unpack, dump

- **Goal:** Load/validate BAR from `.bar` or directory; pack/unpack round-trip; dump for humans.
- **Requirements:** R1–R5, R8 (presence of elo members), AE1, AE2, AE5
- **Files:** `internal/quickbench/` (new package files; remove YAML types), `cmd/quickbench/main.go`, `go.mod`/`go.sum`, `internal/quickbench/*_test.go`
- **Approach:** Implement member layout (KTD3). `openBAR`, `Pack`, `Unpack`, `Dump`. Subcommands `pack|unpack|dump`. Add txtar dependency. Reject yaml paths.
- **Test scenarios:**
  - Pack dir → unpack → identical required members
  - openBAR on `.bar` and dir both validate
  - Missing `elo/criteria.txt` or zero variations fails validate
  - Dump includes variation ids and elo judge line
  - `.yaml` path rejected
- **Verification:** `go test ./internal/quickbench/`; manual dump on fixture
- **Dependencies:** none

### U2. Run matrix (subject models)

- **Goal:** Execute every variation × `--model` cell via RocketCode; collect outputs; no assertion scoring.
- **Requirements:** R6, R7, R9, R15
- **Files:** `internal/quickbench/` run/execute, `cmd/quickbench` `run` subcommand, sample under `cmd/quickbench/examples/`
- **Approach:** Parse subject models with existing selector grammar. Per cell: build tools from mocks, system + transcript → RocketCode Loop, record text/tools/latency. Human + `--json` report of cells (not pass rates).
- **Test scenarios:**
  - Load fixture BAR with 2 variations; with fake/stub runtime if feasible, or unit-test transcript→replay mapping without live API
  - Matrix dimension = len(variations)*len(models)
  - Timeout flag applies per cell
- **Verification:** `go test ./internal/quickbench/`; optional live smoke documented in README
- **Dependencies:** U1

### U3. ELO ranking

- **Goal:** Pairwise ELO over cells using BAR criteria + judge model.
- **Requirements:** R8, R10, R11, AE3
- **Files:** `internal/quickbench/` elo, run wiring, tests with fake judge
- **Approach:** KTD10–KTD11. Inject `judgePair(ctx, criteria, a, b) (result, error)` in tests. CLI prints ladder + pairs; JSON includes ratings and pair list. `--judge` override.
- **Test scenarios:**
  - Three cells, deterministic fake judge → stable ordering
  - Tie handling updates both ratings equally
  - Missing criteria fails before judge calls
  - Output has no passRate-as-primary field requirement
- **Verification:** `go test ./internal/quickbench/`
- **Dependencies:** U2

### U4. Capture library + CLI

- **Goal:** Build BAR from RocketClaw `state.sqlite3` + conversation/thread id.
- **Requirements:** R12–R14, AE4 (library path)
- **Files:** `internal/quickbench/capture.go`, tests with temp sqlite fixtures via harnessbridge helpers if available, `quickbench capture` subcommand
- **Approach:** Resolve db path (flag or workspace + `.rocketclaw`). `ObserveSessionEntries` from 0. Map entries → transcript + mocks (KTD12–KTD13). Write dir or pack `.bar`. Stub elo files with TODO criteria.
- **Test scenarios:**
  - Synthetic session with user/assistant/tool output → BAR validates and contains mock
  - Unknown conversation id errors clearly
  - Written BAR openBAR succeeds
- **Verification:** `go test ./internal/quickbench/`
- **Dependencies:** U1; harnessbridge read APIs

### U5. RocketClaw skill + subagent

- **Goal:** Principal can paste a Slack thread and get a BAR via skill/subagent instructions calling capture.
- **Requirements:** R12, F3
- **Files:** `cmd/quickbench/skills/` (replace YAML skill), optional `agents/` doc under quickbench or skel overlay instructions in skill body; `cmd/quickbench/README.md` section
- **Approach:** Skill describes: resolve thread → conversation id → `go run ./cmd/quickbench capture …` (or installed binary) → edit elo → run. Subagent markdown mirrors skill with narrower task focus. Follow existing RocketClaw skill overlay patterns (do not edit embedded skel blindly; ship under cmd/quickbench and document copy path).
- **Test scenarios:**
  - Skill file exists with name/description frontmatter
  - README documents the capture→edit elo→run loop
- **Verification:** file presence + manual skill load if environment allows
- **Dependencies:** U4

### U6. YAML removal and docs

- **Goal:** No first-class YAML path; docs and root README point at BAR.
- **Requirements:** R16, AE5
- **Files:** delete YAML examples; `cmd/quickbench/README.md`; root `README.md` quickbench blurb; any references in skills
- **Approach:** Replace example with one minimal BAR directory (and optional packed `.bar`). Grep repo for quickbench YAML mentions outside vendor.
- **Test scenarios:**
  - No tests reference `.yaml` benches
  - Help text mentions subcommands and `.bar`
- **Verification:** `go test ./internal/quickbench/`; `rg` clean for old usage in cmd/internal quickbench
- **Dependencies:** U1–U5 content frozen enough to document

---

## Verification Contract

| Command | When |
|---------|------|
| `go test ./internal/quickbench/` | Every unit |
| `gofmt` on touched Go files | Before finalize |
| `make lint` | Before finalize |
| `make test` | Before finalize (repo gate) |
| Manual: `quickbench pack/unpack/dump/run` on example BAR | After U2–U3 |
| Manual: capture from a real or fixture sqlite | After U4 |

No release:validate required unless implementer touches release paths (should not).

---

## Definition of Done

**Global**

- [ ] Product AEs AE1–AE5 covered by tests or documented manual proof
- [ ] YAML input path gone; sample BAR present
- [ ] ELO-only primary output; no assertion pass-rate as product score
- [ ] Skill + subagent docs for Slack → BAR
- [ ] `make lint` and `make test` pass
- [ ] Abandoned greenfield drafts removed from tree
- [ ] README impact considered and updated (`cmd/quickbench/README.md`, root blurb)

**Per unit:** U1–U6 verification bullets satisfied; unit tests listed above green.

---

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Capture cannot restore full live agent (skills/MCP/schemas) | KTD13 fidelity contract; static mocks + transcript |
| ELO judge flaky / unparseable | Strict one-line winner parse; retries once; fail pair visible in report |
| Matrix × pairs expensive | Principal-scale; document cost; no fancy sampling in v1 |
| CLOC budget | Delete old quickbench code aggressively |
| txtar not direct dep today | KTD15 add require |

---

## System-Wide Impact

- Confined to quickbench + docs/skills; optional read-only use of harnessbridge store APIs.
- Does not change RocketClaw runtime behavior for live threads.
- CONCEPTS.md already defines BAR / ELO Scorer / Quickbench.
