package repository

import (
	"database/sql"

	"transjakarta-fleet/internal/domain"
)

// DefaultHistoryLimit adalah batas bawaan jumlah baris riwayat yang
// dikembalikan satu permintaan.
const DefaultHistoryLimit = 1000

// LocationRepository mengakses tabel vehicle_loctions.
type LocationRepository struct {
	db *sql.DB
}

// NewLocationRepository membuat repository untuk tabel vehicle_loctions.
func NewLocationRepository(db *sql.DB) *LocationRepository {
	return &LocationRepository{db: db}
}

// InsertLocation menyimpan satu titik lokasi baru.
func (r *LocationRepository) InsertLocation(vehicleID string, lat, lng float64, ts int64) error {
	query := `INSERT INTO vehicle_loctions (vehicle_id, latitude, longitude, timestamp) VALUES ($1, $2, $3, $4)`
	_, err := r.db.Exec(query, vehicleID, lat, lng, ts)
	return err
}

// GetLastLocation mengembalikan lokasi terakhir sebuah kendaraan.
//
// Urutan ditentukan timestamp (bukan urutan penerimaan pesan); id menjadi
// pemutus seri supaya hasilnya deterministik saat ada dua baris dengan
// timestamp sama.
func (r *LocationRepository) GetLastLocation(vehicleID string) (*domain.LocationRow, error) {
	query := `SELECT vehicle_id, latitude, longitude, timestamp FROM vehicle_loctions
	          WHERE vehicle_id = $1
	          ORDER BY timestamp DESC, id DESC
	          LIMIT 1`
	var row domain.LocationRow
	err := r.db.QueryRow(query, vehicleID).Scan(&row.VehicleID, &row.Latitude, &row.Longitude, &row.Timestamp)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetLastLocations mengembalikan lokasi terakhir dari setiap kendaraan
// (dipakai oleh dashboard dan laporan heartbeat).
//
// limit membatasi jumlah kendaraan yang dikembalikan; nilai 0 berarti
// tanpa batas. Urutan memakai timestamp sebagai kunci utama dengan id
// sebagai pemutus seri.
func (r *LocationRepository) GetLastLocations(limit int) ([]domain.LocationRow, error) {
	query := `SELECT DISTINCT ON (vehicle_id) vehicle_id, latitude, longitude, timestamp
	          FROM vehicle_loctions
	          ORDER BY vehicle_id, timestamp DESC, id DESC`
	args := []any{}
	if limit > 0 {
		query += " LIMIT $1"
		args = append(args, limit)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.LocationRow
	for rows.Next() {
		var row domain.LocationRow
		if err := rows.Scan(&row.VehicleID, &row.Latitude, &row.Longitude, &row.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// GetHistory mengembalikan riwayat lokasi sebuah kendaraan dalam rentang waktu.
//
// Rentang [start, end] bersifat inklusif di kedua sisi. Urutan naik
// (timestamp ASC, id ASC) dan jumlah baris dibatasi agar respons tidak
// membanjiri client.
func (r *LocationRepository) GetHistory(vehicleID string, start, end int64, limit int) ([]domain.LocationRow, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	query := `SELECT vehicle_id, latitude, longitude, timestamp FROM vehicle_loctions
	          WHERE vehicle_id = $1 AND timestamp BETWEEN $2 AND $3
	          ORDER BY timestamp ASC, id ASC
	          LIMIT $4`
	rows, err := r.db.Query(query, vehicleID, start, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.LocationRow
	for rows.Next() {
		var row domain.LocationRow
		if err := rows.Scan(&row.VehicleID, &row.Latitude, &row.Longitude, &row.Timestamp); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
