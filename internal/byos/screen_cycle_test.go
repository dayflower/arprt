package byos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func expireTurn(t *testing.T, c client.Client) {
	t.Helper()
	d := loadDevice(t, c)
	if d.Status.ScreenCycle == nil {
		t.Fatal("missing durable screen cycle")
	}
	d.Status.ScreenCycle.ServedAt = metav1.NewMicroTime(time.Now().Add(-time.Hour))
	if err := c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

func TestMixedScreenCycleRestartRetriesAndPending(t *testing.T) {
	for _, mode := range []string{"playlist", "discovery"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			b := pngFixture(t)
			var methods []string
			rendered := make(chan time.Time, 8)
			renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				rendered <- time.Now().UTC()
				if r.Method == "POST" {
					var input render.Context
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Device.Battery == nil || *input.Device.Battery.Voltage != 3.91 {
						t.Error("POST lost request telemetry", err)
					}
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(b)
			}))
			defer renderer.Close()
			s, c, root := newTestServer(t, renderer.URL)
			post := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "post", UID: "post-uid", Generation: 1}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL, Method: "POST"}}}}
			job := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "job", UID: "job-uid", Generation: 1}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "unused"}}}}}
			for _, obj := range []client.Object{post, job} {
				if err := c.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			d := loadDevice(t, c)
			d.Spec.ScreenRef = nil
			order := []string{"screen-1", "post-uid", "job-uid"}
			if mode == "playlist" {
				p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "cycle", UID: "playlist-uid"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "missing"}, {Name: "screen"}, {Name: "post"}, {Name: "job"}}}}
				if err := c.Create(ctx, p); err != nil {
					t.Fatal(err)
				}
				d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
			} else {
				order = []string{"job-uid", "post-uid", "screen-1"}
			}
			if err := c.Update(ctx, d); err != nil {
				t.Fatal(err)
			}
			if mode == "discovery" {
				for i := 0; i < 2; i++ {
					if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 202 || loadDevice(t, c).Status.ScreenCycle != nil {
						t.Fatal("pending Job advanced or borrowed another image")
					}
				}
			}
			rev, _ := screen.InputRevision(job.Spec)
			if _, err := s.Repository.Publish(ctx, artifact.Metadata{Unit: artifact.Unit{Namespace: "arprt", ScreenUID: "job-uid", DeviceUID: "device-1", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}, Output: render.CurrentTarget(render.Target{Width: 800, Height: 480, Palette: "gray4"}).Output}, InputRevision: rev, ScreenGeneration: 1, AttemptID: "job", Sequence: 1, RenderedAt: time.Now()}, b); err != nil {
				t.Fatal(err)
			}
			for i, uid := range append(order, order[0]) {
				if i > 0 {
					expireTurn(t, c)
				}
				response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
				if response.Status != 0 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != uid {
					t.Fatal("wrong turn", i, uid, response, loadDevice(t, c).Status.ScreenCycle)
				}
				if w := request(s, "GET", strings.TrimPrefix(response.ImageURL, "https://byos.example.test"), auth(), nil); w.Code != 200 {
					t.Fatal("image unavailable")
				}
				if i < len(order) && uid != "job-uid" {
					finished := <-rendered
					if loadDevice(t, c).Status.ScreenCycle.ServedAt.Before(&metav1.MicroTime{Time: finished}) {
						t.Fatal("retry window began before render finished")
					}
				}
				// A new server and repository instance must replay without a new POST.
				repo, err := artifact.NewPVC(root)
				if err != nil {
					t.Fatal(err)
				}
				restarted, err := NewServer(s.Config, c, c, repo)
				if err != nil {
					t.Fatal(err)
				}
				before := len(methods)
				retry := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil))
				if !reflect.DeepEqual(response, retry) || len(methods) != before {
					t.Fatal("restart/retry changed image or rendered again", response, retry)
				}
				s = restarted
			}
			// Removing the last selected resource resets membership deterministically.
			var selected api.ArprtScreen
			name := "screen"
			if mode == "discovery" {
				name = "job"
			}
			if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: name}, &selected); err != nil {
				t.Fatal(err)
			}
			if err := c.Delete(ctx, &selected); err != nil {
				t.Fatal(err)
			}
			if result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); result.Status != 0 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "post-uid" {
				t.Fatal("deleted screen replayed or order did not reset")
			}
		})
	}
}

func TestScreenCycleEditDuringPOSTRejectsResult(t *testing.T) {
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	b := pngFixture(t)
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	s, c, _ := newTestServer(t, renderer.URL)
	sc := postScreen(t, c)
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "list", UID: types.UID("list")}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: sc.Name}}}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
	if err := c.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	done := make(chan DisplayResponse, 1)
	go func() { done <- decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)) }()
	<-entered
	p.Spec.Screens = []api.LocalReference{{Name: "missing"}}
	if err := c.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	close(release)
	if result := <-done; result.Status != 202 || loadDevice(t, c).Status.ScreenCycle != nil {
		t.Fatal("obsolete assignment issued", result)
	}
}

func TestScreenCycleFailureHoldsTurnAndUsesOnlyItsOwnStaleImage(t *testing.T) {
	ctx := context.Background()
	b := pngFixture(t)
	var fail atomic.Bool
	fail.Store(true)
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	s, c, _ := newTestServer(t, renderer.URL)
	post := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "post", UID: "post-uid", Generation: 1}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL, Method: "POST"}}}}
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "list", UID: "list"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "screen"}, {Name: "post"}}}}
	for _, obj := range []client.Object{post, p} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
	if err := c.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 {
		t.Fatal(response)
	}
	expireTurn(t, c)
	for i := 0; i < 2; i++ {
		response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
		if response.Status != 202 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "screen-1" {
			t.Fatal("failed screen borrowed GET output or advanced", response)
		}
	}
	fail.Store(false)
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "post-uid" {
		t.Fatal("recovery did not hold failed turn", response)
	}
	expireTurn(t, c)
	_ = request(s, "GET", devicePath+"/api/display", auth(), nil)
	expireTurn(t, c)
	fail.Store(true)
	if response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); response.Status != 0 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "post-uid" {
		t.Fatal("own stale output unavailable", response)
	}
	if cond := meta.FindStatusCondition(loadDevice(t, c).Status.Conditions, "Ready"); cond == nil || cond.Reason != "StaleImageAvailable" {
		t.Fatal("stale status lost", cond)
	}
}
