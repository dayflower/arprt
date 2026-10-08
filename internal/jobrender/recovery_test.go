package jobrender

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/artifact"
	batchv1 "k8s.io/api/batch/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A lost API response leaves the durable scheduler record behind the API effect.
type uncertainCreateClient struct {
	client.Client
	afterCreate func()
	err         error
}

func (c uncertainCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	c.afterCreate()
	return c.err
}

type deleteErrorClient struct {
	client.Client
	err error
}

func (c deleteErrorClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return c.err
}

func TestCreatedJobBeforeSubmittedSaveRecoversSameAttempt(t *testing.T) {
	ctx := context.Background()
	r, d, _, _ := fixture(t)
	c := r.Client
	injected := errors.New("injected lost Job create response")
	var prepared *state
	var snapshot []byte
	r.Client = uncertainCreateClient{Client: c, err: injected, afterCreate: func() {
		prepared = load(t, r, d)
		if prepared.Active == nil || prepared.Active.Submitted {
			t.Fatal("attempt was not durable before Job creation", prepared)
		}
		var err error
		snapshot, err = os.ReadFile(filepath.Join(r.Store.run(prepared.Active.ID), "context.json"))
		if err != nil || hash(snapshot) != prepared.Active.ContextDigest {
			t.Fatal("context was not durable before Job creation", err)
		}
	}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}); !errors.Is(err, injected) {
		t.Fatal("creation interruption not exercised", err)
	}
	if prepared == nil {
		t.Fatal("Job creation not reached")
	}
	store, err := NewStore(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := artifact.NewPVC(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Reconciler{Client: c, Reader: c, Store: store, Repository: repo, Config: r.Config, Now: r.Now}
	step(t, restarted, d)
	recovered := load(t, restarted, d)
	if recovered.Active == nil || !recovered.Active.Submitted || recovered.Active.ID != prepared.Active.ID || recovered.Active.Sequence != prepared.Active.Sequence || recovered.Active.ContextDigest != prepared.Active.ContextDigest || recovered.Sequence != prepared.Sequence {
		t.Fatal("restart changed attempt or did not recognize the existing Job", recovered)
	}
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs); err != nil || len(jobs.Items) != 1 || jobs.Items[0].Name != "arprt-render-"+prepared.Active.ID {
		t.Fatal("restart duplicated or replaced Job", jobs, err)
	}
	recoveredSnapshot, err := os.ReadFile(filepath.Join(store.run(prepared.Active.ID), "context.json"))
	if err != nil || !bytes.Equal(snapshot, recoveredSnapshot) {
		t.Fatal("restart changed immutable context", err)
	}
	finish(t, restarted, d, png(t), false)
	step(t, restarted, d)
	published, err := repo.Latest(ctx, prepared.Unit)
	if err != nil || published.AttemptID != prepared.Active.ID || published.Sequence != prepared.Active.Sequence || published.ContextDigest != hash(snapshot) || load(t, restarted, d).Active != nil {
		t.Fatal("recovered Job did not complete original attempt", published, err)
	}
}

func TestTerminalReasonSurvivesInterruptedCancellation(t *testing.T) {
	ctx := context.Background()
	r, d, _, now := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	// Model a lost Job whose create response was never recorded and whose
	// remaining output cannot establish renderer success.
	st.Active.Submitted = false
	if err := r.Store.save(st); err != nil {
		t.Fatal(err)
	}
	if err := r.Client.Delete(ctx, r.Config.job(st)); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(r.Store.run(st.Active.ID), "output", "output.png")
	if err := os.WriteFile(outputPath, png(t), 0600); err != nil {
		t.Fatal(err)
	}
	c := r.Client
	injected := errors.New("injected cancellation failure")
	r.Client = deleteErrorClient{Client: c, err: injected}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}); !errors.Is(err, injected) {
		t.Fatal("cancellation interruption not exercised", err)
	}
	interrupted := load(t, r, d)
	if interrupted.Active == nil || interrupted.Active.ID != st.Active.ID || interrupted.Active.TerminalReason != "JobLost" {
		t.Fatal("terminal reason was not persisted before cancellation", interrupted)
	}
	// Without the saved reason, an empty output directory would allow recovery
	// to recreate this unsubmitted Job. A terminal attempt must stay terminal.
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := artifact.NewPVC(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Reconciler{Client: c, Reader: c, Store: store, Repository: repo, Config: r.Config, Now: r.Now}
	step(t, restarted, d)
	recovered := load(t, restarted, d)
	if recovered.Active != nil || recovered.Reason != "JobLost" || recovered.Failures != 1 || !recovered.Due.Equal(now.Add(5*time.Second)) || recovered.Sequence != st.Sequence {
		t.Fatal("restart lost terminal reason or retry bookkeeping", recovered)
	}
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs); err != nil || len(jobs.Items) != 0 {
		t.Fatal("terminal attempt recreated a Job", jobs, err)
	}
	if _, err := repo.Latest(ctx, st.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("unconfirmed output published", err)
	}
	*now = recovered.Due
	step(t, restarted, d)
	next := load(t, restarted, d)
	if next.Active == nil || next.Active.ID == st.Active.ID || next.Active.Sequence != st.Sequence+1 {
		t.Fatal("retry did not allocate a new attempt", next)
	}
}
