package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v2"
)

type ConnectorConfig struct {
	Brokers          []BrokerConfig       `yaml:"brokers"`
	TelemetryService TelemetryServiceConf `yaml:"telemetry_service"`
}

type TelemetryServiceConf struct {
	Address       string `yaml:"address"`
	TimeoutMS     int    `yaml:"timeout_ms"`
	RetryAttempts int    `yaml:"retry_attempts"`
}

type BrokerConfig struct {
	Name             string               `yaml:"name"`
	BrokerURI        string               `yaml:"broker_uri"`
	Username         string               `yaml:"username"`
	Password         string               `yaml:"password"`
	ClientID         string               `yaml:"client_id"`
	BufferSize       int                  `yaml:"buffer_size"`
	ReconnectDelayMS int                  `yaml:"reconnect_delay_ms"`
	Subscriptions    []SubscriptionConfig `yaml:"subscriptions"`
}

type SubscriptionConfig struct {
	Topic           string `yaml:"topic"`
	QoS             byte   `yaml:"qos"`
	DeviceIDPath    string `yaml:"device_id_path"`
	DeviceIDSegment int    `yaml:"device_id_segment"`
	MetricName      string `yaml:"metric_name"`
}

type inboundMessage struct {
	brokerName    string
	subscription  SubscriptionConfig
	topic         string
	payload       []byte
	receivedAt    time.Time
}

type payloadModel struct {
	MetricName           string            `json:"metric_name"`
	Value                float64           `json:"value"`
	TimestampUnixSeconds int64             `json:"timestamp_unix_seconds"`
	Status               string            `json:"status"`
	Tags                 map[string]string `json:"tags"`
}

type brokerWorker struct {
	cfg       BrokerConfig
	telemetry pb.TelemetryServiceClient
	timeout   time.Duration
	queue     chan inboundMessage
	client    mqtt.Client
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	recv      int64
	fwd       int64
	dropped   int64
}

func main() {
	if err := otel.InitTracing("connector"); err != nil {
		log.Printf("⚠️ tracing initialization failed: %v", err)
	}

	cfgPath := resolveConfigPath()
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	conn, err := grpc.Dial(cfg.TelemetryService.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect telemetry service: %v", err)
	}
	defer conn.Close()

	telemetryClient := pb.NewTelemetryServiceClient(conn)
	log.Printf("✓ connected to telemetry service at %s", cfg.TelemetryService.Address)

	workers := make([]*brokerWorker, 0, len(cfg.Brokers))
	for _, brokerCfg := range cfg.Brokers {
		w := newBrokerWorker(brokerCfg, telemetryClient, cfg.TelemetryService.TimeoutMS)
		if err := w.start(); err != nil {
			log.Printf("⚠️ broker %s start failed: %v", brokerCfg.Name, err)
			continue
		}
		workers = append(workers, w)
	}

	if len(workers) == 0 {
		log.Fatal("no broker workers started")
	}

	statsTick := time.NewTicker(10 * time.Second)
	defer statsTick.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-statsTick.C:
			for _, w := range workers {
				log.Printf("broker=%s recv=%d fwd=%d dropped=%d", w.cfg.Name, atomic.LoadInt64(&w.recv), atomic.LoadInt64(&w.fwd), atomic.LoadInt64(&w.dropped))
			}
		case sig := <-sigCh:
			log.Printf("received signal %v, shutting down", sig)
			for _, w := range workers {
				w.stop()
			}
			return
		}
	}
}

func resolveConfigPath() string {
	if fromEnv := os.Getenv("MQTT_REMOTE_CONFIG"); fromEnv != "" {
		return fromEnv
	}
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	if _, err := os.Stat("connector-config.yml"); err == nil {
		return "connector-config.yml"
	}
	return "connector-config.yaml"
}

func loadConfig(path string) (ConnectorConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ConnectorConfig{}, err
	}

	var cfg ConnectorConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return ConnectorConfig{}, err
	}

	if cfg.TelemetryService.Address == "" {
		cfg.TelemetryService.Address = "telemetry-service:50052"
	}
	if cfg.TelemetryService.TimeoutMS <= 0 {
		cfg.TelemetryService.TimeoutMS = 5000
	}

	for i := range cfg.Brokers {
		if cfg.Brokers[i].BufferSize <= 0 {
			cfg.Brokers[i].BufferSize = 1000
		}
		if cfg.Brokers[i].ReconnectDelayMS <= 0 {
			cfg.Brokers[i].ReconnectDelayMS = 5000
		}
	}

	return cfg, nil
}

func newBrokerWorker(cfg BrokerConfig, telemetry pb.TelemetryServiceClient, timeoutMS int) *brokerWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &brokerWorker{
		cfg:       cfg,
		telemetry: telemetry,
		timeout:   time.Duration(timeoutMS) * time.Millisecond,
		queue:     make(chan inboundMessage, cfg.BufferSize),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (w *brokerWorker) start() error {
	opts := mqtt.NewClientOptions().AddBroker(w.cfg.BrokerURI)
	opts.SetClientID(w.cfg.ClientID)
	opts.SetUsername(w.cfg.Username)
	opts.SetPassword(w.cfg.Password)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetMaxReconnectInterval(time.Duration(w.cfg.ReconnectDelayMS) * time.Millisecond)
	
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("⚠️ broker %s connection lost: %v", w.cfg.Name, err)
	})

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		for _, sub := range w.cfg.Subscriptions {
			subCopy := sub
			handler := func(_ mqtt.Client, msg mqtt.Message) {
				atomic.AddInt64(&w.recv, 1)
				w.enqueue(inboundMessage{
					brokerName:   w.cfg.Name,
					subscription: subCopy,
					topic:        msg.Topic(),
					payload:      append([]byte(nil), msg.Payload()...),
					receivedAt:   time.Now().UTC(),
				})
			}
			token := c.Subscribe(sub.Topic, sub.QoS, handler)
			token.Wait()
			if token.Error() != nil {
				log.Printf("❌ broker %s subscribe %s failed: %v", w.cfg.Name, sub.Topic, token.Error())
			} else {
				log.Printf("✓ broker %s subscribed %s", w.cfg.Name, sub.Topic)
			}
		}
	})

	w.client = mqtt.NewClient(opts)
	token := w.client.Connect()
	token.Wait()
	if token.Error() != nil {
		return token.Error()
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.processLoop()
	}()

	return nil
}

func (w *brokerWorker) stop() {
	w.cancel()
	w.wg.Wait()
	if w.client != nil && w.client.IsConnected() {
		w.client.Disconnect(250)
	}
}

func (w *brokerWorker) enqueue(msg inboundMessage) {
	select {
	case w.queue <- msg:
	default:
		// Drop oldest to keep freshest telemetry as requested.
		select {
		case <-w.queue:
			atomic.AddInt64(&w.dropped, 1)
		default:
		}
		select {
		case w.queue <- msg:
		default:
			atomic.AddInt64(&w.dropped, 1)
		}
	}
}

func (w *brokerWorker) processLoop() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case msg := <-w.queue:
			if err := w.forward(msg); err != nil {
				log.Printf("⚠️ broker %s forward failed: %v", w.cfg.Name, err)
			}
		}
	}
}

func (w *brokerWorker) forward(msg inboundMessage) error {
	deviceID, err := extractDeviceID(msg.subscription, msg.topic)
	if err != nil {
		// safer policy: drop unknown/unparseable identity.
		return fmt.Errorf("device id extraction failed for topic %s: %w", msg.topic, err)
	}

	payload, err := parsePayloadStrict(msg.payload)
	if err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	metricName := payload.MetricName
	if metricName == "" {
		metricName = msg.subscription.MetricName
	}
	if metricName == "" {
		metricName = "external_metric"
	}

	timestamp := payload.TimestampUnixSeconds
	if timestamp == 0 {
		timestamp = msg.receivedAt.Unix()
	}

	tags := payload.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	tags["connector_broker"] = msg.brokerName
	tags["connector_topic"] = msg.topic

	ctx, cancel := context.WithTimeout(w.ctx, w.timeout)
	defer cancel()

	// Trace the connector forward operation
	ctx, endSpan := otel.TraceConnectorForward(ctx, msg.brokerName, msg.topic, deviceID, metricName)
	defer endSpan()

	_, err = w.telemetry.SubmitMetric(ctx, &pb.SubmitMetricRequest{
		DeviceId:             deviceID,
		MetricName:           metricName,
		Value:                payload.Value,
		TimestampUnixSeconds: timestamp,
		Status:               toMetricStatus(payload.Status),
		Tags:                 tags,
	})
	if err != nil {
		return err
	}

	atomic.AddInt64(&w.fwd, 1)
	return nil
}

func parsePayloadStrict(raw []byte) (*payloadModel, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var payload payloadModel
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Value != payload.Value {
		return nil, fmt.Errorf("value must be finite")
	}
	return &payload, nil
}

func extractDeviceID(sub SubscriptionConfig, topic string) (string, error) {
	if strings.HasPrefix(strings.ToLower(sub.DeviceIDPath), "segment:") {
		i, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(sub.DeviceIDPath), "segment:"))
		if err != nil {
			return "", err
		}
		return extractTopicSegment(topic, i)
	}

	if sub.DeviceIDSegment > 0 {
		return extractTopicSegment(topic, sub.DeviceIDSegment)
	}

	// Default for common pattern: devices/{id}/...
	return extractTopicSegment(topic, 1)
}

func extractTopicSegment(topic string, idx int) (string, error) {
	parts := strings.Split(topic, "/")
	if idx < 0 || idx >= len(parts) || parts[idx] == "" {
		return "", fmt.Errorf("invalid segment index %d for topic %s", idx, topic)
	}
	return parts[idx], nil
}

func toMetricStatus(statusText string) pb.MetricStatus {
	switch strings.ToUpper(strings.TrimSpace(statusText)) {
	case "CRITICAL", "METRIC_STATUS_CRITICAL":
		return pb.MetricStatus_METRIC_STATUS_CRITICAL
	case "WARNING", "METRIC_STATUS_WARNING":
		return pb.MetricStatus_METRIC_STATUS_WARNING
	default:
		return pb.MetricStatus_METRIC_STATUS_OK
	}
}
