package http

import (
	nethttp "net/http"

	"github.com/gin-gonic/gin"

	"transjakarta-fleet/internal/domain"
)

// HeartbeatProvider menyediakan laporan heartbeat.
//
// Dipisah sebagai interface supaya handler dapat diuji tanpa database.
type HeartbeatProvider interface {
	Report() (domain.HeartbeatReport, error)
}

// HeartbeatHandler menyajikan laporan heartbeat: status online/stale/offline
// tiap kendaraan yang pernah melapor, plus kondisi pipeline ingest.
type HeartbeatHandler struct {
	heartbeat HeartbeatProvider
}

// NewHeartbeatHandler membuat handler heartbeat.
func NewHeartbeatHandler(heartbeat HeartbeatProvider) *HeartbeatHandler {
	return &HeartbeatHandler{heartbeat: heartbeat}
}

// GetHeartbeat menangani GET /api/heartbeat.
func (h *HeartbeatHandler) GetHeartbeat(c *gin.Context) {
	if h.heartbeat == nil {
		c.JSON(nethttp.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	report, err := h.heartbeat.Report()
	if err != nil {
		c.JSON(nethttp.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(nethttp.StatusOK, report)
}
