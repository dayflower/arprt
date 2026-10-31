package byos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/jobrender"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func skipServer(t *testing.T, count int) (*Server, client.Client, *api.ArprtPlaylist) {
	t.Helper()
	s, c := demandServer(t)
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "skip", UID: "skip-uid"}, Spec: api.ArprtPlaylistSpec{FailurePolicy: "Skip"}}
	for i := range count {
		name := "screen"
		if i > 0 {
			name = fmt.Sprintf("screen-%d", i)
			sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: name, UID: types.UID("uid-" + name), Generation: 1}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{ExecutionMode: "OnDemand", Renderer: api.RendererContainer{Image: "renderer:fixture"}}}}}
			if err := c.Create(t.Context(), sc); err != nil {
				t.Fatal(err)
			}
		}
		p.Spec.Screens = append(p.Spec.Screens, api.LocalReference{Name: name})
	}
	if err := c.Create(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	d.Spec.PlaylistRef = &api.LocalReference{Name: p.Name}
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	return s, c, p
}

func expireSkipTurn(t *testing.T, c client.Client) {
	t.Helper()
	d := loadDevice(t, c)
	d.Status.ScreenCycle.ServedAt = metav1.NewMicroTime(time.Now().Add(-time.Hour))
	if err := c.Status().Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
}

func TestSkipTraversalReplayAndRestart(t *testing.T) {
	s, c, _ := skipServer(t, 3)
	bytes := pngFixture(t)
	var calls []string
	var sequence uint64
	failed := "screen"
	executor := demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		calls = append(calls, req.Screen)
		if req.Screen == failed {
			return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
		}
		sequence++
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, Sequence: sequence, AttemptID: fmt.Sprint(sequence), RenderedAt: time.Now()}, bytes)
	})
	s.Jobs = executor
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 || !reflect.DeepEqual(calls, []string{"screen", "screen-1"}) || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "uid-screen-1" {
		t.Fatal(first, calls)
	}
	// A server restart reuses the durable turn without rendering or advancing.
	restarted, err := NewServer(s.Config, c, c, s.Repository)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Jobs = executor
	if replay := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil)); replay != first || len(calls) != 2 {
		t.Fatal(replay, calls)
	}
	expireSkipTurn(t, c)
	next := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if next.Status != 0 || calls[len(calls)-1] != "screen-2" {
		t.Fatal(next, calls)
	}
	// From C, a failure wraps once to A, which is eligible again.
	expireSkipTurn(t, c)
	// Force the previous successful position to B, so C is the next candidate.
	d := loadDevice(t, c)
	d.Status.ScreenCycle.ScreenUID = "uid-screen-1"
	d.Status.ScreenCycle.EntryIndex = 1
	if err := c.Status().Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	failed = "screen-2"
	before := len(calls)
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || !reflect.DeepEqual(calls[before:], []string{"screen-2", "screen"}) {
		t.Fatal(got, calls)
	}
}

func TestSkipAllFailedDoesNotServeStaleOrAdvance(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, c, _ := skipServer(t, count)
			bytes := pngFixture(t)
			var failed bool
			var calls int
			s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
				calls++
				if failed {
					return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
				}
				return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, Sequence: 1, AttemptID: "seed", RenderedAt: time.Now()}, bytes)
			})
			if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 {
				t.Fatal(got)
			}
			expireSkipTurn(t, c)
			previous := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
			failed = true
			for range 2 {
				before := calls
				w := request(s, "GET", devicePath+"/api/display", auth(), nil)
				if w.Code != 200 || w.Body.String() != `{"status":202}` || calls-before != count || !reflect.DeepEqual(previous, loadDevice(t, c).Status.ScreenCycle) {
					t.Fatal(w.Code, w.Body.String(), calls, loadDevice(t, c).Status.ScreenCycle)
				}
			}
		})
	}
}

type countIssuance struct {
	artifact.Repository
	count atomic.Int32
}

func (r *countIssuance) Issue(ctx context.Context, uid, digest string, now time.Time) error {
	r.count.Add(1)
	return r.Repository.Issue(ctx, uid, digest, now)
}

func awaitSkipWaiters(t *testing.T, s *Server, count int) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		s.demandMu.Lock()
		n := 0
		for _, f := range s.skips {
			n += f.waiters
		}
		s.demandMu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Skip waiters did not converge")
}

func TestSkipSharesExplorationIssuanceAndTurn(t *testing.T) {
	s, c, _ := skipServer(t, 2)
	var chosen api.ArprtScreen
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen-1"}, &chosen); err != nil {
		t.Fatal(err)
	}
	chosen.Spec.Overlay = &api.ScreenOverlay{Enabled: true, CycleCorners: true}
	if err := c.Update(t.Context(), &chosen); err != nil {
		t.Fatal(err)
	}
	bytes := pngFixture(t)
	repo := &countIssuance{Repository: s.Repository}
	s.Repository = repo
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		calls.Add(1)
		if req.Screen == "screen" {
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				return artifact.Metadata{}, ctx.Err()
			}
			return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
		}
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, Sequence: 1, AttemptID: "success", RenderedAt: time.Now()}, bytes)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	first := asyncDisplay(s, ctx, auth())
	<-started
	second := asyncDisplay(s, t.Context(), auth())
	third := asyncDisplay(s, t.Context(), auth())
	awaitSkipWaiters(t, s, 3)
	if w := demandResponse(t, first); w.Code != 503 {
		t.Fatal(w.Code)
	}
	close(finish)
	a, b := decodeDisplay(t, demandResponse(t, second)), decodeDisplay(t, demandResponse(t, third))
	if a.Status != 0 || a != b || calls.Load() != 2 || repo.count.Load() != 1 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "uid-screen-1" {
		t.Fatal(a, b, calls.Load(), repo.count.Load())
	}
	corner, err := s.Repository.Corner(t.Context(), "device-1")
	if err != nil || corner.Revision != 1 || corner.Pending != nil {
		t.Fatal("shared turn advanced the overlay corner twice", corner, err)
	}
}

func TestSkipStopsAfterAllWaitersLeave(t *testing.T) {
	s, _, _ := skipServer(t, 2)
	started, stopped := make(chan struct{}), make(chan struct{})
	s.Jobs = demandExecutor(func(ctx context.Context, _ jobrender.ExecutionRequest) (artifact.Metadata, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return artifact.Metadata{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := asyncDisplay(s, ctx, auth())
	<-started
	second := asyncDisplay(s, ctx, auth())
	awaitSkipWaiters(t, s, 2)
	cancel()
	demandResponse(t, first)
	demandResponse(t, second)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("orphaned exploration")
	}
}

func TestSkipInfrastructureAndChangesAreNotSkipped(t *testing.T) {
	for _, change := range []string{"storage", "credentials", "membership", "candidate-definition", "device-uid"} {
		t.Run(change, func(t *testing.T) {
			s, c, p := skipServer(t, 2)
			var calls int
			s.Jobs = demandExecutor(func(ctx context.Context, _ jobrender.ExecutionRequest) (artifact.Metadata, error) {
				calls++
				switch change {
				case "storage":
					return artifact.Metadata{}, errors.New("storage failed")
				case "credentials", "device-uid":
					if err := changeDemandInput(ctx, c, change); err != nil {
						return artifact.Metadata{}, err
					}
				case "membership":
					p.Spec.Screens = p.Spec.Screens[:1]
					if err := c.Update(ctx, p); err != nil {
						return artifact.Metadata{}, err
					}
				case "candidate-definition":
					var sc api.ArprtScreen
					if err := c.Get(ctx, client.ObjectKey{Namespace: "arprt", Name: "screen-1"}, &sc); err != nil {
						return artifact.Metadata{}, err
					}
					sc.Spec.Source.Job.Timeout = "1s"
					if err := c.Update(ctx, &sc); err != nil {
						return artifact.Metadata{}, err
					}
				}
				return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
			})
			w := request(s, "GET", devicePath+"/api/display", auth(), nil)
			want := 200
			if change == "storage" {
				want = 503
			}
			if change == "credentials" {
				want = 401
			}
			if w.Code != want || decodeDisplay(t, w).ImageURL != "" || calls != 1 || loadDevice(t, c).Status.ScreenCycle != nil {
				t.Fatal(w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestSkipMixedHTTPAndScheduledFreshness(t *testing.T) {
	bytes := pngFixture(t)
	var outage atomic.Bool
	var gets, posts atomic.Int32
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
		} else {
			gets.Add(1)
		}
		if outage.Load() {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes)
	}))
	defer renderer.Close()
	s, c, p := skipServer(t, 4)
	s.Jobs = demandExecutor(func(context.Context, jobrender.ExecutionRequest) (artifact.Metadata, error) {
		return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
	})
	sources := []api.ScreenSource{{HTTP: &api.HTTPSource{URL: renderer.URL, Method: "GET"}}, {HTTP: &api.HTTPSource{URL: renderer.URL, Method: "POST"}}, {Job: &api.JobSource{Renderer: api.RendererContainer{Image: "renderer:fixture"}}}}
	for i, source := range sources {
		var sc api.ArprtScreen
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: p.Spec.Screens[i].Name}, &sc); err != nil {
			t.Fatal(err)
		}
		sc.Spec.Source = source
		sc.Spec.RefreshInterval = "1h"
		if i == 1 {
			sc.Spec.RefreshInterval = ""
		}
		if err := c.Update(t.Context(), &sc); err != nil {
			t.Fatal(err)
		}
	}
	// Seed GET, POST and scheduled images; HTTP failures must not return stale.
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 {
		t.Fatal(got)
	}
	expireSkipTurn(t, c)
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || posts.Load() != 1 {
		t.Fatal(got, posts.Load())
	}
	d := loadDevice(t, c)
	var sc api.ArprtScreen
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen-2"}, &sc); err != nil {
		t.Fatal(err)
	}
	revision, err := screen.InputRevision(sc.Spec)
	if err != nil {
		t.Fatal(err)
	}
	unit := artifact.Unit{Namespace: "arprt", ScreenUID: string(sc.UID), DeviceUID: string(d.UID), Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}, Output: render.CurrentTarget(render.Target{Width: 800, Height: 480, Palette: "gray4"}).Output}
	if _, err := s.Repository.Publish(t.Context(), artifact.Metadata{Unit: unit, ScreenGeneration: 1, InputRevision: revision, Sequence: 1, AttemptID: "scheduled", RenderedAt: time.Now()}, bytes); err != nil {
		t.Fatal(err)
	}
	expireSkipTurn(t, c)
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "uid-screen-2" {
		t.Fatal(got)
	}
	// A fresh cached GET is usable after the failed demand candidate is skipped.
	expireSkipTurn(t, c)
	outage.Store(true)
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || gets.Load() != 1 || loadDevice(t, c).Status.ScreenCycle.ScreenUID != "screen-1" { // screen-1 UID is the original GET screen's UID.
		t.Fatal(got, gets.Load(), loadDevice(t, c).Status.ScreenCycle)
	}
	// Expire both cache entries explicitly so slow race builds cannot age the
	// fresh GET out before the assertion above or require a wall-clock sleep.
	for _, u := range []artifact.Unit{
		{Namespace: "arprt", ScreenUID: "screen-1", Target: unit.Target, Output: unit.Output},
		unit,
	} {
		m, err := s.Repository.Latest(t.Context(), u)
		if err != nil {
			t.Fatal(err)
		}
		m.Sequence++
		m.AttemptID += "-expired"
		m.RenderedAt = time.Now().Add(-2 * time.Hour)
		if _, err := s.Repository.Publish(t.Context(), m, bytes); err != nil {
			t.Fatal(err)
		}
	}
	// All expired cached images and the old POST are rejected.
	expireSkipTurn(t, c)
	previous := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
	if w := request(s, "GET", devicePath+"/api/display", auth(), nil); w.Body.String() != `{"status":202}` || !reflect.DeepEqual(previous, loadDevice(t, c).Status.ScreenCycle) {
		t.Fatal(w.Body.String())
	}
}

func TestSkipCandidateAndOverallBudgets(t *testing.T) {
	for _, timeout := range []string{"", "10s"} {
		t.Run("candidate-"+timeout, func(t *testing.T) {
			t.Parallel()
			s, c, _ := skipServer(t, 2)
			if timeout != "" {
				var sc api.ArprtScreen
				if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
					t.Fatal(err)
				}
				sc.Spec.Source.Job.Timeout = timeout
				if err := c.Update(t.Context(), &sc); err != nil {
					t.Fatal(err)
				}
			}
			bytes := pngFixture(t)
			var calls int
			var remaining time.Duration
			s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
				calls++
				if req.RenderDeadline != req.StartedAt.Add(10*time.Second) {
					return artifact.Metadata{}, errors.New("overall deadline reset")
				}
				if req.Screen == "screen" {
					want := 5 * time.Second
					if timeout != "" {
						want = 10 * time.Second
					}
					if req.Timeout != want {
						return artifact.Metadata{}, errors.New("incorrect candidate timeout")
					}
					timer := time.NewTimer(req.Timeout)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-ctx.Done():
					}
					return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobTimedOut"}
				}
				remaining = time.Until(req.RenderDeadline)
				return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, Sequence: 1, AttemptID: "second", RenderedAt: time.Now()}, bytes)
			})
			w := request(s, "GET", devicePath+"/api/display", auth(), nil)
			result := decodeDisplay(t, w)
			if timeout == "" {
				if result.Status != 0 || calls != 2 || remaining <= 0 || remaining >= 5*time.Second {
					t.Fatal(w.Code, result, calls, remaining)
				}
			} else if result.Status != 202 || calls != 1 || loadDevice(t, c).Status.ScreenCycle != nil {
				t.Fatal(w.Code, result, calls)
			}
		})
	}
}

func TestSkipAdmissionCutoffAndHTTPBudget(t *testing.T) {
	for _, spent := range []time.Duration{9900 * time.Millisecond, 10100 * time.Millisecond} {
		t.Run(spent.String(), func(t *testing.T) {
			startedHTTP := make(chan struct{}, 1)
			renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { startedHTTP <- struct{}{}; <-r.Context().Done() }))
			defer renderer.Close()
			s, c, p := skipServer(t, 1)
			var sc api.ArprtScreen
			if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
				t.Fatal(err)
			}
			sc.Spec.Source = api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL, Method: "GET"}}
			if err := c.Update(t.Context(), &sc); err != nil {
				t.Fatal(err)
			}
			d := loadDevice(t, c)
			if err := s.Identity.Register(t.Context(), d, d.Spec.HardwareID, false); err != nil {
				t.Fatal(err)
			}
			observed, err := s.observeDisplay(t.Context(), d, Observation{Model: "og"})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := s.selectDelivery(t.Context(), d, observed.target)
			if err != nil {
				t.Fatal(err)
			}
			if selected.renderTimeout != 10*time.Second || p.Spec.RenderTimeout != "" {
				t.Fatal(selected)
			}
			admitted := time.Now().Add(-spent)
			ctx, cancel := context.WithDeadline(context.WithValue(t.Context(), admissionTimeKey{}, admitted), admitted.Add(13*time.Second))
			defer cancel()
			gate := make(chan struct{}, 1)
			f, leave, err := s.joinSkip(ctx, d, Observation{Model: "og"}, observed.seen, selected, token, gate)
			if err != nil {
				t.Fatal(err)
			}
			defer leave()
			select {
			case <-f.done:
			case <-time.After(time.Second):
				t.Fatal("rendering used finalization reserve")
			}
			if f.err != nil || f.response.Status != 202 || ctx.Err() != nil || loadDevice(t, c).Status.ScreenCycle != nil {
				t.Fatal(f.err, f.response, ctx.Err())
			}
			if spent > 10*time.Second && len(startedHTTP) != 0 {
				t.Fatal("exhausted admission budget started HTTP")
			}
		})
	}
}

func TestSkipPolicyEditInvalidatesReplay(t *testing.T) {
	for _, policy := range []string{"timeout", "Hold"} {
		t.Run(policy, func(t *testing.T) {
			s, c, p := skipServer(t, 1)
			bytes := pngFixture(t)
			var calls int
			s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
				calls++
				return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, ScreenGeneration: 1, InputRevision: req.Revision, Sequence: uint64(calls), AttemptID: fmt.Sprint(calls), RenderedAt: time.Now()}, bytes)
			})
			if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 {
				t.Fatal(got)
			}
			if policy == "Hold" {
				p.Spec.FailurePolicy = "Hold"
			} else {
				p.Spec.RenderTimeout = "1s"
			}
			if err := c.Update(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || calls != 2 {
				t.Fatal(got, calls)
			}
		})
	}
}

type skipStorageFault struct {
	artifact.Repository
	operation string
}

func (r skipStorageFault) Latest(ctx context.Context, u artifact.Unit) (artifact.Metadata, error) {
	if r.operation == "latest" {
		return artifact.Metadata{}, errors.New("storage read failed")
	}
	return r.Repository.Latest(ctx, u)
}
func (r skipStorageFault) Publish(ctx context.Context, m artifact.Metadata, b []byte) (artifact.Metadata, error) {
	if r.operation == "publish" {
		return artifact.Metadata{}, errors.New("storage write failed")
	}
	return r.Repository.Publish(ctx, m, b)
}

type skipCycleFault struct{ client.Client }

func (c skipCycleFault) Status() client.SubResourceWriter {
	return skipCycleFaultWriter{SubResourceWriter: c.Client.Status()}
}

type skipCycleFaultWriter struct{ client.SubResourceWriter }

func (w skipCycleFaultWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if d, ok := obj.(*api.ArprtDevice); ok && d.Status.ScreenCycle != nil {
		return errors.New("cycle write failed")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestSkipStorageIssuanceAndTurnFailures(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, operation := range []string{"latest", "publish", "issue", "cycle"} {
			t.Run(method+"-"+operation, func(t *testing.T) {
				s, c, _ := skipServer(t, 2)
				var sc api.ArprtScreen
				if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &sc); err != nil {
					t.Fatal(err)
				}
				sc.Spec.Source = api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer.example.test", Method: method}}
				if err := c.Update(t.Context(), &sc); err != nil {
					t.Fatal(err)
				}
				bytes := pngFixture(t)
				var renders atomic.Int32
				executor := func(string) render.Executor {
					return executorFunc(func(context.Context, render.Request) (render.Output, error) {
						renders.Add(1)
						return render.Output{PNG: bytes}, nil
					})
				}
				s.GET.NewExecutor = executor
				s.POST.NewExecutor = executor
				switch operation {
				case "latest", "publish":
					repo := skipStorageFault{Repository: s.Repository, operation: operation}
					s.Repository = repo
					s.GET.Repository = repo
					s.POST.Repository = repo
				case "issue":
					s.Repository = &issueErrorRepository{Repository: s.Repository}
				case "cycle":
					s.Client = skipCycleFault{Client: c}
				}
				var jobs atomic.Int32
				s.Jobs = demandExecutor(func(context.Context, jobrender.ExecutionRequest) (artifact.Metadata, error) {
					jobs.Add(1)
					return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
				})
				w := request(s, "GET", devicePath+"/api/display", auth(), nil)
				if w.Code != 503 || jobs.Load() != 0 || loadDevice(t, c).Status.ScreenCycle != nil {
					t.Fatal(w.Code, w.Body.String(), jobs.Load())
				}
				if operation != "latest" && renders.Load() != 1 {
					t.Fatal("failure preceded the intended boundary", renders.Load())
				}
			})
		}
	}
}
