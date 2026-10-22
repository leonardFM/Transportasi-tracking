package mqtt

import "testing"

func TestExtractVehicleID_TopikSesuai(t *testing.T) {
	cases := []struct {
		topic string
		want  string
	}{
		{"/fleet/vehicle/B1234XYZ/location", "B1234XYZ"},
		{"fleet/vehicle/B5678ABC/location", "B5678ABC"},
	}
	for _, c := range cases {
		got, err := ExtractVehicleID(c.topic)
		if err != nil {
			t.Fatalf("topik %q harus valid, got error: %v", c.topic, err)
		}
		if got != c.want {
			t.Fatalf("topik %q harus menghasilkan %q, got %q", c.topic, c.want, got)
		}
	}
}

func TestExtractVehicleID_TopikTidakSesai(t *testing.T) {
	cases := []string{
		"/fleet/vehicle/B1234XYZ",
		"/fleet/vehicle/B1234XYZ/speed",
		"/fleet/vehicle//location",
		"/other/vehicle/B1234XYZ/location",
		"/fleet/vehicle/B1234XYZ/location/extra",
		"",
	}
	for _, topic := range cases {
		if _, err := ExtractVehicleID(topic); err == nil {
			t.Fatalf("topik %q harus ditolak", topic)
		}
	}
}
