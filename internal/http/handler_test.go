package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"transjakarta-fleet/internal/domain"
)

// stubLocationService adalah implementasi palsu LocationService supaya
// handler bisa diuji tanpa database.
type stubLocationService struct {
	last        *domain.LocationResponse
	history     []domain.LocationResponse
	lastErr     error
	historyErr  error
	gotLimit    int
	gotStart    int64
	gotEnd      int64
	gotVehicle  string
	returnEmpty bool
}

func (s *stubLocationService) GetLastLocation(vehicleID string) (*domain.LocationResponse, error) {
	s.gotVehicle = vehicleID
	if s.lastErr != nil {
		return nil, s.lastErr
	}
	return s.last, nil
}

func (s *stubLocationService) GetHistory(vehicleID string, start, end int64, limit int) ([]domain.LocationResponse, error) {
	s.gotVehicle = vehicleID
	s.gotStart = start
	s.gotEnd = end
	s.gotLimit = limit
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	return s.history, nil
}

// stubVehicleLister dan stubEventStore dipakai untuk menguji router tanpa
// menyentuh database.
type stubVehicleLister struct{}

func (stubVehicleLister) GetLastLocations() ([]domain.LocationResponse, error) {
	return nil, nil
}

type stubEventStore struct{}

func (stubEventStore) Recent(int) ([]domain.GeofenceEvent, error) {
	return nil, nil
}

// stubHeartbeatProvider menghasilkan laporan heartbeat statis.
type stubHeartbeatProvider struct {
	report domain.HeartbeatReport
	err    error
}

func (s stubHeartbeatProvider) Report() (domain.HeartbeatReport, error) {
	return s.report, s.err
}

func newTestRouter(service LocationService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	dashboard := NewDashboardHandler(stubVehicleLister{}, stubEventStore{}, domain.GeofenceConfig{})
	heartbeat := NewHeartbeatHandler(stubHeartbeatProvider{})
	return NewRouter(NewHandler(service), dashboard, heartbeat)
}

func TestGetLastLocation_S_Returns200(t *testing.T) {
	service := &stubLocationService{last: &domain.LocationResponse{
		VehicleID: "B1234XYZ",
		Latitude:  -6.175392,
		Longitude: 106.827153,
		Timestamp: 1_700_000_000,
	}}
	router := newTestRouter(service)

	req := httptest.NewRequest("GET", "/vehicles/B1234XYZ/location", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("harus 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if service.gotVehicle != "B1234XYZ" {
		t.Fatalf("vehicle_id harus diteruskan, got %q", service.gotVehicle)
	}
}

func TestGetLastLocation_Returns404UntukKendaraanTidakDikenal(t *testing.T) {
	service := &stubLocationService{lastErr: sql.ErrNoRows}
	router := newTestRouter(service)

	req := httptest.NewRequest("GET", "/vehicles/TIDAKADA/location", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("harus 404, got %d", rec.Code)
	}
}

func TestGetHistory_Returns200DenganParameterValid(t *testing.T) {
	service := &stubLocationService{history: []domain.LocationResponse{
		{VehicleID: "B1234XYZ", Timestamp: 100},
		{VehicleID: "B1234XYZ", Timestamp: 200},
	}}
	router := newTestRouter(service)

	req := httptest.NewRequest("GET", "/vehicles/B1234XYZ/history?start=100&end=300", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("harus 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if service.gotStart != 100 || service.gotEnd != 300 {
		t.Fatalf("start/end harus diteruskan, got %d/%d", service.gotStart, service.gotEnd)
	}
	if service.gotLimit != DefaultHistoryLimit {
		t.Fatalf("limit bawaan harus %d, got %d", DefaultHistoryLimit, service.gotLimit)
	}
}

func TestGetHistory_Returns400UntukParameterTidakValid(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"start hilang", "/vehicles/B1/history?end=300"},
		{"end hilang", "/vehicles/B1/history?start=100"},
		{"bukan angka", "/vehicles/B1/history?start=abc&end=300"},
		{"start > end", "/vehicles/B1/history?start=300&end=100"},
		{"satuan milidetik", "/vehicles/B1/history?start=1700000000000&end=1700000600000"},
		{"limit nol", "/vehicles/B1/history?start=100&end=300&limit=0"},
		{"limit bukan angka", "/vehicles/B1/history?start=100&end=300&limit=abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			router := newTestRouter(&stubLocationService{})

			req := httptest.NewRequest("GET", c.url, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != 400 {
				t.Fatalf("harus 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetHistory_MenerimaParameterLimit(t *testing.T) {
	service := &stubLocationService{}
	router := newTestRouter(service)

	req := httptest.NewRequest("GET", "/vehicles/B1/history?start=100&end=300&limit=25", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("harus 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if service.gotLimit != 25 {
		t.Fatalf("limit harus 25, got %d", service.gotLimit)
	}
}

func TestGetHistory_LimitDibatasiMaksimum(t *testing.T) {
	service := &stubLocationService{}
	router := newTestRouter(service)

	req := httptest.NewRequest("GET", "/vehicles/B1/history?start=100&end=300&limit=999999", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("harus 200, got %d", rec.Code)
	}
	if service.gotLimit != MaxHistoryLimit {
		t.Fatalf("limit harus dibatasi %d, got %d", MaxHistoryLimit, service.gotLimit)
	}
}

func TestGetHistory_InternalError(t *testing.T) {
	service := &stubLocationService{historyErr: errors.New("boom")}
	router := newTestRouter(service)

	req := httptest.NewRequest("GET", "/vehicles/B1/history?start=100&end=300", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Fatalf("harus 500, got %d", rec.Code)
	}
}

// TestGetHeartbeat_Returns200DenganLaporanLengkap memastikan endpoint
// heartbeat mengembalikan ringkasan, daftar kendaraan, dan status pipeline.
func TestGetHeartbeat_Returns200DenganLaporanLengkap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	report := domain.HeartbeatReport{
		GeneratedAt:  1_700_000_000,
		StaleAfter:   30,
		OfflineAfter: 120,
		Summary: map[string]int{
			domain.StatusOnline:  1,
			domain.StatusStale:   0,
			domain.StatusOffline: 1,
		},
		Vehicles: []domain.VehicleStatus{
			{VehicleID: "B1234XYZ", LastSeen: 1_699_999_995, AgeSeconds: 5, Status: domain.StatusOnline},
			{VehicleID: "B5678ABC", LastSeen: 1_699_999_700, AgeSeconds: 300, Status: domain.StatusOffline},
		},
		Pipeline: domain.PipelineStatus{
			MQTTConnected: true,
			LastMessageAt: 1_699_999_999,
			AgeSeconds:    1,
			MessagesTotal: 42,
			Healthy:       true,
		},
	}

	dashboard := NewDashboardHandler(stubVehicleLister{}, stubEventStore{}, domain.GeofenceConfig{})
	router := NewRouter(NewHandler(&stubLocationService{}), dashboard, NewHeartbeatHandler(stubHeartbeatProvider{report: report}))

	req := httptest.NewRequest("GET", "/api/heartbeat", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("harus 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got domain.HeartbeatReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if len(got.Vehicles) != 2 {
		t.Fatalf("harus 2 kendaraan, got %d", len(got.Vehicles))
	}
	if got.Vehicles[1].Status != domain.StatusOffline {
		t.Fatalf("kendaraan kedua harus offline, got %s", got.Vehicles[1].Status)
	}
	if got.Summary[domain.StatusOffline] != 1 {
		t.Fatalf("summary offline harus 1, got %d", got.Summary[domain.StatusOffline])
	}
	if !got.Pipeline.Healthy || got.Pipeline.MessagesTotal != 42 {
		t.Fatalf("pipeline tidak sesuai: %+v", got.Pipeline)
	}
}

// TestGetHeartbeat_InternalError memastikan kegagalan sumber data menjadi
// 500, bukan respons kosong.
func TestGetHeartbeat_InternalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dashboard := NewDashboardHandler(stubVehicleLister{}, stubEventStore{}, domain.GeofenceConfig{})
	router := NewRouter(NewHandler(&stubLocationService{}), dashboard,
		NewHeartbeatHandler(stubHeartbeatProvider{err: errors.New("db down")}))

	req := httptest.NewRequest("GET", "/api/heartbeat", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Fatalf("harus 500, got %d", rec.Code)
	}
}
