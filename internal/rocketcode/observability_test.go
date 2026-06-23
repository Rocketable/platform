package rocketcode

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
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
	looper.DisplayModel = "openai/gpt-test"
	looper.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}
	looper.Permissions = PermissionSet{Buckets: []PermissionBucket{{Name: "read", Rules: []PermissionRule{{Pattern: "*", Action: permissionAllow}}}}}
	tool := testLooperTool("read")
	tool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
		return TextToolResult("file contents"), nil
	}
	looper.Tools = map[string]looperTool{"read": tool}

	record, rendered, interrupted, err := looper.runTurn(context.Background(), nil, nil, nil, testPromptInput(PromptInputRoleUser, "inspect secret", nil))

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
	looper.DisplayModel = "openai/gpt-test"
	looper.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test"), TraceConfig: instrumentation.TraceConfig{HideInputs: true, HideOutputs: true}}

	_, _, interrupted, err := looper.runTurn(context.Background(), nil, nil, nil, testPromptInput(PromptInputRoleUser, "hidden input", nil))

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
		looper.DisplayModel = "openai/gpt-test"
		looper.Observability = ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}

		record, rendered, interrupted, err := looper.runTurn(context.Background(), nil, nil, nil, testPromptInput(PromptInputRoleUser, "question", nil))

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
			attribute.String("rocketcode.provider_diagnostic.phase", providerDiagnosticRetry),
			attribute.Int("rocketcode.provider_diagnostic.http_status", http.StatusTooManyRequests),
			attribute.String("rocketcode.provider_diagnostic.code", "too_many_requests"),
			attribute.String("rocketcode.provider_diagnostic.message", "Too Many Requests"),
			attribute.Int("rocketcode.provider_diagnostic.attempt", 1),
			attribute.String("rocketcode.provider_diagnostic.retry_after", "2s"),
			attribute.String("rocketcode.provider_diagnostic.header.retry-after-ms", "2000"),
			attribute.String("rocketcode.provider_diagnostic.header.x-request-id", "req-rate"),
		}, events[0].Attributes)
	})
}
