---
title: "Claude's \"prompt is too long\" Through CLIProxyAPI Had No Error Code, So a Full Chat Never Compacted"
date: 2026-11-18
category: docs/solutions/integration-issues/
module: internal/rocketclaw/harness
problem_type: integration_issue
component: service_layer
symptoms:
  - "Every new message in one Claude chat failed with: run rocketcode turn: run turn: request response: new response: create response: OpenAI API error: 400 Bad Request"
  - "The agent ran on anthropic/claude-opus-5-5, yet the error named OpenAI"
  - "The chat's last good turn had sent 999,248 prompt tokens against a 1,000,000-token limit, and the chat had never compacted once"
  - "CLIProxyAPI logged 400 on POST /v1/responses in under a second, before any reply streamed"
root_cause: wrong_api
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/backend
  - infrastructure
retire_when: "router-for-me/CLIProxyAPI#7666 (or an equivalent fix) is merged and released, and the deployed CLIProxyAPI runs that release; check the PR, then send an over-limit Claude request to the proxy's /v1/responses and confirm the 400 body carries code context_length_exceeded"
tags:
  - context-overflow
  - compaction
  - cliproxyapi
  - claude
  - anthropic
  - prompt-is-too-long
  - context-length-exceeded
  - openai-go
---

# Claude's "prompt is too long" Through CLIProxyAPI Had No Error Code, So a Full Chat Never Compacted

## Problem

A Claude chat on Wallace stopped accepting messages. Its history had passed Claude's 1M-token limit, and RocketClaw's overflow recovery (compact the oldest history, then retry) never ran. The recovery starts only on the error code `context_length_exceeded`, and the error CLIProxyAPI relayed had no code. Every new message resent the same oversized history and failed the same way.

## Symptoms

- The chat showed `I hit an internal error while waiting for rocketcode.` followed by `... create response: OpenAI API error: 400 Bad Request`. The turn rows were `phase=done terminal=failed`.
- The agent's model was `anthropic/claude-opus-5-5`. Nothing was sent to OpenAI.
- `cliproxyapi` journal: `400 | 828ms | POST "/v1/responses"`. The rejection came with the first response headers, not mid-stream.
- The cause in this chat: the last good turn (17:28) sent 999,248 prompt tokens. A 168 KB result from a 19:00 cron run that reports into the chat then pushed the next request over 1M.

## Why the error said "OpenAI"

RocketClaw sends requests to every provider in the OpenAI Responses format, using the openai-go SDK. On Wallace, the named providers (`anthropic`, `anthropic-approver`, `xai`, `openai-subscription`) point at CLIProxyAPI on `http://localhost:8317/v1`, which translates each request for the upstream model. Only the default `openai` provider calls an OpenAI-compatible endpoint directly. The SDK's `(*Error).Error()` prints only the status and the label "OpenAI API error" (`vendor/github.com/openai/openai-go/v3/internal/apierror/apierror.go:44-52`). Its `Code`, `Type`, and `Message` fields are not in the string. **"OpenAI API error" in a RocketClaw turn names the SDK, not the provider.** The overflow recovery also stays on the failing provider: it calls `l.Client.Compact` with the same client and `params.Model` (`internal/rocketclaw/harness/looper.go:1336-1342`).

The `provider request failed` log line does not show the provider's message either. It is written by HTTP middleware (`internal/rocketclaw/backend/model_resolver.go:146-174`) whose attributes are status, timing, and request IDs (`internal/rocketclaw/backend/bridge.go:2326-2345`). This session saw "prompt is too long" only by sending a request to the proxy directly.

## What Didn't Work

- **Expecting a fix on main.** Wallace ran v0.2.43. Main was 89 commits ahead, but `isContextLengthExceeded` was unchanged there. #1270 ("recover a ChatGPT login from context overflows") covers only the ChatGPT sign-in transport.
- **Assuming the 200,000-token autocompaction protected Claude chats.** RocketClaw sends `context_management: [{type: compaction, compact_threshold}]` on every request (`internal/rocketclaw/harness/looper.go:1720-1723`). CLIProxyAPI's Claude request translator builds the Claude request from a fixed list of fields and drops this one. No config option enables it, and v8.0.77, the newest release when this was written, still drops it. Evidence: Wallace's database had 1,497 compaction records, all from OpenAI models and none from Claude. A Claude request with threshold 1,000 and 7,347 input tokens came back uncompacted.
- **Assuming the proxy never adds the code.** CLIProxyAPI already maps Claude's "prompt is too long" to `context_length_exceeded` (issue router-for-me/CLIProxyAPI#6869), but only for errors that arrive after streaming has started. A request Claude rejects before the first byte goes through the proxy's plain error writer, which leaves the code out. Through the proxy, an oversized request returns exactly `{"error":{"message":"prompt is too long: 2400182 tokens > 1000000 maximum","type":"invalid_request_error"}}`.
- **Fixing it in proxy config.** `override-raw` rules that add Anthropic's `clear_tool_uses` context management worked in isolation: a request with about 1.2M tokens of tool output dropped to about 163k and succeeded. With reasoning on, about 1 request in 8 failed with `context_management: Extra inputs are not permitted`. Which request failed depended on which of the proxy's 9 Claude logins served it, and one of them seems to send no matching beta header. The rules were reverted after 24 seconds, and only test traffic hit them. A first write also produced invalid JSON values because of ssh quoting. The proxy refused to reload that file and kept the old config.
- **Calling `/v1/responses/compact` "Claude's compaction endpoint."** For Claude, the proxy compacts the history itself. It asks Claude for a summary and packs it as `cpa-ag-compact-v1` content. Anthropic's server-side compaction is never involved. It works (it returned 200 for `claude-opus-5-5`), but only when RocketClaw calls it.

## Solution

Two changes. The RocketClaw one is the workaround the user asked for, named so that it is obviously temporary:

```go
func isContextLengthExceeded(err error) bool {
	if errAPI, ok := errors.AsType[*openai.Error](err); ok {
		return errAPI.Code == "context_length_exceeded" || workaroundCLIProxyAPIClaudePromptTooLongWithoutCodeRemoveOnceUpstreamFixed(errAPI)
	}
	...
}

func workaroundCLIProxyAPIClaudePromptTooLongWithoutCodeRemoveOnceUpstreamFixed(errAPI *openai.Error) bool {
	return errAPI.Type == "invalid_request_error" && strings.HasPrefix(errAPI.Message, "prompt is too long")
}
```

(`internal/rocketclaw/harness/looper.go:1477-1491`.) All four overflow checks go through `isContextLengthExceeded`: the first request and the chunked retries, for both HTTP errors and failed responses (`looper.go:1211`, `:1285`, `:1369`, `:1399`). So this one change covers the first overflow and every retry after a partial compaction.

The upstream fix is draft PR router-for-me/CLIProxyAPI#7666, from the fork `ucirello/CLIProxyAPI` into `dev` (the branch upstream merges into). It reuses the proxy's existing mid-stream mapping for errors returned before the first byte. Open as of 2026-11-18.

## Why This Works

The recovery machinery was already correct for Claude: CLIProxyAPI serves `/v1/responses/compact` for Claude models, and the compacted item replays on the retry. Only recognition was missing. Once a coded or uncoded "prompt is too long" 400 counts as an overflow, `newResponseAfterContextCompaction` compacts the oldest tenth of the history blocks, retries, and widens the compacted prefix by another tenth on each further overflow (`internal/rocketclaw/harness/looper.go:1321-1410`).

## Prevention

- **Regression test:** `TestLooperCompactsAndRetriesContextLengthExceeded` (`internal/rocketclaw/harness/looper_test.go:990`) now runs once with OpenAI's coded error and once with `cliProxyAPIClaudePromptTooLongError` (`looper_test.go:347`), the proxy's uncoded shape. The new subtest failed before the fix with the production error.
- **Removing the workaround:** when `retire_when` holds, delete the workaround function, its call in `isContextLengthExceeded`, and the `cliproxyapi claude prompt is too long` subtest. Then confirm with an over-limit request through the deployed proxy that the 400 carries `code: context_length_exceeded`.
- **Still open: Claude chats do not compact at the Autocompaction Threshold.** After this fix, a Claude chat behind CLIProxyAPI compacts only when it hits the 1M limit, so turns near the limit resend about a million tokens. Per this session's estimate, a chat stays near 900k after recovery because only the oldest tenth is compacted. Options are to have RocketClaw request compaction itself (as `codexCompaction` already does for ChatGPT sign-in), or to have the proxy forward `compact_threshold` for Claude. Both are open design decisions.
- **Diagnosis shortcut:** for any `OpenAI API error: <status>` on a non-OpenAI provider, check the deployed `api_base_url` for that provider and reproduce against it directly to see the error body. The RocketClaw log line will not show it. Logging the decoded error type and message there would have shown "prompt is too long" immediately (proposal; product code).

## Related Issues

- `docs/solutions/runtime-errors/chatgpt-codex-retired-responses-compact.md`: the same overflow recovery for ChatGPT sign-in providers. Its claim that API-key providers' servers honor `context_management` does not hold for Claude behind CLIProxyAPI.
- `docs/solutions/architecture-patterns/per-provider-autocompaction-threshold.md`: where `compact_threshold` comes from. For Claude behind CLIProxyAPI, the threshold is sent but has no effect.
- `docs/verification/codex-context-overflow-compaction/`: the formal model of the overflow recovery. This fix adds no transitions; it only lets one more error shape start the recovery.
- router-for-me/CLIProxyAPI#7666 (upstream fix, draft), router-for-me/CLIProxyAPI#6869 (closed issue behind the mid-stream mapping it extends).
