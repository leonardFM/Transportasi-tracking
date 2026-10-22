package service

import (
	"math"
	"testing"
	"time"

	"transjakarta-fleet/internal/domain"
)

// monas adalah titik pusat geofence default (Monas, Jakarta).
const (
	monasLat = -6.175392
	monasLng = 106.827153
)

func newTestTracker(t *testing.T, ttl time.Duration, emitExit bool) (*GeofenceTracker, *time.Time) {
	t.Helper()
	geo := NewGeofenceService(domain.GeofenceConfig{Lat: monasLat, Lng: monasLng, RadiusM: 50})
	tracker := NewGeofenceTracker(geo, ttl, emitExit)
	now := time.Unix(1_700_000_000, 0)
	tracker.SetClock(func() time.Time { return now })
	return tracker, &now
}

// offsetCoordinate menghasilkan koordinat yang berjarak ±meter dari pusat
// geofence pada arah utara (bearing 0).
func offsetCoordinate(meters float64) (float64, float64) {
	return monasLat + meters/111320.0, monasLng
}

func TestDistance_ReferensiKnown(t *testing.T) {
	// Satu derajat lintang = ~111,19 km.
	got := Distance(0, 0, 1, 0)
	want := 111194.9
	if math.Abs(got-want) > 50 {
		t.Fatalf("jarak 1 derajat lintang = %.1f m, harapan ~%.1f m", got, want)
	}

	// Jarak nol untuk titik yang sama.
	if got := Distance(monasLat, monasLng, monasLat, monasLng); got != 0 {
		t.Fatalf("jarak titik identik harus 0, got %f", got)
	}
}

func TestDistance_MonasKeBundaranHI(t *testing.T) {
	// Bundaran HI berada sekitar 2,9 km di utara Monas.
	got := Distance(monasLat, monasLng, -6.149700, 106.829000)
	if got < 2700 || got > 3100 {
		t.Fatalf("jarak Monas ke Bundaran HI ~2,9 km, got %.1f m", got)
	}
}

func TestTracker_EventHanyaSaatTransisiMasuk(t *testing.T) {
	tracker, _ := newTestTracker(t, 60*time.Second, false)

	// Kendaraan di dalam area pada pesan pertama -> event entry.
	lat, lng := offsetCoordinate(0)
	p := domain.LocationPayload{VehicleID: "B1", Latitude: lat, Longitude: lng, Timestamp: 1}
	_, _, ev := tracker.Evaluate(p)
	if ev == nil {
		t.Fatal("pesan pertama di dalam area harus menghasilkan event")
	}
	if ev.Event != domain.EventNameGeofenceEntry {
		t.Fatalf("event harus %s, got %s", domain.EventNameGeofenceEntry, ev.Event)
	}

	// Pesan berikutnya masih di dalam area -> TIDAK ada event baru.
	for i := 2; i <= 10; i++ {
		p.Timestamp = int64(i)
		_, _, ev = tracker.Evaluate(p)
		if ev != nil {
			t.Fatalf("pesan %d di dalam area tidak boleh menghasilkan event baru", i)
		}
	}
}

func TestTracker_EventKembaliSetelahKeluarLaluMasuk(t *testing.T) {
	tracker, _ := newTestTracker(t, 60*time.Second, false)

	insideLat, insideLng := offsetCoordinate(0)
	outsideLat, outsideLng := offsetCoordinate(200)

	// Masuk.
	_, _, ev := tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: insideLat, Longitude: insideLng, Timestamp: 1})
	if ev == nil {
		t.Fatal("transisi masuk harus menghasilkan event")
	}

	// Keluar (tanpa event keluar karena emitExit=false).
	_, _, ev = tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: outsideLat, Longitude: outsideLng, Timestamp: 2})
	if ev != nil {
		t.Fatalf("transisi keluar tidak boleh menghasilkan event saat emitExit=false, got %v", ev)
	}

	// Masuk lagi -> event baru.
	_, _, ev = tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: insideLat, Longitude: insideLng, Timestamp: 3})
	if ev == nil {
		t.Fatal("transisi masuk kedua harus menghasilkan event")
	}
}

func TestTracker_EventKeluarBilaEmitExitAktif(t *testing.T) {
	tracker, _ := newTestTracker(t, 60*time.Second, true)

	insideLat, insideLng := offsetCoordinate(0)
	outsideLat, outsideLng := offsetCoordinate(200)

	tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: insideLat, Longitude: insideLng, Timestamp: 1})

	_, _, ev := tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: outsideLat, Longitude: outsideLng, Timestamp: 2})
	if ev == nil {
		t.Fatal("transisi keluar harus menghasilkan event saat emitExit=true")
	}
	if ev.Event != domain.EventNameGeofenceExit {
		t.Fatalf("event harus %s, got %s", domain.EventNameGeofenceExit, ev.Event)
	}
}

func TestTracker_StateKedaluwarsaMemicuEventMasukLagi(t *testing.T) {
	tracker, now := newTestTracker(t, 30*time.Second, false)

	insideLat, insideLng := offsetCoordinate(0)
	outsideLat, outsideLng := offsetCoordinate(200)

	tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: insideLat, Longitude: insideLng, Timestamp: 1})

	// Kendaraan berhenti melapor selama lebih dari TTL lalu kembali melapor
	// dari luar area: state dianggap basi.
	*now = now.Add(60 * time.Second)
	tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: outsideLat, Longitude: outsideLng, Timestamp: 2})

	// Kembali ke dalam area: karena state sebelumnya basi, harus ada event.
	*now = now.Add(10 * time.Second)
	_, _, ev := tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: insideLat, Longitude: insideLng, Timestamp: 3})
	if ev == nil {
		t.Fatal("setelah state kedaluwarsa, transisi masuk harus tetap menghasilkan event")
	}
}

func TestTracker_StatePerKendaraanTerpisah(t *testing.T) {
	tracker, _ := newTestTracker(t, 60*time.Second, false)

	insideLat, insideLng := offsetCoordinate(0)

	_, _, ev := tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: insideLat, Longitude: insideLng, Timestamp: 1})
	if ev == nil {
		t.Fatal("kendaraan pertama harus menghasilkan event")
	}

	// Kendaraan kedua berbeda: harus dianggap baru, bukanDiam di dalam.
	_, _, ev = tracker.Evaluate(domain.LocationPayload{VehicleID: "B2", Latitude: insideLat, Longitude: insideLng, Timestamp: 2})
	if ev == nil {
		t.Fatal("kendaraan kedua harus menghasilkan event tersendiri")
	}
}

func TestTracker_MenyimpanJarakDanStatusInside(t *testing.T) {
	tracker, _ := newTestTracker(t, 60*time.Second, false)

	lat, lng := offsetCoordinate(30)
	dist, inside, _ := tracker.Evaluate(domain.LocationPayload{VehicleID: "B1", Latitude: lat, Longitude: lng, Timestamp: 1})

	if !inside {
		t.Fatal("posisi 30 meter dari pusat harus di dalam radius 50 meter")
	}
	if math.Abs(dist-30) > 1 {
		t.Fatalf("jarak harus ~30 meter, got %.2f", dist)
	}

	lat, lng = offsetCoordinate(80)
	dist, inside, _ = tracker.Evaluate(domain.LocationPayload{VehicleID: "B2", Latitude: lat, Longitude: lng, Timestamp: 2})
	if inside {
		t.Fatal("posisi 80 meter dari pusat harus di luar radius 50 meter")
	}
	if math.Abs(dist-80) > 1 {
		t.Fatalf("jarak harus ~80 meter, got %.2f", dist)
	}
}
