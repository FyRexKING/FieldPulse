# FieldPulse — Local setup

Use this checklist to run the project on your machine. Commands are run from the **repository root** unless noted.

## Prerequisites

- **Go** (version in `go.mod`, e.g. 1.25+)
- **Docker** with **Compose V2** (`docker compose version`)
- **GNU Make**
- Optional: **Protocol Buffers compiler** (`protoc`) and plugins if you regenerate protos
- Optional: **OpenSSL** (for ad hoc certs; repo includes sample `.crt` files)
- Optional: **grpcurl** for manual gRPC calls

## One-time: clone and dependencies

```bash
git clone <your-repo-url> Fieldpulse
cd Fieldpulse
go mod download
```

## Tooling (protobuf + linter plugins)

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
# optional:
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

## Regenerate protobuf (when `.proto` changes)

```bash
protoc --go_out=. --go-grpc_out=. api/proto/*.proto
# or:
make proto
```

## Build (binaries under `./bin/`)

```bash
make build
# or manually:
go mod tidy
go build -o bin/device-service ./cmd/device-service
go build -o bin/telemetry-service ./cmd/telemetry-service
go build -o bin/alert-service ./cmd/alert-service
go build -o bin/simulator ./cmd/simulator
go build -o bin/connector ./cmd/connector
go build -o bin/api-gateway ./cmd/api-gateway
```

## Unit tests (no Docker required for most packages)

```bash
go test -count=1 -timeout 120s ./...
# or:
make test
```

## Docker Compose stack (Postgres, Redis, MQTT, services)

Ensure `.env.docker` exists (sample is tracked as `.env.docker`).

```bash
docker compose --env-file .env.docker up -d
# or:
make up
```

### Health checks

```bash
make health-check
```

### Logs

```bash
docker compose logs -f
# or:
make logs
```

### Stop stack

```bash
docker compose down
# or:
make down
```

### Rebuild images

```bash
docker compose build
# or:
make docker-build
```

### Reset volumes (destructive)

```bash
docker compose down -v
# or:
make docker-clean
```

## Database migrations

Migrations are applied on **first** Postgres init via `docker-compose` volume mounts. To re-apply SQL manually against the running container:

```bash
make migrate
```

## Integration tests (requires stack up + localhost ports)

Default addresses: see `test/integration_env.go` (`localhost:50051–50053`, Postgres `localhost:5432`).

```bash
go test -tags=integration -count=1 -timeout 10m -v ./test/...
# or:
make test-integration
```

## Optional: simulator / connector (containers running)

```bash
make simulator
make connector
```

## Formatting and vet

```bash
go fmt ./...
go vet ./...
```

## Benchmarks

```bash
go test -bench=. -benchmem -benchtime=5s ./...
# or:
make benchmark
```

## TLS material (local dev)

Repo tracks public certs under `certs/*.crt`. Private keys are gitignored. To regenerate a full local set:

```bash
make cert-gen
```

## API discovery (optional)

```bash
grpcurl -plaintext localhost:50051 list
grpcurl -plaintext localhost:50052 list
grpcurl -plaintext localhost:50053 list
curl -s http://localhost:3000/health
```

## Documentation tracked in git

- **[DECISIONS.md](DECISIONS.md)** — architecture rationale  
- **[CACHE.md](CACHE.md)** — Redis keys, TTLs, source of truth  
- **[decision.md](decision.md)** — pointer to `DECISIONS.md`  
- **This file** — setup commands  

**Note:** This repository’s `.gitignore` tracks only the Markdown files listed above (plus `decision.md`). A richer **`README.md`** may exist on your machine from earlier checkouts but is **not** committed to git in this configuration—use `DECISIONS.md` and `CACHE.md` as the shipped documentation.
