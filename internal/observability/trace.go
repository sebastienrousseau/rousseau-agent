package observability

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Tracer is the process-wide OpenTelemetry tracer. When [StartOTel]
// has not been called it resolves to the global noop tracer, so every
// call site that starts a span using it is safe with or without an
// OTLP endpoint configured.
var Tracer = otel.Tracer("github.com/sebastienrousseau/rousseau-agent")

// StartOTel wires an OTLP-over-HTTP tracer against the supplied
// endpoint (e.g. "http://otel-collector:4318"). It returns a Shutdown
// function that flushes the exporter buffer with a small timeout.
//
// If endpoint is empty the function is a noop and Shutdown does
// nothing; this makes it safe to call from every daemon entry point
// regardless of whether tracing is enabled.
func StartOTel(ctx context.Context, endpoint, serviceVersion string, logger *slog.Logger) (shutdown func(context.Context) error, err error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	exporter, err := otlptrace.New(ctx, otlptracehttp.NewClient(
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithTimeout(10*time.Second),
	))
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("rousseau-agent"),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	Tracer = tp.Tracer("github.com/sebastienrousseau/rousseau-agent")
	logger.Info("otel.started", slog.String("endpoint", endpoint))
	return tp.Shutdown, nil
}

// StartSpan is a convenience wrapper matching the ergonomics of slog:
// call `ctx, span := observability.StartSpan(ctx, "agent.turn", …)`
// and defer span.End(). Tracer selection follows the process-wide
// tracer set by [StartOTel]; without OTel configured it's a noop.
func StartSpan(ctx context.Context, name string, kv ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer.Start(ctx, name, trace.WithAttributes(kv...))
}
