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
	"fieldpulse.io/internal/db"
	"fieldpulse.io/internal/otel"
	"fieldpulse.io/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

const alertServicePort = 50053

func main() {
	if err := otel.InitTracing("alert-service"); err != nil {
		log.Printf("⚠️ tracing initialization failed: %v", err)
	}

	// Database connection
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s",
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_NAME"),
	)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Verify connection
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("Database ping failed: %v", err)
	}

	log.Println("✓ Connected to database")

	// Initialize database layer
	alertDB := db.NewAlertDB(pool)
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis:6379"
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Printf("⚠️ Redis unavailable for alert fast-path: %v", err)
		redisClient = nil
	} else {
		log.Println("✓ Connected to Redis")
	}

	// Initialize service
	alertService := services.NewAlertService(alertDB, redisClient)

	// Create gRPC server
	grpcServer := grpc.NewServer()
	pb.RegisterAlertServiceServer(grpcServer, alertService)

	// Start listening
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", alertServicePort))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", alertServicePort, err)
	}

	log.Printf("🚀 Alert Service listening on :%d", alertServicePort)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal: %v", sig)
		grpcServer.GracefulStop()
		pool.Close()
		os.Exit(0)
	}()

	// Start server
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("gRPC server error: %v", err)
	}
}
