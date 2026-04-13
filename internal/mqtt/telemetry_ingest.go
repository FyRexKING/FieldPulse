package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	pb "fieldpulse.io/api/proto"
)

const submitTimeout = 5 * time.Second

// TelemetrySubmitter is implemented by telemetry gRPC service.
type TelemetrySubmitter interface {
	SubmitMetric(ctx context.Context, req *pb.SubmitMetricRequest) (*pb.SubmitMetricResponse, error)
}

// SubscriberConfig controls MQTT telemetry subscriber behavior.
type SubscriberConfig struct {
	BrokerURL string
	Topic     string
	ClientID  string
	QoS       byte
}

// TelemetrySubscriber consumes MQTT telemetry and forwards to telemetry service.
type TelemetrySubscriber struct {
	cfg       SubscriberConfig
	submitter TelemetrySubmitter
	client    mqtt.Client
}

type mqttTelemetryPayload struct {
	MetricName           string            `json:"metric_name"`
	Value                float64           `json:"value"`
	TimestampUnixSeconds int64             `json:"timestamp_unix_seconds"`
	Status               string            `json:"status"`
	Tags                 map[string]string `json:"tags"`
}

func NewTelemetrySubscriber(cfg SubscriberConfig, submitter TelemetrySubmitter) *TelemetrySubscriber {
	if cfg.Topic == "" {
		cfg.Topic = "devices/+/telemetry"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "fieldpulse-telemetry-subscriber"
	}
	if cfg.QoS == 0 {
		cfg.QoS = 1
	}
	if cfg.QoS > 2 {
		cfg.QoS = 1
	}

	return &TelemetrySubscriber{cfg: cfg, submitter: submitter}
}

func (s *TelemetrySubscriber) Start(ctx context.Context) error {
	opts := mqtt.NewClientOptions().AddBroker(s.cfg.BrokerURL)
	opts.SetClientID(s.cfg.ClientID)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("⚠️ telemetry subscriber connection lost: %v", err)
	})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		token := c.Subscribe(s.cfg.Topic, s.cfg.QoS, s.handleMessage)
		token.Wait()
		if token.Error() != nil {
			log.Printf("❌ telemetry subscriber subscribe failed: %v", token.Error())
			return
		}
		log.Printf("✓ telemetry subscriber connected and subscribed to %s", s.cfg.Topic)
	})

	s.client = mqtt.NewClient(opts)
	token := s.client.Connect()
	token.Wait()
	if token.Error() != nil {
		return fmt.Errorf("mqtt connect failed: %w", token.Error())
	}

	_ = ctx
	return nil
}

func (s *TelemetrySubscriber) Stop() {
	if s.client != nil && s.client.IsConnected() {
		s.client.Disconnect(250)
	}
}

func (s *TelemetrySubscriber) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	deviceID, ok := deviceIDFromTopic(msg.Topic())
	if !ok {
		log.Printf("⚠️ rejecting message: invalid topic %s", msg.Topic())
		return
	}

	payload, err := parsePayloadStrict(msg.Payload())
	if err != nil {
		log.Printf("⚠️ rejecting message for %s: invalid payload: %v", deviceID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	defer cancel()

	_, err = s.submitter.SubmitMetric(ctx, &pb.SubmitMetricRequest{
		DeviceId:             deviceID,
		MetricName:           payload.MetricName,
		Value:                payload.Value,
		TimestampUnixSeconds: payload.TimestampUnixSeconds,
		Status:               parseStatus(payload.Status),
		Tags:                 payload.Tags,
	})
	if err != nil {
		log.Printf("⚠️ failed to submit mqtt metric for %s: %v", deviceID, err)
	}
}

func parsePayloadStrict(raw []byte) (*mqttTelemetryPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var payload mqttTelemetryPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload.MetricName == "" {
		return nil, fmt.Errorf("metric_name required")
	}
	if payload.Tags == nil {
		payload.Tags = map[string]string{}
	}
	return &payload, nil
}

func deviceIDFromTopic(topic string) (string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 3 {
		return "", false
	}
	if parts[0] != "devices" || parts[2] != "telemetry" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func parseStatus(value string) pb.MetricStatus {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "WARNING", "METRIC_STATUS_WARNING":
		return pb.MetricStatus_METRIC_STATUS_WARNING
	case "CRITICAL", "METRIC_STATUS_CRITICAL":
		return pb.MetricStatus_METRIC_STATUS_CRITICAL
	default:
		return pb.MetricStatus_METRIC_STATUS_OK
	}
}
