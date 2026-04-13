package services

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/db"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultSilenceTTL     = 5 * time.Minute
	defaultLastSeenMaxAge = 15 * time.Minute
)

// AlertStore abstracts alert persistence for easier testing and modularity.
type AlertStore interface {
	CreateThreshold(ctx context.Context, threshold db.Threshold) (string, error)
	GetThresholds(ctx context.Context, deviceID, metricName string, onlyEnabled bool) ([]db.Threshold, error)
	UpdateThreshold(ctx context.Context, thresholdID string, updates map[string]interface{}) error
	DeleteThreshold(ctx context.Context, thresholdID string) error
	GetActiveAlerts(ctx context.Context, deviceID, severity string, limit, offset int) ([]db.Alert, int64, error)
	QueryAlertHistory(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time, limit int) ([]db.Alert, error)
	SilenceDevice(ctx context.Context, deviceID, metricName string, durationSeconds int, reason string) (string, error)
	GetActiveSilences(ctx context.Context, deviceID string) ([]db.SilenceRule, error)
	ClearSilences(ctx context.Context, deviceID string) error
	GetAlertStats(ctx context.Context, deviceID string, startTime, endTime time.Time) (map[string]interface{}, error)
	InsertAlert(ctx context.Context, alert db.Alert) error
}

// AlertService implements the alert gRPC service.
type AlertService struct {
	pb.UnimplementedAlertServiceServer
	db          AlertStore
	redisClient *redis.Client
}

// NewAlertService creates a new alert service instance.
func NewAlertService(database AlertStore, redisClients ...*redis.Client) *AlertService {
	service := &AlertService{db: database}
	if len(redisClients) > 0 {
		service.redisClient = redisClients[0]
	}
	return service
}

// CreateThreshold creates an alert threshold.
func (s *AlertService) CreateThreshold(ctx context.Context, req *pb.CreateThresholdRequest) (*pb.CreateThresholdResponse, error) {
	if err := validateThresholdRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	threshold := db.Threshold{
		DeviceID:      req.DeviceId,
		MetricName:    req.MetricName,
		Type:          req.Type.String(),
		LowerBound:    req.LowerBound,
		UpperBound:    req.UpperBound,
		ChangePercent: req.ChangePercent,
		Severity:      req.Severity.String(),
		Enabled:       req.Enabled,
	}

	thresholdID, err := s.db.CreateThreshold(ctx, threshold)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create threshold: %v", err))
	}

	return &pb.CreateThresholdResponse{
		ThresholdId: thresholdID,
		CreatedAt:   timestamppb.Now(),
	}, nil
}

// GetThresholds retrieves thresholds for a device.
func (s *AlertService) GetThresholds(ctx context.Context, req *pb.GetThresholdsRequest) (*pb.GetThresholdsResponse, error) {
	if req.DeviceId == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id required")
	}

	thresholds, err := s.db.GetThresholds(ctx, req.DeviceId, req.MetricName, req.OnlyEnabled)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("query failed: %v", err))
	}

	pbThresholds := make([]*pb.Threshold, len(thresholds))
	for i, t := range thresholds {
		pbThresholds[i] = &pb.Threshold{
			ThresholdId:   t.ThresholdID,
			DeviceId:      t.DeviceID,
			MetricName:    t.MetricName,
			Type:          pb.ThresholdType(pb.ThresholdType_value[t.Type]),
			LowerBound:    t.LowerBound,
			UpperBound:    t.UpperBound,
			ChangePercent: t.ChangePercent,
			Severity:      pb.AlertSeverity(pb.AlertSeverity_value[t.Severity]),
			Enabled:       t.Enabled,
			CreatedAt:     timestamppb.New(t.CreatedAt),
			UpdatedAt:     timestamppb.New(t.UpdatedAt),
		}
	}

	return &pb.GetThresholdsResponse{
		Thresholds: pbThresholds,
		TotalCount: int32(len(pbThresholds)),
	}, nil
}

// UpdateThreshold modifies a threshold.
func (s *AlertService) UpdateThreshold(ctx context.Context, req *pb.UpdateThresholdRequest) (*pb.UpdateThresholdResponse, error) {
	if req.ThresholdId == "" {
		return nil, status.Error(codes.InvalidArgument, "threshold_id required")
	}

	updates := make(map[string]interface{})
	if req.LowerBound > 0 {
		updates["lower_bound"] = req.LowerBound
	}
	if req.UpperBound > 0 {
		updates["upper_bound"] = req.UpperBound
	}
	if req.ChangePercent > 0 {
		updates["change_percent"] = req.ChangePercent
	}
	if req.Severity > 0 {
		updates["severity"] = req.Severity.String()
	}
	if req.Enabled {
		updates["enabled"] = req.Enabled
	}

	if err := s.db.UpdateThreshold(ctx, req.ThresholdId, updates); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("update failed: %v", err))
	}

	return &pb.UpdateThresholdResponse{
		ThresholdId: req.ThresholdId,
		UpdatedAt:   timestamppb.Now(),
	}, nil
}
func (s *AlertService) DeleteThreshold(ctx context.Context, req *pb.DeleteThresholdRequest) (*emptypb.Empty, error) {
	if req.ThresholdId == "" {
		return nil, status.Error(codes.InvalidArgument, "threshold_id required")
	}

	if err := s.db.DeleteThreshold(ctx, req.ThresholdId); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("delete failed: %v", err))
	}

		return &emptypb.Empty{}, nil
}

// GetActiveAlerts retrieves currently triggered alerts.
func (s *AlertService) GetActiveAlerts(ctx context.Context, req *pb.GetActiveAlertsRequest) (*pb.GetActiveAlertsResponse, error) {
	limit := int(req.Limit)
	if limit == 0 || limit > 1000 {
		limit = 100
	}

	alerts, total, err := s.db.GetActiveAlerts(ctx, req.DeviceId, req.MinSeverity.String(), limit, int(req.Offset))
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("query failed: %v", err))
	}

	pbAlerts := make([]*pb.Alert, len(alerts))
	for i, a := range alerts {
		pbAlerts[i] = alertToProto(a)
	}

	return &pb.GetActiveAlertsResponse{
		Alerts:         pbAlerts,
		TotalCount:     int32(total),
		TriggeredCount: int32(len(pbAlerts)),
	}, nil
}

// QueryAlertHistory retrieves historical alerts.
func (s *AlertService) QueryAlertHistory(req *pb.QueryAlertHistoryRequest, stream pb.AlertService_QueryAlertHistoryServer) error {
	if req.DeviceId == "" {
		return status.Error(codes.InvalidArgument, "device_id required")
	}

	limit := int(req.Limit)
	if limit == 0 || limit > 1000 {
		limit = 100
	}

	alerts, err := s.db.QueryAlertHistory(
		stream.Context(), req.DeviceId, req.MetricName,
		req.StartTime.AsTime(), req.EndTime.AsTime(), limit,
	)
	if err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("query failed: %v", err))
	}

	for _, a := range alerts {
		if err := stream.Send(&pb.QueryAlertHistoryResponse{
			Alerts:  []*pb.Alert{alertToProto(a)},
			HasMore: false,
		}); err != nil {
			return err
		}
	}

	return nil
}

// SilenceAlert silences alerts for a device.
func (s *AlertService) SilenceAlert(ctx context.Context, req *pb.SilenceAlertRequest) (*pb.SilenceAlertResponse, error) {
	if req.DeviceId == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id required")
	}

	if req.DurationSeconds <= 0 {
		return nil, status.Error(codes.InvalidArgument, "duration_seconds must be positive")
	}

	silenceID, err := s.db.SilenceDevice(ctx, req.DeviceId, req.MetricName, int(req.DurationSeconds), req.Reason)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("silence failed: %v", err))
	}

	// Redis silencing key is a fast-path cache for active silences.
	_ = s.cacheSilence(ctx, req.DeviceId)

	return &pb.SilenceAlertResponse{
		SilenceId:    silenceID,
		SilencedUntil: timestamppb.New(time.Now().Add(time.Duration(req.DurationSeconds) * time.Second)),
	}, nil
}

// GetSilenceState retrieves active silence rules.
func (s *AlertService) GetSilenceState(ctx context.Context, req *pb.GetSilenceStateRequest) (*pb.GetSilenceStateResponse, error) {
	if req.DeviceId == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id required")
	}

	rules, err := s.db.GetActiveSilences(ctx, req.DeviceId)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("query failed: %v", err))
	}

	pbRules := make([]*pb.SilenceRule, len(rules))
	deviceFullySilenced := false

	for i, r := range rules {
		metricName := ""
		if r.MetricName != nil {
			metricName = *r.MetricName
		}
		pbRules[i] = &pb.SilenceRule{
			SilenceId:    r.SilenceID,
			MetricName:   metricName,
			SilencedUntil: timestamppb.New(r.SilencedUntil),
			Reason:       r.Reason,
		}
		if r.MetricName == nil {
			deviceFullySilenced = true
		}
	}

	return &pb.GetSilenceStateResponse{
		ActiveRules:        pbRules,
		DeviceFullySilenced: deviceFullySilenced,
	}, nil
}

// ClearSilence removes all silence rules for a device.
func (s *AlertService) ClearSilence(ctx context.Context, req *pb.ClearSilenceRequest) (*emptypb.Empty, error) {
	if req.DeviceId == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id required")
	}

	if err := s.db.ClearSilences(ctx, req.DeviceId); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("clear failed: %v", err))
	}

	return &emptypb.Empty{}, nil
}

// GetAlertStats retrieves alert statistics.
func (s *AlertService) GetAlertStats(ctx context.Context, req *pb.GetAlertStatsRequest) (*pb.GetAlertStatsResponse, error) {
	if req.DeviceId == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id required")
	}

	stats, err := s.db.GetAlertStats(ctx, req.DeviceId, req.StartTime.AsTime(), req.EndTime.AsTime())
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("stats query failed: %v", err))
	}

	resp := &pb.GetAlertStatsResponse{
		TotalAlerts: int32(stats["total_alerts"].(int64)),
		ActiveAlerts: int32(stats["active_alerts"].(int64)),
	}

	if bySeverity, ok := stats["by_severity"].(map[string]int64); ok {
		for severity, count := range bySeverity {
			resp.BySeverity = append(resp.BySeverity, &pb.AlertStatsPoint{
				Severity: pb.AlertSeverity(pb.AlertSeverity_value[severity]),
				Count:    int32(count),
			})
		}
	}

	return resp, nil
}

// EvaluateMetric checks if a metric triggers any thresholds and fires alerts.
func (s *AlertService) EvaluateMetric(ctx context.Context, deviceID, metricName string, value float64) error {
	thresholds, err := s.db.GetThresholds(ctx, deviceID, metricName, true)
	if err != nil {
		return err
	}

	for _, t := range thresholds {
		triggered := false
		message := ""

		switch t.Type {
		case "HIGH":
			if value > t.UpperBound {
				triggered = true
				message = fmt.Sprintf("%s > %.2f (HIGH threshold)", metricName, t.UpperBound)
			}
		case "LOW":
			if value < t.LowerBound {
				triggered = true
				message = fmt.Sprintf("%s < %.2f (LOW threshold)", metricName, t.LowerBound)
			}
		case "RANGE":
			if value < t.LowerBound || value > t.UpperBound {
				triggered = true
				message = fmt.Sprintf("%s out of range [%.2f, %.2f]", metricName, t.LowerBound, t.UpperBound)
			}
		}

		if !triggered {
			continue
		}

		// Check if silenced
		silences, _ := s.db.GetActiveSilences(ctx, deviceID)
		isSilenced := s.isSilencedInRedis(ctx, deviceID)
		for _, s := range silences {
			if s.MetricName == nil || *s.MetricName == metricName {
				isSilenced = true
				break
			}
		}

		alert := db.Alert{
			AlertID:       fmt.Sprintf("alr_%d", time.Now().UnixNano()),
			DeviceID:      deviceID,
			MetricName:    metricName,
			MetricValue:   value,
			ThresholdType: t.Type,
			ThresholdValue: math.Max(t.UpperBound, t.LowerBound),
			Severity:      t.Severity,
			Message:       message,
			IsSilenced:    isSilenced,
			TriggeredAt:   time.Now().UTC(),
		}

		if err := s.db.InsertAlert(ctx, alert); err != nil {
			return fmt.Errorf("failed to insert alert: %w", err)
		}
	}

	return nil
}

// Helper functions

func validateThresholdRequest(req *pb.CreateThresholdRequest) error {
	if req.DeviceId == "" {
		return fmt.Errorf("device_id required")
	}
	if req.MetricName == "" {
		return fmt.Errorf("metric_name required")
	}
	if req.Type == pb.ThresholdType_THRESHOLD_TYPE_UNSPECIFIED {
		return fmt.Errorf("type required")
	}

	switch req.Type {
	case pb.ThresholdType_HIGH:
		if math.IsNaN(req.UpperBound) || math.IsInf(req.UpperBound, 0) {
			return fmt.Errorf("upper_bound must be valid number")
		}
	case pb.ThresholdType_LOW:
		if math.IsNaN(req.LowerBound) || math.IsInf(req.LowerBound, 0) {
			return fmt.Errorf("lower_bound must be valid number")
		}
	case pb.ThresholdType_RANGE:
		if math.IsNaN(req.LowerBound) || math.IsInf(req.LowerBound, 0) || 
		   math.IsNaN(req.UpperBound) || math.IsInf(req.UpperBound, 0) {
			return fmt.Errorf("bounds must be valid numbers")
		}
		if req.LowerBound >= req.UpperBound {
			return fmt.Errorf("lower_bound must be < upper_bound")
		}
	case pb.ThresholdType_CHANGE:
		if req.ChangePercent <= 0 || req.ChangePercent >= 100 {
			return fmt.Errorf("change_percent must be between 0 and 100")
		}
	}

	return nil
}

func alertToProto(a db.Alert) *pb.Alert {
	pbAlert := &pb.Alert{
		AlertId:        a.AlertID,
		DeviceId:       a.DeviceID,
		MetricName:     a.MetricName,
		MetricValue:    a.MetricValue,
		ThresholdType:  pb.ThresholdType(pb.ThresholdType_value[a.ThresholdType]),
		ThresholdValue: a.ThresholdValue,
		Severity:       pb.AlertSeverity(pb.AlertSeverity_value[a.Severity]),
		Message:        a.Message,
		IsSilenced:     a.IsSilenced,
		TriggeredAt:    timestamppb.New(a.TriggeredAt),
	}

	if a.ResolvedAt != nil {
		pbAlert.ResolvedAt = timestamppb.New(*a.ResolvedAt)
	}

	return pbAlert
}

func (s *AlertService) cacheSilence(ctx context.Context, deviceID string) error {
	if s.redisClient == nil {
		return nil
	}
	return s.redisClient.Set(ctx, fmt.Sprintf("alert:%s", deviceID), "1", defaultSilenceTTL).Err()
}

func (s *AlertService) isSilencedInRedis(ctx context.Context, deviceID string) bool {
	if s.redisClient == nil {
		return false
	}
	_, err := s.redisClient.Get(ctx, fmt.Sprintf("alert:%s", deviceID)).Result()
	return err == nil
}

// IsDeviceSilent checks whether device has not reported metrics recently.
func (s *AlertService) IsDeviceSilent(ctx context.Context, deviceID string) (bool, error) {
	if s.redisClient == nil {
		return false, nil
	}

	value, err := s.redisClient.Get(ctx, fmt.Sprintf("last_seen:%s", deviceID)).Result()
	if err == redis.Nil {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	lastSeenUnix, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return false, err
	}

	lastSeen := time.Unix(lastSeenUnix, 0).UTC()
	return time.Since(lastSeen) > defaultLastSeenMaxAge, nil
}
