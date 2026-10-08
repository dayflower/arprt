package byos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

func TestAdmissionRejectsBeforeAPIAndStablePollCoalescesStatus(t *testing.T) {
	b := pngFixture(t)
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer renderer.Close()
	s, c, _ := newTestServer(t, renderer.URL)
	for range cap(s.slots) {
		s.slots <- struct{}{}
	}
	if got := request(s, "GET", devicePath+"/api/display", auth(), nil); got.Code != 503 {
		t.Fatal("request limit ignored", got.Code)
	}
	for range cap(s.slots) {
		<-s.slots
	}
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 {
		t.Fatal(first)
	}
	before := loadDevice(t, c)
	second := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	after := loadDevice(t, c)
	if second.ImageURL != first.ImageURL || before.ResourceVersion != after.ResourceVersion {
		t.Fatal("identical retry caused status writes")
	}
	changed := auth()
	changed["Battery-Voltage"] = "3.95"
	_ = request(s, "GET", devicePath+"/api/display", changed, nil)
	if got := loadDevice(t, c); got.Status.Battery.Voltage == nil || *got.Status.Battery.Voltage != 3.95 {
		t.Fatal("coalescing discarded changed telemetry")
	}
}

func TestShutdownCancelsDetachedGETWithoutPublishing(t *testing.T) {
	s, c, _ := newTestServer(t, "http://renderer.invalid")
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.GET.Context = root
	started := make(chan struct{})
	s.GET.NewExecutor = func(string) render.Executor {
		return executorFunc(func(ctx context.Context, _ render.Request) (render.Output, error) {
			close(started)
			<-ctx.Done()
			return render.Output{}, ctx.Err()
		})
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(s, "GET", devicePath+"/api/display", auth(), nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("render did not start")
	}
	cancel()
	select {
	case response := <-done:
		if got := decodeDisplay(t, response); got.Status != 202 {
			t.Fatal("shutdown published incomplete output", got)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown left render running")
	}
	if got := loadDevice(t, c); got.Status.ScreenCycle != nil {
		t.Fatal("shutdown advanced cycle")
	}
}
