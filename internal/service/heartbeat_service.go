package service

import (
	"sync"
	"time"

	"transjakarta-fleet/internal/domain"
)

// DefaultHeartbeatStaleSeconds adalah ambang bawaan untuk menandai data
// kendaraan sebagai "stale" (kurang andal).
const DefaultHeartbeatStaleSeconds = 30

// DefaultHeartbeatOfflineSeconds adalah ambang bawaan untuk menandai
// kendaraan sebagai "offline" (berhenti melapor).
const DefaultHeartbeatOfflineSeconds = 120

// MaxHeartbeatVehicles membatasi jumlah kendaraan yang dilaporkan dalam
// satu heartbeat, supaya respons tidak meledak pada armada yang sangat besar.
const MaxHeartbeatVehicles = 1000

// IngestMonitor merekam kelangsungan hidup stream ingest (MQTT).
//
// Dipakai dua arah:
//   - RecordMessage dipanggil setiap kali sebuah pesan lokasi well-formed
//     diterima, sehingga kita tahu pipeline benar-benar mengalir.
//   - SetConnected dipanggil oleh adapter MQTT saat sambungan established atau
//     putus.
//
// Monitor ini hanya menyimpan counter dan waktu; tidak memuat logika bisnis.
type IngestMonitor struct {
	mu            sync.RWMutex
	connected     bool
	lastMessageAt time.Time
	total         int64
}

// NewIngestMonitor membuat monitor ingest kosong.
func NewIngestMonitor() *IngestMonitor {
	return &IngestMonitor{}
}

// SetConnected mencatat status sambungan ke broker MQTT.
func (m *IngestMonitor) SetConnected(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = connected
}

// RecordMessage mencatat bahwa satu pesan lokasi masuk.
func (m *IngestMonitor) RecordMessage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMessageAt = time.Now()
	m.total++
}

// Stats mengembalikan kondisi mentah stream ingest.
func (m *IngestMonitor) Stats() domain.IngestStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var lastMessageAt int64
	if !m.lastMessageAt.IsZero() {
		lastMessageAt = m.lastMessageAt.Unix()
	}
	return domain.IngestStats{
		MQTTConnected: m.connected,
		LastMessageAt: lastMessageAt,
		MessagesTotal: m.total,
	}
}

// VehicleStatusSource menyediakan posisi terakhir setiap kendaraan.
//
// Dipisah sebagai interface agar HeartbeatService dapat diuji tanpa
// database. Diimplementasikan oleh *repository.LocationRepository.
type VehicleStatusSource interface {
	GetLastLocations(limit int) ([]domain.LocationRow, error)
}

// HeartbeatService menyusun laporan heartbeat: status online/stale/offline
// tiap kendaraan plus kondisi pipeline ingest.
//
// Status dihitung dari umur data terakhir, bukan dari urutan pesan. Jadi
// laporan ini tetap benar walau service API di-restart, dan tidak
// memerlukan tabel atau state tambahan.
type HeartbeatService struct {
	src          VehicleStatusSource
	monitor      *IngestMonitor
	staleAfter   int64
	offlineAfter int64
	now          func() time.Time
}

// NewHeartbeatService membuat service heartbeat.
//
// staleAfter dan offlineAfter adalah ambang umur data dalam detik.
// Bila offlineAfter tidak lebih besar dari staleAfter, nilainya dinormalisasi
// menjadi staleAfter+1 supaya kedua status tidak saling bertabrakan.
func NewHeartbeatService(
	src VehicleStatusSource,
	monitor *IngestMonitor,
	staleAfter int64,
	offlineAfter int64,
) *HeartbeatService {
	if staleAfter < 0 {
		staleAfter = 0
	}
	if offlineAfter <= staleAfter {
		offlineAfter = staleAfter + 1
	}
	return &HeartbeatService{
		src:          src,
		monitor:      monitor,
		staleAfter:   staleAfter,
		offlineAfter: offlineAfter,
		now:          time.Now,
	}
}

// SetClock mengganti sumber waktu service (dipakai pengujian).
func (s *HeartbeatService) SetClock(now func() time.Time) {
	s.now = now
}

// StaleAfter mengembalikan ambang status stale (detik).
func (s *HeartbeatService) StaleAfter() int64 { return s.staleAfter }

// OfflineAfter mengembalikan ambang status offline (detik).
func (s *HeartbeatService) OfflineAfter() int64 { return s.offlineAfter }

// ComputeStatus memetakan umur data (detik) menjadi status kendaraan.
//
//   - age >= offlineAfter  -> offline
//   - age >= staleAfter    -> stale
//   - selain itu           -> online
//
// Ambang 0 berarti ambang tersebut tidak berlaku (data selalu dianggap
// online untuk lapisan itu).
func ComputeStatus(ageSeconds, staleAfter, offlineAfter int64) string {
	switch {
	case offlineAfter > 0 && ageSeconds >= offlineAfter:
		return domain.StatusOffline
	case staleAfter > 0 && ageSeconds >= staleAfter:
		return domain.StatusStale
	default:
		return domain.StatusOnline
	}
}

// Report menyusun laporan heartbeat lengkap.
func (s *HeartbeatService) Report() (domain.HeartbeatReport, error) {
	now := s.now()
	nowUnix := now.Unix()

	report := domain.HeartbeatReport{
		GeneratedAt:  nowUnix,
		StaleAfter:   s.staleAfter,
		OfflineAfter: s.offlineAfter,
		Summary: map[string]int{
			domain.StatusOnline:  0,
			domain.StatusStale:   0,
			domain.StatusOffline: 0,
		},
		Vehicles: []domain.VehicleStatus{},
	}

	rows, err := s.src.GetLastLocations(MaxHeartbeatVehicles)
	if err != nil {
		return domain.HeartbeatReport{}, err
	}

	for _, row := range rows {
		age := nowUnix - row.Timestamp
		if age < 0 {
			// Pesan bertimestamp sedikit ke depan (toleransi clock skew):
			// umur dinolkan, bukan dianggap negatif.
			age = 0
		}
		status := ComputeStatus(age, s.staleAfter, s.offlineAfter)
		report.Summary[status]++
		report.Vehicles = append(report.Vehicles, domain.VehicleStatus{
			VehicleID:  row.VehicleID,
			Latitude:   row.Latitude,
			Longitude:  row.Longitude,
			LastSeen:   row.Timestamp,
			AgeSeconds: age,
			Status:     status,
		})
	}

	report.Pipeline = s.pipelineStatus(nowUnix)
	return report, nil
}

// pipelineStatus menilai kelangsungan hidup stream ingest.
//
// Pipeline dianggap sehat bila subscriber tersambung dan pesan terakhir
// yang masuk tidak lebih tua dari ambang stale. Bila belum ada pesan sama
// sekali, umur dianggap 0 (proses baru saja start) dan kondisi sebenarnya
// terlihat dari messages_total = 0.
func (s *HeartbeatService) pipelineStatus(nowUnix int64) domain.PipelineStatus {
	var stats domain.IngestStats
	if s.monitor != nil {
		stats = s.monitor.Stats()
	}

	age := int64(0)
	if stats.LastMessageAt > 0 {
		age = nowUnix - stats.LastMessageAt
		if age < 0 {
			age = 0
		}
	}

	return domain.PipelineStatus{
		MQTTConnected: stats.MQTTConnected,
		LastMessageAt: stats.LastMessageAt,
		AgeSeconds:    age,
		MessagesTotal: stats.MessagesTotal,
		Healthy:       stats.MQTTConnected && age <= s.staleAfter,
	}
}
