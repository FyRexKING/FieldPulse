package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fieldpulse.io/internal/db"

	"github.com/redis/go-redis/v9"
)

const (
	// DeviceCacheTTL: devices cached for 5 minutes
	DeviceCacheTTL = 5 * time.Minute

	// DeviceCacheKeyPrefix: key prefix for device caches
	DeviceCacheKeyPrefix = "device:"
)

// DeviceCache provides Redis operations for device caching
type DeviceCache struct {
	client *redis.Client
}

// NewDeviceCache creates a new device cache handler
func NewDeviceCache(client *redis.Client) *DeviceCache {
	return &DeviceCache{
		client: client,
	}
}

// Set stores a device in Redis cache with TTL
func (dc *DeviceCache) Set(ctx context.Context, device *db.Device) error {
	// Serialize device to JSON
	data, err := json.Marshal(device)
	if err != nil {
		return fmt.Errorf("failed to marshal device: %w", err)
	}

	// Store with TTL
	key := DeviceCacheKeyPrefix + device.DeviceID
	return dc.client.Set(ctx, key, data, DeviceCacheTTL).Err()
}

// Get retrieves a device from Redis cache
func (dc *DeviceCache) Get(ctx context.Context, deviceID string) (*db.Device, error) {
	key := DeviceCacheKeyPrefix + deviceID

	// Get from Redis
	data, err := dc.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // Cache miss
		}
		return nil, fmt.Errorf("failed to get device from cache: %w", err)
	}

	// Deserialize JSON
	device := &db.Device{}
	if err := json.Unmarshal([]byte(data), device); err != nil {
		return nil, fmt.Errorf("failed to unmarshal device: %w", err)
	}

	return device, nil
}

// Delete removes a device from Redis cache
func (dc *DeviceCache) Delete(ctx context.Context, deviceID string) error {
	key := DeviceCacheKeyPrefix + deviceID
	return dc.client.Del(ctx, key).Err()
}

// Exists checks if a device exists in cache
func (dc *DeviceCache) Exists(ctx context.Context, deviceID string) (bool, error) {
	key := DeviceCacheKeyPrefix + deviceID
	exists, err := dc.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check cache: %w", err)
	}
	return exists > 0, nil
}

// Clear removes all device caches (useful for testing or full invalidation)
func (dc *DeviceCache) Clear(ctx context.Context) error {
	// Scan for all device keys and delete
	iter := dc.client.Scan(ctx, 0, DeviceCacheKeyPrefix+"*", 100).Iterator()
	var keys []string

	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return fmt.Errorf("failed to scan cache keys: %w", err)
	}

	if len(keys) > 0 {
		return dc.client.Del(ctx, keys...).Err()
	}

	return nil
}
