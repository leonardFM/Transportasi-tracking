-- 001_create_vehicle_locations.sql
--
-- Tabel lokasi kendaraan.
--
-- CATATAN: nama tabel sengaja memakai "vehicle_loctions" (ejaan pada
-- spesifikasi tugas TUGAS.md), bukan "vehicle_locations". Selaras dengan
-- implementasi di internal/repository/location_repository.go.
--
-- Script ini dijalankan otomatis oleh image PostgreSQL pada inisialisasi
-- container (folder ini di-mount ke /docker-entrypoint-initdb.d). Script HANYA
-- berjalan pada volume yang masih kosong.

CREATE TABLE IF NOT EXISTS vehicle_loctions (
  id SERIAL PRIMARY KEY,
  vehicle_id VARCHAR(50) NOT NULL,
  latitude DOUBLE PRECISION NOT NULL,
  longitude DOUBLE PRECISION NOT NULL,
  timestamp BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_vehicle_loctions_vehicle_id_timestamp
  ON vehicle_loctions (vehicle_id, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_vehicle_loctions_timestamp
  ON vehicle_loctions (timestamp);
