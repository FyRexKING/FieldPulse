package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DeviceDB handles all database operations for devices
type DeviceDB struct {
	pool *pgxpool.Pool
}

// NewDeviceDB creates a new device database handler
func NewDeviceDB(pool *pgxpool.Pool) *DeviceDB {
	return &DeviceDB{pool: pool}
}

// Device represents a device record in the database
type Device struct {
	DeviceID          string
	Floor             string
	DeviceType        string
	FirmwareVersion   string
	Status            string // enum: active, silent, degraded, inactive
	CertificateCN     string
	ProvisionnedAt    time.Time
	LastSeen          time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// DeviceCertificate represents a device certificate record
type DeviceCertificate struct {
	DeviceID       string
	CertificatePEM string
	CertificateHash string
	ExpiresAt      time.Time
	RevokedAt      *time.Time // NULL if not revoked
}

// CreateDevice inserts a new device record into the database
// Returns error if device_id already exists (unique constraint)
func (db *DeviceDB) CreateDevice(ctx context.Context, device *Device, cert *DeviceCertificate) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Insert device record
	err = tx.QueryRow(ctx, `
		INSERT INTO devices (device_id, floor, device_type, firmware_version, status, certificate_cn, provisioned_at, last_seen, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING created_at, updated_at
	`,
		device.DeviceID,
		device.Floor,
		device.DeviceType,
		device.FirmwareVersion,
		device.Status,
		device.CertificateCN,
		device.ProvisionnedAt,
		device.LastSeen,
		device.CreatedAt,
		device.UpdatedAt,
	).Scan(&device.CreatedAt, &device.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert device: %w", err)
	}

	// Insert certificate record
	_, err = tx.Exec(ctx, `
		INSERT INTO device_certificates (device_id, certificate_pem, certificate_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`,
		cert.DeviceID,
		cert.CertificatePEM,
		cert.CertificateHash,
		cert.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert certificate: %w", err)
	}

	// Commit transaction
	err = tx.Commit(ctx)
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetDevice retrieves a device by ID from the database
// Returns sql.ErrNoRows if device not found
func (db *DeviceDB) GetDevice(ctx context.Context, deviceID string) (*Device, error) {
	device := &Device{}

	err := db.pool.QueryRow(ctx, `
		SELECT device_id, floor, device_type, firmware_version, status, certificate_cn, provisioned_at, last_seen, created_at, updated_at
		FROM devices
		WHERE device_id = $1
	`, deviceID).Scan(
		&device.DeviceID,
		&device.Floor,
		&device.DeviceType,
		&device.FirmwareVersion,
		&device.Status,
		&device.CertificateCN,
		&device.ProvisionnedAt,
		&device.LastSeen,
		&device.CreatedAt,
		&device.UpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("failed to query device: %w", err)
	}

	return device, nil
}

// GetDeviceCertificate retrieves the active certificate for a device
// Returns sql.ErrNoRows if no active certificate found
func (db *DeviceDB) GetDeviceCertificate(ctx context.Context, deviceID string) (*DeviceCertificate, error) {
	cert := &DeviceCertificate{}

	err := db.pool.QueryRow(ctx, `
		SELECT device_id, certificate_pem, certificate_hash, expires_at, revoked_at
		FROM device_certificates
		WHERE device_id = $1 AND revoked_at IS NULL
		ORDER BY expires_at DESC
		LIMIT 1
	`, deviceID).Scan(
		&cert.DeviceID,
		&cert.CertificatePEM,
		&cert.CertificateHash,
		&cert.ExpiresAt,
		&cert.RevokedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("failed to query device certificate: %w", err)
	}

	return cert, nil
}

// ListDevices retrieves devices with optional filtering and pagination
// floor and status can be empty string to skip filtering
func (db *DeviceDB) ListDevices(ctx context.Context, floor, status string, limit, offset int) ([]*Device, int64, error) {
	// Build query with optional filters
	query := `SELECT device_id, floor, device_type, firmware_version, status, certificate_cn, provisioned_at, last_seen, created_at, updated_at FROM devices WHERE 1=1`
	countQuery := `SELECT COUNT(*) FROM devices WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if floor != "" {
		query += fmt.Sprintf(` AND floor = $%d`, argIdx)
		countQuery += fmt.Sprintf(` AND floor = $%d`, argIdx)
		args = append(args, floor)
		argIdx++
	}

	if status != "" {
		query += fmt.Sprintf(` AND status = $%d`, argIdx)
		countQuery += fmt.Sprintf(` AND status = $%d`, argIdx)
		args = append(args, status)
		argIdx++
	}

	// Count total results
	var totalCount int64
	countArgs := args
	err := db.pool.QueryRow(ctx, countQuery, countArgs...).Scan(&totalCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count devices: %w", err)
	}

	// Add pagination
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query devices: %w", err)
	}
	defer rows.Close()

	devices := []*Device{}
	for rows.Next() {
		device := &Device{}
		err := rows.Scan(
			&device.DeviceID,
			&device.Floor,
			&device.DeviceType,
			&device.FirmwareVersion,
			&device.Status,
			&device.CertificateCN,
			&device.ProvisionnedAt,
			&device.LastSeen,
			&device.CreatedAt,
			&device.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan device row: %w", err)
		}
		devices = append(devices, device)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("error iterating device rows: %w", err)
	}

	return devices, totalCount, nil
}

// UpdateDeviceStatus updates device status in the database
// strong consistency read: caller must invalidate Redis cache BEFORE this operation completes
func (db *DeviceDB) UpdateDeviceStatus(ctx context.Context, deviceID, newStatus string) (*Device, error) {
	device := &Device{}

	err := db.pool.QueryRow(ctx, `
		UPDATE devices
		SET status = $1, updated_at = NOW()
		WHERE device_id = $2
		RETURNING device_id, floor, device_type, firmware_version, status, certificate_cn, provisioned_at, last_seen, created_at, updated_at
	`, newStatus, deviceID).Scan(
		&device.DeviceID,
		&device.Floor,
		&device.DeviceType,
		&device.FirmwareVersion,
		&device.Status,
		&device.CertificateCN,
		&device.ProvisionnedAt,
		&device.LastSeen,
		&device.CreatedAt,
		&device.UpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("device not found: %s", deviceID)
		}
		return nil, fmt.Errorf("failed to update device status: %w", err)
	}

	return device, nil
}

// UpdateDeviceLastSeen updates the last_seen timestamp for a device
// Called when device publishes telemetry or connects
func (db *DeviceDB) UpdateDeviceLastSeen(ctx context.Context, deviceID string) error {
	result, err := db.pool.Exec(ctx, `
		UPDATE devices
		SET last_seen = NOW(), updated_at = NOW()
		WHERE device_id = $1
	`, deviceID)

	if err != nil {
		return fmt.Errorf("failed to update device last_seen: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("device not found: %s", deviceID)
	}

	return nil
}

// DeviceExists checks if a device exists in the database
func (db *DeviceDB) DeviceExists(ctx context.Context, deviceID string) (bool, error) {
	var exists bool

	err := db.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM devices WHERE device_id = $1)
	`, deviceID).Scan(&exists)

	if err != nil {
		return false, fmt.Errorf("failed to check device existence: %w", err)
	}

	return exists, nil
}
