package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/cache"
	"fieldpulse.io/internal/db"
	"fieldpulse.io/internal/mqtt"
	"fieldpulse.io/internal/otel"
	"fieldpulse.io/internal/services"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := otel.InitTracing("telemetry-service"); err != nil {
		log.Printf("⚠️ tracing initialization failed: %v", err)
	} else {
		defer func() {
			_ = otel.ShutdownTracing(context.Background())
		}()
	}

	config := loadConfig()
	log.Printf("Starting Telemetry Service (port %s)", config.Port)

	dbPool, err := createDatabasePool(context.Background(), config.PostgresURI)
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer dbPool.Close()
	log.Println("✓ PostgreSQL connected")

	redisClient := createRedisClient(config.RedisAddr)
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Printf("⚠️  Redis unavailable — continuing without dedup/query cache (planning.md §6): %v", err)
		_ = redisClient.Close()
		redisClient = nil
	} else {
		log.Println("✓ Redis connected")
	}

	telemetryDB := db.NewTelemetryDB(dbPool)
	telemetryCache := cache.NewTelemetryCache(redisClient)
	alertDB := db.NewAlertDB(dbPool)

	var alertPub *mqtt.AlertPublisher
	if ap, err := mqtt.NewAlertPublisher(config.MQTTBroker, config.MQTTClientID); err != nil {
		log.Printf("⚠️  MQTT alert publisher disabled: %v", err)
	} else {
		alertPub = ap
		log.Println("✓ MQTT alert publisher ready")
	}
	if alertPub != nil {
		defer alertPub.Disconnect()
	}

	alertEvaluator := services.NewAlertEvaluator(alertDB, redisClient, alertPub)
	telemetryService := services.NewTelemetryServiceWithWriteQueue(telemetryDB, telemetryCache, services.DefaultWriteQueueCapacity, alertEvaluator)
	defer telemetryService.StopWriteWorker()

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	services.StartSilentWatchdog(watchCtx, redisClient, config.DeviceServiceAddr, alertPub)

	mqttSubscriber := mqtt.NewTelemetrySubscriber(mqtt.SubscriberConfig{
		BrokerURL: config.MQTTBroker,
		Topic:     config.MQTTTopic,
		ClientID:  config.MQTTClientID,
		QoS:       1,
	}, telemetryService)

	if err := mqttSubscriber.Start(context.Background()); err != nil {
		log.Printf("⚠️  MQTT subscriber failed to start: %v", err)
	} else {
		log.Printf("✓ MQTT subscriber started on %s topic %s", config.MQTTBroker, config.MQTTTopic)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterTelemetryServiceServer(grpcServer, telemetryService)

	healthChecker := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthChecker)
	healthChecker.SetServingStatus(pb.TelemetryService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)

	listener, err := net.Listen("tcp", ":"+config.Port)
	if err != nil {
		log.Fatalf("Failed to listen on port %s: %v", config.Port, err)
	}

	go func() {
		log.Printf("gRPC server listening on :%s", config.Port)
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutdown signal received, gracefully stopping server...")
	watchCancel()
	mqttSubscriber.Stop()
	grpcServer.GracefulStop()
	log.Println("Server stopped")
}

// Config holds service configuration from environment.
type Config struct {
	Port              string
	PostgresURI       string
	RedisAddr         string
	MQTTBroker        string
	MQTTTopic         string
	MQTTClientID      string
	DeviceServiceAddr string
}

func loadConfig() Config {
	config := Config{
		Port:              getEnv("TELEMETRY_PORT", "50052"),
		PostgresURI:       getEnv("POSTGRES_URI", "postgresql://fieldpulse:fieldpulse_dev@postgres:5432/fieldpulse"),
		RedisAddr:         getEnv("REDIS_ADDR", "redis:6379"),
		MQTTBroker:        getEnv("MQTT_BROKER", "tcp://mqtt:1883"),
		MQTTTopic:         getEnv("MQTT_TOPIC", "devices/+/telemetry"),
		MQTTClientID:      getEnv("MQTT_CLIENT_ID", "fieldpulse-telemetry-subscriber"),
		DeviceServiceAddr: getEnv("DEVICE_SERVICE_ADDR", "device-service:50051"),
	}
	log.Printf("Configuration loaded: port=%s device_service=%s", config.Port, config.DeviceServiceAddr)
	return config
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func createDatabasePool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid database URI: %w", err)
	}

	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = 5 * time.Minute
	config.MaxConnIdleTime = 2 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

func createRedisClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         addr,
		DB:           0,
		MaxRetries:   3,
		PoolSize:     10,
		MinIdleConns: 5,
	})
}
