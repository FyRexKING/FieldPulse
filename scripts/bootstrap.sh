#!/bin/bash

# FieldPulse Bootstrap Script
# Generates CA certificates and starts the development stack

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "🚀 FieldPulse Bootstrap"
echo "======================"

# 1. Generate certificates
echo ""
echo "📋 Step 1: Generating certificates..."
if [ ! -d "$PROJECT_DIR/certs" ]; then
    mkdir -p "$PROJECT_DIR/certs"
fi

# Check if certificates already exist
if [ -f "$PROJECT_DIR/certs/ca.crt" ] && [ -f "$PROJECT_DIR/certs/ca.key" ]; then
    echo "   ✓ Certificates already exist"
else
    echo "   Generating new certificates using OpenSSL..."
    
    # Generate RootCA private key (ECDSA P-256)
    openssl ecparam -name prime256v1 -genkey -noout -out "$PROJECT_DIR/certs/ca.key" 2>/dev/null
    
    # Generate RootCA certificate (10-year validity)
    openssl req -new -x509 -key "$PROJECT_DIR/certs/ca.key" -out "$PROJECT_DIR/certs/ca.crt" -days 3650 \
        -subj "/CN=FieldPulse-RootCA/O=FieldPulse Industrial IoT" 2>/dev/null
    
    # Generate Broker private key
    openssl ecparam -name prime256v1 -genkey -noout -out "$PROJECT_DIR/certs/broker.key" 2>/dev/null
    
    # Generate Broker CSR
    openssl req -new -key "$PROJECT_DIR/certs/broker.key" -out /tmp/broker.csr \
        -subj "/CN=localhost/O=FieldPulse" 2>/dev/null
    
    # Sign Broker certificate with RootCA (1-year validity)
    openssl x509 -req -in /tmp/broker.csr -CA "$PROJECT_DIR/certs/ca.crt" -CAkey "$PROJECT_DIR/certs/ca.key" \
        -CAcreateserial -out "$PROJECT_DIR/certs/broker.crt" -days 365 -sha256 2>/dev/null
    
    # Generate service certificates
    for SERVICE in telemetry-service alert-service connector; do
        openssl ecparam -name prime256v1 -genkey -noout -out "$PROJECT_DIR/certs/$SERVICE.key" 2>/dev/null
        openssl req -new -key "$PROJECT_DIR/certs/$SERVICE.key" -out /tmp/$SERVICE.csr \
            -subj "/CN=$SERVICE/O=FieldPulse" 2>/dev/null
        openssl x509 -req -in /tmp/$SERVICE.csr -CA "$PROJECT_DIR/certs/ca.crt" -CAkey "$PROJECT_DIR/certs/ca.key" \
            -CAcreateserial -out "$PROJECT_DIR/certs/$SERVICE.crt" -days 365 -sha256 2>/dev/null
    done
    
    echo "   ✓ Certificates generated"
fi

# 2. Start Docker Compose
echo ""
echo "🐳 Step 2: Starting Docker Compose stack..."
cd "$PROJECT_DIR"
docker-compose up -d

# Wait for services to be healthy
echo ""
echo "⏳ Waiting for services to be healthy..."
echo "   - Postgres..."
until docker-compose exec -T postgres pg_isready -U fieldpulse > /dev/null 2>&1; do
    sleep 1
done

echo "   - TimescaleDB..."
until docker-compose exec -T timescaledb pg_isready -U fieldpulse > /dev/null 2>&1; do
    sleep 1
done

echo "   - Redis..."
until docker-compose exec -T redis redis-cli ping > /dev/null 2>&1; do
    sleep 1
done

echo "   - MQTT..."
sleep 2  # Mosquitto needs extra time to fully initialize

echo ""
echo "✅ Bootstrap complete!"
echo ""
echo "Next steps:"
echo "  1. Build services: make build"
echo "  2. Run tests: go test ./tests/... -v"
echo "  3. View logs: docker-compose logs -f"
echo ""
echo "Services running:"
echo "  - Postgres: localhost:5432 (fieldpulse/fieldpulse_dev)"
echo "  - TimescaleDB: localhost:5433 (fieldpulse/fieldpulse_dev)"
echo "  - Redis: localhost:6379"
echo "  - MQTT (internal): localhost:8883 (mTLS)"
echo "  - MQTT (remote): localhost:1884 (plain)"
