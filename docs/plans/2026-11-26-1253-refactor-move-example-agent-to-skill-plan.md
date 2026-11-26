---
title: Move the Example Agent Next to the Agent-Writing Skill - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Move the Example Agent Next to the Agent-Writing Skill - Plan

## Goal Capsule

- **Objective:** A new workspace gets no agent files from RocketClaw, and the main agent still finds the tag-groups example agent whenever it writes or updates an agent.
- **Means:** Move `session-tags.example.md` from the embedded `agents/` tree into the embedded `main-create-or-update-agent` skill folder and delete the `agents` embed with everything that only served it (KTD1, KTD2).
- **Authority:** Settled decisions (Key Decisions below) > this plan's KTDs > unit text.
- **Execution profile:** One PR, three units, Lightweight.
- **Stop conditions:** Stop and report if the example file would not reach the runtime skills folder, or if an existing workspace would need cleanup code to keep working.
- **Who finishes:** The implementing agent opens the PR; merging stays with the owner.

## Product Contract

### Summary

Ship the session-tags example agent inside the agent-writing skill instead of copying it into every workspace's `agents/examples/` and the runtime `agents/examples/`. The skill's SKILL.md points to the file, and the skill's file listing shows it when the skill loads.

### Problem Frame

RocketClaw unpacks `skel/agents/examples/session-tags.example.md` into every workspace's `agents/examples/` and into `<runtime>/agents/examples/`. The agent loader reads only top-level `agents/*.md`, so the file is never loaded as an agent; it only clutters the workspace and contradicts the README's statement that RocketClaw copies no agent files into the workspace. The only reader who benefits from the example is the main agent while it writes agents, and that agent already loads `main-create-or-update-agent`.

### Requirements

**Placement**
- R1. The example lives at `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/session-tags.example.md` with unchanged content.
- R2. The skill's SKILL.md gains one short line, beside its `permission.rocketclaw.rocketclaw_set_tag` tag-groups text, naming the example by its path relative to the skill.
- R3. The README link to the example points to the new path.

**Removal**
- R4. RocketClaw no longer writes anything into a workspace's `agents/` or the runtime `agents/` from embedded files.
- R5. Code made dead by R4 is deleted.
- R6. Existing workspaces that already hold `agents/examples/session-tags.example.md` are left alone; no cleanup code is added.

### Key Decisions

- **Move the example beside the agent-writing skill.** (session-settled: user-approved — chosen over keeping it copied into every workspace's `agents/examples/`: the loader never reads it there, and the main agent sees it through the skill.) Governs R1, R2, R4.
- **Leave old copies in existing workspaces.** (session-settled: user-approved — chosen over adding cleanup code: the stale file is harmless and cleanup code would outlive its use.) Governs R6.

### Scope Boundaries

- Not changing the example's content or format.
- Not removing the `*example.md` skip in `workspaceOverlayFilter` (`skel.go`): old workspaces still hold the file and users may write their own example files, so the skip is still live.
- Not touching how overlays or workspace `skills/` are applied.

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete the `agents` embed and both sync calls.** Drop `all:agents` from the `//go:embed` line, the workspace unpack in `SyncInWithOverlays`, and the runtime sync in `SyncEffectiveRuntimeAssets`. The embedded `.rocketclaw` tree already syncs every file under `skills/` into `<runtime>/skills/`, so the moved file needs no new code. (session-settled: user-approved — chosen over keeping the embed with an empty tree: nothing would use it.) Governs R4.
- KTD2. **Delete `syncFSFiltered`'s `overwrite` parameter.** The workspace unpack was its only `false` caller, so every remaining call passes `true`; the `!overwrite` stat-and-skip block goes with it. Governs R5.
- KTD3. **Delete the `.sh` mode branch and the `preserveExecutable` parameter; always copy the source executable bit.** No embedded file ends in `.sh`, so the branch is unreachable. With it gone, `preserveExecutable=false` always yields `0o644`, and `true` yields the same for embedded files because `embed.FS` reports mode `0o444`, so one behavior serves every caller. Governs R5.
- KTD4. **Name the file `session-tags.example.md`.** Keeping the name keeps it recognizable to anyone who saw the old path, and the `.example.md` suffix marks it as not loadable.

### Assumptions

- The skill file listing (`harness/skills.go` `skillFiles`) shows files beside SKILL.md to the agent when the skill loads, so the main agent sees the example without reading SKILL.md's pointer.
- `docs/solutions/best-practices/backend-run-tests-keep-fixture-agents-in-workspace-agents.md` describes the removed unpack step as step 1 of startup; updating it is part of R5's "docs" cleanup rather than new scope.

## Implementation Units

### U1. Move the example and point to it

- **Goal:** The example ships inside the skill, and SKILL.md and README name the new path.
- **Requirements:** R1, R2, R3 (KTD4).
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/skel/agents/examples/session-tags.example.md` → `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/session-tags.example.md` (move)
  - `internal/rocketclaw/skel/.rocketclaw/skills/main-create-or-update-agent/SKILL.md`
  - `README.md`
- **Approach:**
  1. Move the file unchanged.
  2. Add one sentence after the sudo paragraph that mentions `permission.rocketclaw.rocketclaw_set_tag` tag groups, pointing to `session-tags.example.md` in this skill's folder.
  3. Repoint the README link under `rocketclaw_set_tag`.
- **Test scenarios:** covered in U2 (the runtime sync test asserts the file lands in `<runtime>/skills/main-create-or-update-agent/`) and U3 (the backend loader test reads the new path).
- **Verification:** The old `skel/agents` directory no longer exists; README's link resolves.

### U2. Delete the agents embed and its dead code

- **Goal:** No embedded file is written into `agents/`, and `syncFSFiltered` loses the parameters and branch that only served it.
- **Requirements:** R4, R5, R6 (KTD1, KTD2, KTD3).
- **Dependencies:** U1.
- **Files:**
  - `internal/rocketclaw/skel/skel.go`
  - `internal/rocketclaw/skel/skel_test.go`
- **Approach:**
  1. Remove `all:agents` from the embed directive and the two `agentsRoot` sync calls.
  2. Remove `overwrite` and `preserveExecutable` from `syncFSFiltered`, the `!overwrite` block, and the `.sh` branch; always read the source mode.
  3. Update every caller, including tests.
  4. In `TestSyncCreatesEmbeddedRuntimeSupport`, drop the loop that expects `examples` in both `agents` folders and assert the moved file exists under `<runtime>/skills/main-create-or-update-agent/`.
  5. In `TestSyncIgnoresBuiltinAgentFiles`, expect only `bot.md` in the runtime `agents/`.
  6. In `TestValidateRuntimeAssetsWithStagesTheFile`, stop reading and checking the example; keep the `bot.md` check.
- **Patterns to follow:** existing `fstest.MapFS` tests of `syncFSFiltered`.
- **Test scenarios:**
  - Syncing a fresh workspace creates `<runtime>/skills/main-create-or-update-agent/session-tags.example.md`.
  - Syncing with an overlay that ships `bot.md` leaves exactly `bot.md` in `<runtime>/agents/`.
  - `TestSyncFSFilteredPreservesSourceExecutableBit` still passes: an executable source file becomes `0o755` and a non-executable `.sh` stays `0o644`.
- **Verification:** `skel` package tests pass; no remaining reference to `overwrite` or `preserveExecutable`.

### U3. Update the backend test and the solution doc

- **Goal:** The loader test still proves the example is a valid agent, and the startup description no longer lists the removed step.
- **Requirements:** R3, R5.
- **Dependencies:** U1.
- **Files:**
  - `internal/rocketclaw/backend/definitions_test.go`
  - `docs/solutions/best-practices/backend-run-tests-keep-fixture-agents-in-workspace-agents.md`
- **Approach:**
  1. Point the `example` case in `definitions_test.go` to `../skel/.rocketclaw/skills/main-create-or-update-agent/session-tags.example.md`.
  2. In the solution doc, drop the unpack step, renumber, and fix the sentence that relied on it.
- **Test scenarios:**
  - The `example` case loads the moved file as agent `primary` with no warnings and with tag tools allowed.
- **Verification:** `backend` package tests pass.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS=./skel,./backend` |
| Size | `make -C internal/rocketclaw check-cloc-budget`; report `make -C internal/rocketclaw cloc` before and after |

## Definition of Done

- U1–U3 complete and every gate above passes.
- No reference to `skel/agents/` remains in code, tests, README, or the solution doc.
- Source CLOC does not grow.
- No abandoned-attempt code remains in the diff.
