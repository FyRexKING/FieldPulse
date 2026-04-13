//go:build integration

package integration

import "os"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	deviceServiceAddr    = envOr("FIELDPULSE_DEVICE_SERVICE", "localhost:50051")
	telemetryServiceAddr = envOr("FIELDPULSE_TELEMETRY_SERVICE", "localhost:50052")
	alertServiceAddr     = envOr("FIELDPULSE_ALERT_SERVICE", "localhost:50053")
	dbConnStr = envOr("FIELDPULSE_POSTGRES_URL", "postgres://fieldpulse:fieldpulse_dev@localhost:5432/fieldpulse")
)
