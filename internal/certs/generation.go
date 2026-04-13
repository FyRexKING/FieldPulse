package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"time"
)

// CertificateBundle contains the complete provisioning credentials for a device
type CertificateBundle struct {
	// DeviceID: alphanumeric identifier matching CN
	DeviceID string

	// ClientCertPEM: signed certificate for the device (PEM-encoded)
	ClientCertPEM []byte

	// ClientKeyPEM: device's private key (PEM-encoded, NEVER STORED)
	ClientKeyPEM []byte

	// ClientCertHash: SHA256 hash of the certificate (for DB storage)
	ClientCertHash string

	// CertificateExpiry: when the certificate expires
	CertificateExpiry time.Time
}

// KeyGenerator defines the cryptographic key algorithm
type KeyGenerator struct {
	// Algorithm: "ecdsa-p256" (recommended) or "rsa-2048" (legacy)
	Algorithm string

	// ValidityDuration: how long the certificate is valid (default 1 year)
	ValidityDuration time.Duration
}

// NewKeyGenerator creates a new key generator with sensible defaults
func NewKeyGenerator() *KeyGenerator {
	return &KeyGenerator{
		Algorithm:        "ecdsa-p256",
		ValidityDuration: 365 * 24 * time.Hour, // 1 year
	}
}

// GenerateDeviceKeypair generates a new ECDSA P-256 keypair
func (kg *KeyGenerator) GenerateDeviceKeypair() (*ecdsa.PrivateKey, error) {
	switch kg.Algorithm {
	case "ecdsa-p256":
		// ECDSA P-256: Faster signatures, smaller keys, suitable for IoT devices
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	default:
		return nil, fmt.Errorf("unsupported key algorithm: %s", kg.Algorithm)
	}
}

// LoadRootCA loads the platform's root CA certificate and private key from disk
func LoadRootCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	// Load certificate
	certPEM, err := ioutil.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA cert: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("failed to parse CA cert PEM")
	}

	rootCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA cert: %w", err)
	}

	// Load private key
	keyPEM, err := ioutil.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("failed to parse CA key PEM")
	}

	rootKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA key: %w", err)
	}

	return rootCert, rootKey, nil
}

// SignDeviceCertificate creates and signs a certificate for a device
// Parameters:
//  - deviceID: unique device identifier (used as CN in certificate)
//  - devicePublicKey: public key to embed in certificate
//  - rootCert: platform's RootCA certificate (template for signing)
//  - rootKey: platform's RootCA private key (signer)
//
// Returns: Certificate bundle ready for device provisioning
func (kg *KeyGenerator) SignDeviceCertificate(
	deviceID string,
	devicePublicKey *ecdsa.PublicKey,
	rootCert *x509.Certificate,
	rootKey *ecdsa.PrivateKey,
) (*CertificateBundle, error) {
	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   deviceID, // Used as MQTT username (use_identity_as_username in Mosquitto)
			Organization: []string{"FieldPulse Industrial IoT"},
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(kg.ValidityDuration),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},

		// DNS names for mTLS verification
		DNSNames: []string{
			"localhost",
			deviceID,
			fmt.Sprintf("%s.devices.internal", deviceID),
		},
	}

	// Sign the certificate DER bytes with the RootCA
	certDER, err := x509.CreateCertificate(rand.Reader, template, rootCert, devicePublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign certificate: %w", err)
	}

	// Parse back to verify
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse signed certificate: %w", err)
	}

	// Verify certificate CN matches device_id
	if cert.Subject.CommonName != deviceID {
		return nil, fmt.Errorf("certificate CN mismatch: expected %s, got %s", deviceID, cert.Subject.CommonName)
	}

	// Compute certificate hash for storage
	certHash := sha256.Sum256(certDER)
	certHashHex := hex.EncodeToString(certHash[:])

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	return &CertificateBundle{
		DeviceID:          deviceID,
		ClientCertPEM:     certPEM,
		ClientCertHash:    certHashHex,
		CertificateExpiry: cert.NotAfter,
	}, nil
}

// EncodePrivateKeyPEM converts an ECDSA private key to PEM-encoded bytes
func EncodePrivateKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	derBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: derBytes,
	})

	return pemBytes, nil
}

// VerifyDeviceCertificate validates that a certificate is properly signed by the RootCA
// Used in tests and revocation checks
func VerifyDeviceCertificate(certPEM []byte, rootCert *x509.Certificate) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Verify signature chain
	opts := x509.VerifyOptions{
		Roots: x509.NewCertPool(),
	}
	opts.Roots.AddCert(rootCert)

	_, err = cert.Verify(opts)
	return err
}
