package backend

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

func configureInstrumentation(ctx context.Context, cfg config.InstrumentationConfig, posthog config.PostHogConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", "rocketclaw"))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	}

	export := func(endpoint string, headers map[string]string, batchSize int) error {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithHeaders(headers), otlptracehttp.WithTimeout(10*time.Second))
		if err != nil {
			return fmt.Errorf("initialize OTLP trace exporter: %w", err)
		}

		options = append(options, sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithMaxExportBatchSize(batchSize),
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithExportTimeout(30*time.Second),
		))

		return nil
	}

	if cfg.CollectorEndpoint != "" {
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

		if err := export(endpoint, headers, 512); err != nil {
			return nil, err
		}
	}

	if posthog.ProjectToken != "" {
		// PostHog's AI endpoint keeps only gen_ai spans, for LLM analytics; its traces endpoint keeps every span.
		// It rejects exports over 4 MB, and spans carry whole tool outputs, so its batches stay small.
		for _, path := range []string{"/i/v0/ai/otel", "/i/v1/traces"} {
			if err := export(posthog.Host+path, map[string]string{"Authorization": "Bearer " + posthog.ProjectToken}, 32); err != nil {
				return nil, err
			}
		}
	}

	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)

	return func(shutdownCtx context.Context) error {
		otel.SetTracerProvider(previous)
		return provider.Shutdown(shutdownCtx)
	}, nil
}
