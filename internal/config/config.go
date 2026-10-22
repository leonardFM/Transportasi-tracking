// Package config membaca konfigurasi aplikasi dari environment variable.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config berisi seluruh konfigurasi yang dibutuhkan ketiga service
// (api, worker, publisher).
type Config struct {
	Port        string
	DatabaseURL string
	MQTTBroker  string
	RabbitMQURL string

	// Geofence
	GeofenceLat     float64
	GeofenceLng     float64
	GeofenceRadiusM float64
	// GeofenceStateTTLSeconds masa berlaku state "di dalam geofence".
	GeofenceStateTTLSeconds int64
	// GeofenceEmitExit mengaktifkan pengiriman event "geofence_exit".
	// Default false agar format pesan tetap sama dengan spesifikasi.
	GeofenceEmitExit bool

	// Validasi payload
	MaxFutureSeconds int64
	MaxAgeSeconds    int64

	// Heartbeat & deteksi offline
	HeartbeatEnabled        bool
	HeartbeatStaleSeconds   int64
	HeartbeatOfflineSeconds int64

	// Dependency startup
	DependencyTimeoutSeconds int64

	// Publisher (simulator)
	PublisherVehicles           []string
	PublisherIntervalSeconds    int
	PublisherRoutePeriodSeconds int
	PublisherRouteBearingDeg    float64
	PublisherRouteSpreadDeg     float64
}

// Load membaca konfigurasi dari environment variable dengan nilai bawaan.
func Load() *Config {
	cfg := &Config{
		Port:        getEnv("PORT", "8000"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/transjakarta?sslmode=disable"),
		MQTTBroker:  getEnv("MQTT_BROKER", "tcp://localhost:1883"),
		RabbitMQURL: getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),

		GeofenceLat:             getEnvFloat("GEOFENCE_LAT", -6.175392),
		GeofenceLng:             getEnvFloat("GEOFENCE_LNG", 106.827153),
		GeofenceRadiusM:         getEnvFloat("GEOFENCE_RADIUS_M", 50),
		GeofenceStateTTLSeconds: getEnvInt("GEOFENCE_STATE_TTL_SECONDS", 60),
		GeofenceEmitExit:        getEnvBool("GEOFENCE_EMIT_EXIT", false),

		MaxFutureSeconds: getEnvInt("LOCATION_MAX_FUTURE_SECONDS", 300),
		MaxAgeSeconds:    getEnvInt("LOCATION_MAX_AGE_SECONDS", 24*60*60),

		HeartbeatEnabled:        getEnvBool("HEARTBEAT_ENABLED", true),
		HeartbeatStaleSeconds:   getEnvInt("HEARTBEAT_STALE_SECONDS", 30),
		HeartbeatOfflineSeconds: getEnvInt("HEARTBEAT_OFFLINE_SECONDS", 120),

		DependencyTimeoutSeconds: getEnvInt("DEPENDENCY_TIMEOUT_SECONDS", 120),

		PublisherVehicles:           splitAndTrim(getEnv("PUBLISHER_VEHICLES", "B1234XYZ,B5678ABC")),
		PublisherIntervalSeconds:    int(getEnvInt("PUBLISHER_INTERVAL", 2)),
		PublisherRoutePeriodSeconds: int(getEnvInt("PUBLISHER_ROUTE_PERIOD_SECONDS", 60)),
		PublisherRouteBearingDeg:    getEnvFloat("PUBLISHER_ROUTE_BEARING_DEG", 45),
		PublisherRouteSpreadDeg:     getEnvFloat("PUBLISHER_ROUTE_SPREAD_DEG", 20),
	}

	if cfg.PublisherIntervalSeconds <= 0 {
		cfg.PublisherIntervalSeconds = 2
	}
	if cfg.PublisherRoutePeriodSeconds <= 0 {
		cfg.PublisherRoutePeriodSeconds = 60
	}
	return cfg
}

// GeofenceStateTTL mengembalikan masa berlaku state tracker geofence.
func (c *Config) GeofenceStateTTL() time.Duration {
	return time.Duration(c.GeofenceStateTTLSeconds) * time.Second
}

// DependencyTimeout mengembalikan batas menunggu dependency eksternal.
func (c *Config) DependencyTimeout() time.Duration {
	return time.Duration(c.DependencyTimeoutSeconds) * time.Second
}

// HeartbeatStaleAfter mengembalikan ambang status "stale" (detik).
func (c *Config) HeartbeatStaleAfter() int64 {
	return c.HeartbeatStaleSeconds
}

// HeartbeatOfflineAfter mengembalikan ambang status "offline" (detik).
func (c *Config) HeartbeatOfflineAfter() int64 {
	return c.HeartbeatOfflineSeconds
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getEnvInt(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
