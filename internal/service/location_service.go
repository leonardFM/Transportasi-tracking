// Package service berisi logika bisnis aplikasi: validasi payload,
// orquestrasi repository, dan perhitungan geofence.
package service

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"transjakarta-fleet/internal/domain"
	"transjakarta-fleet/internal/repository"
)

var (
	// ErrInvalidPayload dikembalikan ketika payload MQTT tidak memenuhi format.
	ErrInvalidPayload = errors.New("invalid location payload")
	// ErrVehicleMismatch dikembalikan ketika vehicle_id pada payload tidak
	// sama dengan vehicle_id yang terbaca dari topik MQTT.
	ErrVehicleMismatch = errors.New("vehicle_id tidak cocok dengan topik")
)

// vehicleIDPattern membolehkan huruf, angka, tanda hubung, dan underscore
// dengan panjang 1-50 karakter (sesuai kolom vehicle_id VARCHAR(50)).
var vehicleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,49}$`)

// ValidationOptions berisi batas toleransi validasi waktu untuk payload lokasi.
//
// Timestamp pada payload adalah unix time dalam detik. Nilai yang terlalu jauh
// ke depan (clock skew atau pesan rusak) atau terlalu jauh ke belakang (data
// basi) ditolak supaya tidak mengacaukan perhitungan "lokasi terakhir".
type ValidationOptions struct {
	// MaxFutureSeconds toleransi ke depan. 0 berarti tanpa batas.
	MaxFutureSeconds int64
	// MaxAgeSeconds toleransi ke belakang (data basi). 0 berarti tanpa batas.
	MaxAgeSeconds int64
	// Now adalah sumber waktu; useful untuk pengujian. Nil berarti time.Now.
	Now func() time.Time
}

// DefaultValidationOptions mengembalikan batas validasi bawaan:
//
//   - toleransi 300 detik ke depan (clock skew antar perangkat),
//   - toleransi 24 jam ke belakang (data basi yang masih wajar).
func DefaultValidationOptions() ValidationOptions {
	return ValidationOptions{
		MaxFutureSeconds: 300,
		MaxAgeSeconds:    24 * 60 * 60,
		Now:              time.Now,
	}
}

func (o ValidationOptions) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// LocationService mengoordinasikan operasi terhadap data lokasi kendaraan.
type LocationService struct {
	repo  *repository.LocationRepository
	valid ValidationOptions
}

// NewLocationService membuat service lokasi dengan opsi validasi bawaan.
func NewLocationService(repo *repository.LocationRepository) *LocationService {
	return NewLocationServiceWithOptions(repo, DefaultValidationOptions())
}

// NewLocationServiceWithOptions membuat service lokasi dengan konfigurasi
// validasi tertentu.
func NewLocationServiceWithOptions(repo *repository.LocationRepository, opts ValidationOptions) *LocationService {
	return &LocationService{repo: repo, valid: opts}
}

// ValidatePayload memastikan payload MQTT lengkap, koordinatnya masuk akal,
// dan waktunya tidak mustahil (terlalu jauh ke depan atau ke belakang).
//
// expectedVehicleID boleh dikosongkan bila pemanggil tidak punya akses ke topik;
// bila diisi, vehicle_id pada payload WAJIB sama dengan nilai tersebut.
func ValidatePayload(p domain.LocationPayload, expectedVehicleID string, opts ValidationOptions) error {
	if p.VehicleID == "" {
		return fmt.Errorf("%w: vehicle_id wajib diisi", ErrInvalidPayload)
	}
	if !vehicleIDPattern.MatchString(p.VehicleID) {
		return fmt.Errorf("%w: vehicle_id %q tidak valid (hanya huruf, angka, '-', '_'; maksimal 50 karakter)",
			ErrInvalidPayload, p.VehicleID)
	}
	if expectedVehicleID != "" && p.VehicleID != expectedVehicleID {
		return fmt.Errorf("%w: payload menyebut %q, topik menyebut %q",
			ErrVehicleMismatch, p.VehicleID, expectedVehicleID)
	}
	if p.Timestamp == 0 {
		return fmt.Errorf("%w: timestamp wajib diisi", ErrInvalidPayload)
	}
	if p.Timestamp < 0 {
		return fmt.Errorf("%w: timestamp %d tidak valid", ErrInvalidPayload, p.Timestamp)
	}

	now := opts.now().Unix()
	if opts.MaxFutureSeconds > 0 && p.Timestamp > now+opts.MaxFutureSeconds {
		return fmt.Errorf("%w: timestamp %d lebih dari %d detik ke depan (sekarang %d)",
			ErrInvalidPayload, p.Timestamp, opts.MaxFutureSeconds, now)
	}
	if opts.MaxAgeSeconds > 0 && p.Timestamp < now-opts.MaxAgeSeconds {
		return fmt.Errorf("%w: timestamp %d lebih dari %d detik ke belakang (sekarang %d)",
			ErrInvalidPayload, p.Timestamp, opts.MaxAgeSeconds, now)
	}

	if p.Latitude < -90 || p.Latitude > 90 {
		return fmt.Errorf("%w: latitude %.6f di luar rentang [-90, 90]", ErrInvalidPayload, p.Latitude)
	}
	if p.Longitude < -180 || p.Longitude > 180 {
		return fmt.Errorf("%w: longitude %.6f di luar rentang [-180, 180]", ErrInvalidPayload, p.Longitude)
	}
	return nil
}

// Insert menyimpan satu payload lokasi yang sudah tervalidasi.
func (s *LocationService) Insert(p domain.LocationPayload) error {
	if err := ValidatePayload(p, "", s.valid); err != nil {
		return err
	}
	return s.repo.InsertLocation(p.VehicleID, p.Latitude, p.Longitude, p.Timestamp)
}

// GetLastLocation mengembalikan lokasi terakhir sebuah kendaraan.
//
// Urutan ditentukan oleh timestamp (bukan urutan pesan diterima), dengan
// id sebagai pemutus seri agar hasilnya deterministik.
func (s *LocationService) GetLastLocation(vehicleID string) (*domain.LocationResponse, error) {
	row, err := s.repo.GetLastLocation(vehicleID)
	if err != nil {
		return nil, err
	}
	return &domain.LocationResponse{
		VehicleID: row.VehicleID,
		Latitude:  row.Latitude,
		Longitude: row.Longitude,
		Timestamp: row.Timestamp,
	}, nil
}

// GetHistory mengembalikan riwayat lokasi dalam rentang waktu [start, end].
// Rentang bersifat inklusif di kedua sisi dan dibatasi limit baris.
func (s *LocationService) GetHistory(vehicleID string, start, end int64, limit int) ([]domain.LocationResponse, error) {
	rows, err := s.repo.GetHistory(vehicleID, start, end, limit)
	if err != nil {
		return nil, err
	}
	result := make([]domain.LocationResponse, 0, len(rows))
	for _, r := range rows {
		result = append(result, domain.LocationResponse{
			VehicleID: r.VehicleID,
			Latitude:  r.Latitude,
			Longitude: r.Longitude,
			Timestamp: r.Timestamp,
		})
	}
	return result, nil
}

// GetLastLocations mengembalikan lokasi terakhir semua kendaraan.
func (s *LocationService) GetLastLocations() ([]domain.LocationResponse, error) {
	rows, err := s.repo.GetLastLocations(0)
	if err != nil {
		return nil, err
	}
	result := make([]domain.LocationResponse, 0, len(rows))
	for _, r := range rows {
		result = append(result, domain.LocationResponse{
			VehicleID: r.VehicleID,
			Latitude:  r.Latitude,
			Longitude: r.Longitude,
			Timestamp: r.Timestamp,
		})
	}
	return result, nil
}
