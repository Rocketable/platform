# 0001. Runtime Capabilities

Status: Proposed
Human approval required for meaning changes: Yes

## Decision

`openresponsesd` is a separate daemon that exposes Responses/OpenResponses-shaped protocol endpoints to clients and routes those requests to configured upstream provider surfaces. It must live under `cmd/openresponsesd` and `internal/openresponsesd`, and it must not change RocketCode or RocketClaw provider contracts.

The daemon uses Responses/OpenResponses-shaped request, response, item, event, and error models as its canonical client-facing and internal protocol model. Provider-specific SDKs or wire schemas are adapter details and must not become the daemon's client contract or internal canonical model.

## Scope

This ADR governs the public runtime behavior of `cmd/openresponsesd` and its implementation under `internal/openresponsesd`.

In scope:

- HTTP and WebSocket OpenResponses-compatible endpoints.
- Non-streaming JSON responses.
- SSE streaming responses.
- WebSocket response creation events.
- WebSocket-mode upstream forwarding where the selected provider surface supports it.
- Inbound bearer-token authentication.
- Outbound provider credential resolution.
- Provider routing and provider-specific request translation.
- In-memory response state and `previous_response_id` continuation.
- Response compaction with native pass-through where supported and provider-backed fallback otherwise.

Out of scope:

- Embedding `openresponsesd` into RocketCode or RocketClaw.
- Reintroducing Anthropic Messages or OpenAI-compatible Chat Completions support into RocketCode or RocketClaw.
- Durable multi-process response state.
- Provider-hosted tools that cannot be represented safely in OpenResponses.
- Silent lossy translation of unsupported provider-specific extensions.

## Context

RocketCode and RocketClaw are intentionally constrained to first-party OpenAI Responses only by their own ADRs. `openresponsesd` exists for a different product need: clients speak one Responses/OpenResponses-shaped API while the daemon translates to first-party OpenAI Responses, OpenAI-compatible Chat Completions, or Anthropic Messages upstreams.

OpenAI Responses, Chat Completions, and Anthropic Messages differ in request shape, tool call semantics, streaming event shape, system/developer instruction handling, and continuation behavior. The daemon must make those differences explicit at adapter boundaries instead of hiding them behind SDK structs.

## Normative Contracts

### Product Boundary

`openresponsesd` must be implemented as a separate daemon. Its code may reference shared low-level helpers only when doing so does not change RocketCode or RocketClaw runtime behavior.

Adding, removing, or changing an upstream provider surface for `openresponsesd` is a product behavior change and requires this ADR to be updated before implementation.

### Endpoints

The daemon must expose these endpoints:

| Endpoint | Method or transport | Contract |
| --- | --- | --- |
| `/healthz` | `GET` | Returns daemon health and provider summary without secrets. |
| `/v1/responses` | `POST` JSON | Creates one response. If `stream` is false or omitted, returns one JSON response object. If `stream` is true, returns SSE events. |
| `/v1/responses` | WebSocket upgrade | Accepts OpenResponses WebSocket client events, including `response.create`. |
| `/v1/responses/compact` | `POST` JSON | Creates a Responses/OpenResponses-shaped compaction result for supplied or referenced response content. |

Unsupported methods must fail clearly. API routes must return OpenResponses-shaped errors where practical.

### HTTP Request Handling

HTTP API requests must require JSON request bodies with bounded reads. Malformed JSON, invalid request shape, missing required fields, unknown provider routes, and unsupported content must fail with structured OpenResponses-compatible error envelopes.

The daemon must not start its network listener until configuration has loaded and validated successfully.

### Authentication

API routes under `/v1/` require inbound bearer-token authentication when any auth token is configured. The token source is the JSON config `auth.tokens` list, optionally overridden for local development by the documented `--auth-token` flag or `OPENRESPONSESD_AUTH_TOKEN` environment variable.

`GET /healthz` may be unauthenticated, but it must not expose configured auth tokens, provider API keys, or provider key environment variable values.

Outbound provider credentials must come from explicit provider config fields or documented environment variables. The daemon must not read undocumented behavior-changing environment variables.

### Configuration

The daemon uses a JSON config file by default. The documented daemon flags are:

- `--config`
- `--addr`
- `--auth-token`
- `--provider`

The documented environment overrides are:

- `OPENRESPONSESD_CONFIG`
- `OPENRESPONSESD_AUTH_TOKEN`
- `OPENRESPONSESD_OPENAI_API_KEY`
- `OPENRESPONSESD_ANTHROPIC_API_KEY`

Configuration must define a default provider or model routes that deterministically select a provider. Provider definitions must validate their required fields before serving.

Provider config supports these provider types:

- `openai_responses`
- `openai_chat_completions`
- `anthropic_messages`

Unknown provider types must be rejected during config validation.

### Provider Routing

The daemon selects the upstream provider from explicit request routing fields when supported, then from `model_routes`, then from `default_provider`. Model route prefix stripping is allowed only when configured.

If no route matches and no default provider is configured, the request must fail before contacting any provider.

### Provider Adapter Boundary

Adapters accept canonical Responses/OpenResponses-shaped requests and emit canonical Responses/OpenResponses-shaped events or response objects. Adapter implementations may use provider SDKs, raw HTTP, or provider-native WebSocket transports internally, but provider-native types must not leak into HTTP, store, SSE, WebSocket, or client-visible contracts.

Adapters must observe request context cancellation for outbound provider work.

### Supported Provider Surfaces

`openai_responses` routes to the first-party OpenAI Responses API.

`openai_chat_completions` routes to an OpenAI-compatible Chat Completions API. Translation must flatten Responses/OpenResponses input into chat messages and must return Responses/OpenResponses-shaped output items and errors.

`anthropic_messages` routes to Anthropic Messages. Translation must map Responses/OpenResponses system/developer instructions, messages, tools, tool calls, and tool results into Anthropic request shape according to config, and must map Anthropic outputs back to Responses/OpenResponses shape before returning them to clients.

### Streaming

SSE streaming must emit semantic OpenResponses events in deterministic order with monotonically increasing `sequence_number` values per response.

For each SSE event, the `event:` name must match the JSON event body's `type` field. SSE streams must end with the literal terminal line `data: [DONE]` after the final semantic terminal event.

Successful response streams must include response creation, in-progress, output/content progress, and completed events. Failed streams must include a structured error or failed terminal event before `[DONE]`.

### WebSocket

WebSocket connections use `/v1/responses` and follow OpenAI Responses WebSocket mode. Each turn starts with a `type:"response.create"` event whose top-level payload mirrors the normal Responses create body. The daemon must not require a non-standard nested `body` field for client `response.create` events.

A connection may have only one in-flight response at a time. A concurrent `response.create` while another response is in flight must fail clearly on that connection.

WebSocket `response.create` must reject HTTP-specific request fields such as `stream`, `stream_options`, and `background`.

WebSocket `response.create` with `generate:false` is a warmup turn. Native WebSocket-capable provider surfaces must pass through warmup semantics and return a response ID that can be used by later `previous_response_id` turns. Provider surfaces that cannot represent warmup without semantic loss must fail clearly.

For `openai_responses`, WebSocket client turns must be forwarded to the upstream OpenAI Responses WebSocket mode rather than translated through the HTTP create endpoint. The daemon must open the upstream connection to the configured provider's `/v1/responses` WebSocket endpoint and relay OpenResponses-shaped response events back to the client.

For provider surfaces without native WebSocket mode, the daemon may emulate WebSocket mode by running one provider request per turn over the provider's supported non-WebSocket transport, as long as client-visible event order, continuation semantics, and error behavior still follow this ADR.

Sequential responses on one WebSocket connection are supported. Connection-local state may retain the most recent `store:false` response for same-connection continuation. Reconnecting loses connection-local `store:false` state.

### Response State And Continuation

The first implementation uses in-memory process-local state only.

For `store:true` or omitted `store`, the daemon stores normalized input and output items needed for `previous_response_id` continuation until TTL or max-response eviction removes them.

For HTTP, `store:false` responses must not be available through process-global continuation state. For WebSocket, `store:false` continuation may work only on the same connection when connection-local state still exists.

Continuation must logically concatenate prior normalized input, prior output, and new input in semantic order. Unknown, evicted, or unavailable `previous_response_id` values must fail with `previous_response_not_found` or the closest OpenResponses-compatible error code.

Failed continuation against connection-local `store:false` state must evict the referenced failed continuation state for that connection.

### Compaction

`POST /v1/responses/compact` returns a Responses/OpenResponses-shaped compaction response.

When the selected provider supports native compaction, the daemon must pass through native compaction semantics and map the result back to OpenResponses shape.

When native compaction is unavailable, the daemon may use provider-backed summarization fallback. Fallback compaction must preserve conversation boundaries as compaction or checkpoint items and must reject unanswered function calls rather than summarizing across them.

Missing required compaction fields, including model when no route can otherwise be selected, must fail clearly.

### Tool Calls

Function tools and function calls are supported when the selected provider surface can represent them.

Tool call IDs, names, arguments, and tool result ordering must be preserved across translation where the provider surface permits it. If the provider cannot represent a requested tool behavior without semantic loss, the request must fail clearly unless a future ADR explicitly allows a lossy mode.

### Unsupported Content And Extensions

Unsupported standard OpenResponses item or content types must fail clearly. Unknown extension fields may be preserved as opaque JSON only when doing so does not change core semantics and does not imply provider support.

The daemon must not silently drop semantically meaningful content, tools, provider extensions, or request controls.

### Error Mapping

Provider errors must be mapped to Responses/OpenResponses-compatible errors. The mapping must preserve the high-level category without leaking arbitrary upstream headers or secrets.

At minimum, implementation must distinguish invalid requests, authentication or authorization failures, model not found or unsupported model failures, provider rate limits, upstream provider failures, and internal server errors.

## Non-Goals

`openresponsesd` does not define RocketCode or RocketClaw behavior.

`openresponsesd` does not promise lossless translation for every OpenResponses feature across every provider.

`openresponsesd` does not provide durable state, clustering, queueing, retries, tracing, metrics, or multi-process consistency in the first implementation.

`openresponsesd` does not expose provider API keys or inbound auth tokens through health, error, debug, or log output.

## Evidence

- `internal/rocketcode/docs/adr/0001-runtime-capabilities.md`
- `internal/rocketcode/docs/adr/0002-behavior-contracts.md`
- `internal/rocketcode/docs/adr/0003-tools-agents-skills-and-extensibility.md`
- `internal/rocketclaw/docs/adr/0001-runtime-capabilities.md`
- `internal/rocketclaw/docs/adr/0002-behavior-contracts.md`

## Consequences

Implementation must start with tests that assert user-visible protocol behavior for JSON responses, SSE event order, WebSocket event order, provider routing, auth failures, continuation failures, unsupported content failures, and compaction fallback behavior.

Provider adapters may differ internally, but their observable daemon output must follow this ADR.

Any future change to endpoints, provider surfaces, auth policy, continuation semantics, streaming terminal behavior, WebSocket sequencing, compaction behavior, or unsupported feature handling requires an ADR meaning change before implementation.

## Changelog

- 2026-06-26: Initial proposed runtime capabilities snapshot for human approval.
- 2026-06-26: Aligned the product-boundary context with RocketCode and RocketClaw's first-party OpenAI Responses-only cleanup, and clarified that OpenAI-compatible upstream support is Chat Completions rather than Responses.
- 2026-06-26: Clarified that the client-facing daemon protocol always remains Responses/OpenResponses-shaped even when upstream adapters call Chat Completions or Anthropic Messages.
- 2026-06-29: Tightened WebSocket behavior to match OpenAI Responses WebSocket mode on the client-facing side and require native upstream WebSocket forwarding for `openai_responses`.
