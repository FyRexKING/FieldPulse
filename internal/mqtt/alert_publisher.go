package mqtt

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// AlertPublisher publishes alert payloads to MQTT (planning.md §4.3: topic alerts/{device_id}).
type AlertPublisher struct {
	client mqtt.Client
}

// AlertMQTTMessage is the JSON body published for downstream consumers.
type AlertMQTTMessage struct {
	AlertID     string    `json:"alert_id"`
	DeviceID    string    `json:"device_id"`
	MetricName  string    `json:"metric_name"`
	Message     string    `json:"message"`
	Severity    string    `json:"severity"`
	MetricValue float64   `json:"metric_value"`
	TriggeredAt time.Time `json:"triggered_at"`
}

// NewAlertPublisher connects a dedicated MQTT client for outbound alerts.
func NewAlertPublisher(brokerURL, clientID string) (*AlertPublisher, error) {
	if brokerURL == "" {
		return nil, fmt.Errorf("mqtt broker URL required")
	}
	opts := mqtt.NewClientOptions().AddBroker(brokerURL)
	if clientID == "" {
		clientID = "fieldpulse-alert-publisher"
	}
	opts.SetClientID(clientID + "-pub")
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("alert publisher connection lost: %v", err)
	})
	c := mqtt.NewClient(opts)
	token := c.Connect()
	token.Wait()
	if token.Error() != nil {
		return nil, fmt.Errorf("mqtt alert publisher connect: %w", token.Error())
	}
	return &AlertPublisher{client: c}, nil
}

// PublishAlert publishes QoS 1 to alerts/{deviceID}.
func (p *AlertPublisher) PublishAlert(deviceID string, msg AlertMQTTMessage) error {
	if p == nil || p.client == nil {
		return nil
	}
	topic := fmt.Sprintf("alerts/%s", deviceID)
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tok := p.client.Publish(topic, 1, false, body)
	tok.Wait()
	return tok.Error()
}

// PublishRaw publishes arbitrary payload to a topic (e.g. device status / silent detection).
func (p *AlertPublisher) PublishRaw(topic string, payload []byte) error {
	if p == nil || p.client == nil {
		return nil
	}
	tok := p.client.Publish(topic, 1, false, payload)
	tok.Wait()
	return tok.Error()
}

// Disconnect closes the MQTT client.
func (p *AlertPublisher) Disconnect() {
	if p != nil && p.client != nil && p.client.IsConnected() {
		p.client.Disconnect(250)
	}
}
