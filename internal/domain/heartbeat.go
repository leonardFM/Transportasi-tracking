package domain

// Status kelistrikan sebuah kendaraan, dihitung dari umur data terakhir
// yang diterimanya.
const (
	// StatusOnline berarti kendaraan melapor dalam ambang waktu yang wajar.
	StatusOnline = "online"
	// StatusStale berarti data kendaraan sudah cukup tua dan perlu dicurigai.
	StatusStale = "stale"
	// StatusOffline berarti kendaraan tidak melapor melewati ambang offline.
	StatusOffline = "offline"
)

// VehicleStatus adalah status kelistrikan sebuah kendaraan beserta posisi
// terakhir yang diketahui.
type VehicleStatus struct {
	VehicleID  string  `json:"vehicle_id"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	LastSeen   int64   `json:"last_seen"`
	AgeSeconds int64   `json:"age_seconds"`
	Status     string  `json:"status"`
}

// IngestStats adalah kondisi mentah stream ingest (MQTT) yang direkam
// selama proses berjalan.
type IngestStats struct {
	// MQTTConnected true bila subscriber sedang tersambung ke broker.
	MQTTConnected bool `json:"mqtt_connected"`
	// LastMessageAt unix detik pesan terakhir yang diterima. 0 bila belum
	// ada pesan sama sekali.
	LastMessageAt int64 `json:"last_message_at"`
	// MessagesTotal jumlah pesan yang diterima sejak proses start.
	MessagesTotal int64 `json:"messages_total"`
}

// PipelineStatus adalah penilaian kelangsungan hidup stream ingest.
//
// Bedanya dengan IngestStats: di sini sudah ada umur pesan terakhir dan
// penilaian sehat/tidak, yang bergantung pada ambang staleness.
type PipelineStatus struct {
	MQTTConnected bool  `json:"mqtt_connected"`
	LastMessageAt int64 `json:"last_message_at"`
	AgeSeconds    int64 `json:"age_seconds"`
	MessagesTotal int64 `json:"messages_total"`
	Healthy       bool  `json:"healthy"`
}

// HeartbeatReport adalah laporan heartbeat lengkap: status tiap kendaraan
// yang pernah melapor, ringkasannya, dan kondisi pipeline ingest.
type HeartbeatReport struct {
	GeneratedAt  int64           `json:"generated_at"`
	StaleAfter   int64           `json:"stale_after_seconds"`
	OfflineAfter int64           `json:"offline_after_seconds"`
	Summary      map[string]int  `json:"summary"`
	Vehicles     []VehicleStatus `json:"vehicles"`
	Pipeline     PipelineStatus  `json:"pipeline"`
}
