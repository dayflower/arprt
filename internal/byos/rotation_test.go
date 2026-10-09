package byos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
)

func TestRotationDeliveryCacheFallbackAndRetention(t *testing.T) {
	for _, scenario := range []struct {
		name, method string
		cycling      bool
	}{
		{"GET/screen", "GET", false}, {"POST/screen", "POST", false},
		{"GET/cycle", "GET", true}, {"POST/cycle", "POST", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			method := scenario.method
			var calls atomic.Int32
			var expected atomic.Int32
			var fail atomic.Bool
			image := pngFixture(t)
			renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				rotation := expected.Load()
				if r.Method != method || r.URL.Query().Get("rotation") != strconv.Itoa(int(rotation)) {
					t.Error("renderer did not receive desired rotation")
				}
				if method == "POST" {
					var snapshot render.Context
					if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil || snapshot.RenderTarget.Rotation != rotation || snapshot.RenderTarget.Width != 800 || snapshot.RenderTarget.Height != 480 {
						t.Error("Context rotation or physical dimensions differ from query", err)
					}
				}
				if fail.Load() {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(image)
			}))
			defer renderer.Close()
			s, c, root := newTestServer(t, renderer.URL)
			if method == "POST" {
				postScreen(t, c)
			}
			if scenario.cycling {
				d := loadDevice(t, c)
				d.Spec.ScreenRef = nil
				d.Generation++
				if err := c.Update(context.Background(), d); err != nil {
					t.Fatal(err)
				}
			}
			display := func() DisplayResponse {
				return decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			}
			first := display()
			if first.Status != 0 {
				t.Fatal("initial image unavailable")
			}
			issuedPath := strings.TrimPrefix(first.ImageURL, "https://byos.example.test")
			for _, rotation := range []int32{90, 180, 270} {
				d := loadDevice(t, c)
				d.Spec.Rotation = rotation
				d.Generation++
				if err := c.Update(context.Background(), d); err != nil {
					t.Fatal(err)
				}
				expected.Store(rotation)
				fail.Store(true)
				if display().Status != 202 {
					t.Fatal("another rotation supplied stale fallback")
				}
				fail.Store(false)
				if display().Status != 0 {
					t.Fatal("rotation change did not render")
				}
				if d = loadDevice(t, c); d.Status.Display.Width != 800 || d.Status.Display.Height != 480 {
					t.Fatal("rotation changed observed hardware dimensions")
				}
				before := calls.Load()
				if display().Status != 0 {
					t.Fatal("repeat delivery failed")
				}
				if (method == "GET" || scenario.cycling) && calls.Load() != before {
					t.Fatal("same rotation did not reuse GET output")
				}
				if method == "POST" && !scenario.cycling && calls.Load() != before+1 {
					t.Fatal("POST unexpectedly reused fresh output")
				}
			}
			repo, err := artifact.NewPVC(root)
			if err != nil {
				t.Fatal(err)
			}
			s, err = NewServer(s.Config, c, c, repo)
			if err != nil {
				t.Fatal(err)
			}
			fail.Store(true)
			if display().Status != 0 {
				t.Fatal("restart lost compatible rotated output")
			}
			if request(s, "GET", issuedPath, auth(), nil).Code != 200 {
				t.Fatal("rotation change revoked an already-issued image")
			}
			// Returning to zero can still use the original, compatible retained artifact.
			d := loadDevice(t, c)
			d.Spec.Rotation = 0
			d.Generation++
			if err := c.Update(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			expected.Store(0)
			if display().Status != 0 {
				t.Fatal("zero rotation lost compatibility with original persisted output")
			}
		})
	}
}
