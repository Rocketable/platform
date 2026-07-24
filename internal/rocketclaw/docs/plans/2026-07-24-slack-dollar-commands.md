# Canonical Slack Dollar Commands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make dollar commands the sole internal Slack command grammar, translate emoji controls into that grammar, and remove the obsolete `primarytext` package.

**Architecture:** Slack transport normalization converts native dollar input and supported emoji or colon aliases into canonical `$goal`, `$stop`, `$cron`, and `$agent` text. One parser and one context-specific switch dispatch every command; goal grammar receives command arguments rather than emoji-prefixed text. Remaining Slack-only helpers move out of the historical `primarytext` package into private `slackconnector` ownership.

**Tech Stack:** Go 1.26.2+, `slack-go/slack`, `testify`, Jujutsu (`jj`)

## Global Constraints

- The canonical flow is `Slack emoji or colon alias -> canonical dollar text -> command parser -> context-specific dispatch`.
- Dollar commands never translate into emoji syntax.
- Support `$command` and `$ command` with case-insensitive command names.
- Preserve the accepted `🏁`/`🔁`, `🛑`/`⏹️`, `🔂`, and `🎛` controls and their Slack colon aliases.
- Emoji and dollar forms must produce identical stored objective text, queue behavior, placeholders, routing, and feedback.
- Managed threads allow all four commands; root app mentions allow only goal and cron.
- Bare `$`, unknown commands, unavailable commands, and `$stop` with arguments produce ephemeral help and are never submitted as prompts.
- Ephemeral command help uses a Block Kit table; root app-mention help omits `thread_ts` because no active thread exists yet.
- Goal validation messages use canonical `$goal` examples.
- Delete `internal/rocketclaw/primarytext`; keep its remaining behavior private to `slackconnector`.
- Do not alter app, cronjob, events, configuration, or externally visible connector behavior beyond canonical goal error examples and emoji/dollar parity.
- Add no new dependency, exported API, registry, callback, defensive impossible-state guard, context field, or one-line delegating wrapper.
- Error variables start with `err`; use `jj`, never `git`; inspect diffs with `jj diff --git`.
- README impact is resolved: update `cmd/rocketclaw/CHEATSHEET.md`, not the repository README.

---

### Task 1: Canonicalize And Dispatch Commands

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:1717-1822,1977-2039,2429-2465`
- Modify: `internal/rocketclaw/slackconnector/connector_test.go:334-410,5035-5065,5095-5138,5419-5577,5890-6117`
- Modify: `internal/rocketclaw/harnessbridge/goal_request.go:17-78`
- Modify: `internal/rocketclaw/harnessbridge/goal_request_test.go:10-74`

**Interfaces:**
- Produces: `canonicalSlackCommand(text string) (canonical string, ok bool)`
- Retains: `slackDollarCommand(text string) (command, args string, ok bool)` as the sole command parser
- Changes: `harnessbridge.ParseGoalRequest(text string) (GoalRequest, string)` parses goal arguments after command recognition

- [ ] **Step 1: Add failing canonical-normalization tests**

Replace direct Slack transport/goal parser tests with a table that proves the exact canonical direction:

```go
func TestCanonicalSlackCommand(t *testing.T) {
	for _, tt := range []struct {
		name, text, want string
		ok               bool
	}{
		{name: "native attached", text: "$goal ship it", want: "$goal ship it", ok: true},
		{name: "native spaced", text: "$ goal ship it", want: "$ goal ship it", ok: true},
		{name: "goal flag", text: "🏁 ship it", want: "$goal ship it", ok: true},
		{name: "goal repeat", text: "🔁 ship it", want: "$goal ship it", ok: true},
		{name: "goal alias", text: ":checkered_flag: ship it", want: "$goal ship it", ok: true},
		{name: "stop sign", text: "🛑", want: "$stop", ok: true},
		{name: "stop button", text: "⏹️", want: "$stop", ok: true},
		{name: "cron", text: "🔂 cron/daily.md", want: "$cron daily", ok: true},
		{name: "cron alias", text: ":repeat_one: daily", want: "$cron daily", ok: true},
		{name: "agent", text: "🎛 planner", want: "$agent planner", ok: true},
		{name: "agent alias", text: ":control_knobs: planner", want: "$agent planner", ok: true},
		{name: "ordinary", text: "ship it"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := canonicalSlackCommand(tt.text)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

Retain the existing `TestSlackDollarCommand` cases for lexical parsing.

- [ ] **Step 2: Change goal grammar tests to canonical arguments**

Remove emoji prefixes from every `harnessbridge.ParseGoalRequest` fixture and update calls to two return values:

```go
goal, rejection := ParseGoalRequest("maxTurns: 20 update the docs")
require.Empty(t, rejection)
assert.Equal(t, 20, goal.MaxTurns)
```

Add malformed-argument assertions that require `$goal` in the visible examples:

```go
_, rejection := ParseGoalRequest("")
assert.Contains(t, rejection, "`$goal`")

_, rejection = ParseGoalRequest("maxTurns: 5")
assert.Contains(t, rejection, "`$goal maxTurns: 5 update the docs`")
```

- [ ] **Step 3: Run focused tests and verify RED**

Run: `go test ./internal/rocketclaw/slackconnector ./internal/rocketclaw/harnessbridge -run 'Test(CanonicalSlackCommand|ParseGoalRequest)' -count=1`

Expected: FAIL because `canonicalSlackCommand` does not exist and `ParseGoalRequest` still expects emoji triggers and returns three values.

- [ ] **Step 4: Make goal parsing command-agnostic**

Change `ParseGoalRequest` to parse only its argument string:

```go
// ParseGoalRequest parses canonical $goal arguments.
func ParseGoalRequest(text string) (GoalRequest, string) {
	text = strings.TrimSpace(text)
	maxTurns := 5
	checkScript := ""

	if text == "" {
		return GoalRequest{}, "Tell me the goal after `$goal`, for example `$goal maxTurns: 5 update the docs`."
	}

	for {
		text = strings.TrimSpace(text)
		if text == "" {
			return GoalRequest{}, "Tell me the goal after the parameters, for example `$goal maxTurns: 5 update the docs`."
		}

		if after, ok := strings.CutPrefix(text, "maxTurns:"); ok {
			fields := strings.Fields(after)
			if len(fields) == 0 {
				return GoalRequest{}, "`maxTurns:` needs a value like `20`, `0`, `-1`, or `infinite`."
			}

			value := strings.ToLower(fields[0])
			switch value {
			case "infinite":
				maxTurns = 0
			default:
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < -1 {
					return GoalRequest{}, "`maxTurns:` must be a positive integer, `0`, `-1`, or `infinite`."
				}
				maxTurns = max(parsed, 0)
			}

			text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(after), fields[0]))
		} else if after, ok := strings.CutPrefix(text, "checkScript:"); ok {
			value, rest, err := consumeGoalCheckScriptValue(strings.TrimSpace(after))
			if err != nil {
				return GoalRequest{}, err.Error()
			}
			checkScript = value
			text = rest
		} else {
			return GoalRequest{Objective: strings.TrimSpace(text), CheckScript: checkScript, MaxTurns: maxTurns}, ""
		}
	}
}
```

Do not add trigger detection to `harnessbridge`; command recognition belongs to Slack dispatch.

- [ ] **Step 5: Implement Slack canonicalization**

Add one private normalizer near `slackDollarCommand`. It must first preserve native dollar text, then translate only accepted emoji forms:

```go
func canonicalSlackCommand(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if _, _, ok := slackDollarCommand(text); ok {
		return text, true
	}

	canonicalEmoji := emoji.CanonicalizeLeadingAlias(text)
	if after, ok := strings.CutPrefix(canonicalEmoji, "🔁"); ok {
		return "$goal " + strings.TrimSpace(after), true
	}
	if after, ok := strings.CutPrefix(canonicalEmoji, "🏁"); ok {
		return "$goal " + strings.TrimSpace(after), true
	}
	if target, ok := slackOnDemandCronTarget(canonicalEmoji); ok {
		return "$cron " + target, true
	}
	after, isAgent := strings.CutPrefix(canonicalEmoji, "🎛️")
	if !isAgent {
		after, isAgent = strings.CutPrefix(canonicalEmoji, "🎛")
	}
	if isAgent {
		if after != "" {
			r, size := utf8.DecodeRuneInString(after)
			if !unicode.IsSpace(r) {
				return "", false
			}
			after = after[size:]
		}
		return strings.TrimSpace("$agent " + strings.TrimSpace(after)), true
	}
	switch canonicalEmoji {
	case "🛑", "⏹️":
		return "$stop", true
	}

	return "", false
}
```

This preserves the existing agent boundary behavior, including rejected `🎛sudo` and accepted Slack colon aliases, without adding another one-use parser.

- [ ] **Step 6: Replace parallel managed-thread routing with one canonical switch**

After loading the managed thread, normalize and parse once:

```go
var goal harnessbridge.GoalRequest
rejection := ""
isGoal := false

canonical, isCommand := canonicalSlackCommand(text)
if isCommand {
	if !handled {
		return
	}

	command, args, _ := slackDollarCommand(canonical)
	switch command {
	case "agent":
		c.handleSlackSocialAgentSwitch(ctx, ev.Channel, threadTS, ev.User, socialChannelName, args)
		return
	case "cron":
		c.handleOnDemandCronRequest(ctx, args, replyTarget)
		return
	case "stop":
		if args != "" {
			c.postSlackEphemeral(ctx, ev.Channel, threadTS, ev.User, slackDollarCommandHelp, slackDollarCommandHelpTable())
			return
		}
		if err := c.stopSlackThread(ctx, ev.Channel, threadTS); err != nil {
			c.log.Error("stop Slack goal thread", "error", err, "channel", ev.Channel, "thread_ts", threadTS)
		}
		return
	case "goal":
		goal, rejection = harnessbridge.ParseGoalRequest(args)
		isGoal = true
	default:
		c.postSlackEphemeral(ctx, ev.Channel, threadTS, ev.User, slackDollarCommandHelp, slackDollarCommandHelpTable())
		return
	}
}
```

After the switch, retain the existing shared goal rejection/start block, but set `content.Text = goal.Objective` unconditionally before buffering. Delete the later emoji-specific agent, cron, stop, and goal detection branches.

- [ ] **Step 7: Replace parallel app-mention routing with one canonical switch**

After mention removal and authorization, normalize and parse once. Root context accepts only cron and goal:

```go
var goal harnessbridge.GoalRequest
rejection := ""
isGoal := false

canonical, isCommand := canonicalSlackCommand(text)
if isCommand {
	command, args, _ := slackDollarCommand(canonical)
	switch command {
	case "cron":
		c.handleOnDemandCronRequest(ctx, args, replyTarget)
		return
	case "goal":
		goal, rejection = harnessbridge.ParseGoalRequest(args)
		isGoal = true
	default:
		c.postSlackEphemeral(ctx, ev.Channel, "", ev.User, slackDollarCommandHelp, slackDollarCommandHelpTable())
		return
	}
}
```

Non-command text must continue into the ordinary thread-start flow. Keep the existing shared goal rejection, placeholder selection, attachment handling, prompt selection, and goal start block unchanged after the switch.

- [ ] **Step 8: Strengthen behavioral parity assertions**

Run emoji and dollar subcases through the same goal test scaffolding and assert for each:

```go
assert.Equal(t, "fix lint", router.goalStarts[i].objective)
assert.Equal(t, "fix lint", router.goalStarts[i].inbound.Text)
assert.NotContains(t, router.goalStarts[i].inbound.Text, "$")
assert.NotContains(t, router.goalStarts[i].inbound.Text, "🏁")
```

Retain the existing paired stop, agent switch, selector, and root cron tests. Ensure their expected side effects are identical for emoji and dollar subcases.

- [ ] **Step 9: Format, test, review, and commit**

Run: `gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/harnessbridge/goal_request.go internal/rocketclaw/harnessbridge/goal_request_test.go`

Run: `go test ./internal/rocketclaw/slackconnector ./internal/rocketclaw/harnessbridge -count=1`

Expected: PASS.

Run: `go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/harnessbridge/goal_request.go internal/rocketclaw/harnessbridge/goal_request_test.go`

Expected: no diagnostics.

Run: `jj diff --git`

Verify there is no `$goal -> emoji` conversion, no parallel emoji dispatch, no command syntax in goal inbound text, and no unrelated change.

Run: `jj commit -m "internal/rocketclaw: make dollar commands canonical"`

---

### Task 2: Delete The Obsolete Primarytext Package

**Files:**
- Create: `internal/rocketclaw/slackconnector/text.go`
- Modify: `internal/rocketclaw/slackconnector/connector.go:22-31,77-85,158-165,237-247,425-445,1375-1407,2495-2525`
- Modify: `internal/rocketclaw/slackconnector/connector_test.go:30-40,221-248,3170-3195,4040-4070,6740-6765`
- Delete: `internal/rocketclaw/primarytext/primarytext.go`
- Delete: `internal/rocketclaw/primarytext/primarytext_test.go`

**Interfaces:**
- Produces: private `oneOffCronjobRunner` interface in `slackconnector`
- Produces: `splitSlackText(text string, preferredLimit, hardLimit int) []string`
- Produces: `slackGoalProgressText(turnNumber, maxTurns int) string`
- Removes: package `internal/rocketclaw/primarytext`

- [ ] **Step 1: Redirect Slack tests to private helper names and verify RED**

Remove the `primarytext` test import. Change existing assertions to call:

```go
splitSlackText(text, slackPreferredChunkSize, slackTextLimit)
slackGoalProgressText(turnNumber, maxTurns)
```

Change the test connector constructor parameter to `oneOffCronjobRunner`.

Run: `go test ./internal/rocketclaw/slackconnector -run 'Test(SplitSlackResponseTextBoundaries|ProgressTextMessageQuotesAndBoundsText)' -count=1`

Expected: build failure because the private helpers and interface do not exist.

- [ ] **Step 2: Add private Slack text helpers without the generic callback layer**

Create `text.go` with `slackGoalProgressText`, `splitSlackText`, `slackChunkEnd`, and `slackBoundary`. Inline the old `splitText` loop directly into `splitSlackText`:

```go
func splitSlackText(text string, preferredLimit, hardLimit int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	chunks := make([]string, 0, len(runes)/preferredLimit+1)
	for len(runes) > 0 {
		if len(runes) < hardLimit {
			chunks = append(chunks, string(runes))
			break
		}

		end := slackChunkEnd(runes, preferredLimit, hardLimit)
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks
}
```

Move the exact existing boundary and goal progress behavior; do not redesign chunking.

- [ ] **Step 3: Make the cron dependency interface private**

Add near `Connector`:

```go
type oneOffCronjobRunner interface {
	LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error)
	RunOneOffCronjob(context.Context, cronjob.OneOffCronjob, *harnessbridge.RawRunProgress, func(context.Context, cronjob.RunResult, error))
}
```

Change `Connector.oneOffCronjobs`, `New`, and test helpers from `primarytext.OneOffCronjobRunner` to `oneOffCronjobRunner`. The app call remains source-compatible through structural interface satisfaction and must not be edited.

- [ ] **Step 4: Replace all helper call sites**

Replace every `primarytext.SplitSlackText` with `splitSlackText` and every `primarytext.GoalProgressText` with `slackGoalProgressText`. Remove the `primarytext` import.

Run: `go test ./internal/rocketclaw/slackconnector -run 'Test(SplitSlackResponseTextBoundaries|ProgressTextMessageQuotesAndBoundsText)' -count=1`

Expected: PASS.

- [ ] **Step 5: Inline one-off cron orchestration into its only caller**

In `runOnDemandCron`, retain its existing `publish` closure, then inline the exact behavior formerly owned by `primarytext.RunOneOffCronjob`:

```go
thinking := ""
progress := &harnessbridge.RawRunProgress{
	Thinking: func(ctx context.Context, text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		if thinking != "" {
			thinking += "\n"
		}
		thinking += text
		return publish(ctx, "", thinking, false, false, nil)
	},
	Message: func(ctx context.Context, text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		return publish(ctx, text, "", false, true, nil)
	},
}
```

Call `c.oneOffCronjobs.RunOneOffCronjob` directly. Preserve failure text, empty-result fallback, attachments, delivery waiting, publish-error logging, and registration only after successful final delivery.

- [ ] **Step 6: Delete `primarytext` and verify behavior**

Delete both files under `internal/rocketclaw/primarytext` after all call sites are gone.

Run: `go test ./internal/rocketclaw/slackconnector -run 'Test(SplitSlackResponseTextBoundaries|ProgressTextMessageQuotesAndBoundsText|HandleMessageEventReportsOnDemandCronRunFailure|RunOnDemandCronIgnoresBlankProgressAndPublishesEmptyResultFallback|RunOnDemandCronRegistersOnlyAfterFinalDelivery)' -count=1`

Expected: PASS.

Run: `test ! -d internal/rocketclaw/primarytext`

Expected: exit status 0, proving the obsolete package directory is gone.

- [ ] **Step 7: Format, inspect package boundaries, and commit**

Run: `gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/slackconnector/text.go`

Run: `go test ./internal/rocketclaw/slackconnector -count=1`

Expected: PASS.

Run: `go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/slackconnector/text.go`

Expected: no diagnostics.

Run: `jj diff --git`

Verify `primarytext` is deleted, only `slackconnector` owns the moved behavior, the app and cronjob packages are untouched, and no one-use wrapper remains.

Run: `jj commit -m "internal/rocketclaw: remove obsolete primarytext package"`

---

### Task 3: Correct Documentation And Verify The Repository

**Files:**
- Modify: `cmd/rocketclaw/CHEATSHEET.md:3-33`

**Interfaces:**
- Consumes: canonical command behavior from Task 1
- Consumes: private Slack ownership from Task 2
- Produces: user and maintainer documentation matching production behavior

- [ ] **Step 1: Make canonical status explicit in the cheatsheet**

Keep the existing control table and operational details, but describe the `Dollar Command` column as canonical and emoji as aliases. Add this sentence before or after the table:

```markdown
Dollar commands are canonical. RocketClaw translates the listed emoji and Slack aliases into the corresponding dollar command before dispatch.
```

Retain marker-only stop feedback, exact-message/reaction scope, selector requester restrictions, and the statement that command controls do not route to RocketCode prompts.

- [ ] **Step 2: Remove stale reverse-adapter language from project docs**

Search the affected docs and code for reverse translation and stale package references:

Run: `rg -n 'goalText = "🏁|dollar-to-emoji|primarytext|emoji-driven flows|Existing Control' internal/rocketclaw cmd/rocketclaw/CHEATSHEET.md`

Expected after edits: no production reverse adapter, no current design/plan claim that emoji is canonical, and no Go import or package declaration for `primarytext`. Historical unrelated plans may retain historical references.

- [ ] **Step 3: Perform the mandatory Go standards pass**

Inspect the complete changed diff for error-variable naming, error type names, single-use helpers, one-line delegating wrappers, defensive guards, exported symbols, context storage, injected nil behavior, synchronization growth, command order, queue semantics, prompt framing, outbound routing, and source CLOC impact. Remove any violation before running tests.

Run: `gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/slackconnector/text.go internal/rocketclaw/harnessbridge/goal_request.go internal/rocketclaw/harnessbridge/goal_request_test.go`

- [ ] **Step 4: Run focused canonical-direction verification**

Run: `go test ./internal/rocketclaw/slackconnector ./internal/rocketclaw/harnessbridge -run 'Test(CanonicalSlackCommand|SlackDollarCommand|ParseGoalRequest|HandleMessageEvent.*Goal|HandleMessageEventStop|HandleMessageEventSwitches|HandleMessageEventShowsManaged|HandleAppMentionEvent|SlackOnDemandCronTarget)' -count=1`

Expected: PASS.

Run: `go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go internal/rocketclaw/slackconnector/text.go internal/rocketclaw/harnessbridge/goal_request.go internal/rocketclaw/harnessbridge/goal_request_test.go`

Expected: no diagnostics.

- [ ] **Step 5: Run all repository gates**

Run: `go test ./...`

Expected: PASS.

Run: `make lint`

Expected: PASS with zero issues and no suppression or configuration change.

Run: `make test`

Expected: PASS, including race tests, coverage budget, code metrics, and all CLOC budgets.

- [ ] **Step 6: Final semantic review and commit**

Run: `jj diff --git`

Verify every revised-spec invariant explicitly: one-way canonicalization, one parser/dispatch path, exact emoji/dollar parity, canonical goal errors, ephemeral privacy, managed/root context restrictions, deleted `primarytext`, unchanged README, and no unrelated edits.

Run: `jj status`

Run: `jj commit -m "internal/rocketclaw: document canonical dollar commands"`

Run: `jj status`

Expected: clean working copy above the completed implementation.
