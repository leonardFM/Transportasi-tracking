package service

import (
	"errors"
	"testing"
	"time"

	"transjakarta-fleet/internal/domain"
)

// stubVehicleStatusSource menyediakan posisi terakhir dummy.
type stubVehicleStatusSource struct {
	rows []domain.LocationRow
	err  error
}

func (s stubVehicleStatusSource) GetLastLocations(limit int) ([]domain.LocationRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	if limit > 0 && len(s.rows) > limit {
		return s.rows[:limit], nil
	}
	return s.rows, nil
}

func TestComputeStatus_MappingAmbang(t *testing.T) {
	const stale = int64(30)
	const offline = int64(120)

	cases := []struct {
		name string
		age  int64
		want string
	}{
		{"baru saja", 0, domain.StatusOnline},
		{"di bawah ambang stale", 29, domain.StatusOnline},
		{"tepat di ambang stale", 30, domain.StatusStale},
		{"di antara stale dan offline", 119, domain.StatusStale},
		{"tepat di ambang offline", 120, domain.StatusOffline},
		{"jauh melewati ambang offline", 900, domain.StatusOffline},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ComputeStatus(c.age, stale, offline)
			if got != c.want {
				t.Fatalf("umur %d dtk harus berstatus %s, got %s", c.age, c.want, got)
			}
		})
	}
}

func TestComputeStatus_AmbangNolBerartiTanpaBatas(t *testing.T) {
	// staleAfter=0 dan offlineAfter=0: data dianggap selalu online.
	if got := ComputeStatus(99999, 0, 0); got != domain.StatusOnline {
		t.Fatalf("harus online, got %s", got)
	}
	// offlineAfter aktif, staleAfter tidak: langsung ke offline.
	if got := ComputeStatus(10, 0, 5); got != domain.StatusOffline {
		t.Fatalf("harus offline, got %s", got)
	}
}

func TestNewHeartbeatService_AmbangDinormalisasi(t *testing.T) {
	// offlineAfter harus lebih besar dari staleAfter, kalau tidak dinormalisasi
	// status "stale" tidak akan pernah muncul.
	svc := NewHeartbeatService(stubVehicleStatusSource{}, nil, 30, 10)
	if svc.StaleAfter() != 30 {
		t.Fatalf("staleAfter harus 30, got %d", svc.StaleAfter())
	}
	if svc.OfflineAfter() != 31 {
		t.Fatalf("offlineAfter harus dinormalisasi menjadi 31, got %d", svc.OfflineAfter())
	}
	if got := ComputeStatus(30, svc.StaleAfter(), svc.OfflineAfter()); got != domain.StatusStale {
		t.Fatalf("umur 30 dtk harus stale, got %s", got)
	}
	if got := ComputeStatus(31, svc.StaleAfter(), svc.OfflineAfter()); got != domain.StatusOffline {
		t.Fatalf("umur 31 dtk harus offline, got %s", got)
	}
}

func TestHeartbeatService_Report_MenghitungStatusDanSummary(t *testing.T) {
	now := int64(1_700_000_000)
	src := stubVehicleStatusSource{rows: []domain.LocationRow{
		{VehicleID: "ONLINE", Timestamp: now - 5},    // 5 dtk  -> online
		{VehicleID: "STALE", Timestamp: now - 60},    // 60 dtk -> stale
		{VehicleID: "OFFLINE", Timestamp: now - 300}, // 300 dtk -> offline
	}}

	svc := NewHeartbeatService(src, nil, 30, 120)
	svc.SetClock(func() time.Time { return time.Unix(now, 0) })

	report, err := svc.Report()
	if err != nil {
		t.Fatalf("report gagal: %v", err)
	}

	if report.GeneratedAt != now {
		t.Fatalf("generated_at harus %d, got %d", now, report.GeneratedAt)
	}
	if len(report.Vehicles) != 3 {
		t.Fatalf("harus 3 kendaraan, got %d", len(report.Vehicles))
	}

	want := map[string]string{
		"ONLINE":  domain.StatusOnline,
		"STALE":   domain.StatusStale,
		"OFFLINE": domain.StatusOffline,
	}
	wantAge := map[string]int64{"ONLINE": 5, "STALE": 60, "OFFLINE": 300}

	for _, v := range report.Vehicles {
		if v.Status != want[v.VehicleID] {
			t.Fatalf("kendaraan %s harus berstatus %s, got %s", v.VehicleID, want[v.VehicleID], v.Status)
		}
		if v.AgeSeconds != wantAge[v.VehicleID] {
			t.Fatalf("umur %s harus %d dtk, got %d", v.VehicleID, wantAge[v.VehicleID], v.AgeSeconds)
		}
	}

	if report.Summary[domain.StatusOnline] != 1 {
		t.Fatalf("summary online harus 1, got %d", report.Summary[domain.StatusOnline])
	}
	if report.Summary[domain.StatusStale] != 1 {
		t.Fatalf("summary stale harus 1, got %d", report.Summary[domain.StatusStale])
	}
	if report.Summary[domain.StatusOffline] != 1 {
		t.Fatalf("summary offline harus 1, got %d", report.Summary[domain.StatusOffline])
	}
}

func TestHeartbeatService_Report_TimestampKedepanTidakJadiUmurNegatif(t *testing.T) {
	now := int64(1_700_000_000)
	src := stubVehicleStatusSource{rows: []domain.LocationRow{
		{VehicleID: "FUTURE", Timestamp: now + 60},
	}}

	svc := NewHeartbeatService(src, nil, 30, 120)
	svc.SetClock(func() time.Time { return time.Unix(now, 0) })

	report, err := svc.Report()
	if err != nil {
		t.Fatalf("report gagal: %v", err)
	}
	if report.Vehicles[0].AgeSeconds != 0 {
		t.Fatalf("umur tidak boleh negatif, got %d", report.Vehicles[0].AgeSeconds)
	}
}

func TestHeartbeatService_Report_ErrorRepositoryDiteruskan(t *testing.T) {
	boom := errors.New("db down")
	svc := NewHeartbeatService(stubVehicleStatusSource{err: boom}, nil, 30, 120)

	if _, err := svc.Report(); !errors.Is(err, boom) {
		t.Fatalf("error repository harus diteruskan, got %v", err)
	}
}

func TestHeartbeatService_Report_StatusPipeline(t *testing.T) {
	now := int64(1_700_000_000)
	monitor := NewIngestMonitor()

	// Belum ada pesan: pipeline belum sehat karena tidak ada data.
	monitor.SetConnected(true)
	svc := NewHeartbeatService(stubVehicleStatusSource{}, monitor, 30, 120)
	svc.SetClock(func() time.Time { return time.Unix(now, 0) })

	report, _ := svc.Report()
	if !report.Pipeline.MQTTConnected {
		t.Fatal("mqtt_connected harus true")
	}
	if report.Pipeline.MessagesTotal != 0 {
		t.Fatalf("messages_total harus 0, got %d", report.Pipeline.MessagesTotal)
	}
	if report.Pipeline.LastMessageAt != 0 {
		t.Fatalf("last_message_at harus 0, got %d", report.Pipeline.LastMessageAt)
	}

	// Setelah pesan masuk: pipeline sehat dan tercatat.
	monitor.RecordMessage()
	report, _ = svc.Report()
	if report.Pipeline.MessagesTotal != 1 {
		t.Fatalf("messages_total harus 1, got %d", report.Pipeline.MessagesTotal)
	}
	if report.Pipeline.LastMessageAt == 0 {
		t.Fatal("last_message_at harus terisi")
	}
	if !report.Pipeline.Healthy {
		t.Fatal("pipeline harus sehat setelah pesan masuk")
	}

	// Sambungan putus: pipeline tidak sehat lagi.
	monitor.SetConnected(false)
	report, _ = svc.Report()
	if report.Pipeline.Healthy {
		t.Fatal("pipeline tidak boleh sehat saat sambungan putus")
	}
}
