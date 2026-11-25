# One agent record: delegation rule, bounded verification

`Delegation.tla` holds RocketClaw's "may agent A act on agent B" rule twice: `OldAllowed`, as the code on `main` decides it before this change, and `NewAllowed`, as the plan's Key Decisions settle it. TLC checks the settled properties of the new rule over a finite world of callers, targets, rules, sessions and operations, and checks that every case where the two rules disagree is one the decisions name. `DelegationReport.tla` prints each of those cases. The plan is `docs/plans/2026-11-25-1231-refactor-one-agent-record-plan.md` (KTD7). **Passing checks do not prove the Go implementation correct**; the plan's differential run ties the model to the real code.

The model uses plain TLA+, not PlusCal. No step depends on the order of events: each case is one decision on one fixed world. The behavior is trivial. `Init` picks any case of the world and `Next` changes nothing, so TLC visits every case once as an initial state and checks every invariant on it. There is no state diagram for the same reason.

## Run

From the repository root:

```sh
make -C docs/verification/one-agent-record            # every check, about a minute
make -C docs/verification/one-agent-record new-rule   # one check
```

Requires Java 11+, `curl` and Make. The Makefile downloads `tla2tools.jar` v1.8.0 and verifies SHA-256 `67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928`. The tools, Java's temporary files, TLC's working files and each check's log go to the repository root's `.tmp/verification/one-agent-record/`, or to `WORK` when it is set. A check that must pass passes only when TLC exits 0 and reports `Model checking completed. No error has been found.`. A check that must fail passes only when TLC exits 12 or 13 and names the violation the Makefile expects, so a parse error never counts as the expected failure.

## The world

A case is a caller, a target, the caller's rules, a session of the target and an operation.

- **Agents**: every combination of kind (`advanced`, `easy`), sudo (advanced only; easy files have no `sudo` key), sticky (`off`, `user`, `global`, `slack`), receiver list (`absent`, `true`, `false`, a list naming the caller, a list naming others) and, for easy agents, whether `subagents:` holds copy models. That makes 80 agents. One more is the built-in agent with the attributes of its file, which no overlay can replace: `main` (advanced, sudo, sticky global, no receiver key, `task: {main: deny}`, session tools by the sudo default). The built-in `voice` agent was later removed from RocketClaw, so the world no longer holds it.
- **Target**: the caller itself (self) or any agent, counted as a different agent even when its attributes match the caller's.
- **Caller's rules**: an advanced caller's `task` rule for the target is `none`, `allow`, `auto`, `deny` or a `"*": allow` wildcard. An easy caller has a `- agent:` entry for the target or not. The caller has the session tools or not.
- **Session**: the target's top-level session; a helper session (Delegation History) running the target inside its own chat, the caller's chat, a sudo agent's chat or a non-sudo agent's chat; or, for a `sticky: slack` caller acting on itself, one of its own Thread Sessions written from its Channel Session.
- **Operations**: `start` (`task`), `write` (`rocketclaw_sessions_write`, creating or writing), `read` (`rocketclaw_sessions_get`), `list` (a session appearing in `rocketclaw_sessions_list`) and `clone` (the old easy `task`, now `task_clone`). A clone requests all of the parent's tools or any subset of two tool names, from a parent holding any subset of them.

Built from 81 agents in 6,642 caller-target pairs, this gives 745,989 cases.

## Model to code

| Model | Old code (`main@origin`) | New rule (plan) |
|---|---|---|
| `TaskGate` | `harness/permission.go` `evaluateRules`: last matching `task` rule wins; no match is `deny`, or `auto` for sudo; `harness/agents.go` `sudoPermissions` puts `task: "*": allow` ahead of a sudo file's own rules | unchanged for advanced; easy `- agent:` entries become `auto` rules (KTD3) |
| `OldAllowed` start | `harness/tasks.go` `subagentWrite`: advanced callers only; refuses easy targets; `delegable`: sticky `off` or self | |
| `OldAllowed` clone | `harness/tasks.go` easy branch: a copy of the caller on a `subagents:` model with its tools or a subset; `harness/easy.go` gives an easy agent `task: <self>: allow` only when it has subagent models | |
| `OldAccepts`, `OldAllowed` write | `backend/session_tools.go` `checkRecipient`/`agentRecipients`: easy targets accept unless `delegatedFrom` excludes the sender; advanced targets read `rocketclaw_sessions_recipient`, defaulting to accept for non-sudo and refuse for sudo; the sender's sudo is ignored; built-in agents only change the error text; `checkSessionTarget` requires a top-level session; a Channel Session writing into its own Thread Sessions skips `checkRecipient` | |
| `OldAllowed` read, list | `getSessionTool` and `listSessionsTool`: no target check at all, helper sessions included | |
| `Named`, `Reaches` | | KTD2: a non-sudo caller's `task` rule other than `deny`, wildcard included, or an easy `- agent:` entry makes the target a target; sudo callers reach any target except one their own `task` rules deny, and always reach themselves |
| `SudoDenies` | | a later user decision: a sudo caller's `task` rule denying a target other than itself also refuses session tools on that target |
| `Hermetic`, `SudoOwned` | | KTD2, K16: a session whose top-level session belongs to a sudo agent other than the caller is refused, except reads by a sudo caller |
| `NewRule` start | | `task`: the gate, sticky targets only when self (R20), hermetic |
| `NewRule` clone | | `task_clone`: copy models and a tool subset only (R22, KTD2) |
| `NewRule` write, read, list | | session tools granted, `Reaches`; writes still need a top-level session |

The implementation (`harness/delegation.go` `delegates`) has the same shape: hermetic judged by the top-level session's agent, sudo callers reach everything else and read everything except targets other than themselves that their `task` rules deny, non-sudo callers need a `task` rule other than `deny`, and `task` passes the target itself as the top-level owner. It also refuses a non-sudo caller when the session's top-level agent or the target is not loaded (see Limits).

## Properties

Every invariant below is checked against all 745,989 cases.

- `SudoHermetic`: an allowed action on a session owned by another sudo agent is a read or list by a sudo caller (K16, R19).
- `SudoDenialHolds`: a sudo caller never acts on a target other than itself that its `task` rules deny, by `task` or any session tool.
- `NamedOnly`: a non-sudo caller acts on a target only when its own rules name it (K12, K13, K14, R13, R15, R16).
- `SelfOnlyWhenNamed`: a non-sudo caller acts on itself only when it names itself, its own Slack threads included (K15, R18).
- `StickyNotTasked`: `task` never starts a sticky target other than the caller (R20).
- `CloneNeverWidens`: a `task_clone` copy never gets a tool its parent lacks (R22).
- `SessionToolsNeedGrant`: session tools act only for a caller granted them (K13, R15).
- `SudoReadsEverything`: a sudo caller with session tools reads and lists every session but those of another agent its `task` rules deny (R19).
- `SudoActsOnRest`: a sudo caller with session tools reads, lists and writes every session not owned by another sudo agent, but those of another agent its `task` rules deny, its own included even when its rules deny itself (R19).
- `NamedReaches`: a non-sudo caller reaches every target it names that no other sudo agent owns: `task` when the target is not sticky (or is itself), and session tools when it has them (R13, AE1, AE2).
- `ReceiverListsIgnored`: changing the target's receiver list never changes the answer (K12, R14).
- `BuiltinOrdinary`: marking the caller and target as not built in never changes the answer (K17, R20).
- `OnlyAgreedDifferences`: wherever `OldAllowed` and `NewAllowed` disagree, `AgreedChange` names the difference.

The last four "must allow" properties keep the rule from passing by refusing everything. `DelegationReport.tla` also asserts that no difference is left unnamed, that every category explains at least one difference no other category explains (no dead or redundant category), and prints every difference.

## Agreed differences

`AgreedChange` is the disjunction of these categories, each a predicate on the case and the direction of the change.

| Category | Change | Decisions |
|---|---|---|
| `sudo-hermetic` | Refused now: starting, writing into, reading or listing a session owned by another sudo agent, unless a sudo caller reads or lists. This covers helper sessions inside a sudo agent's chat, a helper's own history included. | K16, R19, KTD2 |
| `sudo-denial-refused` | Refused now: a sudo caller using session tools on a target other than itself that its `task` rules deny. Before, a sudo caller's `task` rules gated only `task`. | a later user decision |
| `unnamed-refused` | Refused now: a non-sudo caller using session tools on a target its rules do not name. Before, reads and lists were open to any session and writes needed only the target's receiver list. | K12, K13, K14, R13, R15, R16 |
| `self-needs-naming` | Refused now: a non-sudo caller writing into, reading or listing its own sessions without naming itself. | K15, R18 |
| `slack-own-thread-needs-naming` | Refused now: a Slack Channel Session writing into its own Thread Sessions without naming itself. Before, this skipped the receiver list. | K15, R18, U5 |
| `sudo-acts-on-own-sessions` | Allowed now: a sudo agent writing into its own top-level sessions. Before, a sudo agent without a receiver key, `main` included, refused writes from everyone, itself included. | K16, R19 |
| `receiver-lists-gone` | Allowed now: writes that a receiver list refused, by a caller that names the target or is sudo. | K12, R14 |
| `task-reaches-easy-agents` | Allowed now: an advanced caller `task`ing an easy agent it names. Before, `task` refused easy targets. | K12, R13, R21 |
| `easy-tasks-listed-agents` | Allowed now: an easy caller `task`ing an agent it lists with `- agent:`, itself included even when sticky. Before, an easy agent's `task` could only copy itself, which is now `task_clone`. | K14, K18, R16, R21 |

No difference touches `task_clone`: the old easy copy and the new `task_clone` agree on every case (K18, R22).

## What each check proves

On 2026-11-25, `make` passed (exit 0, 55 s) with TLC `2026.11.19.180144` (rev `bc1b506`) and OpenJDK `21.0.13.1` on linux/aarch64 (16 workers).

| Check | World | Required | Outcome | Distinct states | Time |
|---|---|---|---|---|---|
| `new-rule` | the new rule; prints every difference | every property and report assumption | passed | 745,989 | 32 s |
| `old-rule-as-new` | the old rule in place of the new | `SudoHermetic` violated | violated: a non-sudo caller reads a helper session in a sudo agent's chat | first initial state | under 1 s |
| `no-agreed-change` | no agreed difference | `OnlyAgreedDifferences` violated | violated: an unnamed write the receiver list accepted | first initial state | 1 s |
| `implicit-self` | self reachable without naming | `SelfOnlyWhenNamed` violated | violated | first initial state | 1 s |
| `task-ignores-sticky` | `task` without the sticky refusal | `StickyNotTasked` violated | violated | first initial state | 1 s |
| `clone-unchecked` | `task_clone` without the tool subset check | `CloneNeverWidens` violated | violated | first initial state | 6 s |
| `reads-unnamed` | reads and lists open to unnamed targets | `NamedOnly` violated | violated | first initial state | 1 s |
| `sudo-reads-closed` | sudo callers cannot read other sudo agents | `SudoReadsEverything` violated | violated | first initial state | 3 s |
| `keeps-receivers` | the new rule still consulting receiver lists | `ReceiverListsIgnored` violated | violated | first initial state | 1 s |
| `sudo-ignores-denial` | sudo callers' session tools ignoring their `task` denials | `SudoDenialHolds` violated | violated: a sudo caller writes into an advanced agent it denies | first initial state | 3 s |

While the model was being built, the report's "no unnamed difference" assumption caught a mistake in the category text. `self-needs-naming` had excluded every own-Slack-thread case, so reading one's own Thread Session without naming oneself fell outside every category. The fix narrowed the exclusion to writes, which `slack-own-thread-needs-naming` covers. Neither rule changed.

A differential run then sent the same kinds of case through the real code at `4121ebe1` (the old rule) and at the change (the new rule): 94,373 cases. It matched `NewAllowed` everywhere and `OldAllowed` everywhere except 2,876 cases with caller `voice`. The model had given `voice` no session tools, but an easy file without a `rocketclaw:` key gets every group (`compileEasySections` in `harness/easy.go`, the same in both versions), so `voice` has them. With that fixed, the run has 0 unexplained differences and 0 property violations, and the difference list gained 82 rows with caller `voice` (61 distinct cases), all in existing categories: 53 in `unnamed-refused`, 21 in `sudo-hermetic` and 8 in `self-needs-naming`. The differential run did not cover create-mode writes (`rocketclaw_sessions_write` with an empty `session_id`), `task_clone`, `rocketclaw_sessions_get_url`, `rocketclaw_sessions_move`, `rocketclaw_sessions_current_id`, recursion limits or approver targets; for those, only this model speaks.

The differential is in `differential/`. `world.py` builds the case world. `run.sh` installs `zz_diff_test.go.tmpl` as `internal/rocketclaw/backend/zz_diff_test.go` in both trees, runs it in each, and removes both copies when it exits, whether or not the run passed. `compare.py` then checks every case against `OldAllowed` and `NewAllowed`, the categories of `AgreedChange` and the tables under "Every difference", and checks `SudoHermetic`, `SudoDenialHolds`, `NamedOnly`, `SelfOnlyWhenNamed` and `StickyNotTasked` on the new code's answers. It exits non-zero on any mismatch. To rerun it from the repository root, with the old tree a checkout of `4121ebe1`:

```sh
OLD_TREE=<checkout of 4121ebe1> NEW_TREE=. \
ROCKETCLAW_TEST_DATABASE_URL='postgres://<user>:<password>@<host>:<port>/<database>?sslmode=disable' \
docs/verification/one-agent-record/differential/run.sh
```

Requires Go, Python 3 and a test Postgres. The worlds, the outputs and the tests' temporary files go to `WORK`, which defaults to the repository root's `.tmp/one-agent-record-diff/`. The run takes about nine minutes.

On 2026-11-25, after a sudo caller's `task` denial began refusing its session tools, the rerun on the change (base `4121ebe1`) passed with 94,373 cases. The world's sudo callers with `deny` rules name every agent, themselves included, so the run covers both the refused targets and the sudo caller's own sessions, which stay reachable. The old code matched `OldAllowed` and the new code matched `NewAllowed` on every case. All 38,610 cases where the two versions differ fall in an agreed category and appear in the tables below, up from 32,982 before that decision. No property was violated. Before the code changed, the updated `compare.py` failed on the earlier run's outputs: 5,688 cases where the new code differed from `NewAllowed`, all `SudoDenialHolds` violations.

On 2026-11-25, after `voice` was removed from RocketClaw and dropped from the world, the rerun on the change rebased onto that removal (old tree still `4121ebe1`) passed with 87,969 cases. The old code matched `OldAllowed` and the new code matched `NewAllowed` on every case, all 34,617 cases where the two versions differ fall in an agreed category and appear in the tables below, and no property was violated. The tables no longer hold the rows with caller or target `voice` that the earlier runs listed.

No settled decision proved contradictory or impossible in this world.

## Assumptions

- "Allowed" means the rule lets the call go ahead. A call whose rule is `auto` still goes to the reviewer, who may refuse it. Session tools keep their own `rocketclaw` review action (KTD2).
- A sudo caller reaches any target with session tools unless its `task` rules deny that target (only a non-sudo caller's `task` rule makes a target; a sudo caller's denial, a later user decision, takes one away). Only a matching `deny` counts: sudo's `"*": allow` comes first, so a target its own rules do not mention stays reachable, and `"*": deny` takes away every other agent. A denial of itself takes nothing away from its session tools, since `main`'s `task: {main: deny}` exists to keep `main` from starting `main`: that rule still gates `task`, and `main` still reads, lists and writes its own sessions.
- A helper session is judged by two agents. Naming uses the agent the session runs; the hermetic rule uses the agent owning its top-level chat. `task` and creating a session are judged with the target as its own owner, as `delegates` does.
- Writes still need a top-level session under both rules.
- The same agent files serve both rules. Each rule ignores the fields it does not know: the old rule ignores easy `- agent:` entries, which an old easy file could not hold, and the new rule ignores receiver lists, which a new file can no longer hold.
- `main` keeps its current file.

## Limits

- Bounded to one caller, one target and one session at a time. Rules are per pair, so more agents add more pairs, not new kinds of case. Each operation is decided alone.
- Left out: recursion budgets (`maxRecursion`, K20, R23); an easy agent's reviewer and its `approver.subagents` targets (R17); the `task` tool's listing and enum of allowed agents (R21); `task_clone` model matching and wildcards (K19); sessions of agents that are not loaded, and Web sessions switched between agents (plan Assumptions); the caller's own current session, its sync destination and its `reports_to`, which `checkSessionTarget` refuses by session ID under both rules; `rocketclaw_sessions_get_url`, `rocketclaw_sessions_move`, `rocketclaw_sessions_current_id` and tag tools; turns that drop the write tool (unaddressed, shadow); spaces, which play no part in delegation (Scope Boundaries); and remote OpenCode sessions.
- The model assumes every session's top-level agent is loaded. When it is not, the code refuses non-sudo callers (it fails closed); neither the model nor the differential world covers that case.
- The model follows the code at `main@origin` as read for this record. A change to those files on `main` needs the old rule checked again.

## Every difference

Each row is one class of case: the operation, the caller (`main`, `sudo advanced`, `advanced` or `easy`), whether the caller's rules name the target (`sudo` callers need no naming; `sudo, denies target` marks a sudo caller whose `task` rules deny a target other than itself), the target (a sticky target is marked for `task`), the session, and the new answer. The old answer is always the opposite. A row can appear under two categories when both apply, such as an unnamed read of `main`: the 255 rows below are 215 distinct cases. These tables come from the `new-rule` log.

#### `sudo-hermetic` (82)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| read | advanced | unnamed | advanced | helper in a sudo agent's chat | now refused |
| read | advanced | named | advanced | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | advanced | helper in a sudo agent's chat | now refused |
| list | advanced | named | advanced | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | advanced | helper in a sudo agent's chat | now refused |
| read | easy | named | advanced | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | advanced | helper in a sudo agent's chat | now refused |
| list | easy | named | advanced | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | easy | helper in a sudo agent's chat | now refused |
| read | advanced | named | easy | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | easy | helper in a sudo agent's chat | now refused |
| list | advanced | named | easy | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | easy | helper in a sudo agent's chat | now refused |
| read | easy | named | easy | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | easy | helper in a sudo agent's chat | now refused |
| list | easy | named | easy | helper in a sudo agent's chat | now refused |
| start | advanced | named | sudo advanced | - | now refused |
| read | advanced | unnamed | sudo advanced | top-level | now refused |
| read | advanced | named | sudo advanced | top-level | now refused |
| read | advanced | unnamed | sudo advanced | helper in its own chat | now refused |
| read | advanced | named | sudo advanced | helper in its own chat | now refused |
| read | advanced | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| read | advanced | named | sudo advanced | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | sudo advanced | top-level | now refused |
| list | advanced | named | sudo advanced | top-level | now refused |
| list | advanced | unnamed | sudo advanced | helper in its own chat | now refused |
| list | advanced | named | sudo advanced | helper in its own chat | now refused |
| list | advanced | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| list | advanced | named | sudo advanced | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | sudo advanced | top-level | now refused |
| read | easy | named | sudo advanced | top-level | now refused |
| read | easy | unnamed | sudo advanced | helper in its own chat | now refused |
| read | easy | named | sudo advanced | helper in its own chat | now refused |
| read | easy | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| read | easy | named | sudo advanced | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | sudo advanced | top-level | now refused |
| list | easy | named | sudo advanced | top-level | now refused |
| list | easy | unnamed | sudo advanced | helper in its own chat | now refused |
| list | easy | named | sudo advanced | helper in its own chat | now refused |
| list | easy | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| list | easy | named | sudo advanced | helper in a sudo agent's chat | now refused |
| start | sudo advanced | sudo | sudo advanced | - | now refused |
| start | main | sudo | sudo advanced | - | now refused |
| write | advanced | unnamed | sudo advanced | top-level | now refused |
| write | advanced | named | sudo advanced | top-level | now refused |
| write | easy | unnamed | sudo advanced | top-level | now refused |
| write | easy | named | sudo advanced | top-level | now refused |
| write | sudo advanced | sudo | sudo advanced | top-level | now refused |
| write | sudo advanced | sudo, denies target | sudo advanced | top-level | now refused |
| write | main | sudo | sudo advanced | top-level | now refused |
| read | advanced | unnamed | main | top-level | now refused |
| read | advanced | named | main | top-level | now refused |
| read | advanced | unnamed | main | helper in its own chat | now refused |
| read | advanced | named | main | helper in its own chat | now refused |
| read | advanced | unnamed | main | helper in a sudo agent's chat | now refused |
| read | advanced | named | main | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | main | top-level | now refused |
| list | advanced | named | main | top-level | now refused |
| list | advanced | unnamed | main | helper in its own chat | now refused |
| list | advanced | named | main | helper in its own chat | now refused |
| list | advanced | unnamed | main | helper in a sudo agent's chat | now refused |
| list | advanced | named | main | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | main | top-level | now refused |
| read | easy | named | main | top-level | now refused |
| read | easy | unnamed | main | helper in its own chat | now refused |
| read | easy | named | main | helper in its own chat | now refused |
| read | easy | unnamed | main | helper in a sudo agent's chat | now refused |
| read | easy | named | main | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | main | top-level | now refused |
| list | easy | named | main | top-level | now refused |
| list | easy | unnamed | main | helper in its own chat | now refused |
| list | easy | named | main | helper in its own chat | now refused |
| list | easy | unnamed | main | helper in a sudo agent's chat | now refused |
| list | easy | named | main | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | self | helper in a sudo agent's chat | now refused |
| read | advanced | named | self | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | self | helper in a sudo agent's chat | now refused |
| list | advanced | named | self | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | self | helper in a sudo agent's chat | now refused |
| read | easy | named | self | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | self | helper in a sudo agent's chat | now refused |
| list | easy | named | self | helper in a sudo agent's chat | now refused |

#### `sudo-denial-refused` (43)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| write | sudo advanced | sudo, denies target | advanced | top-level | now refused |
| read | sudo advanced | sudo, denies target | advanced | top-level | now refused |
| read | sudo advanced | sudo, denies target | advanced | helper in its own chat | now refused |
| read | sudo advanced | sudo, denies target | advanced | helper in caller's chat | now refused |
| read | sudo advanced | sudo, denies target | advanced | helper in a sudo agent's chat | now refused |
| read | sudo advanced | sudo, denies target | advanced | helper in a non-sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | advanced | top-level | now refused |
| list | sudo advanced | sudo, denies target | advanced | helper in its own chat | now refused |
| list | sudo advanced | sudo, denies target | advanced | helper in caller's chat | now refused |
| list | sudo advanced | sudo, denies target | advanced | helper in a sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | advanced | helper in a non-sudo agent's chat | now refused |
| write | sudo advanced | sudo, denies target | easy | top-level | now refused |
| read | sudo advanced | sudo, denies target | easy | top-level | now refused |
| read | sudo advanced | sudo, denies target | easy | helper in its own chat | now refused |
| read | sudo advanced | sudo, denies target | easy | helper in caller's chat | now refused |
| read | sudo advanced | sudo, denies target | easy | helper in a sudo agent's chat | now refused |
| read | sudo advanced | sudo, denies target | easy | helper in a non-sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | easy | top-level | now refused |
| list | sudo advanced | sudo, denies target | easy | helper in its own chat | now refused |
| list | sudo advanced | sudo, denies target | easy | helper in caller's chat | now refused |
| list | sudo advanced | sudo, denies target | easy | helper in a sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | easy | helper in a non-sudo agent's chat | now refused |
| read | sudo advanced | sudo, denies target | sudo advanced | top-level | now refused |
| read | sudo advanced | sudo, denies target | sudo advanced | helper in its own chat | now refused |
| read | sudo advanced | sudo, denies target | sudo advanced | helper in caller's chat | now refused |
| read | sudo advanced | sudo, denies target | sudo advanced | helper in a sudo agent's chat | now refused |
| read | sudo advanced | sudo, denies target | sudo advanced | helper in a non-sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | sudo advanced | top-level | now refused |
| list | sudo advanced | sudo, denies target | sudo advanced | helper in its own chat | now refused |
| list | sudo advanced | sudo, denies target | sudo advanced | helper in caller's chat | now refused |
| list | sudo advanced | sudo, denies target | sudo advanced | helper in a sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | sudo advanced | helper in a non-sudo agent's chat | now refused |
| write | sudo advanced | sudo, denies target | sudo advanced | top-level | now refused |
| read | sudo advanced | sudo, denies target | main | top-level | now refused |
| read | sudo advanced | sudo, denies target | main | helper in its own chat | now refused |
| read | sudo advanced | sudo, denies target | main | helper in caller's chat | now refused |
| read | sudo advanced | sudo, denies target | main | helper in a sudo agent's chat | now refused |
| read | sudo advanced | sudo, denies target | main | helper in a non-sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | main | top-level | now refused |
| list | sudo advanced | sudo, denies target | main | helper in its own chat | now refused |
| list | sudo advanced | sudo, denies target | main | helper in caller's chat | now refused |
| list | sudo advanced | sudo, denies target | main | helper in a sudo agent's chat | now refused |
| list | sudo advanced | sudo, denies target | main | helper in a non-sudo agent's chat | now refused |

#### `unnamed-refused` (86)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| write | advanced | unnamed | advanced | top-level | now refused |
| read | advanced | unnamed | advanced | top-level | now refused |
| read | advanced | unnamed | advanced | helper in its own chat | now refused |
| read | advanced | unnamed | advanced | helper in caller's chat | now refused |
| read | advanced | unnamed | advanced | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | advanced | helper in a non-sudo agent's chat | now refused |
| list | advanced | unnamed | advanced | top-level | now refused |
| list | advanced | unnamed | advanced | helper in its own chat | now refused |
| list | advanced | unnamed | advanced | helper in caller's chat | now refused |
| list | advanced | unnamed | advanced | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | advanced | helper in a non-sudo agent's chat | now refused |
| write | easy | unnamed | advanced | top-level | now refused |
| read | easy | unnamed | advanced | top-level | now refused |
| read | easy | unnamed | advanced | helper in its own chat | now refused |
| read | easy | unnamed | advanced | helper in caller's chat | now refused |
| read | easy | unnamed | advanced | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | advanced | helper in a non-sudo agent's chat | now refused |
| list | easy | unnamed | advanced | top-level | now refused |
| list | easy | unnamed | advanced | helper in its own chat | now refused |
| list | easy | unnamed | advanced | helper in caller's chat | now refused |
| list | easy | unnamed | advanced | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | advanced | helper in a non-sudo agent's chat | now refused |
| write | advanced | unnamed | easy | top-level | now refused |
| read | advanced | unnamed | easy | top-level | now refused |
| read | advanced | unnamed | easy | helper in its own chat | now refused |
| read | advanced | unnamed | easy | helper in caller's chat | now refused |
| read | advanced | unnamed | easy | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | easy | helper in a non-sudo agent's chat | now refused |
| list | advanced | unnamed | easy | top-level | now refused |
| list | advanced | unnamed | easy | helper in its own chat | now refused |
| list | advanced | unnamed | easy | helper in caller's chat | now refused |
| list | advanced | unnamed | easy | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | easy | helper in a non-sudo agent's chat | now refused |
| write | easy | unnamed | easy | top-level | now refused |
| read | easy | unnamed | easy | top-level | now refused |
| read | easy | unnamed | easy | helper in its own chat | now refused |
| read | easy | unnamed | easy | helper in caller's chat | now refused |
| read | easy | unnamed | easy | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | easy | helper in a non-sudo agent's chat | now refused |
| list | easy | unnamed | easy | top-level | now refused |
| list | easy | unnamed | easy | helper in its own chat | now refused |
| list | easy | unnamed | easy | helper in caller's chat | now refused |
| list | easy | unnamed | easy | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | easy | helper in a non-sudo agent's chat | now refused |
| read | advanced | unnamed | sudo advanced | top-level | now refused |
| read | advanced | unnamed | sudo advanced | helper in its own chat | now refused |
| read | advanced | unnamed | sudo advanced | helper in caller's chat | now refused |
| read | advanced | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | sudo advanced | helper in a non-sudo agent's chat | now refused |
| list | advanced | unnamed | sudo advanced | top-level | now refused |
| list | advanced | unnamed | sudo advanced | helper in its own chat | now refused |
| list | advanced | unnamed | sudo advanced | helper in caller's chat | now refused |
| list | advanced | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | sudo advanced | helper in a non-sudo agent's chat | now refused |
| read | easy | unnamed | sudo advanced | top-level | now refused |
| read | easy | unnamed | sudo advanced | helper in its own chat | now refused |
| read | easy | unnamed | sudo advanced | helper in caller's chat | now refused |
| read | easy | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | sudo advanced | helper in a non-sudo agent's chat | now refused |
| list | easy | unnamed | sudo advanced | top-level | now refused |
| list | easy | unnamed | sudo advanced | helper in its own chat | now refused |
| list | easy | unnamed | sudo advanced | helper in caller's chat | now refused |
| list | easy | unnamed | sudo advanced | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | sudo advanced | helper in a non-sudo agent's chat | now refused |
| write | advanced | unnamed | sudo advanced | top-level | now refused |
| write | easy | unnamed | sudo advanced | top-level | now refused |
| read | advanced | unnamed | main | top-level | now refused |
| read | advanced | unnamed | main | helper in its own chat | now refused |
| read | advanced | unnamed | main | helper in caller's chat | now refused |
| read | advanced | unnamed | main | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | main | helper in a non-sudo agent's chat | now refused |
| list | advanced | unnamed | main | top-level | now refused |
| list | advanced | unnamed | main | helper in its own chat | now refused |
| list | advanced | unnamed | main | helper in caller's chat | now refused |
| list | advanced | unnamed | main | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | main | helper in a non-sudo agent's chat | now refused |
| read | easy | unnamed | main | top-level | now refused |
| read | easy | unnamed | main | helper in its own chat | now refused |
| read | easy | unnamed | main | helper in caller's chat | now refused |
| read | easy | unnamed | main | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | main | helper in a non-sudo agent's chat | now refused |
| list | easy | unnamed | main | top-level | now refused |
| list | easy | unnamed | main | helper in its own chat | now refused |
| list | easy | unnamed | main | helper in caller's chat | now refused |
| list | easy | unnamed | main | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | main | helper in a non-sudo agent's chat | now refused |

#### `self-needs-naming` (22)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| write | advanced | unnamed | self | top-level | now refused |
| read | advanced | unnamed | self | top-level | now refused |
| read | advanced | unnamed | self | helper in its own chat | now refused |
| read | advanced | unnamed | self | helper in a sudo agent's chat | now refused |
| read | advanced | unnamed | self | helper in a non-sudo agent's chat | now refused |
| list | advanced | unnamed | self | top-level | now refused |
| list | advanced | unnamed | self | helper in its own chat | now refused |
| list | advanced | unnamed | self | helper in a sudo agent's chat | now refused |
| list | advanced | unnamed | self | helper in a non-sudo agent's chat | now refused |
| read | advanced | unnamed | self | own Slack thread | now refused |
| list | advanced | unnamed | self | own Slack thread | now refused |
| write | easy | unnamed | self | top-level | now refused |
| read | easy | unnamed | self | top-level | now refused |
| read | easy | unnamed | self | helper in its own chat | now refused |
| read | easy | unnamed | self | helper in a sudo agent's chat | now refused |
| read | easy | unnamed | self | helper in a non-sudo agent's chat | now refused |
| list | easy | unnamed | self | top-level | now refused |
| list | easy | unnamed | self | helper in its own chat | now refused |
| list | easy | unnamed | self | helper in a sudo agent's chat | now refused |
| list | easy | unnamed | self | helper in a non-sudo agent's chat | now refused |
| read | easy | unnamed | self | own Slack thread | now refused |
| list | easy | unnamed | self | own Slack thread | now refused |

#### `slack-own-thread-needs-naming` (2)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| write | advanced | unnamed | self | own Slack thread | now refused |
| write | easy | unnamed | self | own Slack thread | now refused |

#### `sudo-acts-on-own-sessions` (2)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| write | sudo advanced | sudo | self | top-level | now allowed |
| write | main | sudo | self | top-level | now allowed |

#### `receiver-lists-gone` (11)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| write | advanced | named | advanced | top-level | now allowed |
| write | easy | named | advanced | top-level | now allowed |
| write | sudo advanced | sudo | advanced | top-level | now allowed |
| write | main | sudo | advanced | top-level | now allowed |
| write | advanced | named | easy | top-level | now allowed |
| write | easy | named | easy | top-level | now allowed |
| write | sudo advanced | sudo | easy | top-level | now allowed |
| write | main | sudo | easy | top-level | now allowed |
| write | advanced | named | self | top-level | now allowed |
| write | easy | named | self | top-level | now allowed |
| write | sudo advanced | sudo | self | top-level | now allowed |

#### `task-reaches-easy-agents` (3)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| start | advanced | named | easy | - | now allowed |
| start | sudo advanced | sudo | easy | - | now allowed |
| start | main | sudo | easy | - | now allowed |

#### `easy-tasks-listed-agents` (4)

| Operation | Caller | Caller's rules | Target | Session | Now |
|---|---|---|---|---|---|
| start | easy | named | advanced | - | now allowed |
| start | easy | named | easy | - | now allowed |
| start | easy | named | self | - | now allowed |
| start | easy | named | self (sticky) | - | now allowed |
