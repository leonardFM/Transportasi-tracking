// Package mqtt berisi subscriber yang menerima data lokasi kendaraan dari
// broker MQTT.
package mqtt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"transjakarta-fleet/internal/domain"
	"transjakarta-fleet/internal/messaging"
	"transjakarta-fleet/internal/service"
)

const (
	// TopicPattern adalah pola topik yang di-subscribe. Wildcard "+"
	// menggantikan vehicle_id sehingga satu subscription mencakup semua
	// kendaraan.
	TopicPattern = "/fleet/vehicle/+/location"

	// clientID harus unik agar tidak menendang client lain di broker.
	clientID = "transjakarta-subscriber"
)

// ExtractVehicleID mengekstrak vehicle_id dari topik MQTT
// "/fleet/vehicle/{vehicle_id}/location".
//
// Topik yang tidak sesuai pola tersebut dianggap tidak valid dan pesannya
// dibuang: tanpa ini, payload bisa memakai vehicle_id berbeda dari topik dan
// data tersimpan dengan label yang keliru.
func ExtractVehicleID(topic string) (string, error) {
	parts := strings.Split(topic, "/")

	offset := 0
	if len(parts) > 0 && parts[0] == "" {
		offset = 1 // topik berawalan "/" memiliki level pertama kosong
	}

	if len(parts) != offset+4 ||
		parts[offset] != "fleet" ||
		parts[offset+1] != "vehicle" ||
		parts[offset+3] != "location" {
		return "", fmt.Errorf("topik %q tidak sesuai pola %s", topic, TopicPattern)
	}

	vehicleID := parts[offset+2]
	if vehicleID == "" {
		return "", fmt.Errorf("topik %q tidak memuat vehicle_id", topic)
	}
	return vehicleID, nil
}

// IngestObserver menerima notifikasi kelangsungan hidup stream MQTT.
//
// Dipasangkan dengan *service.IngestMonitor supaya lapisan service bisa
// melaporkan "apakah pipeline benar-benar mengalir" tanpa bergantung pada
// package mqtt.
type IngestObserver interface {
	// RecordMessage dipanggil setiap pesan well-formed yang diterima.
	RecordMessage()
	// SetConnected dipanggil saat sambungan ke broker established atau putus.
	SetConnected(connected bool)
}

// Subscriber menerima pesan lokasi dari broker dan meneruskannya ke service.
type Subscriber struct {
	client   paho.Client
	location *service.LocationService
	tracker  *service.GeofenceTracker
	rabbit   *messaging.Client
	valid    service.ValidationOptions
	observer IngestObserver
}

// NewSubscriber membuat subscriber MQTT dan langsung connects ke broker.
func NewSubscriber(
	broker string,
	location *service.LocationService,
	tracker *service.GeofenceTracker,
	rabbitClient *messaging.Client,
	valid service.ValidationOptions,
	observer IngestObserver,
) (*Subscriber, error) {
	s := &Subscriber{
		location: location,
		tracker:  tracker,
		rabbit:   rabbitClient,
		valid:    valid,
		observer: observer,
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetConnectRetry(false) // percobaan ulang ditangani lapisan pemanggil
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(10 * time.Second)
	opts.SetKeepAlive(30 * time.Second)
	opts.OnConnect = func(c paho.Client) {
		fmt.Println("[MQTT] Connected")
		s.observe(func(o IngestObserver) { o.SetConnected(true) })
		s.subscribe(c)
	}
	opts.OnConnectionLost = func(c paho.Client, err error) {
		fmt.Printf("[MQTT] Connection lost: %v\n", err)
		s.observe(func(o IngestObserver) { o.SetConnected(false) })
	}

	client := paho.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		client.Disconnect(100)
		return nil, token.Error()
	}
	s.client = client
	return s, nil
}

// Start memastikan subscription ke topik aktif.
//
// Subscription sengaja didaftarkan ulang di dalam OnConnect: broker menghapus
// subscription ketika client disconnect (CleanSession=true) dan paho tidak
// mengirim ulang SUBSCRIBE secara otomatis. Tanpa ini, subscriber akan
// tersambung kembali ke broker tetapi tidak lagi menerima data lokasi.
func (s *Subscriber) Start() error {
	if !s.client.IsConnected() {
		return fmt.Errorf("mqtt: tidak terhubung ke broker")
	}
	s.subscribe(s.client)
	return nil
}

// subscribe mendaftarkan pola topik ke broker.
func (s *Subscriber) subscribe(c paho.Client) {
	token := c.Subscribe(TopicPattern, 0, s.handleMessage)
	token.Wait()
	if token.Error() != nil {
		fmt.Printf("[MQTT] Gagal subscribe %s: %v\n", TopicPattern, token.Error())
		return
	}
	fmt.Printf("[MQTT] Subscribed to %s\n", TopicPattern)
}

// observe menjalankan fn pada observer bila observer tersedia.
//
// Observer bersifat opsional supaya subscriber tetap bisa dipakai tanpa
// monitor (mis. pada pengujian).
func (s *Subscriber) observe(fn func(IngestObserver)) {
	if s.observer == nil {
		return
	}
	fn(s.observer)
}

// handleMessage memproses satu pesan lokasi: validasi, simpan ke database,
// lalu evaluasi geofence dan publish event bila terjadi transisi masuk.
func (s *Subscriber) handleMessage(client paho.Client, msg paho.Message) {
	// vehicle_id harus diambil dari topik, bukan dipercaya begitu saja dari
	// isi payload.
	topicVehicleID, err := ExtractVehicleID(msg.Topic())
	if err != nil {
		fmt.Printf("[RECEIVE] topik ditolak: %v\n", err)
		return
	}

	var payload domain.LocationPayload
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
		fmt.Printf("[RECEIVE] Invalid JSON: %v\n", err)
		return
	}

	// Pesan well-formed sudah membuktikan pipeline MQTT mengalir, meski
	// payload-nya nanti ditolak validasi.
	s.observe(func(o IngestObserver) { o.RecordMessage() })

	if err := service.ValidatePayload(payload, topicVehicleID, s.valid); err != nil {
		fmt.Printf("[RECEIVE] Invalid payload: %v\n", err)
		return
	}

	// trace_id dibawa di semua tahap agar satu payload bisa dilacak
	// dari MQTT sampai worker (format sama dengan yang dipakai publisher & worker).
	trace := fmt.Sprintf("%s-%d", payload.VehicleID, payload.Timestamp)
	fmt.Printf("[RECEIVE] trace=%s topic=%s lat=%f lng=%f\n", trace, msg.Topic(), payload.Latitude, payload.Longitude)

	if err := s.location.Insert(payload); err != nil {
		fmt.Printf("[DB] trace=%s failed to insert location: %v\n", trace, err)
		return
	}
	fmt.Printf("[DB] trace=%s saved location lat=%f lng=%f\n", trace, payload.Latitude, payload.Longitude)

	// Geofence: jarak selalu ditampilkan, event hanya terbit saat transisi
	// dari luar ke dalam area (edge-triggered).
	dist, inside, event := s.tracker.Evaluate(payload)
	status := "OUTSIDE"
	if inside {
		status = "INSIDE"
	}
	fmt.Printf("[GEOFENCE] trace=%s jarak=%.1fm radius=%.0fm status=%s\n",
		trace, dist, s.tracker.RadiusM(), status)

	if event == nil {
		return
	}

	if err := s.rabbit.PublishGeofenceEntry(*event); err != nil {
		fmt.Printf("[RABBITMQ] trace=%s failed to publish: %v\n", trace, err)
		return
	}
	fmt.Printf("[RABBITMQ] trace=%s published exchange=%s routing_key=%s queue=%s event=%s\n",
		trace, messaging.ExchangeName, messaging.RoutingKey, messaging.QueueName, event.Event)
}

// Stop memutus koneksi ke broker.
func (s *Subscriber) Stop() {
	s.client.Disconnect(250)
}
