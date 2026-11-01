---
title: PostHog AI Observability Reads OpenTelemetry GenAI Attributes, Not OpenInference
date: 2026-10-26
last_updated: 2026-10-29
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: best_practice
component: observability
severity: medium
applies_when:
  - Changing span attributes in internal/rocketclaw/harness/observability.go, internal/rocketclaw/harness/looper.go, or Bridge.runTurn in internal/rocketclaw/backend/bridge.go
  - Removing attributes that look like duplicates of the OpenInference ones
  - Adding a span, or a turn source that starts agent turns
  - Traces reach PostHog without errors but its AI Observability view stays empty or shows a random person
symptoms:
  - PostHog accepts OpenInference-only spans with HTTP 2xx, but AI Observability shows no generations for them (reported upstream in PostHog/posthog#66614)
  - Traces appear under a random UUID person instead of the user who sent the turn
  - The trace list's Input column is empty even though the model call shows its reply
retire_when: "PostHog/posthog#66614 (OpenInference support in AI Observability) is closed as done; check with `gh issue view 66614 -R PostHog/posthog`"
related_components:
  - internal/rocketclaw/backend/instrumentation.go
  - internal/rocketclaw/harness/observability.go
tags:
  - posthog
  - opentelemetry
  - openinference
  - genai-semconv
  - tracing
  - llm-analytics
  - principal
---

# PostHog AI Observability Reads OpenTelemetry GenAI Attributes, Not OpenInference

## Context

RocketClaw's spans follow OpenInference (`openinference.span.kind`, `llm.model_name`, `llm.token_count.*`, `input.value`, `output.value`), which Phoenix reads. PR #680 added optional PostHog Cloud export. When `posthog.project_token` is set and `instrumentation.enabled` is true, `configureInstrumentation` (`internal/rocketclaw/backend/instrumentation.go:69-77`) sends the same spans to two PostHog endpoints: the AI endpoint (`/i/v0/ai/otel`), which keeps only GenAI spans for AI Observability, and distributed tracing (`/i/v1/traces`), which keeps every span.

PostHog's AI endpoint accepts OpenInference spans with HTTP 2xx and then never shows them as generations. It classifies a span by `gen_ai.operation.name`, which OpenInference does not emit, and maps only `gen_ai.*` attributes to its `$ai_*` properties. This is upstream issue PostHog/posthog#66614, still open as of 2026-10-26. The issue's reproduction shows the gap: the export succeeds and nothing appears, so RocketClaw's logs would show no error either.

This was found by reading that issue before any code was written, not by watching RocketClaw's spans vanish. PR #680 added the GenAI attributes from its first version. The first real trace then showed up in AI Observability, which confirmed PostHog reads them. Two more problems showed up only in the PostHog UI after deploy:

- **Random person.** The first `rocketclaw.turn` trace belonged to a random UUID person. The first idea was to tag every trace with the server's name, as the server log events already are. The human pointed out that a turn already has a human Principal, and that is the better value. A trace tagged with the sender joins that person's page views and replays. A server name would have put every trace on one fake person.
- **Empty input.** After the person fix, the trace showed the sender and the reply, but its Input column was empty ("–"). This was expected, because the model calls' request messages are left out on purpose (see below). The fix sets the turn's own message as the trace input.

## Guidance

Every span RocketClaw sends carries the OpenTelemetry GenAI attributes next to the OpenInference ones. They are not duplicates. Phoenix reads one set and PostHog reads the other.

| Span | GenAI attributes PostHog needs | Where |
| --- | --- | --- |
| `rocketclaw.turn` (the trace root) | `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name`, `gen_ai.conversation.id` | `Bridge.runTurn`, `internal/rocketclaw/backend/bridge.go:2123-2128` |
| `rocketcode.turn` / `rocketcode.provider` / `rocketcode.tool.*` | `gen_ai.operation.name` by span kind (`invoke_agent`, `chat`, `execute_tool`) | `ObservabilityConfig.startSpan`, `internal/rocketclaw/harness/observability.go:31-36` |
| `rocketcode.turn` | `gen_ai.agent.name` | `internal/rocketclaw/harness/looper.go:788` |
| `rocketcode.provider` | `gen_ai.request.model`, `gen_ai.provider.name`, `gen_ai.response.id`, `gen_ai.output.messages`, `gen_ai.usage.input_tokens` / `output_tokens` | `looper.newProviderResponse` (`internal/rocketclaw/harness/looper.go:1304-1316`) and `TokenUsage.attributes` (`internal/rocketclaw/harness/looper.go:552-553`) |
| `rocketcode.tool.*` | `gen_ai.tool.name`, `gen_ai.tool.call.id` | `startToolSpan`, `internal/rocketclaw/harness/observability.go:110-111` |

`gen_ai.output.messages` holds the reply masked by `hide_outputs`, like `output.value`. Token counts of zero are left off the span.

Two more attributes decide what PostHog shows for a trace:

- **Person.** PostHog takes a trace's person from `user.id` or `posthog.distinct_id` on its spans. Without either, it makes up a UUID. `Bridge.runTurn` sets `user.id` through `instrumentation.WithUser`. It uses the turn's `rocketclaw_principal_email` metadata, or else its `rocketclaw_principal` (`internal/rocketclaw/backend/bridge.go:2112`). `ApplyContextAttributes` copies `user.id` onto every span of the turn, as it does `session.id`. Who that is depends on where the turn came from:
  - **Web and voice:** the Web username (`internal/rocketclaw/frontend/rpc/server.go:1527`, `internal/rocketclaw/frontend/rpc/voice.go:455`). The browser sends the same username to `posthog.identify` (`internal/rocketclaw/web/src/posthog.ts:16`), so a trace joins that person's page views and replays.
  - **Slack:** the sender's email set by the connector (`internal/rocketclaw/frontend/slack/connector.go:1140`).
  - **External MCP:** the MCP principal, because caller metadata goes to a separate field and never sets the email key (`cmd/rocketclaw/mcp.go:197-202`).
  - **Cron:** no principal at all (`cmd/rocketclaw/cron.go:37`), so cron traces still get a random person.
  - **Goal-loop continuations and shadow turns:** the email saved from the person who started the goal or the parent turn (`internal/rocketclaw/backend/bridge.go:2060`, `internal/rocketclaw/backend/shadows.go:865`), when there is one.
- **Trace input.** The trace list's Input column reads `$ai_input_state`. When this was written, PostHog's ingestion filled that field only in its middleware for some instrumentation libraries (Traceloop and Vercel AI), from `$ai_input`. RocketClaw sends no `gen_ai.input.messages`, so nothing fills it. `Bridge.runTurn` therefore sets a literal `$ai_input_state` attribute on `rocketclaw.turn` to the turn's message, masked by `hide_inputs` (`internal/rocketclaw/backend/bridge.go:2135`). Per the ingestion source read in this session, PostHog keeps span attributes it does not map as event properties, unchanged. **Not yet confirmed:** nobody had checked in the PostHog UI that the Input column shows this value when this was written. The person and the reply text were both confirmed there.

The request messages of each model call (`gen_ai.input.messages`) are deliberately not sent. Each call repeats the whole conversation, and PostHog's docs say an OTLP export over 4 MB is rejected with HTTP 413. For the same reason, the PostHog exporters send batches of 32 spans instead of the 512 that Phoenix's exporter uses (`internal/rocketclaw/backend/instrumentation.go:64` and `internal/rocketclaw/backend/instrumentation.go:73`). Tool spans carry whole tool outputs. The same docs say a single AI event over 8 MB is dropped from an otherwise successful export, with no error.

## Why This Matters

The GenAI attributes look like a redundant copy of the OpenInference ones, so a cleanup could remove them. Phoenix would keep working. PostHog's AI view would go empty with no export error, which is the failure PostHog/posthog#66614 describes. Two tests guard these attributes and would fail: `TestObservabilityEmitsAgentProviderAndToolSpans` (`internal/rocketclaw/harness/observability_test.go:70`) checks the GenAI attributes on RocketCode spans. `TestRunTurnSpanPreservesMaskingAndFailure` (`internal/rocketclaw/backend/observability_test.go:22`) checks `user.id` on every span of the turn, plus `$ai_input_state` and the GenAI attributes on `rocketclaw.turn`. Nothing at runtime would report the loss.

Looking up these attributes in PostHog does not help when debugging. After mapping, its ingestion removes `gen_ai.operation.name`, `user.id`, and `posthog.distinct_id` from the event properties (per the ingestion source read in this session). So a working span does not show them in PostHog either. Check the span RocketClaw sends, for example in Phoenix or a test, instead of the PostHog event.

## When to Apply

- **Adding a span kind or a span:** give it a `gen_ai.operation.name`. Otherwise PostHog's AI view ignores it, though distributed tracing still shows it.
- **Adding a turn source:** set principal metadata, preferably the email the browser identifies with. Otherwise its traces have no person.
- **Adding request or tool content to spans:** keep exports under 4 MB and events under 8 MB, and keep `hide_inputs` / `hide_outputs` masking.
- **Debugging "spans exported but nothing in AI Observability":** check `gen_ai.operation.name` before you suspect the token or the endpoint.
- **After PostHog/posthog#66614 closes:** check whether the GenAI attributes are still needed before you remove them.

## Examples

What PostHog/posthog#66614 reports: an OpenInference span (`openinference.span.kind=LLM`, `llm.*`) exported to `/i/v0/ai/otel` returns success and never appears as a generation. A control span with `gen_ai.operation.name=chat` appears fully populated.

What RocketClaw saw on its local deploy, Hally, while PR #680 was built:

| State | What PostHog AI Observability showed |
| --- | --- |
| GenAI attributes, no `user.id` | `rocketclaw.turn` trace present, person a random UUID |
| Plus `user.id` from the principal's email | person `ulderico@rocketable.com`, reply text on the model call, Input column "–" |
| Plus `$ai_input_state` on `rocketclaw.turn` | shipped in PR #680, Input column not yet checked in the UI |

The person fix is one call in `Bridge.runTurn`:

```go
ctx = instrumentation.WithUser(instrumentation.WithSession(ctx, b.config.ConversationID),
	cmp.Or(msg.Metadata[protocol.InboundPrincipalEmailMetadataKey], msg.Metadata[protocol.InboundPrincipalMetadataKey]))
```

## Related

- PostHog/posthog#66614: OpenInference spans accepted but never shown as generations (open as of 2026-10-26).
- PostHog docs, OpenTelemetry installation and size limits: https://posthog.com/docs/ai-observability/installation/opentelemetry
- PostHog docs, traces and `$ai_*` properties: https://posthog.com/docs/ai-observability/traces
- `CONCEPTS.md` "Principal": the human actor whose identity names a trace's person.
