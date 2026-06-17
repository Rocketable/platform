package harnessbridge

import (
	"github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func recordRocketClawSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func rocketclawInputValue(cfg *config.Config, text string) attribute.KeyValue {
	traceConfig := instrumentation.TraceConfig{HideInputs: cfg.Instrumentation.HideInputs, HideOutputs: cfg.Instrumentation.HideOutputs}

	return attribute.String(semconv.InputValue, traceConfig.MaskInputValue(text))
}

func rocketclawOutputValue(cfg *config.Config, text string) attribute.KeyValue {
	traceConfig := instrumentation.TraceConfig{HideInputs: cfg.Instrumentation.HideInputs, HideOutputs: cfg.Instrumentation.HideOutputs}

	return attribute.String(semconv.OutputValue, traceConfig.MaskOutputValue(text))
}
