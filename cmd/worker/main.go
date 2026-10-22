// Command worker adalah service yang mengonsumsi queue RabbitMQ
// "geofence_alerts", lalu mendokumentasikan setiap event geofence ke
// PostgreSQL sehingga dashboard pada service API tetap bisa menampilkannya.
//
// Pesan memakai manual ack: event yang gagal diproses di-requeue satu kali,
// lalu dibuang agar tidak berputar tanpa henti.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"transjakarta-fleet/internal/config"
	"transjakarta-fleet/internal/domain"
	"transjakarta-fleet/internal/messaging"
	"transjakarta-fleet/internal/repository"
	"transjakarta-fleet/internal/wait"
)

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := wait.Options{
		Timeout:  cfg.DependencyTimeout(),
		Interval: time.Second,
		Log:      func(format string, args ...any) { log.Printf(format, args...) },
	}

	// 1. PostgreSQL
	var db *sql.DB
	if err := wait.For(ctx, "postgres", opts, func() error {
		conn, err := repository.Open(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		db = conn
		return nil
	}); err != nil {
		log.Fatalf("failed to connect db: %v", err)
	}
	defer db.Close()

	eventRepo := repository.NewGeofenceEventRepository(db)

	// 2. RabbitMQ
	var rabbitClient *messaging.Client
	if err := wait.For(ctx, "rabbitmq", opts, func() error {
		client, err := messaging.New(cfg.RabbitMQURL)
		if err != nil {
			return err
		}
		rabbitClient = client
		return nil
	}); err != nil {
		log.Fatalf("failed to connect rabbitmq: %v", err)
	}
	defer rabbitClient.Close()

	// 3. Konsumsi queue
	if err := rabbitClient.ConsumeGeofenceAlerts(func(body []byte) error {
		var ev domain.GeofenceEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			// JSON rusak tidak akan pernah berhasil diproses.
			return fmt.Errorf("%w: json tidak valid: %v", messaging.ErrPermanent, err)
		}
		if ev.VehicleID == "" || ev.Event == "" {
			return fmt.Errorf("%w: event tidak lengkap: %s", messaging.ErrPermanent, string(body))
		}
		// trace_id sama dengan yang dibuat subscriber/publisher agar bisa dicari di log
		trace := fmt.Sprintf("%s-%d", ev.VehicleID, ev.Timestamp)
		if err := eventRepo.Insert(ev); err != nil {
			return fmt.Errorf("insert geofence event: %w", err)
		}
		fmt.Printf("[WORKER] trace=%s consumed queue=%s event=%s lat=%f lng=%f ts=%d\n",
			trace, messaging.QueueName, ev.Event, ev.Location.Latitude, ev.Location.Longitude, ev.Timestamp)
		return nil
	}); err != nil {
		log.Fatalf("failed to start consumer: %v", err)
	}
	fmt.Printf("[WORKER] Started consuming %s\n", messaging.QueueName)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("[WORKER] Shutting down...")
}
