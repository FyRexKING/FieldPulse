package otel

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer

// InitTracing initializes the package tracer.
func InitTracing(serviceName string) error {
	tracer = otel.Tracer(serviceName)
	log.Printf("✓ OpenTelemetry tracer initialized for %s", serviceName)
	return nil
}

// GetTracer returns the global tracer.
func GetTracer() trace.Tracer {
	if tracer == nil {
		tracer = otel.Tracer("fieldpulse-default")
	}
	return tracer
}

// ShutdownTracing is currently a no-op for default global provider setup.
func ShutdownTracing(_ context.Context) error {
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
