// Package wait berisi utilitas untuk menunggu dependency eksternal siap.
//
// Semua service (api, worker, publisher) memakai paket ini agar startup tidak
// bergantung pada urutan container: bila PostgreSQL/RabbitMQ/Mosquitto belum
// siap, service menunggu dengan backoff eksponensial alih-alih langsung
// crash dan hoping-the-best.
package wait

import (
	"context"
	"fmt"
	"time"
)

// Options mengatur perilaku percobaan ulang.
type Options struct {
	// Timeout adalah batas total menunggu. 0 berarti tanpa batas.
	Timeout time.Duration
	// Interval adalah jeda percobaan pertama. 0 berarti 1 detik.
	Interval time.Duration
	// MaxInterval adalah batas atas jeda antar percobaan. 0 berarti 10 detik.
	MaxInterval time.Duration
	// Log adalah fungsi pencatat (opsional).
	Log func(format string, args ...any)
}

// DefaultOptions mengembalikan opsi bawaan dengan timeout 2 menit.
func DefaultOptions() Options {
	return Options{
		Timeout:     2 * time.Minute,
		Interval:    time.Second,
		MaxInterval: 10 * time.Second,
	}
}

// For menjalankan fn berulang kali sampai fn berhasil atau batas waktu habis.
//
// fn akan dicoba ulang dengan jeda yang bertambah twofold (dibatasi
// MaxInterval). Jika ctx dibatalkan, For langsung mengembalikan ctx.Err().
func For(ctx context.Context, name string, opts Options, fn func() error) error {
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.MaxInterval <= 0 {
		opts.MaxInterval = 10 * time.Second
	}
	logf := opts.Log
	if logf == nil {
		logf = func(format string, args ...any) {}
	}

	deadline := time.Now().Add(opts.Timeout)
	delay := opts.Interval

	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil {
			if attempt > 1 {
				logf("[WAIT] %s siap setelah %d percobaan", name, attempt)
			}
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("menunggu %s gagal setelah %s: %w", name, opts.Timeout, err)
		}

		logf("[WAIT] %s belum siap (percobaan %d): %v", name, attempt, err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		if delay < opts.MaxInterval {
			delay *= 2
			if delay > opts.MaxInterval {
				delay = opts.MaxInterval
			}
		}
	}
}
