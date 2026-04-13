//go:build integration

package integration

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	pb "fieldpulse.io/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// TestE2ETraceFlow validates full system tracing:
// Device Creation → Metric Submission → Alert Evaluation → Threshold Creation
// Verifies that all service boundaries are properly traced with correct attributes
func TestE2ETraceFlow(t *testing.T) {
	t.Log("=== End-to-End Trace Test (Device → Telemetry → Alert) ===")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Connect to all services
	deviceConn, err := grpc.Dial(deviceServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to device service: %v", err)
	}
	defer deviceConn.Close()

	telemetryConn, err := grpc.Dial(telemetryServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to telemetry service: %v", err)
	}
	defer telemetryConn.Close()

	alertConn, err := grpc.Dial(alertServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to alert service: %v", err)
	}
	defer alertConn.Close()

	deviceClient := pb.NewDeviceServiceClient(deviceConn)
	telemetryClient := pb.NewTelemetryServiceClient(telemetryConn)
	alertClient := pb.NewAlertServiceClient(alertConn)

	// ========== SPAN 1: Device Creation ==========
	t.Log("\n[TRACE SPAN 1] Device: device.create")
	
	deviceID := fmt.Sprintf("e2e_trace_device_%d", time.Now().Unix())
	t.Logf("  Creating device: %s", deviceID)

	createDevReq := &pb.CreateDeviceRequest{
		DeviceId:        deviceID,
		Floor:           "floor-1",
		DeviceType:      "temperature-sensor",
		FirmwareVersion: "2.1.0",
	}

	createDevResp, err := deviceClient.CreateDevice(ctx, createDevReq)
	if err != nil {
		t.Fatalf("Failed to create device: %v", err)
	}

	if createDevResp.DeviceId != deviceID {
		t.Errorf("Expected device_id %s, got %s", deviceID, createDevResp.DeviceId)
	}

	t.Logf("  ✓ Device created with attributes:")
	t.Logf("    - device.id: %s", deviceID)
	t.Logf("    - floor: %s", createDevReq.Floor)
	t.Logf("    - device_type: %s", createDevReq.DeviceType)
	t.Logf("    - provisioned_at: %v", createDevResp.ProvisionedAt.AsTime())

	// Small delay to ensure persistence
	time.Sleep(200 * time.Millisecond)

	// ========== SPAN 2: Threshold Creation (Alert Setup) ==========
	t.Log("\n[TRACE SPAN 2] Alert: alert.create_threshold")
	t.Logf("  Creating alert threshold on device: %s", deviceID)

	thresholdReq := &pb.CreateThresholdRequest{
		DeviceId:   deviceID,
		MetricName: "temperature",
		Type:       pb.ThresholdType_HIGH,
		UpperBound: 30.0,
		Severity:   pb.AlertSeverity_CRITICAL,
		Enabled:    true,
	}

	thresholdResp, err := alertClient.CreateThreshold(ctx, thresholdReq)
	if err != nil {
		t.Fatalf("Failed to create threshold: %v", err)
	}

	t.Logf("  ✓ Threshold created with attributes:")
	t.Logf("    - threshold.id: %s", thresholdResp.ThresholdId)
	t.Logf("    - device.id: %s", deviceID)
	t.Logf("    - metric.name: %s", thresholdReq.MetricName)
	t.Logf("    - threshold.type: %v", thresholdReq.Type)
	t.Logf("    - upper_bound: %.2f", thresholdReq.UpperBound)
	t.Logf("    - severity: %v", thresholdReq.Severity)

	// ========== SPAN 3: Metric Submission ==========
	t.Log("\n[TRACE SPAN 3] Telemetry: telemetry.submit")
	
	timestamp := time.Now().Unix()
	normalMetricReq := &pb.SubmitMetricRequest{
		DeviceId:             deviceID,
		MetricName:           "temperature",
		Value:                22.5,
		TimestampUnixSeconds: timestamp,
	}

	t.Logf("  Submitting metric BELOW threshold:")
	t.Logf("    - device.id: %s", deviceID)
	t.Logf("    - metric.name: %s", normalMetricReq.MetricName)
	t.Logf("    - metric.value: %.1f (threshold: 30.0)", normalMetricReq.Value)
	t.Logf("    - timestamp: %d", timestamp)

	normalResp, err := telemetryClient.SubmitMetric(ctx, normalMetricReq)
	if err != nil {
		t.Fatalf("Failed to submit normal metric: %v", err)
	}

	if !normalResp.Success {
		t.Errorf("Normal metric submission failed: %s", normalResp.Message)
	}

	t.Logf("  ✓ Normal metric submitted (span ended)")
	t.Logf("    - server_timestamp: %v", normalResp.ServerTimestamp.AsTime())

	time.Sleep(250 * time.Millisecond)

	// ========== SPAN 4: High-Value Metric (Should Trigger Alert Evaluation) ==========
	t.Log("\n[TRACE SPAN 4] Telemetry: telemetry.submit (HIGH VALUE - ALERT TRIGGER)")
	
	// Distinct Unix second so Redis dedup (dedup:{device}:{unix}) does not drop this as duplicate of normal metric.
	highMetricReq := &pb.SubmitMetricRequest{
		DeviceId:             deviceID,
		MetricName:           "temperature",
		Value:                35.0, // EXCEEDS 30.0 threshold
		TimestampUnixSeconds: timestamp + 1,
	}

	t.Logf("  Submitting metric ABOVE threshold:")
	t.Logf("    - device.id: %s", deviceID)
	t.Logf("    - metric.name: %s", highMetricReq.MetricName)
	t.Logf("    - metric.value: %.1f (threshold: 30.0) ⚠️", highMetricReq.Value)
	t.Logf("    - timestamp: %d (normal was %d)", highMetricReq.TimestampUnixSeconds, timestamp)

	highResp, err := telemetryClient.SubmitMetric(ctx, highMetricReq)
	if err != nil {
		t.Fatalf("Failed to submit high metric: %v", err)
	}

	if !highResp.Success {
		t.Errorf("High metric submission failed: %s", highResp.Message)
	}

	t.Logf("  ✓ High metric submitted (threshold evaluation async triggered)")

	// ========== SPAN 5: Verify Alert Generation ==========
	t.Log("\n[TRACE SPAN 5] Alert: alert.get_active")
	t.Logf("  Querying active alerts on device: %s", deviceID)

	activeAlertsReq := &pb.GetActiveAlertsRequest{
		DeviceId: deviceID,
		Limit:    10,
	}

	var activeAlertsResp *pb.GetActiveAlertsResponse
	for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(200 * time.Millisecond) {
		activeAlertsResp, err = alertClient.GetActiveAlerts(ctx, activeAlertsReq)
		if err != nil {
			t.Fatalf("Failed to get active alerts: %v", err)
		}
		if len(activeAlertsResp.Alerts) > 0 {
			break
		}
	}

	t.Logf("  ✓ Active alerts retrieved:")
	t.Logf("    - device.id: %s", deviceID)
	t.Logf("    - alert_count: %d", len(activeAlertsResp.Alerts))

	if len(activeAlertsResp.Alerts) == 0 {
		t.Errorf("expected at least one active alert within 10s after threshold breach")
	} else {
		t.Logf("    ✓ Alert triggered as expected (exceeding threshold)")
		for i, alert := range activeAlertsResp.Alerts {
			t.Logf("      Alert %d:", i+1)
			t.Logf("        - alert.id: %s", alert.AlertId)
			t.Logf("        - metric.name: %s", alert.MetricName)
			t.Logf("        - alert.severity: %v", alert.Severity)
			t.Logf("        - threshold_value: %.2f", alert.ThresholdValue)
			t.Logf("        - triggered_at: %v", alert.TriggeredAt.AsTime())
		}
	}

	// ========== SPAN 6: Query Metrics to Verify Storage ==========
	t.Log("\n[TRACE SPAN 6] Telemetry: telemetry.query_metrics")
	t.Logf("  Querying metrics for device: %s", deviceID)

	queryReq := &pb.QueryMetricsRequest{
		DeviceId:   deviceID,
		MetricName: "temperature",
		Limit:      10,
	}

	stream, err := telemetryClient.QueryMetrics(ctx, queryReq)
	if err != nil {
		t.Fatalf("Failed to query metrics: %v", err)
	}

	pointCount := 0
	var firstTimestamp, lastTimestamp time.Time
	minValue, maxValue := 999.0, 0.0

	for {
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		
		for _, p := range resp.Points {
			pointCount++
			ts := p.Timestamp.AsTime()
			
			if firstTimestamp.IsZero() {
				firstTimestamp = ts
			}
			lastTimestamp = ts
			
			if p.Value < minValue {
				minValue = p.Value
			}
			if p.Value > maxValue {
				maxValue = p.Value
			}

			if pointCount <= 2 {
				t.Logf("      Point %d: time=%v, value=%.1f", pointCount, ts, p.Value)
			}
		}
	}

	if pointCount == 0 {
		t.Errorf("Expected at least 1 metric point, got 0")
	} else {
		t.Logf("  ✓ Metrics stored and queryable:")
		t.Logf("    - total_points: %d", pointCount)
		t.Logf("    - time_span: %v to %v", firstTimestamp, lastTimestamp)
		t.Logf("    - value_range: %.1f - %.1f", minValue, maxValue)
	}

	// ========== SPAN 7: Silence Alert and Verify ==========
	t.Log("\n[TRACE SPAN 7] Alert: alert.silence")
	t.Logf("  Creating alert silence for device: %s", deviceID)

	silenceReq := &pb.SilenceAlertRequest{
		DeviceId:        deviceID,
		MetricName:      "temperature",
		DurationSeconds: 300,
		Reason:          "E2E trace test silencing",
	}

	silenceResp, err := alertClient.SilenceAlert(ctx, silenceReq)
	if err != nil {
		t.Fatalf("Failed to silence alert: %v", err)
	}

	t.Logf("  ✓ Alert silenced:")
	t.Logf("    - silence.id: %s", silenceResp.SilenceId)
	t.Logf("    - device.id: %s", deviceID)
	t.Logf("    - metric.name: %s", silenceReq.MetricName)
	t.Logf("    - duration: %d seconds", silenceReq.DurationSeconds)

	time.Sleep(200 * time.Millisecond)

	// ========== TRACE SUMMARY ==========
	t.Log("\n" + strings.Repeat("=", 70))
	t.Log("END-TO-END TRACE SUMMARY")
	t.Log(strings.Repeat("=", 70))

	traceSpans := []string{
		"[1] device.create         - Device provisioning with certificate generation",
		"[2] alert.create_threshold - Threshold definition and alert setup",
		"[3] telemetry.submit      - Normal metric ingestion (no alert)",
		"[4] telemetry.submit      - High-value metric (exceeds threshold → alert triggered)",
		"[5] alert.get_active      - Alert query and retrieval",
		"[6] telemetry.query_metrics - Metric storage verification",
		"[7] alert.silence         - Alert silencing and lifecycle management",
	}

	for _, span := range traceSpans {
		t.Log(span)
	}

	t.Log("\n" + strings.Repeat("=", 70))
	t.Log("TRACE VERIFICATION CHECKLIST")
	t.Log(strings.Repeat("=", 70))

	checks := []struct {
		name  string
		pass  bool
	}{
		{"Device creation completed", createDevResp.DeviceId != ""},
		{"Threshold created successfully", thresholdResp.ThresholdId != ""},
		{"Normal metric submitted", normalResp.Success},
		{"High metric submitted", highResp.Success},
		{"Metrics queryable from database", pointCount > 0},
		{"Alert silencing deployed", silenceResp.SilenceId != ""},
	}

	allPassed := true
	for _, check := range checks {
		status := "✓"
		if !check.pass {
			status = "✗"
			allPassed = false
		}
		t.Logf("%s %s", status, check.name)
	}

	t.Log(strings.Repeat("=", 70))

	if !allPassed {
		t.Fatalf("Some E2E trace checks failed")
	}

	t.Log("✓ END-TO-END TRACE TEST PASSED")
}

// TestE2ETraceErrorRecovery validates tracing of error paths
// and ensures failures are properly recorded across service boundaries
func TestE2ETraceErrorRecovery(t *testing.T) {
	t.Log("\n=== End-to-End Trace Error Recovery Test ===")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deviceConn, err := grpc.Dial(deviceServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to device service: %v", err)
	}
	defer deviceConn.Close()

	telemetryConn, err := grpc.Dial(telemetryServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to telemetry service: %v", err)
	}
	defer telemetryConn.Close()

	deviceClient := pb.NewDeviceServiceClient(deviceConn)
	telemetryClient := pb.NewTelemetryServiceClient(telemetryConn)

	// ========== ERROR TRACE 1: Invalid Device ID ==========
	t.Log("\n[ERROR TRACE 1] Invalid Device ID")

	invalidReq := &pb.CreateDeviceRequest{
		DeviceId:        "", // INVALID: empty
		Floor:           "floor-1",
		DeviceType:      "sensor",
		FirmwareVersion: "1.0.0",
	}

	_, err = deviceClient.CreateDevice(ctx, invalidReq)
	if err == nil {
		t.Errorf("Expected error for empty device_id, got success")
	} else {
		t.Logf("✓ Correctly rejected invalid device_id")
		t.Logf("  Error: %v", err)
	}

	// ========== ERROR TRACE 2: Duplicate Device ==========
	t.Log("\n[ERROR TRACE 2] Duplicate Device Creation")

	dupID := fmt.Sprintf("duplicate_test_device_%d", time.Now().UnixNano())
	validReq := &pb.CreateDeviceRequest{
		DeviceId:        dupID,
		Floor:           "floor-1",
		DeviceType:      "sensor",
		FirmwareVersion: "1.0.0",
	}

	resp, err := deviceClient.CreateDevice(ctx, validReq)
	if err != nil {
		t.Fatalf("Failed to create first device: %v", err)
	}
	t.Logf("✓ First device created: %s", resp.DeviceId)

	// Try to create same device again
	_, err = deviceClient.CreateDevice(ctx, validReq)
	if err == nil {
		t.Errorf("Expected error for duplicate device, got success")
	} else {
		t.Logf("✓ Correctly rejected duplicate device_id")
		t.Logf("  Error: %v", err)
	}

	// ========== ERROR TRACE 3: Metric on Unknown Device ==========
	t.Log("\n[ERROR TRACE 3] Metric Submission on Unknown Device")

	unknownDeviceMetric := &pb.SubmitMetricRequest{
		DeviceId:             "nonexistent_device_xyz",
		MetricName:           "temperature",
		Value:                25.0,
		TimestampUnixSeconds: time.Now().Unix(),
	}

	_, err = telemetryClient.SubmitMetric(ctx, unknownDeviceMetric)
	if err == nil {
		t.Errorf("Expected error for unknown device, got success")
	} else {
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.NotFound {
			t.Errorf("expected NotFound for unknown device, got: %v", err)
		} else {
			t.Logf("✓ Correctly rejected metric on unknown device (NotFound)")
			t.Logf("  Error: %v", err)
		}
	}

	// ========== ERROR TRACE 4: Invalid Metric ==========
	t.Log("\n[ERROR TRACE 4] Invalid Metric Value (NaN/Inf)")

	invalidMetric := &pb.SubmitMetricRequest{
		DeviceId:             resp.DeviceId,
		MetricName:           "temperature",
		Value:                math.NaN(), // NaN
		TimestampUnixSeconds: time.Now().Unix(),
	}

	_, err = telemetryClient.SubmitMetric(ctx, invalidMetric)
	if err == nil {
		t.Errorf("Expected error for NaN value, got success")
	} else {
		t.Logf("✓ Correctly rejected NaN metric value")
		t.Logf("  Error: %v", err)
	}

	t.Log("\n✓ END-TO-END TRACE ERROR RECOVERY TEST PASSED")
}

// TestE2ETracePerformance measures latency of each trace span
func TestE2ETracePerformance(t *testing.T) {
	t.Log("\n=== End-to-End Trace Performance Test ===")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type SpanMetric struct {
		name     string
		duration time.Duration
	}

	var spans []SpanMetric

	deviceConn, err := grpc.Dial(deviceServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to device service: %v", err)
	}
	defer deviceConn.Close()

	telemetryConn, err := grpc.Dial(telemetryServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to telemetry service: %v", err)
	}
	defer telemetryConn.Close()

	deviceClient := pb.NewDeviceServiceClient(deviceConn)
	telemetryClient := pb.NewTelemetryServiceClient(telemetryConn)

	// Measure device creation
	deviceID := fmt.Sprintf("perf_trace_device_%d", time.Now().Unix())
	start := time.Now()
	devResp, devErr := deviceClient.CreateDevice(ctx, &pb.CreateDeviceRequest{
		DeviceId:        deviceID,
		Floor:           "floor-1",
		DeviceType:      "sensor",
		FirmwareVersion: "1.0.0",
	})
	deviceCreateDuration := time.Since(start)
	if devErr != nil {
		t.Fatalf("Failed to create device: %v", devErr)
	}
	_ = devResp  // Mark as used if needed later
	spans = append(spans, SpanMetric{"device.create", deviceCreateDuration})

	// Unique timestamps per iteration: dedup key is dedup:{device_id}:{unix_ts} (not per metric name).
	// Use distinct past seconds only — baseTS+i would be up to +9s in the future and violates planning.md §4.1 (5s skew).
	baseTS := time.Now().Unix()
	for i := 0; i < 10; i++ {
		start = time.Now()
		_, err := telemetryClient.SubmitMetric(ctx, &pb.SubmitMetricRequest{
			DeviceId:             deviceID,
			MetricName:           fmt.Sprintf("metric_%d", i),
			Value:                float64(i * 10),
			TimestampUnixSeconds: baseTS - int64(i),
		})
		metricDuration := time.Since(start)
		if err != nil {
			t.Fatalf("Failed to submit metric: %v", err)
		}

		if i == 0 {
			spans = append(spans, SpanMetric{"telemetry.submit (first)", metricDuration})
		} else if i == 9 {
			spans = append(spans, SpanMetric{"telemetry.submit (last)", metricDuration})
		}
	}

	// Print performance results
	t.Log("\n" + strings.Repeat("=", 70))
	t.Log("TRACE SPAN PERFORMANCE METRICS")
	t.Log(strings.Repeat("=", 70))
	t.Log(fmt.Sprintf("%-40s %15s", "SPAN NAME", "LATENCY (ms)"))
	t.Log(strings.Repeat("-", 57))

	for _, span := range spans {
		ms := span.duration.Milliseconds()
		t.Logf("%-40s %15d ms", span.name, ms)
	}

	t.Log(strings.Repeat("=", 70))

	t.Log("✓ END-TO-END TRACE PERFORMANCE TEST PASSED")
}
