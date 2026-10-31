package byos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/dashboard"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestDeliveryRecordsAcrossSourcesAndAssignments(t *testing.T) {
	png := pngFixture(t)
	for _, source := range []string{"GET", "POST", "Scheduled", "OnDemand"} {
		for _, mode := range []string{"single", "Hold", "Skip", "automatic"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				var failed atomic.Bool
				var calls atomic.Uint64
				renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					if failed.Load() {
						w.WriteHeader(500)
						return
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(png)
				}))
				defer renderer.Close()
				s, c, root := newTestServer(t, renderer.URL)
				var sc api.ArprtScreen
				if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
					t.Fatal(err)
				}
				sc.Spec.Overlay = &api.ScreenOverlay{Enabled: true, CycleCorners: true}
				if source == "GET" || source == "POST" {
					sc.Spec.Source.HTTP.Method = source
				} else {
					sc.Spec.Source = api.ScreenSource{Job: &api.JobSource{ExecutionMode: source, Renderer: api.RendererContainer{Image: "renderer:fixture"}}}
				}
				if err := c.Update(t.Context(), &sc); err != nil {
					t.Fatal(err)
				}
				d := loadDevice(t, c)
				if mode != "single" {
					d.Spec.ScreenRef = nil
					if mode != "automatic" {
						p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "cycle", UID: "playlist-1", Generation: 1}, Spec: api.ArprtPlaylistSpec{FailurePolicy: mode, Screens: []api.LocalReference{{Name: "screen"}}}}
						if err := c.Create(t.Context(), p); err != nil {
							t.Fatal(err)
						}
						d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
					}
					d.Generation++
					if err := c.Update(t.Context(), d); err != nil {
						t.Fatal(err)
					}
				}
				if source == "Scheduled" {
					d.Status.Device = &api.DeviceInfoStatus{Model: "og"}
					rev, _ := screen.InputRevision(sc.Spec)
					target := devicestate.Resolved(d)
					if _, err := s.Repository.Publish(t.Context(), artifact.Metadata{Unit: artifact.Unit{Namespace: "arprt", ScreenUID: "screen-1", DeviceUID: string(d.UID), Target: target.Target, Output: target.Output},
						InputRevision: rev, ScreenGeneration: sc.Generation, Sequence: 1, AttemptID: "scheduled", RenderedAt: time.Now().Add(-time.Minute)}, png); err != nil {
						t.Fatal(err)
					}
				}
				s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
					sequence := calls.Add(1)
					if failed.Load() {
						return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
					}
					return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: sc.Generation, InputRevision: req.Revision, Sequence: sequence, AttemptID: fmt.Sprint(sequence), RenderedAt: time.Now().UTC()}, png)
				})
				response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
				r, err := s.Repository.Delivery(t.Context(), string(d.UID))
				if err != nil || response.Status != 0 || r.ScreenName != "screen" || r.RenderedAt == nil || r.Result != "ready" || !strings.Contains(response.ImageURL, r.ImageDigest) {
					t.Fatal("delivery not recorded", response, r, err)
				}
				if r.IssuedAt.Before(*r.RenderedAt) || r.Cycle == nil != (mode == "single") {
					t.Fatal("incorrect provenance or cycle", r)
				}
				// Preview is the issued composite, rather than the base renderer PNG.
				bytes, err := s.Repository.ReadDeliveryPreview(t.Context(), string(d.UID), r.ImageDigest)
				if err != nil || string(bytes) == string(png) {
					t.Fatal("overlay not recorded", err)
				}
				repo, err := artifact.NewPVC(root)
				if err != nil {
					t.Fatal(err)
				}
				restarted, err := NewServer(s.Config, c, c, repo)
				if err != nil {
					t.Fatal(err)
				}
				if mode != "single" {
					before := calls.Load()
					if got := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil)); got != response {
						t.Fatal("restart changed replay", got, response)
					}
					replayed, err := repo.Delivery(t.Context(), string(d.UID))
					if err != nil || replayed.RenderedAt == nil || !replayed.RenderedAt.Equal(*r.RenderedAt) || replayed.Rendering != r.Rendering || calls.Load() != before || !artifact.EqualCycle(replayed.Cycle, r.Cycle) {
						t.Fatal("replay lost original provenance", replayed, err)
					}
					expireTurn(t, c)
				}
				// A definition edit forces refresh; failed output must retain the
				// original generation time even when a new overlay is composed.
				failed.Store(true)
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(&sc), &sc); err != nil {
					t.Fatal(err)
				}
				sc.Spec.ImageProcessing = &api.ImageProcessing{Dithering: "none"}
				sc.Generation++
				if err := c.Update(t.Context(), &sc); err != nil {
					t.Fatal(err)
				}
				fallback := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
				last, err := repo.Delivery(t.Context(), string(d.UID))
				if mode == "Skip" {
					if fallback.Status != 202 || err != nil || last.Result != "ready" || !artifact.EqualCycle(last.Cycle, r.Cycle) {
						t.Fatal("failed Skip changed recorded success", fallback, last, err)
					}
				} else if fallback.Status != 0 || err != nil || last.Result != "stale" || last.RenderedAt == nil || !last.RenderedAt.Equal(*r.RenderedAt) {
					t.Fatal("fallback lost base generation time", fallback, last, err)
				}
				if mode != "Skip" {
					expected := "failed"
					if source == "Scheduled" {
						expected = "unknown"
					}
					if last.Rendering != expected {
						t.Fatal("incorrect rendering evidence", source, last.Rendering)
					}
				}
			})
		}
	}
}

type failedDeliveryStore struct{ artifact.Repository }

func TestIdenticalBytesFromNewRenderKeepDistinctGenerationTimes(t *testing.T) {
	s, c := demandServer(t)
	png := pngFixture(t)
	var sequence uint64
	baseTime := time.Now().UTC().Add(-time.Minute)
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		sequence++
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, Sequence: sequence, AttemptID: fmt.Sprint(sequence), RenderedAt: baseTime.Add(time.Duration(sequence) * time.Second)}, png)
	})
	var prior artifact.DeliveryRecord
	for i := 0; i < 2; i++ {
		if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 {
			t.Fatal(response)
		}
		r, err := s.Repository.Delivery(t.Context(), string(loadDevice(t, c).UID))
		if err != nil || r.RenderedAt == nil || !r.RenderedAt.Equal(baseTime.Add(time.Duration(i+1)*time.Second)) {
			t.Fatal("new success reused digest provenance", r, err)
		}
		if i == 1 && (prior.ImageDigest != r.ImageDigest || !r.RenderedAt.After(*prior.RenderedAt)) {
			t.Fatal("identical bytes lost distinct success times")
		}
		prior = r
	}
}

func (s failedDeliveryStore) RecordDelivery(context.Context, artifact.DeliveryRecord) error {
	return errors.New("injected record failure")
}

func TestDeliveryObservationFailureAndLegacyReplay(t *testing.T) {
	s, c, root := newTestServer(t, "http://unused.invalid")
	png := pngFixture(t)
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) { return render.Output{PNG: png}, nil })
	}
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	prior, err := s.Repository.Delivery(t.Context(), string(d.UID))
	if err != nil || first.Status != 0 {
		t.Fatal(prior, err, first)
	}
	expireTurn(t, c)
	original := s.Repository
	s.Repository = failedDeliveryStore{original}
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 {
		t.Fatal("record failure prevented delivery", response)
	}
	if r, err := original.Delivery(t.Context(), string(d.UID)); err != nil || !r.IssuedAt.Equal(prior.IssuedAt) {
		t.Fatal("record failure changed previous observation", r, err)
	}
	current := loadDevice(t, c)
	if artifact.EqualCycle(current.Status.ScreenCycle, prior.Cycle) {
		t.Fatal("record failure prevented turn commitment")
	}
	dash, err := dashboard.NewServer(dashboard.Config{Namespace: "arprt"}, c, original)
	if err != nil {
		t.Fatal(err)
	}
	page := getDashboardPage(t, dash)
	if len(page.Devices) != 1 || page.Devices[0].Image.LatestGuaranteed || page.Devices[0].Image.Freshness != "last-recorded" || page.Devices[0].Rendering.State != "Unknown" {
		t.Fatal("lagging record asserted as current", page)
	}
	// No provenance may be inferred from equal image bytes or a current latest
	// pointer when the older committed turn has no corresponding observation.
	entries, err := os.ReadDir(filepath.Join(root, "deliveries"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(root, "deliveries", entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	s.Repository = original
	replayed := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	r, err := original.Delivery(t.Context(), string(d.UID))
	if replayed.Status != 0 || err != nil || r.RenderedAt != nil || r.Result != "unknown" || r.Rendering != "unknown" {
		t.Fatal("legacy replay invented generation time", r, err)
	}
}

func getDashboardPage(t *testing.T, handler http.Handler) dashboard.DevicePage {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/devices", nil))
	var page dashboard.DevicePage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return page
}

func TestDashboardReadsDoNotChangeDeviceOrStorage(t *testing.T) {
	s, c, root := newTestServer(t, "http://unused.invalid")
	png := pngFixture(t)
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) { return render.Output{PNG: png}, nil })
	}
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 {
		t.Fatal(got)
	}
	dash, err := dashboard.NewServer(dashboard.Config{Namespace: "arprt"}, c, s.Repository)
	if err != nil {
		t.Fatal(err)
	}
	before := loadDevice(t, c)
	snapshot := func() map[string]string {
		t.Helper()
		files := map[string]string{}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			bytes, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			files[path] = string(bytes) + info.ModTime().String()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	files := snapshot()
	for i := 0; i < 3; i++ {
		page := getDashboardPage(t, dash)
		row := page.Devices[0]
		if row.Image == nil || row.Image.GeneratedAt == nil || row.Battery.Percent != nil || *row.Battery.Voltage != 3.91 || *row.Network.RSSI != -48 {
			t.Fatal("projection lost recorded data", row)
		}
		w := httptest.NewRecorder()
		dash.ServeHTTP(w, httptest.NewRequest("GET", row.Image.PreviewURL, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if !reflect.DeepEqual(before, loadDevice(t, c)) || !reflect.DeepEqual(files, snapshot()) {
		t.Fatal("dashboard modified status, issuance, cache or observation storage")
	}
}
