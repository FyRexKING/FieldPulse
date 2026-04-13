package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"database/sql"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/cache"
	"fieldpulse.io/internal/certs"
	"fieldpulse.io/internal/db"
	oteltracing "fieldpulse.io/internal/otel"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	TracerName = "device-service"
)

// DeviceService implements the gRPC DeviceService
type DeviceService struct {
	pb.UnimplementedDeviceServiceServer

	// Database access layer (PostgreSQL as source of truth)
	deviceDB *db.DeviceDB

	// Cache layer (Redis - not authoritative)
	cache *cache.DeviceCache

	// Certificate generation and signing
	keyGen *certs.KeyGenerator

	// Platform RootCA certificate (loaded at startup)
	rootCert *x509.Certificate

	// Platform RootCA private key (loaded at startup)
	rootKey *ecdsa.PrivateKey

	// Root CA certificate PEM (returned to devices for mTLS verification)
	rootCertPEM []byte

	// Telemetry tracer
	tracer interface{} // trace.Tracer
}

// NewDeviceService creates a new Device Service with injected dependencies
func NewDeviceService(
	deviceDB *db.DeviceDB,
	redisClient *redis.Client,
	rootCert *x509.Certificate,
	rootKey *ecdsa.PrivateKey,
) *DeviceService {
	tracer := otel.Tracer(TracerName)

	// Load RootCA certificate PEM
	rootCertPath := os.Getenv("ROOTCA_CERT_PATH")
	if rootCertPath == "" {
		rootCertPath = "certs/ca.crt"
	}

	rootCertPEM, err := ioutil.ReadFile(rootCertPath)
	if err != nil {
		log.Printf("Warning: Failed to load RootCA PEM: %v (will not be returned in CreateDevice)", err)
	}

	return &DeviceService{
		deviceDB:    deviceDB,
		cache:       cache.NewDeviceCache(redisClient),
		keyGen:      certs.NewKeyGenerator(),
		rootCert:    rootCert,
		rootKey:     rootKey,
		rootCertPEM: rootCertPEM,
		tracer:      tracer,
	}
}

// CreateDevice provisions a new device with mTLS credentials
// DESIGN: Backend-provisioned certificates
//
// Strong consistency: Device record written to PostgreSQL first,
// cache updated asynchronously. Private key returned once only.
func (s *DeviceService) CreateDevice(ctx context.Context, req *pb.CreateDeviceRequest) (*pb.CreateDeviceResponse, error) {
	newCtx, endSpan := oteltracing.TraceDeviceCreate(ctx, req.DeviceId)
	defer endSpan()

	// Input validation
	if req.DeviceId == "" {
		return nil, fmt.Errorf("device_id is required")
	}
	if req.Floor == "" {
		return nil, fmt.Errorf("floor is required")
	}
	if req.DeviceType == "" {
		return nil, fmt.Errorf("device_type is required")
	}

	// Check if device already exists
	exists, err := s.deviceDB.DeviceExists(newCtx, req.DeviceId)
	if err != nil {
		return nil, fmt.Errorf("failed to check device existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("device already exists: %s", req.DeviceId)
	}

	// Generate device keypair (ECDSA P-256)
	devicePrivateKey, err := s.keyGen.GenerateDeviceKeypair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate device keypair: %w", err)
	}

	// Sign device certificate with platform RootCA
	certBundle, err := s.keyGen.SignDeviceCertificate(
		req.DeviceId,
		&devicePrivateKey.PublicKey,
		s.rootCert,
		s.rootKey,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign device certificate: %w", err)
	}

	// Encode device private key to PEM
	keyPEM, err := certs.EncodePrivateKeyPEM(devicePrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encode device key: %w", err)
	}

	// Prepare database records
	now := time.Now()
	deviceRecord := &db.Device{
		DeviceID:        req.DeviceId,
		Floor:           req.Floor,
		DeviceType:      req.DeviceType,
		FirmwareVersion: req.FirmwareVersion,
		Status:          "active",
		CertificateCN:   req.DeviceId, // CN in certificate matches device_id
		ProvisionnedAt:  now,
		LastSeen:        now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	certRecord := &db.DeviceCertificate{
		DeviceID:        req.DeviceId,
		CertificatePEM:  string(certBundle.ClientCertPEM),
		CertificateHash: certBundle.ClientCertHash,
		ExpiresAt:       certBundle.CertificateExpiry,
	}

	// Store in PostgreSQL (source of truth)
	err = s.deviceDB.CreateDevice(newCtx, deviceRecord, certRecord)
	if err != nil {
		return nil, fmt.Errorf("failed to create device in database: %w", err)
	}

	// Async: Warm Redis cache (non-blocking, do not wait for result)
	go s.cacheDevice(context.Background(), deviceRecord)

	// Return provisioning response
	// CRITICAL: Return private key only once; device must securely store it
	return &pb.CreateDeviceResponse{
		DeviceId:      req.DeviceId,
		BrokerUrl:     "mqtls://broker:8883", // Default to internal mTLS broker
		ClientCertPem: certBundle.ClientCertPEM,
		ClientKeyPem:  keyPEM, // NEVER STORED: device must save securely
		CaPem:         s.rootCertPEM,
		ProvisionedAt: timestamppb.New(now),
	}, nil
}

// GetDevice retrieves device details with strong consistency
// DESIGN: PostgreSQL is source of truth
//
// Read path: Query PostgreSQL first (authoritative),
// then async warm Redis for future reads (non-blocking).
// If cache hit on subsequent request, serve from cache while
// async refreshing PostgreSQL value.
func (s *DeviceService) GetDevice(ctx context.Context, req *pb.GetDeviceRequest) (*pb.GetDeviceResponse, error) {
	if req.DeviceId == "" {
		return nil, fmt.Errorf("device_id is required")
	}

	// Try Redis cache first (stale value is acceptable during async refresh)
	cachedDevice, err := s.getDeviceFromCache(ctx, req.DeviceId)
	if err == nil && cachedDevice != nil {
		// Cache hit, but still async refresh in background
		go s.refreshDeviceInCache(context.Background(), req.DeviceId)
		return s.deviceToResponse(cachedDevice)
	}

	// Cache miss or error: query PostgreSQL (source of truth)
	device, err := s.deviceDB.GetDevice(ctx, req.DeviceId)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("device not found: %s", req.DeviceId)
		}
		return nil, fmt.Errorf("failed to get device: %w", err)
	}

	// Async: update cache for next read
	go s.cacheDevice(context.Background(), device)

	// Get certificate metadata
	cert, err := s.deviceDB.GetDeviceCertificate(ctx, req.DeviceId)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get device certificate: %w", err)
	}

	return &pb.GetDeviceResponse{
		Device: &pb.Device{
			DeviceId:        device.DeviceID,
			Floor:           device.Floor,
			DeviceType:      device.DeviceType,
			FirmwareVersion: device.FirmwareVersion,
			Status:          pb.DeviceStatus(pb.DeviceStatus_value[device.Status]),
			CertificateCn:   device.CertificateCN,
			ProvisionedAt:   timestamppb.New(device.ProvisionnedAt),
			LastSeen:        timestamppb.New(device.LastSeen),
			CreatedAt:       timestamppb.New(device.CreatedAt),
			UpdatedAt:       timestamppb.New(device.UpdatedAt),
		},
		CertificateHash:    cert.CertificateHash,
		CertificateExpiresAt: timestamppb.New(cert.ExpiresAt),
	}, nil
}

// ListDevices retrieves devices with filtering and pagination
// DESIGN: No caching (query is dynamic based on filters)
// Strong consistency: All reads from PostgreSQL
func (s *DeviceService) ListDevices(ctx context.Context, req *pb.ListDevicesRequest) (*pb.ListDevicesResponse, error) {
	// Validate pagination
	if req.Limit == 0 {
		req.Limit = 50
	}
	if req.Limit > 1000 {
		req.Limit = 1000 // Enforce max limit
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	// Query database with filters
	devices, totalCount, err := s.deviceDB.ListDevices(
		ctx,
		req.FilterFloor,
		string(req.FilterStatus),  // Convert enum to string
		int(req.Limit),
		int(req.Offset),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}

	// Convert to protobuf
	pbDevices := make([]*pb.Device, len(devices))
	for i, device := range devices {
		pbDevices[i] = &pb.Device{
			DeviceId:        device.DeviceID,
			Floor:           device.Floor,
			DeviceType:      device.DeviceType,
			FirmwareVersion: device.FirmwareVersion,
			Status:          pb.DeviceStatus(pb.DeviceStatus_value[device.Status]),
			CertificateCn:   device.CertificateCN,
			ProvisionedAt:   timestamppb.New(device.ProvisionnedAt),
			LastSeen:        timestamppb.New(device.LastSeen),
			CreatedAt:       timestamppb.New(device.CreatedAt),
			UpdatedAt:       timestamppb.New(device.UpdatedAt),
		}
	}

	return &pb.ListDevicesResponse{
		Devices:    pbDevices,
		TotalCount: totalCount, // Keep as int64 (proto defines as int64)
	}, nil
}

// UpdateDeviceStatus updates a device's operational status
// DESIGN: Strong consistency with immediate cache invalidation
//
// Write pattern:
// 1. Update PostgreSQL (source of truth)
// 2. Synchronously invalidate Redis cache
// 3. Return updated device (from PostgreSQL, not cache)
func (s *DeviceService) UpdateDeviceStatus(ctx context.Context, req *pb.UpdateDeviceStatusRequest) (*pb.UpdateDeviceStatusResponse, error) {
	if req.DeviceId == "" {
		return nil, fmt.Errorf("device_id is required")
	}

	// Update status in PostgreSQL
	device, err := s.deviceDB.UpdateDeviceStatus(ctx, req.DeviceId, string(req.NewStatus))
	if err != nil {
		return nil, fmt.Errorf("failed to update device status: %w", err)
	}

	// Synchronous cache invalidation (BEFORE returning)
	s.invalidateDeviceCache(ctx, req.DeviceId)

	// TODO: Publish MQTT event to subscribers (devices/device_id/status_change)

	return &pb.UpdateDeviceStatusResponse{
		Device: &pb.Device{
			DeviceId:        device.DeviceID,
			Floor:           device.Floor,
			DeviceType:      device.DeviceType,
			FirmwareVersion: device.FirmwareVersion,
			Status:          pb.DeviceStatus(pb.DeviceStatus_value[device.Status]),
			CertificateCn:   device.CertificateCN,
			ProvisionedAt:   timestamppb.New(device.ProvisionnedAt),
			LastSeen:        timestamppb.New(device.LastSeen),
			CreatedAt:       timestamppb.New(device.CreatedAt),
			UpdatedAt:       timestamppb.New(device.UpdatedAt),
		},
		StatusChangedAt: timestamppb.New(device.UpdatedAt),
	}, nil
}

// Helper functions for cache operations

// getDeviceFromCache attempts to retrieve device from Redis
func (s *DeviceService) getDeviceFromCache(ctx context.Context, deviceID string) (*db.Device, error) {
	return s.cache.Get(ctx, deviceID)
}

// cacheDevice stores a device in Redis with 5-minute TTL
func (s *DeviceService) cacheDevice(ctx context.Context, device *db.Device) error {
	if device == nil {
		return fmt.Errorf("cannot cache nil device")
	}
	return s.cache.Set(ctx, device)
}

// refreshDeviceInCache updates a device in Redis from PostgreSQL
func (s *DeviceService) refreshDeviceInCache(ctx context.Context, deviceID string) error {
	device, err := s.deviceDB.GetDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	return s.cacheDevice(ctx, device)
}

// invalidateDeviceCache removes a device from Redis
func (s *DeviceService) invalidateDeviceCache(ctx context.Context, deviceID string) error {
	return s.cache.Delete(ctx, deviceID)
}

// deviceToResponse converts internal Device to protobuf response
func (s *DeviceService) deviceToResponse(device *db.Device) (*pb.GetDeviceResponse, error) {
	// Convert status string to enum (database stores lowercase, proto enum is uppercase)
	statusMap := map[string]pb.DeviceStatus{
		"active":    pb.DeviceStatus_DEVICE_STATUS_ACTIVE,
		"silent":    pb.DeviceStatus_DEVICE_STATUS_SILENT,
		"degraded":  pb.DeviceStatus_DEVICE_STATUS_DEGRADED,
		"inactive":  pb.DeviceStatus_DEVICE_STATUS_INACTIVE,
	}
	
	status := pb.DeviceStatus_DEVICE_STATUS_UNSPECIFIED // Default
	if s, ok := statusMap[device.Status]; ok {
		status = s
	}

	return &pb.GetDeviceResponse{
		Device: &pb.Device{
			DeviceId:        device.DeviceID,
			Floor:           device.Floor,
			DeviceType:      device.DeviceType,
			FirmwareVersion: device.FirmwareVersion,
			Status:          status,
			CertificateCn:   device.CertificateCN,
			ProvisionedAt:   timestamppb.New(device.ProvisionnedAt),
			LastSeen:        timestamppb.New(device.LastSeen),
			CreatedAt:       timestamppb.New(device.CreatedAt),
			UpdatedAt:       timestamppb.New(device.UpdatedAt),
		},
	}, nil
}
