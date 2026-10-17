package byos

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/render"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestProfileHTTPDeliveryAndOverlays(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, model := range []string{"og", "reterminal_e1001", "reterminal_e1002", "reterminal_e1003", "reTerminal E1004", "unreviewed"} {
			t.Run(method+"/"+model, func(t *testing.T) {
				var source bytes.Buffer
				if err := png.Encode(&source, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
					t.Fatal(err)
				}
				targets := make(chan render.Target, 1)
				renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != method {
						t.Errorf("wrong method %s", r.Method)
					}
					q := r.URL.Query()
					width, _ := strconv.Atoi(q.Get("width"))
					height, _ := strconv.Atoi(q.Get("height"))
					targets <- render.Target{Width: int32(width), Height: int32(height), Palette: q.Get("palette")}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(source.Bytes())
				}))
				defer renderer.Close()
				s, c, _ := newTestServer(t, renderer.URL)
				var sc api.ArprtScreen
				if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
					t.Fatal(err)
				}
				sc.Spec.Source.HTTP.Method = method
				sc.Spec.ImageProcessing = &api.ImageProcessing{Dithering: "none"}
				if err := c.Update(t.Context(), &sc); err != nil {
					t.Fatal(err)
				}
				if model == "unreviewed" {
					d := loadDevice(t, c)
					d.Spec.Display = &api.DisplayOverride{Width: ptr.To(int32(320)), Height: ptr.To(int32(240)), Palette: "spectra6", PNGByteLimit: ptr.To(int32(90000))}
					if err := c.Update(t.Context(), d); err != nil {
						t.Fatal(err)
					}
				}
				enableOverlay(t, c, false)
				headers := auth()
				headers["Model"] = model
				delete(headers, "FW-Version")
				delete(headers, "Width")
				delete(headers, "Height")
				response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
				if response.Status != 0 {
					t.Fatal("profile pending", response)
				}
				d := loadDevice(t, c)
				target := devicestate.Resolved(d)
				gotTarget := <-targets
				if gotTarget != target.Target {
					t.Fatal("renderer target differs", gotTarget, target)
				}
				if err := target.ValidateOutput(t.Context(), imageBytes(t, s, response)); err != nil {
					t.Fatal("incompatible profile delivery", err)
				}
				if (model != "unreviewed" && d.Status.Display.Sources.Dimensions != "profile") || d.Status.Device.FirmwareVersion != "" {
					t.Fatal("invented observations", d.Status)
				}
			})
		}
	}
}

func TestObservedDimensionsRemainSeparateAndAtomic(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	d := loadDevice(t, c)
	d.Spec.Display = &api.DisplayOverride{Width: ptr.To(int32(320)), Height: ptr.To(int32(240))}
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	for i, fields := range []map[string]string{
		{"model": "og", "fw-version": "unknown", "width": "600", "height": "400"},
		{"width": "900"},
		{"height": "700"},
		{},
		{"width": "2147483647", "height": "2147483647"},
		{"width": "invalid", "height": "800"},
	} {
		observed, err := s.observeDisplay(t.Context(), d, Observe(fields))
		if err != nil || observed.targetErr != nil {
			t.Fatal(err, observed.targetErr)
		}
		raw := d.Status.Device.ReportedDisplay
		if i == 0 && (*raw.Width != 600 || *raw.Height != 400) {
			t.Fatal("raw pair overwritten")
		}
		if i == 1 && (raw.Width == nil || *raw.Width != 900 || raw.Height != nil) {
			t.Fatal("mixed separate observations", raw)
		}
		if (i == 2 || i == 3) && (raw.Width != nil || raw.Height == nil || *raw.Height != 700) {
			t.Fatal("lost incomplete pair", raw)
		}
		if i == 4 && (*raw.Width != 2147483647 || *raw.Height != 2147483647) {
			t.Fatal("lost unsafe raw pair")
		}
		if i == 5 && (raw.Width != nil || *raw.Height != 800) {
			t.Fatal("invalid observation supplied a value")
		}
		if observed.target.Target.Width != 320 || d.Status.Display.Sources.Dimensions != "explicit" {
			t.Fatal("explicit dimensions lost")
		}
	}
	d.Spec.Display = nil
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	observed, err := s.observeDisplay(t.Context(), d, Observation{})
	if err != nil || observed.target.Target.Width != 800 || d.Status.Display.Sources.Dimensions != "profile" {
		t.Fatal("removal reused old effective pair", err, observed)
	}
}

func TestBudgetEditInvalidatesReplayAndPreservesIssuedBytes(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	calls := 0
	b := pngFixture(t)
	fixtureGET(s, b, &calls)
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 {
		t.Fatal(first)
	}
	oldTurn := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
	d = loadDevice(t, c)
	d.Spec.Display = &api.DisplayOverride{PNGByteLimit: ptr.To(int32(len(b)))}
	d.Generation++
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	second := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if second.Status != 202 || calls != 2 {
		t.Fatal("exclusive budget edit reused old success", second, calls)
	}
	if !bytes.Equal(b, imageBytes(t, s, first)) {
		t.Fatal("budget edit rewrote issued URL")
	}
	if loadDevice(t, c).Status.ScreenCycle.ImageDigest != oldTurn.ImageDigest {
		t.Fatal("pending response advanced cycle")
	}
}

func TestFinalGateRecomputesOutputBudget(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			if method == "POST" {
				postScreen(t, c)
			}
			execute := func(string) render.Executor {
				return executorFunc(func(context.Context, render.Request) (render.Output, error) {
					d := loadDevice(t, c)
					// Change observations without changing generation or spec. The
					// final gate must include constraints, not just context-v1 target.
					d.Status.Device.Model = "reterminal_e1001"
					if err := c.Status().Update(t.Context(), d); err != nil {
						t.Fatal(err)
					}
					return render.Output{PNG: pngFixture(t)}, nil
				})
			}
			s.GET.NewExecutor, s.POST.NewExecutor = execute, execute
			response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			if response.Status != 202 || response.ImageURL != "" {
				t.Fatal("obsolete budget issued", response)
			}
		})
	}
}
