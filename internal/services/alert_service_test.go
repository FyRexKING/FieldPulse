package services

import (
	"context"
	"testing"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/db"
)

// mockAlertDB implements db.AlertDB for testing
type mockAlertDB struct {
	thresholds []db.Threshold
	alerts     []db.Alert
	silences   []db.SilenceRule
}

func (m *mockAlertDB) CreateThreshold(ctx context.Context, t db.Threshold) (string, error) {
	t.ThresholdID = "th_test_1"
	m.thresholds = append(m.thresholds, t)
	return t.ThresholdID, nil
}

func (m *mockAlertDB) GetThresholds(ctx context.Context, deviceID, metricName string, onlyEnabled bool) ([]db.Threshold, error) {
	var results []db.Threshold
	for _, t := range m.thresholds {
		if t.DeviceID != deviceID {
			continue
		}
		if metricName != "" && t.MetricName != metricName {
			continue
		}
		if onlyEnabled && !t.Enabled {
			continue
		}
		results = append(results, t)
	}
	return results, nil
}

func (m *mockAlertDB) UpdateThreshold(ctx context.Context, thresholdID string, updates map[string]interface{}) error {
	for i, t := range m.thresholds {
		if t.ThresholdID == thresholdID {
			if v, ok := updates["enabled"]; ok {
				m.thresholds[i].Enabled = v.(bool)
			}
			if v, ok := updates["severity"]; ok {
				m.thresholds[i].Severity = v.(string)
			}
			return nil
		}
	}
	return nil
}

func (m *mockAlertDB) DeleteThreshold(ctx context.Context, thresholdID string) error {
	for i, t := range m.thresholds {
		if t.ThresholdID == thresholdID {
			m.thresholds = append(m.thresholds[:i], m.thresholds[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockAlertDB) InsertAlert(ctx context.Context, a db.Alert) error {
	m.alerts = append(m.alerts, a)
	return nil
}

func (m *mockAlertDB) GetActiveAlerts(ctx context.Context, deviceID, severity string, limit, offset int) ([]db.Alert, int64, error) {
	var results []db.Alert
	for _, a := range m.alerts {
		if a.ResolvedAt != nil {
			continue
		}
		if deviceID != "" && a.DeviceID != deviceID {
			continue
		}
		// Only filter by severity if a specific severity is requested (not the default unspecified)
		if severity != "" && severity != "ALERT_SEVERITY_UNSPECIFIED" && a.Severity != severity {
			continue
		}
		results = append(results, a)
	}
	return results, int64(len(results)), nil
}

func (m *mockAlertDB) QueryAlertHistory(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time, limit int) ([]db.Alert, error) {
	var results []db.Alert
	for _, a := range m.alerts {
		if a.DeviceID != deviceID {
			continue
		}
		if metricName != "" && a.MetricName != metricName {
			continue
		}
		if a.TriggeredAt.Before(startTime) || a.TriggeredAt.After(endTime) {
			continue
		}
		results = append(results, a)
	}
	return results, nil
}

func (m *mockAlertDB) ResolveAlert(ctx context.Context, alertID string) error {
	for i, a := range m.alerts {
		if a.AlertID == alertID {
			now := time.Now()
			m.alerts[i].ResolvedAt = &now
			return nil
		}
	}
	return nil
}

func (m *mockAlertDB) SilenceDevice(ctx context.Context, deviceID, metricName string, durationSeconds int, reason string) (string, error) {
	silenceID := "sil_test_1"

	var metricNamePtr *string
	if metricName != "" {
		metricNamePtr = &metricName
	}

	m.silences = append(m.silences, db.SilenceRule{
		SilenceID:     silenceID,
		DeviceID:      deviceID,
		MetricName:    metricNamePtr,
		SilencedUntil: time.Now().Add(time.Duration(durationSeconds) * time.Second),
		Reason:        reason,
	})
	return silenceID, nil
}

func (m *mockAlertDB) GetActiveSilences(ctx context.Context, deviceID string) ([]db.SilenceRule, error) {
	var results []db.SilenceRule
	for _, s := range m.silences {
		if s.DeviceID == deviceID && s.SilencedUntil.After(time.Now()) {
			results = append(results, s)
		}
	}
	return results, nil
}

func (m *mockAlertDB) ClearSilences(ctx context.Context, deviceID string) error {
	var newSilences []db.SilenceRule
	for _, s := range m.silences {
		if s.DeviceID != deviceID {
			newSilences = append(newSilences, s)
		}
	}
	m.silences = newSilences
	return nil
}

func (m *mockAlertDB) GetAlertStats(ctx context.Context, deviceID string, startTime, endTime time.Time) (map[string]interface{}, error) {
	stats := make(map[string]interface{})
	total := 0
	active := 0
	bySeverity := make(map[string]int64)

	for _, a := range m.alerts {
		if a.DeviceID == deviceID && a.TriggeredAt.After(startTime) && a.TriggeredAt.Before(endTime) {
			total++
			if a.ResolvedAt == nil {
				active++
			}
			bySeverity[a.Severity]++
		}
	}

	stats["total_alerts"] = int64(total)
	stats["active_alerts"] = int64(active)
	stats["by_severity"] = bySeverity
	return stats, nil
}

// Tests

func TestCreateThreshold(t *testing.T) {
	svc := NewAlertService(&mockAlertDB{})
	ctx := context.Background()

	req := &pb.CreateThresholdRequest{
		DeviceId:   "dev_123",
		MetricName: "temperature",
		Type:       pb.ThresholdType_HIGH,
		UpperBound: 100.0,
		Severity:   pb.AlertSeverity_CRITICAL,
		Enabled:    true,
	}

	resp, err := svc.CreateThreshold(ctx, req)
	if err != nil {
		t.Fatalf("CreateThreshold failed: %v", err)
	}

	if resp.ThresholdId == "" {
		t.Error("Expected threshold_id to be set")
	}
}

func TestCreateThreshold_InvalidInput(t *testing.T) {
	svc := NewAlertService(&mockAlertDB{})
	ctx := context.Background()

	tests := []struct {
		name string
		req  *pb.CreateThresholdRequest
	}{
		{
			name: "missing device_id",
			req: &pb.CreateThresholdRequest{
				MetricName: "temperature",
				Type:       pb.ThresholdType_HIGH,
				UpperBound: 100.0,
			},
		},
		{
			name: "missing metric_name",
			req: &pb.CreateThresholdRequest{
				DeviceId: "dev_123",
				Type:     pb.ThresholdType_HIGH,
			},
		},
		{
			name: "invalid type",
			req: &pb.CreateThresholdRequest{
				DeviceId:   "dev_123",
				MetricName: "temperature",
				Type:       pb.ThresholdType_THRESHOLD_TYPE_UNSPECIFIED,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.CreateThreshold(ctx, test.req)
			if err == nil {
				t.Error("Expected error for invalid input")
			}
		})
	}
}

func TestGetThresholds(t *testing.T) {
	mock := &mockAlertDB{}
	svc := NewAlertService(mock)
	ctx := context.Background()

	// Create a threshold first
	mock.thresholds = append(mock.thresholds, db.Threshold{
		ThresholdID: "th_1",
		DeviceID:    "dev_123",
		MetricName:  "temperature",
		Type:        "HIGH",
		UpperBound:  100.0,
		Severity:    "CRITICAL",
		Enabled:     true,
	})

	req := &pb.GetThresholdsRequest{DeviceId: "dev_123"}
	resp, err := svc.GetThresholds(ctx, req)
	if err != nil {
		t.Fatalf("GetThresholds failed: %v", err)
	}

	if len(resp.Thresholds) != 1 {
		t.Errorf("Expected 1 threshold, got %d", len(resp.Thresholds))
	}
}

func TestGetActiveAlerts(t *testing.T) {
	mock := &mockAlertDB{}
	svc := NewAlertService(mock)
	ctx := context.Background()

	// Insert an alert
	mock.alerts = append(mock.alerts, db.Alert{
		AlertID:     "alr_1",
		DeviceID:    "dev_123",
		MetricName:  "temperature",
		MetricValue: 105.0,
		Severity:    "CRITICAL",
		TriggeredAt: time.Now(),
	})

	req := &pb.GetActiveAlertsRequest{DeviceId: "dev_123", Limit: 100}
	resp, err := svc.GetActiveAlerts(ctx, req)
	if err != nil {
		t.Fatalf("GetActiveAlerts failed: %v", err)
	}

	if len(resp.Alerts) != 1 {
		t.Errorf("Expected 1 alert, got %d", len(resp.Alerts))
	}
}

func TestGetActiveAlerts_WithMinSeverityFilter(t *testing.T) {
	mock := &mockAlertDB{}
	svc := NewAlertService(mock)
	ctx := context.Background()

	mock.alerts = append(mock.alerts,
		db.Alert{AlertID: "a1", DeviceID: "dev_123", MetricName: "temperature", Severity: "CRITICAL", TriggeredAt: time.Now()},
		db.Alert{AlertID: "a2", DeviceID: "dev_123", MetricName: "humidity", Severity: "WARNING", TriggeredAt: time.Now()},
	)

	req := &pb.GetActiveAlertsRequest{
		DeviceId:    "dev_123",
		Limit:       100,
		MinSeverity: pb.AlertSeverity_CRITICAL,
	}
	resp, err := svc.GetActiveAlerts(ctx, req)
	if err != nil {
		t.Fatalf("GetActiveAlerts failed: %v", err)
	}
	if len(resp.Alerts) != 1 || resp.Alerts[0].AlertId != "a1" {
		t.Errorf("expected 1 CRITICAL alert, got %+v", resp.Alerts)
	}
}

func TestSilenceAlert(t *testing.T) {
	svc := NewAlertService(&mockAlertDB{})
	ctx := context.Background()

	req := &pb.SilenceAlertRequest{
		DeviceId:        "dev_123",
		MetricName:      "temperature",
		DurationSeconds: 3600,
		Reason:          "maintenance",
	}

	resp, err := svc.SilenceAlert(ctx, req)
	if err != nil {
		t.Fatalf("SilenceAlert failed: %v", err)
	}

	if resp.SilenceId == "" {
		t.Error("Expected silence_id to be set")
	}
}

func TestAlertEvaluator(t *testing.T) {
	t.Skip("Skipping alert evaluator test - requires mock database interface")
	// TODO: Implement with proper AlertStore interface
}

func TestAlertEvaluator_SilencedAlert(t *testing.T) {
	t.Skip("Skipping alert evaluator test - requires mock database interface")
	// TODO: Implement with proper AlertStore interface
}
