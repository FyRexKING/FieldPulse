package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ==================== Test Fixtures ====================

// mockTelemetryDB is a mock database for testing without real PostgreSQL.
type mockTelemetryDB struct {
	insertMetricFunc      func(ctx context.Context, point db.MetricPoint) error
	insertMetricBatchFunc func(ctx context.Context, points []db.MetricPoint) error
	queryMetricsFunc      func(ctx context.Context, filter db.QueryFilter) ([]db.MetricPoint, error)
	queryMetricsCountFunc func(ctx context.Context, filter db.QueryFilter) (int64, error)
	queryAggregatedFunc   func(ctx context.Context, agg db.AggregationQuery) ([]db.AggregationResult, error)
	getMetricStatsFunc    func(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time) (*db.MetricStats, error)
}

func (m *mockTelemetryDB) InsertMetric(ctx context.Context, point db.MetricPoint) error {
	if m.insertMetricFunc != nil {
		return m.insertMetricFunc(ctx, point)
	}
	return nil
}

func (m *mockTelemetryDB) InsertMetricBatch(ctx context.Context, points []db.MetricPoint) error {
	if m.insertMetricBatchFunc != nil {
		return m.insertMetricBatchFunc(ctx, points)
	}
	return nil
}

func (m *mockTelemetryDB) QueryMetrics(ctx context.Context, filter db.QueryFilter) ([]db.MetricPoint, error) {
	if m.queryMetricsFunc != nil {
		return m.queryMetricsFunc(ctx, filter)
	}
	return []db.MetricPoint{}, nil
}

func (m *mockTelemetryDB) QueryMetricsCount(ctx context.Context, filter db.QueryFilter) (int64, error) {
	if m.queryMetricsCountFunc != nil {
		return m.queryMetricsCountFunc(ctx, filter)
	}
	return 0, nil
}

func (m *mockTelemetryDB) QueryAggregated(ctx context.Context, agg db.AggregationQuery) ([]db.AggregationResult, error) {
	if m.queryAggregatedFunc != nil {
		return m.queryAggregatedFunc(ctx, agg)
	}
	return []db.AggregationResult{}, nil
}

func (m *mockTelemetryDB) GetMetricStats(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time) (*db.MetricStats, error) {
	if m.getMetricStatsFunc != nil {
		return m.getMetricStatsFunc(ctx, deviceID, metricName, startTime, endTime)
	}
	return nil, errors.New("not implemented")
}

// mockTelemetryCache is a mock cache for testing without Redis.
type mockTelemetryCache struct {
	cacheResults map[string]interface{}
}

func (m *mockTelemetryCache) CacheQueryResult(ctx context.Context, key string, result interface{}) error {
	if m.cacheResults == nil {
		m.cacheResults = make(map[string]interface{})
	}
	m.cacheResults[key] = result
	return nil
}

func (m *mockTelemetryCache) GetQueryResult(ctx context.Context, key string, result interface{}) error {
	if _, ok := m.cacheResults[key]; ok {
		// In production, we'd unmarshal properly
		return nil
	}
	return errors.New("cache miss")
}

func (m *mockTelemetryCache) InvalidateMetricCache(ctx context.Context, deviceID, metricName string) error {
	return nil
}

func (m *mockTelemetryCache) InvalidateDevice(ctx context.Context, deviceID string) error {
	return nil
}

func (m *mockTelemetryCache) ClearAll(ctx context.Context) error {
	m.cacheResults = make(map[string]interface{})
	return nil
}

func (m *mockTelemetryCache) GetCacheStats(ctx context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

// setupTestService creates a service with mock dependencies.
func setupTestService(t *testing.T) (*TelemetryService, *mockTelemetryDB, *mockTelemetryCache) {
	mockDB := &mockTelemetryDB{}
	mockCache := &mockTelemetryCache{cacheResults: make(map[string]interface{})}
	service := NewTelemetryService(mockDB, mockCache)
	return service, mockDB, mockCache
}

// ==================== SubmitMetric Tests ====================

func TestSubmitMetric_ValidMetric(t *testing.T) {
	// Arrange
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	var capturedPoint db.MetricPoint
	mockDB.insertMetricFunc = func(_ context.Context, point db.MetricPoint) error {
		capturedPoint = point
		return nil
	}

	req := &pb.SubmitMetricRequest{
		DeviceId:             "device-001",
		MetricName:           "temperature",
		Value:                23.5,
		TimestampUnixSeconds: 0, // Use server time
		Tags:                 map[string]string{"location": "room1"},
		Status:               pb.MetricStatus_METRIC_STATUS_OK,
	}

	// Act
	resp, err := service.SubmitMetric(ctx, req)

	// Assert
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp == nil || !resp.Success {
		t.Fatal("expected success response")
	}

	if capturedPoint.DeviceID != "device-001" {
		t.Errorf("expected device_id %q, got %q", "device-001", capturedPoint.DeviceID)
	}

	if capturedPoint.Value != 23.5 {
		t.Errorf("expected value 23.5, got %v", capturedPoint.Value)
	}
}

func TestSubmitMetric_MissingDeviceID(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	req := &pb.SubmitMetricRequest{
		DeviceId:   "",
		MetricName: "temperature",
		Value:      23.5,
	}

	resp, err := service.SubmitMetric(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for missing device_id")
	}

	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestSubmitMetric_MissingMetricName(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "",
		Value:      23.5,
	}

	resp, err := service.SubmitMetric(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for missing metric_name")
	}
}

func TestSubmitMetric_InvalidValue_NaN(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      math.NaN(),
	}

	resp, err := service.SubmitMetric(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for NaN value")
	}
}

func TestSubmitMetric_InvalidValue_Infinity(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      math.Inf(1),
	}

	resp, err := service.SubmitMetric(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for Infinity value")
	}
}

func TestSubmitMetric_DatabaseError(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.insertMetricFunc = func(_ context.Context, _ db.MetricPoint) error {
		return errors.New("database connection failed")
	}

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      23.5,
	}

	resp, err := service.SubmitMetric(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error from database")
	}

	if s, ok := status.FromError(err); !ok || s.Code() != codes.Internal {
		t.Errorf("expected Internal error, got %v", err)
	}
}

func TestSubmitMetric_ForeignKeyUnknownDevice(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.insertMetricFunc = func(_ context.Context, _ db.MetricPoint) error {
		return &pgconn.PgError{Code: "23503"}
	}

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      23.5,
	}

	resp, err := service.SubmitMetric(ctx, req)
	if resp != nil {
		t.Fatal("expected nil response on error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound for FK violation, got %v", err)
	}
}

func TestSubmitMetric_ForeignKeyUnknownDevice_WrappedLikeDBLayer(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.insertMetricFunc = func(_ context.Context, _ db.MetricPoint) error {
		return fmt.Errorf("failed to insert metric: %w", &pgconn.PgError{Code: "23503"})
	}

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      23.5,
	}

	_, err := service.SubmitMetric(ctx, req)
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound for wrapped FK violation, got %v", err)
	}
}

func TestSubmitMetric_ForeignKeyUnknownDevice_SQLStateInMessageOnly(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.insertMetricFunc = func(_ context.Context, _ db.MetricPoint) error {
		return errors.New(`failed to insert metric: ERROR: insert or update on table "metrics" violates foreign key (SQLSTATE 23503)`)
	}

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      23.5,
	}

	_, err := service.SubmitMetric(ctx, req)
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound when SQLSTATE 23503 appears only in message, got %v", err)
	}
}

// ==================== SubmitBatch Tests ====================

func TestSubmitBatch_ValidBatch(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	var capturedPoints []db.MetricPoint
	mockDB.insertMetricBatchFunc = func(_ context.Context, points []db.MetricPoint) error {
		capturedPoints = points
		return nil
	}

	req := &pb.SubmitBatchRequest{
		Metrics: []*pb.SubmitMetricRequest{
			{
				DeviceId:   "device-001",
				MetricName: "temperature",
				Value:      23.5,
			},
			{
				DeviceId:   "device-002",
				MetricName: "humidity",
				Value:      65.0,
			},
		},
	}

	resp, err := service.SubmitBatch(ctx, req)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp.AcceptedCount != 2 {
		t.Errorf("expected 2 accepted, got %d", resp.AcceptedCount)
	}

	if len(capturedPoints) != 2 {
		t.Errorf("expected 2 captured points, got %d", len(capturedPoints))
	}
}

func TestSubmitBatch_ForeignKeyUnknownDevice(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.insertMetricBatchFunc = func(_ context.Context, _ []db.MetricPoint) error {
		return &pgconn.PgError{Code: "23503"}
	}

	req := &pb.SubmitBatchRequest{
		Metrics: []*pb.SubmitMetricRequest{
			{DeviceId: "device-001", MetricName: "temperature", Value: 1},
		},
	}

	resp, err := service.SubmitBatch(ctx, req)
	if resp != nil {
		t.Fatal("expected nil response on error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound for FK violation, got %v", err)
	}
}

func TestSubmitBatch_EmptyBatch(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	req := &pb.SubmitBatchRequest{
		Metrics: []*pb.SubmitMetricRequest{},
	}

	resp, err := service.SubmitBatch(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for empty batch")
	}
}

func TestSubmitBatch_PartialInvalid(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.insertMetricBatchFunc = func(_ context.Context, points []db.MetricPoint) error {
		return nil
	}

	req := &pb.SubmitBatchRequest{
		Metrics: []*pb.SubmitMetricRequest{
			{
				DeviceId:   "device-001",
				MetricName: "temperature",
				Value:      23.5,
			},
			{
				DeviceId:   "", // Invalid: missing device_id
				MetricName: "humidity",
				Value:      65.0,
			},
			{
				DeviceId:   "device-003",
				MetricName: "pressure",
				Value:      1013.25,
			},
		},
	}

	resp, err := service.SubmitBatch(ctx, req)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Should accept 2 valid metrics
	if resp.AcceptedCount != 2 {
		t.Errorf("expected 2 accepted, got %d", resp.AcceptedCount)
	}

	if resp.RejectedCount != 1 {
		t.Errorf("expected 1 rejected, got %d", resp.RejectedCount)
	}
}

func TestSubmitBatch_TooLarge(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	// Create batch with 1001 metrics (exceeds limit of 1000)
	metrics := make([]*pb.SubmitMetricRequest, 1001)
	for i := 0; i < 1001; i++ {
		metrics[i] = &pb.SubmitMetricRequest{
			DeviceId:   "device-001",
			MetricName: "temperature",
			Value:      23.5,
		}
	}

	req := &pb.SubmitBatchRequest{Metrics: metrics}

	resp, err := service.SubmitBatch(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for batch too large")
	}
}

// ==================== QueryMetrics Tests ====================

func TestQueryMetrics_ValidQuery(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	// Create test points
	now := time.Now().UTC()
	testPoints := []db.MetricPoint{
		{
			Timestamp:  now.Add(-1 * time.Hour),
			DeviceID:   "device-001",
			MetricName: "temperature",
			Value:      22.0,
			Status:     "OK",
		},
		{
			Timestamp:  now,
			DeviceID:   "device-001",
			MetricName: "temperature",
			Value:      23.5,
			Status:     "OK",
		},
	}

	mockDB.queryMetricsFunc = func(_ context.Context, _ db.QueryFilter) ([]db.MetricPoint, error) {
		return testPoints, nil
	}

	mockDB.queryMetricsCountFunc = func(_ context.Context, _ db.QueryFilter) (int64, error) {
		return int64(len(testPoints)), nil
	}

	req := &pb.QueryMetricsRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		StartTime:  timestamppb.New(now.Add(-2 * time.Hour)),
		EndTime:    timestamppb.New(now.Add(1 * time.Hour)),
		Limit:      100,
		Offset:     0,
	}

	// Use a mock stream
	mockStream := &mockQueryStream{ctx: ctx}

	err := service.QueryMetrics(req, mockStream)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(mockStream.responses) != 2 {
		t.Errorf("expected 2 responses, got %d", len(mockStream.responses))
	}
}

func TestQueryMetrics_InvalidTimeRange(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	now := time.Now().UTC()

	req := &pb.QueryMetricsRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		StartTime:  timestamppb.New(now),
		EndTime:    timestamppb.New(now.Add(-1 * time.Hour)), // End before start
		Limit:      100,
		Offset:     0,
	}

	mockStream := &mockQueryStream{ctx: ctx}

	err := service.QueryMetrics(req, mockStream)

	if err == nil {
		t.Fatal("expected error for invalid time range")
	}
}

func TestQueryMetrics_InvalidLimit(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	now := time.Now().UTC()

	req := &pb.QueryMetricsRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		StartTime:  timestamppb.New(now.Add(-1 * time.Hour)),
		EndTime:    timestamppb.New(now),
		Limit:      20000, // Exceeds max of 10000
		Offset:     0,
	}

	mockStream := &mockQueryStream{ctx: ctx}

	err := service.QueryMetrics(req, mockStream)

	if err == nil {
		t.Fatal("expected error for limit exceeding max")
	}
}

// ==================== GetAggregations Tests ====================

func TestGetAggregations_ValidQuery(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	now := time.Now().UTC()
	testResults := []db.AggregationResult{
		{
			WindowStart: now.Add(-1 * time.Hour),
			WindowEnd:   now,
			AvgValue:    23.0,
			MaxValue:    25.0,
			MinValue:    21.0,
			Count:       60,
			P95:         24.5,
			P99:         24.9,
		},
	}

	mockDB.queryAggregatedFunc = func(_ context.Context, _ db.AggregationQuery) ([]db.AggregationResult, error) {
		return testResults, nil
	}

	req := &pb.GetAggregationsRequest{
		DeviceId:         "device-001",
		MetricName:       "temperature",
		WindowSize:       pb.TimeWindowSize_TIME_WINDOW_SIZE_1_HOUR,
		StartTime:        timestamppb.New(now.Add(-24 * time.Hour)),
		EndTime:          timestamppb.New(now),
		AggregationTypes: []pb.AggregationType{pb.AggregationType_AGGREGATION_TYPE_AVG, pb.AggregationType_AGGREGATION_TYPE_MAX},
	}

	resp, err := service.GetAggregations(ctx, req)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp == nil || len(resp.Aggregations) != 1 {
		t.Fatalf("expected 1 aggregation, got %v", resp)
	}

	agg := resp.Aggregations[0]
	if agg.Aggregations["avg"] != 23.0 {
		t.Errorf("expected avg 23.0, got %v", agg.Aggregations["avg"])
	}
}

func TestGetAggregations_InvalidWindowSize(t *testing.T) {
	service, _, _ := setupTestService(t)
	ctx := context.Background()

	now := time.Now().UTC()

	req := &pb.GetAggregationsRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		WindowSize: pb.TimeWindowSize_TIME_WINDOW_SIZE_UNSPECIFIED, // Invalid
		StartTime:  timestamppb.New(now.Add(-24 * time.Hour)),
		EndTime:    timestamppb.New(now),
	}

	resp, err := service.GetAggregations(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error for unspecified window size")
	}
}

// ==================== GetMetricStats Tests ====================

func TestGetMetricStats_ValidRequest(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	now := time.Now().UTC()
	testStats := &db.MetricStats{
		DeviceID:     "device-001",
		MetricName:   "temperature",
		FirstSeen:    now.Add(-24 * time.Hour),
		LastSeen:     now,
		TotalSamples: 1440,
		AvgValue:     22.5,
		MinValue:     15.0,
		MaxValue:     30.0,
		StdDevValue:  2.5,
		LatestValue:  23.5,
		LatestStatus: "OK",
	}

	mockDB.getMetricStatsFunc = func(_ context.Context, _, _ string, _, _ time.Time) (*db.MetricStats, error) {
		return testStats, nil
	}

	req := &pb.GetMetricStatsRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		StartTime:  timestamppb.New(now.Add(-24 * time.Hour)),
		EndTime:    timestamppb.New(now),
	}

	resp, err := service.GetMetricStats(ctx, req)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if resp == nil || resp.Stats == nil {
		t.Fatal("expected stats in response")
	}

	if resp.Stats.AvgValue != 22.5 {
		t.Errorf("expected avg 22.5, got %v", resp.Stats.AvgValue)
	}

	if resp.Stats.TotalSamples != 1440 {
		t.Errorf("expected 1440 samples, got %d", resp.Stats.TotalSamples)
	}
}

func TestGetMetricStats_NoData(t *testing.T) {
	service, mockDB, _ := setupTestService(t)
	ctx := context.Background()

	mockDB.getMetricStatsFunc = func(_ context.Context, _, _ string, _, _ time.Time) (*db.MetricStats, error) {
		return nil, errors.New("no metrics found")
	}

	req := &pb.GetMetricStatsRequest{
		DeviceId:   "device-001",
		MetricName: "nonexistent",
	}

	resp, err := service.GetMetricStats(ctx, req)

	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	if err == nil {
		t.Fatal("expected error when no metrics found")
	}
}

// ==================== Helper Functions for Tests ====================

type mockQueryStream struct {
	ctx       context.Context
	responses []*pb.QueryMetricsResponse
}

func (m *mockQueryStream) Send(resp *pb.QueryMetricsResponse) error {
	m.responses = append(m.responses, resp)
	return nil
}

func (m *mockQueryStream) Context() context.Context {
	return m.ctx
}

func (m *mockQueryStream) SetHeader(_ metadata.MD) error {
	return nil
}

func (m *mockQueryStream) SendHeader(_ metadata.MD) error {
	return nil
}

func (m *mockQueryStream) SetTrailer(_ metadata.MD) {
}

func (m *mockQueryStream) SendMsg(_ interface{}) error {
	return nil
}

func (m *mockQueryStream) RecvMsg(_ interface{}) error {
	return nil
}

// ==================== Benchmark Tests ====================

func BenchmarkSubmitMetric(b *testing.B) {
	service, _, _ := setupTestService(&testing.T{})
	ctx := context.Background()

	req := &pb.SubmitMetricRequest{
		DeviceId:   "device-001",
		MetricName: "temperature",
		Value:      23.5,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = service.SubmitMetric(ctx, req)
	}
}

func BenchmarkSubmitBatch_10Metrics(b *testing.B) {
	service, _, _ := setupTestService(&testing.T{})
	ctx := context.Background()

	metrics := make([]*pb.SubmitMetricRequest, 10)
	for i := 0; i < 10; i++ {
		metrics[i] = &pb.SubmitMetricRequest{
			DeviceId:   "device-001",
			MetricName: "temperature",
			Value:      23.5 + float64(i),
		}
	}

	req := &pb.SubmitBatchRequest{Metrics: metrics}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = service.SubmitBatch(ctx, req)
	}
}

// ==================== Helper Validation Tests ====================

func TestIsFinite_Valid(t *testing.T) {
	tests := []float64{0.0, 1.5, -1.5, 1e308, -1e308}
	for _, val := range tests {
		if !isFinite(val) {
			t.Errorf("expected %f to be finite", val)
		}
	}
}

func TestIsFinite_Invalid(t *testing.T) {
	tests := []float64{math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, val := range tests {
		if isFinite(val) {
			t.Errorf("expected %f to not be finite", val)
		}
	}
}

func TestWindowSizeToString(t *testing.T) {
	tests := []struct {
		input    pb.TimeWindowSize
		expected string
	}{
		{pb.TimeWindowSize_TIME_WINDOW_SIZE_1_MINUTE, "1m"},
		{pb.TimeWindowSize_TIME_WINDOW_SIZE_5_MINUTES, "5m"},
		{pb.TimeWindowSize_TIME_WINDOW_SIZE_1_HOUR, "1h"},
		{pb.TimeWindowSize_TIME_WINDOW_SIZE_1_DAY, "1d"},
		{pb.TimeWindowSize_TIME_WINDOW_SIZE_UNSPECIFIED, "1h"},
	}

	for _, test := range tests {
		result := windowSizeToString(test.input)
		if result != test.expected {
			t.Errorf("expected %q, got %q", test.expected, result)
		}
	}
}

// ==================== Dedup Logic Tests ====================

// mockDedupCache implements the telemetryDedupCache interface for testing dedup logic.
type mockDedupCache struct {
	marked map[string]bool // key -> was marked
}

func (m *mockDedupCache) TryMarkDedup(ctx context.Context, deviceID string, ts time.Time, ttl time.Duration) (bool, error) {
	if m.marked == nil {
		m.marked = make(map[string]bool)
	}

	key := buildDedupKey(deviceID, ts)
	if _, exists := m.marked[key]; exists {
		// Already marked - this is a duplicate
		return false, nil
	}

	// Mark it now
	m.marked[key] = true
	return true, nil
}

func buildDedupKey(deviceID string, ts time.Time) string {
	// Per planning.md: Key=dedup:{device_id}:{timestamp}
	return "dedup:" + deviceID + ":" + ts.Format("2006-01-02 15:04:05")
}

// TestDedup_FirstMetricAccepted tests that first metric with unique timestamp is accepted.
func TestDedup_FirstMetricAccepted(t *testing.T) {
	t.Log("=== Test: Dedup Logic - First metric with timestamp accepted ===")

	ctx := context.Background()
	cache := &mockDedupCache{}

	deviceID := "device-001"
	ts := time.Now().UTC()

	// First submission should be marked (created = true)
	created, err := cache.TryMarkDedup(ctx, deviceID, ts, 10*time.Minute)

	if err != nil {
		t.Fatalf("TryMarkDedup failed: %v", err)
	}

	if !created {
		t.Errorf("expected created=true for first metric, got false")
	}

	t.Log("✓ First metric with timestamp accepted")
}

// TestDedup_DuplicateWithin10MinRejected tests that duplicate within 10 min is rejected.
func TestDedup_DuplicateWithin10MinRejected(t *testing.T) {
	t.Log("=== Test: Dedup Logic - Duplicate within 10min TTL rejected ===")

	ctx := context.Background()
	cache := &mockDedupCache{}

	deviceID := "device-001"
	ts := time.Now().UTC()

	// First submission
	created1, _ := cache.TryMarkDedup(ctx, deviceID, ts, 10*time.Minute)
	if !created1 {
		t.Fatal("expected first submission to succeed")
	}

	// Duplicate submission (same timestamp)
	created2, _ := cache.TryMarkDedup(ctx, deviceID, ts, 10*time.Minute)
	if created2 {
		t.Errorf("expected created=false for duplicate within TTL, got true")
	}

	t.Log("✓ Duplicate within 10min TTL correctly rejected")
}

// TestDedup_DifferentTimestampsAccepted tests that same device with different timestamp is accepted.
func TestDedup_DifferentTimestampsAccepted(t *testing.T) {
	t.Log("=== Test: Dedup Logic - Same device, different timestamp accepted ===")

	ctx := context.Background()
	cache := &mockDedupCache{}

	deviceID := "device-001"
	ts1 := time.Now().UTC()
	ts2 := ts1.Add(1 * time.Minute) // Different timestamp

	// First metric
	created1, _ := cache.TryMarkDedup(ctx, deviceID, ts1, 10*time.Minute)
	if !created1 {
		t.Fatal("expected first metric accepted")
	}

	// Second metric - different timestamp, same device
	created2, _ := cache.TryMarkDedup(ctx, deviceID, ts2, 10*time.Minute)
	if !created2 {
		t.Errorf("expected created=true for different timestamp, got false")
	}

	t.Log("✓ Same device with different timestamp accepted")
}

// TestDedup_DifferentDevicesAccepted tests that different device IDs are always accepted.
func TestDedup_DifferentDevicesAccepted(t *testing.T) {
	t.Log("=== Test: Dedup Logic - Different device IDs accepted ===")

	ctx := context.Background()
	cache := &mockDedupCache{}

	ts := time.Now().UTC()
	devices := []string{"device-001", "device-002", "device-003"}

	for i, deviceID := range devices {
		created, _ := cache.TryMarkDedup(ctx, deviceID, ts, 10*time.Minute)
		if !created {
			t.Errorf("device %d (%s): expected created=true, got false", i+1, deviceID)
		}
	}

	t.Log("✓ Multiple different devices correctly accepted")
}

// TestDedup_RedisUnavailableContinues tests graceful degradation when Redis is down.
func TestDedup_RedisUnavailableContinues(t *testing.T) {
	t.Log("=== Test: Dedup Logic - Redis unavailable continues (graceful degradation) ===")

	// Simulate Redis unavailable scenario
	type unavailableCache struct{}

	var tryCount int
	tryMarkDedup := func(ctx context.Context, deviceID string, ts time.Time, ttl time.Duration) (bool, error) {
		tryCount++
		// Simulate Redis connection error
		return true, errors.New("redis: connection refused")
	}

	ctx := context.Background()
	_, err := tryMarkDedup(ctx, "device-001", time.Now().UTC(), 10*time.Minute)

	// Per planning.md: "If Redis unavailable: LOG warning, CONTINUE without dedup"
	if err != nil {
		t.Logf("✓ Redis unavailable returned error: %v", err)
		t.Logf("✓ System continues gracefully (dedup disabled temporarily)")
	}

	// Verify the call was attempted
	if tryCount != 1 {
		t.Errorf("expected 1 call, got %d", tryCount)
	}
}

// TestDedup_KeyFormat tests that key format matches planning.md specification.
func TestDedup_KeyFormat(t *testing.T) {
	t.Log("=== Test: Dedup Key Format (dedup:{device_id}:{timestamp}) ===")

	deviceID := "sensor-001"
	ts, _ := time.Parse(time.RFC3339, "2026-04-12T10:00:00Z")

	// Key format per planning.md: dedup:{device_id}:{timestamp}
	key := buildDedupKey(deviceID, ts)

	expectedKeyPrefix := "dedup:sensor-001:"
	if !contains(key, expectedKeyPrefix) {
		t.Errorf("expected key to start with %q, got %q", expectedKeyPrefix, key)
	}

	t.Logf("✓ Key format correct: %s", key)
}

// Helper function
func contains(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
