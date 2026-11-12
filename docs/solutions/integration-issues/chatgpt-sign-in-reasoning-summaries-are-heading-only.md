---
title: With ChatGPT Sign-In, Model Thoughts Are Bold Titles Only, and RocketClaw Hid Even Those
date: 2026-11-12
category: docs/solutions/integration-issues/
module: internal/rocketclaw/harness
problem_type: integration_issue
component: service_layer
symptoms:
  - Native RocketClaw chats on a GPT model showed no readable thinking, while an OpenCode screenshot of a different session showed full paragraphs of thoughts
  - "Thought rows held only one short bold title, such as **Checking the fetch result**, with no text under it"
  - Thoughts appeared only after each model response finished, never while the model was working
  - Changing the Web timeline detail level did not bring more thinking text back
root_cause: logic_error
resolution_type: code_fix
severity: medium
related_components:
  - internal/rocketclaw/oai
  - internal/rocketclaw/frontend/rpc
  - internal/rocketclaw/backend
retire_when: "openai/codex#34873 is closed as fixed and GPT reasoning summaries over ChatGPT sign-in contain prose again; check the issue, then rerun the OpenCode length query in this doc"
tags:
  - reasoning-summary
  - thinking
  - chatgpt
  - codex
  - live-progress
  - opencode-parity
  - responses-api
---

# With ChatGPT Sign-In, Model Thoughts Are Bold Titles Only, and RocketClaw Hid Even Those

## Problem

A provider that signs in with ChatGPT gets each GPT thought (a reasoning summary) back as a short bold title with no text under it. This is OpenAI's backend behavior, not a RocketClaw bug, and no RocketClaw setting changes it. RocketClaw had two real bugs on top of it: it kept only the first part of each thought, and it showed nothing from a ChatGPT sign-in provider until the whole response had arrived. Both are fixed in PR #1086 (open, unmerged as of this writing).

## Symptoms

- A Web chat on a GPT model showed at most one bold title per thought. At the Compact and Detailed timeline levels, the Thought row is folded into a collapsed group, which is labelled "Used N tools" when it also holds tool calls (`internal/rocketclaw/web/src/timeline-detail.ts:148`). So it is easy to miss. Quiet and Messages only hide thoughts entirely.
- An OpenCode session in the same screenshot showed long thoughts. That session ran Claude, not GPT.
- During a running turn, no thought and no answer text appeared live for ChatGPT sign-in providers. Live text worked only for providers reached over the Responses WebSocket.
- Thought rows show the raw `**` marks, because the Web renders thought text as plain text (`internal/rocketclaw/web/src/ui.tsx:3869`). PR #1086 does not change this.

## What Didn't Work

- **Comparing against the OpenCode screenshot as it was.** The screenshot showed Claude's thinking, which comes back as full text. Comparing it with RocketClaw on GPT made RocketClaw look broken. The first explanation, which leaned on the upstream GitHub issue alone, was rightly challenged. The comparison only became fair when it used the same model family and the same sign-in on both sides (see Prevention).
- **Looking for a bug in the History RPC, the Web, or the database queries.** A throwaway test fed a real saved entry through `History`, and it returned a `thinking` row. The timeline levels, `TranscriptLine`, the `readable_replay` size guard, and the history SQL all handle thoughts correctly. None of them was the cause.
- **Reading RocketClaw's stored data alone.** RocketClaw does not store raw provider responses. Before the fix it also saved only the first summary part. So its stored thoughts could not show whether OpenAI sent one title or several. OpenCode keeps every part, which is why its stored sessions settled the question.
- **Making thoughts live only for WebSocket providers.** That path already streamed, so the change would have been small. It would not have helped ChatGPT sign-in providers, which never used it.

## Solution

PR #1086 makes two changes.

**1. Keep every summary part.** Before, the replay builder kept only `Summary[0]`, and `TestResponseOutputReplayText` required that ("reasoning uses first summary"), with no reason given:

```go
// before
summary := ""
if len(item.Summary) > 0 {
	summary = item.Summary[0].Text
}
return reasoningReplayInput(item.ID, summary, item.EncryptedContent), true
```

```go
// after (internal/rocketclaw/harness/looper.go:2472)
// reasoningReplayInput keeps every summary part; one without parts keeps a blank part, as the field is required.
func reasoningReplayInput(id string, summary []responses.ResponseReasoningItemSummary, encryptedContent string) responses.ResponseInputItemUnionParam {
	parts := make([]responses.ResponseReasoningItemSummaryParam, max(len(summary), 1))
	for i := range summary {
		parts[i].Text = summary[i].Text
	}
	...
```

Both callers use it: `responseOutputToReplayInput` and `compactedOutputToReplayParams`. The History RPC already joins the non-blank parts with a newline into one `thinking` row (`internal/rocketclaw/frontend/rpc/server.go:950-963`). Nothing else had to change for saved thoughts.

**2. Stream ChatGPT sign-in responses live.** `codexStreamingResponse` in `internal/rocketclaw/oai/oauth.go` used to read the whole event stream with `io.ReadAll` before parsing it. Now it reads line by line (`oauth.go:1076`, with no line-length limit at `:1108`, because the completed event holds the whole response). It passes each event to a `ResponseObserver` that the request's context carries. The harness attaches its observer to every non-WebSocket request (`internal/rocketclaw/harness/responses_websocket.go:135-138`). The observer records `response.reasoning_summary_text.delta` and `.done` events as a new `thinking` live-progress kind (`internal/rocketclaw/harness/public_progress.go:23`). The RPC shows each as a Thought row, and the saved thought replaces it when the response finishes. Raw `response.reasoning_text` events (private reasoning) are still ignored. The compaction side request is read with an inert observer, so it never shows up. A retry after a context overflow calls `BeginResponse` again, which clears the failed attempt's live text.

## Why This Works

Two problems stacked on top of each other, and each hid the other:

- **Upstream:** over ChatGPT sign-in, OpenAI returns each summary part as just a bold heading. This is tracked as [openai/codex#34873](https://github.com/openai/codex/issues/34873), which was still open on 2026-11-12. The paid API (`/v1/responses` with an API key) is reported to return prose for the same models. RocketClaw cannot get prose that the backend never sends.
- **RocketClaw:** a thought often has two or three parts. In OpenCode's stored GPT sessions, roughly 3 in 10 reasoning items had more than one. Keeping only the first part threw away the extra titles. With an API-key provider, it would have thrown away every paragraph after the first.
- **RocketClaw:** a ChatGPT sign-in provider's whole response was buffered in the `oai` transport. So the existing live-progress code had nothing to show until the end, for thoughts and answer text alike.

After the fix, RocketClaw shows what OpenCode shows for the same model and sign-in: every title, while the model works.

## Prevention

- **Before calling missing or short thoughts a RocketClaw bug, compare the same model family with the same sign-in.** Claude sends full thinking text. GPT over ChatGPT sign-in sends titles only.
- **Check how the provider authenticates.** A provider with `auth: chatgpt` goes through the `oai` ChatGPT transport. Its `provider request completed` log lines carry `boundary=sdk_return` (`internal/rocketclaw/backend/model_resolver.go:128-132`). The harness asks for summaries only when the agent sets a reasoning effort (`reasoning.Summary = auto`, `internal/rocketclaw/harness/looper.go:1835-1840`).
- **Measure the stored thoughts, read-only.** Look at lengths and shape, not content. On RocketClaw's database:

  ```sql
  WITH reasoning_items AS (
      SELECT item FROM entries e
      CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
      WHERE item->>'type' = 'reasoning'
  ), parts AS (
      SELECT trim(part->>'text') AS text FROM reasoning_items
      CROSS JOIN LATERAL jsonb_array_elements(item->'summary') part
  )
  SELECT count(*) AS parts,
         percentile_cont(0.5) WITHIN GROUP (ORDER BY length(text)) AS median_length,
         max(length(text)) AS longest,
         count(*) FILTER (WHERE text LIKE '**%**') AS bold_title_only
  FROM parts WHERE text <> '';
  ```

  Then run the same check on OpenCode's local store, which keeps every part. The schema below is the one OpenCode used when this was written, and it may change:

  ```sh
  sqlite3 -readonly ~/.local/share/opencode/opencode.db "
  WITH r AS (
    SELECT json_extract(m.data, '\$.model.providerID') AS provider, trim(c.value->>'text') AS text
    FROM session_message m, json_each(m.data, '\$.content') c
    WHERE m.type = 'assistant' AND c.value->>'type' = 'reasoning'
      AND m.time_created > (unixepoch() - 86400) * 1000
  )
  SELECT provider, count(*) AS parts, max(length(text)) AS longest, sum(text LIKE '**%**') AS bold_title
  FROM r WHERE text <> '' GROUP BY provider;"
  ```

  When this was written, OpenCode's `openai` parts (ChatGPT sign-in) were all bold titles, with a median length around 30 characters and none over about 50. Its `anthropic` parts were prose, some running to tens of thousands of characters, and none was a bare title. If `openai` parts are long prose there but short in RocketClaw, RocketClaw is losing text again.
- **Tests that hold the fix:** `TestResponseOutputReplayText` ("reasoning keeps every summary part") fails if parts are dropped again. `TestCodexStreamingResponseObservesEventsAsTheyArrive` and `TestChatGPTResponsesShowReasoningSummariesWhileStreaming` fail if the ChatGPT transport goes back to buffering. `TestResponseObservationsShowReasoningSummariesLive` pins that private reasoning text is never shown. None of them can tell whether the provider sent prose. Only the comparison above can.
- **A test that pins dropping data should give its reason.** "reasoning uses first summary" had none, so nobody could tell whether it was deliberate.

## Related Issues

- [openai/codex#34873](https://github.com/openai/codex/issues/34873): heading-only reasoning summaries (upstream, open).
- PR #1086: keeps every summary part and streams ChatGPT sign-in responses live (open, unmerged as of this writing).
- `docs/solutions/logic-errors/permission-review-reasons-read-from-transport-specific-trace.md`: the same live-progress records, read from the other side. It says that only the WebSocket transport writes them, which stops being true once PR #1086 merges.
- `docs/solutions/runtime-errors/chatgpt-codex-retired-responses-compact.md`: another ChatGPT sign-in transport change in `codexStreamingResponse`.
