.PHONY: help build build-all test run up down logs clean migrate proto lint docker-build docker-up docker-down simulator connector

# Prefer Docker Compose V2 (`docker compose` plugin). Python `docker-compose` 1.29.x can raise KeyError 'ContainerConfig' when recreating containers on Docker Engine 25+.
COMPOSE_CMD := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; else echo "docker-compose"; fi)

# Default target
help:
	@echo "FieldPulse Makefile targets:"
	@echo "  make build              - Build all Go services"
	@echo "  make build-all          - Build binaries + Docker images"
	@echo "  make test               - Run all tests"
	@echo "  make run                - Run services locally (requires Go 1.23)"
	@echo "  make up                 - Start Docker Compose (postgres, redis, mqtt, services)"
	@echo "  make down               - Stop Docker Compose"
	@echo "  make logs               - Tail Docker Compose logs"
	@echo "  make logs-service NAME  - Tail logs for specific service"
	@echo "  make clean              - Clean build artifacts"
	@echo "  make proto              - Regenerate protobuf files"
	@echo "  make lint               - Run Go linter"
	@echo "  make migrate            - Run database migrations"
	@echo "  make cert-gen           - Generate TLS certificates"
	@echo "  make docker-build       - Build Docker images"
	@echo "  make docker-push        - Push Docker images to registry"
	@echo "  make simulator          - Run device simulator (requires docker up)"
	@echo "  make connector          - Run outbound connector (requires docker up)"
	@echo "  make health-check       - Check all services health"
	@echo "  make benchmark          - Run performance benchmarks"

# Build all Go services
build:
	@echo "[BUILD] Compiling Go services..."
	@go mod tidy
	@go build -v -o bin/device-service ./cmd/device-service
	@go build -v -o bin/telemetry-service ./cmd/telemetry-service
	@go build -v -o bin/alert-service ./cmd/alert-service
	@go build -v -o bin/simulator ./cmd/simulator
	@go build -v -o bin/connector ./cmd/connector
	@go build -v -o bin/api-gateway ./cmd/api-gateway
	@echo "[BUILD] ✓ All binaries compiled successfully"

# Build + Docker images
build-all: build
	@echo "[BUILD] Creating Docker images..."
	@$(COMPOSE_CMD) build
	@echo "[BUILD] ✓ Docker images built successfully"

# Run test suite (unit; ./test uses stub unless -tags=integration)
test:
	@echo "[TEST] Running Go tests..."
	@go test -count=1 -timeout 120s ./...
	@echo "[TEST] ✓ All tests passed"

# Integration tests — requires stack (e.g. make up / docker compose) and published ports on localhost
test-integration:
	@echo "[TEST] Running integration tests (docker compose must be up)..."
	@go test -tags=integration -count=1 -timeout 10m -v ./test/...
	@echo "[TEST] ✓ Integration tests finished"

# Run services locally (no Docker)
run: build
	@echo "[RUN] Starting FieldPulse services..."
	@echo "Prerequisites: PostgreSQL, Redis, MQTT broker running on localhost"
	@echo "Starting services..."
	@(cd cmd/device-service && ./../../bin/device-service) &
	@(cd cmd/telemetry-service && ./../../bin/telemetry-service) &
	@(cd cmd/alert-service && ./../../bin/alert-service) &
	@(cd cmd/api-gateway && ./../../bin/api-gateway) &
	@wait

# Docker Compose management
up:
	@echo "[DOCKER] Starting Docker Compose..."
	@$(COMPOSE_CMD) --env-file .env.docker up -d
	@echo "[DOCKER] ✓ Services started. Waiting for health checks..."
	@sleep 5
	@$(MAKE) health-check

down:
	@echo "[DOCKER] Stopping Docker Compose..."
	@$(COMPOSE_CMD) down
	@echo "[DOCKER] ✓ Services stopped"

logs:
	@$(COMPOSE_CMD) logs -f

logs-service:
	@$(COMPOSE_CMD) logs -f $(NAME)

# Database operations
migrate:
	@echo "[MIGRATE] Running database migrations..."
	@docker exec fieldpulse-postgres bash -c "cd /docker-entrypoint-initdb.d && for f in *.sql; do echo \"Applying $$f\"; psql -U fieldpulse -d fieldpulse < $$f; done"
	@echo "[MIGRATE] ✓ Migrations applied"

# Certificate generation
cert-gen:
	@echo "[CERT] Generating TLS certificates..."
	@mkdir -p certs
	@openssl genrsa -out certs/ca.key 2048
	@openssl req -new -x509 -days 365 -key certs/ca.key -out certs/ca.crt -subj "/CN=fieldpulse-ca"
	@openssl genrsa -out certs/broker.key 2048
	@openssl req -new -key certs/broker.key -out certs/broker.csr -subj "/CN=mqtt"
	@openssl x509 -req -days 365 -in certs/broker.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial -out certs/broker.crt
	@echo "[CERT] ✓ Certificates generated in ./certs"

# Protobuf codegen
proto:
	@echo "[PROTO] Regenerating protobuf files..."
	@protoc --go_out=. --go-grpc_out=. api/proto/*.proto
	@echo "[PROTO] ✓ Protobuf files generated"

# Linting
lint:
	@echo "[LINT] Running golangci-lint..."
	@golangci-lint run ./...
	@echo "[LINT] ✓ No linting issues found"

# Docker operations
docker-build:
	@echo "[DOCKER] Building Docker images..."
	@$(COMPOSE_CMD) build
	@echo "[DOCKER] ✓ Images built successfully"

docker-push:
	@echo "[DOCKER] Pushing images to registry..."
	@echo "Note: Update DOCKER_REGISTRY in .env first"
	@$(COMPOSE_CMD) push
	@echo "[DOCKER] ✓ Images pushed successfully"

# Service management
simulator:
	@echo "[SIMULATOR] Running device metrics simulator..."
	@docker exec fieldpulse-simulator ./simulator --devices=50 --rate=1000 --anomalies=0.05

connector:
	@echo "[CONNECTOR] Running outbound connector..."
	@docker logs -f fieldpulse-connector

# Platform checks
health-check:
	@echo "[HEALTH] Checking service health..."
	@echo "  PostgreSQL:"
	@docker exec fieldpulse-postgres pg_isready || echo "    ✗ Not ready"
	@echo "  Redis:"
	@docker exec fieldpulse-redis redis-cli ping || echo "    ✗ Not ready"
	@echo "  MQTT:"
	@docker exec fieldpulse-mqtt mosquitto_sub -p 1883 -t "test" -C 1 | head -1 || echo "    ✗ Not ready"
	@echo "  Device Service:"
	@docker exec fieldpulse-device-service nc -z localhost 50051 && echo "    ✓ Healthy" || echo "    ✗ Not ready"
	@echo "  Telemetry Service:"
	@docker exec fieldpulse-telemetry-service nc -z localhost 50052 && echo "    ✓ Healthy" || echo "    ✗ Not ready"
	@echo "  Alert Service:"
	@docker exec fieldpulse-alert-service nc -z localhost 50053 && echo "    ✓ Healthy" || echo "    ✗ Not ready"
	@echo "  API Gateway:"
	@curl -s http://localhost:3000/health > /dev/null && echo "    ✓ Healthy" || echo "    ✗ Not ready"

# Performance benchmarking
benchmark:
	@echo "[BENCHMARK] Running performance benchmarks..."
	@go test -bench=. -benchmem -benchtime=5s ./...

# Cleanup
clean:
	@echo "[CLEAN] Removing build artifacts..."
	@rm -rf bin/
	@rm -rf dist/
	@go clean
	@echo "[CLEAN] ✓ Cleanup complete"

# Development helpers
install-tools:
	@echo "[TOOLS] Installing required tools..."
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo "[TOOLS] ✓ Tools installed"

fmt:
	@echo "[FMT] Formatting Go code..."
	@go fmt ./...
	@echo "[FMT] ✓ Code formatted"

# Docker cleanup
docker-clean:
	@echo "[DOCKER] Cleaning up volumes and images..."
	@$(COMPOSE_CMD) down -v
	@echo "[DOCKER] ✓ Cleanup complete"

# Linting
lint:
	@echo "Running linters..."
	go vet ./...
	@echo "✓ Linting complete"

# Build all services
build: proto
	@echo "Building all services..."
	go build -o bin/device-service ./cmd/device-service
	@echo "✓ All services built"

# Build Device Service
build-device-srv: proto
	@echo "Building Device Service..."
	go build -o bin/device-service ./cmd/device-service
	@echo "✓ Device Service built (bin/device-service)"

# Test
test:
	@echo "Running all tests..."
	go test -v ./...

# Test certificates
test-certs:
	@echo "Running certificate generation tests..."
	go test -v ./internal/certs

# Run Device Service
run-device-service: build-device-srv
	@echo "Starting Device Service..."
	@echo "Requires: PostgreSQL (5432), Redis (6379), RootCA certificates"
	./bin/device-service

# Docker infrastructure management
docker-up:
	@echo "Starting Docker infrastructure..."
	@$(COMPOSE_CMD) up -d
	@echo "Waiting for services to be ready..."
	@sleep 5
	@make docker-ps

docker-down:
	@echo "Stopping Docker infrastructure..."
	@$(COMPOSE_CMD) down
	@echo "✓ Services stopped"

docker-logs:
	@echo "Showing Docker logs..."
	@$(COMPOSE_CMD) logs -f

docker-ps:
	@echo ""
	@echo "Docker Container Status:"
	@echo "========================"
	@$(COMPOSE_CMD) ps

# Cleanup
clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/
	rm -f api/proto/*.pb.go
	go clean
	@echo "✓ Cleanup complete"

# Development setup
setup:
	@echo "Setting up development environment..."
	@echo "1. Installing proto compiler..."
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@echo "2. Starting Docker infrastructure..."
	@make docker-up
	@echo ""
	@echo "✓ Development environment ready"
	@echo ""
	@echo "Next steps:"
	@echo "  make proto              # Generate protobuf code"
	@echo "  make build              # Build all services"
	@echo "  make test               # Run tests"
	@echo "  make run-device-service # Start Device Service"

# Integration test
integration-test: docker-up build
	@echo "Running integration tests..."
	@echo "1. Waiting for infrastructure..."
	@sleep 5
	@echo "2. Testing Device Service..."
	go test -v -tags=integration ./...

# Full CI pipeline (for GitHub Actions / local CI)
ci: fmt lint test docker-up build integration-test docker-down
	@echo "✓ CI pipeline complete"
