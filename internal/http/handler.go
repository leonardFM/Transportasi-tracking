// Package http berisi handler dan router REST API.
//
// Handler sengaja bekerja dengan antarmuka (interface), bukan struct konkret
// dari package service, supaya seluruh handler bisa diuji tanpa database.
package http

import (
	"database/sql"
	"errors"
	"fmt"
	nethttp "net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"transjakarta-fleet/internal/domain"
)

const (
	// DefaultHistoryLimit adalah batas bawaan jumlah baris riwayat.
	DefaultHistoryLimit = 1000
	// MaxHistoryLimit adalah batas atas jumlah baris riwayat per permintaan.
	MaxHistoryLimit = 10000
	// millisecondThreshold adalah ambang untuk mendeteksi parameter waktu
	// yang salah satuan. Nilai di atas ambang ini (yaitu > 1e11 detik) hampir
	// pasti unix time dalam milidetik.
	millisecondThreshold = int64(1e11)
)

// LocationService adalah antarmuka pembacaan data lokasi.
type LocationService interface {
	GetLastLocation(vehicleID string) (*domain.LocationResponse, error)
	GetHistory(vehicleID string, start, end int64, limit int) ([]domain.LocationResponse, error)
}

// Handler menangani endpoint API armada.
type Handler struct {
	location LocationService
}

// NewHandler membuat handler API dengan service lokasi.
func NewHandler(location LocationService) *Handler {
	return &Handler{location: location}
}

// GetLastLocation menangani GET /vehicles/:vehicle_id/location.
//
// Lokasi yang dikembalikan adalah yang memiliki timestamp terbesar, bukan
// pesan yang terakhir diterima.
func (h *Handler) GetLastLocation(c *gin.Context) {
	vehicleID := c.Param("vehicle_id")
	resp, err := h.location.GetLastLocation(vehicleID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(nethttp.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(nethttp.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(nethttp.StatusOK, resp)
}

// GetHistory menangani GET /vehicles/:vehicle_id/history?start=&end=&limit=.
//
// start dan end berupa unix timestamp dalam detik dan bersifat inklusif.
// limit bersifat opsional (default 1000, maksimum 10000).
func (h *Handler) GetHistory(c *gin.Context) {
	vehicleID := c.Param("vehicle_id")

	start, err := parseUnixParam(c.Query("start"), "start")
	if err != nil {
		c.JSON(nethttp.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	end, err := parseUnixParam(c.Query("end"), "end")
	if err != nil {
		c.JSON(nethttp.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if start > end {
		c.JSON(nethttp.StatusBadRequest, gin.H{"error": "start must be less than or equal to end"})
		return
	}

	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		c.JSON(nethttp.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rows, err := h.location.GetHistory(vehicleID, start, end, limit)
	if err != nil {
		c.JSON(nethttp.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(nethttp.StatusOK, rows)
}

// Health menangani GET /health.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(nethttp.StatusOK, gin.H{"status": "ok"})
}

// parseUnixParam mem-parsing parameter waktu unix (detik) dan memberi pesan
// error yang jelas bila salah satuan (milidetik) atau tidak valid.
func parseUnixParam(raw, name string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("%s wajib diisi (unix timestamp dalam detik)", name)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s harus berupa unix timestamp (detik)", name)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s tidak boleh negatif", name)
	}
	if value > millisecondThreshold {
		return 0, fmt.Errorf("%s terdeteksi dalam milidetik; gunakan unix timestamp dalam detik", name)
	}
	return value, nil
}

// parseLimit mem-parsing parameter limit (opsional).
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return DefaultHistoryLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("limit harus berupa bilangan bulat")
	}
	if value <= 0 {
		return 0, fmt.Errorf("limit harus lebih besar dari 0")
	}
	if value > MaxHistoryLimit {
		value = MaxHistoryLimit
	}
	return value, nil
}
