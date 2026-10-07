package byos

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/delivery"
	"github.com/dayflower/arprt/internal/render"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const devicePath = "/trmnl/devices/trmnl/living-room"

func newTestServer(t *testing.T, renderer string) (*Server, client.Client, string) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "living-room", UID: "device-1", Generation: 1}, Spec: api.TrmnlDeviceSpec{HardwareID: "02:00:00:00:00:01", AuthSecretRef: api.LocalReference{Name: "auth"}, ScreenRef: &api.LocalReference{Name: "screen"}}}
	screen := &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "screen", UID: "screen-1", Generation: 1}, Spec: api.TrmnlScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer, Method: "GET"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(d, screen).WithObjects(d, screen, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "trmnl", Name: "auth"}, Data: map[string][]byte{"apiKey": []byte(token)}}).Build()
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Config{Namespace: "trmnl", PublicBaseURL: "https://byos.example.test/trmnl", EnableSetup: true}, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, root
}
func request(s *Server, method, path string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func auth() map[string]string {
	return map[string]string{"ID": "02:00:00:00:00:01", "Access-Token": token, "Model": "og", "FW-Version": "1.8.17", "Width": "800", "Height": "480", "Battery-Voltage": "3.91", "RSSI": "-48"}
}
func decodeDisplay(t *testing.T, w *httptest.ResponseRecorder) DisplayResponse {
	t.Helper()
	var result DisplayResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func loadDevice(t *testing.T, c client.Client) *api.TrmnlDevice {
	t.Helper()
	var d api.TrmnlDevice
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "trmnl", Name: "living-room"}, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
func pngFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSetupDisplayPersistenceOutage(t *testing.T) {
	b := pngFixture(t)
	var outage atomic.Bool
	var calls atomic.Int32
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if outage.Load() {
			w.WriteHeader(500)
			return
		}
		if r.Header.Get("ID") != "" || r.Header.Get("Access-Token") != "" || r.URL.Query().Get("width") != "800" {
			t.Error("renderer contract violation")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	s, c, root := newTestServer(t, renderer.URL)
	setup := request(s, "GET", devicePath+"/api/setup", auth(), nil)
	if setup.Code != 200 || !strings.Contains(setup.Body.String(), `"api_key":"`+token+`"`) {
		t.Fatal(setup.Body.String())
	}
	logo := request(s, "GET", devicePath+"/setup-logo.png", nil, nil)
	if logo.Code != 200 || logo.Header().Get("Content-Length") == "" || render.ValidatePNG(logo.Body.Bytes(), render.Target{Width: 800, Height: 480, Palette: "gray4"}) != nil {
		t.Fatal("invalid setup image")
	}
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 || len(first.Filename) > 31 || !strings.HasPrefix(first.ImageURL, "https://byos.example.test"+devicePath+"/images/") {
		t.Fatalf("display failed: %+v", first)
	}
	d := loadDevice(t, c)
	if d.Status.Display == nil || d.Status.Battery == nil || *d.Status.Battery.Voltage != 3.91 || *d.Status.Network.RSSI != -48 || !meta.IsStatusConditionTrue(d.Status.Conditions, "Ready") {
		t.Fatalf("telemetry/readiness: %+v", d.Status)
	}
	second := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if second != first || calls.Load() != 1 {
		t.Fatal("fresh result not reused")
	}
	imagePath := strings.TrimPrefix(first.ImageURL, "https://byos.example.test")
	image := request(s, "GET", imagePath, auth(), nil)
	if image.Code != 200 || !bytes.Equal(image.Body.Bytes(), b) {
		t.Fatal("issued image not fetchable")
	}
	if request(s, "GET", imagePath, nil, nil).Code != 403 {
		t.Fatal("anonymous image fetch accepted")
	}
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewServer(s.Config, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", imagePath, auth(), nil).Code != 200 {
		t.Fatal("issuance lost on restart")
	}
	var screen api.TrmnlScreen
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: "trmnl", Name: "screen"}, &screen)
	screen.Spec.RefreshInterval = "1s"
	screen.Generation++
	_ = c.Update(context.Background(), &screen)
	outage.Store(true)
	stale := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if stale != first {
		t.Fatal("compatible prior image was not preserved")
	}
	if meta.FindStatusCondition(loadDevice(t, c).Status.Conditions, "Ready").Reason != "StaleImageAvailable" {
		t.Fatal("stale outcome hidden")
	}
	// Replacement screen UIDs must not borrow the old resource's image.
	_ = c.Delete(context.Background(), &screen)
	screen.ResourceVersion = ""
	screen.UID = "screen-2"
	_ = c.Create(context.Background(), &screen)
	pending := request(s, "GET", devicePath+"/api/display", auth(), nil)
	if pending.Code != 200 || pending.Body.String() != `{"status":202}` {
		t.Fatal("initial outage must return HTTP 200 / JSON 202", pending.Body.String())
	}
}

func TestAuthenticationRoutingAndPending(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	before := loadDevice(t, c)
	for name, test := range map[string]struct {
		path    string
		headers map[string]string
		code    int
	}{
		"wrong-token":      {devicePath + "/api/display", map[string]string{"ID": before.Spec.HardwareID, "Access-Token": "wrong"}, 401},
		"mismatch":         {devicePath + "/api/setup", map[string]string{"ID": "02:00:00:00:00:02"}, 403},
		"aliases-conflict": {devicePath + "/api/display", map[string]string{"ID": before.Spec.HardwareID, "Access-Token": token, "ACCESS_TOKEN": token}, 400},
		"namespace":        {strings.Replace(devicePath, "devices/trmnl", "devices/other", 1) + "/api/setup", auth(), 404},
		"unknown":          {strings.Replace(devicePath, "living-room", "missing", 1) + "/api/setup", auth(), 404},
		"escaped":          {devicePath + "%2fapi/display", auth(), 404}, "dot": {devicePath + "/../living-room/api/display", auth(), 404},
		"double": {devicePath + "//api/display", auth(), 404}, "prefix": {strings.TrimPrefix(devicePath, "/trmnl") + "/api/display", auth(), 404},
	} {
		t.Run(name, func(t *testing.T) {
			w := request(s, "GET", test.path, test.headers, nil)
			if w.Code != test.code {
				t.Fatalf("got %d, expected %d", w.Code, test.code)
			}
		})
	}
	if !reflect.DeepEqual(before.Status, loadDevice(t, c).Status) {
		t.Fatal("unauthorized request updated telemetry")
	}
	s.Config.EnableSetup = false
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 403 {
		t.Fatal("setup disable ignored")
	}
	if request(s, "POST", devicePath+"/api/display", auth(), nil).Code != 405 {
		t.Fatal("method accepted")
	}
	if request(s, "POST", devicePath+"/api/log", auth(), []byte(`{"logs":[]}`)).Code != 204 {
		t.Fatal("valid log rejected")
	}
	if request(s, "POST", devicePath+"/api/log", auth(), bytes.Repeat([]byte{'a'}, 65537)).Code != 413 {
		t.Fatal("oversized logs accepted")
	}
	headers := auth()
	headers["Model"] = "unreviewed"
	w := request(s, "GET", devicePath+"/api/display", headers, nil)
	if w.Code != 200 || decodeDisplay(t, w).Status != 202 {
		t.Fatal("unknown target accepted")
	}
	if loadDevice(t, c).Status.Display != nil {
		t.Fatal("invented unknown target")
	}
	// Prefix stripping still generates the configured external URL.
	s.Config.ProxyStripsPrefix = true
	s.Config.EnableSetup = true
	w = request(s, "GET", strings.TrimPrefix(devicePath, "/trmnl")+"/api/setup", auth(), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://byos.example.test/trmnl/devices/") {
		t.Fatal("stripped prefix broken")
	}
}

type executorFunc func(context.Context, render.Request) (render.Output, error)

func (f executorFunc) Execute(ctx context.Context, r render.Request) (render.Output, error) {
	return f(ctx, r)
}
func TestConcurrentReuseAndObsoletePublication(t *testing.T) {
	b := pngFixture(t)
	s, c, _ := newTestServer(t, "http://unused.invalid")
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(ctx context.Context, r render.Request) (render.Output, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return render.Output{}, ctx.Err()
			}
			return render.Output{PNG: b}, nil
		})
	}
	var sc api.TrmnlScreen
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: "trmnl", Name: "screen"}, &sc)
	target := render.Target{Width: 800, Height: 480, Palette: "gray4"}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			m, err := s.GET.Image(context.Background(), &sc, target)
			if err != nil || m.ImageDigest == "" {
				t.Errorf("image: %v", err)
			}
		})
	}
	<-entered
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("renders not coalesced: %d", calls.Load())
	}
	_, _ = s.GET.Image(context.Background(), &sc, render.Target{Width: 799, Height: 480, Palette: "gray4"})
	if calls.Load() != 2 {
		t.Fatal("unequal targets reused")
	}
	// A definition edited while rendering must never become latest-success.
	entered = make(chan struct{})
	release = make(chan struct{})
	s.GET = delivery.NewGET(s.Repository, c)
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) {
			close(entered)
			<-release
			return render.Output{PNG: b}, nil
		})
	}
	sc.Generation++
	sc.Spec.Source.HTTP.URL = "http://new.invalid"
	_ = c.Update(context.Background(), &sc)
	oldScreen := sc.DeepCopy()
	done := make(chan error, 1)
	go func() { _, err := s.GET.Image(context.Background(), oldScreen, target); done <- err }()
	<-entered
	sc.Generation++
	sc.Spec.Source.HTTP.URL = "http://newer.invalid"
	_ = c.Update(context.Background(), &sc)
	close(release)
	if err := <-done; err != artifact.ErrObsolete {
		t.Fatalf("obsolete publication: %v", err)
	}
	m, err := s.Repository.Latest(context.Background(), artifact.Unit{Namespace: sc.Namespace, ScreenUID: string(sc.UID), Target: target})
	if err != nil || m.ScreenGeneration != 1 {
		t.Fatal("obsolete success replaced pointer")
	}
	// A recreated device name never inherits issuance to a prior UID.
	d := loadDevice(t, c)
	_ = s.Repository.Issue(context.Background(), string(d.UID), m.ImageDigest, time.Now())
	_ = c.Delete(context.Background(), d)
	d.ResourceVersion = ""
	d.UID = types.UID("device-replacement")
	_ = c.Create(context.Background(), d)
	if request(s, "GET", devicePath+"/images/"+m.ImageDigest+".png", auth(), nil).Code != 404 {
		t.Fatal("recreated device inherited image access")
	}
}
func TestObservationPolicy(t *testing.T) {
	for _, headers := range []map[string]string{{"model": "other", "fw-version": "1.8.17"}, {"width": "799"}, {"width": "invalid"}, {"model": "other"}, {"fw-version": "new"}} {
		if _, _, _, err := Observe(headers).Target("og", "1.8.17"); err == nil {
			t.Fatal("contradiction accepted", headers)
		}
	}
	o := Observe(map[string]string{"battery-voltage": "NaN", "rssi": "bad", "width": "800"})
	if o.Voltage != nil || o.RSSI != nil || len(o.Invalid) != 2 {
		t.Fatal("invalid telemetry manufactured values")
	}
	if _, _, _, err := (Observation{}).Target("og", "1.8.17"); err != nil {
		t.Fatal("valid known tuple unavailable")
	}
	for _, raw := range []string{"/relative", "https://user:pass@host/x", "https://host/x?query=1", "https://host/x#f", "https://host/a//b", "https://host/a/../b", "https://host/a%2fb"} {
		if _, err := (Config{PublicBaseURL: raw}).Base(); err == nil {
			t.Fatal("invalid public URL accepted", raw)
		}
	}
}

func TestSharedGETIssuanceAndMalformedRefresh(t *testing.T) {
	b := pngFixture(t)
	var bad atomic.Bool
	var calls atomic.Int32
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		if bad.Load() {
			_, _ = w.Write(b[:40])
		} else {
			_, _ = w.Write(b)
		}
	}))
	defer renderer.Close()
	s, c, _ := newTestServer(t, renderer.URL)
	second := loadDevice(t, c).DeepCopy()
	second.Name = "second"
	second.UID = "device-2"
	second.ResourceVersion = ""
	second.Spec.HardwareID = "02:00:00:00:00:02"
	if err := c.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	digest := strings.TrimSuffix(first.ImageURL[strings.LastIndex(first.ImageURL, "/")+1:], ".png")
	headers := auth()
	headers["ID"] = second.Spec.HardwareID
	headers["Battery-Voltage"] = "3.12"
	headers["RSSI"] = "-80"
	path := "/trmnl/devices/trmnl/second"
	if request(s, "GET", path+"/images/"+digest+".png", headers, nil).Code != 404 {
		t.Fatal("unissued shared image accessible")
	}
	shared := decodeDisplay(t, request(s, "GET", path+"/api/display", headers, nil))
	if first.Filename != shared.Filename || calls.Load() != 1 {
		t.Fatal("equal-target devices did not reuse GET output")
	}
	if request(s, "GET", path+"/images/"+digest+".png", headers, nil).Code != 200 {
		t.Fatal("shared image not issued")
	}
	var screen api.TrmnlScreen
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "trmnl", Name: "screen"}, &screen); err != nil {
		t.Fatal(err)
	}
	screen.Generation++
	screen.Spec.RefreshInterval = "1s"
	if err := c.Update(context.Background(), &screen); err != nil {
		t.Fatal(err)
	}
	bad.Store(true)
	failed := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if failed.ImageURL != first.ImageURL || failed.Filename != first.Filename {
		t.Fatal("malformed output replaced valid success")
	}
	if request(s, "GET", strings.TrimPrefix(first.ImageURL, "https://byos.example.test"), auth(), nil).Code != 200 {
		t.Fatal("immutable image lost after malformed refresh")
	}
	for _, mode := range []string{"missing", "post", "job", "unassigned", "playlist"} {
		t.Run(mode, func(t *testing.T) {
			d := loadDevice(t, c)
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: "trmnl", Name: "screen"}, &screen); err != nil {
				t.Fatal(err)
			}
			d.Generation++
			switch mode {
			case "missing":
				d.Spec.ScreenRef = &api.LocalReference{Name: "missing"}
			case "post":
				screen.Spec.RefreshInterval = ""
				screen.Spec.Source.HTTP.Method = "POST"
				screen.Generation++
				_ = c.Update(context.Background(), &screen)
				d.Spec.ScreenRef = &api.LocalReference{Name: "screen"}
			case "job":
				screen.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "example:v1"}}}
				screen.Generation++
				_ = c.Update(context.Background(), &screen)
			case "unassigned":
				d.Spec.ScreenRef = nil
			case "playlist":
				d.Spec.ScreenRef = nil
				d.Spec.PlaylistRef = &api.LocalReference{Name: "future"}
			}
			if err := c.Update(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			w := request(s, "GET", devicePath+"/api/display", auth(), nil)
			if w.Code != 200 || decodeDisplay(t, w).Status != 202 || calls.Load() != before {
				t.Fatal("unsupported assignment rendered or supplied image")
			}
			if meta.IsStatusConditionTrue(loadDevice(t, c).Status.Conditions, "AssignmentReady") {
				t.Fatal("unusable assignment reported ready")
			}
		})
	}
}

func TestCredentialRotation(t *testing.T) {
	b := pngFixture(t)
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	s, c, _ := newTestServer(t, renderer.URL)
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	path := strings.TrimPrefix(first.ImageURL, "https://byos.example.test")
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "trmnl", Name: "auth"}, &secret); err != nil {
		t.Fatal(err)
	}
	rotated := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	secret.Data["apiKey"] = []byte(rotated)
	if err := c.Update(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
	before := loadDevice(t, c).Status
	if request(s, "GET", path, auth(), nil).Code != 401 || request(s, "GET", devicePath+"/api/display", auth(), nil).Code != 401 {
		t.Fatal("old token remained valid after rotation")
	}
	if !reflect.DeepEqual(before, loadDevice(t, c).Status) {
		t.Fatal("revoked token updated telemetry")
	}
	headers := auth()
	headers["Access-Token"] = rotated
	if request(s, "GET", path, headers, nil).Code != 200 {
		t.Fatal("rotated token did not authorize already-issued image")
	}
	secret.Data["apiKey"] = []byte(token + "\n")
	if err := c.Update(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", devicePath+"/api/setup", auth(), nil).Code != 401 {
		t.Fatal("non-header-safe key accepted")
	}
}
