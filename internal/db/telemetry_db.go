package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TelemetryDB wraps database operations for time-series metrics.
// All operations target TimescaleDB hypertable "metrics".
type TelemetryDB struct {
	pool *pgxpool.Pool
}

// NewTelemetryDB creates a new database wrapper.
func NewTelemetryDB(pool *pgxpool.Pool) *TelemetryDB {
	return &TelemetryDB{pool: pool}
}

// MetricPoint represents a single time-series measurement.
type MetricPoint struct {
	Timestamp  time.Time
	DeviceID   string
	MetricName string
	Value      float64
	Tags       map[string]string
	Status     string // "OK", "WARNING", "CRITICAL"
}

// AggregationResult represents statistics for a time window.
type AggregationResult struct {
	WindowStart time.Time
	WindowEnd   time.Time
	AvgValue    float64
	MaxValue    float64
	MinValue    float64
	Count       int64
	P95         float64
	P99         float64
}

// MetricStats represents summary statistics for a metric.
type MetricStats struct {
	DeviceID     string
	MetricName   string
	FirstSeen    time.Time
	LastSeen     time.Time
	TotalSamples int64
	AvgValue     float64
	MinValue     float64
	MaxValue     float64
	StdDevValue  float64
	LatestValue  float64
	LatestStatus string
}

// QueryFilter represents parameters for metric queries.
type QueryFilter struct {
	DeviceID       string
	MetricName     string
	StartTime      time.Time
	EndTime        time.Time
	Limit          int32
	Offset         int32
	OrderAscending bool
}

// AggregationQuery represents parameters for aggregated queries.
type AggregationQuery struct {
	DeviceID           string
	MetricName         string
	StartTime          time.Time
	EndTime            time.Time
	WindowSize         string // "1m", "5m", "1h", "1d"
	AggregationTypes   []string
	OnlyCached         bool // If true, use materialized views only
}

// ==================== Insert Operations ====================

// InsertMetric stores a single metric point in TimescaleDB.
func (db *TelemetryDB) InsertMetric(ctx context.Context, point MetricPoint) error {
	const query = `
		INSERT INTO metrics (time, device_id, metric_name, value, tags, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING
	`

	// Convert tag map to JSON string
	tagsJSON := mapToJSON(point.Tags)

	_, err := db.pool.Exec(ctx, query,
		point.Timestamp,
		point.DeviceID,
		point.MetricName,
		point.Value,
		tagsJSON,
		point.Status,
	)
	if err != nil {
		return fmt.Errorf("failed to insert metric: %w", err)
	}
	return nil
}

// InsertMetricBatch stores multiple metrics atomically.
// Returns error if any metric fails validation or insertion.
func (db *TelemetryDB) InsertMetricBatch(ctx context.Context, points []MetricPoint) error {
	if len(points) == 0 {
		return fmt.Errorf("batch cannot be empty")
	}

	// Use COPY for efficient bulk insertion
	const copySQL = `COPY metrics (time, device_id, metric_name, value, tags, status) FROM STDIN`

	rows := make([][]interface{}, len(points))
	for i, point := range points {
		rows[i] = []interface{}{
			point.Timestamp,
			point.DeviceID,
			point.MetricName,
			point.Value,
			mapToJSON(point.Tags),
			point.Status,
		}
	}

	// Execute copy operation
	result, err := db.pool.CopyFrom(
		ctx,
		pgx.Identifier{"metrics"},
		[]string{"time", "device_id", "metric_name", "value", "tags", "status"},
		pgx.CopyFromRows(rows),
	)

	if err != nil {
		return fmt.Errorf("batch insert failed: %w", err)
	}

	if result != int64(len(points)) {
		return fmt.Errorf("batch insert incomplete: inserted %d of %d rows", result, len(points))
	}

	return nil
}

// ==================== Query Operations ====================

// QueryMetrics retrieves raw metric points with optional filtering.
func (db *TelemetryDB) QueryMetrics(ctx context.Context, filter QueryFilter) ([]MetricPoint, error) {
	// Validate filter
	if err := validateQueryFilter(filter); err != nil {
		return nil, err
	}

	// Build query with proper ordering
	orderDir := "DESC"
	if filter.OrderAscending {
		orderDir = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT time, device_id, metric_name, value, tags, status
		FROM metrics
		WHERE device_id = $1
		  AND metric_name = $2
		  AND time >= $3
		  AND time <= $4
		ORDER BY time %s
		LIMIT $5 OFFSET $6
	`, orderDir)

	rows, err := db.pool.Query(ctx, query,
		filter.DeviceID,
		filter.MetricName,
		filter.StartTime,
		filter.EndTime,
		filter.Limit,
		filter.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	points := make([]MetricPoint, 0, filter.Limit)
	for rows.Next() {
		point, err := scanMetricPoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		points = append(points, point)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iteration failed: %w", err)
	}

	return points, nil
}

// QueryMetricsCount returns the total count of metrics matching filter.
func (db *TelemetryDB) QueryMetricsCount(ctx context.Context, filter QueryFilter) (int64, error) {
	const query = `
		SELECT COUNT(*)
		FROM metrics
		WHERE device_id = $1
		  AND metric_name = $2
		  AND time >= $3
		  AND time <= $4
	`

	var count int64
	err := db.pool.QueryRow(ctx, query,
		filter.DeviceID,
		filter.MetricName,
		filter.StartTime,
		filter.EndTime,
	).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("count query failed: %w", err)
	}

	return count, nil
}

// QueryAggregated retrieves pre-calculated aggregations using materialized views.
func (db *TelemetryDB) QueryAggregated(ctx context.Context, agg AggregationQuery) ([]AggregationResult, error) {
	// Determine view name based on window size
	viewName, err := getViewName(agg.WindowSize)
	if err != nil {
		return nil, err
	}

	// Build aggregations select clause
	aggregations := buildAggregationSelect(agg.AggregationTypes)

	query := fmt.Sprintf(`
		SELECT 
			bucket,
			bucket + INTERVAL '%s' AS window_end,
			%s,
			COUNT(*) as count
		FROM %s
		WHERE device_id = $1
		  AND metric_name = $2
		  AND bucket >= $3
		  AND bucket < $4
		ORDER BY bucket ASC
	`, agg.WindowSize, aggregations, viewName)

	rows, err := db.pool.Query(ctx, query,
		agg.DeviceID,
		agg.MetricName,
		agg.StartTime,
		agg.EndTime,
	)
	if err != nil {
		return nil, fmt.Errorf("aggregation query failed: %w", err)
	}
	defer rows.Close()

	results := make([]AggregationResult, 0, 100)
	for rows.Next() {
		result, err := scanAggregationResult(rows)
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		results = append(results, result)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iteration failed: %w", err)
	}

	return results, nil
}

// ==================== Statistics Operations ====================

// GetMetricStats retrieves comprehensive statistics for a metric.
func (db *TelemetryDB) GetMetricStats(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time) (*MetricStats, error) {
	query := `
		SELECT 
			device_id,
			metric_name,
			MIN(time) as first_seen,
			MAX(time) as last_seen,
			COUNT(*) as total_samples,
			AVG(value) as avg_value,
			MIN(value) as min_value,
			MAX(value) as max_value,
			STDDEV(value) as stddev_value,
			FIRST(value, time) as latest_value,
			FIRST(status, time) as latest_status
		FROM metrics
		WHERE device_id = $1
		  AND metric_name = $2
		  AND time >= $3
		  AND time < $4
		GROUP BY device_id, metric_name
	`

	stats := &MetricStats{}
	var stdDev *float64

	err := db.pool.QueryRow(ctx, query, deviceID, metricName, startTime, endTime).Scan(
		&stats.DeviceID,
		&stats.MetricName,
		&stats.FirstSeen,
		&stats.LastSeen,
		&stats.TotalSamples,
		&stats.AvgValue,
		&stats.MinValue,
		&stats.MaxValue,
		&stdDev,
		&stats.LatestValue,
		&stats.LatestStatus,
	)

	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("no metrics found for device %s metric %s", deviceID, metricName)
	}
	if err != nil {
		return nil, fmt.Errorf("stats query failed: %w", err)
	}

	if stdDev != nil {
		stats.StdDevValue = *stdDev
	}

	return stats, nil
}

// GetDeviceMetricNames returns all metric names recorded for a device.
func (db *TelemetryDB) GetDeviceMetricNames(ctx context.Context, deviceID string) ([]string, error) {
	query := `
		SELECT DISTINCT metric_name
		FROM metrics
		WHERE device_id = $1
		ORDER BY metric_name ASC
	`

	rows, err := db.pool.Query(ctx, query, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	names := make([]string, 0, 20)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		names = append(names, name)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iteration failed: %w", err)
	}

	return names, nil
}

// ==================== Maintenance Operations ====================

// DeleteOldMetrics removes metrics older than the specified time.
// Useful for lifecycle management and data cleanup.
func (db *TelemetryDB) DeleteOldMetrics(ctx context.Context, olderThan time.Time) (int64, error) {
	result, err := db.pool.Exec(ctx, `DELETE FROM metrics WHERE time < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("delete failed: %w", err)
	}

	return result.RowsAffected(), nil
}

// GetTableStats returns storage statistics for metrics table.
func (db *TelemetryDB) GetTableStats(ctx context.Context) (map[string]interface{}, error) {
	query := `
		SELECT 
			pg_size_pretty(pg_total_relation_size('metrics')) as total_size,
			(SELECT count(*) FROM metrics) as row_count,
			(SELECT EXTRACT(EPOCH FROM (MAX(time) - MIN(time))) 
			 FROM metrics) as time_span_seconds
	`

	stats := make(map[string]interface{})
	var totalSize, rowCount, timeSpan interface{}

	err := db.pool.QueryRow(ctx, query).Scan(&totalSize, &rowCount, &timeSpan)
	if err != nil {
		return nil, fmt.Errorf("stats query failed: %w", err)
	}

	stats["total_size"] = totalSize
	stats["row_count"] = rowCount
	stats["time_span_seconds"] = timeSpan

	return stats, nil
}

// ==================== Helper Functions (Private) ====================

// scanMetricPoint reads a metric point from a single database row.
func scanMetricPoint(row pgx.Row) (MetricPoint, error) {
	point := MetricPoint{}
	var tagsJSON string

	err := row.Scan(
		&point.Timestamp,
		&point.DeviceID,
		&point.MetricName,
		&point.Value,
		&tagsJSON,
		&point.Status,
	)
	if err != nil {
		return point, err
	}

	point.Tags = jsonToMap(tagsJSON)
	return point, nil
}

// scanAggregationResult reads aggregation metrics from a row.
func scanAggregationResult(row pgx.Row) (AggregationResult, error) {
	result := AggregationResult{}
	var p95, p99 *float64

	err := row.Scan(
		&result.WindowStart,
		&result.WindowEnd,
		&result.AvgValue,
		&result.MaxValue,
		&result.MinValue,
		&p95,
		&p99,
		&result.Count,
	)
	if err != nil {
		return result, err
	}

	if p95 != nil {
		result.P95 = *p95
	}
	if p99 != nil {
		result.P99 = *p99
	}

	return result, nil
}

// validateQueryFilter checks filter parameters for validity.
func validateQueryFilter(filter QueryFilter) error {
	if filter.DeviceID == "" {
		return fmt.Errorf("device_id required")
	}
	if filter.MetricName == "" {
		return fmt.Errorf("metric_name required")
	}
	if filter.EndTime.Before(filter.StartTime) {
		return fmt.Errorf("end_time must be after start_time")
	}
	if filter.Limit <= 0 || filter.Limit > 10000 {
		return fmt.Errorf("limit must be between 1 and 10000")
	}
	if filter.Offset < 0 {
		return fmt.Errorf("offset cannot be negative")
	}
	return nil
}

// getViewName returns the TimescaleDB materialized view name for a window size.
func getViewName(windowSize string) (string, error) {
	views := map[string]string{
		"1m":  "metrics_1m",
		"5m":  "metrics_5m",
		"1h":  "metrics_1h",
		"1d":  "metrics_1d",
	}

	view, ok := views[windowSize]
	if !ok {
		return "", fmt.Errorf("unsupported window size: %s", windowSize)
	}

	return view, nil
}

// buildAggregationSelect builds the SELECT clause for requested aggregations.
func buildAggregationSelect(aggTypes []string) string {
	// Default to all aggregations if none specified
	if len(aggTypes) == 0 {
		return `
			COALESCE(AVG(avg_value), 0) as avg_value,
			COALESCE(MAX(max_value), 0) as max_value,
			COALESCE(MIN(min_value), 0) as min_value,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY avg_value), 0) as p95,
			COALESCE(PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY avg_value), 0) as p99
		`
	}

	// Build from requested types
	selects := make([]string, 0, len(aggTypes))
	for _, aggType := range aggTypes {
		switch aggType {
		case "AVG":
			selects = append(selects, "COALESCE(AVG(avg_value), 0) as avg_value")
		case "MAX":
			selects = append(selects, "COALESCE(MAX(max_value), 0) as max_value")
		case "MIN":
			selects = append(selects, "COALESCE(MIN(min_value), 0) as min_value")
		case "P95":
			selects = append(selects, "COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY avg_value), 0) as p95")
		case "P99":
			selects = append(selects, "COALESCE(PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY avg_value), 0) as p99")
		}
	}

	return fmt.Sprintf("%s", fmt.Sprintf("%-v", selects))
}

// mapToJSON converts a map to JSON string for storage.
func mapToJSON(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	// In production, use encoding/json for proper escaping
	// This is a placeholder for demonstration
	return "{}"
}

// jsonToMap converts stored JSON back to a map.
func jsonToMap(jsonStr string) map[string]string {
	// In production, use encoding/json for proper parsing
	// This is a placeholder for demonstration
	return make(map[string]string)
}
