package byos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/deliverycommit"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/maintenance"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func stateScreen(t *testing.T, s *Server, c client.Client, cycling bool) *api.ArprtScreen {
	t.Helper()
	sc := &api.ArprtScreen{}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, sc); err != nil {
		t.Fatal(err)
	}
	sc.Spec.Source = api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "example.invalid/renderer:v1"}, State: &api.JobState{}}}
	if err := c.Update(t.Context(), sc); err != nil {
		t.Fatal(err)
	}
	d := loadDevice(t, c)
	if cycling {
		d.Spec.ScreenRef = nil
		if err := c.Update(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	request(s, "GET", devicePath+"/api/display", auth(), nil)
	return sc
}

func publishState(t *testing.T, s *Server, c client.Client, sc *api.ArprtScreen, seq uint64) artifact.CandidateManifest {
	t.Helper()
	d := loadDevice(t, c)
	u := artifact.Unit{Namespace: d.Namespace, DeviceUID: string(d.UID), ScreenUID: string(sc.UID), Target: devicestate.Resolved(d).Target, Output: devicestate.Resolved(d).Output}
	if u.Output.IsZero() {
		u.Output = render.CurrentTarget(u.Target).Output
	}
	revision, err := screen.InputRevision(sc.Spec)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Repository.Corner(t.Context(), string(d.UID))
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Repository.ConfigurePair(t.Context(), r, artifact.DefinePair(u, revision))
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Repository.PublishCandidate(t.Context(), r, artifact.PairKey(d.Namespace, string(sc.UID)), artifact.Metadata{Unit: u, InputRevision: revision, ScreenGeneration: sc.Generation, ContextDigest: strings.Repeat("c", 64), AttemptID: fmt.Sprintf("candidate-%d", seq), Sequence: seq, RenderedAt: time.Now().Add(-48 * time.Hour)}, pngFixture(t), render.StateResult{NextState: json.RawMessage(fmt.Sprintf(`{"cursor":%d}`, seq))})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func statePair(t *testing.T, s *Server) (artifact.CornerRecord, artifact.RendererPair) {
	t.Helper()
	r, err := s.Repository.Corner(t.Context(), "device-1")
	if err != nil {
		t.Fatal(err)
	}
	return r, r.Pairs[artifact.PairKey("arprt", "screen-1")]
}

func TestRendererStateDisplayConsumptionReplayAndFallback(t *testing.T) {
	for _, cycling := range []bool{false, true} {
		t.Run(fmt.Sprint(cycling), func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			sc := stateScreen(t, s, c, cycling)
			first := publishState(t, s, c, sc, 1)
			_, p := statePair(t, s)
			if p.Generation != 0 {
				t.Fatal("publication advanced state")
			}
			// Preview and raw PNG access do not select or consume candidates.
			if _, err := s.Repository.Read(t.Context(), first.Metadata.ImageDigest); err != nil {
				t.Fatal(err)
			}
			response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			if response.Status != 0 {
				t.Fatal(response)
			}
			_, p = statePair(t, s)
			if p.Generation != 1 || p.CandidateRef != "" || p.LastConsumed != "candidate-1" {
				t.Fatal(p)
			}
			imageBytes(t, s, response)
			fallback := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			if fallback.Status != 0 {
				t.Fatal("missing compatible fallback")
			}
			_, p = statePair(t, s)
			if p.Generation != 1 {
				t.Fatal("fallback/PNG advanced state")
			}
			second := publishState(t, s, c, sc, 2)
			if first.Metadata.ImageDigest != second.Metadata.ImageDigest {
				t.Fatal("fixture must reuse PNG")
			}
			if cycling {
				replay := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
				if replay != response {
					t.Fatal("replay changed response")
				}
				_, p = statePair(t, s)
				if p.Generation != 1 || p.CandidateRef == "" {
					t.Fatal("replay consumed candidate")
				}
				expireTurn(t, c)
			}
			next := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			if next.Status != 0 {
				t.Fatal(next)
			}
			r, p := statePair(t, s)
			value, err := s.Repository.CommittedState(t.Context(), r, artifact.PairKey("arprt", "screen-1"))
			if err != nil || p.Generation != 2 || string(value) != `{"cursor":2}` {
				t.Fatal(p, string(value), err)
			}
		})
	}
}

func TestRendererStateOverlayAndConcurrentDisplay(t *testing.T) {
	for _, mode := range []string{"disabled", "fixed", "cycling", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			s, c, _ := newTestServer(t, "http://unused.invalid")
			sc := stateScreen(t, s, c, false)
			if mode != "disabled" {
				enableOverlay(t, c, mode == "cycling", "timestamp")
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(sc), sc); err != nil {
					t.Fatal(err)
				}
			}
			m := publishState(t, s, c, sc, 1)
			if mode == "fallback" {
				s.Repository = &derivedErrorRepository{Repository: s.Repository}
			}
			var wg sync.WaitGroup
			for range 5 {
				wg.Go(func() {
					w := request(s, "GET", devicePath+"/api/display", auth(), nil)
					if w.Code != 200 {
						t.Error(w.Code)
					}
				})
			}
			wg.Wait()
			r, p := statePair(t, s)
			if p.Generation != 1 || r.Pending != nil {
				t.Fatal("candidate consumed more than once", p)
			}
			ok, err := s.Repository.Authorized(t.Context(), "device-1", m.Metadata.ImageDigest)
			if mode == "disabled" || mode == "fallback" {
				if err != nil || !ok {
					t.Fatal("base not issued", ok, err)
				}
			}
		})
	}
}

type failedStateFinalize struct {
	artifact.Repository
	commit bool
}

func (f failedStateFinalize) ResolveDelivery(ctx context.Context, r artifact.CornerRecord, commit bool) error {
	if f.commit {
		if err := f.Repository.ResolveDelivery(ctx, r, commit); err != nil {
			return err
		}
	}
	return errors.New("injected finalization failure")
}

func TestRendererStateInterruptedDeliveryRecovery(t *testing.T) {
	for _, cycling := range []bool{false, true} {
		for _, failure := range []string{"issue", "issue-committed", "status-committed", "finalize", "finalize-committed"} {
			t.Run(fmt.Sprint(cycling)+"/"+failure, func(t *testing.T) {
				s, c, root := newTestServer(t, "http://unused.invalid")
				sc := stateScreen(t, s, c, cycling)
				publishState(t, s, c, sc, 1)
				original := s.Repository
				switch failure {
				case "issue", "issue-committed":
					s.Repository = &issueErrorRepository{Repository: original, commit: failure == "issue-committed"}
				case "status-committed":
					if !cycling {
						t.Skip("single-screen has no API turn")
					}
					s.Client = &committedCycleClient{Client: c, fail: true}
				default:
					s.Repository = failedStateFinalize{Repository: original, commit: failure == "finalize-committed"}
				}
				if w := request(s, "GET", devicePath+"/api/display", auth(), nil); w.Code != 503 {
					t.Fatal(w.Code, w.Body.String())
				}
				repo, err := artifact.NewPVC(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := deliverycommit.RecoverAll(t.Context(), repo, c, "arprt"); err != nil {
					t.Fatal(err)
				}
				s.Repository = repo
				r, p := statePair(t, s)
				committed := failure == "finalize-committed" || cycling && (failure == "finalize" || failure == "status-committed")
				if r.Pending != nil || (p.Generation == 1) != committed || (p.CandidateRef == "") != committed {
					t.Fatal("wrong recovery decision", p, r.Pending)
				}
			})
		}
	}
}

func TestRendererStateLateWaiterCannotConsumeNextCandidate(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	sc := stateScreen(t, s, c, false)
	first := publishState(t, s, c, sc, 1)
	d := loadDevice(t, c)
	selected, err := s.selectDelivery(t.Context(), d, first.Metadata.Unit.ResolvedTarget())
	if err != nil {
		t.Fatal(err)
	}
	old, ok, err := s.rendererCandidate(t.Context(), d, selected, &first.Metadata)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if r := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); r.Status != 0 {
		t.Fatal(r)
	}
	publishState(t, s, c, sc, 2)
	r, _ := statePair(t, s)
	if _, err := s.finishDisplay(t.Context(), d, Observation{}, *loadDevice(t, c).Status.LastSeenAt, selected, old, r, token); err != nil {
		t.Fatal(err)
	}
	// Candidate identity remains independent of equal image bytes.
	_, p := statePair(t, s)
	if p.Generation != 1 || p.CandidateRef == "" {
		t.Fatal("old waiter consumed new candidate", p)
	}
}

func TestRendererStateSkipKeepsUnselectedExpiredCandidate(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	first := stateScreen(t, s, c, false)
	second := first.DeepCopy()
	second.Name = "other"
	second.UID = "screen-2"
	second.ResourceVersion = ""
	if err := c.Create(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	playlist := &api.ArprtPlaylist{}
	playlist.Namespace = "arprt"
	playlist.Name = "skip"
	playlist.UID = "playlist-1"
	playlist.Spec.FailurePolicy = "Skip"
	playlist.Spec.Screens = []api.LocalReference{{Name: first.Name}, {Name: second.Name}}
	if err := c.Create(t.Context(), playlist); err != nil {
		t.Fatal(err)
	}
	d := loadDevice(t, c)
	d.Spec.ScreenRef = nil
	d.Spec.PlaylistRef = &api.LocalReference{Name: playlist.Name}
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	publishState(t, s, c, first, 1)
	publishState(t, s, c, second, 1)
	// Both candidates are two days old. Skip must select them without calling a
	// renderer, and consume only the entry whose URL is committed.
	result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if result.Status != 0 {
		t.Fatal(result)
	}
	r, p := statePair(t, s)
	other := r.Pairs[artifact.PairKey("arprt", "screen-2")]
	if p.Generation != 1 || other.Generation != 0 || other.CandidateRef == "" {
		t.Fatal(p, other)
	}
	expireSkipTurn(t, c)
	result = decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if result.Status != 0 {
		t.Fatal(result)
	}
	r, _ = statePair(t, s)
	if r.Pairs[artifact.PairKey("arprt", "screen-2")].Generation != 1 {
		t.Fatal("second candidate not consumed")
	}
}

func TestRendererStateOnDemandReusesCandidate(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	sc := stateScreen(t, s, c, false)
	sc.Spec.Source.Job.ExecutionMode = "OnDemand"
	if err := c.Update(t.Context(), sc); err != nil {
		t.Fatal(err)
	}
	publishState(t, s, c, sc, 1)
	// No executor is configured. Reuse must not depend on starting another Job.
	response := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
	if response.Status != 0 {
		t.Fatal(response)
	}
	_, p := statePair(t, s)
	if p.Generation != 1 {
		t.Fatal(p)
	}
}

func TestRendererStateMaintenanceResetsAndRetains(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	sc := stateScreen(t, s, c, false)
	candidate := publishState(t, s, c, sc, 1)
	repo := s.Repository.(*artifact.PVC)
	now := time.Now().UTC()
	// Even GC with no latest pointer must retain authoritative candidate bytes.
	if err := repo.Collect(t.Context(), func(artifact.Unit) bool { return false }, map[string]bool{"device-1": true}, now.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Read(t.Context(), candidate.Metadata.ImageDigest); err != nil {
		t.Fatal("candidate lost", err)
	}
	if r := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); r.Status != 0 {
		t.Fatal(r)
	}
	if r := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); r.Status != 0 {
		t.Fatal("consumed manifest fallback was lost with latest", r)
	}
	_, before := statePair(t, s)
	maint := maintenance.StorageMaintenance{Reader: c, Repository: repo, Namespace: "arprt"}
	// Leave the assignment without relying on a scheduler record.
	d := loadDevice(t, c)
	d.Spec.ScreenRef = &api.LocalReference{Name: "missing"}
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := maint.Collect(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	_, detached := statePair(t, s)
	if detached.DetachedAt == nil || detached.Generation != 1 || detached.Epoch <= before.Epoch {
		t.Fatal(detached)
	}
	d.Spec.ScreenRef = &api.LocalReference{Name: sc.Name}
	if err := c.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := maint.Collect(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	_, joined := statePair(t, s)
	if joined.DetachedAt != nil || joined.Generation != 1 || joined.StateRef != before.StateRef {
		t.Fatal("compatible rejoin lost state", joined)
	}
	sc.Spec.Source.Job.State.ResetToken = "reset"
	if err := c.Update(t.Context(), sc); err != nil {
		t.Fatal(err)
	}
	if err := maint.Collect(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	_, reset := statePair(t, s)
	if reset.Epoch <= joined.Epoch || reset.Generation != 0 || reset.StateRef != "" {
		t.Fatal("reset retained old state", reset)
	}
}

func TestRendererStateRecoveryWaitsForLiveDecision(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	sc := stateScreen(t, s, c, false)
	m := publishState(t, s, c, sc, 1)
	r, p := statePair(t, s)
	unlock, err := deliverycommit.Lock(t.Context(), "device-1")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	pending := artifact.CornerPending{TransactionID: "live", RecordRevision: r.Revision, Next: r.Current, PairKey: artifact.PairKey("arprt", "screen-1"), CandidateRef: p.CandidateRef, Epoch: p.Epoch, Generation: p.Generation, ImageDigest: m.Metadata.ImageDigest, Filename: m.Metadata.Filename}
	if err := s.Repository.PrepareDelivery(t.Context(), r, pending); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := deliverycommit.RecoverAll(ctx, s.Repository.(*artifact.PVC), c, "arprt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("recovery interrupted a live decision", err)
	}
	r, p = statePair(t, s)
	if r.Pending == nil || p.Generation != 0 {
		t.Fatal("live pending changed")
	}
}

type failedDisplayWriter struct{ *httptest.ResponseRecorder }

func (w failedDisplayWriter) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }

func TestRendererStateResponseFailureDoesNotUndoCommit(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	sc := stateScreen(t, s, c, false)
	publishState(t, s, c, sc, 1)
	req := httptest.NewRequest("GET", devicePath+"/api/display", nil)
	for key, value := range auth() {
		req.Header.Set(key, value)
	}
	s.ServeHTTP(failedDisplayWriter{httptest.NewRecorder()}, req)
	_, p := statePair(t, s)
	if p.Generation != 1 || p.CandidateRef != "" {
		t.Fatal("failed response rolled back state", p)
	}
	if err := deliverycommit.RecoverAll(t.Context(), s.Repository.(*artifact.PVC), c, "arprt"); err != nil {
		t.Fatal(err)
	}
	_, p = statePair(t, s)
	if p.Generation != 1 {
		t.Fatal("recovery rolled back commit", p)
	}
}

func TestRendererStateUnrelatedTurnBlocksRecoveryAndDeletedUIDAborts(t *testing.T) {
	s, c, _ := newTestServer(t, "http://unused.invalid")
	sc := stateScreen(t, s, c, true)
	publishState(t, s, c, sc, 1)
	if result := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil)); result.Status != 0 {
		t.Fatal(result)
	}
	m := publishState(t, s, c, sc, 2)
	d := loadDevice(t, c)
	previous := d.Status.ScreenCycle.DeepCopy()
	proposed := previous.DeepCopy()
	proposed.ServedAt.Time = previous.ServedAt.Add(time.Second)
	r, p := statePair(t, s)
	proposal := artifact.CornerPending{TransactionID: "unknown-turn", RecordRevision: r.Revision, Next: r.Current, Previous: previous, Proposed: proposed, ImageDigest: m.Metadata.ImageDigest, Filename: m.Metadata.Filename, PairKey: artifact.PairKey("arprt", "screen-1"), CandidateRef: p.CandidateRef, Epoch: p.Epoch, Generation: p.Generation}
	if err := s.Repository.PrepareDelivery(t.Context(), r, proposal); err != nil {
		t.Fatal(err)
	}
	d.Status.ScreenCycle.ServedAt.Time = d.Status.ScreenCycle.ServedAt.Add(2 * time.Second)
	if err := c.Status().Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	repo := s.Repository.(*artifact.PVC)
	if err := deliverycommit.RecoverAll(t.Context(), repo, c, "arprt"); !errors.Is(err, artifact.ErrCorrupt) {
		t.Fatal("unrelated turn accepted", err)
	}
	if err := repo.Collect(t.Context(), func(artifact.Unit) bool { return false }, nil, time.Now().Add(72*time.Hour)); !errors.Is(err, artifact.ErrPending) {
		t.Fatal("unresolved pending allowed GC", err)
	}
	if err := c.Delete(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := deliverycommit.RecoverAll(t.Context(), repo, c, "arprt"); err != nil {
		t.Fatal(err)
	}
	r, p = statePair(t, s)
	if r.Pending != nil || p.Generation != 1 || p.CandidateRef == "" {
		t.Fatal("deleted UID adopted ambiguous state", r, p)
	}
	ok, err := repo.Authorized(t.Context(), "device-1", m.Metadata.ImageDigest)
	if err != nil || !ok {
		t.Fatal("prior issuance was revoked", err)
	}
}
