// Command api adalah service utama Transjakarta Fleet: menerima data lokasi
// dari MQTT, menyimpannya ke PostgreSQL, mempublikasikan event geofence ke
// RabbitMQ, dan menyajikan REST API + dashboard.
//
// Startup tidak bergantung pada urutan container: PostgreSQL, RabbitMQ, dan
// broker MQTT dicoba berulang kali sampai siap (lihat internal/wait).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"transjakarta-fleet/internal/config"
	"transjakarta-fleet/internal/domain"
	httphandler "transjakarta-fleet/internal/http"
	"transjakarta-fleet/internal/messaging"
	"transjakarta-fleet/internal/mqtt"
	"transjakarta-fleet/internal/repository"
	"transjakarta-fleet/internal/service"
	"transjakarta-fleet/internal/wait"
)

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. PostgreSQL
	var db *sql.DB
	dbOpts := wait.Options{
		Timeout:  cfg.DependencyTimeout(),
		Interval: time.Second,
		Log:      func(format string, args ...any) { log.Printf(format, args...) },
	}
	if err := wait.For(ctx, "postgres", dbOpts, func() error {
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

	locationRepo := repository.NewLocationRepository(db)
	eventRepo := repository.NewGeofenceEventRepository(db)

	// Monitor ingest: merekam kelangsungan hidup stream MQTT untuk
	// laporan heartbeat.
	ingestMonitor := service.NewIngestMonitor()

	validation := service.ValidationOptions{
		MaxFutureSeconds: cfg.MaxFutureSeconds,
		MaxAgeSeconds:    cfg.MaxAgeSeconds,
	}
	locationService := service.NewLocationServiceWithOptions(locationRepo, validation)
	geofenceService := service.NewGeofenceService(domain.GeofenceConfig{
		Lat:     cfg.GeofenceLat,
		Lng:     cfg.GeofenceLng,
		RadiusM: cfg.GeofenceRadiusM,
	})
	tracker := service.NewGeofenceTracker(geofenceService, cfg.GeofenceStateTTL(), cfg.GeofenceEmitExit)

	// 2. RabbitMQ
	var rabbitClient *messaging.Client
	if err := wait.For(ctx, "rabbitmq", dbOpts, func() error {
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

	// 3. Broker MQTT
	var subscriber *mqtt.Subscriber
	if err := wait.For(ctx, "mqtt broker", dbOpts, func() error {
		sub, err := mqtt.NewSubscriber(cfg.MQTTBroker, locationService, tracker, rabbitClient, validation, ingestMonitor)
		if err != nil {
			return err
		}
		subscriber = sub
		return nil
	}); err != nil {
		log.Fatalf("failed to create mqtt subscriber: %v", err)
	}
	defer subscriber.Stop()
	if err := subscriber.Start(); err != nil {
		log.Fatalf("failed to start mqtt subscriber: %v", err)
	}

	// Laporan heartbeat: status online/stale/offline tiap kendaraan +
	// kondisi pipeline ingest. Bisa dimatikan lewat HEARTBEAT_ENABLED=false.
	var heartbeatHandler *httphandler.HeartbeatHandler
	if cfg.HeartbeatEnabled {
		heartbeatService := service.NewHeartbeatService(
			locationRepo,
			ingestMonitor,
			cfg.HeartbeatStaleAfter(),
			cfg.HeartbeatOfflineAfter(),
		)
		heartbeatHandler = httphandler.NewHeartbeatHandler(heartbeatService)
	} else {
		log.Println("[HEARTBEAT] dinonaktifkan (HEARTBEAT_ENABLED=false)")
	}

	handler := httphandler.NewHandler(locationService)
	dashboard := httphandler.NewDashboardHandler(locationService, eventRepo, geofenceService.Config())
	router := httphandler.NewRouter(handler, dashboard, heartbeatHandler)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		fmt.Printf("[API] Server starting on %s\n", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("failed to run server: %v", err)
		}
	}()

	// Graceful shutdown: hentikan server HTTP dan tutup koneksi MQTT/RabbitMQ.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("[API] Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[API] forced shutdown: %v", err)
	}
}
