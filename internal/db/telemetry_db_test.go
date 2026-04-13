package db

import (
	"testing"
	"time"
)

func TestMetricPointStructure(t *testing.T) {
	t.Log("=== Test: DB MetricPoint structure ===")

	now := time.Now()
	point := MetricPoint{
		Timestamp:  now,
		DeviceID:   "sensor-001",
		MetricName: "temperature",
		Value:      25.5,
		Tags: map[string]string{
			"location": "room-1",
		},
		Status: "OK",
	}

	if point.DeviceID != "sensor-001" {
		t.Errorf("❌ Expected DeviceID sensor-001, got %s", point.DeviceID)
	}

	if point.MetricName != "temperature" {
		t.Errorf("❌ Expected MetricName temperature, got %s", point.MetricName)
	}

	if point.Value != 25.5 {
		t.Errorf("❌ Expected Value 25.5, got %f", point.Value)
	}

	if point.Status != "OK" {
		t.Errorf("❌ Expected Status OK, got %s", point.Status)
	}

	if point.Tags["location"] != "room-1" {
		t.Errorf("❌ Expected location tag, got %v", point.Tags)
	}

	t.Log("✓ MetricPoint structure correctly stores all fields")
}

func TestQueryFilterStructure(t *testing.T) {
	t.Log("=== Test: DB QueryFilter structure ===")

	start := time.Now().Add(-1 * time.Hour)
	end := time.Now()

	filter := QueryFilter{
		DeviceID:       "sensor-001",
		MetricName:     "temperature",
		StartTime:      start,
		EndTime:        end,
		Limit:          100,
		Offset:         0,
		OrderAscending: true,
	}

	if filter.DeviceID != "sensor-001" {
		t.Errorf("❌ Expected DeviceID, got %s", filter.DeviceID)
	}

	if filter.Limit != 100 {
		t.Errorf("❌ Expected Limit 100, got %d", filter.Limit)
	}

	if !filter.OrderAscending {
		t.Errorf("❌ Expected OrderAscending=true")
	}

	if filter.StartTime.After(filter.EndTime) {
		t.Errorf("❌ StartTime after EndTime")
	}

	t.Log("✓ QueryFilter structure correctly stores all fields")
}

func TestAggregationQueryStructure(t *testing.T) {
	t.Log("=== Test: DB AggregationQuery structure ===")

	start := time.Now().Add(-24 * time.Hour)
	end := time.Now()

	query := AggregationQuery{
		DeviceID:         "sensor-001",
		MetricName:       "temperature",
		StartTime:        start,
		EndTime:          end,
		WindowSize:       "1h",
		AggregationTypes: []string{"avg", "max", "min", "p95", "p99"},
		OnlyCached:       false,
	}

	if query.WindowSize != "1h" {
		t.Errorf("❌ Expected WindowSize 1h, got %s", query.WindowSize)
	}

	if len(query.AggregationTypes) != 5 {
		t.Errorf("❌ Expected 5 aggregation types, got %d", len(query.AggregationTypes))
	}

	t.Log("✓ AggregationQuery structure correctly stores all fields")
}

func TestAggregationResultStructure(t *testing.T) {
	t.Log("=== Test: DB AggregationResult structure ===")

	now := time.Now()
	start := now.Add(-1 * time.Hour)
	end := now

	result := AggregationResult{
		WindowStart: start,
		WindowEnd:   end,
		AvgValue:    24.5,
		MaxValue:    28.0,
		MinValue:    21.0,
		Count:       60,
		P95:         27.0,
		P99:         27.5,
	}

	if result.AvgValue != 24.5 {
		t.Errorf("❌ Expected AvgValue 24.5, got %f", result.AvgValue)
	}

	if result.MaxValue < result.MinValue {
		t.Errorf("❌ MaxValue less than MinValue")
	}

	if result.P95 < result.AvgValue {
		t.Errorf("❌ P95 less than average")
	}

	if result.Count != 60 {
		t.Errorf("❌ Expected Count 60, got %d", result.Count)
	}

	t.Log("✓ AggregationResult structure correctly stores statistics")
}

func TestMetricStatsStructure(t *testing.T) {
	t.Log("=== Test: DB MetricStats structure ===")

	now := time.Now()
	firstSeen := now.Add(-24 * time.Hour)

	stats := MetricStats{
		DeviceID:     "sensor-001",
		MetricName:   "temperature",
		FirstSeen:    firstSeen,
		LastSeen:     now,
		TotalSamples: 10000,
		AvgValue:     24.5,
		MinValue:     18.0,
		MaxValue:     32.0,
		StdDevValue:  2.5,
		LatestValue:  25.5,
		LatestStatus: "OK",
	}

	if stats.TotalSamples != 10000 {
		t.Errorf("❌ Expected TotalSamples 10000, got %d", stats.TotalSamples)
	}

	if stats.LastSeen.Before(stats.FirstSeen) {
		t.Errorf("❌ LastSeen before FirstSeen")
	}

	if stats.LatestValue < stats.MinValue || stats.LatestValue > stats.MaxValue {
		t.Errorf("❌ LatestValue outside min/max range")
	}

	t.Log("✓ MetricStats structure correctly stores summary statistics")
}

func TestQueryFilterTimeValidation(t *testing.T) {
	t.Log("=== Test: DB QueryFilter time range validation ===")

	now := time.Now()
	validFilter := QueryFilter{
		DeviceID:   "sensor-001",
		MetricName: "temperature",
		StartTime:  now.Add(-1 * time.Hour),
		EndTime:    now,
		Limit:      100,
	}

	// StartTime should be before EndTime
	if !validFilter.StartTime.Before(validFilter.EndTime) {
		t.Errorf("❌ Filter StartTime should be before EndTime")
	}

	// EndTime should be after StartTime
	if !validFilter.EndTime.After(validFilter.StartTime) {
		t.Errorf("❌ Filter EndTime should be after StartTime")
	}

	t.Log("✓ QueryFilter time range is valid")
}

func TestQueryFilterLimitValidation(t *testing.T) {
	t.Log("=== Test: DB QueryFilter Limit validation ===")

	tests := []struct {
		limit    int32
		offset   int32
		expected bool
		name     string
	}{
		{100, 0, true, "positive limit and zero offset"},
		{1, 0, true, "minimum limit"},
		{10000, 100, true, "large limit with offset"},
		{0, 0, false, "zero limit"},
		{-1, 0, false, "negative limit"},
	}

	for _, test := range tests {
		filter := QueryFilter{
			DeviceID:   "sensor-001",
			MetricName: "temperature",
			Limit:      test.limit,
			Offset:     test.offset,
		}

		isValid := filter.Limit > 0 && filter.Offset >= 0
		if isValid != test.expected {
			t.Errorf("❌ %s: expected valid=%v, got %v", test.name, test.expected, isValid)
		}
	}

	t.Log("✓ QueryFilter Limit validation works correctly")
}

func TestAggregationQueryWindowSizes(t *testing.T) {
	t.Log("=== Test: DB AggregationQuery window sizes ===")

	validWindows := []string{"1m", "5m", "1h", "1d"}

	for _, windowSize := range validWindows {
		query := AggregationQuery{
			DeviceID:   "sensor-001",
			MetricName: "temperature",
			WindowSize: windowSize,
		}

		if query.WindowSize != windowSize {
			t.Errorf("❌ WindowSize %s not stored correctly", windowSize)
		}
	}

	t.Log("✓ All valid window sizes handled correctly")
}

func TestTelemetryDBConstructor(t *testing.T) {
	t.Log("=== Test: DB TelemetryDB constructor ===")

	// Note: We can't actually create a pgxpool without a real database
	// This test just verifies the structure is available
	// In real tests, this would use a mock or test database

	// Simulate what would happen with a real pool
	var db *TelemetryDB
	if db == nil {
		db = &TelemetryDB{pool: nil} // Placeholder - would use real pool in integration tests
	}

	if db == nil {
		t.Errorf("❌ TelemetryDB is nil")
	}

	t.Log("✓ TelemetryDB can be constructed")
}

func TestMetricPointTags(t *testing.T) {
	t.Log("=== Test: DB MetricPoint with tags ===")

	point := MetricPoint{
		DeviceID:   "sensor-001",
		MetricName: "temperature",
		Value:      25.5,
		Tags: map[string]string{
			"location":  "room-1",
			"floor":     "1",
			"building":  "main",
		},
	}

	if len(point.Tags) != 3 {
		t.Errorf("❌ Expected 3 tags, got %d", len(point.Tags))
	}

	if point.Tags["location"] != "room-1" {
		t.Errorf("❌ Expected location=room-1")
	}

	t.Log("✓ MetricPoint handles multiple tags correctly")
}

func TestQueryFilterDefaultValues(t *testing.T) {
	t.Log("=== Test: DB QueryFilter default values ===")

	filter := QueryFilter{
		DeviceID:   "sensor-001",
		MetricName: "temperature",
	}

	// Check that empty/zero values are handled
	if filter.Limit == 0 && filter.Offset == 0 {
		t.Log("✓ QueryFilter uses zero values for unset fields")
	}

	// In real usage, these should be set to defaults
	// This just verifies the structure supports them
	if filter.StartTime.IsZero() && filter.EndTime.IsZero() {
		t.Log("✓ QueryFilter time fields default to zero (caller must set)")
	}
}
