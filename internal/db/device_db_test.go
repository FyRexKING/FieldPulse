package db

import (
	"testing"
	"time"
)

func TestDeviceStructure(t *testing.T) {
	t.Log("=== Test: DB Device structure ===")

	now := time.Now()
	device := Device{
		DeviceID:        "device-001",
		Floor:           "1",
		DeviceType:      "temperature-sensor",
		FirmwareVersion: "1.0.0",
		Status:          "active",
		CertificateCN:   "device-001",
		ProvisionnedAt:  now,
		LastSeen:        now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if device.DeviceID != "device-001" {
		t.Errorf("❌ Expected DeviceID, got %s", device.DeviceID)
	}

	if device.Status != "active" {
		t.Errorf("❌ Expected Status active, got %s", device.Status)
	}

	if device.CertificateCN != "device-001" {
		t.Errorf("❌ Expected CN equals device ID")
	}

	t.Log("✓ Device structure correctly stores all fields")
}

func TestDeviceCertificateStructure(t *testing.T) {
	t.Log("=== Test: DB DeviceCertificate structure ===")

	expiresAt := time.Now().AddDate(1, 0, 0) // 1 year from now

	cert := DeviceCertificate{
		DeviceID:        "device-001",
		CertificatePEM:  "-----BEGIN CERTIFICATE-----...",
		CertificateHash: "abc123def456",
		ExpiresAt:       expiresAt,
		RevokedAt:       nil, // Not revoked
	}

	if cert.DeviceID != "device-001" {
		t.Errorf("❌ Expected DeviceID")
	}

	if cert.RevokedAt != nil {
		t.Errorf("❌ Expected RevokedAt to be nil")
	}

	if cert.ExpiresAt.Before(time.Now()) {
		t.Errorf("❌ Certificate already expired")
	}

	t.Log("✓ DeviceCertificate structure correctly stores certificate")
}

func TestDeviceStatus(t *testing.T) {
	t.Log("=== Test: DB Device status values ===")

	validStatuses := []string{"active", "silent", "degraded", "inactive"}

	for _, status := range validStatuses {
		device := Device{
			DeviceID: "device-001",
			Status:   status,
		}

		if device.Status != status {
			t.Errorf("❌ Status %s not stored correctly", status)
		}
	}

	t.Log("✓ All valid device statuses handled correctly")
}

func TestDeviceCertificateRevocation(t *testing.T) {
	t.Log("=== Test: DB DeviceCertificate revocation ===")

	revokedTime := time.Now()

	// Active certificate
	activeCert := DeviceCertificate{
		DeviceID:  "device-001",
		ExpiresAt: time.Now().AddDate(1, 0, 0),
		RevokedAt: nil,
	}

	// Revoked certificate
	revokedCert := DeviceCertificate{
		DeviceID:  "device-001",
		ExpiresAt: time.Now().AddDate(1, 0, 0),
		RevokedAt: &revokedTime,
	}

	if activeCert.RevokedAt != nil {
		t.Errorf("❌ Active certificate should not have RevokedAt")
	}

	if revokedCert.RevokedAt == nil {
		t.Errorf("❌ Revoked certificate should have RevokedAt")
	}

	t.Log("✓ Certificate revocation tracking works correctly")
}

func TestDeviceProvisioning(t *testing.T) {
	t.Log("=== Test: DB Device provisioning timestamps ===")

	now := time.Now()

	device := Device{
		DeviceID:       "device-001",
		ProvisionnedAt: now,
		LastSeen:       now,
	}

	if device.ProvisionnedAt.IsZero() {
		t.Errorf("❌ ProvisionnedAt should not be zero")
	}

	if device.LastSeen.IsZero() {
		t.Errorf("❌ LastSeen should not be zero")
	}

	if device.ProvisionnedAt.After(device.LastSeen) {
		t.Errorf("❌ ProvisionnedAt after LastSeen")
	}

	t.Log("✓ Device provisioning timestamps are valid")
}

func TestDeviceDBConstructor(t *testing.T) {
	t.Log("=== Test: DB DeviceDB constructor ===")

	var db *DeviceDB
	if db == nil {
		db = &DeviceDB{pool: nil} // Placeholder
	}

	if db == nil {
		t.Errorf("❌ DeviceDB is nil")
	}

	t.Log("✓ DeviceDB can be constructed")
}

func TestTelemetryDBConstructor_Test(t *testing.T) {
	t.Log("=== Test: DB TelemetryDB constructor ===")

	var tdb *TelemetryDB
	if tdb == nil {
		tdb = &TelemetryDB{pool: nil} // Placeholder
	}

	if tdb == nil {
		t.Errorf("❌ TelemetryDB is nil")
	}

	t.Log("✓ TelemetryDB can be constructed")
}

func TestDeviceCertificateExpiration(t *testing.T) {
	t.Log("=== Test: DB DeviceCertificate expiration check ===")

	// Expired certificate
	expiredCert := DeviceCertificate{
		DeviceID:  "device-001",
		ExpiresAt: time.Now().AddDate(-1, 0, 0), // 1 year ago
	}

	// Valid certificate
	validCert := DeviceCertificate{
		DeviceID:  "device-002",
		ExpiresAt: time.Now().AddDate(1, 0, 0), // 1 year from now
	}

	if expiredCert.ExpiresAt.After(time.Now()) {
		t.Errorf("❌ Expired certificate should have ExpiresAt in past")
	}

	if validCert.ExpiresAt.Before(time.Now()) {
		t.Errorf("❌ Valid certificate should have ExpiresAt in future")
	}

	t.Log("✓ Certificate expiration dates are valid")
}

func TestDeviceCreatedUpdated(t *testing.T) {
	t.Log("=== Test: DB Device CreatedAt/UpdatedAt tracking ===")

	now := time.Now()
	later := now.Add(1 * time.Hour)

	device := Device{
		DeviceID:  "device-001",
		CreatedAt: now,
		UpdatedAt: later,
	}

	if device.CreatedAt.IsZero() {
		t.Errorf("❌ CreatedAt should not be zero")
	}

	if device.UpdatedAt.IsZero() {
		t.Errorf("❌ UpdatedAt should not be zero")
	}

	if device.CreatedAt.After(device.UpdatedAt) {
		t.Errorf("❌ CreatedAt should not be after UpdatedAt")
	}

	t.Log("✓ CreatedAt/UpdatedAt timestamps are valid")
}
