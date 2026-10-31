package byos

import (
	"context"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func setPlaylistEntries(t *testing.T, c client.Client, p *api.ArprtPlaylist, names ...string) {
	t.Helper()
	p.Spec.Screens = nil
	for _, name := range names {
		p.Spec.Screens = append(p.Spec.Screens, api.LocalReference{Name: name})
	}
	if err := c.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedEntriesRenderAndReplay(t *testing.T) {
	for _, policy := range []string{"Hold", "Skip"} {
		for _, source := range []string{"GET", "POST", "OnDemand"} {
			t.Run(policy+"/"+source, func(t *testing.T) {
				s, c, p := skipServer(t, 2)
				p.Spec.FailurePolicy = policy
				setPlaylistEntries(t, c, p, "screen", "missing", "screen", "screen-1")
				b := pngFixture(t)
				var calls atomic.Uint64
				if source == "OnDemand" {
					s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
						n := calls.Add(1)
						return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: n, AttemptID: fmt.Sprint(n), RenderedAt: time.Now()}, b)
					})
				} else {
					renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if r.Method != source {
							t.Error("unexpected renderer method", r.Method)
						}
						w.Header().Set("Content-Type", "image/png")
						_, _ = w.Write(b)
					}))
					defer renderer.Close()
					for _, name := range []string{"screen", "screen-1"} {
						var sc api.ArprtScreen
						if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: name}, &sc); err != nil {
							t.Fatal(err)
						}
						sc.Spec.Source = api.ScreenSource{HTTP: &api.HTTPSource{URL: renderer.URL, Method: source}}
						if err := c.Update(t.Context(), &sc); err != nil {
							t.Fatal(err)
						}
					}
				}
				var first DisplayResponse
				for i, index := range []int32{0, 2, 3, 0} {
					if i > 0 {
						expireTurn(t, c)
					}
					got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
					turn := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
					if got.Status != 0 || turn == nil || turn.EntryIndex != index {
						t.Fatal("wrong successful occurrence", got, turn)
					}
					if i == 0 {
						first = got
					} else if i == 1 && got != first {
						t.Fatal("identical bytes changed between occurrences", first, got)
					}
					wantCalls := uint64(i + 1)
					if source == "GET" {
						wantCalls = 1
						if i >= 2 {
							wantCalls = 2
						}
					}
					if calls.Load() != wantCalls {
						t.Fatal("new turn rendered incorrectly", calls.Load(), wantCalls)
					}
					for range 2 {
						replay := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
						if replay != got || calls.Load() != wantCalls || !artifact.EqualCycle(turn, loadDevice(t, c).Status.ScreenCycle) {
							t.Fatal("replay rendered, advanced or extended its window", replay, turn)
						}
					}
				}
			})
		}
	}
}

func TestRepeatedHoldPendingAndStale(t *testing.T) {
	s, c, p := skipServer(t, 1)
	p.Spec.FailurePolicy = "Hold"
	setPlaylistEntries(t, c, p, "screen", "screen")
	b := pngFixture(t)
	failed, calls := true, 0
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		calls++
		if failed {
			return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
		}
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: uint64(calls), AttemptID: fmt.Sprint(calls), RenderedAt: time.Now()}, b)
	})
	for range 2 {
		if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 202 || loadDevice(t, c).Status.ScreenCycle != nil {
			t.Fatal("pending advanced the occurrence", got)
		}
	}
	failed = false
	first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if first.Status != 0 || loadDevice(t, c).Status.ScreenCycle.EntryIndex != 0 {
		t.Fatal(first)
	}
	failed = true
	for _, index := range []int32{1, 0} {
		expireTurn(t, c)
		if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got != first || loadDevice(t, c).Status.ScreenCycle.EntryIndex != index {
			t.Fatal("compatible stale output did not commit the occurrence", got)
		}
		turn, before := loadDevice(t, c).Status.ScreenCycle.DeepCopy(), calls
		if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got != first || calls != before || !artifact.EqualCycle(turn, loadDevice(t, c).Status.ScreenCycle) {
			t.Fatal("stale replay changed the turn", got)
		}
	}
}

func TestRepeatedSkipRetriesEachOccurrenceAndPreservesSuccess(t *testing.T) {
	s, c, p := skipServer(t, 3)
	setPlaylistEntries(t, c, p, "screen", "screen-1", "screen", "screen-2")
	b := pngFixture(t)
	var calls []string
	var deadline time.Time
	failed := false
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		calls = append(calls, req.Screen)
		if deadline.IsZero() {
			deadline = req.RenderDeadline
		} else if req.RenderDeadline != deadline {
			t.Error("candidate reset the shared budget")
		}
		if failed || len(calls) <= 2 {
			return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
		}
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: uint64(len(calls)), AttemptID: fmt.Sprint(len(calls)), RenderedAt: time.Now()}, b)
	})
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || loadDevice(t, c).Status.ScreenCycle.EntryIndex != 2 || !reflect.DeepEqual(calls, []string{"screen", "screen-1", "screen"}) {
		t.Fatal("second occurrence was not retried/committed", got, calls)
	}
	expireTurn(t, c)
	previous := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
	failed, deadline = true, time.Time{}
	calls = nil
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 202 || !artifact.EqualCycle(previous, loadDevice(t, c).Status.ScreenCycle) || !reflect.DeepEqual(calls, []string{"screen-2", "screen", "screen-1", "screen"}) {
		t.Fatal("all failures changed the successful occurrence", got, calls)
	}
	// An already exhausted admission budget tries no occurrence and keeps success.
	calls = nil
	ctx := context.WithValue(t.Context(), admissionTimeKey{}, time.Now().Add(-11*time.Second))
	r := httptest.NewRequest("GET", devicePath+"/api/display", nil).WithContext(ctx)
	for name, value := range auth() {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	s.display(w, r, loadDevice(t, c), Observation{Model: "og"})
	if got := decodeDisplay(t, w); got.Status != 202 || len(calls) != 0 || !artifact.EqualCycle(previous, loadDevice(t, c).Status.ScreenCycle) {
		t.Fatal("budget exhaustion changed the successful occurrence", got, calls)
	}
}

func TestRepeatedEntriesRejectEditsDuringRendering(t *testing.T) {
	for _, policy := range []string{"Hold", "Skip"} {
		for _, change := range []string{"count", "screen-uid"} {
			t.Run(policy+"/"+change, func(t *testing.T) {
				s, c, p := skipServer(t, 1)
				p.Spec.FailurePolicy = policy
				setPlaylistEntries(t, c, p, "screen", "screen")
				b := pngFixture(t)
				s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
					m, err := s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: 1, AttemptID: "obsolete", RenderedAt: time.Now()}, b)
					if err == nil {
						if change == "count" {
							setPlaylistEntries(t, c, p, "screen")
						} else {
							err = changeDemandInput(ctx, c, change)
						}
					}
					return m, err
				})
				repo := &countIssuance{Repository: s.Repository}
				s.Repository = repo
				if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 202 || repo.count.Load() != 0 || loadDevice(t, c).Status.ScreenCycle != nil {
					t.Fatal("obsolete occurrence was issued/committed", got)
				}
			})
		}
	}
}

func TestSkipCompletedFlightDoesNotOwnLaterTurns(t *testing.T) {
	s, c, p := skipServer(t, 1)
	setPlaylistEntries(t, c, p, "screen", "screen")
	b := pngFixture(t)
	var calls atomic.Uint64
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		n := calls.Add(1)
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: n, AttemptID: fmt.Sprint(n), RenderedAt: time.Now()}, b)
	})
	d := loadDevice(t, c)
	if err := s.Identity.Register(t.Context(), d, d.Spec.HardwareID, false); err != nil {
		t.Fatal(err)
	}
	o := Observation{Model: "og"}
	observed, err := s.observeDisplay(t.Context(), d, o)
	if err != nil {
		t.Fatal(err)
	}
	var flights []*skipFlight
	for i := range 4 {
		if i > 0 {
			expireTurn(t, c)
			d = loadDevice(t, c)
		}
		selected, err := s.selectDelivery(t.Context(), d, observed.target)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(t.Context(), admissionTimeKey{}, time.Now())
		f, leave, err := s.joinSkip(ctx, d, o, observed.seen, selected, token, make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		// Keep completed waiters attached across both the next position and wrap.
		defer leave()
		select {
		case <-f.done:
		case <-time.After(3 * time.Second):
			t.Fatal("Skip did not finish")
		}
		for _, old := range flights {
			if old == f {
				t.Fatal("a later turn reused a completed flight")
			}
		}
		flights = append(flights, f)
		if f.err != nil || f.response.Status != 0 || calls.Load() != uint64(i+1) || loadDevice(t, c).Status.ScreenCycle.EntryIndex != int32(i%2) {
			t.Fatal(f.err, f.response, calls.Load(), loadDevice(t, c).Status.ScreenCycle)
		}
	}
}

func TestSkipOlderExplorationCannotOverwriteSuccess(t *testing.T) {
	s, c, p := skipServer(t, 1)
	setPlaylistEntries(t, c, p, "screen", "screen")
	b := pngFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Uint64
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		n := calls.Add(1)
		if n == 2 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return artifact.Metadata{}, ctx.Err()
			}
		}
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: n, AttemptID: fmt.Sprint(n), RenderedAt: time.Now()}, b)
	})
	if got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 {
		t.Fatal(got)
	}
	expireTurn(t, c)
	repo := &countIssuance{Repository: s.Repository}
	s.Repository = repo
	done := asyncDisplay(s, t.Context(), auth())
	<-started
	d := loadDevice(t, c)
	d.Status.ScreenCycle.EntryIndex = 1
	d.Status.ScreenCycle.ServedAt = metav1.NewMicroTime(time.Now().UTC().Truncate(time.Microsecond))
	if err := c.Status().Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	previous := d.Status.ScreenCycle.DeepCopy()
	close(release)
	if got := decodeDisplay(t, demandResponse(t, done)); got.Status != 202 || repo.count.Load() != 0 || !artifact.EqualCycle(previous, loadDevice(t, c).Status.ScreenCycle) {
		t.Fatal("older exploration overwrote/issued after a newer turn", got)
	}
}

func TestRepeatedSkipConcurrentRequestsCommitOnce(t *testing.T) {
	s, c, p := skipServer(t, 1)
	setPlaylistEntries(t, c, p, "screen", "screen")
	b := pngFixture(t)
	repo := &countIssuance{Repository: s.Repository}
	s.Repository = repo
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Uint64
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return artifact.Metadata{}, ctx.Err()
			}
			return artifact.Metadata{}, &jobrender.ExecutionError{Reason: "JobFailed"}
		}
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: n, AttemptID: fmt.Sprint(n), RenderedAt: time.Now()}, b)
	})
	first := asyncDisplay(s, t.Context(), auth())
	<-started
	second := asyncDisplay(s, t.Context(), auth())
	awaitSkipWaiters(t, s, 2)
	close(release)
	a, other := decodeDisplay(t, demandResponse(t, first)), decodeDisplay(t, demandResponse(t, second))
	if a.Status != 0 || a != other || calls.Load() != 2 || repo.count.Load() != 1 || loadDevice(t, c).Status.ScreenCycle.EntryIndex != 1 {
		t.Fatal("shared exploration duplicated issuance or lost the successful occurrence", a, other, calls.Load(), repo.count.Load())
	}
}

func TestOnDemandFlightsDistinguishRepeatedTurns(t *testing.T) {
	s, c, p := skipServer(t, 1)
	p.Spec.FailurePolicy = "Hold"
	setPlaylistEntries(t, c, p, "screen", "screen")
	b := pngFixture(t)
	var calls atomic.Uint64
	s.Jobs = demandExecutor(func(ctx context.Context, req jobrender.ExecutionRequest) (artifact.Metadata, error) {
		n := calls.Add(1)
		return s.Repository.Publish(ctx, artifact.Metadata{Unit: req.Unit, InputRevision: req.Revision, ScreenGeneration: 1, Sequence: n, AttemptID: fmt.Sprint(n), RenderedAt: time.Now()}, b)
	})
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
	ctx := context.WithValue(t.Context(), admissionTimeKey{}, time.Now())
	first, leave, err := s.joinDemand(ctx, d, o, selected)
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	if _, err := s.waitDemand(ctx, d, selected, first); err != nil {
		t.Fatal(err)
	}
	// A retained flight belongs only to this position and this previous turn.
	for _, change := range []string{"position", "previous-turn"} {
		t.Run(change, func(t *testing.T) {
			otherDevice, otherSelection := d.DeepCopy(), selected
			if change == "position" {
				otherSelection.entryIndex = 1
			} else {
				otherDevice.Status.ScreenCycle = &api.ScreenCycleStatus{EntryIndex: 1, ScreenUID: string(selected.screen.UID), ServedAt: metav1.NewMicroTime(time.Now())}
			}
			other, leave, err := s.joinDemand(ctx, otherDevice, o, otherSelection)
			if err != nil {
				t.Fatal(err)
			}
			defer leave()
			if _, err := s.waitDemand(ctx, otherDevice, otherSelection, other); err != nil || other == first {
				t.Fatal("different turn joined old OnDemand rendering", err)
			}
		})
	}
	if calls.Load() != 3 {
		t.Fatal("a new turn reused an old execution", calls.Load())
	}
}
