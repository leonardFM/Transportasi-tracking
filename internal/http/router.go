package http

import (
	"github.com/gin-gonic/gin"
)

// NewRouter mendaftarkan seluruh route aplikasi.
func NewRouter(handler *Handler, dashboard *DashboardHandler, heartbeat *HeartbeatHandler) *gin.Engine {
	r := gin.Default()

	// API armada
	r.GET("/health", handler.Health)
	r.GET("/vehicles/:vehicle_id/location", handler.GetLastLocation)
	r.GET("/vehicles/:vehicle_id/history", handler.GetHistory)

	// Dashboard & API observability
	r.GET("/dashboard", dashboard.Dashboard)
	r.GET("/api/vehicles", dashboard.GetVehicles)
	r.GET("/api/geofence-events", dashboard.GetGeofenceEvents)
	r.GET("/api/geofence", dashboard.GetGeofenceConfig)

	// Heartbeat: status online/stale/offline kendaraan + kondisi pipeline.
	// Hanya didaftarkan bila HEARTBEAT_ENABLED=true.
	if heartbeat != nil {
		r.GET("/api/heartbeat", heartbeat.GetHeartbeat)
	}

	return r
}
