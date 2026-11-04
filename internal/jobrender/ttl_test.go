package jobrender

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func readTTLJob(t *testing.T, r *Reconciler, job *batchv1.Job) *batchv1.Job {
	t.Helper()
	var current batchv1.Job
	if err := r.Reader.Get(t.Context(), client.ObjectKeyFromObject(job), &current); err != nil {
		t.Fatal(err)
	}
	return &current
}

func TestJobTTLAfterDurableCollection(t *testing.T) {
	for _, ttl := range []*int32{nil, ptr.To(int32(0)), ptr.To(int32(30)), ptr.To(int32(7200))} {
		for _, failure := range []bool{false, true} {
			r, d, s, now := fixture(t)
			s.Spec.Source.Job.TTLSecondsAfterFinished = ttl
			if err := r.Client.Update(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			st := load(t, r, d)
			job := r.Config.job(st)
			if got := readTTLJob(t, r, job); got.Spec.TTLSecondsAfterFinished != nil || got.Annotations[ttlAnnotation] == "" {
				t.Fatal("TTL must remain disabled during execution")
			}
			job = finish(t, r, d, png(t), failure)
			*now = now.Add(2 * time.Hour)
			if err := r.maintenance(t.Context()); err != nil {
				t.Fatal(err)
			}
			if readTTLJob(t, r, job).Spec.TTLSecondsAfterFinished != nil {
				t.Fatal("maintenance enabled TTL before collection")
			}
			// A restarted scheduler recovers the same completed execution.
			store, err := NewStore(r.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			r.Store = store
			step(t, r, d)
			if load(t, r, d).Active != nil {
				t.Fatal("completion not durable")
			}
			want := int32(600)
			if ttl != nil {
				want = *ttl
			}
			if got := readTTLJob(t, r, job).Spec.TTLSecondsAfterFinished; got == nil || *got != want {
				t.Fatalf("TTL = %v, want %d", got, want)
			}
			if err := r.maintenance(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(r.Store.run(st.Active.ID)); err != nil {
				t.Fatal("existing Job/Pod did not protect run", err)
			}
			if !failure {
				m, err := r.Repository.Latest(t.Context(), st.Unit)
				if err != nil || m.AttemptID != st.Active.ID {
					t.Fatal("published image lost", err)
				}
			}
		}
	}
}

type ttlPatchErrorClient struct{ client.Client }

func (c ttlPatchErrorClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*batchv1.Job); ok {
		return errors.New("injected TTL patch failure")
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestJobTTLMaintenanceRetriesAndMigrates(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		r, d, s, _ := fixture(t)
		s.Spec.Source.Job.TTLSecondsAfterFinished = ptr.To(int32(7200))
		if err := r.Client.Update(t.Context(), s); err != nil {
			t.Fatal(err)
		}
		step(t, r, d)
		job := finish(t, r, d, png(t), false)
		if legacy {
			job.Annotations = nil
			if err := r.Client.Update(t.Context(), job); err != nil {
				t.Fatal(err)
			}
		}
		original := r.Client
		r.Client = ttlPatchErrorClient{original}
		if _, err := r.Reconcile(t.Context(), clientRequest(load(t, r, d))); err == nil {
			t.Fatal("TTL write failure was hidden")
		}
		if load(t, r, d).Active != nil {
			t.Fatal("publication bookkeeping must precede TTL")
		}
		if err := r.maintenance(t.Context()); err == nil {
			t.Fatal("maintenance error was hidden")
		}
		r.Client = original
		store, err := NewStore(r.Store.Root)
		if err != nil {
			t.Fatal(err)
		}
		r.Store = store
		if err := r.maintenance(t.Context()); err != nil {
			t.Fatal(err)
		}
		want := int32(7200)
		if legacy {
			want = 600
		}
		if got := readTTLJob(t, r, job).Spec.TTLSecondsAfterFinished; got == nil || *got != want {
			t.Fatalf("recovered TTL = %v, want %d", got, want)
		}
		current := readTTLJob(t, r, job)
		current.Spec.TTLSecondsAfterFinished = ptr.To(int32(10000))
		if err := r.Client.Update(t.Context(), current); err != nil {
			t.Fatal(err)
		}
		if err := r.maintenance(t.Context()); err != nil {
			t.Fatal(err)
		}
		if *readTTLJob(t, r, job).Spec.TTLSecondsAfterFinished != 10000 {
			t.Fatal("existing TTL overwritten")
		}
	}
}

func TestJobTTLPreservesRunsUntilPodsDisappear(t *testing.T) {
	r, d, _, now := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	job := finish(t, r, d, png(t), false)
	step(t, r, d)
	*now = now.Add(2 * time.Hour)
	if err := r.Client.Delete(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if err := r.maintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Store.run(st.Active.ID)); err != nil {
		t.Fatal("Pod still protects run", err)
	}
	deletePods(t, r)
	if err := r.maintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Store.run(st.Active.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unused run retained", err)
	}
	if m, err := r.Repository.Latest(t.Context(), st.Unit); err != nil || m.AttemptID != st.Active.ID {
		t.Fatal("run GC removed published image", err)
	}
}

func TestJobTTLOnDemand(t *testing.T) {
	for _, failure := range []bool{false, true} {
		r, d, req := demandFixture(t)
		done := executeAsync(t.Context(), r, req)
		awaitAttempt(t, r, d)
		r.mu.Lock()
		job := finish(t, r, d, png(t), failure)
		r.mu.Unlock()
		result := executionDone(t, done)
		if (result.err != nil) != failure {
			t.Fatal(result.err)
		}
		if got := readTTLJob(t, r, job).Spec.TTLSecondsAfterFinished; got == nil || *got != 600 {
			t.Fatal("OnDemand TTL missing")
		}
	}
}

func TestJobTTLRejectsConflictingOwnership(t *testing.T) {
	r, d, _, _ := fixture(t)
	step(t, r, d)
	job := finish(t, r, d, png(t), false)
	job.OwnerReferences[0].UID = "other-device"
	if err := r.Client.Update(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if err := r.applyJobTTL(t.Context(), job); err == nil {
		t.Fatal("conflicting owner accepted")
	}
	if readTTLJob(t, r, job).Spec.TTLSecondsAfterFinished != nil {
		t.Fatal("conflicting Job modified")
	}
	var pods corev1.PodList
	if err := r.Reader.List(t.Context(), &pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 1 {
		t.Fatal("Pod removed")
	}
	if err := r.Reader.Get(t.Context(), client.ObjectKeyFromObject(job), &batchv1.Job{}); apierrors.IsNotFound(err) {
		t.Fatal("Job removed")
	}
}
