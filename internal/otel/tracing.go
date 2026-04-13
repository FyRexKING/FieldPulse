package otel

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer
var tracerProvider *sdktrace.TracerProvider

// InitTracing initializes the package tracer.
func InitTracing(serviceName string) error {
	if !otelEnabled() {
		tracer = otel.Tracer(serviceName)
		log.Printf("✓ OpenTelemetry disabled; tracer initialized for %s", serviceName)
		return nil
	}

	// If already initialized, reuse.
	if tracerProvider != nil {
		tracer = otel.Tracer(serviceName)
		log.Printf("✓ OpenTelemetry tracer reused for %s", serviceName)
		return nil
	}

	uptraceDSN := strings.TrimSpace(os.Getenv("UPTRACE_DSN"))
	if uptraceDSN == "" {
		// Allow running without export (local dev/tests) even when OTEL_ENABLED=true.
		tracer = otel.Tracer(serviceName)
		log.Printf("⚠️ OpenTelemetry enabled but UPTRACE_DSN is empty; spans will not be exported (service=%s)", serviceName)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Uptrace Cloud OTLP/HTTP endpoint.
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		endpoint = "https://api.uptrace.dev"
	}
	endpoint = strings.TrimSpace(strings.TrimSuffix(endpoint, "/"))

	// otlptracehttp.WithEndpoint expects "host:port" without scheme.
	// Users often provide "https://api.uptrace.dev" (as in Uptrace docs), so normalize.
	insecure := false
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			insecure = true
		case "https":
			insecure = false
		}
		endpoint = u.Host
	} else {
		// If endpoint was provided as host[:port] without scheme, keep as-is.
		// If it's accidentally "https://..." but failed to parse for some reason, fall back to stripping prefix.
		endpoint = strings.TrimPrefix(endpoint, "https://")
		endpoint = strings.TrimPrefix(endpoint, "http://")
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithURLPath("/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"uptrace-dsn": uptraceDSN,
		}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(
		ctx,
		opts...,
	)
	if err != nil {
		return fmt.Errorf("init otlp trace exporter: %w", err)
	}

	res, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return fmt.Errorf("init otel resource: %w", err)
	}

	bsp := sdktrace.NewBatchSpanProcessor(
		exp,
		sdktrace.WithMaxQueueSize(10_000),
		sdktrace.WithMaxExportBatchSize(10_000),
		sdktrace.WithExportTimeout(10*time.Second),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)
	tracerProvider = tp
	otel.SetTracerProvider(tp)

	tracer = otel.Tracer(serviceName)
	log.Printf("✓ OpenTelemetry tracing export enabled for %s (endpoint=%s)", serviceName, endpoint)
	return nil
}

// GetTracer returns the global tracer.
func GetTracer() trace.Tracer {
	if tracer == nil {
		tracer = otel.Tracer("fieldpulse-default")
	}
	return tracer
}

// ShutdownTracing flushes spans and closes exporter if configured.
func ShutdownTracing(ctx context.Context) error {
	if tp := tracerProvider; tp != nil {
		return tp.Shutdown(ctx)
	}
	return nil
}

func TraceDeviceCreate(ctx context.Context, deviceID string) (context.Context, func()) {
	newCtx, span := GetTracer().Start(ctx, "device.create")
	span.SetAttributes(attribute.String("device.id", deviceID))
	return newCtx, func() { span.End() }
}

func TraceTelemetrySubmit(ctx context.Context, deviceID, metricName string, value float64) (context.Context, func()) {
	newCtx, span := GetTracer().Start(ctx, "telemetry.submit")
	span.SetAttributes(
		attribute.String("device.id", deviceID),
		attribute.String("metric.name", metricName),
		attribute.Float64("metric.value", value),
	)
	return newCtx, func() { span.End() }
}

func TraceAlertEvaluation(ctx context.Context, deviceID, metricName string) (context.Context, func()) {
	newCtx, span := GetTracer().Start(ctx, "alert.evaluate")
	span.SetAttributes(
		attribute.String("device.id", deviceID),
		attribute.String("metric.name", metricName),
	)
	return newCtx, func() { span.End() }
}

func TraceAlertFire(ctx context.Context, deviceID, metricName, severity string) (context.Context, func()) {
	newCtx, span := GetTracer().Start(ctx, "alert.fire")
	span.SetAttributes(
		attribute.String("device.id", deviceID),
		attribute.String("metric.name", metricName),
		attribute.String("severity", severity),
	)
	return newCtx, func() { span.End() }
}

func TraceConnectorForward(ctx context.Context, brokerName, topic, deviceID, metricName string) (context.Context, func()) {
	newCtx, span := GetTracer().Start(ctx, "connector.forward")
	span.SetAttributes(
		attribute.String("broker.name", brokerName),
		attribute.String("topic", topic),
		attribute.String("device.id", deviceID),
		attribute.String("metric.name", metricName),
	)
	return newCtx, func() { span.End() }
}

func otelEnabled() bool {
	v := strings.TrimSpace(os.Getenv("OTEL_ENABLED"))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
