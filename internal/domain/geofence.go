package domain

// EventName adalah nama event geofence yang dikirim ke RabbitMQ.
//
// EventNameGeofenceEntry adalah event wajib sesuai spesifikasi TUGAS.md.
// EventNameGeofenceExit bersifat opsional dan hanya dikirim bila konfigurasi
// GEOFENCE_EMIT_EXIT diaktifkan.
const (
	EventNameGeofenceEntry = "geofence_entry"
	EventNameGeofenceExit  = "geofence_exit"
)

// GeofenceEvent adalah pesan yang dikirim ke exchange "fleet.events"
// ketika sebuah kendaraan masuk ke dalam radius geofence.
type GeofenceEvent struct {
	VehicleID string   `json:"vehicle_id"`
	Event     string   `json:"event"`
	Location  Location `json:"location"`
	Timestamp int64    `json:"timestamp"`
}

// GeofenceConfig adalah konfigurasi titik pusat & radius geofence.
type GeofenceConfig struct {
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	RadiusM float64 `json:"radius_m"`
}
