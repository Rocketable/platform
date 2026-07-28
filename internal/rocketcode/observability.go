package rocketcode

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type observabilitySpan struct {
	span trace.Span
}

func (o ObservabilityConfig) startSpan(ctx context.Context, name, kind string, attrs ...attribute.KeyValue) (context.Context, observabilitySpan) {
	tracer := noop.NewTracerProvider().Tracer("rocketcode")
	if !o.Enabled {
		ctx, span := tracer.Start(ctx, name)

		return ctx, observabilitySpan{span: span}
	}

	ctx, span := o.Tracer.Start(ctx, name)
	instrumentation.ApplyContextAttributes(ctx, span)
	span.SetAttributes(attribute.String(semconv.OpenInferenceSpanKind, kind))
	span.SetAttributes(attrs...)

	return ctx, observabilitySpan{span: span}
}

func recordSpanError(span observabilitySpan, err error) {
	if err == nil {
		return
	}

	span.span.RecordError(err)
	span.span.SetStatus(codes.Error, err.Error())
}

func recordProviderDiagnosticEvent(ctx context.Context, diagnostic *ProviderDiagnostic) {
	attrs := []attribute.KeyValue{
		attribute.String("rocketcode.provider_diagnostic.provider", diagnostic.Provider),
		attribute.String("rocketcode.provider_diagnostic.model", diagnostic.Model),
		attribute.String("rocketcode.provider_diagnostic.phase", diagnostic.Phase),
	}

	if diagnostic.HTTPStatus != 0 {
		attrs = append(attrs, attribute.Int("rocketcode.provider_diagnostic.http_status", diagnostic.HTTPStatus))
	}

	if diagnostic.ResponseStatus != "" {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.response_status", diagnostic.ResponseStatus))
	}

	if diagnostic.Code != "" {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.code", diagnostic.Code))
	}

	if diagnostic.Type != "" {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.type", diagnostic.Type))
	}

	if diagnostic.Message != "" {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.message", diagnostic.Message))
	}

	if diagnostic.Attempt != 0 {
		attrs = append(attrs, attribute.Int("rocketcode.provider_diagnostic.attempt", diagnostic.Attempt))
	}

	if diagnostic.RetryAfter != "" {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.retry_after", diagnostic.RetryAfter))
	}

	if diagnostic.ResponseID != "" {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.response_id", diagnostic.ResponseID))
	}

	for name, value := range diagnostic.Headers {
		attrs = append(attrs, attribute.String("rocketcode.provider_diagnostic.header."+name, value))
	}

	trace.SpanFromContext(ctx).AddEvent("rocketcode.provider.diagnostic", trace.WithAttributes(attrs...))
}

func (o ObservabilityConfig) inputValue(text string) attribute.KeyValue {
	return attribute.String(semconv.InputValue, o.TraceConfig.MaskInputValue(text))
}

func (o ObservabilityConfig) outputValue(text string) attribute.KeyValue {
	return attribute.String(semconv.OutputValue, o.TraceConfig.MaskOutputValue(text))
}

func (o ObservabilityConfig) startToolSpan(ctx context.Context, name, callID, permission string, args json.RawMessage, metadata toolCallMetadata) (context.Context, observabilitySpan) {
	attrs := []attribute.KeyValue{
		attribute.String(semconv.ToolName, name),
		attribute.String(semconv.ToolID, callID),
		attribute.String(semconv.InputMimeType, semconv.MimeTypeJSON),
		o.inputValue(string(args)),
		attribute.String("rocketcode.permission", permission),
		attribute.Int("rocketcode.subagent_index", metadata.subagentIndex),
		attribute.Int("rocketcode.subagent_total", metadata.subagentTotal),
	}

	return o.startSpan(ctx, "rocketcode.tool."+name, semconv.SpanKindTool, attrs...)
}

func assistantOutputText(items []ChatResponse) string {
	parts := []string{}

	for i := range items {
		if items[i].Kind == ChatResponseAssistantMessage && strings.TrimSpace(items[i].Text) != "" {
			parts = append(parts, items[i].Text)
		}
	}

	return strings.Join(parts, "\n")
}
