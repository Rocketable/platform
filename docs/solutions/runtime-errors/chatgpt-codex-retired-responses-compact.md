---
title: ChatGPT Codex Retired /responses/compact, So ChatGPT-Mode Compaction Failed Silently
date: 2026-10-29
category: docs/solutions/runtime-errors/
module: internal/rocketclaw/oai
problem_type: runtime_error
component: service_layer
symptoms:
  - "Every ChatGPT-mode compaction attempt logged `Codex HTTP attempt returned` with reason=compaction and status=404"
  - Normal requests to the same provider and model returned 200
  - Compaction was attempted again after every response once usage passed the threshold
  - Long ChatGPT-mode conversations never shrank and grew toward the model's context limit
root_cause: wrong_api
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/harness
  - internal/rocketclaw/backend
tags:
  - compaction
  - compaction-trigger
  - chatgpt
  - codex
  - oauth
  - responses-api
  - upstream-drift
---

# ChatGPT Codex Retired /responses/compact, So ChatGPT-Mode Compaction Failed Silently

## Problem

In ChatGPT (OAuth) mode, RocketClaw compacted conversations by posting to `https://chatgpt.com/backend-api/codex/responses/compact`. The ChatGPT Codex backend no longer serves that route. The transport treats a non-200 compaction reply as "no compaction this time", so conversations stopped compacting and nothing reached the user. The fix is opened in #774, unmerged as of this writing.

## Symptoms

- Every compaction attempt logs `msg="Codex HTTP attempt returned" ... reason=compaction retry_count=0 status=404 outcome=http_error`, while `reason=initial` attempts to the same provider and model return 200. That log line (`internal/rocketclaw/oai/oauth.go:584`) is the only signal; the turn and the user see nothing.
- The attempts repeat about once per model response. This looks like a retry loop but is not one. `codexCompaction` runs after every successful `/responses` call whose `usage.total_tokens` is at or above the threshold (`oauth.go:556`, `oauth.go:857`). A non-200 compaction reply leaves that response unchanged (`oauth.go:884`), so the next response tries again.
- The turn still finishes. The conversation just never gets smaller, and a long one eventually overflows the context window.

## What Didn't Work

- **Starting in the harness.** The Responses websocket `/compact` special case (`internal/rocketclaw/harness/responses_websocket.go:128`) and the looper's `Client.Compact` call (`internal/rocketclaw/harness/looper.go:1490`) look like the compaction path, but neither runs for these requests. `Codex HTTP attempt returned` is logged by the oai transport, which builds its own compaction request in `codexCompactItem`.
- **Suspecting the model.** The failing model was new, but an older model got the same 404. The route is gone for every model, not unsupported for one.
- **Looking for a recent RocketClaw change.** The transport had sent the same `/responses/compact` request since June 2026. The backend moved; the code did not.

## Solution

Diagnose by comparing against the openai/codex CLI, the reference client for this backend:

1. Clone https://github.com/openai/codex under `.tmp/` and search `codex-rs` for the request being debugged. Here `codex-rs/codex-api` no longer had a compact endpoint, and `codex-rs/core/src/compact_remote_v2*.rs` held the replacement.
2. Find when it changed with `gh search prs --repo openai/codex`. Codex introduced the `compaction_trigger` request in openai/codex#22809 (May 2026) and sent all remote compaction for supported providers through it from openai/codex#44255 (2026-09-09).
3. Confirm against the live backend with the stored ChatGPT OAuth access token. Use it as stored and do not refresh it, or the probe races the running service's token rotation. Send the old and new request shapes for two models, then replay the returned compaction item in a follow-up that only the compacted context can answer.

| Request | Result |
|---|---|
| `POST /backend-api/codex/responses/compact` | `404 {"detail":"Not Found"}` for every model tried |
| Streamed `POST /backend-api/codex/responses` whose input ends with `{"type":"compaction_trigger"}` | 200 with exactly one `compaction` output item (`id`, `type`, `encrypted_content`) |
| Follow-up whose input is only that item plus a question, `store:false`, id stripped | 200, answered from the compacted context |

`codexCompactItem` (`internal/rocketclaw/oai/oauth.go:939`) now sends compaction as an ordinary streamed response to the same `/responses` path:

```go
compactInput, _ := json.Marshal(append(input, json.RawMessage(`{"type":"compaction_trigger"}`)))
compactRequest := map[string]json.RawMessage{
	"input":        compactInput,
	"model":        compactSource["model"],
	"instructions": compactSource["instructions"],
	"store":        json.RawMessage("false"),
	"stream":       json.RawMessage("true"),
}
```

It reads the reply with `codexStreamingResponse`. A `response.failed` stream counts as the provider declining, like the old non-200 reply: no compaction item, and the original response is returned unchanged (`oauth.go:979`). The same change removed the transport's pass-through of SDK `Responses.Compact` calls to `/responses/compact`, which ChatGPT mode never reached (see below).

## Why This Works

The ChatGPT Codex backend is not the public OpenAI API. It has no published contract and serves whatever the Codex CLI sends. Codex stopped calling `/responses/compact`, and the backend has since dropped the route. The streamed `compaction_trigger` request is the shape Codex sends today.

The removed SDK pass-through was unreachable in ChatGPT mode. The looper calls `Client.Compact` only when `isContextLengthExceeded` sees an `*openai.Error` or a failed response with code `context_length_exceeded` (`internal/rocketclaw/harness/looper.go:1632`). ChatGPT Codex reports context overflow inside the stream, as observed here and as Codex itself expects; the transport turns it into a `codexStreamError` and recovers itself (`oauth.go:540`).

## Prevention

- **Keep the two compaction contracts separate.** Providers whose `auth` is not `chatgpt` get a plain SDK client (`internal/rocketclaw/backend/model_resolver.go:73`). Their server honors `context_management` (`looper.go:1882`) and still serves `/responses/compact` for the looper's overflow recovery; an OpenAI-compatible API-key provider returned 200 for both during this investigation, with the compacted context intact. Do not move API-key providers to `compaction_trigger`, and do not drop the harness's `/compact` handling because the oai comment says the route is gone. That comment is about the ChatGPT backend only.
- **When a ChatGPT-mode request starts failing, check openai/codex first.** The backend's contract is what current Codex sends. Compare the transport's request with `codex-rs` and with recent openai/codex PRs in that area before reading RocketClaw history.
- **Watch the attempt log, not the turn.** A compaction the provider declines (a non-200 reply or a `response.failed` stream) is deliberately non-fatal, so a retired or renamed route shows up only as `reason=compaction` attempts with a non-2xx `status` in `Codex HTTP attempt returned`. A network error or an unreadable compaction reply is different: it fails the whole request.
- `TestCodexCompactionHandlesErrorAndFallbackBranches` (`internal/rocketclaw/oai/oauth_test.go:1527`) pins the exact path (`/backend-api/codex/responses`) and body (history plus `compaction_trigger`, `store:false`, `stream:true`). It fails with "ChatGPT Codex no longer serves /responses/compact" if the old path comes back.

## Related Issues

- `docs/solutions/architecture-patterns/per-provider-autocompaction-threshold.md`: where the threshold this transport compares usage against comes from, and why ChatGPT requests have `context_management` stripped before sending.
