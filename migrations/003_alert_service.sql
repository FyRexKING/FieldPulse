-- Create alert thresholds table
DROP TABLE IF EXISTS alert_thresholds CASCADE;
CREATE TABLE alert_thresholds (
    threshold_id VARCHAR(255) PRIMARY KEY,
    device_id VARCHAR(255) NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    metric_name VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL CHECK (type IN ('HIGH', 'LOW', 'RANGE', 'CHANGE')),
    lower_bound FLOAT8 DEFAULT 0,
    upper_bound FLOAT8 DEFAULT 0,
    change_percent FLOAT8 DEFAULT 0,
    severity VARCHAR(32) NOT NULL CHECK (severity IN ('CRITICAL', 'WARNING', 'INFO')),
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    CONSTRAINT unique_threshold UNIQUE (device_id, metric_name, type)
);

CREATE INDEX idx_alert_thresholds_device ON alert_thresholds(device_id);
CREATE INDEX idx_alert_thresholds_enabled ON alert_thresholds(enabled);

-- Create alerts table (time-series hypertable)
DROP TABLE IF EXISTS alerts CASCADE;
CREATE TABLE alerts (
    alert_id VARCHAR(255) NOT NULL,
    time TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    device_id VARCHAR(255) NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    metric_name VARCHAR(128) NOT NULL,
    metric_value FLOAT8 NOT NULL,
    threshold_type VARCHAR(32) NOT NULL,
    threshold_value FLOAT8 NOT NULL,
    severity VARCHAR(32) NOT NULL,
    message TEXT,
    is_silenced BOOLEAN DEFAULT false,
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    
    PRIMARY KEY (alert_id, time)
);

-- Convert alerts to hypertable for time-series optimization
SELECT create_hypertable('alerts', 'time', if_not_exists => TRUE);

-- Enable compression on alerts with standard compression
ALTER TABLE alerts SET (
	timescaledb.compress,
	timescaledb.compress_orderby = 'time DESC, device_id, severity'
);

-- Set compression policy on alerts (compress after 1 day)
SELECT add_compression_policy('alerts', INTERVAL '1 day', if_not_exists => TRUE);

-- Set retention policy on alerts (keep for 90 days)
SELECT add_retention_policy('alerts', INTERVAL '90 days', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_alerts_device ON alerts (device_id DESC, time DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts (severity, time DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_unresolved ON alerts (device_id) WHERE resolved_at IS NULL;

-- Create silence_rules table
DROP TABLE IF EXISTS silence_rules CASCADE;
CREATE TABLE silence_rules (
    silence_id VARCHAR(255) PRIMARY KEY,
    device_id VARCHAR(255) NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    metric_name VARCHAR(128),  -- NULL means all metrics
    silenced_until TIMESTAMPTZ NOT NULL,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    CONSTRAINT unique_silence UNIQUE (device_id, metric_name)
);

CREATE INDEX idx_silence_rules_device ON silence_rules(device_id);
CREATE INDEX idx_silence_rules_expiry ON silence_rules(silenced_until);

-- Create continuous aggregate for alert statistics (1 hour window)
CREATE MATERIALIZED VIEW IF NOT EXISTS alert_stats_1h WITH (
    timescaledb.continuous,
    timescaledb.materialized_only = false
) AS
SELECT 
    time_bucket('1 hour', time) AS hour,
    device_id,
    severity,
    COUNT(*) as alert_count
FROM alerts
GROUP BY hour, device_id, severity
WITH NO DATA;

CREATE INDEX IF NOT EXISTS idx_alert_stats_1h_device ON alert_stats_1h(device_id DESC, hour DESC);

-- Refresh policy for alert_stats_1h
SELECT add_continuous_aggregate_policy('alert_stats_1h', 
    start_offset => INTERVAL '4 hours',
    end_offset => INTERVAL '30 minutes',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists => TRUE);
