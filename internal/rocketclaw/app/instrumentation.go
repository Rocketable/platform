package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func configureInstrumentation(ctx context.Context, cfg config.InstrumentationConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	endpoint := strings.TrimRight(cfg.CollectorEndpoint, "/")
	if !strings.HasSuffix(endpoint, "/v1/traces") {
		endpoint += "/v1/traces"
	}

	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("parse instrumentation.collector_endpoint: %q", cfg.CollectorEndpoint)
	}

	headers := map[string]string{}
	if cfg.ProjectName != "" {
		headers["x-project-name"] = cfg.ProjectName
	}

	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithHeaders(headers), otlptracehttp.WithTimeout(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
	}

	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(512),
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithExportTimeout(30*time.Second),
		),
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", "rocketclaw"))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(provider)

	return func(shutdownCtx context.Context) error {
		otel.SetTracerProvider(previous)
		return provider.Shutdown(shutdownCtx)
	}, nil
}
