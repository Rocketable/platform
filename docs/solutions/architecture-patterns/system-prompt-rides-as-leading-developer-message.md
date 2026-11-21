---
title: The System Prompt Rides as the Leading Developer Message, Not in Responses instructions
date: 2026-11-20
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/harness
problem_type: architecture_pattern
component: service_layer
severity: medium
applies_when:
  - Changing how buildParams shapes a Responses request
  - Touching overflow compaction in looper.go or the ChatGPT sign-in compaction retry in oai/oauth.go
  - Writing a fake model server in tests that needs to know which agent is running
  - Moving a model call to openai-go's Instructions field
related_components:
  - internal/rocketclaw/oai
  - internal/rocketclaw/backend
tags:
  - prompt-caching
  - system-prompt
  - developer-message
  - responses-api
  - gpt-6-2
  - compaction
  - cliproxyapi
  - codex
---

# The System Prompt Rides as the Leading Developer Message, Not in Responses instructions

## Context

openai-go's `responses.ResponseNewParams` has an `Instructions` field, and RocketClaw used to put the agent's system prompt there. OpenCode v2.3.2 (anomalyco/opencode#64542 and #64544) measured that GPT-6.2 models never write top-level `instructions` to the prompt cache or read it back, while GPT-6.1 and older cache it either way. The whole system prompt was billed as uncached input on every step. The same text sent as the first `developer` message in `input` cached from the second step on. In OpenCode's own session, uncached input per step dropped from about 17,000 tokens to a few hundred.

No agent in this repository used a GPT-6.2 model when RocketClaw switched, so it had probably not paid this cost yet. It would have started paying it silently, with no failing test or error, as soon as an agent moved to a GPT-6.2 model.

## Guidance

The main agent request puts the system prompt first in `input`, as a `developer` message, and leaves `Instructions` unset. `buildParams` prepends it after it has pruned history and placed the prompt cache marker (`internal/rocketclaw/harness/looper.go:1751`).

Three paths around that request each need a specific rule:

1. **`/responses/compact` gets the same leading developer message, and its echo is dropped.** `newResponseAfterContextCompaction` sends the compact call the system item followed by the history it compacts, with no `Instructions`, so the call reads the prefix that normal steps cached. OpenCode measured 0 cached tokens on a compact request that sent `instructions` instead (anomalyco/opencode#64544; #64542 had first kept `instructions` there). The endpoint echoes that developer message back in its output, as an `input_text` part, and the window's non-assistant messages are replayed as they came back, so the echo would otherwise reach the retry as a second copy of the system prompt. While building replay, `compactedOutputToReplayParams` skips each developer message whose text exactly matches the nonempty system prompt. The retry saves only the history and puts the system item back in front.

   This drops less than OpenCode, which removes every developer message from the compact output. RocketClaw's history carries other developer messages that must survive compaction: goal notes, skill content, and External MCP metadata. Matching the exact text drops only the echoed system prompt.
2. **The ChatGPT sign-in overflow retry keeps the leading developer item.** `retryCodexAfterCompaction` used to replace the whole retry input with the compaction item alone. The system prompt survived that only because it lived in `instructions`. The retry now keeps the first input item when it is a `developer` message (`internal/rocketclaw/oai/oauth.go:963-967`). `codexCompactItem` still copies the request's `instructions` (now the `""` that `cleanCodexRequest` adds, `oauth.go:754`) and sends the developer message inside the compacted input, which matches what OpenCode does for its trigger compaction. LangChain found that ChatGPT sign-in caches only across requests that share a `session-id` header (langchain-ai/langchain#41949). `setCodexHeaders` sends `Session_id` (`oauth.go:637`), which is a different header name. Whether the backend treats the two the same was not verified.
3. **One-shot calls may keep `instructions`.** Title and Slack-summary generation (`internal/rocketclaw/harness/title.go:89`) are single short requests with nothing to reuse from a cache, so they were left alone.

Tests pin the request shape:

- `systemPrompt` in `looper_test.go:192` asserts that `Instructions` is unset and the first input item is the developer prompt.
- `TestLooperCompactsAndRetriesContextLengthExceeded` (`looper_test.go:1000`) mocks a compact reply that echoes the prompt. It checks that the compact call leads with the developer prompt and sets no `Instructions`, that the retry carries the prompt once and no other developer message, and that the saved history never contains the prompt.
- `TestTransportRetriesContextOverflowAfterCompaction` (`oauth_test.go:1343`) checks that the ChatGPT retry keeps the developer item.
- Backend fake model servers read the prompt with `requestSystemPrompt` (`internal/rocketclaw/backend/raw_run_test.go:26`) or match the raw body prefix `"input":[{"content":"<agent prompt>`, instead of `"instructions":"<agent prompt>`.

## Why This Matters

`Instructions` is the obvious field in the SDK, so putting the prompt back there looks like a harmless cleanup. Nothing breaks when someone does it: replies stay correct and every test still passes, unless someone also undoes the test helper. The only symptom is a provider bill that grows on GPT-6.2.

The two compaction rules also protect correctness. Without the echo filter in `compactedOutputToReplayParams`, the retry carries the system prompt twice. Without the ChatGPT retry fix, the request that recovers an overflowing chat runs with no system prompt at all.

## When to Apply

- Any change to `buildParams`, the Responses parameters it sets, or the order of the input items.
- Any new path that rewrites or replaces a request's `input`, such as a compaction, a retry, or a transport middleware. Such a path has to carry the leading developer item across, because it is no longer in a separate field.
- Adding a provider behind an OpenAI-compatible route. Through CLIProxyAPI v8.0.80, the Responses-to-Claude translator (`internal/translator/claude/openai/responses/claude_openai-responses_request.go` in router-for-me/CLIProxyAPI, not this repository) turns both `instructions` and any developer or system items before the first conversation item into the same top-level Claude `system` blocks. Claude requests therefore came out byte-for-byte the same after the switch, and no per-provider switch was needed. CLIProxyAPI releases before 2026-11-07 moved *every* developer item, including mid-conversation ones, ahead of the conversation history (into `system`, or right after the first user message), which invalidates Claude's cached prefix whenever one is added. That is a reason to keep the deployed proxy current, separate from this change. For Claude, the proxy also serves `/responses/compact` itself: it builds a Claude summary request from the compact input, keeping the leading developer message, which its translator maps to the same system prompt as `instructions`, and it returns only a compaction capsule, so no echo comes back. This session did not read how the proxy translates for xAI or the `openai-subscription` route.

## Examples

Request body before and after, as openai-go serializes it:

```diff
 {
-  "instructions": "<system prompt>",
   "input": [
+    {"content": "<system prompt>", "role": "developer", "type": "message"},
     {"content": "...", "role": "user", "type": "message"}
   ],
   "model": "gpt-6.2-sol"
 }
```

A fake model server that picks its reply by agent:

```go
case strings.Contains(body, `"input":[{"content":"Research`):
```

instead of the old `"instructions":"Research`. A test that compares conversation items, such as the shadow parity tests in `shadows_test.go`, splits the leading developer item off in the handler and compares the rest.

Usage numbers come from OpenCode's PR. This session did not measure cache hits on RocketClaw's own traffic, so check a GPT-6.2 agent's `PromptCacheReadTokens` (`looper.go:429`) after the first GPT-6.2 rollout.

## Related

- `docs/solutions/runtime-errors/chatgpt-codex-retired-responses-compact.md`: the ChatGPT sign-in compaction paths (`codexCompactItem`, `retryCodexAfterCompaction`) whose retry input this change altered.
- `docs/solutions/integration-issues/claude-prompt-too-long-via-cliproxyapi-skipped-overflow-compaction.md`: overflow recovery through `/responses/compact` for Claude behind CLIProxyAPI, the path whose `instructions` handling this change kept.
- `docs/verification/codex-context-overflow-compaction/README.md`: the formal model of both overflow retries. Its description of the ChatGPT retry as sending only the compaction item predates this change.
- `docs/solutions/best-practices/new-model-calls-in-web-turns-reach-every-test-fake-provider.md`: why fake model servers that match raw request bodies break when a request field moves.
- anomalyco/opencode#64542: OpenCode's measurements and its move of the system prompt into a leading developer message. anomalyco/opencode#64544: its follow-up, which extended the move to every Responses route and to `/responses/compact`.
