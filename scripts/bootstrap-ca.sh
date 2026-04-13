#!/usr/bin/env go run
//go:build ignore

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CertConfig describes a certificate to generate
type CertConfig struct {
	Name       string
	CommonName string
	IsCA       bool
	Duration   time.Duration
	SignerCert *x509.Certificate
	SignerKey  *ecdsa.PrivateKey
}

func main() {
	outDir := flag.String("out", "certs", "Output directory for certificates")
	flag.Parse()

	// Create output directory
	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	log.Println("🚀 Generating Platform CA & Certificates...")

	// 1. Generate Root CA (10-year validity)
	log.Println("  → Generating RootCA (ECDSA P-256, 10-year validity)...")
	rootCert, rootKey, err := generateRootCA()
	if err != nil {
		log.Fatalf("Failed to generate RootCA: %v", err)
	}

	// Save Root CA files
	caPath := filepath.Join(*outDir, "ca.crt")
	keyPath := filepath.Join(*outDir, "ca.key")
	if err := saveCertificate(caPath, rootCert); err != nil {
		log.Fatalf("Failed to save CA cert: %v", err)
	}
	if err := savePrivateKey(keyPath, rootKey); err != nil {
		log.Fatalf("Failed to save CA key: %v", err)
	}
	log.Printf("     ✓ CA: %s, %s (KEEP ca.key SECURE!)\n", caPath, keyPath)

	// 2. Generate Broker Certificate (signed by RootCA, 1-year validity)
	log.Println("  → Generating Broker Certificate (signed by RootCA, 1-year validity)...")
	brokerCert, brokerKey, err := generateSignedCert(
		"mqtt-broker",
		"localhost",
		1*time.Hour*24*365, // 1 year
		rootCert,
		rootKey,
	)
	if err != nil {
		log.Fatalf("Failed to generate broker cert: %v", err)
	}

	brokerCertPath := filepath.Join(*outDir, "broker.crt")
	brokerKeyPath := filepath.Join(*outDir, "broker.key")
	if err := saveCertificate(brokerCertPath, brokerCert); err != nil {
		log.Fatalf("Failed to save broker cert: %v", err)
	}
	if err := savePrivateKey(brokerKeyPath, brokerKey); err != nil {
		log.Fatalf("Failed to save broker key: %v", err)
	}
	log.Printf("     ✓ Broker: %s, %s\n", brokerCertPath, brokerKeyPath)

	// 3. Generate Service Identity Certificates
	services := []string{"telemetry-service", "alert-service", "connector"}
	for _, svc := range services {
		log.Printf("  → Generating Service Certificate: %s (1-year validity)...\n", svc)
		svcCert, svcKey, err := generateSignedCert(
			svc,
			"127.0.0.1",
			1*time.Hour*24*365, // 1 year
			rootCert,
			rootKey,
		)
		if err != nil {
			log.Fatalf("Failed to generate service cert for %s: %v", svc, err)
		}

		svcCertPath := filepath.Join(*outDir, svc+".crt")
		svcKeyPath := filepath.Join(*outDir, svc+".key")
		if err := saveCertificate(svcCertPath, svcCert); err != nil {
			log.Fatalf("Failed to save service cert: %v", err)
		}
		if err := savePrivateKey(svcKeyPath, svcKey); err != nil {
			log.Fatalf("Failed to save service key: %v", err)
		}
		log.Printf("     ✓ %s: %s, %s\n", svc, svcCertPath, svcKeyPath)
	}

	log.Println("\n✅ Certificate generation complete!")
	log.Println("\nCertificate Details:")
	log.Printf("  Root CA:     %s (Common Name: %s)\n", caPath, rootCert.Subject.CommonName)
	log.Printf("  Broker:      %s (Common Name: %s)\n", brokerCertPath, brokerCert.Subject.CommonName)
	for _, svc := range services {
		log.Printf("  %s: %s\n", svc, filepath.Join(*outDir, svc+".crt"))
	}

	log.Println("\n⚠️  Remember:")
	log.Println("  - ca.key is PRIVATE - never commit to git (in .gitignore)")
	log.Println("  - ca.crt is PUBLIC - safe to commit")
	log.Println("  - service *.key files are PRIVATE - never commit to git")
	log.Println("  - To use with docker-compose: mount certs/ volume")
}

// generateRootCA creates a self-signed RootCA certificate
func generateRootCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	// Generate ECDSA P-256 keypair
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "FieldPulse-RootCA",
			Organization: []string{"FieldPulse Industrial IoT"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            2,
	}

	// Self-sign the certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return cert, privateKey, nil
}

// generateSignedCert creates a certificate signed by the provided CA
func generateSignedCert(
	name, commonName string,
	duration time.Duration,
	signerCert *x509.Certificate,
	signerKey *ecdsa.PrivateKey,
) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	// Generate ECDSA P-256 keypair for the new certificate
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"FieldPulse"},
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(duration),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{"localhost", "127.0.0.1", name},
	}

	// Sign the certificate with the CA key
	derBytes, err := x509.CreateCertificate(rand.Reader, template, signerCert, &privateKey.PublicKey, signerKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return cert, privateKey, nil
}

// saveCertificate writes a certificate to PEM file
func saveCertificate(path string, cert *x509.Certificate) error {
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})

	return ioutil.WriteFile(path, pemBytes, 0644)
}

// savePrivateKey writes an ECDSA private key to PEM file
func savePrivateKey(path string, key *ecdsa.PrivateKey) error {
	derBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: derBytes,
	})

	return ioutil.WriteFile(path, pemBytes, 0600) // Restrict permissions on private key
}

// hashCert returns the SHA256 hash of a certificate (for storage)
func hashCert(cert *x509.Certificate) string {
	hash := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(hash[:])
}
