package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"fieldpulse.io/internal/db"
	"fieldpulse.io/internal/mqtt"
	"fieldpulse.io/internal/otel"

	"github.com/redis/go-redis/v9"
)

const (
	alertSilenceRedisTTL = 5 * time.Minute
)

// AlertEvaluator evaluates incoming metrics against thresholds and fires alerts.
// Runs async to avoid blocking metric ingestion.
// planning.md §4.3: Redis key alert:{device_id} suppresses duplicate alerts for 5m; MQTT publishes to alerts/{device_id}.
type AlertEvaluator struct {
	alertDB   *db.AlertDB
	redis     *redis.Client
	mqttPub   *mqtt.AlertPublisher
	lastValues   map[string]float64 // device:metric -> last_value for CHANGE detection
	lastValuesMu sync.RWMutex
}

// NewAlertEvaluator creates a new alert evaluator. redis and mqttPub may be nil (alerts still evaluated; MQTT/Redis silence skipped).
func NewAlertEvaluator(alertDB *db.AlertDB, redis *redis.Client, mqttPub *mqtt.AlertPublisher) *AlertEvaluator {
	return &AlertEvaluator{
		alertDB:    alertDB,
		redis:      redis,
		mqttPub:    mqttPub,
		lastValues: make(map[string]float64),
	}
}

// EvaluateAsync evaluates a metric against thresholds without blocking.
func (e *AlertEvaluator) EvaluateAsync(ctx context.Context, deviceID, metricName string, value float64) {
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		e.evaluate(ctx, deviceID, metricName, value)
	}()
}

func (e *AlertEvaluator) evaluate(ctx context.Context, deviceID, metricName string, value float64) {
	newCtx, endSpan := otel.TraceAlertEvaluation(ctx, deviceID, metricName)
	defer endSpan()

	thresholds, err := e.alertDB.GetThresholds(newCtx, deviceID, metricName, true)
	if err != nil || len(thresholds) == 0 {
		return
	}

	key := fmt.Sprintf("%s:%s", deviceID, metricName)
	silences, _ := e.alertDB.GetActiveSilences(ctx, deviceID)
	isDBSilenced := e.checkSilenced(metricName, silences)

	for _, t := range thresholds {
		triggered := false
		message := ""

		switch t.Type {
		case "HIGH":
			if value > t.UpperBound {
				triggered = true
				message = fmt.Sprintf("%s exceeded %.2f (value: %.2f)", metricName, t.UpperBound, value)
			}
		case "LOW":
			if value < t.LowerBound {
				triggered = true
				message = fmt.Sprintf("%s dropped below %.2f (value: %.2f)", metricName, t.LowerBound, value)
			}
		case "RANGE":
			if value < t.LowerBound || value > t.UpperBound {
				triggered = true
				message = fmt.Sprintf("%s out of range [%.2f, %.2f]", metricName, t.LowerBound, t.UpperBound)
			}
		case "CHANGE":
			lastVal, exists := e.getLastValue(key)
			if exists && lastVal != 0 {
				percentChange := ((value - lastVal) / lastVal) * 100
				if percentChange > t.ChangePercent || percentChange < -t.ChangePercent {
					triggered = true
					message = fmt.Sprintf("%s changed %.1f%% (from %.2f to %.2f)", metricName, percentChange, lastVal, value)
				}
			}
			e.setLastValue(key, value)
		}

		if !triggered {
			continue
		}

		thresholdVal := t.UpperBound
		if t.UpperBound == 0 {
			thresholdVal = t.LowerBound
		}

		alertID := fmt.Sprintf("alr_%d_%s", time.Now().UnixNano(), deviceID)
		alert := db.Alert{
			AlertID:        alertID,
			DeviceID:       deviceID,
			MetricName:     metricName,
			MetricValue:    value,
			ThresholdType:  t.Type,
			ThresholdValue: thresholdVal,
			Severity:       t.Severity,
			Message:        message,
			IsSilenced:     isDBSilenced,
			TriggeredAt:    time.Now().UTC(),
		}

		if isDBSilenced {
			if err := e.alertDB.InsertAlert(ctx, alert); err != nil {
				log.Printf("alert: insert silenced alert: %v", err)
			}
			continue
		}

		if e.redisAlertSuppressed(ctx, deviceID) {
			continue
		}

		if err := e.alertDB.InsertAlert(ctx, alert); err != nil {
			log.Printf("alert: insert failed: %v", err)
			continue
		}

		e.publishAlertMQTT(deviceID, alert)

		if e.redis != nil {
			if err := e.redis.Set(ctx, fmt.Sprintf("alert:%s", deviceID), "1", alertSilenceRedisTTL).Err(); err != nil {
				log.Printf("alert: redis silence key: %v", err)
			}
		}
	}
}

func (e *AlertEvaluator) redisAlertSuppressed(ctx context.Context, deviceID string) bool {
	if e.redis == nil {
		return false
	}
	_, err := e.redis.Get(ctx, fmt.Sprintf("alert:%s", deviceID)).Result()
	if err == redis.Nil {
		return false
	}
	if err != nil {
		log.Printf("alert: redis get alert:%s: %v", deviceID, err)
		return false
	}
	return true
}

func (e *AlertEvaluator) publishAlertMQTT(deviceID string, alert db.Alert) {
	if e.mqttPub == nil {
		return
	}
	msg := mqtt.AlertMQTTMessage{
		AlertID:     alert.AlertID,
		DeviceID:    alert.DeviceID,
		MetricName:  alert.MetricName,
		Message:     alert.Message,
		Severity:    alert.Severity,
		MetricValue: alert.MetricValue,
		TriggeredAt: alert.TriggeredAt,
	}
	if err := e.mqttPub.PublishAlert(deviceID, msg); err != nil {
		log.Printf("alert: mqtt publish: %v", err)
	}
}

func (e *AlertEvaluator) checkSilenced(metricName string, silences []db.SilenceRule) bool {
	for _, s := range silences {
		if s.MetricName == nil || *s.MetricName == metricName {
			return true
		}
	}
	return false
}

func (e *AlertEvaluator) getLastValue(key string) (float64, bool) {
	e.lastValuesMu.RLock()
	defer e.lastValuesMu.RUnlock()
	val, exists := e.lastValues[key]
	return val, exists
}

func (e *AlertEvaluator) setLastValue(key string, value float64) {
	e.lastValuesMu.Lock()
	defer e.lastValuesMu.Unlock()
	e.lastValues[key] = value
}

// SilentStatusPayload is published when a device is marked silent (planning.md §4.3).
type SilentStatusPayload struct {
	DeviceID  string    `json:"device_id"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// PublishSilentDeviceEvent publishes to devices/{deviceID}/status (JSON).
func PublishSilentDeviceEvent(pub *mqtt.AlertPublisher, deviceID string) error {
	if pub == nil {
		return nil
	}
	p := SilentStatusPayload{
		DeviceID:  deviceID,
		Status:    "silent",
		Reason:    "no_telemetry_exceeds_threshold",
		Timestamp: time.Now().UTC(),
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	topic := fmt.Sprintf("devices/%s/status", deviceID)
	return pub.PublishRaw(topic, b)
}
