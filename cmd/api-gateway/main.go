package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	pb "fieldpulse.io/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// APIGateway holds all gRPC client connections
type APIGateway struct {
	app            *fiber.App
	deviceConn     pb.DeviceServiceClient
	telemetryConn  pb.TelemetryServiceClient
	alertConn      pb.AlertServiceClient
	ctx            context.Context
}

// NewAPIGateway creates a new API gateway
func NewAPIGateway() (*APIGateway, error) {
	app := fiber.New(fiber.Config{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	})

	// Middleware
	app.Use(recover.New())
	app.Use(logger.New())
	app.Use(cors.New())

	deviceServiceAddr := getEnv("DEVICE_SERVICE_ADDR", "device-service:50051")
	telemetryServiceAddr := getEnv("TELEMETRY_SERVICE_ADDR", "telemetry-service:50052")
	alertServiceAddr := getEnv("ALERT_SERVICE_ADDR", "alert-service:50053")

	// Connect to services
	deviceConn, err := grpc.Dial(deviceServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to device service: %w", err)
	}

	telemetryConn, err := grpc.Dial(telemetryServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to telemetry service: %w", err)
	}

	alertConn, err := grpc.Dial(alertServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to alert service: %w", err)
	}

	gateway := &APIGateway{
		app:           app,
		deviceConn:    pb.NewDeviceServiceClient(deviceConn),
		telemetryConn: pb.NewTelemetryServiceClient(telemetryConn),
		alertConn:     pb.NewAlertServiceClient(alertConn),
		ctx:           context.Background(),
	}

	gateway.setupRoutes()
	return gateway, nil
}

func (ag *APIGateway) setupRoutes() {
	// Health check
	ag.app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	// Device endpoints
	deviceGroup := ag.app.Group("/api/v1/devices")
	deviceGroup.Post("", ag.createDevice)
	deviceGroup.Get("/:id", ag.getDevice)
	deviceGroup.Get("", ag.listDevices)
	deviceGroup.Put("/:id/status", ag.updateDeviceStatus)

	// Telemetry endpoints
	telemetryGroup := ag.app.Group("/api/v1/telemetry")
	telemetryGroup.Post("/metric", ag.submitMetric)
	telemetryGroup.Get("/query", ag.queryMetrics)

	// Alert endpoints
	alertGroup := ag.app.Group("/api/v1/alerts")
	alertGroup.Get("/active", ag.getActiveAlerts)
	alertGroup.Get("/stats", ag.getAlertStats)
	
	// Alert threshold endpoints
	thresholdGroup := ag.app.Group("/api/v1/thresholds")
	thresholdGroup.Post("", ag.createThreshold)
	thresholdGroup.Get("", ag.getThresholds)
	thresholdGroup.Put("/:id", ag.updateThreshold)
	thresholdGroup.Delete("/:id", ag.deleteThreshold)
	
	// Alert silence endpoints
	silenceGroup := ag.app.Group("/api/v1/silence")
	silenceGroup.Post("", ag.silenceAlert)
}

// ========== Device Endpoints ==========

func (ag *APIGateway) createDevice(c *fiber.Ctx) error {
	type CreateDeviceReq struct {
		DeviceID      string `json:"device_id"`
		Floor         string `json:"floor"`
		DeviceType    string `json:"device_type"`
		FirmwareVersion string `json:"firmware_version"`
	}

	var req CreateDeviceReq
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.deviceConn.CreateDevice(ctx, &pb.CreateDeviceRequest{
		DeviceId:       req.DeviceID,
		Floor:          req.Floor,
		DeviceType:     req.DeviceType,
		FirmwareVersion: req.FirmwareVersion,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(201).JSON(resp)
}

func (ag *APIGateway) getDevice(c *fiber.Ctx) error {
	deviceID := c.Params("id")
	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.deviceConn.GetDevice(ctx, &pb.GetDeviceRequest{DeviceId: deviceID})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) listDevices(c *fiber.Ctx) error {
	limit := parseInt(c.Query("limit"), 100)
	offset := parseInt(c.Query("offset"), 0)

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.deviceConn.ListDevices(ctx, &pb.ListDevicesRequest{
		Limit:  int32(limit),
		Offset: int32(offset),
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) updateDeviceStatus(c *fiber.Ctx) error {
	deviceID := c.Params("id")

	type UpdateStatusReq struct {
		Status string `json:"status"`
	}

	var req UpdateStatusReq
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
	}

	// Parse status enum
	status, ok := pb.DeviceStatus_value[req.Status]
	if !ok {
		status = int32(pb.DeviceStatus_DEVICE_STATUS_UNSPECIFIED)
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.deviceConn.UpdateDeviceStatus(ctx, &pb.UpdateDeviceStatusRequest{
		DeviceId:  deviceID,
		NewStatus: pb.DeviceStatus(status),
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

// ========== Telemetry Endpoints ==========

func (ag *APIGateway) submitMetric(c *fiber.Ctx) error {
	type MetricReq struct {
		DeviceID   string  `json:"device_id"`
		MetricName string  `json:"metric_name"`
		Value      float64 `json:"value"`
		Timestamp  int64   `json:"timestamp"`
	}

	var req MetricReq
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.telemetryConn.SubmitMetric(ctx, &pb.SubmitMetricRequest{
		DeviceId:             req.DeviceID,
		MetricName:           req.MetricName,
		Value:                req.Value,
		TimestampUnixSeconds: req.Timestamp,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(201).JSON(resp)
}

func (ag *APIGateway) queryMetrics(c *fiber.Ctx) error {
	deviceID := c.Query("device_id")
	metricName := c.Query("metric_name")
	startTime := parseInt(c.Query("start_time"), 0)
	endTime := parseInt(c.Query("end_time"), 0)
	limit := parseInt(c.Query("limit"), 100)

	if deviceID == "" {
		return c.Status(400).JSON(fiber.Map{"error": "device_id required"})
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 10*time.Second)
	defer cancel()

	resp, err := ag.telemetryConn.QueryMetrics(ctx, &pb.QueryMetricsRequest{
		DeviceId:   deviceID,
		MetricName: metricName,
		StartTime:  timestamppb.New(time.Unix(int64(startTime), 0)),
		EndTime:    timestamppb.New(time.Unix(int64(endTime), 0)),
		Limit:      int32(limit),
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

// ========== Alert Endpoints ==========

func (ag *APIGateway) getActiveAlerts(c *fiber.Ctx) error {
	deviceID := c.Query("device_id", "")
	limit := parseInt(c.Query("limit"), 100)

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.alertConn.GetActiveAlerts(ctx, &pb.GetActiveAlertsRequest{
		DeviceId: deviceID,
		Limit:    int32(limit),
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) getAlertStats(c *fiber.Ctx) error {
	deviceID := c.Query("device_id")
	if deviceID == "" {
		return c.Status(400).JSON(fiber.Map{"error": "device_id required"})
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.alertConn.GetAlertStats(ctx, &pb.GetAlertStatsRequest{
		DeviceId: deviceID,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) createThreshold(c *fiber.Ctx) error {
	type CreateThresholdReq struct {
		DeviceID      string  `json:"device_id"`
		MetricName    string  `json:"metric_name"`
		Type          string  `json:"type"` // HIGH, LOW, RANGE, CHANGE
		LowerBound    float64 `json:"lower_bound"`
		UpperBound    float64 `json:"upper_bound"`
		ChangePercent float64 `json:"change_percent"`
		Severity      string  `json:"severity"`
		Enabled       bool    `json:"enabled"`
	}

	var req CreateThresholdReq
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
	}

	// Parse threshold type
	threshType, ok := pb.ThresholdType_value[req.Type]
	if !ok {
		threshType = int32(pb.ThresholdType_HIGH)
	}

	// Parse severity
	severity, ok := pb.AlertSeverity_value[req.Severity]
	if !ok {
		severity = int32(pb.AlertSeverity_WARNING)
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.alertConn.CreateThreshold(ctx, &pb.CreateThresholdRequest{
		DeviceId:      req.DeviceID,
		MetricName:    req.MetricName,
		Type:          pb.ThresholdType(threshType),
		LowerBound:    req.LowerBound,
		UpperBound:    req.UpperBound,
		ChangePercent: req.ChangePercent,
		Severity:      pb.AlertSeverity(severity),
		Enabled:       req.Enabled,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(201).JSON(resp)
}

func (ag *APIGateway) getThresholds(c *fiber.Ctx) error {
	deviceID := c.Query("device_id")
	metricName := c.Query("metric_name")

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.alertConn.GetThresholds(ctx, &pb.GetThresholdsRequest{
		DeviceId:   deviceID,
		MetricName: metricName,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) updateThreshold(c *fiber.Ctx) error {
	thresholdID := c.Params("id")

	type UpdateThresholdReq struct {
		LowerBound    float64 `json:"lower_bound"`
		UpperBound    float64 `json:"upper_bound"`
		ChangePercent float64 `json:"change_percent"`
		Severity      string  `json:"severity"`
		Enabled       bool    `json:"enabled"`
	}

	var req UpdateThresholdReq
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
	}

	// Parse severity
	severity, ok := pb.AlertSeverity_value[req.Severity]
	if !ok {
		severity = int32(pb.AlertSeverity_WARNING)
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.alertConn.UpdateThreshold(ctx, &pb.UpdateThresholdRequest{
		ThresholdId:   thresholdID,
		LowerBound:    req.LowerBound,
		UpperBound:    req.UpperBound,
		ChangePercent: req.ChangePercent,
		Severity:      pb.AlertSeverity(severity),
		Enabled:       req.Enabled,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) deleteThreshold(c *fiber.Ctx) error {
	thresholdID := c.Params("id")

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	_, err := ag.alertConn.DeleteThreshold(ctx, &pb.DeleteThresholdRequest{ThresholdId: thresholdID})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.SendStatus(204)
}

func (ag *APIGateway) silenceAlert(c *fiber.Ctx) error {
	type SilenceReq struct {
		DeviceID    string `json:"device_id"`
		MetricName  string `json:"metric_name"`
		DurationSec int32  `json:"duration_seconds"`
		Reason      string `json:"reason"`
	}

	var req SilenceReq
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request"})
	}

	ctx, cancel := context.WithTimeout(ag.ctx, 5*time.Second)
	defer cancel()

	resp, err := ag.alertConn.SilenceAlert(ctx, &pb.SilenceAlertRequest{
		DeviceId:        req.DeviceID,
		MetricName:      req.MetricName,
		DurationSeconds: req.DurationSec,
		Reason:          req.Reason,
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(resp)
}

func (ag *APIGateway) Start(port int) error {
	log.Printf("🚀 API Gateway starting on port %d", port)
	return ag.app.Listen(fmt.Sprintf(":%d", port))
}

func parseInt(s string, defaultVal int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

func main() {
	gateway, err := NewAPIGateway()
	if err != nil {
		log.Fatalf("Failed to create API gateway: %v", err)
	}

	if err := gateway.Start(3000); err != nil {
		log.Fatalf("API gateway error: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
