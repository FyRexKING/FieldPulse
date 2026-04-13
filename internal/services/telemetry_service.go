package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/db"
	oteltracing "fieldpulse.io/internal/otel"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	dedupTTL      = 10 * time.Minute
	lastSeenTTL   = 24 * time.Hour
	maxBatchSize  = 1000
	maxQueryLimit = 10000
	// futureTimestampSkew is allowed clock skew ahead of server time (planning.md §4.1).
	futureTimestampSkew = 5 * time.Second
	// DefaultWriteQueueCapacity is the buffered channel size for DB writes (planning.md §4.1).
	DefaultWriteQueueCapacity = 1024
	// postMetricSideEffectTimeout bounds Redis/cache work after insert so SCAN/dedup cannot wedge the server.
	postMetricSideEffectTimeout = 5 * time.Second
)

// TelemetryStore abstracts database operations to keep service modular/testable.
type TelemetryStore interface {
	InsertMetric(ctx context.Context, point db.MetricPoint) error
	InsertMetricBatch(ctx context.Context, points []db.MetricPoint) error
	QueryMetrics(ctx context.Context, filter db.QueryFilter) ([]db.MetricPoint, error)
	QueryMetricsCount(ctx context.Context, filter db.QueryFilter) (int64, error)
	QueryAggregated(ctx context.Context, agg db.AggregationQuery) ([]db.AggregationResult, error)
	GetMetricStats(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time) (*db.MetricStats, error)
}

// TelemetryCacheStore abstracts cache operations used by telemetry service.
type TelemetryCacheStore interface {
	CacheQueryResult(ctx context.Context, key string, result interface{}) error
	GetQueryResult(ctx context.Context, key string, result interface{}) error
	InvalidateMetricCache(ctx context.Context, deviceID, metricName string) error
}

type telemetryDedupCache interface {
	TryMarkDedup(ctx context.Context, deviceID string, ts time.Time, ttl time.Duration) (bool, error)
}

type telemetryLastSeenCache interface {
	SetDeviceLastSeen(ctx context.Context, deviceID string, ts time.Time, ttl time.Duration) error
}

// metricWriteJob carries points to the async DB writer (planning.md §4.1 buffered channel).
type metricWriteJob struct {
	points []db.MetricPoint
	done   chan error
	// insertCtx is the gRPC request context; InsertMetric honors deadline/cancel so the worker does not stall the pool after clients time out.
	insertCtx context.Context
}

// TelemetryService implements the telemetry gRPC service.
type TelemetryService struct {
	pb.UnimplementedTelemetryServiceServer
	db          TelemetryStore
	cache       TelemetryCacheStore
	alertEval   *AlertEvaluator
	writeCh     chan metricWriteJob
	writeCancel context.CancelFunc
}

// NewTelemetryService creates a new telemetry service instance with synchronous DB writes (used in tests).
// Optional alert evaluator can be passed for async threshold checks.
func NewTelemetryService(database TelemetryStore, cacheClient TelemetryCacheStore, evaluators ...*AlertEvaluator) *TelemetryService {
	return NewTelemetryServiceWithWriteQueue(database, cacheClient, 0, evaluators...)
}

// NewTelemetryServiceWithWriteQueue creates a telemetry service. If queueCap > 0, inserts go through a buffered channel and a worker goroutine.
func NewTelemetryServiceWithWriteQueue(database TelemetryStore, cacheClient TelemetryCacheStore, queueCap int, evaluators ...*AlertEvaluator) *TelemetryService {
	var evaluator *AlertEvaluator
	if len(evaluators) > 0 {
		evaluator = evaluators[0]
	}

	s := &TelemetryService{
		db:        database,
		cache:     cacheClient,
		alertEval: evaluator,
	}
	if queueCap > 0 {
		s.writeCh = make(chan metricWriteJob, queueCap)
		ctx, cancel := context.WithCancel(context.Background())
		s.writeCancel = cancel
		go s.runMetricWriteWorker(ctx)
	}
	return s
}

// StopWriteWorker stops the async DB writer; call on shutdown.
func (s *TelemetryService) StopWriteWorker() {
	if s.writeCancel != nil {
		s.writeCancel()
	}
}

func (s *TelemetryService) runMetricWriteWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.writeCh:
			insertCtx := job.insertCtx
			if insertCtx == nil {
				insertCtx = ctx
			}
			var err error
			if len(job.points) == 1 {
				err = s.db.InsertMetric(insertCtx, job.points[0])
			} else {
				err = s.db.InsertMetricBatch(insertCtx, job.points)
			}
			if job.done != nil {
				job.done <- err
			}
			if err != nil {
				continue
			}
			if len(job.points) == 1 {
				// Do not block the worker on Redis SCAN / alert eval; otherwise queued SubmitMetrics stall until client deadline.
				pt := job.points[0]
				go s.postMetricWrite(context.Background(), pt)
				continue
			}
			seen := make(map[string]struct{})
			for _, p := range job.points {
				k := p.DeviceID + ":" + p.MetricName
				if _, ok := seen[k]; !ok {
					_ = s.cache.InvalidateMetricCache(ctx, p.DeviceID, p.MetricName)
					seen[k] = struct{}{}
				}
				_ = s.markLastSeen(ctx, p.DeviceID, p.Timestamp)
				s.evaluateAsync(ctx, p.DeviceID, p.MetricName, p.Value)
			}
		}
	}
}

func insertFailureStatus(err error, batch bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return status.Error(codes.NotFound, "device not found")
	}
	// Some pgx/Timescale paths surface FK violations without an unwrap-able *pgconn.PgError.
	if strings.Contains(err.Error(), "SQLSTATE 23503") {
		return status.Error(codes.NotFound, "device not found")
	}
	if batch {
		return status.Error(codes.Internal, fmt.Sprintf("batch insert failed: %v", err))
	}
	return status.Error(codes.Internal, fmt.Sprintf("failed to store metric: %v", err))
}

func (s *TelemetryService) SubmitMetric(ctx context.Context, req *pb.SubmitMetricRequest) (*pb.SubmitMetricResponse, error) {
	newCtx, endSpan := oteltracing.TraceTelemetrySubmit(ctx, req.DeviceId, req.MetricName, req.Value)
	defer endSpan()

	timestamp, err := validateAndResolveMetricTimestamp(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if duplicated, err := s.isDuplicate(newCtx, req.DeviceId, timestamp); err != nil {
		log.Printf("dedup: non-fatal, continuing: %v", err)
		duplicated = false
	} else if duplicated {
		return &pb.SubmitMetricResponse{
			Success:         true,
			ServerTimestamp: timestamppb.New(timestamp),
			Message:         "duplicate ignored",
		}, nil
	}

	point := metricPointFromRequest(req, timestamp)
	if s.writeCh != nil {
		done := make(chan error, 1)
		select {
		case s.writeCh <- metricWriteJob{points: []db.MetricPoint{point}, done: done, insertCtx: newCtx}:
		case <-newCtx.Done():
			return nil, status.Error(codes.Canceled, "request canceled")
		default:
			return nil, status.Error(codes.ResourceExhausted, "metric write queue full")
		}
		select {
		case err := <-done:
			if err != nil {
				return nil, insertFailureStatus(err, false)
			}
		case <-newCtx.Done():
			return nil, status.FromContextError(newCtx.Err()).Err()
		}
	} else {
		if err := s.db.InsertMetric(newCtx, point); err != nil {
			return nil, insertFailureStatus(err, false)
		}
		s.postMetricWrite(newCtx, point)
	}

	return &pb.SubmitMetricResponse{
		Success:         true,
		ServerTimestamp: timestamppb.New(timestamp),
		Message:         "metric stored successfully",
	}, nil
}

func (s *TelemetryService) SubmitBatch(ctx context.Context, req *pb.SubmitBatchRequest) (*pb.SubmitBatchResponse, error) {
	if len(req.Metrics) == 0 {
		return nil, status.Error(codes.InvalidArgument, "batch cannot be empty")
	}
	if len(req.Metrics) > maxBatchSize {
		return nil, status.Error(codes.InvalidArgument, "batch too large (max 1000)")
	}

	now := time.Now().UTC()
	points := make([]db.MetricPoint, 0, len(req.Metrics))
	rejectedCount := 0

	for _, metricReq := range req.Metrics {
		timestamp, err := validateAndResolveMetricTimestamp(metricReq)
		if err != nil {
			rejectedCount++
			continue
		}

		duplicated, err := s.isDuplicate(ctx, metricReq.DeviceId, timestamp)
		if err != nil {
			log.Printf("dedup batch: non-fatal: %v", err)
			duplicated = false
		}
		if duplicated {
			rejectedCount++
			continue
		}

		points = append(points, metricPointFromRequest(metricReq, timestamp))
	}

	if len(points) == 0 {
		return nil, status.Error(codes.InvalidArgument, "all metrics in batch were invalid or duplicated")
	}

	if s.writeCh != nil {
		done := make(chan error, 1)
		select {
		case s.writeCh <- metricWriteJob{points: points, done: done, insertCtx: ctx}:
		case <-ctx.Done():
			return nil, status.Error(codes.Canceled, "request canceled")
		default:
			return nil, status.Error(codes.ResourceExhausted, "metric write queue full")
		}
		select {
		case err := <-done:
			if err != nil {
				return nil, insertFailureStatus(err, true)
			}
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	} else {
		if err := s.db.InsertMetricBatch(ctx, points); err != nil {
			return nil, insertFailureStatus(err, true)
		}
		seen := make(map[string]struct{})
		for _, point := range points {
			k := point.DeviceID + ":" + point.MetricName
			if _, ok := seen[k]; !ok {
				_ = s.cache.InvalidateMetricCache(ctx, point.DeviceID, point.MetricName)
				seen[k] = struct{}{}
			}
			_ = s.markLastSeen(ctx, point.DeviceID, point.Timestamp)
			s.evaluateAsync(ctx, point.DeviceID, point.MetricName, point.Value)
		}
	}

	return &pb.SubmitBatchResponse{
		AcceptedCount:   int32(len(points)),
		RejectedCount:   int32(rejectedCount),
		ServerTimestamp: timestamppb.New(now),
		Message:         fmt.Sprintf("accepted %d metrics, rejected %d", len(points), rejectedCount),
	}, nil
}

func (s *TelemetryService) QueryMetrics(req *pb.QueryMetricsRequest, stream pb.TelemetryService_QueryMetricsServer) error {
	// Set default limit if not specified
	if req.Limit == 0 {
		req.Limit = 100
	}

	if err := validateQueryRequest(req); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// Provide defaults for time range if not specified.
	// Important: nil *timestamppb.Timestamp must not use AsTime() — it returns Unix 0, not Go's zero time,
	// which would incorrectly constrain the query to a single instant (1970) and return no rows.
	var startTime, endTime time.Time
	if req.StartTime != nil {
		startTime = req.StartTime.AsTime()
	}
	if req.EndTime != nil {
		endTime = req.EndTime.AsTime()
	}
	if startTime.IsZero() && endTime.IsZero() {
		endTime = time.Now().UTC()
		startTime = endTime.Add(-24 * time.Hour)
	} else if startTime.IsZero() {
		startTime = endTime.Add(-24 * time.Hour)
	} else if endTime.IsZero() {
		endTime = time.Now().UTC()
	}

	filter := db.QueryFilter{
		DeviceID:       req.DeviceId,
		MetricName:     req.MetricName,
		StartTime:      startTime,
		EndTime:        endTime,
		Limit:          req.Limit,
		Offset:         req.Offset,
		OrderAscending: req.OrderAscending,
	}

	points, err := s.db.QueryMetrics(stream.Context(), filter)
	if err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("query failed: %v", err))
	}

	totalCount, err := s.db.QueryMetricsCount(stream.Context(), filter)
	if err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("count query failed: %v", err))
	}

	for _, dbPoint := range points {
		pbPoint := &pb.MetricPoint{
			Timestamp:  timestamppb.New(dbPoint.Timestamp),
			DeviceId:   dbPoint.DeviceID,
			MetricName: dbPoint.MetricName,
			Value:      dbPoint.Value,
			Tags:       dbPoint.Tags,
			Status:     parseMetricStatus(dbPoint.Status),
		}

		response := &pb.QueryMetricsResponse{
			Points:     []*pb.MetricPoint{pbPoint},
			HasMore:    int32(req.Offset+req.Limit) < int32(totalCount),
			TotalCount: totalCount,
		}
		if err := stream.Send(response); err != nil {
			return status.Error(codes.Internal, fmt.Sprintf("send failed: %v", err))
		}
	}

	return nil
}

func (s *TelemetryService) GetAggregations(ctx context.Context, req *pb.GetAggregationsRequest) (*pb.GetAggregationsResponse, error) {
	if err := validateAggregationRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	windowSize := windowSizeToString(req.WindowSize)
	aggTypes := aggregationTypesToStrings(req.AggregationTypes)
	cacheKey := buildAggCacheKey(req.DeviceId, req.MetricName, windowSize, req.StartTime.AsTime(), req.EndTime.AsTime(), aggTypes)

	cachedResults := make([]*pb.AggregatedMetricPoint, 0)
	if err := s.cache.GetQueryResult(ctx, cacheKey, &cachedResults); err == nil {
		return &pb.GetAggregationsResponse{
			Aggregations:    cachedResults,
			QueryDurationMs: 0,
			FromCache:       true,
			Message:         "result from cache",
		}, nil
	}

	start := time.Now()
	dbResults, err := s.db.QueryAggregated(ctx, db.AggregationQuery{
		DeviceID:         req.DeviceId,
		MetricName:       req.MetricName,
		WindowSize:       windowSize,
		StartTime:        req.StartTime.AsTime(),
		EndTime:          req.EndTime.AsTime(),
		AggregationTypes: aggTypes,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("aggregation query failed: %v", err))
	}

	pbResults := make([]*pb.AggregatedMetricPoint, len(dbResults))
	for i, item := range dbResults {
		agg := map[string]float64{
			"avg": item.AvgValue,
			"max": item.MaxValue,
			"min": item.MinValue,
		}
		if item.P95 > 0 {
			agg["p95"] = item.P95
		}
		if item.P99 > 0 {
			agg["p99"] = item.P99
		}
		pbResults[i] = &pb.AggregatedMetricPoint{
			WindowStart:  timestamppb.New(item.WindowStart),
			WindowEnd:    timestamppb.New(item.WindowEnd),
			Aggregations: agg,
			Count:        item.Count,
			LastValue:    item.P99,
		}
	}

	_ = s.cache.CacheQueryResult(ctx, cacheKey, pbResults)

	return &pb.GetAggregationsResponse{
		Aggregations:    pbResults,
		QueryDurationMs: int32(time.Since(start).Milliseconds()),
		FromCache:       false,
		Message:         fmt.Sprintf("queried %d windows", len(pbResults)),
	}, nil
}

func (s *TelemetryService) GetMetricStats(ctx context.Context, req *pb.GetMetricStatsRequest) (*pb.GetMetricStatsResponse, error) {
	if err := validateStatsRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	dbStats, err := s.db.GetMetricStats(ctx, req.DeviceId, req.MetricName, req.StartTime.AsTime(), req.EndTime.AsTime())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, "context canceled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, "query timeout")
		}
		return nil, status.Error(codes.Internal, fmt.Sprintf("stats query failed: %v", err))
	}

	pbStats := &pb.MetricStats{
		DeviceId:     dbStats.DeviceID,
		MetricName:   dbStats.MetricName,
		FirstSeen:    timestamppb.New(dbStats.FirstSeen),
		LastSeen:     timestamppb.New(dbStats.LastSeen),
		TotalSamples: dbStats.TotalSamples,
		AvgValue:     dbStats.AvgValue,
		MinValue:     dbStats.MinValue,
		MaxValue:     dbStats.MaxValue,
		StddevValue:  dbStats.StdDevValue,
		LatestValue:  dbStats.LatestValue,
		LatestStatus: parseMetricStatus(dbStats.LatestStatus),
	}

	return &pb.GetMetricStatsResponse{
		Stats:        pbStats,
		CalculatedAt: timestamppb.New(time.Now().UTC()),
	}, nil
}

func (s *TelemetryService) postMetricWrite(ctx context.Context, point db.MetricPoint) {
	pwCtx, cancel := context.WithTimeout(ctx, postMetricSideEffectTimeout)
	defer cancel()
	_ = s.cache.InvalidateMetricCache(pwCtx, point.DeviceID, point.MetricName)
	_ = s.markLastSeen(pwCtx, point.DeviceID, point.Timestamp)
	// Do not pass pwCtx: defer cancel() runs when this returns, while EvaluateAsync
	// starts a goroutine that would inherit a canceled context and skip threshold lookup.
	s.evaluateAsync(context.Background(), point.DeviceID, point.MetricName, point.Value)
}

func (s *TelemetryService) evaluateAsync(ctx context.Context, deviceID, metricName string, value float64) {
	if s.alertEval != nil {
		s.alertEval.EvaluateAsync(ctx, deviceID, metricName, value)
	}
}

func (s *TelemetryService) isDuplicate(ctx context.Context, deviceID string, ts time.Time) (bool, error) {
	dedupCache, ok := s.cache.(telemetryDedupCache)
	if !ok {
		return false, nil
	}
	created, err := dedupCache.TryMarkDedup(ctx, deviceID, ts, dedupTTL)
	if err != nil {
		log.Printf("dedup: %v", err)
		return false, nil
	}
	return !created, nil
}

func (s *TelemetryService) markLastSeen(ctx context.Context, deviceID string, ts time.Time) error {
	lastSeenCache, ok := s.cache.(telemetryLastSeenCache)
	if !ok {
		return nil
	}
	return lastSeenCache.SetDeviceLastSeen(ctx, deviceID, ts, lastSeenTTL)
}

func metricPointFromRequest(req *pb.SubmitMetricRequest, ts time.Time) db.MetricPoint {
	return db.MetricPoint{
		Timestamp:  ts,
		DeviceID:   req.DeviceId,
		MetricName: req.MetricName,
		Value:      req.Value,
		Tags:       req.Tags,
		Status:     req.Status.String(),
	}
}

func validateAndResolveMetricTimestamp(req *pb.SubmitMetricRequest) (time.Time, error) {
	if err := validateSubmitRequest(req); err != nil {
		return time.Time{}, err
	}

	now := time.Now().UTC()
	if req.TimestampUnixSeconds <= 0 {
		return now, nil
	}

	ts := time.Unix(req.TimestampUnixSeconds, 0).UTC()
	if ts.After(now.Add(futureTimestampSkew)) {
		return time.Time{}, errors.New("future timestamps are not allowed (beyond 5s skew)")
	}

	return ts, nil
}

func validateSubmitRequest(req *pb.SubmitMetricRequest) error {
	if req.DeviceId == "" {
		return errors.New("device_id required")
	}
	if req.MetricName == "" {
		return errors.New("metric_name required")
	}
	if !isFinite(req.Value) {
		return errors.New("value must be a finite number")
	}
	return nil
}

func validateQueryRequest(req *pb.QueryMetricsRequest) error {
	if req.DeviceId == "" {
		return errors.New("device_id required")
	}
	if req.MetricName == "" {
		return errors.New("metric_name required")
	}
	var startTime, endTime time.Time
	if req.StartTime != nil {
		startTime = req.StartTime.AsTime()
	}
	if req.EndTime != nil {
		endTime = req.EndTime.AsTime()
	}
	if !startTime.IsZero() && !endTime.IsZero() && endTime.Before(startTime) {
		return errors.New("end_time must be after start_time")
	}
	if req.Limit <= 0 || req.Limit > maxQueryLimit {
		return fmt.Errorf("limit must be between 1 and %d", maxQueryLimit)
	}
	return nil
}

func validateAggregationRequest(req *pb.GetAggregationsRequest) error {
	if req.DeviceId == "" {
		return errors.New("device_id required")
	}
	if req.MetricName == "" {
		return errors.New("metric_name required")
	}
	var startTime, endTime time.Time
	if req.StartTime != nil {
		startTime = req.StartTime.AsTime()
	}
	if req.EndTime != nil {
		endTime = req.EndTime.AsTime()
	}
	if !startTime.IsZero() && !endTime.IsZero() && endTime.Before(startTime) {
		return errors.New("end_time must be after start_time")
	}
	if req.WindowSize == pb.TimeWindowSize_TIME_WINDOW_SIZE_UNSPECIFIED {
		return errors.New("window_size required")
	}
	return nil
}

func validateStatsRequest(req *pb.GetMetricStatsRequest) error {
	if req.DeviceId == "" {
		return errors.New("device_id required")
	}
	if req.MetricName == "" {
		return errors.New("metric_name required")
	}
	return nil
}

func parseMetricStatus(statusText string) pb.MetricStatus {
	if statusVal, ok := pb.MetricStatus_value[statusText]; ok {
		return pb.MetricStatus(statusVal)
	}
	return pb.MetricStatus_METRIC_STATUS_OK
}

func isFinite(f float64) bool {
	return !((f != f) || (f > 1e308) || (f < -1e308))
}

func windowSizeToString(ws pb.TimeWindowSize) string {
	switch ws {
	case pb.TimeWindowSize_TIME_WINDOW_SIZE_1_MINUTE:
		return "1m"
	case pb.TimeWindowSize_TIME_WINDOW_SIZE_5_MINUTES:
		return "5m"
	case pb.TimeWindowSize_TIME_WINDOW_SIZE_1_HOUR:
		return "1h"
	case pb.TimeWindowSize_TIME_WINDOW_SIZE_1_DAY:
		return "1d"
	default:
		return "1h"
	}
}

func aggregationTypesToStrings(types []pb.AggregationType) []string {
	if len(types) == 0 {
		return nil
	}
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = t.String()
	}
	return out
}

func buildAggCacheKey(deviceID, metricName, windowSize string, startTime, endTime time.Time, aggTypes []string) string {
	return fmt.Sprintf("agg:%s:%s:%s:%d:%d:%v", deviceID, metricName, windowSize, startTime.Unix(), endTime.Unix(), aggTypes)
}
