package devicestate

import (
	"encoding/json"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"k8s.io/utils/ptr"
)

func TestReviewedProfilesWithoutFirmwareGate(t *testing.T) {
	// Fixed expectations protect the reviewed defaults, including E1003's
	// firmware orientation, rather than deriving expectations from the registry.
	for model, want := range map[string]struct {
		width, height int32
		palette       string
		limit         int
	}{
		"og":               {800, 480, "gray4", 90000},
		"reterminal_e1001": {800, 480, "gray4", 750000},
		"reTerminal E1001": {800, 480, "gray4", 750000},
		"reterminal_e1002": {800, 480, "spectra6", 750000},
		"reTerminal E1002": {800, 480, "spectra6", 750000},
		"reterminal_e1003": {1872, 1404, "gray16", 750000},
		"reTerminal E1003": {1872, 1404, "gray16", 750000},
		"reterminal_e1004": {1200, 1600, "spectra6", 750000},
		"reTerminal E1004": {1200, 1600, "spectra6", 750000},
	} {
		for _, firmware := range []string{"", "unknown", "1.8.17"} {
			d := &api.ArprtDevice{Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: model, FirmwareVersion: firmware}}}
			target, status, err := Resolve(d)
			if err != nil || target.Target.Width != want.width || target.Target.Height != want.height || target.Target.Palette != want.palette || target.Output.PNGByteLimit != want.limit || *status.Sources != (api.DisplaySources{Dimensions: "profile", Palette: "profile", PNGByteLimit: "profile"}) {
				t.Fatalf("profile %q firmware %q: %+v %+v %v", model, firmware, target, status, err)
			}
		}
	}
}

func TestOverrideCombinationsAndProvenance(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		d := &api.ArprtDevice{Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "og", ReportedDisplay: &api.ReportedDisplayStatus{Width: ptr.To(int32(640)), Height: ptr.To(int32(400))}}}}
		d.Spec.Rotation = 90
		d.Spec.Display = &api.DisplayOverride{}
		want := api.DisplayStatus{Width: 640, Height: 400, Palette: "gray4", PNGByteLimit: 90000, Sources: &api.DisplaySources{Dimensions: "reported", Palette: "profile", PNGByteLimit: "profile"}}
		if mask&1 != 0 {
			d.Spec.Display.Width, d.Spec.Display.Height = ptr.To(int32(320)), ptr.To(int32(240))
			want.Width, want.Height, want.Sources.Dimensions = 320, 240, "explicit"
		}
		if mask&2 != 0 {
			d.Spec.Display.Palette = "spectra6"
			want.Palette, want.Sources.Palette = "spectra6", "explicit"
		}
		if mask&4 != 0 {
			d.Spec.Display.PNGByteLimit = ptr.To(int32(750000))
			want.PNGByteLimit, want.Sources.PNGByteLimit = 750000, "explicit"
		}
		target, status, err := Resolve(d)
		if err != nil || status.Width != want.Width || status.Height != want.Height || status.Palette != want.Palette || status.PNGByteLimit != want.PNGByteLimit || *status.Sources != *want.Sources || target.Target.Rotation != 90 {
			t.Fatalf("combination %d: %+v %v", mask, status, err)
		}
		if *d.Status.Device.ReportedDisplay.Width != 640 {
			t.Fatal("override rewrote observations")
		}
		d.Status.Display = status
		d.Spec.Display = nil
		_, restored, err := Resolve(d)
		if err != nil || restored.Width != 640 || restored.Palette != "gray4" || restored.PNGByteLimit != 90000 {
			t.Fatal("removal reused effective status", restored, err)
		}
	}
}

func TestUnknownModelRequiresCompleteCapabilities(t *testing.T) {
	for _, model := range []string{"", "unknown", "OG", "reterminal-e1002"} {
		for mask := 0; mask < 8; mask++ {
			d := &api.ArprtDevice{Spec: api.ArprtDeviceSpec{Display: &api.DisplayOverride{}}, Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: model}}}
			if mask&1 != 0 {
				d.Status.Device.ReportedDisplay = &api.ReportedDisplayStatus{Width: ptr.To(int32(800)), Height: ptr.To(int32(480))}
			}
			if mask&2 != 0 {
				d.Spec.Display.Palette = "gray16"
			}
			if mask&4 != 0 {
				d.Spec.Display.PNGByteLimit = ptr.To(int32(90000))
			}
			if _, _, err := Resolve(d); (err == nil) != (mask == 7) {
				t.Fatalf("model %q mask %d: %v", model, mask, err)
			}
		}
	}
}

func TestInvalidDimensionsAndLegacyStatus(t *testing.T) {
	for _, pair := range [][2]int32{{0, 480}, {800, 0}, {-1, 480}, {4097, 1}, {2049, 2049}, {2147483647, 2147483647}} {
		d := &api.ArprtDevice{Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "og", ReportedDisplay: &api.ReportedDisplayStatus{Width: &pair[0], Height: &pair[1]}}}}
		_, status, err := Resolve(d)
		if err != nil || status.Width != 800 || status.Sources.Dimensions != "profile" {
			t.Fatal("invalid report supplied dimensions", pair, status, err)
		}
		d.Spec.Display = &api.DisplayOverride{Width: &pair[0], Height: &pair[1]}
		if _, _, err := Resolve(d); err == nil {
			t.Fatal("invalid explicit pair accepted", pair)
		}
	}
	for _, override := range []*api.DisplayOverride{{Width: ptr.To(int32(1))}, {Height: ptr.To(int32(1))}, {PNGByteLimit: ptr.To(int32(0))}, {Palette: "bw"}, {Palette: "bwry"}} {
		d := &api.ArprtDevice{Spec: api.ArprtDeviceSpec{Display: override}, Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "og"}}}
		if _, _, err := Resolve(d); err == nil {
			t.Fatal("invalid override accepted", override)
		}
	}
	var d api.ArprtDevice
	if err := json.Unmarshal([]byte(`{"status":{"display":{"width":1,"height":2,"palette":"bwry"},"device":{"model":"og"}}}`), &d); err != nil {
		t.Fatal(err)
	}
	_, status, err := Resolve(&d)
	if err != nil || status.Width != 800 || status.Sources.Dimensions != "profile" || d.Status.Display.Palette != "bwry" {
		t.Fatal("legacy effective state became an observation", status, err)
	}
	d.Status.Device = nil
	if _, _, err := Resolve(&d); err == nil {
		t.Fatal("legacy effective state invented an unknown model's capabilities")
	}
}

func TestEffectiveIdentityExcludesProvenance(t *testing.T) {
	a := &api.ArprtDevice{Status: api.ArprtDeviceStatus{Device: &api.DeviceInfoStatus{Model: "reterminal_e1001"}}}
	b := &api.ArprtDevice{Spec: api.ArprtDeviceSpec{Display: &api.DisplayOverride{Width: ptr.To(int32(800)), Height: ptr.To(int32(480)), Palette: "gray4", PNGByteLimit: ptr.To(int32(750000))}}}
	if Resolved(a) != Resolved(b) {
		t.Fatal("equivalent profiles and overrides fragmented identity")
	}
	b.Spec.Display.PNGByteLimit = ptr.To(int32(90000))
	if Resolved(a) == Resolved(b) {
		t.Fatal("different output constraints share identity")
	}
}
