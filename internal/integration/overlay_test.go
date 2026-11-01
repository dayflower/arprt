//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/overlay"
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyOverlayAPI(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	makeScreen := func(i int) *api.ArprtScreen {
		return &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: fmt.Sprintf("overlay-%d", i)}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}}}
	}
	scale := int32(2)
	cases := []*api.ScreenOverlay{nil, {}, {Enabled: true}, {Enabled: true, Margin: &api.OverlayMargin{}}, {Enabled: true, Margin: &api.OverlayMargin{Top: 4}}, {Enabled: true, Margin: &api.OverlayMargin{Bottom: 8}}, {Enabled: true, Margin: &api.OverlayMargin{Left: 12}}, {Enabled: true, Margin: &api.OverlayMargin{Right: 16}}, {Enabled: true, Margin: &api.OverlayMargin{Top: 4, Bottom: 8, Left: 12, Right: 16}}, {Enabled: true, Items: &[]api.OverlayItem{}}, {Enabled: true, Items: &[]api.OverlayItem{"wifi", "timestamp"}}, {Enabled: true, CycleCorners: true, Margin: &api.OverlayMargin{Left: 8, Right: 8, Top: 8, Bottom: 8}}, {Enabled: true, Position: "bottom-left", Scale: &scale}}
	for i, setting := range cases {
		s := makeScreen(i)
		s.Spec.Overlay = setting
		if err := c.Create(ctx, s); err != nil {
			t.Fatal("valid overlay rejected", err)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
			t.Fatal(err)
		}
		if setting != nil {
			if !reflect.DeepEqual(s.Spec.Overlay.Margin, setting.Margin) {
				t.Fatal("margin changed during admission")
			}
			if s.Spec.Overlay == nil || (s.Spec.Overlay.Items == nil) != (setting.Items == nil) {
				t.Fatal("item omission changed")
			}
			if setting.Items != nil && len(*s.Spec.Overlay.Items) != len(*setting.Items) {
				t.Fatal("empty/items lost")
			}
			if setting.Position == "" && s.Spec.Overlay.Position != "" {
				t.Fatal("admission inserted a position")
			}
			// A second write/read must preserve an explicit empty array.
			if err := c.Update(ctx, s); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			if setting.Items != nil && s.Spec.Overlay.Items == nil {
				t.Fatal("empty selection lost after update")
			}
		} else if s.Spec.Overlay != nil {
			t.Fatal("omitted overlay defaulted into spec")
		}
	}
	zero, negative := int32(0), int32(-1)
	for i, setting := range []*api.ScreenOverlay{{Enabled: true, Items: &[]api.OverlayItem{"wifi", "wifi"}}, {Enabled: true, Items: &[]api.OverlayItem{"unknown"}}, {Enabled: true, Position: "center"}, {Enabled: true, CycleCorners: true, Position: "top-right"}, {Scale: &zero}, {Scale: &negative}, {Margin: &api.OverlayMargin{Top: -1}}, {Margin: &api.OverlayMargin{Bottom: -1}}, {Margin: &api.OverlayMargin{Left: -1}}, {Margin: &api.OverlayMargin{Right: -1}}} {
		s := makeScreen(i + 100)
		s.Spec.Overlay = setting
		if err := c.Create(ctx, s); !apierrors.IsInvalid(err) {
			t.Fatal("invalid overlay accepted", setting, err)
		}
	}
	raw := `{"apiVersion":"arprt.dayflower.github.io/v1alpha1","kind":"ArprtScreen","metadata":{"name":"overlay-unknown","namespace":"other"},"spec":{"source":{"http":{"url":"http://renderer/render"}},"overlay":{"enabled":true,"unknown":true}}}`
	var obj unstructured.Unstructured
	if err := json.Unmarshal([]byte(raw), &obj.Object); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &obj, client.FieldValidation(metav1.FieldValidationStrict)); err == nil {
		t.Fatal("unknown overlay field accepted")
	}
	for _, field := range []string{"horizontal", "vertical"} {
		obj.SetName("overlay-removed-" + field)
		if err := unstructured.SetNestedField(obj.Object, map[string]any{field: int64(4)}, "spec", "overlay", "margin"); err != nil {
			t.Fatal(err)
		}
		unstructured.RemoveNestedField(obj.Object, "spec", "overlay", "unknown")
		if err := c.Create(ctx, &obj, client.FieldValidation(metav1.FieldValidationStrict)); err == nil {
			t.Fatal("removed margin field accepted", field)
		}
	}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "overlay-percent"}, Spec: api.ArprtDeviceSpec{AuthSecretRef: api.LocalReference{Name: "overlay-key"}}}
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, value := range []float64{0, 20.5, 100, -0.1, 100.1} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			t.Fatal(err)
		}
		d.Status.Battery = &api.BatteryStatus{Percent: &value}
		err := c.Status().Update(ctx, d)
		if value < 0 || value > 100 {
			if !apierrors.IsInvalid(err) {
				t.Fatal("invalid status percent accepted", err)
			}
		} else {
			if err != nil {
				t.Fatal("numeric status percent rejected", err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil || d.Status.Battery.Percent == nil || *d.Status.Battery.Percent != value {
				t.Fatal("fraction lost", err)
			}
		}
	}
}

func verifyOverlayDelivery(t *testing.T, c client.Client) {
	t.Helper()
	ctx := context.Background()
	namespace := "other"
	png, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	defer renderer.Close()
	screen := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "overlay-delivery"}, Spec: api.ArprtScreenSpec{Overlay: &api.ScreenOverlay{Enabled: true, CycleCorners: true, Margin: &api.OverlayMargin{Left: 8, Right: 8, Top: 4, Bottom: 4}}, Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "overlay-delivery-key"}, Data: map[string][]byte{"apiKey": []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")}}
	playlist := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "overlay-delivery-list"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: screen.Name}, {Name: screen.Name}}}}
	device := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "overlay-delivery-device"}, Spec: api.ArprtDeviceSpec{HardwareID: "02:00:00:00:00:10", AuthSecretRef: api.LocalReference{Name: secret.Name}, PlaylistRef: &api.LocalReference{Name: playlist.Name}}}
	for _, obj := range []client.Object{screen, secret, playlist, device} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := byos.NewServer(byos.Config{Namespace: namespace, PublicBaseURL: "https://byos.example.test", OverlayTimeZone: "Asia/Tokyo"}, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	route := "/devices/" + namespace + "/" + device.Name
	poll := func(s *byos.Server, percent string) byos.DisplayResponse {
		r := httptest.NewRequest("GET", route+"/api/display", nil)
		for k, v := range map[string]string{"ID": device.Spec.HardwareID, "Access-Token": string(secret.Data["apiKey"]), "Model": "og", "FW-Version": "1.8.17", "Percent-Charged": percent, "RSSI": "-70"} {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var response byos.DisplayResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.Status != 0 {
			t.Fatal("overlay delivery failed", w.Code, response, err)
		}
		return response
	}
	first := poll(server, "20.5")
	if err := c.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil {
		t.Fatal(err)
	}
	committed := device.Status.ScreenCycle.DeepCopy()
	if device.Status.Battery.Percent == nil || *device.Status.Battery.Percent != 20.5 || committed == nil || committed.EntryIndex != 0 {
		t.Fatal("actual API status lost overlay/measurement")
	}
	// Reopen the PVC and verify actual API microsecond serialization supports replay.
	restartedRepo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := byos.NewServer(server.Config, c, c, restartedRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got := poll(restarted, "0"); got != first {
		t.Fatal("API replay redrew composite", got, first)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || !artifact.EqualCycle(committed, device.Status.ScreenCycle) {
		t.Fatal("API replay extended window", err)
	}
	corner, err := restartedRepo.Corner(ctx, string(device.UID))
	if err != nil || corner.Current != overlay.BottomRight || corner.Revision != 1 {
		t.Fatal("API replay advanced corner", corner, err)
	}
	device.Status.ScreenCycle.ServedAt = metav1.NewMicroTime(time.Now().UTC().Add(-time.Hour))
	if err := c.Status().Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	next := poll(restarted, "0")
	if next.Filename == first.Filename {
		t.Fatal("new API turn did not move overlay")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || device.Status.ScreenCycle.EntryIndex != 1 {
		t.Fatal("new HTTP turn lost repeated position", device.Status.ScreenCycle, err)
	}
	corner, err = restartedRepo.Corner(ctx, string(device.UID))
	if err != nil || corner.Revision != 2 || corner.Current != overlay.BottomLeft {
		t.Fatal("API turn not finalized", corner, err)
	}
	// Reopen at the later occurrence: replay must retain it, then wrap to zero.
	restartedRepo, err = artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err = byos.NewServer(server.Config, c, c, restartedRepo)
	if err != nil {
		t.Fatal(err)
	}
	committed = device.Status.ScreenCycle.DeepCopy()
	if got := poll(restarted, "0"); got != next {
		t.Fatal("later occurrence replay changed image", got, next)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || !artifact.EqualCycle(committed, device.Status.ScreenCycle) {
		t.Fatal("later occurrence replay changed turn", err)
	}
	device.Status.ScreenCycle.ServedAt = metav1.NewMicroTime(time.Now().UTC().Add(-time.Hour))
	if err := c.Status().Update(ctx, device); err != nil {
		t.Fatal(err)
	}
	poll(restarted, "0")
	if err := c.Get(ctx, client.ObjectKeyFromObject(device), device); err != nil || device.Status.ScreenCycle.EntryIndex != 0 {
		t.Fatal("repeated HTTP cycle did not wrap", device.Status.ScreenCycle, err)
	}
}
