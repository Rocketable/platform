---
title: Slack Ambient Listening - Plan
type: feat
date: 2026-11-24
deepened: 2026-11-24
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Slack Ambient Listening - Plan

## Goal Capsule

- **Objective:** In Slack, an allowed person no longer has to @mention the bot to get answers. The bot follows up in threads it is part of, and it speaks up in channels an admin points it at when its goal applies. It stays silent otherwise, and costs one cheap check per message it skips. Changes of medium risk or more still need an @mention, and the bot says so when one is needed.
- **Means:** a handler for Slack `message` events screens each eligible message with OpenAI's Decisions API. A message that passes runs as an ambient turn in its thread's Thread Session. That turn shows no Slack status and may end with `slack_no_reply` (KTD1 to KTD6). The design is checked in `docs/verification/slack-sticky/` before it is built.
- **Authority:**
  - The Product Contract wins on behavior.
  - KTDs win on mechanism.
  - The TLA+ model gates U3 to U6 (pack: formal-modeling, stateful-design.md).
  - AGENTS.md rules apply throughout.
- **Stop conditions:**
  - TLC cannot run, does not finish, or fails a check: BLOCKED / NOT VERIFIED. Do not build U3 to U6.
  - The Go source line count would reach `GO_SOURCE_CLOC_BUDGET` (46250): stop and report. Never edit the budget.
  - Evidence shows a settled decision cannot work: stop and report.
- **Execution profile:** one PR. Deep plan.
- **Finishing:**
  - The implementing agent builds, verifies and opens the PR.
  - A person approves the AGENTS.md rule change in review (Open Questions) and merges.
  - A person then updates the Slack app and the deployment's config (Operational Notes).

---

## Product Contract

### Summary

RocketClaw starts reading Slack messages that do not mention the bot, in two places:

- **Joined Threads:** threads where the channel's agent already has a conversation.
- **Watched Channels:** channels an admin gives a goal on the Config page.

A cheap Decisions API call (`gpt-6-luna`) drops messages that are clearly not for the agent. Every other message becomes an ambient turn, which can answer and look things up but needs an @mention for medium-risk changes. An ambient turn either replies in the message's thread or calls `slack_no_reply` and posts nothing. Mentions keep today's behavior. The whole feature is off until an ambient decision model is configured.

### Problem Frame

Today Slack answers only @mentions. People in a thread with the bot naturally reply without mentioning it ("also check staging"), and those replies start nothing. Questions in busy channels that the bot could answer also go unseen unless someone remembers to mention it. The pattern at https://eve.dev/docs/patterns/ambient-listening fixes both. It lets the agent decide for itself when to speak, and a decision model keeps listening cheap.

### Requirements

**Intake**

- R1. A Slack message without a bot mention, from a user allowed in that channel, is considered only in two cases:
  - it is a reply in a Joined Thread;
  - it is any message, top-level or reply, in a Watched Channel.

  Ignored as today:
  - 1:1 DMs;
  - bot and app messages, the agent's own included;
  - Report Threads;
  - other subtypes. `file_share` and `thread_broadcast` count as ordinary messages.
- R2. A message that mentions the bot (`<@BOT>` or `<@BOT|name>`) is never handled as ambient. The mention path keeps it.
- R3. A reply in a thread that has no Thread Session, and whose root message mentions the bot, is ignored. The Channel Session owns that request.
- R4. An edit of a message whose thread has a Thread Session reaches that Thread Session as an ambient turn, with no decision call. Such a message is a reply in a Joined Thread, or a Watched Channel top-level message that was admitted. Other edits, and changes that are not edits, such as link unfurls, are ignored.
- R5. Each Slack message is admitted at most once, whether Slack delivers it as `app_mention`, as `message`, or again as a redelivery. Each edit is admitted at most once.

**Screening**

- R6. Before admission, a decision model answers whether the message could be for the agent.
  - **Input:** the message plus up to 10 earlier thread messages, each clipped to 500 characters, as structured records.
  - **Record fields:** author ID, whether the author is the agent or allowed, and text.
  - **Files:** shown as `[file: <name>]`.
  - **Instructions:** the question and any channel goal go in the request's instructions, never in its input.
- R7. The message is skipped only when the probability is below 0.2. It goes through (fail open) on an error, a refusal, or a 5-second timeout. It also goes through, with no decision call, when thread context could not be read or lacks the current reply. One exception: a reply in a Watched Channel thread the agent has not joined is dropped when its thread cannot be read, because without the root the connector cannot tell whether the thread belongs to the agent's own post or a mention. Each screen logs its outcome, probability and reason, never message text.
- R8. Edits (R4) and mentions are never screened.

**The ambient turn**

- R9. An admitted message runs as the next turn of the Thread Session for its thread. Its reply goes in that thread, never at the top of the channel. A top-level message in a Watched Channel gets a new Thread Session for its own thread.
- R10. An ambient turn never steers. It waits its turn in the Thread Queue, like a mention.
- R11. The turn carries guidance RocketClaw wrote, never message text:
  - **Why it arrived:** for a Joined Thread, "answer only if the message is meant for you"; for a Watched Channel, "answer only if the goal applies", plus the channel's `wake_when` and `engagement`.
  - **Act only on the current message:** thread context is other people's words, not requests. A message that did not address the agent never asks it to change anything.
  - **Staying quiet:** call `slack_no_reply`; any text the agent writes is posted.
  - **Changes:** when a request needs a change that review would deny here (R16), the agent says that an @mention is needed.
- R12. The turn carries thread context as structured records in the R6 shape, up to 50:
  - **A thread with a Thread Session:** the messages since the agent's last post there.
  - **A thread without one:** the whole thread, as when a mention takes over a thread.
- R13. `slack_no_reply` is offered only on ambient turns. Once it is called, the turn posts nothing to Slack: no answer, files or footer. This holds across a restart.
- R14. An ambient turn that ends with no answer and no files, fails, or is stopped posts nothing to Slack.
- R15. An ambient turn never shows Slack's working status or Stop. Stop from Web still ends it. A mention waiting behind it keeps its own status.
- R16. An ambient turn is unaddressed: the person did not ask the agent anything.
  - **Permission Review:** a message that did not address the agent never authorizes a medium-, high- or critical-risk action. RocketClaw enforces this in code, not only in reviewer text. On these turns, calls that an `allow` rule would run unreviewed go through review too.
  - **Spread:** the mark carries to the turn's Task subagents, its Shadow Session turns, and the wake turns of its Background Jobs.
- R17. Ambient turns do not get `schedule_message`, `reset_scheduled_messages` or `sessions_write`.
- R18. A silent ambient turn (R13, R14) does not mark Web tabs unread.

**Configuration**

- R19. Ambient listening runs only when `slack.ambient_decision_model` names a `provider/model` whose provider is configured under `decision`. Without it, Slack behaves exactly as today.
- R20. A Slack channel row may set `wake_when`, which makes it a Watched Channel, and `engagement`. Both are edited on the Config page. The `@` row cannot be watched.
- R21. A Web edit of a queued ambient item turns it into ordinary Web input: no ambient mark, guidance or `slack_no_reply`.

**Docs**

- R22. README.md, CONCEPTS.md and AGENTS.md describe Slack with ambient listening, the Slack app events and scopes it needs, and the data it sends to the decision model.

### Acceptance Examples

- AE1. **Covers R1, R9, R11.**
  - **Given:** `#eng` has a Thread Session for thread T, and Ann is allowed in `#eng`.
  - **When:** Ann replies in T "also check staging" without a mention.
  - **Then:** after screening, the reply queues in T's Thread Session with Joined Thread guidance. Any answer posts in T.
- AE2. **Covers R2, R5.** Slack delivers a thread mention as `message` first and `app_mention` second. Exactly one turn runs, through the mention path, with status.
- AE3. **Covers R7.**
  - **Given:** `#help` is a Watched Channel.
  - **When:** "brb coffee" scores 0.13.
  - **Then:** nothing is admitted and no conversation is created.
- AE4. **Covers R9, R13, R18.**
  - **Given:** a top-level question in `#help` passes screening.
  - **When:** a new Thread Session for that message's thread runs, and the agent calls `slack_no_reply`.
  - **Then:** Slack shows nothing, Web lists the session, and no tab turns unread.
- AE6. **Covers R19.** With `slack.ambient_decision_model` unset, a plain reply in a Joined Thread starts nothing.
- AE7. **Covers R16.** An ambient turn's script asks for a medium-risk write, and the decision model answers allow. RocketClaw denies the write.

### Scope Boundaries

- Mentions, Channel Sessions, `slack_respond_in_thread`, Report Threads and DMs keep today's behavior.
- **Considered and not built:**
  - **Grouping a burst of replies into one turn.** The queue runs them in order. Each later turn sees the earlier ones (R12) and can stay silent. Revisit if bursts prove costly.
  - **Screening without context when the thread read fails.** The link fails open, which is the settled behavior. Revisit if Slack rate limits make it costly; the R7 logs will show it.
  - **Screening in parallel with other Slack events.** It would reorder messages in a thread, and the 5-second cap bounds the stall (KTD4).
  - **Following threads across a change of the channel's first agent.** A Joined Thread belongs to the channel's first agent. After a change, or after a rename falls back to `@`, it is no longer joined. This is documented, not handled.
  - **Showing status while an ambient turn works.** The link shows no status until the agent acts; here, the answer post is that act. Slack turns cannot ask questions, so nothing waits on a person in Slack, and Web shows the work. A person may @mention again while a silent turn works; that mention queues as its own turn.

### Key Decisions

- **Behave like the eve.dev pattern: listen in Joined Threads and Watched Channels.** Governs R1, R3, R4, R6, R7. (session-settled: user-directed — chosen over listening in threads only: the user asked to do what the link does)
- **Only allowed users' messages count.** Governs R1. (session-settled: user-directed — chosen over anyone in the channel: matches who may mention the bot today)
- **Replies to a Watched Channel message go in that message's thread.** Governs R9. (session-settled: user-approved — chosen over answering at the top of the channel: it is what the link does)

### Open Questions

- **Gate on merge:** U7 rewrites the AGENTS.md rule that Slack has "one job: answering @mentions". The person merging must approve that change explicitly in the PR.
- **For the requester:** should a Joined Thread reply from the person who started that thread count as addressed, so it can authorize changes? The plan's default, until answered, is no: every ambient turn is unaddressed (R16).

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Screen with OpenAI's Decisions API (`gpt-6-luna`) through the existing `decision.openai` provider.**
  - **Config:** the new file-config field `slack.ambient_decision_model` names the model. It is validated like `auto_approver_decision.model`: `provider/model`, with that provider configured.
  - **On switch:** the same field turns the feature on (R19).

  Governs R6, R7, R19. (session-settled: user-directed — chosen over a plain yes/no Responses call through the CLIProxyAPI proxy: the user supplied an OpenAI platform key, and the Decisions API answered in 0.12–0.4 s with a probability)
- KTD2. **The backend owns the decision call, behind one new `protocol.PrimaryTextRouter` method.**
  - **Why the backend:** `frontend/slack` does not import `harness`, while `backend` builds deciders (`newDeciders`) and reads runtime config.
  - **Contract:** the connector sends structured records; the backend calls `Decider.Decide` under its own 5-second timeout and returns skip or pass.
  - **Logging:** the backend writes the R7 log line.
  - **Rejected:** screening inside `SubmitMention`, which would mix a network call into the admission transaction's caller and hide a cost behind a dedupe API.
- KTD3. **One ambient metadata key on the inbound drives everything downstream.**
  - **Who sets it:** only the connector.
  - **Edits:** it is in `copyProvenance`'s list, so a Web edit removes it (R21) and a restored claim keeps it.
  - **What reads it:**
    - `SubmitMention` routing: Thread Session of `thread_ts`, or of `ts` for a top-level message, computed with an empty message timestamp as `moveRatcheted` does, with the Slack thread fact recorded;
    - the `unaddressed` provenance token (KTD8);
    - the deferred outbound flag (KTD6);
    - the tool set (KTD5, R17).
  - **Input IDs:** admission keeps `slack:<channel>:<ts>`. An edit carries its edit timestamp on the target, so the ID becomes `slack:<channel>:<ts>:<edited_ts>`.

  Governs R5, R9, R21.
- KTD4. **Screening runs inline on the serial Slack event loop, cheapest check first.**
  - **Order:**
    1. config-only gates;
    2. the stored channel fact for the channel name, falling back to `conversations.info` only when there is no fact;
    3. `MentionThread`, a database read;
    4. at most one `conversations.replies` read;
    5. the decision call.
  - **Failed thread read:** the thread counts as not joined unless the database already records it.
  - **Why inline:** it keeps per-thread order, and the 5-second cap bounds any stall. Measured latency was 0.12–0.86 s.
- KTD5. **`slack_no_reply` copies `slack_respond_in_thread`.**
  - **Tool:** a TurnBound custom tool registered only on ambient turns, saving a `/no-reply` turn step.
  - **Delivery:** `Bridge.finish` reads the step before `finishTurn` deletes turn steps, then clears the outbound text and attachments. The cleared outbound is what `outbound_json` stores, so a resend after a restart stays silent.
  - **Removed tools:** the same branch drops the R17 tools using the existing `DeleteFunc` pattern.
- KTD6. **A deferred outbound flag, mapped from the ambient key the way `ChannelQuiet` is.** For a deferred turn, the connector:
  - never enters it in `c.sessions`;
  - never writes `agents.sessions.setStatus`;
  - never calls `finishResponse`'s settle;
  - posts nothing when the outbound has no text and no files.

  So a mention waiting behind it keeps its title, initiator and `processing` status, and Slack Stop cannot reach the hidden turn. The same flag makes `markTurnEnd` skip silent turns (R18).
- KTD7. **The guidance (R11) is a developer context entry next to the Channel Session note in `Bridge` turn setup.** It is fixed text plus `wake_when` and `engagement` from runtime config when the turn is built. Context entries are saved with the turn, so a resumed turn keeps its guidance.
- KTD8. **Unaddressed is enforced in the harness, not only described.**
  - **Prompt:** `provenanceFromInbound` emits the canonical `unaddressed` token from the ambient key, so the prompt header and the review origin line both show it.
  - **Structured field:** the harness receives a structured unaddressed field beside `OriginHeader`, not by parsing text.
  - **Decision-model path:** in `judgeReviewAnswers`, the only allowed outcome for medium, high and critical risk is deny.
  - **Reviewer-model path:** an allow on medium risk or above becomes deny.
  - **Reviewer text:** `permissionReviewOriginRule` gains one sentence explaining the token.
  - **`allow` rules:** on a turn with the unaddressed field, an `allow` rule match is handled as `auto`, so the call reaches Permission Review.
  - **Spread:** the key is copied into:
    - the Task child inbound;
    - the shadow turn inbound built by `claimShadowTurn`;
    - the Background Job wake inbound built in `pickLaterWork`.

    On a Task child or shadow, the key only sets the unaddressed origin and the R17 tool removal; it registers no `slack_no_reply` and sets no deferred outbound. A wake turn is the ambient conversation's own turn, so the whole key applies. `sessions_write` needs no spread, because R17 removes it from ambient turns and subagents never get it.

  Governs R16. Follows the learning `docs/solutions/` `review-request-claiming-its-own-trust-reads-as-injection.md` on origin wording.
- KTD9. **A Joined Thread is decided only for replies (`thread_ts != "" && thread_ts != ts`).** A reply's thread is joined when either:
  - `MentionThread` reports `recorded && !report`;
  - the thread's root is this agent's marked Channel Session answer (`slackChannelReplyEvent`), the same check `handleAppMentionEvent` makes.

  Top-level messages are never joined. They reach the agent only as Watched Channel messages.
- KTD10. **Extend `docs/verification/slack-sticky/` instead of starting a new model.** Ambient intake changes the properties it already checks: `OneAdmission`, `HeldUntilRootSettles`, `OneReply`, `StatusClears` and `FooterMatchesSession`. A separate model would leave that VERIFIED result standing on assumptions that no longer hold (pack: formal-modeling, stateful-design.md).
- KTD11. **Context and screen records use one structured shape, JSON-encoded, under a RocketClaw-written preface.** Plain `U123: text` labels can be forged with a newline inside a message. Records keep who said what visible to the agent and the reviewer. Governs R6, R12.

### High-Level Technical Design

Intake and screening, one Slack `message` event on the serial loop:

```mermaid
stateDiagram-v2
    direction TB
    [*] --> Gate
    Gate --> Ignored: off, DM, bot or self, subtype, not allowed, mentions bot, parent redelivery
    Gate --> Edit: edited reply
    Gate --> Classify: new message
    Edit --> Admit: thread has a Thread Session
    Edit --> Ignored: unfurl, or no Thread Session
    Classify --> Ignored: report thread, unrecorded thread under a mention root
    Classify --> Screen: reply in a joined thread
    Classify --> Screen: message in a watched channel
    Classify --> Ignored: neither
    Screen --> Ignored: probability below 0.2
    Screen --> Admit: pass, error, refusal, timeout, incomplete context
    Admit --> Ignored: already admitted
    Admit --> Queued: Thread Session of the thread
    Ignored --> [*]
    Queued --> [*]
```

An ambient turn, which never writes Slack status:

```mermaid
stateDiagram-v2
    direction TB
    [*] --> Waiting: queued in Thread Queue
    Waiting --> Running: turn starts
    Running --> Posted: answer or files, with footer
    Running --> Silent: slack_no_reply, empty, failed, or stopped from Web
    Posted --> [*]
    Silent --> [*]
```

Components:

```mermaid
flowchart TB
    S[Slack message event] --> C[slack connector: gate, classify, records]
    C -->|screen| B[backend router: Decisions API, 5 s]
    B -->|pass or skip| C
    C -->|SubmitMention with ambient key| Q[Thread Queue of the Thread Session]
    Q --> T[Bridge turn: guidance, slack_no_reply, unaddressed origin]
    T --> R[harness Permission Review: deny above low risk]
    T --> F[Bridge.finish: no-reply step clears output]
    F --> D[connector delivery: no status, silent or posted]
```

### Model update and verification

`SlackSticky.tla` gains an `Ambient` source process behind a constant `AmbientOn`. Existing checks keep `AmbientOn = FALSE` and must give their current results. New checks set it to `TRUE` in a small world: one channel, one Joined Thread, one watched root, one thread under a mention root, and one Report Thread that receives plain replies.

- **Ambient actions (atomic labels):**
  - deliver a `message` copy of a mention, before or after its `app_mention`;
  - deliver a plain reply, a watched root, or an edit;
  - screen with a nondeterministic skip, pass or error, where error means pass;
  - admit through the existing `Submit` dedupe;
  - run a deferred turn that may call no-reply (a persisted step) and may crash and resume before delivery.
- **New invariants:**
  - `MentionNeverAmbient`
  - `NoAmbientInReport`
  - `NoAmbientUnderMentionRoot`
  - `AmbientInThread`
  - `SilentPostsNothing`
  - `AmbientWritesNoStatus`

  Existing `OneAdmission`, `HeldUntilRootSettles`, `OneReply`, `FooterMatchesSession` and `StatusClears` must also hold with `AmbientOn`.
- **Liveness:** `AmbientSettles`: every admitted ambient item finishes. It uses weak fairness on queue and delivery steps, as the model already assumes.
- **Must-fail mutations:**

  | Mutation | Expected violation |
  |---|---|
  | `ambient-mentions`: the ambient path handles mention-bearing text | `MentionNeverAmbient` |
  | `ambient-root-channel`: a watched root is routed to the Channel Session | `AmbientInThread` |
  | `ambient-mention-root`: a reply under an unrecorded mention root is admitted | `NoAmbientUnderMentionRoot` |
  | `no-reply-footer`: no-reply still posts the footer | `SilentPostsNothing` |
  | `volatile-no-reply`: the step is kept in memory only | `SilentPostsNothing` |
  | `deferred-session`: an ambient turn is tracked as a status session | `AmbientWritesNoStatus` |

- **Step to code:**

  | Model step | Code |
  |---|---|
  | Ambient deliver and gate | `frontend/slack` message handler (U4) |
  | Screen | backend screen method (U3) |
  | Admit | `SubmitMention` ambient routing (U3) |
  | No-reply step and delivery | `slack_no_reply`, `Bridge.finish` (U5, U6) |
  | Status | connector `SendResponse` deferred branch (U6) |

### System-Wide Impact

- **Web list:** every Watched Channel message that passes screening creates a listed Thread Session, even when the agent stays silent. This is expected, because Web is primary.
- **Unread marks:** silent ambient turns skip `markTurnEnd` (R18). Ambient turns that post mark readers as usual.
- **Shadow Sessions:** shadows of the channel's agent receive ambient inputs like any other turn's, and they run tools for real, unaddressed (KTD8). A passing message therefore costs one turn plus one per shadow. This is expected and documented.
- **Web mirror:** Web input that an ambient turn consumes is still reposted into the thread as "📡 web". This is expected.
- **Permission Review:** a new structured unaddressed field reaches the harness. `allow` matches on those turns are reviewed, and Task, shadow and wake origins carry the field (KTD8).
- **Data to the decision provider:** each eligible Joined Thread reply and Watched Channel message, and up to 10 earlier thread messages, go to OpenAI's Decisions API, including private-channel text and messages that end up skipped. The README discloses this.
- **Slack API volume:** each eligible reply costs at most one `conversations.replies` read. `conversations.info` runs only when no channel fact is stored (KTD4).

### Assumptions

These defaults were chosen without the user, because this run had no one to ask. Each is recorded so review can overturn it.

- The feature is opt-in through `slack.ambient_decision_model` (R19). The link also allows running without a decision model, but that would start unscreened turns in every mapped channel at upgrade.
- Edits reach Joined Threads without screening, as in the link (R4).
- When thread context cannot be read, the message passes, as in the link (R7).
- R16 denies medium risk and above. Low-risk actions, such as reads, stay allowed so ambient answers can look things up. Read tools such as `rocketclaw_sessions_get` stay available; their answer is guarded by R11 and KTD11 only.
- R17 removes the tools that act later or elsewhere without review.

### Sources

- Pattern: https://eve.dev/docs/patterns/ambient-listening (fetched 2026-11-24).
- Decisions API on a deployment:
  - the `decision.openai` key answered `gpt-6-luna` predicates in 0.12–0.86 s, with stable scores across two rounds;
  - CLIProxyAPI does not serve `/v1/decisions` (router-for-me/CLIProxyAPI#6586, #6917).
- Intake: `internal/rocketclaw/frontend/slack/connector.go`:
  - `handleEventsAPI`, `handleAppMentionEvent`, `recentRootMention`;
  - `slackThreadReplies`, `slackAdoptHistory`;
  - `SendResponse`, `sessionTurn`, `sendFooterOnlyResponse`.
- Admission and routing:
  - `internal/rocketclaw/backend/thread_bridges.go`: `slackSessionID`, `MentionThread`, `SubmitMention`;
  - `backend/bridge.go`: `moveRatcheted`, `respondInThreadTool`, `copyProvenance`, `newOutboundMessage`, `provenanceFromInbound`, `reviewOriginHeader`, Channel Session note, `finish`.
- Review: `internal/rocketclaw/harness/permission_review.go`: `judgeReviewAnswers`, `permissionReviewOriginRule`, outcome rules.
- Decisions:
  - `internal/rocketclaw/harness/decision.go`;
  - `backend/model_resolver.go` (`newDeciders`);
  - `config/config.go` (`validateDecision`).
- Learnings:
  - `docs/solutions/logic-errors/slack-thread-parent-message-redelivery-enqueued-second-turn.md`: every Slack entry point admits through `SubmitMention`, and `thread_ts == ts` is skipped;
  - `docs/solutions/architecture-patterns/slack-agent-session-status-follows-the-running-turn.md`;
  - `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`.
- Mentions-only history: `docs/plans/2026-10-08-1438-refactor-slack-convenience-surface-plan.md`.

---

## Implementation Units

### U1. Extend the slack-sticky model and run TLC

- **Goal:** prove the ambient design against the existing Slack properties before building it. This unit is pending until TLC passes.
- **Requirements:** R2, R3, R5, R9, R13, R14, R15. Governing decision: KTD10.
- **Dependencies:** none.
- **Files:**
  - `docs/verification/slack-sticky/SlackSticky.tla`
  - new `.cfg` files for the ambient checks and mutations
  - `docs/verification/slack-sticky/Makefile` (`PASS`, `NEGATIVE`, `EXPECTED_*`)
  - `docs/verification/slack-sticky/README.md`
- **Approach:**
  1. Add the `Ambient` process, the `AmbientOn` constant, the new `Mutation` values and the properties from "Model update and verification".
  2. Run `make translate`, then `make -k -C docs/verification/slack-sticky`.
  3. Fix each counterexample in the design, and carry any design change back into this plan's KTDs.
  4. Record the outcome, versions, state counts, bounds and run time in the README beside the existing record.
- **Execution note:** this unit gates U3 to U6. If TLC cannot run, does not finish, or fails, stop with BLOCKED / NOT VERIFIED.
- **Patterns to follow:** the existing processes, mutations and evidence record in `docs/verification/slack-sticky/`.
- **Test scenarios:**
  - Every existing `PASS` check still passes, and every existing `NEGATIVE` check still fails with its named violation.
  - The new ambient pass checks print `Model checking completed. No error has been found.`
  - Each of the six new mutations exits 12 or 13 with its named violation.
- **Verification:** the README gate status reads VERIFIED for the extended model, with the new evidence.

### U2. Configuration: ambient decision model and watched channels

- **Goal:** operators can turn the feature on and give channels goals.
- **Requirements:** R19, R20. Governing decision: KTD1.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/config/config.go`
  - `internal/rocketclaw/config/config_test.go`
  - `internal/rocketclaw/web/src/behavior-settings.tsx`
  - `internal/rocketclaw/web/src/config-page.browser.test.ts`
- **Approach:**
  1. Add `AmbientDecisionModel` (`ambient_decision_model`) to `SlackConfig`, as file config only. Validate it with the `auto_approver_decision.model` rule.
  2. Add `WakeWhen` (`wake_when`) and `Engagement` (`engagement`) to `SlackChannelConfig`. Trim both in `validateSlack`, and reject `wake_when` on the `@` row.
  3. Add two labelled text fields to `ChannelCard`, staying under react-doctor's complexity limit.
- **Patterns to follow:**
  - `validateDecision`;
  - the `ChannelCard` `Input` fields;
  - `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`.
- **Test scenarios:**
  - `slack.ambient_decision_model: openai/gpt-6-luna` with `decision.openai` configured loads.
  - Without `decision.openai`, loading fails with an error naming `slack.ambient_decision_model`.
  - An unqualified `gpt-6-luna` fails.
  - `wake_when` on the `@` row fails validation. On `#help`, it loads trimmed.
  - Browser test: entering a goal and an engagement on a channel card and saving persists both in the Behavior Settings JSON. The channel's other fields survive.
- **Verification:** config tests and the Config page browser test pass. `make lint` in `internal/rocketclaw/web` is clean.

### U3. Backend: screening and ambient admission

- **Goal:** the backend screens a message and admits it, at most once, to the right Thread Session.
- **Requirements:** R5, R6, R7, R9, R10, R21. Governing decisions: KTD1, KTD2, KTD3, KTD11.
- **Dependencies:** U1, U2.
- **Files:**
  - `internal/rocketclaw/protocol/conversation.go` (`PrimaryTextRouter`, `TextConversationTarget`)
  - `internal/rocketclaw/backend/thread_bridges.go`
  - `internal/rocketclaw/backend/thread_bridges_test.go`
  - `internal/rocketclaw/backend/bridge.go` (`copyProvenance` list)
  - `internal/rocketclaw/backend/conversations_test.go`
  - `internal/rocketclaw/frontend/slack/primarytextrouter_mocks_test.go` (regenerated)
  - `internal/rocketclaw/frontend/slack/inert_test.go`
- **Approach:**
  1. Add the screen method. Its implementation:
     - reads `slack.ambient_decision_model` and builds the decider from runtime config;
     - asks one predicate with the link's question texts in `Instructions`, and the R6 records as the typed `Input`;
     - applies the 5-second timeout;
     - returns skip only when `Probability < 0.2`;
     - writes the R7 log line.
  2. In `SubmitMention`, when the ambient key is present:
     - route to the Thread Session per KTD3, and record the Slack thread fact for a top-level message;
     - build the input ID from the edit timestamp when the target has one.
  3. Add the ambient key to `copyProvenance`'s list.
- **Patterns to follow:**
  - `moveRatcheted` for the Thread Session ID of a root;
  - the harness `decide` call shape;
  - mockery v3 via `make mocks`.
- **Test scenarios:**
  - Covers AE3. A fake decider at probability 0.13 returns skip. At 0.2 it returns pass.
  - A decider error, a refusal, and a call slower than 5 seconds each return pass.
  - A screen request marked incomplete-context returns pass without calling the decider, and logs that reason.
  - The decision request carries the question in its instructions and the records in its input. Message text never appears in the instructions.
  - Covers AE4. A top-level ambient message is admitted to `slack-thread:<ch>:<ts>:<agent>`. `SlackThread` for it returns the message's own timestamp, and no Channel Session item exists.
  - Covers AE2. An ambient admission then a mention with the same `slack:<ch>:<ts>` admits one item. The reverse order also admits one.
  - An edit with timestamp `E` is admitted as `slack:<ch>:<ts>:E` once. A second delivery is refused.
  - A Web edit of a queued ambient item leaves it without the ambient key. A restored claim keeps the key.
- **Verification:** backend tests pass, and regenerating the mocks leaves no diff.

### U5. Ambient turn framing: guidance, tools and unaddressed origin

- **Goal:** the agent knows why the message reached it and can stay silent. An overheard message cannot authorize a change, in this turn or in work it hands off.
- **Requirements:** R11, R12, R13, R16, R17, R21. Governing decisions: KTD5, KTD7, KTD8.
- **Dependencies:** U1, U3.
- **Files:**
  - `internal/rocketclaw/backend/bridge.go`
  - `internal/rocketclaw/backend/shadows.go`
  - `internal/rocketclaw/backend/active_turn_test.go`
  - `internal/rocketclaw/backend/bridge_test.go`
  - `internal/rocketclaw/backend/definitions_test.go`
  - `internal/rocketclaw/harness/permission_review.go`
  - `internal/rocketclaw/harness/permission_review_test.go`
  - `internal/rocketclaw/harness/tasks.go` (only if the Task child inbound is built there)
- **Approach:**
  1. When the inbound carries the ambient key:
     - append the R11 developer entry next to the Channel Session note;
     - register `slack_no_reply`, whose first description line states the whole contract;
     - drop the R17 tools.
  2. Add `slack_no_reply` to the permission defaults list and the definitions test lists, beside `slack_respond_in_thread`.
  3. Emit `unaddressed` from `provenanceFromInbound`, and pass the structured unaddressed field to the harness.
  4. Copy the ambient key into the Task child, shadow and wake inbounds (KTD8).
  5. In the harness, enforce KTD8 on both review paths and for `allow` matches, and add one sentence to `permissionReviewOriginRule`.
- **Patterns to follow:**
  - `respondInThreadTool` and its registration;
  - the Channel Session developer note;
  - `TestReviewOriginHeader`;
  - `docs/solutions/` guidance on Code Mode tool descriptions (`code-mode-host-tools-must-state-nested-argument-shapes.md`).
- **Test scenarios:**
  - `slack_no_reply` is offered on an ambient turn. It is not offered on a mention, a Web turn, or a Web-edited ambient item.
  - Calling `slack_no_reply` saves the step.
  - An ambient turn has no `schedule_message`, `reset_scheduled_messages` or `sessions_write`. A mention turn keeps them.
  - A Watched Channel turn's developer entry contains the configured `wake_when` and `engagement` and no message text. A Joined Thread turn has the thread guidance.
  - A resumed ambient turn keeps its original entry after `wake_when` changes.
  - `TestReviewOriginHeader` rows: an ambient turn, its Task child, its shadow turn, its Background Job wake, and its goal origin all contain `unaddressed`. A mention does not.
  - A Task child or shadow of an ambient turn is not offered `slack_no_reply`.
  - An agent with `edit: allow` makes a medium-risk edit on an ambient turn: the edit is reviewed and denied. The same edit on a mention turn runs unreviewed.
  - The guidance entry includes the R11 sentence about needing an @mention for changes.
  - Covers AE7, on the decision-model path: an unaddressed review with medium risk and outcome allow is a conflict that resolves to deny, never allow. With low risk, allow stands.
  - On the reviewer-model path: an unaddressed final answer of allow with medium risk becomes deny.
- **Verification:** backend and harness tests pass. One live Permission Review run with the real reviewer model on an unaddressed medium-risk write is recorded in the PR as extra evidence.

### U6. Delivery and status for deferred turns

- **Goal:** ambient turns are invisible in Slack except for their answer, and they never post a stray footer or mark tabs unread when silent.
- **Requirements:** R13, R14, R15, R18. Governing decisions: KTD5, KTD6.
- **Dependencies:** U1, U5.
- **Files:**
  - `internal/rocketclaw/protocol/types.go` (`OutboundMessage`)
  - `internal/rocketclaw/backend/bridge.go` (`newOutboundMessage`, `finish`)
  - `internal/rocketclaw/backend/store_dao.go` (`markTurnEnd` caller)
  - `internal/rocketclaw/frontend/slack/connector.go` (`SendResponse`, `AbortResponse`)
  - `internal/rocketclaw/frontend/slack/connector_test.go`
  - `internal/rocketclaw/backend/bridge_test.go`
- **Approach:**
  1. Add a deferred field to `OutboundMessage`, mapped from the ambient key.
  2. In `Bridge.finish`, a saved `/no-reply` step clears the text and attachments before `finishTurn`. A deferred outbound with nothing to post skips `markTurnEnd`.
  3. In the connector, a deferred outbound takes its own branch:
     - no `c.sessions` entry and no status call at turn start;
     - at the end, the answer, files and footer post when there is text or files, otherwise nothing;
     - never a settle.
- **Patterns to follow:**
  - the `ChannelQuiet` mapping and `TestChannelQuietFollowsWhatStartedTheTurn`;
  - the status sequence tests around `TestMentionTurnSetsAgentSessionStatus`.
- **Test scenarios:**
  - A deferred turn that answers posts the answer and footer. The stack server records no `agents.sessions.setStatus` call.
  - Covers AE4. A deferred turn whose `/no-reply` step is saved, even with text, posts nothing, writes no status, and marks no reader unread.
  - A deferred turn that ends empty, failed, or stopped from Web posts nothing.
  - A mention queued during a deferred turn keeps its `processing` status, title and initiator after the deferred turn posts. Slack Stop during the deferred turn leaves it running.
  - A no-reply step saved before a restart still suppresses delivery when the turn resumes. A resend of the saved outbound stays silent.
  - The wake turn after an ambient turn's Background Job writes no Slack status.
- **Verification:** connector and bridge tests pass.

### U4. Slack intake for `message` events

- **Goal:** eligible messages without a mention are gated, classified, screened and admitted.
- **Requirements:** R1 to R8, R12. Governing decisions: KTD3, KTD4, KTD9, KTD11.
- **Dependencies:** U1, U3, U5, U6.
- **Files:**
  - `internal/rocketclaw/frontend/slack/connector.go`
  - `internal/rocketclaw/frontend/slack/connector_test.go`
  - `internal/rocketclaw/frontend/slack/adhoc_callout_test.go` (only if shared helpers change)
- **Approach:**
  1. Add a `*slackevents.MessageEvent` case in `handleEventsAPI`.
  2. Gate in KTD4 order. Read the fields as follows:

     | Field | New message | `message_changed` |
     |---|---|---|
     | user, bot ID | `ev.User`, `ev.BotID` | `ev.Message.User`, `ev.Message.BotID` |
     | thread, ts | `ev.ThreadTimeStamp`, `ev.TimeStamp` | `ev.Message.ThreadTimestamp`, `ev.Message.Timestamp` |
     | edit | none | only when `ev.IsEdited()` and the edit timestamp is non-empty |
     | text, files | `ev.Text`, `ev.Message.Files` | `ev.Message.Text`, `ev.Message.Files` |

  3. Classify:
     - Report Thread → ignore;
     - a reply in a Joined Thread (KTD9) → screen with the thread question;
     - an unrecorded thread whose root mentions the bot → ignore (R3);
     - a message in a Watched Channel → screen with the goal question.

     Edits (R4) skip screening. A top-level edit counts only when its own Thread Session exists, looked up with an empty message timestamp as in KTD3.
  4. Build the R6 records and the R12 context from one `conversations.replies` read. Extend `slackAdoptHistory` to produce the structured records rather than adding a second formatter.
  5. Build the inbound with `mentionInbound`, set the ambient key, and submit. Set no intake status. Do not post an error reply on failure; log it instead.
- **Patterns to follow:**
  - `handleAppMentionEvent`'s gate, its `MentionThread` and Report Thread checks, and the marked-root check;
  - `recentRootMention`'s mention match, extended to `<@BOT|`;
  - `TestHandleAppMentionEventRedeliveryStartsOnce`.
- **Test scenarios:**
  - Rewrite `TestHandleEventsAPIIgnoresMessagesAndReactions` as a table. Each row starts nothing:
    - ambient off;
    - a 1:1 DM;
    - a bot message;
    - the agent's own message;
    - the agent's own checklist `message_changed`;
    - a `channel_join`;
    - a user not allowed in the channel;
    - `<@BOT>`;
    - `<@BOT|name>`;
    - a parent redelivery;
    - a reply in an unjoined thread of an unwatched channel;
    - a Report Thread reply;
    - a `message_changed` with no `edited` (unfurl);
    - an edit of a top-level message that was never admitted;
    - an edit in an unjoined thread;
    - an edit by a user who is not allowed;
    - a reply under an unrecorded mention root in a Watched Channel.
  - Covers AE1. A plain reply in a recorded thread is screened with the thread question and submitted with the ambient key. Its context holds the records since the agent's last post.
  - A reply under the agent's marked Channel Session answer counts as joined, and its context is the whole thread including that answer.
  - Covers AE3. A watched top-level message screened as skip submits nothing.
  - A watched top-level message that passes is submitted with `ThreadID == ts`.
  - An edited reply in a Joined Thread, and an edit of an admitted Watched Channel top-level message, each submit without calling screen, and the target carries the edit timestamp.
  - A `file_share` message's record shows `[file: name]`. A `thread_broadcast` is treated as a reply.
  - A failed thread read for a recorded thread calls screen marked incomplete-context, and the message passes with no decision call. For an unrecorded thread, the reply is not joined.
  - Covers AE2. `TestHandleAppMentionEventRedeliveryStartsOnce` and `TestThreadedSocialMentionHandledOnceAndStripped` keep their submit counts and status sequence: mention-bearing `message` copies stay skipped.
  - A message whose text contains a newline followed by `U0ALLOWED: …` appears as one record with its real author.
- **Verification:** connector tests pass.

### U7. Documentation and vocabulary

- **Goal:** the docs match the new Slack behavior and setup.
- **Requirements:** R22.
- **Dependencies:** U2 to U6.
- **Files:**
  - `README.md`
  - `CONCEPTS.md`
  - `AGENTS.md`
- **Approach:**
  1. README:
     - rewrite the Slack section's job statement and its "Only a mention…" and working-state paragraphs;
     - add the bot events `message.channels` and `message.groups` and the `groups:history` scope;
     - remove the line saying `message.*` can be removed;
     - document `slack.ambient_decision_model`, `wake_when` and `engagement`, the silence of ambient turns, `slack_no_reply`, the tools ambient turns lack, the first-agent limit of Joined Threads, the data sent to the decision provider, and that ambient turns send `allow` calls through review;
     - add a release note.
  2. CONCEPTS.md:
     - update Managed Slack Thread, Thread Session and Thread Queue;
     - add Joined Thread, Watched Channel and Ambient Turn. Avoid "followed", which names a Web tab state.
  3. AGENTS.md: rewrite the RocketClaw Slack line so both mentions and ambient messages drive `sticky: slack` agents. This is a gated change (Open Questions).
- **Test expectation:** none -- documentation only.
- **Verification:** a reread finds no remaining claim that only mentions start work, and no internal names.

---

## Risks

| Risk | Mitigation |
|---|---|
| The Go line budget (46250, about 846 lines free) is exceeded | Measure with `make -C internal/rocketclaw cloc` after U3 and U4. Reuse existing helpers. Never edit the budget. |
| The extended model's state space blows up | Ambient checks run in their own small world and cfgs, with bounds recorded. Existing checks keep their bounds. |
| Slack rate limits on `conversations.replies` make fail-open admit many turns | Accepted per the link. The R7 logs show `incomplete-context` counts, and changing that rule is a follow-up. |
| A thread message steers the screen to skip or pass | The screen gates cost only, never authority. R16 and R17 hold whatever the screen decides. |
| Text in thread context poses as the principal's request | KTD11 records with real authors, R11 guidance, and R16 enforcement in code. |
| A 5 s decision stall delays mentions | The KTD4 cap. Measured latency is well under 1 s. A slow provider stalls the loop once per eligible message; the R7 logs show it. |
| A revoked or over-quota decision key makes every screen fail open | Each eligible message then starts a full turn. The R7 logs show `error` outcomes, and removing `slack.ambient_decision_model` turns the feature off. |

---

## Operational Notes

After merge, on a deployment:

1. In the Slack app, subscribe to the bot events `message.channels` and `message.groups`, add the `groups:history` scope (`channels:history` is already granted), and reinstall the app.
2. Add `"ambient_decision_model": "openai/gpt-6-luna"` inside the `slack` object of the deployment's config, then restart. `decision.openai.api_key` must already be set. From then on, Joined Threads in every mapped channel are listened to, including threads answered before the upgrade.
3. Review the R7 screen logs and the agent's Joined Thread replies first. Then give one channel a `wake_when` on the Config page, and review again before watching more channels.

---

## Verification Contract

| Gate | Command or check | Applies to |
|---|---|---|
| Model | `make -k -C docs/verification/slack-sticky`, with every check at its expected result | U1, before U3 to U6 |
| Format | `gofmt` on touched Go files | U2 to U6 |
| Lint | `make lint` at repo root, and `make lint` in `internal/rocketclaw/web` | all code units |
| Tests | `make test` at repo root, which includes coverage at 91.8% and the line budget | all code units |
| Mocks | `make mocks` leaves no diff | U3 |
| Browser | the Config page browser test | U2 |
| Live reviewer | one recorded Permission Review run on an unaddressed medium-risk write | U5 |

---

## Definition of Done

- U1 is VERIFIED, with its evidence in `docs/verification/slack-sticky/README.md`. The model matches the built behavior.
- Every requirement R1 to R22 is covered by a passing test or a documented check.
- `make lint` and `make test` pass. The Go source line count stays under 46250, the budget the owner raised for this work; no further budget edit.
- No source comments are added, and no `nil` stands in for a disabled behavior dependency (AGENTS.md).
- README, CONCEPTS.md and AGENTS.md describe ambient listening. The AGENTS.md change is called out in the PR for approval.
- Code from abandoned attempts is removed from the diff.
