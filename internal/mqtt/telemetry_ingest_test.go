package mqtt

import (
	"context"
	"encoding/json"
	"testing"

	pb "fieldpulse.io/api/proto"
)

func TestParsePayloadStrict_ValidPayload(t *testing.T) {
	t.Log("=== Test: MQTT ParsePayload - Valid payload ===")

	payload := map[string]interface{}{
		"metric_name":            "temperature",
		"value":                  25.5,
		"timestamp_unix_seconds": int64(1700000000),
		"status":                 "OK",
		"tags": map[string]string{
			"location": "room-1",
		},
	}

	raw, _ := json.Marshal(payload)
	result, err := parsePayloadStrict(raw)

	if err != nil {
		t.Errorf("❌ parsePayloadStrict failed: %v", err)
	}

	if result.MetricName != "temperature" {
		t.Errorf("❌ Expected metric_name=temperature, got %s", result.MetricName)
	}

	if result.Value != 25.5 {
		t.Errorf("❌ Expected value=25.5, got %f", result.Value)
	}

	t.Log("✓ Valid payload parsed correctly")
}

func TestParsePayloadStrict_MissingMetricName(t *testing.T) {
	t.Log("=== Test: MQTT ParsePayload - Missing metric_name ===")

	payload := map[string]interface{}{
		"value":                  25.5,
		"timestamp_unix_seconds": int64(1700000000),
	}

	raw, _ := json.Marshal(payload)
	_, err := parsePayloadStrict(raw)

	if err == nil {
		t.Errorf("❌ Expected error for missing metric_name, got nil")
	}

	t.Log("✓ Missing metric_name correctly rejected")
}

func TestParsePayloadStrict_InvalidJSON(t *testing.T) {
	t.Log("=== Test: MQTT ParsePayload - Invalid JSON ===")

	invalid := []byte(`{invalid json}`)
	_, err := parsePayloadStrict(invalid)

	if err == nil {
		t.Errorf("❌ Expected error for invalid JSON, got nil")
	}

	t.Log("✓ Invalid JSON correctly rejected")
}

func TestParsePayloadStrict_UnknownFields(t *testing.T) {
	t.Log("=== Test: MQTT ParsePayload - Unknown fields not allowed ===")

	payload := map[string]interface{}{
		"metric_name":            "temperature",
		"value":                  25.5,
		"timestamp_unix_seconds": int64(1700000000),
		"unknown_field":          "should_reject",
	}

	raw, _ := json.Marshal(payload)
	_, err := parsePayloadStrict(raw)

	if err == nil {
		t.Errorf("❌ Expected error for unknown field, got nil")
	}

	t.Log("✓ Unknown fields correctly rejected (strict parsing)")
}

func TestParsePayloadStrict_TagsNilToEmpty(t *testing.T) {
	t.Log("=== Test: MQTT ParsePayload - Tags nil→empty dict ===")

	payload := map[string]interface{}{
		"metric_name":            "temperature",
		"value":                  25.5,
		"timestamp_unix_seconds": int64(1700000000),
	}

	raw, _ := json.Marshal(payload)
	result, err := parsePayloadStrict(raw)

	if err != nil {
		t.Errorf("❌ parsePayloadStrict failed: %v", err)
	}

	if result.Tags == nil {
		t.Errorf("❌ Expected tags to be empty dict, got nil")
	}

	if len(result.Tags) != 0 {
		t.Errorf("❌ Expected empty tags dict, got %d items", len(result.Tags))
	}

	t.Log("✓ Nil tags converted to empty dict")
}

func TestDeviceIDFromTopic_ValidTopic(t *testing.T) {
	t.Log("=== Test: MQTT DeviceID extraction - Valid topic ===")

	deviceID, ok := deviceIDFromTopic("devices/sensor-001/telemetry")

	if !ok {
		t.Errorf("❌ Expected valid topic, got not ok")
	}

	if deviceID != "sensor-001" {
		t.Errorf("❌ Expected sensor-001, got %s", deviceID)
	}

	t.Log("✓ Device ID extracted correctly")
}

func TestDeviceIDFromTopic_InvalidFormat(t *testing.T) {
	t.Log("=== Test: MQTT DeviceID extraction - Invalid format ===")

	tests := []struct {
		topic string
		name  string
	}{
		{"devices/sensor-001", "missing telemetry"},
		{"devices/sensor-001/telemetry/extra", "extra segment"},
		{"sensors/sensor-001/telemetry", "wrong root"},
		{"devices//telemetry", "empty device id"},
		{"devices/sensor-001/data", "wrong suffix"},
	}

	for _, test := range tests {
		_, ok := deviceIDFromTopic(test.topic)
		if ok {
			t.Errorf("❌ Expected invalid for %s, got ok", test.name)
		}
	}

	t.Log("✓ Invalid topics correctly rejected")
}

func TestParseStatus_ValidStatuses(t *testing.T) {
	t.Log("=== Test: MQTT ParseStatus - Valid statuses ===")

	tests := []struct {
		input    string
		expected pb.MetricStatus
	}{
		{"OK", pb.MetricStatus_METRIC_STATUS_OK},
		{"ok", pb.MetricStatus_METRIC_STATUS_OK},
		{"WARNING", pb.MetricStatus_METRIC_STATUS_WARNING},
		{"warning", pb.MetricStatus_METRIC_STATUS_WARNING},
		{"METRIC_STATUS_WARNING", pb.MetricStatus_METRIC_STATUS_WARNING},
		{"CRITICAL", pb.MetricStatus_METRIC_STATUS_CRITICAL},
		{"critical", pb.MetricStatus_METRIC_STATUS_CRITICAL},
		{"METRIC_STATUS_CRITICAL", pb.MetricStatus_METRIC_STATUS_CRITICAL},
		{"UNKNOWN", pb.MetricStatus_METRIC_STATUS_OK},
		{"", pb.MetricStatus_METRIC_STATUS_OK},
	}

	for _, test := range tests {
		result := parseStatus(test.input)
		if result != test.expected {
			t.Errorf("❌ parseStatus(%s): expected %v, got %v", test.input, test.expected, result)
		}
	}

	t.Log("✓ All status values parsed correctly")
}

func TestParseStatus_CaseInsensitive(t *testing.T) {
	t.Log("=== Test: MQTT ParseStatus - Case insensitive ===")

	statuses := []string{"WARNING", "Warning", "warning", " WARNING ", "wArNiNg"}

	for _, status := range statuses {
		result := parseStatus(status)
		if result != pb.MetricStatus_METRIC_STATUS_WARNING {
			t.Errorf("❌ parseStatus(%s) should be WARNING, got %v", status, result)
		}
	}

	t.Log("✓ Status parsing is case-insensitive")
}

func TestNewTelemetrySubscriber_DefaultConfig(t *testing.T) {
	t.Log("=== Test: MQTT NewTelemetrySubscriber - Defaults ===")

	cfg := SubscriberConfig{BrokerURL: "tls://localhost:8883"}
	mockSubmitter := &mockTelemetrySubmitter{}

	sub := NewTelemetrySubscriber(cfg, mockSubmitter)

	if sub.cfg.Topic != "devices/+/telemetry" {
		t.Errorf("❌ Expected default topic, got %s", sub.cfg.Topic)
	}

	if sub.cfg.ClientID != "fieldpulse-telemetry-subscriber" {
		t.Errorf("❌ Expected default ClientID, got %s", sub.cfg.ClientID)
	}

	if sub.cfg.QoS != 1 {
		t.Errorf("❌ Expected default QoS=1, got %d", sub.cfg.QoS)
	}

	t.Log("✓ Default config applied correctly")
}

func TestNewTelemetrySubscriber_QoSValidation(t *testing.T) {
	t.Log("=== Test: MQTT NewTelemetrySubscriber - QoS capping ===")

	cfg := SubscriberConfig{
		BrokerURL: "tls://localhost:8883",
		QoS:       5, // Invalid (>2)
	}
	mockSubmitter := &mockTelemetrySubmitter{}

	sub := NewTelemetrySubscriber(cfg, mockSubmitter)

	if sub.cfg.QoS != 1 {
		t.Errorf("❌ Expected QoS capped to 1, got %d", sub.cfg.QoS)
	}

	t.Log("✓ QoS capped to valid range")
}

// Mock implementation for testing
type mockTelemetrySubmitter struct{}

func (m *mockTelemetrySubmitter) SubmitMetric(_ context.Context, _ *pb.SubmitMetricRequest) (*pb.SubmitMetricResponse, error) {
	return &pb.SubmitMetricResponse{Success: true}, nil
}
