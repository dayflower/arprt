package byos

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/jobrender"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type demandExecutor func(context.Context, jobrender.ExecutionRequest) (artifact.Metadata, error)

func (f demandExecutor) Execute(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
	return f(ctx, req)
}

func demandServer(t *testing.T) (*Server, client.Client) {
	t.Helper()
	s, c, _ := newTestServer(t, "http://unused.invalid")
	var sc api.ArprtScreen
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
		t.Fatal(err)
	}
	sc.Spec.Source = api.ScreenSource{Job: &api.JobSource{ExecutionMode: "OnDemand", Renderer: api.RendererContainer{Image: "renderer:fixture"}}}
	if err := c.Update(t.Context(), &sc); err != nil {
		t.Fatal(err)
	}
	return s, c
}

func awaitDemandWaiters(t *testing.T, s *Server, count int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.demandMu.Lock()
		waiters := 0
		for _, f := range s.demands {
			waiters += f.waiters
		}
		s.demandMu.Unlock()
		if waiters == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("display waiters did not converge")
}

func asyncDisplay(s *Server, ctx context.Context, headers map[string]string) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", devicePath+"/api/display", nil).WithContext(ctx)
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		done <- w
	}()
	return done
}

func demandResponse(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(4 * time.Second):
		t.Fatal("display did not finish")
		return nil
	}
}

func TestOnDemandSharedDisplayAndIndependentRequests(t *testing.T) {
	s, c := demandServer(t)
	bytes := pngFixture(t)
	ready := make(chan jobrender.ExecutionRequest, 1)
	finish := make(chan struct{})
	var calls atomic.Uint64
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		sequence := calls.Add(1)
		if sequence == 1 {
			ready <- req
		}
		select {
		case <-finish:
		case <-ctx.Done():
			return artifact.Metadata{}, ctx.Err()
		}
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, AttemptID: fmt.Sprint(sequence), Sequence: sequence, RenderedAt: time.Now()}, bytes)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := asyncDisplay(s, ctx, auth())
	req := <-ready
	secondHeaders := auth()
	secondHeaders["Battery-Voltage"] = "3.2"
	second := asyncDisplay(s, t.Context(), secondHeaders)
	awaitDemandWaiters(t, s, 2)
	cancel()
	if w := demandResponse(t, first); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	if req.Timeout != 5*time.Second || req.RenderDeadline.Sub(req.StartedAt) != 10*time.Second || req.Snapshot == nil || *req.Snapshot.Device.Battery.Voltage != 3.91 {
		t.Fatal("invalid budget or first-observation snapshot", req)
	}
	if got := *loadDevice(t, c).Status.Battery.Voltage; got != 3.2 {
		t.Fatal("joining request skipped observations", got)
	}
	close(finish)
	result := decodeDisplay(t, demandResponse(t, second))
	if result.Status != 0 || calls.Load() != 1 {
		t.Fatal(result, calls.Load())
	}
	third := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if third != result || calls.Load() != 2 {
		t.Fatal("independent request did not render identical bytes again", third, calls.Load())
	}
	if w := request(s, "GET", strings.TrimPrefix(result.ImageURL, "https://byos.example.test"), auth(), nil); w.Code != 200 || calls.Load() != 2 {
		t.Fatal("image fetch started a Job", w.Code, calls.Load())
	}
}

func TestOnDemandFailureFallbackAndInfrastructureErrors(t *testing.T) {
	s, _ := demandServer(t)
	bytes := pngFixture(t)
	var failure error = &jobrender.ExecutionError{Reason: "JobFailed"}
	var sequence uint64
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		if failure != nil {
			return artifact.Metadata{}, failure
		}
		sequence++
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, AttemptID: fmt.Sprint(sequence), Sequence: sequence, RenderedAt: time.Now().Add(-time.Hour)}, bytes)
	})
	if result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); result.Status != 202 || result.ImageURL != "" || result.Filename != "" {
		t.Fatal(result)
	}
	failure = nil
	fresh := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if fresh.Status != 0 {
		t.Fatal(fresh)
	}
	for _, err := range []error{&jobrender.ExecutionError{Reason: "CollectionFailed"}, &jobrender.ExecutionError{Reason: "JobTimedOut"}} {
		failure = err
		if stale := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); stale != fresh {
			t.Fatal("lost compatible stale output", stale)
		}
	}
	failure = errors.New("storage unavailable")
	if w := request(s, "GET", devicePath+"/api/display", auth(), nil); w.Code != 503 {
		t.Fatal("infrastructure error hidden by fallback", w.Body.String())
	}
	failure = context.DeadlineExceeded
	if w := request(s, "GET", devicePath+"/api/display", auth(), nil); w.Code != 503 {
		t.Fatal("independent infrastructure timeout hidden by fallback", w.Body.String())
	}
	s.Jobs = nil
	if stale := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); stale != fresh {
		t.Fatal("disabled execution lost compatible fallback", stale)
	}
}

func TestOnDemandAllWaitersDisconnect(t *testing.T) {
	s, _ := demandServer(t)
	started := make(chan struct{})
	stopped := make(chan struct{})
	s.Jobs = demandExecutor(func(ctx context.Context, _ jobrender.ExecutionRequest) (artifact.Metadata, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return artifact.Metadata{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	first := asyncDisplay(s, ctx, auth())
	<-started
	second := asyncDisplay(s, ctx, auth())
	awaitDemandWaiters(t, s, 2)
	cancel()
	for _, done := range []<-chan *httptest.ResponseRecorder{first, second} {
		if w := demandResponse(t, done); w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("work survived all disconnected waiters")
	}
	awaitDemandWaiters(t, s, 0)
}

func TestOnDemandRenderCutoffRetainsFinalizationReserve(t *testing.T) {
	for _, spent := range []time.Duration{9950 * time.Millisecond, 10100 * time.Millisecond} {
		t.Run(spent.String(), func(t *testing.T) {
			s, c := demandServer(t)
			d := loadDevice(t, c)
			o := Observation{Model: "og"}
			observed, err := s.observeDisplay(t.Context(), d, o)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := s.selectDelivery(t.Context(), d, observed.target)
			if err != nil {
				t.Fatal(err)
			}
			bytes := pngFixture(t)
			_, err = s.Repository.Publish(t.Context(), artifact.Metadata{Unit: artifact.Unit{Namespace: d.Namespace, ScreenUID: string(selected.screen.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output}, ScreenGeneration: selected.screen.Generation, InputRevision: selected.revision, AttemptID: "old", Sequence: 1, RenderedAt: time.Now().Add(-time.Hour)}, bytes)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
				calls.Add(1)
				if deadline, _ := ctx.Deadline(); deadline != req.StartedAt.Add(10*time.Second) {
					return artifact.Metadata{}, errors.New("render cutoff extended")
				}
				<-ctx.Done()
				return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobTimedOut"}
			})
			started := time.Now().Add(-spent)
			ctx, cancel := context.WithDeadline(context.WithValue(t.Context(), admissionTimeKey{}, started), started.Add(13*time.Second))
			defer cancel()
			flight, leave, err := s.joinDemand(ctx, d, o, selected)
			if err != nil {
				t.Fatal(err)
			}
			defer leave()
			image, err := s.waitDemand(ctx, d, selected, flight)
			if err != nil || image.metadata.ImageDigest == "" || image.renderErr == nil || ctx.Err() != nil {
				t.Fatal("render expiry consumed finalization reserve", image, err, ctx.Err())
			}
			if spent > 10*time.Second && calls.Load() != 0 {
				t.Fatal("exhausted authentication/gate budget started a Job")
			}
			if err := s.Repository.Issue(ctx, string(d.UID), image.metadata.ImageDigest, time.Now()); err != nil {
				t.Fatal("remaining request budget could not issue fallback", err)
			}
		})
	}
}

func TestOnDemandIssuanceRevalidatesChangedInputs(t *testing.T) {
	for _, change := range []string{"credentials", "screen", "assignment", "target", "device-uid", "screen-uid"} {
		t.Run(change, func(t *testing.T) {
			s, c := demandServer(t)
			bytes := pngFixture(t)
			s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
				m, err := s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, AttemptID: "candidate", Sequence: 1, RenderedAt: time.Now()}, bytes)
				if err != nil {
					return m, err
				}
				return m, changeDemandInput(ctx, c, change)
			})
			w := request(s, "GET", devicePath+"/api/display", auth(), nil)
			want := 200
			if change == "credentials" {
				want = 401
			}
			if w.Code != want || decodeDisplay(t, w).ImageURL != "" {
				t.Fatal("issued changed input", w.Code, w.Body.String())
			}
		})
	}
}

func TestOnDemandFallbackDoesNotBorrowAnotherOwnerOrTarget(t *testing.T) {
	for _, incompatible := range []string{"device", "screen", "rotation", "budget"} {
		t.Run(incompatible, func(t *testing.T) {
			s, c := demandServer(t)
			d := loadDevice(t, c)
			observed, err := s.observeDisplay(t.Context(), d, Observation{Model: "og"})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := s.selectDelivery(t.Context(), d, observed.target)
			if err != nil {
				t.Fatal(err)
			}
			u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(selected.screen.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output}
			switch incompatible {
			case "device":
				u.DeviceUID = "another-device"
			case "screen":
				u.ScreenUID = "another-screen"
			case "rotation":
				u.Target.Rotation = 90
			case "budget":
				u.Output.PNGByteLimit--
			}
			_, err = s.Repository.Publish(t.Context(), artifact.Metadata{Unit: u, ScreenGeneration: 1, InputRevision: selected.revision, AttemptID: "other", Sequence: 1, RenderedAt: time.Now()}, pngFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			s.Jobs = demandExecutor(func(context.Context, jobrender.ExecutionRequest) (artifact.Metadata, error) {
				return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobTimedOut"}
			})
			if result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); result.Status != 202 || result.ImageURL != "" {
				t.Fatal("borrowed incompatible fallback", result)
			}
		})
	}
}

type deadlineIssuance struct{ artifact.Repository }

func (r deadlineIssuance) Issue(ctx context.Context, _, _ string, _ time.Time) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestOnDemandFinalizationStopsAtRequestDeadline(t *testing.T) {
	s, _ := demandServer(t)
	bytes := pngFixture(t)
	var calls atomic.Int32
	s.Jobs = demandExecutor(func(context.Context, jobrender.ExecutionRequest) (artifact.Metadata, error) {
		calls.Add(1)
		return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobTimedOut"}
	})
	s.Repository = deadlineIssuance{s.Repository}
	// Model the final 500ms of the 13s application lifetime without a 13s sleep.
	started := time.Now().Add(-12500 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.WithValue(t.Context(), admissionTimeKey{}, started), started.Add(13*time.Second))
	defer cancel()
	r := httptest.NewRequest("GET", devicePath+"/api/display", nil).WithContext(ctx)
	for name, value := range auth() {
		r.Header.Set(name, value)
	}
	// Seed a success for fallback; rendering is already past the absolute cutoff.
	d := loadDevice(t, s.Client)
	if err := s.Identity.Register(ctx, d, d.Spec.HardwareID, false); err != nil {
		t.Fatal(err)
	}
	observed, err := s.observeDisplay(ctx, d, Observation{Model: "og"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.selectDelivery(ctx, d, observed.target)
	if err != nil {
		t.Fatal(err)
	}
	u := artifact.Unit{Namespace: d.Namespace, ScreenUID: string(selected.screen.UID), DeviceUID: string(d.UID), Target: selected.target, Output: selected.output}
	_, err = s.Repository.Publish(ctx, artifact.Metadata{Unit: u, ScreenGeneration: 1, InputRevision: selected.revision, AttemptID: "previous", Sequence: 1, RenderedAt: time.Now()}, bytes)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.display(w, r, d, Observation{Model: "og"})
	if w.Code != 503 || !errors.Is(ctx.Err(), context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatal("issuance extended request deadline", w.Code, ctx.Err())
	}
}

func changeDemandInput(ctx context.Context, c client.Client, change string) error {
	if change == "credentials" {
		// Deleting the configured Secret invalidates credentials without changing generation.
		return c.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "auth"}})
	}
	var d api.ArprtDevice
	if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: "living-room"}, &d); err != nil {
		return err
	}
	var sc api.ArprtScreen
	if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
		return err
	}
	switch change {
	case "screen":
		sc.Generation++
		sc.Spec.Source.Job.Timeout = "1s"
		return c.Update(ctx, &sc)
	case "assignment":
		d.Spec.ScreenRef.Name = "another"
		return c.Update(ctx, &d)
	case "target":
		d.Spec.Rotation = 90
		return c.Update(ctx, &d)
	case "device-uid":
		if err := c.Delete(ctx, &d); err != nil {
			return err
		}
		d.UID, d.ResourceVersion = "replacement", ""
		return c.Create(ctx, &d)
	case "screen-uid":
		if err := c.Delete(ctx, &sc); err != nil {
			return err
		}
		sc.UID, sc.ResourceVersion = "replacement", ""
		return c.Create(ctx, &sc)
	}
	return nil
}
