package jobrender

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func optIn(t *testing.T, r *Reconciler) {
	t.Helper()
	var s api.ArprtScreen
	if err := r.Client.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Source.Job.State = &api.JobState{}
	if err := r.Client.Update(t.Context(), &s); err != nil {
		t.Fatal(err)
	}
}

func rendererSnapshot(t *testing.T, r *Reconciler, d *api.ArprtDevice) (artifact.CornerRecord, string) {
	t.Helper()
	record, err := r.Repository.(artifact.StateStore).Corner(t.Context(), string(d.UID))
	if err != nil {
		t.Fatal(err)
	}
	return record, artifact.PairKey(d.Namespace, "screen-uid")
}

func writeResult(t *testing.T, r *Reconciler, st *state, result string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Store.run(st.Active.ID), "output", filepath.Base(st.Active.Source.State.ResultPath)), []byte(result), 0600); err != nil {
		t.Fatal(err)
	}
}

func consumeCandidate(t *testing.T, r *Reconciler, d *api.ArprtDevice) {
	t.Helper()
	record, key := rendererSnapshot(t, r, d)
	pair := record.Pairs[key]
	store := r.Repository.(artifact.StateStore)
	m, err := store.Candidate(t.Context(), pair.CandidateRef)
	if err != nil {
		t.Fatal(err)
	}
	pending := artifact.CornerPending{TransactionID: "consume-" + m.Metadata.AttemptID, RecordRevision: record.Revision, PairKey: key, CandidateRef: pair.CandidateRef, Epoch: pair.Epoch, Generation: pair.Generation, ImageDigest: m.Metadata.ImageDigest, Filename: m.Metadata.Filename, Next: record.Current}
	if err := store.PrepareDelivery(t.Context(), record, pending); err != nil {
		t.Fatal(err)
	}
	record, _ = store.Corner(t.Context(), string(d.UID))
	if err := store.ResolveDelivery(t.Context(), record, true); err != nil {
		t.Fatal(err)
	}
}

func TestRendererStateCandidateRetentionAndNextSnapshot(t *testing.T) {
	r, d, _, now := fixture(t)
	optIn(t, r)
	step(t, r, d)
	st := load(t, r, d)
	if st.Active.RendererPair == nil || st.Active.RendererPair.Generation != 0 {
		t.Fatal("missing initial snapshot")
	}
	writeResult(t, r, st, `{"version":"v1","nextState":{"cursor":9007199254740993}}`)
	finish(t, r, d, png(t), false)
	step(t, r, d)
	record, key := rendererSnapshot(t, r, d)
	initial := record.Pairs[key]
	if initial.CandidateRef == "" || initial.Generation != 0 || initial.StateRef != "" {
		t.Fatal("publication committed state", initial)
	}
	for range 3 {
		*now = now.Add(time.Hour)
		step(t, r, d)
		if load(t, r, d).Active != nil {
			t.Fatal("unconsumed candidate replaced")
		}
	}
	consumeCandidate(t, r, d)
	// Maintenance detects consumption even without a new display or watch event.
	if err := r.maintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	st = load(t, r, d)
	if st.Active == nil || st.Active.RendererPair.Generation != 1 {
		t.Fatal("next generation not prepared", st)
	}
	b, err := readSnapshot(filepath.Join(r.Store.run(st.Active.ID), "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot render.Context
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatal(err)
	}
	if string(snapshot.RendererState) != `{"cursor":9007199254740993}` || hash(b) != st.Active.ContextDigest {
		t.Fatal("state or digest changed", string(b))
	}
}

func TestRendererStateCollectionValues(t *testing.T) {
	for _, result := range []string{"absent", `{"version":"v1"}`, `{"version":"v1","nextState":null}`, `{"version":"v1","nextState":false}`, `{"version":"v1","nextState":0}`, `{"version":"v1","nextState":[]}`, `{"version":"v1","nextState":{}}`, ``, `{"version":"v2"}`, `{"version":"v1","secret":"private"}`, `{"version":"v1","nextState":{"a":1,"a":2}}`} {
		t.Run(result, func(t *testing.T) {
			r, d, _, _ := fixture(t)
			optIn(t, r)
			step(t, r, d)
			st := load(t, r, d)
			if result != "absent" {
				writeResult(t, r, st, result)
			}
			finish(t, r, d, png(t), false)
			step(t, r, d)
			record, key := rendererSnapshot(t, r, d)
			_, err := render.ParseStateResult([]byte(result))
			valid := result == "absent" || err == nil
			if (record.Pairs[key].CandidateRef != "") != valid || record.Pairs[key].Generation != 0 {
				t.Fatal("invalid publication", record)
			}
			if !valid && load(t, r, d).Reason != "CollectionFailed" {
				t.Fatal("invalid result not failed")
			}
		})
	}
}

func TestRendererStatePublicationRecovery(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "consumed"}[consumed], func(t *testing.T) {
			r, d, _, _ := fixture(t)
			optIn(t, r)
			step(t, r, d)
			st := load(t, r, d)
			writeResult(t, r, st, `{"version":"v1","nextState":1}`)
			finish(t, r, d, png(t), false)
			step(t, r, d)
			if consumed {
				consumeCandidate(t, r, d)
			}
			// Restore the pre-cleanup scheduler record to model an interrupted save.
			if err := r.Store.save(st); err != nil {
				t.Fatal(err)
			}
			store, err := NewStore(r.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			r.Store = store
			repo, err := artifact.NewPVC(store.Root)
			if err != nil {
				t.Fatal(err)
			}
			r.Repository = repo
			step(t, r, d)
			if load(t, r, d).Active != nil {
				t.Fatal("publication not recovered")
			}
			record, key := rendererSnapshot(t, r, d)
			if (record.Pairs[key].CandidateRef == "") != consumed {
				t.Fatal("candidate resurrected")
			}
		})
	}
}

func TestRendererStateOnDemandReuse(t *testing.T) {
	r, d, req := demandFixture(t)
	optIn(t, r)
	_, _, wanted, err := r.desired(t.Context(), req.Device, req.Screen)
	if err != nil {
		t.Fatal(err)
	}
	req.Revision = wanted.Revision
	done := executeAsync(t.Context(), r, req)
	st := awaitAttempt(t, r, d)
	r.mu.Lock()
	writeResult(t, r, st, `{"version":"v1","nextState":"next"}`)
	finish(t, r, d, png(t), false)
	r.mu.Unlock()
	first := executionDone(t, done)
	if first.err != nil {
		t.Fatal(first.err)
	}
	req.StartedAt = time.Now()
	req.RenderDeadline = req.StartedAt.Add(10 * time.Second)
	second, err := r.Execute(t.Context(), req)
	if err != nil || second.AttemptID != first.m.AttemptID {
		t.Fatal("candidate not reused", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, key := rendererSnapshot(t, r, d)
	if record.Pairs[key].Generation != 0 || load(t, r, d).Active != nil {
		t.Fatal("reuse advanced state or started work")
	}
}

func TestRendererResultFileSafetyAndOptOut(t *testing.T) {
	for _, kind := range []string{"fifo", "directory", "escape", "dangling", "oversize", "io", "optout"} {
		t.Run(kind, func(t *testing.T) {
			r, d, _, _ := fixture(t)
			if kind != "optout" {
				optIn(t, r)
			}
			step(t, r, d)
			st := load(t, r, d)
			path := filepath.Join(r.Store.run(st.Active.ID), "output", "result.json")
			var err error
			switch kind {
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "escape":
				err = os.Symlink("../../context.json", path)
			case "dangling":
				err = os.Symlink("missing.json", path)
			case "oversize":
				err = os.WriteFile(path, []byte(strings.Repeat(" ", render.MaxResultBytes+1)), 0600)
			case "io":
				err = os.Symlink("loop", path)
				if err == nil {
					err = os.Symlink("result.json", filepath.Join(filepath.Dir(path), "loop"))
				}
			case "optout":
				err = unix.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Store.collectResult(t.Context(), st.Active)
			if (err == nil) != (kind == "optout") {
				t.Fatal("unsafe result accepted", err)
			}
		})
	}
}

func TestRendererStateResetAndPendingFence(t *testing.T) {
	r, d, s, _ := fixture(t)
	optIn(t, r)
	step(t, r, d)
	old := load(t, r, d)
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Source.Job.State.ResetToken = "reset"
	if err := r.Client.Update(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	record, key := rendererSnapshot(t, r, d)
	if record.Pairs[key].Epoch <= old.Active.RendererPair.Epoch {
		t.Fatal("reset did not fence attempt")
	}
	if _, err := r.rendererPublished(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	st := load(t, r, d)
	if st.Active == nil {
		t.Fatal("replacement not started")
	}
	finish(t, r, d, png(t), false)
	step(t, r, d)
	record, key = rendererSnapshot(t, r, d)
	pair := record.Pairs[key]
	store := r.Repository.(artifact.StateStore)
	m, err := store.Candidate(t.Context(), pair.CandidateRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareDelivery(t.Context(), record, artifact.CornerPending{TransactionID: "pending", RecordRevision: record.Revision, PairKey: key, CandidateRef: pair.CandidateRef, Epoch: pair.Epoch, Generation: pair.Generation, ImageDigest: m.Metadata.ImageDigest, Filename: m.Metadata.Filename, Next: record.Current}); err != nil {
		t.Fatal(err)
	}
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
	if !errors.Is(err, errPendingDelivery) {
		t.Fatal("pending did not block", err)
	}
}

// Inject repository failures without exposing production stop hooks.
type candidateFailureStore struct {
	*artifact.PVC
	publishErr error
	confirmErr error
}

func (s *candidateFailureStore) PublishCandidate(ctx context.Context, r artifact.CornerRecord, key string, m artifact.Metadata, png []byte, result render.StateResult) (artifact.CandidateManifest, error) {
	if s.publishErr != nil {
		return artifact.CandidateManifest{}, s.publishErr
	}
	return s.PVC.PublishCandidate(ctx, r, key, m, png, result)
}
func (s *candidateFailureStore) ConfirmCandidate(ctx context.Context, ref string) (artifact.CandidateManifest, error) {
	if s.confirmErr != nil {
		return artifact.CandidateManifest{}, s.confirmErr
	}
	return s.PVC.ConfirmCandidate(ctx, ref)
}

func TestRendererStateStorageFailuresPreserveAttempt(t *testing.T) {
	for _, trigger := range []Trigger{Scheduled, OnDemand} {
		t.Run(string(trigger), func(t *testing.T) {
			r, d, _, _ := fixture(t)
			optIn(t, r)
			// Prepare using the real scheduler, then exercise identical collection for
			// both triggers without depending on an HTTP waiter timing out.
			step(t, r, d)
			st := load(t, r, d)
			if trigger == OnDemand {
				st.Trigger = OnDemand
				st.Active.Trigger = OnDemand
				st.Active.Deadline = st.Active.StartedAt.Add(10 * time.Second)
			}
			job := finish(t, r, d, png(t), false)
			injected := errors.New("injected candidate storage failure")
			repo := r.Repository
			broken := &candidateFailureStore{PVC: repo.(*artifact.PVC), publishErr: injected}
			r.Repository = broken
			// Use direct publication to isolate storage from desired-state validation.
			_, err := r.publishCompleted(t.Context(), d, &api.ArprtScreen{}, st, job, png(t), render.StateResult{})
			if !errors.Is(err, injected) || st.Active == nil || st.Failures != 0 {
				t.Fatal("storage failure became renderer failure", err)
			}
			r.Repository = repo
			if _, err := r.publishCompleted(t.Context(), d, &api.ArprtScreen{}, st, job, png(t), render.StateResult{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRendererStateRecoveryRequiresDurability(t *testing.T) {
	r, d, _, _ := fixture(t)
	optIn(t, r)
	step(t, r, d)
	st := load(t, r, d)
	finish(t, r, d, png(t), false)
	step(t, r, d)
	if err := r.Store.save(st); err != nil {
		t.Fatal(err)
	}
	repo := r.Repository
	injected := errors.New("injected directory sync failure")
	broken := &candidateFailureStore{PVC: repo.(*artifact.PVC), confirmErr: injected}
	r.Repository = broken
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
	if !errors.Is(err, injected) || load(t, r, d).Active == nil {
		t.Fatal("unconfirmed publication acknowledged", err)
	}
	broken.confirmErr = nil
	step(t, r, d)
	if load(t, r, d).Active != nil {
		t.Fatal("durable recovery did not finish")
	}
}

func TestRendererResultReadFailureIsInfrastructure(t *testing.T) {
	for _, trigger := range []Trigger{Scheduled, OnDemand} {
		t.Run(string(trigger), func(t *testing.T) {
			r, d, s, _ := fixture(t)
			optIn(t, r)
			step(t, r, d)
			st := load(t, r, d)
			writeResult(t, r, st, `{"version":"v1"}`)
			job := finish(t, r, d, png(t), false)
			path := filepath.Join(r.Store.run(st.Active.ID), "output", "result.json")
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(path, 0600)
			if _, err := os.ReadFile(path); err == nil {
				t.Skip("filesystem does not enforce file permissions")
			}
			st.Trigger = trigger
			if trigger == OnDemand {
				st.Active.Trigger = trigger
				st.Active.Deadline = st.Active.StartedAt.Add(10 * time.Second)
			}
			_, err := r.collectSuccessful(t.Context(), d, s, st, job)
			var executionError *ExecutionError
			if !errors.Is(err, os.ErrPermission) || errors.As(err, &executionError) || st.Active == nil || st.Failures != 0 {
				t.Fatal("read failure became renderer failure", err)
			}
			record, key := rendererSnapshot(t, r, d)
			if record.Pairs[key].CandidateRef != "" || record.Pairs[key].Generation != 0 {
				t.Fatal("read failure published output")
			}
		})
	}
}

func TestRendererStateOptOutResetsCommittedState(t *testing.T) {
	r, d, s, _ := fixture(t)
	optIn(t, r)
	step(t, r, d)
	st := load(t, r, d)
	writeResult(t, r, st, `{"version":"v1","nextState":"old"}`)
	finish(t, r, d, png(t), false)
	step(t, r, d)
	consumeCandidate(t, r, d)
	before, key := rendererSnapshot(t, r, d)
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Source.Job.State = nil
	if err := r.Client.Update(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	optIn(t, r)
	step(t, r, d)
	after, _ := rendererSnapshot(t, r, d)
	pair := after.Pairs[key]
	if pair.Epoch <= before.Pairs[key].Epoch || pair.StateRef != "" || pair.Generation != 0 {
		t.Fatal("opt-out reused committed state", pair)
	}
}

func TestRendererStateHTTPDefinitionChangeResetsState(t *testing.T) {
	r, d, s, _ := fixture(t)
	optIn(t, r)
	step(t, r, d)
	st := load(t, r, d)
	writeResult(t, r, st, `{"version":"v1","nextState":"old"}`)
	finish(t, r, d, png(t), false)
	step(t, r, d)
	consumeCandidate(t, r, d)
	before, key := rendererSnapshot(t, r, d)
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	original := s.Spec.Source.Job.DeepCopy()
	s.Spec.Source.Job = nil
	s.Spec.Source.HTTP = &api.HTTPSource{URL: "https://renderer.example.test/image"}
	if err := r.Client.Update(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Source.Job = original
	s.Spec.Source.HTTP = nil
	if err := r.Client.Update(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	after, _ := rendererSnapshot(t, r, d)
	if pair := after.Pairs[key]; pair.Epoch <= before.Pairs[key].Epoch || pair.StateRef != "" || pair.Generation != 0 {
		t.Fatal("HTTP definition reused committed state", pair)
	}
}

func TestRendererStateDepartureRetainsCommittedState(t *testing.T) {
	r, d, _, _ := fixture(t)
	optIn(t, r)
	step(t, r, d)
	st := load(t, r, d)
	writeResult(t, r, st, `{"version":"v1","nextState":"retained"}`)
	finish(t, r, d, png(t), false)
	step(t, r, d)
	consumeCandidate(t, r, d)
	before, key := rendererSnapshot(t, r, d)
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	d.Spec.ScreenRef.Name = "missing"
	if err := r.Client.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	departed, _ := rendererSnapshot(t, r, d)
	if departed.Pairs[key].DetachedAt == nil {
		t.Fatal("departure not recorded")
	}
	if err := r.Client.Get(t.Context(), client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	d.Spec.ScreenRef.Name = "screen"
	if err := r.Client.Update(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	after, _ := rendererSnapshot(t, r, d)
	pair := after.Pairs[key]
	if pair.DetachedAt != nil || pair.Generation != before.Pairs[key].Generation || pair.StateRef != before.Pairs[key].StateRef {
		t.Fatal("compatible rejoin lost state", pair)
	}
}
