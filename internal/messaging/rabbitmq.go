// Package messaging berisi integrasi dengan RabbitMQ: publish event geofence
// dan consumption queue geofence_alerts.
//
// Ketahanan yang ditangani paket ini:
//   - pesan dikirim sebagai persistent (DeliveryMode) supaya tidak hilang saat
//     broker restart,
//   - publish memakai publisher confirms + mandatory flag sehingga pesan yang
//     tidak bisa di-route tidak hilang diam-diam,
//   - channel publish dan channel consume dipisah,
//   - koneksi dipulihkan otomatis (reconnect) ketika broker restart,
//   - consumption memakai manual ack sehingga pesan tidak hilang saat worker
//     crash di tengah pemrosesan.
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"transjakarta-fleet/internal/domain"
)

const (
	// ExchangeName adalah nama exchange bertipe topic untuk event armada.
	ExchangeName = "fleet.events"
	// QueueName adalah nama queue yang menerima event geofence.
	QueueName = "geofence_alerts"
	// RoutingKey adalah routing key yang dipakai untuk event geofence.
	RoutingKey = "geofence.entry"
	// BindingKey adalah pola binding queue ke exchange.
	BindingKey = "geofence.*"
)

// ErrPermanent menandai pesan yang tidak akan pernah berhasil diproses
// (misalnya JSON rusak). Pesan seperti ini dibuang, bukan di-requeue.
var ErrPermanent = errors.New("permanent message error")

// Handler memproses satu pesan dari queue.
//
// Bila handler mengembalikan error yang membungkus ErrPermanent, pesan akan
// dibuang (Nack tanpa requeue). Error lain menyebabkan pesan di-requeue satu
// kali; bila masih gagal pada pengiriman ulang, pesan dibuang agar tidak
// berputar tanpa henti.
type Handler func(body []byte) error

// Client adalah koneksi RabbitMQ beserta channel AMQP-nya.
type Client struct {
	url string

	dialMu sync.Mutex

	mu       sync.Mutex
	conn     *amqp.Connection
	pub      *amqp.Channel
	cons     *amqp.Channel
	confirms chan amqp.Confirmation
	returns  chan amqp.Return

	publishMu sync.Mutex

	handler Handler
	closed  bool
}

// New terhubung ke RabbitMQ, mendeklarasikan exchange, queue, dan binding.
//
// Bila koneksi gagal, pemanggil sebaiknya memakai wait.For agar mencoba
// ulang; koneksi yang berhasil akan otomatis dipulihkan kembali bila broker
// nanti restart.
func New(url string) (*Client, error) {
	c := &Client{url: url}
	if err := c.dial(); err != nil {
		return nil, err
	}
	return c, nil
}

// dial membuka koneksi baru dan menyiapkan seluruh channel.
func (c *Client) dial() error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return err
	}

	pub, err := conn.Channel()
	if err != nil {
		conn.Close()
		return err
	}
	cons, err := conn.Channel()
	if err != nil {
		pub.Close()
		conn.Close()
		return err
	}

	if err := declareTopology(pub); err != nil {
		pub.Close()
		cons.Close()
		conn.Close()
		return err
	}

	// Publisher confirms: broker memberi tahu apakah pesan benar-benar
	// diterima dan disimpan.
	if err := pub.Confirm(false); err != nil {
		pub.Close()
		cons.Close()
		conn.Close()
		return fmt.Errorf("aktifkan publisher confirm: %w", err)
	}

	confirms := pub.NotifyPublish(make(chan amqp.Confirmation, 1))
	returns := pub.NotifyReturn(make(chan amqp.Return, 1))

	c.mu.Lock()
	c.conn = conn
	c.pub = pub
	c.cons = cons
	c.confirms = confirms
	c.returns = returns
	c.mu.Unlock()

	go c.watch(conn)
	return nil
}

// declareTopology mendeklarasikan exchange, queue, dan binding. Declarasi
// bersifat idempotent sehingga aman dipanggil ulang saat reconnect.
func declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(
		ExchangeName, // name
		"topic",      // type
		true,         // durable
		false,        // auto-deleted
		false,        // internal
		false,        // no-wait
		nil,          // arguments
	); err != nil {
		return fmt.Errorf("declare exchange %s: %w", ExchangeName, err)
	}

	if _, err := ch.QueueDeclare(
		QueueName, // name
		true,      // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	); err != nil {
		return fmt.Errorf("declare queue %s: %w", QueueName, err)
	}

	if err := ch.QueueBind(
		QueueName,    // queue name
		BindingKey,   // routing key
		ExchangeName, // exchange
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind queue %s ke exchange %s: %w", QueueName, ExchangeName, err)
	}
	return nil
}

// watch memantau keberadaan koneksi. Bila koneksi terputus (broker restart),
// client mencoba menyambung kembali dengan backoff, lalu melanjutkan consumption.
func (c *Client) watch(conn *amqp.Connection) {
	errCh := conn.NotifyClose(make(chan *amqp.Error, 1))
	err, ok := <-errCh
	if ok && err != nil {
		fmt.Printf("[RABBITMQ] koneksi terputus: %v\n", err)
	} else {
		fmt.Println("[RABBITMQ] koneksi ditutup")
	}

	c.mu.Lock()
	if c.conn != conn {
		// Koneksi sudah digantikan oleh yang baru; tidak perlu reconnect.
		c.mu.Unlock()
		return
	}
	c.conn = nil
	c.pub = nil
	c.cons = nil
	c.mu.Unlock()

	delay := time.Second
	for {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}

		time.Sleep(delay)
		if err := c.ensureConnected(); err == nil {
			fmt.Println("[RABBITMQ] koneksi berhasil dipulihkan")
			c.mu.Lock()
			handler := c.handler
			c.mu.Unlock()
			if handler != nil {
				if err := c.startConsumer(handler); err != nil {
					fmt.Printf("[RABBITMQ] gagal melanjutkan consumption: %v\n", err)
				}
			}
			return
		}
		if delay < 10*time.Second {
			delay *= 2
		}
	}
}

// ensureConnected memastikan ada koneksi yang hidup, melakukan dial bila perlu.
func (c *Client) ensureConnected() error {
	c.dialMu.Lock()
	defer c.dialMu.Unlock()

	c.mu.Lock()
	alive := c.conn != nil && !c.conn.IsClosed()
	c.mu.Unlock()
	if alive {
		return nil
	}
	return c.dial()
}

// PublishGeofenceEntry mengirim event geofence ke exchange fleet.events.
//
// Publish menunggu konfirmasi broker (publisher confirm). Bila pesan tidak
// bisa di-route ke queue mana pun, Publish mengembalikan error (bukan
// diam-diam hilang).
func (c *Client) PublishGeofenceEntry(ev domain.GeofenceEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return c.publish(body)
}

// publish mengirim payload dan menunggu konfirmasi broker.
func (c *Client) publish(body []byte) error {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()

	if err := c.ensureConnected(); err != nil {
		return fmt.Errorf("rabbitmq: tidak tersambung: %w", err)
	}

	c.mu.Lock()
	pub := c.pub
	confirms := c.confirms
	returns := c.returns
	c.mu.Unlock()
	if pub == nil {
		return errors.New("rabbitmq: channel publish tidak tersedia")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := pub.PublishWithContext(ctx,
		ExchangeName, // exchange
		RoutingKey,   // routing key
		true,         // mandatory: broker wajib mengembalikan pesan bila tidak ada binding
		false,        // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent, // pesan bertahan saat broker restart
			Timestamp:    time.Now(),
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("publish ke %s: %w", ExchangeName, err)
	}

	// Tunggu konfirmasi broker. Pesan yang dikembalikan (unroutable) dikirim
	// broker sebelum ack, jadi kedua channel ini dipantau bersamaan.
	for {
		select {
		case ret := <-returns:
			return fmt.Errorf("rabbitmq: pesan dikembalikan broker (reply=%d %s)",
				ret.ReplyCode, ret.ReplyText)
		case conf, ok := <-confirms:
			if !ok {
				return errors.New("rabbitmq: channel konfirmasi ditutup")
			}
			if !conf.Ack {
				return fmt.Errorf("rabbitmq: publish ditolak broker (delivery_tag=%d)", conf.DeliveryTag)
			}
			// Cek apakah broker sempat mengembalikan pesan (unroutable).
			select {
			case ret := <-returns:
				return fmt.Errorf("rabbitmq: pesan dikembalikan broker (reply=%d %s)",
					ret.ReplyCode, ret.ReplyText)
			default:
			}
			return nil
		case <-ctx.Done():
			return fmt.Errorf("rabbitmq: menunggu konfirmasi publish: %w", ctx.Err())
		}
	}
}

// ConsumeGeofenceAlerts menjalankan handler untuk setiap pesan di queue
// geofence_alerts. Handler berjalan di goroutine terpisah.
//
// Bila koneksi terputus, consumption otomatis dilanjutkan setelah reconnect.
func (c *Client) ConsumeGeofenceAlerts(handler Handler) error {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()
	return c.startConsumer(handler)
}

// startConsumer mendaftarkan consumer pada queue dengan manual ack.
func (c *Client) startConsumer(handler Handler) error {
	c.mu.Lock()
	ch := c.cons
	c.mu.Unlock()
	if ch == nil {
		return errors.New("rabbitmq: channel consume tidak tersedia")
	}

	msgs, err := ch.Consume(
		QueueName, // queue
		"",        // consumer tag (dibuat otomatis)
		false,     // auto-ack: FALSE supaya pesan bisa di-nack saat gagal
		false,     // exclusive
		false,     // no-local
		false,     // no-wait
		nil,       // args
	)
	if err != nil {
		return fmt.Errorf("consume queue %s: %w", QueueName, err)
	}

	go func() {
		for d := range msgs {
			if err := handler(d.Body); err != nil {
				if errors.Is(err, ErrPermanent) || d.Redelivered {
					// Pesan rusak atau sudah dicoba ulang sekali: buang agar
					// tidak berputar tanpa henti.
					fmt.Printf("[RABBITMQ] pesan dibuang: %v\n", err)
					d.Nack(false, false)
				} else {
					fmt.Printf("[RABBITMQ] pesan gagal diproses, di-requeue: %v\n", err)
					d.Nack(false, true)
				}
				continue
			}
			d.Ack(false)
		}
	}()
	return nil
}

// Close menutup channel dan koneksi.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.pub = nil
	c.cons = nil
	c.mu.Unlock()

	if conn != nil {
		conn.Close()
	}
}
