// Command publisher adalah simulator yang mempublikasikan data mock lokasi
// kendaraan ke broker MQTT setiap 2 detik.
//
// Rute geraknya bersifat deterministik dan memang dirancang untuk MELINTASI
// batas geofence: setiap kendaraan bergerak bolak-balik (shuttle) melewati
// titik pusat geofence sepanjang satu garis lurus, sehingga jaraknya ke pusat
// berosilasi antara 0 dan 2,5 x radius. Akibatnya transisi masuk/keluar area
// terjadi secara terjadwal dan dapat diamati, bukan bergantung pada
// kebetulan drift acak.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"transjakarta-fleet/internal/config"
	"transjakarta-fleet/internal/domain"
	"transjakarta-fleet/internal/wait"
)

const (
	// metersPerDegreeLat adalah panjang satu derajat lintang (meter).
	metersPerDegreeLat = 111320.0
	// routeAmplitudeFactor adalah pengali radius untuk amplitudo rute.
	// 2,5 berarti jarak ke pusat berosilasi 0 .. 2,5 x radius sehingga batas
	// radius pasti dilalui dua kali dalam satu periode.
	routeAmplitudeFactor = 2.5
)

func main() {
	cfg := config.Load()

	broker := cfg.MQTTBroker
	vehicles := cfg.PublisherVehicles
	interval := cfg.PublisherIntervalSeconds
	period := float64(cfg.PublisherRoutePeriodSeconds)

	if len(vehicles) == 0 {
		log.Fatalf("PUBLISHER_VEHICLES tidak boleh kosong")
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(fmt.Sprintf("publisher-%d", time.Now().UnixNano()))
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetConnectRetry(false)
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(10 * time.Second)
	opts.SetKeepAlive(30 * time.Second)

	client := paho.NewClient(opts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Broker MQTT mungkin belum siap: coba berulang, jangan langsung panic.
	waitOpts := wait.Options{
		Timeout:  cfg.DependencyTimeout(),
		Interval: time.Second,
		Log:      func(format string, args ...any) { log.Printf(format, args...) },
	}
	if err := wait.For(ctx, "mqtt broker", waitOpts, func() error {
		token := client.Connect()
		token.Wait()
		return token.Error()
	}); err != nil {
		log.Fatalf("failed to connect to mqtt broker %s: %v", broker, err)
	}
	defer client.Disconnect(250)

	fmt.Printf("[PUBLISHER] Connected to %s, interval %ds, rute melewati geofence (%.0fm) tiap %ds\n",
		broker, interval, cfg.GeofenceRadiusM, cfg.PublisherRoutePeriodSeconds)

	// Timestamp monotonik per kendaraan: "lokasi terakhir" ditentukan oleh
	// timestamp, jadi nilainya tidak boleh pernah sama/mundur.
	lastTimestamp := make(map[string]int64, len(vehicles))

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	// Rute dihitung dari waktu sejak publisher mulai (bukan waktu absolut),
	// supaya geraknya deterministik dan selalu melewati batas geofence.
	start := time.Now()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-signals:
			fmt.Println("[PUBLISHER] Shutting down...")
			return
		case <-ctx.Done():
			return
		case tick := <-ticker.C:
			elapsed := tick.Sub(start).Seconds()

			for i, vehicleID := range vehicles {
				lat, lng := routePosition(
					cfg.GeofenceLat,
					cfg.GeofenceLng,
					cfg.GeofenceRadiusM,
					cfg.PublisherRouteBearingDeg+float64(i)*cfg.PublisherRouteSpreadDeg,
					period,
					phaseFor(i, len(vehicles)),
					elapsed,
				)

				timestamp := time.Now().Unix()
				if last, ok := lastTimestamp[vehicleID]; ok && timestamp <= last {
					timestamp = last + 1
				}
				lastTimestamp[vehicleID] = timestamp

				payload := domain.LocationPayload{
					VehicleID: vehicleID,
					Latitude:  lat,
					Longitude: lng,
					Timestamp: timestamp,
				}
				data, err := json.Marshal(payload)
				if err != nil {
					fmt.Printf("[PUBLISHER] marshal error: %v\n", err)
					continue
				}

				topic := fmt.Sprintf("/fleet/vehicle/%s/location", payload.VehicleID)
				token := client.Publish(topic, 0, false, data)
				token.Wait()
				if token.Error() != nil {
					fmt.Printf("[PUBLISHER] publish failed: %v\n", token.Error())
					continue
				}
				trace := fmt.Sprintf("%s-%d", payload.VehicleID, payload.Timestamp)
				fmt.Printf("[PUBLISHER] trace=%s published to %s: lat=%f lng=%f\n",
					trace, topic, payload.Latitude, payload.Longitude)
			}
		}
	}
}

// phaseFor membagi fase antar kendaraan agar tidak bergerak berbaris di
// titik yang sama.
func phaseFor(index, total int) float64 {
	if total <= 1 {
		return 0
	}
	return 2 * math.Pi * float64(index) / float64(total)
}

// routePosition menghitung koordinat kendaraan pada waktu tertentu.
//
// Vehicles bergerak pada satu garis lurus melalui titik pusat geofence dengan
// bearing tertentu. Jarak bertanda dari pusat adalah:
//
//	d(t) = A * sin(2*pi*t/T + fase),  A = 2,5 * radius
//
// Jarak sebenarnya ke pusat adalah |d(t)|, sehingga vehicles pasti melewati
// radius R (saat |d| melewati R) dua kali dalam satu periode.
func routePosition(centerLat, centerLng, radiusM, bearingDeg, periodSec, phase, elapsedSec float64) (lat, lng float64) {
	amplitude := routeAmplitudeFactor * radiusM
	theta := 2*math.Pi*elapsedSec/periodSec + phase
	distance := amplitude * math.Sin(theta)

	bearing := bearingDeg * math.Pi / 180
	lat = centerLat + (distance*math.Cos(bearing))/metersPerDegreeLat
	lng = centerLng + (distance*math.Sin(bearing))/(metersPerDegreeLat*math.Cos(centerLat*math.Pi/180))
	return lat, lng
}
