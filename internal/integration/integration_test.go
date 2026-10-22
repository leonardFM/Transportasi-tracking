//go:build integration

// Package integration berisi pengujian end-to-end terhadap stack yang sedang
// berjalan (PostgreSQL + RabbitMQ + Mosquitto + service API).
//
// Pengujian ini adalah "pembuktian" yang diminta spesifikasi: setiap
// perilaku diperiksa pada tiga sisi sekaligus - database, respons HTTP, dan
// RabbitMQ (management API) - bukan sekadar membaca log.
//
// Cara menjalankan:
//
//	go test -tags integration ./internal/integration/... -v
//
// Konfigurasi lewat environment variable (nilai bawaan sesuai docker compose):
//
//	TEST_DATABASE_URL, TEST_API_URL, TEST_MQTT_BROKER,
//	TEST_RABBITMQ_MANAGEMENT_URL, TEST_RABBITMQ_USER, TEST_RABBITMQ_PASS
package integration

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	_ "github.com/lib/pq"
)

const (
	rabbitExchange = "fleet.events"
	rabbitQueue    = "geofence_alerts"
	rabbitVhost    = "%2F"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func databaseURL() string {
	return envOr("TEST_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/transjakarta?sslmode=disable")
}

func apiURL() string {
	return envOr("TEST_API_URL", "http://localhost:8000")
}

func mqttBroker() string {
	return envOr("TEST_MQTT_BROKER", "tcp://localhost:1883")
}

func rabbitManagementURL() string {
	return envOr("TEST_RABBITMQ_MANAGEMENT_URL", "http://localhost:15672")
}

func rabbitUser() string { return envOr("TEST_RABBITMQ_USER", "guest") }
func rabbitPass() string { return envOr("TEST_RABBITMQ_PASS", "guest") }

// openDB membuka koneksi ke PostgreSQL untuk pengujian.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		t.Fatalf("buka database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping database: %v", err)
	}
	return db
}

// publishLocation mengirim satu payload ke topik MQTT.
func publishLocation(t *testing.T, vehicleID string, lat, lon float64, ts int64) {
	t.Helper()

	payload := map[string]any{
		"vehicle_id": vehicleID,
		"latitude":   lat,
		"longitude":  lon,
		"timestamp":  ts,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(mqttBroker())
	opts.SetClientID(fmt.Sprintf("integration-test-%d", time.Now().UnixNano()))
	opts.SetConnectTimeout(10 * time.Second)

	client := paho.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		t.Fatalf("connect mqtt: %v", err)
	}
	defer client.Disconnect(100)

	topic := fmt.Sprintf("/fleet/vehicle/%s/location", vehicleID)
	pubToken := client.Publish(topic, 0, false, body)
	pubToken.Wait()
	if err := pubToken.Error(); err != nil {
		t.Fatalf("publish %s: %v", topic, err)
	}
}

// publishRawTopic mengirim payload mentah ke topik tertentu (dipakai untuk
// menguji ketidakcocokan vehicle_id dengan topik).
func publishRawTopic(t *testing.T, topic string, body []byte) {
	t.Helper()

	opts := paho.NewClientOptions()
	opts.AddBroker(mqttBroker())
	opts.SetClientID(fmt.Sprintf("integration-test-raw-%d", time.Now().UnixNano()))
	opts.SetConnectTimeout(10 * time.Second)

	client := paho.NewClient(opts)
	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		t.Fatalf("connect mqtt: %v", err)
	}
	defer client.Disconnect(100)

	pubToken := client.Publish(topic, 0, false, body)
	pubToken.Wait()
	if err := pubToken.Error(); err != nil {
		t.Fatalf("publish %s: %v", topic, err)
	}
}

// waitFor menunggu sampai kondisi terpenuhi.
func waitFor(t *testing.T, timeout time.Duration, description string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timeout menunggu: %s", description)
}

// countLocations menghitung jumlah baris untuk sebuah kendaraan.
func countLocations(t *testing.T, db *sql.DB, vehicleID string) int {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT count(*) FROM vehicle_loctions WHERE vehicle_id = $1`, vehicleID).Scan(&count)
	if err != nil {
		t.Fatalf("count locations: %v", err)
	}
	return count
}

// countEvents menghitung jumlah event geofence untuk sebuah kendaraan.
func countEvents(t *testing.T, db *sql.DB, vehicleID string) int {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT count(*) FROM geofence_events WHERE vehicle_id = $1`, vehicleID).Scan(&count)
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	return count
}

// httpGet melakukan permintaan GET ke API dan mengembalikan status + body.
func httpGet(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(apiURL() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, buf
}

// rabbitAPI melakukan permintaan GET ke management API RabbitMQ.
func rabbitAPI(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", rabbitManagementURL()+path, nil)
	if err != nil {
		t.Fatalf("request rabbit: %v", err)
	}
	req.SetBasicAuth(rabbitUser(), rabbitPass())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET rabbit %s: %v", path, err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, buf
}

// geofenceConfig membaca konfigurasi geofence dari API.
func geofenceConfig(t *testing.T) (lat, lng, radiusM float64) {
	t.Helper()
	status, body := httpGet(t, "/api/geofence")
	if status != 200 {
		t.Fatalf("GET /api/geofence harus 200, got %d", status)
	}
	var cfg struct {
		Lat     float64 `json:"lat"`
		Lng     float64 `json:"lng"`
		RadiusM float64 `json:"radius_m"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decode geofence config: %v", err)
	}
	return cfg.Lat, cfg.Lng, cfg.RadiusM
}

// offsetCoordinate menghasilkan koordinat berjarak ±meter dari titik pusat.
func offsetCoordinate(lat, lng, meters float64) (float64, float64) {
	const metersPerDegree = 111320.0
	return lat + meters/metersPerDegree, lng
}

// TestPipeline_MQTTKeDatabaseKeHTTP menguji rantai lengkap: publish ke MQTT,
// data masuk ke database, dan API mengembalikan lokasi yang benar.
func TestPipeline_MQTTKeDatabaseKeHTTP(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	vehicleID := fmt.Sprintf("ITPIPE%d", time.Now().Unix()%100000)
	now := time.Now().Unix()

	publishLocation(t, vehicleID, -6.175392, 106.827153, now)

	waitFor(t, 15*time.Second, "baris lokasi muncul di database", func() bool {
		return countLocations(t, db, vehicleID) > 0
	})

	var gotLat, gotLng float64
	var gotTS int64
	err := db.QueryRow(
		`SELECT latitude, longitude, timestamp FROM vehicle_loctions WHERE vehicle_id = $1 ORDER BY timestamp DESC LIMIT 1`,
		vehicleID,
	).Scan(&gotLat, &gotLng, &gotTS)
	if err != nil {
		t.Fatalf("query lokasi: %v", err)
	}
	if gotTS != now {
		t.Fatalf("timestamp tersimpan %d, harusnya %d", gotTS, now)
	}

	// Sisi HTTP: endpoint lokasi terakhir harus mengembalikan baris yang sama.
	status, body := httpGet(t, "/vehicles/"+vehicleID+"/location")
	if status != 200 {
		t.Fatalf("GET lokasi harus 200, got %d: %s", status, body)
	}
	var resp struct {
		VehicleID string  `json:"vehicle_id"`
		Latitude  float64 `json:"latitude"`
		Timestamp int64   `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode lokasi: %v", err)
	}
	if resp.VehicleID != vehicleID || resp.Timestamp != now {
		t.Fatalf("respons API salah: %+v", resp)
	}

	// Sisi HTTP: riwayat harus memuat data tersebut.
	status, body = httpGet(t, fmt.Sprintf("/vehicles/%s/history?start=%d&end=%d", vehicleID, now-60, now+60))
	if status != 200 {
		t.Fatalf("GET history harus 200, got %d", status)
	}
	var history []struct {
		Timestamp int64 `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("riwayat harus memuat data yang dikirim")
	}
}

// TestValidasi_TolakVehicleIDTidakCocokDenganTopik membuktikan bahwa payload
// dengan vehicle_id berbeda dari topik TIDAK tersimpan.
func TestValidasi_TolakVehicleIDTidakCocokDenganTopik(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	topicVehicle := fmt.Sprintf("ITMISM%d", time.Now().Unix()%100000)
	payloadVehicle := "B9999EVIL"
	now := time.Now().Unix()

	body := fmt.Sprintf(`{"vehicle_id":%q,"latitude":-6.175392,"longitude":106.827153,"timestamp":%d}`,
		payloadVehicle, now)
	publishRawTopic(t, "/fleet/vehicle/"+topicVehicle+"/location", []byte(body))

	// Beri waktu pada subscriber untuk memproses (dan menolak) pesan.
	time.Sleep(3 * time.Second)

	if got := countLocations(t, db, payloadVehicle); got != 0 {
		t.Fatalf("payload dengan vehicle_id %q harus ditolak, tapi ada %d baris", payloadVehicle, got)
	}
	if got := countLocations(t, db, topicVehicle); got != 0 {
		t.Fatalf("tidak boleh ada baris untuk %q, ada %d", topicVehicle, got)
	}
}

// TestValidasi_TolakKoordinatDiLuarRentang membuktikan koordinat tidak valid
// tidak pernah menyentuh database.
func TestValidasi_TolakKoordinatDiLuarRentang(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	vehicleID := fmt.Sprintf("ITCOORD%d", time.Now().Unix()%100000)
	publishLocation(t, vehicleID, 999.5, 106.827153, time.Now().Unix())

	time.Sleep(3 * time.Second)

	if got := countLocations(t, db, vehicleID); got != 0 {
		t.Fatalf("latitude 999.5 harus ditolak, ada %d baris", got)
	}
}

// TestValidasi_TolakTimestampTerlaluJauhKeDepan membuktikan timestamp mustahil
// ditolak, sehingga tidak bisa mengambil alih endpoint "lokasi terakhir".
func TestValidasi_TolakTimestampTerlaluJauhKeDepan(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	vehicleID := fmt.Sprintf("ITFUTURE%d", time.Now().Unix()%100000)
	future := time.Now().Unix() + 86400 // 1 hari ke depan, melewati toleransi

	publishLocation(t, vehicleID, -6.175392, 106.827153, future)

	time.Sleep(3 * time.Second)

	if got := countLocations(t, db, vehicleID); got != 0 {
		t.Fatalf("timestamp 1 hari ke depan harus ditolak, ada %d baris", got)
	}
}

// TestGeofence_HanyaSekaliEventPerTransisi membuktikan event geofence tidak
// diulang setiap pesan selama kendaraan berada di dalam area.
func TestGeofence_HanyaSekaliEventPerTransisi(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	centerLat, centerLng, radius := geofenceConfig(t)
	vehicleID := fmt.Sprintf("ITGEO%d", time.Now().Unix()%100000)

	outsideLat, outsideLng := offsetCoordinate(centerLat, centerLng, radius+200)
	insideLat, insideLng := offsetCoordinate(centerLat, centerLng, 0)

	// 1) Mulai dari luar area.
	publishLocation(t, vehicleID, outsideLat, outsideLng, time.Now().Unix())
	time.Sleep(2 * time.Second)

	// 2) Lima pesan berturut-turut di dalam area: hanya transisi pertama yang
	//    boleh menghasilkan event.
	base := time.Now().Unix()
	for i := 1; i <= 5; i++ {
		publishLocation(t, vehicleID, insideLat, insideLng, base+int64(i))
		time.Sleep(300 * time.Millisecond)
	}

	// 3) Event harus sampai ke worker dan tercatat di tabel geofence_events.
	waitFor(t, 20*time.Second, "event geofence tercatat di database", func() bool {
		return countEvents(t, db, vehicleID) > 0
	})

	// Beri waktu tambahan untuk memastikan tidak ada event berulang.
	time.Sleep(5 * time.Second)

	entries := countEvents(t, db, vehicleID)
	if entries != 1 {
		t.Fatalf("event geofence harus tepat 1 (hanya saat transisi masuk), got %d", entries)
	}

	// Sisi RabbitMQ: event harus benar-benar melalui exchange/queue.
	status, body := rabbitAPI(t, "/api/queues/"+rabbitVhost+"/"+rabbitQueue)
	if status != 200 {
		t.Fatalf("queue %s harus ada di RabbitMQ, got status %d", rabbitQueue, status)
	}
	var queue struct {
		Name      string `json:"name"`
		Consumers int    `json:"consumers"`
	}
	if err := json.Unmarshal(body, &queue); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if queue.Name != rabbitQueue {
		t.Fatalf("nama queue tidak sesuai: %s", queue.Name)
	}
	if queue.Consumers < 1 {
		t.Fatalf("queue %s harus punya minimal 1 consumer (worker), got %d", rabbitQueue, queue.Consumers)
	}
}

// TestRabbitMQ_TopologiNyata membuktikan exchange, queue, dan binding benar-benar
// dibuat di broker (bukan sekadar diasumsikan).
func TestRabbitMQ_TopologiNyata(t *testing.T) {
	status, body := rabbitAPI(t, "/api/exchanges/"+rabbitVhost+"/"+rabbitExchange)
	if status != 200 {
		t.Fatalf("exchange %s harus ada, got status %d: %s", rabbitExchange, status, body)
	}
	var exchange struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &exchange); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if exchange.Type != "topic" {
		t.Fatalf("exchange %s harus bertipe topic, got %s", rabbitExchange, exchange.Type)
	}

	status, body = rabbitAPI(t, "/api/queues/"+rabbitVhost+"/"+rabbitQueue)
	if status != 200 {
		t.Fatalf("queue %s harus ada, got status %d", rabbitQueue, status)
	}

	status, body = rabbitAPI(t, "/api/bindings/"+rabbitVhost+"/e/"+rabbitExchange+"/q/"+rabbitQueue)
	if status != 200 {
		t.Fatalf("binding %s -> %s harus ada, got status %d", rabbitExchange, rabbitQueue, status)
	}
	var bindings []struct {
		RoutingKey string `json:"routing_key"`
	}
	if err := json.Unmarshal(body, &bindings); err != nil {
		t.Fatalf("decode binding: %v", err)
	}
	found := false
	for _, b := range bindings {
		if b.RoutingKey == "geofence.*" {
			found = true
		}
	}
	if !found {
		t.Fatalf("binding dengan routing key geofence.* tidak ditemukan: %s", body)
	}
}

// TestAPI_Errors proves endpoint mengembalikan status yang benar untuk
// permintaan yang tidak valid.
func TestAPI_Errors(t *testing.T) {
	cases := []struct {
		name string
		path string
		want int
	}{
		{"kendaraan tidak dikenal", "/vehicles/TIDAKADA123/location", 404},
		{"start/end tidak valid", "/vehicles/B1/history?start=abc&end=1", 400},
		{"start lebih besar dari end", "/vehicles/B1/history?start=200&end=100", 400},
		{"satuan milidetik", "/vehicles/B1/history?start=1700000000000&end=1700000600000", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := httpGet(t, c.path)
			if status != c.want {
				t.Fatalf("harus %d, got %d: %s", c.want, status, body)
			}
			if !strings.Contains(string(body), "error") {
				t.Fatalf("respons harus memuat pesan error, got: %s", body)
			}
		})
	}
}

// TestAPI_HealthAndDashboard memeriksa endpoint pendukung.
func TestAPI_HealthAndDashboard(t *testing.T) {
	for _, path := range []string{"/health", "/dashboard", "/api/vehicles", "/api/geofence-events", "/api/geofence", "/api/heartbeat"} {
		status, body := httpGet(t, path)
		if status != 200 {
			t.Fatalf("GET %s harus 200, got %d: %s", path, status, body)
		}
	}
}

// TestHeartbeat_StatusOnlineStaleOffline membuktikan deteksi offline dari
// sisi HTTP sekaligus mencocokkannya dengan isi database.
//
// Tiga kendaraan sintetis dikirim dengan umur berbeda:
//
//   - ITHB<suffix>ON  : timestamp sekarang            -> online
//   - ITHB<suffix>ST  : timestamp 60 detik lalu        -> stale
//   - ITHB<suffix>OFF : timestamp 300 detik lalu       -> offline
//
// Timestamp lama tetap lolos validasi (batas data basi 24 jam), sehingga
// ketiga status terdeterministik dan dapat dibuktikan lewat HTTP + SQL.
func TestHeartbeat_StatusOnlineStaleOffline(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	suffix := fmt.Sprintf("%d", time.Now().Unix()%100000)
	now := time.Now().Unix()

	vehicles := []struct {
		id         string
		ageSeconds int64
		wantStatus string
	}{
		{"ITHB" + suffix + "ON", 0, "online"},
		{"ITHB" + suffix + "ST", 60, "stale"},
		{"ITHB" + suffix + "OFF", 300, "offline"},
	}

	for _, v := range vehicles {
		publishLocation(t, v.id, -6.175392, 106.827153, now-v.ageSeconds)
	}

	// Pastikan semua baris sudah tersimpan di database.
	for _, v := range vehicles {
		v := v
		waitFor(t, 15*time.Second, "baris lokasi "+v.id+" muncul di database", func() bool {
			return countLocations(t, db, v.id) > 0
		})
	}

	// Ambil laporan heartbeat.
	status, body := httpGet(t, "/api/heartbeat")
	if status != 200 {
		t.Fatalf("GET /api/heartbeat harus 200, got %d: %s", status, body)
	}

	var report struct {
		GeneratedAt  int64          `json:"generated_at"`
		StaleAfter   int64          `json:"stale_after_seconds"`
		OfflineAfter int64          `json:"offline_after_seconds"`
		Summary      map[string]int `json:"summary"`
		Vehicles     []struct {
			VehicleID  string  `json:"vehicle_id"`
			LastSeen   int64   `json:"last_seen"`
			AgeSeconds int64   `json:"age_seconds"`
			Status     string  `json:"status"`
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
		} `json:"vehicles"`
		Pipeline struct {
			MQTTConnected bool  `json:"mqtt_connected"`
			LastMessageAt int64 `json:"last_message_at"`
			AgeSeconds    int64 `json:"age_seconds"`
			MessagesTotal int64 `json:"messages_total"`
			Healthy       bool  `json:"healthy"`
		} `json:"pipeline"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode heartbeat: %v (body: %s)", err, body)
	}

	// Pipeline ingest harus dilaporkan hidup (level 2).
	if !report.Pipeline.MQTTConnected {
		t.Fatal("mqtt_connected harus true")
	}
	if report.Pipeline.MessagesTotal <= 0 {
		t.Fatalf("messages_total harus > 0, got %d", report.Pipeline.MessagesTotal)
	}
	if !report.Pipeline.Healthy {
		t.Fatalf("pipeline harus healthy: %s", body)
	}

	// Ambang yang dipakai harus sesuai default 30 dtk / 120 dtk.
	if report.StaleAfter != 30 || report.OfflineAfter != 120 {
		t.Fatalf("ambang harus 30/120 dtk, got %d/%d", report.StaleAfter, report.OfflineAfter)
	}

	byID := map[string]int{}
	for i, v := range report.Vehicles {
		byID[v.VehicleID] = i
	}

	for _, v := range vehicles {
		idx, ok := byID[v.id]
		if !ok {
			t.Fatalf("kendaraan %s tidak ada di laporan heartbeat: %s", v.id, body)
		}
		got := report.Vehicles[idx]
		if got.Status != v.wantStatus {
			t.Fatalf("kendaraan %s harus berstatus %s, got %s", v.id, v.wantStatus, got.Status)
		}
		if got.AgeSeconds < v.ageSeconds {
			t.Fatalf("kendaraan %s umur minimal %d dtk, got %d", v.id, v.ageSeconds, got.AgeSeconds)
		}

		// Bukti silang dengan database: last_seen harus sama dengan
		// MAX(timestamp) di SQL.
		var maxTS int64
		if err := db.QueryRow(
			`SELECT COALESCE(MAX(timestamp), 0) FROM vehicle_loctions WHERE vehicle_id = $1`, v.id,
		).Scan(&maxTS); err != nil {
			t.Fatalf("query max timestamp: %v", err)
		}
		if got.LastSeen != maxTS {
			t.Fatalf("kendaraan %s: last_seen di API (%d) harus sama dengan MAX(timestamp) di DB (%d)",
				v.id, got.LastSeen, maxTS)
		}
	}

	// Ringkasan harus konsisten dengan daftar kendaraan.
	for _, statusName := range []string{"online", "stale", "offline"} {
		if _, ok := report.Summary[statusName]; !ok {
			t.Fatalf("summary harus memuat key %s: %s", statusName, body)
		}
	}
	if report.Summary["offline"] < 1 {
		t.Fatalf("summary offline harus >= 1, got %d", report.Summary["offline"])
	}
	if report.Summary["stale"] < 1 {
		t.Fatalf("summary stale harus >= 1, got %d", report.Summary["stale"])
	}
}

// TestURL_Validasi memastikan helper URL tidak merusak path.
func TestURL_Validasi(t *testing.T) {
	if _, err := url.Parse(apiURL()); err != nil {
		t.Fatalf("API URL tidak valid: %v", err)
	}
}
