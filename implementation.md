# FieldPulse — Implementation status
This document is the **single source of truth** for what is built, how it behaves, and how that compares to the contract in [planning.md](planning.md).  
For **why** design choices were made, see [decision.md](decision.md). For **Redis keys and TTLs**, see [cache.md](cache.md). For **commands to run the stack**, see [setup.md](setup.md).
## 1. Runtime services
| Service | Entrypoint | Default port / exposure | Role | Health |
|--------|------------|-------------------------|------|--------|
| **PostgreSQL + Timescale** | Docker `timescale/timescaledb` | `5432` | Devices, metrics hypertable, alerts, thresholds, silences | Migrations on init; compression + retention policies in SQL |
| **Redis** | Docker `redis:7` | `6379` | Dedup, query cache, `last_seen`, alert silence keys | Soft-fail: telemetry starts without it |
| **MQTT (internal)** | Mosquitto | `1883`, `8883` | `devices/+/telemetry` ingest; TLS material present for 8883 | Compose uses plain `1883` for service-to-broker in dev |
| **MQTT (remote stub)** | Second Mosquitto | host `1884` → container `1883` | Connector / remote testing | Optional |
| **device-service** | `cmd/device-service` | gRPC `50051` | Registry, ECDSA certs, `CreateDevice` / `GetDevice` | Depends on Postgres + Redis |
| **telemetry-service** | `cmd/telemetry-service` | gRPC `50052` | gRPC submit/query, MQTT subscriber, async DB writer, alert evaluator hook | Depends on Postgres; Redis/MQTT optional with degradation |
| **alert-service** | `cmd/alert-service` | gRPC `50053` | Thresholds, active alerts, silences, stats | Depends on Postgres + Redis for some paths |
| **api-gateway** | `cmd/api-gateway` | HTTP `3000` | REST → gRPC bridge | Depends on backend gRPC |
| **connector** | `cmd/connector` | — | Remote MQTT → internal broker / telemetry path | Config-driven |
| **simulator** | `cmd/simulator` | — | Load / demo publisher | Docker profile |

**Accuracy note:** All of the above **start under Docker Compose** when images build and env matches [docker-compose.yml](docker-compose.yml). Use **Compose V2** (`docker compose`), not legacy Python `docker-compose`, on recent Docker engines to avoid `ContainerConfig` recreate errors.

---

## 2. Feature matrix vs [planning.md](planning.md)

Legend: **Yes** = matches intent; **Partial** = works with gaps; **No** = missing or wrong vs spec.

### 2.1 Global invariants (planning §1)

| Invariant | Status | Notes |
|-----------|--------|--------|
| Same `(device_id, timestamp)` not written twice | **Partial** | Redis `SETNX` on `dedup:{device_id}:{unix_seconds}`; if Redis fails, dedup is skipped and duplicates can reach DB |
| No invalid / future timestamps (>5s skew) | **Yes** | `validateAndResolveMetricTimestamp` in telemetry service |
| MQTT malformed payload does not crash | **Yes** | JSON decode + validation; errors logged, no panic path in ingest |
| Redis failure does not crash system | **Yes** | Nil Redis client paths in telemetry cache |
| Connector failure isolated per broker | **Partial** | Goroutine-per-broker structure; limited automated proof |
| Private keys not stored server-side | **Yes** | Device service returns key once |
| TLS verify on connections | **Partial** | Certs and Mosquitto config exist; full mTLS on all paths not integration-proven |
| Goroutines respect context | **Partial** | Handlers use context; not exhaustively audited for leaks |
| Services start independently | **Yes** | Separate binaries; compose `depends_on` only for ordering |
| Full stack via docker-compose | **Yes** | [docker-compose.yml](docker-compose.yml); use Compose V2 |

### 2.2 Telemetry (planning §4.1)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Subscribe `devices/+/telemetry` | **Yes** | Wired in telemetry `main` |
| Pipeline: deserialize → validate → time → dedup → insert | **Yes** | Order preserved |
| Dedup key + 600s TTL | **Yes** | 10 minutes in code |
| Buffered / non-blocking DB path | **Yes** | Channel-backed writer in telemetry service |
| Duplicate within window not stored twice | **Partial** | Logic present; depends on Redis |
| Malformed JSON safe | **Yes** | |
| Future timestamp rejected | **Yes** | |

### 2.3 Device service (planning §4.2)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Unique `device_id`, ECDSA P-256, 1y cert, return key once | **Yes** | |
| Duplicate device rejected | **Yes** | Code path; integration coverage varies |

### 2.4 Alerts & silence (planning §4.3)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Threshold evaluation on ingest | **Yes** | `AlertEvaluator` after metric persist (async); `postMetricWrite` must not pass a context canceled on return (see [internal/services/telemetry_service.go](internal/services/telemetry_service.go)) |
| Redis `alert:{device_id}` suppress duplicate fires ~5 min | **Yes** | Aligns with 300s TTL |
| MQTT publish `alerts/{device_id}` | **Yes** | When publisher configured |
| Silent detection: `last_seen`, >15 min → device update + MQTT | **Partial** | `StartSilentWatchdog` scans Redis; needs `last_seen` keys; no 15m integration test |
| Same alert within 5 min not duplicated | **Yes** | Redis gate + DB insert path |

### 2.5 Alert API / data layer caveat

| Item | Status | Notes |
|------|--------|--------|
| `GetActiveAlerts` with default `min_severity` | **Yes** | [internal/services/alert_service.go](internal/services/alert_service.go): omit DB severity filter when `min_severity` is `UNSPECIFIED` / `0` (proto default no longer maps to a bogus `severity = …` clause) |
| Count query matches list filters | **Yes** | [internal/db/alert_db.go](internal/db/alert_db.go): `COUNT(*)` uses the same optional `device_id` and `severity` predicates as the list query |

### 2.6 Unknown device on gRPC submit

| Item | Status | Notes |
|------|--------|--------|
| FK violation on metrics → clear client error | **Yes** | Postgres `23503` (`*pgconn.PgError`) maps to gRPC **NotFound** in [internal/services/telemetry_service.go](internal/services/telemetry_service.go) (`insertFailureStatus`) |
| Integration / E2E strictness | **Yes** | [test/integration_test.go](test/integration_test.go) `TestUnknownDeviceHandling` and [test/e2e_trace_test.go](test/e2e_trace_test.go) `TestE2ETraceErrorRecovery` require `codes.NotFound` |

### 2.7 Connector (planning §4.4)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Bounded queue 1000, drop oldest | **Yes** | |
| GetDevice before publish; drop unknown | **Yes** | |
| Exponential backoff 1s–30s | **Partial** | MQTT client `SetConnectRetry`; full backoff spec not verified line-by-line |
| Per-broker isolation | **Partial** | Structural |

### 2.8 Simulator (planning §4.5)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Goroutines, random interval, anomalies/duplicates | **Partial** | Flags exist; exact % match to spec not audited |

### 2.9 API gateway (planning §4.6)

| Requirement | Status | Notes |
|-------------|--------|--------|
| HTTP → gRPC, validation only | **Yes** | No business logic intended |

### 2.10 Database (planning §5)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Hypertable on time, 1d chunks | **Yes** | `metrics` table |
| Retention 90d, compression after 7d | **Yes** | Policies in [migrations/002_timescale_telemetry.sql](migrations/002_timescale_telemetry.sql) |
| Continuous aggregates | **Yes** | 1m, 5m, 1h, 1d (spec mentioned 1h; implementation is richer) |
| Fixed columns `temperature/vibration/current` | **No** | Implemented as EAV `metric_name` + `value`; documented in [decision.md](decision.md) |

### 2.11 Redis table (planning §6)

| Key | Spec TTL | Implemented | Notes |
|-----|----------|-------------|--------|
| `dedup:…` | 10 min | **Yes** | |
| `alert:{id}` | 5 min | **Yes** | |
| `last_seen:{id}` | “none” in table | **24h** in code | See [cache.md](cache.md) |

### 2.12 Observability (planning §11)

| Requirement | Status | Notes |
|-------------|--------|--------|
| OpenTelemetry traces | **Partial** | Instrumentation present; full connector→DB trace not guaranteed everywhere |

### 2.13 Repository layout (planning §3)

| Requirement | Status | Notes |
|-------------|--------|--------|
| Strict `/internal/{service}/handler|service|repository|…` | **No** | Code uses `internal/services`, `internal/db`, `internal/mqtt`, etc. |

---

## 3. Automated tests (accuracy)

| Suite | Command | What it proves |
|-------|---------|----------------|
| Unit / package tests | `go test ./...` | Validation, mocks, cert generation, service logic |
| Integration | `go test -tags=integration ./test/...` | Live gRPC + DB pipeline; **requires** compose stack and localhost ports |

**Integration gaps (high value):**

- **Alert end-to-end:** `TestAlertTrigger` does not submit a breaching metric nor assert `GetActiveAlerts` ≥ 1; it only creates a threshold. E2E trace test may still warn if evaluation races.
- **Silent device:** No 15-minute test; only device creation / ACTIVE check.
- **Unknown device:** Does not require `NotFound` or specific gRPC code.
- **Connector:** Mapping test is structural, not full remote broker.

---

## 4. Prioritized follow-ups (robustness & spec alignment)

1. **Fix `GetActiveAlerts` severity filter** when `min_severity` is unset ([internal/services/alert_service.go](internal/services/alert_service.go)).
2. **Map FK `23503` to `NotFound`** on telemetry submit/batch and tighten integration tests.
3. **Strengthen integration tests:** breach + poll alerts; optional `NotFound` assertion for unknown device.
4. **Document or enforce** repository layout vs planning (or formally amend planning).
5. **Prove connector backoff** and multi-broker failure isolation with tests or doc references to code.
6. **mTLS path:** integration test or runbook for 8883 client paths vs current 1883 internal traffic.

---

## 5. Document map (this repo)

| File | Purpose |
|------|---------|
| [planning.md](planning.md) | Execution contract (authoritative spec) |
| [implementation.md](implementation.md) | This file — features and status |
| [decision.md](decision.md) | Architectural decisions |
| [cache.md](cache.md) | Redis usage and TTLs |
| [setup.md](setup.md) | Commands to build, run, test |
| [README.md](README.md) | Short project entry and links |
