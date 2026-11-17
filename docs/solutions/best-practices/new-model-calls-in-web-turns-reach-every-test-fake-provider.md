---
title: A New Model Call in Web Turns Reaches Every Test Fake Provider
date: 2026-11-17
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - Adding a model request that a Web turn makes besides the turn itself, such as a title, summary, tag, or classifier call
  - Starting work from Bridge.runTurn in the background, outside the turn's own errgroup
  - Many backend or frontend/rpc tests fail or hang after a change that adds a model request to every first Web turn
symptoms:
  - About 15 backend tests failed and one hung until the 40-minute go test timeout, so tests after it never ran
  - Fakes that script replies by call index or request count returned the wrong reply, or a test waited on a reply the extra request had used
  - Fakes that decode the request body into a fixed shape failed on a request whose input is a plain string
  - frontend/rpc live and browser tests whose fake provider speaks the Responses WebSocket protocol got an unexpected first message
root_cause: test_isolation
resolution_type: test_fix
related_components:
  - internal/rocketclaw/frontend/rpc
  - internal/rocketclaw/harness
  - internal/rocketclaw/config
tags: [fake-provider, httptest, websocket, session-title, background-work, bridge, test-fixtures, model-call]
---

# A New Model Call in Web Turns Reaches Every Test Fake Provider

## Context

Automatic session titles added a second model request to a person's first Web message: `Bridge.runTurn` starts `harness.GenerateTitle` in the background while the turn runs (`internal/rocketclaw/backend/bridge.go:2214-2225`). Most backend tests create `web_chat` conversations and submit human Web prompts, so every one of them now sent that extra request to its fake provider.

The first full backend run had about 15 failures and one hang until the 40-minute timeout, so the true count was unknown. Roughly 30 fake provider servers needed changes across `internal/rocketclaw/backend` and `internal/rocketclaw/frontend/rpc`. They break in three ways:

- **Scripted by order.** Many fakes pick their reply by request number, or read requests from a channel the test drains in order. The title request runs at the same time as the turn, so it uses up a scripted reply or takes the slot the test waits on. The result changes from run to run.
- **Strict decoding.** Fakes that decode the body into a typed struct (for example `Input []struct{...}`) fail or assert on the title request, whose `input` is a plain string.
- **WebSocket fakes.** The rpc live tests serve the Responses API over WebSocket. `GenerateTitle` makes its own `responsesWebsocketDoer` (`internal/rocketclaw/harness/title.go:54`), so the title arrives as a **new connection** whose first message is the title request. It does not share the turn's connection.

There is a second trap. `Bridge.background` (the Background Job registry) is set only in production, where `app.go:237` builds the thread bridge manager. Most test fixtures build bridges without it, so starting work through `b.background.group.Go` panics on a nil pointer in those tests.

## Guidance

**Give the new call a dedicated model setting and a way to recognize it, and answer it first in every fake.**

1. **Use a separate config model.** Titles use `title_model` in `rocketclaw.json` (`internal/rocketclaw/config/config.go:38`). When it is empty, `GenerateTitle` uses `openai/gpt-6-luna#low` (`internal/rocketclaw/harness/title.go:40`). A separate model keeps the call from depending on the agent's model and effort. The owner asked for this, and it also makes the request easy to recognize in tests.

2. **Track background work with a zero-value-safe owner on `Bridge`, not `b.background`.** Titles use a plain `sync.WaitGroup` field (`bridge.go:107`). `runTurn` starts the work with `b.titles.Go(...)`, and `Run` waits on it after the loop ends (`bridge.go:271-272`). A zero `WaitGroup` works in every test bridge without fixture changes. Do not put the work in the turn's local errgroup either: then the turn, and stopping it, waits on the extra call.

3. **Answer the request in shared helpers that every fake calls first.**
   - Backend: `answerTitleRequest(w, r)` (`internal/rocketclaw/backend/bridge_test.go:4226`). It reads the body, puts it back on `r.Body` so the fake can still decode it, and answers if the body contains `"instructions":"You name conversations`.
   - rpc: `titleRequest(body)` and `answerWebsocketTitle(t, conn, message)` (`internal/rocketclaw/frontend/rpc/server_test.go:4078-4092`). The WebSocket helper replies with one `response.completed` event built from `titleResponse`. The connection is used only for the title, so the handler returns after answering.

   ```go
   server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
   	if answerTitleRequest(w, r) {
   		return
   	}
   	// the fake's existing scripted behavior
   }))
   ```

   ```go
   _, message, err := conn.ReadMessage()
   if !assert.NoError(t, err) || answerWebsocketTitle(t, conn, message) {
   	return
   }
   ```

4. **Leave some fakes alone on purpose.** A fake that sends the same valid reply to every request just produces that reply as the title, and its test does not care. Collectors that only record requests need no change either, unless the test counts them.

The underlying cost is that each test writes its own fake Responses server. Moving the backend and rpc tests onto one shared fake provider would make the next extra call a one-place change. That consolidation has not been done.

## Why This Matters

Without this, the next engineer who adds a per-turn call, such as a summary, an auto-tag, or a classifier, will see the same things in order: a nil-pointer panic if the work goes through `b.background`, then a full backend run of about 3 minutes when it passes, or up to the timeout when one test hangs, with failures that change between runs. After that, they must find every fake, including the WebSocket ones in rpc that only the live and browser tests use. A test that hangs also hides every test after it, so the first run's failure count is too low.

The hang is worse because `Run` waits for background work. Per this session's diagnosis, a fake that never answers the extra request, for example one that holds every request until the test releases it, blocks the bridge's shutdown. The test then hangs instead of failing. `TestMoveToBackgroundWithdrawsQuestionAndMovesSubagent` hung this way until the `noteTest` fixture answered titles.

Alternatives rejected in this session:

- **Off by default behind a config flag.** Tests stay untouched, but the feature would be off by default, unlike OpenCode, and most tests would never run it.
- **Catch title requests once per package, such as a `TestMain` hook on `http.DefaultTransport`.** The diff is small, but the feature stays hidden from most tests. It cannot reach the WebSocket fakes. The dedicated title tests would also need the interceptor to record requests.
- **Run the call in the turn's errgroup.** No nil registry problem, but every first turn, and stopping it, waits on the title. The fakes still see the extra request.
- **Fall back to the agent's own model.** The owner rejected this in favor of one configurable `title_model`.

## When to Apply

- Any new model request made per Web turn, or from `runTurn`, besides the turn itself.
- Any new background goroutine started from `Bridge` that tests reach through fixtures without a Background Job registry.
- Changing `titlePrompt` in `internal/rocketclaw/harness/title.go`. Both helpers recognize the request by its first words, `You name conversations`, so rewording it makes every fake receive titles again.

## Examples

Before: the `noteTest` fixture (`newNoteTest` in `bridge_test.go`) sent every request body to `nt.requests` and gave its responder a running call number. A concurrent title request moved every later call number up by one and put an unexpected body in the channel the test reads.

After: the fixture calls `answerTitleRequest(w, r)` before recording, and `TestFirstWebMessageNamesUnnamedSession` (`bridge_test.go:4240`) checks the feature. Only an unnamed session takes the generated title, and only its first message names it. The test calls `bridge.titles.Wait()` after each final reply, because a final reply does not mean the background title has finished.

For a new kind of call, copy this pattern: a dedicated model setting, a unique instruction prefix, one helper per test package that recognizes it, and a guard at the top of each fake. Then run the full `internal/rocketclaw/backend` and `internal/rocketclaw/frontend/rpc` packages before calling the change done. Running only the targeted tests misses fakes that the change reaches indirectly.

## Related

- `docs/solutions/best-practices/backend-run-tests-keep-fixture-agents-in-workspace-agents.md`: another case where backend and rpc test fixtures differ from production without any warning.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: setup needed before the rpc live and browser tests that use the WebSocket fakes will run.
- `docs/solutions/logic-errors/permission-review-reasons-read-from-transport-specific-trace.md`: another extra model call, the automatic approver, that behaves differently over HTTP and the Responses WebSocket transport.
