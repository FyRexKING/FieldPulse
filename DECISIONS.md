# FieldPulse — Architectural decisions

This document explains **why** the system is shaped the way it is, not only **what** it does. It aligns with `planning.md` where that contract exists.

## MQTT QoS for telemetry and alerts

**Choice:** QoS **1** (at-least-once) for `devices/+/telemetry` and published alerts on `alerts/{device_id}`.

**Why:** Field networks and brokers routinely redeliver messages. At-least-once delivery is a good fit for telemetry volume. Duplicates are bounded in the database by **Redis-backed dedup** (see `CACHE.md`) so the same logical sample is not written twice under normal conditions.

## Dedup window (ingestion)

**Choice:** Key `dedup:{device_id}:{unix_seconds}` with TTL **10 minutes**, enforced at telemetry ingest.

**Why:** Covers MQTT redelivery and brief reconnect storms without unbounded Redis memory. Matches the execution contract for idempotent ingest at the (device, second) granularity.

## Alert rate limiting in Redis

**Choice:** After a non-silenced alert is stored, set `alert:{device_id}` with TTL **5 minutes**.

**Why:** Suppresses **duplicate alert rows** for the same device while the condition may still be true, without scanning the alerts table on every metric. Operator silences in Postgres (`silence_rules`) still apply for maintenance windows.

## Connector queue under load

**Choice:** Bounded queue (**1000**), **drop oldest** on overflow.

**Why:** Caps memory during broker storms. For a bridge, the oldest queued batch is usually the least valuable to deliver next.

## Unknown devices on the connector path

**Choice:** If registry lookup fails, **drop** the message; no auto-registration from the connector.

**Why:** Prevents unvalidated device IDs from entering TimescaleDB; improves security and auditability. Registration stays on the provisioning path.

## Device certificates

**Choice:** **ECDSA P-256**, **1 year** validity, private key returned **once** at provisioning and **not** stored server-side.

**Why:** Smaller keys than RSA (typical for IoT). Returning the key once limits exposure if the API or DB is compromised.

## Timescale / metrics schema

**Choice:** Hypertable **`metrics(time, device_id, metric_name, value, tags, status, …)`** (flexible name/value model) rather than fixed per-sensor columns.

**Why:** Supports arbitrary sensor types and future metrics without migrations per field. Retention, compression, and continuous aggregates remain available on the time column.

## Internal MQTT and TLS (dev vs prod)

**Choice:** Default **docker-compose** uses plain **1883** on the internal bridge; **8883** is reserved for device-facing mTLS in Mosquitto config.

**Why:** Keeps local development simple while production can enforce TLS and client certificates for publishers.

## Buffered, asynchronous writes to Timescale (telemetry)

**Choice:** Optional bounded channel between gRPC `SubmitMetric` / batch handlers and the DB writer; post-insert cache and alert evaluation run with tight timeouts off the hot path.

**Why:** Protects ingest latency from slow queries, Redis SCAN work, and alert evaluation. Throughput scales better under burst load; failures surface as `ResourceExhausted` if the queue fills rather than wedging all goroutines.

## Alert evaluation outside the RPC critical path

**Choice:** After a metric is persisted, **evaluate thresholds asynchronously** (in-process goroutine with timeout), using the same Postgres `alert_thresholds` / `alerts` tables as the alert service.

**Why:** Threshold checks and MQTT publish must not block ingestion. Correctness for “was it stored?” is decoupled from “did we finish evaluating every threshold?” within a short window.

## gRPC between internal services

**Choice:** Device, telemetry, and alert capabilities exposed as **gRPC** services; optional gateway in front for HTTP clients.

**Why:** Strong contracts via protobuf, efficient on the wire, first-class streaming for metric queries, and straightforward health checks for orchestration.

## Redis is optional at telemetry startup

**Choice:** If Redis is unreachable, telemetry **still starts**; dedup, query cache, and `last_seen` updates are degraded or skipped.

**Why:** Ingest and Timescale remain the source of truth; cache and dedup are **optimizations and safety rails**, not hard prerequisites for bringing the service up (see `CACHE.md`).
