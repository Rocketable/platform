package rocketcode

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"testing/synctest"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestObservabilityEmitsAgentProviderAndToolSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	looper := testLooper(mockResponses(
		responseWithFunctionCalls("resp-tool", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "read", `{"filePath":"secret.txt"}`)}),
		responseWithUsage(responseWithMessage("resp-final", "final answer"), `{"input_tokens":12,"input_tokens_details":{"cached_tokens":3},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":17}`),
	))
	looper.agent = Agent{Name: "main"}
	looper.DisplayModel = "gpt-test"
	looper.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("read")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return TextToolResult("file contents"), nil
	}
	looper.Tools = map[string]looperTool{"read": tool}

	record, rendered, interrupted, err := looper.runTurn(context.Background(), nil, nil, nil, nil, false, nil, false, false, testPromptInput(PromptInputRoleUser, "inspect secret", nil))

	require.NoError(t, err)
	require.False(t, interrupted)
	require.Equal(t, "resp-final", record.ResponseID)
	require.Equal(t, []ChatResponse{assistantMessage("final answer")}, rendered)

	spans := recorder.Ended()
	require.Len(t, spans, 4)
	require.Equal(t, "rocketcode.provider", spans[0].Name())
	require.Equal(t, "rocketcode.tool.read", spans[1].Name())
	require.Equal(t, "rocketcode.provider", spans[2].Name())
	require.Equal(t, "rocketcode.turn", spans[3].Name())
	require.Contains(t, spans[3].Attributes(), attribute.String(semconv.InputValue, "inspect secret"))
	require.Contains(t, spans[3].Attributes(), attribute.String(semconv.OutputValue, "final answer"))
	require.Contains(t, spans[2].Attributes(), attribute.Int64(semconv.LLMTokenCountPrompt, 12))
	require.Contains(t, spans[2].Attributes(), attribute.Int64(semconv.LLMTokenCountCompletion, 5))
	require.Contains(t, spans[2].Attributes(), attribute.Int64(semconv.LLMTokenCountTotal, 17))
	require.Contains(t, spans[2].Attributes(), attribute.Int64(semconv.LLMTokenCountPromptDetailsCacheRead, 3))
	require.Contains(t, spans[2].Attributes(), attribute.Int64(semconv.LLMTokenCountCompletionDetailsReasoning, 2))
	require.Contains(t, spans[1].Attributes(), attribute.String(semconv.InputValue, `{"filePath":"secret.txt"}`))
	require.Contains(t, spans[1].Attributes(), attribute.String(semconv.OutputValue, "file contents"))
}

func TestObservabilityRedactionComesFromConfigObject(t *testing.T) {
	t.Setenv(instrumentation.EnvHideInputs, "false")
	t.Setenv(instrumentation.EnvHideOutputs, "false")

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	looper := testLooper(mockResponses(responseWithMessage("resp-final", "hidden output")))
	looper.agent = Agent{Name: "main"}
	looper.DisplayModel = "gpt-test"
	looper.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test"), TraceConfig: instrumentation.TraceConfig{HideInputs: true, HideOutputs: true}}

	_, _, interrupted, err := looper.runTurn(context.Background(), nil, nil, nil, nil, false, nil, false, false, testPromptInput(PromptInputRoleUser, "hidden input", nil))

	require.NoError(t, err)
	require.False(t, interrupted)

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	require.Contains(t, spans[1].Attributes(), attribute.String(semconv.InputValue, instrumentation.RedactedValue))
	require.Contains(t, spans[1].Attributes(), attribute.String(semconv.OutputValue, instrumentation.RedactedValue))
}

func TestObservabilityEmitsProviderDiagnosticSpanEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		headers := http.Header{"Retry-After-Ms": []string{"2000"}, "X-Request-ID": []string{"req-rate"}}
		calls := 0
		mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
			calls++
			if calls == 1 {
				return nil, openAIError("too_many_requests", "Too Many Requests", headers)
			}

			return responseWithMessage("resp-ok", "done"), nil
		})
		looper := testLooper(mock)
		looper.DisplayModel = "gpt-test"
		looper.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}

		record, rendered, interrupted, err := looper.runTurn(context.Background(), nil, nil, nil, nil, false, nil, false, false, testPromptInput(PromptInputRoleUser, "question", nil))

		require.NoError(t, err)
		require.False(t, interrupted)
		require.Equal(t, "resp-ok", record.ResponseID)
		require.Equal(t, []ChatResponse{assistantMessage("done")}, rendered)

		spans := recorder.Ended()
		require.Len(t, spans, 2)
		require.Equal(t, "rocketcode.provider", spans[0].Name())

		events := spans[0].Events()
		require.Len(t, events, 1)
		require.Equal(t, "rocketcode.provider.diagnostic", events[0].Name)
		require.ElementsMatch(t, []attribute.KeyValue{
			attribute.String("rocketcode.provider_diagnostic.provider_id", "openai"),
			attribute.String("rocketcode.provider_diagnostic.phase", providerDiagnosticRetry),
			attribute.Int("rocketcode.provider_diagnostic.http_status", http.StatusTooManyRequests),
			attribute.String("rocketcode.provider_diagnostic.code", "too_many_requests"),
			attribute.Int("rocketcode.provider_diagnostic.attempt", 1),
			attribute.String("rocketcode.provider_diagnostic.retry_after", "2s"),
			attribute.String("rocketcode.provider_diagnostic.header.retry-after-ms", "2000"),
			attribute.String("rocketcode.provider_diagnostic.header.x-request-id", "req-rate"),
		}, events[0].Attributes)
	})
}

func TestObservabilityRedactsProviderErrorSentinels(t *testing.T) {
	const sentinel = "https://user:token@example.test/private/account-123?api_key=digest-secret"

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	headers := http.Header{
		"X-Request-ID":                 []string{"req-safe"},
		"X-OpenAI-Authorization-Error": []string{sentinel},
		"X-Error-JSON":                 []string{sentinel},
		"X-Codex-Auth-Epoch":           []string{sentinel},
	}
	loop := testLooper(mockResponseError(openAIError("invalid_request", sentinel, headers)))
	loop.Diagnostics = true
	loop.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}
	output := make(chan ChatResponse, 2)

	record, rendered, interrupted, err := loop.runTurn(t.Context(), output, nil, nil, nil, false, nil, false, false, testPromptInput(PromptInputRoleUser, "question", nil))
	require.Error(t, err)
	require.Empty(t, record.ResponseID)
	require.Empty(t, rendered)
	require.False(t, interrupted)
	require.NotContains(t, err.Error(), sentinel)

	diagnostic := <-output
	diagnosticJSON := marshalJSON(t, diagnostic)
	require.NotContains(t, diagnosticJSON, sentinel)
	require.Contains(t, diagnosticJSON, "req-safe")

	for _, span := range recorder.Ended() {
		require.NotContains(t, marshalJSON(t, span.Events()), sentinel)
		require.NotContains(t, span.Status().Description, sentinel)
	}
}

func TestObservabilityUsesResolvedProviderAndModel(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	loop := testLooper(mockResponses(responseWithMessage("resp", "done")))
	loop.Origin = ProviderOrigin{ProviderID: "work", Route: "route", ModelID: "provider-model", AuthenticationEpoch: "secret-epoch"}
	loop.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}

	record, rendered, interrupted, err := loop.runTurn(t.Context(), nil, nil, nil, nil, false, nil, false, false, testPromptInput(PromptInputRoleUser, "question", nil))
	require.NoError(t, err)
	require.Equal(t, "resp", record.ResponseID)
	require.Equal(t, []ChatResponse{assistantMessage("done")}, rendered)
	require.False(t, interrupted)

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	require.Contains(t, spans[0].Attributes(), attribute.String(semconv.LLMProvider, "work"))
	require.Contains(t, spans[0].Attributes(), attribute.String(semconv.LLMModelName, "provider-model"))
	require.NotContains(t, spans[0].Attributes(), attribute.String("rocketcode.authentication_epoch", "secret-epoch"))
	require.Contains(t, spans[1].Attributes(), attribute.String("rocketcode.model", "provider-model"))
}

func TestObservabilityRecordsOrdinaryOriginMismatchHandoff(t *testing.T) {
	const sentinel = "route-secret epoch-secret"

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	destination := ProviderOrigin{ProviderID: "work", Route: "destination-route-secret", ModelID: "worker", AuthenticationEpoch: "destination-epoch-secret"}
	source := ProviderOrigin{ProviderID: "work", Route: destination.Route, ModelID: destination.ModelID, AuthenticationEpoch: "source-epoch-secret"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-id", "readable", "sealed")})
	require.NoError(t, err)

	loop := testLooper(mockResponses(responseWithMessage("resp-1", "done one"), responseWithMessage("resp-2", "done two")))
	loop.Origin = destination
	loop.Diagnostics = true
	loop.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}
	output := make(chan ChatResponse, 2)
	secondOutput := make(chan ChatResponse, 2)

	input := make(chan PromptInput, 2)
	input <- testPromptInput(PromptInputRoleUser, "question", output)

	input <- testPromptInput(PromptInputRoleUser, "follow-up", secondOutput)

	close(input)

	require.NoError(t, loop.Loop(t.Context(), input, sessionEntries([]SessionEntry{{Origin: &source, ReplayInput: replay}}), discardSession, make(chan os.Signal, 1)))

	events := recorder.Ended()[1].Events()
	require.Len(t, events, 1)
	require.Equal(t, "rocketcode.provider.handoff", events[0].Name)
	require.Contains(t, events[0].Attributes, attribute.String("rocketcode.provider_handoff.provider_id", "work"))
	require.Contains(t, events[0].Attributes, attribute.String("rocketcode.provider_handoff.model_id", "worker"))
	require.Contains(t, events[0].Attributes, attribute.Bool("rocketcode.provider_handoff.authentication_change", true))
	require.NotContains(t, marshalJSON(t, events), sentinel)

	diagnostics := collectResponses(output)
	require.Equal(t, "handoff", diagnostics[0].Provider.Phase)
	require.NotContains(t, marshalJSON(t, diagnostics), "epoch-secret")

	handoffs := 0

	for _, span := range recorder.Ended() {
		for _, event := range span.Events() {
			if event.Name == "rocketcode.provider.handoff" {
				handoffs++
			}
		}
	}

	require.Equal(t, 1, handoffs)
}

func TestObservabilityDoesNotRepeatCompletedOriginHandoffAfterRestart(t *testing.T) {
	destination := ProviderOrigin{ProviderID: "work", Route: "destination-route", ModelID: "worker", AuthenticationEpoch: "destination-epoch"}
	source := ProviderOrigin{ProviderID: "source", Route: "source-route", ModelID: "source-model", AuthenticationEpoch: "source-epoch"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-id", "readable", "sealed")})
	require.NoError(t, err)

	first := testLooper(mockResponses(responseWithMessage("resp-1", "handoff complete")))
	first.Origin = destination

	firstInput := make(chan PromptInput, 1)
	firstInput <- testPromptInput(PromptInputRoleUser, "question", make(chan ChatResponse, 2))

	close(firstInput)

	var completed SessionEntry

	require.NoError(t, first.Loop(t.Context(), firstInput, sessionEntries([]SessionEntry{{Origin: &source, ReplayInput: replay}}), func(entry SessionEntry) error {
		completed = entry
		return nil
	}, make(chan os.Signal, 1)))
	require.Equal(t, &destination, completed.Origin)

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	restarted := testLooper(mockResponses(responseWithMessage("resp-2", "continued")))
	restarted.Origin = destination
	restarted.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}

	secondInput := make(chan PromptInput, 1)
	secondInput <- testPromptInput(PromptInputRoleUser, "follow-up", make(chan ChatResponse, 1))

	close(secondInput)

	require.NoError(t, restarted.Loop(t.Context(), secondInput, sessionEntries([]SessionEntry{{Origin: &source, ReplayInput: replay}, completed}), discardSession, make(chan os.Signal, 1)))

	for _, span := range recorder.Ended() {
		for _, event := range span.Events() {
			require.NotEqual(t, "rocketcode.provider.handoff", event.Name)
		}
	}
}

func TestObservabilityRecordsPortableHandoffWithoutAuthenticationEpoch(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	calls := 0
	mock := mockResponseFunc(func(context.Context, *responses.ResponseNewParams) (*responses.Response, error) {
		calls++
		if calls == 1 {
			return nil, &openai.Error{StatusCode: http.StatusBadRequest, Message: "invalid encrypted content"}
		}

		return responseWithMessage("resp-portable", "done"), nil
	})
	loop := testLooper(mock)
	loop.Origin = ProviderOrigin{ProviderID: "work", Route: "route-secret-digest", ModelID: "worker", AuthenticationEpoch: "epoch-secret-token"}
	loop.Diagnostics = true
	loop.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}
	opaque := []responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-legacy", "readable", "sealed")}
	portable, err := projectPortableReplay(opaque)
	require.NoError(t, err)

	output := make(chan ChatResponse, 2)

	_, _, interrupted, err := loop.runTurn(t.Context(), output, nil, opaque, portable, true, nil, false, false, testPromptInput(PromptInputRoleUser, "question", nil))
	require.NoError(t, err)
	require.False(t, interrupted)
	close(output)
	diagnostics := collectResponses(output)
	handoffs := 0

	for _, diagnostic := range diagnostics {
		if diagnostic.Provider != nil && diagnostic.Provider.Phase == "handoff" {
			handoffs++

			require.Equal(t, &ProviderDiagnostic{ProviderID: "work", ModelID: "worker", Phase: "handoff"}, diagnostic.Provider)
		}
	}

	require.Equal(t, 1, handoffs)
	require.NotContains(t, marshalJSON(t, diagnostics), "epoch-secret-token")
	require.NotContains(t, marshalJSON(t, diagnostics), "route-secret-digest")

	spans := recorder.Ended()
	require.Len(t, spans, 3)
	events := spans[2].Events()
	require.Len(t, events, 1)
	require.Equal(t, "rocketcode.provider.handoff", events[0].Name)
	require.ElementsMatch(t, []attribute.KeyValue{
		attribute.String("rocketcode.provider_handoff.provider_id", "work"),
		attribute.String("rocketcode.provider_handoff.model_id", "worker"),
		attribute.Bool("rocketcode.provider_handoff.legacy", true),
	}, events[0].Attributes)
	eventJSON := marshalJSON(t, events)
	require.NotContains(t, eventJSON, "epoch-secret-token")
	require.NotContains(t, eventJSON, "route-secret-digest")
}
