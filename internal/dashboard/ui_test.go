package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/assignment"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEmbeddedUIRouting(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	for _, tc := range []struct{ path, contentType string }{
		{"/", "text/html; charset=utf-8"},
		{"/assets/dashboard.css", "text/css; charset=utf-8"},
		{"/assets/dashboard.js", "text/javascript; charset=utf-8"},
		{"/assets/mark.svg", "image/svg+xml"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := get(t, s, tc.path)
			if w.Code != 200 || w.Header().Get("Content-Type") != tc.contentType || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("incorrect embedded UI response", w.Code, w.Header())
			}
			if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
				t.Fatal("incorrect asset length")
			}
			w = httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if w.Code != 405 || w.Header().Get("Allow") != "GET" {
				t.Fatal("UI accepted a write method")
			}
			if w := get(t, s, tc.path+"?unexpected=true"); w.Code != 400 {
				t.Fatal("UI accepted query parameters", w.Code)
			}
		})
	}
	for _, path := range []string{"/assets/", "/assets/index.html", "/assets/../server.go", "/assets/%64ashboard.js", "/unknown", "/api/unknown"} {
		if w := get(t, s, path); w.Code != 404 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Fatal("unknown path fell back to UI", path, w.Code)
		}
	}
}

// The fake client does not implement Kubernetes pagination. This reader provides
// deterministic pages for the browser suite; real pagination is covered by envtest.
type browserPageReader struct{ client.Reader }

func (r browserPageReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	devices, ok := list.(*api.ArprtDeviceList)
	if !ok {
		return r.Reader.List(ctx, list, opts...)
	}
	options := &client.ListOptions{}
	options.ApplyOptions(opts)
	if err := r.Reader.List(ctx, devices, client.InNamespace(options.Namespace)); err != nil {
		return err
	}
	sort.Slice(devices.Items, func(i, j int) bool { return devices.Items[i].Name < devices.Items[j].Name })
	start, _ := strconv.Atoi(options.Continue)
	end := min(start+int(options.Limit), len(devices.Items))
	if end < len(devices.Items) {
		devices.Continue = strconv.Itoa(end)
	}
	devices.Items = devices.Items[start:end]
	return nil
}

func TestBrowserUI(t *testing.T) {
	if os.Getenv("ARPRT_DASHBOARD_BROWSER") != "1" {
		t.Skip("set ARPRT_DASHBOARD_BROWSER=1 with Playwright and Chromium installed")
	}
	s, repo, c, d, sc := fixture(t)
	record := recordFixture(t, repo, c, d, sc)
	if err := c.Delete(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	playlist := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: "morning", UID: "playlist-fixture"}, Spec: api.ArprtPlaylistSpec{FailurePolicy: "Skip", Screens: []api.LocalReference{{Name: sc.Name}, {Name: "missing-screen"}}}}
	if err := c.Create(t.Context(), playlist); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 23; i++ {
		device := d.DeepCopy()
		device.ObjectMeta = metav1.ObjectMeta{Namespace: d.Namespace, Name: fmt.Sprintf("device-%02d", i), UID: types.UID(fmt.Sprintf("browser-device-%02d", i)), Generation: 1}
		device.Status.LastSeenAt = &metav1.Time{Time: time.Now().UTC().Add(-3 * time.Minute)}
		device.Status.Battery = &api.BatteryStatus{Voltage: ptr.To(3.9)}
		device.Status.Network = &api.NetworkStatus{RSSI: ptr.To(int32(-54))}
		setCondition(device, "Ready", metav1.ConditionTrue, "ImageReady", 1)
		r := record
		r.DeviceUID = string(device.UID)
		switch i {
		case 2:
			device.Status.Battery.Percent = ptr.To(0.0)
			setCondition(device, "Ready", metav1.ConditionTrue, "StaleImageAvailable", 1)
			setCondition(device, "RenderReady", metav1.ConditionFalse, "RenderFailed", 1)
			r.Result, r.Rendering = "stale", "failed"
		case 3:
			device.Status.LastSeenAt = &metav1.Time{Time: time.Now().UTC().Add(-time.Hour)}
			setCondition(device, "Ready", metav1.ConditionTrue, "StaleImageAvailable", 1)
			setCondition(device, "RenderReady", metav1.ConditionFalse, "RenderFailed", 1)
			setCondition(device, "TelemetryValid", metav1.ConditionFalse, "InvalidTelemetry", 1)
			r.Result, r.Rendering = "stale", "failed"
		case 4:
			device.Status = api.ArprtDeviceStatus{}
		case 5:
			device.Spec.ScreenRef, device.Spec.PlaylistRef = nil, &api.LocalReference{Name: playlist.Name}
			setCondition(device, "Ready", metav1.ConditionFalse, "ImagePending", 1)
		case 6:
			device.Spec.ScreenRef = nil
		case 7:
			r.ImageDigest = strings.Repeat("f", 64)
		case 8:
			r.RenderedAt, r.Result, r.Rendering = nil, "unknown", "unknown"
		case 9:
			device.Status.Battery = &api.BatteryStatus{Percent: ptr.To(82.0)}
			device.Status.Network = nil
		}
		if err := c.Create(t.Context(), device); err != nil {
			t.Fatal(err)
		}
		if i != 4 && i != 5 {
			set, err := (assignment.Explicit{Reader: c}).Eligible(t.Context(), device)
			if err != nil {
				t.Fatal(err)
			}
			r.Assignment = set.Fingerprint
			if err := repo.RecordDelivery(t.Context(), r); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.reader = browserPageReader{Reader: c}
	// A five-minute device poll must not delay UI refreshes to five minutes.
	s.config.RefreshSeconds = 300
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	script, err := filepath.Abs("../../tools/dashboard-browser.cjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Env = append(os.Environ(), "ARPRT_DASHBOARD_URL="+httpServer.URL)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("browser checks failed:", err)
	}
}
