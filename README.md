# FieldPulse

Industrial-style IoT backend: **MQTT → telemetry → TimescaleDB**, **gRPC** services, **Redis** for dedup/cache, **alerts** with thresholds and MQTT notification.

## Documentation (tracked in git)

| Doc | Purpose |
|-----|---------|
| [implementation.md](implementation.md) | **Features, service ports, status vs [planning.md](planning.md), test coverage, known gaps** |
| [setup.md](setup.md) | Prerequisites and commands to build, run Docker Compose, and test |
| [decision.md](decision.md) | Architectural decisions (why, not what) |
| [cache.md](cache.md) | Redis keys, TTLs, source of truth |
| [planning.md](planning.md) | Strict execution specification (contract) |

## Quick start

```bash
go mod download
docker compose --env-file .env.docker up -d   # or: make up
go test ./...
```

See [setup.md](setup.md) for integration tests and tooling.

## Services (typical Compose)

| Port | Service |
|------|---------|
| 50051 | device-service (gRPC) |
| 50052 | telemetry-service (gRPC) |
| 50053 | alert-service (gRPC) |
| 3000 | api-gateway (HTTP) |
| 5432 | Postgres / Timescale |
| 6379 | Redis |
| 1883 / 8883 | MQTT |

## License / status

Internal / educational project; see [implementation.md](implementation.md) for compliance and open issues.
