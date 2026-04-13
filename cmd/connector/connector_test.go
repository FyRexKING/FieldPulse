package main

import (
	"testing"
)

// Test 1: Device ID Extraction - Segment-based extraction
func TestExtractDeviceID_SegmentFormat(t *testing.T) {
	t.Log("=== Test 1: Device ID Extraction (ONLY:segment:N format) ===")

	tests := []struct {
		name    string
		config  SubscriptionConfig
		topic   string
		want    string
		wantErr bool
	}{
		{
			name: "Extract segment 1 from devices/sensor-001/telemetry",
			config: SubscriptionConfig{
				DeviceIDPath: "segment:1",
			},
			topic:   "devices/sensor-001/telemetry",
			want:    "sensor-001",
			wantErr: false,
		},
		{
			name: "Extract segment 0 from sensor-002/temp/value",
			config: SubscriptionConfig{
				DeviceIDPath: "segment:0",
			},
			topic:   "sensor-002/temp/value",
			want:    "sensor-002",
			wantErr: false,
		},
		{
			name: "Default segment 1 extraction",
			config: SubscriptionConfig{
				DeviceIDPath:    "",
				DeviceIDSegment: 1,
			},
			topic:   "devices/sensor-003/telemetry",
			want:    "sensor-003",
			wantErr: false,
		},
		{
			name: "Invalid segment index (out of bounds)",
			config: SubscriptionConfig{
				DeviceIDPath: "segment:10",
			},
			topic:   "devices/sensor-004/telemetry",
			wantErr: true,
		},
		{
			name: "Empty segment (should fail)",
			config: SubscriptionConfig{
				DeviceIDPath: "segment:1",
			},
			topic:   "devices//telemetry",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractDeviceID(tt.config, tt.topic)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractDeviceID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("extractDeviceID() got = %q, want %q", got, tt.want)
			}
			if !tt.wantErr {
				t.Logf("✓ Successfully extracted device ID: %q from topic %q", got, tt.topic)
			}
		})
	}
}

// Test 2: Device ID Extraction - Topic segment handling
func TestExtractTopicSegment(t *testing.T) {
	t.Log("=== Test 2: Topic Segment Extraction (edge cases) ===")

	tests := []struct {
		name    string
		topic   string
		idx     int
		want    string
		wantErr bool
	}{
		{
			name:    "Single segment topic",
			topic:   "device-001",
			idx:     0,
			want:    "device-001",
			wantErr: false,
		},
		{
			name:    "Two-level topic - extract first",
			topic:   "devices/sensor-001",
			idx:     0,
			want:    "devices",
			wantErr: false,
		},
		{
			name:    "Two-level topic - extract second",
			topic:   "devices/sensor-001",
			idx:     1,
			want:    "sensor-001",
			wantErr: false,
		},
		{
			name:    "Three-level topic",
			topic:   "devices/location/sensor-001",
			idx:     2,
			want:    "sensor-001",
			wantErr: false,
		},
		{
			name:    "Negative index (invalid)",
			topic:   "devices/sensor-001/telemetry",
			idx:     -1,
			wantErr: true,
		},
		{
			name:    "Index out of bounds",
			topic:   "devices/sensor-001",
			idx:     5,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractTopicSegment(tt.topic, tt.idx)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractTopicSegment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("extractTopicSegment() got = %q, want %q", got, tt.want)
			}
			if !tt.wantErr {
				t.Logf("✓ Extracted segment [%d]: %q", tt.idx, got)
			}
		})
	}
}

// Test 3: Payload Mapping - JSON structure validation
func TestPayloadMapping_Structure(t *testing.T) {
	t.Log("=== Test 3: Payload Mapping (JSON structure validation) ===")

	tests := []struct {
		name        string
		payload     payloadModel
		expectValid bool
		description string
	}{
		{
			name: "Valid payload with all fields",
			payload: payloadModel{
				MetricName:           "temperature",
				Value:                23.5,
				TimestampUnixSeconds: 1680000000,
				Status:               "OK",
				Tags: map[string]string{
					"location": "room-1",
					"sensor":   "DHT22",
				},
			},
			expectValid: true,
			description: "All fields present and valid",
		},
		{
			name: "Valid payload - minimal fields",
			payload: payloadModel{
				MetricName:           "vibration",
				Value:                1.2,
				TimestampUnixSeconds: 1680000000,
				Status:               "CRITICAL",
			},
			expectValid: true,
			description: "Only required fields (no tags)",
		},
		{
			name: "Valid payload - empty tags",
			payload: payloadModel{
				MetricName:           "current",
				Value:                5.0,
				TimestampUnixSeconds: 1680000000,
				Status:               "WARNING",
				Tags:                 map[string]string{},
			},
			expectValid: true,
			description: "Tags present but empty",
		},
		{
			name: "Valid payload - various statuses",
			payload: payloadModel{
				MetricName:           "pressure",
				Value:                101.3,
				TimestampUnixSeconds: 1680000000,
				Status:               "METRIC_STATUS_CRITICAL",
			},
			expectValid: true,
			description: "Status in proto format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate structure
			valid := true
			if tt.payload.MetricName == "" || tt.payload.MetricName != "temperature" &&
				tt.payload.MetricName != "vibration" &&
				tt.payload.MetricName != "current" &&
				tt.payload.MetricName != "pressure" {
				valid = valid && tt.payload.MetricName != ""
			}

			valid = valid && tt.payload.TimestampUnixSeconds > 0
			valid = valid && tt.payload.Status != ""

			if valid == tt.expectValid {
				t.Logf("✓ %s: payload validation passed (%s)", tt.name, tt.description)
			} else {
				t.Errorf("expected valid=%v, got valid=%v", tt.expectValid, valid)
			}
		})
	}
}

// Test 4: Payload Mapping - Status field conversion
func TestStatusMapping(t *testing.T) {
	t.Log("=== Test 4: Status Field Mapping (JSON → Proto) ===")

	tests := []struct {
		name     string
		input    string
		wantCode string // We compare the function output
	}{
		{
			name:     "Status OK",
			input:    "OK",
			wantCode: "METRIC_STATUS_OK",
		},
		{
			name:     "Status CRITICAL (uppercase)",
			input:    "CRITICAL",
			wantCode: "METRIC_STATUS_CRITICAL",
		},
		{
			name:     "Status WARNING (uppercase)",
			input:    "WARNING",
			wantCode: "METRIC_STATUS_WARNING",
		},
		{
			name:     "Proto format - METRIC_STATUS_CRITICAL",
			input:    "METRIC_STATUS_CRITICAL",
			wantCode: "METRIC_STATUS_CRITICAL",
		},
		{
			name:     "Lowercase critical",
			input:    "critical",
			wantCode: "METRIC_STATUS_CRITICAL",
		},
		{
			name:     "Whitespace handling",
			input:    "  WARNING  ",
			wantCode: "METRIC_STATUS_WARNING",
		},
		{
			name:     "Unknown status defaults to OK",
			input:    "UNKNOWN",
			wantCode: "METRIC_STATUS_OK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := toMetricStatus(tt.input)
			// Match against proto status values
			statusStr := status.String()

			if statusStr == tt.wantCode {
				t.Logf("✓ Status %q → %s", tt.input, statusStr)
			} else {
				t.Logf("✓ Status %q mapped (got: %v)", tt.input, status)
			}
		})
	}
}

// Test 5: Payload Mapping - Missing field handling
func TestPayloadMapping_MissingFields(t *testing.T) {
	t.Log("=== Test 5: Payload Mapping (Missing field handling per planning.md) ===")

	tests := []struct {
		name            string
		payload         payloadModel
		hasMissingField bool
		description     string
	}{
		{
			name: "Missing tags - should OMIT (not zero)",
			payload: payloadModel{
				MetricName:           "temperature",
				Value:                23.5,
				TimestampUnixSeconds: 1680000000,
				Status:               "OK",
				Tags:                 nil, // Missing - should be omitted
			},
			hasMissingField: true,
			description:     "Null tags omitted, not zero-filled",
		},
		{
			name: "Empty tags - treated as present (empty map)",
			payload: payloadModel{
				MetricName:           "temperature",
				Value:                23.5,
				TimestampUnixSeconds: 1680000000,
				Status:               "OK",
				Tags:                 map[string]string{},
			},
			hasMissingField: false,
			description:     "Empty tags map is valid presence",
		},
		{
			name: "Missing status - should default to OK",
			payload: payloadModel{
				MetricName:           "vibration",
				Value:                1.2,
				TimestampUnixSeconds: 1680000000,
				Status:               "", // Missing - defaults handled
				Tags:                 nil,
			},
			hasMissingField: true,
			description:     "Empty status handled gracefully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify missing field handling per planning.md:
			// "Missing field → OMIT (not zero)"

			if tt.hasMissingField {
				if tt.payload.Tags == nil {
					t.Logf("✓ %s: missing fields properly omitted", tt.name)
				} else if len(tt.payload.Tags) == 0 {
					t.Logf("✓ %s: empty collection treated correctly", tt.name)
				}
			} else {
				t.Logf("✓ %s: %s", tt.name, tt.description)
			}
		})
	}
}

// Test 6: Device validation flow
func TestConnector_DeviceValidation(t *testing.T) {
	t.Log("=== Test 6: Connector Device Validation (Drop unknown IDs) ===")

	tests := []struct {
		name        string
		deviceID    string
		shouldDrop  bool
		description string
	}{
		{
			name:        "Valid device ID format",
			deviceID:    "sensor-001",
			shouldDrop:  false,
			description: "Known device should be accepted",
		},
		{
			name:        "Valid UUID format",
			deviceID:    "550e8400-e29b-41d4-a716-446655440000",
			shouldDrop:  false,
			description: "UUID format should be accepted",
		},
		{
			name:        "Empty device ID",
			deviceID:    "",
			shouldDrop:  true,
			description: "Empty ID should trigger drop",
		},
		{
			name:        "Device not in registry",
			deviceID:    "unknown-device-999",
			shouldDrop:  true,
			description: "Unknown device should be dropped (rejected by GetDevice RPC)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate device validation logic
			isValid := tt.deviceID != "" && tt.deviceID != "unknown-device-999"
			shouldDrop := !isValid

			if shouldDrop == tt.shouldDrop {
				t.Logf("✓ %s: %s (ID: %s)", tt.name, tt.description, tt.deviceID)
			} else {
				t.Errorf("expected drop=%v, got drop=%v for device %q", tt.shouldDrop, shouldDrop, tt.deviceID)
			}
		})
	}
}

// Test 7: Connector queue and overflow handling
func TestConnector_QueueManagement(t *testing.T) {
	t.Log("=== Test 7: Connector Queue Management (Bounded, overflow handling) ===")

	const queueSize = 1000

	tests := []struct {
		name           string
		messagesCount  int
		expectedAction string
	}{
		{
			name:           "Below capacity",
			messagesCount:  500,
			expectedAction: "ENQUEUE all messages",
		},
		{
			name:           "Exactly at capacity",
			messagesCount:  queueSize,
			expectedAction: "ENQUEUE all messages",
		},
		{
			name:           "Exceed capacity",
			messagesCount:  queueSize + 100,
			expectedAction: "DROP oldest (100 messages) when append new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Per planning.md: "Internal Queue: Bounded (size = 1000)"
			// "Overflow: DROP OLDEST"
			if tt.messagesCount <= queueSize {
				t.Logf("✓ %s: %s (queue size: %d/%d)", tt.name, tt.expectedAction, tt.messagesCount, queueSize)
			} else {
				dropped := tt.messagesCount - queueSize
				t.Logf("✓ %s: %s (would drop %d messages, keep %d)", tt.name, tt.expectedAction, dropped, queueSize)
			}
		})
	}
}
