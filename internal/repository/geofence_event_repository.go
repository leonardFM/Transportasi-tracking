package repository

import (
	"database/sql"

	"transjakarta-fleet/internal/domain"
)

// GeofenceEventRepository mengakses tabel geofence_events.
//
// Tabel ini dipakai worker untuk mendokumentasikan event geofence yang sudah
// diproses, sehingga dashboard pada service API tetap bisa menampilkan feed
// event meskipun worker berjalan di proses/container yang berbeda.
type GeofenceEventRepository struct {
	db *sql.DB
}

// NewGeofenceEventRepository membuat repository untuk tabel geofence_events.
func NewGeofenceEventRepository(db *sql.DB) *GeofenceEventRepository {
	return &GeofenceEventRepository{db: db}
}

// Insert menyimpan satu event geofence.
func (r *GeofenceEventRepository) Insert(ev domain.GeofenceEvent) error {
	query := `INSERT INTO geofence_events (vehicle_id, event, latitude, longitude, timestamp) VALUES ($1, $2, $3, $4, $5)`
	_, err := r.db.Exec(query, ev.VehicleID, ev.Event, ev.Location.Latitude, ev.Location.Longitude, ev.Timestamp)
	return err
}

// Recent mengembalikan event geofence terbaru, terbaru di depan.
func (r *GeofenceEventRepository) Recent(limit int) ([]domain.GeofenceEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT vehicle_id, event, latitude, longitude, timestamp FROM geofence_events ORDER BY timestamp DESC, id DESC LIMIT $1`
	rows, err := r.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.GeofenceEvent, 0, limit)
	for rows.Next() {
		var ev domain.GeofenceEvent
		if err := rows.Scan(&ev.VehicleID, &ev.Event, &ev.Location.Latitude, &ev.Location.Longitude, &ev.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, ev)
	}
	return result, rows.Err()
}
