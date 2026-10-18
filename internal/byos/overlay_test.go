package byos

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/overlay"
	"github.com/dayflower/arprt/internal/render"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func enableOverlay(t *testing.T, c client.Client, cycle bool, items ...api.OverlayItem) {
	t.Helper()
	var screen api.ArprtScreen
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &screen); err != nil {
		t.Fatal(err)
	}
	screen.Spec.Overlay = &api.ScreenOverlay{Enabled: true, CycleCorners: cycle}
	if items != nil {
		screen.Spec.Overlay.Items = &items
	}
	if err := c.Update(context.Background(), &screen); err != nil {
		t.Fatal(err)
	}
}
func baseImage(t *testing.T, s *Server, c client.Client, private bool) artifact.Metadata {
	t.Helper()
	d := loadDevice(t, c)
	u := artifact.Unit{Namespace: d.Namespace, ScreenUID: "screen-1", Target: devicestate.Target(d), Output: render.CurrentTarget(devicestate.Target(d)).Output}
	if private {
		u.DeviceUID = string(d.UID)
	}
	m, err := s.Repository.Latest(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func imageBytes(t *testing.T, s *Server, r DisplayResponse) []byte {
	t.Helper()
	w := request(s, "GET", strings.TrimPrefix(r.ImageURL, "https://byos.example.test"), auth(), nil)
	if w.Code != 200 {
		t.Fatal("issued image unavailable", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}
func fixtureGET(s *Server, b []byte, calls *int) {
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) {
			*calls++
			return render.Output{PNG: b}, nil
		})
	}
}

func TestE1002VoltageBatteryOverlay(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, false, api.OverlayItem("battery"))
	b, err := os.ReadFile("../../testdata/png/target-spectra6.png")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixtureGET(s, b, &calls)
	headers := auth()
	headers["Model"] = "reterminal_e1002"
	headers["Battery-Voltage"] = "4.07"
	response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if response.Status != 0 {
		t.Fatal(response)
	}
	d := loadDevice(t, c)
	target := devicestate.Resolved(d)
	level := 2
	drawing := overlay.Drawing{Version: overlay.AppearanceVersion, Battery: &level, Corner: overlay.TopRight, Scale: 1}
	expected, err := overlay.ComposeResolved(t.Context(), b, target, drawing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(imageBytes(t, s, response), expected) {
		t.Fatal("E1002 voltage did not select the high battery icon")
	}
	if d.Status.Battery == nil || d.Status.Battery.Percent != nil {
		t.Fatal("voltage fabricated a measured percentage")
	}
	// A previous voltage remains known state but cannot fill missing current
	// observations when deciding whether the overlay item is visible.
	delete(headers, "Battery-Voltage")
	missing := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if missing.Status != 0 || !bytes.Equal(imageBytes(t, s, missing), b) || calls != 1 {
		t.Fatal("stored voltage entered the overlay or refreshed the base", missing, calls)
	}
}

func TestOverlayCurrentMeasurementsBaseSeparationAndSharedGET(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, false)
	b := pngFixture(t)
	calls := 0
	fixtureGET(s, b, &calls)
	headers := auth()
	headers["Percent-Charged"] = "20.5"
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if first.Status != 0 {
		t.Fatal(first)
	}
	base := baseImage(t, s, c, false)
	drawing, err := overlay.NormalizeResolved(base.Unit.ResolvedTarget(), overlay.Settings{Items: []string{"timestamp", "battery", "wifi"}}, base.RenderedAt, time.UTC, overlay.Telemetry{Percent: loadDevice(t, c).Status.Battery.Percent, RSSI: loadDevice(t, c).Status.Network.RSSI}, overlay.TopRight)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := overlay.ComposeResolved(context.Background(), b, base.Unit.ResolvedTarget(), drawing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(imageBytes(t, s, first), expected) {
		t.Fatal("fraction/current measurements not drawn")
	}
	if first.Filename == base.Filename || first.ImageURL == "" || calls != 1 {
		t.Fatal("composite not issued", first, calls)
	}
	// Raw differences in one icon level reuse bytes and filename.
	headers["Percent-Charged"] = "59.9"
	headers["RSSI"] = "-50"
	same := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if same.Filename != first.Filename {
		t.Fatal("unnormalized cache inputs")
	}
	// Old status remains valid renderer input but cannot enter the new overlay.
	delete(headers, "Percent-Charged")
	delete(headers, "Battery-Voltage")
	delete(headers, "RSSI")
	missing := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	drawing.Battery = nil
	drawing.WiFi = nil
	expected, err = overlay.ComposeResolved(context.Background(), b, base.Unit.ResolvedTarget(), drawing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(imageBytes(t, s, missing), expected) {
		t.Fatal("old telemetry entered overlay")
	}
	if got := loadDevice(t, c); got.Status.Battery.Percent == nil || *got.Status.Battery.Percent != 59.9 {
		t.Fatal("known percentage was cleared")
	}
	after := baseImage(t, s, c, false)
	if !reflect.DeepEqual(base, after) || calls != 1 {
		t.Fatal("composites refreshed base", after)
	}
	// A second UID shares the base while receiving its own telemetry/composite.
	second := loadDevice(t, c).DeepCopy()
	second.Name = "second"
	second.UID = "second-uid"
	second.ResourceVersion = ""
	second.Spec.HardwareID = "02:00:00:00:00:02"
	second.Status = api.ArprtDeviceStatus{}
	if err := c.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondHeaders := auth()
	secondHeaders["ID"] = second.Spec.HardwareID
	secondHeaders["Percent-Charged"] = "0"
	secondHeaders["RSSI"] = "-99"
	response := decodeDisplay(t, request(s, "GET", strings.Replace(devicePath, "living-room", "second", 1)+"/api/display", secondHeaders, nil))
	if response.Status != 0 || response.Filename == first.Filename || calls != 1 {
		t.Fatal("GET sharing or per-device drawing failed", response, calls)
	}
	digest := strings.TrimSuffix(response.ImageURL[strings.LastIndex(response.ImageURL, "/")+1:], ".png")
	if allowed, err := s.Repository.Authorized(context.Background(), "device-1", digest); err != nil || allowed {
		t.Fatal("composite issuance leaked to another UID")
	}
}
func TestOverlayCyclingReplayRestartAndNoDrawing(t *testing.T) {
	s, c, root := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, true)
	b := pngFixture(t)
	calls := 0
	fixtureGET(s, b, &calls)
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	if err := c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 {
		t.Fatal(first)
	}
	turn := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
	firstBytes := imageBytes(t, s, first)
	r, err := s.Repository.Corner(context.Background(), "device-1")
	if err != nil || r.Current != overlay.BottomRight || r.Revision != 1 || r.Pending != nil {
		t.Fatal(r, err)
	}
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Config
	cfg.OverlayTimeZone = "Asia/Tokyo"
	restarted, err := NewServer(cfg, c, c, repo)
	if err != nil {
		t.Fatal(err)
	}
	newer := auth()
	newer["Percent-Charged"] = "0"
	newer["RSSI"] = "-100"
	replay := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", newer, nil))
	if replay != first || !bytes.Equal(imageBytes(t, restarted, replay), firstBytes) || !artifact.EqualCycle(turn, loadDevice(t, c).Status.ScreenCycle) {
		t.Fatal("replay changed image or time")
	}
	r, err = repo.Corner(context.Background(), "device-1")
	if err != nil || r.Revision != 1 {
		t.Fatal("replay advanced corner", r, err)
	}
	s = restarted
	fixtureGET(s, b, &calls)
	for _, next := range []overlay.Corner{overlay.BottomLeft, overlay.TopLeft, overlay.TopRight} {
		expireTurn(t, c)
		response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
		if response.Status != 0 {
			t.Fatal(response)
		}
		r, err = repo.Corner(context.Background(), "device-1")
		if err != nil || r.Current != next {
			t.Fatal("wrong cycle order", r, err)
		}
	}
	before := r.Revision
	enableOverlay(t, c, false, "timestamp")
	expireTurn(t, c)
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 {
		t.Fatal(response)
	}
	enableOverlay(t, c, true, []api.OverlayItem{}...)
	expireTurn(t, c)
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 || !bytes.Equal(imageBytes(t, s, response), b) {
		t.Fatal("empty overlay didn't deliver base", response)
	}
	r, err = repo.Corner(context.Background(), "device-1")
	if err != nil || r.Revision != before {
		t.Fatal("fixed/empty overlay advanced")
	}
}

type derivedErrorRepository struct {
	artifact.Repository
	after func()
	calls int
}

func (r *derivedErrorRepository) StoreDerived(ctx context.Context, m artifact.Metadata, d overlay.Drawing, b []byte) (artifact.Metadata, func(), error) {
	r.calls++
	if r.after != nil {
		r.after()
	}
	return artifact.Metadata{}, nil, errors.New("injected derived failure")
}
func TestOverlayPersistenceFallbackAndRequiredIssuanceFailure(t *testing.T) {
	for _, failure := range []string{"persistence", "layout", "issue", "issue-committed"} {
		t.Run(failure, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			enableOverlay(t, c, true)
			b := pngFixture(t)
			calls := 0
			fixtureGET(s, b, &calls)
			original := s.Repository
			if failure == "persistence" {
				s.Repository = &derivedErrorRepository{Repository: original}
			}
			if failure == "layout" {
				var screen api.ArprtScreen
				c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &screen)
				scale := int32(100)
				screen.Spec.Overlay.Scale = &scale
				if err := c.Update(context.Background(), &screen); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(failure, "issue") {
				s.Repository = &issueErrorRepository{Repository: original, commit: failure == "issue-committed"}
			}
			response := request(s, "GET", devicePath+"/api/display", auth(), nil)
			if strings.HasPrefix(failure, "issue") {
				if response.Code != 503 {
					t.Fatal("issue failure became fallback success", response.Code)
				}
			} else {
				display := decodeDisplay(t, response)
				if display.Status != 0 || !bytes.Equal(imageBytes(t, s, display), b) {
					t.Fatal("failed overlay did not issue base", display)
				}
			}
			r, err := original.Corner(context.Background(), "device-1")
			if err != nil || r.Revision != 0 || r.Current != overlay.TopRight {
				t.Fatal("failed overlay advanced", r, err)
			}
		})
	}
}

type cornerErrorRepository struct {
	artifact.Repository
	commit bool
	fail   bool
}

func (r *cornerErrorRepository) ResolveCorner(ctx context.Context, record artifact.CornerRecord, advance bool) error {
	if r.fail && advance {
		if r.commit {
			if err := r.Repository.ResolveCorner(ctx, record, advance); err != nil {
				return err
			}
		}
		return errors.New("injected finalization failure")
	}
	return r.Repository.ResolveCorner(ctx, record, advance)
}

type committedCycleClient struct {
	client.Client
	fail bool
}

func (c *committedCycleClient) Status() client.SubResourceWriter {
	return committedCycleWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type committedCycleWriter struct {
	client.SubResourceWriter
	parent *committedCycleClient
}

func (w committedCycleWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if err := w.SubResourceWriter.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if d, ok := obj.(*api.ArprtDevice); ok && w.parent.fail && d.Status.ScreenCycle != nil {
		return errors.New("injected failure after status commitment")
	}
	return nil
}
func TestOverlayCornerRecoveryAcrossDeliveryFailureWindows(t *testing.T) {
	for _, failure := range []string{"issue", "issue-committed", "status", "status-committed", "finalize", "finalize-committed"} {
		t.Run(failure, func(t *testing.T) {
			s, c, root := newTestServer(t, "http://unused.invalid")
			enableOverlay(t, c, true)
			b := pngFixture(t)
			calls := 0
			fixtureGET(s, b, &calls)
			d := loadDevice(t, c)
			d.Spec.ScreenRef = nil
			if err := c.Update(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			original := s.Repository
			switch failure {
			case "issue", "issue-committed":
				s.Repository = &issueErrorRepository{Repository: original, commit: failure == "issue-committed"}
			case "status":
				s.Client = &statusErrorClient{Client: c, reporting: true}
			case "status-committed":
				s.Client = &committedCycleClient{Client: c, fail: true}
			case "finalize", "finalize-committed":
				s.Repository = &cornerErrorRepository{Repository: original, fail: true, commit: failure == "finalize-committed"}
			}
			// Existing statusErrorClient allows observations but rejects Ready reporting;
			// use a dedicated cycle writer for failures before turn commitment.
			if failure == "status" {
				s.Client = &overlayCycleErrorClient{Client: c}
			}
			w := request(s, "GET", devicePath+"/api/display", auth(), nil)
			if w.Code != 503 {
				t.Fatal("required write failure acknowledged", w.Code, w.Body.String())
			}
			committed := failure == "status-committed" || strings.HasPrefix(failure, "finalize")
			turn := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
			if (turn != nil) != committed {
				t.Fatal("wrong status failure window")
			}
			repo, err := artifact.NewPVC(root)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewServer(s.Config, c, c, repo)
			if err != nil {
				t.Fatal(err)
			}
			fixtureGET(restarted, b, &calls)
			recovered := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil))
			if recovered.Status != 0 {
				t.Fatal("recovery failed", recovered)
			}
			record, err := repo.Corner(context.Background(), "device-1")
			if err != nil || record.Current != overlay.BottomRight || record.Revision != 1 || record.Pending != nil {
				t.Fatal("corner advanced twice or was lost", record, err)
			}
			if committed && (!artifact.EqualCycle(turn, loadDevice(t, c).Status.ScreenCycle) || recovered.Filename != turn.Filename) {
				t.Fatal("committed turn was redrawn")
			}
		})
	}
}

type overlayCycleErrorClient struct{ client.Client }

func (c *overlayCycleErrorClient) Status() client.SubResourceWriter {
	return overlayCycleErrorWriter{SubResourceWriter: c.Client.Status()}
}

type overlayCycleErrorWriter struct{ client.SubResourceWriter }

func (w overlayCycleErrorWriter) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
	if d, ok := obj.(*api.ArprtDevice); ok && d.Status.ScreenCycle != nil {
		return errors.New("injected turn failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, p, opts...)
}

func TestConcurrentOverlayRequestsCommitOnePlaylistTurn(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, true)
	calls := 0
	fixtureGET(s, pngFixture(t), &calls)
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	if err := c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	responses := make([]*httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(i int) { defer wg.Done(); responses[i] = request(s, "GET", devicePath+"/api/display", auth(), nil) }(i)
	}
	wg.Wait()
	var first DisplayResponse
	for i, w := range responses {
		got := decodeDisplay(t, w)
		if i == 0 {
			first = got
		}
		if got.Status != 0 || got != first {
			t.Fatal("concurrent requests didn't replay", got)
		}
	}
	r, err := s.Repository.Corner(context.Background(), "device-1")
	if err != nil || r.Revision != 1 || calls != 1 {
		t.Fatal("multiple commits", r, calls, err)
	}
}
func TestOverlayJobUsesCurrentDisplayTelemetryAndOriginalTimestamp(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, false, "battery", "wifi")
	var screen api.ArprtScreen
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &screen); err != nil {
		t.Fatal(err)
	}
	screen.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:v1"}}}
	if err := c.Update(context.Background(), &screen); err != nil {
		t.Fatal(err)
	}
	// Observe target without a Job image, then publish an older immutable snapshot.
	request(s, "GET", devicePath+"/api/display", auth(), nil)
	d := loadDevice(t, c)
	m := artifact.Metadata{Unit: artifact.Unit{Namespace: "arprt", ScreenUID: string(screen.UID), DeviceUID: string(d.UID), Target: devicestate.Target(d), Output: render.CurrentTarget(devicestate.Target(d)).Output}, InputRevision: strings.Repeat("a", 64), Sequence: 1, AttemptID: "job-attempt", ContextDigest: strings.Repeat("b", 64), RenderedAt: time.Now().UTC().Add(-48 * time.Hour)}
	b, err := os.ReadFile("../../testdata/png/target-indexed-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	b = normalizeFixture(t, b)
	published, err := s.Repository.Publish(context.Background(), m, b)
	if err != nil {
		t.Fatal(err)
	}
	headers := auth()
	headers["Percent-Charged"] = "0"
	headers["RSSI"] = "-99"
	got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if got.Status != 0 {
		t.Fatal(got)
	}
	percent, rssi := 0.0, int32(-99)
	drawing, _ := overlay.NormalizeResolved(m.Unit.ResolvedTarget(), overlay.Settings{Items: []string{"battery", "wifi"}}, m.RenderedAt, time.UTC, overlay.Telemetry{Percent: &percent, RSSI: &rssi}, overlay.TopRight)
	expected, err := overlay.ComposeResolved(context.Background(), b, m.Unit.ResolvedTarget(), drawing)
	if err != nil || !bytes.Equal(imageBytes(t, s, got), expected) {
		t.Fatal("Job snapshot telemetry used for display", err)
	}
	headers["USB-Connected"] = "true"
	usb := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if usb.Filename != got.Filename {
		t.Fatal("USB changed current Job delivery battery level")
	}
	headers["Battery-Charging"] = "1"
	charging := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	drawing.Battery = nil
	drawing.Charging = true
	expected, err = overlay.ComposeResolved(context.Background(), b, m.Unit.ResolvedTarget(), drawing)
	if err != nil || !bytes.Equal(imageBytes(t, s, charging), expected) {
		t.Fatal("current Job delivery charging flag lost", err)
	}
	latest, err := s.Repository.Latest(context.Background(), m.Unit)
	if err != nil || !reflect.DeepEqual(latest, published) {
		t.Fatal("Job freshness changed", err)
	}
}

func TestFractionalPOSTContextAndStaleOverlay(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, true)
	screen := postScreen(t, c)
	b := pngFixture(t)
	calls := 0
	var snapshot render.Context
	fail := false
	s.POST.NewExecutor = func(string) render.Executor {
		return executorFunc(func(_ context.Context, r render.Request) (render.Output, error) {
			calls++
			snapshot = *r.Context
			if fail {
				return render.Output{}, errors.New("renderer unavailable")
			}
			return render.Output{PNG: b}, nil
		})
	}
	headers := auth()
	headers["Percent-Charged"] = "20.5"
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if first.Status != 0 || snapshot.Device.Battery.Percent == nil || *snapshot.Device.Battery.Percent != 20.5 {
		t.Fatal("current percent absent from POST context")
	}
	d := loadDevice(t, c)
	unit := artifact.Unit{Namespace: "arprt", ScreenUID: string(screen.UID), DeviceUID: string(d.UID), Target: devicestate.Target(d), Output: render.CurrentTarget(devicestate.Target(d)).Output}
	original, err := s.Repository.Latest(context.Background(), unit)
	if err != nil {
		t.Fatal(err)
	}
	fail = true
	headers["Percent-Charged"] = "0"
	stale := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if stale.Status != 0 || stale.Filename == first.Filename || calls != 2 {
		t.Fatal("stale base was not recomposited", stale)
	}
	latest, err := s.Repository.Latest(context.Background(), unit)
	if err != nil || !reflect.DeepEqual(latest, original) {
		t.Fatal("overlay refreshed failed POST success")
	}
	record, err := s.Repository.Corner(context.Background(), string(d.UID))
	if err != nil || record.Revision != 2 {
		t.Fatal("single screen deliveries didn't advance", record, err)
	}
	conditions := loadDevice(t, c).Status.Conditions
	for _, condition := range conditions {
		if condition.Type == "RenderReady" && condition.Status != "False" {
			t.Fatal("overlay hid render failure")
		}
	}
}

type derivedGateRepository struct {
	artifact.Repository
	after func()
}

func (r *derivedGateRepository) StoreDerived(ctx context.Context, m artifact.Metadata, d overlay.Drawing, b []byte) (artifact.Metadata, func(), error) {
	out, unpin, err := r.Repository.StoreDerived(ctx, m, d, b)
	if err == nil {
		r.after()
	}
	return out, unpin, err
}
func TestOverlayFinalGateRunsAfterPersistenceAndFallback(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "composite", true: "fallback"}[fallback], func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			enableOverlay(t, c, true)
			calls := 0
			fixtureGET(s, pngFixture(t), &calls)
			original := s.Repository
			change := func() {
				var screen api.ArprtScreen
				if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &screen); err != nil {
					t.Fatal(err)
				}
				screen.Spec.Source.HTTP.URL = "http://changed.invalid"
				screen.Generation++
				if err := c.Update(context.Background(), &screen); err != nil {
					t.Fatal(err)
				}
			}
			if fallback {
				s.Repository = &derivedErrorRepository{Repository: original, after: change}
			} else {
				s.Repository = &derivedGateRepository{Repository: original, after: change}
			}
			w := request(s, "GET", devicePath+"/api/display", auth(), nil)
			response := decodeDisplay(t, w)
			if w.Code != 200 || response.Status != 202 || response.ImageURL != "" {
				t.Fatal("final gate bypassed", w.Code, response)
			}
			r, err := original.Corner(context.Background(), "device-1")
			if err != nil || r.Revision != 0 {
				t.Fatal("failed gate advanced corner", r, err)
			}
			// A pending proposal is canceled before the next delivery reads its position.
			d := loadDevice(t, c)
			if _, err := s.recoverCorner(context.Background(), d); err != nil {
				t.Fatal("failed gate left unrecoverable pending", err)
			}
		})
	}
}
func TestCornerRecoveryRejectsDifferentTurnWithSameDigest(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	enableOverlay(t, c, true)
	calls := 0
	fixtureGET(s, pngFixture(t), &calls)
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	if err := c.Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	s.Repository = &cornerErrorRepository{Repository: s.Repository, fail: true}
	if w := request(s, "GET", devicePath+"/api/display", auth(), nil); w.Code != 503 {
		t.Fatal("failure not injected")
	}
	d = loadDevice(t, c)
	d.Status.ScreenCycle.ServedAt.Time = d.Status.ScreenCycle.ServedAt.Time.Add(time.Microsecond)
	if err := c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", devicePath+"/api/display", auth(), nil); w.Code != 503 {
		t.Fatal("digest-only pending recovery accepted unrelated turn")
	}
}
