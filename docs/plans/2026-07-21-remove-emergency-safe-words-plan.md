# Remove Emergency Safe Words Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the `emergency_safe_words` configuration and the Slack process-exit behavior it controls.

**Architecture:** First update the normative RocketClaw behavior contract and stop for the repository's mandatory explicit ADR approval. After approval, delete the feature end to end from configuration, example data, application wiring, and Slack message handling without adding migration, rejection, warning, or replacement behavior.

**Tech Stack:** Go 1.26.2+, standard-library `encoding/json`, Slack connector, Jujutsu (`jj`), Make.

## Global Constraints

- Remove the emergency-safe-word capability completely while preserving existing Slack routing and managed-conversation stop controls.
- Do not change behavior-affecting code or tests until the ADR 0002 diff is visible and the human explicitly approves the ADR meaning change.
- Old `emergency_safe_words` JSON properties must remain harmlessly ignored by ordinary `json.Unmarshal`; do not add strict decoding, rejection, migration, warnings, or compatibility fields.
- Do not replace the feature or change existing `🛑` and `⏹️` managed-conversation stop controls.
- Keep Slack authorization, routing, message ordering, attachments, cron handling, and delivery unchanged.
- Make the smallest deletion-only production change; do not refactor adjacent code or add helpers, types, fields, callbacks, packages, instrumentation, or defensive checks.
- Use `jj`, never `git`; inspect diffs with `jj diff --git`.
- Do not edit `SOURCE_CLOC_BUDGET` or evade source, coverage, lint, or ownership metrics.
- Rocketable Platform targets Linux and macOS only; do not add Windows-specific behavior.
- Apply the repository's Go standards to every touched Go hunk before editing, before tests, and before final response.
- Run `gofmt` on touched Go files, then `go test ./...`, `make lint`, and `make test` before completion.

---

## File Map

- `internal/rocketclaw/docs/adr/0002-behavior-contracts.md`: remove the normative safe-word ordering sentence and append the approved changelog entry.
- `internal/rocketclaw/config/config.go`: remove the public config field, validation call, normalization helper, and now-unused `unicode` import.
- `internal/rocketclaw/config/config_test.go`: remove the dedicated normalization test.
- `internal/rocketclaw/rocketclaw.example.json`: remove the advertised example property.
- `internal/rocketclaw/app/app.go`: stop passing safe words into the Slack connector.
- `internal/rocketclaw/slackconnector/connector.go`: remove connector state, constructor input, message matching, process exit, and the now-unused `os` import. Keep `unicode` because `slackThinkingMessage` still uses `unicode.IsSpace`.
- `internal/rocketclaw/slackconnector/connector_test.go`: drop the removed argument from direct constructor calls without changing test behavior.
- `README.md`: no change; it contains no reference to the feature.

### Task 1: Update The Normative Behavior Contract

**Files:**
- Modify: `internal/rocketclaw/docs/adr/0002-behavior-contracts.md:96`
- Modify: `internal/rocketclaw/docs/adr/0002-behavior-contracts.md` append-only changelog

**Interfaces:**
- Consumes: Existing ADR 0002 Slack message-flow contract.
- Produces: Human-approved product contract in which emergency safe words and their ordering requirement no longer exist.

- [ ] **Step 1: Remove only the emergency-safe-word sentence**

Replace the complete Slack binding bullet with this exact plain-language text:

```markdown
- Slack binding: configured-channel thread replies that contain Slack-resolved direct pings to another user or bot, broadcast target, or user group must be skipped silently unless the same message also contains the RocketClaw bot mention. Slack channel references do not trigger this skip. Skipped replies must not create placeholders, reactions, connector replies, attachment processing, or thread-router submissions. Raw unresolved `@word` text and non-pinging Slack markup such as dates do not trigger this skip.
```

- [ ] **Step 2: Append the ADR changelog entry**

Append this exact entry without changing or reordering existing entries:

```markdown
- 2026-07-21: Removed emergency safe words and their process-exit behavior from Slack message handling.
```

- [ ] **Step 3: Inspect the ADR-only diff**

Run:

```bash
jj diff --git -- internal/rocketclaw/docs/adr/0002-behavior-contracts.md
```

Expected: The diff removes only `Emergency safe words are checked before this skip.` from the message-flow bullet and appends the one changelog entry. No code or test file is changed.

- [ ] **Step 4: Stop for explicit ADR meaning approval**

Show the diff to the human and ask exactly:

```text
Do you explicitly approve these ADR meaning changes?
```

Do not continue unless the human answer explicitly approves the ADR meaning change, such as `I approve these ADR changes` or `approved ADR wording`. A generic `proceed`, `go ahead`, `sounds good`, or approval of this implementation plan does not satisfy the gate.

- [ ] **Step 5: Commit the approved ADR change**

After explicit approval, run:

```bash
jj status
jj diff --git
jj log -n 10 --no-graph -T 'commit_id.short() ++ " " ++ description.first_line() ++ "\n"'
jj commit -m "docs: remove emergency safe word contract"
```

Expected: The commit contains only `internal/rocketclaw/docs/adr/0002-behavior-contracts.md`, and `jj` creates a new empty working-copy change.

### Task 2: Delete The Complete Feature Surface

**Files:**
- Modify: `internal/rocketclaw/config/config.go:4-29,277-287,478-512`
- Modify: `internal/rocketclaw/config/config_test.go:216-222`
- Modify: `internal/rocketclaw/rocketclaw.example.json:15-17`
- Modify: `internal/rocketclaw/app/app.go:369-372`
- Modify: `internal/rocketclaw/slackconnector/connector.go:4-20,73-83,145-153,1358-1370`
- Modify: `internal/rocketclaw/slackconnector/connector_test.go:317,338-343`

**Interfaces:**
- Consumes: The explicitly approved ADR 0002 contract from Task 1 and existing `slackconnector.New` callers.
- Produces: `func New(cfg *config.SlackConfig, bus *events.Bus, threadRouter harnessbridge.PrimaryTextRouter, oneOffCronjobs primarytext.OneOffCronjobRunner, answerQuestion func(context.Context, string, events.AskUserQuestionAnswer) bool, logger *slog.Logger) *Connector`; a `Config` without `EmergencySafeWords`; ordinary Slack routing with no safe-word hard-exit branch.

- [ ] **Step 1: Establish the focused baseline**

Run:

```bash
go test ./internal/rocketclaw/config ./internal/rocketclaw/slackconnector ./internal/rocketclaw/app
```

Expected: All three packages report `ok`. If the baseline fails, stop and diagnose the pre-existing failure before editing.

- [ ] **Step 2: Remove the configuration field and normalization**

In `internal/rocketclaw/config/config.go`, remove the `unicode` import, remove this field:

```go
EmergencySafeWords []string `json:"emergency_safe_words,omitempty"`
```

remove this validation statement:

```go
c.EmergencySafeWords = normalizeEmergencySafeWords(c.EmergencySafeWords)
```

and delete the complete `normalizeEmergencySafeWords` function. The surrounding `Config` fields, `Validate` ordering, and `normalizeStrings` function remain unchanged.

- [ ] **Step 3: Remove the dedicated deleted-behavior test**

Delete this complete test from `internal/rocketclaw/config/config_test.go`:

```go
func TestValidateNormalizesEmergencySafeWords(t *testing.T) {
	cfg := validConfig()
	cfg.EmergencySafeWords = []string{"  Red Button! ", "red-button", "Angstrom 42", "!!!", ""}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []string{"redbutton", "angstrom42"}, cfg.EmergencySafeWords)
}
```

Do not add a stale-key rejection test or any other test for removed behavior.

- [ ] **Step 4: Remove the example configuration property**

Delete this complete property from `internal/rocketclaw/rocketclaw.example.json`, preserving valid JSON and the neighboring `models` and `logging` properties:

```json
  "emergency_safe_words": [
    "stop"
  ],
```

- [ ] **Step 5: Remove application-to-connector wiring**

In `internal/rocketclaw/app/app.go`, replace the constructor call with:

```go
slackSink = slackconnector.New(&cfg.Slack, bus, threadBridges, cronjobs, questionBroker.answer, logger)
```

- [ ] **Step 6: Remove Slack connector state and matching**

In `internal/rocketclaw/slackconnector/connector.go`:

1. Remove the `os` import. Keep `unicode` because line-level thinking formatting still uses `unicode.IsSpace`.
2. Remove `emergencySafeWords []string` from `Connector`.
3. Change `New` to this exact signature:

```go
func New(cfg *config.SlackConfig, bus *events.Bus, threadRouter harnessbridge.PrimaryTextRouter, oneOffCronjobs primarytext.OneOffCronjobRunner, answerQuestion func(context.Context, string, events.AskUserQuestionAnswer) bool, logger *slog.Logger) *Connector {
```

4. Replace the dependency initialization line with:

```go
threadRouter: threadRouter, oneOffCronjobs: oneOffCronjobs,
```

5. Delete this complete branch from `handleMessageEvent`:

```go
normalizedText := strings.Map(func(r rune) rune {
	switch {
	case unicode.IsLetter(r):
		return unicode.ToLower(r)
	case unicode.IsDigit(r):
		return r
	default:
		return -1
	}
}, text)
if slices.Contains(c.emergencySafeWords, normalizedText) {
	os.Exit(254)
}
```

The next statement must remain the social-thread ping suppression check:

```go
if socialThreadReply && c.slackSocialThreadReplyPingsAway(rawText) {
```

In `internal/rocketclaw/slackconnector/connector_test.go`, remove only the obsolete safe-words `nil` argument from both existing `New` calls. Do not change either test's behavior.

- [ ] **Step 7: Format and run focused tests**

Run:

```bash
gofmt -w internal/rocketclaw/config/config.go internal/rocketclaw/config/config_test.go internal/rocketclaw/app/app.go internal/rocketclaw/slackconnector/connector.go
go test ./internal/rocketclaw/config ./internal/rocketclaw/slackconnector ./internal/rocketclaw/app
```

Expected: `gofmt` produces no errors and all three packages report `ok`.

- [ ] **Step 8: Prove complete first-party removal**

Search first-party paths, excluding the approved design and implementation-plan records that intentionally describe the feature:

```bash
rg -n --glob '!vendor/**' --glob '!docs/plans/**' 'emergency_safe_words|EmergencySafeWords|emergencySafeWords|emergency safe words|os\.Exit\(254\)' .
```

Expected: No matches. The implementation plan under `docs/plans/` is excluded because it intentionally describes the removed feature.

- [ ] **Step 9: Review the touched Go diff against repository standards**

Run:

```bash
jj diff --git
```

Expected review results:

- Every production change is a deletion or direct constructor-call adjustment required by the feature removal.
- No single-use helper, delegating wrapper, defensive guard, abstraction, exported symbol, context storage, callback, goroutine, timer, mutex, or instrumentation was added.
- No error variable or error type was added or renamed.
- The Slack message-flow statement after deletion is `slackSocialThreadReplyPingsAway`; routing order after it is unchanged.
- `slackconnector.New` and its only production caller use the same exact signature.
- The `unicode` import remains in `connector.go` for `unicode.IsSpace`; the `os` import is gone.
- The `unicode` import is gone from `config.go`.
- The diff has net source deletion and does not touch `SOURCE_CLOC_BUDGET`.

- [ ] **Step 10: Run all mandatory verification**

Run each command separately and retain its exit status:

```bash
go test ./...
make lint
make test
```

Expected: Every command exits 0. If any command fails, fix only failures caused by this removal and rerun all three commands. If a required command cannot run, stop and report the blocker instead of claiming success.

- [ ] **Step 11: Confirm documentation impact and working-copy scope**

Run:

```bash
rg -n 'emergency_safe_words|EmergencySafeWords|emergencySafeWords|emergency safe words' README.md
jj status
jj diff --git
```

Expected: README search has no matches. `jj status` and `jj diff --git` show only the intended config, example, app, Slack connector, and config-test deletions because the ADR change was committed separately in Task 1.

- [ ] **Step 12: Commit the implementation**

Run:

```bash
jj status
jj diff --git
jj log -n 10 --no-graph -T 'commit_id.short() ++ " " ++ description.first_line() ++ "\n"'
jj commit -m "internal/rocketclaw: remove emergency safe words"
```

Expected: The commit contains exactly the six implementation/test/example files listed for Task 2, and `jj` creates a new empty working-copy change.
