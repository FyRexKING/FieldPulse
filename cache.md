# FieldPulse — Caching and Redis usage

Redis is used for **ephemeral coordination, deduplication, and read-through acceleration**. It is **not** the system of record. **PostgreSQL + TimescaleDB** hold durable state: devices, certificates metadata, metrics hypertable, alert thresholds, alert history, and silences.

---

## What we cache (and related keys)

| Key pattern | Purpose | TTL | Notes |
|-------------|---------|-----|--------|
| `dedup:{device_id}:{unix_ts}` | Ingest dedup (`SETNX`); same device + Unix second accepted once per window | **10 minutes** | Passed from telemetry service (`dedupTTL`); value is a placeholder. |
| `last_seen:{device_id}` | Latest observed metric time (Unix seconds string) for silent-device / staleness logic | **24 hours** | Refreshed on successful ingest path when Redis is up. |
| `alert:{device_id}` | Short-term suppression after a firing alert (avoid duplicate alert inserts) | **5 minutes** | Set by alert evaluator and alert service paths; independent of DB silences. |
| `point:{device}:{metric}:{start}:{end}:{limit}:{offset}` | Cached **raw point query** results (JSON) | **1 minute** | Telemetry query cache-aside. |
| `agg:{device}:{metric}:{window}:{start}:{end}:{agg_types...}` | Cached **aggregation** query results | **5 minutes** | |
| `stats:{device}:{metric}:{start}:{end}` | Cached **metric stats** responses | **10 minutes** | |
| Other query keys (unknown prefix) | Fallback TTL for serialized query payloads | **5 minutes** | Default in `getTTL` when prefix is not `point` / `agg` / `stats`. |
| `device:{device_id}` | Cached **device registry** row (JSON) | **5 minutes** | Device service read-through cache. |

Keys are created by `internal/cache/telemetry_cache.go` and `internal/cache/device_cache.go` (and alert-related sets in services). Exact string formats may include additional fields for aggregations; the prefixes above determine TTL behavior for telemetry query caching.

---

## Failure and degradation behavior

- **Redis down at telemetry startup:** Service starts; dedup and query cache are off; `last_seen` is not updated (silent-device signals may be limited until Redis returns).
- **Redis error during dedup:** Ingestion **continues**; duplicates at the same timestamp are possible.
- **Redis error during query cache:** Read path falls back to **Postgres/Timescale**; cache is advisory only.

This matches the intent of `planning.md` §6 (cache as acceleration, not correctness).

---

## Why Redis instead of Timescale as “source of truth”

**Postgres + TimescaleDB** remain authoritative because:

1. **Durability and recovery:** Metrics, devices, and alerts must survive restarts, failovers, and backups. Redis is configured for speed; persistence policies vary and are not treated as the legal record for compliance or forensics.
2. **Relational integrity:** Device IDs in metrics reference the device registry; alerts reference thresholds. Foreign keys and transactions are enforced in SQL, not in a key-value store.
3. **Time-series features:** Hypertables, retention policies, compression, and time-oriented queries are first-class in Timescale. Modeling billions of samples purely in Redis would be expensive in RAM and weaker for ad hoc analytics.
4. **TTL semantics:** Dedup, alert suppression, and dashboard caches are **intentionally short-lived**. Losing Redis should mean “less dedup / colder cache,” not “we lost the fleet’s history.”
5. **Operational simplicity:** One queryable store for “what happened” keeps reporting, migrations, and incident analysis straightforward.

**What Redis is good for here:** sub-millisecond **SETNX** dedup, cheap **per-device counters/flags**, **read-through** caching for repeated dashboard queries, and **last-seen** hints for watchdog logic—workloads where eventual loss or expiry is acceptable.

**Summary:** Timescale answers “what do we know, durably, about devices and metrics?” Redis answers “how do we avoid duplicate writes, throttle noisy alerts, and serve hot reads cheaply for a few minutes?”
