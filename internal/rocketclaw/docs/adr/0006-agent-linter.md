# 0006. Agent Linter

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw provides `rocketclaw lint [next|current]` as a whole-system safety linter for effective RocketCode agents, skills, scripts, and task delegation. `rocketclaw lint` defaults to `rocketclaw lint next`.

## Scope

This ADR governs the user-visible linter command, lint target semantics, finding codes, suppressions, and setup-skill guidance for agent-system safety. It does not require startup to run lint automatically.

## Context

RocketCode agent safety depends on the whole effective agent system. A single agent cannot be fully evaluated in isolation because permissions, scripts, skills, and task delegation can combine across agents into loops, write-to-execute escalation, or external-content contamination.

RocketClaw already relies on embedded create/update skills to guide agents toward safe configuration edits. The linter is a second line of defense that checks the complete effective system before or after restart.

## Normative Contracts

### Command Contract

- `rocketclaw lint` is equivalent to `rocketclaw lint next`.
- `rocketclaw lint next` checks the effective runtime assets that would be materialized after a RocketClaw restart using the selected config.
- `rocketclaw lint current` checks the currently materialized selected runtime directory.
- Unknown lint targets must return a usage-style error.
- A clean lint exits `0` and prints a concise OK line that names the target.
- Lint findings exit `1` and print deterministic line-oriented findings.
- Config, load, and internal errors return normal command errors.
- Help text lists `rocketclaw lint [next|current]`.

### Target Semantics

- Config selection follows the existing operational precedence: `femtoclaw.json` selects `.femtoclaw/` when present; otherwise `rocketclaw.json` selects `.rocketclaw/`.
- `current` reads effective assets from the selected `<runtime-dir>/agents`, `<runtime-dir>/skills`, and `<runtime-dir>/scripts` as they already exist on disk.
- `current` does not apply overlays, fetch git overlays, inspect pending local overlay edits, or recreate workspace script symlinks.
- `next` builds a temporary startup-equivalent effective runtime tree from embedded assets, configured git overlays, and local workspace overlays in startup order.
- `next` must not mutate the real `.rocketclaw/` or `.femtoclaw/` directory.
- `next` must not recreate or remove workspace `scripts/` symlinks.
- `next` must not modify real configured overlay clone directories under the selected runtime directory.
- Temporary assets created for `next` are deleted after lint completes.

### Finding Codes

- `RC001` reports a same-agent write XOR execute violation when one agent can edit a file or path that can influence one of its allowed `bash` command subjects.
- `RC002` reports same-agent read plus constrained execute leakage when one agent can read a script/path and execute it through a constrained `bash` permission in a way that can bypass prompt-intended command shape.
- `RC003` reports a task delegation cycle, including self-loops, unless every agent participating in the cycle has bounded `maxRecursion`. Omitted `maxRecursion` and `maxRecursion: -1` are unbounded. `maxRecursion: 0` and positive values are bounded.
- `RC004` reports delegation-chain write-to-execute escalation when an agent can edit a script/path and can directly or transitively delegate within the same inference tree to another agent that can execute that path.
- `RC005` reports external-content contamination when an agent with `websearch` or `webfetch` permission can edit a path another agent can read. Task reachability is not required.
- `RC006` reports plural `permissions` frontmatter because RocketCode runtime parsing uses singular `permission`; plural `permissions` is ignored.

### Matching And Evaluation

- The linter uses runtime-compatible RocketCode permission parsing and evaluation wherever possible.
- Write capability comes from effective `edit` allow rules.
- Read capability comes from effective `read` allow rules, including read-from-edit inheritance under RocketCode permission semantics.
- Execute capability comes from effective `bash` allow rules.
- Delegation capability comes from effective `task` allow rules.
- External-content capability comes from effective `websearch: allow` or any effective `webfetch` allow.
- Paths are normalized to slash-separated workspace-relative paths for overlap checks.
- `./scripts/x.sh` and `scripts/x.sh` are overlapping references to the same workspace path.
- Glob-like or wildcard permission patterns are checked conservatively; possible overlap is enough to report a safety finding.
- Task graph edges are built by evaluating each agent's effective `task` permission against every potential target agent name, preserving RocketCode last-match-wins behavior.

### Suppressions

- `#nolint RC001: reason` suppresses only that finding code for the same field or rule.
- `#nolint: reason` suppresses finding codes attached directly to the same field or rule.
- A non-empty human reason is required.
- Malformed, reasonless, or unknown-code `#nolint` comments produce a lint finding instead of being silently ignored.
- Cross-agent findings may be suppressed from any directly contributing field or rule.
- Suppression is local and must not suppress unrelated findings elsewhere.

### Output

- Clean output is line-oriented and includes the command target, for example `rocketclaw lint next: OK`.
- Finding output begins with a line naming the target and finding count.
- Each finding line includes code, severity, relevant agent path or paths, and the relevant path or rule when practical.
- Findings sort deterministically by code, path, and message.
- Initial linter findings are blocking `error` findings; this ADR does not introduce warning-only lint behavior.

### Setup Skills

- The embedded create/update agent skill must instruct agents to run `rocketclaw lint` after requested `agents/` edits or relevant `scripts/` edits and before `rocketclaw_restart`.
- The embedded create/update agent skill must explain write XOR execute, delegation-chain escalation, read-plus-execute leakage, external-content contamination, and precise `#nolint RCxxx: reason` use when the human explicitly accepts the risk.
- The embedded create/update skill skill must instruct agents to run `rocketclaw lint` before restart when skill edits affect agent behavior, permission guidance, task delegation, or scripts.
- The linter remains the second line of defense; setup skills are the first line of defense for shaping safe edits before lint runs.

## Non-Goals

- This ADR does not make `rocketclaw run` fail automatically on lint findings.
- This ADR does not change RocketCode runtime permission semantics.
- This ADR does not make plural `permissions` valid configuration.
- This ADR does not require hot reload of agents, skills, cron, scripts, or overlays.
- This ADR does not define a general static-analysis framework outside RocketClaw agent-system safety.

## Evidence

- `cmd/rocketclaw/main.go`
- `internal/rocketclaw/app/app.go`
- `internal/rocketclaw/skel/skel.go`
- `internal/rocketclaw/harnessbridge/bridge.go`
- `internal/rocketcode/agents.go`
- `internal/rocketcode/permission.go`
- `internal/rocketcode/tasks.go`

## Consequences

- `lint next` must share effective asset semantics with startup without inheriting startup's runtime-directory reset or workspace script-symlink side effects.
- Agent-system safety checks must consider the whole effective system, including transitive task reachability.
- Human suppressions are explicit, local, and justified instead of silent or global.
- Tests for lint behavior must cover both target semantics and each finding code.

## Changelog

- 2026-06-11: Initial accepted snapshot.
