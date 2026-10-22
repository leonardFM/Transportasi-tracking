# Transjakarta Fleet

Backend sistem manajemen armada kendaraan: menerima data lokasi via **MQTT**,
memvalidasinya, menyimpannya ke **PostgreSQL**, mendeteksi **geofence**
(radius 50 meter) **berbasis transisi**, mempublikasikan event ke **RabbitMQ**,
dan menyajikan **REST API** + dashboard.

> 📊 **Flowchart lengkap** (Mermaid + PNG): [`docs/FLOWCHART.md`](docs/FLOWCHART.md)
> — alur end-to-end, validasi 3 lapis, state machine geofence, dan
> perhitungan status `online`/`stale`/`offline`.

## Struktur Folder

```text
transjakarta-fleet/
├── cmd/
│   ├── api/                          # Service API + ingest MQTT + publish event geofence
│   │   └── main.go
│   ├── worker/                       # Service consumer queue geofence_alerts
│   │   └── main.go
│   └── publisher/                    # Simulator: publish data mock tiap 2 detik
│       └── main.go
├── internal/
│   ├── config/                       # Pembacaan environment variable
│   ├── domain/                       # Tipe inti (Location, GeofenceEvent, ...)
│   ├── mqtt/
│   │   ├── subscriber.go             # Subscriber MQTT + parser topik
│   │   └── subscriber_test.go
│   ├── repository/                   # Lapisan akses data (SQL)
│   │   ├── location_repository.go
│   │   ├── geofence_event_repository.go
│   │   └── repository.go             # Helper koneksi database
│   ├── service/                      # Logika bisnis
│   │   ├── location_service.go       # Validasi payload
│   │   ├── geofence_service.go       # Haversine + evaluasi radius
│   │   └── geofence_tracker.go       # State machine transisi masuk/keluar
│   ├── http/                         # REST API + dashboard
│   │   ├── handler.go
│   │   ├── router.go
│   │   ├── dashboard.go
│   │   ├── dashboard.html
│   │   └── handler_test.go
│   ├── messaging/
│   │   └── rabbitmq.go               # Client RabbitMQ (publish + consume + reconnect)
│   ├── wait/                         # Helper retry/backoff untuk dependency
│   └── integration/                  # Integration test (build tag `integration`)
│       └── integration_test.go
├── migrations/
│   ├── 001_create_vehicle_locations.sql
│   └── 002_create_geofence_events.sql
├── postman/
│   ├── fleet-api.postman_collection.json
│   └── postman_environment.json
├── mosquitto/
│   └── mosquitto.conf
├── mqtt-explorer/
│   └── settings.json
├── docs/
│   ├── FLOWCHART.md                  # Flowchart lengkap (Mermaid + PNG)
│   └── diagrams/
│       ├── *.mmd                     # Sumber diagram Mermaid
│       ├── *.png                     # Hasil render PNG
│       ├── render.sh                 # Render ulang PNG + FLOWCHART.md
│       └── build_md.py               # Generator FLOWCHART.md
├── Dockerfile                        # Satu image untuk api/worker/publisher
├── docker-compose.yml
├── Makefile                          # Perintah bantu (build, test, up, logs, ...)
├── go.mod
├── go.sum
├── .env.example
├── .gitignore
└── README.md
```

### Lapisan Arsitektur

Aliran dependensi satu arah: `cmd → http/mqtt → service → repository → database`.

| Layer | Tanggung jawab |
|---|---|
| `domain` | Tipe data inti. Tidak mengimpor package internal lain. |
| `repository` | Seluruh query SQL. Tidak ada SQL di luar package ini. |
| `service` | Logika bisnis: validasi payload, perhitungan geofence, state transisi. |
| `http` / `mqtt` / `messaging` | Adapter masuk (REST, MQTT, RabbitMQ). |
| `cmd` | Composition root: merangkai semua layer dan menjalankan service. |

Package `http` hanya bergantung pada antarmuka (`LocationService`,
`VehicleLister`, `GeofenceEventStore`), bukan struct konkret — sehingga
seluruh handler bisa diuji tanpa database.

## Arsitektur Runtime

```text
[Publisher] --JSON--> [Mosquitto] --MQTT--> [API service] --SQL--> [PostgreSQL]
                                            |
                                            +--Haversine+tracker--> [RabbitMQ] --> [Worker service] --SQL--> [PostgreSQL]
                                                        |
                                                        +--> [REST API + Dashboard]
```

Tiga service Go berjalan sebagai container terpisah:

| Service | Peran | Port |
|---|---|---|
| `api` | Subscriber MQTT, publisher event geofence, REST API, dashboard | 8000 |
| `worker` | Consumer queue `geofence_alerts`, menyimpan log event ke DB | - |
| `publisher` | Simulator data mock lokasi setiap 2 detik | - |

Karena worker terpisah dari API, event geofence yang sudah diproses dicatat di
tabel `geofence_events` — dashboard tetap bisa menampilkannya lintas container.

## Menjalankan

```bash
cp .env.example .env      # opsional, semua nilai punya default
docker compose up -d --build
# atau: make up
```

Layanan yang tersedia:

| Layanan | URL |
|---|---|
| REST API + Dashboard | http://localhost:8000 |
| RabbitMQ Management | http://localhost:15672 (guest/guest) |
| Adminer | http://localhost:8080 (server: postgres, user/postgres: postgres) |
| Dozzle (live log) | http://localhost:8088 |
| MQTT Explorer | http://localhost:3001 (pilih koneksi "transjakarta (mosquitto)") |

Menghentikan:

```bash
docker compose down        # container saja
docker compose down -v     # container + volume (data Postgres ikut terhapus)
```

## Endpoint

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/health` | Health check |
| `GET` | `/vehicles/{vehicle_id}/location` | Lokasi terakhir kendaraan (timestamp terbesar) |
| `GET` | `/vehicles/{vehicle_id}/history?start=&end=&limit=` | Riwayat lokasi (Unix detik, inklusif) |
| `GET` | `/dashboard` | Dashboard observability (peta Leaflet) |
| `GET` | `/api/vehicles` | Lokasi terakhir semua kendaraan |
| `GET` | `/api/geofence-events` | 100 event geofence terakhir |
| `GET` | `/api/geofence` | Konfigurasi geofence |
| `GET` | `/api/heartbeat` | Status online/stale/offline tiap kendaraan + kondisi pipeline ingest |

Contoh:

```bash
curl http://localhost:8000/vehicles/B1234XYZ/location
curl "http://localhost:8000/vehicles/B1234XYZ/history?start=1715000000&end=1715009999&limit=100"
```

Parameter `start`/`end` berupa unix timestamp **detik** dan inklusif di kedua
sisi. Nilai dalam milidetik ditolak dengan pesan yang jelas, bukan
diam-diam mengembalikan array kosong. `limit` bersifat opsional (default
1000, maksimum 10000).

## Perilaku yang Dijamin

### 1. Validasi payload berlapis

Tiga lapis, semuanya di layer `service`:

| Lapis | Pemeriksaan | Contoh yang ditolak |
|---|---|---|
| Format | `vehicle_id` terisi, charset `[A-Za-z0-9_-]`, maks 50 karakter | `"B 1234"`, `"B1234#"` |
| Format | koordinat dalam rentang geografis | `latitude: 999.5` |
| Format | timestamp tidak nol, tidak negatif | `timestamp: 0` |
| Konsistensi | `vehicle_id` payload **wajib sama** dengan `vehicle_id` di topik | topik `.../B1234XYZ/...` + payload `B9999EVIL` |
| Sanity waktu | timestamp maksimal 300 detik ke depan, maksimal 24 jam ke belakang | `timestamp` tahun 2030 |

Poin konsistensi topik penting: tanpa itu, pesan yang dikirim ke topik
`/fleet/vehicle/B1234XYZ/location` akan tersimpan dengan label kendaraan
yang keliru. Poin sanity waktu mencegah payload rusak mengambil alih
endpoint "lokasi terakhir".

### 2. Lokasi terakhir ditentukan timestamp

`ORDER BY timestamp DESC, id DESC` — bukan urutan pesan diterima. `id`
sebagai pemutus seri membuat hasil deterministik ketika dua baris memiliki
timestamp sama. `DISTINCT ON (vehicle_id)` (endpoint `/api/vehicles`) memakai
urutan yang sama.

### 3. Geofence berbasis transisi (edge-triggered)

Event **hanya** terbit saat kendaraan berpindah dari luar ke dalam area,
bukan setiap pesan selama berada di dalam. TUGAS.md berbunyi *"jika kendaraan
**masuk** dalam radius 50 meter"*, jadi perilaku ini yang sesuai spesifikasi.

- State awal dianggap "di luar" → pesan pertama yang di dalam langsung memicu event.
- TTL state (default 60 detik): kalau kendaraan berhenti melapor saat masih
  "di dalam", state dianggap basi sehingga transisi masuk berikutnya tetap
  terpicu (event tidak hilang permanen).
- Event keluar (`geofence_exit`) opsional, aktifkan dengan `GEOFENCE_EMIT_EXIT=true`.

### 4. Ketahanan runtime

| Aspek | Perilaku |
|---|---|
| Startup | Dependency (PostgreSQL/RabbitMQ/Mosquitto) dicoba ulang dengan backoff eksponensial — tidak crash-loop, tidak bergantung urutan container. |
| RabbitMQ | Pesan `persistent`, publisher confirms + `mandatory` (pesan tanpa binding tidak hilang diam-diam), channel publish & consume terpisah, **auto-reconnect** bila broker restart. |
| Worker | Manual ack + `Nack(requeue)`; pesan rusak dibuang agar tidak berputar tanpa henti. |
| MQTT | Re-subscribe di dalam `OnConnect` (broker menghapus subscription saat disconnect), auto-reconnect. |
| Healthcheck | PostgreSQL, RabbitMQ (`check_port_connectivity`), dan Mosquitto (publish probe) → `depends_on: service_healthy`. |

### 5. Publisher dengan rute yang memotong batas geofence

Setiap kendaraan bergerak bolak-balik (shuttle) melewati titik pusat
geofence sepanjang satu garis lurus. Jarak ke pusat:

```text
d(t) = 2,5 × radius × sin(2π·t/T + fase)
```

Amplitudo 2,5 × radius membuat jarak berosilasi 0 … 2,5 × radius, sehingga
batas radius pasti dilewati **dua kali per periode** — transisi masuk/keluar
terjadwal dan dapat diamati, bukan bergantung drift acak. Publisher membaca
titik & radius geofence yang sama dengan service API, sehingga memindahkan
geofence lewat `.env` tidak membuat rute berhenti memotong batas.

Timestamp publisher bersifat monotonik per kendaraan, jadi "lokasi terakhir"
tidak pernah ambigu.

### 6. Heartbeat & deteksi offline GPS

Sistem bisa membedakan "kendaraan melapor", "kendaraan diam", dan
"kendaraan berhenti melapor" pada dua level:

**Level 1 — status perangkat.** Dihitung dari umur data terakhir tiap
kendaraan (bukan urutan pesan, bukan state di memori). Threshold:

| Status | Kondisi | Default |
|---|---|---|
| `online` | data younger dari ambang stale | — |
| `stale` | umur data ≥ ambang stale (data terlalu tua, perlu dicurigai) | 30 detik |
| `offline` | umur data ≥ ambang offline (berhenti melapor) | 120 detik |

Karena cukup dibaca dari `MAX(timestamp)` di database, status ini **tidak
perlu tabel baru, tidak perlu state di memori, dan tetap benar setelah
service API di-restart**.

**Level 2 — heartbeat pipeline.** Subscriber mencatat kapan pesan terakhir
masuk dan apakah sambungan ke broker masih hidup. Ini menangkap kasus yang
tidak bisa dilihat dari sisi perangkat: broker hidup, tetapi tidak ada
kendaraan yang melapor.

```bash
curl http://localhost:8000/api/heartbeat
```

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

`last_seen` pada laporan identik dengan `MAX(timestamp)` di tabel
`vehicle_loctions` — jadi status bisa diverifikasi ulang lewat SQL.

Dashboard menampilkan dampaknya: badge **Koneksi** per kendaraan
(hijau `ONLINE` / kuning `STALE` / merah `OFFLINE`), marker peta berubah
warna, kartu ringkasan "Kendaraan stale" & "Kendaraan offline", serta
indikator pipeline di header.

## Konfigurasi

| Variable | Default | Keterangan |
|---|---|---|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/transjakarta?sslmode=disable` | Koneksi PostgreSQL |
| `MQTT_BROKER` | `tcp://localhost:1883` | Alamat broker MQTT |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | Koneksi RabbitMQ |
| `PORT` | `8000` | Port REST API |
| `GEOFENCE_LAT` | `-6.175392` | Latitude pusat geofence |
| `GEOFENCE_LNG` | `106.827153` | Longitude pusat geofence |
| `GEOFENCE_RADIUS_M` | `50` | Radius geofence (meter) |
| `GEOFENCE_STATE_TTL_SECONDS` | `60` | Masa berlaku state "di dalam geofence" |
| `GEOFENCE_EMIT_EXIT` | `false` | Kirim juga event `geofence_exit` |
| `LOCATION_MAX_FUTURE_SECONDS` | `300` | Toleransi timestamp ke depan |
| `LOCATION_MAX_AGE_SECONDS` | `86400` | Toleransi data basi (24 jam) |
| `DEPENDENCY_TIMEOUT_SECONDS` | `120` | Batas menunggu dependency saat startup |
| `HEARTBEAT_ENABLED` | `true` | Aktifkan endpoint `/api/heartbeat` |
| `HEARTBEAT_STALE_SECONDS` | `30` | Ambang status `stale` (data terlalu tua) |
| `HEARTBEAT_OFFLINE_SECONDS` | `120` | Ambang status `offline` (berhenti melapor) |
| `PUBLISHER_VEHICLES` | `B1234XYZ,B5678ABC` | ID kendaraan yang disimulasikan |
| `PUBLISHER_INTERVAL` | `2` | Interval publish (detik) |
| `PUBLISHER_ROUTE_PERIOD_SECONDS` | `60` | Periode satu putaran rute shuttle |
| `PUBLISHER_ROUTE_BEARING_DEG` | `45` | Arah garis rute terhadap utara |
| `PUBLISHER_ROUTE_SPREAD_DEG` | `20` | Selisih arah antar kendaraan |

## Menjalankan Tanpa Docker

```bash
go run ./cmd/publisher      # terminal 1 (butuh broker MQTT)
go run ./cmd/api            # terminal 2
go run ./cmd/worker         # terminal 3
```

## Database

Migration di folder `migrations/` dijalankan otomatis oleh image PostgreSQL
saat inisialisasi container. **Script hanya berjalan pada volume yang masih
kosong.** Untuk volume yang sudah terisi, jalankan manual:

```bash
docker exec -i transjakarta_postgres psql -U postgres -d transjakarta \
  < migrations/002_create_geofence_events.sql
```

| Tabel | Isi |
|---|---|
| `vehicle_loctions` | Riwayat lokasi kendaraan (nama tabel mengikuti spesifikasi tugas) |
| `geofence_events` | Log event geofence yang diproses worker |

## Observability

Setiap payload membawa `trace_id` berformat `{vehicle_id}-{timestamp}`, dipakai
publisher, subscriber, dan worker. Satu data bisa dilacak ujung ke ujung:

```text
[PUBLISHER] trace=B1234XYZ-1715003456 published to /fleet/vehicle/B1234XYZ/location
[RECEIVE]   trace=B1234XYZ-1715003456 topic=/fleet/vehicle/B1234XYZ/location lat=... lng=...
[DB]        trace=B1234XYZ-1715003456 saved location lat=... lng=...
[GEOFENCE]  trace=B1234XYZ-1715003456 jarak=38.1m radius=50m status=INSIDE
[RABBITMQ]  trace=B1234XYZ-1715003456 published exchange=fleet.events routing_key=geofence.entry
[WORKER]    trace=B1234XYZ-1715003456 consumed queue=geofence_alerts event=geofence_entry
```

Cara melihat: filter log di Dozzle dengan `trace=B1234XYZ-`, atau pakai
`docker compose logs -f api worker`.

## Pengujian

`Makefile` menyediakan pintasan; perintah yang setara (dan bisa dipakai
langsung tanpa `make`) adalah:

```bash
# semua pemeriksaan (gofmt + vet + build + unit test)
gofmt -l . && go vet ./... && go build ./... && go test ./...

# unit test saja
go test ./...

# integration test (butuh stack docker hidup)
go test -count=1 -tags integration ./internal/integration/... -v
```

Jika `make` tersedia:

```bash
make check             # gofmt + vet + build + unit test
make test              # unit test saja
make test-integration  # integration test (butuh stack docker hidup)
```

| Lapis | Cakupan |
|---|---|
| Unit test | Validasi payload (semua kasus tolak + mismatch topik), `Distance` (nilai acuan), state machine geofence (entry/exit/TTL), handler HTTP (200/400/404/500, parameter limit). |
| Integration test | Rantai MQTT → database → HTTP, kasus negatif (payload tidak valid tidak boleh menyentuh DB), event geofence tepat 1 per transisi, topologi RabbitMQ nyata (exchange/queue/binding/consumer). |
| Postman | 12 request lengkap dengan assertion (status, bentuk respons, koordinat, urutan timestamp). |

Integration test memverifikasi lewat **tiga sisi sekaligus** — kueri SQL,
respons HTTP, dan RabbitMQ management API — bukan sekadar membaca log:

```bash
docker compose up -d --build
make test-integration
```

Konfigurasi integration test lewat environment variable:
`TEST_DATABASE_URL`, `TEST_API_URL`, `TEST_MQTT_BROKER`,
`TEST_RABBITMQ_MANAGEMENT_URL`, `TEST_RABBITMQ_USER`, `TEST_RABBITMQ_PASS`.
