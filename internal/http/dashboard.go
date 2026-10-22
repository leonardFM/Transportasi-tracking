package http

import (
	_ "embed"
	nethttp "net/http"

	"github.com/gin-gonic/gin"

	"transjakarta-fleet/internal/domain"
)

//go:embed dashboard.html
var dashboardHTML []byte

// VehicleLister adalah antarmuka pembacaan lokasi terakhir semua kendaraan.
type VehicleLister interface {
	GetLastLocations() ([]domain.LocationResponse, error)
}

// GeofenceEventStore adalah antarmuka pembacaan event geofence.
type GeofenceEventStore interface {
	Recent(limit int) ([]domain.GeofenceEvent, error)
}

// DashboardHandler menyajikan dashboard observability dan API pendukungnya.
type DashboardHandler struct {
	location VehicleLister
	events   GeofenceEventStore
	geo      domain.GeofenceConfig
}

// NewDashboardHandler membuat handler dashboard.
func NewDashboardHandler(location VehicleLister, events GeofenceEventStore, geo domain.GeofenceConfig) *DashboardHandler {
	return &DashboardHandler{location: location, events: events, geo: geo}
}

// Dashboard menyajikan halaman dashboard (peta + tabel + feed event).
func (h *DashboardHandler) Dashboard(c *gin.Context) {
	c.Data(nethttp.StatusOK, "text/html; charset=utf-8", dashboardHTML)
}

// GetVehicles mengembalikan lokasi terakhir semua kendaraan.
func (h *DashboardHandler) GetVehicles(c *gin.Context) {
	rows, err := h.location.GetLastLocations()
	if err != nil {
		c.JSON(nethttp.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(nethttp.StatusOK, rows)
}

// GetGeofenceEvents mengembalikan 100 event geofence terakhir.
func (h *DashboardHandler) GetGeofenceEvents(c *gin.Context) {
	events, err := h.events.Recent(100)
	if err != nil {
		c.JSON(nethttp.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(nethttp.StatusOK, events)
}

// GetGeofenceConfig mengembalikan konfigurasi geofence untuk digambar di peta.
func (h *DashboardHandler) GetGeofenceConfig(c *gin.Context) {
	c.JSON(nethttp.StatusOK, h.geo)
}
