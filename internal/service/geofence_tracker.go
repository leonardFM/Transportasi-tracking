package service

import (
	"sync"
	"time"

	"transjakarta-fleet/internal/domain"
)

// DefaultGeofenceStateTTL adalah masa berlaku state "di dalam geofence" pada
// tracker. Setelah TTL terlampaui tanpa laporan baru, state dianggap sudah
// basi sehingga transisi masuk berikutnya tetap terpicu.
const DefaultGeofenceStateTTL = 60 * time.Second

// vehicleGeofenceState menyimpan kondisi terakhir sebuah kendaraan.
type vehicleGeofenceState struct {
	inside   bool
	lastSeen time.Time
}

// GeofenceTracker menentukan KAPAN event geofence perlu dikirim.
//
// Tanpa tracker, setiap pesan dari kendaraan yang sedang berada di dalam
// radius akan menghasilkan event baru (level-triggered) sehingga queue
// RabbitMQ terisi event berulang setiap 2 detik. Tracker ini membuat event
// hanya terbit pada TRANSISI dari luar ke dalam (edge-triggered), sesuai
// kalimat "jika kendaraan MASUK dalam radius" pada spesifikasi.
//
// State disimpan di memori proses API. Bila service API dijalankan lebih dari
// satu replika, masing-masing replika memiliki state sendiri sehingga satu
// transisi bisa menghasilkan event lebih dari sekali. Untuk deployment
// multi-replica, state sebaiknya dipindahkan ke penyimpanan bersama
// (Redis/PostgreSQL).
type GeofenceTracker struct {
	mu     sync.Mutex
	states map[string]vehicleGeofenceState

	geo      *GeofenceService
	ttl      time.Duration
	emitExit bool
	now      func() time.Time
}

// NewGeofenceTracker membuat tracker geofence.
//
// ttl menentukan berapa lama state sebuah kendaraan dianggap valid;
// emitExit menentukan apakah event "geofence_exit" juga dikirim.
func NewGeofenceTracker(geo *GeofenceService, ttl time.Duration, emitExit bool) *GeofenceTracker {
	if ttl <= 0 {
		ttl = DefaultGeofenceStateTTL
	}
	return &GeofenceTracker{
		states:   make(map[string]vehicleGeofenceState),
		geo:      geo,
		ttl:      ttl,
		emitExit: emitExit,
		now:      time.Now,
	}
}

// SetClock mengganti sumber waktu tracker (dipakai pengujian).
func (t *GeofenceTracker) SetClock(now func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}

// RadiusM mengembalikan radius geofence (meter) yang dipakai tracker.
func (t *GeofenceTracker) RadiusM() float64 {
	return t.geo.Config().RadiusM
}

// Evaluate menghitung jarak ke pusat geofence dan menentukan apakah terjadi
// transisi yang menghasilkan event.
//
// Nilai balik:
//   - distanceM: jarak ke pusat geofence dalam meter,
//   - inside: apakah posisi saat ini berada di dalam radius,
//   - event: event geofence bila terjadi transisi, nil bila tidak.
func (t *GeofenceTracker) Evaluate(p domain.LocationPayload) (distanceM float64, inside bool, event *domain.GeofenceEvent) {
	distanceM, inside = t.geo.Evaluate(p.Latitude, p.Longitude)

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	prev, existed := t.states[p.VehicleID]
	prevInside := existed && prev.inside

	// State yang sudah lewat TTL dianggap basi: perlakukan kendaraan sebagai
	// berada di luar area supaya transisi masuk berikutnya tetap terpicu.
	if existed && now.Sub(prev.lastSeen) > t.ttl {
		prevInside = false
	}

	t.states[p.VehicleID] = vehicleGeofenceState{inside: inside, lastSeen: now}
	t.pruneLocked(now)

	switch {
	case inside && !prevInside:
		ev := t.geo.BuildEvent(p, domain.EventNameGeofenceEntry)
		return distanceM, inside, &ev
	case !inside && prevInside && t.emitExit:
		ev := t.geo.BuildEvent(p, domain.EventNameGeofenceExit)
		return distanceM, inside, &ev
	}
	return distanceM, inside, nil
}

// State mengembalikan kondisi terakhir sebuah kendaraan.
//
// known bernilai false bila kendaraan belum pernah dilaporkan atau state-nya
// sudah kedaluwarsa.
func (t *GeofenceTracker) State(vehicleID string) (inside bool, known bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.states[vehicleID]
	if !ok {
		return false, false
	}
	if t.now().Sub(state.lastSeen) > t.ttl {
		return false, false
	}
	return state.inside, true
}

// pruneLocked membuang state kendaraan yang sudah tidak melapor selama TTL,
// supaya map tidak tumbuh tanpa batas.
func (t *GeofenceTracker) pruneLocked(now time.Time) {
	for id, state := range t.states {
		if now.Sub(state.lastSeen) > t.ttl {
			delete(t.states, id)
		}
	}
}
