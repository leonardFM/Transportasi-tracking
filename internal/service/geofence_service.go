package service

import (
	"math"

	"transjakarta-fleet/internal/domain"
)

// earthRadiusM adalah jari-jari bumi dalam meter.
const earthRadiusM = 6371000.0

// GeofenceService mengevaluasi apakah sebuah posisi berada di dalam radius
// geofence, menggunakan formula Haversine.
//
// Service ini bersifat stateless (murni geometri). Penetapan state "kendaraan
// sedang di dalam atau di luar area" ditangani oleh GeofenceTracker.
type GeofenceService struct {
	cfg domain.GeofenceConfig
}

// NewGeofenceService membuat service geofence dengan konfigurasi tertentu.
func NewGeofenceService(cfg domain.GeofenceConfig) *GeofenceService {
	return &GeofenceService{cfg: cfg}
}

// Config mengembalikan konfigurasi geofence yang dipakai.
func (s *GeofenceService) Config() domain.GeofenceConfig {
	return s.cfg
}

// Distance menghitung jarak dua titik koordinat dalam meter (Haversine).
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	dPhi := (lat2 - lat1) * math.Pi / 180
	dLambda := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(dPhi/2)*math.Sin(dPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(dLambda/2)*math.Sin(dLambda/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusM * c
}

// Evaluate mengembalikan jarak ke pusat geofence (meter) dan status apakah
// kendaraan berada di dalam radius.
func (s *GeofenceService) Evaluate(lat, lon float64) (distanceM float64, inside bool) {
	distanceM = Distance(lat, lon, s.cfg.Lat, s.cfg.Lng)
	return distanceM, distanceM <= s.cfg.RadiusM
}

// BuildEvent menyusun event geofence dari sebuah payload lokasi.
//
// eventName biasanya domain.EventNameGeofenceEntry; gunakan
// domain.EventNameGeofenceExit bila event keluar juga diaktifkan.
func (s *GeofenceService) BuildEvent(p domain.LocationPayload, eventName string) domain.GeofenceEvent {
	return domain.GeofenceEvent{
		VehicleID: p.VehicleID,
		Event:     eventName,
		Location: domain.Location{
			Latitude:  p.Latitude,
			Longitude: p.Longitude,
		},
		Timestamp: p.Timestamp,
	}
}
