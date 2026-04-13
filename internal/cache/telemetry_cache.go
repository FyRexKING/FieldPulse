package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// TelemetryCache wraps Redis operations for metric query caching.
// Uses cache-aside pattern: miss → fetch from DB → populate cache → return.
// When client is nil (Redis unavailable), operations degrade gracefully: no dedup, cache miss, no last_seen.
type TelemetryCache struct {
	client *redis.Client
	ttl    map[string]time.Duration // TTL by cache key prefix
}

// NewTelemetryCache creates a new metric cache wrapper. Pass nil client when Redis is down — ingestion continues without dedup/caching per planning.md.
func NewTelemetryCache(client *redis.Client) *TelemetryCache {
	return &TelemetryCache{
		client: client,
		ttl: map[string]time.Duration{
			"agg":   5 * time.Minute,  // Aggregations: 5 min TTL
			"stats": 10 * time.Minute, // Statistics: 10 min TTL
			"point": 1 * time.Minute,  // Point queries: 1 min TTL
		},
	}
}

// ==================== Query Result Caching ====================

// CacheQueryResult stores a query result with type-specific TTL.
func (c *TelemetryCache) CacheQueryResult(ctx context.Context, key string, result interface{}) error {
	if c.client == nil {
		return nil
	}
	ttl := c.getTTL(key)

	// Serialize to JSON
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	// Store in Redis
	err = c.client.Set(ctx, key, data, ttl).Err()
	if err != nil {
		// Non-fatal error: cache is optional, don't fail the operation
		return fmt.Errorf("failed to cache result: %w", err)
	}

	return nil
}

// GetQueryResult retrieves a cached query result.
// Returns error only if cache is actually unavailable (not a miss).
func (c *TelemetryCache) GetQueryResult(ctx context.Context, key string, result interface{}) error {
	if c.client == nil {
		return fmt.Errorf("cache miss")
	}
	// Try to get from cache
	data, err := c.client.Get(ctx, key).Result()

	if err == redis.Nil {
		// Cache miss - expected, not an error
		return fmt.Errorf("cache miss")
	}

	if err != nil {
		// Real error accessing cache
		return fmt.Errorf("failed to retrieve from cache: %w", err)
	}

	// Deserialize
	err = json.Unmarshal([]byte(data), result)
	if err != nil {
		return fmt.Errorf("failed to unmarshal cached result: %w", err)
	}

	return nil
}

// ==================== Key Generation ====================

// buildQueryCacheKey creates a cache key for a metric query result.
func (c *TelemetryCache) buildQueryCacheKey(deviceID, metricName string, startTime, endTime time.Time, limit, offset int32) string {
	// Format: "point:device-1:temperature:1704067200:1704153600:1000:0"
	return fmt.Sprintf(
		"point:%s:%s:%d:%d:%d:%d",
		deviceID,
		metricName,
		startTime.Unix(),
		endTime.Unix(),
		limit,
		offset,
	)
}

// buildAggregationCacheKey creates a cache key for aggregation results.
func (c *TelemetryCache) buildAggregationCacheKey(deviceID, metricName, windowSize string, startTime, endTime time.Time, aggTypes []string) string {
	// Format: "agg:device-1:temperature:1h:1704067200:1704153600:AVG:MAX:MIN"
	aggTypeStr := fmt.Sprintf("%v", aggTypes)
	return fmt.Sprintf(
		"agg:%s:%s:%s:%d:%d:%s",
		deviceID,
		metricName,
		windowSize,
		startTime.Unix(),
		endTime.Unix(),
		aggTypeStr,
	)
}

// buildStatsCacheKey creates a cache key for metric statistics.
func (c *TelemetryCache) buildStatsCacheKey(deviceID, metricName string, startTime, endTime time.Time) string {
	// Format: "stats:device-1:temperature:1704067200:1704153600"
	return fmt.Sprintf(
		"stats:%s:%s:%d:%d",
		deviceID,
		metricName,
		startTime.Unix(),
		endTime.Unix(),
	)
}

// ==================== Cache Invalidation ====================

// InvalidateMetricCache removes caches for a specific metric.
// Call this after inserting new data to prevent stale reads.
func (c *TelemetryCache) InvalidateMetricCache(ctx context.Context, deviceID, metricName string) error {
	if c.client == nil {
		return nil
	}
	// Delete all cache entries matching pattern "point:device-1:temperature:*"
	pattern := fmt.Sprintf("point:%s:%s:*", deviceID, metricName)

	cursor := uint64(0)
	for {
		keys, cursor, err := c.client.Scan(ctx, cursor, pattern, 0).Result()
		if err != nil {
			return fmt.Errorf("failed to scan cache keys: %w", err)
		}

		if len(keys) > 0 {
			err = c.client.Del(ctx, keys...).Err()
			if err != nil {
				return fmt.Errorf("failed to delete cache keys: %w", err)
			}
		}

		if cursor == 0 {
			break
		}
	}

	// Also invalidate stats cache
	statsPattern := fmt.Sprintf("stats:%s:%s:*", deviceID, metricName)
	cursor = 0
	for {
		keys, cursor, err := c.client.Scan(ctx, cursor, statsPattern, 0).Result()
		if err != nil {
			return fmt.Errorf("failed to scan stats cache keys: %w", err)
		}

		if len(keys) > 0 {
			err = c.client.Del(ctx, keys...).Err()
			if err != nil {
				return fmt.Errorf("failed to delete stats cache keys: %w", err)
			}
		}

		if cursor == 0 {
			break
		}
	}

	return nil
}

// InvalidateDevice removes all caches for a device.
// Call when a device is deleted or updated.
func (c *TelemetryCache) InvalidateDevice(ctx context.Context, deviceID string) error {
	if c.client == nil {
		return nil
	}
	// Delete pattern "*:device-1:*"
	pattern := fmt.Sprintf("*:%s:*", deviceID)

	cursor := uint64(0)
	deleted := int64(0)

	for {
		keys, cursor, err := c.client.Scan(ctx, cursor, pattern, 0).Result()
		if err != nil {
			return fmt.Errorf("failed to scan device cache keys: %w", err)
		}

		if len(keys) > 0 {
			result, err := c.client.Del(ctx, keys...).Result()
			if err != nil {
				return fmt.Errorf("failed to delete device cache keys: %w", err)
			}
			deleted += result
		}

		if cursor == 0 {
			break
		}
	}

	return nil
}

// ClearAll removes all telemetry-related cache entries.
// Use with caution - typically only in testing or maintenance.
func (c *TelemetryCache) ClearAll(ctx context.Context) error {
	if c.client == nil {
		return nil
	}
	// Delete all keys matching our prefixes
	patterns := []string{"point:*", "agg:*", "stats:*"}

	for _, pattern := range patterns {
		cursor := uint64(0)
		for {
			keys, cursor, err := c.client.Scan(ctx, cursor, pattern, 0).Result()
			if err != nil {
				return fmt.Errorf("failed to scan cache keys: %w", err)
			}

			if len(keys) > 0 {
				err = c.client.Del(ctx, keys...).Err()
				if err != nil {
					return fmt.Errorf("failed to delete cache keys: %w", err)
				}
			}

			if cursor == 0 {
				break
			}
		}
	}

	return nil
}

// ==================== Cache Stats ====================

// GetCacheStats returns statistics about cache usage.
func (c *TelemetryCache) GetCacheStats(ctx context.Context) (map[string]interface{}, error) {
	if c.client == nil {
		return map[string]interface{}{"redis": "disabled"}, nil
	}
	info, err := c.client.Info(ctx, "stats").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get cache stats: %w", err)
	}

	stats := make(map[string]interface{})
	stats["info"] = info

	// Count entries by prefix
	for prefix := range c.ttl {
		cursor := uint64(0)
		count := int64(0)

		pattern := prefix + ":*"
		for {
			keys, cursor, err := c.client.Scan(ctx, cursor, pattern, 0).Result()
			if err != nil {
				return nil, fmt.Errorf("failed to count %s entries: %w", prefix, err)
			}
			count += int64(len(keys))

			if cursor == 0 {
				break
			}
		}

		stats[prefix+"_count"] = count
	}

	return stats, nil
}

// ==================== Private Helper Methods ====================

// getTTL returns the TTL for a given cache key based on its prefix.
func (c *TelemetryCache) getTTL(key string) time.Duration {
	// Determine prefix (first part before first colon)
	var prefix string
	for i, ch := range key {
		if ch == ':' {
			prefix = key[:i]
			break
		}
	}

	ttl, ok := c.ttl[prefix]
	if !ok {
		// Default TTL: 5 minutes
		return 5 * time.Minute
	}

	return ttl
}

// ==================== Batch Operations ====================

// PrefetchAggregations pre-populates cache with common aggregation queries.
// Useful for dashboards to pre-warm cache during off-peak hours.
func (c *TelemetryCache) PrefetchAggregations(ctx context.Context, deviceID, metricName string, startTime, endTime time.Time) error {
	// This would typically be called by a background job
	// Implementation depends on database availability in this context
	// Placeholder for external coordination
	return nil
}

// EvictLRU removes least-recently-used entries if cache is full.
// Redis handles this automatically based on eviction policy.
func (c *TelemetryCache) EvictLRU(ctx context.Context) error {
	// Redis maxmemory-policy is configured at the container/server level
	// This is a no-op here but provided for API completeness
	return nil
}

// TryMarkDedup marks a metric as seen for a short TTL window.
// Returns true when the key is newly set, false when it already exists.
// On Redis error, logs and returns (true, nil) so ingestion continues without dedup (planning.md §4.1).
func (c *TelemetryCache) TryMarkDedup(ctx context.Context, deviceID string, ts time.Time, ttl time.Duration) (bool, error) {
	if c.client == nil {
		return true, nil
	}
	key := fmt.Sprintf("dedup:%s:%d", deviceID, ts.Unix())
	created, err := c.client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		log.Printf("warning: redis dedup unavailable, continuing without dedup: %v", err)
		return true, nil
	}
	return created, nil
}

// SetDeviceLastSeen updates the latest observed metric timestamp for a device.
func (c *TelemetryCache) SetDeviceLastSeen(ctx context.Context, deviceID string, ts time.Time, ttl time.Duration) error {
	if c.client == nil {
		return nil
	}
	key := fmt.Sprintf("last_seen:%s", deviceID)
	if err := c.client.Set(ctx, key, strconv.FormatInt(ts.Unix(), 10), ttl).Err(); err != nil {
		return fmt.Errorf("failed to set last_seen key: %w", err)
	}
	return nil
}

// GetDeviceLastSeen returns the last seen timestamp for a device.
func (c *TelemetryCache) GetDeviceLastSeen(ctx context.Context, deviceID string) (time.Time, error) {
	if c.client == nil {
		return time.Time{}, nil
	}
	key := fmt.Sprintf("last_seen:%s", deviceID)
	value, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last_seen key: %w", err)
	}

	unixSeconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid last_seen value: %w", err)
	}

	return time.Unix(unixSeconds, 0).UTC(), nil
}
