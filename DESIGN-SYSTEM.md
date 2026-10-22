# Design System — Transjakarta Fleet

Dokumen ini menjelaskan arsitektur, komponen, model data, dan keputusan desain
backend **Fleet Management** Transjakarta.

---

## 1. Ringkasan

Backend terdiri dari **tiga binary Go** (`api`, `worker`, `publisher`) yang
berjalan sebagai container terpisah di Docker Compose, dengan pemisahan
lapisan `domain → repository → service → adapter (http/mqtt/messaging)`.

| Service | Peran | Port host |
|---|---|---|
| `api` | Subscriber MQTT, publisher event geofence, REST API, dashboard | 8000 |
| `worker` | Consumer queue `geofence_alerts`, mencatat log event ke DB | — |
| `publisher` | Simulator: publish data mock lokasi tiap 2 detik | — |

Infrastruktur pendukung: PostgreSQL 16, RabbitMQ 3.13, Eclipse Mosquitto 2,
Adminer, Dozzle, MQTT Explorer.

---

## 2. Gambaran Arsitektur

### 2.1 Lapisan aplikasi

```text
┌───────────────────────────────────────────────────────────────────┐
│                          cmd/ (composition root)                  │
│      api/main.go          worker/main.go       publisher/main.go  │
└───────────┬──────────────────────┬──────────────────────┬─────────┘
            │                      │                      │
            ▼                      ▼                      ▼
   ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
   │ internal/http   │    │ internal/mqtt   │    │ internal/       │
   │ internal/mqtt   │    │ internal/       │    │   messaging     │
   │ internal/       │    │   messaging     │    └─────────────────┘
   │   messaging     │    └─────────────────┘
   └────────┬────────┘
            ▼
   ┌─────────────────┐
   │ internal/service│  validasi payload · geofence · state transisi
   └────────┬────────┘
            ▼
   ┌─────────────────┐
   │internal/repository│  seluruh query SQL
   └────────┬────────┘
            ▼
   ┌─────────────────┐
   │ internal/domain │  tipe inti, tanpa dependensi internal
   └─────────────────┘
```

Aturan dependensi: `cmd → adapter → service → repository → domain`.
`domain` tidak mengimpor package internal lain. Tidak ada SQL di luar
`repository`, tidak ada logika bisnis di luar `service`.

Package `http` bergantung pada **antarmuka** (`LocationService`,
`VehicleLister`, `GeofenceEventStore`), bukan struct konkret — sehingga
handler dapat diuji tanpa database.

### 2.2 Diagram runtime

```text
                        ┌──────────────────────┐
                        │  Publisher (Go CLI)  │  simulasi, tiap 2 detik
                        │  cmd/publisher       │  rute shuttle memotong batas
                        └──────────┬───────────┘
                                   │ JSON MQTT QoS 0
                                   │ topic: /fleet/vehicle/{id}/location
                                   ▼
                        ┌──────────────────────┐
                        │ Eclipse Mosquitto    │  :1883 (WS :9001)
                        └──────────┬───────────┘
                                   │ subscribe /fleet/vehicle/+/location
                                   ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │  api service (container: transjakarta_api)                          │
   │                                                                     │
   │  ┌──────────────┐   ┌────────────────┐   ┌──────────────────────┐   │
   │  │ MQTT         │──▶│ LocationService │──▶│ PostgreSQL           │   │
   │  │ Subscriber   │   │  .Insert        │   │ vehicle_loctions     │   │
   │  │ internal/mqtt│   └────────────────┘   └──────────────────────┘   │
   │  └──────────────┘            │                                       │
   │            ┌────────────────┘                                        │
   │            ▼                                                         │
   │  ┌────────────────┐   ┌────────────────┐   ┌──────────────────────┐   │
   │  │ GeofenceTracker│─▶│ RabbitMQ       │   │ PostgreSQL           │   │
   │  │ Haversine+state│   │ publish        │   │ geofence_events      │   │
   │  └────────────────┘   └────────┬───────┘   └──────────▲───────────┘   │
   │                                │                       │               │
   │  ┌──────────────┐              │                       │               │
   │  │ Gin Router   │  :8000       │   queue geofence_alerts│               │
   │  │ internal/http│  /health     │                       │               │
   │  │              │  /vehicles/* │                       │               │
   │  └──────┬───────┘  /api/*      │                       │               │
   │         │          /dashboard  │                       │               │
   └─────────┼─────────────────────┼───────────────────────┼───────────────┘
             │                     │                       │
             │                     ▼                       │
             │            ┌─────────────────┐              │
             │            │ worker service  │──────────────┘
             │            │ cmd/worker      │  INSERT log event
             │            │ internal/messaging                │
             │            └─────────────────┘              │
             ▼                                            │
        Browser / Postman                                │
             │                                            │
             └──── /api/geofence-events ◀────────────────┘
```

### 2.3 Peta jaringan Docker

Semua service berada di network `transjakarta_network` dan saling mengakses
lewat nama container sebagai hostname:

| Dari | Ke | Alamat |
|---|---|---|
| api | postgres | `postgres:5432` |
| api | rabbitmq | `rabbitmq:5672` |
| api | mosquitto | `mosquitto:1883` (TCP), `:9001` (WS) |
| worker | postgres | `postgres:5432` |
| worker | rabbitmq | `rabbitmq:5672` |
| publisher | mosquitto | `mosquitto:1883` |
| mqtt-explorer | mosquitto | `mosquitto:1883` |
| client eksternal | api / ui | `localhost:8000`, `:8080`, `:8088`, `:15672`, `:1883`, `:3001` |

---

## 3. Komponen & Tanggung Jawab

### 3.1 Publisher (`cmd/publisher/main.go`)

- Simulator wajib dari tugas. Terhubung ke broker (dengan retry), lalu publish
  JSON posisi tiap `PUBLISHER_INTERVAL` detik.
- **Rute shuttle deterministik** yang memang dirancang untuk memotong batas
  geofence. Setiap kendaraan bergerak bolak-balik melewati titik pusat
  geofence sepanjang satu garis lurus:

  ```text
  d(t) = 2,5 × radius × sin(2π·t/T + fase)
  ```

  Jarak ke pusat = `|d(t)|`, sehingga batas radius pasti dilewati **dua kali
  per periode** (T = `PUBLISHER_ROUTE_PERIOD_SECONDS`, default 60 detik).
  Fase antar kendaraan berbeda (`2π·i/N`) dan arah garisrutanya disebar
  (`PUBLISHER_ROUTE_SPREAD_DEG`), sehingga kendaraan tidak berbaris.
- **Titik & radius geofence dibaca dari env yang sama dengan service API**
  (`GEOFENCE_LAT/LNG/RADIUS_M`). Dulu publisher memakai titik hardcode, sehingga
  ketika geofence digeser lewat `.env`, rute tidak lagi melewati batas.
- **Timestamp monotonik per kendaraan** (dinaikkan manual bila perlu), agar
  "lokasi terakhir" tidak pernah ambigu.
- Log `trace=<vehicle_id>-<timestamp>`.
- **Catatan desain:** publisher adalah tool demo, bukan bagian dari service
  produksi.

### 3.2 MQTT Subscriber (`internal/mqtt/subscriber.go`)

- Client `paho.mqtt.golang`, ClientID tetap `transjakarta-subscriber`, QoS 0.
- Berlangganan wildcard `/fleet/vehicle/+/location` (single-level, sesuai tugas).
- **Re-subscribe saat reconnect.** Subscription didaftarkan di dalam callback
  `OnConnect`, bukan hanya sekali di `Start()`. Alasannya: broker menghapus
  subscription saat client disconnect (`CleanSession=true`) dan paho v1.5.0
  tidak mengirim ulang paket SUBSCRIBE secara otomatis — tanpa ini subscriber
  akan tersambung kembali tetapi buta dan diam-diam berhenti menerima data.
- **`ExtractVehicleID(topic)`** mengambil `vehicle_id` dari topik
  `/fleet/vehicle/{vehicle_id}/location`; topik yang tidak sesuai pola ditolak.
- Tanggung jawab per pesan:
  1. **Ekstrak** `vehicle_id` dari topik.
  2. **Validasi** — JSON valid + 3 lapis (lihat §4.1).
  3. **Persist** — `LocationService.Insert(...)`.
  4. **Geofence** — hitung jarak Haversine,evaluasi transisi via tracker, log
     `status=INSIDE|OUTSIDE`.
  5. **Publish event** — hanya bila terjadi transisi masuk.
- Log berformat `[RECEIVE]`, `[DB]`, `[GEOFENCE]`, `[RABBITMQ]` + `trace=`.

### 3.3 Geofence Service (`internal/service/geofence_service.go`)

- Fungsi murni tanpa state: `Distance(lat1,lng1,lat2,lng2)` meter dan
  `Evaluate(...)` yang mengembalikan jarak + status di dalam radius.
- `BuildEvent(payload, eventName)` menyusun payload event.
- **Keputusan desain:** rumus Haversine (bukan Euclidean) agar akurat pada
  skala geografis; bola bumi diasumsikan radius 6.371.000 m.

### 3.4 Geofence Tracker (`internal/service/geofence_tracker.go`)

- State machine per kendaraan: `map[vehicle_id]{inside, lastSeen}` + `sync.Mutex`.
- `Evaluate(payload)` mengembalikan jarak, status, dan **event bila terjadi
  transisi**:

  | Kondisi | Event |
  |---|---|
  | dalam ∧ sebelumnya luar | `geofence_entry` |
  | luar ∧ sebelumnya dalam | `geofence_exit` (hanya bila `GEOFENCE_EMIT_EXIT=true`) |
  | status tidak berubah | tidak ada |

- **State awal = "di luar"**, sehingga pesan pertama yang di dalam langsung
  memicu entry.
- **TTL state** (`GEOFENCE_STATE_TTL_SECONDS`, default 60 detik): state yang
  sudah lewat TTL dianggap basi dan dibuang, sehingga transisi masuk berikutnya
  tetap terpicu. Ini mencegah "kendaraan hilang → masuk lagi → event hilang
  permanen". State basi juga dibersihkan berkala agar map tidak membengkak.
- **Keputusan desain:** tracker memakai memori proses. Bila service API
  dijalankan multi-replica, state tiap replika terpisah sehingga satu transisi
  bisa menghasilkan event lebih dari sekali — untuk deployment multi-replica
  state sebaiknya dipindahkan ke penyimpanan bersama (Redis/PostgreSQL).

### 3.5 Location Service (`internal/service/location_service.go`)

- Titik masuk logika data lokasi: validasi payload, insert, dan pemetaan row
  database ke bentuk response API.
- Validasi berada di sini (bukan di adapter) agar dapat dipakai ulang oleh
  publisher, subscriber, dan command lain.
- `ValidationOptions` menyimpan batas toleransi waktu (`MaxFutureSeconds`,
  `MaxAgeSeconds`) dan sumber waktu (`Now`) agar mudah diuji.

### 3.6 Repository (`internal/repository/`)

- Driver `lib/pq`, `sql.DB` (connection pool bawaan).
- Seluruh query **parameterized** (`$1..$4`) untuk mencegah SQL injection.
- `location_repository.go` — insert, last-location, last-location semua
  kendaraan (`DISTINCT ON`), history (dengan `LIMIT`).
- `geofence_event_repository.go` — insert & ambil 100 event terakhir.
- `repository.go` — helper membuka koneksi + `Ping()`.
- **Urutan deterministik:** setiap query yang mengurutkan waktu memakai `id`
  sebagai pemutus seri (`ORDER BY timestamp DESC, id DESC`).

### 3.7 RabbitMQ Client (`internal/messaging/rabbitmq.go`)

- Satu `Connection` dengan **dua channel terpisah**: publish & consume.
- Saat connect: deklarasikan exchange `fleet.events` (topic, durable), queue
  `geofence_alerts` (durable), lalu bind dengan `geofence.*`.
- **Publish yang aman:**
  - `DeliveryMode: amqp.Persistent` — pesan bertahan saat broker restart.
  - `mandatory: true` + `NotifyReturn` — pesan tanpa binding dikembalikan broker
    (tidak hilang diam-diam).
  - **Publisher confirms** (`Confirm(false)` + `NotifyPublish`) — publish
    menunggu konfirmasi broker.
- **Auto-reconnect:** `conn.NotifyClose` memicu re-dial dengan backoff →
  re-declare topologi → lanjutkan consumption. Pemublish yang menemukan
  koneksi mati memanggil `ensureConnected()` lebih dulu.
- **Konsumsi dengan manual ack:** pesan gagal diproses di-`Nack(requeue=true)`
  sekali; bila masih gagal pada pengiriman ulang atau error ditandai
  `ErrPermanent` (mis. JSON rusak), pesan dibuang agar tidak berputar tanpa
  henti.
- Konstanta `ExchangeName`, `QueueName`, `RoutingKey`, `BindingKey` dipakai
  bersama oleh api dan worker agar tidak ada string yang terduplikasi.

### 3.8 Worker (`cmd/worker/main.go`)

- Mengecek payload JSON dari queue, menyimpannya ke tabel
  `geofence_events`, lalu menulis log `[WORKER] trace=... consumed`.
- JSON rusak / event tidak lengkap ditandai `ErrPermanent` sehingga dibuang,
  bukan di-requeue tanpa henti.
- Berjalan sebagai proses terpisah dari API, sehingga event log tetap terlihat
  di dashboard lintas container.

### 3.9 REST API (`internal/http/`)

- Framework Gin. Handler tipis: validasi input → panggil service → bentuk
  response.
- `router.go` pusat pendaftaran route; `handler.go` endpoint armada;
  `dashboard.go` + `dashboard.html` dashboard observability.
- Dashboard HTML di-embed ke binary via `//go:embed dashboard.html`, jadi tidak
  ada file statis eksternal di image.
- **Antarmuka, bukan struct konkret:** `LocationService`, `VehicleLister`,
  `GeofenceEventStore` — membuat handler bisa diuji dengan stub.

### 3.10 Wait Helper (`internal/wait/`)

- `wait.For(ctx, name, opts, fn)` menjalankan `fn` berulang dengan backoff
  eksponensial sampai berhasil atau timeout.
- Dipakai api, worker, dan publisher untuk menunggu PostgreSQL/RabbitMQ/
  Mosquitto siap saat startup.

### 3.11 Heartbeat Service & Ingest Monitor (`internal/service/heartbeat_service.go`)

- `HeartbeatService` menyusun laporan status online/stale/offline tiap
  kendaraan. Sumber datanya adalah `VehicleStatusSource` (interface) —
  diimplementasikan `*repository.LocationRepository` — sehingga service bisa
  diuji tanpa database.
- Status dihitung dari **umur data terakhir** (`now - MAX(timestamp)`),
  bukan dari state di memori. Konsekuensinya: laporan tetap benar setelah
  service di-restart, tidak perlu tabel tambahan, dan tidak perlu
  koordinasi antar replika untuk satu instance.
- `ComputeStatus(age, staleAfter, offlineAfter)` adalah fungsi murni —
  mudah diuji dan dipanggil juga oleh integration test.
- `IngestMonitor` merekam kelangsungan hidup stream MQTT: jumlah pesan
  masuk, pesan terakhir, dan status sambungan broker. Dihubungkan ke
  subscriber lewat interface `mqtt.IngestObserver`, jadi lapisan service
  tidak bergantung pada package MQTT.
- Ditempatkan di `service` (bukan `domain`) karena ia menyimpan state proses;
  tipenya sendiri (`domain.IngestStats`) ada di `domain` agar package
  `http` tidak perlu mengimpor `service`.

### 3.12 Heartbeat Handler (`internal/http/heartbeat.go`)

- Handler tipis dengan interface `HeartbeatProvider`, konsisten dengan
  `LocationService`/`VehicleLister`/`GeofenceEventStore`.
- Route `/api/heartbeat` hanya didaftarkan bila `HEARTBEAT_ENABLED=true`.

---

## 4. Alur Data Detail

### 4.1 Jalur 1 — Ingest & persist (MQTT → PostgreSQL)

```text
Publisher → Mosquitto → Subscriber.handleMessage
  → ExtractVehicleID(topic)
  → unmarshal JSON
  → LocationService.ValidatePayload(payload, topicVehicleID, opts)
  → INSERT INTO vehicle_loctions
  → log [DB] saved
```

Setiap pesan yang lolos validasi menghasilkan **satu baris baru**
(append-only). Tidak ada deduplikasi atau upsert: sistem menyimpan riwayat,
bukan hanya posisi terkini.

**Validasi 3 lapis:**

| Lapis | Aturan | Env |
|---|---|---|
| Format | `vehicle_id` terisi, charset `[A-Za-z0-9_-]`, maks 50 karakter | — |
| Format | `latitude` ∈ [-90, 90], `longitude` ∈ [-180, 180] | — |
| Format | `timestamp` ≠ 0, ≠ negatif | — |
| Konsistensi topik | `vehicle_id` payload = `vehicle_id` topik | — |
| Sanity waktu | `timestamp` ≤ 300 detik ke depan | `LOCATION_MAX_FUTURE_SECONDS` |
| Sanity waktu | `timestamp` ≥ 24 jam ke belakang | `LOCATION_MAX_AGE_SECONDS` |

Tanpa aturan konsistensi topik, pesan yang dikirim ke topik
`/fleet/vehicle/B1234XYZ/location` dengan payload `vehicle_id: "B9999EVIL"`
akan tersimpan dengan label kendaraan yang keliru. Tanpa aturan sanity waktu,
payload bertimestamp tahun 2030 akan **mengambil alih** endpoint "lokasi
terakhir" karena `ORDER BY timestamp DESC` tetap benar secara formal.

### 4.2 Jalur 2 — Geofence → RabbitMQ → Worker

```text
setelah insert:
  dist = Haversine(posisi, pusat)
  log [GEOFENCE] jarak=.. radius=50 status=INSIDE|OUTSIDE
  ev, changed = GeofenceTracker.Evaluate(payload)
  jika ev != nil (transisi masuk):
     publish → exchange fleet.events, routing key geofence.entry
        → queue geofence_alerts
     log [RABBITMQ] published
  worker menerima → INSERT INTO geofence_events + log [WORKER] consumed
```

**Karakteristik penting:**

- Event dikirim **hanya saat transisi luar→dalam**, bukan setiap pesan selama
  di dalam radius. Ini sesuai kalimat tugas *"jika kendaraan **masuk** dalam
  radius 50 meter"*.
- Dampak terukur: rasio event terhadap jumlah pesan turun dari **57–65%**
  (perilaku lama) menjadi **±7%** — sisanya adalah jumlah penyeberangan
  batas yang memang terjadi.
- Konsumsi memakai manual ack + requeue sekali, sehingga queue tetap bisa
  menumpuk saat worker lambat, tetapi pesan tidak hilang saat worker crash.

### 4.3 Jalur 3 — Baca via REST API

```text
Client → Gin → LocationService → repository query → JSON
```

### 4.4 Jalur 4 — Observability

```text
api log ─────────────▶ stdout container ──▶ Dozzle (:8088)
publisher ───────────▶ Mosquitto ─────────▶ MQTT Explorer (:3001)
worker log ──────────▶ stdout container ──▶ Dozzle (:8088)
worker ──INSERT──▶ geofence_events ──▶ /api/geofence-events ──▶ Dashboard
browser ────────────▶ /api/vehicles ────▶ PostgreSQL (lokasi terakhir)
```

Trace ID `{vehicle_id}-{timestamp}` konsisten di seluruh tahap sehingga satu
payload dapat dilacak dari `[PUBLISHER]` sampai `[WORKER]`.

---

## 5. Model Data

### 5.1 Skema PostgreSQL (`migrations/`)

```sql
-- 001_create_vehicle_locations.sql
CREATE TABLE IF NOT EXISTS vehicle_loctions (
  id         SERIAL PRIMARY KEY,
  vehicle_id VARCHAR(50)        NOT NULL,
  latitude   DOUBLE PRECISION   NOT NULL,
  longitude  DOUBLE PRECISION   NOT NULL,
  timestamp  BIGINT             NOT NULL
);
CREATE INDEX idx_vehicle_loctions_vehicle_id_timestamp
  ON vehicle_loctions (vehicle_id, timestamp DESC);
CREATE INDEX idx_vehicle_loctions_timestamp
  ON vehicle_loctions (timestamp);

-- 002_create_geofence_events.sql
CREATE TABLE IF NOT EXISTS geofence_events (
  id         SERIAL PRIMARY KEY,
  vehicle_id VARCHAR(50)        NOT NULL,
  event      VARCHAR(50)        NOT NULL,
  latitude   DOUBLE PRECISION   NOT NULL,
  longitude  DOUBLE PRECISION   NOT NULL,
  timestamp  BIGINT             NOT NULL
);
CREATE INDEX idx_geofence_events_timestamp
  ON geofence_events (timestamp DESC);
```

- Nama tabel **`vehicle_loctions`** sengaja mempertahankan ejaan pada
  spesifikasi tugas (bukan `locations`).
- `timestamp` = Unix seconds (bukan millisecond), konsisten dengan tugas.
- Index komposit `(vehicle_id, timestamp DESC)` melayani query "lokasi terakhir";
  index `timestamp` melayani filter rentang waktu & potensi retensi.
- Folder `migrations/` di-mount ke `/docker-entrypoint-initdb.d`. Script hanya
  berjalan pada volume yang masih kosong; untuk volume lama jalankan manual.

### 5.2 Struktur data Go (`internal/domain/`)

| Struct | File | Field (JSON) |
|---|---|---|
| `LocationPayload` | `location.go` | `vehicle_id`, `latitude`, `longitude`, `timestamp` |
| `LocationResponse` | `location.go` | sama dengan payload (response baca) |
| `Location` | `location.go` | `latitude`, `longitude` |
| `LocationRow` | `location.go` | representasi baris tabel (tanpa JSON tag) |
| `GeofenceEvent` | `geofence.go` | `vehicle_id`, `event`, `location{latitude,longitude}`, `timestamp` |
| `GeofenceConfig` | `geofence.go` | `lat`, `lng`, `radius_m` |
| `VehicleStatus` | `heartbeat.go` | `vehicle_id`, `latitude`, `longitude`, `last_seen`, `age_seconds`, `status` |
| `PipelineStatus` | `heartbeat.go` | `mqtt_connected`, `last_message_at`, `age_seconds`, `messages_total`, `healthy` |
| `HeartbeatReport` | `heartbeat.go` | `generated_at`, `stale_after_seconds`, `offline_after_seconds`, `summary`, `vehicles`, `pipeline` |
| `IngestStats` | `heartbeat.go` | kondisi mentah stream ingest (`mqtt_connected`, `last_message_at`, `messages_total`) |

Konstanta status kendaraan: `StatusOnline = "online"`,
`StatusStale = "stale"`, `StatusOffline = "offline"`.

Konstanta event: `EventNameGeofenceEntry = "geofence_entry"` (wajib sesuai
spesifikasi) dan `EventNameGeofenceExit = "geofence_exit"` (opsional).

### 5.3 Pesan RabbitMQ

```json
{
  "vehicle_id": "B1234XYZ",
  "event": "geofence_entry",
  "location": { "latitude": -6.2088, "longitude": 106.8456 },
  "timestamp": 1715003456
}
```

---

## 6. Kontrak API

| Method | Path | Deskripsi | Sumber data |
|---|---|---|---|
| GET | `/health` | Health check | — |
| GET | `/vehicles/:vehicle_id/location` | Lokasi terakhir kendaraan | PostgreSQL |
| GET | `/vehicles/:vehicle_id/history?start=&end=&limit=` | Riwayat dalam rentang Unix seconds | PostgreSQL |
| GET | `/dashboard` | Halaman HTML dashboard | embedded HTML |
| GET | `/api/vehicles` | Lokasi terakhir **semua** kendaraan | PostgreSQL `DISTINCT ON` |
| GET | `/api/geofence-events` | 100 event geofence terakhir | tabel `geofence_events` |
| GET | `/api/geofence` | Konfigurasi geofence untuk peta | config |
| GET | `/api/heartbeat` | Status online/stale/offline kendaraan + kondisi pipeline | PostgreSQL + `IngestMonitor` |

Body `/api/heartbeat`:

```json
{
  "generated_at": 1715003500,
  "stale_after_seconds": 30,
  "offline_after_seconds": 120,
  "summary": { "online": 1, "stale": 0, "offline": 1 },
  "vehicles": [
    {
      "vehicle_id": "B1234XYZ",
      "latitude": -6.175392,
      "longitude": 106.827153,
      "last_seen": 1715003499,
      "age_seconds": 1,
      "status": "online"
    }
  ],
  "pipeline": {
    "mqtt_connected": true,
    "last_message_at": 1715003499,
    "age_seconds": 1,
    "messages_total": 1024,
    "healthy": true
  }
}
```

`last_seen` = `MAX(timestamp)` di `vehicle_loctions` untuk kendaraan tersebut.

Perilaku error:

- `404 {"error":"not found"}` — kendaraan belum punya data.
- `400 {"error":"start wajib diisi (unix timestamp dalam detik)"}` — parameter hilang.
- `400 {"error":"start harus berupa unix timestamp (detik)"}` — bukan angka.
- `400 {"error":"start terdeteksi dalam milidetik; gunakan unix timestamp dalam detik"}` — salah satuan.
- `400 {"error":"start must be less than or equal to end"}` — rentang terbalik.
- `400 {"error":"limit harus lebih besar dari 0"}` — limit tidak valid.
- `500 {"error":"internal error"}` — kegagalan DB.

---

## 7. Konfigurasi

Dibaca oleh `internal/config/config.go` dari environment variable:

| Variabel | Default | Kegunaan |
|---|---|---|
| `PORT` | `8000` | Port REST API |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/transjakarta?sslmode=disable` | DSN PostgreSQL |
| `MQTT_BROKER` | `tcp://localhost:1883` | Alamat broker MQTT |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | DSN AMQP |
| `GEOFENCE_LAT` | `-6.175392` | Lintang pusat geofence (Monas) |
| `GEOFENCE_LNG` | `106.827153` | Bujur pusat geofence |
| `GEOFENCE_RADIUS_M` | `50` | Radius geofence (meter) |
| `GEOFENCE_STATE_TTL_SECONDS` | `60` | Masa berlaku state "di dalam geofence" |
| `GEOFENCE_EMIT_EXIT` | `false` | Kirim juga event `geofence_exit` |
| `LOCATION_MAX_FUTURE_SECONDS` | `300` | Toleransi timestamp ke depan |
| `LOCATION_MAX_AGE_SECONDS` | `86400` | Toleransi data basi (24 jam) |
| `DEPENDENCY_TIMEOUT_SECONDS` | `120` | Batas menunggu dependency saat startup |
| `HEARTBEAT_ENABLED` | `true` | Daftarkan endpoint `/api/heartbeat` |
| `HEARTBEAT_STALE_SECONDS` | `30` | Ambang status `stale` (data terlalu tua) |
| `HEARTBEAT_OFFLINE_SECONDS` | `120` | Ambang status `offline` (berhenti melapor) |

Variabel publisher:

| Variabel | Default | Kegunaan |
|---|---|---|
| `PUBLISHER_VEHICLES` | `B1234XYZ,B5678ABC` | ID kendaraan yang disimulasikan |
| `PUBLISHER_INTERVAL` | `2` | Interval publish (detik) |
| `PUBLISHER_ROUTE_PERIOD_SECONDS` | `60` | Periode satu putaran rute shuttle |
| `PUBLISHER_ROUTE_BEARING_DEG` | `45` | Arah garis rute terhadap utara |
| `PUBLISHER_ROUTE_SPREAD_DEG` | `20` | Selisih arah antar kendaraan |

Port host (opsional, di `.env`): `POSTGRES_PORT`, `RABBITMQ_PORT`,
`RABBITMQ_MANAGEMENT_PORT`, `MQTT_PORT`, `MQTT_WS_PORT`, `ADMINER_PORT`,
`APP_PORT`, `DOZZLE_PORT`, `MQTT_EXPLORER_PORT`.

---

## 8. Deployment & Build

- **Multi-stage Dockerfile** (root `Dockerfile`): stage builder
  `golang:1.23-alpine` → `CGO_ENABLED=0 go build` untuk ketiga binary →
  stage runtime `alpine:3.20` + `ca-certificates`. Satu image dipakai tiga
  service; service mana yang dijalankan dipilih lewat `command:`.
- **docker-compose.yml** mendefinisikan: `postgres`, `rabbitmq`, `mosquitto`,
  `api`, `worker`, `publisher`, `adminer`, `dozzle`, `mqtt-explorer`.
- Urutan boot diatur `depends_on`: api & worker menunggu `postgres` dan
  `rabbitmq` `service_healthy`, serta `mosquitto` `service_healthy`.
- **Healthcheck**:
  - PostgreSQL — `pg_isready`
  - RabbitMQ — `rabbitmq-diagnostics -q check_port_connectivity` (lebih akurat
    daripada `ping` untuk kesiapan AMQP)
  - Mosquitto — `mosquitto_pub` probe publish (membuktikan broker benar-benar
    siap menerima koneksi, bukan hanya prosesnya jalan)
- Volume persisten: `postgres_data`, `rabbitmq_data`, `mosquitto_data/log`.

---

## 9. Lifecycle Aplikasi

### 9.1 `cmd/api/main.go`

1. `config.Load()` — baca env.
2. `wait.For(postgres)` — buka + `Ping()` PostgreSQL dengan retry/backoff.
3. `wait.For(rabbitmq)` — dial, buat channel, declare exchange/queue/binding.
4. `wait.For(mqtt)` — connect & subscribe (re-subscribe tiap reconnect).
5. `http.NewRouter()` — daftarkan route, jalankan `http.Server` di goroutine.
6. Tunggu `SIGINT`/`SIGTERM` → `server.Shutdown()` dengan timeout 10 detik.

Ketidaktersediaan dependency **tidak** menghentikan proses: service menunggu
sampai batas `DEPENDENCY_TIMEOUT_SECONDS` (default 120 detik) sebelum
mengambil keputusan.

### 9.2 `cmd/worker/main.go`

1. `config.Load()`.
2. `wait.For(postgres)` — untuk menulis log event.
3. `wait.For(rabbitmq)` — declare exchange/queue/binding.
4. `ConsumeGeofenceAlerts(...)` — insert ke `geofence_events` + log.
5. Tunggu `SIGINT`/`SIGTERM` → tutup koneksi.

### 9.3 `cmd/publisher/main.go`

1. `config.Load()` — termasuk titik & radius geofence.
2. `wait.For(mqtt broker)` — connect dengan retry.
3. Loop publish tiap `PUBLISHER_INTERVAL` detik mengikuti rute shuttle.
4. Tunggu `SIGINT`/`SIGTERM` → disconnect.

---

## 10. Keputusan Desain (Ringkasan)

| # | Keputusan | Alasan / trade-off |
|---|---|---|
| 1 | Tiga binary terpisah (`api`, `worker`, `publisher`) | Setiap service punya scaling & failure domain sendiri; trade-off: satu service crash tidak mematikan yang lain |
| 2 | Lapisan `domain / repository / service / adapter` | Dependensi satu arah, SQL terisolasi, logika bisnis teruji tanpa infra |
| 3 | Tabel append-only, tanpa dedup | Menyimpan riwayat penuh perjalanan; trade-off: volume tumbuh ~1 baris/detik saat demo |
| 4 | Haversine | Akurasi lebih baik dari jarak Euclidean untuk koordinat lintang-bujur |
| 5 | Event geofence **berbasis transisi** (bukan per-pesan) | Sesuai kalimat tugas "jika kendaraan **masuk**"; mencegah queue terisi event berulang tiap 2 detik. Trade-off: state berada di memori proses (lihat §3.4) |
| 6 | Log event geofence ke tabel `geofence_events` | Dashboard tetap jalan lintas container; trade-off: satu tabel tambahan di luar spesifikasi tugas |
| 7 | `//go:embed` untuk HTML dashboard | Binary mandiri, tidak perlu volume/file statis |
| 8 | Konsumsi **manual ack + requeue sekali** | Pesan tidak hilang saat worker crash; trade-off: perlu membedakan poison message (`ErrPermanent`) |
| 9 | Nama tabel `vehicle_loctions` | Mengikuti ejaan spesifikasi tugas secara harfiah |
| 10 | Trace ID `{vehicle_id}-{timestamp}` | Korelasi log antar tahap tanpa infrastruktur tracing |
| 11 | Re-subscribe di `OnConnect` | Broker menghapus subscription saat disconnect; tanpa ini pipeline diam-diam mati |
| 12 | Validasi 3 lapis (format/topic/waktu) | Menutup celah data: label kendaraan salah & timestamp mustahil yang mengambil alih "lokasi terakhir" |
| 13 | Retry/backoff saat startup (`internal/wait`) | Startup tidak bergantung urutan container; tanpa ini service crash-loop |
| 14 | Publisher + persistent + publisher confirms | Pesan tidak hilang diam-diam; broker menyimpan pesan sampai queue benar-benar kosong |
| 15 | Auto-reconnect RabbitMQ | Restart broker tidak lagi mematikan publish & consume secara permanen |
| 16 | Rute publisher yang deterministik & memotong batas | Geofence bisa diamati/diuji, bukan bergantung drift acak |
| 17 | Antarmuka pada package `http` | Handler dapat diuji tanpa database |
| 18 | `ORDER BY timestamp …, id …` | Hasil deterministik saat beberapa baris memiliki timestamp sama |
| 19 | Status heartbeat **dihitung** dari umur data, bukan disimpan | Tidak perlu tabel/state baru, tetap benar setelah restart, dan bisa diverifikasi lewat SQL (`last_seen` = `MAX(timestamp)`); trade-off: hanya knows vehicles yang pernah melapor, dan deteksi baru terlihat saat ada yang membaca laporan |
| 20 | Heartbeat sebagai endpoint HTTP, bukan event RabbitMQ baru | Tidak menambah event di luar spesifikasi `geofence_entry`, tidak menambah queue/binding, dan tidak menambah permukaan kegagalan |

---

## 11. Keterbatasan & Rekomendasi (Technical Debt)

### 11.1 State geofence di memori proses ⚠️

`GeofenceTracker` menyimpan state per kendaraan di memori service API. Bila
service API dijalankan lebih dari satu replika, masing-masing replika memiliki
state sendiri sehingga satu transisi bisa menghasilkan event lebih dari
satu kali. **Rekomendasi:** pindahkan state ke penyimpanan bersama (Redis atau
kolom state di PostgreSQL) bila deployment menjadi multi-replica.

### 11.2 Pertumbuhan data tanpa retensi

Tabel tumbuh tanpa batas. **Rekomendasi:** job retensi berkala (hapus data lebih
tua dari N menit/hari) atau partisi berdasarkan waktu.

### 11.3 QoS 0 pada publisher

MQTT QoS 0 = paling cepat, tanpa jaminan. **Rekomendasi:** naikkan ke QoS 1 untuk
data lokasi jika kehilangan pesan tidak dapat ditoleransi.

### 11.4 Belum ada autentikasi & TLS

Mosquitto `allow_anonymous true`; API terbuka tanpa auth. **Rekomendasi:**
kredensial MQTT, TLS, dan auth API bila dipakai di luar demo.

### 11.5 Unit test repository belum ada

Repository sulit diuji unit tanpa database; saat ini cakupan ada di
integration test (query SQL nyata). **Rekomendasi:** gunakan `sqlmock` atau
container test untuk mengunci bentuk query.

### 11.6 Batas "poison message" masih sederhana

Worker meng-requeue satu kali lalu membuang. **Rekomendasi:** quorum queue
(`x-queue-type: quorum`) dan dead-letter exchange untuk pesan yang gagal
berulang.

### 11.7 Deteksi offline bersifat pasif (per-request)

Status `offline` dihitung saat laporan heartbeat diminta, sehingga transisi
online → offline **tidak dipublikasikan** sebagai event. Konsekuensinya,
integrasi eksternal (mis. notifikasi WhatsApp/email) harus polled.
**Rekomendasi:** bila notifikasi real-time dibutuhkan, tambahkan event
`vehicle.offline` ke exchange `fleet.events` dengan routing key & binding
baru (jangan reuse binding `geofence.*`).

### 11.8 Armada besar = laporan heartbeat terpotong

`HeartbeatService` membatasi laporan pada 1000 kendaraan
(`service.MaxHeartbeatVehicles`). **Rekomendasi:** bila armada melebihi itu,
tambahkan pagination atau filter status (`?status=offline`).

---

## 12. Matriks Traceability Tugas → Implementasi

| Butir tugas | Implementasi | Lokasi |
|---|---|---|
| Subscriber MQTT topik `/fleet/vehicle/{id}/location` | `Subscribe("/fleet/vehicle/+/location")` | `internal/mqtt/subscriber.go` |
| Validasi format data | `ValidatePayload` + `ExtractVehicleID` + batas waktu | `internal/service/location_service.go`, `internal/mqtt/subscriber.go` |
| Simpan ke tabel `vehicle_loctions` | `INSERT INTO vehicle_loctions` | `internal/repository/location_repository.go` |
| REST API Gin — lokasi terakhir | `GET /vehicles/:vehicle_id/location` | `internal/http/handler.go` |
| REST API Gin — riwayat | `GET /vehicles/:vehicle_id/history` | `internal/http/handler.go` |
| Geofence radius 50 m | `Distance` + `GEOFENCE_RADIUS_M` | `internal/service/geofence_service.go` |
| Event saat kendaraan **masuk** radius | `GeofenceTracker` transisi masuk | `internal/service/geofence_tracker.go` |
| Exchange `fleet.events`, queue `geofence_alerts` | declare + bind `geofence.*` | `internal/messaging/rabbitmq.go` |
| Format pesan event | `domain.GeofenceEvent` | `internal/domain/geofence.go` |
| Worker baca `geofence_alerts` | `ConsumeGeofenceAlerts` | `cmd/worker/main.go` |
| Docker Compose (app, PG, RabbitMQ, Mosquitto) | service definitions | `docker-compose.yml` |
| Dockerfile Go | multi-stage build, 3 binary | `Dockerfile` |
| Script publisher mock tiap 2 detik | ticker + rute shuttle | `cmd/publisher/main.go` |

---

## 13. Lampiran — Peta File

```text
transjakarta-fleet/
├── cmd/
│   ├── api/main.go                 # composition root: subscriber + API + publish event
│   ├── worker/main.go              # consumer geofence_alerts → simpan log event
│   └── publisher/main.go           # simulator MQTT + rute shuttle
├── internal/
│   ├── config/config.go            # pembacaan env
│   ├── domain/                     # tipe inti
│   │   ├── location.go
│   │   ├── geofence.go
│   │   └── heartbeat.go
│   ├── mqtt/subscriber.go          # subscriber + re-subscribe + parser topik + observer
│   ├── repository/                 # lapisan data
│   │   ├── repository.go           # helper koneksi
│   │   ├── location_repository.go
│   │   └── geofence_event_repository.go
│   ├── service/                    # logika bisnis
│   │   ├── location_service.go
│   │   ├── geofence_service.go
│   │   ├── geofence_tracker.go
│   │   └── heartbeat_service.go    # status online/stale/offline + ingest monitor
│   ├── http/                       # REST API + dashboard
│   │   ├── handler.go
│   │   ├── router.go
│   │   ├── dashboard.go
│   │   ├── heartbeat.go
│   │   └── dashboard.html
│   ├── messaging/rabbitmq.go       # AMQP client (publish + consume + reconnect)
│   ├── wait/wait.go                # retry/backoff dependency
│   └── integration/integration_test.go  # integration test (build tag)
├── migrations/
│   ├── 001_create_vehicle_locations.sql
│   └── 002_create_geofence_events.sql
├── mosquitto/mosquitto.conf        # listener 1883, anon, persistence
├── mqtt-explorer/settings.json     # pre-config koneksi MQTT Explorer
├── postman/
│   ├── fleet-api.postman_collection.json
│   └── postman_environment.json
├── docs/
│   ├── FLOWCHART.md                # flowchart lengkap (Mermaid + PNG), generated
│   └── diagrams/
│       ├── 01-alur-end-to-end.mmd  # sumber diagram (single source of truth)
│       ├── 02-validasi.mmd
│       ├── 03-geofence.mmd
│       ├── 04-status-heartbeat.mmd
│       ├── 05-timeline.mmd
│       ├── 06-peta-folder.mmd
│       ├── *.png                   # hasil render PNG (2x)
│       ├── render.sh               # render PNG + tulis ulang FLOWCHART.md
│       ├── build_md.py             # generator FLOWCHART.md dari .mmd
│       ├── mermaid-config.json     # tema Mermaid
│       └── puppeteer-config.json   # path chromium headless
├── Dockerfile                      # multi-stage build, 3 binary
├── docker-compose.yml              # orkestrasi 9 service
├── Makefile                        # build / test / up / logs
├── go.mod
├── go.sum
├── .env.example
├── .gitignore
├── SYSTEM.md                       # gambaran sistem
├── README.md                       # cara pakai
└── DESIGN-SYSTEM.md                # dokumen ini
```
