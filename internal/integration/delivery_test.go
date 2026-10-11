//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/render"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Concurrent real API status patches race the running reconciler. Request
// telemetry and delivery results must survive conflict retries and watches.
func verifyDelivery(t *testing.T, c client.Client) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) { verifyHTTPDelivery(t, c, method) })
	}
}

func verifyHTTPDelivery(t *testing.T, c client.Client, method string) {
	t.Helper()
	ctx := context.Background()
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	name := "delivery-" + strings.ToLower(method)
	var calls atomic.Int32
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != method {
			t.Errorf("expected %s, got %s", method, r.Method)
		}
		if method == "POST" {
			var snapshot render.Context
			if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil || snapshot.Device.Name != name || snapshot.Device.Battery == nil || snapshot.Device.Battery.Voltage == nil || *snapshot.Device.Battery.Voltage != 3.92 || snapshot.Device.Network == nil || snapshot.Device.Network.RSSI == nil || *snapshot.Device.Network.RSSI != -49 || snapshot.RenderTarget != (render.Target{Width: 800, Height: 480, Palette: "gray4"}) {
				t.Errorf("current request telemetry missing from context: %+v %v", snapshot, err)
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	screen := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: name}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL, Method: method}}}}
	hardwareID := "02:00:00:00:00:04"
	if method == "POST" {
		hardwareID = "02:00:00:00:00:05"
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: name}, Spec: api.ArprtDeviceSpec{HardwareID: hardwareID, AuthSecretRef: api.LocalReference{Name: "delivery-auth-" + strings.ToLower(method)}, ScreenRef: &api.LocalReference{Name: name}}}
	token := strings.Repeat("A", 43)
	for _, obj := range []client.Object{screen, d, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: d.Spec.AuthSecretRef.Name}, Data: map[string][]byte{"apiKey": []byte(token)}}} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := artifact.NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := byos.NewServer(byos.Config{Namespace: "arprt", PublicBaseURL: "https://byos.example.test/arprt"}, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			req, _ := http.NewRequest("GET", httpServer.URL+"/arprt/devices/arprt/"+name+"/api/display", nil)
			for key, value := range map[string]string{"ID": d.Spec.HardwareID, "Access-Token": token, "Model": "og", "FW-Version": "1.8.17", "Battery-Voltage": "3.92", "RSSI": "-49"} {
				req.Header.Set(key, value)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var display byos.DisplayResponse
			if err := json.NewDecoder(resp.Body).Decode(&display); err != nil || display.Status != 0 || resp.StatusCode != 200 {
				t.Errorf("display failed: %+v %v", display, err)
				return
			}
			req, _ = http.NewRequest("GET", httpServer.URL+strings.TrimPrefix(display.ImageURL, "https://byos.example.test"), nil)
			req.Header.Set("ID", d.Spec.HardwareID)
			req.Header.Set("Access-Token", token)
			image, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer image.Body.Close()
			body, err := io.ReadAll(image.Body)
			if err != nil || image.StatusCode != 200 || !bytes.Equal(body, b) {
				t.Error("authenticated image fetch failed")
			}
		})
	}
	wg.Wait()
	if method == "POST" && calls.Load() != 8 {
		t.Fatal("POST requests were coalesced or reused", calls.Load())
	}
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(d), d) != nil {
			return false
		}
		return d.Status.Battery != nil && *d.Status.Battery.Voltage == 3.92 && d.Status.Network != nil && *d.Status.Network.RSSI == -49 && meta.IsStatusConditionTrue(d.Status.Conditions, "Ready") && meta.IsStatusConditionTrue(d.Status.Conditions, "AuthenticationReady")
	})
	if d.Spec.HardwareID != hardwareID {
		t.Fatal("protocol mutated spec")
	}
}
