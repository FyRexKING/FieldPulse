package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type SimulationConfig struct {
	BrokerURL       string
	NumDevices      int
	MinIntervalSec  int
	MaxIntervalSec  int
	DuplicateRate   float64
	SpikeRate       float64
	DurationSeconds int
}

type telemetryPayload struct {
	MetricName           string            `json:"metric_name"`
	Value                float64           `json:"value"`
	TimestampUnixSeconds int64             `json:"timestamp_unix_seconds"`
	Status               string            `json:"status"`
	Tags                 map[string]string `json:"tags"`
}

type deviceWorker struct {
	deviceID     string
	client       mqtt.Client
	cfg          SimulationConfig
	rng          *rand.Rand
	lastTemp     float64
	sentCount    int64
	anomalyCount int64
	dupCount     int64
	stopCh       <-chan struct{}
}

func main() {
	cfg := SimulationConfig{
		BrokerURL:       "tcp://localhost:1883",
		NumDevices:      50,
		MinIntervalSec:  2,
		MaxIntervalSec:  10,
		DuplicateRate:   0.03,
		SpikeRate:       0.05,
		DurationSeconds: 0,
	}

	flag.StringVar(&cfg.BrokerURL, "broker", cfg.BrokerURL, "MQTT broker URL")
	flag.IntVar(&cfg.NumDevices, "devices", cfg.NumDevices, "Number of device goroutines")
	flag.IntVar(&cfg.MinIntervalSec, "min-interval", cfg.MinIntervalSec, "Minimum publish interval in seconds")
	flag.IntVar(&cfg.MaxIntervalSec, "max-interval", cfg.MaxIntervalSec, "Maximum publish interval in seconds")
	flag.Float64Var(&cfg.DuplicateRate, "duplicates", cfg.DuplicateRate, "Duplicate publish probability (0-1)")
	flag.Float64Var(&cfg.SpikeRate, "anomalies", cfg.SpikeRate, "Anomaly spike probability (0-1)")
	flag.IntVar(&cfg.DurationSeconds, "duration", cfg.DurationSeconds, "Run duration in seconds (0=infinite)")
	flag.Parse()

	if cfg.MinIntervalSec <= 0 || cfg.MaxIntervalSec < cfg.MinIntervalSec {
		log.Fatal("invalid interval configuration")
	}

	client, err := connectMQTT(cfg.BrokerURL)
	if err != nil {
		log.Fatalf("failed to connect MQTT broker: %v", err)
	}
	defer client.Disconnect(250)

	log.Printf("✓ simulator connected to %s", cfg.BrokerURL)
	log.Printf("starting %d device workers", cfg.NumDevices)

	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	var totalSent int64
	var totalAnomalies int64
	var totalDup int64

	for i := 0; i < cfg.NumDevices; i++ {
		worker := &deviceWorker{
			deviceID: fmt.Sprintf("sim_dev_%03d", i),
			client:   client,
			cfg:      cfg,
			rng:      rand.New(rand.NewSource(time.Now().UnixNano() + int64(i)*7919)),
			lastTemp: 25.0,
			stopCh:   stopCh,
		}

		wg.Add(1)
		go func(w *deviceWorker) {
			defer wg.Done()
			w.run()
			atomic.AddInt64(&totalSent, atomic.LoadInt64(&w.sentCount))
			atomic.AddInt64(&totalAnomalies, atomic.LoadInt64(&w.anomalyCount))
			atomic.AddInt64(&totalDup, atomic.LoadInt64(&w.dupCount))
		}(worker)
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	var timeout <-chan time.Time
	if cfg.DurationSeconds > 0 {
		timeout = time.After(time.Duration(cfg.DurationSeconds) * time.Second)
	}

	for {
		select {
		case <-ticker.C:
			log.Printf("simulator running: devices=%d", cfg.NumDevices)
		case <-timeout:
			close(stopCh)
			wg.Wait()
			log.Printf("done: sent=%d anomalies=%d duplicates=%d", totalSent, totalAnomalies, totalDup)
			return
		case sig := <-sigs:
			log.Printf("received signal %v", sig)
			close(stopCh)
			wg.Wait()
			log.Printf("done: sent=%d anomalies=%d duplicates=%d", totalSent, totalAnomalies, totalDup)
			return
		}
	}
}

func connectMQTT(brokerURL string) (mqtt.Client, error) {
	opts := mqtt.NewClientOptions().AddBroker(brokerURL)
	opts.SetClientID(fmt.Sprintf("fieldpulse-simulator-%d", time.Now().UnixNano()))
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)

	client := mqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if token.Error() != nil {
		return nil, token.Error()
	}
	return client, nil
}

func (w *deviceWorker) run() {
	for {
		select {
		case <-w.stopCh:
			return
		default:
			w.publishOne()
			time.Sleep(w.nextInterval())
		}
	}
}

func (w *deviceWorker) publishOne() {
	temp := w.nextTemperature()
	payload := telemetryPayload{
		MetricName:           "temperature",
		Value:                temp,
		TimestampUnixSeconds: time.Now().UTC().Unix(),
		Status:               "OK",
		Tags: map[string]string{
			"source": "simulator",
		},
	}

	if w.rng.Float64() < w.cfg.SpikeRate {
		payload.Value = temp + (10 + w.rng.Float64()*15)
		payload.Status = "CRITICAL"
		atomic.AddInt64(&w.anomalyCount, 1)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}

	topic := fmt.Sprintf("devices/%s/telemetry", w.deviceID)
	if err := publishQoS1(w.client, topic, raw); err != nil {
		log.Printf("publish failed for %s: %v", w.deviceID, err)
		return
	}
	atomic.AddInt64(&w.sentCount, 1)

	if w.rng.Float64() < w.cfg.DuplicateRate {
		if err := publishQoS1(w.client, topic, raw); err == nil {
			atomic.AddInt64(&w.dupCount, 1)
		}
	}
}

func (w *deviceWorker) nextTemperature() float64 {
	drift := (25.0 - w.lastTemp) * 0.05
	noise := (w.rng.Float64() - 0.5) * 1.2
	w.lastTemp = w.lastTemp + drift + noise
	return w.lastTemp
}

func (w *deviceWorker) nextInterval() time.Duration {
	if w.cfg.MinIntervalSec == w.cfg.MaxIntervalSec {
		return time.Duration(w.cfg.MinIntervalSec) * time.Second
	}
	rangeSec := w.cfg.MaxIntervalSec - w.cfg.MinIntervalSec + 1
	sec := w.cfg.MinIntervalSec + w.rng.Intn(rangeSec)
	return time.Duration(sec) * time.Second
}

func publishQoS1(client mqtt.Client, topic string, payload []byte) error {
	token := client.Publish(topic, 1, false, payload)
	token.Wait()
	return token.Error()
}
