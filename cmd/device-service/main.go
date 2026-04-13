package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/certs"
	"fieldpulse.io/internal/db"
	"fieldpulse.io/internal/otel"
	"fieldpulse.io/internal/services"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

const (
	grpcPort = 50051
)

func getPostgresDS() string {
	if ds := os.Getenv("POSTGRES_URI"); ds != "" {
		return ds
	}
	return "postgres://fieldpulse:fieldpulse@localhost:5432/fieldpulse"
}

func getRedisAddr() string {
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}
	return "localhost:6379"
}

func main() {
	ctx := context.Background()

	if err := otel.InitTracing("device-service"); err != nil {
		log.Printf("⚠️ tracing initialization failed: %v", err)
	} else {
		defer func() {
			_ = otel.ShutdownTracing(context.Background())
		}()
	}

	// Initialize PostgreSQL connection pool
	log.Println("Connecting to PostgreSQL...")
	pgPool, err := pgxpool.New(ctx, getPostgresDS())
	if err != nil {
		log.Fatalf("Failed to create PostgreSQL connection pool: %v", err)
	}
	defer pgPool.Close()

	// Test connection
	if err := pgPool.Ping(ctx); err != nil {
		log.Fatalf("Failed to ping PostgreSQL: %v", err)
	}
	log.Println("✓ PostgreSQL connected")

	// Initialize Redis client
	log.Println("Connecting to Redis...")
	redisClient := redis.NewClient(&redis.Options{
		Addr: getRedisAddr(),
	})
	defer redisClient.Close()

	// Test connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to ping Redis: %v", err)
	}
	log.Println("✓ Redis connected")

	// Load platform RootCA certificate
	log.Println("Loading RootCA certificate...")
	rootCertPath := os.Getenv("ROOTCA_CERT_PATH")
	if rootCertPath == "" {
		rootCertPath = "certs/ca.crt"
	}

	rootKeyPath := os.Getenv("ROOTCA_KEY_PATH")
	if rootKeyPath == "" {
		rootKeyPath = "certs/ca.key"
	}

	rootCert, rootKey, err := certs.LoadRootCA(rootCertPath, rootKeyPath)
	if err != nil {
		log.Fatalf("Failed to load RootCA: %v", err)
	}
	log.Printf("✓ RootCA loaded (expires: %s)\n", rootCert.NotAfter.String())

	// Verify RootCA is self-signed and valid
	if err := rootCert.CheckSignatureFrom(rootCert); err != nil {
		log.Fatalf("RootCA signature verification failed: %v", err)
	}

	// Create database layer
	deviceDB := db.NewDeviceDB(pgPool)

	// Create Device Service
	deviceService := services.NewDeviceService(deviceDB, redisClient, rootCert, rootKey)

	// Create gRPC server
	log.Println("Starting gRPC server...")
	grpcServer := grpc.NewServer()

	// Register Device Service
	pb.RegisterDeviceServiceServer(grpcServer, deviceService)

	// Listen on port 50051
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", grpcPort, err)
	}

	// Start server in goroutine
	go func() {
		log.Printf("gRPC server listening on :%d\n", grpcPort)
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Printf("Received signal: %v", sig)

	// Graceful shutdown
	log.Println("Shutting down gRPC server...")
	grpcServer.GracefulStop()

	log.Println("Server stopped")
}
