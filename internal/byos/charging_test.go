package byos

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/overlay"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPowerObservations(t *testing.T) {
	for _, tt := range []struct {
		name    string
		yes, no string
		value   func(Observation) *bool
	}{
		{"battery-charging", "1", "0", func(o Observation) *bool { return o.Charging }},
		{"usb-connected", "true", "false", func(o Observation) *bool { return o.USBConnected }},
	} {
		for _, value := range []string{tt.yes, tt.no} {
			o := Observe(map[string]string{tt.name: value})
			if tt.value(o) == nil || *tt.value(o) != (value == tt.yes) || len(o.Invalid) != 0 {
				t.Fatal("known power state lost", tt.name, value, o)
			}
		}
		for _, value := range []string{"", "unknown", "yes", "TRUE", "2", "-1"} {
			o := Observe(map[string]string{tt.name: value})
			if tt.value(o) != nil || len(o.Invalid) != 1 {
				t.Fatal("invalid power observation accepted", tt.name, value, o)
			}
		}
		alias := "BaTtErY_ChArGiNg"
		if tt.name == "usb-connected" {
			alias = "UsB_CoNnEcTeD"
		}
		h, err := Headers(http.Header{alias: []string{" " + tt.no + " "}})
		if err != nil || tt.value(Observe(h)) == nil || *tt.value(Observe(h)) {
			t.Fatal("alias/known false lost", h, err)
		}
		for _, headers := range []http.Header{{tt.name: []string{tt.yes, tt.no}}, {tt.name: []string{tt.yes}, alias: []string{tt.no}}} {
			h, err = Headers(headers)
			if err != nil || tt.value(Observe(h)) != nil || len(Observe(h).Invalid) != 1 {
				t.Fatal("duplicate chose a power state", h, err)
			}
		}
		if tt.value(Observe(map[string]string{})) != nil {
			t.Fatal("missing observation became false")
		}
	}
}

func TestChargingDeliveryCurrentInputsAndCache(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, false, "battery")
	var screen api.ArprtScreen
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &screen); err != nil {
		t.Fatal(err)
	}
	screen.Spec.Overlay.Margin = &api.OverlayMargin{Left: 8, Right: 8, Top: 4, Bottom: 4}
	if err := c.Update(context.Background(), &screen); err != nil {
		t.Fatal(err)
	}
	b := pngFixture(t)
	calls := 0
	fixtureGET(s, b, &calls)
	poll := func(headers map[string]string) DisplayResponse {
		r := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
		if r.Status != 0 {
			t.Fatal(r)
		}
		return r
	}
	baseline := poll(auth())
	h := auth()
	h["Battery-Charging"] = "1"
	delete(h, "Battery-Voltage")
	direct := poll(h)
	base := baseImage(t, s, c, false)
	yes := true
	drawing, err := overlay.NormalizeResolved(base.Unit.ResolvedTarget(), overlay.Settings{Items: []string{"battery"}, Margin: overlay.Margins{Left: 8, Right: 8, Top: 4, Bottom: 4}}, base.RenderedAt, time.UTC, overlay.Telemetry{Charging: &yes}, overlay.TopRight)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := overlay.ComposeResolved(context.Background(), b, base.Unit.ResolvedTarget(), drawing)
	if err != nil || !bytes.Equal(imageBytes(t, s, direct), expected) {
		t.Fatal("Screen margin/charging did not reach drawing", err)
	}
	if direct.Filename == baseline.Filename {
		t.Fatal("charging mark not substituted")
	}
	// USB presence never changes the visible mark, whether charging is known,
	// unknown, invalid, aliased or duplicated. With no level measurement the
	// battery item disappears unless a valid charging observation is present.
	for _, usb := range []http.Header{
		{},
		{"USB-Connected": []string{"true"}},
		{"USB-Connected": []string{"false"}},
		{"UsB_CoNnEcTeD": []string{"true"}},
		{"USB-Connected": []string{"invalid"}},
		{"USB-Connected": []string{"true", "false"}},
	} {
		for _, charging := range []struct {
			headers http.Header
			active  bool
		}{
			{http.Header{}, false},
			{http.Header{"Battery-Charging": []string{"1"}}, true},
			{http.Header{"Battery-Charging": []string{"0"}}, false},
			{http.Header{"BaTtErY_ChArGiNg": []string{"1"}}, true},
			{http.Header{"Battery-Charging": []string{"invalid"}}, false},
			{http.Header{"Battery-Charging": []string{"1", "0"}}, false},
			{http.Header{"Battery-Charging": []string{"1"}, "battery_charging": []string{"0"}}, false},
		} {
			for _, measured := range []bool{true, false} {
				headers := http.Header{}
				for k, v := range auth() {
					headers[k] = []string{v}
				}
				if !measured {
					delete(headers, "Battery-Voltage")
				}
				for k, v := range usb {
					headers[k] = v
				}
				for k, v := range charging.headers {
					headers[k] = v
				}
				req := httptest.NewRequest("GET", devicePath+"/api/display", nil)
				req.Header = headers
				rec := httptest.NewRecorder()
				s.ServeHTTP(rec, req)
				r := decodeDisplay(t, rec)
				if r.Status != 0 {
					t.Fatal(r)
				}
				if charging.active {
					if r.Filename != direct.Filename {
						t.Fatal("valid charging did not select the charging mark", usb, charging, measured)
					}
				} else if measured {
					if r.Filename != baseline.Filename {
						t.Fatal("USB or unavailable charging changed battery level", usb, charging)
					}
				} else if !bytes.Equal(imageBytes(t, s, r), b) {
					t.Fatal("USB or stored power state supplied missing battery item", usb, charging)
				}
			}
		}
	}
	if calls != 1 {
		t.Fatal("charging redraw refreshed GET base", calls)
	}
}
