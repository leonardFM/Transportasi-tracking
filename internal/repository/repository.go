// Package repository adalah lapisan akses data. Semua query SQL terhadap
// PostgreSQL berada di sini, tidak boleh bocor ke layer service/http.
package repository

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
)

// Open membuka koneksi ke PostgreSQL dan memverifikasi koneksinya.
func Open(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}
