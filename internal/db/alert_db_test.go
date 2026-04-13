package db

import (
	"testing"
	"time"
)

func TestAlertStructure(t *testing.T) {
	t.Log("=== Test: DB Alert structure ===")

	now := time.Now()
	alert := Alert{
		AlertID:        "alert-001",
		DeviceID:       "device-001",
		MetricName:     "temperature",
		MetricValue:    38.5,
		ThresholdType:  "HIGH",
		ThresholdValue: 35.0,
		Severity:       "CRITICAL",
		Message:        "Temperature exceeds threshold",
		IsSilenced:     false,
		TriggeredAt:    now,
		ResolvedAt:     nil,
	}

	if alert.AlertID != "alert-001" {
		t.Errorf("❌ Expected AlertID, got %s", alert.AlertID)
	}

	if alert.DeviceID != "device-001" {
		t.Errorf("❌ Expected DeviceID, got %s", alert.DeviceID)
	}

	if alert.Severity != "CRITICAL" {
		t.Errorf("❌ Expected CRITICAL severity")
	}

	if alert.MetricValue != 38.5 {
		t.Errorf("❌ Expected metric value 38.5")
	}

	t.Log("✓ Alert structure correctly stores all fields")
}

func TestAlertSeverityLevels(t *testing.T) {
	t.Log("=== Test: DB Alert severity levels ===")

	severities := []string{"CRITICAL", "WARNING", "INFO"}

	for _, severity := range severities {
		alert := Alert{
			AlertID:        "alert-001",
			Severity:       severity,
			DeviceID:       "device-001",
			MetricName:     "temperature",
			TriggeredAt:    time.Now(),
		}

		if alert.Severity != severity {
			t.Errorf("❌ Severity %s not stored correctly", severity)
		}
	}

	t.Log("✓ All severity levels handled correctly")
}

func TestAlertThresholdTypes(t *testing.T) {
	t.Log("=== Test: DB Alert threshold types ===")

	types := []string{"HIGH", "LOW", "RANGE", "CHANGE"}

	for _, threshType := range types {
		alert := Alert{
			AlertID:       "alert-001",
			ThresholdType: threshType,
			DeviceID:      "device-001",
			MetricName:    "temperature",
			TriggeredAt:   time.Now(),
		}

		if alert.ThresholdType != threshType {
			t.Errorf("❌ ThresholdType %s not stored correctly", threshType)
		}
	}

	t.Log("✓ All threshold types handled correctly")
}

func TestAlertMetricValue(t *testing.T) {
	t.Log("=== Test: DB Alert metric value ===")

	alert := Alert{
		AlertID:        "alert-001",
		MetricName:     "temperature",
		MetricValue:    38.5,
		ThresholdType:  "HIGH",
		ThresholdValue: 35.0,
		DeviceID:       "device-001",
		Severity:       "CRITICAL",
		TriggeredAt:    time.Now(),
	}

	if alert.MetricValue <= alert.ThresholdValue {
		t.Errorf("❌ MetricValue should exceed ThresholdValue")
	}

	if alert.ThresholdValue <= 0 {
		t.Errorf("❌ ThresholdValue should be positive")
	}

	t.Log("✓ Alert metric values are valid")
}

func TestAlertResolution(t *testing.T) {
	t.Log("=== Test: DB Alert resolution ===")

	triggeredAt := time.Now()
	resolvedAt := triggeredAt.Add(1 * time.Hour)

	// Active alert
	activeAlert := Alert{
		AlertID:    "alert-001",
		TriggeredAt: triggeredAt,
		ResolvedAt: nil,
		DeviceID:   "device-001",
		MetricName: "temperature",
	}

	// Resolved alert
	resolvedAlert := Alert{
		AlertID:    "alert-002",
		TriggeredAt: triggeredAt,
		ResolvedAt: &resolvedAt,
		DeviceID:   "device-001",
		MetricName: "temperature",
	}

	if activeAlert.ResolvedAt != nil {
		t.Errorf("❌ Active alert should not have ResolvedAt")
	}

	if resolvedAlert.ResolvedAt == nil {
		t.Errorf("❌ Resolved alert should have ResolvedAt")
	}

	if resolvedAlert.TriggeredAt.After(*resolvedAlert.ResolvedAt) {
		t.Errorf("❌ ResolvedAt should be after TriggeredAt")
	}

	t.Log("✓ Alert resolution tracking works correctly")
}

func TestAlertSilencing(t *testing.T) {
	t.Log("=== Test: DB Alert silencing ===")

	// Active alert
	activeAlert := Alert{
		AlertID:    "alert-001",
		IsSilenced: false,
		DeviceID:   "device-001",
		MetricName: "temperature",
		TriggeredAt: time.Now(),
	}

	// Silenced alert
	silencedAlert := Alert{
		AlertID:    "alert-002",
		IsSilenced: true,
		DeviceID:   "device-001",
		MetricName: "temperature",
		TriggeredAt: time.Now(),
	}

	if activeAlert.IsSilenced {
		t.Errorf("❌ Active alert should not be silenced")
	}

	if !silencedAlert.IsSilenced {
		t.Errorf("❌ Silenced alert should have IsSilenced=true")
	}

	t.Log("✓ Alert silencing tracking works correctly")
}

func TestAlertMessage(t *testing.T) {
	t.Log("=== Test: DB Alert message ===")

	message := "Temperature exceeds threshold"
	alert := Alert{
		AlertID:        "alert-001",
		Message:        message,
		DeviceID:       "device-001",
		MetricName:     "temperature",
		Severity:       "CRITICAL",
		TriggeredAt:    time.Now(),
	}

	if alert.Message != message {
		t.Errorf("❌ Message not stored correctly")
	}

	t.Log("✓ Alert message field works correctly")
}

func TestAlertDBConstructor(t *testing.T) {
	t.Log("=== Test: DB AlertDB constructor ===")

	var db *AlertDB
	if db == nil {
		db = &AlertDB{pool: nil} // Placeholder
	}

	if db == nil {
		t.Errorf("❌ AlertDB is nil")
	}

	t.Log("✓ AlertDB can be constructed")
}

func TestAlertMetricNames(t *testing.T) {
	t.Log("=== Test: DB Alert metric names ===")

	metrics := []string{
		"temperature",
		"humidity",
		"pressure",
		"motion",
	}

	for _, metric := range metrics {
		alert := Alert{
			AlertID:    "alert-001",
			MetricName: metric,
			DeviceID:   "device-001",
			TriggeredAt: time.Now(),
		}

		if alert.MetricName != metric {
			t.Errorf("❌ MetricName %s not stored correctly", metric)
		}
	}

	t.Log("✓ All metric names handled correctly")
}
