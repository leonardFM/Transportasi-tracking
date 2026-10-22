package service

import (
	"errors"
	"testing"
	"time"

	"transjakarta-fleet/internal/domain"
)

// nowFixed mengembalikan sumber waktu yang stabil agar pengujian deterministik.
func nowFixed(ts int64) func() time.Time {
	return func() time.Time { return time.Unix(ts, 0) }
}

func validPayload() domain.LocationPayload {
	return domain.LocationPayload{
		VehicleID: "B1234XYZ",
		Latitude:  -6.175392,
		Longitude: 106.827153,
		Timestamp: 1_700_000_000,
	}
}

func TestValidatePayload_MenerimaPayloadValid(t *testing.T) {
	p := validPayload()
	opts := ValidationOptions{MaxFutureSeconds: 300, MaxAgeSeconds: 86400, Now: nowFixed(p.Timestamp)}

	if err := ValidatePayload(p, "B1234XYZ", opts); err != nil {
		t.Fatalf("payload valid harus diterima, got error: %v", err)
	}
}

func TestValidatePayload_TolakVehicleIDKosong(t *testing.T) {
	p := validPayload()
	p.VehicleID = ""

	err := ValidatePayload(p, "", DefaultValidationOptions())
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("harus ErrInvalidPayload, got %v", err)
	}
}

func TestValidatePayload_TolakVehicleIDKarakterIlegal(t *testing.T) {
	cases := []string{"B 1234", "B1234/567", "B1234#", "B1234'", "B1234.SPEC"}
	for _, id := range cases {
		p := validPayload()
		p.VehicleID = id

		err := ValidatePayload(p, "", DefaultValidationOptions())
		if !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("vehicle_id %q harus ditolak, got %v", id, err)
		}
	}
}

func TestValidatePayload_TolakKendaraanTidakCocokDenganTopik(t *testing.T) {
	p := validPayload()

	err := ValidatePayload(p, "B9999EVIL", DefaultValidationOptions())
	if !errors.Is(err, ErrVehicleMismatch) {
		t.Fatalf("harus ErrVehicleMismatch, got %v", err)
	}
}

func TestValidatePayload_TolakKoordinatDiLuarRentang(t *testing.T) {
	cases := []struct {
		name string
		lat  float64
		lng  float64
	}{
		{"latitude > 90", 90.5, 106.8},
		{"latitude < -90", -90.5, 106.8},
		{"longitude > 180", -6.17, 180.5},
		{"longitude < -180", -6.17, -180.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := validPayload()
			p.Latitude = c.lat
			p.Longitude = c.lng

			err := ValidatePayload(p, "", DefaultValidationOptions())
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("koordinat %v harus ditolak, got %v", c, err)
			}
		})
	}
}

func TestValidatePayload_TolakTimestampKosongDanNegatif(t *testing.T) {
	for _, ts := range []int64{0, -1} {
		p := validPayload()
		p.Timestamp = ts

		err := ValidatePayload(p, "", DefaultValidationOptions())
		if !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("timestamp %d harus ditolak, got %v", ts, err)
		}
	}
}

func TestValidatePayload_TolakTimestampTerlaluJauhKeDepan(t *testing.T) {
	now := int64(1_700_000_000)
	p := validPayload()
	p.Timestamp = now + 301 // melewati toleransi 300 detik

	err := ValidatePayload(p, "", ValidationOptions{
		MaxFutureSeconds: 300,
		MaxAgeSeconds:    86400,
		Now:              nowFixed(now),
	})
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("timestamp 301 detik ke depan harus ditolak, got %v", err)
	}
}

func TestValidatePayload_TolakTimestampTerlaluJauhKeBelakang(t *testing.T) {
	now := int64(1_700_000_000)
	p := validPayload()
	p.Timestamp = now - 86_401 // melewati toleransi 24 jam

	err := ValidatePayload(p, "", ValidationOptions{
		MaxFutureSeconds: 300,
		MaxAgeSeconds:    86_400,
		Now:              nowFixed(now),
	})
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("timestamp lebih dari 24 jam ke belakang harus ditolak, got %v", err)
	}
}

func TestValidatePayload_TerimaTimestampTepatDiBatas(t *testing.T) {
	now := int64(1_700_000_000)
	opts := ValidationOptions{MaxFutureSeconds: 300, MaxAgeSeconds: 86_400, Now: nowFixed(now)}

	for _, delta := range []int64{-86_400, 300} {
		p := validPayload()
		p.Timestamp = now + delta

		if err := ValidatePayload(p, "", opts); err != nil {
			t.Fatalf("timestamp di batas (delta %d) harus diterima, got %v", delta, err)
		}
	}
}
