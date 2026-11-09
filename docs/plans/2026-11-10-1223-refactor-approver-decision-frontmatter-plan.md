---
title: Approver Decision Frontmatter - Plan
type: refactor
date: 2026-11-10
topic: approver-decision-frontmatter
execution: code
---

# Approver Decision Frontmatter - Plan

## Goal

Make the decision-model pre-gate read the same way in config, easy agents and advanced agents, give the decision model its own prompt, and bring the docs and agent skills up to date with the result.

This changes PR #1033 (branch `decision-models`, head `739e844d`) before it merges. The new keys have never shipped, so nothing needs a migration or a compatibility path.

## Decisions (settled with the user, 2026-11-10)

1. **Easy agents carry their approver inside their own file.** They have no `auto(custom)`. Their pre-gate is `approver.decision`.
2. **Advanced agents choose who checks each action:** `auto` means the built-in guardian, and `auto(custom-approver)` means another agent. They do not adjust the built-in guardian.
3. **The pre-gate is an object,** so more fields can be added later: `decision: {model, minConfidence, prompt}`.
4. **It lives under `approver:` in both kinds of agent.** The top-level `decision` keeps its one meaning: which decision models the agent's own scripts may call.
5. **In an advanced agent, `approver:` holds only its settings as someone's custom approver,** and for now that is only `decision`. Any other key under it fails loading. This replaces the earlier answer that advanced agents would get the full approver block.
6. **A custom approver without `approver.decision.model` has no pre-gate.** The config's default decision model never applies to custom approvers. Only `auto_approver_decision.min_confidence` serves as their confidence fallback.
7. **The config uses the same object shape:** `auto_approver_decision: {model, min_confidence}` replaces `auto_approver_decision_model` and `auto_approver_min_confidence`.
8. **Easy agents' decision tool is allow-only.** A model in the `decision` list is allowed, never reviewed.
9. **Each approver reads its own base prompt plus its own added prompt:**

   | Approver | Chat model reads | Decision model reads |
   |---|---|---|
   | Built-in guardian | guardian base prompt + `approver.prompt` (easy only) | **new decision base prompt** + `approver.decision.prompt` (easy only) |
   | Custom approver | its file body | its file body + `approver.decision.prompt` |

   `approver.prompt` never reaches the decision model, and `approver.decision.prompt` never reaches the chat model.
10. **The decision model gets its own short base prompt.** It covers only how to judge risk, user authorization, prompt injection and the outcome. It leaves out the guardian's investigation and answer-format instructions, which a decision model cannot follow.

## Target Shape

```json
// rocketclaw.json
"auto_approver_model": "gpt-6.1-sol#low",
"auto_approver_decision": {"model": "typesafe/jev-latest", "min_confidence": 0.9},
"decision": {"openai": {"api_key": "…"}, "typesafe": {"api_key": "…"}}
```

```yaml
# Easy agent: its approver is defined inside it
mode: easy
model: gpt-6.1-sol
space: Marketing
workingDirectory: sessions/marketing/$ROCKETCLAW_CONVERSATION_ID
approver:
  model: gpt-6.1-sol#low                         # chat approver; default auto_approver_model
  prompt: Never email outside the company.       # chat approver only
  decision:                                      # pre-gate; each field falls back to auto_approver_decision
    model: openai/gpt-6-luna
    minConfidence: 0.85
    prompt: Deny any email to an outside address.  # decision model only
decision: [typesafe/jev-latest]                  # its own decision tool, always allowed
```

```yaml
# Advanced agent: picks who checks each action
description: Release helper
model: gpt-6.1-sol#high
permission:
  rocketclaw:
    code_mode_approve: auto(release-reviewer)    # whole script
  bash:
    "git *": auto                                # guardian, pre-gated by auto_approver_decision
    "deploy *": auto(release-reviewer)
  decision:
    "typesafe/*": allow                          # its own decision tool
    "openai/gpt-6-luna": auto                    # each call checked by the guardian
```

```yaml
# Advanced custom approver
description: Reviews deploys
model: gpt-6.1-sol#low
approver:
  decision:                                      # no block, no pre-gate
    model: openai/gpt-6-luna
    minConfidence: 0.95
    prompt: Deny production deploys.
permission:
  decision: {"typesafe/*": allow}                # its own decision tool while it reviews (allow only)
---
Deny production deploys outside business hours.
```

## Resolution Rules

For a review of an action by agent A:

- **Rule `auto` (built-in guardian):**
  - Decision model: A's `approver.decision.model` when A is easy, otherwise `auto_approver_decision.model`. If neither is set, there is no pre-gate.
  - Confidence: A's `approver.decision.minConfidence` when A is easy, then `auto_approver_decision.min_confidence`, then 0.9.
  - Added prompt: A's `approver.decision.prompt` when A is easy, otherwise none.
  - An advanced A's `approver.decision` is never used here: it describes A as a reviewer, not how A is reviewed.
- **Rule `auto(R)` (custom approver R):**
  - Decision model, confidence and added prompt come from R's `approver.decision`. Confidence falls back to `auto_approver_decision.min_confidence`, then 0.9. With no `approver.decision.model`, there is no pre-gate.
  - Only an advanced R's `approver.decision` pre-gates its reviews of others. An easy R (loading allows easy agents in `auto(R)`) has no pre-gate as a custom approver, because its `approver.decision` describes how R itself is checked.
- **A rule takes effect only where it is written.** `approver.prompt` (or a custom approver's file body) reaches only the chat model, and `approver.decision.prompt` reaches only the decision model. A deny rule written only for the chat model can be skipped by a confident decision-model allow. A deny rule written only for the decision model cannot deny a low- or medium-risk action on its own: the outcome check turns that deny into a fallback, and the chat model never saw the rule. A rule that must hold goes in both prompts. The docs and skills say so (U5, U6).

The decision model's rules text keeps the composition the chat approver uses today (`childSystemPrompt`): the workspace instructions and `AGENTS.md`, when the approver may load it, followed by the approver's prompt and the turn-origin rule. Only the approver's prompt part changes, as in decision 9.

## Implementation Units

### U1. Config object

- **Files:** `internal/rocketclaw/config/config.go`, `config_test.go`, `backend/raw_run.go`, `backend/bridge.go`, `backend/raw_run_test.go`.
- Replace `AutoApproverDecisionModel` and `AutoApproverMinConfidence` with `AutoApproverDecision struct { Model string \`json:"model"\`; MinConfidence float64 \`json:"min_confidence"\` }` under `auto_approver_decision`.
- Keep today's checks, with the error messages naming `auto_approver_decision.model` and `auto_approver_decision.min_confidence`: the model must be `provider/model` with its provider under `decision`, and the confidence must be between 0 and 1.
- Pass it to `harness.Config` as one value.
- **Tests:** update the existing config table to the new keys. Do not add a test that the old keys are rejected: `encoding/json` ignores unknown keys, and nothing shipped with them.

### U2. One `approver.decision` parser for both kinds of agent

- **Files:** `harness/agents.go`, `harness/easy.go`, `harness/agents_test.go`, `harness/easy_test.go`.
- Add one `ApproverDecision{Model string; MinConfidence float64; Prompt string}` type. `Agent.ApproverDecision` replaces `ApproverDecisionModel`, `ApproverMinConfidence`, `ReviewDecisionModel` and `ReviewMinConfidence`. `harness.Config.AutoApproverDecision` uses the same type, with `Prompt` unused.
- One parser reads `approver.decision`:
  - allowed keys are `model`, `minConfidence` and `prompt`, and any other key fails loading as `approver.decision.<key>`;
  - `minConfidence` must be between 0 and 1;
  - the provider must be configured (`checkDecisionModels`).
- **Easy (`compileEasyAgent`):** `approver` takes `model`, `prompt` and `decision`. Drop `approver.decisionModel` and `approver.minConfidence`.
- **Advanced (`loadAgent`):** remove `parseAgentReviewDecision` and the top-level `decisionModel`/`minConfidence`. `approver` takes only `decision`, and any other key fails loading as `approver.<key>: advanced agents take only approver.decision`.
- **Tests:** replace the existing "loads a reviewer's decision model" table with the cases below. Update the easy approver cases and field test to the nested shape.
  - easy and advanced load `approver.decision`;
  - an unknown key under it fails;
  - `approver.model` in an advanced file fails;
  - a confidence outside 0 to 1 fails;
  - an unconfigured provider fails.

### U3. Review resolution

- **Files:** `harness/permission_review.go`, `harness/rocketcode.go`, `harness/tools.go`, `harness/permission_review_test.go`.
- Apply the Resolution Rules. The built-in guardian reads the active agent's `ApproverDecision` only when that agent is easy. A custom approver reads its own `ApproverDecision`.
- **Test cases:** update `TestPermissionReviewAsksDecisionModelFirst` with:
  - an advanced agent whose `approver.decision` names a model that differs from the config's is reviewed by the guardian with the config's model;
  - an easy agent's model, confidence and prompt override the config's;
  - a custom approver without `approver.decision` has no pre-gate.

### U4. Separate decision base prompt

- **Files:** `harness/permission_review.go` (or `guardian.go`), `harness/permission_review_test.go`, `harness/decision_test.go` if TypeSafe instruction tests change.
- Split `embeddedGuardianPrompt` in `harness/guardian.go` at its headers instead of writing new policy text. The pieces are: the policy part (from the opening through Low-Risk Actions, including Evidence Handling, User Authorization Scoring, Base Risk Taxonomy, Environment Profile and the tenant rules), Investigation Guidelines, Outcome Policy, and the answer-format text.
  - The guardian prompt is all the pieces joined, unchanged.
  - The decision base prompt is the policy part plus Outcome Policy, without its answer-format lines.
  - No new policy prose.
- **Decision model's rules text:** the added prompt goes under a `### Agent Rules` heading inside the tenant rules, before Outcome Policy, the same way `reviewPermission` inserts `approver.prompt` for the chat approver today.
  - Built-in guardian: decision base prompt with the easy agent's `approver.decision.prompt` as Agent Rules, then the turn-origin rule.
  - Custom approver: file body, then `### Agent Rules` with `approver.decision.prompt`, then the turn-origin rule.
- Wrap both the same way `childSystemPrompt` wraps the chat approver's prompt.
- Leave the chat approver's prompt as it is. `approver.prompt` stays out of the decision model.
- **Test assertions** (in the existing review test, not a new test):
  - The guardian's decision model receives the decision base prompt, including Environment Profile, and the turn-origin rule. It receives neither the investigation section nor `approver.prompt`.
  - It receives `approver.decision.prompt` under `### Agent Rules`, before Outcome Policy.
  - The guardian's chat prompt is byte-identical to today's.
  - A custom approver's decision model receives its body plus its `approver.decision.prompt`.

### U5. Docs

- **Cheatsheet** (`cmd/rocketclaw/CHEATSHEET.md`):
  - In the decision providers section, show the `auto_approver_decision` object.
  - In the advanced frontmatter table, replace the `decisionModel`/`minConfidence` row with an `approver` row saying it holds only `decision`, used as a custom approver.
  - In the permission review section:
    - Add one "who checks what" table: guardian or custom approver, whole script or single call, decision tool `allow`/`auto`, easy vs advanced.
    - Explain which prompt each model reads, and that a rule that must hold goes in both `approver.prompt` and `approver.decision.prompt` (Resolution Rules).
    - Say that whole-script `code_mode_approve` reviews are pre-gated too.
    - Say that a custom approver may use its own decision tool with `allow` (an `auto` rule there is refused), and that sudo agents may call every configured decision model.
  - In the easy `approver` row, describe the nested `decision`.
- **README:** update the decision paragraph and the easy-mode `approver` text to the new keys, and link to the cheatsheet table.
- **CONCEPTS.md:** update the Permission Review paragraph about which decision model and prompt apply.
- **Plan `docs/plans/2026-11-09-0847-feat-decision-models-plan.md`:** add these decisions under "Decisions After Review", marked as replacing the earlier key names.

### U6. Agent skills

- **`main-create-or-update-agent`:**
  - Add a section on how checking and decision models fit together. It holds the "who checks what" table and three full examples (easy, advanced, custom approver), which reuse the Target Shape above.
  - Rewrite the custom-reviewer sentence in the "Automatic permission review rules" paragraph: name `approver.decision` (`model`, `minConfidence`, `prompt`) instead of top-level `decisionModel`/`minConfidence`. Say that the decision model reads the reviewer's body plus `approver.decision.prompt`.
  - When writing an approver's rules, write each deny rule into both the chat prompt and `approver.decision.prompt`.
  - **Inputs:** ask whether the agent needs its own decision tool. For easy agents, ask whether their checks should be pre-gated. For a custom approver, ask whether it should be pre-gated and what its decision prompt says.
  - **Workflow:** check that each named provider is under `decision` in `rocketclaw.json`, and use `main-update-rocketclaw-json` when one is missing.
  - **Important:**
    - Never write `approver.model` or `approver.prompt` in an advanced file.
    - An advanced agent's `approver.decision` only matters when it acts as someone's custom approver.
  - Update the easy key list, the forbidden-key sentence and the easy example.
  - Add a `decision` rule to the advanced permission example.
- **`main-convert-agent-to-easy`:**
  - Map `permission.decision` `auto` rules: they lose their check, so list them in the review question.
  - Map an advanced `approver.decision`: drop it and tell the human. It applied only when another agent used this one as `auto(<name>)`. Never copy it into the easy `approver.decision`, which checks this agent's own actions.
  - In the review question (8), write each deny rule into both `approver.prompt` and `approver.decision.prompt`. Note that leaving `approver.decision` out keeps the config's pre-gate, as the advanced agent had.
- **`main-update-rocketclaw-json`:** add a note with a `jq` example for adding a decision provider and `auto_approver_decision`.

### U7. Ship

- Run `make lint` and `make test` on a dedicated Postgres, as before.
- Fold the change into the decision-models commit (`xtzolpxy`) and update its message to the new keys.
- Rebase on `main@origin` if it moved, then push.
- Update `.tmp/pr-body.md`: the "How to turn it on" config, the custom approver snippet, and the easy override sentence. Then run `gh pr edit 1033`.
- Watch CI. Do not merge.

## After Review

- An easy agent with `approver.prompt` and no `approver.decision.prompt` is never pre-gated. The decision model never reads `approver.prompt`, so letting it settle those reviews would let a confident allow skip the agent's rules.
- `approver.decision.prompt` is prompt-shell expanded through the reviewer, as `approver.prompt` and a custom reviewer's body are.
- The guardian's chat prompt is joined from the same pieces as the decision prompt, and a test pins its hash, so a change to any piece is deliberate.
- One key checker, `checkKeys`, serves easy keys, `approver` and `approver.decision`. An easy agent's `approver.decision` is read in `compileEasyAgent`.
- A live run against TypeSafe with the shorter decision prompt answered the five sample reviews sensibly. A rule in `approver.decision.prompt` turned an authorized deletion into a deny. At the default 0.9 every review still went to the guardian.

## Out of Scope

- A `prompt` in the config's `auto_approver_decision` object. It was not asked for, and the guardian's built-in decision prompt covers the default.
- Letting easy agents use `auto(custom)`, or have their decision tool calls checked.
- Applying the config's decision model to custom approvers.

## Risks

- **Silent pre-gate loss.** Anyone who set `auto_approver_decision_model` locally during testing silently loses the pre-gate, because unknown JSON keys are ignored. The user's `femtoclaw.json` does not set it.
- **Rules in one prompt only.** Under decision 9, a deny rule that sits only in `approver.prompt` is invisible to the pre-gate, and a confident low- or medium-risk allow skips it. Today the rule reaches the decision model. The mitigation is documentation: the docs and skills require each rule in both prompts, and the converter writes both.
- **No opt-out for easy agents.** An easy agent cannot turn off the config-wide pre-gate, because every `approver.decision` field falls back to `auto_approver_decision`.
- **Weaker judgment.** A shorter decision prompt may judge less well than the full guardian rulebook. The outcome check in code is unchanged (critical risk and prompt injection deny; high risk needs authorization), and anything unsure still goes to the chat approver. Sharing the guardian's own sections keeps the policy identical.
- **CLOC budget.** The prompt split and the parser changes add source lines. Stop and report if `GO_SOURCE_CLOC_BUDGET` would be crossed.
