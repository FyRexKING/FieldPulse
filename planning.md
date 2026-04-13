# FieldPulse — Strict Execution Specification

THIS DOCUMENT IS A CONTRACT.

All generated code MUST:
- Follow every constraint defined here
- Pass all acceptance criteria
- NOT introduce undocumented behavior

If conflict arises → THIS DOCUMENT OVERRIDES EVERYTHING.

---

# 0. SYSTEM OBJECTIVE

Build a fault-tolerant industrial IoT backend system that:

1. Ingests telemetry via MQTT
2. Deduplicates and validates data
3. Stores time-series data efficiently
4. Triggers alerts and silent detection
5. Bridges external MQTT brokers
6. Enforces mTLS authentication
7. Provides query APIs via gRPC

System must survive:
- duplicate messages
- broker restarts
- Redis outages
- slow database writes
- partial connector failures

---

# 1. NON-NEGOTIABLE GLOBAL INVARIANTS

These MUST hold across the system:

1. SAME (device_id, timestamp) MUST NEVER be written twice
2. NO invalid or future timestamp must enter DB
3. MQTT ingestion MUST NOT crash on malformed payload
4. Redis failure MUST NOT crash system
5. Connector failure MUST be isolated per broker
6. Private keys MUST NEVER be stored server-side
7. All TLS connections MUST verify certificates
8. Every goroutine MUST be cancellable via context
9. All services MUST start independently
10. System MUST run fully via docker-compose

---

# 2. ARCHITECTURE CONTRACT

## Data Flow

Simulator → MQTT → Telemetry → TimescaleDB  
                        ↓  
                    Alert Service  
                        ↓  
                   MQTT alerts/{device_id}  

Connector:

Remote MQTT → Connector → Internal MQTT → Telemetry

API:

HTTP → API Gateway → gRPC → Services

---

# 3. REPOSITORY STRUCTURE (STRICT)
/cmd/{service}/main.go

/internal/{service}/
handler/
service/
repository/
model/

internal/common/
mqtt/
grpc/
redis/
otel/
config/

/proto/
/migrations/
/scripts/
/mosquitto/
/tests/

---

# 4. SERVICE IMPLEMENTATION SPEC

---

## 4.1 TELEMETRY SERVICE (CRITICAL PATH)

### Responsibilities

- Subscribe to MQTT topic:
  devices/+/telemetry

### Processing Pipeline (MANDATORY ORDER)

1. Deserialize JSON
2. Validate schema
3. Validate timestamp:
   - MUST NOT be in future (>5s skew allowed)
4. Deduplicate via Redis
5. Insert into TimescaleDB

---

### Dedup Logic (STRICT)

Key:dedup:{device_id}:{timestamp}

Behavior:

- SETNX key
- TTL = 600 seconds

If key exists:
→ DROP message (no DB write)

If Redis unavailable:
→ LOG warning
→ CONTINUE without dedup

---

### DB Insert Rules

- Use pgx ONLY
- No ORM
- MUST be non-blocking (use buffered channel)

---

### Acceptance Criteria

- Duplicate message within 30 sec → NOT stored twice
- Malformed JSON → does NOT crash service
- Future timestamp → rejected

---

## 4.2 DEVICE SERVICE

### Responsibilities

- Device registry
- Certificate provisioning

---

### CreateDevice FLOW (STRICT)

1. Validate device_id uniqueness
2. Generate ECDSA P-256 keypair
3. Create X509 certificate:
   - CN = device_id
   - Signed by RootCA
   - Validity = 1 year
4. Store:
   - device metadata
   - certificate ONLY
5. Return:
   - cert
   - private key
   - CA cert

---

### SECURITY INVARIANTS

- Private key NEVER stored
- CA private key loaded from file/env ONLY

---

### Acceptance Criteria

- Returned cert CN == device_id
- Cert chain valid against CA
- Duplicate device_id rejected

---

## 4.3 ALERT SERVICE

### Responsibilities

- Threshold evaluation
- Alert publishing
- Silent detection

---

### Alert Logic

1. Receive telemetry
2. Compare against thresholds
3. Check Redis silence key:
   key = alert:{device_id}

If exists:
→ SUPPRESS alert

Else:
→ publish alert
→ set Redis key TTL=300s

---

### Silent Detection

- Track:
  last_seen:{device_id}

- If >15 min:
  → call Device Service (gRPC)
  → publish MQTT event

---

### Acceptance Criteria

- Same alert within 5 min → NOT duplicated
- Silent device correctly marked

---

## 4.4 CONNECTOR

### Responsibilities

- Connect to external brokers
- Normalize payload
- Republish internally

---

### Concurrency Model

- One goroutine per connector
- One goroutine per topic subscription

---

### Device ID Extraction

ONLY:segment:N

---

### Payload Mapping

- Dot notation traversal
- Missing field → OMIT (not zero)

---

### Device Validation

- Call GetDevice before publish

If not found:
→ DROP + LOG

---

### Internal Queue

- Bounded (size = 1000)

Overflow:
→ DROP OLDEST

---

### Retry Logic

- Exponential backoff:
  base = 1s
  max = 30s

---

### Acceptance Criteria

- Remote broker down → reconnects automatically
- One connector crash → others unaffected
- Missing metric → still ingested

---

## 4.5 SIMULATOR

### Behavior

- Default 50 goroutines
- Publish interval: random(2–10 sec)

---

### Fault Injection

- 5% anomaly spike
- 3% duplicates
- 1 silent device every 5 min

---

### Acceptance Criteria

- Produces duplicates
- Produces anomalies
- Supports TLS + remote mode

---

## 4.6 API GATEWAY

### Responsibilities

- HTTP → gRPC translation
- Validation only

---

### MUST NOT

- Contain business logic

---

# 5. DATABASE SPEC

---

## TimescaleDB

Table:
telemetry(
device_id TEXT,
timestamp TIMESTAMPTZ,
temperature DOUBLE,
vibration DOUBLE,
current DOUBLE
)

---

### Requirements

- Hypertable on timestamp
- Chunk interval: 1 day
- Retention: 90 days
- Compression: after 7 days

---

### Continuous Aggregate

- 1 hour bucket

---

# 6. REDIS USAGE (STRICT)

| Use Case        | Key Format                  | TTL     |
|----------------|----------------------------|--------|
| Dedup          | dedup:{id}:{timestamp}     | 10 min |
| Alert silence  | alert:{device_id}          | 5 min  |
| Last seen      | last_seen:{device_id}      | none   |

---

### Failure Behavior

- Redis down → system continues
- Dedup disabled temporarily

---

# 7. MQTT CONFIG

Internal Broker:

- Port: 8883
- mTLS REQUIRED
- use_identity_as_username = true

---

# 8. SECURITY MODEL

- All services use mTLS
- No insecure TLS flags
- CA key never committed

---

# 9. CONCURRENCY CONTRACT

Every goroutine MUST:

- Accept context
- Exit on cancellation
- Not leak

---

### PROHIBITED

- Infinite loops without select
- Blocking writes without timeout

---

# 10. TESTING SPEC

---

## Unit Tests (MANDATORY)

- Payload validation
- Dedup logic
- Alert logic
- Device ID extraction
- Dot mapping
- Certificate generation

---

## Integration Tests

### FULL PIPELINE

1. CreateDevice
2. Run simulator
3. Validate:

- Data in DB
- Duplicate dropped
- Alert triggered
- Silent detected

---

### SECURITY TEST

- Invalid cert connection → FAIL

---

### CONNECTOR TEST

- Remote mode
- Mapping correct
- Missing field handled
- Unknown device dropped

---

# 11. OBSERVABILITY

- OpenTelemetry REQUIRED
- Context propagation REQUIRED

---

### MUST TRACE

- Connector → MQTT → Telemetry → DB

---

# 12. CODING STANDARDS

---

## Layer Rules

| Layer     | Responsibility          |
|----------|------------------------|
| handler  | input/output only      |
| service  | business logic         |
| repo     | DB access only         |

---

### PROHIBITED

- DB calls in handler
- business logic in repo

---

## Errors

- Always wrapped
- No silent failures

---

# 13. DEPLOYMENT

System MUST run via:
docker-compose up

---

# 14. IMPLEMENTATION ORDER (STRICT)

1. Docker infra
2. Telemetry Service
3. Simulator
4. Device Service
5. Alert Service
6. Connector
7. API Gateway
8. Observability
9. Tests

---

# FINAL RULE

If ANY code:
- violates invariants
- skips failure handling
- introduces tight coupling

→ DELETE AND REWRITE

THIS SYSTEM IS EVALUATED ON:
- correctness
- reasoning
- resilience

NOT feature count.