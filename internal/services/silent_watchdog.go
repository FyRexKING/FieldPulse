package services

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	pb "fieldpulse.io/api/proto"
	"fieldpulse.io/internal/mqtt"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	silentWatchInterval = 2 * time.Minute
	silentMaxAge        = 15 * time.Minute
)

// StartSilentWatchdog periodically checks Redis last_seen:* keys; if idle >15m, marks device SILENT via gRPC and publishes MQTT (planning.md §4.3).
func StartSilentWatchdog(ctx context.Context, redis *redis.Client, deviceServiceAddr string, mqttPub *mqtt.AlertPublisher) {
	if redis == nil || deviceServiceAddr == "" {
		return
	}
	conn, err := grpc.Dial(deviceServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("silent watchdog: grpc dial: %v", err)
		return
	}
	go func() {
		defer conn.Close()
		client := pb.NewDeviceServiceClient(conn)
		ticker := time.NewTicker(silentWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runSilentScan(ctx, redis, client, mqttPub)
			}
		}
	}()
}

func runSilentScan(ctx context.Context, r *redis.Client, client pb.DeviceServiceClient, mqttPub *mqtt.AlertPublisher) {
	scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var cursor uint64
	for {
		keys, next, err := r.Scan(scanCtx, cursor, "last_seen:*", 128).Result()
		if err != nil {
			log.Printf("silent watchdog: redis scan: %v", err)
			return
		}
		for _, key := range keys {
			deviceID := strings.TrimPrefix(key, "last_seen:")
			if deviceID == "" {
				continue
			}
			val, err := r.Get(scanCtx, key).Result()
			if err != nil {
				continue
			}
			unixSec, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				continue
			}
			last := time.Unix(unixSec, 0).UTC()
			if time.Since(last) <= silentMaxAge {
				continue
			}
			gd, err := client.GetDevice(scanCtx, &pb.GetDeviceRequest{DeviceId: deviceID})
			if err != nil || gd.Device == nil {
				continue
			}
			if gd.Device.Status == pb.DeviceStatus_DEVICE_STATUS_SILENT {
				continue
			}
			_, err = client.UpdateDeviceStatus(scanCtx, &pb.UpdateDeviceStatusRequest{
				DeviceId:  deviceID,
				NewStatus: pb.DeviceStatus_DEVICE_STATUS_SILENT,
			})
			if err != nil {
				log.Printf("silent watchdog: UpdateDeviceStatus %s: %v", deviceID, err)
				continue
			}
			if err := PublishSilentDeviceEvent(mqttPub, deviceID); err != nil {
				log.Printf("silent watchdog: mqtt %s: %v", deviceID, err)
			}
			log.Printf("silent watchdog: marked device %s SILENT (last_seen %v)", deviceID, last)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
}
