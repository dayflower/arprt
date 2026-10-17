package render

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestContextFixtures(t *testing.T) {
	for _, telemetry := range []bool{true, false} {
		known := Observation{Model: "og", FirmwareVersion: "1.8.17"}
		current := Observation{}
		fixture := "render-context-no-telemetry.json"
		if telemetry {
			current.Voltage, current.RSSI = ptr(3.91), ptr(int32(-48))
			fixture = "render-context-v1.json"
		}
		target := Target{Width: 800, Height: 480, Palette: "gray4"}
		if telemetry {
			target.Rotation = 90
		}
		got, err := Build("living-room", target, known, current)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile("../../testdata/render-context/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		var gv, wv any
		if err := json.Unmarshal(b, &gv); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(want, &wv); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gv, wv) {
			t.Fatalf("context does not match %s: %s", fixture, b)
		}
	}
}

func TestContextZeroInvalidAndSnapshot(t *testing.T) {
	known := Observation{Voltage: ptr(3.91), Percent: ptr(float64(50)), RSSI: ptr(int32(-48))}
	current := Observation{Voltage: ptr(0.0), Percent: ptr(float64(0)), RSSI: ptr(int32(0))}
	got, err := Build("device", Target{Width: 800, Height: 480, Palette: "gray4"}, known, current)
	if err != nil {
		t.Fatal(err)
	}
	*current.Voltage, *current.Percent, *current.RSSI = 4, 80, -70
	if *got.Device.Battery.Voltage != 0 || *got.Device.Battery.Percent != 0 || *got.Device.Network.RSSI != 0 {
		t.Fatal("zero measurements were lost or snapshot was mutated")
	}
	got, err = Build("device", Target{Width: 800, Height: 480, Palette: "gray4"}, Observation{}, Observation{Voltage: ptr(math.NaN()), Percent: ptr(float64(101))})
	if err != nil {
		t.Fatal(err)
	}
	if got.Device.Battery != nil || got.Device.Network != nil || got.Device.Firmware != nil {
		t.Fatal("empty optional objects must be omitted")
	}
	got, err = Build("device", Target{Width: 800, Height: 480, Palette: "gray4"}, known, Observation{Voltage: ptr(math.Inf(1)), Percent: ptr(float64(-1))})
	if err != nil {
		t.Fatal(err)
	}
	if *got.Device.Battery.Voltage != 3.91 || *got.Device.Battery.Percent != 50 {
		t.Fatal("invalid current telemetry replaced known valid state")
	}
}

func TestContextUnknownFieldsAndRequiredInputs(t *testing.T) {
	var got Context
	if err := json.Unmarshal([]byte(`{"version":"v1","renderTarget":{"width":800,"height":480,"palette":"gray4","future":1},"device":{"name":"test","future":true},"future":{}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1" || got.Device.Name != "test" {
		t.Fatal("unknown fields prevented v1 decoding")
	}
	for _, tt := range []struct {
		name   string
		target Target
	}{{"", Target{Width: 800, Height: 480, Palette: "gray4"}}, {"test", Target{Width: 0, Height: 480, Palette: "gray4"}}, {"test", Target{Width: 800, Height: 480, Palette: "unknown"}}} {
		if _, err := Build(tt.name, tt.target, Observation{}, Observation{}); err == nil {
			t.Fatal("invalid context inputs were accepted")
		}
	}
}

func TestRotationValidationAndPNGDimensions(t *testing.T) {
	for _, rotation := range []int32{0, 90, 180, 270, -90, 45, 360} {
		target := og
		target.Rotation = rotation
		valid := rotation == 0 || rotation == 90 || rotation == 180 || rotation == 270
		if (target.Validate() == nil) != valid {
			t.Fatalf("unexpected rotation validation for %d", rotation)
		}
		if (ValidatePNG(fixture(t), target) == nil) != valid {
			t.Fatalf("rotation %d changed the physical PNG dimensions or bypassed validation", rotation)
		}
	}
}

func TestMeasuredFractionalPercentAndLegacyJSON(t *testing.T) {
	for _, value := range []float64{0, 20.5, 100} {
		current := Observation{Percent: ptr(value)}
		got, err := Build("device", Target{Width: 800, Height: 480, Palette: "gray4"}, Observation{}, current)
		if err != nil || got.Device.Battery == nil || *got.Device.Battery.Percent != value {
			t.Fatal("fraction lost", got, err)
		}
		*current.Percent = 99
		if *got.Device.Battery.Percent != value {
			t.Fatal("snapshot aliases current observation")
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 101} {
		got, err := Build("device", Target{Width: 800, Height: 480, Palette: "gray4"}, Observation{}, Observation{Percent: ptr(value)})
		if err != nil || got.Device.Battery != nil {
			t.Fatal("invalid percent entered context", got, err)
		}
	}
	var legacy Battery
	if err := json.Unmarshal([]byte(`{"percent":50}`), &legacy); err != nil || legacy.Percent == nil || *legacy.Percent != 50 {
		t.Fatal("legacy integer incompatible", err)
	}
}

func TestFractionalContextFixture(t *testing.T) {
	got, err := Build("living-room", Target{Width: 800, Height: 480, Palette: "gray4", Rotation: 90}, Observation{Model: "og", FirmwareVersion: "1.8.17"}, Observation{Voltage: ptr(3.91), Percent: ptr(20.5), RSSI: ptr(int32(-48))})
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../testdata/render-context/render-context-fractional-percent.json")
	if err != nil {
		t.Fatal(err)
	}
	var want Context
	if err := json.Unmarshal(fixture, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("fractional fixture incompatible", got, want)
	}
}

func TestProfileContextFixtures(t *testing.T) {
	for _, name := range []string{"spectra6", "gray16"} {
		b, err := os.ReadFile("../../testdata/render-context/render-context-" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var want Context
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatal(err)
		}
		got, err := Build(want.Device.Name, want.RenderTarget, Observation{Model: want.Device.Model}, Observation{})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("profile changed context v1", name, got, err)
		}
	}
}
