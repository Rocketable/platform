package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func configureInstrumentation(ctx context.Context, posthog config.PostHogConfig) (func(context.Context) error, error) {
	if posthog.ProjectToken == "" {
		return func(context.Context) error { return nil }, nil
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", "rocketclaw"))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	}

	for _, path := range []string{"/i/v0/ai/otel", "/i/v1/traces"} {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(posthog.Host+path), otlptracehttp.WithHeaders(map[string]string{"Authorization": "Bearer " + posthog.ProjectToken}), otlptracehttp.WithTimeout(10*time.Second))
		if err != nil {
			return nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
		}

		options = append(options, sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(32),
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithExportTimeout(30*time.Second),
		))
	}

	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)

	return func(shutdownCtx context.Context) error {
		otel.SetTracerProvider(previous)
		return provider.Shutdown(shutdownCtx)
	}, nil
}
