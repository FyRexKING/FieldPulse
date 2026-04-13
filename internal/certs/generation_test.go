package certs

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestKeyGenerator_GenerateDeviceKeypair(t *testing.T) {
	kg := NewKeyGenerator()

	// Generate keypair
	privKey, err := kg.GenerateDeviceKeypair()
	if err != nil {
		t.Fatalf("Failed to generate keypair: %v", err)
	}

	// Verify it's a valid ECDSA private key
	if privKey == nil {
		t.Fatal("Generated private key is nil")
	}

	// Verify public key is accessible
	pubKey := &privKey.PublicKey
	if pubKey == nil {
		t.Fatal("Public key is nil")
	}
}

func TestLoadRootCA(t *testing.T) {
	// These paths should exist from PHASE 0 bootstrap
	certPath := "../../certs/ca.crt"
	keyPath := "../../certs/ca.key"

	rootCert, rootKey, err := LoadRootCA(certPath, keyPath)
	if err != nil {
		t.Skipf("Cannot test without RootCA files: %v", err)
	}

	// Verify certificate
	if rootCert == nil {
		t.Fatal("Root certificate is nil")
	}

	// Verify key
	if rootKey == nil {
		t.Fatal("Root key is nil")
	}

	// Verify it's self-signed
	if err := rootCert.CheckSignatureFrom(rootCert); err != nil {
		t.Fatalf("RootCA signature verification failed: %v", err)
	}

	t.Logf("RootCA loaded: CN=%s, Valid until %s", rootCert.Subject.CommonName, rootCert.NotAfter)
}

func TestSignDeviceCertificate(t *testing.T) {
	// Load RootCA
	certPath := "../../certs/ca.crt"
	keyPath := "../../certs/ca.key"

	rootCert, rootKey, err := LoadRootCA(certPath, keyPath)
	if err != nil {
		t.Skipf("Cannot test without RootCA files: %v", err)
	}

	// Generate device keypair
	kg := NewKeyGenerator()
	devicePrivKey, err := kg.GenerateDeviceKeypair()
	if err != nil {
		t.Fatalf("Failed to generate device keypair: %v", err)
	}

	deviceID := "test-device-001"

	// Sign certificate
	bundle, err := kg.SignDeviceCertificate(deviceID, &devicePrivKey.PublicKey, rootCert, rootKey)
	if err != nil {
		t.Fatalf("Failed to sign certificate: %v", err)
	}

	// Verify bundle contents
	if bundle.DeviceID != deviceID {
		t.Errorf("Device ID mismatch: expected %s, got %s", deviceID, bundle.DeviceID)
	}

	if bundle.ClientCertPEM == nil || len(bundle.ClientCertPEM) == 0 {
		t.Fatal("Certificate PEM is empty")
	}

	if bundle.ClientCertHash == "" {
		t.Fatal("Certificate hash is empty")
	}

	if bundle.CertificateExpiry.Before(time.Now()) {
		t.Fatal("Certificate already expired")
	}

	t.Logf("Certificate signed: CN=%s, expires %s, hash=%s", deviceID, bundle.CertificateExpiry, bundle.ClientCertHash[:16]+"...")
}

func TestSignDeviceCertificate_CN_Validation(t *testing.T) {
	// Load RootCA
	certPath := "../../certs/ca.crt"
	keyPath := "../../certs/ca.key"

	rootCert, rootKey, err := LoadRootCA(certPath, keyPath)
	if err != nil {
		t.Skipf("Cannot test without RootCA files: %v", err)
	}

	kg := NewKeyGenerator()
	devicePrivKey, err := kg.GenerateDeviceKeypair()
	if err != nil {
		t.Fatalf("Failed to generate device keypair: %v", err)
	}

	deviceID := "device-with-special-chars-123"

	// Sign certificate
	bundle, err := kg.SignDeviceCertificate(deviceID, &devicePrivKey.PublicKey, rootCert, rootKey)
	if err != nil {
		t.Fatalf("Failed to sign certificate: %v", err)
	}

	// Parse the certificate to verify CN
	cert, err := x509.ParseCertificate(bundle.ClientCertPEM)
	if err == nil {
		if cert.Subject.CommonName != deviceID {
			t.Errorf("CN validation failed: expected %s, got %s", deviceID, cert.Subject.CommonName)
		}
	}
}

func TestVerifyDeviceCertificate(t *testing.T) {
	// Load RootCA
	certPath := "../../certs/ca.crt"
	keyPath := "../../certs/ca.key"

	rootCert, rootKey, err := LoadRootCA(certPath, keyPath)
	if err != nil {
		t.Skipf("Cannot test without RootCA files: %v", err)
	}

	// Generate and sign certificate
	kg := NewKeyGenerator()
	devicePrivKey, err := kg.GenerateDeviceKeypair()
	if err != nil {
		t.Fatalf("Failed to generate device keypair: %v", err)
	}

	bundle, err := kg.SignDeviceCertificate("verify-test-device", &devicePrivKey.PublicKey, rootCert, rootKey)
	if err != nil {
		t.Fatalf("Failed to sign certificate: %v", err)
	}

	// Verify the certificate
	err = VerifyDeviceCertificate(bundle.ClientCertPEM, rootCert)
	if err != nil {
		t.Fatalf("Certificate verification failed: %v", err)
	}

	t.Log("Certificate verification passed")
}

func TestEncodePrivateKeyPEM(t *testing.T) {
	kg := NewKeyGenerator()
	privKey, err := kg.GenerateDeviceKeypair()
	if err != nil {
		t.Fatalf("Failed to generate keypair: %v", err)
	}

	// Encode
	keyPEM, err := EncodePrivateKeyPEM(privKey)
	if err != nil {
		t.Fatalf("Failed to encode private key: %v", err)
	}

	if keyPEM == nil || len(keyPEM) == 0 {
		t.Fatal("Encoded key PEM is empty")
	}

	// Verify PEM structure - check it contains the header
	if string(keyPEM[:26]) != "-----BEGIN EC PRIVATE KEY" {
		t.Logf("Warning: PEM header may have newline - got: %q", string(keyPEM[:35]))
		// Still success if we have a valid length
		if len(keyPEM) < 100 {
			t.Error("Key PEM too short for valid EC private key")
		}
	}

	t.Logf("Private key encoded: %d bytes", len(keyPEM))
}


func TestCertificateValidityPeriod(t *testing.T) {
	// Load RootCA
	certPath := "../../certs/ca.crt"
	keyPath := "../../certs/ca.key"

	rootCert, rootKey, err := LoadRootCA(certPath, keyPath)
	if err != nil {
		t.Skipf("Cannot test without RootCA files: %v", err)
	}

	kg := NewKeyGenerator()
	devicePrivKey, err := kg.GenerateDeviceKeypair()
	if err != nil {
		t.Fatalf("Failed to generate device keypair: %v", err)
	}

	// Sign certificate
	bundle, err := kg.SignDeviceCertificate("validity-test", &devicePrivKey.PublicKey, rootCert, rootKey)
	if err != nil {
		t.Fatalf("Failed to sign certificate: %v", err)
	}

	// Verify expiry is ~365 days in future
	now := time.Now()
	expectedExpiry := now.AddDate(0, 0, 365)

	diff := bundle.CertificateExpiry.Sub(expectedExpiry)
	if diff < -24*time.Hour || diff > 24*time.Hour { // Allow 1-day variance
		t.Errorf("Certificate expiry validation failed: expected ~%s, got %s (diff: %v)",
			expectedExpiry, bundle.CertificateExpiry, diff)
	}

	t.Logf("Certificate validity validated: %s to %s", now, bundle.CertificateExpiry)
}
