---
title: A DNS Lookup Inside a synctest Bubble Crashes the Next Lookup Outside It
date: 2026-11-24
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - A backend test builds a config.Config whose OpenAI field has no APIBaseURL, and the test can start a turn, a title, a summary, or any other model call
  - A test sets only a named provider in Providers, but some call it triggers uses a model with the default openai provider, such as the default title model
  - A test package uses synctest.Test and t.Parallel, and any of its tests resolve a hostname
  - "A test run dies with \"fatal error: send on synctest channel from outside bubble\" (or \"select on ...\") whose stack runs only through the net package"
symptoms:
  - "make test sometimes killed the internal/rocketclaw/backend package with \"fatal error: send on synctest channel from outside bubble\"; a rerun passed"
  - The crashing goroutine was net.(*resolverConfig).tryAcquireSema under lookupIP, started by singleflight, with no test code in its stack
  - Go reported no failing test, because a fatal error ends the process instead of failing one test
root_cause: test_isolation
resolution_type: test_fix
related_components:
  - internal/rocketclaw/harness
  - internal/rocketclaw/config
tags: [synctest, dns, flaky-test, test-fixtures, openai-provider, api-base-url, net-resolver, fatal-error]
---

# A DNS Lookup Inside a synctest Bubble Crashes the Next Lookup Outside It

## Context

`make test` sometimes crashed the whole `internal/rocketclaw/backend` test package with `fatal error: send on synctest channel from outside bubble`. A rerun passed. The crashing goroutine's stack ran only through Go's `net` package: `net.(*resolverConfig).tryAcquireSema` ← `tryUpdate` ← `getSystemDNSConfigNamed` ← `hostLookupOrder` ← `(*Resolver).lookupIP`, started by `internal/singleflight.(*Group).DoChan`. No test name appears, because a fatal error stops the process rather than failing a test.

Two tests were at fault. Neither set a base URL for the default `openai` provider, so they sent real requests to `api.openai.com`. That hostname is 14 characters long, which matches the `0xe` length argument in the crash stack.

- `TestBridgeKeepsRecurringScheduledMessageAfterSuccessfulHandling` runs inside `synctest.Test`. When its recurring scheduled message fired again, it started a real turn with the agent's model on the default provider.
- `TestRunTurnUsesSelectedAgentAdditionalInstructions` runs outside any bubble. It pointed only the `work` provider at its fake server. The automatic session title request does not use the agent's model. When `title_model` is empty, `harness.GenerateTitle` uses `openai/gpt-6-luna#low` (`internal/rocketclaw/harness/title.go:42`), so that request went to the default `openai` provider and missed the fake server, even though the server already answers title requests through `answerTitleRequest`.

**What went wrong first.** The first explanation blamed `TestHiddenRunTurnRetriesItsNotes` (`internal/rocketclaw/backend/background_store_test.go:357`), which opens a Postgres connection inside `synctest.Test`. The reasoning was that the connection needs a DNS lookup. It doesn't: the test database URL uses `127.0.0.1`, and Go returns an IP literal before it touches the resolver (`$GOROOT/src/net/lookup.go:310` in Go 1.27.4). That wrong guess is still written in the description of PR #1837. The passing rerun proved nothing either way, since only the test order had changed.

**How the real cause was found** (per the diagnosis session):

1. Run the package with `GODEBUG=netdns=go+2`. The Go resolver then prints one `go package net: hostLookupOrder(<host>) = ...` line per lookup. The full package showed only `api.openai.com`.
2. Build the test binary once, list its tests with `-test.list '.*'`, and run each alone with `-test.run '^Name$'` under the same `GODEBUG`. Out of 694 tests, only the two above printed a lookup.
3. Run both together: `go test -race -count 10 -run '^(TestBridgeKeepsRecurringScheduledMessageAfterSuccessfulHandling|TestRunTurnUsesSelectedAgentAdditionalInstructions)$' ./internal/rocketclaw/backend/`. This crashed on every attempt with the same stack.

## Guidance

**Every backend test that can reach a model must point the default `openai` provider at a local address.** Setting only a named provider does not cover side calls such as titles, which choose their own model.

PR #1838 changed two lines in `internal/rocketclaw/backend/bridge_test.go`. The bubbled test never checks its follow-up turn, so it now points the provider at a closed loopback port, which fails fast with no lookup:

```go
config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: "http://127.0.0.1:1"}})
```

The other test now sends the title request to its own fake server:

```go
config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}})
```

Rules that follow from it:

- Use IP literals. `httptest.NewServer` URLs are `127.0.0.1`, and so is `http://127.0.0.1:1`, which `agent_draft_save_test.go` also uses. Do not use `localhost` or any made-up hostname in a synctest test: resolving `localhost` triggers the same crash (see the upstream report below).
- Do not count on the test failing when it talks to the real endpoint. Both tests passed while sending these requests, because neither checked their result. The only visible sign was the crash, and only in some test orders.
- To check a package, run it with `GODEBUG=netdns=go+2` and look for `hostLookupOrder`. A clean package prints none. After PR #1838 the backend package prints none.

## Why This Matters

`modelResolver.Resolve` adds `option.WithBaseURL` only when the provider's `APIBaseURL` is set (`internal/rocketclaw/backend/model_resolver.go:91-93`). Otherwise `openai.NewClient` uses `OPENAI_BASE_URL` from the environment when it is set, and the public `https://api.openai.com/v1/` when it is not. `newModelResolver` always registers `cfg.OpenAI` as the `openai` provider (`model_resolver.go:37`), so a zero `config.Config` quietly means "call the real API".

In Go 1.27.4 the `net` package creates the resolver's one-slot semaphore channel inside a `sync.Once`, on the first lookup in the process (`$GOROOT/src/net/dnsclient_unix.go:378-386`), and every later lookup sends on it (`:461`). A channel created inside a `synctest` bubble belongs to that bubble. If the first lookup in the test binary happens in a bubble, the next lookup from any goroutine outside it is a fatal error. Because the tests use `t.Parallel()`, which lookup comes first changes from run to run, so the crash is rare and moves around.

This is tracked upstream as golang/go#80903 ("net: lazily created semaphore channels associate with a synctest bubble"), which was still open at the time of writing. Its proposed change creates `resolvConf.ch` and `nssConfig.ch` up front, but leaves `threadLimit` lazy. The cgo resolver path aborts there with `select on synctest channel from outside bubble` in `net.acquireThread`. Even with that fix, a test that calls the real API depends on the network and can still crash on the cgo path, so the guidance above still applies.

A future engineer starting from the crash would likely repeat the first wrong guess. The stack names no test, the rerun passes, and the obvious suspect (a database connection in a bubble) looks plausible until you notice the database address is an IP literal.

## When to Apply

- Writing or changing a backend test whose `config.Config` lacks `OpenAI.APIBaseURL`, especially one inside `synctest.Test` or one that can trigger a title, a summary, or a later scheduled turn.
- Adding a new model side call whose default model uses the `openai` provider, since every test that reaches it now needs that provider pointed at a fake.
- Investigating any `... on synctest channel from outside bubble` crash whose stack is in `net`.

**Not yet checked:** this diagnosis looked only at `internal/rocketclaw/backend`. `synctest.Test` is also used in `cmd/rocketclaw`, `internal/rocketclaw/codemode`, `internal/rocketclaw/frontend/rpc`, `internal/rocketclaw/frontend/slack`, `internal/rocketclaw/harness`, `internal/rocketclaw/oai` and `internal/rocketclaw/sandbox`. Those packages may have the same problem. The `GODEBUG` check above is the quick way to find out.

## Examples

Recommended check, not built: run the backend tests in CI with `GODEBUG=netdns=go+2` and fail when the output has a `hostLookupOrder(` line, or add an equivalent guard to the package's existing `TestMain` (`internal/rocketclaw/backend/store_fixture_test.go:28`). The failure would read something like "a backend test resolved <host>; point its config at an httptest server or http://127.0.0.1:1". Neither exists yet, so nothing stops a new test from leaving `OpenAI.APIBaseURL` empty.

Before (bubbled test, resolves `api.openai.com` when the scheduled message fires again):

```go
bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), bus, &Config{...}, logger)
```

After:

```go
bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: "http://127.0.0.1:1"}}), bus, &Config{...}, logger)
```

## Related

- `docs/solutions/best-practices/new-model-calls-in-web-turns-reach-every-test-fake-provider.md`: the title request that sent one of these tests to the real endpoint, and the `answerTitleRequest` helper the fixed test now reaches.
- `docs/solutions/best-practices/backend-run-tests-keep-fixture-agents-in-workspace-agents.md`: another backend fixture default that quietly differs from what the test author assumed.
- golang/go#80903: the upstream `net` and `synctest` issue.
