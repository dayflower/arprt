package byos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/render"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func postScreen(t *testing.T, c client.Client) *api.TrmnlScreen {
	t.Helper()
	var s api.TrmnlScreen
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Source.HTTP.Method = "POST"
	s.Generation++
	if err := c.Update(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestPOSTTelemetryOwnershipFreshnessAndRestart(t *testing.T) {
	b := pngFixture(t)
	indexed, err := os.ReadFile("../../testdata/phase2/target-indexed-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	var mu sync.Mutex
	var contexts []render.Context
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got render.Context
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "image/png" || r.Header.Get("ID") != "" || r.Header.Get("Access-Token") != "" || r.Header.Get("Cookie") != "" || bytes.Contains(body, []byte(token)) || bytes.Contains(body, []byte("hardwareID")) || bytes.Contains(body, []byte("authSecretRef")) {
			t.Error("POST contract or credential isolation failed")
		}
		q := r.URL.Query()
		if got.RenderTarget != (render.Target{Width: 800, Height: 480, Palette: "gray4"}) || q.Get("width") != "800" || q.Get("height") != "480" || q.Get("palette") != "gray4" {
			t.Error("body and query target differ")
		}
		mu.Lock()
		contexts = append(contexts, got)
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		if fail.Load() {
			_, _ = w.Write(b[:40])
		} else if got.Device.Name == "second" {
			_, _ = w.Write(indexed)
		} else {
			_, _ = w.Write(b)
		}
	}))
	defer renderer.Close()
	s, c, root := newTestServer(t, renderer.URL)
	screen := postScreen(t, c)
	headers := auth()
	headers["Cookie"] = "must-not-forward=private"
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if first.Status != 0 {
		t.Fatal("POST initial delivery failed")
	}
	headers["Battery-Voltage"], headers["RSSI"] = "3.72", "-61"
	if decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil)).Status != 0 {
		t.Fatal("POST repeated delivery failed")
	}
	d := loadDevice(t, c)
	second := d.DeepCopy()
	second.Name, second.UID, second.ResourceVersion = "second", "device-2", ""
	second.Spec.HardwareID = "02:00:00:00:00:02"
	second.Status = api.TrmnlDeviceStatus{}
	if err := c.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondHeaders := auth()
	secondHeaders["ID"], secondHeaders["Battery-Voltage"], secondHeaders["RSSI"] = second.Spec.HardwareID, "3.12", "-80"
	secondPath := strings.Replace(devicePath, "living-room", "second", 1)
	fail.Store(true)
	if decodeDisplay(t, request(s, "GET", secondPath+"/api/display", secondHeaders, nil)).Status != 202 {
		t.Fatal("second device borrowed first device's fallback")
	}
	fail.Store(false)
	isolated := decodeDisplay(t, request(s, "GET", secondPath+"/api/display", secondHeaders, nil))
	if isolated.Status != 0 || isolated.Filename == first.Filename {
		t.Fatal("device-specific output was reused across devices")
	}
	firstPath := strings.TrimPrefix(first.ImageURL, "https://byos.example.test")
	if request(s, "GET", strings.Replace(firstPath, "living-room", "second", 1), secondHeaders, nil).Code != 404 {
		t.Fatal("another device's unissued image was accessible")
	}
	// Omitted/invalid current measurements may use known valid telemetry.
	delete(headers, "Battery-Voltage")
	headers["RSSI"] = "invalid"
	_ = request(s, "GET", devicePath+"/api/display", headers, nil)
	mu.Lock()
	if len(contexts) != 5 || *contexts[0].Device.Battery.Voltage != 3.91 || *contexts[1].Device.Battery.Voltage != 3.72 || *contexts[1].Device.Network.RSSI != -61 || contexts[3].Device.Name != "second" || *contexts[3].Device.Battery.Voltage != 3.12 || *contexts[4].Device.Battery.Voltage != 3.72 || *contexts[4].Device.Network.RSSI != -61 {
		t.Error("latest request or known-state context was lost", contexts)
	}
	mu.Unlock()
	unit := artifact.Unit{Namespace: "arprt", ScreenUID: string(screen.UID), DeviceUID: string(d.UID), Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	before, err := s.Repository.Latest(context.Background(), unit)
	if err != nil || before.Sequence != 3 || len(before.ContextDigest) != 64 {
		t.Fatal("POST context metadata not persisted", before, err)
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
	// A failed refresh after a definition edit can still use this pair's old
	// compatible success; revision changes prohibit only fresh cache reuse.
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(screen), screen); err != nil {
		t.Fatal(err)
	}
	screen.Spec.Source.HTTP.URL += "?revision=2"
	screen.Generation++
	if err := c.Update(context.Background(), screen); err != nil {
		t.Fatal(err)
	}
	stale := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil))
	if stale != first || request(s, "GET", firstPath, headers, nil).Code != 200 || meta.FindStatusCondition(loadDevice(t, c).Status.Conditions, "Ready").Reason != "StaleImageAvailable" {
		t.Fatal("restart or invalid output lost compatible prior success")
	}
	fail.Store(false)
	_ = request(s, "GET", devicePath+"/api/display", headers, nil)
	after, err := repo.Latest(context.Background(), unit)
	if err != nil || after.Sequence <= before.Sequence {
		t.Fatal("attempt ordering did not resume from persisted success", err)
	}
	// Replacing the physical identity cannot inherit the old POST fallback.
	d = loadDevice(t, c)
	if err := c.Delete(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	d.UID, d.ResourceVersion = "replacement", ""
	if err := c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if decodeDisplay(t, request(s, "GET", devicePath+"/api/display", headers, nil)).Status != 202 {
		t.Fatal("replacement UID inherited prior POST success")
	}
}

func postPair(t *testing.T) (*Server, client.Client, *api.TrmnlScreen, *api.TrmnlDevice, render.Context) {
	t.Helper()
	s, c, _ := newTestServer(t, "http://unused.invalid")
	screen := postScreen(t, c)
	d := loadDevice(t, c)
	target := render.Target{Width: 800, Height: 480, Palette: "gray4"}
	d.Status.Display = &api.DisplayStatus{Width: target.Width, Height: target.Height, Palette: target.Palette}
	if err := c.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	snapshot, err := render.Build(d.Name, target, devicestate.Observation(d.Status), render.Observation{})
	if err != nil {
		t.Fatal(err)
	}
	return s, c, screen, d, snapshot
}

func TestPOSTLateAttemptAndTimeout(t *testing.T) {
	s, _, screen, d, snapshot := postPair(t)
	b := pngFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.POST.NewExecutor = func(string) render.Executor {
		return executorFunc(func(context.Context, render.Request) (render.Output, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return render.Output{PNG: b}, nil
		})
	}
	done := make(chan error, 1)
	go func() { _, err := s.POST.Image(context.Background(), screen, d, snapshot); done <- err }()
	<-entered
	latest, err := s.POST.Image(context.Background(), screen, d, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, artifact.ErrObsolete) {
		t.Fatal("late response was allowed to publish", err)
	}
	stored, err := s.Repository.Latest(context.Background(), latest.Unit)
	if err != nil || stored.AttemptID != latest.AttemptID {
		t.Fatal("older attempt replaced latest success", err)
	}
	s.POST.NewExecutor = func(string) render.Executor {
		return executorFunc(func(ctx context.Context, _ render.Request) (render.Output, error) {
			<-ctx.Done()
			return render.Output{}, ctx.Err()
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stale, err := s.POST.Image(ctx, screen, d, snapshot)
	if err == nil || stale.AttemptID != latest.AttemptID {
		t.Fatal("timeout did not preserve latest success", err)
	}
}

func TestPOSTChangedInputsCannotPublish(t *testing.T) {
	for _, change := range []string{"target", "screen-definition", "screen-uid", "device-uid", "assignment", "rotation"} {
		t.Run(change, func(t *testing.T) {
			s, c, screen, d, snapshot := postPair(t)
			b := pngFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			s.POST.NewExecutor = func(string) render.Executor {
				return executorFunc(func(context.Context, render.Request) (render.Output, error) {
					close(entered)
					<-release
					return render.Output{PNG: b}, nil
				})
			}
			done := make(chan error, 1)
			go func() { _, err := s.POST.Image(context.Background(), screen, d, snapshot); done <- err }()
			<-entered
			device := d.DeepCopy()
			sc := screen.DeepCopy()
			switch change {
			case "target":
				device.Status.Display.Width = 799
				if err := c.Status().Update(context.Background(), device); err != nil {
					t.Fatal(err)
				}
			case "screen-definition":
				sc.Spec.Source.HTTP.URL = "http://new.invalid"
				sc.Generation++
				if err := c.Update(context.Background(), sc); err != nil {
					t.Fatal(err)
				}
			case "screen-uid":
				if err := c.Delete(context.Background(), sc); err != nil {
					t.Fatal(err)
				}
				sc.UID, sc.ResourceVersion = "replacement", ""
				if err := c.Create(context.Background(), sc); err != nil {
					t.Fatal(err)
				}
			case "device-uid":
				if err := c.Delete(context.Background(), device); err != nil {
					t.Fatal(err)
				}
				device.UID, device.ResourceVersion = "replacement", ""
				if err := c.Create(context.Background(), device); err != nil {
					t.Fatal(err)
				}
			case "rotation":
				device.Spec.Rotation = 90
				device.Generation++
				if err := c.Update(context.Background(), device); err != nil {
					t.Fatal(err)
				}
			case "assignment":
				device.Spec.ScreenRef.Name = "other"
				device.Generation++
				if err := c.Update(context.Background(), device); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if err := <-done; !errors.Is(err, artifact.ErrObsolete) {
				t.Fatal("obsolete input published", err)
			}
			_, err := s.Repository.Latest(context.Background(), artifact.Unit{Namespace: "arprt", ScreenUID: string(screen.UID), DeviceUID: string(d.UID), Target: snapshot.RenderTarget})
			if !errors.Is(err, artifact.ErrNotFound) {
				t.Fatal("obsolete success changed pointer", err)
			}
		})
	}
}

type conflictClient struct {
	client.Client
	injected atomic.Bool
}

func (c *conflictClient) Status() client.SubResourceWriter {
	return conflictWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type conflictWriter struct {
	client.SubResourceWriter
	parent *conflictClient
}

func (w conflictWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if d, ok := obj.(*api.TrmnlDevice); ok && w.parent.injected.CompareAndSwap(false, true) {
		var concurrent api.TrmnlDevice
		if err := w.parent.Client.Get(ctx, client.ObjectKeyFromObject(d), &concurrent); err != nil {
			return err
		}
		condition(&concurrent, "ConcurrentController", true, "Updated", "Concurrent status update preserved")
		if err := w.parent.Client.Status().Update(ctx, &concurrent); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "trmnldevices"}, d.Name, errors.New("injected optimistic conflict"))
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestPOSTContextSurvivesStatusConflict(t *testing.T) {
	s, c, _, _, _ := postPair(t)
	writer := &conflictClient{Client: c}
	s.Client = writer
	b := pngFixture(t)
	s.POST.NewExecutor = func(string) render.Executor {
		return executorFunc(func(_ context.Context, input render.Request) (render.Output, error) {
			if input.Context.Device.Battery == nil || *input.Context.Device.Battery.Voltage != 3.91 || input.Context.Device.Network == nil || *input.Context.Device.Network.RSSI != -48 {
				t.Error("status conflict lost current request telemetry")
			}
			return render.Output{PNG: b}, nil
		})
	}
	result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if result.Status != 0 || !writer.injected.Load() || !meta.IsStatusConditionTrue(loadDevice(t, c).Status.Conditions, "ConcurrentController") {
		t.Fatal("status retry lost concurrent controller state or delivery")
	}
}
