-- migrations/001_init_device_registry.sql
-- Device registry schema for PostgreSQL
-- Stores device metadata and certificate information

CREATE TABLE IF NOT EXISTS devices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id VARCHAR(255) UNIQUE NOT NULL,
    floor VARCHAR(100) NOT NULL,
    device_type VARCHAR(100) NOT NULL,
    firmware_version VARCHAR(50),
    status VARCHAR(20) DEFAULT 'active' CHECK (status IN ('active', 'silent', 'degraded', 'inactive')),
    certificate_cn VARCHAR(255) NOT NULL,
    certificate_hash VARCHAR(255),
    provisioned_at TIMESTAMP WITH TIME ZONE,
    last_seen TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_devices_device_id ON devices(device_id);
CREATE INDEX idx_devices_floor ON devices(floor);
CREATE INDEX idx_devices_status ON devices(status);
CREATE INDEX idx_devices_created_at ON devices(created_at DESC);

-- Device certificates storage (for revocation checks in future)
CREATE TABLE IF NOT EXISTS device_certificates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id VARCHAR(255) NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    certificate_pem TEXT NOT NULL,
    certificate_hash VARCHAR(255) UNIQUE NOT NULL,
    issued_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    revoked_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_device_certificates_device_id ON device_certificates(device_id);
CREATE INDEX idx_device_certificates_expires_at ON device_certificates(expires_at);
CREATE INDEX idx_device_certificates_revoked_at ON device_certificates(revoked_at);
