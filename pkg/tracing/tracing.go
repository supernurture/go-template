// Package tracing sets up OpenTelemetry tracing over OTLP/HTTP and puts the active span's IDs on log lines.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Config describes the service the spans come from and where they are sent.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Env            string
	// Endpoint is the collector's OTLP/HTTP base URL; spans go to <Endpoint>/v1/traces, http:// without TLS.
	// Empty falls back to OTEL_EXPORTER_OTLP_ENDPOINT, then localhost:4318.
	Endpoint string
	// SampleRatio is the share of new traces kept, 0 to 1; a caller's traceparent decision wins.
	SampleRatio float64
}

// newExporter is a seam: otlptracehttp.New does not dial, so a test cannot make it fail otherwise.
var newExporter = otlptracehttp.New

// New builds a TracerProvider that batches spans without dialing, so a down collector drops spans
// rather than failing the start. Shutdown flushes the buffer.
func New(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return nil, fmt.Errorf("sample ratio %v is outside 0..1", cfg.SampleRatio)
	}

	var opts []otlptracehttp.Option
	if cfg.Endpoint != "" {
		if err := checkEndpoint(cfg.Endpoint); err != nil {
			return nil, err
		}
		opts = append(opts, otlptracehttp.WithEndpointURL(strings.TrimRight(cfg.Endpoint, "/")+"/v1/traces"))
	}
	exporter, err := newExporter(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("build OTLP exporter: %w", err)
	}

	// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES come last, so a deployment can override the config.
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.ServiceVersion),
			attribute.String("deployment.environment.name", cfg.Env),
		),
		resource.WithFromEnv(),
	)
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return nil, fmt.Errorf("build resource: %w", err)
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	), nil
}

// Propagator reads and writes the W3C traceparent and baggage headers, so a trace crosses services.
func Propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

// LogFields is a logger.ContextFields hook adding trace_id, span_id and trace_flags, named as the OTel
// spec asks of non-OTLP logs; nothing without a span.
func LogFields(ctx context.Context) map[string]any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return map[string]any{
		"trace_id":    sc.TraceID().String(),
		"span_id":     sc.SpanID().String(),
		"trace_flags": sc.TraceFlags().String(),
	}
}

// checkEndpoint catches a typo at startup; the exporter would otherwise fail on every export.
func checkEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("tracing endpoint: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("tracing endpoint must be an http:// or https:// URL with a host, e.g. http://localhost:4318")
	}
	return nil
}
