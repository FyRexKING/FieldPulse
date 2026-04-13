-- Timescale telemetry schema
-- Applies to fieldpulse database where TimescaleDB extension is available.

CREATE EXTENSION IF NOT EXISTS timescaledb;

DROP TABLE IF EXISTS metrics CASCADE;
CREATE TABLE metrics (
	time TIMESTAMPTZ NOT NULL,
	device_id VARCHAR(255) NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
	metric_name VARCHAR(128) NOT NULL,
	value DOUBLE PRECISION NOT NULL,
	tags JSONB DEFAULT '{}'::jsonb,
	status VARCHAR(32) NOT NULL DEFAULT 'METRIC_STATUS_OK',
	PRIMARY KEY (time, device_id, metric_name)
);

SELECT create_hypertable(
	'metrics',
	'time',
	if_not_exists => TRUE,
	chunk_time_interval => INTERVAL '1 day'
);

CREATE INDEX IF NOT EXISTS idx_metrics_device_metric_time_desc
	ON metrics (device_id, metric_name, time DESC);

CREATE MATERIALIZED VIEW IF NOT EXISTS metrics_1m WITH (
	timescaledb.continuous,
	timescaledb.materialized_only = false
) AS
SELECT
	time_bucket('1 minute', time) AS bucket,
	device_id,
	metric_name,
	AVG(value) AS avg_value,
	MAX(value) AS max_value,
	MIN(value) AS min_value,
	PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY value) AS p95,
	PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY value) AS p99,
	COUNT(*) AS sample_count
FROM metrics
GROUP BY bucket, device_id, metric_name;

CREATE MATERIALIZED VIEW IF NOT EXISTS metrics_5m WITH (
	timescaledb.continuous,
	timescaledb.materialized_only = false
) AS
SELECT
	time_bucket('5 minute', time) AS bucket,
	device_id,
	metric_name,
	AVG(value) AS avg_value,
	MAX(value) AS max_value,
	MIN(value) AS min_value,
	PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY value) AS p95,
	PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY value) AS p99,
	COUNT(*) AS sample_count
FROM metrics
GROUP BY bucket, device_id, metric_name;

CREATE MATERIALIZED VIEW IF NOT EXISTS metrics_1h WITH (
	timescaledb.continuous,
	timescaledb.materialized_only = false
) AS
SELECT
	time_bucket('1 hour', time) AS bucket,
	device_id,
	metric_name,
	AVG(value) AS avg_value,
	MAX(value) AS max_value,
	MIN(value) AS min_value,
	PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY value) AS p95,
	PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY value) AS p99,
	COUNT(*) AS sample_count
FROM metrics
GROUP BY bucket, device_id, metric_name;

CREATE MATERIALIZED VIEW IF NOT EXISTS metrics_1d WITH (
	timescaledb.continuous,
	timescaledb.materialized_only = false
) AS
SELECT
	time_bucket('1 day', time) AS bucket,
	device_id,
	metric_name,
	AVG(value) AS avg_value,
	MAX(value) AS max_value,
	MIN(value) AS min_value,
	PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY value) AS p95,
	PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY value) AS p99,
	COUNT(*) AS sample_count
FROM metrics
GROUP BY bucket, device_id, metric_name;

CREATE INDEX IF NOT EXISTS idx_metrics_1m_lookup ON metrics_1m (device_id, metric_name, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_5m_lookup ON metrics_5m (device_id, metric_name, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_1h_lookup ON metrics_1h (device_id, metric_name, bucket DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_1d_lookup ON metrics_1d (device_id, metric_name, bucket DESC);

SELECT add_continuous_aggregate_policy(
	'metrics_1m',
	start_offset => INTERVAL '2 hours',
	end_offset => INTERVAL '1 minute',
	schedule_interval => INTERVAL '1 minute',
	if_not_exists => TRUE
);

SELECT add_continuous_aggregate_policy(
	'metrics_5m',
	start_offset => INTERVAL '1 day',
	end_offset => INTERVAL '5 minutes',
	schedule_interval => INTERVAL '5 minutes',
	if_not_exists => TRUE
);

SELECT add_continuous_aggregate_policy(
	'metrics_1h',
	start_offset => INTERVAL '7 days',
	end_offset => INTERVAL '1 hour',
	schedule_interval => INTERVAL '30 minutes',
	if_not_exists => TRUE
);

SELECT add_continuous_aggregate_policy(
	'metrics_1d',
	start_offset => INTERVAL '90 days',
	end_offset => INTERVAL '1 day',
	schedule_interval => INTERVAL '12 hours',
	if_not_exists => TRUE
);

-- Compression policy: compress chunks older than 7 days (per planning.md)
-- Note: Columnstore compression requires additional setup; using standard compression
ALTER TABLE metrics SET (
	timescaledb.compress,
	timescaledb.compress_orderby = 'time DESC, device_id, metric_name'
);

SELECT add_compression_policy(
	'metrics',
	compress_after => INTERVAL '7 days',
	if_not_exists => TRUE
);

-- Data retention policy: drop chunks older than 90 days (per planning.md)
SELECT add_retention_policy(
	'metrics',
	INTERVAL '90 days',
	if_not_exists => TRUE
);

