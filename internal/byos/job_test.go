package byos

import (
	"context"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestJobDisplayOnlyReadsCompletedPair(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	ctx := context.Background()
	var sc api.TrmnlScreen
	if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
		t.Fatal(err)
	}
	sc.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "busybox:1.37.0"}}}
	if err := c.Update(ctx, &sc); err != nil {
		t.Fatal(err)
	}
	s.GET.NewExecutor = func(string) render.Executor { t.Fatal("display started renderer"); return nil }
	for i := 0; i < 3; i++ {
		if result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); result.Status != 202 || result.ImageURL != "" {
			t.Fatal("initial Job not pending")
		}
	}
	rev, _ := screen.InputRevision(sc.Spec)
	u := artifact.Unit{Namespace: sc.Namespace, ScreenUID: string(sc.UID), DeviceUID: "device-1", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}
	metadata := artifact.Metadata{Unit: u, ScreenGeneration: sc.Generation, InputRevision: rev, AttemptID: "job-1", Sequence: 1, RenderedAt: time.Now()}
	m, err := s.Repository.Publish(ctx, metadata, pngFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if result.Status != 0 || !strings.Contains(result.ImageURL, m.ImageDigest) {
		t.Fatal("completed Job not served")
	}
	if w := request(s, "GET", strings.TrimPrefix(result.ImageURL, "https://byos.example.test"), auth(), nil); w.Code != 200 {
		t.Fatal("Job image not fetchable")
	}
	// A pending refresh/failure preserves both serving readiness and the
	// reconciler's actionable reason; polling never reports it as a fresh render.
	d := loadDevice(t, c)
	condition(d, "RenderReady", false, "CollectionFailed", "Missing renderer output")
	if err = c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	result = decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if result.Status != 0 {
		t.Fatal("lost fallback")
	}
	d = loadDevice(t, c)
	if cond := meta.FindStatusCondition(d.Status.Conditions, "RenderReady"); cond == nil || cond.Reason != "CollectionFailed" {
		t.Fatal("poll erased collection failure")
	}
	// Equal targets belonging to another device cannot borrow this artifact.
	u.DeviceUID = "another-device"
	metadata.Unit = u
	metadata.Sequence = 2
	_, _ = s.Repository.Publish(ctx, metadata, pngFixture(t))
	if err = c.Delete(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.UID = "replacement"
	d.ResourceVersion = ""
	if err = c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if result = decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); result.Status != 202 {
		t.Fatal("replacement borrowed Job artifact")
	}
}
