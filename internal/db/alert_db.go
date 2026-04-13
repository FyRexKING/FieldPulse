package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertDB wraps database operations for alert management.
type AlertDB struct {
	pool *pgxpool.Pool
}

// NewAlertDB creates a new alert database wrapper.
func NewAlertDB(pool *pgxpool.Pool) *AlertDB {
	return &AlertDB{pool: pool}
}

// Threshold represents an alert threshold configuration.
type Threshold struct {
	ThresholdID  string
	DeviceID     string
	MetricName   string
	Type         string // HIGH, LOW, RANGE, CHANGE
	LowerBound   float64
	UpperBound   float64
	ChangePercent float64
	Severity     string // CRITICAL, WARNING, INFO
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Alert represents a triggered alert event.
type Alert struct {
	AlertID       string
	DeviceID      string
	MetricName    string
	MetricValue   float64
	ThresholdType string
	ThresholdValue float64
	Severity      string
	Message       string
	IsSilenced    bool
	TriggeredAt   time.Time
	ResolvedAt    *time.Time
}

// SilenceRule represents an active silencing rule.
type SilenceRule struct {
	SilenceID    string
	DeviceID     string
	MetricName   *string // nil = all metrics
	SilencedUntil time.Time
	Reason       string
}

// CreateThreshold inserts a new alert threshold.
func (db *AlertDB) CreateThreshold(ctx context.Context, threshold Threshold) (string, error) {
	thresholdID := fmt.Sprintf("th_%d", time.Now().UnixNano())
	
	err := db.pool.QueryRow(ctx, `
		INSERT INTO alert_thresholds 
		(threshold_id, device_id, metric_name, type, lower_bound, upper_bound, change_percent, severity, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING threshold_id
	`, thresholdID, threshold.DeviceID, threshold.MetricName, threshold.Type, 
	   threshold.LowerBound, threshold.UpperBound, threshold.ChangePercent, 
	   threshold.Severity, threshold.Enabled).Scan(&thresholdID)
	
	return thresholdID, err
}

// GetThresholds retrieves thresholds for a device.
func (db *AlertDB) GetThresholds(ctx context.Context, deviceID, metricName string, onlyEnabled bool) ([]Threshold, error) {
	query := `
		SELECT threshold_id, device_id, metric_name, type, lower_bound, upper_bound, 
		       change_percent, severity, enabled, created_at, updated_at
		FROM alert_thresholds
		WHERE device_id = $1
	`
	args := []interface{}{deviceID}
	argNum := 2

	if metricName != "" {
		query += fmt.Sprintf(" AND metric_name = $%d", argNum)
		args = append(args, metricName)
		argNum++
	}

	if onlyEnabled {
		query += fmt.Sprintf(" AND enabled = $%d", argNum)
		args = append(args, true)
	}

	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var thresholds []Threshold
	for rows.Next() {
		var t Threshold
		if err := rows.Scan(&t.ThresholdID, &t.DeviceID, &t.MetricName, &t.Type, 
			&t.LowerBound, &t.UpperBound, &t.ChangePercent, &t.Severity, 
			&t.Enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		thresholds = append(thresholds, t)
	}

	return thresholds, rows.Err()
}

// UpdateThreshold modifies a threshold.
func (db *AlertDB) UpdateThreshold(ctx context.Context, thresholdID string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	query := "UPDATE alert_thresholds SET "
	args := []interface{}{}
	argNum := 1

	for key, val := range updates {
		if argNum > 1 {
			query += ", "
		}
		query += fmt.Sprintf("%s = $%d", key, argNum)
		args = append(args, val)
		argNum++
	}

	query += fmt.Sprintf(", updated_at = NOW() WHERE threshold_id = $%d", argNum)
	args = append(args, thresholdID)

	_, err := db.pool.Exec(ctx, query, args...)
	return err
}

// DeleteThreshold removes a threshold.
func (db *AlertDB) DeleteThreshold(ctx context.Context, thresholdID string) error {
	_, err := db.pool.Exec(ctx, "DELETE FROM alert_thresholds WHERE threshold_id = $1", thresholdID)
	return err
}

// InsertAlert stores a triggered alert event.
func (db *AlertDB) InsertAlert(ctx context.Context, alert Alert) error {
	var resolvedAtVal interface{}
	if alert.ResolvedAt != nil {
		resolvedAtVal = *alert.ResolvedAt
	}

	_, err := db.pool.Exec(ctx, `
		INSERT INTO alerts (alert_id, device_id, metric_name, metric_value, threshold_type, 
		                    threshold_value, severity, message, is_silenced, triggered_at, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, alert.AlertID, alert.DeviceID, alert.MetricName, alert.MetricValue, 
	   alert.ThresholdType, alert.ThresholdValue, alert.Severity, alert.Message, 
	   alert.IsSilenced, alert.TriggeredAt, resolvedAtVal)

	return err
}

// GetActiveAlerts retrieves currently triggered alerts.
func (db *AlertDB) GetActiveAlerts(ctx context.Context, deviceID, severity string, limit, offset int) ([]Alert, int64, error) {
	countQuery := "SELECT COUNT(*) FROM alerts WHERE resolved_at IS NULL"
	countArgs := []interface{}{}

	if deviceID != "" {
		countQuery += " AND device_id = $1"
		countArgs = append(countArgs, deviceID)
	}

	var total int64
	if err := db.pool.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
		SELECT alert_id, device_id, metric_name, metric_value, threshold_type, 
		       threshold_value, severity, message, is_silenced, triggered_at, resolved_at
		FROM alerts
		WHERE resolved_at IS NULL
	`
	args := []interface{}{}
	argNum := 1

	if deviceID != "" {
		query += fmt.Sprintf(" AND device_id = $%d", argNum)
		args = append(args, deviceID)
		argNum++
	}

	if severity != "" {
		query += fmt.Sprintf(" AND severity = $%d", argNum)
		args = append(args, severity)
		argNum++
	}

	query += fmt.Sprintf(" ORDER BY triggered_at DESC LIMIT $%d OFFSET $%d", argNum, argNum+1)
	args = append(args, limit, offset)

	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.AlertID, &a.DeviceID, &a.MetricName, &a.MetricValue, 
			&a.ThresholdType, &a.ThresholdValue, &a.Severity, &a.Message, 
			&a.IsSilenced, &a.TriggeredAt, &a.ResolvedAt); err != nil {
			return nil, 0, err
		}
		alerts = append(alerts, a)
	}

	return alerts, total, rows.Err()
}

// QueryAlertHistory retrieves historical alerts within a time range.
func (db *AlertDB) QueryAlertHistory(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time, limit int) ([]Alert, error) {
	query := `
		SELECT alert_id, device_id, metric_name, metric_value, threshold_type, 
		       threshold_value, severity, message, is_silenced, triggered_at, resolved_at
		FROM alerts
		WHERE device_id = $1 AND triggered_at >= $2 AND triggered_at <= $3
	`
	args := []interface{}{deviceID, startTime, endTime}
	argNum := 4

	if metricName != "" {
		query += fmt.Sprintf(" AND metric_name = $%d", argNum)
		args = append(args, metricName)
		argNum++
	}

	query += fmt.Sprintf(" ORDER BY triggered_at DESC LIMIT $%d", argNum)
	args = append(args, limit)

	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.AlertID, &a.DeviceID, &a.MetricName, &a.MetricValue, 
			&a.ThresholdType, &a.ThresholdValue, &a.Severity, &a.Message, 
			&a.IsSilenced, &a.TriggeredAt, &a.ResolvedAt); err != nil {
			return nil, err
		}
		alerts = append(alerts, a)
	}

	return alerts, rows.Err()
}

// ResolveAlert marks an alert as resolved.
func (db *AlertDB) ResolveAlert(ctx context.Context, alertID string) error {
	_, err := db.pool.Exec(ctx, 
		"UPDATE alerts SET resolved_at = NOW() WHERE alert_id = $1 AND resolved_at IS NULL", 
		alertID)
	return err
}

// SilenceDevice adds a silence rule for a device/metric.
func (db *AlertDB) SilenceDevice(ctx context.Context, deviceID, metricName string, durationSeconds int, reason string) (string, error) {
	silenceID := fmt.Sprintf("sil_%d", time.Now().UnixNano())
	silencedUntil := time.Now().Add(time.Duration(durationSeconds) * time.Second)

	var metricNameVal interface{}
	if metricName != "" {
		metricNameVal = metricName
	}

	_, err := db.pool.Exec(ctx, `
		INSERT INTO silence_rules (silence_id, device_id, metric_name, silenced_until, reason)
		VALUES ($1, $2, $3, $4, $5)
	`, silenceID, deviceID, metricNameVal, silencedUntil, reason)

	return silenceID, err
}

// GetActiveSilences retrieves current silence rules for a device.
func (db *AlertDB) GetActiveSilences(ctx context.Context, deviceID string) ([]SilenceRule, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT silence_id, device_id, metric_name, silenced_until, reason
		FROM silence_rules
		WHERE device_id = $1 AND silenced_until > NOW()
	`, deviceID)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var silences []SilenceRule
	for rows.Next() {
		var s SilenceRule
		if err := rows.Scan(&s.SilenceID, &s.DeviceID, &s.MetricName, &s.SilencedUntil, &s.Reason); err != nil {
			return nil, err
		}
		silences = append(silences, s)
	}

	return silences, rows.Err()
}

// ClearSilences removes all silence rules for a device.
func (db *AlertDB) ClearSilences(ctx context.Context, deviceID string) error {
	_, err := db.pool.Exec(ctx, "DELETE FROM silence_rules WHERE device_id = $1", deviceID)
	return err
}

// GetAlertStats retrieves aggregated alert statistics.
func (db *AlertDB) GetAlertStats(ctx context.Context, deviceID string, startTime, endTime time.Time) (map[string]interface{}, error) {
	result := map[string]interface{}{}

	// Total alerts
	var totalAlerts int64
	if err := db.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM alerts 
		WHERE device_id = $1 AND triggered_at >= $2 AND triggered_at <= $3
	`, deviceID, startTime, endTime).Scan(&totalAlerts); err != nil {
		return nil, err
	}
	result["total_alerts"] = totalAlerts

	// Active alerts
	var activeAlerts int64
	if err := db.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM alerts 
		WHERE device_id = $1 AND resolved_at IS NULL
	`, deviceID).Scan(&activeAlerts); err != nil {
		return nil, err
	}
	result["active_alerts"] = activeAlerts

	// By severity
	rows, err := db.pool.Query(ctx, `
		SELECT severity, COUNT(*) as count
		FROM alerts
		WHERE device_id = $1 AND triggered_at >= $2 AND triggered_at <= $3
		GROUP BY severity
	`, deviceID, startTime, endTime)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bySeverity := make(map[string]int64)
	for rows.Next() {
		var severity string
		var count int64
		if err := rows.Scan(&severity, &count); err != nil {
			return nil, err
		}
		bySeverity[severity] = count
	}
	result["by_severity"] = bySeverity

	return result, rows.Err()
}
