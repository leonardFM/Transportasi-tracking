-- 002_create_geofence_events.sql
--
-- Log event geofence yang sudah diproses oleh worker (cmd/worker).
--
-- Tabel ini memungkinkan dashboard pada service API menampilkan feed event
-- meskipun service worker berjalan di proses/container yang terpisah.
--
-- Script ini dijalankan otomatis oleh image PostgreSQL pada inisialisasi
-- container (folder ini di-mount ke /docker-entrypoint-initdb.d). Script HANYA
-- berjalan pada volume yang masih kosong. Untuk volume yang sudah terisi,
-- jalankan manual:
--
--   docker exec -i transjakarta_postgres psql -U postgres -d transjakarta \
--     < migrations/002_create_geofence_events.sql

CREATE TABLE IF NOT EXISTS geofence_events (
  id SERIAL PRIMARY KEY,
  vehicle_id VARCHAR(50) NOT NULL,
  event VARCHAR(50) NOT NULL,
  latitude DOUBLE PRECISION NOT NULL,
  longitude DOUBLE PRECISION NOT NULL,
  timestamp BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_geofence_events_timestamp
  ON geofence_events (timestamp DESC);
