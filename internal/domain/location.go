// Package domain berisi tipe-tipe inti bisnis yang dipakai lintas layer.
// Package ini tidak boleh mengimpor package internal lain (tidak ada dependensi).
package domain

// LocationPayload adalah bentuk data yang dikirim perangkat melalui MQTT.
type LocationPayload struct {
	VehicleID string  `json:"vehicle_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timestamp int64   `json:"timestamp"`
}

// LocationResponse adalah bentuk data yang dikembalikan REST API.
type LocationResponse struct {
	VehicleID string  `json:"vehicle_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timestamp int64   `json:"timestamp"`
}

// Location adalah koordinat saja, dipakai di dalam payload event geofence.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// LocationRow adalah satu baris tabel vehicle_loctions.
type LocationRow struct {
	VehicleID string
	Latitude  float64
	Longitude float64
	Timestamp int64
}
