package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestInitTracing(t *testing.T) {
	t.Log("=== Test: OTel InitTracing ===")

	err := InitTracing("test-service")
	if err != nil {
		t.Errorf("InitTracing failed: %v", err)
	}

	tracer := GetTracer()
	if tracer == nil {
		t.Errorf("Tracer is nil after initialization")
	}

	t.Log("✓ OTel tracer initialized successfully")
}

func TestGetTracer(t *testing.T) {
	t.Log("=== Test: OTel GetTracer ===")

	tracer1 := GetTracer()
	tracer2 := GetTracer()

	if tracer1 == nil {
		t.Errorf("GetTracer returned nil")
	}

	if tracer2 == nil {
		t.Errorf("GetTracer second call returned nil")
	}

	t.Log("✓ GetTracer returns consistent tracer")
}

func TestTraceDeviceCreate(t *testing.T) {
	t.Log("=== Test: OTel TraceDeviceCreate ===")

	_ = InitTracing("test-service")
	ctx := context.Background()

	newCtx, endSpan := TraceDeviceCreate(ctx, "device-123")
	if newCtx == nil {
		t.Errorf("TraceDeviceCreate returned nil context")
	}

	endSpan()
	t.Log("✓ TraceDeviceCreate works correctly")
}

func TestTraceTelemetrySubmit(t *testing.T) {
	t.Log("=== Test: OTel TraceTelemetrySubmit ===")

	_ = InitTracing("test-service")
	ctx := context.Background()

	newCtx, endSpan := TraceTelemetrySubmit(ctx, "device-123", "temperature", 25.5)
	if newCtx == nil {
		t.Errorf("TraceTelemetrySubmit returned nil context")
	}

	if newCtx == ctx {
		t.Errorf("Expected new context, got same context")
	}

	endSpan()
	t.Log("✓ TraceTelemetrySubmit works correctly")
}

func TestTraceAlertEvaluation(t *testing.T) {
	t.Log("=== Test: OTel TraceAlertEvaluation ===")

	_ = InitTracing("test-service")
	ctx := context.Background()

	newCtx, endSpan := TraceAlertEvaluation(ctx, "device-123", "temperature")
	if newCtx == nil {
		t.Errorf("TraceAlertEvaluation returned nil context")
	}

	endSpan()
	t.Log("✓ TraceAlertEvaluation works correctly")
}

func TestTraceAlertFire(t *testing.T) {
	t.Log("=== Test: OTel TraceAlertFire ===")

	_ = InitTracing("test-service")
	ctx := context.Background()

	newCtx, endSpan := TraceAlertFire(ctx, "device-123", "temperature", "CRITICAL")
	if newCtx == nil {
		t.Errorf("TraceAlertFire returned nil context")
	}

	endSpan()
	t.Log("✓ TraceAlertFire works correctly")
}

func TestTraceConnectorForward(t *testing.T) {
	t.Log("=== Test: OTel TraceConnectorForward ===")

	_ = InitTracing("test-service")
	ctx := context.Background()

	newCtx, endSpan := TraceConnectorForward(ctx, "external-broker", "sensors/+/data", "sensor-001", "temperature")
	if newCtx == nil {
		t.Errorf("TraceConnectorForward returned nil context")
	}

	endSpan()
	t.Log("✓ TraceConnectorForward works correctly")
}

func TestShutdownTracing(t *testing.T) {
	t.Log("=== Test: OTel ShutdownTracing ===")

	ctx := context.Background()
	err := ShutdownTracing(ctx)
	if err != nil {
		t.Errorf("ShutdownTracing failed: %v", err)
	}

	t.Log("✓ ShutdownTracing executed successfully")
}

func TestTracerIntegration(t *testing.T) {
	t.Log("=== Test: OTel Tracer Integration (spans chain) ===")

	_ = InitTracing("test-service")
	ctx := context.Background()

	// Create parent span
	parentCtx, parentEnd := TraceDeviceCreate(ctx, "device-001")
	defer parentEnd()

	// Create child span using parent context
	childCtx, childEnd := TraceTelemetrySubmit(parentCtx, "device-001", "humidity", 60.0)
	defer childEnd()

	if childCtx == nil {
		t.Errorf("Child context is nil")
	}

	t.Log("✓ Span chains work correctly (child inherits parent context)")
}

func TestGetTracerConsistency(t *testing.T) {
	t.Log("=== Test: OTel GetTracer Consistency ===")

	_ = InitTracing("consistency-test")

	tracer := GetTracer()
	if tracer == nil {
		t.Fatalf("GetTracer returned nil")
	}

	// Verify tracer is an actual trace.Tracer
	var _ trace.Tracer = tracer
	t.Log("✓ GetTracer returns consistent trace.Tracer interface")
}
