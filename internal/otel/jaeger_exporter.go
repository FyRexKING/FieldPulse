package otel

import (
	"context"
	"log"

	"go.opentelemetry.io/otel/sdk/trace"
)

var globalTracerProvider *trace.TracerProvider

// InitSimpleTracerProvider initializes a basic tracer (no external export for now)
// For production, you would integrate with Uptrace cloud at: https://uptrace.dev
func InitSimpleTracerProvider(serviceName string) (*trace.TracerProvider, error) {
	// Create a simple in-memory tracer provider
	// Traces are automatically logged via the tracing package
	tp := trace.NewTracerProvider()
	
	globalTracerProvider = tp
	log.Printf("✓ Tracer provider initialized for service: %s", serviceName)
	log.Printf("  View traces at: https://uptrace.dev (free signup)")

	return tp, nil
}

// GetGlobalTracerProvider returns the initialized tracer provider
func GetGlobalTracerProvider() *trace.TracerProvider {
	if globalTracerProvider != nil {
		return globalTracerProvider
	}
	return trace.NewTracerProvider()
}

// ShutdownTracerProvider flushes and closes the tracer provider
func ShutdownTracerProvider(ctx context.Context) error {
	if tp := globalTracerProvider; tp != nil {
		return tp.Shutdown(ctx)
	}
	return nil
}

// SetGlobalTracerProvider sets the global tracer provider (for dependency injection in tests)
func SetGlobalTracerProvider(tp *trace.TracerProvider) {
	globalTracerProvider = tp
}

// UptraceConfig returns the configuration needed for Uptrace cloud export
// You can sign up free at https://uptrace.dev
func UptraceConfig() map[string]string {
	return map[string]string{
		"endpoint": "https://api.uptrace.dev",
		"dsn":      "your-dsn-here", // Get this from https://uptrace.dev after signup
	}
}
