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
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/byos"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Concurrent real API status patches race the running reconciler. Request
// telemetry and delivery results must survive conflict retries and watches.
func verifyDelivery(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	screen := &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "delivery"}, Spec: api.TrmnlScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL}}}}
	d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "delivery"}, Spec: api.TrmnlDeviceSpec{HardwareID: "02:00:00:00:00:04", AuthSecretRef: api.LocalReference{Name: "delivery-auth"}, ScreenRef: &api.LocalReference{Name: "delivery"}}}
	token := strings.Repeat("A", 43)
	for _, obj := range []client.Object{screen, d, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "delivery-auth"}, Data: map[string][]byte{"apiKey": []byte(token)}}} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := artifact.NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := byos.NewServer(byos.Config{Namespace: "trmnl", PublicBaseURL: "https://byos.example.test/trmnl"}, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			req, _ := http.NewRequest("GET", httpServer.URL+"/trmnl/devices/trmnl/delivery/api/display", nil)
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
	eventually(t, func() bool {
		if c.Get(ctx, client.ObjectKeyFromObject(d), d) != nil {
			return false
		}
		return d.Status.Battery != nil && *d.Status.Battery.Voltage == 3.92 && d.Status.Network != nil && *d.Status.Network.RSSI == -49 && meta.IsStatusConditionTrue(d.Status.Conditions, "Ready") && meta.IsStatusConditionTrue(d.Status.Conditions, "AuthenticationReady")
	})
	if d.Spec.HardwareID != "02:00:00:00:00:04" {
		t.Fatal("protocol mutated spec")
	}
}
