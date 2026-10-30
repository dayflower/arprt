package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/screen"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(t *testing.T) (*Server, *artifact.PVC, client.Client, *api.ArprtDevice, *api.ArprtScreen) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "desk", UID: "device-1", Generation: 1},
		Spec:   api.ArprtDeviceSpec{HardwareID: "02:00:00:00:00:01", AuthSecretRef: api.LocalReference{Name: "private-auth"}, ScreenRef: &api.LocalReference{Name: "photo"}},
		Status: api.ArprtDeviceStatus{HardwareID: "02:00:00:00:00:01", Device: &api.DeviceInfoStatus{Model: "og", FirmwareVersion: "fixture"}}}
	sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "photo", UID: "screen-1", Generation: 1},
		Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer.example.test/render", Method: "GET"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(d, sc).WithObjects(d, sc).Build()
	repo, err := artifact.NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{Namespace: "arprt", RefreshSeconds: 30}, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, c, d, sc
}

func recordFixture(t *testing.T, repo *artifact.PVC, reader client.Reader, d *api.ArprtDevice, sc *api.ArprtScreen) artifact.DeliveryRecord {
	t.Helper()
	set, err := (assignment.Explicit{Reader: reader}).Eligible(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := screen.InputRevision(sc.Spec)
	target := devicestate.Resolved(d)
	b, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	generated := time.Now().UTC().Add(-48 * time.Hour)
	m, err := repo.Publish(t.Context(), artifact.Metadata{Unit: artifact.Unit{Namespace: d.Namespace, ScreenUID: string(sc.UID), Target: target.Target},
		ScreenGeneration: sc.Generation, InputRevision: rev, Sequence: 1, AttemptID: "one", RenderedAt: generated}, b)
	if err != nil {
		t.Fatal(err)
	}
	r := artifact.DeliveryRecord{Version: 1, DeviceUID: string(d.UID), DeviceGeneration: d.Generation, ScreenName: sc.Name, ScreenUID: string(sc.UID), ScreenGeneration: sc.Generation,
		Assignment: set.Fingerprint, InputRevision: rev, Target: target, ImageDigest: m.ImageDigest, RenderedAt: &generated, IssuedAt: time.Now().UTC(), Result: "ready", Rendering: "ready"}
	if err := repo.RecordDelivery(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func get(t *testing.T, s http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func page(t *testing.T, s http.Handler) DevicePage {
	t.Helper()
	w := get(t, s, "/api/devices")
	var p DevicePage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return p
}

func setCondition(d *api.ArprtDevice, kind string, status metav1.ConditionStatus, reason string, gen int64) {
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: "fixture", ObservedGeneration: gen})
}

func TestStatesRemainIndependentAndGenerationAware(t *testing.T) {
	for _, tc := range []struct {
		name, communication, delivery, rendering string
		edit                                     func(*api.ArprtDevice, *api.ArprtScreen)
	}{
		{"normal-old-image", "Ready", "Ready", "Ready", func(*api.ArprtDevice, *api.ArprtScreen) {}},
		{"silent", "Silent", "Ready", "Ready", func(d *api.ArprtDevice, _ *api.ArprtScreen) {
			d.Status.LastSeenAt = &metav1.Time{Time: time.Now().Add(-121 * time.Second)}
		}},
		{"never-seen", "Never seen", "Ready", "Ready", func(d *api.ArprtDevice, _ *api.ArprtScreen) { d.Status.LastSeenAt = nil }},
		{"unknown-clock", "Unknown", "Ready", "Ready", func(d *api.ArprtDevice, _ *api.ArprtScreen) {
			d.Status.LastSeenAt = &metav1.Time{Time: time.Now().Add(time.Hour)}
		}},
		{"serving-stale", "Ready", "Serving stale image", "Render failed", func(d *api.ArprtDevice, _ *api.ArprtScreen) {
			setCondition(d, "Ready", metav1.ConditionTrue, "StaleImageAvailable", 1)
			setCondition(d, "RenderReady", metav1.ConditionFalse, "RenderFailed", 1)
		}},
		{"pending", "Ready", "Image pending", "Image pending", func(d *api.ArprtDevice, _ *api.ArprtScreen) {
			setCondition(d, "Ready", metav1.ConditionFalse, "ImagePending", 1)
			setCondition(d, "RenderReady", metav1.ConditionFalse, "JobPending", 1)
		}},
		{"old-condition", "Ready", "Unknown", "Ready", func(d *api.ArprtDevice, _ *api.ArprtScreen) {
			setCondition(d, "Ready", metav1.ConditionTrue, "ImageReady", 0)
			setCondition(d, "RenderReady", metav1.ConditionFalse, "RenderFailed", 0)
		}},
		{"new-screen-generation", "Ready", "Ready", "Unknown", func(_ *api.ArprtDevice, sc *api.ArprtScreen) { sc.Generation++ }},
		{"replaced-screen", "Ready", "Ready", "Unknown", func(_ *api.ArprtDevice, sc *api.ArprtScreen) { sc.UID = "screen-2" }},
		{"device-edit", "Ready", "Unknown", "Unknown", func(d *api.ArprtDevice, _ *api.ArprtScreen) { d.Generation++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, c, d, sc := fixture(t)
			recordFixture(t, repo, c, d, sc)
			d.Status.LastSeenAt = &metav1.Time{Time: time.Now().UTC()}
			setCondition(d, "Ready", metav1.ConditionTrue, "ImageReady", d.Generation)
			tc.edit(d, sc)
			// Use an updated resource snapshot for screen-generation/UID tests.
			if err := c.Update(t.Context(), sc); err != nil {
				t.Fatal(err)
			}
			row, err := s.device(t.Context(), c, d, time.Now().UTC())
			if err != nil || row.Communication.State != tc.communication || row.Delivery.State != tc.delivery || row.Rendering.State != tc.rendering {
				t.Fatal("incorrect state projection", row, err)
			}
			if row.Image == nil || row.Image.LatestGuaranteed || row.Image.Freshness != "last-recorded" {
				t.Fatal("record claimed guaranteed latestness", row.Image)
			}
		})
	}
}

func TestStaleJobObservationDoesNotAssertRenderingFailure(t *testing.T) {
	for _, source := range []string{"HTTP", "Scheduled", "OnDemand"} {
		t.Run(source, func(t *testing.T) {
			s, repo, c, d, sc := fixture(t)
			if source != "HTTP" {
				sc.Spec.Source = api.ScreenSource{Job: &api.JobSource{ExecutionMode: source, Renderer: api.RendererContainer{Image: "renderer:fixture"}}}
				if err := c.Update(t.Context(), sc); err != nil {
					t.Fatal(err)
				}
			}
			r := recordFixture(t, repo, c, d, sc)
			r.Result, r.IssuedAt = "stale", r.IssuedAt.Add(time.Second)
			r.Rendering = "failed"
			if source == "Scheduled" {
				r.Rendering = "unknown"
			}
			if err := repo.RecordDelivery(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			setCondition(d, "Ready", metav1.ConditionTrue, "StaleImageAvailable", d.Generation)
			row, err := s.device(t.Context(), c, d, time.Now())
			expected := "Unknown"
			if source != "Scheduled" {
				expected = "Render failed"
			}
			if err != nil || row.Delivery.State != "Serving stale image" || row.Rendering.State != expected || len(row.Problems) == 0 {
				t.Fatal("stale output or age asserted an unobserved Job failure", row, err)
			}
		})
	}
}

func TestTelemetryMissingRecordsAndMultipleProblems(t *testing.T) {
	s, _, c, d, _ := fixture(t)
	for _, tc := range []struct {
		percent, voltage *float64
		rssi             *int32
	}{
		{}, {voltage: ptr.To(3.9)}, {percent: ptr.To(0.0)}, {percent: ptr.To(82.0), voltage: ptr.To(4.1), rssi: ptr.To(int32(-50))},
	} {
		d.Status.Battery, d.Status.Network = &api.BatteryStatus{Percent: tc.percent, Voltage: tc.voltage}, &api.NetworkStatus{RSSI: tc.rssi}
		d.Status.LastSeenAt = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		setCondition(d, "Ready", metav1.ConditionFalse, "NoImage", d.Generation)
		setCondition(d, "RenderReady", metav1.ConditionFalse, "JobFailed", d.Generation)
		setCondition(d, "TelemetryValid", metav1.ConditionFalse, "InvalidTelemetry", d.Generation)
		row, err := s.device(t.Context(), c, d, time.Now())
		if err != nil || row.Image != nil || row.RecordStatus != "missing" || row.Communication.State != "Silent" || row.Delivery.State != "Image pending" || row.Rendering.State != "Render failed" || len(row.Problems) < 3 {
			t.Fatal("missing records hid independent problems", row, err)
		}
		if !reflect.DeepEqual(row.Battery, d.Status.Battery) || !reflect.DeepEqual(row.Network, d.Status.Network) {
			t.Fatal("telemetry inferred or dropped", row)
		}
	}
}

func TestPreviewOwnershipCollectionAndSafeProjection(t *testing.T) {
	s, repo, c, d, sc := fixture(t)
	r := recordFixture(t, repo, c, d, sc)
	w := get(t, s, "/api/devices")
	for _, private := range []string{d.Spec.HardwareID, "private-auth", "hardwareID", "authSecretRef", "screenUID", "deviceUID", sc.Spec.Source.HTTP.URL} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private data in projection", private)
		}
	}
	row := page(t, s).Devices[0]
	if got := get(t, s, row.Image.PreviewURL); got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("Content-Type") != "image/png" {
		t.Fatal("preview missing", got.Code)
	}
	if got := get(t, s, "/api/devices/desk/preview/"+strings.Repeat("f", 64)+".png"); got.Code != 404 {
		t.Fatal("arbitrary digest readable", got.Code)
	}
	// Missing bytes leave the observation and its actual generation time intact.
	rootUsed, _, err := repo.Usage(t.Context())
	if err != nil || rootUsed == 0 {
		t.Fatal(err)
	}
	if err := repo.Collect(t.Context(), func(artifact.Unit) bool { return false }, map[string]bool{string(d.UID): true}, time.Now().Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The blob mtime is recent, but the collection's future clock exceeds 24h.
	row = page(t, s).Devices[0]
	if row.Image.PreviewState != "Preview unavailable" || row.Image.GeneratedAt == nil || !row.Image.GeneratedAt.Equal(*r.RenderedAt) {
		t.Fatal("collected preview lost observation", row.Image)
	}
	if got := get(t, s, "/api/devices/desk/preview/"+r.ImageDigest+".png"); got.Code != 404 {
		t.Fatal("collected bytes served", got.Code)
	}
	if err := c.Delete(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	d.ResourceVersion, d.UID = "", "device-recreated"
	if err := c.Create(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	row = page(t, s).Devices[0]
	if row.Image != nil || row.RecordStatus != "missing" || get(t, s, "/api/devices/desk/preview/"+r.ImageDigest+".png").Code != 404 {
		t.Fatal("replacement inherited old observation", row)
	}
}

func TestAssignmentsAndNonAggregatedScreenReports(t *testing.T) {
	s, repo, c, d, sc := fixture(t)
	d.Spec.ScreenRef, d.Spec.PlaylistRef = nil, &api.LocalReference{Name: "cycle"}
	playlist := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "cycle", UID: "playlist-1"}, Spec: api.ArprtPlaylistSpec{FailurePolicy: "Skip", Screens: []api.LocalReference{{Name: "missing"}, {Name: sc.Name}}}}
	if err := c.Create(t.Context(), playlist); err != nil {
		t.Fatal(err)
	}
	// Per-screen status may describe a different pair; it must appear as a
	// separate problem, never be promoted to this device's rendering result.
	meta.SetStatusCondition(&sc.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "OtherPairFailed", Message: "fixture", ObservedGeneration: sc.Generation})
	if err := c.Status().Update(t.Context(), sc); err != nil {
		t.Fatal(err)
	}
	recordFixture(t, repo, c, d, sc)
	setCondition(d, "RenderReady", metav1.ConditionTrue, "ImageReady", d.Generation)
	row, err := s.device(t.Context(), c, d, time.Now())
	if err != nil || row.Assignment.Mode != "playlist" || row.Assignment.FailurePolicy != "Skip" || len(row.Assignment.Screens) != 2 || row.Assignment.Screens[0].Available || !row.Assignment.Screens[1].Available || row.Rendering.State != "Unknown" {
		t.Fatal("incorrect playlist projection", row, err)
	}
	if len(row.Problems) != 1 || row.Problems[0].Scope != "screen" || row.Problems[0].Screen != sc.Name {
		t.Fatal("screen error aggregated or hidden", row.Problems)
	}
	d.Spec.PlaylistRef = nil
	row, err = s.device(t.Context(), c, d, time.Now())
	if err != nil || row.Assignment.Mode != "automatic" || len(row.Assignment.Screens) != 1 {
		t.Fatal("automatic assignment unavailable", row, err)
	}
}

type unavailableStore struct{ artifact.DeliveryReader }

func (s unavailableStore) Delivery(context.Context, string) (artifact.DeliveryRecord, error) {
	return artifact.DeliveryRecord{}, errors.New("private storage path and sensitive error")
}

func TestRoutingLimitsFailuresAndEmptyList(t *testing.T) {
	s, _, c, d, _ := fixture(t)
	for _, path := range []string{"/api/devices?limit=0", "/api/devices?limit=101", "/api/devices?limit=wat", "/api/devices?limit=1&limit=2", "/api/devices?namespace=other", "/api/devices?continue=%zz"} {
		if w := get(t, s, path); w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/devices/desk/preview/../../secrets", "/api/devices/%64esk/preview/" + strings.Repeat("a", 64) + ".png", "/api/devices/desk/preview/" + strings.Repeat("a", 63) + ".png", "/devices/other/desk"} {
		if w := get(t, s, path); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/api/devices", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatal("write method accepted", w.Code)
	}
	s.store = unavailableStore{s.store}
	row := page(t, s).Devices[0]
	if row.RecordStatus != "unavailable" || row.Image != nil {
		t.Fatal("unreadable record treated as missing or current", row)
	}
	if w := get(t, s, "/api/devices"); strings.Contains(w.Body.String(), "sensitive") || strings.Contains(w.Body.String(), "private storage") {
		t.Fatal("storage error leaked")
	}
	if err := c.Delete(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if p := page(t, s); len(p.Devices) != 0 || p.Devices == nil {
		t.Fatal("empty list unavailable", p)
	}
	// All namespace-scoped lookups are independent of unrelated objects.
	d.ResourceVersion, d.Namespace = "", "other"
	if err := c.Create(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if p := page(t, s); len(p.Devices) != 0 {
		t.Fatal("other namespace exposed")
	}
}
