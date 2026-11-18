---
title: "encoding/json/v2 Keeps Unknown Members Only With `embed`; `unknown` and `inline` Are Silently Ignored"
date: 2026-11-18
category: docs/solutions/logic-errors/
module: internal/rocketclaw/frontend/rpc
problem_type: logic_error
component: execute
symptoms:
  - "A struct field tagged `json:\",unknown\"` or `json:\",inline\"` marshals as a member named after the Go field, such as \"Other\":null"
  - Object members the struct does not name are dropped after an Unmarshal and Marshal round trip
  - go build, go vet and make lint all pass, with 0 issues
root_cause: wrong_api
resolution_type: code_fix
severity: medium
framework_version: go 1.27.4
retire_when: "staticcheck SA5008 reports the `unknown` and `inline` json tag options as invalid; check staticcheck/sa5008/jsonv2.go in the honnef.co/go/tools version that the golangci-lint pinned in internal/rocketclaw/Makefile depends on"
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/.golangci.yml
tags:
  - json-v2
  - jsontext
  - struct-tags
  - embed
  - staticcheck
  - sa5008
  - go-1-27
---

# encoding/json/v2 Keeps Unknown Members Only With `embed`; `unknown` and `inline` Are Silently Ignored

## Problem

In Go 1.27's stable `encoding/json/v2`, the struct-tag option that collects the object members a struct does not name into a `jsontext.Value` field is `embed`. The names from the Go 1.25 and 1.26 `GOEXPERIMENT=jsonv2` API, `unknown` and `inline`, are now ignored without an error. A field tagged with them becomes an ordinary member named after the Go field, so a round trip drops every member the struct does not name.

## Symptoms

- `historyEvent` in `internal/rocketclaw/frontend/rpc/server.go` rewrites only the `code` member of an `execute` call's arguments. With ``Other jsontext.Value `json:",unknown"` ``, the `description` member disappeared and `"Other":null` appeared: `{"code":"true"}` came out as `{"code":"true","Other":null}`, and `{}` as `{"Other":null}`.
- Nothing before the database-backed tests noticed. The code compiled, `go vet` was clean, and `make lint` reported `0 issues`.

## What Didn't Work

- **Trusting lint.** `make lint` runs golangci-lint v2.15.0 (`internal/rocketclaw/Makefile:28`), which includes staticcheck from `honnef.co/go/tools` v0.8.2. Its struct-tag check, SA5008, lists `inline` and `unknown` as valid json options (`staticcheck/sa5008/jsonv2.go:144-145` in that module), while it does flag an unknown option such as `bogus`. So lint confirms the old names rather than catching them. Upstream `master` of `dominikh/go-tools` still accepts both, as of this writing.
- **Running the package tests by themselves.** `go test ./internal/rocketclaw/frontend/rpc/` without `ROCKETCLAW_TEST_DATABASE_URL` fails on the missing database, so the failures look like setup noise and the real check waits for `make test`, which starts its own Postgres.
- **Suspecting a tool rewrite.** The first guess at the cause was that `golangci-lint --fix` had rewritten the tag. It had not. The tag was simply not an option Go 1.27 knows.
- **Searching `go doc encoding/json/v2` for "unknown".** That finds only the prose about "unknown members" and `RejectUnknownMembers`. The option list under "JSON Representation of Go structs" names `embed`, which is the actual clue.

## Solution

Tag the catch-all field with `embed` and nothing else (`internal/rocketclaw/frontend/rpc/server.go:911-914`):

```go
// Before: compiles, passes vet and lint, drops members
var execute struct {
	Code  string         `json:"code,omitzero"`
	Other jsontext.Value `json:",unknown"`
}

// After
var execute struct {
	Code  string         `json:"code,omitzero"`
	Other jsontext.Value `json:",embed"`
}
```

A scratch program confirmed the fixed shape before the full suite ran again. With `embed`, `{"description":"Tag","code":"x","background":true}` round-trips as `{"code":"x","description":"Tag","background":true}`. Named fields come first, followed by the other members in input order. `{}` stays `{}`. `<&>` inside a string is not escaped. A duplicate `code` member fails `Unmarshal` (`jsontext: duplicate object member name "code"`), so `historyEvent` leaves those arguments as they were.

## Why This Works

Go 1.27's tag parser (`src/encoding/json/v2/fields.go` in the Go 1.27.4 tree) has a `case "embed":` and no case for `unknown` or `inline`. Its fallback rejects only near-misses of real options, such as `Embed` or `omit_empty`. Anything else falls under `// NOTE: Everything else is ignored.` The Go 1.25.0 and 1.26.0 versions of the same file had `case "inline":` and `case "unknown":`.

`embed` on a `jsontext.Value` or `map[~string]T` field makes it the struct's "embedded fallback". Members that no other field matches are stored there on unmarshal and written back on marshal. That is what the old `unknown` did. Without a recognized option, the field is just a member named `Other`, and a nil `jsontext.Value` marshals as `null`.

## Prevention

- **The existing tests already guard this call site.** `TestSessionEntries` gives an `execute` call a `description` (`internal/rocketclaw/frontend/rpc/server_test.go:1961`) and expects it back in the exact text (`server_test.go:2008`). `TestHistoryPublicProgress` expects `{}` to stay `{}` (`internal/rocketclaw/frontend/rpc/live_test.go:906`). `internal/rocketclaw/web/src/entry-transport.test.ts:60` checks the same text from the web side. They need Postgres, which `make test` and the CI coverage shards (`.github/workflows/test.yml`) provide. No new test was added for this site.
- **Nothing guards a new call site.** No check in the repo would catch `,unknown` or `,inline` on another struct. A source-scanning test would be a convention test with no behavior behind it, which the repo's test rules discourage. A gocritic ruleguard rule would need a rules setup the repo does not have. The right place is upstream: SA5008 should treat `unknown` and `inline` as invalid when the code builds with Go 1.27 or later. Until it does, any new `encoding/json/v2` struct that must keep unknown members needs a round-trip test with a member the struct does not name.
- **Do not trust memory or older articles for v2 option names.** The Go 1.25 experiment, the experiment blog post (`go.dev/blog/jsonv2-exp`, which names `inline`) and code written against them use the old names. The Go 1.27 post in AGENTS.md's reading list (`go.dev/blog/jsonv2`) and `go doc encoding/json/v2` use `embed`. Check `go doc -all encoding/json/v2` for the option list of the Go version in `go.mod`.
- **Recognize the symptom.** A member named after a Go field, such as `"Other":null`, in v2 output means the field's tag option was not recognized.

## Related Issues

- `docs/solutions/architecture-patterns/code-mode-scripts-format-on-starlark-go-parser-not-buildifier.md` describes what this `execute` argument rewrite is for.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md` covers the setup the tests that caught this need.
- Rocketable/platform#1343, open as of this writing, carries the fix.
- golang/go#79985 is why `inline` became `embed`: much existing code, mostly in Kubernetes, already carried a no-op `json:",inline"` tag, so giving it meaning in Go 1.27 would have changed that code's behavior. The stable API also has no `unknown`; one `embed` covers both jobs.
- dominikh/go-tools#1722, merged, added `embed` to SA5008 but kept the experiment's names.
